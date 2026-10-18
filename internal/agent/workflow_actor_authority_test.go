package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestCaptureWorkflowTypeInitializesAndDispatchesFirstAction(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	capture := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Workflow work","value_statement":"Ship the workflow","kind":"task","project_ids":["project-1"],"workflow_type_ref":"workflow.implementation","idempotency_key":"capture-workflow-31"}`)}
	captured, err := Dispatch(context.Background(), s, service, capture, env)
	if err != nil || captured.Outcome != OutcomeOK || len(*captured.ChangedRefs) != 1 {
		t.Fatalf("capture response=%+v err=%v", captured, err)
	}
	if (*captured.ChangedRefs)[0].Version != "4" {
		t.Fatalf("captured version=%s, want 4", (*captured.ChangedRefs)[0].Version)
	}
	workID := (*captured.ChangedRefs)[0].ID
	var definition string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if definition != "workflow.implementation" {
		t.Fatalf("definition=%q", definition)
	}
	var eventKinds []string
	rows, err := s.DatabaseForTesting().Query(`SELECT kind FROM domain_events WHERE subject_id=? ORDER BY seq`, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		eventKinds = append(eventKinds, kind)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"work.created", "work.memberships_replaced", store.WorkflowActorRecorded, store.WorkflowDefinitionSelected}
	if len(eventKinds) != len(want) {
		t.Fatalf("initialization events=%v, want %v", eventKinds, want)
	}
	for i := range want {
		if eventKinds[i] != want[i] {
			t.Fatalf("initialization event[%d]=%q, want %q", i, eventKinds[i], want[i])
		}
	}

	action := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"` + workID + `","expected_version":4,"action_id":"record_proposal","idempotency_key":"first-workflow-action-31"}`)}
	action.Input = json.RawMessage(`{"work_id":"` + workID + `","expected_version":4,"action_id":"record_proposal","fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"idempotency_key":"first-workflow-action-31"}`)
	acted, err := Dispatch(context.Background(), s, service, action, env)
	if err != nil || acted.Outcome != OutcomeOK {
		t.Fatalf("first action response=%+v err=%v", acted, err)
	}
	var completed, currentStep int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, store.WorkflowActionCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id=? AND current_step='alignment'`, workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	if completed != 1 || currentStep != 1 {
		t.Fatalf("first action completion=%d step=%d", completed, currentStep)
	}
}

// TestCaptureLiveAuthoringSelectorRefusesRetiredFamiliesAndUnnamedDecisionKinds
// is the CD-0218 D1 public refusal witness the decision record names: the
// retired families refuse capture and revise before any write, decision and
// other captures refuse until the capture names a retained family, the three
// kind defaults pin their family's current head, and an explicit retained
// family captures without touching the operator kind vocabulary.
func TestCaptureLiveAuthoringSelectorRefusesRetiredFamiliesAndUnnamedDecisionKinds(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	type captureState struct {
		works, instances, contracts, events, changes int
	}
	snapshot := func() captureState {
		t.Helper()
		var state captureState
		if err := s.DatabaseForTesting().QueryRow(`SELECT
			(SELECT count(*) FROM work_items),
			(SELECT count(*) FROM workflow_instances),
			(SELECT count(*) FROM workflow_contracts),
			(SELECT count(*) FROM domain_events),
			total_changes()`).Scan(&state.works, &state.instances, &state.contracts, &state.events, &state.changes); err != nil {
			t.Fatal(err)
		}
		return state
	}
	// Every dispatch re-reads the scope version first: each accepted capture
	// bumps it, so a stale envelope would refuse a later capture.
	capture := func(kind, ref, key string, defectIntake bool) Envelope {
		t.Helper()
		freshScope, _, scopeErr := s.ScopeVersion(context.Background(), "project-1")
		if scopeErr != nil {
			t.Fatal(scopeErr)
		}
		freshEnv := mutationEnvelope(grant, freshScope)
		raw := `{"title":"Live authoring probe","value_statement":"Probe the live authoring selector","kind":"` + kind + `","project_ids":["project-1"],`
		if ref != "" {
			raw += `"workflow_type_ref":"` + ref + `",`
		}
		if defectIntake {
			raw += `"defect_intake":{"failure_shape":"live-authoring-selector-probe","reproduction":"Capture with kind bug pins the break-fix head through the live selector.","searched":"Searched the fixture store for an earlier same-shape capture; none exists.","related_defect_ids":[]},`
		}
		raw += `"idempotency_key":"` + key + `"}`
		captured, dispatchErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(raw)}, freshEnv)
		if dispatchErr != nil {
			t.Fatal(dispatchErr)
		}
		return captured
	}
	baseline := snapshot()

	for _, ref := range []string{"workflow.architecture_spike", "workflow.ops_runbook", "workflow.static_analysis", "workflow.generic_one_off"} {
		refused := capture("task", ref, "retired-"+ref, false)
		// The boundary renders the authoring refusal as invariant_violation;
		// the message is the typed answer and must name the family.
		if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "invariant_violation" {
			t.Fatalf("retired capture %s = %+v, want the archived-authoring refusal", ref, refused.Error)
		}
		if !strings.Contains(refused.Error.Message, ref) {
			t.Fatalf("retired capture refusal %q must name the family", refused.Error.Message)
		}
		if after := snapshot(); after != baseline {
			t.Fatalf("retired capture %s wrote state: before=%+v after=%+v", ref, baseline, after)
		}
	}

	// Decision and other work carries no implicit family: the unnamed kind
	// refuses with the typed workflow_authoring answer and commits nothing.
	for _, kind := range []string{"decision", "other"} {
		refused := capture(kind, "", "unnamed-"+kind, false)
		// The boundary renders the unnamed-kind authoring refusal as
		// invalid_input; the message is the typed workflow_authoring answer.
		if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_input" {
			t.Fatalf("unnamed %s capture = %+v, want the explicit-family refusal", kind, refused.Error)
		}
		if !strings.Contains(refused.Error.Message, "explicit retained workflow family") {
			t.Fatalf("unnamed %s refusal %q must name the explicit-family rule", kind, refused.Error.Message)
		}
		if after := snapshot(); after != baseline {
			t.Fatalf("unnamed %s capture wrote state: before=%+v after=%+v", kind, baseline, after)
		}
	}

	// The kind defaults pin their family's current head; an explicit retained
	// family captures exactly what the caller named. The operator kind stays
	// its own vocabulary: a decision work item is still kind decision. The
	// bug capture carries its defect intake admission.
	capturedWorks := map[string]string{}
	for _, happy := range []struct {
		kind, ref, wantRef string
		wantVersion        int64
		defectIntake       bool
	}{
		{kind: "task", ref: "", wantRef: "workflow.implementation", wantVersion: 34},
		{kind: "bug", ref: "", wantRef: "workflow.break_fix", wantVersion: 31, defectIntake: true},
		{kind: "research", ref: "", wantRef: "workflow.research", wantVersion: 20},
		{kind: "decision", ref: "workflow.break_fix", wantRef: "workflow.break_fix", wantVersion: 31},
		{kind: "other", ref: "workflow.research", wantRef: "workflow.research", wantVersion: 20},
	} {
		happy := happy
		t.Run("capture-"+happy.kind+"-"+happy.ref, func(t *testing.T) {
			captured := capture(happy.kind, happy.ref, "happy-"+happy.kind+"-"+happy.ref, happy.defectIntake)
			if captured.Outcome != OutcomeOK || len(*captured.ChangedRefs) != 1 {
				t.Fatalf("capture kind=%s ref=%q response=%+v err=%v", happy.kind, happy.ref, captured, captured.Error)
			}
			workID := (*captured.ChangedRefs)[0].ID
			var pinnedRef string
			var pinnedVersion int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&pinnedRef, &pinnedVersion); err != nil {
				t.Fatal(err)
			}
			if pinnedRef != happy.wantRef || pinnedVersion != happy.wantVersion {
				t.Fatalf("capture kind=%s ref=%q pinned %s v%d, want %s v%d", happy.kind, happy.ref, pinnedRef, pinnedVersion, happy.wantRef, happy.wantVersion)
			}
			var workKind string
			if err := s.DatabaseForTesting().QueryRow(`SELECT kind FROM work_items WHERE id=?`, workID).Scan(&workKind); err != nil {
				t.Fatal(err)
			}
			if workKind != happy.kind {
				t.Fatalf("captured work kind=%q, want the operator kind %q unchanged", workKind, happy.kind)
			}
			capturedWorks[happy.kind+"|"+happy.ref] = workID
		})
	}

	// A revise that names a retired family refuses before any write, and the
	// same revise through the live selector re-pins the fresh instance. The
	// target is the decision work the happy captures created.
	target, ok := capturedWorks["decision|workflow.break_fix"]
	if !ok {
		t.Fatal("the explicit decision capture left no work id to revise")
	}
	var beforeVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, target).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	beforeEvents := snapshot().events
	workVersionOf := func(id string) int64 {
		t.Helper()
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, id).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	revise := func(ref, key string) Envelope {
		t.Helper()
		freshScope, _, scopeErr := s.ScopeVersion(context.Background(), "project-1")
		if scopeErr != nil {
			t.Fatal(scopeErr)
		}
		freshEnv := mutationEnvelope(grant, freshScope)
		raw := `{"work_id":"` + target + `","expected_version":` + strconv.FormatInt(beforeVersion, 10) + `,"title":"Live authoring probe","value_statement":"Probe the live authoring selector","kind":"decision","reason":"retarget the family"`
		if ref != "" {
			raw += `,"workflow_type_ref":"` + ref + `"`
		}
		raw += `,"idempotency_key":"` + key + `"}`
		response, dispatchErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: json.RawMessage(raw)}, freshEnv)
		if dispatchErr != nil {
			t.Fatal(dispatchErr)
		}
		return response
	}
	retired := revise("workflow.generic_one_off", "revise-retired-ref")
	if retired.Outcome != OutcomeError || retired.Error == nil || retired.Error.Kind != "invariant_violation" || !strings.Contains(retired.Error.Message, "workflow.generic_one_off") {
		t.Fatalf("retired revise = %+v, want the archived-authoring refusal naming the family", retired.Error)
	}
	if gotVersion := workVersionOf(target); gotVersion != beforeVersion {
		t.Fatalf("retired revise moved the work from %d to %d", beforeVersion, gotVersion)
	}
	if gotEvents := snapshot().events; gotEvents != beforeEvents {
		t.Fatalf("retired revise wrote %d events over baseline %d", gotEvents, beforeEvents)
	}
	repinned := revise("workflow.research", "revise-explicit-family")
	if repinned.Outcome != OutcomeOK {
		t.Fatalf("explicit retained revise = %+v err=%v", repinned, repinned.Error)
	}
	var pinnedRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, target).Scan(&pinnedRef); err != nil {
		t.Fatal(err)
	}
	if pinnedRef != "workflow.research" {
		t.Fatalf("repinned family = %s, want workflow.research", pinnedRef)
	}
}

func TestOperatorQuestionPremiseSummaryUsesSchemaRuneBound(t *testing.T) {
	t.Parallel()
	choice := map[string]any{"id": "confirm", "label": "Confirm", "description": "Continue", "action_id": "confirm_premise"}
	for _, testCase := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "ascii-256", value: strings.Repeat("a", 256), valid: true},
		{name: "ascii-257", value: strings.Repeat("a", 257), valid: false},
		{name: "multibyte-256", value: strings.Repeat("界", 256), valid: true},
		{name: "multibyte-257", value: strings.Repeat("界", 257), valid: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"action_id": "confirm_premise", "prompt": "Choose", "header": "Checkpoint", "choices": []any{choice},
				"allow_multiple": false, "allow_custom": false, "premise_summary": testCase.value,
				"contract_summary": "contract v1", "decision_context_digest": "sha256:" + strings.Repeat("a", 64),
			})
			if err != nil {
				t.Fatal(err)
			}
			err = ValidatePayloadSchema("operator_question", payload)
			if (err == nil) != testCase.valid {
				t.Fatalf("schema result err=%v valid=%v", err, testCase.valid)
			}
		})
	}
}

func TestWorkflowActionMalformedBoundaryPrecedesPinAndAuthorityChecks(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	malformed := []string{
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-1","unknown":true}`,
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","action_id":"record_design","idempotency_key":"bad-2"}`,
		`{"work_id":7,"expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-3"}`,
		`{"work_id":"work-1","expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-4"} trailing`,
		`{"work_id":"","expected_version":1,"action_id":"record_proposal","idempotency_key":"bad-5"}`,
	}
	for _, raw := range malformed {
		response, dispatchErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(raw)}, env)
		if dispatchErr != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
			t.Fatalf("malformed workflow request response=%+v err=%v", response, dispatchErr)
		}
	}
}

