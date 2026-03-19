package daemon

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/healthcheck"
	"github.com/vitalvas/systemd-supervisord/internal/notify"
	"github.com/vitalvas/systemd-supervisord/internal/restarter"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

type mockManager struct {
	mu           sync.Mutex
	startCalls   []string
	stopCalls    []string
	restartCalls []string
	stateCalls   []string
	listCalls    []string
	watchCalls   []string
	unitStates   map[string]*systemd.UnitState
	listResult   map[string][]string
	startErr     error
	stopErr      error
	restartErr   error
	getStateErr  error
	listErr      error
	watchErr     error
	closeCalled  bool
}

func newMockManager() *mockManager {
	return &mockManager{
		unitStates: make(map[string]*systemd.UnitState),
		listResult: make(map[string][]string),
	}
}

func (m *mockManager) Start(_ context.Context, unit string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.startCalls = append(m.startCalls, unit)

	return m.startErr
}

func (m *mockManager) Stop(_ context.Context, unit string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopCalls = append(m.stopCalls, unit)

	return m.stopErr
}

func (m *mockManager) Restart(_ context.Context, unit string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.restartCalls = append(m.restartCalls, unit)

	return m.restartErr
}

func (m *mockManager) GetUnitState(_ context.Context, unit string) (*systemd.UnitState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stateCalls = append(m.stateCalls, unit)

	if m.getStateErr != nil {
		return nil, m.getStateErr
	}

	state, ok := m.unitStates[unit]
	if !ok {
		return &systemd.UnitState{
			Name:        unit,
			ActiveState: "inactive",
			SubState:    "dead",
			LoadState:   "loaded",
		}, nil
	}

	return state, nil
}

func (m *mockManager) ListUnits(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.listCalls = append(m.listCalls, prefix)

	if m.listErr != nil {
		return nil, m.listErr
	}

	return m.listResult[prefix], nil
}

func (m *mockManager) WatchUnit(_ context.Context, unit string, _ chan<- systemd.StateChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.watchCalls = append(m.watchCalls, unit)

	return m.watchErr
}

func (m *mockManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closeCalled = true

	return nil
}

func newTestDaemon(mgr systemd.Manager, cfg *config.Config) *Daemon {
	sm := statemanager.New(100)
	notif := notify.New(cfg.Notify)
	rest := restarter.New(mgr, sm)

	sm.OnEvent(notif.HandleEvent)
	sm.OnEvent(rest.HandleEvent)

	return &Daemon{
		cfg:             cfg,
		mgr:             mgr,
		sm:              sm,
		notifier:        notif,
		restarter:       rest,
		changeCh:        make(chan systemd.StateChange, 100),
		healthCh:        make(chan healthcheck.Result, 100),
		registeredUnits: make(map[string]struct{}),
	}
}

func TestExtractInstance(t *testing.T) {
	t.Run("standard template instance", func(t *testing.T) {
		result := extractInstance("myapp@instance1.service", "myapp@", "service")
		assert.Equal(t, "instance1", result)
	})

	t.Run("numeric instance", func(t *testing.T) {
		result := extractInstance("worker@8080.service", "worker@", "service")
		assert.Equal(t, "8080", result)
	})

	t.Run("timer type", func(t *testing.T) {
		result := extractInstance("backup@daily.timer", "backup@", "timer")
		assert.Equal(t, "daily", result)
	})

	t.Run("empty instance", func(t *testing.T) {
		result := extractInstance("app@.service", "app@", "service")
		assert.Equal(t, "", result)
	})

	t.Run("no matching prefix", func(t *testing.T) {
		result := extractInstance("other@inst.service", "myapp@", "service")
		assert.Equal(t, "other@inst", result)
	})

	t.Run("no matching suffix", func(t *testing.T) {
		result := extractInstance("myapp@inst.timer", "myapp@", "service")
		assert.Equal(t, "inst.timer", result)
	})

	t.Run("instance with special characters", func(t *testing.T) {
		result := extractInstance("app@host-name_01.service", "app@", "service")
		assert.Equal(t, "host-name_01", result)
	})
}

func TestSetupLogging(t *testing.T) {
	t.Run("debug level", func(_ *testing.T) {
		setupLogging("debug")
	})

	t.Run("warn level", func(_ *testing.T) {
		setupLogging("warn")
	})

	t.Run("error level", func(_ *testing.T) {
		setupLogging("error")
	})

	t.Run("info level explicit", func(_ *testing.T) {
		setupLogging("info")
	})

	t.Run("default level for unknown value", func(_ *testing.T) {
		setupLogging("unknown")
	})

	t.Run("empty string defaults to info", func(_ *testing.T) {
		setupLogging("")
	})
}

