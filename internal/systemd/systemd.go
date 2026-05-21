package systemd

import (
	"context"
	"time"
)

const (
	ActiveStateActive       = "active"
	ActiveStateReloading    = "reloading"
	ActiveStateInactive     = "inactive"
	ActiveStateFailed       = "failed"
	ActiveStateActivating   = "activating"
	ActiveStateDeactivating = "deactivating"
	ActiveStateMaintenance  = "maintenance"
)

const (
	SubStateRunning     = "running"
	SubStateDead        = "dead"
	SubStateExited      = "exited"
	SubStateFailed      = "failed"
	SubStateWaiting     = "waiting"
	SubStateListening   = "listening"
	SubStateStartPre    = "start-pre"
	SubStateStart       = "start"
	SubStateStartPost   = "start-post"
	SubStateStopPre     = "stop-pre"
	SubStateStop        = "stop"
	SubStateStopPost    = "stop-post"
	SubStateAutoRestart = "auto-restart"
	SubStateReload      = "reload"
)

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
	GetTimerLastTrigger(ctx context.Context, unit string) (time.Time, error)
	ListUnits(ctx context.Context, prefix string) ([]string, error)
	WatchUnit(ctx context.Context, unit string, ch chan<- StateChange) error
	Close() error
}
