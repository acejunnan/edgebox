// Package management defines observation of the independently supervised
// management plane. It deliberately has no Start, Stop, or Restart operation.
package management

import (
	"context"
	"time"

	"edgebox/internal/health"
)

type Observer interface {
	Observe(context.Context) health.Observation
}

type UnknownObserver struct {
	now func() time.Time
}

func NewUnknownObserver(now func() time.Time) *UnknownObserver {
	if now == nil {
		now = time.Now
	}
	return &UnknownObserver{now: now}
}

func (o *UnknownObserver) Observe(context.Context) health.Observation {
	return health.Observation{
		Domain:     health.DomainManagement,
		Status:     health.StatusUnknown,
		ObservedAt: o.now().UTC(),
		ReasonCode: "management_not_observed",
	}
}