func TestHasDiscoverableUnits(t *testing.T) {
	t.Run("no units", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{},
			},
		}

		assert.False(t, d.hasDiscoverableUnits())
	})

	t.Run("enabled template with discover true", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{
						Name:     "myapp@",
						Type:     "service",
						Enabled:  true,
						Discover: true,
					},
				},
			},
		}

		assert.True(t, d.hasDiscoverableUnits())
	})

	t.Run("disabled template with discover true", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{
						Name:     "myapp@",
						Type:     "service",
						Enabled:  false,
						Discover: true,
					},
				},
			},
		}

		assert.False(t, d.hasDiscoverableUnits())
	})

	t.Run("enabled non-template with discover true", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{
						Name:     "myapp",
						Type:     "service",
						Enabled:  true,
						Discover: true,
					},
				},
			},
		}

		assert.False(t, d.hasDiscoverableUnits())
	})

	t.Run("enabled template with discover false", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{
						Name:     "myapp@",
						Type:     "service",
						Enabled:  true,
						Discover: false,
					},
				},
			},
		}

		assert.False(t, d.hasDiscoverableUnits())
	})

	t.Run("mixed units with one discoverable", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{
						Name:    "regular",
						Type:    "service",
						Enabled: true,
					},
					{
						Name:     "template@",
						Type:     "service",
						Enabled:  true,
						Discover: true,
					},
				},
			},
		}

		assert.True(t, d.hasDiscoverableUnits())
	})
}

func TestRegisterUnit(t *testing.T) {
	t.Run("registers new unit with state fetch", func(t *testing.T) {
		mgr := newMockManager()
		mgr.unitStates["myapp.service"] = &systemd.UnitState{
			Name:        "myapp.service",
			ActiveState: "active",
			SubState:    "running",
			LoadState:   "loaded",
		}

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:    "myapp",
			Type:    "service",
			Enabled: true,
		}

		d.registerUnit(context.Background(), "myapp.service", unit)

		assert.Contains(t, d.registeredUnits, "myapp.service")
		assert.Contains(t, mgr.stateCalls, "myapp.service")
		assert.Contains(t, mgr.watchCalls, "myapp.service")

		status := d.sm.GetStatus("myapp.service")
		require.NotNil(t, status)
		assert.Equal(t, "active", status.ActiveState)
		assert.Equal(t, "running", status.SubState)
	})

	t.Run("idempotent registration", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:    "myapp",
			Type:    "service",
			Enabled: true,
		}

		d.registerUnit(context.Background(), "myapp.service", unit)
		d.registerUnit(context.Background(), "myapp.service", unit)

		assert.Len(t, mgr.stateCalls, 1)
		assert.Len(t, mgr.watchCalls, 1)
	})

	t.Run("registers unit with restart policy", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:    "myapp",
			Type:    "service",
			Enabled: true,
			Restart: &config.RestartPolicy{
				Enabled:  true,
				Backoff:  5 * time.Second,
				Cooldown: 60 * time.Second,
			},
		}

		d.registerUnit(context.Background(), "myapp.service", unit)

		assert.Contains(t, d.registeredUnits, "myapp.service")
	})

	t.Run("handles state fetch error gracefully", func(t *testing.T) {
		mgr := newMockManager()
		mgr.getStateErr = fmt.Errorf("dbus error")

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:    "myapp",
			Type:    "service",
			Enabled: true,
		}

		d.registerUnit(context.Background(), "myapp.service", unit)

		assert.Contains(t, d.registeredUnits, "myapp.service")

		status := d.sm.GetStatus("myapp.service")
		require.NotNil(t, status)
		assert.Empty(t, status.ActiveState)
	})

	t.Run("handles watch error gracefully", func(t *testing.T) {
		mgr := newMockManager()
		mgr.watchErr = fmt.Errorf("watch error")

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:    "myapp",
			Type:    "service",
			Enabled: true,
		}

		d.registerUnit(context.Background(), "myapp.service", unit)

		assert.Contains(t, d.registeredUnits, "myapp.service")
	})

	t.Run("registers template instance with resolved health checks", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:     "myapp@",
			Type:     "service",
			Enabled:  true,
			Discover: true,
			HealthChecks: []config.HealthCheck{
				{
					Type:     "tcp",
					TCP:      &config.TCPHealthCheck{Address: "localhost:{{instance}}"},
					Interval: 10 * time.Second,
					Timeout:  5 * time.Second,
					Retries:  3,
				},
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		d.registerUnit(ctx, "myapp@8080.service", unit)

		assert.Contains(t, d.registeredUnits, "myapp@8080.service")
	})
}

