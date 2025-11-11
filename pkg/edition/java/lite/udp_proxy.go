package lite

import (
    "context"
    "errors"
    "fmt"
    "net"
    "strconv"
    "sync"
    "time"

    "github.com/go-logr/logr"
    "go.minekube.com/gate/pkg/edition/java/lite/config"
    "go.minekube.com/gate/pkg/util/errs"
    "go.minekube.com/gate/pkg/util/netutil"
)

// UDPManager manages UDP listeners for lite routes with UDP enabled.
type UDPManager struct {
    log logr.Logger

    mu       sync.Mutex
    running  bool
    cancel   context.CancelFunc
    listeners map[int]*udpRouteListener // key: udpListenPort

    strategy *StrategyManager
}

func NewUDPManager(log logr.Logger, sm *StrategyManager) *UDPManager {
    return &UDPManager{
        log:       log,
        listeners: make(map[int]*udpRouteListener),
        strategy:  sm,
    }
}

// Restart rebuilds all UDP listeners for provided routes on given bind host.
func (m *UDPManager) Restart(ctx context.Context, bindHost string, routes []config.Route) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    // Stop existing
    if m.cancel != nil {
        m.cancel()
        m.cancel = nil
    }
    for port, l := range m.listeners {
        _ = l.close()
        delete(m.listeners, port)
    }

    // Build new set
    runCtx, cancel := context.WithCancel(ctx)
    m.cancel = cancel
    for i := range routes {
        r := &routes[i]
        if !r.UDP {
            continue
        }
        if err := m.startListener(runCtx, bindHost, r); err != nil {
            // Log and continue starting other listeners
            errs.V(m.log, err).Info("failed starting udp listener", "port", r.UDPListenPort, "error", err)
        }
    }
    m.running = true
    return nil
}

// Stop stops all listeners.
func (m *UDPManager) Stop() {
    m.mu.Lock()
    defer m.mu.Unlock()
    if m.cancel != nil {
        m.cancel()
        m.cancel = nil
    }
    for port, l := range m.listeners {
        _ = l.close()
        delete(m.listeners, port)
    }
    m.running = false
}

func (m *UDPManager) startListener(ctx context.Context, bindHost string, r *config.Route) error {
    addr := net.JoinHostPort(bindHost, strconv.Itoa(r.UDPListenPort))
    lc := net.ListenConfig{}
    pc, err := lc.ListenPacket(ctx, "udp", addr)
    if err != nil {
        return fmt.Errorf("listen udp %s: %w", addr, err)
    }
    conn, ok := pc.(*net.UDPConn)
    if !ok {
        _ = pc.Close()
        return fmt.Errorf("listener is not UDPConn")
    }

    // Set buffers
    _ = conn.SetReadBuffer(r.GetUDPReadBuffer())
    _ = conn.SetWriteBuffer(r.GetUDPWriteBuffer())

    l := &udpRouteListener{
        log:       m.log.WithName("lite-udp").WithValues("listen", addr),
        conn:      conn,
        route:     r,
        strategy:  m.strategy,
        clients:   make(map[string]*udpClient),
        idleAfter: r.GetUDPIdleTimeout(),
        limit:     r.GetUDPClientLimit(),
        writeBuf:  r.GetUDPWriteBuffer(),
        readBuf:   r.GetUDPReadBuffer(),
    }
    l.start(ctx)

    m.listeners[r.UDPListenPort] = l
    m.log.Info("udp listener started", "addr", addr)
    return nil
}

type udpRouteListener struct {
    log      logr.Logger
    conn     *net.UDPConn
    route    *config.Route
    strategy *StrategyManager

    mu      sync.Mutex
    clients map[string]*udpClient

    idleAfter time.Duration
    limit     int

    readBuf  int
    writeBuf int

    gcOnce sync.Once
}

type udpClient struct {
    src   *net.UDPAddr
    up    *net.UDPConn
    done  chan struct{}
    last  time.Time
    backend string
}

func (l *udpRouteListener) start(ctx context.Context) {
    // GC loop (start once)
    l.gcOnce.Do(func() {
        go l.gcLoop(ctx)
    })

    go func() {
        buf := make([]byte, 2048)
        for {
            n, src, err := l.conn.ReadFromUDP(buf)
            if err != nil {
                // closed or error
                var ne *net.OpError
                if !(errors.As(err, &ne) && ne.Timeout()) {
                    errs.V(l.log, err).Info("udp read error", "error", err)
                }
                return
            }

            if n == 0 {
                continue
            }

            // Update/increment metric in
            addDatagramsIn(1)
            addBytesIn(int64(n))

            l.handlePacket(ctx, src, buf[:n])
        }
    }()
}

