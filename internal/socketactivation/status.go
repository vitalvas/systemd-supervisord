package socketactivation

// Status is a point-in-time snapshot of a single socket-activation listener,
// suitable for reporting over the CLI socket.
type Status struct {
	Name              string   `json:"name"`
	Unit              string   `json:"unit"`
	Listen            string   `json:"listen"`
	Protocol          []string `json:"protocol"`
	Backend           string   `json:"backend"`
	Running           bool     `json:"running"`
	ActiveConnections int      `json:"active_connections"`
	IdleSeconds       float64  `json:"idle_seconds"`
}

// Status returns a snapshot of the activator's current runtime state.
func (a *Activator) Status() Status {
	a.mu.Lock()
	running := a.running
	active := a.active
	lastActive := a.lastActive
	a.mu.Unlock()

	var idleSeconds float64
	if running && active == 0 && !lastActive.IsZero() {
		idleSeconds = a.clock.Now().Sub(lastActive).Seconds()
	}

	return Status{
		Name:              a.cfg.Name,
		Unit:              a.cfg.Unit,
		Listen:            a.cfg.Listen,
		Protocol:          a.cfg.Protocol,
		Backend:           a.cfg.Backend,
		Running:           running,
		ActiveConnections: active,
		IdleSeconds:       idleSeconds,
	}
}

// Statuses returns a snapshot of every configured listener.
func (m *Manager) Statuses() []Status {
	statuses := make([]Status, 0, len(m.activators))
	for _, a := range m.activators {
		statuses = append(statuses, a.Status())
	}

	return statuses
}
