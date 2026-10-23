// Package network defines side-effect-free network plans and transactions.
// Phase 1 intentionally contains no Linux command execution.
package network

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"edgebox/internal/config"
)

type ObjectKind string

const (
	ObjectPolicyRule    ObjectKind = "policy_rule"
	ObjectRoute         ObjectKind = "route"
	ObjectNftablesTable ObjectKind = "nftables_table"
	ObjectNftablesChain ObjectKind = "nftables_chain"
	ObjectNftablesRule  ObjectKind = "nftables_rule"
	ObjectQDisc         ObjectKind = "qdisc"
	ObjectTrafficClass  ObjectKind = "traffic_class"
)

type OwnedObject struct {
	Kind       ObjectKind `json:"kind"`
	Identifier string     `json:"identifier"`
}

type DesiredPlan struct {
	version     string
	owner       config.NetworkOwnership
	objects     []OwnedObject
	fingerprint string
}

func NewDesiredPlan(version string, owner config.NetworkOwnership, objects []OwnedObject) (DesiredPlan, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return DesiredPlan{}, errors.New("network plan version is required")
	}
	if err := owner.Validate(); err != nil {
		return DesiredPlan{}, fmt.Errorf("network plan ownership: %w", err)
	}
	cloned := append([]OwnedObject(nil), objects...)
	seen := make(map[string]struct{}, len(cloned))
	for i, object := range cloned {
		if !validObjectKind(object.Kind) {
			return DesiredPlan{}, fmt.Errorf("network plan object %d has invalid kind %q", i, object.Kind)
		}
		object.Identifier = strings.TrimSpace(object.Identifier)
		if object.Identifier == "" {
			return DesiredPlan{}, fmt.Errorf("network plan object %d identifier is required", i)
		}
		key := string(object.Kind) + "\x00" + object.Identifier
		if _, exists := seen[key]; exists {
			return DesiredPlan{}, fmt.Errorf("network plan contains duplicate %s object %q", object.Kind, object.Identifier)
		}
		seen[key] = struct{}{}
		cloned[i] = object
	}
	payload := struct {
		Version string                  `json:"version"`
		Owner   config.NetworkOwnership `json:"owner"`
		Objects []OwnedObject           `json:"objects"`
	}{Version: version, Owner: owner, Objects: cloned}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return DesiredPlan{}, fmt.Errorf("fingerprint network plan: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return DesiredPlan{
		version:     version,
		owner:       owner,
		objects:     cloned,
		fingerprint: "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func (p DesiredPlan) Version() string                { return p.version }
func (p DesiredPlan) Owner() config.NetworkOwnership { return p.owner }
func (p DesiredPlan) Objects() []OwnedObject         { return append([]OwnedObject(nil), p.objects...) }
func (p DesiredPlan) Fingerprint() string            { return p.fingerprint }

type Phase string

const (
	PhaseValidate Phase = "validate"
	PhasePrepare  Phase = "prepare"
	PhaseApply    Phase = "apply"
	PhaseVerify   Phase = "verify"
	PhaseCommit   Phase = "commit"
	PhaseRollback Phase = "rollback"
)

var transactionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type Snapshot struct {
	ID          string
	PlanVersion string
	PlanDigest  string
	OwnerID     string
	Phase       Phase
	UpdatedAt   time.Time
	Reason      string
}

type Transaction struct {
	mu        sync.RWMutex
	id        string
	plan      DesiredPlan
	phase     Phase
	updatedAt time.Time
	reason    string
	now       func() time.Time
}

func NewTransaction(id string, plan DesiredPlan, now func() time.Time) (*Transaction, error) {
	if !transactionIDPattern.MatchString(id) {
		return nil, errors.New("network transaction id is invalid")
	}
	if plan.Fingerprint() == "" {
		return nil, errors.New("network transaction requires a validated desired plan")
	}
	if now == nil {
		now = time.Now
	}
	return &Transaction{
		id:        id,
		plan:      plan,
		phase:     PhaseValidate,
		updatedAt: now().UTC(),
		reason:    "plan_validated",
		now:       now,
	}, nil
}

func (t *Transaction) Advance(to Phase, reason string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if reason == "" {
		return errors.New("network transaction transition reason is required")
	}
	expected := map[Phase]Phase{
		PhaseValidate: PhasePrepare,
		PhasePrepare:  PhaseApply,
		PhaseApply:    PhaseVerify,
		PhaseVerify:   PhaseCommit,
	}
	if expected[t.phase] != to {
		return fmt.Errorf("illegal network transaction transition %s -> %s", t.phase, to)
	}
	t.phase = to
	t.reason = reason
	t.updatedAt = t.now().UTC()
	return nil
}

func (t *Transaction) Rollback(reason string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if reason == "" {
		return errors.New("network transaction rollback reason is required")
	}
	switch t.phase {
	case PhasePrepare, PhaseApply, PhaseVerify:
		t.phase = PhaseRollback
		t.reason = reason
		t.updatedAt = t.now().UTC()
		return nil
	default:
		return fmt.Errorf("cannot roll back network transaction in %s phase", t.phase)
	}
}

func (t *Transaction) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return Snapshot{
		ID:          t.id,
		PlanVersion: t.plan.Version(),
		PlanDigest:  t.plan.Fingerprint(),
		OwnerID:     t.plan.Owner().OwnerID,
		Phase:       t.phase,
		UpdatedAt:   t.updatedAt,
		Reason:      t.reason,
	}
}

func validObjectKind(kind ObjectKind) bool {
	switch kind {
	case ObjectPolicyRule, ObjectRoute, ObjectNftablesTable, ObjectNftablesChain, ObjectNftablesRule, ObjectQDisc, ObjectTrafficClass:
		return true
	default:
		return false
	}
}