func (l *udpRouteListener) handlePacket(ctx context.Context, src *net.UDPAddr, data []byte) {
    key := src.String()
    c := l.getOrCreateClient(ctx, key, src)
    if c == nil {
        // limit exceeded or dial failed
        return
    }

    // write downstream->upstream
    if _, err := c.up.Write(data); err != nil {
        errs.V(l.log, err).Info("udp write upstream error", "backend", c.backend)
        l.removeClient(key)
        return
    }

    l.touch(key)
}

func (l *udpRouteListener) getOrCreateClient(ctx context.Context, key string, src *net.UDPAddr) *udpClient {
    l.mu.Lock()
    if c, ok := l.clients[key]; ok {
        l.mu.Unlock()
        return c
    }
    if len(l.clients) >= l.limit {
        l.mu.Unlock()
        l.log.V(1).Info("udp client limit reached, dropping", "limit", l.limit)
        addDropsLimit(1)
        return nil
    }
    l.mu.Unlock()

    // Select backend using strategy
    hostLabel := "udp"
    if len(l.route.Host) > 0 {
        hostLabel = l.route.Host[0]
    }

    tryBackends := l.route.Backend.Copy()
    var backend string
    for {
        if len(tryBackends) == 0 {
            break
        }
        cand, _, ok2 := l.strategy.GetNextBackend(l.log, l.route, hostLabel, tryBackends)
        if !ok2 {
            break
        }
        backend = cand
        // Remove selected from list for next iteration on failure
        // Note: we don't normalize here; it's okay for simple matching
        for i, b := range tryBackends {
            if b == backend {
                tryBackends = append(tryBackends[:i], tryBackends[i+1:]...)
                break
            }
        }
        // Dial to selected backend host with UDPBackendPort
        rhost := netutil.HostStr(backend)
        raddrStr := net.JoinHostPort(rhost, strconv.Itoa(l.route.UDPBackendPort))
        raddr, err := net.ResolveUDPAddr("udp", raddrStr)
        if err != nil {
            errs.V(l.log, err).Info("resolve udp backend failed", "addr", raddrStr)
            continue
        }
        up, err := net.DialUDP("udp", nil, raddr)
        if err != nil {
            errs.V(l.log, err).Info("dial udp backend failed", "addr", raddrStr)
            continue
        }
        _ = up.SetReadBuffer(l.readBuf)
        _ = up.SetWriteBuffer(l.writeBuf)

        client := &udpClient{src: src, up: up, done: make(chan struct{}), last: time.Now(), backend: raddrStr}
        // Start upstream reader
        go l.pipeUpstream(ctx, key, client)

        l.mu.Lock()
        l.clients[key] = client
        l.mu.Unlock()

        // Update metric
        addActiveClients(1)

        l.log.V(1).Info("udp mapping created", "client", key, "backend", raddrStr)
        return client
    }

    // All backends failed
    return nil
}

func (l *udpRouteListener) pipeUpstream(ctx context.Context, key string, c *udpClient) {
    defer close(c.done)
    buf := make([]byte, 2048)
    for {
        n, _, err := c.up.ReadFromUDP(buf)
        if err != nil {
            errs.V(l.log, err).Info("udp read upstream error", "backend", c.backend)
            _ = c.up.Close()
            l.removeClient(key)
            return
        }
        if n == 0 {
            continue
        }
        // upstream -> client
        if _, err := l.conn.WriteToUDP(buf[:n], c.src); err != nil {
            errs.V(l.log, err).Info("udp write to client error", "client", c.src)
            _ = c.up.Close()
            l.removeClient(key)
            return
        }
        addDatagramsOut(1)
        addBytesOut(int64(n))
    }
}

func (l *udpRouteListener) touch(key string) {
    l.mu.Lock()
    if c, ok := l.clients[key]; ok {
        c.last = time.Now()
    }
    l.mu.Unlock()
}

func (l *udpRouteListener) removeClient(key string) {
    l.mu.Lock()
    c, ok := l.clients[key]
    if ok {
        delete(l.clients, key)
    }
    l.mu.Unlock()
    if ok {
        _ = c.up.Close()
        addActiveClients(-1)
        l.log.V(1).Info("udp mapping removed", "client", key)
    }
}

func (l *udpRouteListener) gcLoop(ctx context.Context) {
    if l.idleAfter <= 0 {
        return
    }
    ticker := time.NewTicker(time.Second * 10)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            cutoff := time.Now().Add(-l.idleAfter)
            var toClose []string
            l.mu.Lock()
            for k, c := range l.clients {
                if c.last.Before(cutoff) {
                    toClose = append(toClose, k)
                }
            }
            l.mu.Unlock()
            for _, k := range toClose {
                l.removeClient(k)
            }
        }
    }
}

func (l *udpRouteListener) close() error {
    l.mu.Lock()
    for k, c := range l.clients {
        _ = c.up.Close()
        delete(l.clients, k)
    }
    l.mu.Unlock()
    return l.conn.Close()
}
