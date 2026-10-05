package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

// Promoted CON-830 review probes (c922 review). The assertions are the
// review's, unchanged; the queryQ10 call site tracks the internal signature.

// A historical AllowDegraded allowance must not bypass the strict
// current-source verification behind a contextual negative: the two
// degradation policies are independent (CON-830 review).
func TestReviewC922StrictNegativeIndependentHistoricalDegradation(t *testing.T) {
	for _, qualified := range []bool{false, true} {
		name := "bare"
		if qualified {
			name = "qualified"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			defer s.Close()
			home := seedAmendmentContextHome(t)
			authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
			if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(home.RepoPath, home.RepoPath+"-unreachable"); err != nil {
				t.Fatal(err)
			}
			id := "CD-9999"
			if qualified {
				id = home.HomeProjectID + "/" + id
			}
			req := Q10Request{Product: "amendment-product", KnowledgeID: id, IncludeAmendmentContext: true}
			strict, err := s.QueryQ10(ctx, req)
			if err == nil {
				t.Fatalf("control strict read did not refuse: %+v", strict)
			}
			req.AllowDegraded = true // historical allowance only; current context remains strict
			result, err := s.QueryQ10(ctx, req)
			if err == nil {
				t.Fatalf("historical degradation bypassed strict current-source refusal: state=%s authority=%s omissions=%v", result.Status, result.Authority, result.Omissions)
			}
		})
	}
}

// A classified contextual negative must seal its verified population against
// the lookup read snapshot: a source registered during the demand-freshness
// window is drift, never a quiet authoritative negative.
func TestReviewC922NegativeSourceSetDrift(t *testing.T) {
	for _, allow := range []bool{false, true} {
		name := "strict"
		if allow {
			name = "degraded_allowed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			defer s.Close()
			home := seedAmendmentContextHome(t)
			authorizeKnowledgeProductHome(t, s, "amendment-product", home)
			if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			// Force the real freshness callback, then add a required unreadable
			// source after selection but before the negative's SQL lookup retries.
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
			result, err := queryQ10(ctx, s.db, freshen, nil, Q10Request{Product: "amendment-product", KnowledgeID: "CD-9999", IncludeAmendmentContext: true, AmendmentContextAllowDegraded: allow})
			if !called {
				t.Fatal("drift hook was not exercised")
			}
			current, srcErr := resolveKnowledgeQuerySources(ctx, s.db, "amendment-product", "probe")
			if srcErr != nil || len(current) != 2 {
				t.Fatalf("drift fixture did not register two sources: %v %v", current, srcErr)
			}
			if err == nil && result.Authority == "authoritative" {
				t.Fatalf("negative claimed verified population after required source gained: state=%s authority=%s omissions=%v sources=%d", result.Status, result.Authority, result.Omissions, len(current))
			}
		})
	}
}

