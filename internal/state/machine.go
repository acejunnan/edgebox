// Package state contains the explicit EdgeBox agent lifecycle.
package state

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type State string

const (
	Boot        State = "BOOT"
	NetworkWait State = "NETWORK_WAIT"
	Registering State = "REGISTERING"
	Configuring State = "CONFIGURING"
	Connecting  State = "CONNECTING"
	Online      State = "ONLINE"
	Degraded    State = "DEGRADED"
	Recovering  State = "RECOVERING"
	SafeMode    State = "SAFE_MODE"
)

var transitions = map[State]map[State]struct{}{
	Boot:        set(NetworkWait),
	NetworkWait: set(Registering, Degraded, SafeMode),
	Registering: set(Configuring, NetworkWait, Degraded, SafeMode),
	Configuring: set(Connecting, NetworkWait, Degraded, SafeMode),
	Connecting:  set(Online, NetworkWait, Degraded, SafeMode),
	Online:      set(NetworkWait, Degraded, SafeMode),
	Degraded:    set(NetworkWait, Recovering, SafeMode),
	Recovering:  set(NetworkWait, Configuring, Connecting, Online, Degraded, SafeMode),
	SafeMode:    set(Recovering),
}

type Transition struct {
	From   State
	To     State
	At     time.Time
	Reason string
}

type Machine struct {
	mu      sync.RWMutex
	current State
	history []Transition
	now     func() time.Time
}

func NewMachine(now func() time.Time) *Machine {
	if now == nil {
		now = time.Now
	}
	return &Machine{current: Boot, now: now}
}

func (m *Machine) Current() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

func (m *Machine) Transition(to State, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if reason == "" {
		return errors.New("state transition reason is required")
	}
	if _, ok := transitions[m.current][to]; !ok {
		return fmt.Errorf("illegal state transition %s -> %s", m.current, to)
	}
	m.history = append(m.history, Transition{
		From:   m.current,
		To:     to,
		At:     m.now().UTC(),
		Reason: reason,
	})
	m.current = to
	return nil
}

func (m *Machine) History() []Transition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Transition(nil), m.history...)
}

func set(states ...State) map[State]struct{} {
	result := make(map[State]struct{}, len(states))
	for _, state := range states {
		result[state] = struct{}{}
	}
	return result
}
