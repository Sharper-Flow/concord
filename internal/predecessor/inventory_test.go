package predecessor

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestInventoryAggregatesTotalsFromPerProjectCounts(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	report := Inventory(snapshot)

	expectedTotals := Totals{
		Projects:        2,
		ActiveChanges:   55 + 1,
		ArchivedChanges: 7 + 3,
		ClosedChanges:   4 + 1,
		WisdomEntries:   1 + 2,
		Reflections:     1 + 0,
	}
	if report.Totals != expectedTotals {
		t.Fatalf("Totals = %+v, want %+v", report.Totals, expectedTotals)
	}
}

func TestInventoryPreservesProvenanceEcho(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	report := Inventory(snapshot)

	if report.SchemaVersion != snapshot.SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", report.SchemaVersion, snapshot.SchemaVersion)
	}
	if report.Producer != snapshot.Producer {
		t.Fatalf("Producer = %q, want %q", report.Producer, snapshot.Producer)
	}
	if report.SourceSystem != snapshot.SourceSystem {
		t.Fatalf("SourceSystem = %q, want %q", report.SourceSystem, snapshot.SourceSystem)
	}
	if !report.CapturedAt.Equal(snapshot.CapturedAt) {
		t.Fatalf("CapturedAt = %v, want %v", report.CapturedAt, snapshot.CapturedAt)
	}
}

func TestInventoryPerProjectCountsMatchSnapshot(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	report := Inventory(snapshot)

	if len(report.Projects) != len(snapshot.Projects) {
		t.Fatalf("len(Projects) = %d, want %d", len(report.Projects), len(snapshot.Projects))
	}
	for index, project := range snapshot.Projects {
		got := report.Projects[index]
		if got.ProjectID != project.ProjectID {
			t.Fatalf("projects[%d].ProjectID = %q, want %q", index, got.ProjectID, project.ProjectID)
		}
		if got.Locator != project.Locator {
			t.Fatalf("projects[%d].Locator = %q, want %q", index, got.Locator, project.Locator)
		}
		if got.ActiveChangesCount != len(project.ActiveChanges) {
			t.Fatalf("projects[%d].ActiveChangesCount = %d, want %d", index, got.ActiveChangesCount, len(project.ActiveChanges))
		}
		if got.ArchivedChanges != project.ArchivedChanges {
			t.Fatalf("projects[%d].ArchivedChanges = %d, want %d", index, got.ArchivedChanges, project.ArchivedChanges)
		}
		if got.ClosedChanges != project.ClosedChanges {
			t.Fatalf("projects[%d].ClosedChanges = %d, want %d", index, got.ClosedChanges, project.ClosedChanges)
		}
		if got.WisdomEntriesCount != len(project.WisdomEntries) {
			t.Fatalf("projects[%d].WisdomEntriesCount = %d, want %d", index, got.WisdomEntriesCount, len(project.WisdomEntries))
		}
		if got.ReflectionsCount != len(project.Reflections) {
			t.Fatalf("projects[%d].ReflectionsCount = %d, want %d", index, got.ReflectionsCount, len(project.Reflections))
		}
	}
}

func TestInventoryCapsActiveChangeIDsAtMostRecentByUpdatedAt(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	report := Inventory(snapshot)

	alpha := report.Projects[0]
	if alpha.ActiveChangesCount != 55 {
		t.Fatalf("alpha ActiveChangesCount = %d, want 55", alpha.ActiveChangesCount)
	}
	if alpha.ActiveChangesListed != ActiveChangeListCap {
		t.Fatalf("alpha ActiveChangesListed = %d, want %d", alpha.ActiveChangesListed, ActiveChangeListCap)
	}
	if alpha.ActiveChangesOmitted != 5 {
		t.Fatalf("alpha ActiveChangesOmitted = %d, want 5", alpha.ActiveChangesOmitted)
	}
	if len(alpha.ActiveChangeIDs) != ActiveChangeListCap {
		t.Fatalf("alpha ActiveChangeIDs length = %d, want %d", len(alpha.ActiveChangeIDs), ActiveChangeListCap)
	}
	// The last five synthetic entries (change_id 051..055) share the newest
	// timestamp, so they lead the listed IDs in change_id ascending order.
	// They are followed by entries with strictly newer-than-base timestamps
	// in descending updated_at order: synth-change-050, 049, ..., 006.
	wantPrefix := []string{
		"synth-change-051",
		"synth-change-052",
		"synth-change-053",
		"synth-change-054",
		"synth-change-055",
		"synth-change-050",
		"synth-change-049",
		"synth-change-048",
		"synth-change-047",
		"synth-change-046",
	}
	for i, want := range wantPrefix {
		if alpha.ActiveChangeIDs[i] != want {
			t.Fatalf("alpha.ActiveChangeIDs[%d] = %q, want %q", i, alpha.ActiveChangeIDs[i], want)
		}
	}
	// The oldest five entries (synth-change-001 .. 005) are omitted because
	// the cap is exactly 50.
	last := alpha.ActiveChangeIDs[len(alpha.ActiveChangeIDs)-1]
	if last != "synth-change-006" {
		t.Fatalf("alpha.ActiveChangeIDs[-1] = %q, want %q (the 50th most-recent)", last, "synth-change-006")
	}
}

