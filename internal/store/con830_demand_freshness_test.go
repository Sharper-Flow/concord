package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// advanceReachableKnowledgeHead moves a reachable knowledge home's head
// with a semantically identical manifest byte change: the content digest
// moves so the projection reads stale, and the rebuild re-projects the same
// authored graph, so the advancement is exactly the repairable kind the
// demand-freshen owner exists for.
func advanceReachableKnowledgeHead(t *testing.T, home KnowledgeHome) {
	t.Helper()
	manifestPath := filepath.Join(home.RepoPath, filepath.FromSlash(knowledgeManifestPath))
	current, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(current, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	commitKnowledgeRepo(t, home.RepoPath, "advance the reachable knowledge head")
}

// CON-830 review repair: a valid reachable source whose git head advanced
// demand-freshens through the existing owner before current-context proof,
// on the workflow law_context path exactly as on contextual note
// resolution. The same advanced source answers both readers from one
// verified snapshot, and a workflow verifier without the freshener still
// reports the pre-repair degraded verdict.
func TestWorkflowLawContextDemandFreshensReachableAdvancement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	workID := "workflow-demand-freshness"
	seedStatements := []string{
		`INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES('` + home.HomeProjectID + `','Amendment demand freshness',1,'now','now')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('` + workID + `','task','Demand freshness','needed',1,1,'now','now')`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('` + workID + `','` + home.HomeProjectID + `','primary')`,
		`INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES('amendment-product','` + home.HomeProjectID + `','primary')`,
	}
	for _, statement := range seedStatements {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(statement); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
	}
	advanceReachableKnowledgeHead(t, home)

	withoutFreshen := verifyWorkflowLawContextSources(ctx, s.DatabaseForTesting(), nil, workID)
	if !withoutFreshen.verification.degraded {
		t.Fatalf("advanced source without the freshener verified clean: %+v", withoutFreshen.verification)
	}
	freshened := verifyWorkflowLawContextSources(ctx, s.DatabaseForTesting(), s.EnsureKnowledgeIndexFresh, workID)
	if freshened.verification.degraded || len(freshened.verification.omissions) != 0 {
		t.Fatalf("reachable advanced source stayed degraded after demand-freshening: %+v", freshened.verification)
	}
	if len(freshened.sources) != 1 {
		t.Fatalf("freshened verification lost the source set: %+v", freshened.sources)
	}
	page, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-0001"}, Sources: freshened.sources})
	if err != nil {
		t.Fatalf("contextual resolution over the advanced source refused: %v", err)
	}
	if page.Authority != "authoritative" {
		t.Fatalf("contextual resolution authority=%s omissions=%v, want the freshened authoritative snapshot", page.Authority, page.Omissions)
	}
	if len(page.Edges) != 1 || page.Edges[0].EndpointLawID != "CD-0002" {
		t.Fatalf("freshened snapshot lost the authored refinement: %+v", page.Edges)
	}
	if len(page.SourceWatermarks) != 1 || page.SourceWatermarks[0].Watermark != freshened.verification.watermarks[0].Watermark {
		t.Fatalf("workflow and contextual reads hold different snapshots: workflow=%+v contextual=%+v", freshened.verification.watermarks, page.SourceWatermarks)
	}
}

// The bare-ID population verification of a contextual opt-in read
// demand-freshens the same reachable advancement, while the historical-only
// bare-ID read keeps its exact pre-repair refusal: the repair is scoped to
// the opt-in current context and changes no historical verdict.
func TestQ10BareRootPopulationDemandFreshensOnlyContextualReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	peer := seedRefinementScaleSource(t, s, "fresh-peer", "fresh-peer-loc", 1)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO product_projects(product_id,project_id,role) VALUES('amendment-product',?,'primary')`, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO product_projects(product_id,project_id,role) VALUES('amendment-product',?,'secondary')`, peer.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO product_knowledge_sources(product_id,project_id,locator_id,registered_at) VALUES('amendment-product',?,?,'now')`, peer.HomeProjectID, peer.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	advanceReachableKnowledgeHead(t, home)

	historical := Q10Request{Product: "amendment-product", KnowledgeID: "CD-0001"}
	if _, err := s.QueryQ10(ctx, historical); err == nil {
		t.Fatal("historical-only bare-root read silently accepted the stale population: the repair must not change historical verdicts")
	}

	strict := Q10Request{Product: "amendment-product", KnowledgeID: "CD-0001", IncludeAmendmentContext: true}
	result, err := s.QueryQ10(ctx, strict)
	if err != nil {
		t.Fatalf("strict contextual bare-root read refused a freshenable advanced source: %v", err)
	}
	if result.Authority != "authoritative" || len(result.Omissions) != 0 {
		t.Fatalf("contextual bare-root authority=%s omissions=%v, want the freshened authoritative population", result.Authority, result.Omissions)
	}
	if result.Result == nil || result.Result.CurrentAmendmentContext == nil || result.Result.CurrentAmendmentContext.Authority != "authoritative" {
		t.Fatalf("contextual bare-root amendment section missing or not authoritative: %+v", result.Result)
	}
	if len(result.Result.CurrentAmendmentContext.SourceWatermarks) != 2 {
		t.Fatalf("contextual section dropped a registered source: %+v", result.Result.CurrentAmendmentContext.SourceWatermarks)
	}

}
