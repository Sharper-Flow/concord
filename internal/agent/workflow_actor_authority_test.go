package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
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
	rows, err := s.DatabaseForTesting().Query(`SELECT kind FROM effect_receipts WHERE subject_id=? ORDER BY effect_seq`, workID)
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, store.WorkflowActionCompleted).Scan(&completed); err != nil {
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
			(SELECT last FROM store_sequences WHERE name='effect'),
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
		wantDigest         string
		defectIntake       bool
	}{
		{kind: "task", ref: "", wantRef: "workflow.implementation", wantDigest: liveHeadDigest(t, "workflow.implementation")},
		{kind: "bug", ref: "", wantRef: "workflow.break_fix", wantDigest: liveHeadDigest(t, "workflow.break_fix"), defectIntake: true},
		{kind: "research", ref: "", wantRef: "workflow.research", wantDigest: liveHeadDigest(t, "workflow.research")},
		{kind: "decision", ref: "workflow.break_fix", wantRef: "workflow.break_fix", wantDigest: liveHeadDigest(t, "workflow.break_fix")},
		{kind: "other", ref: "workflow.research", wantRef: "workflow.research", wantDigest: liveHeadDigest(t, "workflow.research")},
	} {
		happy := happy
		t.Run("capture-"+happy.kind+"-"+happy.ref, func(t *testing.T) {
			captured := capture(happy.kind, happy.ref, "happy-"+happy.kind+"-"+happy.ref, happy.defectIntake)
			if captured.Outcome != OutcomeOK || len(*captured.ChangedRefs) != 1 {
				t.Fatalf("capture kind=%s ref=%q response=%+v err=%v", happy.kind, happy.ref, captured, captured.Error)
			}
			workID := (*captured.ChangedRefs)[0].ID
			var pinnedRef string
			var pinnedDigest string
			if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pinnedRef, &pinnedDigest); err != nil {
				t.Fatal(err)
			}
			if pinnedRef != happy.wantRef || pinnedDigest != happy.wantDigest {
				t.Fatalf("capture kind=%s ref=%q pinned %s %s, want %s %s", happy.kind, happy.ref, pinnedRef, pinnedDigest, happy.wantRef, happy.wantDigest)
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

func workOneVersion(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
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

// liveHeadDigest reads the family's current authoring head from the
// registry, so the capture assertions follow the head the selector owns.
func liveHeadDigest(t *testing.T, ref string) string {
	t.Helper()
	live, err := store.LiveWorkflowDefinitionForRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	return live.Digest
}
