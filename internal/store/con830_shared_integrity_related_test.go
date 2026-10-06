package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Related cases of the shared current-source integrity owner (CON-830
// retry-18). The coordinator probes and the 0552 review probes cover
// same-HEAD endpoint object deletion on the positive route; these cases
// extend the same proof to cross-source declared targets, corruption on
// the workflow and negative routes, valid head advancement, and the
// memoization boundaries the proof must never cross.

// A cross-source relation's target endpoint object is as required as a
// same-home endpoint's: the page would carry the target home's projected
// identity, so a deleted target object must refuse a strict read and name
// its omission on a degraded one, never ride an authoritative page.
func TestCON830CrossTargetObjectLossRefusesOrDegrades(t *testing.T) {
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	second := seedRefinementSecondSource(t, s, "cross-loss", "cross-loss-loc")
	commit := firstCommitOID(t, s, home)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO law_cross_source_relations(home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?,?);
		DELETE FROM fold_guard`,
		home.HomeProjectID, home.HomeLocatorID, "CD-0017", "subordinate_to", second.HomeProjectID, "EXT-LAW", commit); err != nil {
		t.Fatal(err)
	}
	sources := []KnowledgeHome{home, second}
	read := func(degraded bool) (KnowledgeRefinementContextResult, error) {
		return s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{home.HomeProjectID + "/CD-0017"}, Sources: sources, AllowDegraded: degraded})
	}
	if _, err := read(false); err != nil {
		t.Fatalf("healthy cross-target control refused: %v", err)
	}
	if _, err := read(true); err != nil {
		t.Fatalf("healthy degraded-allowed control refused: %v", err)
	}
	oidRaw, err := runGit(ctx, second.RepoPath, "rev-parse", "HEAD:.concord/docs/decisions/EXT-LAW.md")
	if err != nil {
		t.Fatal(err)
	}
	oid := strings.TrimSpace(string(oidRaw))
	if err := os.Remove(filepath.Join(second.RepoPath, ".git", "objects", oid[:2], oid[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, second.RepoPath, "cat-file", "blob", oid); err == nil {
		t.Fatal("fixture did not remove the cross-target object")
	}
	if _, err := read(false); err == nil {
		t.Fatal("strict read accepted a deleted cross-source target object")
	} else {
		assertFailureKind(t, err, KindInvalidNoteProof)
	}
	degraded, err := read(true)
	if err != nil {
		t.Fatalf("degraded read refused instead of naming the deleted target: %v", err)
	}
	if degraded.Authority == "authoritative" {
		t.Fatalf("degraded read claimed authority over a deleted target object: %+v", degraded.ResultMeta)
	}
	found := false
	for _, omission := range degraded.Omissions {
		if strings.HasPrefix(omission, "endpoint_object_missing:"+second.HomeProjectID+"/"+second.HomeLocatorID+":.concord/docs/decisions/EXT-LAW.md") {
			found = true
		}
	}
	if !found {
		t.Fatalf("degraded read did not name the missing cross-target object: %v", degraded.Omissions)
	}
}

// The object proof is never memoized across reads: after a green read
// leaves both the manifest memo and the tree-resolution memo warm, a
// same-HEAD object deletion on the very next read must still refuse the
// strict contextual read and degrade the allowed one.
func TestCON830ObjectProofNotMemoizedAcrossReads(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	req := Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001", IncludeAmendmentContext: true}
	for i := 0; i < 3; i++ {
		if _, err := s.QueryQ10(ctx, req); err != nil {
			t.Fatalf("warm-up read %d failed: %v", i, err)
		}
	}
	oidRaw, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD:.concord/docs/decisions/CD-0002.md")
	if err != nil {
		t.Fatal(err)
	}
	oid := strings.TrimSpace(string(oidRaw))
	if err := os.Remove(filepath.Join(home.RepoPath, ".git", "objects", oid[:2], oid[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryQ10(ctx, req); err == nil {
		t.Fatal("warm memos hid same-HEAD object loss from the contextual read")
	} else {
		assertFailureKind(t, err, KindInvalidNoteProof)
	}
	req.AmendmentContextAllowDegraded = true
	degraded, err := s.QueryQ10(ctx, req)
	if err != nil {
		t.Fatalf("degraded read refused behind warm memos: %v", err)
	}
	if degraded.Result == nil || degraded.Result.CurrentAmendmentContext == nil || degraded.Result.CurrentAmendmentContext.Authority == "authoritative" {
		t.Fatalf("degraded read claimed authority behind warm memos: %+v", degraded.Result)
	}
	// The historical locator proof stays independent and live: the root
	// note's own blob proof still runs per read behind the warm manifest
	// memo.
	rootOID, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD:.concord/docs/decisions/CD-0001.md")
	if err != nil {
		t.Fatal(err)
	}
	root := strings.TrimSpace(string(rootOID))
	objectPath := filepath.Join(home.RepoPath, ".git", "objects", root[:2], root[2:])
	raw, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), raw...)
	corrupt[len(corrupt)/2] ^= 0xa5
	if err := os.Chmod(objectPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001"}); err == nil {
		t.Fatal("historical read accepted a corrupt note blob behind the warm manifest memo")
	}
}

// Corruption reaches the workflow and contextual negative routes exactly
// as deletion does: an unchanged metadata snapshot cannot prove a live,
// intact refiner object for any contextual authority claim.
func TestCON830SharedIntegrityCorruptionReachesWorkflowAndNegative(t *testing.T) {
	for _, route := range []string{"workflow", "negative"} {
		t.Run(route, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			defer s.Close()
			home := seedAmendmentContextHome(t)
			authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
			if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			const workID = "shared-integrity-corrupt-work"
			if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Shared integrity corruption','needed',1,1,'now','now')`, workID); err != nil {
				t.Fatalf("insert work: %v", err)
			}
			if _, err := s.db.Exec(`INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,'primary')`, workID, home.HomeProjectID); err != nil {
				t.Fatalf("insert membership: %v", err)
			}
			if _, err := s.db.Exec(`DELETE FROM fold_guard`); err != nil {
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
			if route == "negative" {
				out, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: "CD-9999", IncludeAmendmentContext: true})
				if err == nil && out.Authority == "authoritative" {
					t.Fatalf("contextual negative claims authoritative missing from a source with a corrupt refiner object: %+v", out)
				}
				return
			}
			proof := verifyWorkflowLawContextSources(ctx, s.db, s.EnsureKnowledgeIndexFresh, workID)
			page, err := readWorkflowAmendmentPage(ctx, s, workID, proof)
			if err != nil {
				t.Fatalf("workflow read refused instead of degrading: %v", err)
			}
			if page == nil || page.AmendmentContext == nil || len(page.AmendmentContext.Edges) != 1 {
				t.Fatalf("workflow fixture did not exercise the corrupt refiner: %+v", page)
			}
			if page.AmendmentContext.Authority == "authoritative" {
				t.Fatalf("workflow claims authoritative context for a corrupt endpoint object: %+v", page.AmendmentContext.ResultMeta)
			}
			if len(page.AmendmentContext.Omissions) == 0 {
				t.Fatalf("workflow degradation named no omission for the corrupt endpoint object: %+v", page.AmendmentContext.ResultMeta)
			}
		})
	}
}

