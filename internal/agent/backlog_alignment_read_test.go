package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestTraceHistoryReadsBacklogAlignment(t *testing.T) {
	t.Parallel()
	for _, workflow := range []string{"workflow.implementation", "workflow.break_fix"} {
		for _, matches := range []int{0, 2, 64} {
			t.Run(fmt.Sprintf("%s/%d", workflow, matches), func(t *testing.T) {
				s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "product_read"})
				registered, err := store.LiveWorkflowDefinitionForRef(workflow)
				if err != nil {
					t.Fatal(err)
				}
				version := seedAgentWorkflowAt(t, s, grant, registered)
				scopeVersion, _, err := s.ScopeVersion(t.Context(), "project-1")
				if err != nil {
					t.Fatal(err)
				}
				env := mutationEnvelope(grant, scopeVersion)
				read := func() map[string]json.RawMessage {
					t.Helper()
					response := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: json.RawMessage(`{"work_id":"work-1","limit":1}`)}, env)
					if response.Outcome != agentcontract.OutcomeOK {
						t.Fatalf("history: %+v", response.Error)
					}
					var page struct {
						Workflow map[string]json.RawMessage `json:"workflow"`
					}
					if err := json.Unmarshal(response.Result, &page); err != nil {
						t.Fatal(err)
					}
					return page.Workflow
				}
				if _, present := read()["backlog_alignment"]; present {
					t.Fatal("never-checked work carries backlog_alignment")
				}
				action := "record_diagnosis"
				if workflow == "workflow.implementation" {
					invokeWorkflowIssue31Action(t, s, service, env, "record_proposal", version, "alignment-read-proposal")
					version = workOneVersion(t, s)
					action = "record_alignment"
				}
				outcome := "none_found"
				related := []string{}
				for i := matches - 1; i >= 0; i-- {
					id := fmt.Sprintf("related-%02d", i)
					if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
						EventID: "created-" + id, Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: id,
						Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"),
						Payload: json.RawMessage(`{"work_kind":"task","title":"Related work","priority":1}`),
					}, {
						EventID: "membership-" + id, Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: id,
						Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.memberships_replaced"),
						Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`),
					}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, id): 0}}); err != nil {
						t.Fatal(err)
					}
					related = append(related, id)
				}
				searched := "Searched open work for duplicate objectives; no inferred relations."
				if matches == 64 {
					searched = strings.Repeat("s", 4096)
				}
				fields := map[string]any{"searched": searched, "outcome": outcome}
				if matches > 0 {
					outcome = "related_found"
					fields["outcome"], fields["related_ids"] = outcome, related
				}
				if action == "record_diagnosis" {
					fields["reproduction"], fields["root_cause"] = "The defect reproduces.", "The cause is identified."
				}
				raw, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": action, "fields": fields, "idempotency_key": "alignment-read-record"})
				if err != nil {
					t.Fatal(err)
				}
				response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
				if response.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("%s: %+v", action, response.Error)
				}
				var alignment struct {
					Searched   string   `json:"searched"`
					Outcome    string   `json:"outcome"`
					RelatedIDs []string `json:"related_ids"`
				}
				commits := s.DurableCommits()
				slices.Sort(related)
				for range 2 {
					page := read()
					if err := json.Unmarshal(page["backlog_alignment"], &alignment); err != nil {
						t.Fatalf("recorded alignment absent or malformed: %s: %v", page["backlog_alignment"], err)
					}
					if alignment.Searched != searched || alignment.Outcome != outcome || alignment.RelatedIDs == nil || !slices.Equal(alignment.RelatedIDs, related) {
						t.Fatalf("alignment facts differ: %+v", alignment)
					}
				}
				if s.DurableCommits() != commits {
					t.Fatal("reading alignment committed a write")
				}
			})
		}
	}
}
