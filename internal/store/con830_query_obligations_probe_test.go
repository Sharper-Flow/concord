package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// refinementCapturedStatement is one production SQL statement with its
// parameter bindings, captured on the wire the core actually issues.
type refinementCapturedStatement struct {
	query string
	args  []any
}

// refinementStatementProbe wraps a queryer and records every statement the
// tx-scoped core issues through it, so query-plan and fan-out assertions
// run against the production SQL text instead of a copied fragment.
type refinementStatementProbe struct {
	queryer
	statements []refinementCapturedStatement
}

func (p *refinementStatementProbe) record(query string, args []any) {
	captured := make([]any, len(args))
	copy(captured, args)
	p.statements = append(p.statements, refinementCapturedStatement{query: query, args: captured})
}

func (p *refinementStatementProbe) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	p.record(query, args)
	return p.queryer.QueryContext(ctx, query, args...)
}

func (p *refinementStatementProbe) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.record(query, args)
	return p.queryer.QueryRowContext(ctx, query, args...)
}

// coordinatorCON830CountingQueryer counts the SQL calls issued through it.
type coordinatorCON830CountingQueryer struct {
	queryer
	queries int
}

func (q *coordinatorCON830CountingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.queries++
	return q.queryer.QueryContext(ctx, query, args...)
}

func (q *coordinatorCON830CountingQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.queries++
	return q.queryer.QueryRowContext(ctx, query, args...)
}

// seedRefinementScaleSource seeds one auxiliary verified source holding the
// given number of accepted EXT laws plus its consistent watermark, and
// returns the seeded home.
func seedRefinementScaleSource(t *testing.T, s *Store, projectID, locatorID string, subjects int) KnowledgeHome {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "README.md", "acceptance-scale source fixture")
	commit := commitKnowledgeRepo(t, repo, "acceptance-scale source fixture")
	second := KnowledgeHome{HomeProjectID: projectID, HomeLocatorID: locatorID, RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, second)
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < subjects; i++ {
		id := fmt.Sprintf("EXT-%04d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`,
			projectID, locatorID, id, "decision", "accepted", ".concord/docs/decisions/"+id+".md", "Scale endpoint "+id, "sha256:"+strings.Repeat("9", 64), commit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_content_digest,scanned_at,complete,projection_version) VALUES(?,?,?,?,?,?,1,?)`,
		projectID, locatorID, second.HeadRef, commit, seedRefinementDigest(t, second, commit), commit, knowledgeProjectionVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return second
}

// seedRefinementCrossEndpoints declares cross-source outgoing relations
// from the given home roots into the second source's EXT laws.
func seedRefinementCrossEndpoints(t *testing.T, s *Store, home, second KnowledgeHome, commit string, roots []string, per int) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for index, root := range roots {
		for i := 0; i < per; i++ {
			target := fmt.Sprintf("EXT-%04d", (index*per+i)%32)
			if _, err := tx.ExecContext(ctx, `INSERT INTO law_cross_source_relations(home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?,?)`,
				home.HomeProjectID, home.HomeLocatorID, root, "subordinate_to", second.HomeProjectID, target, commit); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// refinementCoreVerification rebuilds the pool verifier's conclusions from
// the committed watermark projection, the same authoritative identity the
// wrapper hands the tx-scoped core.
func refinementCoreVerification(t *testing.T, s *Store, homes []KnowledgeHome) refinementSourceVerification {
	t.Helper()
	verification := refinementSourceVerification{watermarks: make([]KnowledgeSourceWatermark, 0, len(homes))}
	for _, home := range homes {
		var watermark string
		if err := s.DatabaseForTesting().QueryRow(`SELECT scanned_commit_oid FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&watermark); err != nil {
			t.Fatal(err)
		}
		verification.watermarks = append(verification.watermarks, KnowledgeSourceWatermark{ProjectID: home.HomeProjectID, LocatorID: home.HomeLocatorID, Watermark: watermark, Authority: "authoritative"})
		verification.scanned = append(verification.scanned, home.HomeProjectID+"/"+home.HomeLocatorID+"@"+watermark)
	}
	return verification
}

