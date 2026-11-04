package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkListDispatchItemFullNativeRun(t *testing.T) {
	s, _, _, _, _ := workflowEngineFixture(t, "list native run")
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES (1);
		INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref)
		VALUES('work-1','run-1','start','started','event-1','client-1','actor-1','run:1',?,'evidence:1','digest','2026-08-20T12:00:00Z','2026-08-20T12:00:00Z','trusted_client_report','{}','policy','policy'); DELETE FROM write_guard`, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	for _, tc := range []struct {
		detail string
		ids    []string
		want   bool
	}{{"full", []string{"work-1"}, true}, {"summary", []string{"work-1"}, false}, {"full", nil, false}} {
		input, err := json.Marshal(map[string]any{"product_id": "product-1", "detail": tc.detail, "work_ids": tc.ids})
		if err != nil {
			t.Fatal(err)
		}
		response, err := r.read(context.Background(), agentcontract.NewBase("list-facts", r.Tool, r.Operation), input, "PM1.Q3")
		if err != nil || response.Error != nil {
			t.Fatalf("dispatch: %+v %v", response.Error, err)
		}
		if err := agentcontract.ValidateOperationPayload(r.Tool, r.Operation, response.Result, true); err != nil {
			t.Fatal(err)
		}
		var page struct {
			Items []struct {
				NativeRuns []struct {
					RunID string `json:"run_id"`
				} `json:"native_runs"`
			} `json:"items"`
		}
		if err := json.Unmarshal(response.Result, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("items: %+v", page.Items)
		}
		if got := len(page.Items[0].NativeRuns) != 0; got != tc.want {
			t.Fatalf("native runs present %v, want %v", got, tc.want)
		}
		if tc.want && page.Items[0].NativeRuns[0].RunID != "run-1" {
			t.Fatalf("native runs: %+v", page.Items[0].NativeRuns)
		}
	}
	intent, err := json.Marshal(map[string]any{"task": strings.Repeat("<", 8192), "value_statement": "value"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES (1); UPDATE work_items SET intent_json=? WHERE id='work-1'; DELETE FROM write_guard`, string(intent)); err != nil {
		t.Fatal(err)
	}
	items, fragments := readFullListDocuments(t, context.Background(), r, map[string]any{"product_id": "product-1", "detail": "full", "work_ids": []string{"work-1"}})
	if fragments < 2 || len(items) != 1 || len(items[0].NativeRuns) != 1 || items[0].NativeRuns[0].RunID != "run-1" {
		t.Fatalf("projection lost native run: fragments=%d items=%+v", fragments, items)
	}
}

func TestWorkListFactProjectionSchema(t *testing.T) {
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "list fact projection")
	r := runtime{Store: s, Tool: "concord_work_browse", Operation: "list", Envelope: CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-fixture"}}
	q, err := s.QueryQ3(ctx, store.Q3Request{Product: "product-1", WorkIDs: []string{"work-1"}, Detail: "full"})
	if err != nil || len(q.Items) != 1 {
		t.Fatalf("query: %v, %+v", err, q.Items)
	}
	q.Items[0].PendingProjectHandoff = &store.ProjectHandoff{HandoffID: "handoff-1", WorkID: "work-1", ContractVersion: 1, SourceProjectID: "project-1", TargetProjectID: "project-2", SourceSessionRef: "session-1", BoundedJob: "job", NextAction: "act", State: "recorded", RecordedAt: "2026-08-20T12:00:00Z"}
	q.Items[0].LawContext = &store.WorkflowLawContext{Laws: []store.WorkflowLawContextLaw{{Roles: []string{"mandated"}, LawID: "law-1"}}, Domains: []store.WorkflowLawContextDomain{}}
	q.Items[0].NativeRuns = []store.NativeRunReport{{RunID: "run-1", Phase: "start", Status: "started", EventID: "event-1", ReportingAuthorityRef: "client-1", ActorRef: "actor-1", NativeSubjectRef: "run:1", SubjectDigest: "digest", EvidenceRef: "evidence:1", EvidenceDigest: "digest", AssertedAt: "2026-08-20T12:00:00Z", RecordedAt: "2026-08-20T12:00:00Z", VerificationState: "unverified", Unverified: true}}
	base := agentcontract.NewBase("fact-projection", r.Tool, r.Operation)
	response, err := r.q3(base, q)
	if err != nil {
		t.Fatal(err)
	}
	if err := agentcontract.ValidateOperationPayload(r.Tool, r.Operation, response.Result, true); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []workSummary `json:"items"`
	}
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].PendingProjectHandoff == nil || page.Items[0].LawContext == nil || len(page.Items[0].NativeRuns) != 1 {
		t.Fatalf("facts missing: %+v", page.Items)
	}
}

