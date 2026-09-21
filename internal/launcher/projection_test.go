package launcher

import (
	"strings"
	"testing"
)

func rankedProjectionSnapshot() Snapshot {
	rows := make([]RankedWork, 0, 3)
	for i := 0; i < 3; i++ {
		rows = append(rows, RankedWork{
			ID:           "work-" + string(rune('1'+i)),
			Kind:         "task",
			Title:        "Work " + string(rune('1'+i)),
			Lifecycle:    "in_progress",
			Priority:     int64(i + 1),
			Urgency:      "high",
			TerminalAt:   "2026-09-01T00:00:00Z",
			ProjectCount: 2,
		})
	}
	return Snapshot{Screen: ScreenProduct, AmbientProduct: "p-1", Section: SectionRanked, Ranked: rows}
}

// TestProjectConsumesWidthBudgetByDroppingLowestPriorityColumns proves
// check:launcher/projection-consumes-width: Project takes its width parameter
// for real. A wide budget keeps the full declared column set; a narrowed
// budget drops the lowest-priority columns (rightmost first) until the set
// fits, and every row stays parallel to the surviving columns.
func TestProjectConsumesWidthBudgetByDroppingLowestPriorityColumns(t *testing.T) {
	snapshot := rankedProjectionSnapshot()
	wide := Project(snapshot, 400)
	wantOrder := "Work,Kind,Priority,Urgency,Readiness,Lifecycle,TerminalAt,Projects"
	if strings.Join(wide.Columns, ",") != wantOrder {
		t.Fatalf("wide projection columns = %v, want %s", wide.Columns, wantOrder)
	}
	for _, row := range wide.Rows {
		if len(row) != 8 {
			t.Fatalf("wide row %#v is not parallel to the 8 declared columns", row)
		}
	}
	narrow := Project(snapshot, 30)
	if len(narrow.Columns) < 2 {
		t.Fatalf("narrow projection dropped every column but the first: %v", narrow.Columns)
	}
	if narrow.Columns[0] != "Work" {
		t.Fatalf("narrow projection dropped the highest-priority column: %v", narrow.Columns)
	}
	// TerminalAt and Projects are the declared lowest priority: they drop
	// before anything that survives a narrowed budget.
	for _, column := range narrow.Columns {
		if column == "TerminalAt" || column == "Projects" {
			t.Fatalf("lowest-priority column %q survived the narrow budget: %v", column, narrow.Columns)
		}
	}
	for _, row := range narrow.Rows {
		if len(row) != len(narrow.Columns) {
			t.Fatalf("narrow row %#v is not parallel to columns %v", row, narrow.Columns)
		}
	}
}

// TestProjectNeverDropsTheHighestPriorityColumn proves the floor of the
// budget: below any fitting width the projection keeps the first column with
// its cells, so the renderer always has a well-formed table to truncate.
func TestProjectNeverDropsTheHighestPriorityColumn(t *testing.T) {
	snapshot := Snapshot{
		Screen: ScreenPortfolio,
		Rows: []ProductRow{{
			ID: "p-1", Name: "Product with an unboundedly long name", Stage: "in_progress",
			Reliance: "clear", Actions: 1, Focus: "Focus",
		}},
	}
	projection := Project(snapshot, 1)
	if len(projection.Columns) != 1 || projection.Columns[0] != "Product" {
		t.Fatalf("width-1 projection columns = %v, want [Product]", projection.Columns)
	}
	if len(projection.Rows) != 1 || len(projection.Rows[0]) != 1 || projection.Rows[0][0] != "Product with an unboundedly long name" {
		t.Fatalf("width-1 projection rows = %#v", projection.Rows)
	}
}

// TestProjectKeepsEveryColumnWhenTheBudgetSeatsIt proves the budget never
// drops columns that fit, so a wide terminal keeps the full declared set.
func TestProjectKeepsEveryColumnWhenTheBudgetSeatsIt(t *testing.T) {
	snapshot := rankedProjectionSnapshot()
	at120 := Project(snapshot, 120)
	if len(at120.Columns) != 8 {
		t.Fatalf("120-column projection dropped fitting columns: %v", at120.Columns)
	}
	domains := Snapshot{Screen: ScreenProduct, Section: SectionDomains, Domains: DomainSection{
		Read: true, State: "authoritative",
		Domains: []DomainRow{{ID: "d-1", Name: "Domain one", Home: true}},
	}}
	if got := len(Project(domains, 120).Columns); got != 4 {
		t.Fatalf("domain projection columns = %d, want 4", got)
	}
}
