package agent

import (
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
// a correction admission consumes, and none of the projection's internal
// fields the closed result schema refuses.
func TestTraceHistoryPageCarriesPublishedWorkflowRead(t *testing.T) {
	t.Parallel()
	workflow := store.WorkflowReadProjection{
		WorkID: "work-1", State: "completed", CurrentStep: "acceptance",
		Definition: store.WorkflowReadDefinition{Ref: "workflow.task", Version: 1, Digest: "sha256:" + strings.Repeat("a", 64)},
		DeliveryAssertion: &store.WorkflowReadDeliveryAssertion{
			EventID: "assertion-1", Seq: 7, TargetPayloadVersion: 2,
			Artifact: "file:internal/store/impl.go", State: "asserted",
			ActorRef: "actor:owner", AssertedAt: "2026-09-24T00:00:00Z",
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
				TargetPayloadVersion int `json:"target_payload_version"`
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
	if page.Workflow.DeliveryAssertion.Correction.EvidenceSource != store.DeliveryEvidenceSourceCoordinatorAsserted {
		t.Fatalf("correction provenance = %q, want coordinator_asserted", page.Workflow.DeliveryAssertion.Correction.EvidenceSource)
	}
	var shapedWorkflow map[string]any
	if err := json.Unmarshal(envelope.Result, &shapedWorkflow); err != nil {
		t.Fatal(err)
	}
	workflowShape := shapedWorkflow["workflow"].(map[string]any)
	for _, internal := range workflowReadInternalFields {
		if _, present := workflowShape[internal]; present {
			t.Fatalf("published page leaks the internal field %q: %s", internal, envelope.Result)
		}
	}
}
