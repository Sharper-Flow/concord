package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// A scope list that names the same work item twice must not mask a
// typed outcome behind an Envelope.MarshalJSON failure. Scope-list
// normalization is the shared owner: bindings render strictly sorted and
// unique however often a path repeats an identity, resolved scopes stay
// unique, and the strict envelope check that rejects duplicate bindings
// stays exactly as strict as it was.

func TestNormalizeScopeListDeduplicatesStringAndJSONArrayLists(t *testing.T) {
	t.Parallel()
	inOrder := []string{"work-b", "work-a", "work-b", "work-c", "work-a"}
	want := []string{"work-b", "work-a", "work-c"}
	if got := normalizeScopeList(inOrder); !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeScopeList(%v)=%v want %v", inOrder, got, want)
	}
	// A JSON-decoded list (stored challenge, approval, or idempotency
	// snapshot) carries []any with non-string members possible.
	decoded := []any{"work-b", "work-a", 7, nil, "work-c", "work-b", "work-a"}
	if got := scopeListValues(decoded); !reflect.DeepEqual(got, want) {
		t.Fatalf("scopeListValues(%v)=%v want %v", decoded, got, want)
	}
	if got := scopeListValues("work-a"); got != nil {
		t.Fatalf("scalar scope value decoded to %v want nil", got)
	}
	if got := scopeListValues([]any{1.5, true}); len(got) != 0 {
		t.Fatalf("non-string members decoded to %v want none", got)
	}
}

func TestNormalizeScopeListPreservesUniqueOrderBytesAndOwnership(t *testing.T) {
	t.Parallel()
	unique := []string{"work-c", "work-a", "work-b"}
	before, _ := json.Marshal(unique)
	normalized := normalizeScopeList(unique)
	after, _ := json.Marshal(normalized)
	if string(before) != string(after) {
		t.Fatalf("already unique list changed bytes: %s became %s", before, after)
	}
	// Non-aliasing: the normalized copy never shares a backing array with
	// the caller's slice, so neither can observe the other change.
	if len(normalized) > 0 && &normalized[0] == &unique[0] {
		t.Fatal("normalized list aliases the caller's slice")
	}
	normalized[0] = "work-mutated"
	if unique[0] != "work-c" {
		t.Fatalf("caller-owned slice mutated: %v", unique)
	}
	// scopeFromMap resolves the same contract for the wire scope, for both
	// builder-set and JSON-decoded lists.
	scope := map[string]any{"work_ids": []string{"w-1", "w-1", "w-2"}, "project_ids": []any{"p-2", "p-1", "p-2"}}
	resolved := scopeFromMap(scope)
	if !reflect.DeepEqual(resolved.WorkIDs, []string{"w-1", "w-2"}) || !reflect.DeepEqual(resolved.ProjectIDs, []string{"p-2", "p-1"}) {
		t.Fatalf("resolved scope work=%v project=%v want unique first-occurrence order", resolved.WorkIDs, resolved.ProjectIDs)
	}
	if err := validateScope(resolved); err != nil {
		t.Fatalf("resolved scope with repeated identities rejected: %v", err)
	}
}

func TestScopeListValuesPreservesUniqueJSONBytesAndOwnership(t *testing.T) {
	t.Parallel()
	for _, input := range []any{[]string(nil), []string{}, []any(nil), []any{}, []any{"work-c", "work-a", "work-b"}} {
		before, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		got := scopeListValues(input)
		after, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("unique %T list changed JSON bytes: %s became %s", input, before, after)
		}
		if !reflect.DeepEqual(scopeListValues(got), got) {
			t.Errorf("normalization is not idempotent for %v", input)
		}
		if len(got) > 0 {
			got[0] = "work-mutated"
			unchanged, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(unchanged) {
				t.Errorf("normalization aliases input: %s became %s", before, unchanged)
			}
		}
	}
}

func TestApprovalScopeBindingsRenderUniqueSortedBindingsForRepeatedIdentities(t *testing.T) {
	t.Parallel()
	scope := map[string]any{
		"product_id":    "product-1",
		"product_ids":   []any{"product-1", "product-2", "product-1"},
		"project_ids":   []string{"project-1"},
		"work_ids":      []string{"work-1", "work-2", "work-1", "work-2"},
		"scope_version": "sv-1",
	}
	bindings := approvalScopeBindings(scope)
	if !sortedBoundedList(bindings, maxConsequenceSummaryBindings) {
		t.Fatalf("bindings are not strictly sorted and unique: %v", bindings)
	}
	duplicates := 0
	for i := 1; i < len(bindings); i++ {
		if bindings[i] == bindings[i-1] {
			duplicates++
		}
	}
	if duplicates != 0 {
		t.Fatalf("bindings carry %d duplicate entries: %v", duplicates, bindings)
	}
	for _, binding := range bindings {
		if strings.Count(strings.Join(bindings, "\n"), binding) != 1 {
			t.Fatalf("binding %q appears more than once in %v", binding, bindings)
		}
	}
	// The repeated work_ids list renders exactly one binding per identity.
	found := 0
	for _, binding := range bindings {
		if strings.HasPrefix(binding, "work_ids:") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("work_ids rendered %d bindings want 2: %v", found, bindings)
	}
}