// TestCoordinatorCON830EndpointPlanUsesAnIndex EXPLAINs every statement the
// production core issues for a source-qualified multi-root page over the
// acceptance-scale fixture — watermark identity, root resolution, the
// same-home UNION, the outgoing cross-source read, and the structured
// endpoint lookup — and refuses a whole-table scan of any law projection.
// The plan evidence comes from the captured production SQL text, not from a
// copied fragment that could drift from the shipped query.
func TestCoordinatorCON830EndpointPlanUsesAnIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home := refinementTestStore(t)
	defer s.Close()
	commit := firstCommitOID(t, s, home)
	seedWorkflowAmendmentFanout(t, s, home, commit)
	second := seedRefinementScaleSource(t, s, "refinement-plan", "refinement-plan-loc", 32)
	seedRefinementCrossEndpoints(t, s, home, second, commit, []string{"CD-0017", "CD-0054"}, 8)
	// CD-0058 carries only two same-home outgoing refines, so its bounded
	// page also holds its declared cross-source relations: the captured
	// production path then includes the structured endpoint lookup the
	// page's cross edges require.
	seedRefinementCrossEndpoints(t, s, home, second, commit, []string{"CD-0058"}, 8)
	// Resolve the verifier's conclusions on the pool before the read
	// transaction opens: the store pools one connection, so a pool read
	// inside the open transaction would park forever.
	verification := refinementCoreVerification(t, s, []KnowledgeHome{home, second})
	tx, err := s.DatabaseForTesting().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	probe := &refinementStatementProbe{queryer: tx}
	roots := []string{home.HomeProjectID + "/CD-0017", home.HomeProjectID + "/CD-0054"}
	if _, err := queryKnowledgeRefinementContext(ctx, probe, KnowledgeRefinementContextRequest{}, []KnowledgeHome{home, second}, roots, refinementContextMaxLimit, verification); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := queryKnowledgeRefinementContext(ctx, probe, KnowledgeRefinementContextRequest{}, []KnowledgeHome{home, second}, []string{home.HomeProjectID + "/CD-0058"}, refinementContextMaxLimit, verification); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if len(probe.statements) == 0 {
		t.Fatal("fixture captured no production statements")
	}
	seen := map[string]bool{}
	for _, statement := range probe.statements {
		fragments := map[string]bool{}
		for _, fragment := range []string{"law_subjects", "law_relations", "law_cross_source_relations", "knowledge_index_watermark", "project_locators"} {
			if strings.Contains(statement.query, fragment) {
				fragments[fragment] = true
			}
		}
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
				t.Fatalf("production statement %q degenerated to a table scan: %s", statement.query, detail)
			}
			t.Logf("plan [%s]: %s", strings.Join(keysOf(fragments), ","), detail)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		seen[strings.TrimSpace(statement.query)] = true
	}
	for _, required := range []string{"law_relations r", "law_cross_source_relations r", "knowledge_index_watermark", "project_locators", "FROM law_subjects WHERE home_project_id=? AND law_id IN"} {
		found := false
		for query := range seen {
			if strings.Contains(query, required) {
				found = true
			}
		}
		if !found {
			t.Fatalf("captured production path never issued a statement matching %q", required)
		}
	}
}

func keysOf(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestCoordinatorCON830QualifiedRootsHaveNoPerRootSQLFanout pins statement
// count against root-set growth: resolving three qualified roots of one
// Project issues no more SQL than resolving one.
func TestCoordinatorCON830QualifiedRootsHaveNoPerRootSQLFanout(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	q := &coordinatorCON830CountingQueryer{queryer: s.DatabaseForTesting()}
	if _, err := resolveRefinementRoots(context.Background(), q, []string{home.HomeProjectID + "/CD-0017"}, []KnowledgeHome{home}); err != nil {
		t.Fatal(err)
	}
	one := q.queries
	q.queries = 0
	if _, err := resolveRefinementRoots(context.Background(), q, []string{home.HomeProjectID + "/CD-0017", home.HomeProjectID + "/CD-0054", home.HomeProjectID + "/CD-0058"}, []KnowledgeHome{home}); err != nil {
		t.Fatal(err)
	}
	if q.queries > one {
		t.Fatalf("qualified-root SQL calls grow per root for the same source: one root=%d, three roots=%d", one, q.queries)
	}
}

// TestCoordinatorCON830LatencyFixtureUsesAcceptanceScale pins the latency
// fixture to the corpus dataset multiplier: the home must hold at least
// knowledgeBodyBenchmarkRows subject rows over as many distinct laws, so
// measured P50/P99 and output sizes describe the synthetic acceptance
// population rather than a 42-law fixture.
func TestCoordinatorCON830LatencyFixtureUsesAcceptanceScale(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	seedWorkflowAmendmentFanout(t, s, home, firstCommitOID(t, s, home))
	var total, distinct int
	if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(*),COUNT(DISTINCT law_id) FROM law_subjects WHERE home_project_id=? AND home_locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&total, &distinct); err != nil {
		t.Fatal(err)
	}
	if total < knowledgeBodyBenchmarkRows || distinct < knowledgeBodyBenchmarkRows {
		t.Fatalf("latency fixture has %d subject rows and %d distinct laws, not the 10x synthetic acceptance population of %d", total, distinct, knowledgeBodyBenchmarkRows)
	}
}
