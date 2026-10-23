// Package health defines independent, domain-oriented health observations.
package health

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

type Domain string

const (
	DomainSystem         Domain = "system"
	DomainWAN            Domain = "wan"
	DomainManagement     Domain = "management"
	DomainControl        Domain = "control"
	DomainTransport      Domain = "transport"
	DomainRouting        Domain = "routing"
	DomainNAT            Domain = "nat"
	DomainInternetEgress Domain = "internet_egress"
	DomainAgentState     Domain = "agent_state"
)

var domains = [...]Domain{
	DomainSystem,
	DomainWAN,
	DomainManagement,
	DomainControl,
	DomainTransport,
	DomainRouting,
	DomainNAT,
	DomainInternetEgress,
	DomainAgentState,
}

// Domains returns a copy of the supported health-domain registry.
func Domains() []Domain {
	return append([]Domain(nil), domains[:]...)
}

type Status string

const (
	StatusUnknown   Status = "unknown"
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
)

var reasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type Observation struct {
	Domain       Domain             `json:"domain"`
	Status       Status             `json:"status"`
	ObservedAt   time.Time          `json:"observed_at"`
	ReasonCode   string             `json:"reason_code"`
	Details      map[string]string  `json:"details,omitempty"`
	Measurements map[string]float64 `json:"measurements,omitempty"`
}

func (o Observation) Validate() error {
	if !validDomain(o.Domain) {
		return fmt.Errorf("health domain %q is not supported", o.Domain)
	}
	if !validStatus(o.Status) {
		return fmt.Errorf("health status %q is not supported", o.Status)
	}
	if o.ObservedAt.IsZero() {
		return errors.New("health observed_at is required")
	}
	if !reasonPattern.MatchString(o.ReasonCode) {
		return fmt.Errorf("health reason_code must match %s", reasonPattern.String())
	}
	return nil
}

func (o Observation) clone() Observation {
	o.ObservedAt = o.ObservedAt.UTC()
	o.Details = cloneMap(o.Details)
	o.Measurements = cloneMap(o.Measurements)
	return o
}

type Report struct {
	mu           sync.RWMutex
	observations map[Domain]Observation
}

func NewReport(at time.Time) *Report {
	at = at.UTC()
	observations := make(map[Domain]Observation, len(domains))
	for _, domain := range domains {
		observations[domain] = Observation{
			Domain:     domain,
			Status:     StatusUnknown,
			ObservedAt: at,
			ReasonCode: "not_observed",
		}
	}
	return &Report{observations: observations}
}

func (r *Report) Set(observation Observation) error {
	if err := observation.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations[observation.Domain] = observation.clone()
	return nil
}

func (r *Report) Get(domain Domain) (Observation, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	observation, ok := r.observations[domain]
	return observation.clone(), ok
}

func (r *Report) Snapshot() map[Domain]Observation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[Domain]Observation, len(r.observations))
	for domain, observation := range r.observations {
		result[domain] = observation.clone()
	}
	return result
}

func validDomain(domain Domain) bool {
	for _, candidate := range domains {
		if domain == candidate {
			return true
		}
	}
	return false
}

func validStatus(status Status) bool {
	switch status {
	case StatusUnknown, StatusHealthy, StatusDegraded, StatusUnhealthy:
		return true
	default:
		return false
	}
}

func cloneMap[K comparable, V any](input map[K]V) map[K]V {
	if input == nil {
		return nil
	}
	output := make(map[K]V, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
