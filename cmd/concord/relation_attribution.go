package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

type attributionInput struct {
	Action         string                    `json:"action"`
	Cutoff         string                    `json:"cutoff,omitempty"`
	BackupPath     string                    `json:"backup_path,omitempty"`
	Manifest       store.AttributionManifest `json:"manifest,omitempty"`
	IdempotencyKey string                    `json:"idempotency_key,omitempty"`
	ClearKey       string                    `json:"clear_key,omitempty"`
}

var clearAttribution = (*store.Store).ClearRelationAttribution
var undoAttribution = (*store.Store).UndoRelationAttribution

func (r attributionInput) validate() error {
	if r.Action == "inspect" {
		if _, err := time.Parse(time.RFC3339Nano, r.Cutoff); err != nil {
			return fmt.Errorf("inspect requires an RFC3339 cutoff")
		}
		if r.IdempotencyKey != "" || r.ClearKey != "" || r.Manifest.Version != 0 || r.Manifest.Cutoff != "" || r.Manifest.BackupChecksum != "" || r.Manifest.GraphDigest != "" || r.Manifest.ReceiptDigest != "" || r.Manifest.Candidates != nil {
			return fmt.Errorf("inspect accepts only cutoff and optional backup_path")
		}
		return nil
	}
	if r.Action != "clear" && r.Action != "verify" && r.Action != "undo" {
		return fmt.Errorf("action must be inspect, clear, verify or undo")
	}
	if r.Cutoff != "" || strings.TrimSpace(r.BackupPath) == "" || !filepath.IsAbs(r.BackupPath) || filepath.Clean(r.BackupPath) != r.BackupPath || strings.TrimSpace(r.IdempotencyKey) == "" {
		return fmt.Errorf("backup_path and idempotency_key are required; cutoff belongs in manifest")
	}
	m := r.Manifest
	if m.Version != 1 || m.BackupChecksum == "" || m.GraphDigest == "" || m.ReceiptDigest == "" || len(m.Candidates) == 0 {
		return fmt.Errorf("a nonempty expected manifest is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.Cutoff); err != nil {
		return fmt.Errorf("manifest cutoff must be RFC3339")
	}
	for _, c := range m.Candidates {
		if c.ID <= 0 || strings.TrimSpace(c.Fingerprint) == "" {
			return fmt.Errorf("manifest candidates require positive IDs and fingerprints")
		}
	}
	if (r.Action == "undo" && (strings.TrimSpace(r.ClearKey) == "" || r.ClearKey == r.IdempotencyKey)) || (r.Action != "undo" && r.ClearKey != "") {
		return fmt.Errorf("undo requires a distinct clear_key; other actions reject it")
	}
	return nil
}

func attributionSnapshot(path, root string) (string, string, error) {
	snapshot, present, err := snapshotBeforeMutation(path, root, "repair", version.Value)
	if err == nil && !present {
		err = fmt.Errorf("source store disappeared")
	}
	if err != nil {
		return snapshot, "", err
	}
	m, err := store.VerifyBackup(context.Background(), snapshot)
	if err != nil {
		return snapshot, "", err
	}
	hold, err := writeCompactHold(snapshot, m)
	return snapshot, hold, err
}

func checkAttributionHold(ctx context.Context, path, root string) error {
	if filepath.Dir(path) != filepath.Join(root, "backups") {
		return fmt.Errorf("treatment backup must be in the store backup directory")
	}
	m, err := store.VerifyBackup(ctx, path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path + compactHoldSuffix) //nolint:gosec // Admitted backup path and its adjacent operator hold.
	if err != nil {
		return err
	}
	var hold compactHold
	if err := json.Unmarshal(data, &hold); err != nil || hold != (compactHold{Snapshot: filepath.Base(path), SnapshotID: m.SnapshotID, Checksum: m.DBChecksum}) {
		return fmt.Errorf("backup hold does not match verified snapshot")
	}
	return nil
}

func runRelationAttribution(args []string, in io.Reader, out, errOut io.Writer) int {
	const command = "relation-attribution"
	var input attributionInput
	if code := decodeReleaseInput(args, in, errOut, command, &input); code != 0 {
		return code
	}
	if err := input.validate(); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return inputRefusalExit
	}
	ctx := context.Background()
	if input.Action == "inspect" && input.BackupPath != "" {
		m, err := store.InspectRelationAttribution(ctx, input.BackupPath, input.Cutoff)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeJSON(out, map[string]any{"snapshot": input.BackupPath, "manifest": m}, errOut)
	}
	path, err := databasePath()
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err == nil {
		err = store.AdmitStorePath(path)
	}
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	root := filepath.Dir(path)
	release, err := hostlease.AcquireMaintenance(root)
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	defer release()
	releaseRoot, binary, err := selfRelease()
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	id, err := hostlease.NewFenceID()
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	fence, err := hostlease.EnsureFence(root, hostlease.Fence{FenceID: id, Operation: hostlease.FenceOperationRelationAttribution, ReleaseRoot: releaseRoot, CoreBinary: binary, SchemaVersion: store.CurrentSchemaVersion(), Notice: "relation attribution maintenance excludes session admission"})
	if err != nil || fence.FenceID != id {
		if err == nil {
			err = fmt.Errorf("existing maintenance fence is retained; refusing to adopt it")
		}
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	boundary := upgradeFence{dataRoot: root, id: id, owned: true, strictCompact: true, commandName: command}
	held, code := boundary.observeSessions(true, false, true, errOut)
	if code != 0 {
		return code
	}
	if code := boundary.refuseLiveSessions(true, held, errOut); code != 0 {
		return code
	}
	return runFencedAttribution(ctx, input, path, root, boundary, out, errOut)
}

// attributionPaths names every snapshot and hold the operator may need to keep.
type attributionPaths struct{ snapshot, hold, safety, safetyHold string }

func (p attributionPaths) String() string {
	return fmt.Sprintf("snapshot: %s; hold: %s; safety snapshot: %s; safety hold: %s", p.snapshot, p.hold, p.safety, p.safetyHold)
}

// runFencedAttribution keeps the fence only when a treatment may have written.
// The maintenance handle always closes before session admission reopens.
func runFencedAttribution(ctx context.Context, input attributionInput, path, root string, boundary upgradeFence, out, errOut io.Writer) int {
	const command = "relation-attribution"
	s, err := store.OpenMaintenance(ctx, path)
	if err != nil {
		boundary.close(errOut)
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	report, paths, treating, err := attributionAction(ctx, s, input, path, root)
	var failure *store.Failure
	retain := treating && err != nil && (!errors.As(err, &failure) || failure.EffectPossible)
	if retain {
		_ = s.Close()
	} else if releaseErr := releaseAttributionFence(s, root, boundary.id); releaseErr != nil {
		retain = true
		if err == nil {
			err = fmt.Errorf("operation finished but %w", releaseErr)
		} else {
			err = errors.Join(err, releaseErr)
		}
	}
	if err != nil {
		writeOperatorDiagnostic(errOut, command, fmt.Sprintf("%v; %s; fence retained: %t; reconcile before retry if retained", err, paths, retain))
		return 1
	}
	return writeJSON(out, report, errOut)
}

func releaseAttributionFence(s *store.Store, root, id string) error {
	if err := s.Close(); err != nil {
		return fmt.Errorf("store close failed: %w", err)
	}
	if err := hostlease.RemoveFenceOwned(root, id); err != nil {
		return fmt.Errorf("fence cleanup failed: %w", err)
	}
	return nil
}

// attributionAction reports treating when a failed clear or undo may have
// reached its write transaction.
func attributionAction(ctx context.Context, s *store.Store, input attributionInput, path, root string) (map[string]any, attributionPaths, bool, error) {
	if input.Action == "inspect" {
		var p attributionPaths
		var manifest store.AttributionManifest
		var err error
		p.snapshot, p.hold, err = attributionSnapshot(path, root)
		if err == nil {
			manifest, err = store.InspectRelationAttribution(ctx, p.snapshot, input.Cutoff)
		}
		if err != nil {
			return nil, p, false, fmt.Errorf("inspection failed: %w", err)
		}
		return map[string]any{"snapshot": p.snapshot, "hold": p.hold, "manifest": manifest}, p, false, nil
	}
	p := attributionPaths{snapshot: input.BackupPath, hold: input.BackupPath + compactHoldSuffix}
	if err := attributionPreflight(ctx, s, input, path, root, &p); err != nil {
		return nil, p, false, fmt.Errorf("refused before treatment: %w", err)
	}
	req := store.AttributionRequest{BackupPath: input.BackupPath, Manifest: input.Manifest, IdempotencyKey: input.IdempotencyKey}
	var result store.AttributionResult
	var err error
	switch input.Action {
	case "clear":
		result, err = clearAttribution(s, ctx, req)
	case "verify":
		result, err = s.VerifyRelationAttribution(ctx, req)
	case "undo":
		result, err = undoAttribution(s, ctx, store.AttributionUndoRequest{AttributionRequest: req, ClearKey: input.ClearKey})
	}
	if err != nil {
		return nil, p, input.Action != "verify", err
	}
	return map[string]any{"snapshot": p.snapshot, "hold": p.hold, "safety_snapshot": p.safety, "safety_hold": p.safetyHold, "result": result}, p, false, nil
}

// attributionPreflight skips checks a recorded replay does not need. A new
// clear requires the held treatment backup; a new undo first takes and holds
// a safety snapshot of the current store.
func attributionPreflight(ctx context.Context, s *store.Store, input attributionInput, path, root string, p *attributionPaths) error {
	if input.Action == "verify" {
		return nil
	}
	recorded, err := s.RelationAttributionRecorded(ctx, input.Action, input.IdempotencyKey)
	switch {
	case err != nil || recorded:
		return err
	case input.Action == "clear":
		return checkAttributionHold(ctx, input.BackupPath, root)
	default:
		p.safety, p.safetyHold, err = attributionSnapshot(path, root)
		return err
	}
}
