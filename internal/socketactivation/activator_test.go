package socketactivation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

type mockController struct {
	mu       sync.Mutex
	starts   int
	stops    int
	startErr error
	stopErr  error
}

func (m *mockController) Start(_ context.Context, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.starts++

	return m.startErr
}

func (m *mockController) Stop(_ context.Context, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stops++

	return m.stopErr
}

func (m *mockController) startCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.starts
}

func (m *mockController) stopCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.stops
}

type blockingStopController struct {
	starts      atomic.Int32
	stops       atomic.Int32
	stopStarted chan struct{}
	allowStop   chan struct{}
	stopOnce    sync.Once
}

func newBlockingStopController() *blockingStopController {
	return &blockingStopController{
		stopStarted: make(chan struct{}),
		allowStop:   make(chan struct{}),
	}
}

func (m *blockingStopController) Start(_ context.Context, _ string) error {
	m.starts.Add(1)

	return nil
}

func (m *blockingStopController) Stop(ctx context.Context, _ string) error {
	m.stops.Add(1)
	m.stopOnce.Do(func() { close(m.stopStarted) })

	select {
	case <-m.allowStop:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type fakeProbe struct {
	healthy atomic.Bool
	calls   atomic.Int32
}

func (p *fakeProbe) Probe(_ context.Context) error {
	p.calls.Add(1)

	if p.healthy.Load() {
		return nil
	}

	return errors.New("not healthy")
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// echoServer accepts connections on a random port and echoes everything back.
func echoServer(t *testing.T) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			go func(c net.Conn) {
				defer c.Close()

				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if _, werr := c.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	t.Cleanup(func() { ln.Close() })

	return ln
}

func newTestActivator(t *testing.T, cfg config.SocketActivationConfig, ctrl UnitController, probe HealthProbe) (*Activator, *manualClock) {
	t.Helper()

	clk := &manualClock{now: time.Unix(1000, 0)}

	a := &Activator{
		cfg:    cfg,
		ctrl:   ctrl,
		probe:  probe,
		dial:   defaultDialer,
		clock:  clk,
		logger: testLogger(),
	}

	return a, clk
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseConfig(listen, backend string) config.SocketActivationConfig {
	return config.SocketActivationConfig{
		Name:           "test",
		Listen:         listen,
		Unit:           "backend.service",
		Backend:        backend,
		StartupTimeout: 2 * time.Second,
		IdleTimeout:    10 * time.Second,
		HealthInterval: 10 * time.Millisecond,
		HealthTimeout:  time.Second,
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ln.Close()

	return addr
}

func TestActivatorProxiesAfterHealthy(t *testing.T) {
	backend := echoServer(t)
	listen := freeAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := baseConfig(listen, backend.Addr().String())
	a, _ := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	roundTrip(t, "tcp", listen, "hello")
	assert.Equal(t, 1, ctrl.startCount())
}

func TestActivatorWaitsUntilHealthy(t *testing.T) {
	backend := echoServer(t)
	listen := freeAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{}

	cfg := baseConfig(listen, backend.Addr().String())
	a, _ := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	// Flip to healthy shortly after the connection arrives.
	go func() {
		time.Sleep(50 * time.Millisecond)
		probe.healthy.Store(true)
	}()

	conn, err := net.Dial("tcp", listen)
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte("data!"))
	require.NoError(t, err)

	buf := make([]byte, 5)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "data!", string(buf[:n]))
	assert.GreaterOrEqual(t, int(probe.calls.Load()), 1)
}

func TestActivatorStartupTimeout(t *testing.T) {
	backend := echoServer(t)
	listen := freeAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{} // never healthy

	cfg := baseConfig(listen, backend.Addr().String())
	cfg.StartupTimeout = 100 * time.Millisecond
	a, _ := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	conn, err := net.Dial("tcp", listen)
	require.NoError(t, err)
	defer conn.Close()

	// Connection is closed by the activator once startup times out.
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	assert.Error(t, err) // EOF

	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	assert.False(t, running)
	assert.Equal(t, 1, ctrl.stopCount())
}

func TestActivatorStartErrorPropagates(t *testing.T) {
	listen := freeAddr(t)

	ctrl := &mockController{startErr: errors.New("boom")}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := baseConfig(listen, "127.0.0.1:1")
	a, _ := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	conn, err := net.Dial("tcp", listen)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	assert.Error(t, err)
}

func TestActivatorConcurrentConnectionsSingleStart(t *testing.T) {
	backend := echoServer(t)
	listen := freeAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := baseConfig(listen, backend.Addr().String())
	a, _ := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			conn, err := net.Dial("tcp", listen)
			if err != nil {
				return
			}
			defer conn.Close()

			conn.Write([]byte("x"))
			buf := make([]byte, 1)
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			conn.Read(buf)
		}()
	}

	wg.Wait()

	assert.Equal(t, 1, ctrl.startCount())
}

func TestActivatorIdleStop(t *testing.T) {
	backend := echoServer(t)
	listen := freeAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := baseConfig(listen, backend.Addr().String())
	a, clk := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	conn, err := net.Dial("tcp", listen)
	require.NoError(t, err)

	conn.Write([]byte("x"))
	buf := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	conn.Read(buf)
	conn.Close()

	// Wait for the connection to be fully released.
	require.Eventually(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()

		return a.active == 0 && a.running
	}, 2*time.Second, 10*time.Millisecond)

	// Not yet idle long enough.
	a.maybeStopIdle(ctx)
	assert.Equal(t, 0, ctrl.stopCount())

	// Advance past the idle timeout.
	clk.advance(cfg.IdleTimeout + time.Second)
	a.maybeStopIdle(ctx)

	assert.Equal(t, 1, ctrl.stopCount())

	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	assert.False(t, running)
}

