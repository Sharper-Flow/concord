package launcher

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Projection is the deterministic, terminal-independent projection the
// renderer draws: one bordered table per screen, with no permanent header
// lines. Markers parallel the rows; "!" marks a row whose facts already
// demand attention, so the renderer colours a data fact and never sniffs a
// rendered string for its colour.
type Projection struct {
	Columns []string
	Rows    [][]string
	Markers []string
}

// CellMeasure prices a string in the display cells a terminal spends on it.
// The renderer owns the measurement — it prices every table it draws with
// its own measurement library — and supplies it through the projection
// input, so the core keeps the column-priority policy without importing the
// renderer, its Charm dependencies, or any width logic of its own.
type CellMeasure func(string) int

// Project is a deterministic, terminal-independent projection. It performs
// no reads and emits textual markers so meaning survives no-color output and
// screen-reader consumption. Width is the terminal budget the projected
// table may span, and measure prices each cell in the display cells a
// terminal spends: when the set does not fit, columns drop from the
// per-screen priority order until it does, so a narrowed pane sheds whole
// columns instead of truncating all of them.
func Project(snapshot Snapshot, width int, measure CellMeasure) Projection {
	return applyColumnBudget(project(snapshot, width, measure), width, measure)
}

func project(snapshot Snapshot, width int, measure CellMeasure) Projection {
	switch snapshot.Screen {
	case ScreenPortfolio:
		return projectPortfolio(snapshot)
	case ScreenProduct:
		return projectWorkList(snapshot, width, measure)
	default:
		return Projection{}
	}
}

// projectPortfolio renders the portfolio's two columns: the Product and its
// live Actions counts. An abnormal reliance or unavailable counts mark the
// row: the marker colours the row and prefixes the Actions cell, so the
// attention fact survives no-color output and screen-reader consumption.
func projectPortfolio(snapshot Snapshot) Projection {
	columns := []string{"Product", "Actions"}
	rows := make([][]string, 0, len(snapshot.Rows))
	markers := make([]string, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		actions := actionText(row)
		marker := rowMarker(row)
		if marker == "!" {
			actions = "! " + actions
		}
		rows = append(rows, []string{row.Name + row.NameSuffix, actions})
		markers = append(markers, marker)
	}
	return Projection{Columns: columns, Rows: rows, Markers: markers}
}

// projectWorkList renders the Product screen's work list sorted by
// updated_at descending (the default MRU ordering), one row per work item:
// number, readiness marker, the linked Linear key when linked, the title,
// and the blocking ticket reference, with relative updated and live-session
// columns. The Work cell bounds itself so the Updated and Live cells seat on
// every row at every supported width.
func projectWorkList(snapshot Snapshot, width int, measure CellMeasure) Projection {
	columns := []string{"Work", "Updated", "Live"}
	ranked := SortRankedByRecency(snapshot.Ranked)
	now := relativeTimeNow()
	updatedWidth, liveWidth := measure(columns[1]), measure(columns[2])
	for _, item := range ranked {
		if cells := measure(RelativeTime(item.UpdatedAt, now)); cells > updatedWidth {
			updatedWidth = cells
		}
		if cells := measure(liveCellText(item, snapshot)); cells > liveWidth {
			liveWidth = cells
		}
	}
	// The cursor gutter and each column's inter-column padding, priced as
	// the renderer prices them.
	workBudget := width - projectedCursorGutter - 3*projectedColumnPadding - updatedWidth - liveWidth
	workBudget = max(minWorkCellBudget, workBudget)
	rows := make([][]string, 0, len(ranked))
	markers := make([]string, 0, len(ranked))
	for i, item := range ranked {
		rows = append(rows, []string{
			workCellText(i+1, item, workBudget, measure),
			RelativeTime(item.UpdatedAt, now),
			liveCellText(item, snapshot),
		})
		marker := ""
		if item.Readiness() == "blocked" {
			marker = "!"
		}
		markers = append(markers, marker)
	}
	if len(ranked) == 0 {
		rows = append(rows, drillDownStateRow(rankedSectionState(snapshot), columns))
		markers = append(markers, stateRowMarker(snapshot))
	}
	if snapshot.Backlog {
		// The picker row is an action, not a work item: it ends the list and
		// opens the issue-key prompt.
		picker := RankedWork{ID: "backlog", Kind: "new", Title: "New / Backlog", Lifecycle: "needed", Backlog: true}
		rows = append(rows, []string{workCellText(len(ranked)+1, picker, workBudget, measure), RelativeTime("", now), ""})
		markers = append(markers, "")
	}
	return Projection{Columns: columns, Rows: rows, Markers: markers}
}

