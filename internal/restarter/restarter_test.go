package restarter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

type mockManager struct {
	restartCh chan string
}

func (m *mockManager) Start(_ context.Context, _ string) error { return nil }
func (m *mockManager) Stop(_ context.Context, _ string) error  { return nil }
func (m *mockManager) Close() error                            { return nil }
func (m *mockManager) GetUnitState(_ context.Context, _ string) (*systemd.UnitState, error) {
	return nil, nil
}
func (m *mockManager) ListUnits(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (m *mockManager) WatchUnit(_ context.Context, _ string, _ chan<- systemd.StateChange) error {
	return nil
}
func (m *mockManager) Restart(_ context.Context, unit string) error {
	m.restartCh <- unit

	return nil
}

type failingMockManager struct {
	restartCh chan string
	err       error
}

func (m *failingMockManager) Start(_ context.Context, _ string) error { return nil }
func (m *failingMockManager) Stop(_ context.Context, _ string) error  { return nil }
func (m *failingMockManager) Close() error                            { return nil }
func (m *failingMockManager) GetUnitState(_ context.Context, _ string) (*systemd.UnitState, error) {
	return nil, nil
}
func (m *failingMockManager) ListUnits(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (m *failingMockManager) WatchUnit(_ context.Context, _ string, _ chan<- systemd.StateChange) error {
	return nil
}
func (m *failingMockManager) Restart(_ context.Context, unit string) error {
	m.restartCh <- unit

	return m.err
}

func TestRestarter(t *testing.T) {
	t.Run("restart on failed state", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 1 * time.Second,
		})

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		select {
		case unit := <-mock.restartCh:
			assert.Equal(t, "app.service", unit)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for restart")
		}
	})

	t.Run("keeps restarting on repeated failures", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 1 * time.Second,
		})

		for i := 0; i < 5; i++ {
			r.HandleEvent(statemanager.Event{
				Type:        statemanager.EventStateChanged,
				UnitName:    "app.service",
				ActiveState: "failed",
				Timestamp:   time.Now(),
			})

			select {
			case unit := <-mock.restartCh:
				assert.Equal(t, "app.service", unit)
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out waiting for restart attempt %d", i+1)
			}
		}
	})

	t.Run("no restart when not enabled", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled: false,
		})

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		time.Sleep(200 * time.Millisecond)
		assert.Empty(t, mock.restartCh)
	})

	t.Run("no restart on active state", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 1 * time.Second,
		})

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "active",
			SubState:    "running",
			Timestamp:   time.Now(),
		})

		time.Sleep(200 * time.Millisecond)
		assert.Empty(t, mock.restartCh)
	})

	t.Run("recovery resets attempts", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 1 * time.Second,
		})

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})
		<-mock.restartCh

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "active",
			SubState:    "running",
			Timestamp:   time.Now(),
		})

		r.mu.Lock()
		assert.Equal(t, 0, r.trackers["app.service"].attempts)
		r.mu.Unlock()
	})

	t.Run("health check failure triggers restart", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 1 * time.Second,
		})

		healthy := false
		r.HandleEvent(statemanager.Event{
			Type:      statemanager.EventHealthChanged,
			UnitName:  "app.service",
			Healthy:   &healthy,
			Timestamp: time.Now(),
		})

		select {
		case unit := <-mock.restartCh:
			assert.Equal(t, "app.service", unit)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for restart")
		}
	})

	t.Run("unknown unit ignored", func(t *testing.T) {
		sm := statemanager.New(10)

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "unknown.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		time.Sleep(200 * time.Millisecond)
		assert.Empty(t, mock.restartCh)
	})

	t.Run("cooldown respected after restart command fails", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 5 * time.Second,
		})

		r.mu.Lock()
		r.trackers["app.service"].cooldownUntil = time.Now().Add(5 * time.Second)
		r.mu.Unlock()

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		time.Sleep(200 * time.Millisecond)
		assert.Empty(t, mock.restartCh)
	})

	t.Run("failed restart sets cooldown", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &failingMockManager{
			restartCh: make(chan string, 10),
			err:       errors.New("restart failed"),
		}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 5 * time.Second,
		})

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		select {
		case unit := <-mock.restartCh:
			assert.Equal(t, "app.service", unit)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for restart attempt")
		}

		time.Sleep(100 * time.Millisecond)

		r.mu.Lock()
		tracker := r.trackers["app.service"]
		assert.True(t, time.Now().Before(tracker.cooldownUntil), "cooldown should be set after failed restart")
		r.mu.Unlock()
	})

	t.Run("backoff delays restart", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  50 * time.Millisecond,
			Cooldown: 1 * time.Second,
		})

		start := time.Now()

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		select {
		case unit := <-mock.restartCh:
			elapsed := time.Since(start)
			assert.Equal(t, "app.service", unit)
			assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "restart should be delayed by backoff")
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for restart")
		}
	})

	t.Run("backoff increases exponentially", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  30 * time.Millisecond,
			Cooldown: 1 * time.Second,
		})

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		select {
		case <-mock.restartCh:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for first restart")
		}

		start := time.Now()

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		select {
		case <-mock.restartCh:
			elapsed := time.Since(start)
			assert.GreaterOrEqual(t, elapsed, 60*time.Millisecond, "second attempt should have 2x backoff")
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for second restart")
		}
	})

	t.Run("attemptRestart returns early for removed tracker", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)

		r.attemptRestart("nonexistent.service")

		time.Sleep(100 * time.Millisecond)
		assert.Empty(t, mock.restartCh)
	})

	t.Run("health recovery resets attempts", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 1 * time.Second,
		})

		unhealthy := false
		r.HandleEvent(statemanager.Event{
			Type:      statemanager.EventHealthChanged,
			UnitName:  "app.service",
			Healthy:   &unhealthy,
			Timestamp: time.Now(),
		})

		select {
		case <-mock.restartCh:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for restart")
		}

		r.mu.Lock()
		assert.Equal(t, 1, r.trackers["app.service"].attempts)
		r.mu.Unlock()

		healthy := true
		r.HandleEvent(statemanager.Event{
			Type:      statemanager.EventHealthChanged,
			UnitName:  "app.service",
			Healthy:   &healthy,
			Timestamp: time.Now(),
		})

		time.Sleep(100 * time.Millisecond)

		r.mu.Lock()
		assert.Equal(t, 0, r.trackers["app.service"].attempts)
		assert.True(t, r.trackers["app.service"].cooldownUntil.IsZero())
		r.mu.Unlock()
	})

	t.Run("cascade restart on dependent units", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("db.service")
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("db.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 1 * time.Second,
		})

		r.SetDependents(map[string][]string{
			"db.service": {"app.service"},
		})

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "db.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		var restarted []string
		for i := 0; i < 2; i++ {
			select {
			case unit := <-mock.restartCh:
				restarted = append(restarted, unit)
			case <-time.After(3 * time.Second):
				t.Fatalf("timed out waiting for restart %d", i+1)
			}
		}

		assert.Contains(t, restarted, "db.service")
		assert.Contains(t, restarted, "app.service")
	})

	t.Run("no cascade on failed restart", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("db.service")

		mock := &failingMockManager{
			restartCh: make(chan string, 10),
			err:       errors.New("restart failed"),
		}

		r := New(mock, sm)
		r.Register("db.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 1 * time.Second,
		})

		r.SetDependents(map[string][]string{
			"db.service": {"app.service"},
		})

		r.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "db.service",
			ActiveState: "failed",
			Timestamp:   time.Now(),
		})

		select {
		case unit := <-mock.restartCh:
			assert.Equal(t, "db.service", unit)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for restart")
		}

		time.Sleep(200 * time.Millisecond)

		select {
		case unit := <-mock.restartCh:
			t.Fatalf("unexpected cascade restart of %s after failed primary restart", unit)
		default:
		}
	})

	t.Run("set dependents", func(t *testing.T) {
		sm := statemanager.New(10)
		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)

		deps := map[string][]string{
			"db.service": {"app.service", "worker.service"},
		}

		r.SetDependents(deps)

		r.mu.Lock()
		assert.Equal(t, deps, r.dependents)
		r.mu.Unlock()
	})

	t.Run("unknown event type ignored", func(t *testing.T) {
		sm := statemanager.New(10)
		sm.Register("app.service")

		mock := &mockManager{restartCh: make(chan string, 10)}

		r := New(mock, sm)
		r.Register("app.service", config.RestartPolicy{
			Enabled:  true,
			Backoff:  0,
			Cooldown: 1 * time.Second,
		})

		r.HandleEvent(statemanager.Event{
			Type:      99,
			UnitName:  "app.service",
			Timestamp: time.Now(),
		})

		time.Sleep(200 * time.Millisecond)
		assert.Empty(t, mock.restartCh)
	})
}
