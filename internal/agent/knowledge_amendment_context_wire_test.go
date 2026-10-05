package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// seedAmendmentWireHome commits the prod-alpha knowledge home with two
// accepted decisions joined by one authored refines edge, then projects it.
// It is the agent-level form of the CON-830 store fixture: the handler test
// drives the real generated surface against a real verified source.
func seedAmendmentWireHome(t *testing.T, s *store.Store) {
	t.Helper()
	repo, err := os.MkdirTemp(t.TempDir(), "amendment-wire-")
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "--initial-branch=main")
	gitRun(t, repo, "config", "user.email", "concord@example.invalid")
	gitRun(t, repo, "config", "user.name", "Concord Amendment Wire Test")
	rootPath := ".concord/docs/decisions/CD-9001-amend-root.md"
	refinerPath := ".concord/docs/decisions/CD-9002-amend-refiner.md"
	bodies := map[string]string{
		rootPath:    "The root amendment wire rule.\n",
		refinerPath: "The refining amendment wire rule.\n",
	}
	for path, body := range bodies {
		full := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	legislated := store.KnowledgeAuthority{Tier: "legislated", LegislatedBy: "fixture-authority", ContractVersion: 1}
	manifest := store.KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: store.KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "amendment-wire", RootDomainID: "product-root:amendment-wire",
			Domains: []store.KnowledgeDomain{
				{DomainID: "product-root:amendment-wire", Name: "Amendment wire", Purpose: "Amendment wire law", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
				{DomainID: "amendment-wire-law", Name: "Amendment wire law", Purpose: "Holds the amendment wire decisions", Status: "current", ParentDomainID: "product-root:amendment-wire", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
			},
		},
		Records: []store.KnowledgeRecord{
			{
				ID: "CD-9001", Kind: "decision", Path: rootPath, Status: "accepted",
				Date: "2026-10-04T00:00:00Z", Title: "Root amendment wire rule", Summary: "The root rule", Tags: []string{},
				Authority: legislated, Scopes: store.KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
				HomeDomainID: "amendment-wire-law", SHA256: pm1fixture.ContentDigest(bodies[rootPath]),
			},
			{
				ID: "CD-9002", Kind: "decision", Path: refinerPath, Status: "accepted",
				Date: "2026-10-04T00:00:00Z", Title: "Refining amendment wire rule", Summary: "The refining rule", Tags: []string{},
				Authority: legislated, Scopes: store.KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
				HomeDomainID: "amendment-wire-law", SHA256: pm1fixture.ContentDigest(bodies[refinerPath]),
				LawRelations: []store.KnowledgeRelation{{Kind: "refines", TargetID: "CD-9001"}},
			},
		},
	}
	if err := pm1fixture.WriteKnowledgeShards(repo, manifest); err != nil {
		t.Fatalf("pm1fixture.WriteKnowledgeShards: %v", err)
	}
	gitRun(t, repo, "add", "--", ".")
	gitRun(t, repo, "commit", "--quiet", "-m", "amendment wire knowledge home")
	execPopulationStatement(t, s, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('proj-amend','Amendment wire',1,'now','now')`)
	execPopulationStatement(t, s, `INSERT INTO product_projects(product_id,project_id,role) VALUES('prod-alpha','proj-amend','secondary')`)
	execPopulationStatement(t, s, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('amendment-wire-locator','proj-amend','canonical_path',?,?,'now','now')`, repo, repo)
	execPopulationStatement(t, s, `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('prod-alpha','proj-amend','amendment-wire-locator')`)
	home := store.KnowledgeHome{HomeProjectID: "proj-amend", HomeLocatorID: "amendment-wire-locator", RepoPath: repo, HeadRef: "HEAD"}
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("rebuild amendment wire knowledge index: %v", err)
	}
}

func seedUnreachableAmendmentPeer(t *testing.T, s *store.Store) {
	t.Helper()
	execPopulationStatement(t, s, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('proj-amend-peer','Amendment peer',1,'now','now')`)
	execPopulationStatement(t, s, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('amendment-peer-locator','proj-amend-peer','canonical_path','/nonexistent/amendment-peer','/nonexistent/amendment-peer','now','now')`)
	execPopulationStatement(t, s, `INSERT INTO product_projects(product_id,project_id,role) VALUES('prod-alpha','proj-amend-peer','secondary')`)
	execPopulationStatement(t, s, `INSERT INTO product_knowledge_sources(product_id,project_id,locator_id,registered_at) VALUES('prod-alpha','proj-amend-peer','amendment-peer-locator','now')`)
}