func TestActivatorIdleStopFailureKeepsRunning(t *testing.T) {
	ctrl := &mockController{stopErr: errors.New("stop failed")}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := baseConfig("127.0.0.1:4101", "127.0.0.1:5101")
	a, clk := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.mu.Lock()
	a.running = true
	a.lastActive = clk.Now()
	a.mu.Unlock()

	clk.advance(cfg.IdleTimeout + time.Second)
	a.maybeStopIdle(ctx)

	assert.Equal(t, 1, ctrl.stopCount())

	a.mu.Lock()
	running := a.running
	stopping := a.stopping
	a.mu.Unlock()

	assert.True(t, running)
	assert.False(t, stopping)
}

func TestActivatorWaitsForIdleStopBeforeRestart(t *testing.T) {
	ctrl := newBlockingStopController()
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := baseConfig("127.0.0.1:4101", "127.0.0.1:5101")
	a, clk := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.mu.Lock()
	a.running = true
	a.lastActive = clk.Now()
	a.mu.Unlock()

	clk.advance(cfg.IdleTimeout + time.Second)

	go a.maybeStopIdle(ctx)

	select {
	case <-ctrl.stopStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("idle stop did not start")
	}

	a.connOpened()
	defer a.connClosed()

	done := make(chan error, 1)
	go func() {
		done <- a.ensureRunning(ctx)
	}()

	select {
	case err := <-done:
		t.Fatalf("ensureRunning returned before idle stop completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(ctrl.allowStop)

	require.NoError(t, <-done)
	assert.Equal(t, int32(1), ctrl.starts.Load())
	assert.Equal(t, int32(1), ctrl.stops.Load())
}

func TestActivatorIdleNotStoppedWithActiveConnection(t *testing.T) {
	backend := echoServer(t)
	listen := freeAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := baseConfig(listen, backend.Addr().String())
	a, clk := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	conn, err := net.Dial("tcp", listen)
	require.NoError(t, err)
	defer conn.Close()

	conn.Write([]byte("x"))
	buf := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	conn.Read(buf)

	require.Eventually(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()

		return a.active == 1
	}, 2*time.Second, 10*time.Millisecond)

	// Even far past idle timeout, an active connection prevents stopping.
	clk.advance(cfg.IdleTimeout * 10)
	a.maybeStopIdle(ctx)

	assert.Equal(t, 0, ctrl.stopCount())
}

func TestActivatorRestartsAfterIdleStop(t *testing.T) {
	backend := echoServer(t)
	listen := freeAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := baseConfig(listen, backend.Addr().String())
	a, clk := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	dialEcho := func() {
		conn, err := net.Dial("tcp", listen)
		require.NoError(t, err)
		defer conn.Close()

		conn.Write([]byte("x"))
		buf := make([]byte, 1)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		conn.Read(buf)
	}

	dialEcho()

	require.Eventually(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()

		return a.active == 0
	}, 2*time.Second, 10*time.Millisecond)

	clk.advance(cfg.IdleTimeout + time.Second)
	a.maybeStopIdle(ctx)
	require.Equal(t, 1, ctrl.stopCount())

	// New connection should start the unit again.
	dialEcho()
	assert.Equal(t, 2, ctrl.startCount())
}
