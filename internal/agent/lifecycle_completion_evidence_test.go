package agent

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestLifecycleCompletionEvidenceRefs proves the response envelope for a
// terminal lifecycle transition carries the evidence the caller supplied.
//
// The adapter's closure receipt reads lifecycle and evidence_refs from one
// envelope. Before this binding existed, evidence reached only the transition
// event payload, so the receipt could never render: the single response that
// reported lifecycle "completed" always carried an empty evidence_refs.
func TestLifecycleCompletionEvidenceRefs(t *testing.T) {
	s, service, grant, privateKey, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	_, preVersion := readWorkFromStore(t, s, "work-cross")
	idempotencyKey := fmt.Sprintf("evidence-refs-cross-%d", preVersion)
	input := []byte(fmt.Sprintf(`{"work_id":"work-cross","expected_version":%d,"target":"completed","reason":"complete with verification","idempotency_key":"%s","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"verification-pass"}]}`, preVersion, idempotencyKey))

	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if first.Error == nil || first.Error.Kind != "approval_required" {
		t.Fatalf("expected approval_required, got outcome=%s err=%+v", first.Outcome, first.Error)
	}
	challengeRef, ok := first.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval challenge malformed: %v", first.Error.Details)
	}
	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatalf("inject approval: %v", err)
	}
	scope := map[string]any{
		"product_id":    "prod-alpha",
		"product_ids":   []string{"prod-alpha"},
		"project_ids":   []string{"proj-web"},
		"work_ids":      []string{"work-cross"},
		"scope_version": env.ScopeVersion,
	}
	versions := map[string]any{"work": preVersion}
	digest := mutationDigest("concord_work_transition", "lifecycle", env, withApproval)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))

	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: withApproval}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved dispatch failed outcome=%s err=%+v", approved.Outcome, approved.Error)
	}
	if lifecycle, _ := readWorkFromStore(t, s, "work-cross"); lifecycle != "completed" {
		t.Fatalf("post-lifecycle=%q, want completed", lifecycle)
	}
	// The receipt gate needs both facts in this one envelope.
	if len(approved.EvidenceRefs) != 1 {
		t.Fatalf("completion envelope evidence_refs=%d, want 1: %+v", len(approved.EvidenceRefs), approved.EvidenceRefs)
	}
	if got := approved.EvidenceRefs[0].Locator; got != "verification-pass" {
		t.Fatalf("evidence_refs[0].locator=%q, want verification-pass", got)
	}
	if got := approved.EvidenceRefs[0].Kind; got != "verification" {
		t.Fatalf("evidence_refs[0].kind=%q, want verification", got)
	}

	// A replayed completion must surface the same evidence, or the receipt
	// renders once and silently stops on every retry of the same key.
	replayDigest := mutationDigest("concord_work_transition", "lifecycle", env, withApproval)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, replayDigest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: withApproval}, env)
	if !replay.Replayed {
		t.Fatalf("replay did not set Replayed=true")
	}
	if len(replay.EvidenceRefs) != 1 || replay.EvidenceRefs[0].Locator != "verification-pass" {
		t.Fatalf("replayed completion evidence_refs=%+v, want the original verification evidence", replay.EvidenceRefs)
	}
}

// TestCompleteActionBindsNoEvidenceRefs proves the workflow_action "complete"
// no longer binds envelope evidence_refs.
//
// The binding used to live there and could never fire: "complete" is not a
// lifecycle writer, so the envelope that carried the evidence always reported
// a non-terminal lifecycle. Keeping both bindings would leave the dead one
// beside the live one, so this asserts the dead one is gone rather than
// merely bypassed.
func TestCompleteActionBindsNoEvidenceRefs(t *testing.T) {
	actionPayload := []byte(`{"work_id":"work-cross","expected_version":3,"action_id":"complete","idempotency_key":"complete-no-binding","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"verification-pass"}],"fields":{"impact_verdict":"non-breaking"}}`)
	if evidence, bound := completionEvidenceRefs(actionPayload); bound || evidence != nil {
		t.Fatalf("workflow_action complete still binds evidence_refs: bound=%v evidence=%+v", bound, evidence)
	}

	// The same helper must recognize the lifecycle completion that replaced it.
	lifecyclePayload := []byte(`{"work_id":"work-cross","expected_version":3,"target":"completed","reason":"done","idempotency_key":"lifecycle-binding","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"verification-pass"}]}`)
	evidence, bound := completionEvidenceRefs(lifecyclePayload)
	if !bound {
		t.Fatalf("lifecycle completion does not bind evidence_refs")
	}
	if len(evidence) != 1 || evidence[0].Locator != "verification-pass" {
		t.Fatalf("lifecycle completion evidence=%+v, want the supplied verification evidence", evidence)
	}

	// A non-terminal lifecycle target carries no closure evidence.
	inProgress := []byte(`{"work_id":"work-cross","expected_version":3,"target":"in_progress","reason":"begin","idempotency_key":"lifecycle-non-terminal"}`)
	if _, bound := completionEvidenceRefs(inProgress); bound {
		t.Fatalf("non-terminal lifecycle target bound closure evidence")
	}

	// Guard the JSON shape the helper depends on.
	var probe lifecycleMutationInput
	if err := json.Unmarshal(lifecyclePayload, &probe); err != nil {
		t.Fatalf("lifecycle payload no longer decodes: %v", err)
	}
	if probe.Target != "completed" || len(probe.Evidence) != 1 {
		t.Fatalf("decoded lifecycle input target=%q evidence=%d", probe.Target, len(probe.Evidence))
	}
}
