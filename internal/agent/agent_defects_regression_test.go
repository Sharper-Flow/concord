package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// TestRegressionDefect1_RelationEnvelopeShapeIsFromToKind drives
// concord_work_trace.relations through Dispatch against a fixture whose target
// work item HAS at least one relation (work-blocked has a depends_on/blocks
// edge to work-prereq) and asserts the envelope outcome is OK and the result's
// edges[0] carries from/to/kind with the expected IDs.
//
// Regression for the pre-fix q8 marshalling of []store.RelationEdge directly
// into the result. The store struct uses source/target JSON tags; the public
// agent envelope (work_relation_graph.edges in
// contracts/agent-tool-surface-payloads.schema.json) requires from/to/kind
// with additionalProperties: false. The pre-fix code produced a result that
// failed the schema's required-field check on any non-empty edge list.
//
// Pre-fix failure mode: Dispatch returns an envelope whose result-validation
// step rejects the envelope ("missing required $.edges[0].from"). Verified
// by stashing the q8 translation and observing Dispatch fail; the fix
// translates each store.RelationEdge into the agent-facing shape before
// building the result map.
func TestRegressionDefect1_RelationEnvelopeShapeIsFromToKind(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)

	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_trace",
		Operation: "relations",
		Input:     json.RawMessage(`{"work_id":"work-blocked","relation_kinds":["blocks"],"direction":"incoming"}`),
	}, env)

	if resp.Outcome != OutcomeOK {
		t.Fatalf("Dispatch concord_work_trace.relations outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal relations result: %v", err)
	}
	edges, ok := result["edges"].([]any)
	if !ok {
		t.Fatalf("edges missing or not a slice: %T", result["edges"])
	}
	if len(edges) == 0 {
		t.Fatalf("edges is empty for work-blocked; expected at least one blocks edge")
	}
	first, ok := edges[0].(map[string]any)
	if !ok {
		t.Fatalf("edges[0] is not an object: %T", edges[0])
	}
	// The envelope must use from/to/kind — the public spelling defined by the
	// payload schema. The store struct's source/target spelling is private.
	from, fromOK := first["from"].(string)
	to, toOK := first["to"].(string)
	kind, kindOK := first["kind"].(string)
	if !fromOK || !toOK || !kindOK {
		t.Fatalf("edges[0] missing from/to/kind: got %#v", first)
	}
	// direction=incoming for blocks kind: the stored row is
	// (work_id_from=work-prereq, work_id_to=work-blocked). The envelope
	// carries from=blocker, to=blocked per the work_relation_graph schema.
	if from != "work-prereq" || to != "work-blocked" || kind != "blocks" {
		t.Fatalf("edges[0] wrong ids: from=%q to=%q kind=%q", from, to, kind)
	}
	// The envelope schema forbids additional properties on edges[0]; in
	// particular the old store-source/store-target keys must not leak.
	if _, hasSource := first["source"]; hasSource {
		t.Fatalf("edges[0] leaks store-source key; from/to/kind are the envelope contract")
	}
	if _, hasTarget := first["target"]; hasTarget {
		t.Fatalf("edges[0] leaks store-target key; from/to/kind are the envelope contract")
	}
}

// TestRegressionDefect2_SupersededLifecycleInWorkSummaryValidates asserts that
// a work_summary-bearing result containing a superseded work item validates
// against the public envelope schema. Pre-fix, work_summary.properties.lifecycle
// resolved to a four-value enum (needed/in_progress/completed/cancelled), so
// any Product that had ever superseded a work item produced a result that
// failed validation with "enum mismatch at $.items[N].lifecycle". The fix
// widens the shared #/$defs/lifecycle enum to include 'superseded', while a
// narrower sibling def (#/$defs/lifecycle_transition_target) protects
// transition inputs.
//
// Pre-fix failure mode: ValidateOperationPayload against the work_summary
// result shape returns an enum-mismatch error whenever any item carries
// lifecycle=superseded.
func TestRegressionDefect2_SupersededLifecycleInWorkSummaryValidates(t *testing.T) {
	payload := []byte(`{"items":[{"id":"work-old","kind":"task","title":"Old","lifecycle":"superseded","version":3}],"next_cursor":null}`)
	// work_browse.list returns work_page which is a list of work_summary.
	if err := ValidateOperationPayload("concord_work_browse", "list", payload, true); err != nil {
		t.Fatalf("superseded lifecycle in work_summary must validate, got %v", err)
	}
}

