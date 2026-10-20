package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestInvokeContinuityLimitCorrection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	invoke := func(input map[string]any) Envelope {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_trace", Operation: "continuity", Input: raw}, env))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := response.Encode(); err != nil {
			t.Fatalf("continuity response cannot serialize: %v", err)
		}
		return response
	}
	snapshot := func() [2]int64 {
		t.Helper()
		state := [2]int64{workVersion(t, s, "work-1")}
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&state[1]); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	for _, field := range []string{"limit", "page.limit"} {
		for _, limit := range []int{21, 100} {
			t.Run(fmt.Sprintf("%s/%d", field, limit), func(t *testing.T) {
				input := map[string]any{"work_id": "work-1"}
				bounds := input
				if field == "page.limit" {
					bounds = map[string]any{"cursor": nil}
					input["page"] = bounds
				}
				bounds["limit"] = limit
				refused := invoke(input)
				assertInputCorrection(t, refused, nil, field)
				if !strings.Contains(refused.Error.Message, "20") {
					t.Fatalf("refusal does not name the page bound: %+v", refused.Error)
				}
				if refused.RequestID != env.RequestID || refused.QueryID != "C19.Continuity" || refused.Tool != "concord_work_trace" || refused.Operation != "continuity" {
					t.Fatalf("refusal lost request identity: %+v", refused)
				}
				if after := snapshot(); after != before {
					t.Fatalf("refusal mutated work version/events: %v -> %v", before, after)
				}
				bounds["limit"] = 20
				if corrected := invoke(input); corrected.Outcome != OutcomeOK {
					t.Fatalf("correcting only %s under unchanged authority failed: %+v", field, corrected.Error)
				}
			})
		}
	}
	if defaulted := invoke(map[string]any{"work_id": "work-1"}); defaulted.Outcome != OutcomeOK {
		t.Fatalf("default continuity page failed: %+v", defaulted.Error)
	}
	if after := snapshot(); after != before {
		t.Fatalf("continuity reads mutated work version/events: %v -> %v", before, after)
	}
}