func TestRegisterUnitWithGracePeriod(t *testing.T) {
	t.Run("health check delayed by grace period", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:        "app",
			Type:        "service",
			Enabled:     true,
			GracePeriod: 200 * time.Millisecond,
			HealthChecks: []config.HealthCheck{
				{
					Type:     "tcp",
					TCP:      &config.TCPHealthCheck{Address: "127.0.0.1:1"},
					Interval: 50 * time.Millisecond,
					Timeout:  50 * time.Millisecond,
					Retries:  1,
				},
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		d.registerUnit(ctx, "app.service", unit)

		time.Sleep(100 * time.Millisecond)
		assert.Empty(t, d.healthCh, "health check should not have started yet during grace period")
	})

	t.Run("health check starts after grace period", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:        "app",
			Type:        "service",
			Enabled:     true,
			GracePeriod: 100 * time.Millisecond,
			HealthChecks: []config.HealthCheck{
				{
					Type:     "tcp",
					TCP:      &config.TCPHealthCheck{Address: "127.0.0.1:1"},
					Interval: 50 * time.Millisecond,
					Timeout:  50 * time.Millisecond,
					Retries:  1,
				},
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		d.registerUnit(ctx, "app.service", unit)

		time.Sleep(50 * time.Millisecond)
		assert.Empty(t, d.healthCh, "health check should not fire before grace period")

		time.Sleep(250 * time.Millisecond)

		select {
		case <-d.healthCh:
		case <-ctx.Done():
			t.Fatal("timed out waiting for health check result after grace period")
		}
	})

	t.Run("grace period context cancelled", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:        "app",
			Type:        "service",
			Enabled:     true,
			GracePeriod: 5 * time.Second,
			HealthChecks: []config.HealthCheck{
				{
					Type:     "tcp",
					TCP:      &config.TCPHealthCheck{Address: "127.0.0.1:1"},
					Interval: 50 * time.Millisecond,
					Timeout:  50 * time.Millisecond,
					Retries:  1,
				},
			},
		}

		ctx, cancel := context.WithCancel(context.Background())

		d.registerUnit(ctx, "app.service", unit)

		cancel()

		time.Sleep(100 * time.Millisecond)
		assert.Empty(t, d.healthCh)
	})
}

func TestDiscoverInstances(t *testing.T) {
	t.Run("discovers and registers matching units", func(t *testing.T) {
		mgr := newMockManager()
		mgr.listResult["myapp@"] = []string{
			"myapp@inst1.service",
			"myapp@inst2.service",
			"myapp@inst3.service",
		}

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:     "myapp@",
			Type:     "service",
			Enabled:  true,
			Discover: true,
		}

		d.discoverInstances(context.Background(), unit)

		assert.Contains(t, d.registeredUnits, "myapp@inst1.service")
		assert.Contains(t, d.registeredUnits, "myapp@inst2.service")
		assert.Contains(t, d.registeredUnits, "myapp@inst3.service")
		assert.Len(t, d.registeredUnits, 3)
	})

	t.Run("filters out units with wrong suffix", func(t *testing.T) {
		mgr := newMockManager()
		mgr.listResult["myapp@"] = []string{
			"myapp@inst1.service",
			"myapp@inst2.timer",
		}

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:     "myapp@",
			Type:     "service",
			Enabled:  true,
			Discover: true,
		}

		d.discoverInstances(context.Background(), unit)

		assert.Contains(t, d.registeredUnits, "myapp@inst1.service")
		assert.NotContains(t, d.registeredUnits, "myapp@inst2.timer")
		assert.Len(t, d.registeredUnits, 1)
	})

	t.Run("handles list error gracefully", func(t *testing.T) {
		mgr := newMockManager()
		mgr.listErr = fmt.Errorf("dbus error")

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:     "myapp@",
			Type:     "service",
			Enabled:  true,
			Discover: true,
		}

		d.discoverInstances(context.Background(), unit)

		assert.Empty(t, d.registeredUnits)
	})

	t.Run("does not re-register already registered units", func(t *testing.T) {
		mgr := newMockManager()
		mgr.listResult["myapp@"] = []string{
			"myapp@inst1.service",
		}

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:     "myapp@",
			Type:     "service",
			Enabled:  true,
			Discover: true,
		}

		d.discoverInstances(context.Background(), unit)
		d.discoverInstances(context.Background(), unit)

		assert.Len(t, mgr.stateCalls, 1)
	})

	t.Run("handles empty list result", func(t *testing.T) {
		mgr := newMockManager()
		mgr.listResult["myapp@"] = []string{}

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:     "myapp@",
			Type:     "service",
			Enabled:  true,
			Discover: true,
		}

		d.discoverInstances(context.Background(), unit)

		assert.Empty(t, d.registeredUnits)
	})
}