// TestConsequenceSummaryStillRejectsNonCanonicalBindings pins the strict
// envelope half of the contract: canonicalization happens at the scope-list
// owner, never by loosening the summary validator.
func TestConsequenceSummaryStillRejectsNonCanonicalBindings(t *testing.T) {
	t.Parallel()
	duplicate := &ConsequenceSummary{Tool: "concord_work_relate", Operation: "message_send", Consequence: "relation", OperationDigest: "sha256:" + strings.Repeat("0", 64), Scope: []string{"work_ids:work-a", "work_ids:work-a"}, Versions: []string{}, ExpiresAt: fixedTime().Format(time.RFC3339Nano)}
	if err := validateConsequenceSummaryShape(duplicate); err == nil {
		t.Fatal("duplicate summary bindings accepted")
	}
	unsorted := &ConsequenceSummary{Tool: "concord_work_relate", Operation: "message_send", Consequence: "relation", OperationDigest: "sha256:" + strings.Repeat("0", 64), Scope: []string{"work_ids:work-b", "work_ids:work-a"}, Versions: []string{}, ExpiresAt: fixedTime().Format(time.RFC3339Nano)}
	if err := validateConsequenceSummaryShape(unsorted); err == nil {
		t.Fatal("unsorted summary bindings accepted")
	}
	canonical := &ConsequenceSummary{Tool: "concord_work_relate", Operation: "message_send", Consequence: "relation", OperationDigest: "sha256:" + strings.Repeat("0", 64), Scope: []string{"work_ids:work-a", "work_ids:work-b"}, Versions: []string{}, ExpiresAt: fixedTime().Format(time.RFC3339Nano)}
	if err := validateConsequenceSummaryShape(canonical); err != nil {
		t.Fatalf("canonical summary bindings rejected: %v", err)
	}
}

