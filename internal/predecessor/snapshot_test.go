package predecessor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// fixtureCapturedAt is a fixed timestamp for stable test assertions.
var fixtureCapturedAt = time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)

// buildSyntheticSnapshot constructs a two-project predecessor harvest with
// obviously fictional identifiers, varied counts, and enough active changes
// in project one to exercise the ActiveChangeListCap ordering. Project two
// carries a project-level wisdom entry (empty change_id) and a promoted one,
// plus one reflection.
func buildSyntheticSnapshot() Snapshot {
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	activeChanges := make([]ActiveChange, 0, 55)
	for i := 0; i < 55; i++ {
		// The last five entries share the newest timestamp so the tie-break
		// rule (change_id ascending) is visible at the top of the listed IDs.
		// The first fifty entries have unique timestamps staggered from base.
		var stamp time.Time
		if i >= 50 {
			stamp = base.Add(50 * time.Minute)
		} else {
			stamp = base.Add(time.Duration(i) * time.Minute)
		}
		activeChanges = append(activeChanges, ActiveChange{
			ChangeID:       fmt.Sprintf("synth-change-%03d", i+1),
			Summary:        fmt.Sprintf("synthetic active change %d", i+1),
			Status:         "draft",
			CompletedGates: []string{"proposal"},
			TasksTotal:     4,
			TasksDone:      1,
			UpdatedAt:      stamp,
		})
	}
	return Snapshot{
		SchemaVersion: 1,
		CapturedAt:    fixtureCapturedAt,
		Producer:      "synthetic-harvest-v1",
		SourceSystem:  "advance",
		Projects: []Project{
			{
				ProjectID:       "synth-proj-alpha",
				Locator:         "synth://alpha",
				ArchivedChanges: 7,
				ClosedChanges:   4,
				ActiveChanges:   activeChanges,
				WisdomEntries: []WisdomEntry{
					{
						ID:         "synth-wisdom-alpha-1",
						Type:       "lesson",
						Content:    "synthetic alpha lesson",
						ChangeID:   "synth-change-001",
						Promoted:   true,
						RecordedAt: fixtureCapturedAt,
					},
				},
				Reflections: []Reflection{
					{
						ID:              "synth-reflection-alpha-1",
						ChangeID:        "synth-change-001",
						RecordedAt:      fixtureCapturedAt,
						FrictionCount:   2,
						SuggestionCount: 1,
					},
				},
			},
			{
				ProjectID:       "synth-proj-beta",
				Locator:         "synth://beta",
				ArchivedChanges: 3,
				ClosedChanges:   1,
				ActiveChanges: []ActiveChange{
					{
						ChangeID:       "synth-change-beta-1",
						Summary:        "synthetic beta change",
						Status:         "discovery",
						CompletedGates: nil,
						TasksTotal:     0,
						TasksDone:      0,
						UpdatedAt:      fixtureCapturedAt,
					},
				},
				WisdomEntries: []WisdomEntry{
					{
						ID:         "synth-wisdom-beta-project-level",
						Type:       "rule",
						Content:    "project-level synthetic wisdom",
						ChangeID:   "",
						Promoted:   false,
						RecordedAt: fixtureCapturedAt,
					},
					{
						ID:         "synth-wisdom-beta-promoted",
						Type:       "rule",
						Content:    "promoted synthetic wisdom",
						ChangeID:   "synth-change-beta-1",
						Promoted:   true,
						RecordedAt: fixtureCapturedAt,
					},
				},
				Reflections: nil,
			},
		},
	}
}

// writeSnapshotFile encodes the snapshot as JSON and writes it to a fresh
// temp file, returning the path. Cleanup is the caller's responsibility via
// t.TempDir().
func writeSnapshotFile(t *testing.T, snapshot Snapshot) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic-snapshot.json")
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatalf("marshal synthetic snapshot: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return path
}

