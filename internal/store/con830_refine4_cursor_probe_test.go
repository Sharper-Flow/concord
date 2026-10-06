package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// A degraded participating source's scanned identity must bind the cursor snapshot. The
// source advances with unchanged relation counts, so only the projection
// identity — not the counts — can refuse the outstanding continuation.
func TestCoordinatorRefine4DegradedCursorBindsScannedSource(t *testing.T) {
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	sources := []KnowledgeHome{home}
	verification := refinementCoreVerification(t, s, sources)
	verification.watermarks[0].Authority = "degraded"
	verification.degraded = true
	verification.omissions = []string{"source_verification_failed:" + home.HomeProjectID}
	read := func(cursor string) (KnowledgeRefinementContextResult, error) {
		tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return queryKnowledgeRefinementContext(ctx, tx, KnowledgeRefinementContextRequest{AllowDegraded: true, Cursor: cursor}, sources, []string{home.HomeProjectID + "/CD-0017"}, 1, verification)
	}
	first, err := read("")
	if err != nil || first.NextCursor == nil || first.Authority == "authoritative" {
		t.Fatalf("first degraded page: err=%v result=%+v", err, first)
	}
	writeKnowledgeFile(t, home.RepoPath, "README.md", "advanced degraded source fixture")
	commit := commitKnowledgeRepo(t, home.RepoPath, "advance degraded source fixture")
	digest := seedRefinementDigest(t, home, commit)
	mutateRefine4Probe(t, s, []refine4ProbeStatement{
		{"UPDATE knowledge_index_watermark SET scanned_commit_oid=?, scanned_content_digest=? WHERE home_project_id=? AND home_locator_id=?", []any{commit, digest, home.HomeProjectID, home.HomeLocatorID}},
		{"UPDATE law_relations SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?", []any{commit, home.HomeProjectID, home.HomeLocatorID}},
		{"UPDATE law_subjects SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?", []any{commit, home.HomeProjectID, home.HomeLocatorID}},
	})
	second, err := read(*first.NextCursor)
	if err == nil {
		t.Fatalf("continuation accepted changed scanned commit/content identity with unchanged edge counts: %+v", second)
	}
}

// Client-owned count hints cannot suppress the incomplete-root marker. Incomplete roots
// derive from the read snapshot and the keyset position, never from a
// cumulative count carried inside the opaque cursor.
func TestCoordinatorRefine4CursorCountsCannotHideIncompleteRoot(t *testing.T) {
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	commit := firstCommitOID(t, s, home)
	// The seeded edge joins two real committed subjects, so the shared
	// current-source object proof stays green and the probe exercises the
	// cursor's count-hint immunity, not an unprovable endpoint.
	mutateRefine4Probe(t, s, []refine4ProbeStatement{
		{"INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,'CD-0017','subordinate_to','CD-0058',?)", []any{home.HomeProjectID, home.HomeLocatorID, commit}},
	})
	req := KnowledgeRefinementContextRequest{Sources: []KnowledgeHome{home}, Roots: []string{home.HomeProjectID + "/CD-0017"}, Limit: 1}
	first, err := s.QueryKnowledgeRefinementContext(ctx, req)
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page: err=%v result=%+v", err, first)
	}
	if len(first.IncompleteRoots) != 1 || first.IncompleteRoots[0] != home.HomeProjectID+"/CD-0017" {
		t.Fatalf("first page incomplete roots = %v, want the paged root", first.IncompleteRoots)
	}
	raw, err := base64.RawURLEncoding.DecodeString(*first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	var cursor map[string]any
	if err := json.Unmarshal(raw, &cursor); err != nil {
		t.Fatal(err)
	}
	for _, hint := range []any{[]any{1000000}, []any{-1}, []any{0}, []any{math.MaxInt64}, "counts", nil} {
		tampered := map[string]any{}
		for key, value := range cursor {
			tampered[key] = value
		}
		tampered["root_counts"] = hint
		encoded, err := json.Marshal(tampered)
		if err != nil {
			t.Fatal(err)
		}
		req.Cursor = base64.RawURLEncoding.EncodeToString(encoded)
		second, err := s.QueryKnowledgeRefinementContext(ctx, req)
		if err != nil {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidCursor {
				t.Fatalf("cursor count hint %v returned an unexpected failure: %v", hint, err)
			}
			continue
		}
		if len(second.Edges) != 1 || second.NextCursor == nil || len(second.IncompleteRoots) != 1 || second.IncompleteRoots[0] != req.Roots[0] {
			t.Fatalf("cursor count hint %v changed the bounded page or its incomplete-root marker: %+v", hint, second)
		}
	}
}

// A participating source whose watermark projection vanishes between pages
// changes the snapshot identity the cursor bound; continuation refuses even
// on a degraded read, because an absent identity is a different snapshot.
func TestCoordinatorRefine4MissingWatermarkTransitionRefusesContinuation(t *testing.T) {
	assertRefine4WatermarkTransitionRefuses(t, "DELETE FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=?", "a source whose watermark projection vanished between pages")
}

// A watermark that keeps its commit but loses completeness changes the
// projection identity the cursor bound; the outstanding continuation
// refuses instead of riding a projection that declares itself unfinished.
func TestCoordinatorRefine4IncompleteProjectionTransitionRefusesContinuation(t *testing.T) {
	assertRefine4WatermarkTransitionRefuses(t, "UPDATE knowledge_index_watermark SET complete=0 WHERE home_project_id=? AND home_locator_id=?", "a projection that lost completeness between pages")
}

