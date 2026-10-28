package store

import (
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/workflowproof"
)

// CD-0243 D3/D7 remove ordinary consent, while the typed hold remains required.
func TestOrdinaryOutsideRepairAttributesAgent(t *testing.T) {
	s := openTemp(t)
	const work = "ordinary-outside-repair"
	seedOutsideRepairTestWork(t, s, work)
	request := OutsideRepairRequest{Actor: WorkflowActor{PrincipalRef: "principal/ordinary", ClientRef: "client/ordinary", AgentRef: "agent/ordinary", SessionRef: "session/ordinary", ActorClass: ActorAgent}, WorkID: work, Reason: "bounded outside repair", EventID: "ordinary-outside-hold", ExpectedVersion: 3, OccurredAt: time.Unix(10, 0).UTC()}
	if err := outsideRepairApply(t, s, request, WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}); err != nil {
		t.Fatalf("ordinary hold: %+v", err)
	}
	request.EventID, request.ExpectedVersion = "ordinary-outside-resume", 5
	if err := outsideRepairApply(t, s, request, WorkflowOutsideRepairResumed, workflowproof.Evidence{}); err != nil {
		t.Fatalf("ordinary resume: %+v", err)
	}
}
