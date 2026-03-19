package restarter

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

const maxBackoffShift = 6

type unitTracker struct {
	policy        config.RestartPolicy
	attempts      int
	cooldownUntil time.Time
}

type Restarter struct {
	mgr        systemd.Manager
	sm         *statemanager.StateManager
	trackers   map[string]*unitTracker
	dependents map[string][]string
	mu         sync.Mutex
}

func New(mgr systemd.Manager, sm *statemanager.StateManager) *Restarter {
	return &Restarter{
		mgr:        mgr,
		sm:         sm,
		trackers:   make(map[string]*unitTracker),
		dependents: make(map[string][]string),
	}
}

func (r *Restarter) SetDependents(dependents map[string][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.dependents = dependents
}

func (r *Restarter) Register(unitName string, policy config.RestartPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.trackers[unitName] = &unitTracker{
		policy: policy,
	}
}

func (r *Restarter) HandleEvent(ev statemanager.Event) {
	if !r.shouldRestart(ev) {
		return
	}

	go r.attemptRestart(ev.UnitName)
}

func (r *Restarter) shouldRestart(ev statemanager.Event) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	tracker, ok := r.trackers[ev.UnitName]
	if !ok || !tracker.policy.Enabled {
		return false
	}

	switch ev.Type {
	case statemanager.EventStateChanged:
		if ev.ActiveState != "failed" {
			if ev.ActiveState == "active" {
				tracker.attempts = 0
				tracker.cooldownUntil = time.Time{}

				r.sm.ResetRestartCount(ev.UnitName)
			}

			return false
		}
	case statemanager.EventHealthChanged:
		if ev.Healthy != nil && *ev.Healthy {
			tracker.attempts = 0
			tracker.cooldownUntil = time.Time{}

			r.sm.ResetRestartCount(ev.UnitName)

			return false
		}
	default:
		return false
	}

	if time.Now().Before(tracker.cooldownUntil) {
		slog.Warn("unit in cooldown, skipping restart",
			"unit", ev.UnitName,
			"cooldown_until", tracker.cooldownUntil,
		)

		return false
	}

	return true
}

func (r *Restarter) attemptRestart(unitName string) {
	r.mu.Lock()
	tracker, ok := r.trackers[unitName]
	if !ok {
		r.mu.Unlock()

		return
	}

	shift := tracker.attempts
	if shift > maxBackoffShift {
		shift = maxBackoffShift
	}

	backoff := tracker.policy.Backoff * time.Duration(1<<shift)
	tracker.attempts++

	r.mu.Unlock()

	if backoff > 0 {
		slog.Info("waiting before restart", "unit", unitName, "backoff", backoff)
		time.Sleep(backoff)
	}

	r.sm.IncrementRestartCount(unitName)

	slog.Info("restarting unit", "unit", unitName, "attempt", tracker.attempts)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := r.mgr.Restart(ctx, unitName); err != nil {
		slog.Error("restart failed", "unit", unitName, "error", err)

		r.mu.Lock()
		tracker.cooldownUntil = time.Now().Add(tracker.policy.Cooldown)
		r.mu.Unlock()

		slog.Warn("entering cooldown after failed restart",
			"unit", unitName,
			"cooldown", tracker.policy.Cooldown,
		)

		return
	}

	r.cascadeRestart(unitName)
}

func (r *Restarter) cascadeRestart(unitName string) {
	r.mu.Lock()
	deps := r.dependents[unitName]
	r.mu.Unlock()

	for _, dep := range deps {
		slog.Info("cascade restarting dependent unit", "unit", dep, "dependency", unitName)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		if err := r.mgr.Restart(ctx, dep); err != nil {
			slog.Error("cascade restart failed", "unit", dep, "error", err)
		}

		cancel()
	}
}
