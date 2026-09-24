package storeport

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/store"
)

// knowledgeHomeStore designates one Product knowledge home whose canonical
// locator points outside the host, so the git authority is unreachable and
// the read degrades. The home table is fold-only, so the guard brackets the
// insert.
func knowledgeHomeStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "launcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seedLauncherStoreFixture(t, s)
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES ('scope-a','proj-a1','loc-a1')`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

func knowledgePort(s *store.Store) *Port {
	port := New(s)
	port.SessionProbe = func(context.Context, store.WorktreeEntry) bool { return false }
	return port
}

// productScreen enters a Product through the launcher Model over the real
// store port, the path the terminal session takes, and returns the screen
// with the plain Product read taken at the same store state.
func productScreen(t *testing.T, s *store.Store, product string) (launcher.Snapshot, launcher.Snapshot) {
	t.Helper()
	ctx := context.Background()
	port := knowledgePort(s)
	plain, err := port.Read(ctx, launcher.ReadRequest{Kind: launcher.ReadProduct, Product: product, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	m := launcher.New(port)
	if err := m.SelectProduct(ctx, product); err != nil {
		t.Fatalf("Product entry errored: %v", err)
	}
	return m.Snapshot(), plain
}

// The defect this floor row exists for: a knowledge read that fails or
// degrades is one section's state, never the screen's. The Product work list,
// screen coverage, reliance, watermark, and status stay exactly as the
// Product read set them, and a missing knowledge home renders as a typed
// unavailable Knowledge section carrying the store's reason.
func TestKnowledgeReadFailureStaysInTheKnowledgeSection(t *testing.T) {
	s := openLauncherStore(t)
	seedLauncherStoreFixture(t, s)

	snapshot, plain := productScreen(t, s, "scope-a")
	if snapshot.Screen != launcher.ScreenProduct || snapshot.AmbientProduct != "scope-a" {
		t.Fatalf("Product entry landed on %s for %q", snapshot.Screen, snapshot.AmbientProduct)
	}
	if !snapshot.Knowledge.Read || snapshot.Knowledge.State != "unavailable" || snapshot.Knowledge.Reason != string(store.KindUnknownScope) {
		t.Fatalf("missing knowledge home was not typed unavailable with the store reason: %#v", snapshot.Knowledge)
	}
	// The work read's answer survives beside the unavailable section: the
	// Product still lists its work.
	if len(plain.Ranked) < 2 || len(snapshot.Ranked) != len(plain.Ranked) {
		t.Fatalf("knowledge failure changed the Product work list: screen=%d plain=%d", len(snapshot.Ranked), len(plain.Ranked))
	}
	assertScreenFieldsFromPlainRead(t, snapshot, plain)
}

// A degraded git authority is the Knowledge section's state alone. The screen
// fields stay as the Product read set them, so a lagging index never renders
// as a degraded Product.
func TestKnowledgeReadDegradedAuthorityChangesOnlyTheKnowledgeSection(t *testing.T) {
	snapshot, plain := productScreen(t, knowledgeHomeStore(t), "scope-a")
	if !snapshot.Knowledge.Read || snapshot.Knowledge.State != "unavailable" || snapshot.Knowledge.Reason != "knowledge_index_lagging_or_unreachable" {
		t.Fatalf("degraded authority was not typed in the Knowledge section: %#v", snapshot.Knowledge)
	}
	if len(plain.Ranked) < 2 || len(snapshot.Ranked) != len(plain.Ranked) {
		t.Fatalf("degraded knowledge read changed the Product work list: screen=%d plain=%d", len(snapshot.Ranked), len(plain.Ranked))
	}
	assertScreenFieldsFromPlainRead(t, snapshot, plain)
}

// The work-screen knowledge read answers its section beside the work read:
// the work detail, watermark, and coverage stay as the work read set them.
func TestWorkKnowledgeReadKeepsTheWorkReadAnswer(t *testing.T) {
	ctx := context.Background()
	m := launcher.New(knowledgePort(knowledgeHomeStore(t)))
	if err := m.SelectProduct(ctx, "scope-a"); err != nil {
		t.Fatal(err)
	}
	if err := m.SelectWork(ctx, "scope-live"); err != nil {
		t.Fatal(err)
	}
	work := m.Snapshot()
	if err := m.EnsureKnowledge(ctx); err != nil {
		t.Fatalf("work knowledge read errored the screen: %v", err)
	}
	snapshot := m.Snapshot()
	if snapshot.Screen != launcher.ScreenWork || snapshot.Section != launcher.SectionKnowledge || snapshot.SelectedWorkID != "scope-live" {
		t.Fatalf("work knowledge read landed on %s/%s for %q", snapshot.Screen, snapshot.Section, snapshot.SelectedWorkID)
	}
	if snapshot.Detail.Item.ID != "scope-live" {
		t.Fatalf("work knowledge read dropped the work detail: %#v", snapshot.Detail.Item)
	}
	if !snapshot.Knowledge.Read || snapshot.Knowledge.State != "authoritative-empty" {
		t.Fatalf("uncompacted live work was not an authoritative-empty knowledge section: %#v", snapshot.Knowledge)
	}
	assertScreenFieldsFromPlainRead(t, snapshot, work)
}

func assertScreenFieldsFromPlainRead(t *testing.T, screen, plain launcher.Snapshot) {
	t.Helper()
	if plain.Coverage != "authoritative" || plain.Watermark == "" {
		t.Fatalf("plain read is not an authoritative baseline: coverage=%q watermark=%q", plain.Coverage, plain.Watermark)
	}
	if screen.Coverage != plain.Coverage || screen.Reliance != plain.Reliance {
		t.Fatalf("knowledge read moved screen coverage or reliance: coverage=%q reliance=%q, want %q %q", screen.Coverage, screen.Reliance, plain.Coverage, plain.Reliance)
	}
	if screen.Watermark != plain.Watermark {
		t.Fatalf("knowledge read moved the screen watermark: %q, want %q", screen.Watermark, plain.Watermark)
	}
	if screen.StatusMessage != plain.StatusMessage {
		t.Fatalf("knowledge read moved the screen status: %q, want %q", screen.StatusMessage, plain.StatusMessage)
	}
}
