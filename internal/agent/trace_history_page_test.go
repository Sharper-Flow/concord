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
}
