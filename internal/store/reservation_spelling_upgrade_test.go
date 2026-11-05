package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// seedReservationSpellingUpgrade seeds the pre-CON-1703 duplicate-spelling
// rows a store could admit before the seam resolved identities: two works
// holding one identity under two spellings, one contract carrying both
// spellings, and a foreign-home spelling that stays its own identity.
func seedReservationSpellingUpgrade(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	seed := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	seed(fmt.Sprintf(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product','Product','prototype','operator_only',1,'now','now');
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project','Project',1,'now','now');
INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator','project','canonical_path','/tmp/concord-reservation-upgrade','/tmp/concord-reservation-upgrade','now','now');
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES
  ('reservation-early','task','Early','in_progress',0,1,'now','now'),
  ('reservation-late','task','Late','in_progress',0,1,'now','now'),
  ('reservation-both','task','Both','in_progress',0,1,'now','now');
INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES
  ('%[1]s','p:early','c:early','a:early','s:early','agent','now'),
  ('%[2]s','p:late','c:late','a:late','s:late','agent','now'),
  ('%[3]s','p:both','c:both','a:both','s:both','agent','now');
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES
  ('reservation-early',1,'early','internal_sqlite','[]','2026-01-01T00:00:00Z','%[1]s','[]','[]',1,'prototype_internal'),
  ('reservation-late',1,'late','internal_sqlite','[]','2026-03-01T00:00:00Z','%[2]s','[]','[]',1,'prototype_internal'),
  ('reservation-both',1,'both','internal_sqlite','[]','2026-02-01T00:00:00Z','%[3]s','[]','[]',1,'prototype_internal');
INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES
  ('reservation-early',1,'product','root','sha256:0000000000000000000000000000000000000000000000000000000000000000'),
  ('reservation-late',1,'product','root','sha256:0000000000000000000000000000000000000000000000000000000000000000'),
  ('reservation-both',1,'product','root','sha256:0000000000000000000000000000000000000000000000000000000000000000');
INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product','project','locator');
INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES
  ('product','law:dual','reservation-early',1,'root'),
  ('product','project/law:dual','reservation-late',1,'root'),
  ('product','law:pair','reservation-both',1,'root'),
  ('product','project/law:pair','reservation-both',1,'root'),
  ('product','other/law:foreign','reservation-early',1,'root');
INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES
  ('reservation-early',1,'product','law:dual','root','reservation-early',1),
  ('reservation-late',1,'product','project/law:dual','root','reservation-late',1),
  ('reservation-both',1,'product','law:pair','root','reservation-both',1),
  ('reservation-both',1,'product','project/law:pair','root','reservation-both',1),
  ('reservation-early',1,'product','other/law:foreign','root','reservation-early',1);
DELETE FROM write_guard WHERE active = 1;`,
		DeriveWorkflowActorRef("p:early", "c:early", "a:early", "s:early"),
		DeriveWorkflowActorRef("p:late", "c:late", "a:late", "s:late"),
		DeriveWorkflowActorRef("p:both", "c:both", "a:both", "s:both")))
}

// TestMigrateLawAdditionReservationsToCanonicalIdentity drives the
// reservation-identity migration over the seeded pre-fix rows: both
// spellings of one identity fold to the earliest approved contract, the
// duplicate owner's contract-addition row goes with its folded reservation
// while the identity hold survives, and a foreign-home spelling keeps its
// own row untouched.
func TestMigrateLawAdditionReservationsToCanonicalIdentity(t *testing.T) {
	ctx := context.Background()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "reservation-identity.db"), len(migrations)-1)
	seedReservationSpellingUpgrade(t, db)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reservation := func(lawID string) (owner string, count int) {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT owner_work_id FROM workflow_law_addition_reservations WHERE law_id=?`, lawID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			if err := rows.Scan(&owner); err != nil {
				t.Fatal(err)
			}
			count++
		}
		return owner, count
	}
	if owner, count := reservation("law:dual"); count != 1 || owner != "reservation-early" {
		t.Fatalf("law:dual owner=%q rows=%d, want the earliest owner once", owner, count)
	}
	if owner, count := reservation("law:pair"); count != 1 || owner != "reservation-both" {
		t.Fatalf("law:pair owner=%q rows=%d, want the both-spellings contract once", owner, count)
	}
	if owner, count := reservation("other/law:foreign"); count != 1 || owner != "reservation-early" {
		t.Fatalf("other/law:foreign owner=%q rows=%d, want the untouched foreign spelling", owner, count)
	}
	var qualifiedRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_law_addition_reservations WHERE law_id LIKE '%/%'`).Scan(&qualifiedRows); err != nil || qualifiedRows != 1 {
		t.Fatalf("surviving qualified-spelling reservations=%d err=%v, want only the foreign home", qualifiedRows, err)
	}
	addition := func(workID, lawID string) int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contract_law_additions WHERE work_id=? AND law_id=?`, workID, lawID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if addition("reservation-early", "law:dual") != 1 {
		t.Fatal("the earliest owner lost its canonical addition row")
	}
	if addition("reservation-late", "project/law:dual") != 0 || addition("reservation-late", "law:dual") != 0 {
		t.Fatal("the folded duplicate owner kept an addition row")
	}
	if addition("reservation-both", "law:pair") != 1 || addition("reservation-both", "project/law:pair") != 0 {
		t.Fatal("the both-spellings contract did not fold to one canonical addition row")
	}
	if addition("reservation-early", "other/law:foreign") != 1 {
		t.Fatal("the foreign-home addition row was rewritten or dropped")
	}
}
