package launcher

import (
	"fmt"
	"strings"
)

type Projection struct {
	Header  []string
	Columns []string
	Rows    [][]string
	Markers []string
}

// Project is a deterministic, terminal-independent projection. It performs
// no reads and emits textual reliance markers so meaning survives no-color
// output and screen-reader consumption.
func Project(snapshot Snapshot, _ int) Projection {
	if len(snapshot.Products) > 0 || len(snapshot.WorkItems) > 0 {
		rows := make([][]string, 0, len(snapshot.WorkItems))
		for _, item := range snapshot.WorkItems {
			state := item.SessionState
			if state == "" {
				state = "idle"
				if item.Live > 0 {
					state = "live"
				}
			}
			rows = append(rows, []string{item.ProductID, item.ID + " " + item.Name, item.Lifecycle, state})
		}
		return Projection{
			Header:  []string{"WORK BROWSER", "PRODUCTS: " + fmt.Sprintf("%d", len(snapshot.Products))},
			Columns: []string{"Product", "Work", "Lifecycle", "Session"},
			Rows:    rows,
		}
	}
	columns := []string{"Product", "Stage", "Reliance", "Actions", "Focus"}
	rows := make([][]string, 0, len(snapshot.Rows))
	markers := make([]string, 0, len(snapshot.Rows))
	// Each block below draws from the data the snapshot holds. None asks which
	// context is current, and the more specific data wins by assigning rows
	// outright rather than appending to a less specific table.
	drilled := false
	if snapshot.Section == SectionDomains {
		drilled = true
		columns = []string{"Domain", "Marker", "Parent", "Relations"}
		rows = nil
		for _, domain := range snapshot.Domains.Domains {
			marker := "DOMAIN"
			if domain.Home {
				marker = "HOME"
			}
			parent := domain.ParentID
			if parent == "" {
				parent = "-"
			}
			relations := 0
			for _, relation := range snapshot.Domains.Relations {
				if relation.Source == domain.ID || relation.Target == domain.ID {
					relations++
				}
			}
			rows = append(rows, []string{domain.ID + " " + domain.Name, marker, parent, fmt.Sprintf("r%d law%d act%d", relations, domain.CurrentLawCount, domain.ActiveWorkCount)})
		}
		if snapshot.Domains.State == "unavailable" {
			rows = append(rows, []string{"unavailable: " + snapshot.Domains.Reason, "!", "-", "-"})
		}
	}
	if snapshot.RankedWorkRead && snapshot.Section != SectionDomains && snapshot.Detail.Item.ID == "" {
		drilled = true
		columns = []string{"Work", "Kind", "Priority", "Urgency", "Readiness", "Lifecycle", "TerminalAt", "Projects"}
		rows = nil
		for _, item := range snapshot.Ranked {
			terminalAt := item.TerminalAt
			if terminalAt == "" {
				terminalAt = "-"
			}
			rows = append(rows, []string{item.ID + " " + item.Title, item.Kind, fmt.Sprintf("%d", item.Priority), item.Urgency, item.Readiness(), item.Lifecycle, terminalAt, fmt.Sprintf("%d", item.ProjectCount)})
		}
		if len(snapshot.Ranked) == 0 {
			rows = append(rows, drillDownStateRow(rankedSectionState(snapshot), columns))
		}
	}
	if snapshot.Detail.Item.ID != "" && snapshot.Section != SectionDomains {
		drilled = true
		columns = []string{"Work", "Lifecycle", "Priority", "Urgency", "Projects", "Section"}
		rows = nil
		item := snapshot.Detail.Item
		rows = append(rows, []string{item.ID + " " + item.Title, item.Lifecycle, fmt.Sprintf("%d", item.Priority), item.Urgency, fmt.Sprintf("%d", item.ProjectCount), string(snapshot.Section)})
	}
	// A drill-down table already claimed the rows. The Product table below
	// fills them only when no more specific read did.
	if drilled {
		return Projection{Header: []string{"PRODUCT: " + productText(snapshot.AmbientProduct), "WATERMARK: " + watermarkText(snapshot.Watermark), "AGE: " + watermarkText(snapshot.ObservedAt), "RELIANCE: " + relianceText(snapshot.Reliance), "COVERAGE: " + coverageText(snapshot.Coverage), "SECTION: " + string(snapshot.Section)}, Columns: columns, Rows: rows, Markers: []string{strings.ToUpper(string(snapshot.Section))}}
	}
	for _, row := range snapshot.Rows {
		name := row.Name + row.NameSuffix
		reliance := row.Reliance
		marker := "OK"
		if reliance != "clear" && reliance != "ready" && reliance != "" {
			marker = "!"
		}
		actions := fmt.Sprintf("ip:%d b:%d r:%d p:%d a:%d", row.InProgress, row.Blocked, row.Ready, row.ActiveProblems, row.ApprovalRequired)
		if row.OverdueAwaits > 0 {
			actions += fmt.Sprintf(" overdue:%d", row.OverdueAwaits)
		}
		if row.FocusAttentionKind == "approval_required" && row.FocusBlockedSessionCount > 0 {
			actions += fmt.Sprintf(" (waiting: %s)", row.FocusOldestBlockedSession)
		}
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
			"RELIANCE: " + reliance,
			"COVERAGE: " + coverage,
		},
		Columns: columns, Rows: rows, Markers: markers,
	}
}

func watermarkText(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
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
// section already declared, mirroring the Domains unavailable row.
func drillDownStateRow(state string, columns []string) []string {
	row := []string{state}
	for i := 1; i < len(columns); i++ {
		row = append(row, "-")
	}
	return row
}
func relianceText(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
func coverageText(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// productText names the ambient Product, or states its absence, so a header
// never renders an empty field.
func productText(product string) string {
	if product == "" {
		return "(none)"
	}
	return product
}
