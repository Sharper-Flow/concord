package store

import (
	"database/sql"
	"testing"
)

// acceptanceOracleFieldsWithoutPreparationsForTest binds control definitions to
// the fixture's approved predicate and owning Domain. It records no execution.
func acceptanceOracleFieldsWithoutPreparationsForTest(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	db := s.DatabaseForTesting()
	var version int64
	if err := db.QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	predicate := "predicate:oracle-fixture"
	err := db.QueryRow(`SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? ORDER BY ordinal,predicate_id LIMIT 1`, workID, version).Scan(&predicate)
	if err == sql.ErrNoRows {
		if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
			INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,?,?,0,'check','{"kind":"check","check_ref":"check:oracle-fixture","immutable_subject_ref":"commit:oracle-fixture","expected_result":"pass"}');
			DELETE FROM write_guard`, workID, version, predicate); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	var project, product string
	if err := db.QueryRow(`SELECT wp.project_id,pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? AND wp.role='primary' LIMIT 1`, workID).Scan(&project, &product); err != nil {
		t.Fatal(err)
	}
	// The owner's Domain is the committed registry root when the Product has
	// a designated home, else a synthetic root: owner Domains are recorded,
	// not resolved against the registry.
	domain := "root"
	if list, err := s.QueryDomainList(t.Context(), DomainListRequest{Product: product}); err == nil && list.Registry != nil {
		domain = list.Registry.RootDomainID
	}
	var affected string
	err = db.QueryRow(`SELECT d.domain_id FROM workflow_contract_affected_domains d JOIN workflow_architecture_bindings a ON a.work_id=d.work_id AND a.contract_version=d.contract_version WHERE d.work_id=? AND d.contract_version=? ORDER BY d.domain_id LIMIT 1`, workID, version).Scan(&affected)
	if err == nil {
		domain = affected
	} else if err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return map[string]any{
		"owners": []map[string]any{{
			"owner_id": "owner:fixture", "domain_id": domain,
			"mechanism":  map[string]any{"project_id": project, "path": "pkg/value.go", "entry_point": "Value"},
			"obligation": "the candidate returns the approved value", "predicate_ids": []string{predicate}, "law_bindings": []map[string]any{},
		}},
		"cases": []map[string]any{{
			"case_id": "case:fixture", "owner_id": "owner:fixture", "entry_point": "Value",
			"input_class": "candidate value", "expected_state": "approved value returned", "control_ids": []string{"control:fixture"},
		}},
		"controls": []map[string]any{{
			"control_id": "control:fixture", "owner_id": "owner:fixture", "predicate_ids": []string{predicate}, "case_ids": []string{"case:fixture"},
			"recipe_source": map[string]any{"kind": "repository_file", "project_id": project, "path": "pkg/oracle.json", "commit_oid": testOracleCommit()},
			"argv":          []string{"go", "test", "./...", "-run", "fixture"}, "cwd": "pkg", "expected_result": "pass",
			"required_evidence_role": "reported", "readiness_evidence_refs": []string{},
		}},
	}
}
