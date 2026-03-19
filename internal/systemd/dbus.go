//go:build linux

package systemd

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
)

type DBusManager struct {
	conn *dbus.Conn
	mu   sync.Mutex
}

func NewDBusManager(ctx context.Context) (*DBusManager, error) {
	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to system D-Bus: %w", err)
	}

	return &DBusManager{conn: conn}, nil
}

func (m *DBusManager) Start(ctx context.Context, unit string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan string, 1)

	_, err := m.conn.StartUnitContext(ctx, unit, "replace", ch)
	if err != nil {
		return fmt.Errorf("starting unit %s: %w", unit, err)
	}

	result := <-ch
	if result != "done" {
		return fmt.Errorf("starting unit %s: job result %s", unit, result)
	}

	return nil
}

func (m *DBusManager) Stop(ctx context.Context, unit string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan string, 1)

	_, err := m.conn.StopUnitContext(ctx, unit, "replace", ch)
	if err != nil {
		return fmt.Errorf("stopping unit %s: %w", unit, err)
	}

	result := <-ch
	if result != "done" {
		return fmt.Errorf("stopping unit %s: job result %s", unit, result)
	}

	return nil
}

func (m *DBusManager) Restart(ctx context.Context, unit string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan string, 1)

	_, err := m.conn.RestartUnitContext(ctx, unit, "replace", ch)
	if err != nil {
		return fmt.Errorf("restarting unit %s: %w", unit, err)
	}

	result := <-ch
	if result != "done" {
		return fmt.Errorf("restarting unit %s: job result %s", unit, result)
	}

	return nil
}

func (m *DBusManager) GetUnitState(ctx context.Context, unit string) (*UnitState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	props, err := m.conn.GetUnitPropertiesContext(ctx, unit)
	if err != nil {
		return nil, fmt.Errorf("getting properties for %s: %w", unit, err)
	}

	return &UnitState{
		Name:        unit,
		ActiveState: stringProp(props, "ActiveState"),
		SubState:    stringProp(props, "SubState"),
		LoadState:   stringProp(props, "LoadState"),
	}, nil
}

func (m *DBusManager) GetTimerLastTrigger(ctx context.Context, unit string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	props, err := m.conn.GetUnitTypePropertiesContext(ctx, unit, "Timer")
	if err != nil {
		return time.Time{}, fmt.Errorf("getting timer properties for %s: %w", unit, err)
	}

	usec, ok := props["LastTriggerUSec"]
	if !ok {
		return time.Time{}, nil
	}

	v, ok := usec.(uint64)
	if !ok {
		return time.Time{}, nil
	}

	if v == 0 {
		return time.Time{}, nil
	}

	return time.UnixMicro(int64(v)), nil
}

func (m *DBusManager) ListUnits(ctx context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	units, err := m.conn.ListUnitsByPatternsContext(ctx, nil, []string{fmt.Sprintf("%s*", prefix)})
	if err != nil {
		return nil, fmt.Errorf("listing units with prefix %s: %w", prefix, err)
	}

	var names []string

	for _, u := range units {
		if u.LoadState == "loaded" {
			names = append(names, u.Name)
		}
	}

	return names, nil
}

func (m *DBusManager) WatchUnit(ctx context.Context, unit string, ch chan<- StateChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.conn.Subscribe(); err != nil {
		return fmt.Errorf("subscribing to D-Bus signals: %w", err)
	}

	evCh, errCh := m.conn.SubscribeUnitsCustomContext(ctx, 1, 0, func(u1, _ *dbus.UnitStatus) bool {
		return u1.Name == unit
	}, func(_ string) bool {
		return true
	})

	go processDBusEvents(ctx, evCh, errCh, ch)

	return nil
}

func processDBusEvents(ctx context.Context, evCh <-chan map[string]*dbus.UnitStatus, errCh <-chan error, ch chan<- StateChange) {
	for {
		select {
		case <-ctx.Done():
			return
		case units := <-evCh:
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
		case err := <-errCh:
			if err != nil {
				slog.Error("D-Bus subscription error", "error", err)
			}
		}
	}
}

func (m *DBusManager) Close() error {
	m.conn.Close()

	return nil
}

func stringProp(props map[string]interface{}, key string) string {
	v, ok := props[key]
	if !ok {
		return ""
	}

	s, ok := v.(string)
	if !ok {
		return ""
	}

	return s
}
