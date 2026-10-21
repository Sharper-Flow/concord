package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorkflowTimingRecordsTheFirstAgentAction(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	workID := "work-timing-first"
	seedStepWork(t, s, workID)
	registry := NewWorkflowDefinitionRegistry()
	definition := mustCompileWorkflowComposition(implementationCompositionV33())
	entry, err := registry.Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := stepFixtureActor()
	bootstrap.ActorClass = ActorOperator
	bootstrap.SessionRef = "session-timing-bootstrap"
	ctx := context.Background()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: entry, Registry: registry, Actor: bootstrap, Now: time.Unix(5, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_timing WHERE work_id=?`, workID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("operator bootstrap recorded timing: rows=%d, error=%v", count, err)
	}
	actor := stepFixtureActor()
	at := time.Unix(10, 123).UTC()
	request := timingAction(t, s, workID, "record_proposal", "timing-first", actor, timingProposal(), at)
	if err := invokeTimingAction(ctx, s, registry, request); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := s.db.QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_proposal'`, workID, WorkflowActionCompleted).Scan(&source); err != nil {
		t.Fatal(err)
	}
	var gotSource, gotAt string
	if err := s.db.QueryRow(`SELECT define_event_id,define_started_at FROM workflow_timing WHERE work_id=?`, workID).Scan(&gotSource, &gotAt); err != nil {
		t.Fatal(err)
	}
	if gotSource != source || gotAt != at.Format(time.RFC3339Nano) {
		t.Fatalf("define source/time = %q/%q, want %q/%q", gotSource, gotAt, source, at.Format(time.RFC3339Nano))
	}
	request.ActionID = "record_alignment"
	request.Payload = mustJSONValue(map[string]any{"searched": "The bounded search area.", "outcome": "none_found"})
	request.ExpectedVersion = readWorkVersion(t, s, workID)
	request.OperationID = "timing-later"
	request.IdempotencyIdentity = "timing-later"
	request.IdempotencyKey = "timing-later"
	request.RequestID = "request:timing-later"
	request.Now = at.Add(-time.Second)
	if err := invokeTimingAction(ctx, s, registry, request); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT define_event_id,define_started_at FROM workflow_timing WHERE work_id=?`, workID).Scan(&gotSource, &gotAt); err != nil {
		t.Fatal(err)
	}
	if gotSource != source || gotAt != at.Format(time.RFC3339Nano) {
		t.Fatalf("later action replaced define source/time: %q/%q", gotSource, gotAt)
	}
}

func invokeTimingAction(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionExecutionRequest) error {
	preflight := WorkflowActionPreflightRequest{WorkID: request.WorkID, ExpectedVersion: request.ExpectedVersion, ActionID: request.ActionID, Payload: request.Payload, Actor: request.Actor}
	return AuthorizeWorkflowActionAtBoundaryTx(ctx, s, registry, preflight, nil, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, registry, request)
		return err
	})
}

func TestWorkflowTimingReleasedDefinitionsHaveNoShipAction(t *testing.T) {
	t.Parallel()
	for _, chain := range workflowDefinitionChains() {
		for _, definition := range chain {
			for _, step := range definition.StepGraph.TerminalSteps {
				if definitionStepAllows(definition, step, "record_delivery") {
					t.Errorf("released %s v%d acquired a ship action", definition.Ref, definition.Version)
				}
			}
		}
	}
}

func timingAction(t *testing.T, s *Store, workID, actionID, id string, actor WorkflowActor, payload map[string]any, at time.Time) WorkflowActionExecutionRequest {
	t.Helper()
	return WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: actionID, Actor: actor, Payload: mustJSONValue(payload),
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: id, OperationID: id,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id, RequestID: "request:" + id,
		ContractDigest: testManifestDigest, Now: at,
	}
}

func timingProposal() map[string]any {
	return map[string]any{"problem": "A bounded problem.", "affected": []string{"The test subject."}, "stakes": "The bounded stakes.", "user_outcomes": []string{"The expected outcome."}}
}

func timingSnapshot(t *testing.T, s *Store, workID string) workflowTiming {
	t.Helper()
	result, err := readWorkflowTiming(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestWorkflowTimingIgnoresOperatorAndTypedOnlyActions(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const workID = "timing-actor-types"
	seedStepWork(t, s, workID)
	definition := mustCompileWorkflowComposition(implementationCompositionV33())
	initializeStepWorkflow(t, s, workID, definition)
	ctx := context.Background()
	actor := stepFixtureActor()
	checkpoint := timingAction(t, s, workID, "checkpoint_context", "timing-typed", actor, map[string]any{
		"active_unit": "The proposal.", "hypothesis": "A bounded hypothesis.", "diagnosis": "A bounded diagnosis.", "strategy": "A bounded strategy.",
		"touched_refs": []string{"internal/store"}, "evidence_refs": []string{"evidence:timing"}, "pending_questions": []string{}, "pending_decisions": []string{},
	}, time.Unix(10, 0).UTC())
	if err := invokeTimingAction(ctx, s, BuiltinWorkflowRegistry(), checkpoint); err != nil {
		t.Fatal(err)
	}
	if got := timingSnapshot(t, s, workID); got.DefineEventID != "" {
		t.Fatalf("typed-only action started define: %+v", got)
	}
	operator := actor
	operator.ActorClass = ActorOperator
	operator.SessionRef = "session:timing-operator"
	proposal := timingAction(t, s, workID, "record_proposal", "timing-operator", operator, timingProposal(), time.Unix(11, 0).UTC())
	if err := invokeTimingAction(ctx, s, BuiltinWorkflowRegistry(), proposal); err != nil {
		t.Fatal(err)
	}
	if got := timingSnapshot(t, s, workID); got.DefineEventID != "" {
		t.Fatalf("operator action started define: %+v", got)
	}
	agent := timingAction(t, s, workID, "record_alignment", "timing-agent", actor, map[string]any{"searched": "The bounded search area.", "outcome": "none_found"}, time.Unix(12, 0).UTC())
	if err := invokeTimingAction(ctx, s, BuiltinWorkflowRegistry(), agent); err != nil {
		t.Fatal(err)
	}
	if got := timingSnapshot(t, s, workID); got.DefineStartedAt != agent.Now.Format(time.RFC3339Nano) {
		t.Fatalf("define did not start at the first agent completion: %+v", got)
	}
}

func TestWorkflowTimingRollsBackWithTheAction(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "timing-rollback"
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, mustCompileWorkflowComposition(implementationCompositionV33()))
	ctx := context.Background()
	request := timingAction(t, s, workID, "record_proposal", "timing-rollback", stepFixtureActor(), timingProposal(), time.Unix(15, 0).UTC())
	preflight := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: request.ExpectedVersion, ActionID: request.ActionID, Payload: request.Payload, Actor: request.Actor}
	abort := errors.New("abort the enclosing transaction")
	err := AuthorizeWorkflowActionAtBoundaryTx(ctx, s, BuiltinWorkflowRegistry(), preflight, nil, func(tx *Transaction) error {
		if _, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), request); err != nil {
			return err
		}
		inside, err := readWorkflowTiming(ctx, tx.tx, workID)
		if err != nil {
			return err
		}
		if inside.DefineEventID == "" {
			return errors.New("action did not write timing inside the transaction")
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("rollback error = %v", err)
	}
	if got := timingSnapshot(t, s, workID); got.DefineEventID != "" {
		t.Fatalf("rolled-back timing remained: %+v", got)
	}
	if version := readWorkVersion(t, s, workID); version != request.ExpectedVersion {
		t.Fatalf("rolled-back action changed the version: %d", version)
	}
}

func TestWorkflowTimingFollowsTheResizeRootAndRebuilds(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const root = "timing-resize-root"
	pinnedLargerWork(t, s, root)
	first, err := overrideWorkSize(t, s, root, "timing-first-resize", WorkSizeCrucial)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := timingAction(t, s, first.SuccessorWorkID, "record_proposal", "timing-successor-action", resizeTestAgent, timingProposal(), time.Unix(20, 42).UTC())
	if err := invokeTimingAction(ctx, s, BuiltinWorkflowRegistry(), request); err != nil {
		t.Fatal(err)
	}
	second, err := overrideWorkSize(t, s, first.SuccessorWorkID, "timing-second-resize", WorkSizeLarger)
	if err != nil {
		t.Fatal(err)
	}
	var source string
	if err := s.db.QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_proposal'`, first.SuccessorWorkID, WorkflowActionCompleted).Scan(&source); err != nil {
		t.Fatal(err)
	}
	want := workflowTiming{RootWorkID: root, DefineEventID: source, DefineStartedAt: request.Now.Format(time.RFC3339Nano)}
	ids := []string{root, first.SuccessorWorkID, second.SuccessorWorkID}
	for _, id := range ids {
		if got := timingSnapshot(t, s, id); got != want {
			t.Fatalf("timing for %s = %+v, want %+v", id, got, want)
		}
	}
	var reserved int
	if err := s.db.QueryRow(`SELECT count(*) FROM work_resize_lineage WHERE root_define_start_event_id IS NOT NULL`).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("changed released resize provenance: count=%d, error=%v", reserved, err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if got := timingSnapshot(t, s, id); got != want {
			t.Fatalf("rebuilt timing for %s = %+v, want %+v", id, got, want)
		}
	}
}

