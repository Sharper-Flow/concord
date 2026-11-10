package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeDeleteForeignKeyPlans(t *testing.T) {
	useStampedBuild(t)
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "plans.db"), 194)
	retirementUpgradeExec(t, db, `PRAGMA foreign_keys=ON`)
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, err = %v", fk, err)
	}
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES('work','task','Witness','needed',1,'time','time')`)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("upgrade from 194: %v", err)
	}
	var breaking, count int
	if err := db.QueryRow(`SELECT breaking FROM schema_migrations WHERE version=195`).Scan(&breaking); err != nil || breaking != 0 {
		t.Fatalf("migration 195 breaking=%d err=%v", breaking, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE id='work' AND title='Witness'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("preserved work count=%d err=%v", count, err)
	}
	for _, tc := range []struct {
		name, query, table, index, constraints string
		args                                   []any
	}{
		{"overlap_from", `DELETE FROM workflow_contracts WHERE work_id=?`, "workflow_overlap_resolutions", "workflow_overlap_resolutions_from_contract", "from_work_id=? AND from_contract_version=?", []any{"work"}},
		{"overlap_to", `DELETE FROM workflow_contracts WHERE work_id=?`, "workflow_overlap_resolutions", "workflow_overlap_resolutions_to_contract", "to_work_id=? AND to_contract_version=?", []any{"work"}},
		{"reservation_owner", `DELETE FROM workflow_contracts WHERE work_id=?`, "workflow_law_addition_reservations", "workflow_law_addition_reservations_owner_contract", "owner_work_id=? AND owner_contract_version=?", []any{"work"}},
		{"contract_supersession", `DELETE FROM workflow_contracts WHERE work_id=?`, "workflow_contracts", "workflow_contracts_superseded_by", "work_id=? AND superseded_by=?", []any{"work"}},
		{"law_reservation", `DELETE FROM workflow_law_addition_reservations WHERE owner_work_id=?`, "workflow_contract_law_additions", "workflow_contract_law_additions_reservation", "product_id=? AND law_id=? AND reservation_owner_work_id=? AND reservation_owner_contract_version=? AND home_domain_id=?", []any{"work"}},
		{"law_architecture", `DELETE FROM workflow_architecture_bindings WHERE work_id=?`, "workflow_contract_law_additions", "workflow_contract_law_additions_architecture", "work_id=? AND contract_version=? AND product_id=?", []any{"work"}},
		{"knowledge_locator", `DELETE FROM project_locators WHERE locator_id=? AND project_id=?`, "product_knowledge_homes", "product_knowledge_homes_locator", "locator_id=?", []any{"locator", "project"}},
		// Several indexes lead with a work_items endpoint, so the planner's
		// choice among them is not pinned; only the full-key search is.
		{"overlap_work_from", `DELETE FROM work_items WHERE id=?`, "workflow_overlap_resolutions", "", "from_work_id=?", []any{"work"}},
		{"overlap_work_to", `DELETE FROM work_items WHERE id=?`, "workflow_overlap_resolutions", "", "to_work_id=?", []any{"work"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			search := "INDEX " + tc.index + " (" + tc.constraints + ")"
			if tc.index == "" {
				search = " (" + tc.constraints + ")"
			}
			rows := retirementUpgradeRows(t, db, `EXPLAIN QUERY PLAN `+tc.query, tc.args...)
			for _, row := range rows {
				detail, _ := row[3].(string)
				if strings.Contains(detail, "SEARCH "+tc.table+" USING") && strings.Contains(detail, search) {
					return
				}
			}
			t.Errorf("missing full-key search of %s %q: %v", tc.table, search, rows)
		})
	}
}