func TestEventLoop(t *testing.T) {
	t.Run("exits on context cancellation", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan error, 1)
		go func() {
			done <- d.eventLoop(ctx)
		}()

		cancel()

		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("eventLoop did not exit on context cancellation")
		}
	})

	t.Run("forwards state changes to state manager", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.sm.Register("test.service")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- d.eventLoop(ctx)
		}()

		d.changeCh <- systemd.StateChange{
			UnitName:    "test.service",
			ActiveState: "active",
			SubState:    "running",
		}

		require.Eventually(t, func() bool {
			status := d.sm.GetStatus("test.service")
			return status != nil && status.ActiveState == "active" && status.SubState == "running"
		}, 2*time.Second, 10*time.Millisecond)

		cancel()

		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("eventLoop did not exit")
		}
	})

	t.Run("forwards health results to state manager", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.sm.Register("test.service")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- d.eventLoop(ctx)
		}()

		d.healthCh <- healthcheck.Result{
			UnitName: "test.service",
			Healthy:  true,
		}

		require.Eventually(t, func() bool {
			status := d.sm.GetStatus("test.service")
			return status != nil && status.Healthy != nil && *status.Healthy
		}, 2*time.Second, 10*time.Millisecond)

		cancel()

		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("eventLoop did not exit")
		}
	})

	t.Run("handles SIGTERM signal", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		d.cancel = cancel

		done := make(chan error, 1)
		go func() {
			done <- d.eventLoop(ctx)
		}()

		time.Sleep(50 * time.Millisecond)

		proc, err := os.FindProcess(os.Getpid())
		require.NoError(t, err)
		require.NoError(t, proc.Signal(syscall.SIGTERM))

		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("eventLoop did not exit on SIGTERM")
		}
	})

	t.Run("handles SIGHUP signal for reload", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `units:
  - name: test
    type: service
    enabled: true
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "test", Type: "service", Enabled: true},
			},
		}
		d := newTestDaemon(mgr, cfg)
		d.configPath = tmpFile.Name()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		d.cancel = cancel

		done := make(chan error, 1)
		go func() {
			done <- d.eventLoop(ctx)
		}()

		time.Sleep(50 * time.Millisecond)

		proc, err := os.FindProcess(os.Getpid())
		require.NoError(t, err)
		require.NoError(t, proc.Signal(syscall.SIGHUP))

		// Give time for SIGHUP to be processed (reload happens asynchronously).
		time.Sleep(200 * time.Millisecond)

		// Verify eventLoop is still running after SIGHUP (reload did not crash).
		select {
		case <-done:
			t.Fatal("eventLoop should still be running after SIGHUP")
		default:
		}

		cancel()

		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("eventLoop did not exit after cancel")
		}
	})
}

func TestNew(t *testing.T) {
	t.Run("returns daemon with correct configPath", func(t *testing.T) {
		d := New("/etc/test/config.yaml")

		assert.Equal(t, "/etc/test/config.yaml", d.configPath)
	})

	t.Run("initializes registeredUnits map", func(t *testing.T) {
		d := New("/etc/test/config.yaml")

		assert.NotNil(t, d.registeredUnits)
		assert.Empty(t, d.registeredUnits)
	})

	t.Run("other fields are zero values", func(t *testing.T) {
		d := New("/some/path.yaml")

		assert.Nil(t, d.cfg)
		assert.Nil(t, d.mgr)
		assert.Nil(t, d.sm)
		assert.Nil(t, d.notifier)
		assert.Nil(t, d.restarter)
		assert.Nil(t, d.sdNotifier)
		assert.Nil(t, d.cancel)
		assert.Nil(t, d.changeCh)
		assert.Nil(t, d.healthCh)
	})
}

func TestRun(t *testing.T) {
	t.Run("returns error for nonexistent config", func(t *testing.T) {
		d := New("/nonexistent/config.yaml")

		err := d.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading config")
	})

	t.Run("returns error for invalid config", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString("invalid: [yaml\n")
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		d := New(tmpFile.Name())

		err = d.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading config")
	})

	t.Run("returns error for invalid socket path", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `socket: /nonexistent/deeply/nested/path/test.sock
units:
  - name: test
    type: service
    enabled: false
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		d := New(tmpFile.Name())

		err = d.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "socket")
	})

	t.Run("returns error for config with no units", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString("units: []\n")
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		d := New(tmpFile.Name())

		err = d.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading config")
	})
}