func TestLoadSyntheticFixture(t *testing.T) {
	path := writeSnapshotFile(t, buildSyntheticSnapshot())
	snapshot, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) error = %v, want nil", path, err)
	}
	if snapshot.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", snapshot.SchemaVersion)
	}
	if snapshot.SourceSystem != "advance" {
		t.Fatalf("SourceSystem = %q, want %q", snapshot.SourceSystem, "advance")
	}
	if len(snapshot.Projects) != 2 {
		t.Fatalf("len(Projects) = %d, want 2", len(snapshot.Projects))
	}
	if snapshot.Projects[1].WisdomEntries[0].ChangeID != "" {
		t.Fatalf("project-level wisdom entry should have empty change_id, got %q", snapshot.Projects[1].WisdomEntries[0].ChangeID)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	_, err := Load(missing)
	if err == nil {
		t.Fatalf("Load(missing) returned nil error")
	}
	failure := failureFromError(t, err)
	if failure.Kind != store.KindUnavailable {
		t.Fatalf("missing file Kind = %q, want %q", failure.Kind, store.KindUnavailable)
	}
}

func TestLoadRejectsDirectoryPath(t *testing.T) {
	directory := t.TempDir()
	_, err := Load(directory)
	if err == nil {
		t.Fatalf("Load(directory) returned nil error")
	}
	failure := failureFromError(t, err)
	if failure.Kind != store.KindInvalidPayload {
		t.Fatalf("directory Kind = %q, want %q", failure.Kind, store.KindInvalidPayload)
	}
}

func TestLoadRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversize.json")
	// Write one byte beyond the cap so the LimitReader refuses. The cap is
	// enforced twice: once via Stat, once via the read limit.
	handle, err := os.Create(path)
	if err != nil {
		t.Fatalf("create oversized file: %v", err)
	}
	if _, err := handle.Write(make([]byte, MaxSnapshotBytes+1)); err != nil {
		t.Fatalf("write oversized body: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close oversized file: %v", err)
	}
	_, err = Load(path)
	failure := failureFromError(t, err)
	if failure.Kind != store.KindLimitExceeded {
		t.Fatalf("oversized Kind = %q, want %q (detail=%s)", failure.Kind, store.KindLimitExceeded, failure.Detail)
	}
}

func TestLoadRejectsEmptyProjectsArray(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	snapshot.Projects = nil
	path := writeSnapshotFile(t, snapshot)
	_, err := Load(path)
	failure := failureFromError(t, err)
	if failure.Kind != store.KindInvalidPayload {
		t.Fatalf("empty projects Kind = %q, want %q", failure.Kind, store.KindInvalidPayload)
	}
	if !strings.Contains(failure.Detail, "projects must contain at least one entry") {
		t.Fatalf("empty projects detail = %q, want projects-minimum message", failure.Detail)
	}
}

func TestLoadRejectsWrongSchemaVersion(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	snapshot.SchemaVersion = 2
	path := writeSnapshotFile(t, snapshot)
	_, err := Load(path)
	failure := failureFromError(t, err)
	if failure.Kind != store.KindUnsupportedPayloadVersion {
		t.Fatalf("wrong schema_version Kind = %q, want %q", failure.Kind, store.KindUnsupportedPayloadVersion)
	}
}

func TestLoadRejectsTasksDoneOverTotal(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	snapshot.Projects[0].ActiveChanges[0].TasksDone = 5
	snapshot.Projects[0].ActiveChanges[0].TasksTotal = 4
	path := writeSnapshotFile(t, snapshot)
	_, err := Load(path)
	failure := failureFromError(t, err)
	if failure.Kind != store.KindInvalidPayload {
		t.Fatalf("tasks overflow Kind = %q, want %q", failure.Kind, store.KindInvalidPayload)
	}
	if !strings.Contains(failure.Detail, "cannot exceed tasks_total") {
		t.Fatalf("tasks overflow detail = %q, want reconciliation message", failure.Detail)
	}
}

