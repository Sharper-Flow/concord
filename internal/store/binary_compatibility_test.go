package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/version"
)

func TestBinaryCompatibilityFloorAcrossOpenUpgradeAndReadiness(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	for _, breaking := range []bool{false, true} {
		t.Run(fmt.Sprintf("breaking=%t", breaking), func(t *testing.T) {
			s := openTemp(t)
			seedWork(t, s, "work-skew")
			owner := WorkflowActor{PrincipalRef: "principal:skew", ClientRef: "client:skew", AgentRef: "agent:skew", SessionRef: "session:skew", ActorClass: ActorAgent}
			if err := s.Transact(ctx, func(tx *Transaction) error {
				return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: "work-skew", Actor: owner, Now: time.Unix(10, 0).UTC()})
			}); err != nil {
				t.Fatal(err)
			}
			seedWorkflowPlanFrontier(t, s, "work-skew", WorkflowStepPlanFix, "implement")
			future := CurrentSchemaVersion() + 1
			if _, err := s.db.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking) VALUES(?,'future','future-checksum','t',?)`, future, breaking); err != nil {
				t.Fatal(err)
			}
			const instanceQuery = `SELECT work_id,workflow_type,step_plan,prelude_state,instance_state FROM workflow_instances ORDER BY work_id`
			before := captureEvidenceRows(t, s.db, instanceQuery)
			manifest := captureEvidenceRows(t, s.db, `SELECT version,name,checksum,applied_at,breaking FROM schema_migrations ORDER BY version`)
			path := s.Path()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			opened, openErr := Open(ctx, path)
			if opened != nil {
				defer opened.Close()
			}
			report, upgradeErr := Upgrade(ctx, path, nil)
			plan, readinessErr := PlanUpgradeReadiness(ctx, path)
			if breaking {
				for _, err := range []error{openErr, upgradeErr, readinessErr} {
					assertFailureKind(t, err, KindSchemaUnsupported)
					if !strings.Contains(err.Error(), fmt.Sprint(future)) {
						t.Fatalf("floor refusal must name schema %d: %v", future, err)
					}
				}
			} else {
				if openErr != nil || upgradeErr != nil || readinessErr != nil {
					t.Fatalf("additive schema refused: open=%v upgrade=%v readiness=%v", openErr, upgradeErr, readinessErr)
				}
				if len(report.Applied) != 0 || report.SchemaVersion != future || plan.ActivationBlocked || plan.SchemaVersion != future || plan.CompatibilityFloor != CurrentSchemaVersion() {
					t.Fatalf("additive schema report=%+v readiness=%+v", report, plan)
				}
				if got := captureEvidenceRows(t, opened.db, instanceQuery); !reflect.DeepEqual(got, before) {
					t.Fatalf("open changed the current plan: got %v want %v", got, before)
				}
				currentVersion := readWorkVersion(t, opened, "work-skew")
				if err := ApplyOperation(ctx, opened, Operation{Events: []Event{{
					EventID: "work-skew-cancel", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "work-skew",
					Actor: "operator", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1,
					Payload: mustJSONValue(map[string]any{"from": "needed", "to": "cancelled", "reason": "fixture retirement", "expected_version": currentVersion, "resulting_version": currentVersion + 1}),
				}}, ExpectedVersions: workVersion("work-skew", currentVersion)}); err != nil {
					t.Fatalf("current write on additive schema: %v", err)
				}
			}
			db, err := openReadonlyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if got := captureEvidenceRows(t, db, `SELECT version,name,checksum,applied_at,breaking FROM schema_migrations ORDER BY version`); !reflect.DeepEqual(got, manifest) {
				t.Fatalf("routes changed manifest: got %v want %v", got, manifest)
			}
			if breaking {
				if got := captureEvidenceRows(t, db, instanceQuery); !reflect.DeepEqual(got, before) {
					t.Fatalf("refused routes changed work: got %v want %v", got, before)
				}
			}
		})
	}
}

func TestBinaryFloorCutoverRequiresExplicitUpgradeAndDrainedLease(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pre-cutover.db")
	db := openMigratedTo(t, path, 182)
	defer db.Close()
	const manifestQuery = `SELECT version,name,checksum,applied_at,breaking FROM schema_migrations ORDER BY version`
	before := captureEvidenceRows(t, db, manifestQuery)
	plan, err := PlanUpgradeReadiness(ctx, path)
	if err != nil || !plan.ActivationBlocked || len(plan.PendingBreaking) == 0 || plan.PendingBreaking[0].Version != 183 {
		t.Fatalf("pre-cutover readiness=%+v err=%v", plan, err)
	}
	opened, err := Open(ctx, path)
	if opened != nil {
		opened.Close()
	}
	assertFailureKind(t, err, KindUpgradeRequired)
	_, err = Upgrade(ctx, path, []HeldSchema{{PID: 1234, SchemaVersion: 182}})
	assertFailureKind(t, err, KindUpgradeBlocked)
	if got := captureEvidenceRows(t, db, manifestQuery); !reflect.DeepEqual(got, before) {
		t.Fatalf("open or blocked upgrade changed manifest: got %v want %v", got, before)
	}
	report, err := Upgrade(ctx, path, nil)
	if err != nil || report.SchemaVersion != CurrentSchemaVersion() || len(report.Applied) != 1 || report.Applied[0] != 183 {
		t.Fatalf("drained upgrade=%+v err=%v", report, err)
	}
	plan, err = PlanUpgradeReadiness(ctx, path)
	if err != nil || plan.ActivationBlocked || plan.CompatibilityFloor != 183 {
		t.Fatalf("post-cutover readiness=%+v err=%v", plan, err)
	}
	opened, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("open after upgrade: %v", err)
	}
	defer opened.Close()
}

func TestRuntimeStateWriterCommitsWithItsRepresentation(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for _, commit := range []bool{false, true} {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := recordRuntimeStateWriter(ctx, tx, knowledgeStateWriter(1)); err != nil {
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

func TestBinaryCompatibilityDiagnosticDoesNotGuessLegacyWriter(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	writer := knowledgeStateWriter(7)
	err := binaryCompatibilityFailure(ctx, s.db, writer, "historical knowledge state")
	assertFailureKind(t, err, KindSchemaUnsupported)
	if !strings.Contains(err.Error(), "unknown (writer provenance was not recorded)") {
		t.Fatalf("missing writer must remain unknown: %v", err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return recordRuntimeStateWriter(ctx, tx.tx, writer)
	}); err != nil {
		t.Fatal(err)
	}
	err = binaryCompatibilityFailure(ctx, s.db, writer, "historical knowledge state")
	assertFailureKind(t, err, KindSchemaUnsupported)
	if !strings.Contains(err.Error(), "state writer binary "+version.Value) {
		t.Fatalf("recorded writer missing from diagnostic: %v", err)
	}
}

// knowledgeStateWriter is the writer row a released binary recorded for the
// retired knowledge projection. Fixtures seed it as historical state.
func knowledgeStateWriter(projectionVersion int64) runtimeStateWriter {
	return runtimeStateWriter{surface: "knowledge", projectionVersion: projectionVersion}
}
