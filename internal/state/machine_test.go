package state

import (
	"strings"
	"testing"
	"time"
)

func TestLegalLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.FixedZone("test", 8*60*60))
	machine := NewMachine(func() time.Time { return now })
	for _, next := range []State{NetworkWait, Registering, Configuring, Connecting, Online, Degraded, Recovering, Online} {
		if err := machine.Transition(next, "test_transition"); err != nil {
			t.Fatalf("Transition(%s) error = %v", next, err)
		}
	}
	if got := machine.Current(); got != Online {
		t.Fatalf("Current() = %s, want %s", got, Online)
	}
	if got := machine.History()[0].At.Location(); got != time.UTC {
		t.Fatalf("history timestamp location = %v, want UTC", got)
	}
}

func TestIllegalTransitions(t *testing.T) {
	tests := []struct {
		name string
		from []State
		to   State
	}{
		{name: "skip startup", to: Online},
		{name: "no reboot transition", from: []State{NetworkWait}, to: Boot},
		{name: "safe mode requires recovery", from: []State{NetworkWait, SafeMode}, to: Online},
		{name: "same state is not a transition", from: []State{NetworkWait}, to: NetworkWait},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machine := NewMachine(time.Now)
			for _, next := range tt.from {
				if err := machine.Transition(next, "setup"); err != nil {
					t.Fatalf("setup transition: %v", err)
				}
			}
			err := machine.Transition(tt.to, "test")
			if err == nil || !strings.Contains(err.Error(), "illegal state transition") {
				t.Fatalf("Transition(%s) error = %v", tt.to, err)
			}
		})
	}
}

func TestTransitionRequiresReason(t *testing.T) {
	machine := NewMachine(time.Now)
	if err := machine.Transition(NetworkWait, ""); err == nil {
		t.Fatal("Transition() error = nil")
	}
	if machine.Current() != Boot {
		t.Fatal("failed transition changed state")
	}
}
