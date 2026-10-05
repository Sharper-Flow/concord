package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCON830FullContextPageFitsActualResolveEnvelope(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	var repo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE locator_id='amendment-wire-locator'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(repo, ".concord/docs/knowledge/records/CD-9002.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("CD-%04d", 9100+i)
		record["id"] = id
		record["path"] = ".concord/docs/decisions/" + id + ".md"
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".concord/docs/knowledge/records", id+".json"), encoded, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".concord/docs/decisions", id+".md"), []byte("The refining amendment wire rule.\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "add", "--", ".")
	gitRun(t, repo, "commit", "--quiet", "-m", "add synthetic full-page refinements")
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	got := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_knowledge", Operation: "resolve_note", Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9001","current_amendment_context":{"limit":32}}`)}, env)
	if got.Outcome != OutcomeOK {
		t.Fatalf("full-page resolve failed: outcome=%s error=%+v", got.Outcome, got.Error)
	}
	var payload struct {
		Context struct {
			Edges      []json.RawMessage `json:"edges"`
			NextCursor *string           `json:"next_cursor"`
		} `json:"current_amendment_context"`
	}
	if err := json.Unmarshal(got.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Context.Edges) != 32 || payload.Context.NextCursor == nil {
		t.Fatalf("full page lost its bound or continuation: %s", got.Result)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 51200 {
		t.Fatalf("actual serialized envelope exceeds 51200 bytes: %d", len(encoded))
	}
	t.Logf("actual resolve envelope with 32 authored edges and continuation: %d bytes", len(encoded))
}
