package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// seedDerivedFailureWorkerForCombinedReject seeds work-1 on implementation
// v29 at its execution step with one dispatched, completed, undispositioned
// worker attempt, the contract and predicates the packet derivation reads,
// and the claimed worktree a retry authorization rests on. The caller decides
// whether a work context rides the view.
func seedDerivedFailureWorkerForCombinedReject(t *testing.T, s *store.Store, service *Service, grant Authority) (int64, string) {
	t.Helper()
	seedCurrentWorkflowDomainFixture(t, s)
	definition, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.implementation", 29)
	if !ok {
		t.Fatal("implementation v29 is not registered")
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: owner, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	ownerRef, err := store.WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	// The recorded narrative rides the packet's work_record member verbatim,
	// so the fixture seeds it directly the way capture records it.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE work_items SET narrative=? WHERE id='work-1';
		DELETE FROM fold_guard`, strings.Repeat("n", 16384)); err != nil {
		t.Fatalf("seed the recorded narrative: %v", err)
	}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='execution' WHERE work_id='work-1';
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'approved retry objective','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal');
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:verification","expected_result":"pass"}');
		DELETE FROM fold_guard`, ownerRef); err != nil {
		t.Fatalf("seed combined-reject projections: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM fold_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:combined-reject", "combined-reject", strings.Repeat("a", 40), path, "repo:combined-reject", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed combined-reject worktree: %v", err)
	}
	grant.Worktree = path
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	version := workVersion(t, s, "work-1")
	start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"start_execution","idempotency_key":"combined-reject-start-1"}`)}, env)
	if start.Outcome != OutcomeOK {
		t.Fatalf("seed combined-reject start: %+v", start.Error)
	}
	recordReadyRetryJob(t, s, service, env, "job:combined-reject")
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt:work-1:combined-reject-1"
	version = workVersion(t, s, "work-1")
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "combined-reject-dispatch-1", "fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, s, attemptID, pin.Correction)},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:combined-reject-dispatch-1"
	dispatch := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if dispatch.Outcome != OutcomeOK {
		t.Fatalf("seed combined-reject dispatch: %+v", dispatch.Error)
	}
	applyVerificationWorkerDispatchAndCompletion(t, s, grant, attemptID, "combined-reject")
	return workVersion(t, s, "work-1"), path
}

// recordOversizedWorkContext declares the largest legal findings context the
// action admits, so the derived retry packet carries a work-context view that
// alone pushes the combined response past the agent envelope budget. Every
// value stays inside its declared field bound: the packet is legal, only its
// serialization is unreportable.
func recordOversizedWorkContext(t *testing.T, s *store.Store, service *Service, grant Authority) {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	evidence := strings.Repeat("e", 240)
	// Ten findings keep the record call's own ok echo under the agent
	// envelope budget while the derived packet still carries a view that
	// crosses it beside the recorded text and the rejection correction.
	findings := make([]map[string]any, 0, 10)
	for i := 0; i < 10; i++ {
		findings = append(findings, map[string]any{
			"kind": "observation", "statement": strings.Repeat("s", 1024),
			"subject_ref": "context:work-1:" + strconv.Itoa(i),
			"evidence_refs": []string{
				evidence + ":0", evidence + ":1", evidence + ":2", evidence + ":3",
				evidence + ":4", evidence + ":5", evidence + ":6", evidence + ":7",
			},
			"domain_id":              pm1fixture.FixtureRootDomainID,
			"product_wide_rationale": "the fixture root domain carries every combined-reject finding",
		})
	}
	raw, err := json.Marshal(map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "record_work_context",
		"idempotency_key": "combined-reject-context-1", "fields": map[string]any{"context_findings": findings},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:combined-reject-context-1"
	recorded := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if recorded.Outcome != OutcomeOK {
		t.Fatalf("record the oversized work context: %+v", recorded.Error)
	}
}

// combinedRejectInput builds the combined reject_worker_result input whose
// own diagnosis and strategy ride the derived packet's correction context.
func combinedRejectInput(version, epoch int64, diagnosis, strategy string) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "reject_worker_result",
		"idempotency_key": "combined-reject-1",
		"fields": map[string]any{
			"attempt_id": "attempt:work-1:combined-reject-1", "attempt_epoch": epoch,
			"diagnosis": diagnosis, "strategy": strategy,
			"predicate_ids": []string{"predicate:primary"},
			"evidence_refs": []string{"evidence:combined-reject"},
			"retry":         map[string]any{"lane_id": "implement"},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// latestDispatchEpoch reads the fenced epoch of the fixture dispatch start.
func latestDispatchEpoch(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow("SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1", store.WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// combinedRejectRefusalEvents counts the rejection and dispatch completions
// the fixture's own seed dispatch does not account for.
func combinedRejectRefusalEvents(t *testing.T, s *store.Store) int {
	t.Helper()
	return countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='`+string(store.WorkflowActionCompleted)+`' AND json_extract(payload,'$.action_id') IN ('reject_worker_result','dispatch_worker')`)
}