// TestRegressionDefect2_TransitionTargetRejectsSuperseded asserts that the
// structural split between #/$defs/lifecycle (observed) and
// #/$defs/lifecycle_transition_target (four agent-requestable targets) is
// honored by the work_transition_lifecycle_input schema. Without this test,
// a future widening of #/$defs/lifecycle alone would silently permit an
// agent to request a direct transition to "superseded", violating
// AJ5-atomic-supersession's atomicity requirement.
//
// Pre-fix failure mode (after only widening lifecycle, without the new def):
// ValidateOperationPayload for concord_work_transition.lifecycle would accept
// target="superseded", violating the AJ5 invariant that supersession is
// atomic with its supersedes relation. The split restores the structural
// guarantee; the runtime guard in mutations.go remains as defense-in-depth.
func TestRegressionDefect2_TransitionTargetRejectsSuperseded(t *testing.T) {
	payload := []byte(`{"work_id":"work-x","expected_version":1,"target":"superseded","reason":"probe","idempotency_key":"probe-superseded"}`)
	err := ValidateOperationPayload("concord_work_transition", "lifecycle", payload, false)
	if err == nil {
		t.Fatalf("work_transition_lifecycle_input with target=superseded must be rejected by the schema; the structural guarantee forbids direct transitions")
	}
	// The remaining four targets must still validate.
	for _, target := range []string{"needed", "in_progress", "completed", "cancelled"} {
		ok := []byte(`{"work_id":"work-x","expected_version":1,"target":"` + target + `","reason":"probe","idempotency_key":"probe-` + target + `"}`)
		if err := ValidateOperationPayload("concord_work_transition", "lifecycle", ok, false); err != nil {
			t.Fatalf("work_transition_lifecycle_input with target=%s must validate, got %v", target, err)
		}
	}
}

// TestRegressionDefect2_LifecycleFilterAcceptsSuperseded asserts that the
// work_browse_list_input filter (a third reference to #/$defs/lifecycle) also
// accepts lifecycle=superseded, so a Product that has ever superseded work
// can still be filtered by that state. This guards against an accidental
// re-narrowing of the shared def that would re-break defect 2 only on the
// read-filter side.
func TestRegressionDefect2_LifecycleFilterAcceptsSuperseded(t *testing.T) {
	payload := []byte(`{"page":{"cursor":null,"limit":20},"lifecycle":"superseded"}`)
	if err := ValidateOperationPayload("concord_work_browse", "list", payload, false); err != nil {
		t.Fatalf("work_browse_list_input with lifecycle=superseded must validate, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Defect 3 — terminal lifecycle transitions must refuse missing evidence
// with the typed missing_evidence error and recovery_action=provide_evidence,
// and (separately) must mint a real approval_ref when evidence IS present.
// ---------------------------------------------------------------------------
//
// Pre-fix failure mode (D3): a single guard at mutations.go:1107 conflated
// missing evidence with missing approval and returned approval_required
// without ever calling requiresApproval, so the agent received an
// unactionable response ("request_approval") with no approval_ref to act
// on. Completing or cancelling any work item through the agent surface
// was therefore impossible: the agent could not progress past the gate
// because no challenge was ever minted.
//
// Post-fix behaviour: missing evidence is refused with kind=missing_evidence,
// recovery_action=provide_evidence, and details.required_kind=verification.
// The presence of evidence then lets the normal challenge-minting path
// execute and emit a real approval_ref the agent can sign against.

// TestRegressionDefect3_TerminalWithoutEvidenceReturnsMissingEvidence pins
// the missing-evidence side of the D3 repair: a complete transition with
// NO evidence must fail closed with kind=missing_evidence and recovery
// provide_evidence, and no approval_ref should be surfaced (because the
// agent has nothing to approve until it supplies evidence).
func TestRegressionDefect3_TerminalWithoutEvidenceReturnsMissingEvidence(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	resp, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "lifecycle",
		Input:     json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"completed","reason":"complete","idempotency_key":"d3-no-evidence"}`),
	}, env)
	if err != nil {
		t.Fatalf("dispatch returned err=%v", err)
	}
	if resp.Outcome != OutcomeError || resp.Error == nil {
		t.Fatalf("expected typed error, got outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "missing_evidence" {
		t.Fatalf("error.kind=%q, want missing_evidence (D3 repair)", resp.Error.Kind)
	}
	if resp.Error.RecoveryAction.Kind != "provide_evidence" {
		t.Fatalf("error.recovery_action=%q, want provide_evidence (D3 repair)", resp.Error.RecoveryAction.Kind)
	}
	if requiredKind, _ := resp.Error.Details["required_kind"].(string); requiredKind != "verification" {
		t.Fatalf("error.details.required_kind=%q, want verification", requiredKind)
	}
	// Crucially, the missing-evidence refusal must NOT carry an
	// approval_ref — the agent has nothing to approve yet.
	if _, hasApproval := resp.Error.Details["approval_ref"]; hasApproval {
		t.Fatalf("missing_evidence refusal leaked approval_ref; the agent cannot act on a challenge for unsupplied evidence")
	}
}

// TestRegressionDefect3_TerminalWithEvidenceMintsApprovalChallenge pins the
// approval-minting side of the D3 repair: a complete transition WITH
// evidence must take the normal challenge-minting path and emit a real
// 64-character approval_ref. Without this half of the repair the agent
// is permanently unable to complete work even when it does supply the
// correct evidence — the entire lifecycle workflow is unreachable.
func TestRegressionDefect3_TerminalWithEvidenceMintsApprovalChallenge(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	resp, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "lifecycle",
		Input:     json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"completed","reason":"complete","idempotency_key":"d3-with-evidence","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"verification-pass"}]}`),
	}, env)
	if err != nil {
		t.Fatalf("dispatch returned err=%v", err)
	}
	if resp.Outcome != OutcomeError || resp.Error == nil {
		t.Fatalf("expected approval_required, got outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "approval_required" {
		t.Fatalf("error.kind=%q, want approval_required (D3 repair)", resp.Error.Kind)
	}
	approvalRef, ok := resp.Error.Details["approval_ref"].(string)
	if !ok || len(approvalRef) != 64 {
		t.Fatalf("terminal-with-evidence must mint a 64-char approval_ref, got %v", resp.Error.Details["approval_ref"])
	}
}

