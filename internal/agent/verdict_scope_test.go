package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0023: the recorded acceptance verdict of a terminal work item is
// readable by every authority except the actor recorded as executing it.

func verdictScopeFixture(t *testing.T) (*store.Store, *Service, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "verdict-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Verdict Scope","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "verdict-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Verdict Project"}`)},
		{EventID: "verdict-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"verdict fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "verdict-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"bug","title":"Verdict Scope Work","priority":1}`)},
		{EventID: "verdict-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}

	service := NewService(s.DB())
	service.Now = fixedTime
	service.ProjectResolver = func(context.Context, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1"}, nil
	}

	clients := []string{"client-session-exec-aaaa", "client-session-other-bbbb"}
	keys := []ed25519.PrivateKey{}
	for _, client := range clients {
		publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
		policy := TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}
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
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: execActor, Now: fixedTime()}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Drive the real engine to the external repair step: record, record,
	// then start_repair, whose action_started fold records the executing
	// actor on the instance. No operator approval gates this path.
	execRef := store.DeriveWorkflowActorRef("human-1", "client-session-exec-aaaa", "agent-exec", "session-exec-aaaa")
	engineAction := func(version int64, actionID string, payload map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		request := store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: actionID, Payload: raw,
			Actor: execActor, AcceptedInputsDigest: "sha256:" + strings.Repeat("2", 64), IdempotencyIdentity: "verdict-seed-" + actionID,
			OperationID: "verdict-seed-op-" + actionID, PrincipalRef: "human-1", Tool: "concord-test", IdempotencyKey: "verdict-seed-key-" + actionID,
			RequestID: "verdict-seed-request-" + actionID, AcceptedScope: `{"project":"project-1"}`, ContractVersion: "2.4.0", Now: fixedTime(),
		}
		preflight := store.WorkflowActionPreflightRequest{WorkID: "work-1", ExpectedVersion: version, ActionID: actionID, Payload: raw, Actor: execActor}
		if err := store.AuthorizeWorkflowActionAtBoundaryTx(ctx, s, store.BuiltinWorkflowRegistry(), preflight, nil, fixedTime(), nil, func(tx *sql.Tx) error {
			_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), request)
			return err
		}); err != nil {
			t.Fatalf("%s at v%d: %v", actionID, version, err)
		}
	}
	record := func(actionID string) {
		engineAction(map[string]int64{"record_reproduction": 4, "record_root_cause": 5}[actionID], actionID, map[string]any{
			"payload": map[string]any{"work": "work-1", "outcome": nil}, "title": "Verdict Scope Work", "value_statement": "the verdict scope fixture value statement",
		})
	}
	record("record_reproduction")
	record("record_root_cause")
	engineAction(6, "start_repair", map[string]any{"payload": map[string]any{"work": "work-1", "outcome": nil}})
	seedVerdictEvent(t, s.DB(), execRef)
	return s, service, keys[0], keys[1]
}

// seedVerdictEvent records the terminal verdict event. domain_events is
// append-visible to this test; the verdict payload shape is the engine's
// completed-payload contract.
func seedVerdictEvent(t *testing.T, db *sql.DB, execActorRef string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"work_id": "work-1", "terminal_state": "completed", "final_verdict_kind": "ok",
		"verdict_actor_ref": execActorRef, "premise_confirmed": true, "evidence_count": 1,
		"changed_refs_digest": "sha256:" + strings.Repeat("1", 64), "impact_verdict": "non-breaking",
	})
	if _, err := db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('verdict-seed-completed','workflow.completed','work_item','work-1',?, '2026-08-14T00:01:00Z', 1, ?)`,
		execActorRef, string(payload)); err != nil {
		t.Fatal(err)
	}
}

func scopeReadFor(t *testing.T, s *store.Store, service *Service, privateKey ed25519.PrivateKey, nonce, session, agent, client string) Envelope {
	t.Helper()
	ctx := context.Background()
	req := grantRequest(privateKey, nonce)
	req.Assertion.SessionRef = session
	req.Assertion.AgentRef = agent
	req.Assertion.ClientRef = client
	req.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(req.Assertion))
	grant, err := service.IssueGrant(ctx, req)
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
	s, service, execKey, _ := verdictScopeFixture(t)

	response := scopeReadFor(t, s, service, execKey, "verdict-exec-nonce-0001", "session-exec-aaaa", "agent-exec", "client-session-exec-aaaa")
	if response.Outcome != OutcomeOK {
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
	s, service, _, otherKey := verdictScopeFixture(t)

	response := scopeReadFor(t, s, service, otherKey, "verdict-distinct-nonce-2", "session-other-bbbb", "agent-other", "client-session-other-bbbb")
	if response.Outcome != OutcomeOK {
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