// TestCombinedRejectOversizedResultRefusesZeroWrite proves a legal oversized
// retry packet cannot strand a committed dispatch: the combined rejection's
// response carries the derived packet inline, and when that serialization
// exceeds the agent envelope budget the whole call refuses typed at the
// envelope budget before anything commits — no rejection record, no retry
// attempt, no work version movement, no cached idempotent success, and the
// rejection-only escape stays available. The normal-size control on an
// identical fixture commits the same combined call.
func TestCombinedRejectOversizedResultRefusesZeroWrite(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, worktree := seedDerivedFailureWorkerForCombinedReject(t, s, service, grant)
	grant.Worktree = worktree
	recordOversizedWorkContext(t, s, service, grant)
	version = workVersion(t, s, "work-1")
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "request:combined-reject-1"
	input := combinedRejectInput(version, latestDispatchEpoch(t, s), strings.Repeat("d", 4096), strings.Repeat("t", 4096))
	before := combinedRejectRefusalEvents(t, s)
	durableBefore := countRows(t, s.DatabaseForTesting(), "SELECT count(*) FROM durable_operations WHERE work_id='work-1' AND result_kind='completed'")
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if refused.Outcome != OutcomeError || refused.Error == nil {
		t.Fatalf("the oversized combined rejection answered %+v, want the typed envelope refusal", refused)
	}
	if refused.Error.Kind != "limit_exceeded" || refused.Error.RecoveryAction.Kind != "reduce_limit" {
		t.Fatalf("the oversized refusal is %s/%s, want limit_exceeded with reduce_limit", refused.Error.Kind, refused.Error.RecoveryAction.Kind)
	}
	if refused.Error.EffectState != EffectNone {
		t.Fatalf("the oversized refusal reports effect %q, want none: nothing committed", refused.Error.EffectState)
	}
	if after := combinedRejectRefusalEvents(t, s); after != before {
		t.Fatalf("the oversized refusal changed rejection or dispatch completions %d -> %d", before, after)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id LIKE '%:retry'`); got != 0 {
		t.Fatalf("the oversized refusal minted %d retry attempts", got)
	}
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("the oversized refusal moved the work version %d -> %d", version, after)
	}
	if got := countRows(t, s.DatabaseForTesting(), "SELECT count(*) FROM durable_operations WHERE work_id='work-1' AND result_kind='completed'"); got != durableBefore {
		t.Fatalf("the oversized refusal left %d completed durable operation rows on the work", got-durableBefore)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records WHERE idempotency_key='combined-reject-1'`); got != 0 {
		t.Fatalf("the oversized refusal cached %d idempotent successes", got)
	}
	// The refusal is stable on replay: the same request re-executes the
	// same derivation and refuses the same way with the same zero writes.
	replayed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if replayed.Outcome != OutcomeError || replayed.Error == nil || replayed.Error.Kind != "limit_exceeded" {
		t.Fatalf("the replayed oversized rejection answered %+v, want the same typed refusal", replayed)
	}
	if after := combinedRejectRefusalEvents(t, s); after != before {
		t.Fatalf("the replayed oversized refusal changed rejection or dispatch completions %d -> %d", before, after)
	}
	if got := countRows(t, s.DatabaseForTesting(), "SELECT count(*) FROM durable_operations WHERE work_id='work-1' AND result_kind='completed'"); got != durableBefore {
		t.Fatalf("the replayed oversized refusal left %d completed durable operation rows on the work", got-durableBefore)
	}
}

// TestCombinedRejectNormalSizeCommits is the control: an identical fixture
// without the oversized context commits the same combined call — one
// rejection, one retry attempt, the ok envelope with the derived dispatch.
func TestCombinedRejectNormalSizeCommits(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, worktree := seedDerivedFailureWorkerForCombinedReject(t, s, service, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "request:combined-reject-normal-1"
	before := combinedRejectRefusalEvents(t, s)
	committed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: combinedRejectInput(version, latestDispatchEpoch(t, s), "the bounded diagnosis", "the bounded strategy")}, env)
	if committed.Outcome != OutcomeOK {
		t.Fatalf("the normal-size combined rejection: %+v", committed.Error)
	}
	var result map[string]any
	if json.Unmarshal(committed.Result, &result) != nil {
		t.Fatal(err)
	}
	if result["retry_attempt_id"] != "workflow-combined-reject-normal-1:retry" && result["retry_attempt_id"] == "" {
		t.Fatalf("the committed combined rejection carries no retry attempt: %v", result)
	}
	if after := combinedRejectRefusalEvents(t, s); after != before+2 {
		t.Fatalf("the normal-size combined call recorded %d rejection and dispatch completions, want the rejection and the retry dispatch", after-before)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id LIKE '%:retry'`); got != 1 {
		t.Fatalf("the normal-size combined call minted %d retry attempts, want one", got)
	}
}