// The real indexed-note acceptance scale (c922 review): authored records,
// committed bodies, the production rebuild, and a moved head with identical
// projected metadata, so every measured read takes the changed-head
// freshness path. The direct refinement query and the public contextual Q10
// read must both hold the 100ms P99 budget over the real population.
func TestReviewC922ChangedHeadIndexedPopulation(t *testing.T) {
	for _, n := range []int{1000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			defer s.Close()
			home := seedAmendmentContextHome(t)
			m := readCON830AcceptanceManifest(t, home)
			for i := 2; i < 1000; i++ {
				r := m.Records[1]
				r.ID = fmt.Sprintf("CD-%04d", i+1)
				r.Path = ".concord/docs/decisions/" + r.ID + ".md"
				if i > 33 {
					r.LawRelations = nil
				}
				writeKnowledgeFile(t, home.RepoPath, r.Path, "refining storage rule\n")
				m.Records = append(m.Records, r)
			}
			for i := 1000; i < n; i++ {
				id := fmt.Sprintf("scale-work-%04d", i)
				body := "---\nconcord_work_id: " + id + "\nwork_type: implementation\ntitle: Scale work note\ncompleted_at: 2026-09-20T00:00:00Z\noutcome_tag: shipped\nlesson_tags: []\nterminal_state: completed\npriority: 2\nsummary: Scale work note\nproduct_ids: [amendment-product]\nproject_ids: [amend-project]\ndomain_ids: [amendment-storage]\ntag_ids: []\n---\n\nDurable note.\n"
				writeKnowledgeFile(t, home.RepoPath, ".concord/docs/work/"+id+".md", body)
			}
			scanned := commitCON830AcceptanceManifest(t, home, m)
			authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
			if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			var count, distinct int
			var pathBytes int
			if err := s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT id), SUM(length(note_path)+1) FROM archived_work WHERE home_project_id=? AND home_locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&count, &distinct, &pathBytes); err != nil {
				t.Fatal(err)
			}
			if count != n || distinct != n {
				t.Fatalf("real indexed population rows=%d distinct=%d want=%d", count, distinct, n)
			}
			writeKnowledgeFile(t, home.RepoPath, "unrelated.txt", "unrelated committed change\n")
			head := commitKnowledgeRepo(t, home.RepoPath, "unrelated change after real rebuild")
			if head == scanned || seedRefinementDigest(t, home, head) != seedRefinementDigest(t, home, scanned) {
				t.Fatal("fixture is not moved-head identical-metadata")
			}
			plans, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT note_path FROM archived_work WHERE home_project_id=? AND home_locator_id=?`, home.HomeProjectID, home.HomeLocatorID)
			if err != nil {
				t.Fatal(err)
			}
			for plans.Next() {
				var a, b, c int
				var d string
				if err := plans.Scan(&a, &b, &c, &d); err != nil {
					t.Fatal(err)
				}
				t.Logf("PRODUCTION freshness plan: %s", d)
			}
			plans.Close()
			unchanged, err := knowledgeProjectedBlobsUnchanged(ctx, s.db, home, scanned, head)
			if err != nil || !unchanged {
				t.Fatalf("unchanged blobs=%v err=%v", unchanged, err)
			}
			var allocBefore, allocAfter runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&allocBefore)
			if _, err := knowledgeProjectedBlobsUnchanged(ctx, s.db, home, scanned, head); err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&allocAfter)
			t.Logf("PRODUCTION freshness returned/examined rows=%d distinct=%d path argv bytes=%d allocated bytes=%d; SQL has no LIMIT", count, distinct, pathBytes, allocAfter.TotalAlloc-allocBefore.TotalAlloc)
			samples := make([]time.Duration, 0, 200)
			for i := 0; i < 200; i++ {
				start := time.Now()
				page, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Product: "amendment-product", Home: home, Roots: []string{home.HomeProjectID + "/CD-0001"}, Limit: 32})
				samples = append(samples, time.Since(start))
				if err != nil {
					t.Fatal(err)
				}
				if page.Authority != "authoritative" || len(page.Edges) != 32 || page.NextCursor == nil || len(page.SourceWatermarks) != 1 || page.SourceWatermarks[0].Watermark != scanned {
					t.Fatalf("unexpected current page: %+v", page)
				}
				if i == 0 {
					raw, err := json.Marshal(page)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("page bytes=%d edges=%d", len(raw), len(page.Edges))
				}
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			t.Logf("REAL indexed notes=%d changed-head P50=%s P99=%s N=200", n, samples[100], samples[198])
			if samples[198] > 100*time.Millisecond {
				t.Errorf("changed-head P99=%s exceeds approved <=100ms", samples[198])
			}
			samples = nil
			for i := 0; i < 200; i++ {
				start := time.Now()
				result, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001", IncludeAmendmentContext: true, AmendmentContextLimit: 32})
				samples = append(samples, time.Since(start))
				if err != nil {
					t.Fatal(err)
				}
				if result.Result == nil || result.Result.CurrentAmendmentContext == nil || result.Result.CurrentAmendmentContext.Authority != "authoritative" || len(result.Result.CurrentAmendmentContext.Edges) != 32 {
					t.Fatalf("unexpected Q10 result: %+v", result)
				}
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			t.Logf("PUBLIC Q10 real indexed notes=%d changed-head P50=%s P99=%s N=200", n, samples[100], samples[198])
			if samples[198] > 100*time.Millisecond {
				t.Errorf("public Q10 changed-head P99=%s exceeds approved <=100ms", samples[198])
			}
			control := make([]time.Duration, 0, 20)
			for i := 0; i < 20; i++ {
				start := time.Now()
				_, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001"})
				control = append(control, time.Since(start))
				if err != nil {
					t.Fatal(err)
				}
			}
			sort.Slice(control, func(i, j int) bool { return control[i] < control[j] })
			t.Logf("historical-only diagnostic control P50=%s N=20 (not base-branch evidence)", control[10])
		})
	}
}
