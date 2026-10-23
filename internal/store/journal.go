package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"edgebox/internal/config"
)

const JournalSchemaVersion = 1

var (
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

type JournalPhase string

const (
	JournalPhaseValidate JournalPhase = "validate"
	JournalPhasePrepare  JournalPhase = "prepare"
	JournalPhaseApply    JournalPhase = "apply"
	JournalPhaseVerify   JournalPhase = "verify"
	JournalPhaseCommit   JournalPhase = "commit"
	JournalPhaseRollback JournalPhase = "rollback"
)

type JournalObjectKind string

const (
	JournalObjectPolicyRule    JournalObjectKind = "policy_rule"
	JournalObjectRoute         JournalObjectKind = "route"
	JournalObjectNftablesTable JournalObjectKind = "nftables_table"
	JournalObjectNftablesChain JournalObjectKind = "nftables_chain"
	JournalObjectNftablesRule  JournalObjectKind = "nftables_rule"
	JournalObjectQDisc         JournalObjectKind = "qdisc"
	JournalObjectTrafficClass  JournalObjectKind = "traffic_class"
)

type JournalObject struct {
	Kind       JournalObjectKind `json:"kind"`
	Identifier string            `json:"identifier"`
}

type CommittedConfiguration struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type JournalReason struct {
	Code      string `json:"code"`
	Component string `json:"component,omitempty"`
	Retryable bool   `json:"retryable"`
}

type JournalInput struct {
	ID                 string
	Phase              JournalPhase
	DesiredPlanVersion string
	DesiredPlanDigest  string
	NetworkOwner       config.NetworkOwnership
	OwnedObjects       []JournalObject
	PreviousCommitted  *CommittedConfiguration
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Reason             *JournalReason
}

// Journal is immutable after construction. Slice and pointer accessors return
// copies so later recovery cannot be changed by a caller that retained input.
type Journal struct {
	schemaVersion      int
	id                 string
	phase              JournalPhase
	desiredPlanVersion string
	desiredPlanDigest  string
	networkOwner       config.NetworkOwnership
	ownedObjects       []JournalObject
	previousCommitted  *CommittedConfiguration
	createdAt          time.Time
	updatedAt          time.Time
	reason             *JournalReason
}

func NewJournal(input JournalInput) (Journal, error) {
	journal := Journal{
		schemaVersion:      JournalSchemaVersion,
		id:                 strings.TrimSpace(input.ID),
		phase:              input.Phase,
		desiredPlanVersion: strings.TrimSpace(input.DesiredPlanVersion),
		desiredPlanDigest:  strings.TrimSpace(input.DesiredPlanDigest),
		networkOwner:       input.NetworkOwner,
		ownedObjects:       append([]JournalObject(nil), input.OwnedObjects...),
		createdAt:          input.CreatedAt.UTC(),
		updatedAt:          input.UpdatedAt.UTC(),
	}
	if input.PreviousCommitted != nil {
		previous := *input.PreviousCommitted
		journal.previousCommitted = &previous
	}
	if input.Reason != nil {
		reason := *input.Reason
		journal.reason = &reason
	}
	if err := journal.Validate(); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (j Journal) SchemaVersion() int                    { return j.schemaVersion }
func (j Journal) ID() string                            { return j.id }
func (j Journal) Phase() JournalPhase                   { return j.phase }
func (j Journal) DesiredPlanVersion() string            { return j.desiredPlanVersion }
func (j Journal) DesiredPlanDigest() string             { return j.desiredPlanDigest }
func (j Journal) NetworkOwner() config.NetworkOwnership { return j.networkOwner }
func (j Journal) CreatedAt() time.Time                  { return j.createdAt }
func (j Journal) UpdatedAt() time.Time                  { return j.updatedAt }
func (j Journal) OwnedObjects() []JournalObject {
	return append([]JournalObject(nil), j.ownedObjects...)
}

func (j Journal) PreviousCommitted() (CommittedConfiguration, bool) {
	if j.previousCommitted == nil {
		return CommittedConfiguration{}, false
	}
	return *j.previousCommitted, true
}

func (j Journal) Reason() (JournalReason, bool) {
	if j.reason == nil {
		return JournalReason{}, false
	}
	return *j.reason, true
}

func (j Journal) Validate() error {
	var errs []error
	if j.schemaVersion != JournalSchemaVersion {
		errs = append(errs, fmt.Errorf("transaction journal schema_version: unsupported value %d", j.schemaVersion))
	}
	if !safeNamePattern.MatchString(j.id) {
		errs = append(errs, errors.New("transaction journal transaction_id is invalid"))
	}
	if !validJournalPhase(j.phase) {
		errs = append(errs, fmt.Errorf("transaction journal phase %q is invalid", j.phase))
	}
	if j.desiredPlanVersion == "" {
		errs = append(errs, errors.New("transaction journal desired_plan.version is required"))
	}
	if !digestPattern.MatchString(j.desiredPlanDigest) {
		errs = append(errs, errors.New("transaction journal desired_plan.digest must be a sha256 digest"))
	}
	if err := j.networkOwner.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("transaction journal network_owner: %w", err))
	}
	seen := make(map[string]struct{}, len(j.ownedObjects))
	for index, object := range j.ownedObjects {
		if !validJournalObjectKind(object.Kind) {
			errs = append(errs, fmt.Errorf("transaction journal owned_objects[%d].kind %q is invalid", index, object.Kind))
		}
		if strings.TrimSpace(object.Identifier) == "" {
			errs = append(errs, fmt.Errorf("transaction journal owned_objects[%d].identifier is required", index))
		}
		key := string(object.Kind) + "\x00" + object.Identifier
		if _, exists := seen[key]; exists {
			errs = append(errs, fmt.Errorf("transaction journal owned_objects[%d] duplicates %q", index, object.Identifier))
		}
		seen[key] = struct{}{}
	}
	if j.previousCommitted != nil {
		if strings.TrimSpace(j.previousCommitted.Version) == "" {
			errs = append(errs, errors.New("transaction journal previous_committed.version is required"))
		}
		if !digestPattern.MatchString(j.previousCommitted.Digest) {
			errs = append(errs, errors.New("transaction journal previous_committed.digest must be a sha256 digest"))
		}
	}
	if j.createdAt.IsZero() || j.updatedAt.IsZero() {
		errs = append(errs, errors.New("transaction journal created_at and updated_at are required"))
	} else if j.updatedAt.Before(j.createdAt) {
		errs = append(errs, errors.New("transaction journal updated_at must not precede created_at"))
	}
	if j.reason != nil {
		if !reasonCodePattern.MatchString(j.reason.Code) {
			errs = append(errs, errors.New("transaction journal reason.code is invalid"))
		}
		if j.reason.Component != "" && !reasonCodePattern.MatchString(j.reason.Component) {
			errs = append(errs, errors.New("transaction journal reason.component is invalid"))
		}
	}
	return errors.Join(errs...)
}

