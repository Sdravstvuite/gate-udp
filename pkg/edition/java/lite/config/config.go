package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"go.minekube.com/gate/pkg/edition/java/forge/modinfo"
	"go.minekube.com/gate/pkg/edition/java/ping"
	"go.minekube.com/gate/pkg/gate/proto"
	"go.minekube.com/gate/pkg/util/configutil"
	"go.minekube.com/gate/pkg/util/favicon"
	"go.minekube.com/gate/pkg/util/netutil"
)

// DefaultConfig is the default configuration for Lite mode.
var DefaultConfig = Config{
	Enabled: false,
	Routes:  []Route{},
}

type (
	// Config is the configuration for Lite mode.
	Config struct {
		Enabled bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
		Routes  []Route `yaml:"routes,omitempty" json:"routes,omitempty"`
	}
	Route struct {
		Host          configutil.SingleOrMulti[string] `json:"host,omitempty" yaml:"host,omitempty"`
		Backend       configutil.SingleOrMulti[string] `json:"backend,omitempty" yaml:"backend,omitempty"`
		CachePingTTL  configutil.Duration              `json:"cachePingTTL,omitempty" yaml:"cachePingTTL,omitempty"` // 0 = default, < 0 = disabled
		Fallback      *Status                          `json:"fallback,omitempty" yaml:"fallback,omitempty"`         // nil = disabled
		ProxyProtocol bool                             `json:"proxyProtocol,omitempty" yaml:"proxyProtocol,omitempty"`
		// Deprecated: use TCPShieldRealIP instead.
		RealIP            bool     `json:"realIP,omitempty" yaml:"realIP,omitempty"`
		TCPShieldRealIP   bool     `json:"tcpShieldRealIP,omitempty" yaml:"tcpShieldRealIP,omitempty"`
		ModifyVirtualHost bool     `json:"modifyVirtualHost,omitempty" yaml:"modifyVirtualHost,omitempty"`
		Strategy          Strategy `json:"strategy,omitempty" yaml:"strategy,omitempty"`

		// UDP forwarding options for Lite mode. When enabled, a separate UDP listener is started
		// on the same bind IP as the proxy with a configurable port, and datagrams are forwarded
		// to the selected backend host on the given UDP backend port.
		UDP            bool                `json:"udp,omitempty" yaml:"udp,omitempty"`
		UDPListenPort  int                 `json:"udpListenPort,omitempty" yaml:"udpListenPort,omitempty"`
		UDPBackendPort int                 `json:"udpBackendPort,omitempty" yaml:"udpBackendPort,omitempty"`
		UDPIdleTimeout configutil.Duration `json:"udpIdleTimeout,omitempty" yaml:"udpIdleTimeout,omitempty"` // 0 = default, < 0 = disabled
		UDPClientLimit int                 `json:"udpClientLimit,omitempty" yaml:"udpClientLimit,omitempty"` // 0 = default
		UDPReadBuffer  int                 `json:"udpReadBuffer,omitempty" yaml:"udpReadBuffer,omitempty"`   // bytes; 0 = default
		UDPWriteBuffer int                 `json:"udpWriteBuffer,omitempty" yaml:"udpWriteBuffer,omitempty"` // bytes; 0 = default
	}
	Status struct {
		MOTD    *configutil.TextComponent `yaml:"motd,omitempty" json:"motd,omitempty"`
		Version ping.Version              `yaml:"version,omitempty" json:"version,omitempty"`
		Players *ping.Players             `json:"players,omitempty" yaml:"players,omitempty"`
		Favicon favicon.Favicon           `yaml:"favicon,omitempty" json:"favicon,omitempty"`
		ModInfo modinfo.ModInfo           `yaml:"modInfo,omitempty" json:"modInfo,omitempty"`
	}
)

// Response returns the configured status response.
func (s *Status) Response(proto.Protocol) (*ping.ServerPing, error) {
	return &ping.ServerPing{
		Version:     s.Version,
		Players:     s.Players,
		Description: s.MOTD.T(),
		Favicon:     s.Favicon,
		ModInfo:     &s.ModInfo,
	}, nil
}

