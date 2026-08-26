// Package predecessor validates and inventories snapshots harvested from the
// predecessor system. Concord owns the interchange contract; the host-side
// harvest produces the file, and this package refuses anything that does not
// match the pinned schema fail-closed. The enumeration projection here is a
// pure read — it does not open or write the Concord store.
package predecessor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// Snapshot is the strict Go shape of a predecessor harvest. The struct fields
// mirror contracts/predecessor-snapshot.schema.json verbatim; the decoder uses
// DisallowUnknownFields so any drift in either direction fails validation.
type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	CapturedAt    time.Time `json:"captured_at"`
	Producer      string    `json:"producer"`
	SourceSystem  string    `json:"source_system"`
	Projects      []Project `json:"projects"`
}

// Project is one predecessor project and its accumulated counts.
type Project struct {
	ProjectID       string         `json:"project_id"`
	Locator         string         `json:"locator"`
	ArchivedChanges int            `json:"archived_changes"`
	ClosedChanges   int            `json:"closed_changes"`
	ActiveChanges   []ActiveChange `json:"active_changes"`
	WisdomEntries   []WisdomEntry  `json:"wisdom_entries"`
	Reflections     []Reflection   `json:"reflections"`
}

// ActiveChange is a currently-open predecessor change.
type ActiveChange struct {
	ChangeID       string    `json:"change_id"`
	Summary        string    `json:"summary"`
	Status         string    `json:"status"`
	CompletedGates []string  `json:"completed_gates"`
	TasksTotal     int       `json:"tasks_total"`
	TasksDone      int       `json:"tasks_done"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// WisdomEntry is a recorded wisdom item. ChangeID may be empty for entries
// scoped at the project level rather than a single change.
type WisdomEntry struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Content    string    `json:"content"`
	ChangeID   string    `json:"change_id"`
	Promoted   bool      `json:"promoted"`
	RecordedAt time.Time `json:"recorded_at"`
}

// Reflection is a post-change reflection record.
type Reflection struct {
	ID              string    `json:"id"`
	ChangeID        string    `json:"change_id"`
	RecordedAt      time.Time `json:"recorded_at"`
	FrictionCount   int       `json:"friction_count"`
	SuggestionCount int       `json:"suggestion_count"`
}

// MaxSnapshotBytes bounds a single snapshot file. Anything larger is a
// refused payload — Concord does not stream partial harvests.
const MaxSnapshotBytes = 64 * 1024 * 1024

// failureOp is the single op name reported on every typed failure this package
// emits. Keeping it stable keeps CLI diagnostics scannable.
const failureOp = "predecessor.snapshot.load"

// Load reads, decodes, and validates a snapshot file. The decoder rejects
// unknown fields and trailing JSON. Every constraint the schema states is
// enforced after the structural decode so a field-shaped rejection still names
// its cause. Returns a *store.Failure with a closed FailureKind so the CLI can
// surface a typed diagnostic.
func Load(path string) (Snapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, &store.Failure{Kind: store.KindUnavailable, Op: failureOp,
				Detail:         fmt.Sprintf("snapshot file does not exist: %s", path),
				RecoveryAction: "provide a path produced by the host-side harvest"}
		}
		return Snapshot{}, &store.Failure{Kind: store.KindUnavailable, Op: failureOp,
			Detail:         fmt.Sprintf("snapshot path is unavailable: %s", err.Error()),
			RecoveryAction: "verify the path is readable"}
	}
	if info.IsDir() {
		return Snapshot{}, &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("snapshot path is a directory: %s", path),
			RecoveryAction: "provide a file path, not a directory"}
	}
	if info.Size() > MaxSnapshotBytes {
		return Snapshot{}, &store.Failure{Kind: store.KindLimitExceeded, Op: failureOp,
			Detail:         fmt.Sprintf("snapshot file exceeds %d bytes", MaxSnapshotBytes),
			RecoveryAction: "reduce the snapshot or split it into multiple files"}
	}

	file, err := os.Open(path) //nolint:gosec // Load explicitly accepts one operator-selected snapshot file and validates its type and size above.
	if err != nil {
		return Snapshot{}, &store.Failure{Kind: store.KindUnavailable, Op: failureOp,
			Detail:         fmt.Sprintf("cannot open snapshot file: %s", err.Error()),
			RecoveryAction: "verify the path is readable"}
	}
	limited := io.LimitReader(file, MaxSnapshotBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		_ = file.Close()
		return Snapshot{}, &store.Failure{Kind: store.KindUnavailable, Op: failureOp,
			Detail:         fmt.Sprintf("cannot read snapshot file: %s", err.Error()),
			RecoveryAction: "verify the path is readable"}
	}
	if err := file.Close(); err != nil {
		return Snapshot{}, &store.Failure{Kind: store.KindUnavailable, Op: failureOp,
			Detail:         fmt.Sprintf("cannot close snapshot file: %s", err.Error()),
			RecoveryAction: "verify the path is readable"}
	}
	if int64(len(raw)) > MaxSnapshotBytes {
		return Snapshot{}, &store.Failure{Kind: store.KindLimitExceeded, Op: failureOp,
			Detail:         fmt.Sprintf("snapshot file exceeds %d bytes", MaxSnapshotBytes),
			RecoveryAction: "reduce the snapshot or split it into multiple files"}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var snapshot Snapshot
	if err := dec.Decode(&snapshot); err != nil {
		return Snapshot{}, &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("snapshot does not match the schema: %s", err.Error()),
			RecoveryAction: "repair the harvest or regenerate the snapshot"}
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return Snapshot{}, &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         "snapshot contains trailing JSON values",
			RecoveryAction: "produce a single JSON object with no trailing data"}
	}

	if err := validateSnapshot(&snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// validateSnapshot enforces the constraints the schema states but the Go
// decoder cannot: required-but-empty strings, tasks_done <= tasks_total, the
// closed source_system value, and the projects minItems=1 floor.
func validateSnapshot(snapshot *Snapshot) error {
	if snapshot.SchemaVersion != 1 {
		return &store.Failure{Kind: store.KindUnsupportedPayloadVersion, Op: failureOp,
			Detail:         fmt.Sprintf("schema_version %d is not supported; this binary handles schema_version 1", snapshot.SchemaVersion),
			RecoveryAction: "regenerate the snapshot with the current harvest version"}
	}
	if strings.TrimSpace(snapshot.Producer) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         "producer must be a non-empty string",
			RecoveryAction: "identify the harvest procedure that produced the snapshot"}
	}
	if snapshot.SourceSystem != "advance" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("source_system %q is not accepted; this contract accepts only %q", snapshot.SourceSystem, "advance"),
			RecoveryAction: "produce the snapshot from the sanctioned harvest"}
	}
	if snapshot.CapturedAt.IsZero() {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         "captured_at must be an RFC3339 timestamp",
			RecoveryAction: "record the capture time as an RFC3339 timestamp"}
	}
	if len(snapshot.Projects) == 0 {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         "projects must contain at least one entry",
			RecoveryAction: "harvest at least one project before emitting a snapshot"}
	}
	for projectIndex := range snapshot.Projects {
		if err := validateProject(&snapshot.Projects[projectIndex], projectIndex); err != nil {
			return err
		}
	}
	return nil
}

func validateProject(project *Project, projectIndex int) error {
	if strings.TrimSpace(project.ProjectID) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].project_id must be a non-empty string", projectIndex),
			RecoveryAction: "include the predecessor project id"}
	}
	if strings.TrimSpace(project.Locator) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].locator must be a non-empty string", projectIndex),
			RecoveryAction: "include the predecessor project locator"}
	}
	if project.ArchivedChanges < 0 {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].archived_changes must be >= 0", projectIndex),
			RecoveryAction: "non-negative integer required"}
	}
	if project.ClosedChanges < 0 {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].closed_changes must be >= 0", projectIndex),
			RecoveryAction: "non-negative integer required"}
	}
	for changeIndex := range project.ActiveChanges {
		if err := validateActiveChange(&project.ActiveChanges[changeIndex], projectIndex, changeIndex); err != nil {
			return err
		}
	}
	for wisdomIndex := range project.WisdomEntries {
		if err := validateWisdomEntry(&project.WisdomEntries[wisdomIndex], projectIndex, wisdomIndex); err != nil {
			return err
		}
	}
	for reflectionIndex := range project.Reflections {
		if err := validateReflection(&project.Reflections[reflectionIndex], projectIndex, reflectionIndex); err != nil {
			return err
		}
	}
	return nil
}

func validateActiveChange(change *ActiveChange, projectIndex, changeIndex int) error {
	if strings.TrimSpace(change.ChangeID) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].active_changes[%d].change_id must be a non-empty string", projectIndex, changeIndex),
			RecoveryAction: "include the predecessor change id"}
	}
	if strings.TrimSpace(change.Summary) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].active_changes[%d].summary must be a non-empty string", projectIndex, changeIndex),
			RecoveryAction: "include a one-line summary of the change"}
	}
	if strings.TrimSpace(change.Status) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].active_changes[%d].status must be a non-empty string", projectIndex, changeIndex),
			RecoveryAction: "include the change status"}
	}
	if change.TasksTotal < 0 {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].active_changes[%d].tasks_total must be >= 0", projectIndex, changeIndex),
			RecoveryAction: "non-negative integer required"}
	}
	if change.TasksDone < 0 {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].active_changes[%d].tasks_done must be >= 0", projectIndex, changeIndex),
			RecoveryAction: "non-negative integer required"}
	}
	if change.TasksDone > change.TasksTotal {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].active_changes[%d].tasks_done (%d) cannot exceed tasks_total (%d)", projectIndex, changeIndex, change.TasksDone, change.TasksTotal),
			RecoveryAction: "reconcile the task counts in the harvest"}
	}
	for gateIndex, gate := range change.CompletedGates {
		if strings.TrimSpace(gate) == "" {
			return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
				Detail:         fmt.Sprintf("projects[%d].active_changes[%d].completed_gates[%d] must be a non-empty string", projectIndex, changeIndex, gateIndex),
				RecoveryAction: "record each gate by its canonical name"}
		}
	}
	if change.UpdatedAt.IsZero() {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].active_changes[%d].updated_at must be an RFC3339 timestamp", projectIndex, changeIndex),
			RecoveryAction: "record the last update as an RFC3339 timestamp"}
	}
	return nil
}

func validateWisdomEntry(entry *WisdomEntry, projectIndex, wisdomIndex int) error {
	if strings.TrimSpace(entry.ID) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].wisdom_entries[%d].id must be a non-empty string", projectIndex, wisdomIndex),
			RecoveryAction: "include the wisdom entry id"}
	}
	if strings.TrimSpace(entry.Type) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].wisdom_entries[%d].type must be a non-empty string", projectIndex, wisdomIndex),
			RecoveryAction: "include the wisdom entry type"}
	}
	if strings.TrimSpace(entry.Content) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].wisdom_entries[%d].content must be a non-empty string", projectIndex, wisdomIndex),
			RecoveryAction: "include the wisdom entry content"}
	}
	if entry.RecordedAt.IsZero() {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].wisdom_entries[%d].recorded_at must be an RFC3339 timestamp", projectIndex, wisdomIndex),
			RecoveryAction: "record the wisdom entry timestamp as an RFC3339 value"}
	}
	return nil
}

func validateReflection(reflection *Reflection, projectIndex, reflectionIndex int) error {
	if strings.TrimSpace(reflection.ID) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].reflections[%d].id must be a non-empty string", projectIndex, reflectionIndex),
			RecoveryAction: "include the reflection id"}
	}
	if strings.TrimSpace(reflection.ChangeID) == "" {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].reflections[%d].change_id must be a non-empty string", projectIndex, reflectionIndex),
			RecoveryAction: "include the change id the reflection is anchored to"}
	}
	if reflection.RecordedAt.IsZero() {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].reflections[%d].recorded_at must be an RFC3339 timestamp", projectIndex, reflectionIndex),
			RecoveryAction: "record the reflection timestamp as an RFC3339 value"}
	}
	if reflection.FrictionCount < 0 {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].reflections[%d].friction_count must be >= 0", projectIndex, reflectionIndex),
			RecoveryAction: "non-negative integer required"}
	}
	if reflection.SuggestionCount < 0 {
		return &store.Failure{Kind: store.KindInvalidPayload, Op: failureOp,
			Detail:         fmt.Sprintf("projects[%d].reflections[%d].suggestion_count must be >= 0", projectIndex, reflectionIndex),
			RecoveryAction: "non-negative integer required"}
	}
	return nil
}
