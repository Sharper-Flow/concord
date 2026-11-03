package store

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
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
	db := openMigratedTo(t, path, len(migrations)-1)
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

func TestInFlightWorkflowSurvivesRemoteLocatorUpgrade(t *testing.T) {
	ctx := context.Background()
	s := remoteUpgradeFixtureForProject(t, "project", ProjectLocator{ID: "remote", Kind: LocatorGitRemote, Value: "https://host/o/r"})
	beforeVersion, err := readSchemaManifestVersion(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if beforeVersion != 127 {
		t.Fatalf("remote fixture schema=%d, want predecessor 127", beforeVersion)
	}
	if got := retirementUpgradeRows(t, s.db, `SELECT normalized_value FROM project_locators WHERE locator_id='remote'`); !reflect.DeepEqual(got, [][]any{{"ssh://https/host/o/r"}}) {
		t.Fatalf("remote fixture did not retain v1 identity: %v", got)
	}
	const parked = "remote-upgrade-inflight-work"
	const delivered = "remote-upgrade-delivered-work"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, workID := range []string{parked, delivered} {
		event := workCreatedEvent(workID, "create-"+workID)
		result, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, event.EventID, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload))
		if err != nil {
			t.Fatal(err)
		}
		event.Seq, err = result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		seedPreQ7WorkCreated(t, tx, event)
		membership := operationEvent("membership-"+workID, "work_project.added", SubjectWorkItem, workID, map[string]any{"work_id": workID, "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, membership.EventID, membership.Kind, membership.SubjectType, membership.SubjectID, membership.Actor, membership.OccurredAt.UTC().Format(time.RFC3339Nano), membership.PayloadVersion, string(membership.Payload)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id,project_id,role) VALUES(?,'project','primary'); UPDATE work_items SET version=2,lifecycle='in_progress' WHERE id=?`, workID, workID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state) VALUES(?,?,1,?,?,'running')`, workID, "workflow.legacy", "sha256:"+strings.Repeat("a", 64), "legacy"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('delivery:retained','workflow.action_completed','work_item',?,'actor:historical','2026-01-01T00:00:00Z',1,'{"step_id":"legacy","action_id":"record_delivery","delivery_artifact":"https://example.com/change/1","delivery_state":"asserted"}')`, delivered); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	if err := ensureInstallationKey(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	afterVersion, err := readSchemaManifestVersion(ctx, s.db)
	if err != nil || afterVersion <= beforeVersion || afterVersion != CurrentSchemaVersion() {
		t.Fatalf("schema %d -> %d err=%v", beforeVersion, afterVersion, err)
	}
	locators, err := s.ProjectLocators(ctx, "project")
	if err != nil || len(locators) != 1 || locators[0].NormalizedValue != "https://host/o/r" {
		t.Fatalf("remote not corrected: %+v err=%v", locators, err)
	}
	if got := retirementUpgradeRows(t, s.db, `SELECT kind,subject_type,subject_id FROM effect_receipts WHERE event_id=?`, remoteLocatorUpgradeName+":remote"); !reflect.DeepEqual(got, [][]any{{"project.locator_updated", "project", "project"}}) {
		t.Fatalf("remote correction receipt=%v", got)
	}
	got := retirementUpgradeRows(t, s.db, `SELECT w.id,w.lifecycle,i.instance_state,i.workflow_type,i.step_plan FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id IN (?,?) ORDER BY w.id`, parked, delivered)
	want := [][]any{{delivered, "completed", "completed", nil, nil}, {parked, "in_progress", "intake", nil, nil}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cutover states=%v want %v", got, want)
	}
	var terminalTime string
	if err := s.db.QueryRowContext(ctx, `SELECT terminal_time FROM work_items WHERE id=?`, delivered).Scan(&terminalTime); err != nil || terminalTime != "2026-01-01T00:00:00Z" {
		t.Fatalf("delivery terminal time=%q err=%v", terminalTime, err)
	}
	pin, err := ReadWorkPin(ctx, s, parked)
	if err != nil || pin.ProjectID != "project" || pin.Step != "intake" || pin.Version != 2 {
		t.Fatalf("parked work after migration: %+v err=%v", pin, err)
	}
	var receipts int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, parked).Scan(&receipts); err != nil || receipts < 2 {
		t.Fatalf("parked work history=%d err=%v", receipts, err)
	}
}
