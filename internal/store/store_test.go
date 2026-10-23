package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"edgebox/internal/config"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFactoryAndIdentityAreWriteOnceAndIdempotent(t *testing.T) {
	store := openTestStore(t)
	if err := store.WriteFactory([]byte("factory")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteFactory([]byte("factory")); err != nil {
		t.Fatalf("idempotent WriteFactory() error = %v", err)
	}
	if err := store.WriteFactory([]byte("different")); err == nil {
		t.Fatal("different factory configuration replaced write-once state")
	}

	if err := store.WriteDeviceID("device-001"); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteDeviceID("device-001"); err != nil {
		t.Fatalf("idempotent WriteDeviceID() error = %v", err)
	}
	if err := store.WriteDeviceID("device-002"); err == nil {
		t.Fatal("different device identity replaced persistent identity")
	}
}

func TestCommitMaintainsPreviousKnownGood(t *testing.T) {
	store := openTestStore(t)
	if err := store.CommitCurrent([]byte("version-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadPrevious(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadPrevious() error = %v, want os.ErrNotExist", err)
	}
	if err := store.CommitCurrent([]byte("version-2")); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitCurrent([]byte("version-2")); err != nil {
		t.Fatal(err)
	}
	current, _ := store.ReadCurrent()
	previous, _ := store.ReadPrevious()
	if string(current) != "version-2" || string(previous) != "version-1" {
		t.Fatalf("current=%q previous=%q", current, previous)
	}
}

func TestInterruptedAtomicWriteLeavesCommittedStateAndIsRecovered(t *testing.T) {
	store := openTestStore(t)
	if err := store.CommitCurrent([]byte("known-good")); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(store.root, "current", temporaryPrefix+"interrupted")
	if err := os.WriteFile(temporary, []byte("partial-new-state"), 0o600); err != nil {
		t.Fatal(err)
	}

	current, err := store.ReadCurrent()
	if err != nil || string(current) != "known-good" {
		t.Fatalf("committed state after interruption = %q, %v", current, err)
	}
	recovery, err := store.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery.RemovedTemporaryFiles) != 1 {
		t.Fatalf("removed temporary files = %v", recovery.RemovedTemporaryFiles)
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file still exists: %v", err)
	}
}

func TestJournalRoundTripContainsRecoverySemantics(t *testing.T) {
	stateStore := openTestStore(t)
	journal := testJournal(t, "txn-roundtrip", JournalPhaseApply)
	if err := stateStore.WriteJournal(journal); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(stateStore.root)
	if err != nil {
		t.Fatal(err)
	}
	journals, err := reopened.ListJournals()
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 1 {
		t.Fatalf("journal count = %d", len(journals))
	}
	got := journals[0]
	if got.SchemaVersion() != JournalSchemaVersion || got.ID() != "txn-roundtrip" || got.Phase() != JournalPhaseApply {
		t.Fatalf("journal identity/phase did not round trip")
	}
	if got.DesiredPlanVersion() != "config-2" || got.DesiredPlanDigest() != digest("a") {
		t.Fatalf("desired plan did not round trip")
	}
	if got.NetworkOwner().OwnerID != "edgebox-v01" || len(got.OwnedObjects()) != 3 {
		t.Fatalf("ownership manifest did not round trip: %+v", got.OwnedObjects())
	}
	previous, ok := got.PreviousCommitted()
	if !ok || previous.Version != "config-1" || previous.Digest != digest("b") {
		t.Fatalf("previous committed state = %+v, %v", previous, ok)
	}
	reason, ok := got.Reason()
	if !ok || reason.Code != "verification_pending" || reason.Component != "network" || !reason.Retryable {
		t.Fatalf("reason = %+v, %v", reason, ok)
	}
}

