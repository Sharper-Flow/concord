package launcher

import "fmt"

type Projection struct {
	Header  []string
	Columns []string
	Rows    [][]string
	Markers []string
}

// Project is a deterministic, terminal-independent projection. It performs
// no reads and emits textual reliance markers so meaning survives no-color
// output and screen-reader consumption.
func Project(snapshot Snapshot, width int) Projection {
	if width < 80 {
		width = 80
	}
	columns := []string{"Product", "Stage", "Reliance", "Actions", "Focus"}
	rows := make([][]string, 0, len(snapshot.Rows))
	markers := make([]string, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		name := row.Name + row.NameSuffix
		reliance := row.Reliance
		marker := "OK"
		if reliance != "clear" && reliance != "ready" && reliance != "" {
			marker = "!"
		}
		actions := fmt.Sprintf("ip:%d b:%d r:%d p:%d a:%d", row.InProgress, row.Blocked, row.Ready, row.ActiveProblems, row.ApprovalRequired)
		if row.Actions != 0 && row.InProgress == 0 && row.Blocked == 0 && row.Ready == 0 && row.ActiveProblems == 0 && row.ApprovalRequired == 0 {
			actions = fmt.Sprintf("%d", row.Actions)
		}
		if row.CountsState == "unavailable" {
			actions = "unavailable: " + row.UnavailableReason
		}
		focus := row.Focus
		if focus == "" {
			focus = "none: " + row.FocusAbsentReason
		}
		rows = append(rows, []string{
			name,
			row.Stage,
			marker + " " + reliance,
			actions,
			focus,
		})
		markers = append(markers, marker)
	}
	ambient := snapshot.AmbientProduct
	if ambient == "" {
		ambient = "(none)"
	}
	watermark := snapshot.Watermark
	if watermark == "" {
		watermark = "unknown"
	}
	age := snapshot.ObservedAt
	if age == "" {
		age = "unknown"
	}
	reliance := snapshot.Reliance
	if reliance == "" {
		reliance = "unknown"
	}
	coverage := snapshot.Coverage
	if coverage == "" {
		coverage = "unknown"
	}
	return Projection{
		Header: []string{
			"PRODUCT: " + ambient,
			"WATERMARK: " + watermark,
			"AGE: " + age,
			"SCREEN: " + string(snapshot.Screen),
			"RELIANCE: " + reliance,
			"COVERAGE: " + coverage,
		},
		Columns: columns, Rows: rows, Markers: markers,
	}
}
