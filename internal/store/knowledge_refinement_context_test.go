package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// seedRefinementContextLaw seeds the authored CD-0017/CD-0054/CD-0058
// refinement chain plus one superseded refiner, exactly as the git-derived
// projection would fold the three accepted record shards.
func seedRefinementContextLaw(t *testing.T, s *Store, home KnowledgeHome, commit string) {
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
	subjects := []struct {
		id, kind, status, title string
	}{
		{"CD-0017", "decision", "accepted", "Typed workers and model routing"},
		{"CD-0054", "decision", "accepted", "Routing-policy authority is load-time host state"},
		{"CD-0058", "decision", "accepted", "Concord performs no model routing"},
		{"CD-0999", "decision", "superseded", "Superseded stray refiner"},
	}
	for _, subject := range subjects {
		hash := "sha256:" + strings.Repeat(fmt.Sprintf("%x", len(subject.id)%16), 64)[:64]
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, subject.id, subject.kind, subject.status, ".concord/docs/decisions/"+subject.id+".md", subject.title, hash, commit); err != nil {
			t.Fatal(err)
		}
	}
	relations := []struct {
		source, kind, target string
	}{
		{"CD-0054", "refines", "CD-0017"},
		{"CD-0058", "refines", "CD-0054"},
		{"CD-0058", "refines", "CD-0017"},
		{"CD-0999", "refines", "CD-0017"},
	}
	for _, relation := range relations {
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, relation.source, relation.kind, relation.target, commit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_content_digest,scanned_at,complete,projection_version) VALUES(?,?,?,?,?,?,1,?)`,
		home.HomeProjectID, home.HomeLocatorID, home.HeadRef, commit, seedRefinementDigest(t, home, commit), commit, knowledgeProjectionVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedRefinementDigest(t *testing.T, home KnowledgeHome, commit string) string {
	t.Helper()
	digest, err := knowledgeContentDigest(context.Background(), home, commit)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func refinementTestStore(t *testing.T) (*Store, KnowledgeHome) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "README.md", "refinement context fixture")
	commit := commitKnowledgeRepo(t, repo, "refinement context fixture")
	home := KnowledgeHome{HomeProjectID: "refinement-project", HomeLocatorID: "refinement-locator", RepoPath: repo, HeadRef: "HEAD"}
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "refinement-product", home)
	seedRefinementContextLaw(t, s, home, commit)
	return s, home
}

// T1 authored chain: the three authored refines edges surface as direct
// incoming accepted refinements with qualified endpoints and source
// identity.
func TestRefinementContextReturnsAuthoredChain(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	result, err := s.QueryKnowledgeRefinementContext(context.Background(), KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if result.Authority != "authoritative" {
		t.Fatalf("authority=%s", result.Authority)
	}
	var incoming []string
	for _, edge := range result.Edges {
		if edge.RootID == "CD-0017" && edge.Direction == "incoming" {
			incoming = append(incoming, edge.EndpointLawID)
			if edge.EndpointStatus != "accepted" {
				t.Fatalf("endpoint %s status=%s", edge.EndpointLawID, edge.EndpointStatus)
			}
			if edge.EndpointProjectID != home.HomeProjectID || edge.EndpointLocatorID != home.HomeLocatorID || edge.EndpointTitle == "" || edge.EndpointContentHash == "" || edge.ScannedCommitOID == "" {
				t.Fatalf("endpoint %s missing qualification: %+v", edge.EndpointLawID, edge)
			}
		}
	}
	if len(incoming) != 2 || incoming[0] != "CD-0054" || incoming[1] != "CD-0058" {
		t.Fatalf("incoming refinements of CD-0017 = %v", incoming)
	}
}

// T2 no inference: a root sees only direct one-hop edges; the
// CD-0058→CD-0054 edge never surfaces under CD-0017, and superseded
// refiners never surface as accepted amendments.
func TestRefinementContextInfersNoTransitiveOrUnacceptedEdges(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	result, err := s.QueryKnowledgeRefinementContext(context.Background(), KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Edges) != 2 {
		t.Fatalf("one-hop result for CD-0017 = %d edges, want exactly the two direct accepted refines", len(result.Edges))
	}
	for _, edge := range result.Edges {
		if edge.Direction != "incoming" || edge.Kind != "refines" {
			t.Fatalf("unauthored edge shape surfaced under CD-0017: %+v", edge)
		}
		if edge.EndpointLawID == "CD-0999" {
			t.Fatalf("superseded refiner surfaced as accepted amendment: %+v", edge)
		}
	}
	// CD-0058 declares outgoing refines toward both CD-0054 and CD-0017.
	outgoing, err := s.QueryKnowledgeRefinementContext(context.Background(), KnowledgeRefinementContextRequest{Roots: []string{"CD-0058"}, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, edge := range outgoing.Edges {
		if edge.Direction == "outgoing" && edge.Kind == "refines" {
			count++
			if edge.SourceProjectID != home.HomeProjectID {
				t.Fatalf("edge missing source identity: %+v", edge)
			}
		}
	}
	if count != 2 {
		t.Fatalf("outgoing refines from CD-0058 = %d, want 2", count)
	}
}

// T8/T9 bounds and snapshot-bound continuation: a bounded page names
// incomplete roots and continues; a changed relation snapshot refuses the
// outstanding cursor.
func TestRefinementContextPagesWithSnapshotBoundCursor(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	ctx := context.Background()
	first, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Limit: 1, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Edges) != 1 || first.NextCursor == nil {
		t.Fatalf("first page edges=%d cursor=%v", len(first.Edges), first.NextCursor)
	}
	if len(first.IncompleteRoots) != 1 || first.IncompleteRoots[0] != "CD-0017" {
		t.Fatalf("incomplete roots=%v", first.IncompleteRoots)
	}
	second, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Limit: 1, Cursor: *first.NextCursor, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Edges) != 1 || second.Edges[0].EndpointLawID == first.Edges[0].EndpointLawID {
		t.Fatalf("second page did not advance: %+v", second.Edges)
	}
	// Snapshot drift: an authored relation change between pages refuses
	// continuation instead of splicing two snapshots.
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`,
		home.HomeProjectID, home.HomeLocatorID, "CD-0015", "decision", "accepted", ".concord/docs/decisions/CD-0015-typed-law-relations.md", "Typed law relations", "sha256:"+strings.Repeat("1", 64), first.Edges[0].ScannedCommitOID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?)`,
		home.HomeProjectID, home.HomeLocatorID, "CD-0017", "subordinate_to", "CD-0015", first.Edges[0].ScannedCommitOID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Limit: 1, Cursor: *first.NextCursor, Home: home})
	assertFailureKind(t, err, KindInvalidCursor)
}

func TestRefinementContextRejectsUnboundedLimit(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	for _, limit := range []int{-1, 33, 100} {
		_, err := s.QueryKnowledgeRefinementContext(context.Background(), KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Limit: limit, Home: home})
		assertFailureKind(t, err, KindInvalidFilter)
	}
}

// seedRefinementSecondSource seeds one auxiliary verified source holding
// CD-0017, its own accepted refiner SRC-2, and the cross-source endpoint
// law EXT-LAW, then returns the seeded home.
func seedRefinementSecondSource(t *testing.T, s *Store, projectID, locatorID string) KnowledgeHome {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "README.md", "second source fixture")
	commit := commitKnowledgeRepo(t, repo, "second source fixture")
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
	for _, subject := range []struct {
		id, kind, status, title string
	}{
		{"CD-0017", "decision", "accepted", "Foreign typed workers"},
		{"SRC-2", "decision", "accepted", "Second-source refiner"},
		{"EXT-LAW", "decision", "accepted", "Cross-source endpoint"},
	} {
		hash := "sha256:" + strings.Repeat(fmt.Sprintf("%x", len(subject.id)%16), 64)[:64]
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`,
			projectID, locatorID, subject.id, subject.kind, subject.status, ".concord/docs/decisions/"+subject.id+".md", subject.title, hash, commit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?)`,
		projectID, locatorID, "SRC-2", "refines", "CD-0017", commit); err != nil {
		t.Fatal(err)
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

// Coordinator probe promotion (obs:78fdea53ed5a2e29): a source-qualified
// root resolves through the Project's canonical locator and returns the
// same one-hop graph as the bare root, never an authoritative empty graph.
func TestRefinementContextResolvesQualifiedRoot(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	ctx := context.Background()
	bare, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	qualified, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{home.HomeProjectID + "/CD-0017"}, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.Edges) != 2 || len(qualified.Edges) != len(bare.Edges) {
		t.Fatalf("bare root returns %d edges, qualified root returns %d (%s authority); want 2 for both", len(bare.Edges), len(qualified.Edges), qualified.Authority)
	}
	if qualified.Authority != "authoritative" || len(qualified.Omissions) != 0 {
		t.Fatalf("qualified root authority=%s omissions=%v", qualified.Authority, qualified.Omissions)
	}
	if qualified.Roots[0] != home.HomeProjectID+"/CD-0017" {
		t.Fatalf("qualified root echoed as %v", qualified.Roots)
	}
}

// A source-qualified root reads only its own source. A bare root whose ID
// is held by two sources of the set refuses as ambiguous (CD-0200 D4):
// the set never federates same-ID subjects into one unspecified graph, so
// the caller must qualify the root (CON-830).
func TestRefinementContextQualifiedRootScopesToItsSource(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	ctx := context.Background()
	second := seedRefinementSecondSource(t, s, "refinement-second", "refinement-second-loc")
	set := []KnowledgeHome{home, second}
	countIncoming := func(result KnowledgeRefinementContextResult) map[string]int {
		found := map[string]int{}
		for _, edge := range result.Edges {
			if edge.Direction == "incoming" {
				found[edge.EndpointProjectID+"/"+edge.EndpointLawID]++
			}
		}
		return found
	}
	_, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Sources: set})
	assertFailureKind(t, err, KindAmbiguousScope)
	qualifiedHome, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{home.HomeProjectID + "/CD-0017"}, Sources: set})
	if err != nil {
		t.Fatal(err)
	}
	if found := countIncoming(qualifiedHome); len(found) != 2 || found[second.HomeProjectID+"/SRC-2"] != 0 {
		t.Fatalf("qualified home incoming leaked the second source: %v", found)
	}
	qualifiedSecond, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{second.HomeProjectID + "/CD-0017"}, Sources: set})
	if err != nil {
		t.Fatal(err)
	}
	if found := countIncoming(qualifiedSecond); len(found) != 1 || found[second.HomeProjectID+"/SRC-2"] != 1 {
		t.Fatalf("qualified second incoming = %v", found)
	}
}

// Qualified-root refusals mirror the existing owner: an unknown Project
// cannot resolve, and a Project whose canonical home sits outside the
// requested source set never answers through another source.
func TestRefinementContextQualifiedRootRefusals(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	ctx := context.Background()
	_, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{"refinement-unknown/CD-0017"}, Home: home})
	assertFailureKind(t, err, KindKnowledgeUnavailable)
	outside := seedRefinementSecondSource(t, s, "refinement-outside", "refinement-outside-loc")
	_, err = s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{outside.HomeProjectID + "/CD-0017"}, Home: home})
	assertFailureKind(t, err, KindUnknownScope)
}

// Cross-source endpoints are inspected, never trusted: an endpoint inside
// the set resolves its identity, an endpoint outside the set is named as an
// omission, and an endpoint held by two locators of one Project refuses to
// pick either. An incomplete graph never reads as authoritative.
func TestRefinementContextInspectsCrossSourceEndpoints(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	ctx := context.Background()
	solo := seedRefinementSecondSource(t, s, "refinement-solo", "refinement-solo-loc")
	twinA := seedRefinementSecondSource(t, s, "refinement-twin", "refinement-twin-a")
	twinB := seedRefinementSecondSource(t, s, "refinement-twin", "refinement-twin-b")
	commit := firstCommitOID(t, s, home)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []struct {
		kind, targetProject, targetLaw string
	}{
		{"subordinate_to", solo.HomeProjectID, "EXT-LAW"},
		{"subordinate_to", twinA.HomeProjectID, "EXT-LAW"},
		{"subordinate_to", "refinement-unregistered", "GHOST-LAW"},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_cross_source_relations(home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?,?)`,
			home.HomeProjectID, home.HomeLocatorID, "CD-0017", rel.kind, rel.targetProject, rel.targetLaw, commit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{home.HomeProjectID + "/CD-0017"}, Sources: []KnowledgeHome{home, solo, twinA, twinB}})
	if err != nil {
		t.Fatal(err)
	}
	var resolved, ghost bool
	for _, edge := range result.Edges {
		if edge.Direction != "outgoing" {
			continue
		}
		switch {
		case edge.EndpointProjectID == solo.HomeProjectID && edge.EndpointLawID == "EXT-LAW":
			resolved = edge.EndpointTitle == "Cross-source endpoint" && edge.EndpointLocatorID != "" && edge.EndpointContentHash != "" && edge.EndpointStatus == "accepted"
		case edge.EndpointLawID == "GHOST-LAW":
			ghost = true
		}
	}
	if !resolved {
		t.Fatalf("in-set cross-source endpoint did not resolve its identity: %+v", result.Edges)
	}
	if !ghost {
		t.Fatalf("out-of-set cross-source endpoint missing from the page: %+v", result.Edges)
	}
	joined := strings.Join(result.Omissions, "\n")
	if !strings.Contains(joined, "endpoint_source_not_in_set:refinement-unregistered/GHOST-LAW") {
		t.Fatalf("out-of-set endpoint not named as an omission: %v", result.Omissions)
	}
	if !strings.Contains(joined, "endpoint_identity_ambiguous:refinement-twin/EXT-LAW") {
		t.Fatalf("ambiguous endpoint identity not named as an omission: %v", result.Omissions)
	}
	if result.Authority == "authoritative" {
		t.Fatal("incomplete cross-source graph claimed authoritative authority")
	}
}

