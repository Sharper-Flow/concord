package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

type compactHold struct {
	Snapshot   string `json:"snapshot"`
	SnapshotID string `json:"snapshot_id"`
	Checksum   string `json:"checksum"`
}

var compactDatabase = store.Compact

func writeCompactHold(snapshot string, manifest store.BackupManifest) (string, error) {
	path := snapshot + compactHoldSuffix
	for _, member := range []string{snapshot, snapshot + ".manifest.json"} {
		f, err := os.Open(member) //nolint:gosec // The snapshot is generated under the admitted data root.
		if err != nil {
			return path, err
		}
		err = f.Sync()
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return path, err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // The hold is adjacent to the generated snapshot and cannot replace a file.
	if err != nil {
		return path, err
	}
	hold := compactHold{Snapshot: filepath.Base(snapshot), SnapshotID: manifest.SnapshotID, Checksum: manifest.DBChecksum}
	err = json.NewEncoder(f).Encode(hold)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return path, err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return path, err
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return path, err
	}
	root, err := os.Open(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return path, err
	}
	err = root.Sync()
	if closeErr := root.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return path, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // Read back the hold this call just created.
	if err != nil {
		return path, err
	}
	var actual compactHold
	if err := json.Unmarshal(data, &actual); err != nil || actual != hold {
		return path, fmt.Errorf("compact hold verification failed: %v", err)
	}
	return path, nil
}

func runCompactCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	var input struct{}
	if code := decodeReleaseInput(args, in, errOut, "compact", &input); code != 0 {
		return code
	}
	path, err := databasePath()
	if err != nil {
		writeOperatorDiagnostic(errOut, "compact", diagnosticText(err))
		return 1
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		writeOperatorDiagnostic(errOut, "compact", "cannot resolve the existing store: "+diagnosticText(err))
		return 1
	}
	dataRoot := filepath.Dir(path)
	start := time.Now()
	release, err := hostlease.AcquireMaintenance(dataRoot)
	if err != nil {
		writeOperatorDiagnostic(errOut, "compact", diagnosticText(err))
		return 1
	}
	defer release()
	root, binary, err := selfRelease()
	if err != nil {
		writeOperatorDiagnostic(errOut, "compact", diagnosticText(err))
		return 1
	}
	id, err := hostlease.NewFenceID()
	if err != nil {
		writeOperatorDiagnostic(errOut, "compact", diagnosticText(err))
		return 1
	}
	fence, err := hostlease.EnsureFence(dataRoot, hostlease.Fence{FenceID: id, Operation: hostlease.FenceOperationCompact, ReleaseRoot: root, CoreBinary: binary, SchemaVersion: store.CurrentSchemaVersion(), Notice: "store compaction excludes session admission; retain the verified snapshot until operator release"})
	if err != nil {
		writeOperatorDiagnostic(errOut, "compact", diagnosticText(err))
		return 1
	}
	if fence.FenceID != id {
		writeOperatorDiagnostic(errOut, "compact", "an existing maintenance fence is retained; compact will not adopt it")
		return 1
	}
	boundary := upgradeFence{dataRoot: dataRoot, id: id, owned: true, strictCompact: true}
	held, code := boundary.observeSessions(true, false, true, errOut)
	if code != 0 {
		return code
	}
	if code := boundary.refuseLiveSessions(true, held, errOut); code != 0 {
		return code
	}
	snapshot, present, err := snapshotBeforeMutation(path, dataRoot, "repair", version.Value)
	if err == nil && !present {
		err = fmt.Errorf("source database %s disappeared before the snapshot", path)
	}
	if err != nil {
		boundary.close(errOut)
		writeOperatorDiagnostic(errOut, "compact", fmt.Sprintf("snapshot failed before write-back (%s): %v", snapshot, err))
		return 1
	}
	manifest, err := store.VerifyBackup(context.Background(), snapshot)
	if err != nil {
		boundary.close(errOut)
		writeOperatorDiagnostic(errOut, "compact", diagnosticText(err))
		return 1
	}
	hold, err := writeCompactHold(snapshot, manifest)
	if err != nil {
		boundary.close(errOut)
		writeOperatorDiagnostic(errOut, "compact", fmt.Sprintf("snapshot hold %s failed before write-back: %v", hold, err))
		return 1
	}
	report, err := compactDatabase(context.Background(), path, snapshot)
	if err != nil {
		if !report.WriteBackStarted {
			boundary.close(errOut)
		}
		writeOperatorDiagnostic(errOut, "compact", fmt.Sprintf("%v; verified snapshot: %s; hold: %s; maintenance fence retained: %t", err, snapshot, hold, report.WriteBackStarted))
		return 1
	}
	if err := hostlease.RemoveFenceOwned(dataRoot, id); err != nil {
		writeOperatorDiagnostic(errOut, "compact", fmt.Sprintf("compact finished but fence cleanup failed; snapshot: %s; hold: %s: %v", snapshot, hold, err))
		return 1
	}
	retention, retentionErr := pruneManagedSnapshots(dataRoot, snapshot, time.Now().UTC())
	if retentionErr != nil {
		retention.Error = diagnosticText(retentionErr)
	}
	return writeJSON(out, map[string]any{"snapshot": snapshot, "hold": hold, "compact": report, "maintenance_ms": time.Since(start).Milliseconds(), "snapshot_retention": retention}, errOut)
}
