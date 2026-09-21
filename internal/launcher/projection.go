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
	columns := []string{"Product", "Stage", "Reliance", "Actions", "Focus"}
	rows := make([][]string, 0, len(snapshot.Rows))
	markers := make([]string, 0, len(snapshot.Rows))
	if snapshot.Screen == ScreenProduct && snapshot.Section == SectionDomains {
		columns = []string{"Domain", "Marker", "Parent", "Relations"}
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
	if snapshot.Screen == ScreenProduct && snapshot.Section != SectionDomains {
		columns = []string{"Work", "Kind", "Priority", "Urgency", "Readiness", "Lifecycle", "TerminalAt", "Projects"}
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
	if snapshot.Screen == ScreenWork {
		columns = []string{"Work", "Lifecycle", "Priority", "Urgency", "Projects", "Section"}
		item := snapshot.Detail.Item
		rows = append(rows, []string{item.ID + " " + item.Title, item.Lifecycle, fmt.Sprintf("%d", item.Priority), item.Urgency, fmt.Sprintf("%d", item.ProjectCount), string(snapshot.Section)})
	}
	if snapshot.Screen != ScreenPortfolio {
		ambient := snapshot.AmbientProduct
		if ambient == "" {
			ambient = "(none)"
		}
		return Projection{Header: []string{"PRODUCT: " + ambient, "WATERMARK: " + watermarkText(snapshot.Watermark), "AGE: " + watermarkText(snapshot.ObservedAt), "SCREEN: " + string(snapshot.Screen), "RELIANCE: " + relianceText(snapshot.Reliance), "COVERAGE: " + coverageText(snapshot.Coverage), "SECTION: " + string(snapshot.Section)}, Columns: columns, Rows: rows, Markers: []string{strings.ToUpper(string(snapshot.Section))}}
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
			"SCREEN: " + string(snapshot.Screen),
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

// RankedColumn is one data column in the Product work projection.
type RankedColumn struct {
	Key, Value string
}

// RankedColumns returns the stable data columns for a work item.
func RankedColumns(item RankedWork, snapshot Snapshot) []RankedColumn {
	urgency := item.Urgency
	if urgency == "" {
		urgency = "standard"
	}
	kind := item.Kind
	if kind == "" {
		kind = "-"
	}
	live := ""
	if item.Live > 0 {
		live = "yes"
	} else if snapshot.ActiveWorkOnly && !item.Backlog {
		live = "no"
	}
	return []RankedColumn{
		{Key: "issue", Value: item.LinearIssueKey},
		{Key: "live", Value: live},
		{Key: "kind", Value: kind},
		{Key: "priority", Value: fmt.Sprintf("%d", item.Priority)},
		{Key: "urgency", Value: urgency},
		{Key: "lifecycle", Value: item.Lifecycle},
		{Key: "terminal", Value: item.TerminalAt},
		{Key: "projects", Value: fmt.Sprintf("%d", item.ProjectCount)},
	}
}

// CollapsedRankedKeys returns columns that carry one value across visible rows.
func CollapsedRankedKeys(ranked []RankedWork, snapshot Snapshot) map[string]bool {
	constant := map[string]bool{}
	if len(ranked) == 0 {
		return constant
	}
	first := RankedColumns(ranked[0], snapshot)
	for _, column := range first {
		same := true
		for _, item := range ranked[1:] {
			values := RankedColumns(item, snapshot)
			found := false
			for _, value := range values {
				if value.Key == column.Key {
					found = value.Value == column.Value
					break
				}
			}
			if !found {
				same = false
				break
			}
		}
		if same {
			constant[column.Key] = true
		}
	}
	return constant
}
