package agent

import (
	"context"
	"testing"
	"time"

	"edgebox/internal/health"
	"edgebox/internal/state"
	"edgebox/internal/store"
	"edgebox/internal/transport"
)

type fixedManagementObserver struct{ observation health.Observation }

func (o fixedManagementObserver) Observe(context.Context) health.Observation { return o.observation }

type fixedTransport struct{ snapshot transport.Snapshot }

func (fixedTransport) Start(context.Context) error                   { return nil }
func (fixedTransport) Stop(context.Context) error                    { return nil }
func (t fixedTransport) Snapshot(context.Context) transport.Snapshot { return t.snapshot }

func TestManagementHealthIsIndependentFromTransport(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	stateStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := health.NewReport(now)
	agent, err := New(
		stateStore,
		state.NewMachine(func() time.Time { return now }),
		report,
		fixedTransport{snapshot: transport.Snapshot{Health: health.Observation{
			Domain: health.DomainTransport, Status: health.StatusUnhealthy, ObservedAt: now, ReasonCode: "transport_down",
		}}},
		fixedManagementObserver{observation: health.Observation{
			Domain: health.DomainManagement, Status: health.StatusHealthy, ObservedAt: now, ReasonCode: "tunnel_observed",
		}},
		nil,
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := agent.HealthSnapshot()
	if snapshot[health.DomainTransport].Status != health.StatusUnhealthy {
		t.Fatalf("transport status = %s", snapshot[health.DomainTransport].Status)
	}
	if snapshot[health.DomainManagement].Status != health.StatusHealthy {
		t.Fatalf("management status was coupled to transport: %s", snapshot[health.DomainManagement].Status)
	}
}
