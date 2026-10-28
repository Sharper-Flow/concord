package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestPublishedExecutionAbsenceAuthorizationAndWire(t *testing.T) {
	s := runtimeKnowledgeStore(t, "work-published", "work_note", []string{"product-a"}, map[string]string{"product-a": "member"})
	defer s.Close()
	ctx := t.Context()
	request := InvokeRequest{Tool: "concord_work_trace", Operation: "observations", Input: json.RawMessage(`{"work_id":"work-published"}`)}
	for _, products := range [][]string{{"product-b"}, {"product-a"}} {
		err := validateRequestedScope(ctx, s, CallEnvelope{SelectedProductID: products[0]}, Authority{ProductScope: products}, request, agentcontract.OperationRead)
		if products[0] == "product-b" {
			var failure *runtimeFailure
			if !errors.As(err, &failure) || failure.kind != "unauthorized" || strings.Contains(err.Error(), ".concord/docs/work/") {
				t.Fatalf("scope guard leaked or admitted: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("authorized outcome owner refused: %v", err)
		}
		r := runtime{Store: s, Tool: request.Tool, Operation: request.Operation, Envelope: CallEnvelope{SelectedProductID: "product-a"}}
		response, err := r.read(ctx, agentcontract.NewBase("published-read", r.Tool, r.Operation), request.Input, "CD-0030.R1")
		if err != nil {
			response = failureEnvelope(agentcontract.NewBase("published-read", r.Tool, r.Operation), err)
		}
		if response.Error == nil || response.Error.Kind != "unknown_scope" {
			t.Fatalf("wire failure=%+v", response)
		}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"execution state is not retained", ".concord/docs/work/work-published.md", "stored-project", "stored-locator"} {
			if !strings.Contains(string(raw), text) {
				t.Errorf("wire lost %q: %s", text, raw)
			}
		}
	}
}

func TestPublishedExecutionAbsenceUnresolvedLocatorWire(t *testing.T) {
	for _, tc := range []struct {
		name, sql, kind string
	}{
		{"ambiguous", `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('other-locator','stored-project','canonical_path','/synthetic/other','/synthetic/other','now','now'); INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) SELECT id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,'other-locator',note_path,commit_oid,content_hash,scope_mode FROM archived_work`, "ambiguous_scope"},
		{"unindexed classification", `DELETE FROM archived_work_products; DELETE FROM archived_work; INSERT INTO work_items(id,kind,title,lifecycle,version,priority,created_at,updated_at,intent_json) VALUES('work-published','bug','Classified bug','completed',3,0,'now','now','{}')`, "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := runtimeKnowledgeStore(t, "work-published", "work_note", []string{"product-a"}, map[string]string{"product-a": "member"})
			defer s.Close()
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); ` + tc.sql + `; DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			_, err := s.ReadWorkItemSummary(t.Context(), "work-published")
			if err == nil {
				t.Fatal("removed execution returned live metadata")
			}
			response := failureEnvelope(agentcontract.NewBase("published-read", "concord_work_trace", "observations"), err)
			if response.Error == nil || response.Error.Kind != tc.kind {
				t.Fatalf("wire failure=%+v, want %s", response.Error, tc.kind)
			}
			if _, err := json.Marshal(response); err != nil {
				t.Fatal(err)
			}
		})
	}
}
