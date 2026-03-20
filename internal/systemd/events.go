package systemd

import (
	"context"
	"log/slog"

	"github.com/coreos/go-systemd/v22/dbus"
)

func processDBusEvents(ctx context.Context, evCh <-chan map[string]*dbus.UnitStatus, errCh <-chan error, ch chan<- StateChange) {
	for {
		select {
		case <-ctx.Done():
			return
		case units, ok := <-evCh:
			if !ok {
				return
			}

			for name, status := range units {
				if status == nil {
					continue
				}

				ch <- StateChange{
					UnitName:    name,
					ActiveState: status.ActiveState,
					SubState:    status.SubState,
				}
			}
		case err, ok := <-errCh:
			if !ok {
				return
			}

			if err != nil {
				slog.Error("D-Bus subscription error", "error", err)
			}
		}
	}
}