func TestReload(t *testing.T) {
	t.Run("successful reload with valid config", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `units:
  - name: test
    type: service
    enabled: true
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "old", Type: "service", Enabled: true},
			},
		}
		d := newTestDaemon(mgr, cfg)
		d.configPath = tmpFile.Name()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.reload()

		assert.NotNil(t, d.cfg)
		require.Len(t, d.cfg.Units, 1)
		assert.Equal(t, "test", d.cfg.Units[0].Name)
	})

	t.Run("failed reload with invalid config", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString("invalid: [yaml: content\n")
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		originalCfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "original", Type: "service", Enabled: true},
			},
		}
		d := newTestDaemon(mgr, originalCfg)
		d.configPath = tmpFile.Name()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.reload()

		assert.Equal(t, "original", d.cfg.Units[0].Name)
	})

	t.Run("failed reload with nonexistent file", func(t *testing.T) {
		mgr := newMockManager()
		originalCfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "original", Type: "service", Enabled: true},
			},
		}
		d := newTestDaemon(mgr, originalCfg)
		d.configPath = "/nonexistent/path/config.yaml"
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.reload()

		assert.Equal(t, "original", d.cfg.Units[0].Name)
	})

	t.Run("failed reload with empty units", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString("units: []\n")
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		originalCfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "original", Type: "service", Enabled: true},
			},
		}
		d := newTestDaemon(mgr, originalCfg)
		d.configPath = tmpFile.Name()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.reload()

		assert.Equal(t, "original", d.cfg.Units[0].Name)
	})
}

func TestDiscoveryLoop(t *testing.T) {
	t.Run("discovers units periodically and exits on context cancel", func(t *testing.T) {
		mgr := newMockManager()
		mgr.listResult["myapp@"] = []string{
			"myapp@inst1.service",
			"myapp@inst2.service",
		}

		cfg := &config.Config{
			DiscoveryInterval: 50 * time.Millisecond,
			Units: []config.UnitConfig{
				{
					Name:     "myapp@",
					Type:     "service",
					Enabled:  true,
					Discover: true,
				},
			},
		}
		d := newTestDaemon(mgr, cfg)

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			defer close(done)
			d.discoveryLoop(ctx)
		}()

		require.Eventually(t, func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return len(d.registeredUnits) >= 2
		}, 2*time.Second, 10*time.Millisecond)

		cancel()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("discoveryLoop did not exit on context cancellation")
		}

		assert.Contains(t, d.registeredUnits, "myapp@inst1.service")
		assert.Contains(t, d.registeredUnits, "myapp@inst2.service")
	})

	t.Run("skips non-discoverable units", func(t *testing.T) {
		mgr := newMockManager()
		mgr.listResult["myapp@"] = []string{
			"myapp@inst1.service",
		}

		cfg := &config.Config{
			DiscoveryInterval: 50 * time.Millisecond,
			Units: []config.UnitConfig{
				{
					Name:     "regular",
					Type:     "service",
					Enabled:  true,
					Discover: false,
				},
				{
					Name:     "disabled@",
					Type:     "service",
					Enabled:  false,
					Discover: true,
				},
				{
					Name:     "nodiscover@",
					Type:     "service",
					Enabled:  true,
					Discover: false,
				},
			},
		}
		d := newTestDaemon(mgr, cfg)

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			defer close(done)
			d.discoveryLoop(ctx)
		}()

		time.Sleep(150 * time.Millisecond)

		cancel()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("discoveryLoop did not exit on context cancellation")
		}

		assert.Empty(t, d.registeredUnits)
	})
}
