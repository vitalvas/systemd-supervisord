package systemd

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

type CtlManager struct {
	pollInterval time.Duration
}

func NewCtlManager() *CtlManager {
	return &CtlManager{
		pollInterval: 2 * time.Second,
	}
}

func (m *CtlManager) Start(ctx context.Context, unit string) error {
	return m.runSystemctl(ctx, "start", unit)
}

func (m *CtlManager) Stop(ctx context.Context, unit string) error {
	return m.runSystemctl(ctx, "stop", unit)
}

func (m *CtlManager) Restart(ctx context.Context, unit string) error {
	return m.runSystemctl(ctx, "restart", unit)
}

func (m *CtlManager) GetUnitState(ctx context.Context, unit string) (*UnitState, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit,
		"--property=ActiveState,SubState,LoadState").Output()
	if err != nil {
		return nil, fmt.Errorf("running systemctl show for %s: %w", unit, err)
	}

	props := parseProperties(string(out))

	return &UnitState{
		Name:        unit,
		ActiveState: props["ActiveState"],
		SubState:    props["SubState"],
		LoadState:   props["LoadState"],
	}, nil
}

func (m *CtlManager) GetTimerLastTrigger(ctx context.Context, unit string) (time.Time, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit,
		"--property=LastTriggerUSec").Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("getting timer properties for %s: %w", unit, err)
	}

	props := parseProperties(string(out))

	val, ok := props["LastTriggerUSec"]
	if !ok || val == "" || val == "n/a" {
		return time.Time{}, nil
	}

	t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", val)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing LastTriggerUSec for %s: %w", unit, err)
	}

	return t, nil
}

func (m *CtlManager) ListUnits(ctx context.Context, prefix string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "list-units",
		"--type=service,timer", "--no-legend", "--no-pager", "--plain",
		fmt.Sprintf("%s*", prefix)).Output()
	if err != nil {
		return nil, fmt.Errorf("listing units with prefix %s: %w", prefix, err)
	}

	var names []string

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}

	return names, nil
}

func (m *CtlManager) WatchUnit(ctx context.Context, unit string, ch chan<- StateChange) error {
	go func() {
		var lastState string

		ticker := time.NewTicker(m.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				state, err := m.GetUnitState(ctx, unit)
				if err != nil {
					slog.Error("polling unit state", "unit", unit, "error", err)

					continue
				}

				current := fmt.Sprintf("%s/%s", state.ActiveState, state.SubState)
				if current != lastState {
					lastState = current

					ch <- StateChange{
						UnitName:    unit,
						ActiveState: state.ActiveState,
						SubState:    state.SubState,
					}
				}
			}
		}
	}()

	return nil
}

func (m *CtlManager) Close() error {
	return nil
}

func (m *CtlManager) runSystemctl(ctx context.Context, action, unit string) error {
	out, err := exec.CommandContext(ctx, "systemctl", action, unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s %s: %s: %w", action, unit, strings.TrimSpace(string(out)), err)
	}

	return nil
}

func parseProperties(output string) map[string]string {
	props := make(map[string]string)

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		props[key] = value
	}

	return props
}