func TestConfirmPremiseInvokeDerivesOperatorFromSignedApproval(t *testing.T) {
	t.Parallel()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	evaluatorEnv := mutationEnvelope(issue31EvaluatorGrant(t, service, privateKey), scopeVersion)
	// The canonical head removed the discovery marker: alignment feeds design
	// directly, so the pre-contract walk is proposal, alignment, design.
	for _, actionID := range []string{"record_proposal", "record_alignment", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, actionID, workflowIssue31Version(t, s), "issue31-"+actionID)
	}
	approve := workflowContractActionInput(t, "issue31-approve", "")
	challenge := invokeWorkflowIssue31(t, s, service, env, approve)
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approval challenge=%+v", challenge)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, approve)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 10}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "issue31-approve-nonce")
	approved := workflowContractActionInput(t, "issue31-approve", challengeRef)
	response := invokeWorkflowIssue31(t, s, service, env, approved)
	if response.Outcome != OutcomeOK {
		t.Fatalf("approved contract=%+v", response)
	}
	invokeWorkflowIssue31Action(t, s, service, env, "start_execution", workflowIssue31Version(t, s), "issue31-start")
	// The premise gate refuses while contract-required evidence is unbound.
	invokeWorkflowIssue31Action(t, s, service, env, "bind_evidence", workflowIssue31Version(t, s), "issue31-bind-verification")
	// The identity boundary is the subject of this test; place the fixture at
	// its accepted checkpoint without bypassing the fold-only trigger policy.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1'; DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	invokeWorkflowIssue31Action(t, s, service, evaluatorEnv, "record_verdict", workflowIssue31Version(t, s), "issue31-verdict")

	confirm := issue31ConfirmInput(t, s, workflowIssue31Version(t, s), "issue31-confirm")
	challenge = invokeWorkflowIssue31(t, s, service, env, confirm)
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		if challenge.Error != nil {
			t.Fatalf("premise challenge error=%+v response=%+v", *challenge.Error, challenge)
		}
		t.Fatalf("premise challenge=%+v", challenge)
	}
	challengeRef, _ = challenge.Error.Details["approval_ref"].(string)
	digest = mutationDigest("concord_work_transition", "workflow_action", env, confirm)
	versions = map[string]any{"work": workflowIssue31Version(t, s), "contract": 1}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "issue31-confirm-nonce")
	approved = json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(workflowIssue31Version(t, s), 10) + `,"action_id":"confirm_premise","selected_choice":"confirm","decision_context_digest":"` + extractDecisionDigest(t, confirm) + `","idempotency_key":"issue31-confirm","approval":{"approval_ref":"` + challengeRef + `"}}`)
	beforeEvents := countWorkflowEvents(t, s)
	response = invokeWorkflowIssue31(t, s, service, env, approved)
	if response.Outcome != OutcomeOK {
		t.Fatalf("confirmed premise=%+v error=%+v", response, response.Error)
	}
	var actorClass, confirmedBy, eventActor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT a.actor_class,pc.confirmed_by,e.actor FROM workflow_premise_confirmations pc JOIN workflow_actors a ON a.actor_ref=pc.confirmed_by JOIN domain_events e ON e.subject_id=pc.work_id AND e.kind=?`, store.WorkflowPremiseConfirmed).Scan(&actorClass, &confirmedBy, &eventActor); err != nil {
		t.Fatal(err)
	}
	if actorClass != string(store.ActorOperator) || confirmedBy != eventActor {
		t.Fatalf("operator confirmation actor_class=%q confirming=%q event_actor=%q", actorClass, confirmedBy, eventActor)
	}
	var actorRows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_class=?`, store.ActorOperator).Scan(&actorRows); err != nil {
		t.Fatal(err)
	}
	if actorRows != 1 || countWorkflowEvents(t, s) <= beforeEvents {
		t.Fatalf("operator actor/event persistence rows=%d events_before=%d events_after=%d", actorRows, beforeEvents, countWorkflowEvents(t, s))
	}

	replay := invokeWorkflowIssue31(t, s, service, env, approved)
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("stable replay=%+v error=%+v", replay, replay.Error)
	}
	var replayActors, replayPremises int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_class=?`, store.ActorOperator).Scan(&replayActors); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations`).Scan(&replayPremises); err != nil {
		t.Fatal(err)
	}
	if replayActors != 1 || replayPremises != 1 || countWorkflowEvents(t, s) <= beforeEvents {
		t.Fatalf("replay changed operator state actors=%d premises=%d events=%d", replayActors, replayPremises, countWorkflowEvents(t, s))
	}
}

