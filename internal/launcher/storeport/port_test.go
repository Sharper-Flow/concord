package storeport

import (
	"os"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestRelationTreeKeepsStructuralComponentAndInverseOutOfCycleOracle(t *testing.T) {
	tree := relationTree([]store.RelationEdge{
		{Kind: "parent", Source: "a", Target: "b"},
		{Kind: "parent", Source: "b", Target: "c"},
		{Kind: "blocks", Source: "c", Target: "d"},
		{Kind: "depends_on", Source: "d", Target: "c"},
	}, 3, "authoritative")
	if tree.Invariant != "" {
		t.Fatalf("inverse edge became cycle: %#v", tree)
	}
	if len(tree.Components) != 1 || len(tree.Components[0]) != 4 {
		t.Fatalf("components=%#v", tree.Components)
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
	if len(tree.Components) != 1 {
		t.Fatalf("cycle component=%#v", tree.Components)
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
