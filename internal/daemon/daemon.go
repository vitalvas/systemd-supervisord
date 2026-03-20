package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/healthcheck"
	"github.com/vitalvas/systemd-supervisord/internal/notify"
	"github.com/vitalvas/systemd-supervisord/internal/restarter"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

type Daemon struct {
	configPath string
	cfg        *config.Config
	mgr        systemd.Manager
	sm         *statemanager.StateManager
	notifier   *notify.Notifier
	restarter  *restarter.Restarter
	sdNotifier *systemd.Notifier
	cancel     context.CancelFunc

	changeCh chan systemd.StateChange
	healthCh chan healthcheck.Result

	registeredUnits map[string]struct{}
	mu              sync.Mutex
}

func New(configPath string) *Daemon {
	return &Daemon{
		configPath:      configPath,
		registeredUnits: make(map[string]struct{}),
	}
}

func (d *Daemon) Run(ctx context.Context) error {
	cfg, err := config.Load(d.configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	d.cfg = cfg

	setupLogging(cfg.LogLevel)

	ctx, d.cancel = context.WithCancel(ctx)
	defer d.cancel()

	d.mgr = systemd.NewManager(ctx)
	defer d.mgr.Close()

	d.sm = statemanager.New(100)
	d.notifier = notify.New(cfg.Notify)
	d.restarter = restarter.New(d.mgr, d.sm)

	d.sm.OnEvent(d.notifier.HandleEvent)
	d.sm.OnEvent(d.restarter.HandleEvent)

	dependents := make(map[string][]string)
	for _, u := range cfg.Units {
		for _, dep := range u.DependsOn {
			depUnit := fmt.Sprintf("%s.%s", dep, u.Type)
			dependents[depUnit] = append(dependents[depUnit], u.UnitName())
		}
	}

	d.restarter.SetDependents(dependents)

	d.changeCh = make(chan systemd.StateChange, 100)
	d.healthCh = make(chan healthcheck.Result, 100)

	for i := range cfg.Units {
		unit := &cfg.Units[i]
		if !unit.IsEnabled() {
			continue
		}

		if unit.IsTemplate() {
			d.discoverInstances(ctx, unit)
		} else {
			d.registerUnit(ctx, unit.UnitName(), unit)
		}
	}

	if d.hasDiscoverableUnits() {
		go d.discoveryLoop(ctx)
	}

	if d.hasTimerMonitoring() {
		go d.timerMonitorLoop(ctx)
	}

	if err := d.ListenSocket(ctx); err != nil {
		return fmt.Errorf("starting socket listener: %w", err)
	}

	d.sdNotifier = systemd.NewNotifier(func() string {
		total, healthy, unhealthy := d.sm.Summary()

		return fmt.Sprintf("watching %d units: %d healthy, %d unhealthy", total, healthy, unhealthy)
	})

	d.sdNotifier.Ready()
	d.sdNotifier.StartWatchdog()
	defer d.sdNotifier.StopWatchdog()

	slog.Info("daemon started", "registered_units", len(d.registeredUnits))

	return d.eventLoop(ctx)
}

func (d *Daemon) registerUnit(ctx context.Context, unitName string, unit *config.UnitConfig) {
	d.mu.Lock()
	if _, exists := d.registeredUnits[unitName]; exists {
		d.mu.Unlock()

		return
	}

	d.registeredUnits[unitName] = struct{}{}
	d.mu.Unlock()

	d.sm.Register(unitName)

	state, err := d.mgr.GetUnitState(ctx, unitName)
	if err != nil {
		slog.Error("getting initial unit state", "unit", unitName, "error", err)
	} else {
		d.sm.UpdateState(unitName, state.ActiveState, state.SubState)
	}

	if err := d.mgr.WatchUnit(ctx, unitName, d.changeCh); err != nil {
		slog.Error("watching unit", "unit", unitName, "error", err)
	}

	var checks []config.HealthCheck

	if unit.IsTemplate() {
		instance := extractInstance(unitName, unit.TemplatePrefix(), unit.Type)
		checks = unit.ResolveHealthChecks(instance)
	} else {
		checks = unit.HealthChecks
	}

	if len(checks) > 0 {
		checker := healthcheck.New(unitName, checks, d.healthCh)

		if unit.GracePeriod > 0 {
			go func() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(unit.GracePeriod):
					checker.Run(ctx)
				}
			}()
		} else {
			go checker.Run(ctx)
		}
	}

	if unit.Restart != nil {
		d.restarter.Register(unitName, *unit.Restart)
	}

	slog.Info("registered unit", "unit", unitName)
}

