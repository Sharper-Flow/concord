package storeport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/store"
)

func TestScanRootCandidatesIncludesGitProjectsAndPins(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONCORD_LAUNCHER_SCAN_ROOTS", root)
	t.Setenv("CONCORD_LAUNCHER_PINS", project)
	got := ScanRootCandidates()
	if len(got) != 1 || got[0].Path != project || !got[0].Pinned || !got[0].Available {
		t.Fatalf("scan candidates = %#v", got)
	}
}

func TestProbeFailureIsTypedPreviewState(t *testing.T) {
	port := New(nil)
	port.VisionProbe = func(context.Context) (bool, string) { return false, "daemon unavailable" }
	port.LgrepProbe = func(context.Context) (bool, string) { return false, "index unavailable" }
	got := port.Probe(context.Background())
	if len(got) != 2 || got[0].Available || got[1].Available || got[0].Reason == "" || got[1].Reason == "" {
		t.Fatalf("probe state = %#v", got)
	}
}

func TestCandidatesWithoutStoreStayBoundedToScanRoots(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 101; i++ {
		project := filepath.Join(root, fmt.Sprintf("project-%03d", i))
		if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CONCORD_LAUNCHER_SCAN_ROOTS", root)
	got, err := (&Port{}).Candidates(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Fatalf("bounded candidates = %d, want 20", len(got))
	}
}

func TestRelationTreeKeepsStructuralComponentAndInverseOutOfCycleOracle(t *testing.T) {
	tree := relationTree([]store.RelationEdge{
		{Kind: "parent", Source: "a", Target: "b"},
		{Kind: "parent", Source: "b", Target: "c"},
		{Kind: "blocks", Source: "c", Target: "d"},
		{Kind: "blocked_by", Source: "d", Target: "c"},
	}, 3, "authoritative")
	if tree.Invariant != "" {
		t.Fatalf("inverse edge became cycle: %#v", tree)
	}
	if len(tree.Clusters) != 1 || len(tree.Clusters[0]) != 4 {
		t.Fatalf("components=%#v", tree.Clusters)
	}
}

func TestSearchPortUsesOnlyCompositeStoreOperation(t *testing.T) {
	source, err := os.ReadFile("port.go")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(source), "func (p *Port) readSearch")
	end := strings.Index(string(source[start:]), "func snapshotFromSearch")
	if start < 0 || end < 0 {
		t.Fatal("readSearch boundaries not found")
	}
	body := string(source[start : start+end])
	if !strings.Contains(body, "QueryLauncherSearch") || strings.Contains(body, "QueryQ9") || strings.Contains(body, "QueryLauncherProduct") || strings.Contains(body, "QueryLauncherWork") {
		t.Fatalf("search port must make one composite store call: %s", body)
	}
}

func TestRelationTreeSurfacesCycles(t *testing.T) {
	tree := relationTree([]store.RelationEdge{{Kind: "parent", Source: "a", Target: "b"}, {Kind: "parent", Source: "b", Target: "a"}}, 3, "authoritative")
	if tree.Invariant != "invariant_violation" {
		t.Fatalf("cycle hidden: %#v", tree)
	}
	if len(tree.Clusters) != 1 {
		t.Fatalf("cycle component=%#v", tree.Clusters)
	}
}

func TestRelationTreeResolvesSupersessionChainOnce(t *testing.T) {
	tree := relationTree([]store.RelationEdge{{Kind: "supersedes", Source: "old", Target: "middle"}, {Kind: "supersedes", Source: "middle", Target: "current"}}, 3, "authoritative")
	seen := map[string]bool{}
	for _, edge := range tree.Edges {
		if edge.Kind == "supersedes" {
			seen[edge.Source+"->"+edge.Target] = true
		}
	}
	if !seen["old->current"] || !seen["middle->current"] {
		t.Fatalf("supersession=%#v", tree.Edges)
	}
	if len(seen) != 2 {
		t.Fatalf("duplicate successor=%#v", tree.Edges)
	}
}

func TestRelationTreeMarksDepthTruncationUnavailable(t *testing.T) {
	tree := relationTree([]store.RelationEdge{
		{Kind: "parent", Source: "a", Target: "b"},
		{Kind: "parent", Source: "b", Target: "c"},
		{Kind: "parent", Source: "c", Target: "d"},
		{Kind: "parent", Source: "d", Target: "e"},
	}, 3, "authoritative")
	if tree.Coverage != "unavailable" || tree.Unavailable != "relation depth limit reached" {
		t.Fatalf("depth truncation must be visible: %#v", tree)
	}
}

func TestReadDomainsMapsAbsentRegistryToTypedUnavailableSection(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "launcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	port := New(s)
	snapshot, err := port.Read(context.Background(), launcher.ReadRequest{Kind: launcher.ReadDomains, Product: "product", Limit: 20, Section: launcher.SectionDomains})
	if err != nil {
		t.Fatalf("absent registry must not error the screen: %v", err)
	}
	if snapshot.Screen != launcher.ScreenProduct || snapshot.Section != launcher.SectionDomains {
		t.Fatalf("screen/section = %s/%s", snapshot.Screen, snapshot.Section)
	}
	if snapshot.Coverage != "unavailable" || snapshot.Domains.State != "unavailable" || snapshot.Domains.Reason != "domain_registry_absent" {
		t.Fatalf("absent registry must render typed unavailable, not empty: %#v", snapshot.Domains)
	}
}

func TestProductReadAppendsTerminalDrillDownTail(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "launcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES ('drill','Drill','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('drill-project','Drill project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('drill','drill-project','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES
		('drill-live','task','Live work','needed',1,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z',NULL),
		('drill-done','bug','Done work','completed',1,1,'2026-08-01T00:00:00Z','2026-08-04T00:00:00Z','2026-08-04T00:00:00Z');
		INSERT INTO work_projects(work_id,project_id,role) VALUES ('drill-live','drill-project','primary'),('drill-done','drill-project','primary');
	`); err != nil {
		t.Fatal(err)
	}
	port := New(s)
	snapshot, err := port.Read(ctx, launcher.ReadRequest{Kind: launcher.ReadProduct, Product: "drill", Limit: 20, Section: launcher.SectionRanked})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Coverage != "authoritative" || len(snapshot.Ranked) != 2 {
		t.Fatalf("drill-down snapshot=%#v", snapshot)
	}
	live, done := snapshot.Ranked[0], snapshot.Ranked[1]
	if live.ID != "drill-live" || live.Terminal || live.TerminalAt != "" || live.Readiness() != "ready" {
		t.Fatalf("active drill-down item=%#v", live)
	}
	if done.ID != "drill-done" || !done.Terminal || done.TerminalAt != "2026-08-04T00:00:00Z" || done.Kind != "bug" || done.Readiness() != "terminal" {
		t.Fatalf("terminal drill-down item=%#v", done)
	}
	// The store answer feeds the S2 summary contract: the terminal tail must
	// not answer blocked/next.
	stack := snapshot.S2AnswerStack()
	if stack.Blocked.Work == nil || stack.Blocked.Work.ID != "drill-live" {
		t.Fatalf("terminal tail supplied a coordination summary: %#v", stack)
	}
}