// ---------------------------------------------------------------------------
// Defect 4 — version_conflict must carry a typed current_version
// structurally, not just a prose description in error.message.
// ---------------------------------------------------------------------------
//
// Pre-fix failure mode (D4): store.Failure formats the current version
// into Detail ("work-ready-low has version 2, want 1") but has no typed
// carrier. TypedError.CurrentVersions is therefore empty and agents had
// to regex the human prose to recover the live projection version —
// which broke under any wording change.
//
// Post-fix: store.Failure carries []SubjectCurrentVersion. The agent
// runtime maps it into TypedError.CurrentVersions with one ChangedRef
// per typed carrier, and the envelope validation at envelope.go:591
// enforces (kind=version_conflict ⇒ CurrentVersions non-empty and
// recovery=reread_entities). The corpus AJ4-stale-version scenario
// depends on this structural path.

// TestRegressionDefect4_VersionConflictCarriesTypedCurrentVersion drives
// concord_work_transition.lifecycle with a stale expected_version and
// asserts that the resulting TypedError carries a structurally typed
// CurrentVersions entry the agent can read without parsing prose.
func TestRegressionDefect4_VersionConflictCarriesTypedCurrentVersion(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	resp, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "lifecycle",
		Input:     json.RawMessage(`{"work_id":"work-1","expected_version":1,"target":"in_progress","reason":"stale","idempotency_key":"d4-stale"}`),
	}, env)
	if err != nil {
		t.Fatalf("dispatch returned err=%v", err)
	}
	if resp.Outcome != OutcomeError || resp.Error == nil {
		t.Fatalf("expected typed error, got outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "version_conflict" {
		t.Fatalf("error.kind=%q, want version_conflict", resp.Error.Kind)
	}
	if resp.Error.RecoveryAction.Kind != "reread_entities" {
		t.Fatalf("error.recovery_action=%q, want reread_entities", resp.Error.RecoveryAction.Kind)
	}
	if len(resp.Error.CurrentVersions) != 1 {
		t.Fatalf("error.current_versions is empty; D4 typed carrier must carry the live version")
	}
	current := resp.Error.CurrentVersions[0]
	if current.EntityKind != "work_item" {
		t.Fatalf("error.current_versions[0].entity_kind=%q, want work_item", current.EntityKind)
	}
	if current.ID != "work-1" {
		t.Fatalf("error.current_versions[0].id=%q, want work-1", current.ID)
	}
	if current.Version != "2" {
		t.Fatalf("error.current_versions[0].version=%q, want 2 (the live work-1 version after the fixture seeds memberships)", current.Version)
	}
}

