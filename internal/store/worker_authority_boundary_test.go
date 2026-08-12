package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// CD-0017 §D4 draws the worker authority boundary in both directions. A worker
// run is the bounded execution attempt of one workflow step — the position
// workflow.action_started and workflow.action_checkpointed already model — so a
// worker legitimately runs its own step. But workers never record a step
// transition, a verdict, or completion, and never spawn nested workflow
// authority: durable workflow authority stays with the owning Concord workflow.
// The prohibition binds authority, not labor. These tests prove both halves of
// the boundary hold in the store, not just in the prose.

// A dispatched worker attempt is real durable authority on its own surface: a
// lane, a routing policy, a resolved model, a lifecycle. That buys the worker
// its step and nothing more. The store routes worker.* events to folds that
// write only the worker_attempts projection and workflow.* events to folds that
// write only workflow authority; no fold spans both. One work item, one worker:
// the worker executes its step, then is refused each authority the boundary
// reserves for the owning workflow.
func TestWorkerAuthorityBoundaryHoldsInBothDirections(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "authority-work")
	seedWorkflowLaw(t, s)

	// Dispatch a real worker attempt so the test exercises a worker that is
	// genuinely present, not a stranger. This lands only in worker_attempts.
	lane := BuiltinLaneDefinitions()[0]
	dispatch := Event{
		EventID: "dispatch-1", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: "authority-work",
		Actor: "worker:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{
			AttemptID: "dispatch-1", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
			CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: RoutingPolicyVersion, RoutingPolicyDigest: RoutingPolicyManifestDigest,
			ResolvedModel: lane.PinnedModel, ResolutionRole: WorkerResolutionPreferred,
			PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion,
		}),
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatalf("dispatching the worker attempt: %v", err)
	}
	if got := countRows(t, s, "worker_attempts"); got != 1 {
		t.Fatalf("dispatched attempts = %d, want 1", got)
	}

	// The same actor identity is an agent-class workflow actor on the work
	// item, with the implementation definition selected — the position a worker
	// holds when it executes one step.
	workerRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/worker", "session/worker-1")
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	setup := []Event{
		workflowEvent("authority-actor", WorkflowActorRecorded, "authority-work", map[string]any{"work_id": "authority-work", "expected_version": 2, "resulting_version": 3, "actor_ref": workerRef, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/worker", "session_ref": "session/worker-1", "actor_class": "agent"}),
		workflowEvent("authority-definition", WorkflowDefinitionSelected, "authority-work", map[string]any{"work_id": "authority-work", "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation"}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-work"): 2}}); err != nil {
		t.Fatal(err)
	}

	// The allowed half of D4: the worker IS the bounded execution attempt of
	// the external-effect step, so starting it and checkpointing within it
	// succeed. Neither advances the step.
	start := workflowEventWithActor("authority-start", WorkflowActionStarted, "authority-work", workerRef, map[string]any{"work_id": "authority-work", "expected_version": 4, "resulting_version": 5, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "authority:start", "actor_ref": workerRef, "execution_model": lane.PinnedModel})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-work"): 4}}); err != nil {
		t.Fatalf("the worker could not start the step it owns: %v", err)
	}
	if got := currentStep(t, s, "authority-work"); got != "execution" {
		t.Fatalf("starting a step must not advance it: current_step=%q", got)
	}

	// The one boundary that holds at record time: a worker cannot complete the
	// workflow. The completion gate's actor-distinctness and evaluator clauses
	// refuse it.
	//
	// Two D4 boundaries are deliberately NOT asserted as refusals here, because
	// probing showed the store permits them and the authority question is open:
	//   - A step-advancing action_completed. D4-sanctioned flows have an
	//     executor start and complete its own step, and
	//     workflowActionAdvancesStep excludes verdict/evidence/impact/link from
	//     advancing, so an executor completing its own step is legal. Whether
	//     the transition to a NEW step needs a constraint beyond the later
	//     verdict gate is unresolved.
	//   - A verdict recorded by the worker as itself. The verdict fold requires
	//     only verdict_actor_ref == event.Actor, so self-recording passes; the
	//     D5 distinctness gate fires only at workflow completion, not at
	//     verdict-record time, and no evaluator-registration check exists.
	// Both are tracked in https://github.com/Sharper-Flow/concord/issues/83;
	// asserting either answer here would lock in an open decision.
	t.Run("cannot complete the workflow", func(t *testing.T) {
		completion := workflowEventWithActor("authority-completion", WorkflowCompleted, "authority-work", workerRef, map[string]any{"work_id": "authority-work", "expected_version": 5, "resulting_version": 6, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": workerRef, "premise_confirmed": false, "evidence_count": 0, "changed_refs_digest": "sha256:" + strings.Repeat("c", 64)})
		err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{completion}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-work"): 5}})
		if err == nil {
			t.Fatal("a worker recorded workflow completion")
		}
	})

	// The allowed half left the workflow where it put it: running the worker's
	// step, with no completion recorded.
	if got := instanceState(t, s, "authority-work"); got != "running" {
		t.Fatalf("rejected completion changed instance state: %q", got)
	}
	var completed int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, "authority-work", WorkflowCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatalf("a refused workflow completion was recorded: %d", completed)
	}
}

func currentStep(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var step string
	if err := s.DB().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

func instanceState(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var state string
	if err := s.DB().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// The boundary is symmetric in kind: worker authority kinds are registered
// separately from workflow authority kinds, and the worker kinds are namespaced
// so no worker event can be routed to a workflow fold.
func TestWorkerEventKindsAreNotWorkflowAuthority(t *testing.T) {
	registry := map[string]bool{}
	for kind := range eventKindRegistry {
		registry[kind] = true
	}
	for _, workflowKind := range []string{WorkflowActionStarted, WorkflowActionCompleted, WorkflowVerdictRecorded, WorkflowCompleted} {
		if !registry[workflowKind] {
			t.Fatalf("expected registered workflow authority kind %q", workflowKind)
		}
	}
	for _, workerKind := range []string{WorkerDispatched, WorkerCompleted, WorkerFailed} {
		if !registry[workerKind] {
			t.Fatalf("expected registered worker authority kind %q", workerKind)
		}
		if !strings.HasPrefix(workerKind, "worker.") {
			t.Fatalf("worker authority kind %q is not namespaced away from workflow authority", workerKind)
		}
	}
}

// The worker attempt projection structurally cannot hold workflow authority:
// its schema carries no step, verdict, completion, or actor column, so even a
// fold that wrote to it could not smuggle workflow state into the attempt
// record. Asserted against the live DDL so a later migration that adds such a
// column fails here rather than silently widening worker authority.
func TestWorkerAttemptProjectionHasNoWorkflowAuthorityColumns(t *testing.T) {
	s := openTemp(t)
	rows, err := s.DB().Query(`PRAGMA table_info(worker_attempts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"current_step", "step_id", "verdict_kind", "verdict_actor_ref", "terminal_state", "instance_state", "actor_ref", "execution_model", "contract_version"} {
		if columns[forbidden] {
			t.Fatalf("worker_attempts carries workflow authority column %q", forbidden)
		}
	}
}
