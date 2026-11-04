package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestReadWorkPinReportsEveryCoordinatorSessionDrivingTheItem pins the gap a
// live incident exposed: two coordinator sessions drove one work item, each
// recorded workflow state, and neither pin ever named the other. Concord
// admits the second coordinator by design: session preparation records no
// identity that could make one coordinator exclusive (CD-0061 D4). Admission
// is not the defect. Silence is: a coordinator reads a pin on every response
// and cannot learn that another session is driving the same item.
func TestReadWorkPinReportsEveryCoordinatorSessionDrivingTheItem(t *testing.T) {
	s := openTemp(t)
	const workID = "workpin-codriver"
	seedWork(t, s, workID)
	first := WorkflowActor{PrincipalRef: "principal:continuity", ClientRef: "client:continuity", AgentRef: "agent:continuity", SessionRef: "session:continuity", ActorClass: ActorAgent}
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		return initializeWorkflowRawTx(t.Context(), tx.tx, WorkflowInitializationRequest{WorkID: workID, Actor: first, Now: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	version, err := continuityAction(t, s, workID, version, "record_intake", "codriver-intake", map[string]any{"outcome": "proceed", "open_questions_found": false}, first)
	if err != nil {
		t.Fatal(err)
	}

	second := WorkflowActor{
		PrincipalRef: first.PrincipalRef,
		ClientRef:    first.ClientRef,
		AgentRef:     first.AgentRef,
		SessionRef:   "session:second-coordinator",
		ActorClass:   ActorAgent,
	}
	version, err = continuityAction(t, s, workID, version, "pick_type", "codriver-pick", map[string]any{"type": "change", "reason": "bounded change"}, first)
	if err != nil {
		t.Fatal(err)
	}
	plan := map[string]any{"outcomes": "working outcome", "approach": "bounded change", "decisions": []map[string]any{{"id": "decision:one", "question": "which?", "choice": "one", "rationale": "fits", "rejected": []string{}}}, "touches": []string{"file:one"}, "merge_split": "one item", "risks": "bounded risk"}
	version, err = continuityAction(t, s, workID, version, "record_plan", "codriver-first-plan", plan, first)
	if err != nil {
		t.Fatal(err)
	}
	plan["reason"] = "second coordinator revision"
	version, err = continuityAction(t, s, workID, version, "record_plan", "codriver-plan", plan, second)
	if err != nil {
		t.Fatalf("second coordinator was refused: %v, want admission per CD-0061 D4", err)
	}

	pin, err := ReadWorkPin(context.Background(), s, "workpin-codriver")
	if err != nil {
		t.Fatal(err)
	}
	if len(pin.DrivingSessions) != 2 || pin.DrivingSessions[0].SessionRef != first.SessionRef || pin.DrivingSessions[1].SessionRef != second.SessionRef {
		t.Fatalf("driving sessions=%v, want the two coordinator sessions in order", pin.DrivingSessions)
	}
	if pin.Version != version || pin.DrivingSessions[0].LastActionID != "record_plan" || pin.DrivingSessions[0].LastActedAt != "2026-08-11T00:00:00Z" || pin.DrivingSessions[1].LastActionID != "record_plan" || pin.DrivingSessions[1].LastActedAt != "2026-08-11T00:00:00Z" {
		t.Fatalf("driving sessions=%+v, want each latest action and time", pin.DrivingSessions)
	}
	encoded, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{first.SessionRef, second.SessionRef} {
		if !strings.Contains(string(encoded), session) {
			t.Fatalf("pin does not name coordinator session %q; a coordinator cannot see it shares the item\npin=%s", session, encoded)
		}
	}
}
