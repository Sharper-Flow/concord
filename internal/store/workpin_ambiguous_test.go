package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A work pin is the one point-in-time projection a caller needs to prepare an
// action. An ambiguous contract must not produce a false contract projection;
// declared plan routes are not admission to a contract-dependent mutation.

// TestWorkPinAmbiguousContractsCarryWatermark is the reproduction. It builds a
// workflow with two active contracts and holds the pin to the shape every
// well-formed pin carries.
func TestWorkPinAmbiguousContractsCarryWatermark(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	actor, version := seedTestWorkflow(t, s, "workpin-ambiguous")

	db := s.DatabaseForTesting()
	var actorRef string
	if err := db.QueryRow(`SELECT actor_ref FROM workflow_actors LIMIT 1`).Scan(&actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
SELECT 'workpin-ambiguous',1,'ambiguous fixture premise','internal_sqlite','[]','2026-09-21T00:00:00Z',?,'[]','[]',1,'prototype_internal';
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
SELECT 'workpin-ambiguous',2,'ambiguous fixture premise','internal_sqlite','[]','2026-09-21T00:00:00Z',?,'[]','[]',1,'prototype_internal';
DELETE FROM write_guard`, actorRef, actorRef); err != nil {
		t.Fatal(err)
	}

	pin, err := ReadWorkPin(ctx, s, "workpin-ambiguous")
	if err != nil {
		t.Fatalf("the ambiguous pin must stay readable without granting authority: %v", err)
	}
	if pin.Version != version {
		t.Fatalf("pin version=%d, want %d", pin.Version, version)
	}
	if !strings.HasPrefix(pin.Watermark, "seq:") || pin.Watermark == "seq:0" {
		t.Fatalf("ambiguous pin watermark=%q, want a sequence watermark", pin.Watermark)
	}
	encoded, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if watermark, _ := decoded["watermark"].(string); !strings.HasPrefix(watermark, "seq:") {
		t.Fatalf("serialized ambiguous pin watermark=%q, want a sequence watermark", watermark)
	}
	if !workPinContainsAction(pin.NextValidIntents, "insert_step") || !workPinContainsAction(pin.NextValidIntents, "pick_type") {
		t.Fatalf("ambiguous pin omitted independent plan routes: %+v", pin.NextValidIntents)
	}
	before := outsideRepairDatabaseSnapshot(t, s)
	err = reviseContractRecord(t, s, "workpin-ambiguous", actor, map[string]any{}, version)
	if !hasFailureKind(err, KindInvariantViolation) || !strings.Contains(err.Error(), "multiple active contracts") {
		t.Fatalf("ambiguous contract revision = %v", err)
	}
	if outsideRepairDatabaseSnapshot(t, s) != before || readWorkVersion(t, s, "workpin-ambiguous") != version {
		t.Fatal("refused ambiguous contract revision changed durable state")
	}
}
