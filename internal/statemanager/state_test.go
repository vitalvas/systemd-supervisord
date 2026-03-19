package statemanager

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateManager(t *testing.T) {
	t.Run("register and get status", func(t *testing.T) {
		sm := New(10)
		sm.Register("nginx.service")

		status := sm.GetStatus("nginx.service")
		require.NotNil(t, status)
		assert.Equal(t, "nginx.service", status.UnitName)
		assert.Empty(t, status.ActiveState)
		assert.Nil(t, status.Healthy)
		assert.Zero(t, status.RestartCount)
	})

	t.Run("get unknown unit", func(t *testing.T) {
		sm := New(10)
		assert.Nil(t, sm.GetStatus("unknown.service"))
	})

	t.Run("update state emits event", func(t *testing.T) {
		sm := New(10)
		sm.Register("nginx.service")

		var received []Event
		var mu sync.Mutex

		sm.OnEvent(func(ev Event) {
			mu.Lock()
			received = append(received, ev)
			mu.Unlock()
		})

		sm.UpdateState("nginx.service", "active", "running")

		mu.Lock()
		require.Len(t, received, 1)
		assert.Equal(t, EventStateChanged, received[0].Type)
		assert.Equal(t, "nginx.service", received[0].UnitName)
		assert.Equal(t, "active", received[0].ActiveState)
		assert.Equal(t, "running", received[0].SubState)
		mu.Unlock()

		status := sm.GetStatus("nginx.service")
		assert.Equal(t, "active", status.ActiveState)
		assert.Equal(t, "running", status.SubState)
	})

	t.Run("duplicate state does not emit", func(t *testing.T) {
		sm := New(10)
		sm.Register("nginx.service")

		var count int

		sm.OnEvent(func(_ Event) {
			count++
		})

		sm.UpdateState("nginx.service", "active", "running")
		sm.UpdateState("nginx.service", "active", "running")

		assert.Equal(t, 1, count)
	})

	t.Run("update health emits event", func(t *testing.T) {
		sm := New(10)
		sm.Register("nginx.service")

		var received []Event

		sm.OnEvent(func(ev Event) {
			received = append(received, ev)
		})

		sm.UpdateHealth("nginx.service", true)

		require.Len(t, received, 1)
		assert.Equal(t, EventHealthChanged, received[0].Type)
		require.NotNil(t, received[0].Healthy)
		assert.True(t, *received[0].Healthy)

		status := sm.GetStatus("nginx.service")
		require.NotNil(t, status.Healthy)
		assert.True(t, *status.Healthy)
	})

	t.Run("duplicate health does not emit", func(t *testing.T) {
		sm := New(10)
		sm.Register("nginx.service")

		var count int

		sm.OnEvent(func(_ Event) {
			count++
		})

		sm.UpdateHealth("nginx.service", true)
		sm.UpdateHealth("nginx.service", true)

		assert.Equal(t, 1, count)
	})

	t.Run("restart count", func(t *testing.T) {
		sm := New(10)
		sm.Register("nginx.service")

		sm.IncrementRestartCount("nginx.service")
		sm.IncrementRestartCount("nginx.service")

		status := sm.GetStatus("nginx.service")
		assert.Equal(t, 2, status.RestartCount)

		sm.ResetRestartCount("nginx.service")

		status = sm.GetStatus("nginx.service")
		assert.Zero(t, status.RestartCount)
	})

	t.Run("get all statuses", func(t *testing.T) {
		sm := New(10)
		sm.Register("a.service")
		sm.Register("b.timer")

		statuses := sm.GetAllStatuses()
		assert.Len(t, statuses, 2)
	})

	t.Run("summary", func(t *testing.T) {
		sm := New(10)
		sm.Register("a.service")
		sm.Register("b.service")
		sm.Register("c.service")

		sm.UpdateHealth("a.service", true)
		sm.UpdateHealth("b.service", false)

		total, healthy, unhealthy := sm.Summary()
		assert.Equal(t, 3, total)
		assert.Equal(t, 1, healthy)
		assert.Equal(t, 1, unhealthy)
	})
}