// Coordinator probe promotion (CON-830): a bare root held by two sources
// of the requested set refuses as ambiguous instead of returning a
// federated same-ID graph with no error (CD-0200 D4).
func TestRefinementContextAmbiguousBareRootRefuses(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	second := seedRefinementSecondSource(t, s, "refinement-second", "refinement-second-loc")
	_, err := s.QueryKnowledgeRefinementContext(context.Background(), KnowledgeRefinementContextRequest{Roots: []string{"CD-0017"}, Sources: []KnowledgeHome{home, second}})
	assertFailureKind(t, err, KindAmbiguousScope)
}

// Coordinator probe promotion (CON-830): two roots whose outgoing
// cross-source relations target the same endpoint both carry the resolved
// endpoint metadata; enrichment deduplicates the lookup, never the edges.
func TestRefinementContextRepeatedCrossEndpointMetadata(t *testing.T) {
	t.Parallel()
	s, home := refinementTestStore(t)
	defer s.Close()
	second := seedRefinementSecondSource(t, s, "refinement-second", "refinement-second-loc")
	ctx := context.Background()
	commit := firstCommitOID(t, s, home)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"CD-0017", "CD-0054"} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_cross_source_relations(home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id,scanned_commit_oid) VALUES(?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, root, "subordinate_to", second.HomeProjectID, "EXT-LAW", commit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Roots: []string{home.HomeProjectID + "/CD-0017", home.HomeProjectID + "/CD-0054"}, Sources: []KnowledgeHome{home, second}})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, edge := range result.Edges {
		if edge.EndpointProjectID != second.HomeProjectID || edge.EndpointLawID != "EXT-LAW" {
			continue
		}
		count++
		if edge.EndpointLocatorID != second.HomeLocatorID || edge.EndpointTitle == "" || edge.EndpointContentHash == "" {
			t.Errorf("shared cross-source endpoint lost metadata on root %s: %+v", edge.RootID, edge)
		}
	}
	if count != 2 {
		t.Fatalf("cross-source edge count = %d, want 2", count)
	}
}

// firstCommitOID reads the seeded watermark commit of one home.
func firstCommitOID(t *testing.T, s *Store, home KnowledgeHome) string {
	t.Helper()
	var commit string
	if err := s.DatabaseForTesting().QueryRow(`SELECT scanned_commit_oid FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&commit); err != nil {
		t.Fatal(err)
	}
	return commit
}
