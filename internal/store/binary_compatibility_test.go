package store

import (
	"context"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/version"
)

// The schema floor admits this database. Runtime state compatibility must
// refuse it before a command can reach the per-home or per-work-item guards.
func TestOpenRefusesUpgradeWindowState(t *testing.T) {
	for _, axis := range []string{"knowledge", "workflow", "contract"} {
		t.Run(axis, func(t *testing.T) {
			s := openTemp(t)
			seedUpgradeWindowState(t, s, axis)
			path := s.path
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			for _, route := range []string{"open", "upgrade", "readiness"} {
				var err error
				switch route {
				case "open":
					var opened *Store
					opened, err = Open(context.Background(), path)
					if opened != nil {
						_ = opened.Close()
					}
				case "upgrade":
					_, err = Upgrade(context.Background(), path, nil)
				case "readiness":
					_, err = PlanUpgradeReadiness(context.Background(), path)
				}
				assertFailureKind(t, err, KindSchemaUnsupported)
				if !strings.Contains(err.Error(), "binary compatibility") || !strings.Contains(err.Error(), version.Value) || !strings.Contains(err.Error(), "v99.0.0") {
					t.Fatalf("%s refusal must name the compatibility boundary and both binaries, got %v", route, err)
				}
			}
		})
	}
}

func seedUpgradeWindowState(t *testing.T, s *Store, axis string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
			INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('skew-home','skew-home',1,'t','t');
			INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('skew-product','skew-product','prototype','operator_only',1,'t','t');
			INSERT INTO product_projects(product_id,project_id,role) VALUES('skew-product','skew-home','secondary');
			INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at)
			VALUES('skew-locator','skew-home','canonical_path','/test/skew','/test/skew','t','t');`); err != nil {
		t.Fatal(err)
	}
	if axis == "knowledge" {
		if _, err := db.Exec(`
			INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_at,complete,projection_version)
			VALUES('skew-home','skew-locator','HEAD','commit','t',1,?)`, knowledgeProjectionVersion+1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO runtime_state_writers(surface,definition_ref,version,digest,binary_version) VALUES('knowledge','',?,'','v99.0.0')`, knowledgeProjectionVersion+1); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := db.Exec(`
			INSERT INTO work_items(id,kind,title,urgency,lifecycle,priority,version,created_at,updated_at)
			VALUES('work-skew','task','skew','standard','needed',50,1,'t','t');
			INSERT INTO work_projects(work_id,project_id,role) VALUES('work-skew','skew-home','primary');
			INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state)
			VALUES('work-skew','workflow.implementation',2147483647,?,'proposal','planned')`, "sha256:"+strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO runtime_state_writers(surface,definition_ref,version,digest,binary_version) VALUES('workflow','workflow.implementation',2147483647,?,'v99.0.0')`, "sha256:"+strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		if axis == "contract" {
			actor := "actor:" + strings.Repeat("a", 64)
			if _, err := db.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at)
				VALUES(?,'principal-skew','client-skew','agent-skew','session-skew','operator','t');
				INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,definition_ref,definition_version,definition_digest)
				SELECT work_id,1,'premise','internal_sqlite','[]','[]','t',?,'[]','prototype_internal',definition_ref,definition_version,definition_digest FROM workflow_instances;
				DELETE FROM workflow_instances`, actor, actor); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func TestBinaryCompatibilityAdmitsHistoricalPinsAndLaterAdditiveSchema(t *testing.T) {
	s := openTemp(t)
	db := s.DatabaseForTesting()
	seedUpgradeWindowState(t, s, "workflow")
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("released workflow version 1 must remain registered")
	}
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET definition_version=1,definition_digest=? WHERE work_id='work-skew'; DELETE FROM fold_guard`, definition.Digest); err != nil {
		t.Fatal(err)
	}
	// A release stamp alone is not authority to reject a compatible pair.
	if _, err := db.Exec(`INSERT INTO runtime_state_writers(surface,definition_ref,version,digest,binary_version) VALUES('knowledge','',?,'','v99.0.0')`, knowledgeProjectionVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking) VALUES(?,'future-additive','future-checksum','t',0)`, CurrentSchemaVersion()+1); err != nil {
		t.Fatal(err)
	}
	path := s.path
	_ = s.Close()
	opened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if _, err := PlanUpgradeReadiness(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}

func TestBinaryCompatibilityDoesNotGuessLegacyWriter(t *testing.T) {
	s := openTemp(t)
	seedUpgradeWindowState(t, s, "knowledge")
	if _, err := s.db.Exec(`DROP TABLE runtime_state_writers; DELETE FROM schema_migrations WHERE version=117`); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(context.Background(), s.path)
	if opened != nil {
		_ = opened.Close()
	}
	assertFailureKind(t, err, KindSchemaUnsupported)
	if !strings.Contains(err.Error(), "unknown (writer provenance was not recorded)") {
		t.Fatalf("legacy writer must remain unknown: %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='runtime_state_writers'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused open must not apply the pending additive migration: count=%d error=%v", count, err)
	}
}

func TestRuntimeStateWriterCommitsWithItsRepresentation(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for _, commit := range []bool{false, true} {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := recordRuntimeStateWriter(ctx, tx, "knowledge", "", knowledgeProjectionVersion, ""); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM runtime_state_writers WHERE binary_version=?`, version.Value).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if commit && count != 1 || !commit && count != 0 {
			t.Fatalf("commit=%v produced %d writer records", commit, count)
		}
	}
}