// SortRankedByRecency orders work rows most-recently-updated first and
// returns the ordered copy: an unparsable or absent updated_at sorts last,
// and equal keys keep their given order. The read port cuts its bounded page
// by the same key, so the display order and the page cut agree.
func SortRankedByRecency(ranked []RankedWork) []RankedWork {
	out := append([]RankedWork(nil), ranked...)
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := parseTime(out[i].UpdatedAt), parseTime(out[j].UpdatedAt)
		if ti.Equal(tj) {
			return false
		}
		if ti.IsZero() {
			return false
		}
		if tj.IsZero() {
			return true
		}
		return ti.After(tj)
	})
	return out
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// relativeTimeNow is the clock the Updated cell ages against. Tests replace
// it; production leaves the wall clock in place.
var relativeTimeNow = time.Now

// RelativeTime renders a timestamp as the compact relative age the Updated
// column carries: "2h", "3d", and so on. An absent or unparsable timestamp
// renders as "-", never as a fabricated age.
func RelativeTime(value string, now time.Time) string {
	parsed := parseTime(value)
	if parsed.IsZero() {
		return "-"
	}
	d := now.Sub(parsed)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d < 30*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	case d < 365*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/(24*30))) + "mo"
	default:
		return strconv.Itoa(int(d.Hours()/(24*365))) + "y"
	}
}

// liveCellText is the live-session cell: "yes" when a live session holds the
// work, "no" when the host attested none under the active-only picker, and
// empty where the read carried no occupancy answer.
func liveCellText(item RankedWork, snapshot Snapshot) string {
	if item.Live > 0 {
		return "yes"
	}
	if snapshot.ActiveWorkOnly && !item.Backlog {
		return "no"
	}
	return ""
}

// workCellText composes one work row's Work cell within the budget: the row
// number, the readiness marker, the linked Linear key when linked, the
// title, and each blocking ticket reference. The title truncates first; the
// marker, number, key, and blocking references yield last, in that order.
// The Backlog picker row is an action, not work state, so it carries no
// readiness marker.
func workCellText(number int, item RankedWork, budget int, measure CellMeasure) string {
	head := strconv.Itoa(number) + " "
	line := head
	spare := budget - measure(head)
	if !item.Backlog {
		marker := rankedMarker(item) + " "
		if spare >= measure(marker) {
			line += marker
			spare -= measure(marker)
		}
	}
	if key := item.LinearIssueKey; key != "" && spare >= measure(key)+1 {
		line += key + " "
		spare -= measure(key) + 1
	}
	if title := item.Title; title != "" && spare > 1 {
		truncated := truncateCells(title, spare, measure)
		line += truncated
		spare -= measure(truncated)
	}
	if spare > 1 {
		for _, blocker := range item.Blockers {
			reference := blocker.IssueKey
			if reference == "" {
				reference = blocker.ID
			}
			cell := " !" + reference
			if spare < measure(cell) {
				break
			}
			line += cell
			spare -= measure(cell)
		}
	}
	return line
}

// rankedMarker is the readiness marker the Work cell leads with. The words
// stay readable with colour off.
func rankedMarker(item RankedWork) string {
	switch item.Readiness() {
	case "terminal":
		return "-TERMINAL"
	case "blocked":
		return "!BLOCKED"
	case "ready":
		return "+READY"
	default:
		return "~ACTIVE"
	}
}

// truncateCells clips a cell to a display-width budget and marks the cut
// with an ellipsis, so a clipped title never reads as complete. It measures
// with the supplied CellMeasure.
func truncateCells(value string, width int, measure CellMeasure) string {
	if width <= 0 || measure(value) <= width {
		return value
	}
	cut := width - 1 // the ellipsis occupies the final cell
	clipped := ""
	for _, r := range value {
		next := clipped + string(r)
		if measure(next) > cut {
			break
		}
		clipped = next
	}
	return clipped + "…"
}

func actionText(row ProductRow) string {
	if row.CountsState == "unavailable" {
		text := "unavailable: " + row.UnavailableReason
		if len(row.UnavailableOmissions) > 0 {
			text += " (omissions: " + strings.Join(row.UnavailableOmissions, ",") + ")"
		}
		return text
	}
	if row.InProgress == 0 && row.Blocked == 0 && row.Ready == 0 && row.ActiveProblems == 0 && row.ApprovalRequired == 0 && row.Actions != 0 {
		return fmt.Sprintf("%d", row.Actions)
	}
	return "ip:" + fmt.Sprintf("%d", row.InProgress) + " b:" + fmt.Sprintf("%d", row.Blocked) + " r:" + fmt.Sprintf("%d", row.Ready) + " p:" + fmt.Sprintf("%d", row.ActiveProblems) + " a:" + fmt.Sprintf("%d", row.ApprovalRequired)
}

