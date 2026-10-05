package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// seedAmendmentContextHome writes one knowledge home whose manifest carries
// CD-0001 accepted and CD-0002 accepted with an authored refines edge to
// CD-0001, plus a work note, exactly as the CON-830 record shards shape the
// real refinement chain.
func seedAmendmentContextHome(t *testing.T) KnowledgeHome {
	t.Helper()
	repo := initKnowledgeRepo(t)
	rootBody, refinerBody := "root storage rule\n", "refining storage rule\n"
	writeKnowledgeFile(t, repo, ".concord/docs/decisions/CD-0001.md", rootBody)
	writeKnowledgeFile(t, repo, ".concord/docs/decisions/CD-0002.md", refinerBody)
	rootSum, refinerSum := sha256.Sum256([]byte(rootBody)), sha256.Sum256([]byte(refinerBody))
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision", "work_note"},
		IndexedKinds:   []string{"decision", "work_note"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "amendment-product", RootDomainID: "product-root:amendment-product",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:amendment-product", Name: "Amendment", Purpose: "Amendment law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "amendment-storage", Name: "Storage", Purpose: "Storage law", Status: "current", ParentDomainID: "product-root:amendment-product", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: []KnowledgeRecord{
			{ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001.md", Status: "accepted",
				Date: "2026-09-20T00:00:00Z", Title: "Root storage rule", Summary: "A root storage rule", Tags: []string{},
				Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "fixture-authority", ContractVersion: 1},
				Scopes:    homeScope(), HomeDomainID: "amendment-storage", SHA256: "sha256:" + hex.EncodeToString(rootSum[:])},
			{ID: "CD-0002", Kind: "decision", Path: ".concord/docs/decisions/CD-0002.md", Status: "accepted",
				Date: "2026-09-20T00:00:00Z", Title: "Refining storage rule", Summary: "A refining storage rule", Tags: []string{},
				Authority:    KnowledgeAuthority{Tier: "legislated", LegislatedBy: "fixture-authority", ContractVersion: 1},
				LawRelations: []KnowledgeRelation{{Kind: "refines", TargetID: "CD-0001"}},
				Scopes:       homeScope(), HomeDomainID: "amendment-storage", SHA256: "sha256:" + hex.EncodeToString(refinerSum[:])},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(manifestBytes)+"\n")
	commitKnowledgeRepo(t, repo, "amendment context fixture")
	return KnowledgeHome{HomeProjectID: "amend-project", HomeLocatorID: "amend-locator", RepoPath: repo, HeadRef: "HEAD"}
}

// T2/T5: the opt-in Q10 read carries a separately verified
// current_amendment_context section with the authored incoming accepted
// refinement, while the historical-only read keeps its existing shape.
func TestQ10OptInCurrentAmendmentContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := seedAmendmentContextHome(t)
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	historical, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "CD-0001", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if historical.Status != "canonical" || historical.Result == nil {
		t.Fatalf("historical Q10 = %#v", historical)
	}
	if historical.Result.CurrentAmendmentContext != nil {
		t.Fatalf("historical-only Q10 carries amendment context: %#v", historical.Result.CurrentAmendmentContext)
	}

	opted, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "CD-0001", Home: home, IncludeAmendmentContext: true})
	if err != nil {
		t.Fatal(err)
	}
	if opted.Status != "canonical" || opted.Result == nil {
		t.Fatalf("opt-in Q10 = %#v", opted)
	}
	amendment := opted.Result.CurrentAmendmentContext
	if amendment == nil {
		t.Fatalf("opt-in Q10 carries no current_amendment_context: %#v", opted.Result)
	}
	if amendment.ResultMeta.QueryID != "PM1.Q10.amendment_context" || amendment.Authority != "authoritative" {
		t.Fatalf("amendment context meta = %#v", amendment.ResultMeta)
	}
	if len(amendment.Edges) != 1 {
		t.Fatalf("amendment context edges = %#v", amendment.Edges)
	}
	edge := amendment.Edges[0]
	if edge.RootID != "CD-0001" || edge.Direction != "incoming" || edge.Kind != "refines" || edge.EndpointLawID != "CD-0002" || edge.EndpointStatus != "accepted" || edge.SourceProjectID != "amend-project" {
		t.Fatalf("authored incoming edge = %#v", edge)
	}
	if edge.ScannedCommitOID == "" || edge.EndpointContentHash == "" {
		t.Fatalf("edge carries no scanned commit or content hash: %#v", edge)
	}
	if len(amendment.SourceWatermarks) != 1 || amendment.SourceWatermarks[0].Authority != "authoritative" {
		t.Fatalf("source watermarks = %#v", amendment.SourceWatermarks)
	}
	// The historical proof stays intact beside the separate current proof.
	if opted.Result.LawStatus != "accepted" || opted.Result.Note == nil || opted.Result.Note.CommitOID == "" {
		t.Fatalf("historical payload degraded by opt-in: %#v", opted.Result)
	}

}
