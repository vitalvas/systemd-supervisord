package statemanager

import "time"

type EventType int

const (
	EventStateChanged EventType = iota
	EventHealthChanged
)

type Event struct {
	Type        EventType
	UnitName    string
	ActiveState string
	SubState    string
	Healthy     *bool
	Timestamp   time.Time
}
