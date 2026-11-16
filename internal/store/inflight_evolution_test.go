package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestInFlightWorkflowSurvivesSchemaMigration(t *testing.T) {
	original := migrations
	t.Cleanup(func() { migrations = original })
	migrations = append(append([]migration(nil), migrations...), migration{
		Version: CurrentSchemaVersion() + 1, Name: "inflight_schema_tail", SQL: `CREATE TABLE inflight_schema_tail (value TEXT);`,
	})
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-inflight.db")
	tail := migrations[len(migrations)-1]
	db := openMigratedTo(t, path, tail.Version-1)
	s := &Store{db: db, path: path}
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}
	const workID = "schema-evolution-work"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:evolution", ClientRef: "client:evolution", AgentRef: "agent:evolution", SessionRef: "session:evolution", ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanFix, "diagnose")
	beforePlan, err := readWorkflowPlanTx(ctx, db, workID)
	if err != nil || beforePlan == nil {
		t.Fatalf("plan before migration: %+v, %v", beforePlan, err)
	}
	version := readWorkVersion(t, s, workID)
	beforeVersion, err := readSchemaManifestVersion(ctx, db)
	if err != nil || beforeVersion >= tail.Version {
		t.Fatalf("pre-tail version=%d tail=%d err=%v", beforeVersion, tail.Version, err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migration with in-flight work: %v", err)
	}
	afterVersion, err := readSchemaManifestVersion(ctx, db)
	if err != nil || afterVersion != CurrentSchemaVersion() || afterVersion <= beforeVersion {
		t.Fatalf("schema %d -> %d err=%v", beforeVersion, afterVersion, err)
	}
	migrations = append(append([]migration(nil), migrations...), migration{Version: CurrentSchemaVersion() + 1, Name: "inflight_schema_probe", SQL: `CREATE TABLE inflight_schema_probe (value TEXT);`})
	probeBefore, err := readSchemaManifestVersion(ctx, db)
	if err != nil || probeBefore != afterVersion {
		t.Fatalf("probe before=%d want=%d err=%v", probeBefore, afterVersion, err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("probe migration with in-flight work: %v", err)
	}
	probeAfter, err := readSchemaManifestVersion(ctx, db)
	if err != nil || probeAfter != CurrentSchemaVersion() || probeAfter <= probeBefore {
		t.Fatalf("schema %d -> %d err=%v", probeBefore, probeAfter, err)
	}
	afterPlan, err := readWorkflowPlanTx(ctx, db, workID)
	if err != nil || !reflect.DeepEqual(beforePlan, afterPlan) {
		t.Fatalf("plan changed: before=%+v after=%+v err=%v", beforePlan, afterPlan, err)
	}
	diagnosis := workflowTypedEvent("schema-evolution-diagnosis", WorkflowActionCompleted, workID, actorRef, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), version, map[string]any{"step_id": "diagnose", "action_id": "record_diagnosis", "attempt_epoch": 1, "actor_ref": actorRef, "result_evidence_refs": []string{}, "changed_refs": []string{workID}})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{diagnosis}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("in-flight diagnosis could not record: %v", err)
	}
	version = readWorkVersion(t, s, workID)
	event := workflowTypedEvent("schema-evolution-step", WorkflowStepCompleted, workID, actorRef, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), version, map[string]any{"step_id": "diagnose", "note": "diagnosis completed"})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("in-flight work could not advance: %v", err)
	}
	plan, err := readWorkflowPlanTx(ctx, db, workID)
	if err != nil || plan == nil {
		t.Fatalf("plan after advance: %+v, %v", plan, err)
	}
	step, ok := stepByID(plan.Steps, "diagnose")
	if !ok || len(step.Attempts) != 1 || readWorkVersion(t, s, workID) != version+1 {
		t.Fatalf("plan did not advance: %+v", plan)
	}
	var events int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events < 2 {
		t.Fatalf("work history has %d events after migration", events)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migration retry: %v", err)
	}
}
