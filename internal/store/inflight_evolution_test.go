package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// Floor condition 5 requires that a schema or workflow-definition change lands
// without corrupting or stranding work already in progress. Migration safety and
// event readability are each proven elsewhere in isolation; these tests prove the
// composition, where an item is already mid-flight when the change lands.

func workflowInstancePin(t *testing.T, s *Store, workID string) (string, int64, string) {
	t.Helper()
	var ref, digest string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version, &digest); err != nil {
		t.Fatal(err)
	}
	return ref, version, digest
}

func startWorkflowPinnedTo(t *testing.T, s *Store, workID string, definition RegisteredDefinition) (WorkflowActor, int64) {
	t.Helper()
	ctx := context.Background()
	seedWork(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:evolution", ClientRef: "client:evolution", AgentRef: "agent:evolution", SessionRef: "session:evolution", ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
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

// A newer definition version must not rebase, strand, or silently upgrade an
// item that is already mid-flight on an older pin. The supersession is a
// registry property, so it is proven against the test fixture family, which
// carries two versions; the shipped built-ins carry exactly one version each.
func TestInFlightWorkflowSurvivesDefinitionVersionSupersession(t *testing.T) {
	s := openTemp(t)
	const workID = "definition-evolution-work"
	registry := BuiltinWorkflowRegistry()
	v1 := workflowFixtureDefinition(t, 1)
	latest := workflowFixtureDefinition(t, 2)
	if latest.Definition.Version <= v1.Definition.Version || latest.Digest == v1.Digest {
		t.Fatalf("registry does not carry a superseding version: v%d %s vs v%d %s", v1.Definition.Version, v1.Digest, latest.Definition.Version, latest.Digest)
	}

	actor, version := startWorkflowPinnedTo(t, s, workID, v1)

	// The newer version is registered and resolvable as latest for the same ref
	// while this item is mid-flight. That is the change landing.
	ref, pinnedVersion, pinnedDigest := workflowInstancePin(t, s, workID)
	if ref != v1.Definition.Ref || pinnedVersion != v1.Definition.Version || pinnedDigest != v1.Digest {
		t.Fatalf("pin after start = %s v%d %s, want the v1 pin", ref, pinnedVersion, pinnedDigest)
	}

	// Not stranded: an action declared by the pinned version still advances.
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

	// Not silently upgraded. The superseding version differs by introducing
	// accept_worker_result, so resolving this item's own pin must still yield the
	// definition that lacks it. This asserts through VerifyWorkflowDefinitionPin —
	// the resolution the engine performs before every action — rather than
	// inferring the pin from an action rejection that a step guard could equally
	// explain.
	if containsString(v1.Definition.AvailableActions, "accept_worker_result") {
		t.Fatal("v1 already declares accept_worker_result; the supersession no longer differs")
	}
	if !containsString(latest.Definition.AvailableActions, "accept_worker_result") {
		t.Fatal("the superseding version does not declare accept_worker_result")
	}
	resolved, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: ref, Version: pinnedVersion, Digest: pinnedDigest})
	if err != nil {
		t.Fatalf("in-flight pin no longer resolves after supersession: %v", err)
	}
	if resolved.Definition.Version != v1.Definition.Version || containsString(resolved.Definition.AvailableActions, "accept_worker_result") {
		t.Fatalf("engine resolved v%d for an item pinned to v%d", resolved.Definition.Version, v1.Definition.Version)
	}

	// The pin is unchanged by the advance.
	ref, pinnedVersion, pinnedDigest = workflowInstancePin(t, s, workID)
	if ref != v1.Definition.Ref || pinnedVersion != v1.Definition.Version || pinnedDigest != v1.Digest {
		t.Fatalf("pin drifted to %s v%d %s", ref, pinnedVersion, pinnedDigest)
	}

	// History remains readable: the full log rebuilds to the same projection.
	before := projectionSnapshot(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild after supersession: %v", err)
	}
	if after := projectionSnapshot(t, s); after != before {
		t.Fatalf("rebuild drifted projections after supersession")
	}
}

// A schema migration applied while an item is mid-flight must leave it advanceable
// and its history readable.
func TestInFlightWorkflowSurvivesSchemaMigration(t *testing.T) {
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
	priorMigrations := migrations[:len(migrations)-1]
	for _, migration := range priorMigrations {
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
	actor, version := startWorkflowPinnedTo(t, s, workID, definition)
	beforeVersion, err := readSchemaManifestVersion(ctx, db)
	if err != nil {
		t.Fatal(err)
	}

	// The change lands while the item is open, not before it starts.
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

	// Not corrupted: the pin survives the migration byte for byte.
	ref, pinnedVersion, pinnedDigest := workflowInstancePin(t, s, workID)
	if ref != definition.Definition.Ref || pinnedVersion != definition.Definition.Version || pinnedDigest != definition.Digest {
		t.Fatalf("pin after migration = %s v%d %s, want %s v%d %s", ref, pinnedVersion, pinnedDigest, definition.Definition.Ref, definition.Definition.Version, definition.Digest)
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
	before := projectionSnapshot(t, s)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild after migration: %v", err)
	}
	if after := projectionSnapshot(t, s); after != before {
		t.Fatal("rebuild drifted projections after migration")
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("re-running the migration on in-flight work failed: %v", err)
	}
}