// assertSelfSendInvariantRefusal holds the shared regression shape:
// an authorized direct self-addressed message_send answers with an
// encodable typed invariant_violation, effect state none, naming the store
// invariant's two sources, and leaves no message, version, or approval
// challenge effect behind.
func assertSelfSendInvariantRefusal(t *testing.T, s *store.Store, response Envelope, workID string, dispatchErr error) {
	t.Helper()
	if dispatchErr != nil {
		t.Fatalf("self-addressed send returned err=%v", dispatchErr)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invariant_violation" {
		t.Fatalf("self-addressed send wants invariant_violation: outcome=%s error=%+v", response.Outcome, response.Error)
	}
	if response.Error.EffectState != EffectNone {
		t.Fatalf("self-addressed send effect_state=%s want none", response.Error.EffectState)
	}
	if response.Error.RetrySafe {
		t.Fatal("self-addressed send must not be retry-safe")
	}
	if response.Error.RecoveryAction.Kind != "reread_entities" {
		t.Fatalf("self-addressed send recovery=%s want reread_entities (the envelope schema couples an options-less invariant_violation to it)", response.Error.RecoveryAction.Kind)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("the refusal envelope does not marshal: %v", err)
	}
	for _, source := range []string{
		"internal/store/work_messages.go foldMessageSent",
		"internal/store/schema.go work_messages CHECK(recipient_work_id != sender_work_id)",
	} {
		if !strings.Contains(string(encoded), source) {
			t.Fatalf("refusal does not name invariant source %q: %s", source, encoded)
		}
	}
	sources, ok := response.Error.Details["invariant_sources"].([]string)
	if !ok || len(sources) != 2 {
		t.Fatalf("invariant provenance missing: %+v", response.Error.Details)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_messages`); got != 0 {
		t.Fatalf("self-addressed send left %d messages", got)
	}
	if version := workVersion(t, s, workID); version != 2 {
		t.Fatalf("self-addressed send changed the sender version to %d", version)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
		t.Fatalf("self-addressed send minted %d approval challenges", got)
	}
}

// TestMessageSendSelfRecipientRefusedAsInvariantSingleProduct holds the
// single-Product case: the refusal precedes any effect, so the store fold
// and its CHECK constraint are never reached with a self-addressed event.
func TestMessageSendSelfRecipientRefusedAsInvariantSingleProduct(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant := messagesFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"work_id": "work-sender", "recipient_work_id": "work-sender", "body": "a coordinator cannot hand off to itself", "expected_version": 2, "idempotency_key": "msg-self-1"})
	response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "message_send", Input: raw}, mutationEnvelope(grant, scopeVersion))
	assertSelfSendInvariantRefusal(t, s, response, "work-sender", dispatchErr)
}

// TestMessageSendSelfRecipientRefusedAsInvariantCrossProduct holds the
// observed failure shape: on shared cross-Product work the unfixed core
// minted an approval challenge whose scope named the shared work twice, and
// the duplicate binding made the refusal envelope unmarshalable, so the
// operator never received the typed outcome at all.
func TestMessageSendSelfRecipientRefusedAsInvariantCrossProduct(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := crossProductDispatchFixture(t, []Capability{"work_relate", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"work_id": "work-1", "recipient_work_id": "work-1", "body": "a coordinator cannot leave a durable handoff on its own shared work item", "expected_version": 2, "idempotency_key": "msg-self-cross-1"})
	response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "message_send", Input: raw}, mutationEnvelope(grant, scopeVersion))
	assertSelfSendInvariantRefusal(t, s, response, "work-1", dispatchErr)
}

// TestLessonPublishSelfPublicationCanonicalScopeRoundTrip holds the repeated
// -work scope path end to end: a publication that pins itself as its own
// publication work mints a challenge whose summary bindings are canonical,
// the host assertion built from the same duplicate-named scope map consumes
// the challenge, and the resolved scope carries each work once.
func TestLessonPublishSelfPublicationCanonicalScopeRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey, _, worktree := lessonDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-pub","lesson_id":"lesson-self-pub-scope","title":"Self publication","summary":"The publication work is the sending work itself.","content":"# Self publication\n","coverage":{"state":"out_of_scope","reason":"Fixture probe for the canonical scope owner."},"idempotency_key":"lesson-self-pub-1","publication_work_id":"work-pub"}`)
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "lesson_publish", Input: input}

	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil || missing.Outcome != OutcomeError || missing.Error == nil || missing.Error.Kind != "approval_required" {
		t.Fatalf("missing approval response=%+v err=%v", missing.Error, err)
	}
	summary := missing.Error.ConsequenceSummary
	if summary == nil {
		t.Fatalf("challenge carries no consequence summary: %+v", missing.Error.Details)
	}
	if !sortedBoundedList(summary.Scope, maxConsequenceSummaryBindings) {
		t.Fatalf("summary bindings are not strictly sorted and unique: %v", summary.Scope)
	}
	duplicateScope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-pub", "work-pub"}, "scope_version": scopeVersion}
	if want := approvalScopeBindings(duplicateScope); !reflect.DeepEqual(summary.Scope, want) {
		t.Fatalf("summary scope=%v want the canonical bindings of the duplicate-named scope %v", summary.Scope, want)
	}
	encoded, err := json.Marshal(missing)
	if err != nil {
		t.Fatalf("the challenge envelope does not marshal: %v", err)
	}
	if strings.Contains(string(encoded), `"work_ids:work-pub","work_ids:work-pub"`) {
		t.Fatalf("the challenge envelope carries a duplicate binding: %s", encoded)
	}

	challengeRef, ok := missing.Error.Details["approval_ref"].(string)
	if !ok {
		t.Fatalf("challenge carries no approval ref: %v", missing.Error.Details)
	}
	approvedInput, _ := json.Marshal(map[string]any{
		"work_id": "work-pub", "lesson_id": "lesson-self-pub-scope",
		"title": "Self publication", "summary": "The publication work is the sending work itself.",
		"content":             "# Self publication\n",
		"coverage":            map[string]any{"state": "out_of_scope", "reason": "Fixture probe for the canonical scope owner."},
		"publication_work_id": "work-pub", "idempotency_key": "lesson-self-pub-1",
		"approval": map[string]any{"approval_ref": challengeRef},
	})
	request.Input = approvedInput
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, duplicateScope, map[string]any{"work": 2}, "session-1", "agent-1", worktree, fixedTime(), "self-pub-approval-0001")

	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved self-publication response=%+v err=%v", approved.Error, err)
	}
	if approved.ResolvedScope == nil || !reflect.DeepEqual(approved.ResolvedScope.WorkIDs, []string{"work-pub"}) {
		t.Fatalf("resolved work scope=%v want [work-pub] once", approved.ResolvedScope)
	}
	if err := validateScope(approved.ResolvedScope); err != nil {
		t.Fatalf("resolved scope invalid: %v", err)
	}
	// An exact retry replays from the stored snapshot, whose JSON-decoded
	// scope list names the work twice. The compact replay resolves its
	// wire scope from step state, so the snapshot itself is the authority
	// here: decoded through the shared owner it must resolve each work
	// once, exactly as a non-compact mutation replay's scopeFromMap does.
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var snapshot string
	if err := s.DatabaseForTesting().QueryRow(`SELECT authorized_scope_snapshot FROM idempotency_records WHERE idempotency_key=?`, "lesson-self-pub-1").Scan(&snapshot); err != nil {
		t.Fatalf("stored snapshot unreadable: %v", err)
	}
	authorizedScope, err := authorizedScopeFromSnapshot(snapshot)
	if err != nil {
		t.Fatalf("stored snapshot undecodable: %v", err)
	}
	if resolved := scopeFromMap(authorizedScope); resolved == nil || !reflect.DeepEqual(resolved.WorkIDs, []string{"work-pub"}) {
		t.Fatalf("snapshot-decoded resolved work scope=%v want [work-pub] once", resolved)
	}
}
