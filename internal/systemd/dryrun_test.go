package systemd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubManager struct {
	startCalled   int
	stopCalled    int
	restartCalled int
}

func (s *stubManager) Start(_ context.Context, _ string) error {
	s.startCalled++
	return nil
}

func (s *stubManager) Stop(_ context.Context, _ string) error {
	s.stopCalled++
	return nil
}

func (s *stubManager) Restart(_ context.Context, _ string) error {
	s.restartCalled++
	return nil
}

func (s *stubManager) GetUnitState(_ context.Context, _ string) (*UnitState, error) {
	return &UnitState{
		Name:        "test.service",
		ActiveState: ActiveStateActive,
		SubState:    SubStateRunning,
	}, nil
}

func (s *stubManager) GetTimerLastTrigger(_ context.Context, _ string) (time.Time, error) {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil
}

func (s *stubManager) ListUnits(_ context.Context, _ string) ([]string, error) {
	return []string{"test.service"}, nil
}

func (s *stubManager) WatchUnit(_ context.Context, _ string, _ chan<- StateChange) error {
	return nil
}

func (s *stubManager) Close() error {
	return nil
}

func TestDryRunManager(t *testing.T) {
	t.Run("start does not call real manager", func(t *testing.T) {
		stub := &stubManager{}
		drm := NewDryRunManager(stub)

		err := drm.Start(context.Background(), "test.service")
		require.NoError(t, err)
		assert.Equal(t, 0, stub.startCalled)
	})

	t.Run("stop does not call real manager", func(t *testing.T) {
		stub := &stubManager{}
		drm := NewDryRunManager(stub)

		err := drm.Stop(context.Background(), "test.service")
		require.NoError(t, err)
		assert.Equal(t, 0, stub.stopCalled)
	})

	t.Run("restart does not call real manager", func(t *testing.T) {
		stub := &stubManager{}
		drm := NewDryRunManager(stub)

		err := drm.Restart(context.Background(), "test.service")
		require.NoError(t, err)
		assert.Equal(t, 0, stub.restartCalled)
	})

	t.Run("get unit state delegates to real manager", func(t *testing.T) {
		stub := &stubManager{}
		drm := NewDryRunManager(stub)

		state, err := drm.GetUnitState(context.Background(), "test.service")
		require.NoError(t, err)
		assert.Equal(t, ActiveStateActive, state.ActiveState)
	})

	t.Run("get timer last trigger delegates to real manager", func(t *testing.T) {
		stub := &stubManager{}
		drm := NewDryRunManager(stub)

		ts, err := drm.GetTimerLastTrigger(context.Background(), "test.timer")
		require.NoError(t, err)
		assert.Equal(t, 2026, ts.Year())
	})

	t.Run("list units delegates to real manager", func(t *testing.T) {
		stub := &stubManager{}
		drm := NewDryRunManager(stub)

		units, err := drm.ListUnits(context.Background(), "test")
		require.NoError(t, err)
		assert.Equal(t, []string{"test.service"}, units)
	})

	t.Run("watch unit delegates to real manager", func(t *testing.T) {
		stub := &stubManager{}
		drm := NewDryRunManager(stub)

		ch := make(chan StateChange, 1)

		err := drm.WatchUnit(context.Background(), "test.service", ch)
		require.NoError(t, err)
	})

	t.Run("close delegates to real manager", func(t *testing.T) {
		stub := &stubManager{}
		drm := NewDryRunManager(stub)

		err := drm.Close()
		require.NoError(t, err)
	})
}