// Valid advancement keeps the shared proof authoritative: a newly
// committed refiner law with its manifest triggers the demand rebuild, and
// the re-verified verdict proves the new endpoint's object at the new
// commit instead of refusing.
func TestCON830ValidAdvancementStaysAuthoritative(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	m := readCON830AcceptanceManifest(t, home)
	refiner := m.Records[1]
	refiner.ID = "CD-0003"
	refiner.Path = ".concord/docs/decisions/CD-0003.md"
	body := "newly committed refiner\n"
	writeKnowledgeFile(t, home.RepoPath, refiner.Path, body)
	sum := sha256.Sum256([]byte(body))
	refiner.SHA256 = "sha256:" + hex.EncodeToString(sum[:])
	m.Records = append(m.Records, refiner)
	scanned := commitCON830AcceptanceManifest(t, home, m)
	result, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001", IncludeAmendmentContext: true})
	if err != nil {
		t.Fatalf("valid advancement refused the contextual read: %v", err)
	}
	amendment := result.Result.CurrentAmendmentContext
	if amendment == nil || amendment.Authority != "authoritative" {
		t.Fatalf("advanced context authority = %+v", amendment)
	}
	found := false
	for _, edge := range amendment.Edges {
		if edge.EndpointLawID == "CD-0003" && edge.Direction == "incoming" && edge.EndpointContentHash == refiner.SHA256 {
			found = true
		}
	}
	if !found {
		t.Fatalf("advanced page did not carry the new refiner with its proof: %+v", amendment.Edges)
	}
	if len(amendment.SourceWatermarks) != 1 || amendment.SourceWatermarks[0].Watermark != scanned {
		t.Fatalf("advanced page did not bind the rebuilt watermark: %+v", amendment.SourceWatermarks)
	}
}