// TestRegressionDefect4_VersionConflictEnvelopeValidates couples the
// D4 typed carrier to the envelope validator's coupling rule
// (envelope.go:591): a version_conflict response with empty
// CurrentVersions or the wrong recovery_action must be rejected at the
// envelope boundary. The pre-fix runtime produced such responses; the
// test pins the validator's coupling so a future regression is
// detected.
func TestRegressionDefect4_VersionConflictEnvelopeValidates(t *testing.T) {
	// Force-build an envelope whose kind/recovery are coupled to the
	// rule. The pre-fix runtime would have produced exactly this
	// shape, so this test acts as a forward guard on the coupling.
	env := NewBase("d4", "concord_work_transition", "lifecycle", ManifestVersion)
	errEnv := env
	errEnv.Outcome = OutcomeError
	errEnv.Error = &TypedError{
		Kind:            "version_conflict",
		RetrySafe:       false,
		RecoveryAction:  RecoveryAction{Kind: "reread_entities"},
		EffectState:     EffectNone,
		CurrentVersions: []ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: "2"}},
	}
	if err := errEnv.Validate(); err != nil {
		t.Fatalf("typed version_conflict with typed CurrentVersions must validate, got %v", err)
	}
	// Negative: a version_conflict with empty CurrentVersions must be
	// rejected. This is the coupling the D4 repair enforces — the
	// validator refuses the pre-fix shape.
	badEnv := NewBase("d4-bad", "concord_work_transition", "lifecycle", ManifestVersion)
	badEnv.Outcome = OutcomeError
	badEnv.Error = &TypedError{
		Kind:           "version_conflict",
		RetrySafe:      false,
		RecoveryAction: RecoveryAction{Kind: "reread_entities"},
		EffectState:    EffectNone,
		// intentionally no CurrentVersions
	}
	if err := badEnv.Validate(); err == nil {
		t.Fatalf("typed version_conflict without CurrentVersions must be rejected by the envelope validator (D4 coupling)")
	}
}

// ---------------------------------------------------------------------------
// Defect 5 — cycle refusal must carry a typed Violations list
// structurally, not just a prose description in error.message.
// ---------------------------------------------------------------------------
//
// Pre-fix failure mode (D5): the relation-cycle refusal at
// store/lifecycle.go:558 sets error message
// "blocks relation would create a cycle" but never populates a typed
// carrier for the offending edge. TypedError.Violations is therefore
// empty and agents had to regex the prose — which broke under any
// wording change and made the cycle edge unreachable structurally.
//
// Post-fix: every KindCycleDetected refusal in the relation path now
// carries []string{"<kind>:<from>-><to>"} on store.Failure.Violations,
// and the runtime maps it into TypedError.Violations at the agent
// boundary.

// TestRegressionDefect5_CycleRefusalCarriesTypedViolations drives
// concord_work_relate.link with a pair of work items that already form
// a blocks edge in reverse, expecting the cycle detector to refuse.
// The test asserts that the typed Violations entry is non-empty AND
// names the offending edge structurally — without that the runner has
// no machine-readable way to know which edge caused the cycle.
func TestRegressionDefect5_CycleRefusalCarriesTypedViolations(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	// Seed work-2 so the cycle has two distinct endpoints, and seed
	// the work-1 → work-2 blocks edge so a reverse edge would cycle.
	// Seed work-2 so the cycle has two distinct endpoints, and seed
	// the work-1 → work-2 blocks edge so a reverse edge would cycle.
	// Each step is its own ApplyOperation so the version checks line
	// up with the durable log (the seeded events bump versions
	// incrementally, and ApplyOperation only accepts one expected
	// version per subject per batch).
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{
		{EventID: "d5-work-2-created", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"D5 second work","priority":1}`)},
		{EventID: "d5-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{
		{EventID: "d5-work-1-blocks-work-2", Kind: "relation.added", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"work-1","to":"work-2","kind":"blocks","reason":"seed","expected_version":2,"resulting_version":3,"to_expected_version":2,"to_resulting_version":3}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-1"): 2, store.VersionRef(store.SubjectWorkItem, "work-2"): 2}}); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	// Attempt to add the reverse blocks edge — work-2 → work-1 — which
	// would close the cycle work-1 → work-2 → work-1.
	resp, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool:      "concord_work_relate",
		Operation: "link",
		Input:     json.RawMessage(`{"from_work_id":"work-2","to_work_id":"work-1","from_expected_version":3,"to_expected_version":3,"kind":"blocks","reason":"cycle","idempotency_key":"d5-cycle"}`),
	}, env)
	if err != nil {
		t.Fatalf("dispatch returned err=%v", err)
	}
	if resp.Outcome != OutcomeError || resp.Error == nil {
		t.Fatalf("expected typed error, got outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "invalid_relation" {
		t.Fatalf("error.kind=%q, want invalid_relation", resp.Error.Kind)
	}
	if len(resp.Error.Violations) == 0 {
		t.Fatalf("error.violations empty; D5 typed carrier is missing — agents must regex prose otherwise")
	}
	found := false
	for _, v := range resp.Error.Violations {
		if v == "blocks:work-2->work-1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("error.violations missing blocks:work-2->work-1: %v", resp.Error.Violations)
	}
}
