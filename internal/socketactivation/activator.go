package socketactivation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

// UnitController starts, stops, and restarts the backend systemd unit backing a
// listener. It is the subset of systemd.Manager the activator depends on.
type UnitController interface {
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
	Restart(ctx context.Context, unit string) error
}

// Dialer establishes a connection to the backend. It is abstracted for testing.
type Dialer func(ctx context.Context, address string) (net.Conn, error)

func defaultDialer(ctx context.Context, address string) (net.Conn, error) {
	var d net.Dialer

	return d.DialContext(ctx, "tcp", address)
}

// clock abstracts time for deterministic idle-timeout testing.
type clock interface {
	Now() time.Time
}

// RealClock is the production clock backed by time.Now.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// Activator owns a single socket_activation entry: it listens on the public
// address, lazily starts the backend unit on first connection, waits until the
// backend is healthy, proxies traffic, and stops the unit after the idle
// timeout once there are no active connections and no recent traffic.
type Activator struct {
	cfg       config.SocketActivationConfig
	ctrl      UnitController
	probe     healthProbe
	monitor   *unitMonitor
	dial      Dialer
	udpDialer UDPDialer
	clock     clock
	logger    *slog.Logger

	mu         sync.Mutex
	serveCtx   context.Context
	running    bool
	starting   bool
	stopping   bool
	startErr   error
	active     int
	lastActive time.Time
	startWait  chan struct{}
	stopWait   chan struct{}
}

// New builds an Activator for the given entry using the supplied unit
// controller. The readiness probe runs the entry's health_checks; when none are
// configured the backend is considered ready as soon as the unit is started.
//
// When health_checks are configured and sup is non-nil, the backend is also
// continuously monitored while running: the activator registers it with the
// shared health-check and restart pipeline via sup and unregisters it on
// idle-stop, so it is checked and restarted only for as long as it is up.
func New(cfg config.SocketActivationConfig, ctrl UnitController, sup HealthSupervisor) *Activator {
	var probe healthProbe

	var monitor *unitMonitor

	if len(cfg.HealthChecks) > 0 {
		probe = newChecksProbe(cfg.HealthChecks)

		if sup != nil {
			policy := config.RestartPolicy{}
			if cfg.Restart != nil {
				policy = *cfg.Restart
			}

			monitor = newUnitMonitor(sup, cfg.Unit, cfg.HealthChecks, policy)
		}
	}

	return &Activator{
		cfg:     cfg,
		ctrl:    ctrl,
		probe:   probe,
		monitor: monitor,
		dial:    defaultDialer,
		clock:   RealClock{},
		logger:  slog.With("socket_activation", cfg.Name),
	}
}

// Name returns the configured entry name.
func (a *Activator) Name() string {
	return a.cfg.Name
}

// Start binds and serves a listener for every configured protocol, sharing a
// single on-demand unit lifecycle and idle tracker across them. It returns once
// all listeners are bound; serving continues in background goroutines until ctx
// is cancelled. If any listener fails to bind, previously bound listeners are
// closed and the error is returned.
func (a *Activator) Start(ctx context.Context) error {
	a.mu.Lock()
	a.serveCtx = ctx
	a.mu.Unlock()

	protocols := a.cfg.Protocol
	if len(protocols) == 0 {
		protocols = []string{"tcp"}
	}

	var closers []func()

	for _, proto := range protocols {
		closer, err := a.startProtocol(ctx, proto)
		if err != nil {
			for _, c := range closers {
				c()
			}

			return err
		}

		closers = append(closers, closer)
	}

	go a.idleLoop(ctx)

	return nil
}

// startProtocol binds and serves a single protocol listener. It returns a
// function that closes the listener so callers can unwind on partial failure.
func (a *Activator) startProtocol(ctx context.Context, proto string) (func(), error) {
	switch proto {
	case "tcp":
		return a.startTCP(ctx)
	case "udp":
		return a.startUDP(ctx)
	default:
		return nil, fmt.Errorf("unsupported protocol %q", proto)
	}
}

func (a *Activator) startTCP(ctx context.Context) (func(), error) {
	ln, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listening on tcp %s: %w", a.cfg.Listen, err)
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	go a.acceptLoop(ctx, ln)

	a.logger.Info("socket activation listener started",
		"protocol", "tcp",
		"listen", a.cfg.Listen,
		"unit", a.cfg.Unit,
		"backend", a.cfg.Backend,
	)

	return func() { _ = ln.Close() }, nil
}

func (a *Activator) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			if errors.Is(err, net.ErrClosed) {
				return
			}

			a.logger.Error("accept failed", "error", err)

			continue
		}

		go a.handle(ctx, conn)
	}
}

func (a *Activator) handle(ctx context.Context, client net.Conn) {
	defer client.Close()

	a.connOpened()
	defer a.connClosed()

	if err := a.ensureRunning(ctx); err != nil {
		a.logger.Error("backend not available", "error", err)

		return
	}

	backend, err := a.dial(ctx, a.cfg.Backend)
	if err != nil {
		a.logger.Error("dialing backend failed", "backend", a.cfg.Backend, "error", err)

		return
	}

	defer backend.Close()

	a.logger.Debug("proxying connection", "remote", client.RemoteAddr().String())

	relay(client, backend, a.markTraffic)
}

