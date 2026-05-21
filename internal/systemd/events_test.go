package systemd

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessDBusEvents(t *testing.T) {
	t.Run("forwards state changes", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		evCh := make(chan map[string]*dbus.UnitStatus, 1)
		errCh := make(chan error, 1)
		ch := make(chan StateChange, 10)

		go processDBusEvents(ctx, evCh, errCh, ch)

		evCh <- map[string]*dbus.UnitStatus{
			"nginx.service": {
				Name:        "nginx.service",
				ActiveState: ActiveStateActive,
				SubState:    SubStateRunning,
			},
		}

		select {
		case change := <-ch:
			assert.Equal(t, "nginx.service", change.UnitName)
			assert.Equal(t, ActiveStateActive, change.ActiveState)
			assert.Equal(t, SubStateRunning, change.SubState)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for state change")
		}
	})

	t.Run("skips nil status entries", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		evCh := make(chan map[string]*dbus.UnitStatus, 1)
		errCh := make(chan error, 1)
		ch := make(chan StateChange, 10)

		go processDBusEvents(ctx, evCh, errCh, ch)

		evCh <- map[string]*dbus.UnitStatus{
			"removed.service": nil,
			"nginx.service": {
				Name:        "nginx.service",
				ActiveState: ActiveStateActive,
				SubState:    SubStateRunning,
			},
		}

		select {
		case change := <-ch:
			assert.Equal(t, "nginx.service", change.UnitName)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for state change")
		}

		select {
		case <-ch:
			t.Fatal("unexpected extra state change")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("stops on context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		evCh := make(chan map[string]*dbus.UnitStatus)
		errCh := make(chan error)
		ch := make(chan StateChange, 10)

		done := make(chan struct{})
		go func() {
			processDBusEvents(ctx, evCh, errCh, ch)
			close(done)
		}()

		cancel()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("processDBusEvents did not stop on context cancellation")
		}
	})

	t.Run("stops on closed event channel", func(t *testing.T) {
		ctx := context.Background()

		evCh := make(chan map[string]*dbus.UnitStatus)
		errCh := make(chan error)
		ch := make(chan StateChange, 10)

		done := make(chan struct{})
		go func() {
			processDBusEvents(ctx, evCh, errCh, ch)
			close(done)
		}()

		close(evCh)

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("processDBusEvents did not stop on closed event channel")
		}
	})

	t.Run("stops on closed error channel", func(t *testing.T) {
		ctx := context.Background()

		evCh := make(chan map[string]*dbus.UnitStatus)
		errCh := make(chan error)
		ch := make(chan StateChange, 10)

		done := make(chan struct{})
		go func() {
			processDBusEvents(ctx, evCh, errCh, ch)
			close(done)
		}()

		close(errCh)

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("processDBusEvents did not stop on closed error channel")
		}
	})

	t.Run("handles error from error channel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		evCh := make(chan map[string]*dbus.UnitStatus)
		errCh := make(chan error, 1)
		ch := make(chan StateChange, 10)

		go processDBusEvents(ctx, evCh, errCh, ch)

		errCh <- fmt.Errorf("test error")

		time.Sleep(50 * time.Millisecond)

		require.Empty(t, ch)
	})

	t.Run("forwards multiple events", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		evCh := make(chan map[string]*dbus.UnitStatus, 2)
		errCh := make(chan error, 1)
		ch := make(chan StateChange, 10)

		go processDBusEvents(ctx, evCh, errCh, ch)

		evCh <- map[string]*dbus.UnitStatus{
			"a.service": {
				Name:        "a.service",
				ActiveState: ActiveStateActive,
				SubState:    SubStateRunning,
			},
		}
		evCh <- map[string]*dbus.UnitStatus{
			"b.service": {
				Name:        "b.service",
				ActiveState: ActiveStateFailed,
				SubState:    SubStateFailed,
			},
		}

		var changes []StateChange
		timeout := time.After(time.Second)

		for i := 0; i < 2; i++ {
			select {
			case c := <-ch:
				changes = append(changes, c)
			case <-timeout:
				t.Fatal("timed out waiting for state changes")
			}
		}

		require.Len(t, changes, 2)
	})
}
