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
	"github.com/vitalvas/systemd-supervisord/internal/httphealth"
	"github.com/vitalvas/systemd-supervisord/internal/notify"
	"github.com/vitalvas/systemd-supervisord/internal/restarter"
	"github.com/vitalvas/systemd-supervisord/internal/socketactivation"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

type Daemon struct {
	configPath string
	dryRun     bool
	cfg        *config.Config
	ctx        context.Context
	mgr        systemd.Manager
	sm         *statemanager.StateManager
	notifier   *notify.Notifier
	restarter  *restarter.Restarter
	sdNotifier *systemd.Notifier
	httpServer *httphealth.Server
	socketMgr  *socketactivation.Manager
	cancel     context.CancelFunc

	changeCh chan systemd.StateChange
	healthCh chan healthcheck.Result

	registeredUnits   map[string]struct{}
	criticalUnits     map[string]struct{}
	unitCancels       map[string]context.CancelFunc
	socketMonitors    map[string]context.CancelFunc
	discoveryReloadCh chan struct{}
	timerReloadCh     chan struct{}
	discoveryRunning  bool
	timerRunning      bool
	mu                sync.Mutex
}

func New(configPath string, dryRun bool) *Daemon {
	return &Daemon{
		configPath:        configPath,
		dryRun:            dryRun,
		registeredUnits:   make(map[string]struct{}),
		criticalUnits:     make(map[string]struct{}),
		unitCancels:       make(map[string]context.CancelFunc),
		socketMonitors:    make(map[string]context.CancelFunc),
		discoveryReloadCh: make(chan struct{}, 1),
		timerReloadCh:     make(chan struct{}, 1),
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
	d.ctx = ctx
	defer d.cancel()

	mgr := systemd.NewManager(ctx)
	if d.dryRun {
		d.mgr = systemd.NewDryRunManager(mgr)
		slog.Warn("running in dry-run mode, no actions will be performed")
	} else {
		d.mgr = mgr
	}
	defer d.mgr.Close()

	d.sm = statemanager.New(100)
	d.notifier = notify.New(cfg.Notify)
	d.restarter = restarter.New(d.mgr, d.sm)

	d.sm.OnEvent(d.notifier.HandleEvent)
	d.sm.OnEvent(d.restarter.HandleEvent)

	d.restarter.SetDependents(buildDependents(cfg.Units))

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
		d.discoveryRunning = true
		go d.discoveryLoop(ctx)
	}

	if d.hasTimerMonitoring() {
		d.timerRunning = true
		go d.timerMonitorLoop(ctx)
	}

	if err := d.ListenSocket(ctx); err != nil {
		return fmt.Errorf("starting socket listener: %w", err)
	}

	if d.cfg.HTTP.Enabled() {
		d.httpServer = httphealth.New(d.cfg.HTTP, d.sm, d)
		if err := d.httpServer.Start(ctx); err != nil {
			return fmt.Errorf("starting http health server: %w", err)
		}
	}

	if len(d.cfg.SocketActivation) > 0 {
		d.socketMgr = socketactivation.NewManager(d.cfg.SocketActivation, d.mgr, d)
		if err := d.socketMgr.Start(ctx); err != nil {
			return fmt.Errorf("starting socket activation: %w", err)
		}
	}

	d.sdNotifier = systemd.NewNotifier(func() string {
		total, healthy, unhealthy := d.sm.Summary()

		return fmt.Sprintf("watching %d units: %d healthy, %d unhealthy", total, healthy, unhealthy)
	})

	d.sdNotifier.Ready()
	d.sdNotifier.StartWatchdog()
	defer d.sdNotifier.StopWatchdog()

	if d.httpServer != nil {
		d.httpServer.MarkReady()
	}

	slog.Info("daemon started", "registered_units", len(d.registeredUnits))

	return d.eventLoop(ctx)
}

func (d *Daemon) CriticalUnits() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	names := make([]string, 0, len(d.criticalUnits))
	for name := range d.criticalUnits {
		names = append(names, name)
	}

	return names
}

func (d *Daemon) registerUnit(ctx context.Context, unitName string, unit *config.UnitConfig) {
	d.mu.Lock()
	if _, exists := d.registeredUnits[unitName]; exists {
		d.mu.Unlock()

		return
	}

	unitCtx, unitCancel := context.WithCancel(ctx)
	d.registeredUnits[unitName] = struct{}{}
	d.unitCancels[unitName] = unitCancel

	if unit.Critical {
		d.criticalUnits[unitName] = struct{}{}
	}
	d.mu.Unlock()

	d.sm.Register(unitName)

	if unit.Restart != nil {
		d.restarter.Register(unitName, *unit.Restart)
	}

	state, err := d.mgr.GetUnitState(unitCtx, unitName)
	if err != nil {
		slog.Error("getting initial unit state", "unit", unitName, "error", err)
	} else {
		d.sm.UpdateState(unitName, state.ActiveState, state.SubState)
	}

	if err := d.mgr.WatchUnit(unitCtx, unitName, d.changeCh); err != nil {
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
				case <-unitCtx.Done():
					return
				case <-time.After(unit.GracePeriod):
					checker.Run(unitCtx)
				}
			}()
		} else {
			go checker.Run(unitCtx)
		}
	}

	slog.Info("registered unit", "unit", unitName)
}

func (d *Daemon) unregisterUnit(unitName string) {
	d.mu.Lock()

	cancel, ok := d.unitCancels[unitName]
	if !ok {
		d.mu.Unlock()

		return
	}

	delete(d.registeredUnits, unitName)
	delete(d.unitCancels, unitName)
	delete(d.criticalUnits, unitName)
	d.mu.Unlock()

	cancel()

	d.restarter.Unregister(unitName)
	d.sm.Unregister(unitName)

	slog.Info("unregistered unit", "unit", unitName)
}

