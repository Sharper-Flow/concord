package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestReview809578ContinuityAmendmentSchema(t *testing.T) {
	snapshot := store.ContinuitySnapshot{
		WorkID: "work-constitution", ProductIdentity: []string{"product"}, WorkflowStep: "execution",
		SpecMandate: []string{"const:one"}, RestartUnavailableReason: "dispatch window closed",
		Boundaries: []store.ContextBoundary{}, Watermark: "seq:1",
		LawContext: &store.WorkflowLawContext{
			Laws:             []store.WorkflowLawContextLaw{{Roles: []string{"mandated"}, LawID: "const:one", Kind: "constitution", Status: "accepted"}},
			Domains:          []store.WorkflowLawContextDomain{},
			AmendmentContext: &store.KnowledgeRefinementContextResult{Edges: []store.KnowledgeRefinementEdge{}, Roots: []string{"const:one"}},
		},
	}
	snapshot.LawContext.AmendmentContext.Authority = "authoritative"
	raw, err := json.Marshal(ContinuityPayload(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("continuity_snapshot", raw); err != nil {
		t.Fatalf("mandated-law continuity becomes schema-invalid: %v", err)
	}
}

func TestReview809578OptInDemandFreshness(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	var repo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE locator_id='amendment-wire-locator'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".concord/docs/knowledge/records/CD-9002.json")
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes, &record); err != nil {
		t.Fatal(err)
	}
	delete(record, "law_relations")
	bytes, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "--", ".")
	gitRun(t, repo, "commit", "--quiet", "-m", "remove synthetic authored refinement")
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	invoke := InvokeRequest{Tool: "concord_knowledge", Operation: "resolve_note", Input: json.RawMessage(`{"knowledge_id":"proj-amend/CD-9001","current_amendment_context":{}}`)}
	result := dispatchRead(t, s, service, invoke, env)
	if result.Outcome != OutcomeOK {
		t.Logf("first opt-in demand refuses: %+v", result.Error)
		home := store.KnowledgeHome{HomeProjectID: "proj-amend", HomeLocatorID: "amendment-wire-locator", RepoPath: repo, HeadRef: "HEAD"}
		if err := s.EnsureKnowledgeIndexFresh(context.Background(), home); err != nil {
			t.Fatal(err)
		}
		env.ScopeVersion = scopeVersionForProject(t, s, env.AmbientProjectID)
		after := dispatchRead(t, s, service, invoke, env)
		if after.Outcome != OutcomeOK {
			t.Fatalf("diagnostic rebuild did not restore read: %+v", after.Error)
		}
		t.Fatalf("reachable valid new source refuses until an extra freshness call: initial=%+v, after_rebuild=%s", result.Error, after.Result)
	}
}

func TestReview809578MissingWithUnverifiedPopulation(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	seedUnreachableAmendmentPeer(t, s)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	result := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_knowledge", Operation: "resolve_note", Input: json.RawMessage(`{"knowledge_id":"CD-9999","current_amendment_context":{"allow_degraded":true}}`)}, env)
	if result.Outcome == OutcomeOK && result.Authority == "authoritative" {
		t.Fatalf("unverified registered source became an authoritative negative: authority=%s omissions=%v payload=%s", result.Authority, result.Omissions, result.Result)
	}
}
