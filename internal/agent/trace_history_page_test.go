package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// An ordinary lifecycle event carries no reason and may carry no actor: both
// are optional in work_event_page with a minimum length. The history page
// must omit the keys, not emit them empty — a present-but-empty optional
// field fails the generated result schema and refuses the whole page
// (issue #383).
func TestTraceHistoryPageAcceptsReasonlessEvents(t *testing.T) {
	t.Parallel()
	q := store.Q7Result{
		ResultMeta: store.ResultMeta{QueryID: "q7", ContractVersion: "1.0", ResolvedScope: store.ResolvedScope{ProductID: "product-1"}, Authority: "authoritative"},
		Entries: []store.HistoryEntry{
			{EntryID: "lifecycle:created", EntryKind: "lifecycle", Position: store.HistoryPosition{EffectSeq: 1, Basis: "effect"}, Version: 1, VersionWorkID: "created", Lifecycle: "needed", EvidenceRefs: []string{}},
			{EntryID: "lifecycle:declined", EntryKind: "lifecycle", Position: store.HistoryPosition{EffectSeq: 2, Basis: "effect"}, Version: 2, VersionWorkID: "declined", Lifecycle: "cancelled", Actor: "actor:operator", Reason: "declined by operator", EvidenceRefs: []string{"evidence-1"}},
		},
	}
	envelope, err := (runtime{}).q7(agentcontract.NewBase("reasonless", "concord_work_trace", "history"), q)
	if err != nil {
		t.Fatalf("history page refused a legitimate reasonless page: %v", err)
	}
	if envelope.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("outcome = %q, want ok", envelope.Outcome)
	}
	var page struct {
		Events []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(envelope.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(page.Events))
	}
	created := page.Events[0]
	if _, present := created["reason"]; present {
		t.Fatalf("reasonless event carries a reason key: %v", created)
	}
	if _, present := created["actor"]; present {
		t.Fatalf("actorless event carries an actor key: %v", created)
	}
	declined := page.Events[1]
	if declined["reason"] != "declined by operator" || declined["actor"] != "actor:operator" {
		t.Fatalf("reasoned event lost its optional fields: %v", declined)
	}
	if !strings.Contains(string(envelope.Result), `"evidence_refs"`) {
		t.Fatal("evidence array missing from the rendered page")
	}
	var whole map[string]any
	if err := json.Unmarshal(envelope.Result, &whole); err != nil {
		t.Fatal(err)
	}
	if _, present := whole["workflow"]; present {
		t.Fatalf("page without a workflow projection carries a workflow key: %v", whole["workflow"])
	}
}

func TestTraceHistoryPageAcceptsStoredProducerReferences(t *testing.T) {
	t.Parallel()
	for _, producer := range []string{"principal/owner", "ses_1a2b-msg_3c4d", "request with spaces", "é"} {
		t.Run(producer, func(t *testing.T) {
			q := store.Q7Result{
				ResultMeta: store.ResultMeta{QueryID: "q7", ContractVersion: "1.0", ResolvedScope: store.ResolvedScope{ProductID: "product-1"}, Authority: "authoritative"},
				Entries: []store.HistoryEntry{{
					EntryID: "evidence_binding:bind-1", EntryKind: "evidence_binding", Position: store.HistoryPosition{EffectSeq: 3, Basis: "effect"},
					Version: 3, VersionWorkID: "work-1", Actor: "actor:owner", EvidenceRefs: []string{"commit:abc"}, BindingID: "bind-1", EvidenceKind: "commit", ImmutableSubjectRef: "commit:abc",
					ProducerID: producer, ProducerRunRef: producer, ProducerWatermark: producer, ObservedAt: "2026-10-24T00:00:00Z",
				}},
			}
			envelope, err := (runtime{}).q7(agentcontract.NewBase("producer", "concord_work_trace", "history"), q)
			if err != nil || envelope.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("history page refused stored producer %q: %v %+v", producer, err, envelope.Error)
			}
		})
	}
}

