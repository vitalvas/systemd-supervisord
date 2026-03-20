package systemd

import (
	"context"
	"log/slog"
	"time"
)

type DryRunManager struct {
	real Manager
}

func NewDryRunManager(mgr Manager) *DryRunManager {
	return &DryRunManager{real: mgr}
}

func (m *DryRunManager) Start(_ context.Context, unit string) error {
	slog.Warn("dry-run: would start unit", "unit", unit)

	return nil
}

func (m *DryRunManager) Stop(_ context.Context, unit string) error {
	slog.Warn("dry-run: would stop unit", "unit", unit)

	return nil
}

func (m *DryRunManager) Restart(_ context.Context, unit string) error {
	slog.Warn("dry-run: would restart unit", "unit", unit)

	return nil
}

func (m *DryRunManager) GetUnitState(ctx context.Context, unit string) (*UnitState, error) {
	return m.real.GetUnitState(ctx, unit)
}

func (m *DryRunManager) GetTimerLastTrigger(ctx context.Context, unit string) (time.Time, error) {
	return m.real.GetTimerLastTrigger(ctx, unit)
}

func (m *DryRunManager) ListUnits(ctx context.Context, prefix string) ([]string, error) {
	return m.real.ListUnits(ctx, prefix)
}

func (m *DryRunManager) WatchUnit(ctx context.Context, unit string, ch chan<- StateChange) error {
	return m.real.WatchUnit(ctx, unit, ch)
}

func (m *DryRunManager) Close() error {
	return m.real.Close()
}
