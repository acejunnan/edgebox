// Package agent wires the Phase 1 safety models together without performing
// any networking or supervising the management service.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"edgebox/internal/health"
	"edgebox/internal/management"
	"edgebox/internal/state"
	"edgebox/internal/store"
	"edgebox/internal/transport"
)

type Agent struct {
	store      *store.Store
	machine    *state.Machine
	health     *health.Report
	transport  transport.Transport
	management management.Observer
	logger     *slog.Logger
	now        func() time.Time
}

func New(
	stateStore *store.Store,
	machine *state.Machine,
	report *health.Report,
	packetTransport transport.Transport,
	managementObserver management.Observer,
	logger *slog.Logger,
	now func() time.Time,
) (*Agent, error) {
	if stateStore == nil || machine == nil || report == nil || packetTransport == nil || managementObserver == nil {
		return nil, errors.New("agent dependencies must not be nil")
	}
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	return &Agent{
		store:      stateStore,
		machine:    machine,
		health:     report,
		transport:  packetTransport,
		management: managementObserver,
		logger:     logger,
		now:        now,
	}, nil
}

func (a *Agent) Start(ctx context.Context) error {
	if a.machine.Current() != state.Boot {
		return errors.New("agent can only start from BOOT")
	}
	recovery, err := a.store.Recover()
	if err != nil {
		return err
	}
	if err := a.machine.Transition(state.NetworkWait, "startup_recovery_complete"); err != nil {
		return err
	}
	if err := a.RefreshHealth(ctx); err != nil {
		return err
	}
	interruptedTransactions := recovery.InterruptedTransactions()
	if len(interruptedTransactions) > 0 {
		if err := a.machine.Transition(state.Degraded, "incomplete_transaction_detected"); err != nil {
			return err
		}
		if err := a.setAgentStateHealth(health.StatusDegraded, "incomplete_transaction_detected"); err != nil {
			return err
		}
		a.logger.Warn("incomplete transaction requires a future rollback executor", "pending_transactions", len(interruptedTransactions))
	}
	if terminal := len(recovery.Reconciliations) - len(interruptedTransactions); terminal > 0 {
		a.logger.Info("transaction journals have deterministic reconciliation actions", "non_interrupted_reconciliations", terminal)
	}
	a.logger.Info("phase 1 agent foundation started", "removed_temporary_files", len(recovery.RemovedTemporaryFiles))
	return nil
}

func (a *Agent) RefreshHealth(ctx context.Context) error {
	managementObservation := a.management.Observe(ctx)
	if managementObservation.Domain != health.DomainManagement {
		return errors.New("management observer returned the wrong health domain")
	}
	if err := a.health.Set(managementObservation); err != nil {
		return err
	}

	transportSnapshot := a.transport.Snapshot(ctx)
	if transportSnapshot.Health.Domain != health.DomainTransport {
		return errors.New("transport returned the wrong health domain")
	}
	if err := a.health.Set(transportSnapshot.Health); err != nil {
		return err
	}

	status, reason := stateHealth(a.machine.Current())
	return a.setAgentStateHealth(status, reason)
}

func (a *Agent) HealthSnapshot() map[health.Domain]health.Observation {
	return a.health.Snapshot()
}

func (a *Agent) State() state.State { return a.machine.Current() }

func (a *Agent) setAgentStateHealth(status health.Status, reason string) error {
	return a.health.Set(health.Observation{
		Domain:     health.DomainAgentState,
		Status:     status,
		ObservedAt: a.now().UTC(),
		ReasonCode: reason,
		Details:    map[string]string{"state": string(a.machine.Current())},
	})
}

func stateHealth(current state.State) (health.Status, string) {
	switch current {
	case state.Online:
		return health.StatusHealthy, "online"
	case state.Degraded, state.Recovering:
		return health.StatusDegraded, "agent_degraded"
	case state.SafeMode:
		return health.StatusDegraded, "safe_mode"
	default:
		return health.StatusUnknown, "startup_in_progress"
	}
}

type discardWriter struct{}

func (discardWriter) Write(data []byte) (int, error) { return len(data), nil }