func TestWorkflowTimingExcludesTheNativeDerivedFailure(t *testing.T) {
	t.Parallel()
	const workID = "timing-derived-failure"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 27, "repair")
	s := fixture.store
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	attempt := "attempt:" + workID
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, dispatcher, 0, "timing-failure-dispatch", &WorkerJobBinding{JobID: "job:" + workID})
	if err := s2WorkerFailedLive(t, s, s2WorkerFailedEvent(workID, attempt, "timing-native-failure", "The worker failed before reporting.", preferredModelForLane(reviewGateLane(t, "implementation")))); err != nil {
		t.Fatal(err)
	}
	records := s2DerivedFailureRecords(t, s, workID, attempt)
	if len(records) != 1 {
		t.Fatalf("derived failure records = %+v", records)
	}
	var qualified int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events e JOIN workflow_actors a ON a.actor_ref=e.actor WHERE `+workflowDefineStartPredicate+` AND e.event_id=?`, WorkflowActionCompleted, len(workflowDerivedWorkerFailureEventIDSuffix), workflowDerivedWorkerFailureEventIDSuffix, records[0]["event_id"]).Scan(&qualified); err != nil {
		t.Fatal(err)
	}
	if qualified != 0 {
		t.Fatal("a native core-derived failure qualified as an agent action")
	}
}

func TestWorkflowTimingMigrationReconstructsRecordedSources(t *testing.T) {
	t.Parallel()
	for _, removed := range []bool{false, true} {
		name := "live"
		if removed {
			name = "removed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			source := openTemp(t)
			defer source.Close()
			const workID = "timing-migration"
			seedStepWork(t, source, workID)
			initializeStepWorkflow(t, source, workID, mustCompileWorkflowComposition(implementationCompositionV33()))
			request := timingAction(t, source, workID, "record_proposal", "timing-before-upgrade", stepFixtureActor(), timingProposal(), time.Unix(20, 123).UTC())
			if err := invokeTimingAction(ctx, source, BuiltinWorkflowRegistry(), request); err != nil {
				t.Fatal(err)
			}
			want := timingSnapshot(t, source, workID)
			if removed {
				removal := removalTestRequest()
				removal.WorkID = workID
				removal.ExpectedVersion = readWorkVersion(t, source, workID)
				if _, err := source.RemoveWork(ctx, removal); err != nil {
					t.Fatal(err)
				}
			}
			readTx, err := source.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			events, err := readEvents(ctx, readTx)
			_ = readTx.Rollback()
			if err != nil {
				t.Fatal(err)
			}
			target := slices.IndexFunc(migrations, func(m migration) bool { return m.Name == "workflow_define_and_ship_timing" })
			if target < 0 {
				t.Fatal("timing migration is missing")
			}
			path := filepath.Join(t.TempDir(), "pre-timing.db")
			db := openMigratedTo(t, path, target)
			insertHistoricalEvents(t, db, events)
			if removed {
				if _, err := db.ExecContext(ctx, `ATTACH DATABASE ? AS timing_source`, source.path); err != nil {
					t.Fatal(err)
				}
			}
			// Populate only the migration's dependency projections from the genuine
			// source log. No action is executed by a new binary on the old schema.
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := enterFold(ctx, tx); err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Kind != WorkflowActionCompleted && event.Kind != WorkRemoved {
					if err := foldRegisteredEvent(ctx, tx, event); err != nil {
						t.Fatal(err)
					}
				}
			}
			if removed {
				if err := deleteWorkOwnedProjections(ctx, tx, workID); err != nil {
					t.Fatal(err)
				}
				// Removal receipts are direct authority, not replay projections.
				// Carry the real native receipt alongside its unchanged event.
				if _, err := tx.ExecContext(ctx, `INSERT INTO work_removal_operations SELECT * FROM timing_source.work_removal_operations WHERE work_id=?`, workID); err != nil {
					t.Fatal(err)
				}
			}
			if err := leaveFold(ctx, tx); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if removed {
				if _, err := db.ExecContext(ctx, `DETACH DATABASE timing_source`); err != nil {
					t.Fatal(err)
				}
			}
			before := domainEventDump(t, db)
			if err := Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			upgraded := &Store{db: db, path: path}
			projection := func() workflowTiming {
				t.Helper()
				got := workflowTiming{RootWorkID: workID}
				if err := db.QueryRowContext(ctx, `SELECT coalesce(define_event_id,''),coalesce(define_started_at,''),coalesce(queue_event_id,''),coalesce(queue_entered_at,''),coalesce(queue_artifact,'') FROM workflow_timing WHERE work_id=?`, workID).
					Scan(&got.DefineEventID, &got.DefineStartedAt, &got.QueueEventID, &got.QueueEnteredAt, &got.QueueArtifact); err != nil {
					t.Fatal(err)
				}
				return got
			}
			if got := projection(); got != want {
				t.Fatalf("migration timing = %+v, want recorded source %+v", got, want)
			}
			assertUnchangedDump(t, "migration source events", before, domainEventDump(t, db))
			if err := RebuildFromLog(ctx, upgraded); err != nil {
				t.Fatal(err)
			}
			if got := projection(); got != want {
				t.Fatalf("upgraded/rebuilt timing = %+v, want %+v", got, want)
			}
		})
	}
}

func TestWorkflowTimingDoesNotConstrainHistoricalRebuildDeletions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	const workID = "timing-legacy-rebuild"
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, mustCompileWorkflowComposition(implementationCompositionV33()))
	request := timingAction(t, s, workID, "record_proposal", "timing-legacy-action", stepFixtureActor(), timingProposal(), time.Unix(25, 10).UTC())
	if err := invokeTimingAction(ctx, s, BuiltinWorkflowRegistry(), request); err != nil {
		t.Fatal(err)
	}
	want := timingSnapshot(t, s, workID)
	if err := s.Transact(ctx, func(transaction *Transaction) (err error) {
		tx := transaction.tx
		scope, err := beginFold(ctx, tx)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, scope.close(ctx)) }()
		// Isolate the older deletion algorithm's risk from this projection:
		// its closed clear list cannot name the newly introduced timing table.
		for _, table := range replayProjectionClearTables {
			if table == "workflow_timing" {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
		var source string
		if err := tx.QueryRowContext(ctx, `SELECT define_event_id FROM workflow_timing WHERE work_id=?`, workID).Scan(&source); err != nil {
			return err
		}
		if source != want.DefineEventID {
			return errors.New("historical rebuild deletion lost event provenance")
		}
		return nil
	}); err != nil {
		t.Fatalf("timing constrained an older rebuild's deletions: %v", err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := timingSnapshot(t, s, workID); got != want {
		t.Fatalf("timing after legacy deletions and rebuild = %+v, want %+v", got, want)
	}
}

// These fixtures exercise the projection fold, not delivery admission. They
// append no worker report or verification authority.
func TestWorkflowTimingShipClassificationAndFirstSubmission(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "timing-ship"
	seedStepWork(t, s, workID)
	definition := mustCompileWorkflowComposition(implementationCompositionV33())
	definition.StepGraph.StartStep = "execution"
	for i := range definition.StepGraph.Steps {
		if definition.StepGraph.Steps[i].ID == "release" {
			definition.StepGraph.Steps[i].Actions = append([]string{"record_delivery"}, definition.StepGraph.Steps[i].Actions...)
		}
	}
	definition.StepGraph.Steps = slices.DeleteFunc(definition.StepGraph.Steps, func(step WorkflowStep) bool {
		return step.ID != "execution" && step.ID != "refine" && step.ID != "release"
	})
	definition.StepGraph.Edges = []WorkflowEdge{{From: "execution", To: "refine", Kind: WorkflowEdgeForward}, {From: "refine", To: "release", Kind: WorkflowEdgeForward}}
	definition.RecoveryRoutes = nil
	registry, entry := newResolutionRegistry(t, definition)
	ctx := context.Background()
	actor := stepFixtureActor()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: entry, Registry: registry, Actor: actor, Now: time.Unix(5, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	appendCompletion := func(id, step, state, artifact string, at time.Time) {
		t.Helper()
		version := readWorkVersion(t, s, workID)
		payload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": step, "action_id": "record_delivery", "attempt_epoch": 1,
			"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": mustActorRef(t, actor), "delivery_state": state, "delivery_artifact": artifact}
		event := workflowEventWithActor(id, WorkflowActionCompleted, workID, mustActorRef(t, actor), payload)
		event.OccurredAt = at
		if err := tryAppendWorkflowEventsWithRegistry(s, registry, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, version)}); err != nil {
			t.Fatal(err)
		}
	}
	appendCompletion("timing-execution-delivery", "execution", "asserted", "artifact:execution", time.Unix(9, 0).UTC())
	if got := timingSnapshot(t, s, workID); got.QueueEventID != "" {
		t.Fatalf("execution delivery entered the merge route: %+v", got)
	}
	appendCompletion("timing-refine-delivery", "refine", "asserted", "artifact:local", time.Unix(10, 0).UTC())
	if got := timingSnapshot(t, s, workID); got.QueueEventID != "" {
		t.Fatalf("refine delivery entered the merge route: %+v", got)
	}
	appendCompletion("timing-no-assertion", "release", "", "", time.Unix(11, 0).UTC())
	if got := timingSnapshot(t, s, workID); got.QueueEventID != "" {
		t.Fatalf("empty assertion entered the merge route: %+v", got)
	}
	appendCompletion("timing-no-artifact", "release", "asserted", "", time.Unix(11, 100).UTC())
	appendCompletion("timing-no-state", "release", "", "https://github.com/example/project/pull/1", time.Unix(11, 200).UTC())
	if got := timingSnapshot(t, s, workID); got.QueueEventID != "" {
		t.Fatalf("incomplete assertion entered the merge route: %+v", got)
	}
	appendCompletion("timing-first-submission", "release", "asserted", "https://github.com/example/project/pull/1", time.Unix(12, 987).UTC())
	want := timingSnapshot(t, s, workID)
	if want.QueueEventID != "" || want.QueueArtifact != "" || want.QueueEnteredAt != "" {
		t.Fatalf("delivery action was mistaken for native completion submission: %+v", want)
	}
	appendCompletion("timing-later-submission", "release", "asserted", "https://github.com/example/project/pull/2", time.Unix(13, 0).UTC())
	if got := timingSnapshot(t, s, workID); !reflect.DeepEqual(got, want) {
		t.Fatalf("later assertion replaced timing: %+v, want %+v", got, want)
	}
	for _, statement := range []string{
		`INSERT INTO workflow_timing(work_id) VALUES(?)`,
		`UPDATE workflow_timing SET queue_artifact='artifact:outside-fold' WHERE work_id=?`,
		`DELETE FROM workflow_timing WHERE work_id=?`,
	} {
		if _, err := s.db.Exec(statement, workID); err == nil || !strings.Contains(err.Error(), "fold-only") {
			t.Fatalf("unguarded timing mutation %s = %v", statement, err)
		}
	}
}

func TestWorkflowTimingUsesNewGenerations(t *testing.T) {
	for ref, versions := range map[string][]int64{
		"workflow.implementation": {38, 39}, "workflow.break_fix": {35}, "workflow.research": {23},
	} {
		for _, version := range versions {
			entry, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
			if !ok {
				t.Fatalf("timed generation %s v%d is missing", ref, version)
			}
			for _, action := range entry.Definition.ActionDefinitions {
				if action.ID != "complete" {
					continue
				}
				var pair int
				for _, field := range action.Payload.Fields {
					if field.Name == "delivery_artifact" || field.Name == "delivery_state" {
						if field.Required {
							t.Fatalf("%s v%d imposes submission on unsupported routes", ref, version)
						}
						pair++
					}
				}
				if pair != 2 {
					t.Fatalf("%s v%d completion has no optional submission pair", ref, version)
				}
			}
		}
	}
}

func timingRegisteredRepository(t *testing.T, s *Store, remote string) {
	t.Helper()
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM projects WHERE id='project'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(t.Context(), "project", ProjectLocator{ID: "timing-remote", Kind: LocatorGitRemote, Value: remote}, version); err != nil {
		t.Fatal(err)
	}
}

// These cases isolate classification; the native journey below exercises the
// verified pin, completion transaction and replay rather than a synthetic gate.
func TestWorkflowTimingSubmissionClassification(t *testing.T) {
	for _, test := range []struct {
		name, remote, artifact, state, kind, reason string
		sized                                       bool
		composition                                 WorkflowComposition
	}{
		{"unsized", "https://github.com/example/project.git", "", "", "not_applicable", "unsized", false, implementationTimedV39()},
		{"unregistered", "", "", "", "unavailable", "unregistered_merge_route", true, implementationTimedV39()},
		{"other forge", "https://elsewhere.invalid/example/project", "", "", "unavailable", "unregistered_merge_route", true, breakFixTimedV35()},
		{"research", "https://github.com/example/project", "", "", "not_applicable", "research_outcome", true, researchTimedV23()},
		{"legacy pin", "https://github.com/example/project", "", "", "unavailable", "legacy_definition", true, implementationObligationV37()},
		{"full implementation", "git@github.com:Example/Project.git", "https://github.com/Example/Project/pull/7", "asserted", "merge_route", "", true, implementationTimedV39()},
		{"break fix", "https://github.com/example/project", "https://github.com/example/project/pull/8", "asserted", "merge_route", "", true, breakFixTimedV35()},
	} {
		t.Run(test.name, func(t *testing.T) {
			const workID = "timing-classification"
			s, _ := footprintBindingItemWithChecks(t, workID, test.sized, nil)
			if test.remote != "" {
				timingRegisteredRepository(t, s, test.remote)
			}
			got, err := deriveWorkflowCompletionTiming(t.Context(), s.db, workID, mustCompileWorkflowComposition(test.composition), test.artifact, test.state, 0)
			if err != nil || got.RootWorkID != workID || got.Submission.Kind != test.kind || got.Submission.Reason != test.reason {
				t.Fatalf("classification = %+v, %v; want %s:%s", got, err, test.kind, test.reason)
			}
		})
	}
}

func TestWorkflowTimingNativeSmallCompletionAndReplay(t *testing.T) {
	j := smallReviewDispositionJourney(t, "timing-native-small")
	if err := j.acceptEvidence(t, "small-review"); err != nil {
		t.Fatal(err)
	}
	smallProofRecordVerdict(t, j)
	smallProofRecordInvestigation(t, j)
	smallProofConfirm(t, j)
	timingRegisteredRepository(t, j.s, "https://github.com/example/project.git")
	before := readWorkVersion(t, j.s, j.workID)
	for _, fields := range []map[string]any{
		{"impact_verdict": "non-breaking"},
		{"impact_verdict": "non-breaking", "delivery_artifact": "https://github.com/another/project/pull/1", "delivery_state": "asserted"},
	} {
		request := WorkflowActionPreflightRequest{WorkID: j.workID, ExpectedVersion: before, ActionID: "complete", Actor: j.fixture.owner, Payload: mustJSONValue(fields)}
		if err := WorkflowActionPreflightWithRegistry(t.Context(), j.s, BuiltinWorkflowRegistry(), request); err == nil || !strings.Contains(err.Error(), "merge-route submission") {
			t.Fatalf("preflight admitted missing/foreign submission: %v", err)
		}
		if err := j.action(t, "complete", fields, j.fixture.owner); err == nil || !strings.Contains(err.Error(), "merge-route submission") {
			t.Fatalf("direct completion admitted missing/foreign submission: %v", err)
		}
		if got := readWorkVersion(t, j.s, j.workID); got != before {
			t.Fatalf("refused submission changed version %d to %d", before, got)
		}
	}
	artifact := "https://github.com/example/project/pull/9"
	if err := j.action(t, "complete", map[string]any{"impact_verdict": "non-breaking", "delivery_artifact": artifact, "delivery_state": "asserted"}, j.fixture.owner); err != nil {
		t.Fatalf("complete native small submission: %v", err)
	}
	var id, occurredAt, raw string
	var version int
	var seq int64
	if err := j.s.db.QueryRow(`SELECT event_id,occurred_at,payload,payload_version,seq FROM domain_events WHERE subject_id=? AND kind=?`, j.workID, WorkflowCompleted).Scan(&id, &occurredAt, &raw, &version, &seq); err != nil {
		t.Fatal(err)
	}
	var completed workflowCompletedPayload
	if err := json.Unmarshal([]byte(raw), &completed); err != nil {
		t.Fatal(err)
	}
	if version != 3 || completed.Timing == nil || completed.Timing.RootWorkID != j.workID || completed.Timing.DefineStartEventID == nil || completed.Timing.Submission.Artifact != artifact {
		t.Fatalf("native timing absent or unbound: v%d %s", version, raw)
	}
	want := timingSnapshot(t, j.s, j.workID)
	if want.QueueEventID != id || want.QueueEnteredAt != occurredAt || want.QueueArtifact != artifact || want.DefineEventID != *completed.Timing.DefineStartEventID {
		t.Fatalf("submission differs from native completion: %+v, %s", want, raw)
	}
	for name, corrupt := range map[string]func(*workflowCompletionTiming){
		"valid":      func(*workflowCompletionTiming) {},
		"root":       func(v *workflowCompletionTiming) { v.RootWorkID = "unrelated-work" },
		"start":      func(v *workflowCompletionTiming) { v.DefineStartEventID = &id },
		"repository": func(v *workflowCompletionTiming) { v.Submission.Artifact = "https://github.com/another/project/pull/9" },
		"typed state": func(v *workflowCompletionTiming) {
			v.Submission = workflowTimingSubmission{Kind: "not_applicable", Reason: "unsized"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			forged := *completed.Timing
			corrupt(&forged)
			tx, err := j.s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			at, err := time.Parse(time.RFC3339Nano, occurredAt)
			if err != nil {
				t.Fatal(err)
			}
			event := Event{SubjectID: j.workID, EventID: id, Seq: seq, PayloadVersion: version, OccurredAt: at}
			err = foldWorkflowCompletionTiming(t.Context(), tx, event, mustCompileWorkflowComposition(implementationTimedSmallV38()), &forged)
			if (name == "valid") != (err == nil) {
				t.Fatalf("timing fold metadata verdict for %s: %v", name, err)
			}
		})
	}
	if err := RebuildFromLog(t.Context(), j.s); err != nil {
		t.Fatal(err)
	}
	if got := timingSnapshot(t, j.s, j.workID); got != want {
		t.Fatalf("replay retimed submission: %+v, want %+v", got, want)
	}
}

func TestWorkflowTimingLegacyUpcastDoesNotInventMetadata(t *testing.T) {
	for _, payload := range []string{`{"impact_verdict":"non-breaking"}`, `{"timing":null}`, `{"timing":{}}`} {
		event := Event{PayloadVersion: 2, Payload: json.RawMessage(payload)}
		got, err := upcastWorkflowCompletedV2(event)
		if strings.Contains(payload, "timing") {
			if err == nil {
				t.Fatalf("v2 upcast admitted timing: %s", payload)
			}
		} else if err != nil || got.PayloadVersion != 3 || string(got.Payload) != payload {
			t.Fatalf("legacy upcast changed completion content: %+v, %v", got, err)
		}
	}
}

func TestWorkflowTimingKeepsBaselineActionContracts(t *testing.T) {
	for _, pair := range [][2]WorkflowComposition{
		{smallImplementation(implementationObligationV37()), implementationTimedSmallV38()},
		{implementationObligationV37(), implementationTimedV39()},
		{breakFixObligationV34(), breakFixTimedV35()},
		{researchObligationV22(), researchTimedV23()},
	} {
		before, after := mustCompileWorkflowComposition(pair[0]), mustCompileWorkflowComposition(pair[1])
		for _, action := range before.ActionDefinitions {
			if action.ID == "complete" {
				continue
			}
			index := slices.IndexFunc(after.ActionDefinitions, func(candidate WorkflowActionDefinition) bool { return candidate.ID == action.ID })
			if index < 0 || !reflect.DeepEqual(action, after.ActionDefinitions[index]) {
				t.Errorf("timed %s changes baseline action %s", before.Ref, action.ID)
			}
		}
	}
}
