package systemd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectWatchdogInterval(t *testing.T) {
	t.Run("no env var returns zero", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := &Notifier{stop: make(chan struct{})}
		interval := n.detectWatchdogInterval()
		assert.Equal(t, time.Duration(0), interval)
	})

	t.Run("valid value returns half interval", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "2000000") // 2 seconds

		n := &Notifier{stop: make(chan struct{})}
		interval := n.detectWatchdogInterval()
		assert.Equal(t, 1*time.Second, interval)
	})

	t.Run("small value", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "100") // 100 microseconds

		n := &Notifier{stop: make(chan struct{})}
		interval := n.detectWatchdogInterval()
		assert.Equal(t, 50*time.Microsecond, interval)
	})

	t.Run("invalid value returns zero", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "not-a-number")

		n := &Notifier{stop: make(chan struct{})}
		interval := n.detectWatchdogInterval()
		assert.Equal(t, time.Duration(0), interval)
	})

	t.Run("negative value returns negative duration", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "-1000")

		n := &Notifier{stop: make(chan struct{})}
		interval := n.detectWatchdogInterval()
		assert.True(t, interval < 0)
	})
}

func TestNewNotifier(t *testing.T) {
	t.Run("returns non-nil notifier", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(func() string { return "ok" })
		require.NotNil(t, n)
	})

	t.Run("stop channel initialized", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(func() string { return "ok" })
		require.NotNil(t, n.stop)
	})

	t.Run("status function stored", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		statusFn := func() string { return "test-status" }
		n := NewNotifier(statusFn)
		assert.Equal(t, "test-status", n.statusFn())
	})

	t.Run("nil status function accepted", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(nil)
		require.NotNil(t, n)
		assert.Nil(t, n.statusFn)
	})

	t.Run("watchdog interval from env", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "4000000") // 4 seconds

		n := NewNotifier(func() string { return "" })
		assert.Equal(t, 2*time.Second, n.watchdogInterval)
	})

	t.Run("watchdog interval zero without env", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(func() string { return "" })
		assert.Equal(t, time.Duration(0), n.watchdogInterval)
	})
}

func TestStartStopWatchdog(t *testing.T) {
	t.Run("start with no interval returns immediately", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(func() string { return "ok" })
		n.StartWatchdog()
		// No goroutine started, StopWatchdog should still be safe
		n.StopWatchdog()
	})

	t.Run("start and stop with interval", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "2000000")

		n := NewNotifier(func() string { return "ok" })
		require.NotZero(t, n.watchdogInterval)

		n.StartWatchdog()

		// Give the goroutine a moment to start
		time.Sleep(50 * time.Millisecond)

		n.StopWatchdog()

		// Give the goroutine a moment to stop
		time.Sleep(50 * time.Millisecond)
	})
}

func TestNotifierNotifications(t *testing.T) {
	t.Run("ready does not panic", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(func() string { return "ok" })
		assert.NotPanics(t, func() { n.Ready() })
	})

	t.Run("reloading does not panic", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(func() string { return "ok" })
		assert.NotPanics(t, func() { n.Reloading() })
	})

	t.Run("stopping does not panic", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(func() string { return "ok" })
		assert.NotPanics(t, func() { n.Stopping() })
	})

	t.Run("status does not panic", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(func() string { return "ok" })
		assert.NotPanics(t, func() { n.Status("running") })
	})

	t.Run("status with empty string does not panic", func(t *testing.T) {
		t.Setenv("WATCHDOG_USEC", "")

		n := NewNotifier(func() string { return "ok" })
		assert.NotPanics(t, func() { n.Status("") })
	})
}
