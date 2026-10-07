package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// The public defect journey: a first bug capture, the recurrence refusal that
// names the research route, a research capture carrying the same intake, a
// real workflow.research completion driven entirely through the public
// mutation boundary (signed contract approval, finding, report, an
// independent verdict, operator premise confirmation, completion), and the
// recurrent retry admitted behind that completed root cause. No workflow or
// lifecycle state is seeded or forged: every step is a public dispatch.
func TestPublicDefectJourneyThroughRealResearchCompletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"product_read", "work_define", "work_transition"})
	// The operator question gate reads a current Domain registry; the home is
	// a real committed knowledge repository, the way production derives it.
	worktree := recoveryDomainRepository(t, s)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.Worktree = worktree
	capture := func(requestID, input string) Envelope {
		t.Helper()
		env.RequestID = requestID
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(input)}, env)
		if dispatchErr != nil {
			t.Fatalf("capture dispatch %s: %v", requestID, dispatchErr)
		}
		return response
	}
	action := func(actorEnv CallEnvelope, workID, actionID string, fields map[string]any, key string) Envelope {
		t.Helper()
		version := workVersion(t, s, workID)
		input := map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "idempotency_key": key}
		if fields != nil {
			input["fields"] = fields
		}
		actorEnv.RequestID = "request:" + key + ":" + strconv.FormatInt(version, 10)
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, actorEnv)
		if dispatchErr != nil {
			t.Fatalf("action %s dispatch: %v", actionID, dispatchErr)
		}
		return response
	}
	// approvedAction dispatches one action input and, when the core minted
	// an approval challenge, resubmits it with the signed host approval the
	// challenge binds. The typed consequence summary is the only wire owner
	// of the challenge's scope and version bindings; the details carry the
	// reference metadata alone.
	approvedAction := func(actorEnv CallEnvelope, input map[string]any, key string) Envelope {
		t.Helper()
		raw := retryJSON(input)
		request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}
		actorEnv.RequestID = "request:" + key
		challenge := dispatchMutation(t, s, service, request, actorEnv)
		if challenge.Outcome == OutcomeOK {
			return challenge
		}
		if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
			t.Fatalf("%s without approval outcome=%s error=%+v, want approval_required or ok", input["action_id"], challenge.Outcome, challenge.Error)
		}
		var details struct {
			Ref      string   `json:"approval_ref"`
			Digest   string   `json:"operation_digest"`
			Scope    []string `json:"-"`
			Versions []string `json:"-"`
		}
		if err := json.Unmarshal(retryJSON(challenge.Error.Details), &details); err != nil {
			t.Fatal(err)
		}
		summary := challenge.Error.ConsequenceSummary
		if summary == nil || summary.Tool != request.Tool || summary.Operation != request.Operation || summary.OperationDigest != details.Digest {
			t.Fatalf("%s challenge lacks a typed consequence summary bound to this request: summary=%+v details=%+v", input["action_id"], summary, details)
		}
		details.Scope, details.Versions = summary.Scope, summary.Versions
		wantWork := "work:" + strconv.FormatInt(input["expected_version"].(int64), 10)
		if details.Digest != mutationDigest(request.Tool, request.Operation, actorEnv, raw) || !contains(details.Versions, wantWork) {
			t.Fatalf("%s challenge does not bind the exact intent: %+v", input["action_id"], details)
		}
		approved := cloneWithApproval(t, input, details.Ref)
		request.Input = retryJSON(approved)
		approvedEnv := actorEnv
		approvedEnv.HostApproval = &HostApprovalAssertion{ChallengeRef: details.Ref, RequestDigest: details.Digest, Scope: details.Scope, Versions: details.Versions, SessionRef: actorEnv.SessionRef, AgentRef: actorEnv.AgentRef, Worktree: actorEnv.Worktree, IssuedAt: fixedTime().Format(time.RFC3339Nano)}
		response, dispatchErr := Dispatch(ctx, s, service, request, approvedEnv)
		if dispatchErr != nil {
			t.Fatalf("approved %s dispatch: %v", input["action_id"], dispatchErr)
		}
		if response.Error != nil && response.Error.Kind == "approval_invalid" {
			t.Fatalf("%s approval refused on its bindings: error=%+v summary=%+v details=%+v", input["action_id"], response.Error, summary, details)
		}
		return response
	}

	// 1. The first bug of the shape is admitted with its intake.
	first := capture("defect-journey-first",
		`{"title":"Checksum mismatch","value_statement":"Uploads lose their checksum","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-journey-first","defect_intake":{"failure_shape":"journey-upload-checksum","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first bug capture=%+v", first.Error)
	}
	bugID := (*first.ChangedRefs)[0].ID

	// 2. The repeat refuses and names the research route.
	repeat := capture("defect-journey-repeat",
		`{"title":"Checksum mismatch again","value_statement":"Uploads still lose their checksum","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-journey-repeat","defect_intake":{"failure_shape":"journey-upload-checksum","reproduction":"upload the fixture and compare digests again","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`)
	if repeat.Outcome != OutcomeError || repeat.Error == nil {
		t.Fatalf("repeat capture outcome=%s, want typed refusal", repeat.Outcome)
	}
	for _, needle := range []string{"journey-upload-checksum", bugID, "research", "root_cause_work_id"} {
		if !strings.Contains(repeat.Error.Message, needle) {
			t.Fatalf("repeat refusal %q does not name %s", repeat.Error.Message, needle)
		}
	}

	// 3. The research capture carries the same intake as the cluster identity.
	research := capture("defect-journey-research",
		`{"title":"Why uploads lose checksums","value_statement":"The cluster needs a root cause","kind":"research","project_ids":["project-1"],"idempotency_key":"defect-journey-research","defect_intake":{"failure_shape":"journey-upload-checksum","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":["`+bugID+`"]}}`)
	if research.Outcome != OutcomeOK {
		t.Fatalf("research capture=%+v", research.Error)
	}
	rcaID := (*research.ChangedRefs)[0].ID

	// 4. The real workflow.research completion, every step a public dispatch.
	contractFields := map[string]any{
		"premise":            "The upload checksum cluster has one root cause.",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}},
		"spec_mandate":       []string{},
		"law_modifies":       []string{},
	}
	approved := approvedAction(env, map[string]any{"work_id": rcaID, "expected_version": workVersion(t, s, rcaID), "action_id": "approve_contract", "fields": contractFields, "idempotency_key": "defect-journey-contract"}, "defect-journey-contract")
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approve_contract=%+v", approved.Error)
	}
	if response := action(env, rcaID, "record_finding", map[string]any{}, "defect-journey-finding"); response.Outcome != OutcomeOK {
		t.Fatalf("record_finding=%+v", response.Error)
	}
	const reportEvidence = "evidence:defect-journey-report"
	report := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": rcaID, "expected_version": workVersion(t, s, rcaID), "action_id": "record_report",
		"fields":          map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": reportEvidence},
		"evidence":        []map[string]any{{"kind": "artifact", "authority": "concord", "locator_kind": "reference", "locator": reportEvidence}},
		"idempotency_key": "defect-journey-report",
	})}, env)
	if report.Outcome != OutcomeOK {
		t.Fatalf("record_report=%+v", report.Error)
	}
	if response := action(env, rcaID, "record_conclusion", map[string]any{}, "defect-journey-conclusion"); response.Outcome != OutcomeOK {
		t.Fatalf("record_conclusion=%+v", response.Error)
	}

	// The operator question gate reads an investigation observation naming a
	// current Domain of this Product and another work item; both are real.
	_, rootDomain := recoveryRegistry(t, s)
	observation := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: retryJSON(map[string]any{
		"work_id": rcaID, "observation_id": "obs:a1b2c3d4e5f60718", "statement": "The checksum cluster investigation names its scope",
		"refs": []string{rootDomain, bugID}, "idempotency_key": "defect-journey-observation",
	})}, env)
	if observation.Outcome != OutcomeOK {
		t.Fatalf("observation_record=%+v", observation.Error)
	}

	// 5. The independent verdict: a distinct evaluator session records the
	// healthy verdict against the approved contract.
	verifier := issue31EvaluatorGrant(t, service, privateKey)
	verifier.Worktree = worktree
	verifierEnv := mutationEnvelope(verifier, scopeVersion)
	verifierEnv.Worktree = worktree
	if response := action(verifierEnv, rcaID, "record_verdict", map[string]any{"contract_version": 1, "predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{reportEvidence}}, "defect-journey-verdict"); response.Outcome != OutcomeOK {
		t.Fatalf("independent record_verdict=%+v", response.Error)
	}

	// 6. The operator premise confirmation and the completion. The question
	// digest binds the operator's exact decision context.
	question, questionErr := store.ReadWorkflowOperatorQuestion(ctx, s, rcaID)
	if questionErr != nil || question == nil {
		t.Fatalf("operator question=%+v err=%v", question, questionErr)
	}
	confirmed := approvedAction(env, map[string]any{"work_id": rcaID, "expected_version": workVersion(t, s, rcaID), "action_id": "confirm_premise", "selected_choice": "confirm", "decision_context_digest": question.DecisionContextDigest, "idempotency_key": "defect-journey-confirm"}, "defect-journey-confirm")
	if confirmed.Outcome != OutcomeOK {
		t.Fatalf("confirm_premise=%+v", confirmed.Error)
	}
	completed := approvedAction(env, map[string]any{"work_id": rcaID, "expected_version": workVersion(t, s, rcaID), "action_id": "complete", "fields": map[string]any{"impact_verdict": "non-breaking"}, "idempotency_key": "defect-journey-complete"}, "defect-journey-complete")
	if completed.Outcome != OutcomeOK {
		t.Fatalf("complete=%+v", completed.Error)
	}
	if lifecycle := workLifecycle(t, s, rcaID); lifecycle != "completed" {
		t.Fatalf("root cause lifecycle=%s, want completed", lifecycle)
	}
	var definitionRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, rcaID).Scan(&definitionRef); err != nil {
		t.Fatal(err)
	}
	if definitionRef != "workflow.research" {
		t.Fatalf("root cause workflow family=%s", definitionRef)
	}

	// 7. The recurrent retry is admitted behind the completed root cause.
	retry := capture("defect-journey-retry",
		`{"title":"Checksum mismatch returned","value_statement":"The checksum failure returned after the fix","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-journey-retry","defect_intake":{"failure_shape":"journey-upload-checksum","reproduction":"upload the fixture and compare digests after the repair","searched":"the Product work list and the public tracker","related_defect_ids":[],"root_cause_work_id":"`+rcaID+`"}}`)
	if retry.Outcome != OutcomeOK {
		t.Fatalf("retry behind completed root cause=%+v", retry.Error)
	}
	retryID := (*retry.ChangedRefs)[0].ID
	var siblingsJSON string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json,'$.defect.sibling_ids') FROM work_items WHERE id=?`, retryID).Scan(&siblingsJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(siblingsJSON, bugID) {
		t.Fatalf("retry sibling snapshot=%s, want the first bug %s", siblingsJSON, bugID)
	}
}
