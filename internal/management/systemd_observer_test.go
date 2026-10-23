package management

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"edgebox/internal/health"
)

type fakeStateReader struct {
	state ServiceState
	err   error
	unit  string
}

func (r *fakeStateReader) ReadState(_ context.Context, unit string) (ServiceState, error) {
	r.unit = unit
	return r.state, r.err
}

func TestSystemdObserverMapsServiceStateWithoutControllingIt(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		state  ServiceState
		err    error
		status health.Status
		reason string
	}{
		{name: "active", state: ServiceState{LoadState: "loaded", ActiveState: "active", SubState: "running"}, status: health.StatusHealthy, reason: "management_service_active"},
		{name: "failed", state: ServiceState{LoadState: "loaded", ActiveState: "failed", SubState: "failed"}, status: health.StatusUnhealthy, reason: "management_service_failed"},
		{name: "transitioning", state: ServiceState{LoadState: "loaded", ActiveState: "activating", SubState: "auto-restart"}, status: health.StatusDegraded, reason: "management_service_transitioning"},
		{name: "not found", state: ServiceState{LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}, status: health.StatusUnhealthy, reason: "management_service_not_found"},
		{name: "observer failure", err: errors.New("test failure"), status: health.StatusUnknown, reason: "management_observation_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeStateReader{state: test.state, err: test.err}
			observer, err := NewSystemdObserver("edge-management.service", reader, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			observation := observer.Observe(context.Background())
			if observation.Status != test.status || observation.ReasonCode != test.reason {
				t.Fatalf("observation = %+v", observation)
			}
			if reader.unit != "edge-management.service" {
				t.Fatalf("observed unit = %q", reader.unit)
			}
		})
	}
}

func TestSystemctlReaderExecutesOnlyShowWithValidatedUnit(t *testing.T) {
	var gotName string
	var gotArguments []string
	reader := &SystemctlStateReader{
		systemctlPath: "/test/systemctl",
		output: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			gotName = name
			gotArguments = append([]string(nil), arguments...)
			return []byte("LoadState=loaded\nActiveState=active\nSubState=running\n"), nil
		},
	}
	state, err := reader.ReadState(context.Background(), "edge-management.service")
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveState != "active" || gotName != "/test/systemctl" {
		t.Fatalf("state=%+v command=%q", state, gotName)
	}
	wantArguments := []string{"show", "--no-pager", "--property=LoadState", "--property=ActiveState", "--property=SubState", "--", "edge-management.service"}
	if !reflect.DeepEqual(gotArguments, wantArguments) {
		t.Fatalf("arguments = %v", gotArguments)
	}
	if _, err := reader.ReadState(context.Background(), "bad.service\nstart evil.service"); err == nil {
		t.Fatal("ReadState() accepted injected unit name")
	}
}

func TestObserverInterfaceHasNoLifecycleControl(t *testing.T) {
	type observeOnly interface {
		Observe(context.Context) health.Observation
	}
	var observer Observer = NewUnknownObserver(time.Now)
	if _, ok := observer.(observeOnly); !ok {
		t.Fatal("management observer does not implement observation")
	}
	observerType := reflect.TypeOf((*Observer)(nil)).Elem()
	if observerType.NumMethod() != 1 || observerType.Method(0).Name != "Observe" {
		t.Fatalf("management observer methods changed: %v", observerType)
	}
}