// Issue #808: the production adapter sends an empty envelope principal by
// design — no host assertion can name a human (CD-0013 D5) — while every
// fixture envelope carried the grant principal. The relabel guard read the
// principal from the raw invocation, so production confirm_premise failed
// where every test passed.
func TestConfirmPremiseInvokeToleratesEmptyHostPrincipal(t *testing.T) {
	t.Parallel()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	evaluatorEnv := mutationEnvelope(issue31EvaluatorGrant(t, service, privateKey), scopeVersion)
	for _, actionID := range []string{"record_proposal", "record_alignment", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, actionID, workflowIssue31Version(t, s), "empty-principal-"+actionID)
	}
	approve := workflowContractActionInput(t, "empty-principal-approve", "")
	challenge := invokeWorkflowIssue31(t, s, service, env, approve)
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approval challenge=%+v", challenge)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, approve)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 10}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "empty-principal-approve-nonce")
	approved := workflowContractActionInput(t, "empty-principal-approve", challengeRef)
	if response := invokeWorkflowIssue31(t, s, service, env, approved); response.Outcome != OutcomeOK {
		t.Fatalf("approved contract=%+v", response)
	}
	invokeWorkflowIssue31Action(t, s, service, env, "start_execution", workflowIssue31Version(t, s), "empty-principal-start")
	invokeWorkflowIssue31Action(t, s, service, env, "bind_evidence", workflowIssue31Version(t, s), "empty-principal-bind-verification")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1'; DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	invokeWorkflowIssue31Action(t, s, service, evaluatorEnv, "record_verdict", workflowIssue31Version(t, s), "empty-principal-verdict")

	confirm := issue31ConfirmInput(t, s, workflowIssue31Version(t, s), "empty-principal-confirm")
	challenge = invokeWorkflowIssue31(t, s, service, env, confirm)
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("premise challenge=%+v", challenge)
	}
	challengeRef, _ = challenge.Error.Details["approval_ref"].(string)
	digest = mutationDigest("concord_work_transition", "workflow_action", env, confirm)
	versions = map[string]any{"work": workflowIssue31Version(t, s), "contract": 1}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "empty-principal-confirm-nonce")
	// The production shape: the confirming envelope carries no host-named
	// principal. The registry, not the host, owns that field.
	env.PrincipalRef = ""
	confirmApproved := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(workflowIssue31Version(t, s), 10) + `,"action_id":"confirm_premise","selected_choice":"confirm","decision_context_digest":"` + extractDecisionDigest(t, confirm) + `","idempotency_key":"empty-principal-confirm","approval":{"approval_ref":"` + challengeRef + `"}}`)
	response := invokeWorkflowIssue31(t, s, service, env, confirmApproved)
	if response.Outcome != OutcomeOK {
		t.Fatalf("confirmed premise with empty host principal=%+v error=%+v", response, response.Error)
	}
	var actorClass string
	if err := s.DatabaseForTesting().QueryRow(`SELECT a.actor_class FROM workflow_premise_confirmations pc JOIN workflow_actors a ON a.actor_ref=pc.confirmed_by`).Scan(&actorClass); err != nil {
		t.Fatal(err)
	}
	if actorClass != string(store.ActorOperator) {
		t.Fatalf("confirmation actor_class=%q, want operator", actorClass)
	}
}

