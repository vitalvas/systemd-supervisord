package statemanager

import (
	"sync"
	"time"
)

type UnitStatus struct {
	UnitName       string
	ActiveState    string
	SubState       string
	Healthy        *bool
	RestartCount   int
	LastTransition time.Time
}

type StateManager struct {
	units    map[string]*UnitStatus
	mu       sync.RWMutex
	eventCh  chan Event
	handlers []func(Event)
}

func New(eventBufferSize int) *StateManager {
	return &StateManager{
		units:   make(map[string]*UnitStatus),
		eventCh: make(chan Event, eventBufferSize),
	}
}

func (sm *StateManager) Register(unit string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.units[unit] = &UnitStatus{
		UnitName: unit,
	}
}

func (sm *StateManager) Unregister(unit string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	delete(sm.units, unit)
}

func (sm *StateManager) OnEvent(handler func(Event)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.handlers = append(sm.handlers, handler)
}

func (sm *StateManager) UpdateState(unit, activeState, subState string) {
	sm.mu.Lock()

	status, ok := sm.units[unit]
	if !ok {
		sm.mu.Unlock()

		return
	}

	if status.ActiveState == activeState && status.SubState == subState {
		sm.mu.Unlock()

		return
	}

	status.ActiveState = activeState
	status.SubState = subState
	status.LastTransition = time.Now()

	ev := Event{
		Type:        EventStateChanged,
		UnitName:    unit,
		ActiveState: activeState,
		SubState:    subState,
		Timestamp:   status.LastTransition,
	}

	sm.mu.Unlock()

	sm.emit(ev)
}

func (sm *StateManager) UpdateHealth(unit string, healthy bool) {
	sm.mu.Lock()

	status, ok := sm.units[unit]
	if !ok {
		sm.mu.Unlock()

		return
	}

	if status.Healthy != nil && *status.Healthy == healthy {
		sm.mu.Unlock()

		return
	}

	status.Healthy = &healthy

	ev := Event{
		Type:      EventHealthChanged,
		UnitName:  unit,
		Healthy:   &healthy,
		Timestamp: time.Now(),
	}

	sm.mu.Unlock()

	sm.emit(ev)
}

func (sm *StateManager) IncrementRestartCount(unit string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if status, ok := sm.units[unit]; ok {
		status.RestartCount++
	}
}

func (sm *StateManager) ResetRestartCount(unit string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if status, ok := sm.units[unit]; ok {
		status.RestartCount = 0
	}
}

func (sm *StateManager) GetStatus(unit string) *UnitStatus {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	status, ok := sm.units[unit]
	if !ok {
		return nil
	}

	cp := *status

	return &cp
}

func (sm *StateManager) GetAllStatuses() []UnitStatus {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	statuses := make([]UnitStatus, 0, len(sm.units))
	for _, s := range sm.units {
		statuses = append(statuses, *s)
	}

	return statuses
}

func (sm *StateManager) Summary() (total, healthy, unhealthy int) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	total = len(sm.units)

	for _, s := range sm.units {
		if s.Healthy == nil {
			continue
		}

		if *s.Healthy {
			healthy++
		} else {
			unhealthy++
		}
	}

	return total, healthy, unhealthy
}

func (sm *StateManager) emit(ev Event) {
	for _, h := range sm.handlers {
		h(ev)
	}
}