// TestKnowledgeResolveNoteAmendmentContextOptIn drives the real
// concord_knowledge.resolve_note handler and generated input surface: the
// opt-in read carries the separately proved current_amendment_context
// section, the historical-only read keeps its shape, and the wire refuses
// out-of-band limits and unknown fields before any domain call.
func TestKnowledgeResolveNoteAmendmentContextOptIn(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := agentJobsPM1Fixture(t)
	seedAmendmentWireHome(t, s)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")

	optIn := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"CD-9001","current_amendment_context":{"limit":5}}`),
	}, env)
	if optIn.Outcome != OutcomeOK {
		t.Fatalf("opt-in resolve outcome=%s error=%+v", optIn.Outcome, optIn.Error)
	}
	var payload map[string]any
	if err := json.Unmarshal(optIn.Result, &payload); err != nil {
		t.Fatalf("unmarshal opt-in result: %v", err)
	}
	if payload["state"] != "canonical" || payload["status"] != "accepted" {
		t.Fatalf("historical locator state changed under the opt-in: %s", optIn.Result)
	}
	section, ok := payload["current_amendment_context"].(map[string]any)
	if !ok {
		t.Fatalf("opt-in result carries no current_amendment_context section: %s", optIn.Result)
	}
	if section["authority"] != "authoritative" {
		t.Fatalf("amendment section authority=%v want authoritative", section["authority"])
	}
	edges, ok := section["edges"].([]any)
	if !ok || len(edges) != 1 {
		t.Fatalf("amendment section edges=%v want the one authored refinement", section["edges"])
	}
	edge, _ := edges[0].(map[string]any)
	if edge["direction"] != "incoming" || edge["kind"] != "refines" {
		t.Fatalf("edge is not the incoming authored refinement: %v", edge)
	}
	if edge["endpoint_law_id"] != "CD-9002" || edge["endpoint_status"] != "accepted" {
		t.Fatalf("edge endpoint mismatch: %v", edge)
	}
	if edge["endpoint_project_id"] != "proj-amend" || edge["endpoint_locator_id"] != "amendment-wire-locator" {
		t.Fatalf("edge endpoint is not source-qualified: %v", edge)
	}
	if edge["scanned_commit_oid"] == "" || edge["endpoint_content_hash"] == "" {
		t.Fatalf("edge lacks its scanned commit or content hash proof: %v", edge)
	}
	watermarks, ok := section["source_watermarks"].([]any)
	if !ok || len(watermarks) != 1 {
		t.Fatalf("amendment section carries no per-source watermark: %s", optIn.Result)
	}

	historical := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"CD-9001"}`),
	}, env)
	if historical.Outcome != OutcomeOK {
		t.Fatalf("historical resolve outcome=%s error=%+v", historical.Outcome, historical.Error)
	}
	if strings.Contains(string(historical.Result), "current_amendment_context") {
		t.Fatalf("historical-only read gained the amendment section: %s", historical.Result)
	}

	if err := ValidateOperationPayload("concord_knowledge", "resolve_note", []byte(`{"knowledge_id":"CD-9001","current_amendment_context":{"limit":32}}`), false); err != nil {
		t.Fatalf("limit 32 refused: %v", err)
	}
	if err := ValidateOperationPayload("concord_knowledge", "resolve_note", []byte(`{"knowledge_id":"CD-9001","current_amendment_context":{"limit":33}}`), false); err == nil {
		t.Fatal("limit 33 passed the closed input surface")
	}
	if err := ValidateOperationPayload("concord_knowledge", "resolve_note", []byte(`{"knowledge_id":"CD-9001","current_amendment_context":{"limits":5}}`), false); err == nil {
		t.Fatal("unknown field passed the closed input surface")
	}
	// The read boundary refuses both shapes before any domain call: the
	// schema-bound limit and the unknown field surface as Dispatch errors,
	// the same typed refusal every other closed read input takes.
	overEnv := env
	overEnv.ScopeVersion = scopeVersionForProject(t, s, env.AmbientProjectID)
	if _, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"CD-9001","current_amendment_context":{"limit":33}}`),
	}, overEnv); err == nil {
		t.Fatal("limit 33 dispatch answered instead of refusing")
	}
	if _, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool: "concord_knowledge", Operation: "resolve_note",
		Input: json.RawMessage(`{"knowledge_id":"CD-9001","current_amendment_context":{"limits":5}}`),
	}, overEnv); err == nil {
		t.Fatal("unknown-field dispatch answered instead of refusing")
	}
}
