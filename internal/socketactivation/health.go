package socketactivation

import (
	"context"
	"errors"
	"time"

	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/healthcheck"
)

// healthProbe reports whether the backend is ready to receive connections.
// probe returns nil when the backend is healthy, or an error describing why it
// is not; implementations must respect the provided context deadline. pollInterval
// reports the cadence to re-run probe during the startup readiness wait.
type healthProbe interface {
	probe(ctx context.Context) error
	pollInterval() time.Duration
}

// checksProbe runs a set of general health checks once and treats the backend
// as ready only when every check passes. It reuses the shared single-shot probe
// from the healthcheck package so socket activation and continuous monitoring
// share identical check semantics.
type checksProbe struct {
	checks []config.HealthCheck
}

func newChecksProbe(checks []config.HealthCheck) *checksProbe {
	return &checksProbe{checks: checks}
}

func (p *checksProbe) probe(ctx context.Context) error {
	var errs []error

	for i := range p.checks {
		if err := healthcheck.Probe(ctx, &p.checks[i]); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// pollInterval returns the cadence to poll the checks during the startup
// readiness wait. It is the smallest configured check interval, falling back to
// one second when no checks are configured.
func (p *checksProbe) pollInterval() time.Duration {
	interval := time.Duration(0)

	for i := range p.checks {
		if interval == 0 || p.checks[i].Interval < interval {
			interval = p.checks[i].Interval
		}
	}

	if interval <= 0 {
		return time.Second
	}

	return interval
}
