package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Floor condition 5 requires that a schema or workflow-definition change lands
// without corrupting or stranding work already in progress. Migration safety and
// event readability are each proven elsewhere in isolation; these tests prove the
// composition, where an item is already mid-flight when the change lands.

func workflowInstancePin(t *testing.T, s *Store, workID string) (string, string) {
	t.Helper()
	var ref, digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &digest); err != nil {
		t.Fatal(err)
	}
	return ref, digest
}

func startWorkflowPinnedTo(t *testing.T, s *Store, workID string, definition RegisteredDefinition) (WorkflowActor, int64) {
	return startWorkflowPinnedToContext(t, context.Background(), s, workID, definition)
}

func startWorkflowPinnedToContext(t *testing.T, ctx context.Context, s *Store, workID string, definition RegisteredDefinition) (WorkflowActor, int64) {
	t.Helper()
	seedWork(t, s, workID)
	return startSeededWorkflowPinnedToContext(t, ctx, s, workID, definition)
}

func startSeededWorkflowPinnedToContext(t *testing.T, ctx context.Context, s *Store, workID string, definition RegisteredDefinition) (WorkflowActor, int64) {
	t.Helper()
	actor := WorkflowActor{PrincipalRef: "principal:evolution", ClientRef: "client:evolution", AgentRef: "agent:evolution", SessionRef: "session:evolution", ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	start := workflowEventWithActor("evolution-start-"+workID, WorkflowActionStarted, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"step_id": "proposal", "action_id": "record_proposal", "attempt_epoch": 1,
		"accepted_inputs_digest": "sha256:evolution-start", "idempotency_identity": "evolution-start:" + workID,
		"actor_ref": actorRef, "execution_model": preferredModelForLane(BuiltinLaneDefinitions()[0]),
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return actor, version + 1
}

// Definition changes resolve through current content without stranding an
// in-flight item or rewriting its recorded selection metadata.
func TestInFlightWorkflowSurvivesDefinitionContentChange(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const workID = "definition-evolution-work"
	registry := NewWorkflowDefinitionRegistry()
	v1 := workflowFixtureDefinition(t, 1)
	if _, err := registry.Register(v1.Definition); err != nil {
		t.Fatal(err)
	}

	actor, version := startWorkflowPinnedTo(t, s, workID, v1)

	changed := cloneWorkflowDefinition(v1.Definition)
	changed.RequiredEvidenceKinds = append(changed.RequiredEvidenceKinds, EvidenceDurableNote)
	latest, err := registry.Register(changed)
	if err != nil || latest.Digest == v1.Digest {
		t.Fatalf("changed content did not register a distinct digest: %+v, %v", latest, err)
	}
	ref, pinnedDigest := workflowInstancePin(t, s, workID)
	if ref != v1.Definition.Ref || pinnedDigest != v1.Digest {
		t.Fatalf("pin after start = %s %s, want the recorded selection", ref, pinnedDigest)
	}

	// A shared action still advances the in-flight item.
	next, err := continuityAction(t, s, workID, version, "checkpoint_context", "evolution-checkpoint", map[string]any{
		"active_unit": "unit:implementation", "hypothesis": "hypothesis:one", "diagnosis": "diagnosis:one", "strategy": "strategy:one",
		"touched_refs": []string{"ref:file"}, "evidence_refs": []string{"evidence:one"}, "pending_questions": []string{}, "pending_decisions": []string{},
	}, actor)
	if err != nil {
		t.Fatalf("pinned-version action failed after supersession: %v", err)
	}
	if next <= version {
		t.Fatalf("resulting version=%d did not advance past %d", next, version)
	}

	resolved, err := VerifyWorkflowInstanceDefinition(context.Background(), s, registry, workID)
	if err != nil {
		t.Fatalf("in-flight pin no longer resolves after supersession: %v", err)
	}
	if resolved.Digest != latest.Digest || !slices.Contains(resolved.Definition.RequiredEvidenceKinds, EvidenceDurableNote) {
		t.Fatalf("engine did not resolve current definition content: %+v", resolved)
	}
	if _, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: ref, Digest: pinnedDigest}); !hasFailureKind(err, KindInvariantViolation) {
		t.Fatalf("obsolete explicit digest refusal = %v", err)
	}

	// The pin is unchanged by the advance.
	ref, pinnedDigest = workflowInstancePin(t, s, workID)
	if ref != v1.Definition.Ref || pinnedDigest != v1.Digest {
		t.Fatalf("recorded selection drifted to %s %s", ref, pinnedDigest)
	}

}