func invokeWorkflowIssue31Action(t *testing.T, s *store.Store, service *Service, env CallEnvelope, actionID string, version int64, key string) {
	t.Helper()
	fields := ""
	if actionID == "record_alignment" {
		fields = `,"fields":{"searched":"The bounded backlog search statement.","outcome":"none_found"}`
	}
	if actionID == "record_design" {
		fields = `,"fields":{"approach":"The recorded approach is the implementation boundary.","decisions":[{"id":"decision:issue31","question":"What crosses into execution?","choice":"The typed design record.","rationale":"The worker must receive the approved decision.","rejected":[]}],"touched_refs":["path:issue31"]}`
	}
	if actionID == "record_proposal" {
		fields = `,"fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`
	}
	if actionID == "record_verdict" {
		// The canonical head declares the verdicts batch alone: the batch
		// entry keeps the entry semantics the single form bound (one
		// predicate, explicit ok kind, minted evidence).
		fields = `,"fields":{"verdicts":[{"predicate_id":"predicate:primary","verdict_kind":"ok"}]}`
	}
	input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"` + actionID + `"` + fields + `,"idempotency_key":"` + key + `"}`)
	response := invokeWorkflowIssue31(t, s, service, env, input)
	if response.Outcome != OutcomeOK {
		if response.Error != nil {
			t.Fatalf("action=%s error=%+v response=%+v", actionID, *response.Error, response)
		}
		t.Fatalf("action=%s response=%+v", actionID, response)
	}
}

func invokeWorkflowIssue31(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input json.RawMessage) Envelope {
	t.Helper()
	request, err := json.Marshal(struct {
		CallEnvelope CallEnvelope    `json:"call_envelope"`
		Tool         string          `json:"tool"`
		Operation    string          `json:"operation"`
		Input        json.RawMessage `json:"input"`
	}{CallEnvelope: env, Tool: "concord_work_transition", Operation: "workflow_action", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Invoke(context.Background(), s, service, request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func workflowIssue31Version(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// ensureIssue31InvestigationArtifact seeds the investigation observation the
// operator question gate requires: a current Domain of the fixture Product and
// a second work item to compare against.
func ensureIssue31InvestigationArtifact(t *testing.T, s *store.Store) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	// The fixture store already projects a Domain registry for product-1 whose
	// root Domain id is "root"; the observation names that Domain and a second
	// work item so both refs resolve.
	statements := []string{
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) SELECT 'work-issue31-compared','task','Investigation comparison','needed',0,1,'2026-09-09T00:00:00Z','2026-09-09T00:00:00Z' WHERE NOT EXISTS(SELECT 1 FROM work_items WHERE id='work-issue31-compared')`,
		`INSERT INTO work_projects(work_id,project_id,role) SELECT 'work-issue31-compared','project-1','secondary' WHERE NOT EXISTS(SELECT 1 FROM work_projects WHERE work_id='work-issue31-compared')`,
		`INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) SELECT 'obs:issue31investig0','work-1','issue31 investigation','["root","work-issue31-compared"]','[]','2026-09-09T00:00:00Z' WHERE NOT EXISTS(SELECT 1 FROM work_observations WHERE work_id='work-1')`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(context.Background(), statement); err != nil {
			_, _ = tx.ExecContext(context.Background(), `DELETE FROM fold_guard WHERE active=1`)
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM fold_guard WHERE active=1`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func issue31ConfirmInput(t *testing.T, s *store.Store, version int64, key string) json.RawMessage {
	t.Helper()
	ensureIssue31InvestigationArtifact(t, s)
	question, err := store.ReadWorkflowOperatorQuestion(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if question == nil {
		t.Fatal("missing operator question")
	}
	if question.ActionID != "confirm_premise" || question.Header != "Operator checkpoint" || question.AllowMultiple || question.AllowCustom || len(question.Choices) != 3 {
		t.Fatalf("operator question shape=%+v", question)
	}
	if question.Choices[0].ID != "confirm" || question.Choices[0].ActionID != "confirm_premise" || question.Choices[1].ActionID != "supersede_contract" || question.Choices[2].ActionID != "concord_work_transition.lifecycle" {
		t.Fatalf("operator question choices=%+v", question.Choices)
	}
	return json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"confirm_premise","selected_choice":"confirm","decision_context_digest":"` + question.DecisionContextDigest + `","idempotency_key":"` + key + `"}`)
}

