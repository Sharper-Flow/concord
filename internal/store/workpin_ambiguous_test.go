package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A work pin is the one point-in-time projection a caller needs to prepare an
// action. For an item whose contract projection is ambiguous the pin stops
// early by design: it reports the item's position and the one recovery route,
// and stops. The struct it returns must still be a complete pin. The watermark
// used to be assigned after that early return, so the escape-hatch pin
// serialized with an empty watermark and every mutation whose result carried
// the pin failed closed-schema response validation. The recovery the pin
// advertises stayed reachable only because it retires both contracts before
// its own pin is built; every other mutation touching the item was refused.

// TestWorkPinAmbiguousContractsCarryWatermark is the reproduction. It builds a
// workflow with two active contracts and holds the pin to the shape every
// well-formed pin carries.
func TestWorkPinAmbiguousContractsCarryWatermark(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_, version := continuityTestWorkflow(t, s, "workpin-ambiguous")

	db := s.DatabaseForTesting()
	var actorRef string
	if err := db.QueryRow(`SELECT actor_ref FROM workflow_actors LIMIT 1`).Scan(&actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
SELECT 'workpin-ambiguous',1,'ambiguous fixture premise','internal_sqlite','[]','[]','2026-09-21T00:00:00Z',?,'[]','[]',1,'prototype_internal';
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
SELECT 'workpin-ambiguous',2,'ambiguous fixture premise','internal_sqlite','[]','[]','2026-09-21T00:00:00Z',?,'[]','[]',1,'prototype_internal';
DELETE FROM fold_guard`, actorRef, actorRef); err != nil {
		t.Fatal(err)
	}

	pin, err := ReadWorkPin(ctx, s, "workpin-ambiguous")
	if err != nil {
		t.Fatalf("the ambiguous pin must stay readable so the operator can reach recovery: %v", err)
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
	offersRecovery := false
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "supersede_contract" {
			offersRecovery = true
		}
	}
	if !offersRecovery {
		t.Fatal("the ambiguous pin must keep advertising the supersede_contract recovery")
	}
}
