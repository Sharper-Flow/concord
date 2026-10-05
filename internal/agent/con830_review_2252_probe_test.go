package agent

import (
	"context"
	"encoding/json"
	"github.com/sharper-flow/concord/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestReview2252NewQualifiedLawDemandFreshness(t *testing.T) {
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
	record["id"] = "CD-9003"
	record["path"] = ".concord/docs/decisions/CD-9003.md"
	raw, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".concord/docs/knowledge/records/CD-9003.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".concord/docs/decisions/CD-9003.md"), []byte("The refining amendment wire rule.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "--", ".")
	gitRun(t, repo, "commit", "--quiet", "-m", "add new synthetic law")
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	invoke := InvokeRequest{Tool: "concord_knowledge", Operation: "resolve_note", Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9003","current_amendment_context":{}}`)}
	got := dispatchRead(t, s, service, invoke, env)
	var payload map[string]any
	if err := json.Unmarshal(got.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["state"] != "canonical" {
		if err := s.EnsureKnowledgeIndexFresh(context.Background(), store.KnowledgeHome{HomeProjectID: "proj-amend", HomeLocatorID: "amendment-wire-locator", RepoPath: repo, HeadRef: "HEAD"}); err != nil {
			t.Fatal(err)
		}
		env.ScopeVersion = scopeVersionForProject(t, s, env.AmbientProjectID)
		after := dispatchRead(t, s, service, invoke, env)
		t.Fatalf("new qualified law returned %s authority=%s without demand freshness; manual refresh returns %s outcome=%s", got.Result, got.Authority, after.Result, after.Outcome)
	}
}

func TestReview2252SingleSourceMissingUnreachable(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	var repo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE locator_id='amendment-wire-locator'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo, repo+"-unreachable"); err != nil {
		t.Fatal(err)
	}
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	got := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_knowledge", Operation: "resolve_note", Input: json.RawMessage(`{"knowledge_id":"CD-9999","current_amendment_context":{"allow_degraded":true}}`)}, env)
	if got.Outcome == OutcomeOK && got.Authority == "authoritative" {
		t.Fatalf("single unreachable source returned authoritative negative: result=%s omissions=%v", got.Result, got.Omissions)
	}
}

func TestReview2252CurrentBlobMismatchUnchangedMetadata(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	var repo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE locator_id='amendment-wire-locator'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".concord/docs/decisions/CD-9002-amend-refiner.md"), []byte("Invalid current blob with unchanged authored hash.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "--", ".")
	gitRun(t, repo, "commit", "--quiet", "-m", "break current blob hash without metadata change")
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	got := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_knowledge", Operation: "resolve_note", Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9001","current_amendment_context":{}}`)}, env)
	if got.Outcome == OutcomeOK {
		var payload map[string]any
		if err := json.Unmarshal(got.Result, &payload); err != nil {
			t.Fatal(err)
		}
		section, _ := payload["current_amendment_context"].(map[string]any)
		if section["authority"] == "authoritative" {
			rebuildErr := s.RebuildKnowledgeIndex(context.Background(), store.KnowledgeHome{HomeProjectID: "proj-amend", HomeLocatorID: "amendment-wire-locator", RepoPath: repo, HeadRef: "HEAD"})
			t.Fatalf("strict current context accepted hash-invalid current blob; same-head rebuild error=%v: %s", rebuildErr, got.Result)
		}
	}
}
