package network

import (
	"testing"
	"time"

	"edgebox/internal/config"
)

func testOwnership() config.NetworkOwnership {
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

func TestDesiredPlanIsImmutableAndIdentifiesOwnedObjects(t *testing.T) {
	objects := []OwnedObject{{Kind: ObjectRoute, Identifier: "table=100 destination=default"}}
	plan, err := NewDesiredPlan("config-1", testOwnership(), objects)
	if err != nil {
		t.Fatal(err)
	}
	objects[0].Identifier = "caller mutation"
	returned := plan.Objects()
	returned[0].Identifier = "returned mutation"
	if got := plan.Objects()[0].Identifier; got != "table=100 destination=default" {
		t.Fatalf("immutable plan object changed to %q", got)
	}
	if plan.Owner().OwnerID != "edgebox-v01" || plan.Fingerprint() == "" {
		t.Fatal("plan did not retain ownership and fingerprint")
	}
}

func TestTransactionLifecycleAndRollback(t *testing.T) {
	plan, err := NewDesiredPlan("config-1", testOwnership(), nil)
	if err != nil {
		t.Fatal(err)
	}
	txn, err := NewTransaction("txn-001", plan, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []Phase{PhasePrepare, PhaseApply, PhaseVerify, PhaseCommit} {
		if err := txn.Advance(phase, "test"); err != nil {
			t.Fatalf("Advance(%s) error = %v", phase, err)
		}
	}
	if err := txn.Rollback("too late"); err == nil {
		t.Fatal("committed transaction rolled back")
	}

	txn, _ = NewTransaction("txn-002", plan, time.Now)
	_ = txn.Advance(PhasePrepare, "prepared")
	_ = txn.Advance(PhaseApply, "applied")
	if err := txn.Rollback("verification_failed"); err != nil {
		t.Fatal(err)
	}
	if txn.Snapshot().Phase != PhaseRollback {
		t.Fatalf("phase = %s", txn.Snapshot().Phase)
	}
}

func TestTransactionRejectsSkippedPhase(t *testing.T) {
	plan, _ := NewDesiredPlan("config-1", testOwnership(), nil)
	txn, _ := NewTransaction("txn-001", plan, time.Now)
	if err := txn.Advance(PhaseApply, "skip prepare"); err == nil {
		t.Fatal("transaction allowed skipped prepare phase")
	}
}