// rowMarker marks the portfolio rows whose facts already demand attention:
// an abnormal reliance (not clear, ready, or empty; stale; or blocking
// execution) and unavailable action counts.
func rowMarker(row ProductRow) string {
	if row.CountsState == "unavailable" {
		return "!"
	}
	reliance := row.Reliance
	if row.RelianceStale || row.BlocksExecution {
		reliance = "stale"
	}
	if reliance != "" && reliance != "clear" && reliance != "ready" {
		return "!"
	}
	return ""
}

func stateRowMarker(snapshot Snapshot) string {
	if rankedSectionState(snapshot) != "authoritative-empty" {
		return "!"
	}
	return ""
}

const (
	// projectedColumnPadding mirrors the renderer's inter-column padding, so
	// the budget estimates the rendered table without importing it.
	projectedColumnPadding = 2
	// projectedCursorGutter reserves the two-column cursor gutter the renderer
	// places inside every data table.
	projectedCursorGutter = 2
	// minWorkCellBudget keeps a Work cell wide enough for its number, its
	// readiness marker, and a usable title head, where a narrowed table would
	// otherwise starve the one cell every row is read by.
	minWorkCellBudget = 14
)

// ColumnBudget returns how many leading columns of a projected table fit the
// width budget: the cursor gutter plus each column's widest cell plus the
// renderer's inter-column padding, all priced by the supplied CellMeasure.
// The measurements come from the renderer through the projection input: a
// rune count misprices near the threshold in both directions, and the core
// carries no width logic to misprice with. The first column never drops, so
// a table under any budget keeps one readable column. The renderer applies
// this same rule to tables it composes outside Project, so a narrowed pane
// sheds whole columns everywhere instead of truncating all of them. A nil
// measure is a caller bug and panics.
func ColumnBudget(headers []string, rows [][]string, width int, measure CellMeasure) int {
	if measure == nil {
		panic("launcher: nil CellMeasure: supply the renderer's display measurement")
	}
	if width <= 0 || len(headers) == 0 {
		return len(headers)
	}
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = measure(header)
		for _, row := range rows {
			if i < len(row) {
				if cells := measure(row[i]); cells > widths[i] {
					widths[i] = cells
				}
			}
		}
	}
	keep := len(headers)
	for keep > 1 {
		total := projectedCursorGutter
		for i := 0; i < keep; i++ {
			total += widths[i] + projectedColumnPadding
		}
		if total <= width {
			break
		}
		keep--
	}
	return keep
}

// applyColumnBudget drops the lowest-priority columns until the projected
// table fits the width budget. Priority is the declared display order: the
// rightmost column is the first dropped, and the first column never drops.
// Cells drop in parallel so every row stays aligned with the surviving
// columns.
func applyColumnBudget(projection Projection, width int, measure CellMeasure) Projection {
	if width <= 0 || len(projection.Columns) <= 1 {
		return projection
	}
	keep := ColumnBudget(projection.Columns, projection.Rows, width, measure)
	if keep == len(projection.Columns) {
		return projection
	}
	projection.Columns = append([]string(nil), projection.Columns[:keep]...)
	rows := make([][]string, 0, len(projection.Rows))
	for _, row := range projection.Rows {
		cells := make([]string, 0, keep)
		for i := 0; i < keep && i < len(row); i++ {
			cells = append(cells, row[i])
		}
		rows = append(rows, cells)
	}
	projection.Rows = rows
	return projection
}

// rankedSectionState types the drill-down list state so a degraded source
// never renders as an authoritative empty list.
func rankedSectionState(snapshot Snapshot) string {
	if snapshot.Coverage != "" && snapshot.Coverage != "authoritative" {
		reason := strings.TrimPrefix(snapshot.StatusMessage, "unavailable: ")
		if reason == "" {
			reason = snapshot.Coverage
		}
		return "unavailable: " + reason
	}
	return "authoritative-empty"
}

// drillDownStateRow renders a list state as one row with the column arity the
// screen already declared.
func drillDownStateRow(state string, columns []string) []string {
	row := []string{state}
	for i := 1; i < len(columns); i++ {
		row = append(row, "-")
	}
	return row
}
