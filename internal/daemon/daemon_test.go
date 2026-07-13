package daemon

import (
	"context"
	"fmt"
	"net"
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
	"github.com/vitalvas/systemd-supervisord/internal/socketactivation"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

type mockManager struct {
	mu                sync.Mutex
	startCalls        []string
	stopCalls         []string
	restartCalls      []string
	stateCalls        []string
	timerTriggerCalls []string
	listCalls         []string
	watchCalls        []string
	unitStates        map[string]*systemd.UnitState
	timerTriggers     map[string]time.Time
	listResult        map[string][]string
	startErr          error
	stopErr           error
	restartErr        error
	getStateErr       error
	timerTriggerErr   error
	listErr           error
	watchErr          error
	closeCalled       bool
}

func boolPtr(b bool) *bool { return &b }

func newMockManager() *mockManager {
	return &mockManager{
		unitStates:    make(map[string]*systemd.UnitState),
		timerTriggers: make(map[string]time.Time),
		listResult:    make(map[string][]string),
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
			ActiveState: systemd.ActiveStateInactive,
			SubState:    systemd.SubStateDead,
			LoadState:   "loaded",
		}, nil
	}

	return state, nil
}

func (m *mockManager) GetTimerLastTrigger(_ context.Context, unit string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.timerTriggerCalls = append(m.timerTriggerCalls, unit)

	if m.timerTriggerErr != nil {
		return time.Time{}, m.timerTriggerErr
	}

	return m.timerTriggers[unit], nil
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
		cfg:               cfg,
		mgr:               mgr,
		sm:                sm,
		notifier:          notif,
		restarter:         rest,
		changeCh:          make(chan systemd.StateChange, 100),
		healthCh:          make(chan healthcheck.Result, 100),
		registeredUnits:   make(map[string]struct{}),
		criticalUnits:     make(map[string]struct{}),
		unitCancels:       make(map[string]context.CancelFunc),
		socketMonitors:    make(map[string]context.CancelFunc),
		discoveryReloadCh: make(chan struct{}, 1),
		timerReloadCh:     make(chan struct{}, 1),
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

func TestBuildDependents(t *testing.T) {
	t.Run("bare dependency uses dependent type", func(t *testing.T) {
		units := []config.UnitConfig{
			{Name: "db", Type: "service"},
			{Name: "app", Type: "service", DependsOn: []string{"db"}},
		}

		deps := buildDependents(units)
		assert.Equal(t, []string{"app.service"}, deps["db.service"])
	})

	t.Run("qualified dependency uses explicit type", func(t *testing.T) {
		units := []config.UnitConfig{
			{Name: "backup", Type: "service"},
			{Name: "backup", Type: "timer"},
			{Name: "app", Type: "service", DependsOn: []string{"backup.timer"}},
		}

		deps := buildDependents(units)
		assert.Equal(t, []string{"app.service"}, deps["backup.timer"])
		assert.Empty(t, deps["backup.service"])
	})

	t.Run("mixed bare and qualified", func(t *testing.T) {
		units := []config.UnitConfig{
			{Name: "db", Type: "service"},
			{Name: "cache", Type: "service"},
			{Name: "app", Type: "service", DependsOn: []string{"db", "cache.service"}},
		}

		deps := buildDependents(units)
		assert.Equal(t, []string{"app.service"}, deps["db.service"])
		assert.Equal(t, []string{"app.service"}, deps["cache.service"])
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

	t.Run("enabled template", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{Name: "myapp@", Type: "service"},
				},
			},
		}

		assert.True(t, d.hasDiscoverableUnits())
	})

	t.Run("disabled template", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{Name: "myapp@", Type: "service", Enabled: boolPtr(false)},
				},
			},
		}

		assert.False(t, d.hasDiscoverableUnits())
	})

	t.Run("non-template unit", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{Name: "myapp", Type: "service"},
				},
			},
		}

		assert.False(t, d.hasDiscoverableUnits())
	})

	t.Run("mixed units with one template", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{Name: "regular", Type: "service"},
					{Name: "template@", Type: "service"},
				},
			},
		}

		assert.True(t, d.hasDiscoverableUnits())
	})
}

