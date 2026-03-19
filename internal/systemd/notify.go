package systemd

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
)

type Notifier struct {
	watchdogInterval time.Duration
	stop             chan struct{}
	statusFn         func() string
}

func NewNotifier(statusFn func() string) *Notifier {
	n := &Notifier{
		stop:     make(chan struct{}),
		statusFn: statusFn,
	}

	n.watchdogInterval = n.detectWatchdogInterval()

	return n
}

func (n *Notifier) Ready() {
	sent, err := daemon.SdNotify(false, daemon.SdNotifyReady)
	if err != nil {
		slog.Error("sd_notify READY", "error", err)
	}

	if !sent {
		slog.Debug("sd_notify not available (not running under systemd)")
	}
}

func (n *Notifier) Reloading() {
	if _, err := daemon.SdNotify(false, daemon.SdNotifyReloading); err != nil {
		slog.Error("sd_notify RELOADING", "error", err)
	}
}

func (n *Notifier) Stopping() {
	if _, err := daemon.SdNotify(false, daemon.SdNotifyStopping); err != nil {
		slog.Error("sd_notify STOPPING", "error", err)
	}
}

func (n *Notifier) Status(status string) {
	if _, err := daemon.SdNotify(false, fmt.Sprintf("STATUS=%s", status)); err != nil {
		slog.Error("sd_notify STATUS", "error", err)
	}
}

func (n *Notifier) StartWatchdog() {
	if n.watchdogInterval == 0 {
		slog.Debug("watchdog disabled (WATCHDOG_USEC not set)")

		return
	}

	slog.Info("starting watchdog", "interval", n.watchdogInterval)

	go func() {
		ticker := time.NewTicker(n.watchdogInterval)
		defer ticker.Stop()

		for {
			select {
			case <-n.stop:
				return
			case <-ticker.C:
				if n.statusFn != nil {
					n.Status(n.statusFn())
				}

				if _, err := daemon.SdNotify(false, daemon.SdNotifyWatchdog); err != nil {
					slog.Error("sd_notify WATCHDOG", "error", err)
				}
			}
		}
	}()
}

func (n *Notifier) StopWatchdog() {
	close(n.stop)
}

func (n *Notifier) detectWatchdogInterval() time.Duration {
	usecStr := os.Getenv("WATCHDOG_USEC")
	if usecStr == "" {
		return 0
	}

	usec, err := strconv.ParseInt(usecStr, 10, 64)
	if err != nil {
		slog.Error("parsing WATCHDOG_USEC", "value", usecStr, "error", err)

		return 0
	}

	interval := time.Duration(usec) * time.Microsecond / 2

	return interval
}
