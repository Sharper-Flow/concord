package storeport

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/store"
)

// seedBeyondLimitFixture seeds one Product with more active work than the
// launcher's hundred-item request bound, each row carrying a work_item-subject
// log event at a distinct time, so the launcher's log-derived order must drain
// past the cut the old limit imposed.
func seedBeyondLimitFixture(t *testing.T, s *store.Store, count int) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	rows := make([]string, 0, count)
	events := make([]string, 0, count)
	members := make([]string, 0, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("overflow-%03d", i)
		stamp := fmt.Sprintf("2026-08-05T00:00:%02d.%09dZ", i/60, i%60)
		rows = append(rows, fmt.Sprintf("(%q,'task','Overflow work %03d','needed',1,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z',NULL)", id, i))
		events = append(events, fmt.Sprintf("(%q,'work.created','work_item',%q,'operator',%q,1,'{}')", id+"-ev", id, stamp))
		members = append(members, fmt.Sprintf("(%q,'overflow-project','primary')", id))
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES ('overflow','Overflow','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('overflow-project','Overflow project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('overflow','overflow-project','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES `+strings.Join(rows, ",")+`;
		INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES `+strings.Join(events, ",")+`;
		INSERT INTO work_projects(work_id,project_id,role) VALUES `+strings.Join(members, ",")+`;
	`); err != nil {
		t.Fatal(err)
	}
}

// TestProductReadRendersAllActiveWorkBeyondOneHundred proves the Product read
// has no omission-by-limit state: the store returns the Product's complete
// active set even when it holds far more items than the request bound, the
// port carries every row onto the snapshot in stored order, and the coverage
// stays authoritative.
func TestProductReadRendersAllActiveWorkBeyondOneHundred(t *testing.T) {
	t.Parallel()
	const count = 150
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "launcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBeyondLimitFixture(t, s, count)
	port := New(s)
	snapshot, err := port.Read(context.Background(), launcher.ReadRequest{Kind: launcher.ReadProduct, Product: "overflow", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Ranked) != count {
		t.Fatalf("active rows = %d, want the complete set of %d (first omitted row would be %q)", len(snapshot.Ranked), count, fmt.Sprintf("overflow-%03d", len(snapshot.Ranked)))
	}
	if snapshot.Coverage != "authoritative" {
		t.Fatalf("coverage = %q, message %q: a limit must never turn the work list unavailable", snapshot.Coverage, snapshot.StatusMessage)
	}
	if snapshot.Ranked[0].ID != "overflow-149" || snapshot.Ranked[count-1].ID != "overflow-000" {
		t.Fatalf("stored order broke at the ends: first=%s last=%s", snapshot.Ranked[0].ID, snapshot.Ranked[count-1].ID)
	}
}