func assertRefine4WatermarkTransitionRefuses(t *testing.T, mutation, detail string) {
	t.Helper()
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	sources := []KnowledgeHome{home}
	verification := refinementCoreVerification(t, s, sources)
	read := func(cursor string) (KnowledgeRefinementContextResult, error) {
		tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return queryKnowledgeRefinementContext(ctx, tx, KnowledgeRefinementContextRequest{AllowDegraded: true, Cursor: cursor}, sources, []string{home.HomeProjectID + "/CD-0017"}, 1, verification)
	}
	first, err := read("")
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page: err=%v result=%+v", err, first)
	}
	mutateRefine4Probe(t, s, []refine4ProbeStatement{
		{mutation, []any{home.HomeProjectID, home.HomeLocatorID}},
	})
	if _, err := read(*first.NextCursor); err == nil {
		t.Fatalf("continuation accepted %s", detail)
	}
}

// An earlier source's drift must not stop identity collection: the later
// participating source keeps its identity in the cursor snapshot even when
// the first source drifts on every read, so a change to the later source
// still refuses the outstanding continuation.
func TestCoordinatorRefine4EarlyDriftStillBindsLaterSourceIdentity(t *testing.T) {
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	second := seedRefinementSecondSource(t, s, "refine4-drift", "refine4-drift-loc")
	sources := []KnowledgeHome{home, second}
	verification := refinementCoreVerification(t, s, sources)
	// The first source carries a proof the projection never held, so it
	// drifts on every read while its own identity stays unchanged; the
	// second source is degraded, so only identity collection binds it.
	verification.watermarks[0].Watermark = "0000000000000000000000000000000000000000"
	verification.watermarks[1].Authority = "degraded"
	verification.degraded = true
	verification.omissions = append(verification.omissions, "knowledge_source_degraded:"+second.HomeProjectID+"/"+second.HomeLocatorID)
	read := func(cursor string) (KnowledgeRefinementContextResult, error) {
		tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return queryKnowledgeRefinementContext(ctx, tx, KnowledgeRefinementContextRequest{AllowDegraded: true, Cursor: cursor}, sources, []string{home.HomeProjectID + "/CD-0017"}, 1, verification)
	}
	first, err := read("")
	if err != nil || first.NextCursor == nil || first.Authority == "authoritative" {
		t.Fatalf("first degraded page: err=%v result=%+v", err, first)
	}
	writeKnowledgeFile(t, second.RepoPath, "README.md", "advance later degraded source")
	commit := commitKnowledgeRepo(t, second.RepoPath, "advance later degraded source")
	digest := seedRefinementDigest(t, second, commit)
	mutateRefine4Probe(t, s, []refine4ProbeStatement{
		{"UPDATE knowledge_index_watermark SET scanned_commit_oid=?, scanned_content_digest=? WHERE home_project_id=? AND home_locator_id=?", []any{commit, digest, second.HomeProjectID, second.HomeLocatorID}},
		{"UPDATE law_relations SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?", []any{commit, second.HomeProjectID, second.HomeLocatorID}},
		{"UPDATE law_subjects SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?", []any{commit, second.HomeProjectID, second.HomeLocatorID}},
	})
	if _, err := read(*first.NextCursor); err == nil {
		t.Fatal("continuation accepted a later source identity change behind an earlier drift")
	}
}

// Lossless bounded pagination across roots: every limit from 1 to the 32-edge
// bound pages the same 42-edge population without losing or repeating an
// edge, and the terminal page names no incomplete root.
func TestCoordinatorRefine4PaginationIsLosslessAcrossLimits(t *testing.T) {
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	seedWorkflowAmendmentFanout(t, s, home, firstCommitOID(t, s, home))
	const total = 42 // 22 edges under CD-0017, 19 incoming plus 1 outgoing under CD-0054
	for limit := 1; limit <= refinementContextMaxLimit; limit++ {
		seen := map[string]bool{}
		cursor := ""
		pages := 0
		for {
			page, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Sources: []KnowledgeHome{home}, Roots: []string{"CD-0017", "CD-0054"}, Limit: limit, Cursor: cursor})
			if err != nil {
				t.Fatalf("limit %d: %v", limit, err)
			}
			if len(page.Edges) > limit {
				t.Fatalf("limit %d page returned %d edges", limit, len(page.Edges))
			}
			for _, edge := range page.Edges {
				key := refinementEdgeKey(edge)
				joined := strings.Join(key[:], "\x00")
				if seen[joined] {
					t.Fatalf("limit %d repeated edge %v", limit, key)
				}
				seen[joined] = true
			}
			pages++
			if page.NextCursor == nil {
				if len(page.IncompleteRoots) != 0 {
					t.Fatalf("limit %d terminal page named incomplete roots %v", limit, page.IncompleteRoots)
				}
				break
			}
			if pages > 64 {
				t.Fatalf("limit %d did not terminate", limit)
			}
			cursor = *page.NextCursor
		}
		if len(seen) != total {
			t.Fatalf("limit %d delivered %d distinct edges, want %d", limit, len(seen), total)
		}
		if want := (total + limit - 1) / limit; pages != want {
			t.Fatalf("limit %d used %d pages, want %d", limit, pages, want)
		}
	}
}

type refine4ProbeStatement struct {
	sql  string
	args []any
}

func mutateRefine4Probe(t *testing.T, s *Store, statements []refine4ProbeStatement) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO fold_guard(active) VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec("DELETE FROM fold_guard"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
