package management

import (
	"strings"
	"testing"
)

func validRegistry() PortRegistry {
	return PortRegistry{
		SchemaVersion: RegistrySchemaVersion,
		AllowedRange:  PortRange{Start: 22000, End: 22999},
		ReservedPorts: []uint16{22999, 22000},
		Assignments: []PortAssignment{
			{DeviceID: "lab-device-b", Port: 22202},
			{DeviceID: "lab-device-a", Port: 22201},
		},
	}
}

func TestRegistryDeterministicRoundTrip(t *testing.T) {
	data, err := validRegistry().MarshalDeterministic()
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"22000", "22999", "lab-device-a", "lab-device-b"}
	last := -1
	for _, want := range wantOrder {
		position := strings.Index(string(data), want)
		if position <= last {
			t.Fatalf("deterministic registry order missing for %q:\n%s", want, data)
		}
		last = position
	}
	parsed, err := ParsePortRegistry(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(validRegistry()) {
		t.Fatal("registry did not round trip")
	}
}

func TestRegistryRejectsDuplicateAndReservedPorts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PortRegistry)
		want   string
	}{
		{name: "duplicate port", mutate: func(r *PortRegistry) { r.Assignments[1].Port = r.Assignments[0].Port }, want: "already assigned"},
		{name: "duplicate device", mutate: func(r *PortRegistry) { r.Assignments[1].DeviceID = r.Assignments[0].DeviceID }, want: "duplicate device"},
		{name: "reserved port", mutate: func(r *PortRegistry) { r.Assignments[0].Port = 22000 }, want: "reserved"},
		{name: "outside range", mutate: func(r *PortRegistry) { r.Assignments[0].Port = 23000 }, want: "outside allowed range"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := validRegistry()
			test.mutate(&registry)
			if err := registry.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRegistryRejectsInvalidDeviceIDs(t *testing.T) {
	for _, deviceID := range []string{"", "-leading", "bad device", "bad\nassignment", strings.Repeat("a", 65)} {
		registry := validRegistry()
		registry.Assignments[0].DeviceID = deviceID
		if err := registry.Validate(); err == nil {
			t.Errorf("Validate() accepted device ID %q", deviceID)
		}
	}
}

func TestRegistryRejectsUnknownJSONFields(t *testing.T) {
	input := `{"schema_version":1,"allowed_port_range":{"start":22000,"end":22999},"assignments":[],"injected":true}`
	if _, err := ParsePortRegistry(strings.NewReader(input)); err == nil {
		t.Fatal("ParsePortRegistry() accepted unknown field")
	}
}
