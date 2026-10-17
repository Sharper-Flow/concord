package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestPredecessorImportRepeatedRunAppliesOnlyNewEffects pins the new-effects
// half of the CD-0097 D4 replay contract: a repeated run that selects more of
// the harvest imports exactly the new selections under their derived
// identities, reports the earlier selections as already imported, and is a
// no-op once replayed unchanged.
func TestPredecessorImportRepeatedRunAppliesOnlyNewEffects(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	snapshotPath := writeSyntheticSnapshot(t)
	narrow := predecessorImportRequest(t, snapshotPath)
	narrow["select_change_ids"] = []string{"synth-change-alpha-1"}
	if code, _, diag := runPredecessorImportRequest(t, dbPath, narrow); code != 0 {
		t.Fatalf("narrow import exit=%d, want 0; stderr=%q", code, diag)
	}

	wider := predecessorImportRequest(t, snapshotPath)
	type replayReport struct {
		NoOp            bool                    `json:"no_op"`
		ProductsCreated int                     `json:"products_created"`
		ProjectsCreated int                     `json:"projects_created"`
		WorkImported    int                     `json:"work_imported"`
		AlreadyImported int                     `json:"already_imported"`
		Work            []predecessorWorkReport `json:"work"`
	}
	runWider := func() replayReport {
		t.Helper()
		code, raw, diag := runPredecessorImportRequest(t, dbPath, wider)
		if code != 0 {
			t.Fatalf("wider import exit=%d, want 0; stderr=%q", code, diag)
		}
		var report replayReport
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatalf("wider report is not parseable JSON: %v; raw=%s", err, raw)
		}
		return report
	}

	report := runWider()
	// 2 (Product and primary project bootstrap) + 1 (secondary project)
	// + 1 (alpha-1 from the narrow run).
	if report.NoOp || report.ProductsCreated != 0 || report.ProjectsCreated != 0 || report.WorkImported != 1 || report.AlreadyImported != 4 {
		t.Fatalf("wider run no_op=%v counts products:%d projects:%d work:%d already:%d, want false 0/0/1/4",
			report.NoOp, report.ProductsCreated, report.ProjectsCreated, report.WorkImported, report.AlreadyImported)
	}
	wantStatus := map[string]string{"synth-change-alpha-1": "already_imported", "synth-change-alpha-2": "imported"}
	if len(report.Work) != len(wantStatus) {
		t.Fatalf("wider run work list = %+v, want %d entries", report.Work, len(wantStatus))
	}
	for _, item := range report.Work {
		if item.Status != wantStatus[item.ChangeID] {
			t.Fatalf("wider run status for %s = %q, want %q", item.ChangeID, item.Status, wantStatus[item.ChangeID])
		}
		if item.WorkID != "import-advance-work-"+item.ChangeID || item.ExternalRef != "advance:"+item.ChangeID {
			t.Fatalf("wider run identity for %s = %s/%s, want the identity derived from the change id", item.ChangeID, item.WorkID, item.ExternalRef)
		}
	}
	if got := workCount(t, dbPath); got != 2 {
		t.Fatalf("work items after wider run = %d, want 2", got)
	}

	replay := runWider()
	if !replay.NoOp || replay.WorkImported != 0 || replay.AlreadyImported != 5 {
		t.Fatalf("unchanged replay no_op=%v work:%d already:%d, want true/0/5", replay.NoOp, replay.WorkImported, replay.AlreadyImported)
	}
}

// TestPredecessorModeCommandsLeavePredecessorStateUntouched proves the
// CD-0097 D1 rule that no mode command writes, freezes, disables, or deletes
// predecessor state. The snapshot's locators name synthetic predecessor
// Project directories, and every mode command runs against that snapshot: the
// inventory, a dry-run import, the import, and its replay. Afterwards the
// predecessor tree, snapshot included, keeps every entry, mode, size,
// modification time, and content byte.
func TestPredecessorModeCommandsLeavePredecessorStateUntouched(t *testing.T) {
	root := t.TempDir()
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(predecessorSyntheticFixture), &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot["projects"].([]any) {
		project := entry.(map[string]any)
		dir := filepath.Join(root, project["project_id"].(string))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"writable":true}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		project["locator"] = dir
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(root, "snapshot.json")
	if err := os.WriteFile(snapshotPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	before := predecessorTreeState(t, root)

	dbPath := freshMigratedCLIDatabase(t)
	inventoryRequest, err := json.Marshal(map[string]any{"snapshot_path": snapshotPath})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"predecessor-inventory"}, bytes.NewReader(inventoryRequest), &out, &errOut); code != 0 {
		t.Fatalf("predecessor-inventory exit=%d stderr=%q", code, errOut.String())
	}
	dryRun := predecessorImportRequest(t, snapshotPath)
	dryRun["dry_run"] = true
	for _, payload := range []map[string]any{dryRun, predecessorImportRequest(t, snapshotPath), predecessorImportRequest(t, snapshotPath)} {
		if code, _, diag := runPredecessorImportRequest(t, dbPath, payload); code != 0 {
			t.Fatalf("predecessor-import dry_run=%v exit=%d, want 0; stderr=%q", payload["dry_run"], code, diag)
		}
	}

	after := predecessorTreeState(t, root)
	for path, was := range before {
		if now, present := after[path]; !present || now != was {
			t.Errorf("mode commands changed predecessor entry %q: before %+v, after %+v (present=%v)", path, was, now, present)
		}
	}
	for path := range after {
		if _, existed := before[path]; !existed {
			t.Errorf("mode commands created predecessor entry %q", path)
		}
	}
}

// predecessorEntryState is the observable state of one predecessor tree entry.
// Access time is excluded because a read may update it.
type predecessorEntryState struct {
	Mode    fs.FileMode
	Size    int64
	ModTime int64
	Sum     string
}

func predecessorTreeState(t *testing.T, root string) map[string]predecessorEntryState {
	t.Helper()
	state := map[string]predecessorEntryState{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		observed := predecessorEntryState{Mode: info.Mode(), ModTime: info.ModTime().UnixNano()}
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(content)
			observed.Size = info.Size()
			observed.Sum = hex.EncodeToString(sum[:])
		}
		state[relative] = observed
		return nil
	})
	if err != nil {
		t.Fatalf("read predecessor tree: %v", err)
	}
	return state
}