func TestCriticalUnits(t *testing.T) {
	t.Run("returns empty when no critical units", func(t *testing.T) {
		d := newTestDaemon(newMockManager(), &config.Config{})

		assert.Empty(t, d.CriticalUnits())
	})

	t.Run("returns registered critical units", func(t *testing.T) {
		d := newTestDaemon(newMockManager(), &config.Config{})

		d.mu.Lock()
		d.criticalUnits["app.service"] = struct{}{}
		d.criticalUnits["db.service"] = struct{}{}
		d.mu.Unlock()

		assert.ElementsMatch(t, []string{"app.service", "db.service"}, d.CriticalUnits())
	})
}

func TestRegisterUnit(t *testing.T) {
	t.Run("registers new unit with state fetch", func(t *testing.T) {
		mgr := newMockManager()
		mgr.unitStates["myapp.service"] = &systemd.UnitState{
			Name:        "myapp.service",
			ActiveState: systemd.ActiveStateActive,
			SubState:    systemd.SubStateRunning,
			LoadState:   "loaded",
		}

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:    "myapp",
			Type:    "service",
			Enabled: boolPtr(true),
		}

		d.registerUnit(context.Background(), "myapp.service", unit)

		assert.Contains(t, d.registeredUnits, "myapp.service")
		assert.Contains(t, mgr.stateCalls, "myapp.service")
		assert.Contains(t, mgr.watchCalls, "myapp.service")

		status := d.sm.GetStatus("myapp.service")
		require.NotNil(t, status)
		assert.Equal(t, systemd.ActiveStateActive, status.ActiveState)
		assert.Equal(t, systemd.SubStateRunning, status.SubState)
	})

	t.Run("idempotent registration", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:    "myapp",
			Type:    "service",
			Enabled: boolPtr(true),
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
			Enabled: boolPtr(true),
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
			Enabled: boolPtr(true),
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
			Enabled: boolPtr(true),
		}

		d.registerUnit(context.Background(), "myapp.service", unit)

		assert.Contains(t, d.registeredUnits, "myapp.service")
	})

	t.Run("registers template instance with resolved health checks", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		unit := &config.UnitConfig{
			Name:    "myapp@",
			Type:    "service",
			Enabled: boolPtr(true),

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
			Enabled:     boolPtr(true),
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
			Enabled:     boolPtr(true),
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
			Enabled:     boolPtr(true),
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
			Name:    "myapp@",
			Type:    "service",
			Enabled: boolPtr(true),
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
			Name:    "myapp@",
			Type:    "service",
			Enabled: boolPtr(true),
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
			Name:    "myapp@",
			Type:    "service",
			Enabled: boolPtr(true),
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
			Name:    "myapp@",
			Type:    "service",
			Enabled: boolPtr(true),
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
			Name:    "myapp@",
			Type:    "service",
			Enabled: boolPtr(true),
		}

		d.discoverInstances(context.Background(), unit)

		assert.Empty(t, d.registeredUnits)
	})

	t.Run("filters by instance pattern", func(t *testing.T) {
		mgr := newMockManager()
		mgr.listResult["runtime@"] = []string{
			"runtime@app-web1.service",
			"runtime@app-api2.service",
			"runtime@db-main.service",
		}

		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString(`
units:
  "runtime@{app-[a-z]+[0-9]+}.service": {}
`)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		cfg, err := config.Load(tmpFile.Name())
		require.NoError(t, err)

		d := newTestDaemon(mgr, cfg)

		d.discoverInstances(context.Background(), &cfg.Units[0])

		assert.Contains(t, d.registeredUnits, "runtime@app-web1.service")
		assert.Contains(t, d.registeredUnits, "runtime@app-api2.service")
		assert.NotContains(t, d.registeredUnits, "runtime@db-main.service")
		assert.Len(t, d.registeredUnits, 2)
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
			ActiveState: systemd.ActiveStateActive,
			SubState:    systemd.SubStateRunning,
		}

		require.Eventually(t, func() bool {
			status := d.sm.GetStatus("test.service")
			return status != nil && status.ActiveState == systemd.ActiveStateActive && status.SubState == systemd.SubStateRunning
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
  test.service:
    enabled: true
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "test", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, cfg)
		d.configPath = tmpFile.Name()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		d.ctx = ctx
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
		d := New("/etc/test/config.yaml", false)

		assert.Equal(t, "/etc/test/config.yaml", d.configPath)
	})

	t.Run("initializes registeredUnits map", func(t *testing.T) {
		d := New("/etc/test/config.yaml", false)

		assert.NotNil(t, d.registeredUnits)
		assert.Empty(t, d.registeredUnits)
	})

	t.Run("other fields are zero values", func(t *testing.T) {
		d := New("/some/path.yaml", false)

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

	t.Run("sets dry-run flag", func(t *testing.T) {
		d := New("/etc/test/config.yaml", true)

		assert.True(t, d.dryRun)
	})
}

func TestRun(t *testing.T) {
	t.Run("returns error for nonexistent config", func(t *testing.T) {
		d := New("/nonexistent/config.yaml", false)

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

		d := New(tmpFile.Name(), false)

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
  test.service:
    enabled: false
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		d := New(tmpFile.Name(), false)

		err = d.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "socket")
	})

	t.Run("returns error for config with no units", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString("units: {}\n")
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		d := New(tmpFile.Name(), false)

		err = d.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading config")
	})
}