func (d *Daemon) discoverInstances(ctx context.Context, unit *config.UnitConfig) {
	prefix := unit.TemplatePrefix()
	suffix := fmt.Sprintf(".%s", unit.Type)

	units, err := d.mgr.ListUnits(ctx, prefix)
	if err != nil {
		slog.Error("discovering instances", "prefix", prefix, "error", err)

		return
	}

	for _, name := range units {
		if !strings.HasSuffix(name, suffix) {
			continue
		}

		instance := extractInstance(name, prefix, unit.Type)
		if !unit.MatchInstance(instance) {
			continue
		}

		d.registerUnit(ctx, name, unit)
	}
}

func (d *Daemon) hasDiscoverableUnits() bool {
	for i := range d.cfg.Units {
		if d.cfg.Units[i].IsEnabled() && d.cfg.Units[i].IsTemplate() {
			return true
		}
	}

	return false
}

func (d *Daemon) discoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.DiscoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := range d.cfg.Units {
				unit := &d.cfg.Units[i]
				if !unit.IsEnabled() || !unit.IsTemplate() {
					continue
				}

				d.discoverInstances(ctx, unit)
			}
		}
	}
}

func (d *Daemon) hasTimerMonitoring() bool {
	for i := range d.cfg.Units {
		if d.cfg.Units[i].IsEnabled() && d.cfg.Units[i].Type == "timer" && d.cfg.Units[i].MaxDelay > 0 {
			return true
		}
	}

	return false
}

func (d *Daemon) timerMonitorLoop(ctx context.Context) {
	interval := d.cfg.DiscoveryInterval
	if interval == 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkTimers(ctx)
		}
	}
}

func (d *Daemon) checkTimers(ctx context.Context) {
	for i := range d.cfg.Units {
		unit := &d.cfg.Units[i]
		if !unit.IsEnabled() || unit.Type != "timer" || unit.MaxDelay == 0 {
			continue
		}

		unitName := unit.UnitName()

		lastTrigger, err := d.mgr.GetTimerLastTrigger(ctx, unitName)
		if err != nil {
			slog.Error("getting timer last trigger", "unit", unitName, "error", err)

			continue
		}

		if lastTrigger.IsZero() {
			continue
		}

		if time.Since(lastTrigger) > unit.MaxDelay {
			slog.Warn("timer overdue, restarting", "unit", unitName, "last_trigger", lastTrigger, "max_delay", unit.MaxDelay)

			if err := d.mgr.Restart(ctx, unitName); err != nil {
				slog.Error("restarting overdue timer", "unit", unitName, "error", err)
			}
		}
	}
}

func (d *Daemon) eventLoop(ctx context.Context) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-ctx.Done():
			d.sdNotifier.Stopping()

			slog.Info("daemon stopping")

			return nil

		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				d.reload()
			case syscall.SIGTERM, syscall.SIGINT:
				d.sdNotifier.Stopping()

				slog.Info("received shutdown signal", "signal", sig)

				d.cancel()
			}

		case change := <-d.changeCh:
			d.sm.UpdateState(change.UnitName, change.ActiveState, change.SubState)

		case result := <-d.healthCh:
			d.sm.UpdateHealth(result.UnitName, result.Healthy)
		}
	}
}

func (d *Daemon) reload() {
	slog.Info("reloading configuration")

	d.sdNotifier.Reloading()

	cfg, err := config.Load(d.configPath)
	if err != nil {
		slog.Error("reloading config failed", "error", err)
		d.sdNotifier.Status(fmt.Sprintf("reload failed: %s", err.Error()))
		d.sdNotifier.Ready()

		return
	}

	d.cfg = cfg

	setupLogging(cfg.LogLevel)

	d.sdNotifier.Ready()

	slog.Info("configuration reloaded")
}

func setupLogging(level string) {
	var logLevel slog.Level

	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})

	slog.SetDefault(slog.New(handler))
}

func extractInstance(unitName, prefix, unitType string) string {
	name := strings.TrimPrefix(unitName, prefix)
	name = strings.TrimSuffix(name, fmt.Sprintf(".%s", unitType))

	return name
}
