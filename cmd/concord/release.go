package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

// leaseDataRoot returns the root the lease directory hangs from. Leases and
// releases share the data root, so the database location decides it: the
// override when one is set, the platform data home otherwise.
func leaseDataRoot() (string, error) {
	path, err := databasePath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

// selfRelease describes the core binary taking or reporting a lease. The
// executable path resolves through /proc/self/exe, so a symlinked launcher
// still reports the release root it points into.
func selfRelease() (root string, binary string, err error) {
	binary, err = os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("cannot locate the running core binary: %w", err)
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve the running core binary: %w", err)
	}
	return filepath.Dir(filepath.Dir(binary)), binary, nil
}

func runHostLeaseCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	var request struct {
		PID int `json:"pid"`
	}
	if code := decodeReleaseInput(args, in, out, errOut, "host-lease", &request); code != 0 {
		return code
	}
	if request.PID <= 0 {
		writeOperatorDiagnostic(errOut, "host-lease", "pid must be a positive integer")
		return 1
	}
	start, err := hostlease.ProcessStart(request.PID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "host-lease", err.Error())
		return 1
	}
	root, binary, err := selfRelease()
	if err != nil {
		writeOperatorDiagnostic(errOut, "host-lease", err.Error())
		return 1
	}
	dataRoot, err := leaseDataRoot()
	if err != nil {
		writeOperatorDiagnostic(errOut, "host-lease", err.Error())
		return 1
	}
	lease := hostlease.Lease{
		PID:            request.PID,
		PidStart:       start,
		ReleaseRoot:    root,
		CoreBinary:     binary,
		SchemaVersion:  store.CurrentSchemaVersion(),
		ManifestDigest: agent.ManifestDigest,
		RecordedAt:     nowUTC(),
	}
	if err := hostlease.Write(dataRoot, lease); err != nil {
		writeOperatorDiagnostic(errOut, "host-lease", err.Error())
		return 1
	}
	return writeJSON(out, lease, errOut)
}

func runHostLeasesCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	var ignored struct{}
	if code := decodeReleaseInput(args, in, out, errOut, "host-leases", &ignored); code != 0 {
		return code
	}
	dataRoot, err := leaseDataRoot()
	if err != nil {
		writeOperatorDiagnostic(errOut, "host-leases", err.Error())
		return 1
	}
	live, err := hostlease.List(dataRoot)
	if err != nil {
		writeOperatorDiagnostic(errOut, "host-leases", err.Error())
		return 1
	}
	if live == nil {
		live = []hostlease.Lease{}
	}
	return writeJSON(out, map[string]any{"leases": live}, errOut)
}

func runUpgradeCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	var ignored struct{}
	if code := decodeReleaseInput(args, in, out, errOut, "upgrade", &ignored); code != 0 {
		return code
	}
	path, err := databasePath()
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", err.Error())
		return 1
	}
	dataRoot, err := leaseDataRoot()
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", err.Error())
		return 1
	}
	live, err := hostlease.List(dataRoot)
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", "cannot observe live host sessions: "+err.Error())
		return 1
	}
	held := make([]store.HeldSchema, 0, len(live))
	for _, lease := range live {
		held = append(held, store.HeldSchema{PID: lease.PID, ReleaseRoot: lease.ReleaseRoot, SchemaVersion: lease.SchemaVersion})
	}
	report, err := store.Upgrade(context.Background(), path, held)
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", err.Error())
		return 1
	}
	return writeJSON(out, map[string]any{
		"from_version":   version.Value,
		"schema_version": report.SchemaVersion,
		"applied":        report.Applied,
	}, errOut)
}

// decodeReleaseInput applies the shared argument and stdin discipline to the
// release verbs. Each reads at most one JSON object, and host-leases and
// upgrade accept an empty one.
func decodeReleaseInput(args []string, in io.Reader, out, errOut io.Writer, command string, target any) int {
	if len(args) != 0 {
		writeDiagnostic(errOut, fmt.Sprintf("concord %s: unsupported arguments: %s", command, args[0]))
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(in, agent.MaxEnvelopeBytes+1))
	if err != nil || int64(len(raw)) > agent.MaxEnvelopeBytes {
		writeOperatorDiagnostic(errOut, command, "cannot read the request")
		return 1
	}
	if len(raw) == 0 {
		return 0
	}
	if err := decodeObject(raw, target); err != nil {
		writeOperatorDiagnostic(errOut, command, err.Error())
		return 1
	}
	return 0
}

// nowUTC is the lease timestamp shape: RFC 3339, UTC.
func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