func listResumeFacts(t *testing.T, s *store.Store, service *Service, env CallEnvelope) workSummary {
	t.Helper()
	input := json.RawMessage(`{"product_id":"product-1","work_ids":["work-1"],"detail":"full"}`)
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "list", Input: input}, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("list dispatch: error=%v response=%+v", err, response.Error)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_browse", "list", response.Result, true); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []workSummary `json:"items"`
	}
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items: %+v", page.Items)
	}
	return page.Items[0]
}

func TestWorkListDispatchAddressedProjectHandoff(t *testing.T) {
	s, source, receiver, sourceEnv, receiverEnv, _ := handoffDispatchFixture(t)
	recorded := handoffInvoke(t, s, source, sourceEnv, "project_handoff_record", map[string]any{
		"idempotency_key": "list-record", "work_id": "work-1", "target_project_id": "project-2",
		"bounded_job": "verify the receiving repository", "next_action": "run the bounded job",
	})
	if recorded.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record: %+v", recorded.Error)
	}
	id := resultField(t, recorded, "handoff_id")
	if id == "" {
		t.Fatal("record returned no handoff ID")
	}
	read := func(service *Service, env CallEnvelope) *store.ProjectHandoff {
		t.Helper()
		item := listResumeFacts(t, s, service, env)
		snapshot, err := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: "work-1", ProjectID: env.AmbientProjectID})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(item.PendingProjectHandoff, snapshot.PendingProjectHandoff) {
			t.Fatalf("list handoff=%+v continuity handoff=%+v", item.PendingProjectHandoff, snapshot.PendingProjectHandoff)
		}
		return item.PendingProjectHandoff
	}
	for _, got := range []*store.ProjectHandoff{read(receiver, receiverEnv), read(source, sourceEnv)} {
		if got == nil || got.HandoffID != id || got.State != store.ProjectHandoffRecorded {
			t.Fatalf("addressed or fallback handoff: %+v", got)
		}
	}
	consumed := handoffInvoke(t, s, receiver, receiverEnv, "project_handoff_consume", map[string]any{
		"idempotency_key": "list-consume", "work_id": "work-1", "handoff_id": id,
	})
	if consumed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("consume: %+v", consumed.Error)
	}
	if got := read(receiver, receiverEnv); got == nil || got.HandoffID != id || got.State != store.ProjectHandoffConsumed {
		t.Fatalf("addressed consumed handoff: %+v", got)
	}
	if got := read(source, sourceEnv); got != nil {
		t.Fatalf("consumed fallback: %+v", got)
	}
}

func TestWorkListDispatchApprovedLawContext(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_define", "work_transition"})
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	if got := listResumeFacts(t, s, service, env).LawContext; got != nil {
		t.Fatalf("unbound law context: %+v", got)
	}
	seedCurrentWorkflowDomainFixture(t, s)
	ctx = agentWorkflowSourceProof(t, ctx, s, "work-1")
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Actor: actor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	advanceCapturedWorkToContract(t, s, service, env, "work-1", "list-law")
	fields := workflowContractFieldsForSeededRegistry(t, s)
	fields["spec_mandate"] = []string{"CD-0001"}
	approved := invokeWorkflowBoundary(t, s, service, env, map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "approve_contract", "fields": fields, "idempotency_key": "list-law-approve",
	})
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve: %+v", approved.Error)
	}
	law := listResumeFacts(t, s, service, env).LawContext
	if law == nil {
		t.Fatal("approved law context is absent")
	}
	for _, entry := range law.Laws {
		if entry.LawID == "CD-0001" {
			return
		}
	}
	t.Fatalf("approved law missing from context: %+v", law)
}
