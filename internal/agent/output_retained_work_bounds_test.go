package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkListAdmitsRetainedWorkText(t *testing.T) {
	t.Parallel()
	for _, detail := range []string{"summary", "full"} {
		t.Run(detail, func(t *testing.T) { testWorkListRetainedText(t, detail) })
	}
}

func testWorkListRetainedText(t *testing.T, detail string) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, _ := researchSurfaceFixture(t)
	workID := strings.Repeat("w", 129)
	if detail == "full" {
		// Full detail also reads continuity, whose independent request guard
		// refuses work IDs above 128 bytes before it constructs a work pin.
		workID = "retained-work"
	}
	title := strings.Repeat("t", 257)
	value := strings.Repeat("v", 257)
	tag := strings.Repeat("g", 129)
	workflow := strings.Repeat("f", 129)
	payload, err := json.Marshal(map[string]any{
		"work_kind": "task", "title": title, "value_statement": value,
		"priority": 1, "tags": []string{tag}, "workflow_type_ref": workflow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{
		Events: []store.Event{
			{EventID: "retained-work-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID,
				Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: payload},
			{EventID: "retained-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID,
				Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1,
				Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		},
		ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): 0},
	}); err != nil {
		t.Fatalf("the retained event writer must admit the wider fields: %v", err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"project_ids": []string{"project-1"}, "detail": detail, "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Dispatch(ctx, s, service, InvokeRequest{
		Tool: "concord_work_browse", Operation: "list",
		Input: input,
	}, mutationEnvelope(grant, scopeVersion))
	if err != nil || out.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("a retained work row below the envelope ceiling must publish intact: err=%v refusal=%+v", err, out.Error)
	}
	var result struct {
		Items []workSummary `json:"items"`
	}
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatal(err)
	}
	for _, item := range result.Items {
		if item.ID == workID {
			if item.Title != title {
				t.Fatalf("the read changed retained work text: %+v", item)
			}
			if detail == "full" && (item.ValueStatement != value || item.WorkflowTypeRef != workflow || len(item.Tags) != 1 || item.Tags[0] != tag) {
				t.Fatalf("the full read changed retained intent: %+v", item)
			}
			return
		}
	}
	t.Fatalf("the read omitted the retained work row: %s", out.Result)
}
