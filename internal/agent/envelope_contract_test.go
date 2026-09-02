package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #701 regression coverage. Committed mutation envelopes must satisfy
// the generated TS7 envelope contract — the same
// contracts/agent-tool-envelope.schema.json projection the adapter validates
// against. The core emitted derived product identifiers inside
// resolved_scope, a member the envelope law does not name, so committed
// lifecycle and worktree_reclaim mutations reached agents as
// operation_conflict/unknown_effect after the effect had already landed.
// Envelope.Validate now fails closed on that drift; these tests hold the
// line at both ends: a committed effect must return a contract-valid
// envelope, and the contract must keep refusing undeclared scope members.

func TestCommittedLifecycleEnvelopeSatisfiesGeneratedContract(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"in_progress","reason":"begin work","idempotency_key":"701-lifecycle"}`)}
	committed, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || committed.Outcome != OutcomeOK {
		t.Fatalf("lifecycle response=%+v err=%v", committed, err)
	}
	encoded, err := committed.Encode()
	if err != nil {
		t.Fatalf("committed lifecycle envelope failed its own producer validation: %v", err)
	}
	if err := ValidateGeneratedEnvelope(encoded); err != nil {
		t.Fatalf("committed lifecycle envelope failed the generated TS7 contract: %v; envelope=%s", err, encoded)
	}
	var read Envelope
	if err := json.Unmarshal(encoded, &read); err != nil {
		t.Fatalf("committed lifecycle envelope cannot be re-read as a TS7 envelope: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	scope, ok := document["resolved_scope"].(map[string]any)
	if !ok {
		t.Fatalf("committed envelope has no resolved_scope: %s", encoded)
	}
	if _, present := scope["product_ids"]; present {
		t.Fatalf("resolved_scope carries derived product identifiers the envelope law does not declare: %s", encoded)
	}
}

func TestCommittedReclaimEnvelopeSatisfiesGeneratedContract(t *testing.T) {
	ctx := context.Background()
	s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(t.TempDir(), "linked-wt")
	claimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-1", "project_id": "project-1",
		"branch": "work/contract-701", "base_sha": baseSHA, "path": worktreePath,
		"expected_version": 2, "idempotency_key": "701-claim",
	})
	claim, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: claimInput}, mutationEnvelope(grant, scopeVersion))
	if err != nil || claim.Outcome != OutcomeOK {
		t.Fatalf("claim response=%+v err=%v", claim, err)
	}
	reclaimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-1", "project_id": "project-1",
		"default_ref": "main", "expected_version": 3, "idempotency_key": "701-reclaim",
	})
	reclaim := InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_reclaim", Input: reclaimInput}
	committed, err := Dispatch(ctx, s, service, reclaim, mutationEnvelope(grant, scopeVersion))
	if err != nil || committed.Outcome != OutcomeOK {
		t.Fatalf("reclaim response=%+v err=%v", committed, err)
	}
	encoded, err := committed.Encode()
	if err != nil {
		t.Fatalf("committed reclaim envelope failed its own producer validation: %v", err)
	}
	if err := ValidateGeneratedEnvelope(encoded); err != nil {
		t.Fatalf("committed reclaim envelope failed the generated TS7 contract: %v; envelope=%s", err, encoded)
	}
	// The claimed worktree was clean and merged, so the contract-valid
	// envelope describes a real reclamation: the native worktree is gone.
	if listing := gitRun(t, repoRoot, "worktree", "list"); containsWorktreePath(listing, worktreePath) {
		t.Fatalf("native worktree still present after a committed reclaim:\n%s", listing)
	}
}

func containsWorktreePath(listing, path string) bool {
	for _, line := range strings.Split(listing, "\n") {
		if strings.TrimSpace(line) == "worktree "+path {
			return true
		}
	}
	return false
}

func TestGeneratedContractRejectsUndeclaredScopeMembers(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"in_progress","reason":"begin work","idempotency_key":"701-negative"}`)}
	committed, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || committed.Outcome != OutcomeOK {
		t.Fatalf("lifecycle response=%+v err=%v", committed, err)
	}
	encoded, err := committed.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	scope, ok := document["resolved_scope"].(map[string]any)
	if !ok {
		t.Fatalf("committed envelope has no resolved_scope: %s", encoded)
	}
	scope["product_ids"] = []string{"product-1"}
	tampered, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGeneratedEnvelope(tampered); err == nil {
		t.Fatal("generated TS7 contract accepted resolved_scope.product_ids, the exact #701 drift shape")
	}
}