type journalWire struct {
	SchemaVersion     int                     `json:"schema_version"`
	TransactionID     string                  `json:"transaction_id"`
	Phase             JournalPhase            `json:"phase"`
	DesiredPlan       desiredPlanWire         `json:"desired_plan"`
	NetworkOwner      config.NetworkOwnership `json:"network_owner"`
	OwnedObjects      []JournalObject         `json:"owned_objects"`
	PreviousCommitted *CommittedConfiguration `json:"previous_committed,omitempty"`
	CreatedAt         time.Time               `json:"created_at"`
	UpdatedAt         time.Time               `json:"updated_at"`
	Reason            *JournalReason          `json:"reason,omitempty"`
}

type desiredPlanWire struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

func (j Journal) MarshalJSON() ([]byte, error) {
	if err := j.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(j.toWire())
}

func (j *Journal) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire journalWire
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("transaction journal contains trailing JSON values")
		}
		return fmt.Errorf("decode trailing transaction journal data: %w", err)
	}
	decoded, err := NewJournal(JournalInput{
		ID:                 wire.TransactionID,
		Phase:              wire.Phase,
		DesiredPlanVersion: wire.DesiredPlan.Version,
		DesiredPlanDigest:  wire.DesiredPlan.Digest,
		NetworkOwner:       wire.NetworkOwner,
		OwnedObjects:       wire.OwnedObjects,
		PreviousCommitted:  wire.PreviousCommitted,
		CreatedAt:          wire.CreatedAt,
		UpdatedAt:          wire.UpdatedAt,
		Reason:             wire.Reason,
	})
	if err != nil {
		return err
	}
	if wire.SchemaVersion != JournalSchemaVersion {
		return fmt.Errorf("transaction journal schema_version: unsupported value %d", wire.SchemaVersion)
	}
	*j = decoded
	return nil
}

