package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
		PID       int    `json:"pid"`
		Directory string `json:"directory"`
		Worktree  string `json:"worktree"`
	}
	if code := decodeReleaseInput(args, in, out, errOut, "host-lease", &request); code != 0 {
		return code
	}
	if request.PID <= 0 {
		writeOperatorDiagnostic(errOut, "host-lease", "pid must be a positive integer")
		return 1
	}
	// The session location is advisory identity, not authority input: it is
	// bounded and never interpreted, only named back by a refusal.
	for name, value := range map[string]string{"directory": request.Directory, "worktree": request.Worktree} {
		if len(value) > 4096 {
			writeOperatorDiagnostic(errOut, "host-lease", name+" must not exceed 4096 characters")
			return 1
		}
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
		Directory:      request.Directory,
		Worktree:       request.Worktree,
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

// openStoreForCommand opens the store for a command route. When open stops
// before a pending breaking migration, the refusal is turned actionable: it
// names every live session holding an older schema, with its directory, so
// the operator ends the right terminals instead of correlating pids by hand.
func openStoreForCommand(ctx context.Context, path string) (*store.Store, error) {
	s, err := store.Open(ctx, path)
	if err == nil {
		return s, nil
	}
	dataRoot, rootErr := leaseDataRoot()
	if rootErr != nil {
		return nil, err
	}
	live, listErr := hostlease.List(dataRoot)
	if listErr != nil {
		return nil, err
	}
	return nil, actionableUpgradeRefusal(err, live, store.CurrentSchemaVersion())
}

// actionableUpgradeRefusal appends the older-holding sessions to an
// upgrade-required refusal. Every other error, and an upgrade-required
// refusal with no older holder observable, passes through unchanged.
func actionableUpgradeRefusal(err error, leases []hostlease.Lease, current int) error {
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.Kind != store.KindUpgradeRequired {
		return err
	}
	var older []string
	for _, lease := range leases {
		if lease.SchemaVersion >= current {
			continue
		}
		holder := fmt.Sprintf("pid %d holds %s at schema version %d", lease.PID, lease.ReleaseRoot, lease.SchemaVersion)
		if lease.Directory != "" {
			holder += ", directory " + lease.Directory
		}
		older = append(older, holder)
	}
	if len(older) == 0 {
		return err
	}
	return fmt.Errorf("%s\nlive session(s) holding an older schema: %s\nend or move those sessions to the installed release, then run concord upgrade",
		err.Error(), strings.Join(older, "; "))
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
		held = append(held, store.HeldSchema{PID: lease.PID, ReleaseRoot: lease.ReleaseRoot, SchemaVersion: lease.SchemaVersion, Directory: lease.Directory, Worktree: lease.Worktree})
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
