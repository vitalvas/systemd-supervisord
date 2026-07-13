package socketactivation

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

type fakeSupervisor struct {
	mu        sync.Mutex
	watched   []string
	unwatched []string
	lastCtx   context.Context
}

func (s *fakeSupervisor) Watch(ctx context.Context, unit string, _ []config.HealthCheck, _ config.RestartPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.watched = append(s.watched, unit)
	s.lastCtx = ctx
}

func (s *fakeSupervisor) Unwatch(unit string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.unwatched = append(s.unwatched, unit)
}

func (s *fakeSupervisor) watchedUnits() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.watched...)
}

func (s *fakeSupervisor) unwatchedUnits() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.unwatched...)
}

func TestUnitMonitor(t *testing.T) {
	checks := []config.HealthCheck{{Type: "tcp", TCP: &config.TCPHealthCheck{Address: "127.0.0.1:1"}}}
	policy := config.RestartPolicy{Enabled: true}

	t.Run("start watches and stop unwatches", func(t *testing.T) {
		sup := &fakeSupervisor{}
		m := newUnitMonitor(sup, "backend.service", checks, policy)

		m.start(context.Background())
		assert.Equal(t, []string{"backend.service"}, sup.watchedUnits())

		m.stop()
		assert.Equal(t, []string{"backend.service"}, sup.unwatchedUnits())
	})

	t.Run("start cancels monitoring context on stop", func(t *testing.T) {
		sup := &fakeSupervisor{}
		m := newUnitMonitor(sup, "backend.service", checks, policy)

		m.start(context.Background())
		ctx := sup.lastCtx

		m.stop()

		select {
		case <-ctx.Done():
		default:
			t.Fatal("monitoring context was not cancelled on stop")
		}
	})

	t.Run("stop without start still unwatches", func(t *testing.T) {
		sup := &fakeSupervisor{}
		m := newUnitMonitor(sup, "backend.service", checks, policy)

		m.stop()
		assert.Equal(t, []string{"backend.service"}, sup.unwatchedUnits())
	})
}
