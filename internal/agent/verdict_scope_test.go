package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// workflowEngineFixture seeds a workflow and returns an engine closure that
// applies further workflow actions at the work item's current version.
func workflowEngineFixture(t *testing.T, _ string) (*store.Store, *Service, func(string, map[string]any)) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := append(agentFixtureProductEvents("verdict", "product-1", "project-1", "Verdict Scope", "Verdict Project", "verdict fixture"),
		store.Event{EventID: "verdict-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"bug","title":"Verdict Scope Work"}`)},
		store.Event{EventID: "verdict-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	)
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	seedCurrentWorkflowDomainFixture(t, s)

	service := NewService(s)
	service.Now = fixedTime
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1"})

	clients := []string{"client-session-exec-aaaa", "client-session-other-bbbb"}
	for _, client := range clients {
		publicKey, _, _ := ed25519.GenerateKey(cryptorand.Reader)
		policy := TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []agentcontract.Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}, AgentScope: testFixtureAgents}
		if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: client, KeyID: "key-" + client, PublicKey: publicKey, Policy: policy}); err != nil {
			t.Fatal(err)
		}
	}

	// The executing actor is session 0's identity; session 1 is distinct.
	// The workflow instance and executing actor come from the real
	// initialization path, so the executing identity is folded, not faked.
	execActor := store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-session-exec-aaaa", AgentRef: "agent-exec", SessionRef: "session-exec-aaaa", ActorClass: store.ActorAgent}
	ctx = agentWorkflowSourceProof(t, ctx, s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Actor: execActor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	// Drive the real engine through the Product-changing planning contract and
	// into repair, whose action_started fold records the executing actor.
	// Distinct invocations of the same action kind need distinct durable
	// identities, so every applied action claims the next sequence number.
	engineSequence := 0
	engineAction := func(version int64, actionID string, payload map[string]any) {
		t.Helper()
		engineSequence++
		identity := fmt.Sprintf("verdict-seed-%s-%d", actionID, engineSequence)
		raw, _ := json.Marshal(payload)
		request := store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: actionID, Payload: raw,
			Actor: execActor, AcceptedInputsDigest: "sha256:" + strings.Repeat("2", 64), IdempotencyIdentity: identity,
			OperationID: "verdict-seed-op-" + identity, PrincipalRef: "human-1", Tool: "concord-test", IdempotencyKey: "verdict-seed-key-" + identity,
			RequestID: "verdict-seed-request-" + identity, AcceptedScope: `{"project":"project-1"}`, ContractDigest: agentcontract.ManifestDigest, Now: fixedTime(),
		}
		preflight := store.WorkflowActionPreflightRequest{WorkID: "work-1", ExpectedVersion: version, ActionID: actionID, Payload: raw, Actor: execActor}
		if err := store.AuthorizeWorkflowActionAtBoundaryTx(ctx, s, preflight, nil, func(tx *store.Transaction) error {
			_, err := store.ApplyWorkflowActionTx(ctx, tx, request)
			return err
		}); err != nil {
			t.Fatalf("%s at v%d: %v", actionID, version, err)
		}
	}
	engine := func(actionID string, payload map[string]any) {
		t.Helper()
		var version int64
		if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT version FROM work_items WHERE id=?`, "work-1").Scan(&version); err != nil {
			t.Fatalf("cannot read work version for %s: %v", actionID, err)
		}
		engineAction(version, actionID, payload)
	}
	engine("record_intake", map[string]any{"outcome": "proceed"})
	engine("pick_type", map[string]any{"type": "fix", "reason": "The work repairs a defect."})
	engine("record_diagnosis", map[string]any{
		"reproduction": "The bounded defect reproduction statement.",
		"searched":     "The bounded backlog search statement.",
		"outcome":      "none_found",
		"root_cause":   "The bounded root cause statement.",
	})
	engine("complete_step", map[string]any{"step_id": "diagnose", "note": "Diagnosis recorded."})
	engine("record_clarify", map[string]any{"resolved_questions": []string{}, "note": "No operator questions remain."})
	engine("complete_step", map[string]any{"step_id": "clarify", "note": "Clarification complete."})
	engine("record_plan", agentPlanFields())
	contractFields := workflowContractFieldsForSeededRegistry(t, s)
	contractFields["legislative_subjects"] = []string{"work-1"}
	engine("approve_contract", contractFields)
	engine("complete_step", map[string]any{"step_id": "plan", "note": "Plan approved."})
	engine("start_execution", map[string]any{})
	return s, service, engine
}
