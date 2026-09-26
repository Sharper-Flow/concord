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
			UpdatedAt:    "2026-09-2" + string(rune('0'+i)) + "T00:00:00Z",
			TerminalAt:   "2026-09-01T00:00:00Z",
			ProjectCount: 2,
		})
	}
	return Snapshot{Screen: ScreenProduct, AmbientProduct: "p-1", Coverage: "authoritative", Ranked: rows}
}

// TestProjectConsumesWidthBudgetByDroppingLowestPriorityColumns proves
// check:launcher/projection-consumes-width: Project takes its width parameter
// for real. A wide budget keeps the full declared column set; a narrowed
// budget drops the lowest-priority columns (rightmost first) until the set
// fits, and every row stays parallel to the surviving columns.
func TestProjectConsumesWidthBudgetByDroppingLowestPriorityColumns(t *testing.T) {
	snapshot := rankedProjectionSnapshot()
	wide := Project(snapshot, 400, fixtureMeasure(nil))
	wantOrder := "Work,Updated,Live"
	if strings.Join(wide.Columns, ",") != wantOrder {
		t.Fatalf("wide projection columns = %v, want %s", wide.Columns, wantOrder)
	}
	for _, row := range wide.Rows {
		if len(row) != 3 {
			t.Fatalf("wide row %#v is not parallel to the 3 declared columns", row)
		}
	}
	narrow := Project(snapshot, 30, fixtureMeasure(nil))
	if len(narrow.Columns) < 2 {
		t.Fatalf("narrow projection dropped every column but the first: %v", narrow.Columns)
	}
	if narrow.Columns[0] != "Work" {
		t.Fatalf("narrow projection dropped the highest-priority column: %v", narrow.Columns)
	}
	for _, column := range narrow.Columns {
		if column == "Live" {
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
	projection := Project(snapshot, 1, fixtureMeasure(nil))
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
	at120 := Project(snapshot, 120, fixtureMeasure(nil))
	if len(at120.Columns) != 3 {
		t.Fatalf("120-column projection dropped fitting columns: %v", at120.Columns)
	}
	portfolio := Snapshot{Screen: ScreenPortfolio, Coverage: "authoritative", Rows: []ProductRow{{ID: "p-1", Name: "One", Reliance: "clear", Actions: 1}}}
	if got := len(Project(portfolio, 120, fixtureMeasure(nil)).Columns); got != 2 {
		t.Fatalf("portfolio projection columns = %d, want 2", got)
	}
}

// fixtureMeasure stands in for the renderer's CellMeasure: the map prices
// the listed fixture strings in the display cells a terminal spends, and
// every unlisted string counts one cell per rune. The map is data, not
// width logic: the projection consumes the measurements it is given and
// owns only the column-priority policy, so the fixture prices are the
// inputs the tests control.
func fixtureMeasure(cells map[string]int) CellMeasure {
	return func(s string) int {
		if priced, ok := cells[s]; ok {
			return priced
		}
		return len([]rune(s))
	}
}

// TestColumnBudgetConsumesTheSuppliedMeasure proves the budget prices cells
// with the measurement the caller supplies, not a count the core computes.
// The fixtures state the cells a terminal spends: CJK and emoji occupy two
// cells per rune, and combining marks one cell per two runes, so near the
// threshold a rune count answers differently in both directions.
func TestColumnBudgetConsumesTheSuppliedMeasure(t *testing.T) {
	measure := fixtureMeasure(map[string]int{
		"界界界界":                         8, // four runes, eight cells
		"👍👍👍":                          6, // three runes, six cells
		"e\u0301e\u0301e\u0301e\u0301": 4, // eight runes, four cells
	})
	cases := []struct {
		name         string
		headers      []string
		rows         [][]string
		width        int
		want         int
		runeCountGot int // what a rune count answers, for the near-threshold contrast
	}{
		{
			name:         "cjk cells exceed the budget the rune count accepts",
			headers:      []string{"A", "B"},
			rows:         [][]string{{"界界界界", "x"}},
			width:        12,
			want:         1,
			runeCountGot: 2,
		},
		{
			name:         "cjk cells seat both columns at the fitting width",
			headers:      []string{"A", "B"},
			rows:         [][]string{{"界界界界", "x"}},
			width:        15,
			want:         2,
			runeCountGot: 2,
		},
		{
			name:         "emoji cells exceed the budget the rune count accepts",
			headers:      []string{"A", "B"},
			rows:         [][]string{{"👍👍👍", "x"}},
			width:        12,
			want:         1,
			runeCountGot: 2,
		},
		{
			name:         "combining marks fit where a rune count drops the column",
			headers:      []string{"AA", "BB"},
			rows:         [][]string{{"e\u0301e\u0301e\u0301e\u0301", "xx"}},
			width:        12,
			want:         2,
			runeCountGot: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ColumnBudget(tc.headers, tc.rows, tc.width, measure); got != tc.want {
				t.Fatalf("ColumnBudget=%d, want %d", got, tc.want)
			}
			if tc.runeCountGot == tc.want {
				return
			}
			runes := make([]int, len(tc.headers))
			for i, header := range tc.headers {
				runes[i] = len([]rune(header))
				for _, row := range tc.rows {
					if i < len(row) && len([]rune(row[i])) > runes[i] {
						runes[i] = len([]rune(row[i]))
					}
				}
			}
			total := 2
			for i := range tc.headers {
				total += runes[i] + 2
			}
			kept := len(tc.headers)
			for kept > 1 && total > tc.width {
				kept--
				total -= runes[kept] + 2
			}
			if kept != tc.runeCountGot {
				t.Fatalf("rune-count contrast drifted: rune-count budget=%d, want the recorded %d", kept, tc.runeCountGot)
			}
		})
	}
}

// TestProjectShedsByTheSuppliedMeasure applies the supplied measurement
// through the Project entry point. The fixture prices the cells: the Product
// name "界界界界" spans 8 cells (4 runes) and the Actions text "ip:0 b:0 r:0
// p:0 a:0" prices far wider than its runes, so the two-column set seats and
// sheds on the supplied prices, not on a rune count.
func TestProjectShedsByTheSuppliedMeasure(t *testing.T) {
	snapshot := Snapshot{
		Screen: ScreenPortfolio,
		Rows: []ProductRow{{
			ID: "p-1", Name: "界界界界", Stage: "in_progress",
			Reliance: "clear", Actions: 1,
		}},
	}
	measure := fixtureMeasure(map[string]int{
		"界界界界": 8,
		"1":    40, // the collapsed Actions cell, priced absurdly wide
	})
	narrow := Project(snapshot, 20, measure)
	if len(narrow.Columns) != 1 || narrow.Columns[0] != "Product" {
		t.Fatalf("narrow projection kept %v, want [Product] under the supplied prices", narrow.Columns)
	}
	wide := Project(snapshot, 60, measure)
	if len(wide.Columns) != 2 {
		t.Fatalf("wide projection dropped fitting columns: %v", wide.Columns)
	}
}