// Watch plugs a running socket-activation backend into the shared health-check
// and restart pipeline. It registers the unit with the state manager and
// restarter and runs a health checker feeding the same result channel used by
// regular units, so an unhealthy backend is restarted with the configured
// policy. Monitoring lasts until ctx is cancelled or Unwatch is called. It
// satisfies socketactivation.HealthSupervisor.
func (d *Daemon) Watch(ctx context.Context, unit string, checks []config.HealthCheck, policy config.RestartPolicy) {
	if len(checks) == 0 {
		return
	}

	monitorCtx, cancel := context.WithCancel(ctx)

	d.mu.Lock()
	if existing, ok := d.socketMonitors[unit]; ok {
		existing()
	}
	d.socketMonitors[unit] = cancel
	d.mu.Unlock()

	d.sm.Register(unit)
	d.restarter.Register(unit, policy)

	checker := healthcheck.New(unit, checks, d.healthCh)
	go checker.Run(monitorCtx)

	slog.Info("monitoring socket activation backend", "unit", unit)
}

// Unwatch stops monitoring a socket-activation backend and removes it from the
// restart pipeline. It satisfies socketactivation.HealthSupervisor.
func (d *Daemon) Unwatch(unit string) {
	d.mu.Lock()
	cancel, ok := d.socketMonitors[unit]
	if ok {
		delete(d.socketMonitors, unit)
	}
	d.mu.Unlock()

	if !ok {
		return
	}

	cancel()

	d.restarter.Unregister(unit)
	d.sm.Unregister(unit)

	slog.Info("stopped monitoring socket activation backend", "unit", unit)
}

func (d *Daemon) findMatchingTemplate(unitName string, templates []*config.UnitConfig) *config.UnitConfig {
	for _, tmpl := range templates {
		prefix := tmpl.TemplatePrefix()
		suffix := fmt.Sprintf(".%s", tmpl.Type)

		if !strings.HasPrefix(unitName, prefix) || !strings.HasSuffix(unitName, suffix) {
			continue
		}

		instance := extractInstance(unitName, prefix, tmpl.Type)
		if tmpl.MatchInstance(instance) {
			return tmpl
		}
	}

	return nil
}

func (d *Daemon) updateUnit(unitName string, unit *config.UnitConfig) {
	d.unregisterUnit(unitName)
	d.registerUnit(d.ctx, unitName, unit)
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
		case <-d.discoveryReloadCh:
			ticker.Reset(d.cfg.DiscoveryInterval)
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
		case <-d.timerReloadCh:
			newInterval := d.cfg.DiscoveryInterval
			if newInterval == 0 {
				newInterval = 30 * time.Second
			}

			ticker.Reset(newInterval)
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

	setupLogging(cfg.LogLevel)

	d.notifier.UpdateConfig(cfg.Notify)

	newUnits := make(map[string]*config.UnitConfig)
	var templates []*config.UnitConfig

	for i := range cfg.Units {
		unit := &cfg.Units[i]
		if !unit.IsEnabled() {
			continue
		}

		if unit.IsTemplate() {
			templates = append(templates, unit)
		} else {
			newUnits[unit.UnitName()] = unit
		}
	}

	d.mu.Lock()
	oldUnits := make(map[string]struct{}, len(d.registeredUnits))
	for name := range d.registeredUnits {
		oldUnits[name] = struct{}{}
	}
	d.mu.Unlock()

	for name := range oldUnits {
		if _, ok := newUnits[name]; ok {
			continue
		}

		if tmpl := d.findMatchingTemplate(name, templates); tmpl != nil {
			d.updateUnit(name, tmpl)

			continue
		}

		d.unregisterUnit(name)
	}

	d.restarter.SetDependents(buildDependents(cfg.Units))

	for unitName, unit := range newUnits {
		d.mu.Lock()
		_, exists := d.registeredUnits[unitName]
		d.mu.Unlock()

		if !exists {
			d.registerUnit(d.ctx, unitName, unit)
		} else {
			d.updateUnit(unitName, unit)
		}
	}

	for _, tmpl := range templates {
		d.discoverInstances(d.ctx, tmpl)
	}

	d.cfg = cfg

	if !d.discoveryRunning && d.hasDiscoverableUnits() {
		d.discoveryRunning = true
		go d.discoveryLoop(d.ctx)
	} else {
		select {
		case d.discoveryReloadCh <- struct{}{}:
		default:
		}
	}

	if !d.timerRunning && d.hasTimerMonitoring() {
		d.timerRunning = true
		go d.timerMonitorLoop(d.ctx)
	} else {
		select {
		case d.timerReloadCh <- struct{}{}:
		default:
		}
	}

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

func buildDependents(units []config.UnitConfig) map[string][]string {
	dependents := make(map[string][]string)

	for _, u := range units {
		for _, dep := range u.DependsOn {
			var depUnit string
			if strings.HasSuffix(dep, ".service") || strings.HasSuffix(dep, ".timer") {
				depUnit = dep
			} else {
				depUnit = fmt.Sprintf("%s.%s", dep, u.Type)
			}

			dependents[depUnit] = append(dependents[depUnit], u.UnitName())
		}
	}

	return dependents
}

func extractInstance(unitName, prefix, unitType string) string {
	name := strings.TrimPrefix(unitName, prefix)
	name = strings.TrimSuffix(name, fmt.Sprintf(".%s", unitType))

	return name
}
