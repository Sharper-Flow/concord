package predecessor

import (
	"sort"
	"time"
)

// ActiveChangeListCap bounds the number of change IDs reported per project in
// the enumeration. A snapshot can carry an arbitrarily long active list; the
// inventory is the operator's first read, not a full export, so we cap and
// disclose the omission count.
const ActiveChangeListCap = 50

// Report is the bounded enumeration summary emitted by the operator verb. The
// shape is fixed; totals must always equal the sum of the per-project counts.
// Surfaces is the preflight enumeration CD-0097 D2 requires: every surface
// the mode covers, included or excluded, with counts and capture gaps, before
// any import starts. A surface the inventory does not enumerate does not
// migrate.
type Report struct {
	SchemaVersion int             `json:"schema_version"`
	Producer      string          `json:"producer"`
	SourceSystem  string          `json:"source_system"`
	CapturedAt    time.Time       `json:"captured_at"`
	Totals        Totals          `json:"totals"`
	Surfaces      []SurfaceReport `json:"surfaces"`
	Projects      []ProjectReport `json:"projects"`
}

// SurfaceReport is one mode surface's preflight entry: whether the import
// includes it, the snapshot count it carries, the route it takes when
// excluded, and the structural capture gap when one exists.
type SurfaceReport struct {
	Surface    string `json:"surface"`
	Inclusion  string `json:"inclusion"`
	Count      int    `json:"count"`
	Route      string `json:"route"`
	CaptureGap string `json:"capture_gap,omitempty"`
}

// Totals aggregates the snapshot-wide counts.
type Totals struct {
	Projects        int `json:"projects"`
	ActiveChanges   int `json:"active_changes"`
	ArchivedChanges int `json:"archived_changes"`
	ClosedChanges   int `json:"closed_changes"`
	WisdomEntries   int `json:"wisdom_entries"`
	Reflections     int `json:"reflections"`
}

// ProjectReport is one project's counts plus its capped active-change list.
type ProjectReport struct {
	ProjectID            string   `json:"project_id"`
	Locator              string   `json:"locator"`
	ActiveChangesCount   int      `json:"active_changes_count"`
	ArchivedChanges      int      `json:"archived_changes"`
	ClosedChanges        int      `json:"closed_changes"`
	WisdomEntriesCount   int      `json:"wisdom_entries_count"`
	ReflectionsCount     int      `json:"reflections_count"`
	ActiveChangeIDs      []string `json:"active_change_ids"`
	ActiveChangesListed  int      `json:"active_changes_listed"`
	ActiveChangesOmitted int      `json:"active_changes_omitted"`
}

// Inventory projects a validated Snapshot into the operator-facing Report.
// It is a pure function: no I/O, no store access, no clock dependency. The
// ordering of active change IDs is most-recent-first by updated_at, with ties
// broken by change_id ascending so the output is stable across runs.
func Inventory(snapshot Snapshot) Report {
	report := Report{
		SchemaVersion: snapshot.SchemaVersion,
		Producer:      snapshot.Producer,
		SourceSystem:  snapshot.SourceSystem,
		CapturedAt:    snapshot.CapturedAt,
		Projects:      make([]ProjectReport, 0, len(snapshot.Projects)),
	}
	for _, project := range snapshot.Projects {
		projectReport := ProjectReport{
			ProjectID:          project.ProjectID,
			Locator:            project.Locator,
			ActiveChangesCount: len(project.ActiveChanges),
			ArchivedChanges:    project.ArchivedChanges,
			ClosedChanges:      project.ClosedChanges,
			WisdomEntriesCount: len(project.WisdomEntries),
			ReflectionsCount:   len(project.Reflections),
			ActiveChangeIDs:    capActiveChanges(project.ActiveChanges),
		}
		projectReport.ActiveChangesListed = len(projectReport.ActiveChangeIDs)
		projectReport.ActiveChangesOmitted = projectReport.ActiveChangesCount - projectReport.ActiveChangesListed
		report.Projects = append(report.Projects, projectReport)
		report.Totals.Projects++
		report.Totals.ActiveChanges += projectReport.ActiveChangesCount
		report.Totals.ArchivedChanges += projectReport.ArchivedChanges
		report.Totals.ClosedChanges += projectReport.ClosedChanges
		report.Totals.WisdomEntries += projectReport.WisdomEntriesCount
		report.Totals.Reflections += projectReport.ReflectionsCount
	}
	report.Surfaces = preflightSurfaces(report.Totals)
	return report
}

// preflightSurfaces projects the snapshot totals into the mode's surface
// enumeration. Every surface in the route table appears exactly once, in
// route-table order, so two runs of the same snapshot produce identical
// surfaces blocks.
func preflightSurfaces(totals Totals) []SurfaceReport {
	counts := map[string]int{
		SurfaceSpecifications:  0,
		SurfaceActiveWork:      totals.ActiveChanges,
		SurfaceTerminalHistory: totals.ArchivedChanges + totals.ClosedChanges,
		SurfaceWisdom:          totals.WisdomEntries,
		SurfaceReflections:     totals.Reflections,
	}
	surfaces := make([]SurfaceReport, 0, len(surfaceRoutes))
	for _, route := range surfaceRoutes {
		inclusion := InclusionExcluded
		if route.Importable {
			inclusion = InclusionIncluded
		}
		surfaces = append(surfaces, SurfaceReport{
			Surface:    route.Surface,
			Inclusion:  inclusion,
			Count:      counts[route.Surface],
			Route:      route.Route,
			CaptureGap: route.CaptureGap,
		})
	}
	return surfaces
}

// capActiveChanges returns the most recent change IDs, capped at
// ActiveChangeListCap, with ties broken by change_id ascending so the slice is
// stable across runs of the same snapshot.
func capActiveChanges(changes []ActiveChange) []string {
	if len(changes) == 0 {
		return []string{}
	}
	indices := make([]int, len(changes))
	for i := range changes {
		indices[i] = i
	}
	sort.SliceStable(indices, func(a, b int) bool {
		left := changes[indices[a]]
		right := changes[indices[b]]
		if left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.ChangeID < right.ChangeID
		}
		if left.UpdatedAt.After(right.UpdatedAt) {
			return true
		}
		return false
	})
	limit := len(indices)
	if limit > ActiveChangeListCap {
		limit = ActiveChangeListCap
	}
	ids := make([]string, limit)
	for i := 0; i < limit; i++ {
		ids[i] = changes[indices[i]].ChangeID
	}
	return ids
}
