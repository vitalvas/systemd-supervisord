package socketactivation

import (
	"context"
	"fmt"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

// Manager starts and supervises all configured socket_activation listeners.
type Manager struct {
	activators []*Activator
}

// NewManager builds a Manager for the given entries, wiring each activator to
// the shared unit controller and health supervisor. sup may be nil to disable
// continuous monitoring of running backends.
func NewManager(entries []config.SocketActivationConfig, ctrl UnitController, sup HealthSupervisor) *Manager {
	activators := make([]*Activator, 0, len(entries))
	for i := range entries {
		activators = append(activators, New(entries[i], ctrl, sup))
	}

	return &Manager{activators: activators}
}

// Enabled reports whether any listeners are configured.
func (m *Manager) Enabled() bool {
	return len(m.activators) > 0
}

// Start binds and serves every configured listener. If any listener fails to
// bind, Start returns the first error; listeners already started keep running
// until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) error {
	for _, a := range m.activators {
		if err := a.Start(ctx); err != nil {
			return fmt.Errorf("starting socket activation %q: %w", a.Name(), err)
		}
	}

	return nil
}