// GetCachePingTTL returns the configured ping cache TTL or a default duration if not set.
func (r *Route) GetCachePingTTL() time.Duration {
    const defaultTTL = time.Second * 10
    if r.CachePingTTL == 0 {
        return defaultTTL
    }
    return time.Duration(r.CachePingTTL)
}

// CachePingEnabled returns true if the route has a ping cache enabled.
func (r *Route) CachePingEnabled() bool { return r.GetCachePingTTL() > 0 }

// GetTCPShieldRealIP returns the configured TCPShieldRealIP or deprecated RealIP value.
func (r *Route) GetTCPShieldRealIP() bool { return r.TCPShieldRealIP || r.RealIP }

// UDP helpers and defaults
func (r *Route) GetUDPIdleTimeout() time.Duration {
    const defaultUDPIdle = time.Second * 90
    if r.UDPIdleTimeout == 0 {
        return defaultUDPIdle
    }
    d := time.Duration(r.UDPIdleTimeout)
    if d < 0 {
        return 0
    }
    return d
}

func (r *Route) GetUDPClientLimit() int {
    const def = 10000
    if r.UDPClientLimit <= 0 {
        return def
    }
    return r.UDPClientLimit
}

func (r *Route) GetUDPReadBuffer() int {
    const def = 2 * 1024 * 1024 // 2 MiB
    if r.UDPReadBuffer <= 0 {
        return def
    }
    return r.UDPReadBuffer
}

func (r *Route) GetUDPWriteBuffer() int {
    const def = 2 * 1024 * 1024 // 2 MiB
    if r.UDPWriteBuffer <= 0 {
        return def
    }
    return r.UDPWriteBuffer
}

// Strategy represents a load balancing strategy for lite mode routes.
type Strategy string

const (
	// StrategySequential selects backends in config order for each connection attempt.
	StrategySequential Strategy = "sequential"

	// StrategyRandom selects a random backend from available options.
	StrategyRandom Strategy = "random"

	// StrategyRoundRobin cycles through backends in order for each new connection.
	StrategyRoundRobin Strategy = "round-robin"

	// StrategyLeastConnections selects the backend with the fewest active connections.
	StrategyLeastConnections Strategy = "least-connections"

	// StrategyLowestLatency selects the backend with the lowest ping response time.
	StrategyLowestLatency Strategy = "lowest-latency"
)

var allowedStrategies = []Strategy{
	StrategySequential,
	StrategyRandom,
	StrategyRoundRobin,
	StrategyLeastConnections,
	StrategyLowestLatency,
}

func (c Config) Validate() (warns []error, errs []error) {
    e := func(m string, args ...any) { errs = append(errs, fmt.Errorf(m, args...)) }

	if len(c.Routes) == 0 {
		e("No routes configured")
		return
	}

    usedUDPPorts := map[int]bool{}
    for i, ep := range c.Routes {
        if len(ep.Host) == 0 {
            e("Route %d: no host configured", i)
        }
        if len(ep.Backend) == 0 {
            e("Route %d: no backend configured", i)
        }
        if !slices.Contains(allowedStrategies, ep.Strategy) && ep.Strategy != "" {
            e("Route %d: invalid strategy '%s', allowed: %v", i, ep.Strategy, allowedStrategies)
        }
        for i, addr := range ep.Backend {
            _, err := netutil.Parse(addr, "tcp")
            if err != nil {
                e("Route %d: backend %d: failed to parse address: %w", i, err)
            }
        }

        // UDP validation
        if ep.UDP {
            if ep.UDPListenPort <= 0 || ep.UDPListenPort > 65535 {
                e("Route %d: invalid udpListenPort %d", i, ep.UDPListenPort)
            }
            if ep.UDPBackendPort <= 0 || ep.UDPBackendPort > 65535 {
                e("Route %d: invalid udpBackendPort %d", i, ep.UDPBackendPort)
            }
            if usedUDPPorts[ep.UDPListenPort] {
                e("Route %d: duplicate udpListenPort %d", i, ep.UDPListenPort)
            }
            usedUDPPorts[ep.UDPListenPort] = true
        }
    }

	return
}

// Equal returns true if the Routes are equal.
func (r *Route) Equal(other *Route) bool {
	j, err := json.Marshal(r)
	if err != nil {
		return false
	}
	o, err := json.Marshal(other)
	if err != nil {
		return false
	}
	return string(j) == string(o)
}
