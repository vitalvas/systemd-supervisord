//go:build linux

package systemd

import (
	"context"
	"log/slog"
)

func NewManager(ctx context.Context) Manager {
	mgr, err := NewDBusManager(ctx)
	if err != nil {
		slog.Warn("D-Bus unavailable, falling back to systemctl", "error", err)

		return NewCtlManager()
	}

	slog.Info("using D-Bus for systemd communication")

	return mgr
}
