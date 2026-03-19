package systemd

import "context"

type UnitState struct {
	Name        string
	ActiveState string
	SubState    string
	LoadState   string
}

type StateChange struct {
	UnitName    string
	ActiveState string
	SubState    string
}

type Manager interface {
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
	Restart(ctx context.Context, unit string) error
	GetUnitState(ctx context.Context, unit string) (*UnitState, error)
	ListUnits(ctx context.Context, prefix string) ([]string, error)
	WatchUnit(ctx context.Context, unit string, ch chan<- StateChange) error
	Close() error
}
