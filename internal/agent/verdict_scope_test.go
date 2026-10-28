package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// CD-0023: the recorded acceptance verdict of a terminal work item is
// readable by every authority except the actor recorded as executing it.

func verdictScopeFixture(t *testing.T) (*store.Store, *Service, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	s, service, execKey, otherKey, _ := workflowEngineFixture(t, "")
	execRef := store.DeriveWorkflowActorRef("human-1", "client-session-exec-aaaa", "agent-exec", "session-exec-aaaa")
	seedVerdictEvent(t, s.DatabaseForTesting(), execRef)
	return s, service, execKey, otherKey
}

// workflowEngineFixture seeds the verdict-scope workflow with an optional
// contract premise override and returns an engine closure that applies
// further workflow actions at the work item's current version. The premise
// override exists so the bound round-trip test can approve the legal maximum.
func workflowEngineFixture(t *testing.T, premise string) (*store.Store, *Service, ed25519.PrivateKey, ed25519.PrivateKey, func(string, map[string]any)) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := append(agentFixtureProductEvents("verdict", "product-1", "project-1", "Verdict Scope", "Verdict Project", "verdict fixture"),
		store.Event{EventID: "verdict-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"bug","title":"Verdict Scope Work","priority":1,"defect_intake":{"failure_shape":"verdict-scope-read","reproduction":"Read the verdict from the executing and distinct sessions.","searched":"The fixture store contains no earlier defect of this shape.","related_defect_ids":[]}}`)},
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
	keys := []ed25519.PrivateKey{}
	for _, client := range clients {
		publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
		policy := TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []agentcontract.Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}, AgentScope: testFixtureAgents}
		if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: client, KeyID: "key-" + client, PublicKey: publicKey, Policy: policy}); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, privateKey)
	}

	// The executing actor is session 0's identity; session 1 is distinct.
	// The workflow instance and executing actor come from the real
	// initialization path, so the executing identity is folded, not faked.
	definition, defErr := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if defErr != nil {
		t.Fatal(defErr)
	}
	execActor := store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-session-exec-aaaa", AgentRef: "agent-exec", SessionRef: "session-exec-aaaa", ActorClass: store.ActorAgent}
	ctx = agentWorkflowSourceProof(t, ctx, s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: execActor, Now: fixedTime()})
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
		if err := store.AuthorizeWorkflowActionAtBoundaryTx(ctx, s, store.BuiltinWorkflowRegistry(), preflight, nil, func(tx *store.Transaction) error {
			_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), request)
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
	// The current head folds the three diagnosis records into one typed
	// record_diagnosis and derives the repair start from the qualifying
	// checkpoint (CD-0198 D2/D3).
	engine("record_diagnosis", map[string]any{
		"reproduction": "The bounded defect reproduction statement.",
		"searched":     "The bounded backlog search statement.",
		"outcome":      "none_found",
		"root_cause":   "The bounded root cause statement.",
	})
	contractFields := workflowContractFieldsForSeededRegistry(t, s)
	contractFields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "absent", "outcome_payload": map[string]any{
		"kind": "absent", "surface": "behavior:defect", "subjects": []string{"work-1"}, "distinguish_from": []string{"disabled"},
	}}}
	if premise != "" {
		contractFields["premise"] = premise
	}
	engine("approve_contract", contractFields)
	engine("checkpoint_repair", map[string]any{})
	return s, service, keys[0], keys[1], engine
}

