package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkflowReadLargeFixedProjectionRefusesTypedBytesNotFieldSchema(t *testing.T) {
	t.Parallel()
	s, service, _, _, _ := workflowEngineFixture(t, "")
	const workID = "work-1"
	subjects := make([]string, 100)
	for i := range subjects {
		prefix := fmt.Sprintf("subject:%03d:", i)
		subjects[i] = prefix + strings.Repeat("x", 128-len(prefix))
	}
	payload, err := json.Marshal(map[string]any{"kind": "absent", "surface": "behavior:defect", "subjects": subjects, "distinguish_from": []string{"disabled"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecodeWorkflowPredicate(payload); err != nil {
		t.Fatalf("large predicate must satisfy production bounds: %v", err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_contract_predicates WHERE work_id=?`, workID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := tx.Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,?,?,'absent',?)`, workID, fmt.Sprintf("predicate:%d", i), i, string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	readProjection := func() *store.WorkflowReadProjection {
		t.Helper()
		out, err := s.QueryQ7(t.Context(), store.Q7Request{Work: workID})
		if err != nil || out.Workflow == nil || out.Workflow.Contract == nil {
			t.Fatalf("store workflow projection: %+v, %v", out, err)
		}
		return out.Workflow
	}
	before, err := json.Marshal(readProjection())
	if err != nil || len(before) <= agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("fixed projection bytes=%d err=%v, want above %d", len(before), err, agentcontract.MaxResultEnvelopeBytes)
	}
	grant, err := service.Authorize(t.Context(), Invocation{ClientRef: "client-session-exec-aaaa", PrincipalRef: "human-1", SessionRef: "session-exec-aaaa", AgentRef: "agent-exec", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "product_read", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(t.Context(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: json.RawMessage(`{"work_id":"work-1","limit":1}`)}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatalf("oversized fixed projection must refuse as a typed envelope, not a transport fault: %v", err)
	}
	if envelope.Outcome != agentcontract.OutcomeError || envelope.Error == nil || envelope.Error.Kind != "limit_exceeded" {
		t.Fatalf("outcome %s error %+v, want the typed byte refusal", envelope.Outcome, envelope.Error)
	}
	if envelope.Error.RecoveryAction.Kind != "reduce_limit" || !strings.Contains(envelope.Error.Message, "fixed projection") {
		t.Fatalf("fixed-projection recovery: %+v", envelope.Error)
	}
	details := envelope.Error.Details
	if details["reason"] != "result_envelope_bytes" || details["maximum_bytes"] != agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("byte refusal details: %+v", details)
	}
	serialized, ok := details["serialized_bytes"].(int)
	if !ok || serialized <= agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("serialized_bytes %v, want an observed size above %d", details["serialized_bytes"], agentcontract.MaxResultEnvelopeBytes)
	}
	if len(envelope.Result) != 0 {
		t.Fatalf("refused envelope still carries %d result bytes", len(envelope.Result))
	}
	after, err := json.Marshal(readProjection())
	if err != nil || string(after) != string(before) {
		t.Fatalf("refused read changed the durable producer: %v", err)
	}
}