func TestJournalReconstructionDataIsImmutable(t *testing.T) {
	objects := []JournalObject{{Kind: JournalObjectRoute, Identifier: "table=100 destination=default"}}
	previous := &CommittedConfiguration{Version: "config-1", Digest: digest("b")}
	reason := &JournalReason{Code: "apply_interrupted", Component: "network", Retryable: true}
	now := time.Now()
	journal, err := NewJournal(JournalInput{
		ID:                 "txn-immutable",
		Phase:              JournalPhaseApply,
		DesiredPlanVersion: "config-2",
		DesiredPlanDigest:  digest("a"),
		NetworkOwner:       testNetworkOwner(),
		OwnedObjects:       objects,
		PreviousCommitted:  previous,
		CreatedAt:          now,
		UpdatedAt:          now,
		Reason:             reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	objects[0].Identifier = "input mutation"
	previous.Version = "input mutation"
	reason.Code = "input_mutation"
	returned := journal.OwnedObjects()
	returned[0].Identifier = "accessor mutation"

	if journal.OwnedObjects()[0].Identifier != "table=100 destination=default" {
		t.Fatal("journal manifest was mutable")
	}
	gotPrevious, _ := journal.PreviousCommitted()
	gotReason, _ := journal.Reason()
	if gotPrevious.Version != "config-1" || gotReason.Code != "apply_interrupted" {
		t.Fatal("journal pointer input was retained")
	}
}

func TestRecoverClassifiesInterruptedPrepareApplyAndVerify(t *testing.T) {
	stateStore := openTestStore(t)
	for index, phase := range []JournalPhase{JournalPhasePrepare, JournalPhaseApply, JournalPhaseVerify} {
		journal := testJournal(t, fmtID(index), phase)
		if err := stateStore.WriteJournal(journal); err != nil {
			t.Fatal(err)
		}
	}
	recovery, err := stateStore.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if got := recovery.InterruptedTransactions(); len(got) != 3 {
		t.Fatalf("interrupted transaction count = %d", len(got))
	}
	for _, reconciliation := range recovery.Reconciliations {
		if reconciliation.Action != ReconcileRollbackInterrupted {
			t.Fatalf("phase %s action = %s", reconciliation.Journal.Phase(), reconciliation.Action)
		}
	}
}

func TestRecoverClassifiesTerminalTransactionsDeterministically(t *testing.T) {
	stateStore := openTestStore(t)
	for _, test := range []struct {
		id     string
		phase  JournalPhase
		action ReconciliationAction
	}{
		{id: "txn-commit", phase: JournalPhaseCommit, action: ReconcileFinalizeCommit},
		{id: "txn-rollback", phase: JournalPhaseRollback, action: ReconcileFinalizeRollback},
		{id: "txn-validate", phase: JournalPhaseValidate, action: ReconcileDiscardValidated},
	} {
		if err := stateStore.WriteJournal(testJournal(t, test.id, test.phase)); err != nil {
			t.Fatal(err)
		}
	}
	recovery, err := stateStore.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery.InterruptedTransactions()) != 0 {
		t.Fatal("terminal/validated journals were treated as interrupted apply")
	}
	want := map[string]ReconciliationAction{
		"txn-commit":   ReconcileFinalizeCommit,
		"txn-rollback": ReconcileFinalizeRollback,
		"txn-validate": ReconcileDiscardValidated,
	}
	for _, reconciliation := range recovery.Reconciliations {
		if reconciliation.Action != want[reconciliation.Journal.ID()] {
			t.Fatalf("transaction %s action = %s", reconciliation.Journal.ID(), reconciliation.Action)
		}
	}
}

func TestMalformedJournalsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "unsupported schema", mutate: func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `"schema_version":1`, `"schema_version":99`, 1))
		}},
		{name: "invalid digest", mutate: func(data []byte) []byte {
			return []byte(strings.Replace(string(data), digest("a"), "not-a-digest", 1))
		}},
		{name: "unknown field", mutate: func(data []byte) []byte {
			return []byte(strings.Replace(string(data), "{", `{"unexpected":true,`, 1))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateStore := openTestStore(t)
			data, err := json.Marshal(testJournal(t, "malformed", JournalPhaseApply))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stateStore.journalPath("malformed"), test.mutate(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := stateStore.Recover(); err == nil {
				t.Fatal("Recover() accepted malformed journal")
			}
		})
	}
}

func TestRecoverOnlyRemovesStoreOwnedTemporaryFiles(t *testing.T) {
	stateStore := openTestStore(t)
	ownedTemporary := filepath.Join(stateStore.root, "current", temporaryPrefix+"config")
	managementDirectory := filepath.Join(stateStore.root, "management")
	managementTemporary := filepath.Join(managementDirectory, temporaryPrefix+"key")
	if err := os.WriteFile(ownedTemporary, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(managementDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managementTemporary, []byte("must-survive"), 0o600); err != nil {
		t.Fatal(err)
	}

	recovery, err := stateStore.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery.RemovedTemporaryFiles) != 1 {
		t.Fatalf("removed files = %v", recovery.RemovedTemporaryFiles)
	}
	if _, err := os.Stat(ownedTemporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned temporary file was not removed: %v", err)
	}
	if data, err := os.ReadFile(managementTemporary); err != nil || string(data) != "must-survive" {
		t.Fatalf("non-owned management file changed: data=%q error=%v", data, err)
	}
}

func TestOpenCreatesOnlyExplicitOwnedLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "first", "time", "edgebox")
	stateStore, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(stateStore.root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	want := append([]string(nil), ownedDirectories[:]...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("owned layout = %v, want %v", got, want)
	}
}

func testJournal(t *testing.T, id string, phase JournalPhase) Journal {
	t.Helper()
	created := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	previous := &CommittedConfiguration{Version: "config-1", Digest: digest("b")}
	reason := &JournalReason{Code: "verification_pending", Component: "network", Retryable: true}
	journal, err := NewJournal(JournalInput{
		ID:                 id,
		Phase:              phase,
		DesiredPlanVersion: "config-2",
		DesiredPlanDigest:  digest("a"),
		NetworkOwner:       testNetworkOwner(),
		OwnedObjects: []JournalObject{
			{Kind: JournalObjectNftablesTable, Identifier: "family=inet table=edgebox"},
			{Kind: JournalObjectPolicyRule, Identifier: "priority=10000 mark=1 table=100"},
			{Kind: JournalObjectRoute, Identifier: "table=100 destination=default"},
		},
		PreviousCommitted: previous,
		CreatedAt:         created,
		UpdatedAt:         created.Add(time.Minute),
		Reason:            reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

func testNetworkOwner() config.NetworkOwnership {
	return config.NetworkOwnership{
		OwnerID:          "edgebox-v01",
		NftablesFamily:   "inet",
		NftablesTable:    "edgebox",
		RoutingTable:     100,
		RulePriority:     10000,
		PacketMark:       1,
		ObjectNamePrefix: "edgebox",
	}
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }

func fmtID(index int) string { return "txn-interrupted-" + string(rune('1'+index)) }
