package socketactivation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

func TestManagerEnabled(t *testing.T) {
	assert.False(t, NewManager(nil, &mockController{}).Enabled())
	assert.True(t, NewManager([]config.SocketActivationConfig{
		baseConfig(freeAddr(t), "127.0.0.1:1"),
	}, &mockController{}).Enabled())
}

func TestManagerStart(t *testing.T) {
	t.Run("starts all listeners", func(t *testing.T) {
		entries := []config.SocketActivationConfig{
			baseConfig(freeAddr(t), "127.0.0.1:1"),
			baseConfig(freeAddr(t), "127.0.0.1:2"),
		}
		entries[1].Name = "second"

		mgr := NewManager(entries, &mockController{})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		require.NoError(t, mgr.Start(ctx))
	})

	t.Run("returns error on bind failure", func(t *testing.T) {
		addr := freeAddr(t)
		entries := []config.SocketActivationConfig{
			baseConfig(addr, "127.0.0.1:1"),
			baseConfig(addr, "127.0.0.1:2"),
		}
		entries[1].Name = "second"

		mgr := NewManager(entries, &mockController{})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := mgr.Start(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "starting socket activation")
	})
}

func TestNewSelectsProbe(t *testing.T) {
	t.Run("http probe when health_url set", func(t *testing.T) {
		cfg := baseConfig("127.0.0.1:0", "127.0.0.1:5101")
		cfg.HealthURL = "http://127.0.0.1:5101/health"

		a := New(cfg, &mockController{})
		_, ok := a.probe.(*HTTPProbe)
		assert.True(t, ok)
		assert.Equal(t, "test", a.Name())
	})

	t.Run("tcp probe when no health_url", func(t *testing.T) {
		cfg := baseConfig("127.0.0.1:0", "127.0.0.1:5101")

		a := New(cfg, &mockController{})
		_, ok := a.probe.(*TCPProbe)
		assert.True(t, ok)
	})
}

func TestRealClock(t *testing.T) {
	before := time.Now()
	got := RealClock{}.Now()
	assert.False(t, got.Before(before))
}