// seedVerdictEvent seeds the current terminal verdict without driving the
// workflow's unrelated completion gates.
func seedVerdictEvent(t *testing.T, db *sql.DB, execActorRef string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"work_id": "work-1", "terminal_state": "completed", "final_verdict_kind": "ok",
		"verdict_actor_ref": execActorRef, "premise_confirmed": true, "evidence_count": 1,
		"changed_refs_digest": "sha256:" + strings.Repeat("1", 64), "impact_verdict": "non-breaking",
	})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	agentSeedReceipt(t, tx, "verdict-seed-completed", store.WorkflowCompleted, "work-1", execActorRef, 1, payload)
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET
			completion_payload=?,
			completion_occurred_at='2026-08-14T00:01:00Z'
		WHERE work_id='work-1';
		DELETE FROM write_guard`, string(payload)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func scopeReadFor(t *testing.T, s *store.Store, service *Service, privateKey ed25519.PrivateKey, nonce, session, agent, client string) agentcontract.Envelope {
	t.Helper()
	ctx := context.Background()
	_ = privateKey
	_ = nonce
	grant, err := service.Authorize(ctx, Invocation{ClientRef: client, PrincipalRef: "human-1", SessionRef: session, AgentRef: agent, Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "product_read", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "scope", Input: json.RawMessage(`{"product_id":"product-1","work_id":"work-1"}`)}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestExecutingSessionCannotReadItsOwnVerdict(t *testing.T) {
	t.Parallel()
	s, service, execKey, _ := verdictScopeFixture(t)

	response := scopeReadFor(t, s, service, execKey, "verdict-exec-nonce-0001", "session-exec-aaaa", "agent-exec", "client-session-exec-aaaa")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("executing-session read failed: %+v", response.Error)
	}
	raw, _ := json.Marshal(response.Result)
	if strings.Contains(string(raw), "final_verdict_kind") || strings.Contains(string(raw), "verdict_actor_ref") {
		t.Fatalf("executing session must not read the verdict, got: %s", raw)
	}
	redacted := false
	for _, omission := range response.Omissions {
		if omission.Kind == "redacted" && omission.SourceID == "work-1" {
			redacted = true
		}
	}
	if !redacted {
		t.Fatalf("expected an explicit redaction omission, got %+v", response.Omissions)
	}
}

func TestDistinctSessionReadsTheVerdict(t *testing.T) {
	t.Parallel()
	s, service, _, otherKey := verdictScopeFixture(t)

	response := scopeReadFor(t, s, service, otherKey, "verdict-distinct-nonce-2", "session-other-bbbb", "agent-other", "client-session-other-bbbb")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("distinct-session read failed: %+v", response.Error)
	}
	raw, _ := json.Marshal(response.Result)
	if !strings.Contains(string(raw), `"final_verdict_kind":"ok"`) {
		t.Fatalf("distinct session must read the verdict, got: %s", raw)
	}
	if len(response.Omissions) != 0 {
		t.Fatalf("distinct session must not see redaction, got %+v", response.Omissions)
	}
}

func TestVerdictReadScopeRequiresBothRecordedIdentityRefs(t *testing.T) {
	for _, tc := range []struct {
		name, agent, session string
	}{
		{"different session", "agent-exec", "session-other-bbbb"},
		{"different agent", "agent-other", "session-exec-aaaa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, execKey, _ := verdictScopeFixture(t)
			response := scopeReadFor(t, s, service, execKey, "verdict-tuple-nonce", tc.session, tc.agent, "client-session-exec-aaaa")
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("scope read failed: %+v", response.Error)
			}
			raw, err := json.Marshal(response.Result)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"final_verdict_kind":"ok"`) || len(response.Omissions) != 0 {
				t.Fatalf("only the exact recorded agent+session tuple may be redacted: result=%s omissions=%+v", raw, response.Omissions)
			}
		})
	}
}

func TestVerdictReadScopeDoesNotInferMissingInstanceIdentity(t *testing.T) {
	s, service, execKey, _ := verdictScopeFixture(t)
	// Isolate the read owner from the execution writer: the selection event
	// remains, but no current executing tuple is recorded on the instance.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET execution_actor_ref=NULL WHERE work_id='work-1'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	response := scopeReadFor(t, s, service, execKey, "verdict-current-identity", "session-exec-aaaa", "agent-exec", "client-session-exec-aaaa")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("scope read failed: %+v", response.Error)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"final_verdict_kind":"ok"`) || len(response.Omissions) != 0 {
		t.Fatalf("scope read inferred an identity outside the current instance: result=%s omissions=%+v", raw, response.Omissions)
	}
}
