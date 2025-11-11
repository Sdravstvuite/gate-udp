package lite

// Lite encapsulates all lite mode functionality for a Gate proxy instance.
// This provides a clean abstraction for lite mode features and avoids global state.
type Lite struct {
    strategyManager *StrategyManager
    udpManager      *UDPManager
}

// NewLite creates a new Lite instance for a Gate proxy.
func NewLite() *Lite {
    return &Lite{
        strategyManager: NewStrategyManager(),
        udpManager:      nil,
    }
}

// StrategyManager returns the strategy manager for load balancing.
func (l *Lite) StrategyManager() *StrategyManager {
    return l.strategyManager
}

// UDPManager returns the UDP manager, may be nil until initialized by proxy.
func (l *Lite) UDPManager() *UDPManager { return l.udpManager }

// SetUDPManager sets the UDP manager (used by proxy for lifecycle management).
func (l *Lite) SetUDPManager(m *UDPManager) { l.udpManager = m }