func TestSocketActivationWiring(t *testing.T) {
	// Backend echo server the activator proxies to.
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backend.Close()

	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}

			go func(c net.Conn) {
				defer c.Close()

				buf := make([]byte, 64)
				n, rerr := c.Read(buf)
				if rerr == nil {
					c.Write(buf[:n])
				}
			}(conn)
		}
	}()

	listenLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listenAddr := listenLn.Addr().String()
	listenLn.Close()

	mgr := newMockManager()
	cfg := &config.Config{
		Units: []config.UnitConfig{{Name: "placeholder", Type: "service", Enabled: boolPtr(false)}},
		SocketActivation: []config.SocketActivationConfig{{
			Name:           "test",
			Listen:         listenAddr,
			Unit:           "backend.service",
			Backend:        backend.Addr().String(),
			StartupTimeout: 2 * time.Second,
			IdleTimeout:    10 * time.Second,
			HealthChecks: []config.HealthCheck{{
				Type:     "tcp",
				Interval: 10 * time.Millisecond,
				Timeout:  time.Second,
				Retries:  1,
				TCP:      &config.TCPHealthCheck{Address: backend.Addr().String()},
			}},
		}},
	}

	d := newTestDaemon(mgr, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.socketMgr = socketactivation.NewManager(cfg.SocketActivation, d.mgr, d)
	require.NoError(t, d.socketMgr.Start(ctx))

	conn, err := net.Dial("tcp", listenAddr)
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)

	buf := make([]byte, 4)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buf[:n]))

	mgr.mu.Lock()
	starts := append([]string(nil), mgr.startCalls...)
	mgr.mu.Unlock()
	assert.Contains(t, starts, "backend.service")
}

func TestSocketActivationHealthSupervisor(t *testing.T) {
	checks := []config.HealthCheck{{
		Type:     "tcp",
		Interval: 10 * time.Millisecond,
		Timeout:  time.Second,
		Retries:  1,
		TCP:      &config.TCPHealthCheck{Address: "127.0.0.1:1"},
	}}

	t.Run("restarts unhealthy backend and cleans up on unwatch", func(t *testing.T) {
		mgr := newMockManager()
		d := newTestDaemon(mgr, &config.Config{})
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- d.eventLoop(ctx)
		}()

		policy := config.RestartPolicy{Enabled: true, Backoff: 0, Cooldown: time.Minute}
		d.Watch(ctx, "backend.service", checks, policy)

		// The unit is registered and its failing check drives a restart.
		require.Eventually(t, func() bool {
			return d.sm.GetStatus("backend.service") != nil
		}, 2*time.Second, 10*time.Millisecond)

		require.Eventually(t, func() bool {
			mgr.mu.Lock()
			defer mgr.mu.Unlock()

			return len(mgr.restartCalls) > 0 && mgr.restartCalls[0] == "backend.service"
		}, 3*time.Second, 10*time.Millisecond)

		d.Unwatch("backend.service")

		assert.Nil(t, d.sm.GetStatus("backend.service"))

		d.mu.Lock()
		_, monitored := d.socketMonitors["backend.service"]
		d.mu.Unlock()
		assert.False(t, monitored)

		cancel()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("eventLoop did not exit")
		}
	})

	t.Run("watch is a no-op without checks", func(t *testing.T) {
		mgr := newMockManager()
		d := newTestDaemon(mgr, &config.Config{})

		d.Watch(context.Background(), "backend.service", nil, config.RestartPolicy{Enabled: true})

		assert.Nil(t, d.sm.GetStatus("backend.service"))

		d.mu.Lock()
		_, monitored := d.socketMonitors["backend.service"]
		d.mu.Unlock()
		assert.False(t, monitored)
	})

	t.Run("unwatch without watch is safe", func(t *testing.T) {
		mgr := newMockManager()
		d := newTestDaemon(mgr, &config.Config{})

		assert.NotPanics(t, func() {
			d.Unwatch("backend.service")
		})
	})
}

