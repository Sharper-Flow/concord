package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

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
	if err != nil || envelope.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("history page refused: %v %+v", err, envelope.Error)
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
	created, declined := page.Events[0], page.Events[1]
	if _, present := created["reason"]; present {
		t.Fatalf("reasonless event carries a reason: %v", created)
	}
	if _, present := created["actor"]; present {
		t.Fatalf("actorless event carries an actor: %v", created)
	}
	if declined["reason"] != "declined by operator" || declined["actor"] != "actor:operator" {
		t.Fatalf("optional fields lost: %v", declined)
	}
	if !strings.Contains(string(envelope.Result), `"evidence_refs"`) {
		t.Fatal("evidence array missing")
	}
}

func TestTraceHistoryPageAcceptsStoredProducerReferences(t *testing.T) {
	t.Parallel()
	for _, producer := range []string{"principal/owner", "ses_1a2b-msg_3c4d", "request with spaces", "é"} {
		t.Run(producer, func(t *testing.T) {
			q := store.Q7Result{
				ResultMeta: store.ResultMeta{QueryID: "q7", ContractVersion: "1.0", ResolvedScope: store.ResolvedScope{ProductID: "product-1"}, Authority: "authoritative"},
				Entries:    []store.HistoryEntry{{EntryID: "evidence_binding:bind-1", EntryKind: "evidence_binding", Position: store.HistoryPosition{EffectSeq: 3, Basis: "effect"}, Version: 3, VersionWorkID: "work-1", Actor: "actor:owner", EvidenceRefs: []string{"commit:abc"}, BindingID: "bind-1", EvidenceKind: "commit", ImmutableSubjectRef: "commit:abc", ProducerID: producer, ProducerRunRef: producer, ProducerWatermark: producer, ObservedAt: "2026-10-24T00:00:00Z"}},
			}
			envelope, err := (runtime{}).q7(agentcontract.NewBase("producer", "concord_work_trace", "history"), q)
			if err != nil || envelope.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("history refused %q: %v %+v", producer, err, envelope.Error)
			}
		})
	}
}

func TestHistoryRejectsRetiredContractVersion(t *testing.T) {
	if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(`{"work_id":"work-1"}`), false); err != nil {
		t.Fatalf("ordinary history input refused: %v", err)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(`{"work_id":"work-1","contract_version":1}`), false); err == nil {
		t.Fatal("history accepted the retired contract selector")
	}
}

func TestWorkflowPlanRead(t *testing.T) {
	s, _, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "product_read"})
	seedAgentWorkflowAt(t, s, grant, store.WorkflowStepPlanFix, "implement")
	resume, err := store.ReadWorkflowResume(t.Context(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if resume.Type != store.WorkflowStepPlanFix || len(resume.NextSteps) != 1 || resume.NextSteps[0] != "implement" {
		t.Fatalf("workflow resume = %+v", resume)
	}
	for _, step := range resume.Steps {
		if step.ID == "implement" && step.Reviewed {
			return
		}
	}
	t.Fatal("implement step is not reviewed")
}
