package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
)

const RegistrySchemaVersion = 1

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type PortRange struct {
	Start uint16 `json:"start"`
	End   uint16 `json:"end"`
}

type PortAssignment struct {
	DeviceID string `json:"device_id"`
	Port     uint16 `json:"port"`
}

type PortRegistry struct {
	SchemaVersion int              `json:"schema_version"`
	AllowedRange  PortRange        `json:"allowed_port_range"`
	ReservedPorts []uint16         `json:"reserved_ports,omitempty"`
	Assignments   []PortAssignment `json:"assignments"`
}

func ParsePortRegistry(reader io.Reader) (PortRegistry, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var registry PortRegistry
	if err := decoder.Decode(&registry); err != nil {
		return PortRegistry{}, fmt.Errorf("decode management port registry: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return PortRegistry{}, errors.New("management port registry contains trailing JSON values")
		}
		return PortRegistry{}, fmt.Errorf("decode trailing management port registry data: %w", err)
	}
	return registry.Normalize()
}

func (r PortRegistry) Normalize() (PortRegistry, error) {
	r.ReservedPorts = append([]uint16(nil), r.ReservedPorts...)
	r.Assignments = append([]PortAssignment(nil), r.Assignments...)
	sort.Slice(r.ReservedPorts, func(i, j int) bool { return r.ReservedPorts[i] < r.ReservedPorts[j] })
	sort.Slice(r.Assignments, func(i, j int) bool { return r.Assignments[i].DeviceID < r.Assignments[j].DeviceID })
	if err := r.Validate(); err != nil {
		return PortRegistry{}, err
	}
	return r, nil
}

func (r PortRegistry) Validate() error {
	var errs []error
	if r.SchemaVersion != RegistrySchemaVersion {
		errs = append(errs, fmt.Errorf("registry schema_version: unsupported value %d", r.SchemaVersion))
	}
	if r.AllowedRange.Start < 1024 || r.AllowedRange.End < r.AllowedRange.Start {
		errs = append(errs, errors.New("allowed_port_range: must be an ordered unprivileged TCP port range"))
	}

	reserved := make(map[uint16]struct{}, len(r.ReservedPorts))
	for index, port := range r.ReservedPorts {
		if !r.AllowedRange.contains(port) {
			errs = append(errs, fmt.Errorf("reserved_ports[%d]: port %d is outside allowed range", index, port))
		}
		if _, exists := reserved[port]; exists {
			errs = append(errs, fmt.Errorf("reserved_ports[%d]: duplicate port %d", index, port))
		}
		reserved[port] = struct{}{}
	}

	deviceIDs := make(map[string]struct{}, len(r.Assignments))
	ports := make(map[uint16]string, len(r.Assignments))
	for index, assignment := range r.Assignments {
		if !deviceIDPattern.MatchString(assignment.DeviceID) {
			errs = append(errs, fmt.Errorf("assignments[%d].device_id: invalid device ID", index))
		}
		if _, exists := deviceIDs[assignment.DeviceID]; exists {
			errs = append(errs, fmt.Errorf("assignments[%d].device_id: duplicate device ID %q", index, assignment.DeviceID))
		}
		deviceIDs[assignment.DeviceID] = struct{}{}
		if !r.AllowedRange.contains(assignment.Port) {
			errs = append(errs, fmt.Errorf("assignments[%d].port: port %d is outside allowed range", index, assignment.Port))
		}
		if _, isReserved := reserved[assignment.Port]; isReserved {
			errs = append(errs, fmt.Errorf("assignments[%d].port: port %d is reserved", index, assignment.Port))
		}
		if existingDevice, exists := ports[assignment.Port]; exists {
			errs = append(errs, fmt.Errorf("assignments[%d].port: port %d is already assigned to %q", index, assignment.Port, existingDevice))
		}
		ports[assignment.Port] = assignment.DeviceID
	}
	return errors.Join(errs...)
}

func (r PortRegistry) Assignment(deviceID string) (PortAssignment, bool) {
	for _, assignment := range r.Assignments {
		if assignment.DeviceID == deviceID {
			return assignment, true
		}
	}
	return PortAssignment{}, false
}

func (r PortRegistry) MarshalDeterministic() ([]byte, error) {
	normalized, err := r.Normalize()
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode management port registry: %w", err)
	}
	return append(data, '\n'), nil
}

func (r PortRegistry) Equal(other PortRegistry) bool {
	left, leftErr := r.MarshalDeterministic()
	right, rightErr := other.MarshalDeterministic()
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func (r PortRange) contains(port uint16) bool {
	return port >= r.Start && port <= r.End
}
