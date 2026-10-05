package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func readCON830AcceptanceManifest(t *testing.T, home KnowledgeHome) KnowledgeManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home.RepoPath, filepath.FromSlash(knowledgeManifestPath)))
	if err != nil {
		t.Fatal(err)
	}
	var manifest KnowledgeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func commitCON830AcceptanceManifest(t *testing.T, home KnowledgeHome, manifest KnowledgeManifest) string {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, home.RepoPath, knowledgeManifestPath, string(raw)+"\n")
	return commitKnowledgeRepo(t, home.RepoPath, "commit synthetic acceptance metadata")
}

func con830DerivedProjectionSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	tables := append([]string{}, derivedKnowledgeClearOrder...)
	tables = append(tables, "archived_work", "archived_work_products", "archived_work_projects", "archived_work_components", "archived_work_domains", "archived_work_tags", "knowledge_kind_coverage", "knowledge_index_watermark")
	var snapshot []string
	for _, table := range tables {
		rows, err := s.DatabaseForTesting().Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var serialized []string
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			serialized = append(serialized, string(raw))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(serialized)
		snapshot = append(snapshot, table+":"+strings.Join(serialized, "\n"))
	}
	return strings.Join(snapshot, "\n")
}

func TestCON830ProseDoesNotAuthorAmendmentMetadata(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	manifest := readCON830AcceptanceManifest(t, home)
	manifest.Records[1].LawRelations = nil
	commitCON830AcceptanceManifest(t, home, manifest)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	before := readCON830AcceptanceManifest(t, home)
	result, err := s.QueryQ10(ctx, Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001", IncludeAmendmentContext: true})
	if err != nil {
		t.Fatal(err)
	}
	page := result.Result.CurrentAmendmentContext
	if page == nil || page.Authority != "authoritative" || len(page.Edges) != 0 || len(page.SourceWatermarks) != 1 || page.SourceWatermarks[0].Authority != "authoritative" {
		t.Fatalf("prose-only refining rule did not yield a verified empty graph: %+v", page)
	}
	if after := readCON830AcceptanceManifest(t, home); !reflect.DeepEqual(after, before) {
		t.Fatal("contextual read authored or modified knowledge metadata")
	}
}

func TestCON830TwoEdgeCD0121ShapeKeepsWholeRecordsAccepted(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	manifest := readCON830AcceptanceManifest(t, home)
	secondRoot := manifest.Records[0]
	secondRoot.ID, secondRoot.Path = "CD-0089", ".concord/docs/decisions/CD-0089.md"
	writeKnowledgeFile(t, home.RepoPath, secondRoot.Path, "root storage rule\n")
	manifest.Records[0].ID = "CD-0010"
	manifest.Records[1].ID = "CD-0121"
	manifest.Records[1].LawRelations = []KnowledgeRelation{{Kind: "refines", TargetID: "CD-0010"}, {Kind: "refines", TargetID: "CD-0089"}}
	manifest.Records = append(manifest.Records, secondRoot)
	commitCON830AcceptanceManifest(t, home, manifest)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	page, err := s.QueryKnowledgeRefinementContext(ctx, KnowledgeRefinementContextRequest{Product: "amendment-product", Home: home, Roots: []string{home.HomeProjectID + "/CD-0121"}})
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, edge := range page.Edges {
		if edge.Kind != "refines" || edge.Direction != "outgoing" || edge.EndpointStatus != "accepted" || edge.EndpointContentHash == "" || edge.EndpointProjectID != home.HomeProjectID {
			t.Fatalf("two-edge fixture changed whole-record status or qualification: %+v", edge)
		}
		targets = append(targets, edge.EndpointLawID)
	}
	if !reflect.DeepEqual(targets, []string{"CD-0010", "CD-0089"}) {
		t.Fatalf("CD-0121-shaped outgoing targets=%v", targets)
	}
	if after := readCON830AcceptanceManifest(t, home); !reflect.DeepEqual(after, manifest) {
		t.Fatal("two-edge contextual read changed accepted record metadata")
	}
}

func TestCON830HistoricalCommitAIsSeparateFromFreshCurrentCommitB(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	req := Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001"}
	historical, err := s.QueryQ10(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readCON830AcceptanceManifest(t, home)
	bodyB := "Current refining storage rule at commit B.\n"
	writeKnowledgeFile(t, home.RepoPath, manifest.Records[1].Path, bodyB)
	sum := sha256.Sum256([]byte(bodyB))
	manifest.Records[1].SHA256 = "sha256:" + hex.EncodeToString(sum[:])
	commitB := commitCON830AcceptanceManifest(t, home, manifest)
	plain, err := s.QueryQ10(ctx, req)
	if err != nil || plain.Result.CurrentAmendmentContext != nil || !reflect.DeepEqual(plain.Note, historical.Note) {
		t.Fatalf("historical-only read changed before contextual demand: result=%+v err=%v", plain, err)
	}
	req.IncludeAmendmentContext = true
	current, err := s.QueryQ10(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current.Note, historical.Note) || current.Note.CommitOID == commitB {
		t.Fatalf("current context replaced historical commit A: historical=%+v current=%+v", historical.Note, current.Note)
	}
	page := current.Result.CurrentAmendmentContext
	if page == nil || page.Authority != "authoritative" || len(page.Edges) != 1 {
		t.Fatalf("missing independently verified current page: %+v", page)
	}
	edge := page.Edges[0]
	if edge.ScannedCommitOID != commitB || edge.EndpointContentHash != manifest.Records[1].SHA256 || len(page.SourceWatermarks) != 1 || page.SourceWatermarks[0].Watermark != commitB {
		t.Fatalf("current edge/blob/source proof did not bind commit B: edge=%+v sources=%+v", edge, page.SourceWatermarks)
	}
}

func TestCON830InvalidMetadataDeltasPreserveAllPriorProjections(t *testing.T) {
	for name, mutate := range map[string]func(*KnowledgeManifest){
		"endpoint":      func(m *KnowledgeManifest) { m.Records[1].LawRelations[0].TargetID = "CD-9999" },
		"relation_type": func(m *KnowledgeManifest) { m.Records[1].LawRelations[0].Kind = "binds" },
		"endpoint_type": func(m *KnowledgeManifest) {
			m.SupportedKinds = append(m.SupportedKinds, "lesson")
			m.IndexedKinds = append(m.IndexedKinds, "lesson")
			m.Records[0].Kind, m.Records[0].Status = "lesson", "published"
			m.Records[0].Authority = KnowledgeAuthority{Tier: "derived"}
			m.Records[0].HomeDomainID = ""
			m.Records[0].ProductWideRationale = ""
			m.Records[0].Path = ".concord/docs/lessons/lesson-root.md"
		},
		"self": func(m *KnowledgeManifest) { m.Records[1].LawRelations[0].TargetID = m.Records[1].ID },
		"duplicate": func(m *KnowledgeManifest) {
			m.Records[1].LawRelations = append(m.Records[1].LawRelations, m.Records[1].LawRelations[0])
		},
		"cycle": func(m *KnowledgeManifest) {
			m.Records[0].LawRelations = []KnowledgeRelation{{Kind: "refines", TargetID: m.Records[1].ID}}
		},
		"successor": func(m *KnowledgeManifest) { m.Records[1].Status, m.Records[1].Successor = "superseded", "CD-9999" },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			defer s.Close()
			home := seedAmendmentContextHome(t)
			authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
			if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			before := con830DerivedProjectionSnapshot(t, s)
			manifest := readCON830AcceptanceManifest(t, home)
			mutate(&manifest)
			writeKnowledgeFile(t, home.RepoPath, manifest.Records[0].Path, "root storage rule\n")
			commitCON830AcceptanceManifest(t, home, manifest)
			err := s.RebuildKnowledgeIndex(ctx, home)
			if err == nil {
				t.Fatal("invalid metadata delta rebuilt successfully")
			}
			wantKind := KindInvalidNoteProof
			if name == "cycle" {
				wantKind = KindCycleDetected
			}
			assertFailureKind(t, err, wantKind)
			if after := con830DerivedProjectionSnapshot(t, s); after != before {
				t.Fatalf("invalid %s metadata changed prior projections:\nbefore=%s\nafter=%s", name, before, after)
			}
		})
	}
}