func TestInventoryReportsOmittedCountForProjectsUnderCap(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	report := Inventory(snapshot)

	beta := report.Projects[1]
	if beta.ActiveChangesCount != 1 {
		t.Fatalf("beta ActiveChangesCount = %d, want 1", beta.ActiveChangesCount)
	}
	if beta.ActiveChangesListed != 1 {
		t.Fatalf("beta ActiveChangesListed = %d, want 1", beta.ActiveChangesListed)
	}
	if beta.ActiveChangesOmitted != 0 {
		t.Fatalf("beta ActiveChangesOmitted = %d, want 0", beta.ActiveChangesOmitted)
	}
	if !reflect.DeepEqual(beta.ActiveChangeIDs, []string{"synth-change-beta-1"}) {
		t.Fatalf("beta ActiveChangeIDs = %v, want [synth-change-beta-1]", beta.ActiveChangeIDs)
	}
}

func TestInventoryTotalsEqualSumOfPerProjectCounts(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	report := Inventory(snapshot)

	var (
		activeSum      int
		archivedSum    int
		closedSum      int
		wisdomSum      int
		reflectionsSum int
	)
	for _, project := range report.Projects {
		activeSum += project.ActiveChangesCount
		archivedSum += project.ArchivedChanges
		closedSum += project.ClosedChanges
		wisdomSum += project.WisdomEntriesCount
		reflectionsSum += project.ReflectionsCount
	}
	if report.Totals.ActiveChanges != activeSum {
		t.Fatalf("Totals.ActiveChanges = %d, want sum %d", report.Totals.ActiveChanges, activeSum)
	}
	if report.Totals.ArchivedChanges != archivedSum {
		t.Fatalf("Totals.ArchivedChanges = %d, want sum %d", report.Totals.ArchivedChanges, archivedSum)
	}
	if report.Totals.ClosedChanges != closedSum {
		t.Fatalf("Totals.ClosedChanges = %d, want sum %d", report.Totals.ClosedChanges, closedSum)
	}
	if report.Totals.WisdomEntries != wisdomSum {
		t.Fatalf("Totals.WisdomEntries = %d, want sum %d", report.Totals.WisdomEntries, wisdomSum)
	}
	if report.Totals.Reflections != reflectionsSum {
		t.Fatalf("Totals.Reflections = %d, want sum %d", report.Totals.Reflections, reflectionsSum)
	}
}

func TestInventoryEmptySnapshotYieldsEmptyProjectsArray(t *testing.T) {
	// The schema requires at least one project, so this case is exercised at
	// the validator level. The pure projection must still be safe: passing a
	// zero-value Snapshot is allowed because Inventory is a pure function.
	empty := Snapshot{}
	report := Inventory(empty)
	if report.Totals != (Totals{}) {
		t.Fatalf("empty Totals = %+v, want zero value", report.Totals)
	}
	if len(report.Projects) != 0 {
		t.Fatalf("empty Projects length = %d, want 0", len(report.Projects))
	}
	if report.CapturedAt != (time.Time{}) {
		t.Fatalf("empty CapturedAt = %v, want zero value", report.CapturedAt)
	}
}

func TestInventoryJSONShape(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	report := Inventory(snapshot)

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	decoded := struct {
		SchemaVersion int             `json:"schema_version"`
		Producer      string          `json:"producer"`
		SourceSystem  string          `json:"source_system"`
		CapturedAt    time.Time       `json:"captured_at"`
		Totals        Totals          `json:"totals"`
		Projects      []ProjectReport `json:"projects"`
	}{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if decoded.SchemaVersion != 1 || decoded.SourceSystem != "advance" || decoded.Producer != snapshot.Producer {
		t.Fatalf("decoded provenance = %+v, want schema_version=1 source_system=advance producer=%q", decoded, snapshot.Producer)
	}
	if decoded.Totals.Projects != 2 {
		t.Fatalf("decoded Totals.Projects = %d, want 2", decoded.Totals.Projects)
	}
	if len(decoded.Projects) != 2 {
		t.Fatalf("decoded Projects length = %d, want 2", len(decoded.Projects))
	}
}
