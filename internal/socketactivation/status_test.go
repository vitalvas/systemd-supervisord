package socketactivation

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

func TestActivatorStatus(t *testing.T) {
	t.Run("reports config fields when stopped", func(t *testing.T) {
		cfg := baseConfig("127.0.0.1:4101", "127.0.0.1:5101")
		cfg.Protocol = []string{"tcp", "udp"}

		a, _ := newTestActivator(t, cfg, &mockController{}, &fakeProbe{})

		s := a.Status()
		assert.Equal(t, cfg.Name, s.Name)
		assert.Equal(t, "backend.service", s.Unit)
		assert.Equal(t, "127.0.0.1:4101", s.Listen)
		assert.Equal(t, []string{"tcp", "udp"}, s.Protocol)
		assert.Equal(t, "127.0.0.1:5101", s.Backend)
		assert.False(t, s.Running)
		assert.Equal(t, 0, s.ActiveConnections)
		assert.Zero(t, s.IdleSeconds)
	})

	t.Run("reports running and active connections", func(t *testing.T) {
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

		conn, err := net.Dial("tcp", listen)
		require.NoError(t, err)
		defer conn.Close()

		conn.Write([]byte("x"))
		buf := make([]byte, 1)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		conn.Read(buf)

		require.Eventually(t, func() bool {
			return a.Status().ActiveConnections == 1
		}, 2*time.Second, 10*time.Millisecond)

		s := a.Status()
		assert.True(t, s.Running)
		// Idle time is zero while a connection is active.
		assert.Zero(t, s.IdleSeconds)
	})

	t.Run("reports idle seconds when running and idle", func(t *testing.T) {
		cfg := baseConfig("127.0.0.1:4101", "127.0.0.1:5101")
		a, clk := newTestActivator(t, cfg, &mockController{}, &fakeProbe{})

		// Simulate a running-but-idle backend.
		a.mu.Lock()
		a.running = true
		a.lastActive = clk.Now()
		a.mu.Unlock()

		clk.advance(90 * time.Second)

		s := a.Status()
		assert.True(t, s.Running)
		assert.Equal(t, 0, s.ActiveConnections)
		assert.InDelta(t, 90.0, s.IdleSeconds, 0.001)
	})
}

func TestManagerStatuses(t *testing.T) {
	entries := []config.SocketActivationConfig{
		baseConfig("127.0.0.1:4101", "127.0.0.1:5101"),
		baseConfig("127.0.0.1:4102", "127.0.0.1:5102"),
	}
	entries[0].Unit = "a.service"
	entries[0].Name = "a"
	entries[1].Unit = "b.service"
	entries[1].Name = "b"

	mgr := NewManager(entries, &mockController{})

	statuses := mgr.Statuses()
	require.Len(t, statuses, 2)
	assert.Equal(t, "a.service", statuses[0].Unit)
	assert.Equal(t, "b.service", statuses[1].Unit)
}