// A history page for work with a workflow instance carries the published
// workflow_read subset: the delivery assertion with the target_payload_version
// a correction admission consumes and the effective artifact the correction
// overlay selects, and no projection field the contract does not declare.
func TestTraceHistoryPageCarriesPublishedWorkflowRead(t *testing.T) {
	t.Parallel()
	workflow := store.WorkflowReadProjection{
		WorkID: "work-1", State: "completed", CurrentStep: "acceptance",
		Definition: store.WorkflowReadDefinition{Ref: "workflow.task", Digest: "sha256:" + strings.Repeat("a", 64)},
		DeliveryAssertion: &store.WorkflowReadDeliveryAssertion{
			EventID: "assertion-1", Seq: 7, TargetPayloadVersion: 2,
			Artifact: "file:internal/store/impl.go", State: "asserted",
			ActorRef: "actor:owner", AssertedAt: "2026-09-24T00:00:00Z",
			EffectiveArtifact: "https://github.com/Sharper-Flow/concord/pull/1339",
			Correction: &store.WorkflowReadDeliveryCorrection{
				EventID: "correction-1", Reason: "asserted repository paths", Artifact: "https://github.com/Sharper-Flow/concord/pull/1339",
				EvidenceSource: store.DeliveryEvidenceSourceCoordinatorAsserted, CorrectedAt: "2026-09-25T00:00:00Z",
			},
		},
		CandidateIDs:        []string{},
		CompletionWarnings:  []string{},
		ChangesProductTruth: true,
	}
	envelope, err := (runtime{}).q7(agentcontract.NewBase("workflow-page", "concord_work_trace", "history"), store.Q7Result{
		ResultMeta: store.ResultMeta{QueryID: "q7", ContractVersion: "1.0", ResolvedScope: store.ResolvedScope{ProductID: "product-1"}, Authority: "authoritative"},
		Entries:    []store.HistoryEntry{},
		Workflow:   &workflow,
	})
	if err != nil {
		t.Fatalf("history page refused a workflow projection: %v", err)
	}
	if envelope.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("outcome = %q, want ok", envelope.Outcome)
	}
	var page struct {
		Workflow *struct {
			DeliveryAssertion *struct {
				TargetPayloadVersion int    `json:"target_payload_version"`
				EffectiveArtifact    string `json:"effective_artifact"`
				Correction           *struct {
					Artifact       string `json:"artifact"`
					EvidenceSource string `json:"evidence_source"`
				} `json:"correction"`
			} `json:"delivery_assertion"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(envelope.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.Workflow == nil || page.Workflow.DeliveryAssertion == nil || page.Workflow.DeliveryAssertion.Correction == nil {
		t.Fatalf("page carries no corrected delivery assertion: %s", envelope.Result)
	}
	if page.Workflow.DeliveryAssertion.TargetPayloadVersion != 2 {
		t.Fatalf("target_payload_version = %d, want 2", page.Workflow.DeliveryAssertion.TargetPayloadVersion)
	}
	if page.Workflow.DeliveryAssertion.EffectiveArtifact != "https://github.com/Sharper-Flow/concord/pull/1339" {
		t.Fatalf("effective_artifact = %q, want the corrected merge evidence", page.Workflow.DeliveryAssertion.EffectiveArtifact)
	}
	if page.Workflow.DeliveryAssertion.Correction.EvidenceSource != store.DeliveryEvidenceSourceCoordinatorAsserted {
		t.Fatalf("correction provenance = %q, want coordinator_asserted", page.Workflow.DeliveryAssertion.Correction.EvidenceSource)
	}
	var shapedWorkflow map[string]any
	if err := json.Unmarshal(envelope.Result, &shapedWorkflow); err != nil {
		t.Fatal(err)
	}
	workflowShape := shapedWorkflow["workflow"].(map[string]any)
	// Every published key is a contract name: the field set the page emits is
	// derived from $defs/workflow_read/properties, not from a copied list.
	published := workflowReadPublishedFields()
	for field := range workflowShape {
		if _, ok := published[field]; !ok {
			t.Fatalf("published page emits %q outside the derived workflow_read field set: %s", field, envelope.Result)
		}
	}
}

// TestTraceHistoryBindsTheContractVersionSelector proves the history input
// reaches the store selector: an approved version answers that contract,
// a version the work never had refuses unknown_scope, and contract events
// render their versions.
func TestTraceHistoryBindsTheContractVersionSelector(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "product_read"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for _, actionID := range []string{"record_proposal", "record_alignment"} {
		invokeWorkflowIssue31Action(t, s, service, env, actionID, version, "history-selector-"+actionID)
		version = workOneVersion(t, s)
	}
	if approved, _ := approvedWorkflowContract(t, s, service, env, version, workflowContractFieldsForSeededRegistry(t, s), "history-selector-approve"); approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract = %+v", approved.Error)
	}

	history := func(contractVersion int) agentcontract.Envelope {
		raw, err := json.Marshal(map[string]any{"work_id": "work-1", "record_kinds": []string{"lifecycle"}, "contract_version": contractVersion})
		if err != nil {
			t.Fatal(err)
		}
		return dispatchRead(t, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: raw}, env)
	}
	selected := history(1)
	if selected.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("history contract_version 1 = %+v", selected.Error)
	}
	var page struct {
		Entries  []store.HistoryEntry `json:"entries"`
		Workflow struct {
			Contract struct {
				Version int `json:"version"`
			} `json:"contract"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(selected.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.Workflow.Contract.Version != 1 || len(page.Entries) != 1 || page.Entries[0].EntryKind != "lifecycle" {
		t.Fatalf("selected history = %s", selected.Result)
	}
	refused := history(9)
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "unknown_scope" {
		t.Fatalf("history contract_version 9 = outcome %s error %+v, want unknown_scope", refused.Outcome, refused.Error)
	}
}

// TestTraceHistoryContractVersionInputBounds proves the published input
// refuses a selector outside 1..2147483647 before the handler runs. The
// store reads a zero selector as the active contract, so a zero that
// reached the handler would answer the active contract instead of refusing.
func TestTraceHistoryContractVersionInputBounds(t *testing.T) {
	t.Parallel()
	for _, valid := range []string{`{"work_id":"work-1"}`, `{"work_id":"work-1","contract_version":1}`, `{"work_id":"work-1","contract_version":2147483647}`} {
		if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(valid), false); err != nil {
			t.Fatalf("valid history input refused: %s: %v", valid, err)
		}
	}
	for _, invalid := range []string{`{"work_id":"work-1","contract_version":0}`, `{"work_id":"work-1","contract_version":-1}`, `{"work_id":"work-1","contract_version":2147483648}`, `{"work_id":"work-1","contract_version":"1"}`, `{"work_id":"work-1","contract_version":null}`} {
		if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(invalid), false); err == nil {
			t.Fatalf("history input accepted: %s", invalid)
		}
	}
}