func TestLoadRejectsBadTimestamp(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	snapshot.Projects[1].Reflections = []Reflection{{
		ID:              "synth-reflection-bad",
		ChangeID:        "synth-change-beta-1",
		RecordedAt:      time.Time{}, // zero — forces parse failure when JSON encoded as "0001-01-01T00:00:00Z" is invalid for the schema's date-time; a manually broken string exercises the path the same way.
		FrictionCount:   0,
		SuggestionCount: 0,
	}}
	// Hand-craft the JSON so the timestamp is provably unparseable. The Go
	// stdlib happily serializes a zero time as "0001-01-01T00:00:00Z", which
	// would parse, so we need to write the literal broken string to force the
	// failure the validator must surface.
	path := filepath.Join(t.TempDir(), "bad-timestamp.json")
	body := `{
        "schema_version": 1,
        "captured_at": "2026-08-21T14:00:00Z",
        "producer": "synthetic-harvest-v1",
        "source_system": "advance",
        "projects": [{
            "project_id": "synth-proj-bad-ts",
            "locator": "synth://bad",
            "archived_changes": 0,
            "closed_changes": 0,
            "active_changes": [],
            "wisdom_entries": [],
            "reflections": [{
                "id": "synth-reflection-bad",
                "change_id": "synth-change-bad",
                "recorded_at": "not-an-rfc3339-value",
                "friction_count": 0,
                "suggestion_count": 0
            }]
        }]
    }`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write bad timestamp: %v", err)
	}
	_, err := Load(path)
	failure := failureFromError(t, err)
	if failure.Kind != store.KindInvalidPayload {
		t.Fatalf("bad timestamp Kind = %q, want %q (detail=%s)", failure.Kind, store.KindInvalidPayload, failure.Detail)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	snapshot := buildSyntheticSnapshot()
	snapshot.Projects[0].ActiveChanges[0].Summary = "valid summary" // keep schema valid
	path := filepath.Join(t.TempDir(), "unknown-field.json")
	body := `{
        "schema_version": 1,
        "captured_at": "2026-08-21T14:00:00Z",
        "producer": "synthetic-harvest-v1",
        "source_system": "advance",
        "projects": [{
            "project_id": "synth-proj-unknown",
            "locator": "synth://unknown",
            "archived_changes": 0,
            "closed_changes": 0,
            "active_changes": [],
            "wisdom_entries": [],
            "reflections": [],
            "unexpected_field": "fail-closed"
        }]
    }`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write unknown-field file: %v", err)
	}
	_, err := Load(path)
	failure := failureFromError(t, err)
	if failure.Kind != store.KindInvalidPayload {
		t.Fatalf("unknown field Kind = %q, want %q", failure.Kind, store.KindInvalidPayload)
	}
	if !strings.Contains(failure.Detail, "unexpected_field") && !strings.Contains(failure.Detail, "unknown field") {
		t.Fatalf("unknown field detail = %q, want unknown-field wording", failure.Detail)
	}
}

func TestLoadRejectsTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trailing.json")
	body := []byte(`{"schema_version":1,"captured_at":"2026-08-21T14:00:00Z","producer":"synthetic-harvest-v1","source_system":"advance","projects":[{"project_id":"synth-proj-trailing","locator":"synth://trailing","archived_changes":0,"closed_changes":0,"active_changes":[],"wisdom_entries":[],"reflections":[]}]}{"extra":"object"}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write trailing file: %v", err)
	}
	_, err := Load(path)
	failure := failureFromError(t, err)
	if failure.Kind != store.KindInvalidPayload {
		t.Fatalf("trailing Kind = %q, want %q", failure.Kind, store.KindInvalidPayload)
	}
	if !strings.Contains(failure.Detail, "trailing") {
		t.Fatalf("trailing detail = %q, want trailing-JSON message", failure.Detail)
	}
}

func TestLoadRejectsMissingRequiredField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-field.json")
	body := []byte(`{"schema_version":1,"captured_at":"2026-08-21T14:00:00Z","source_system":"advance","projects":[{"project_id":"synth-proj-missing","locator":"synth://missing","archived_changes":0,"closed_changes":0,"active_changes":[],"wisdom_entries":[],"reflections":[]}]}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write missing-field file: %v", err)
	}
	_, err := Load(path)
	failure := failureFromError(t, err)
	if failure.Kind != store.KindInvalidPayload {
		t.Fatalf("missing required Kind = %q, want %q", failure.Kind, store.KindInvalidPayload)
	}
	if !strings.Contains(failure.Detail, "producer") {
		t.Fatalf("missing required detail = %q, want producer-missing wording", failure.Detail)
	}
}

// failureFromError extracts the typed *store.Failure so per-case assertions can
// branch on Kind and Detail. Other error shapes fail the test loudly.
func failureFromError(t *testing.T, err error) *store.Failure {
	t.Helper()
	var failure *store.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error is not a *store.Failure: %v", err)
	}
	return failure
}