func extractDecisionDigest(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var input struct {
		DecisionContextDigest string `json:"decision_context_digest"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	return input.DecisionContextDigest
}

func TestConfirmPremiseInvokeRejectsMissingWrongAndPayloadActorsWithoutState(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
	}{
		{name: "missing assertion"},
		{name: "wrong version assertion"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s, service, grant, privateKey, env, confirm, challengeRef, digest, scope, versions := prepareIssue31Confirm(t)
			beforeEvents := countWorkflowEvents(t, s)
			beforeVersion := workflowIssue31Version(t, s)
			beforeActors := countWorkflowActorRowsIssue31(t, s)
			if testCase.name == "missing assertion" {
				env.HostApproval = nil
			} else {
				versions["work"] = beforeVersion + 1
				env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "issue31-wrong-version")
			}
			approved := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(beforeVersion, 10) + `,"action_id":"confirm_premise","idempotency_key":"issue31-rejected-confirm","approval":{"approval_ref":"` + challengeRef + `"}}`)
			response := invokeWorkflowIssue31(t, s, service, env, approved)
			if response.Outcome != OutcomeError {
				t.Fatalf("rejected confirmation response=%+v", response)
			}
			if countWorkflowEvents(t, s) != beforeEvents || workflowIssue31Version(t, s) != beforeVersion || countWorkflowActorRowsIssue31(t, s) != beforeActors {
				t.Fatalf("rejected confirmation changed state events=%d/%d version=%d/%d actors=%d/%d", countWorkflowEvents(t, s), beforeEvents, workflowIssue31Version(t, s), beforeVersion, countWorkflowActorRowsIssue31(t, s), beforeActors)
			}
			_ = confirm
		})
	}
}

func TestConfirmPremiseInvokeRejectsPayloadActorBorrowing(t *testing.T) {
	t.Parallel()
	s, service, _, _, env, _, challengeRef, _, _, _ := prepareIssue31Confirm(t)
	beforeEvents := countWorkflowEvents(t, s)
	beforeVersion := workflowIssue31Version(t, s)
	beforeActors := countWorkflowActorRowsIssue31(t, s)
	input := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(beforeVersion, 10) + `,"action_id":"confirm_premise","fields":{"confirming_actor_ref":"actor:payload-selected"},"idempotency_key":"payload-actor-confirm","approval":{"approval_ref":"` + challengeRef + `"}}`)
	response := invokeWorkflowIssue31(t, s, service, env, input)
	if response.Outcome != OutcomeError {
		t.Fatalf("payload actor response=%+v", response)
	}
	if countWorkflowEvents(t, s) != beforeEvents || workflowIssue31Version(t, s) != beforeVersion || countWorkflowActorRowsIssue31(t, s) != beforeActors {
		t.Fatalf("payload actor changed state events=%d/%d version=%d/%d actors=%d/%d", countWorkflowEvents(t, s), beforeEvents, workflowIssue31Version(t, s), beforeVersion, countWorkflowActorRowsIssue31(t, s), beforeActors)
	}
}

func TestConfirmPremiseQuestionFailuresAreAuthorityNoOps(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing-digest", mutate: func(input map[string]any) { delete(input, "decision_context_digest") }},
		{name: "malformed-digest", mutate: func(input map[string]any) { input["decision_context_digest"] = "not-a-digest" }},
		{name: "stale-digest", mutate: func(input map[string]any) { input["decision_context_digest"] = "sha256:" + strings.Repeat("f", 64) }},
		{name: "revise-routed-away", mutate: func(input map[string]any) { input["selected_choice"] = "revise" }},
		{name: "stop-routed-away", mutate: func(input map[string]any) { input["selected_choice"] = "stop" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s, service, _, _, env, confirm, _, _, _, _ := prepareIssue31Confirm(t)
			before := workflowIssue31AuthoritySnapshot(t, s)
			var input map[string]any
			if err := json.Unmarshal(confirm, &input); err != nil {
				t.Fatal(err)
			}
			input["idempotency_key"] = "negative-" + testCase.name
			testCase.mutate(input)
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			response := invokeWorkflowIssue31(t, s, service, env, raw)
			if response.Outcome == OutcomeOK {
				t.Fatalf("negative selection unexpectedly succeeded: %+v", response)
			}
			after := workflowIssue31AuthoritySnapshot(t, s)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("negative selection changed authority before=%+v after=%+v", before, after)
			}
		})
	}
}

type workflowIssue31AuthorityState struct {
	Version        int64
	Events         int
	Actors         int
	Challenges     int
	Approvals      int
	Projections    map[string]int
	InstanceState  string
	CurrentStep    string
	ChallengeState string
	ApprovalState  string
}

func workflowIssue31AuthoritySnapshot(t *testing.T, s *store.Store) workflowIssue31AuthorityState {
	t.Helper()
	state := workflowIssue31AuthorityState{Projections: map[string]int{}}
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&state.Version); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='work-1'`).Scan(&state.Events); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&state.Actors); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_approval_challenges`).Scan(&state.Challenges); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_approvals`).Scan(&state.Approvals); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state,current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&state.InstanceState, &state.CurrentStep); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"workflow_instances", "workflow_contracts", "workflow_candidate_sets", "workflow_actors", "workflow_checkpoints", "workflow_external_conditions", "workflow_impact_edges", "workflow_impact_notices", "workflow_decision_records", "workflow_premise_confirmations"} {
		var count int
		if err := s.DatabaseForTesting().QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		state.Projections[table] = count
	}
	state.ChallengeState = workflowIssue31Rows(t, s, `SELECT challenge_ref,status,used_count FROM agent_approval_challenges ORDER BY challenge_ref`)
	state.ApprovalState = workflowIssue31Rows(t, s, `SELECT approval_ref,used_count FROM agent_approvals ORDER BY approval_ref`)
	return state
}