func TestReload(t *testing.T) {
	t.Run("successful reload with valid config", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `units:
  test.service:
    enabled: true
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "old", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, cfg)
		d.configPath = tmpFile.Name()
		d.ctx = context.Background()
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
				{Name: "original", Type: "service"},
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
				{Name: "original", Type: "service"},
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

		_, err = tmpFile.WriteString("units: {}\n")
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		originalCfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "original", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, originalCfg)
		d.configPath = tmpFile.Name()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.reload()

		assert.Equal(t, "original", d.cfg.Units[0].Name)
	})

	t.Run("reload registers new units", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `units:
  old.service: {}
  new.service: {}
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "old", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, cfg)
		d.configPath = tmpFile.Name()
		d.ctx = context.Background()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.registerUnit(context.Background(), "old.service", &cfg.Units[0])

		d.reload()

		d.mu.Lock()
		_, hasOld := d.registeredUnits["old.service"]
		_, hasNew := d.registeredUnits["new.service"]
		d.mu.Unlock()

		assert.True(t, hasOld)
		assert.True(t, hasNew)
	})

	t.Run("reload unregisters removed units", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `units:
  kept.service: {}
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "kept", Type: "service"},
				{Name: "removed", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, cfg)
		d.configPath = tmpFile.Name()
		d.ctx = context.Background()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.registerUnit(context.Background(), "kept.service", &cfg.Units[0])
		d.registerUnit(context.Background(), "removed.service", &cfg.Units[1])

		d.reload()

		d.mu.Lock()
		_, hasKept := d.registeredUnits["kept.service"]
		_, hasRemoved := d.registeredUnits["removed.service"]
		d.mu.Unlock()

		assert.True(t, hasKept)
		assert.False(t, hasRemoved)
	})

	t.Run("reload preserves template instances", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `units:
  "worker@.service": {}
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		mgr.listResult = map[string][]string{
			"worker@": {"worker@a.service", "worker@b.service"},
		}

		oldCfg, loadErr := config.Load(tmpFile.Name())
		require.NoError(t, loadErr)

		d := newTestDaemon(mgr, oldCfg)
		d.configPath = tmpFile.Name()
		d.ctx = context.Background()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.registerUnit(context.Background(), "worker@a.service", &oldCfg.Units[0])
		d.registerUnit(context.Background(), "worker@b.service", &oldCfg.Units[0])

		d.reload()

		d.mu.Lock()
		_, hasA := d.registeredUnits["worker@a.service"]
		_, hasB := d.registeredUnits["worker@b.service"]
		d.mu.Unlock()

		assert.True(t, hasA)
		assert.True(t, hasB)
	})

	t.Run("reload starts discovery loop when template added", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `units:
  "worker@.service": {}
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		mgr.listResult = map[string][]string{
			"worker@": {"worker@a.service"},
		}

		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "plain", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, cfg)
		d.configPath = tmpFile.Name()
		d.ctx = context.Background()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		assert.False(t, d.discoveryRunning)

		d.reload()

		assert.True(t, d.discoveryRunning)
	})

	t.Run("reload starts timer loop when timer with max_delay added", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `units:
  backup.timer:
    max_delay: 1h
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()

		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "plain", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, cfg)
		d.configPath = tmpFile.Name()
		d.ctx = context.Background()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		assert.False(t, d.timerRunning)

		d.reload()

		assert.True(t, d.timerRunning)
	})

	t.Run("reload re-registers existing units to update config", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		configData := `units:
  app.service: {}
`
		_, err = tmpFile.WriteString(configData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		mgr := newMockManager()
		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "app", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, cfg)
		d.configPath = tmpFile.Name()
		d.ctx = context.Background()
		d.sdNotifier = systemd.NewNotifier(func() string { return "test" })

		d.registerUnit(context.Background(), "app.service", &cfg.Units[0])

		d.reload()

		d.mu.Lock()
		_, hasApp := d.registeredUnits["app.service"]
		d.mu.Unlock()

		assert.True(t, hasApp)
	})
}

