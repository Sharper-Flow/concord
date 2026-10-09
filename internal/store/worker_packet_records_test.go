package store

import (
	"strings"
	"testing"
)

// recordsDispatchPacket builds a truthful implement-lane packet for the
// fixture and applies edit to its inputs before dispatch.
func recordsDispatchPacket(t *testing.T, f workContextFixture, attemptID string, edit func(inputs map[string]any)) map[string]any {
	t.Helper()
	packet := workContextDispatchPacket(t, f, attemptID, nil)
	if edit != nil {
		edit(packet["inputs"].(map[string]any))
	}
	return packet
}

// A packet carrying the current recorded members admits. The fixture holds a
// law context, so the admission compared it.
func TestDispatchAdmitsPacketCarryingCurrentRecords(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "records-admit")
	defer fixture.store.Close()
	packet := recordsDispatchPacket(t, fixture, "attempt-records-admit", nil)
	inputs := packet["inputs"].(map[string]any)
	if _, ok := inputs["law_context"]; !ok {
		t.Fatalf("fixture packet carries no law_context: %v", inputs)
	}
	if err := dispatchWorkContextAttempt(t, fixture, packet); err != nil {
		t.Fatalf("dispatch refused a packet carrying the current records: %v", err)
	}
	if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 1 {
		t.Fatalf("dispatch_worker started events = %d, want 1", got)
	}
}

func TestDispatchRefusesPacketRecordMismatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(t *testing.T, inputs map[string]any)
		want string
	}{
		{"missing law context", func(_ *testing.T, inputs map[string]any) { delete(inputs, "law_context") }, "worker packet does not carry the current inputs.law_context"},
		{"tampered law context", func(t *testing.T, inputs map[string]any) {
			law := *inputs["law_context"].(*WorkflowLawContext)
			law.RegistryPath = "/a/registry/path/the/core/never/resolved.json"
			inputs["law_context"] = law
		}, "worker packet inputs.law_context differs from the current record"},
		{"unbacked work record", func(_ *testing.T, inputs map[string]any) {
			inputs["work_record"] = map[string]any{"narrative": "a narrative the work item never recorded"}
		}, "worker packet carries inputs.work_record that no current record backs"},
		{"unknown work record member", func(_ *testing.T, inputs map[string]any) {
			inputs["work_record"] = map[string]any{"narrative": "x", "summary": "x"}
		}, "unknown property $.inputs.work_record.summary"},
		{"legacy prose context", func(_ *testing.T, inputs map[string]any) {
			inputs["context"] = "prose the core cannot hold to a record"
		}, "unknown property $.inputs.context"},
		{"unbacked design record", func(_ *testing.T, inputs map[string]any) {
			inputs["design_record"] = map[string]any{"work_version": 1, "approach": "an approach no record backs", "decisions": []any{map[string]any{"id": "decision:unbacked", "question": "q", "choice": "c", "rationale": "r", "rejected": []any{}}}, "touched_refs": []any{"path:unbacked"}, "recorded_at": "2026-01-01T00:00:00Z"}
		}, "worker packet carries inputs.design_record that no current record backs"},
		{"unbacked proposal", func(_ *testing.T, inputs map[string]any) {
			inputs["proposal_record"] = map[string]any{"problem": "a problem no record backs", "user_outcomes": []any{}, "constraints": []any{}}
		}, "worker packet carries inputs.proposal_record that no current record backs"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := seedWorkContextFixture(t, "records-refuse-"+string(rune('a'+index)))
			defer fixture.store.Close()
			packet := recordsDispatchPacket(t, fixture, "attempt-records-refuse", func(inputs map[string]any) { tc.edit(t, inputs) })
			err := dispatchWorkContextAttempt(t, fixture, packet)
			if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("dispatch error = %v, want %s containing %q", err, KindInvalidPayload, tc.want)
			}
			if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 0 {
				t.Fatalf("dispatch_worker started events = %d, want 0 on refusal", got)
			}
		})
	}
}

// The law context the dispatch admission resolves equals the one the pinned
// continuity serves, so a packet copied from the pin admits. A law context the
// contract does not bind refuses.
func TestDispatchRefusesLawContextTheContractDoesNotBind(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "records-law")
	defer fixture.store.Close()
	packet := recordsDispatchPacket(t, fixture, "attempt-records-law", func(inputs map[string]any) {
		inputs["law_context"] = map[string]any{"laws": []any{map[string]any{"roles": []any{"mandated"}, "law_id": "spec:unbound"}}, "domains": []any{}}
	})
	err := dispatchWorkContextAttempt(t, fixture, packet)
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "inputs.law_context") {
		t.Fatalf("dispatch error = %v, want %s refusing the unbound law context", err, KindInvalidPayload)
	}
}