// ensureRunning guarantees the unit is started and healthy. Concurrent callers
// share a single startup attempt.
func (a *Activator) ensureRunning(ctx context.Context) error {
	for {
		a.mu.Lock()

		if a.stopping {
			wait := a.stopWait
			a.mu.Unlock()

			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if a.running {
			a.mu.Unlock()

			return nil
		}

		if a.starting {
			wait := a.startWait
			a.mu.Unlock()

			select {
			case <-wait:
			case <-ctx.Done():
				return ctx.Err()
			}

			a.mu.Lock()
			err := a.startErr
			running := a.running
			a.mu.Unlock()

			if running {
				return nil
			}

			if err != nil {
				return err
			}

			return errors.New("backend failed to start")
		}

		a.starting = true
		a.startErr = nil
		a.startWait = make(chan struct{})
		wait := a.startWait
		a.mu.Unlock()

		err := a.startAndWait(ctx)

		a.mu.Lock()
		a.starting = false
		a.startErr = err

		if err == nil {
			a.running = true
			a.startMonitor()
		}

		close(wait)
		a.mu.Unlock()

		return err
	}
}

// startMonitor begins continuous monitoring of the running backend. It must be
// called with a.mu held. It is a no-op when no monitor is configured (no health
// checks or no supervisor).
func (a *Activator) startMonitor() {
	if a.monitor == nil {
		return
	}

	a.monitor.start(a.serveCtx)
}

// stopMonitor ends continuous monitoring of the backend. It must be called with
// a.mu held and is a no-op when no monitor is configured.
func (a *Activator) stopMonitor() {
	if a.monitor == nil {
		return
	}

	a.monitor.stop()
}

func (a *Activator) startAndWait(ctx context.Context) error {
	a.logger.Info("starting backend unit on demand", "unit", a.cfg.Unit)

	if err := a.ctrl.Start(ctx, a.cfg.Unit); err != nil {
		return fmt.Errorf("starting unit %s: %w", a.cfg.Unit, err)
	}

	if a.probe == nil {
		a.logger.Info("backend started, no health checks configured", "unit", a.cfg.Unit)

		return nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, a.cfg.StartupTimeout)
	defer cancel()

	ticker := time.NewTicker(a.probe.pollInterval())
	defer ticker.Stop()

	for {
		if err := a.probe.probe(waitCtx); err == nil {
			a.logger.Info("backend healthy", "unit", a.cfg.Unit)

			return nil
		}

		select {
		case <-waitCtx.Done():
			err := fmt.Errorf("backend %s not healthy within %s", a.cfg.Unit, a.cfg.StartupTimeout)
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				a.stopAfterFailedStart(ctx, err)
			}

			return err
		case <-ticker.C:
		}
	}
}

func (a *Activator) stopAfterFailedStart(ctx context.Context, cause error) {
	a.logger.Warn("stopping backend unit after failed startup", "unit", a.cfg.Unit, "error", cause)

	if err := a.ctrl.Stop(ctx, a.cfg.Unit); err != nil {
		a.logger.Error("stopping failed startup unit failed", "unit", a.cfg.Unit, "error", err)
	}
}

func (a *Activator) connOpened() {
	a.mu.Lock()
	a.active++
	a.lastActive = a.clock.Now()
	a.mu.Unlock()
}

func (a *Activator) connClosed() {
	a.mu.Lock()
	a.active--
	a.lastActive = a.clock.Now()
	a.mu.Unlock()
}

func (a *Activator) markTraffic() {
	a.mu.Lock()
	a.lastActive = a.clock.Now()
	a.mu.Unlock()
}

func (a *Activator) idleLoop(ctx context.Context) {
	interval := max(a.cfg.IdleTimeout/4, time.Second)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.maybeStopIdle(ctx)
		}
	}
}

// maybeStopIdle stops the backend unit when it is running, has no active
// connections, and no traffic has occurred within the idle timeout.
func (a *Activator) maybeStopIdle(ctx context.Context) {
	a.mu.Lock()

	if !a.running || a.starting || a.stopping || a.active > 0 {
		a.mu.Unlock()

		return
	}

	idleFor := a.clock.Now().Sub(a.lastActive)
	if idleFor < a.cfg.IdleTimeout {
		a.mu.Unlock()

		return
	}

	a.stopping = true
	a.stopWait = make(chan struct{})
	wait := a.stopWait
	a.stopMonitor()
	a.mu.Unlock()

	a.logger.Info("stopping idle backend unit", "unit", a.cfg.Unit, "idle_for", idleFor)

	if err := a.ctrl.Stop(ctx, a.cfg.Unit); err != nil {
		a.logger.Error("stopping idle unit failed", "unit", a.cfg.Unit, "error", err)
	} else {
		a.mu.Lock()
		a.running = false
		a.mu.Unlock()
	}

	a.mu.Lock()
	a.stopping = false
	close(wait)
	a.mu.Unlock()
}