func (j Journal) toWire() journalWire {
	objects := j.OwnedObjects()
	previous, hasPrevious := j.PreviousCommitted()
	reason, hasReason := j.Reason()
	wire := journalWire{
		SchemaVersion: j.schemaVersion,
		TransactionID: j.id,
		Phase:         j.phase,
		DesiredPlan: desiredPlanWire{
			Version: j.desiredPlanVersion,
			Digest:  j.desiredPlanDigest,
		},
		NetworkOwner: j.networkOwner,
		OwnedObjects: objects,
		CreatedAt:    j.createdAt,
		UpdatedAt:    j.updatedAt,
	}
	if hasPrevious {
		wire.PreviousCommitted = &previous
	}
	if hasReason {
		wire.Reason = &reason
	}
	return wire
}

func (s *Store) WriteJournal(journal Journal) error {
	if err := journal.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode transaction journal: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(s.journalPath(journal.ID()), append(data, '\n'), 0o600)
}

func (s *Store) ListJournals() ([]Journal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readJournals()
}

func (s *Store) readJournals() ([]Journal, error) {
	directory := s.path("transactions")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("list transaction journals: %w", err)
	}
	journals := make([]Journal, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read transaction journal %q: %w", entry.Name(), err)
		}
		var journal Journal
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, fmt.Errorf("decode transaction journal %q: %w", entry.Name(), err)
		}
		if entry.Name() != journal.ID()+".json" {
			return nil, fmt.Errorf("transaction journal filename %q does not match transaction_id %q", entry.Name(), journal.ID())
		}
		journals = append(journals, journal)
	}
	sort.Slice(journals, func(i, k int) bool { return journals[i].ID() < journals[k].ID() })
	return journals, nil
}

type ReconciliationAction string

const (
	ReconcileDiscardValidated    ReconciliationAction = "discard_validated"
	ReconcileRollbackInterrupted ReconciliationAction = "rollback_interrupted"
	ReconcileFinalizeCommit      ReconciliationAction = "finalize_commit"
	ReconcileFinalizeRollback    ReconciliationAction = "finalize_rollback"
)

type Reconciliation struct {
	Journal Journal
	Action  ReconciliationAction
}

type Recovery struct {
	RemovedTemporaryFiles []string
	Reconciliations       []Reconciliation
}

func (r Recovery) InterruptedTransactions() []Reconciliation {
	interrupted := make([]Reconciliation, 0, len(r.Reconciliations))
	for _, reconciliation := range r.Reconciliations {
		if reconciliation.Action == ReconcileRollbackInterrupted {
			interrupted = append(interrupted, reconciliation)
		}
	}
	return interrupted
}

func reconciliationAction(phase JournalPhase) ReconciliationAction {
	switch phase {
	case JournalPhaseValidate:
		return ReconcileDiscardValidated
	case JournalPhasePrepare, JournalPhaseApply, JournalPhaseVerify:
		return ReconcileRollbackInterrupted
	case JournalPhaseCommit:
		return ReconcileFinalizeCommit
	case JournalPhaseRollback:
		return ReconcileFinalizeRollback
	default:
		panic("validated journal has an unknown phase")
	}
}

func validJournalPhase(phase JournalPhase) bool {
	switch phase {
	case JournalPhaseValidate, JournalPhasePrepare, JournalPhaseApply, JournalPhaseVerify, JournalPhaseCommit, JournalPhaseRollback:
		return true
	default:
		return false
	}
}

func validJournalObjectKind(kind JournalObjectKind) bool {
	switch kind {
	case JournalObjectPolicyRule, JournalObjectRoute, JournalObjectNftablesTable, JournalObjectNftablesChain, JournalObjectNftablesRule, JournalObjectQDisc, JournalObjectTrafficClass:
		return true
	default:
		return false
	}
}