// A schema migration applied while an item is mid-flight must leave it advanceable
// and its history readable.
func TestInFlightWorkflowSurvivesSchemaMigration(t *testing.T) {
	original := migrations
	t.Cleanup(func() { migrations = original })
	migrations = append(append([]migration(nil), migrations...), migration{
		Version: CurrentSchemaVersion() + 1, Name: "inflight_schema_tail",
		SQL: `CREATE TABLE inflight_schema_tail (value TEXT);`,
	})
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-inflight.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	// Build the pre-tail schema from the registered migrations: the prefix
	// steps apply directly and the final registered step stays for Migrate
	// to execute below.
	prefix := migrations[:len(migrations)-1]
	tail := migrations[len(migrations)-1]
	for _, migration := range prefix {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-11T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{db: db, path: path}
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}

	const workID = "schema-evolution-work"
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	// The item starts mid-flight on the pre-tail schema through the
	// production initialize path, before the final registered migration
	// applies.
	actor, version := startWorkflowPinnedTo(t, s, workID, definition)
	beforeVersion, err := readSchemaManifestVersion(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if beforeVersion >= tail.Version {
		t.Fatalf("pre-tail manifest version %d already records the tail step %d", beforeVersion, tail.Version)
	}

	// The change lands while the item is open, not before it starts. Migrate
	// executes the real tail step here: its manifest row exists only through
	// that execution, written in the same transaction as the step's SQL.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migration on a database with in-flight work: %v", err)
	}
	afterVersion, err := readSchemaManifestVersion(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if afterVersion != CurrentSchemaVersion() || afterVersion <= beforeVersion {
		t.Fatalf("schema version %d -> %d, want an advance to %d", beforeVersion, afterVersion, CurrentSchemaVersion())
	}

	// The registered tail is spent, so the next advance is a synthetic step
	// beyond it: the open item crosses that boundary on the combined schema
	// too, and the manifest tracks it the same way.
	migrations = append(append([]migration(nil), migrations...), migration{
		Version: CurrentSchemaVersion() + 1, Name: "inflight_schema_probe",
		SQL: `CREATE TABLE inflight_schema_probe (value TEXT);`,
	})
	probeBeforeVersion, err := readSchemaManifestVersion(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if probeBeforeVersion != afterVersion {
		t.Fatalf("pre-probe manifest version %d moved from the migrated version %d", probeBeforeVersion, afterVersion)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migration on a database with in-flight work: %v", err)
	}
	probeAfterVersion, err := readSchemaManifestVersion(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if probeAfterVersion != CurrentSchemaVersion() || probeAfterVersion <= probeBeforeVersion {
		t.Fatalf("schema version %d -> %d, want an advance to %d", probeBeforeVersion, probeAfterVersion, CurrentSchemaVersion())
	}

	// Not corrupted: the pin survives the migration byte for byte.
	ref, pinnedDigest := workflowInstancePin(t, s, workID)
	if ref != definition.Definition.Ref || pinnedDigest != definition.Digest {
		t.Fatalf("pin after migration = %s %s, want %s %s", ref, pinnedDigest, definition.Definition.Ref, definition.Digest)
	}

	// Not stranded: the item still advances after the change.
	if _, err := continuityAction(t, s, workID, version, "checkpoint_context", "schema-evolution-checkpoint", map[string]any{
		"active_unit": "unit:implementation", "hypothesis": "hypothesis:one", "diagnosis": "diagnosis:one", "strategy": "strategy:one",
		"touched_refs": []string{"ref:file"}, "evidence_refs": []string{"evidence:one"}, "pending_questions": []string{}, "pending_decisions": []string{},
	}, actor); err != nil {
		t.Fatalf("in-flight work could not advance after migration: %v", err)
	}

	// History remains readable across the change.
	var events int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events < 2 {
		t.Fatalf("work history has %d events after migration", events)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("re-running the migration on in-flight work failed: %v", err)
	}
}

func TestInFlightWorkflowSurvivesRemoteLocatorUpgrade(t *testing.T) {
	ctx := context.Background()
	s := remoteUpgradeFixtureForProject(t, "project", ProjectLocator{ID: "remote", Kind: LocatorGitRemote, Value: "https://host/o/r"})
	beforeVersion, err := readSchemaManifestVersion(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	const workID = "remote-upgrade-inflight-work"
	actor := WorkflowActor{PrincipalRef: "principal:evolution", ClientRef: "client:evolution", AgentRef: "agent:evolution", SessionRef: "session:evolution", ActorClass: ActorAgent}
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	// Seed the older authority's open work without current-binary write admission.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	// Historical rows predate receipts. Seed the original effects and their
	// projections without asking the current append seam to write old schema.
	selection := workflowEventWithActor("workflow-init:"+workID+":definition:"+definition.Definition.Ref, WorkflowDefinitionSelected, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": 3, "resulting_version": 4,
		"ref": definition.Definition.Ref, "version": 1,
		"digest": definition.Digest, "work_kind": definition.Definition.WorkKind,
	})
	events := []Event{
		workCreatedEvent(workID, "create-"+workID),
		operationEvent("membership-"+workID, "work_project.added", SubjectWorkItem, workID, map[string]any{
			"work_id": workID, "project_id": "project", "role": "primary", "reason": "test",
			"expected_version": 1, "resulting_version": 2,
		}),
		workflowEventWithActor("workflow-init:"+workID+":actor:"+actorRef, WorkflowActorRecorded, workID, actorRef, map[string]any{
			"work_id": workID, "expected_version": 2, "resulting_version": 3,
			"actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef,
			"agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": actor.ActorClass,
		}),
		selection,
	}
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		result, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, event.EventID, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload))
		if err != nil {
			t.Fatal(err)
		}
		event.Seq, err = result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == "work.created" {
			seedPreQ7WorkCreated(t, tx, event)
			continue
		}
		if event.EventID == selection.EventID {
			continue
		}
		if err := foldRegisteredEvent(ctx, tx, event, scope); err != nil {
			t.Fatal(err)
		}
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	startStep, err := workflowDefinitionStartStep(definition.Definition)
	if err != nil {
		t.Fatal(err)
	}
	// The older store already owns an initialized, in-progress workflow.
	// Write the older selection projection; do not run current-binary
	// definition or action-start writers on its older schema.
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE work_items SET version=4,updated_at=? WHERE id=? AND version=3`, selection.OccurredAt.UTC().Format(time.RFC3339Nano), workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state) VALUES(?,?,1,?,?,'planned')`, workID, definition.Definition.Ref, definition.Digest, startStep); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE workflow_instances SET instance_state='running',execution_actor_ref=?,execution_model=? WHERE work_id=?`, actorRef, preferredModelForLane(BuiltinLaneDefinitions()[0]), workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE work_items SET lifecycle='in_progress' WHERE id=?`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	var beforeStep string
	var beforeWorkVersion int64
	if err := s.db.QueryRowContext(ctx, `SELECT i.current_step,w.version FROM workflow_instances i JOIN work_items w ON w.id=i.work_id WHERE w.id=?`, workID).Scan(&beforeStep, &beforeWorkVersion); err != nil {
		t.Fatal(err)
	}
	if beforeStep != "proposal" || beforeWorkVersion != version {
		t.Fatalf("work must be open before migration: step=%s version=%d, want proposal/%d", beforeStep, beforeWorkVersion, version)
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
		t.Fatalf("remote not corrected for in-flight Project: %+v err=%v", locators, err)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil || pin.ProjectID != "project" || pin.Step != "proposal" || pin.Version != version {
		t.Fatalf("work after migration: %+v err=%v", pin, err)
	}
	next, err := continuityAction(t, s, workID, version, "record_proposal", "remote-upgrade-proposal", map[string]any{}, actor)
	if err != nil || next <= version {
		t.Fatalf("in-flight action version=%d err=%v", next, err)
	}
	pin, err = ReadWorkPin(ctx, s, workID)
	if err != nil || pin.Step != "alignment" || pin.Version != next {
		t.Fatalf("work did not advance: %+v err=%v", pin, err)
	}
	ref, digest := workflowInstancePin(t, s, workID)
	if ref != definition.Definition.Ref || digest != definition.Digest {
		t.Fatalf("workflow pin drifted: %s %s", ref, digest)
	}
}