func TestFindMatchingTemplate(t *testing.T) {
	t.Run("matches bare template", func(t *testing.T) {
		cfgData := `units:
  "worker@.service": {}
`
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString(cfgData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		cfg, loadErr := config.Load(tmpFile.Name())
		require.NoError(t, loadErr)

		mgr := newMockManager()
		d := newTestDaemon(mgr, cfg)

		templates := []*config.UnitConfig{&cfg.Units[0]}

		assert.NotNil(t, d.findMatchingTemplate("worker@a.service", templates))
		assert.Nil(t, d.findMatchingTemplate("other@a.service", templates))
	})

	t.Run("matches pattern template", func(t *testing.T) {
		cfgData := `units:
  "app@{web-[0-9]+}.service": {}
`
		tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString(cfgData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		cfg, loadErr := config.Load(tmpFile.Name())
		require.NoError(t, loadErr)

		mgr := newMockManager()
		d := newTestDaemon(mgr, cfg)

		templates := []*config.UnitConfig{&cfg.Units[0]}

		assert.NotNil(t, d.findMatchingTemplate("app@web-1.service", templates))
		assert.Nil(t, d.findMatchingTemplate("app@db-1.service", templates))
	})
}

func TestUnregisterUnit(t *testing.T) {
	t.Run("unregisters existing unit", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "app", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, cfg)

		d.registerUnit(context.Background(), "app.service", &cfg.Units[0])

		d.mu.Lock()
		_, exists := d.registeredUnits["app.service"]
		d.mu.Unlock()
		assert.True(t, exists)

		d.unregisterUnit("app.service")

		d.mu.Lock()
		_, exists = d.registeredUnits["app.service"]
		d.mu.Unlock()
		assert.False(t, exists)

		assert.Nil(t, d.sm.GetStatus("app.service"))
	})

	t.Run("no-op for unknown unit", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		d.unregisterUnit("unknown.service")

		d.mu.Lock()
		assert.Empty(t, d.registeredUnits)
		d.mu.Unlock()
	})
}

func TestHasTimerMonitoring(t *testing.T) {
	t.Run("no timer units", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{Name: "app", Type: "service"},
				},
			},
		}

		assert.False(t, d.hasTimerMonitoring())
	})

	t.Run("timer without max_delay", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{Name: "backup", Type: "timer"},
				},
			},
		}

		assert.False(t, d.hasTimerMonitoring())
	})

	t.Run("timer with max_delay", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{Name: "backup", Type: "timer", Enabled: boolPtr(true), MaxDelay: 24 * time.Hour},
				},
			},
		}

		assert.True(t, d.hasTimerMonitoring())
	})

	t.Run("disabled timer with max_delay", func(t *testing.T) {
		d := &Daemon{
			cfg: &config.Config{
				Units: []config.UnitConfig{
					{Name: "backup", Type: "timer", Enabled: boolPtr(false), MaxDelay: 24 * time.Hour},
				},
			},
		}

		assert.False(t, d.hasTimerMonitoring())
	})
}

