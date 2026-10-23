// Package transport defines the generic packet-transport boundary. It has no
// WireGuard, routing, NAT, or management concepts.
package transport

import (
	"context"
	"errors"
	"time"

	"edgebox/internal/health"
)

type EndpointKind string

const (
	EndpointKernelInterface EndpointKind = "kernel_interface"
	EndpointUserspacePacket EndpointKind = "userspace_packet"
)

// PacketEndpoint is an opaque packet boundary. ID is interpreted only by the
// transport/data-plane adapter for Kind and is not necessarily an interface.
type PacketEndpoint struct {
	Kind EndpointKind
	ID   string
}

type Snapshot struct {
	Name         string
	Running      bool
	Endpoint     *PacketEndpoint
	EffectiveMTU int
	Health       health.Observation
}

type Transport interface {
	Start(context.Context) error
	Stop(context.Context) error
	Snapshot(context.Context) Snapshot
}

// Unconfigured is the Phase 1 transport used by orchestration before a real
// transport implementation is selected. It performs no system operations.
type Unconfigured struct {
	now func() time.Time
}

func NewUnconfigured(now func() time.Time) *Unconfigured {
	if now == nil {
		now = time.Now
	}
	return &Unconfigured{now: now}
}

func (u *Unconfigured) Start(context.Context) error {
	return errors.New("transport is not configured")
}

func (u *Unconfigured) Stop(context.Context) error { return nil }

func (u *Unconfigured) Snapshot(context.Context) Snapshot {
	return Snapshot{
		Name: "unconfigured",
		Health: health.Observation{
			Domain:     health.DomainTransport,
			Status:     health.StatusUnknown,
			ObservedAt: u.now().UTC(),
			ReasonCode: "transport_not_configured",
		},
	}
}
