package socketactivation

import (
	"context"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

// HealthSupervisor plugs a running socket-activation backend into the shared
// health-check and restart pipeline. While the backend is running the activator
// registers it so the existing healthcheck.Checker and restarter monitor and
// restart it; on idle-stop the activator unregisters it again.
//
// It is the subset of the daemon the activator depends on for continuous
// monitoring, kept as an interface so socket activation does not import the
// daemon or statemanager directly.
type HealthSupervisor interface {
	// Watch begins monitoring unit with the given checks and restart policy. The
	// supplied context bounds the monitoring lifetime; when it is cancelled the
	// supervisor stops the checker for unit.
	Watch(ctx context.Context, unit string, checks []config.HealthCheck, policy config.RestartPolicy)
	// Unwatch stops monitoring unit and removes it from the restart pipeline.
	Unwatch(unit string)
}

// unitMonitor tracks the monitoring lifecycle for a single activator run. It is
// created when the backend transitions to running and torn down when it stops,
// so monitoring is active only while the activator holds the unit up.
type unitMonitor struct {
	sup    HealthSupervisor
	unit   string
	checks []config.HealthCheck
	policy config.RestartPolicy
	cancel context.CancelFunc
}

func newUnitMonitor(sup HealthSupervisor, unit string, checks []config.HealthCheck, policy config.RestartPolicy) *unitMonitor {
	return &unitMonitor{
		sup:    sup,
		unit:   unit,
		checks: checks,
		policy: policy,
	}
}

// start registers the unit with the shared pipeline and begins monitoring. The
// monitoring context is derived from ctx so it is cancelled either explicitly by
// stop or when the parent serving context ends.
func (m *unitMonitor) start(ctx context.Context) {
	monitorCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	m.sup.Watch(monitorCtx, m.unit, m.checks, m.policy)
}

// stop ends monitoring and removes the unit from the restart pipeline.
func (m *unitMonitor) stop() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	m.sup.Unwatch(m.unit)
}
