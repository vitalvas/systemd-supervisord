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
	assert.False(t, NewManager(nil, &mockController{}, nil).Enabled())
	assert.True(t, NewManager([]config.SocketActivationConfig{
		baseConfig(freeAddr(t), "127.0.0.1:1"),
	}, &mockController{}, nil).Enabled())
}

func TestManagerStart(t *testing.T) {
	t.Run("starts all listeners", func(t *testing.T) {
		entries := []config.SocketActivationConfig{
			baseConfig(freeAddr(t), "127.0.0.1:1"),
			baseConfig(freeAddr(t), "127.0.0.1:2"),
		}
		entries[1].Name = "second"

		mgr := NewManager(entries, &mockController{}, nil)

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

		mgr := NewManager(entries, &mockController{}, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := mgr.Start(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "starting socket activation")
	})
}

func TestNewSelectsProbe(t *testing.T) {
	t.Run("checks probe and monitor when health_checks set", func(t *testing.T) {
		cfg := baseConfig("127.0.0.1:0", "127.0.0.1:5101")
		cfg.HealthChecks = []config.HealthCheck{
			{Type: "http", Timeout: time.Second, HTTP: &config.HTTPHealthCheck{Address: "http://127.0.0.1:5101/health"}},
		}

		a := New(cfg, &mockController{}, &fakeSupervisor{})
		_, ok := a.probe.(*checksProbe)
		assert.True(t, ok)
		assert.NotNil(t, a.monitor)
		assert.Equal(t, "test", a.Name())
	})

	t.Run("no monitor without supervisor", func(t *testing.T) {
		cfg := baseConfig("127.0.0.1:0", "127.0.0.1:5101")
		cfg.HealthChecks = []config.HealthCheck{
			{Type: "http", Timeout: time.Second, HTTP: &config.HTTPHealthCheck{Address: "http://127.0.0.1:5101/health"}},
		}

		a := New(cfg, &mockController{}, nil)
		assert.NotNil(t, a.probe)
		assert.Nil(t, a.monitor)
	})

	t.Run("no probe when no health_checks", func(t *testing.T) {
		cfg := baseConfig("127.0.0.1:0", "127.0.0.1:5101")

		a := New(cfg, &mockController{}, &fakeSupervisor{})
		assert.Nil(t, a.probe)
		assert.Nil(t, a.monitor)
	})
}

func TestRealClock(t *testing.T) {
	before := time.Now()
	got := RealClock{}.Now()
	assert.False(t, got.Before(before))
}
