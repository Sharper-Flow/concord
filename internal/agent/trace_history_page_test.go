package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
		Events: []store.TimelineEvent{
			{EventID: "evt-created", Seq: 1, Kind: "work.created", OccurredAt: "2026-08-24T00:00:00Z", EvidenceRefs: nil},
			{EventID: "evt-declined", Seq: 2, Kind: "work.transitioned", Actor: "actor:operator", OccurredAt: "2026-08-24T00:00:01Z", Reason: "declined by operator", EvidenceRefs: []string{"evidence-1"}},
		},
	}
	envelope, err := (runtime{}).q7(NewBase("reasonless", "concord_work_trace", "history"), q)
	if err != nil {
		t.Fatalf("history page refused a legitimate reasonless page: %v", err)
	}
	if envelope.Outcome != OutcomeOK {
		t.Fatalf("outcome = %q, want ok", envelope.Outcome)
	}
	var page struct {
		Events []map[string]any `json:"events"`
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
	if !strings.Contains(string(envelope.Result), `"evidence"`) {
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
				EvidenceSource: store.DeliveryEvidenceSourceCoordinatorAsserted, ApprovalRef: "approval-1", CorrectedAt: "2026-09-25T00:00:00Z",
			},
		},
		CandidateIDs:         []string{},
		Conditions:           []store.WorkflowReadCondition{},
		UnresolvedConditions: []string{},
		UnreadableConditions: []string{},
		BlockingConditions:   []string{},
		ImpactNotices:        []store.WorkflowReadNotice{},
		CompletionWarnings:   []string{},
		ChangesProductTruth:  true,
		OverdueAwaits:        []string{"cond-1"},
		AwaitHealth:          []store.WorkflowReadCondition{{ID: "cond-1"}},
	}
	envelope, err := (runtime{}).q7(NewBase("workflow-page", "concord_work_trace", "history"), store.Q7Result{
		ResultMeta: store.ResultMeta{QueryID: "q7", ContractVersion: "1.0", ResolvedScope: store.ResolvedScope{ProductID: "product-1"}, Authority: "authoritative"},
		Events:     []store.TimelineEvent{},
		Workflow:   &workflow,
	})
	if err != nil {
		t.Fatalf("history page refused a workflow projection: %v", err)
	}
	if envelope.Outcome != OutcomeOK {
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
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "product_read"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for _, actionID := range []string{"record_proposal", "record_alignment", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, actionID, version, "history-selector-"+actionID)
		version = workOneVersion(t, s)
	}
	if approved, _ := approvedWorkflowContract(t, s, service, grant, privateKey, env, version, workflowContractFieldsFixture(), "history-selector-approve"); approved.Outcome != OutcomeOK {
		t.Fatalf("approve_contract = %+v", approved.Error)
	}

	history := func(contractVersion int) Envelope {
		raw, err := json.Marshal(map[string]any{"work_id": "work-1", "event_kinds": []string{store.WorkflowContractApproved}, "contract_version": contractVersion})
		if err != nil {
			t.Fatal(err)
		}
		return dispatchRead(t, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: raw}, env)
	}
	selected := history(1)
	if selected.Outcome != OutcomeOK {
		t.Fatalf("history contract_version 1 = %+v", selected.Error)
	}
	var page struct {
		Events []struct {
			Kind            string `json:"kind"`
			ContractVersion int    `json:"contract_version"`
		} `json:"events"`
		Workflow struct {
			Contract struct {
				Version int `json:"version"`
			} `json:"contract"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(selected.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.Workflow.Contract.Version != 1 || len(page.Events) != 1 || page.Events[0].ContractVersion != 1 {
		t.Fatalf("selected history = %s", selected.Result)
	}
	refused := history(9)
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "unknown_scope" {
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
		if err := ValidateOperationPayload("concord_work_trace", "history", []byte(valid), false); err != nil {
			t.Fatalf("valid history input refused: %s: %v", valid, err)
		}
	}
	for _, invalid := range []string{`{"work_id":"work-1","contract_version":0}`, `{"work_id":"work-1","contract_version":-1}`, `{"work_id":"work-1","contract_version":2147483648}`, `{"work_id":"work-1","contract_version":"1"}`, `{"work_id":"work-1","contract_version":null}`} {
		if err := ValidateOperationPayload("concord_work_trace", "history", []byte(invalid), false); err == nil {
			t.Fatalf("history input accepted: %s", invalid)
		}
	}
}