func workflowIssue31Rows(t *testing.T, s *store.Store, query string) string {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out strings.Builder
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			out.WriteString(fmt.Sprint(value))
			out.WriteByte('|')
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func countWorkflowActorRowsIssue31(t *testing.T, s *store.Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func prepareIssue31Confirm(t *testing.T) (*store.Store, *Service, Authority, ed25519.PrivateKey, CallEnvelope, json.RawMessage, string, string, map[string]any, map[string]any) {
	t.Helper()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for _, actionID := range []string{"record_proposal", "record_alignment", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, actionID, workflowIssue31Version(t, s), "prepare-"+actionID)
	}
	approve := workflowContractActionInput(t, "prepare-approve", "")
	challenge := invokeWorkflowIssue31(t, s, service, env, approve)
	if challenge.Error == nil {
		t.Fatalf("missing contract approval challenge: %+v", challenge)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	approveDigest := mutationDigest("concord_work_transition", "workflow_action", env, approve)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, approveDigest, scope, map[string]any{"work": 10}, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "prepare-approve-nonce")
	approved := workflowContractActionInput(t, "prepare-approve", challengeRef)
	if response := invokeWorkflowIssue31(t, s, service, env, approved); response.Outcome != OutcomeOK {
		t.Fatalf("contract approval response=%+v", response)
	}
	invokeWorkflowIssue31Action(t, s, service, env, "start_execution", workflowIssue31Version(t, s), "prepare-start")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1'; DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	evaluatorEnv := mutationEnvelope(issue31EvaluatorGrant(t, service, privateKey), scopeVersion)
	invokeWorkflowIssue31Action(t, s, service, evaluatorEnv, "record_verdict", workflowIssue31Version(t, s), "prepare-verdict")
	confirm := issue31ConfirmInput(t, s, workflowIssue31Version(t, s), "prepare-confirm")
	challenge = invokeWorkflowIssue31(t, s, service, env, confirm)
	if challenge.Error == nil {
		t.Fatalf("missing premise challenge: %+v", challenge)
	}
	challengeRef, _ = challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, confirm)
	versions := map[string]any{"work": workflowIssue31Version(t, s), "contract": 1}
	return s, service, grant, privateKey, env, confirm, challengeRef, digest, scope, versions
}

func issue31EvaluatorGrant(t *testing.T, service *Service, privateKey ed25519.PrivateKey) Authority {
	t.Helper()
	_ = privateKey
	grant, err := service.Authorize(context.Background(), Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-evaluator", AgentRef: "agent-evaluator", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: "work_transition", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}
