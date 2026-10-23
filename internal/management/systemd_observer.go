package management

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"edgebox/internal/health"
)

var systemdUnitPattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]+\.service$`)

type ServiceState struct {
	LoadState   string
	ActiveState string
	SubState    string
}

type ServiceStateReader interface {
	ReadState(context.Context, string) (ServiceState, error)
}

type SystemdObserver struct {
	unit   string
	reader ServiceStateReader
	now    func() time.Time
}

func NewSystemdObserver(unit string, reader ServiceStateReader, now func() time.Time) (*SystemdObserver, error) {
	if !systemdUnitPattern.MatchString(unit) {
		return nil, errors.New("management systemd unit name is invalid")
	}
	if reader == nil {
		return nil, errors.New("management service-state reader is required")
	}
	if now == nil {
		now = time.Now
	}
	return &SystemdObserver{unit: unit, reader: reader, now: now}, nil
}

func (o *SystemdObserver) Observe(ctx context.Context) health.Observation {
	state, err := o.reader.ReadState(ctx, o.unit)
	observation := health.Observation{
		Domain:     health.DomainManagement,
		ObservedAt: o.now().UTC(),
	}
	if err != nil {
		observation.Status = health.StatusUnknown
		observation.ReasonCode = "management_observation_failed"
		return observation
	}
	observation.Details = map[string]string{
		"unit":         o.unit,
		"load_state":   state.LoadState,
		"active_state": state.ActiveState,
		"sub_state":    state.SubState,
	}
	switch {
	case state.LoadState == "not-found":
		observation.Status = health.StatusUnhealthy
		observation.ReasonCode = "management_service_not_found"
	case state.ActiveState == "active":
		observation.Status = health.StatusHealthy
		observation.ReasonCode = "management_service_active"
	case state.ActiveState == "activating" || state.ActiveState == "deactivating":
		observation.Status = health.StatusDegraded
		observation.ReasonCode = "management_service_transitioning"
	case state.ActiveState == "failed":
		observation.Status = health.StatusUnhealthy
		observation.ReasonCode = "management_service_failed"
	default:
		observation.Status = health.StatusUnhealthy
		observation.ReasonCode = "management_service_inactive"
	}
	return observation
}

type commandOutput func(context.Context, string, ...string) ([]byte, error)

// SystemctlStateReader is an observation-only adapter. It invokes only
// `systemctl show`; it has no process-control methods.
type SystemctlStateReader struct {
	systemctlPath string
	output        commandOutput
}

func NewSystemctlStateReader() *SystemctlStateReader {
	return &SystemctlStateReader{
		systemctlPath: "/usr/bin/systemctl",
		output: func(ctx context.Context, name string, arguments ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, arguments...).Output()
		},
	}
}

func (r *SystemctlStateReader) ReadState(ctx context.Context, unit string) (ServiceState, error) {
	if !systemdUnitPattern.MatchString(unit) {
		return ServiceState{}, errors.New("management systemd unit name is invalid")
	}
	output, err := r.output(ctx, r.systemctlPath,
		"show",
		"--no-pager",
		"--property=LoadState",
		"--property=ActiveState",
		"--property=SubState",
		"--",
		unit,
	)
	if err != nil {
		return ServiceState{}, fmt.Errorf("observe management service state: %w", err)
	}
	state := ServiceState{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch key {
		case "LoadState":
			state.LoadState = value
		case "ActiveState":
			state.ActiveState = value
		case "SubState":
			state.SubState = value
		}
	}
	if state.LoadState == "" || state.ActiveState == "" || state.SubState == "" {
		return ServiceState{}, errors.New("systemctl returned incomplete management service state")
	}
	return state, nil
}