func TestCON830ThirtyThreeAuthoredEdgesPageAsThirtyTwoPlusOne(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	manifest := readCON830AcceptanceManifest(t, home)
	for i := 0; i < 32; i++ {
		refiner := manifest.Records[1]
		refiner.ID = fmt.Sprintf("CD-%04d", i+3)
		refiner.Path = ".concord/docs/decisions/" + refiner.ID + ".md"
		writeKnowledgeFile(t, home.RepoPath, refiner.Path, "refining storage rule\n")
		manifest.Records = append(manifest.Records, refiner)
	}
	commitCON830AcceptanceManifest(t, home, manifest)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	req := KnowledgeRefinementContextRequest{Product: "amendment-product", Home: home, Roots: []string{home.HomeProjectID + "/CD-0001"}, Limit: 32}
	first, err := s.QueryKnowledgeRefinementContext(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Edges) != 32 || first.NextCursor == nil || !reflect.DeepEqual(first.IncompleteRoots, req.Roots) {
		t.Fatalf("first page did not declare 32 edges plus continuation/incomplete root: %+v", first)
	}
	req.Cursor = *first.NextCursor
	second, err := s.QueryKnowledgeRefinementContext(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Edges) != 1 || second.NextCursor != nil || len(second.IncompleteRoots) != 0 {
		t.Fatalf("second page did not close the 33-edge graph: %+v", second)
	}
	seen := map[string]bool{}
	for _, edge := range append(first.Edges, second.Edges...) {
		if seen[edge.EndpointLawID] {
			t.Fatalf("duplicate endpoint across pages: %s", edge.EndpointLawID)
		}
		seen[edge.EndpointLawID] = true
	}
	if len(seen) != 33 || second.Edges[0].EndpointLawID != "CD-0034" {
		t.Fatalf("paging lost an edge or stable ordering: seen=%d final=%+v", len(seen), second.Edges[0])
	}
}

func TestCON830HashMismatchedCurrentBlobNeverProvesNoAmendments(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	manifest := readCON830AcceptanceManifest(t, home)
	writeKnowledgeFile(t, home.RepoPath, manifest.Records[1].Path, "Current blob does not match its declared hash.\n")
	// Advance the authored metadata identity without correcting its blob
	// hash. An unchanged metadata identity would retain the prior verified
	// snapshot, whose immutable blob still matches its declared proof.
	manifest.Records[1].Title = "Hash-mismatched current refiner"
	commitCON830AcceptanceManifest(t, home, manifest)
	req := Q10Request{Product: "amendment-product", KnowledgeID: home.HomeProjectID + "/CD-0001", IncludeAmendmentContext: true}
	if _, err := s.QueryQ10(ctx, req); err == nil {
		t.Fatal("strict current context accepted a hash-mismatched blob")
	}
	req.AmendmentContextAllowDegraded = true
	result, err := s.QueryQ10(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	page := result.Result.CurrentAmendmentContext
	if result.Note == nil || page == nil || page.Authority == "authoritative" || len(page.Omissions) == 0 {
		t.Fatalf("hash-mismatched current source claimed authoritative no-amendments or lost historical proof: %+v", result)
	}
}
