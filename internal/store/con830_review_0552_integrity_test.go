package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Promoted CON-830 review probes (0552 review). The assertions are the
// review's, unchanged in strength; the corrupt-object variant covers
// same-HEAD tampering the missing-object probe does not reach. Historical
// root proof stays an independent control in every case: deleting or
// corrupting a refiner's current blob must never fail a historical-only
// read, while the strict current context refuses and the degraded context
// names the omission instead of claiming an authoritative graph.

func TestReview0552CurrentEndpointMissingObject(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	req := Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001", IncludeAmendmentContext: true}
	if _, err := s.QueryQ10(ctx, req); err != nil {
		t.Fatal(err)
	}
	oidRaw, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD:.concord/docs/decisions/CD-0002.md")
	if err != nil {
		t.Fatal(err)
	}
	oid := strings.TrimSpace(string(oidRaw))
	if err := os.Remove(filepath.Join(home.RepoPath, ".git", "objects", oid[:2], oid[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, home.RepoPath, "cat-file", "blob", oid); err == nil {
		t.Fatal("missing-object fixture failed")
	}
	if _, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001"}); err != nil {
		t.Fatalf("historical control failed: %v", err)
	}
	strict, err := s.QueryQ10(ctx, req)
	if err == nil {
		t.Errorf("strict context accepted missing current endpoint blob: authority=%s edges=%+v", strict.Result.CurrentAmendmentContext.Authority, strict.Result.CurrentAmendmentContext.Edges)
	} else {
		assertFailureKind(t, err, KindInvalidNoteProof)
	}
	req.AmendmentContextAllowDegraded = true
	degraded, err := s.QueryQ10(ctx, req)
	if err == nil && degraded.Result.CurrentAmendmentContext.Authority == "authoritative" {
		t.Errorf("degraded-allowed context hid missing blob: omissions=%v", degraded.Result.CurrentAmendmentContext.Omissions)
	}
	if err == nil && len(degraded.Result.CurrentAmendmentContext.Omissions) == 0 {
		t.Errorf("degraded-allowed context named no omission for the missing blob: %+v", degraded.Result.CurrentAmendmentContext.ResultMeta)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err == nil {
		t.Fatal("rebuild accepted missing endpoint blob")
	}
}

// A corrupt endpoint object is the same defect as a missing one: unchanged
// metadata and HEAD cannot prove the live object still matches its recorded
// content, so git reports the spec as missing and the read must refuse or
// degrade — never serve the projected metadata as authoritative.
func TestReview0552CurrentEndpointCorruptObject(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	req := Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001", IncludeAmendmentContext: true}
	if _, err := s.QueryQ10(ctx, req); err != nil {
		t.Fatal(err)
	}
	oidRaw, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD:.concord/docs/decisions/CD-0002.md")
	if err != nil {
		t.Fatal(err)
	}
	oid := strings.TrimSpace(string(oidRaw))
	objectPath := filepath.Join(home.RepoPath, ".git", "objects", oid[:2], oid[2:])
	raw, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), raw...)
	corrupt[len(corrupt)/2] ^= 0x5a
	if err := os.Chmod(objectPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001"}); err != nil {
		t.Fatalf("historical control failed: %v", err)
	}
	if _, err := s.QueryQ10(ctx, req); err == nil {
		t.Error("strict context accepted corrupt current endpoint blob")
	}
	req.AmendmentContextAllowDegraded = true
	degraded, err := s.QueryQ10(ctx, req)
	if err == nil && degraded.Result.CurrentAmendmentContext.Authority == "authoritative" {
		t.Errorf("degraded-allowed context hid corrupt blob: omissions=%v", degraded.Result.CurrentAmendmentContext.Omissions)
	}
	if err == nil && len(degraded.Result.CurrentAmendmentContext.Omissions) == 0 {
		t.Errorf("degraded-allowed context named no omission for the corrupt blob: %+v", degraded.Result.CurrentAmendmentContext.ResultMeta)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err == nil {
		t.Fatal("rebuild accepted corrupt endpoint blob")
	}
}

func TestReview0552NegativeDriftHonorsDegradedAllowance(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	m := readCON830AcceptanceManifest(t, home)
	m.Records[0].Summary = "Changed metadata triggers demand rebuild"
	commitCON830AcceptanceManifest(t, home, m)
	peer := seedRefinementScaleSource(t, s, "negative-drift-peer", "negative-drift-peer-loc", 1)
	if err := os.Rename(peer.RepoPath, peer.RepoPath+"-unreachable"); err != nil {
		t.Fatal(err)
	}
	called := false
	freshen := func(ctx context.Context, h KnowledgeHome) error {
		if err := s.EnsureKnowledgeIndexFresh(ctx, h); err != nil {
			return err
		}
		if !called {
			registerA1Peer(t, s, home, peer)
			called = true
		}
		return nil
	}
	result, err := queryQ10(ctx, s.db, freshen, nil, Q10Request{Product: "amendment-product", KnowledgeID: "CD-9999", IncludeAmendmentContext: true, AmendmentContextAllowDegraded: true})
	if !called {
		t.Fatal("interleave not exercised")
	}
	if err != nil {
		t.Fatalf("degraded-allowed first read refused instead of naming newly unavailable source: %v", err)
	}
	if result.Authority != "degraded" || len(result.Omissions) == 0 {
		t.Fatalf("degraded negative lost omissions: %+v", result)
	}
}
