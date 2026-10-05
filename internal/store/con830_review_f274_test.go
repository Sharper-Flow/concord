package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestReviewF274IncomingCrossSource(t *testing.T) {
	s, home := refinementTestStore(t)
	defer s.Close()
	peer := seedRefinementScaleSource(t, s, "review-peer", "review-peer-loc", 1)
	commit := firstCommitOID(t, s, home)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
 INSERT INTO law_cross_source_relations(home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id,scanned_commit_oid) VALUES(?,?,?,'refines',?,?,?);
 DELETE FROM fold_guard`, home.HomeProjectID, home.HomeLocatorID, "CD-0058", peer.HomeProjectID, "EXT-0000", commit); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"EXT-0000", peer.HomeProjectID + "/EXT-0000"} {
		page, err := s.QueryKnowledgeRefinementContext(context.Background(), KnowledgeRefinementContextRequest{Roots: []string{root}, Sources: []KnowledgeHome{home, peer}})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Edges) != 1 || page.Edges[0].Direction != "incoming" {
			t.Errorf("root %s omitted declared accepted cross-source refines: authority=%s edges=%+v omissions=%v", root, page.Authority, page.Edges, page.Omissions)
		}
	}
}

func TestReviewF274SameCommitCoverageDrift(t *testing.T) {
	s, home := refinementTestStore(t)
	defer s.Close()
	ctx := context.Background()
	scanned, authority, err := validateKnowledgeHomeForQueryCore(ctx, s.DatabaseForTesting(), home, false, "review.snapshot")
	if err != nil || authority != "authoritative" {
		t.Fatalf("initial actual verification: scanned=%s authority=%s err=%v", scanned, authority, err)
	}
	proof := refinementSourceVerification{scanned: []string{home.HomeProjectID + "/" + home.HomeLocatorID + "@" + scanned}, watermarks: []KnowledgeSourceWatermark{{ProjectID: home.HomeProjectID, LocatorID: home.HomeLocatorID, Watermark: scanned, Authority: authority}}}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`UPDATE knowledge_index_watermark SET complete=0 WHERE home_project_id=? AND home_locator_id=?`, home.HomeProjectID, home.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	page, err := queryKnowledgeRefinementContext(ctx, tx, KnowledgeRefinementContextRequest{}, []KnowledgeHome{home}, []string{"CD-9999"}, 32, proof)
	if err == nil && page.Authority == "authoritative" {
		t.Fatalf("same-commit incomplete projection drift accepted: authority=%s edges=%+v omissions=%v", page.Authority, page.Edges, page.Omissions)
	}
}

func TestReviewF274QualifiedQ10SourceCoverage(t *testing.T) {
	s := openTemp(t)
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	peer := seedRefinementScaleSource(t, s, "review-peer", "review-peer-loc", 1)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO product_projects(product_id,project_id,role) VALUES('amendment-product',?,'primary')`, home.HomeProjectID); err != nil {
		t.Fatalf("home membership: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO product_projects(product_id,project_id,role) VALUES('amendment-product',?,'secondary')`, peer.HomeProjectID); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO product_knowledge_sources(product_id,project_id,locator_id,registered_at) VALUES('amendment-product',?,?,'now')`, peer.HomeProjectID, peer.HomeLocatorID); err != nil {
		t.Fatalf("source: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"CD-0001", home.HomeProjectID + "/CD-0001"} {
		result, err := s.QueryQ10(context.Background(), Q10Request{Product: "amendment-product", KnowledgeID: root, IncludeAmendmentContext: true})
		if err != nil {
			t.Fatal(err)
		}
		page := result.Result.CurrentAmendmentContext
		if page.Authority != "authoritative" || len(page.SourceWatermarks) != 2 {
			t.Errorf("root %s dropped registered source: authority=%s watermarks=%+v omissions=%v", root, page.Authority, page.SourceWatermarks, page.Omissions)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`UPDATE knowledge_index_watermark SET complete=0 WHERE home_project_id=? AND home_locator_id=?`, peer.HomeProjectID, peer.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryQ10(context.Background(), Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001", IncludeAmendmentContext: true})
	if err == nil {
		t.Errorf("strict qualified opt-in ignored incomplete registered peer: authority=%s omissions=%v", result.Result.CurrentAmendmentContext.Authority, result.Result.CurrentAmendmentContext.Omissions)
	}
}
