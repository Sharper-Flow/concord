package bubbletea

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sharper-flow/concord/internal/launcher"
)

// reorderPort serves a different store-ordered snapshot on every read, so a
// test can refresh into a list whose rows moved.
type reorderPort struct {
	reads  int
	frames []launcher.Snapshot
}

func (p *reorderPort) Read(_ context.Context, _ launcher.ReadRequest) (launcher.Snapshot, error) {
	p.reads++
	return p.frames[min(p.reads-1, len(p.frames)-1)], nil
}

// TestRefreshRestoresCursorToSelectedWork proves the operator's selection
// survives a refresh that reorders the work list: the cursor follows the
// selected work ID to its new row, the display shows the store's new order,
// and the row the cursor leaves is never a different item.
func TestRefreshRestoresCursorToSelectedWork(t *testing.T) {
	productFrame := func(ids ...string) launcher.Snapshot {
		snapshot := launcher.Snapshot{
			Screen: launcher.ScreenProduct, AmbientProduct: "product-1", Coverage: "authoritative", ActiveWorkOnly: true,
		}
		for _, id := range ids {
			snapshot.Ranked = append(snapshot.Ranked, launcher.RankedWork{ID: id, Title: id, Lifecycle: "in_progress"})
		}
		return snapshot
	}
	p := &reorderPort{reads: 1, frames: []launcher.Snapshot{
		productFrame("work-a", "work-b", "work-c"),
		productFrame("work-c", "work-a", "work-b"),
	}}
	// reads starts at 1: the entry read consumed the first frame, so the
	// refresh below reads the reordered one.
	core := launcher.New(p)
	core.RestoreSnapshot(p.frames[0])
	m := New(core, context.Background(), Profile{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	m.UpdateKey("j") // cursor onto work-b at index 1
	if rows := m.filteredRanked(); m.Cursor() != 1 || rows[m.Cursor()].ID != "work-b" {
		t.Fatalf("cursor=%d rows=%#v, want work-b at index 1", m.Cursor(), rows)
	}

	m.UpdateKey("r") // refresh: the store returns the reordered list
	rows := m.filteredRanked()
	want := []string{"work-c", "work-a", "work-b"}
	for i, id := range want {
		if rows[i].ID != id {
			t.Fatalf("refreshed rows[%d] = %s, want the store's new order %v", i, rows[i].ID, want)
		}
	}
	if m.Cursor() != 2 || rows[m.Cursor()].ID != "work-b" {
		t.Fatalf("cursor=%d on %q, want work-b at its new index 2", m.Cursor(), rows[m.Cursor()].ID)
	}
}