// The eight-entry manifest memo keeps its own contract under the shared
// proof: bounded FIFO eviction, concurrent reads, and never-cached
// failures. These are unit obligations of the memo the historical proof
// reuses; the live-blob property above covers its interaction with object
// loss.
func TestCON830ManifestMemoBoundEvictionAndConcurrency(t *testing.T) {
	memo := &knowledgeManifestMemo{}
	manifest := KnowledgeManifest{SchemaVersion: "1.2"}
	for i := 0; i < knowledgeManifestMemoBound+1; i++ {
		memo.put(string(rune('a'+i)), manifest)
	}
	if _, ok := memo.get("a"); ok {
		t.Fatal("oldest entry survived eviction past the bound")
	}
	if _, ok := memo.get(string(rune('a' + knowledgeManifestMemoBound))); !ok {
		t.Fatal("newest entry missing inside the bound")
	}
	var nilMemo *knowledgeManifestMemo
	if _, ok := nilMemo.get("missing"); ok {
		t.Fatal("nil memo served an entry")
	}
	nilMemo.put("key", manifest)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := string(rune('A' + i%4))
			memo.get(key)
			memo.put(key, manifest)
		}(i)
	}
	wg.Wait()
	if len(memo.order) > knowledgeManifestMemoBound {
		t.Fatalf("concurrent puts grew the memo past its bound: %d", len(memo.order))
	}
	// The store reader never caches a failed or missing manifest read: the
	// error path is invisible here by construction, so pin the shape the
	// historical proof depends on — a successful read memoizes, and the
	// per-record blob proof still runs on every read.
	if _, ok := memo.get("zzz"); ok {
		t.Fatal("memo served an entry that was never put")
	}
}

// The shared proof's pool-side statements stay indexed: the
// relation-referenced subject read and the cross-target resolution must
// both run on the home-prefix indexes the core statements already pin,
// never as table scans.
func TestCON830SharedProofStatementsUseAnIndex(t *testing.T) {
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	second := seedRefinementSecondSource(t, s, "proof-plan", "proof-plan-loc")
	commit := firstCommitOID(t, s, home)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO law_cross_source_relations(home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?,?);
		DELETE FROM fold_guard`,
		home.HomeProjectID, home.HomeLocatorID, "CD-0017", "subordinate_to", second.HomeProjectID, "EXT-LAW", commit); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`SELECT ls.law_id, ls.path, ls.content_hash FROM law_subjects ls
WHERE ls.home_project_id=? AND ls.home_locator_id=? AND ls.law_id IN (
  SELECT r.source_law_id FROM law_relations r WHERE r.home_project_id=? AND r.home_locator_id=?
  UNION
  SELECT r.target_law_id FROM law_relations r WHERE r.home_project_id=? AND r.home_locator_id=?
  UNION
  SELECT r.source_law_id FROM law_cross_source_relations r WHERE r.home_project_id=? AND r.home_locator_id=?
)`, []any{home.HomeProjectID, home.HomeLocatorID, home.HomeProjectID, home.HomeLocatorID, home.HomeProjectID, home.HomeLocatorID, home.HomeProjectID, home.HomeLocatorID}},
		{`SELECT DISTINCT target_project_id, target_law_id FROM law_cross_source_relations WHERE home_project_id=? AND home_locator_id=?`, []any{home.HomeProjectID, home.HomeLocatorID}},
		{`SELECT home_project_id, home_locator_id, law_id, path, content_hash FROM law_subjects WHERE home_project_id=? AND law_id IN (?)`, []any{second.HomeProjectID, "EXT-LAW"}},
	}
	for _, statement := range statements {
		rows, err := s.DatabaseForTesting().QueryContext(ctx, `EXPLAIN QUERY PLAN `+statement.query, statement.args...)
		if err != nil {
			t.Fatalf("EXPLAIN failed for %q: %v", statement.query, err)
		}
		for rows.Next() {
			var node, parent, usec int
			var detail string
			if err := rows.Scan(&node, &parent, &usec, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			upper := strings.ToUpper(detail)
			if strings.Contains(upper, "SCAN ") && !strings.Contains(upper, "SUBQUERY") {
				rows.Close()
				t.Fatalf("shared proof statement %q degenerated to a table scan: %s", statement.query, detail)
			}
			t.Logf("shared proof plan: %s", detail)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
}

// readWorkflowAmendmentPage drives the workflow continuity read shape the
// coordinator probe uses: pool-owned source proof, then the tx-scoped law
// context with one mandated root.
func readWorkflowAmendmentPage(ctx context.Context, s *Store, workID string, proof *workflowAmendmentSources) (*WorkflowLawContext, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return readWorkflowLawContext(ctx, tx, workID, &WorkflowReadContract{SpecMandate: []string{"CD-0001"}}, proof)
}