func TestCheckTimers(t *testing.T) {
	t.Run("restarts overdue timer", func(t *testing.T) {
		mgr := newMockManager()
		mgr.timerTriggers["backup.timer"] = time.Now().Add(-25 * time.Hour)

		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "backup", Type: "timer", Enabled: boolPtr(true), MaxDelay: 24 * time.Hour},
			},
		}
		d := newTestDaemon(mgr, cfg)

		d.checkTimers(context.Background())

		mgr.mu.Lock()
		defer mgr.mu.Unlock()

		assert.Contains(t, mgr.restartCalls, "backup.timer")
	})

	t.Run("does not restart recent timer", func(t *testing.T) {
		mgr := newMockManager()
		mgr.timerTriggers["backup.timer"] = time.Now().Add(-1 * time.Hour)

		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "backup", Type: "timer", Enabled: boolPtr(true), MaxDelay: 24 * time.Hour},
			},
		}
		d := newTestDaemon(mgr, cfg)

		d.checkTimers(context.Background())

		mgr.mu.Lock()
		defer mgr.mu.Unlock()

		assert.Empty(t, mgr.restartCalls)
	})

	t.Run("skips timer with zero last trigger", func(t *testing.T) {
		mgr := newMockManager()

		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "backup", Type: "timer", Enabled: boolPtr(true), MaxDelay: 24 * time.Hour},
			},
		}
		d := newTestDaemon(mgr, cfg)

		d.checkTimers(context.Background())

		mgr.mu.Lock()
		defer mgr.mu.Unlock()

		assert.Empty(t, mgr.restartCalls)
	})

	t.Run("skips disabled units", func(t *testing.T) {
		mgr := newMockManager()
		mgr.timerTriggers["backup.timer"] = time.Now().Add(-25 * time.Hour)

		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "backup", Type: "timer", Enabled: boolPtr(false), MaxDelay: 24 * time.Hour},
			},
		}
		d := newTestDaemon(mgr, cfg)

		d.checkTimers(context.Background())

		mgr.mu.Lock()
		defer mgr.mu.Unlock()

		assert.Empty(t, mgr.restartCalls)
		assert.Empty(t, mgr.timerTriggerCalls)
	})

	t.Run("skips service units", func(t *testing.T) {
		mgr := newMockManager()

		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "app", Type: "service"},
			},
		}
		d := newTestDaemon(mgr, cfg)

		d.checkTimers(context.Background())

		mgr.mu.Lock()
		defer mgr.mu.Unlock()

		assert.Empty(t, mgr.timerTriggerCalls)
	})

	t.Run("handles get trigger error gracefully", func(t *testing.T) {
		mgr := newMockManager()
		mgr.timerTriggerErr = fmt.Errorf("dbus error")

		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "backup", Type: "timer", Enabled: boolPtr(true), MaxDelay: 24 * time.Hour},
			},
		}
		d := newTestDaemon(mgr, cfg)

		d.checkTimers(context.Background())

		mgr.mu.Lock()
		defer mgr.mu.Unlock()

		assert.Empty(t, mgr.restartCalls)
	})

	t.Run("handles restart error gracefully", func(t *testing.T) {
		mgr := newMockManager()
		mgr.timerTriggers["backup.timer"] = time.Now().Add(-25 * time.Hour)
		mgr.restartErr = fmt.Errorf("restart failed")

		cfg := &config.Config{
			Units: []config.UnitConfig{
				{Name: "backup", Type: "timer", Enabled: boolPtr(true), MaxDelay: 24 * time.Hour},
			},
		}
		d := newTestDaemon(mgr, cfg)

		d.checkTimers(context.Background())

		mgr.mu.Lock()
		defer mgr.mu.Unlock()

		assert.Contains(t, mgr.restartCalls, "backup.timer")
	})
}

func TestTimerMonitorLoop(t *testing.T) {
	t.Run("checks timers periodically and exits on cancel", func(t *testing.T) {
		mgr := newMockManager()
		mgr.timerTriggers["backup.timer"] = time.Now().Add(-25 * time.Hour)

		cfg := &config.Config{
			DiscoveryInterval: 50 * time.Millisecond,
			Units: []config.UnitConfig{
				{Name: "backup", Type: "timer", Enabled: boolPtr(true), MaxDelay: 24 * time.Hour},
			},
		}
		d := newTestDaemon(mgr, cfg)

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			defer close(done)
			d.timerMonitorLoop(ctx)
		}()

		require.Eventually(t, func() bool {
			mgr.mu.Lock()
			defer mgr.mu.Unlock()
			return len(mgr.restartCalls) >= 1
		}, 2*time.Second, 10*time.Millisecond)

		cancel()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timerMonitorLoop did not exit on context cancellation")
		}
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
					Name:    "myapp@",
					Type:    "service",
					Enabled: boolPtr(true),
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

	t.Run("skips non-template and disabled units", func(t *testing.T) {
		mgr := newMockManager()

		cfg := &config.Config{
			DiscoveryInterval: 50 * time.Millisecond,
			Units: []config.UnitConfig{
				{Name: "regular", Type: "service"},
				{Name: "disabled@", Type: "service", Enabled: boolPtr(false)},
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
