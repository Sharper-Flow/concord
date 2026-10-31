package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/agentcontract"
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

// writeCoreDescriptor reports the side-effect-free capability descriptor the
// installer reads while staging a release tree: the release
// identity, the schema this core defines, the maintenance-fence protocol it
// speaks, and the pinned adapter manifest digest. The route prints and exits;
// it touches no store, no lease directory, and no fence, so probing it can
// never change readiness or admission state. Capability truth lives here, in
// the core itself — never in an installer-origin assumption about the bytes
// it staged.
func writeCoreDescriptor(out, errOut io.Writer) int {
	root, binary, err := selfRelease()
	if err != nil {
		writeOperatorDiagnostic(errOut, "--version", diagnosticText(err))
		return 1
	}
	return writeJSON(out, map[string]any{
		"version":         version.Value,
		"schema_version":  store.CurrentSchemaVersion(),
		"fence_protocol":  hostlease.CurrentFenceProtocol,
		"manifest_digest": agentcontract.ManifestDigest,
		"release_root":    root,
		"core_binary":     binary,
	}, errOut)
}

// runHostLeaseCommand records this host session's release claim.
func runHostLeaseCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	var request struct {
		PID       int    `json:"pid"`
		Directory string `json:"directory"`
		Worktree  string `json:"worktree"`
	}
	if code := decodeReleaseInput(args, in, errOut, "host-lease", &request); code != 0 {
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
		writeOperatorDiagnostic(errOut, "host-lease", diagnosticText(err))
		return 1
	}
	root, binary, err := selfRelease()
	if err != nil {
		writeOperatorDiagnostic(errOut, "host-lease", diagnosticText(err))
		return 1
	}
	dataRoot, err := leaseDataRoot()
	if err != nil {
		writeOperatorDiagnostic(errOut, "host-lease", diagnosticText(err))
		return 1
	}
	lease := hostlease.Lease{
		PID:            request.PID,
		PidStart:       start,
		ReleaseRoot:    root,
		CoreBinary:     binary,
		SchemaVersion:  store.CurrentSchemaVersion(),
		ManifestDigest: agentcontract.ManifestDigest,
		RecordedAt:     nowUTC(),
		FenceProtocol:  hostlease.CurrentFenceProtocol,
		Directory:      request.Directory,
		Worktree:       request.Worktree,
	}
	if err := hostlease.Write(dataRoot, lease); err != nil {
		writeOperatorDiagnostic(errOut, "host-lease", diagnosticText(err))
		return 1
	}
	return writeJSON(out, lease, errOut)
}

func runHostLeasesCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	var ignored struct{}
	if code := decodeReleaseInput(args, in, errOut, "host-leases", &ignored); code != 0 {
		return code
	}
	dataRoot, err := leaseDataRoot()
	if err != nil {
		writeOperatorDiagnostic(errOut, "host-leases", diagnosticText(err))
		return 1
	}
	live, err := hostlease.List(dataRoot)
	if err != nil {
		writeOperatorDiagnostic(errOut, "host-leases", diagnosticText(err))
		return 1
	}
	if live == nil {
		live = []hostlease.Lease{}
	}
	return writeJSON(out, map[string]any{"leases": live}, errOut)
}

// fenceProtocolMarker is the file a release tree carries when its core
// declares the maintenance-fence protocol: it reads the fence before
// admitting a session, under the shared admission lock. The installer
// writes the marker into every tree it stages; a tree without one is a
// legacy or foreign core the boundary cannot exclude.
const fenceProtocolMarker = "fence-protocol"

// unfenceableReleaseRoots names the installed release trees under dataRoot,
// other than selfRoot, that hold a core binary but no fence-protocol marker
// (or one this protocol does not accept). Only a tree with bin/concord can
// admit a session, so directories without one are inert and not reported.
func unfenceableReleaseRoots(dataRoot, selfRoot string) ([]string, error) {
	entries, err := os.ReadDir(dataRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var unfenceable []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(dataRoot, entry.Name())
		if root == selfRoot {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "bin", "concord")); err != nil {
			continue // not a runnable release tree
		}
		marker, err := os.ReadFile(filepath.Join(root, fenceProtocolMarker)) //nolint:gosec // root is a release tree listed directly under the operator's data root.
		if err == nil {
			if protocol, convErr := strconv.Atoi(strings.TrimSpace(string(marker))); convErr == nil && protocol == hostlease.CurrentFenceProtocol {
				continue
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("cannot read %s: %w", filepath.Join(root, fenceProtocolMarker), err)
		}
		unfenceable = append(unfenceable, root)
	}
	return unfenceable, nil
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
		older = append(older, leaseHolder(lease))
	}
	if len(older) == 0 {
		return err
	}
	return fmt.Errorf("%w\nlive session(s) holding an older schema: %s", err, strings.Join(older, "; "))
}

// leaseHolder names one live session the way the operator finds its terminal.
func leaseHolder(lease hostlease.Lease) string {
	holder := fmt.Sprintf("pid %d holds %s at schema version %d", lease.PID, lease.ReleaseRoot, lease.SchemaVersion)
	if lease.Directory != "" {
		holder += ", directory " + lease.Directory
	}
	return holder
}

// upgradeInput is the JSON-stdin surface of the upgrade verb. plan asks for
// the read-only readiness report instead of applying anything.
// confirm_sessions_stopped is the operator's statement that no session runs on
// an installed release tree the maintenance fence cannot exclude; without it
// such a tree refuses the boundary-owned mutation. cancel_cutover_pinned_work
// selects ordinary lifecycle cancellation of every authoring-cutover blocker.
type upgradeInput struct {
	Plan                    bool `json:"plan"`
	ConfirmSessionsStopped  bool `json:"confirm_sessions_stopped"`
	CancelCutoverPinnedWork bool `json:"cancel_cutover_pinned_work"`
	RequireNoSessions       bool `json:"require_no_sessions"`
}

// preparedReleaseRecord carries the one field the CLI echoes from the
// installer-owned prepared-release record: the exact activation command the
// installer recorded when it prepared the candidate. The installer owns the
// record and its validation; the core only repeats what it says.
type preparedReleaseRecord struct {
	Version           string `json:"version"`
	ActivationCommand string `json:"activation_command"`
}

// preparedReleasePath names the installer's durable prepared-candidate
// record beside the store. It exists only while a prepared release waits
// for the operator's migration and activation commands.
func preparedReleasePath(dataRoot string) string {
	return filepath.Join(dataRoot, "prepared-release.json")
}

// activationCommandFor names the exact activation command when a prepared
// record carries one, and otherwise names the route that produces it: only
// the installer knows its own invocation, so the core never guesses one.
func activationCommandFor(dataRoot string) string {
	raw, err := os.ReadFile(preparedReleasePath(dataRoot))
	if err == nil {
		var record preparedReleaseRecord
		if json.Unmarshal(raw, &record) == nil && record.ActivationCommand != "" {
			return record.ActivationCommand
		}
	}
	return "run the installer; it records the exact activation command in " + preparedReleasePath(dataRoot)
}

// upgradePlanReport is the CLI's readiness plan: the store's pure facts with
// the exact commands and the maintenance-fence status the operator needs
// beside them. The store fills descriptive command defaults; this layer
// replaces them with the invocations an operator can run verbatim.
type upgradePlanReport struct {
	store.UpgradeReadiness
	MaintenanceFence *hostlease.Fence `json:"maintenance_fence"`
	SnapshotRequired bool             `json:"snapshot_required"`
}

func runUpgradeCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	var request upgradeInput
	if code := decodeReleaseInput(args, in, errOut, "upgrade", &request); code != 0 {
		return code
	}
	path, err := databasePath()
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
		return 1
	}
	dataRoot, err := leaseDataRoot()
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
		return 1
	}
	// A migrating run holds the maintenance lock from before it reads the
	// store until it exits. Its boundary-closing decision below rests on
	// what this run committed, so no other run may migrate under the same
	// boundary meanwhile. The read-only plan takes no lock.
	if !request.Plan {
		release, err := hostlease.AcquireMaintenance(dataRoot)
		if err != nil {
			writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
			return 1
		}
		defer release()
	}
	// The fence state participates in every answer and fails closed: an
	// unreadable exclusion record may be an open boundary.
	fenceBefore, err := hostlease.ReadFence(dataRoot)
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
		return 1
	}
	// Readiness is established by reading before anything is applied or
	// fenced: an unknown store never reaches the migration path.
	options := store.UpgradeOptions{CancelCutoverPinnedWork: request.CancelCutoverPinnedWork}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path, options)
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
		return 1
	}
	maintenanceRequired := len(plan.PendingBreaking) > 0 || request.CancelCutoverPinnedWork
	if request.Plan {
		_, binary, err := selfRelease()
		if err != nil {
			writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
			return 1
		}
		plan.MigrationCommand = shellWord(binary) + " upgrade"
		plan.ActivationCommand = activationCommandFor(dataRoot)
		return writeJSON(out, upgradePlanReport{UpgradeReadiness: plan, MaintenanceFence: fenceBefore, SnapshotRequired: maintenanceRequired}, errOut)
	}
	if plan.CutoverDisposition != nil && len(plan.CutoverDisposition.Blockers) > 0 {
		writeOperatorDiagnostic(errOut, "upgrade", strings.Join(plan.CutoverDisposition.Blockers, "; "))
		return 1
	}
	if request.CancelCutoverPinnedWork && plan.ActivationBlocked && len(plan.PendingBreaking) == 0 {
		// With the disposition clear and nothing breaking pending, the plan's
		// only blocker is the unstamped-build rule (CD-0139). It carries no
		// refusal, so the recovery line is added here.
		writeOperatorDiagnostic(errOut, "upgrade", strings.Join(plan.Blockers, "; ")+"\n"+recoveryLine(store.UnstampedRecovery))
		return 1
	}
	// A breaking migration or cutover cancellation requires the maintenance
	// boundary: the fence excludes new session admission before the final
	// lease check, and the activation that follows removes it. The command
	// itself is the boundary, so no flag can skip the exclusion.
	// The boundary is claimed with a pre-minted identity, so this run can
	// close only a fence it itself opened. A fence that already existed —
	// read at entry, or opened by a concurrent operation before EnsureFence
	// took the admission lock — is adopted with its own identity and is
	// never this run's to remove; removal is identity-checked so a no-op or
	// failed run cannot delete another operation's exclusion.
	boundary := upgradeFence{dataRoot: dataRoot}
	if maintenanceRequired || request.RequireNoSessions {
		var code int
		boundary, code = openUpgradeFence(dataRoot, errOut)
		if code != 0 {
			return code
		}
	}
	held, code := boundary.observeSessions(maintenanceRequired || request.RequireNoSessions, request.ConfirmSessionsStopped, errOut)
	if code != 0 {
		return code
	}
	if request.RequireNoSessions && len(held) != 0 {
		var holders []string
		for _, lease := range held {
			holders = append(holders, fmt.Sprintf("pid %d holds %s at schema version %d", lease.PID, lease.ReleaseRoot, lease.SchemaVersion))
		}
		boundary.close(errOut)
		writeOperatorDiagnostic(errOut, "upgrade", "prepared cutover requires no live sessions: "+strings.Join(holders, "; "))
		return 1
	}
	// Every boundary-owned mutation requires a verified snapshot. A failed
	// session check or snapshot applies no migration or cancellation.
	snapshotPath := ""
	if maintenanceRequired {
		if err := store.CheckUpgradeSessions(plan.PendingBreaking, held); err != nil {
			boundary.close(errOut)
			writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
			return 1
		}
		var present bool
		snapshotPath, present, err = snapshotBeforeMutation(path, dataRoot, "upgrade", version.Value)
		if err == nil && !present {
			err = fmt.Errorf("source database %s disappeared before the snapshot", path)
		}
		if err != nil {
			boundary.close(errOut)
			writeOperatorDiagnostic(errOut, "upgrade", fmt.Sprintf("snapshot %s failed; no migration was applied: %s", snapshotPath, diagnosticText(err)))
			return 1
		}
	}
	report, err := store.Upgrade(context.Background(), path, held, options)
	if err != nil {
		return boundary.reportFailure(err, path, plan.PendingBreaking, options, snapshotPath, errOut)
	}
	return boundary.finish(report, plan.PendingBreaking, fenceBefore, snapshotPath, out, errOut)
}

// upgradeFence owns only the boundary this invocation opened. An adopted
// boundary can authorize migration, but this invocation never removes it.
type upgradeFence struct {
	dataRoot string
	id       string
	owned    bool
}

func openUpgradeFence(dataRoot string, errOut io.Writer) (upgradeFence, int) {
	boundary := upgradeFence{dataRoot: dataRoot}
	root, binary, err := selfRelease()
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
		return boundary, 1
	}
	candidate, err := hostlease.NewFenceID()
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
		return boundary, 1
	}
	ensured, err := hostlease.EnsureFence(dataRoot, hostlease.Fence{
		FenceID:       candidate,
		Operation:     hostlease.FenceOperationUpgrade,
		ReleaseRoot:   root,
		CoreBinary:    binary,
		SchemaVersion: store.CurrentSchemaVersion(),
		Notice:        "session admission reopens when the prepared release activates; activation command: " + activationCommandFor(dataRoot),
	})
	if err != nil {
		writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
		return boundary, 1
	}
	boundary.owned = ensured.FenceID == candidate
	boundary.id = ensured.FenceID
	if !boundary.owned {
		// An adopted boundary authorizes this migration only when it
		// is provably this same command's own opening: the upgrade
		// operation, this release root, this binary, this schema —
		// the legitimate failed-migration and committed-migration
		// recovery tie. Anything else — no operation, a foreign
		// operation, another release's identity, or an uncertain
		// hand-written record — stays untouched on disk and moves no
		// migration.
		if !ensured.AuthorizesNativeMigration(root, binary, store.CurrentSchemaVersion()) {
			writeOperatorDiagnostic(errOut, "upgrade",
				fmt.Sprintf("an open maintenance boundary at %s is not this binary's own upgrade boundary (operation %q, release root %q, core binary %q, schema %d); an unattributed or foreign boundary authorizes no migration and stays unchanged. Close it through the operator-owned offline bootstrap when no migration is in progress",
					hostlease.FencePath(dataRoot), ensured.Operation, ensured.ReleaseRoot, ensured.CoreBinary, ensured.SchemaVersion))
			return boundary, 1
		}
	}
	return boundary, 0
}

func (boundary upgradeFence) close(errOut io.Writer) {
	if !boundary.owned {
		return
	}
	if removeErr := hostlease.RemoveFenceOwned(boundary.dataRoot, boundary.id); removeErr != nil {
		writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(removeErr))
	}
}

func (boundary upgradeFence) observeSessions(maintenanceRequired, confirmSessionsStopped bool, errOut io.Writer) ([]store.HeldSchema, int) {
	dataRoot := boundary.dataRoot
	// The lease list is read after the fence is in place, so the check
	// store.Upgrade performs is the final one: no session can be admitted
	// between it and the migration while the boundary is open.
	live, err := hostlease.List(dataRoot)
	if err != nil {
		boundary.close(errOut)
		writeOperatorDiagnostic(errOut, "upgrade", "cannot observe live host sessions: "+diagnosticText(err))
		return nil, 1
	}
	// A legacy lease names a core that admits sessions without reading the
	// fence, so the boundary cannot exclude it: an unfenceable participant.
	// The boundary fails closed on one, because a session it admitted after
	// the final check would strand on the migrated store.
	if maintenanceRequired {
		var legacy []string
		for _, lease := range live {
			if lease.FenceProtocol == hostlease.CurrentFenceProtocol {
				continue
			}
			holder := fmt.Sprintf("pid %d holds %s at schema version %d with fence protocol %d",
				lease.PID, lease.ReleaseRoot, lease.SchemaVersion, lease.FenceProtocol)
			if lease.Directory != "" {
				holder += ", directory " + lease.Directory
			}
			legacy = append(legacy, holder)
		}
		if len(legacy) > 0 {
			boundary.close(errOut)
			writeOperatorDiagnostic(errOut, "upgrade",
				"an incompatible migration requires every live session's release to honor the maintenance fence; unfenceable legacy participant(s) hold the boundary open: "+
					strings.Join(legacy, "; ")+
					"; end or move those sessions to this release, then re-run concord upgrade")
			return nil, 1
		}
	}
	// An installed release tree that carries no fence-protocol marker names a
	// core that admits sessions without reading the fence. No live lease is
	// needed for it to matter: a pinned adapter can start that core after the
	// final lease check, and its session would strand on the migrated store.
	// The fence cannot exclude it, so the operator decides: the command names
	// every such tree and proceeds only when the operator confirms no session
	// runs on one.
	if maintenanceRequired {
		selfRoot, _, err := selfRelease()
		if err != nil {
			boundary.close(errOut)
			writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
			return nil, 1
		}
		unfenceable, err := unfenceableReleaseRoots(dataRoot, selfRoot)
		if err != nil {
			boundary.close(errOut)
			writeOperatorDiagnostic(errOut, "upgrade", "cannot enumerate installed release trees: "+diagnosticText(err))
			return nil, 1
		}
		if len(unfenceable) > 0 && !confirmSessionsStopped {
			boundary.close(errOut)
			writeOperatorDiagnostic(errOut, "upgrade",
				"release tree(s) without a fence-protocol marker can start a session the maintenance fence cannot exclude: "+
					strings.Join(unfenceable, "; ")+
					`; make sure no session runs on them, then re-run with {"confirm_sessions_stopped":true}`)
			return nil, 1
		}
		if len(unfenceable) > 0 {
			writeOperatorDiagnostic(errOut, "upgrade",
				"proceeding on the operator's confirmation that no session runs on release tree(s) without a fence-protocol marker: "+
					strings.Join(unfenceable, "; "))
		}
	}
	held := make([]store.HeldSchema, 0, len(live))
	for _, lease := range live {
		held = append(held, store.HeldSchema{PID: lease.PID, ReleaseRoot: lease.ReleaseRoot, SchemaVersion: lease.SchemaVersion, Directory: lease.Directory, Worktree: lease.Worktree})
	}
	return held, 0
}

func (boundary upgradeFence) reportFailure(err error, path string, pendingBreaking []store.PendingMigration, options store.UpgradeOptions, snapshotPath string, errOut io.Writer) int {
	dataRoot := boundary.dataRoot
	diagnostic := diagnosticText(err)
	if snapshotPath != "" {
		diagnostic += "\nverified database snapshot: " + snapshotPath
	}
	// The error may sit after the commit: Upgrade applies every
	// migration and then finishes the open, and a post-commit
	// finishOpen refusal does not uncommit a breaking step. Re-read
	// readiness and keep the boundary open when any pending breaking
	// step committed: the older releases are unusable from here, and
	// only the prepared candidate's activation may reopen admission.
	if breakingCommittedAfterFailure(path, pendingBreaking, options) {
		if fence, fenceErr := hostlease.ReadFence(dataRoot); fenceErr == nil && fence != nil {
			writeOperatorDiagnostic(errOut, "upgrade", diagnostic+
				"\nthe incompatible migration committed before this failure; session admission stays excluded until the prepared release activates; activation command: "+
				activationCommandFor(dataRoot))
			return 1
		}
		writeOperatorDiagnostic(errOut, "upgrade", diagnostic+
			"\nthe incompatible migration committed before this failure, but the admission fence is not observable; treat admission as excluded and re-run the activation")
		return 1
	}
	boundary.close(errOut)
	writeOperatorDiagnostic(errOut, "upgrade", diagnostic)
	return 1
}

func (boundary upgradeFence) finish(report store.UpgradeReport, pendingBreaking []store.PendingMigration, fenceBefore *hostlease.Fence, snapshotPath string, out, errOut io.Writer) int {
	dataRoot := boundary.dataRoot
	breakingCommitted := false
	for _, version := range report.Applied {
		for _, pending := range pendingBreaking {
			if version == pending.Version {
				breakingCommitted = true
			}
		}
	}
	fence := fenceBefore
	if breakingCommitted {
		// The committed breaking step made older releases unusable: the
		// boundary stays open until activation completes, and the report
		// names the exact command that completes it.
		current, err := hostlease.ReadFence(dataRoot)
		if err != nil {
			writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(err))
			return 1
		}
		fence = current
	} else if boundary.owned {
		// No breaking step committed under this run's boundary, so the
		// boundary this run opened closes — by identity, so a boundary a
		// concurrent operation opened in between survives untouched.
		if removeErr := hostlease.RemoveFenceOwned(dataRoot, boundary.id); removeErr != nil {
			writeOperatorDiagnostic(errOut, "upgrade", diagnosticText(removeErr))
			return 1
		}
	}
	result := map[string]any{
		"from_version":   version.Value,
		"schema_version": report.SchemaVersion,
		"applied":        report.Applied,
	}
	if report.CutoverDisposition != nil {
		result["cutover_disposition"] = report.CutoverDisposition
	}
	if snapshotPath != "" {
		result["backup"] = snapshotPath
		// Cleanup failure is reported but never fails the finished upgrade
		// or drops the maintenance report below.
		retention, err := pruneManagedSnapshots(dataRoot, snapshotPath, time.Now().UTC())
		if err != nil {
			retention.Error = diagnosticText(err)
			writeOperatorDiagnostic(errOut, "upgrade", "upgrade finished; snapshot retention failed: "+retention.Error)
		}
		result["snapshot_retention"] = retention
	}
	if fence != nil {
		result["maintenance"] = map[string]any{
			"fence_id":           fence.FenceID,
			"notice":             fence.Notice,
			"activation_command": activationCommandFor(dataRoot),
		}
	}
	return writeJSON(out, result, errOut)
}

// breakingCommittedAfterFailure re-reads the store read-only and reports
// whether any migration the plan listed as pending-breaking is no longer
// pending. An unreadable re-check is an uncertain outcome and conservatively
// reports committed: after an error, exclusion is retained on both committed
// and uncertain outcomes, and only a re-check that still shows every breaking
// step pending proves nothing committed.
func breakingCommittedAfterFailure(path string, pendingBreaking []store.PendingMigration, options store.UpgradeOptions) bool {
	if len(pendingBreaking) == 0 {
		return false
	}
	recheck, err := store.PlanUpgradeReadiness(context.Background(), path, options)
	if err != nil {
		return true
	}
	stillPending := make(map[int]bool, len(recheck.PendingBreaking))
	for _, m := range recheck.PendingBreaking {
		stillPending[m.Version] = true
	}
	for _, m := range pendingBreaking {
		if !stillPending[m.Version] {
			return true
		}
	}
	return false
}

// shellWord quotes one word for a POSIX shell when it needs quoting, so a
// recorded command with a space in its path survives splitting into the
// arguments an operator's shell will run.
func shellWord(word string) string {
	if word == "" {
		return "''"
	}
	needsQuoting := false
	for _, r := range word {
		if r == '\'' {
			return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
		}
		if strings.ContainsRune(" \t\n\"\\$&<>()|;*?[]#~=`", r) {
			needsQuoting = true
		}
	}
	if needsQuoting {
		return "'" + word + "'"
	}
	return word
}

// decodeReleaseInput applies the shared argument and stdin discipline to the
// release verbs. Each reads at most one JSON object, and host-leases and
// upgrade accept an empty one.
func decodeReleaseInput(args []string, in io.Reader, errOut io.Writer, command string, target any) int {
	if len(args) != 0 {
		writeDiagnostic(errOut, fmt.Sprintf("concord %s: unsupported arguments: %s", command, args[0]))
		writeCommandUsageSection(errOut, command)
		return 2
	}
	if command == "upgrade" {
		if file, ok := in.(*os.File); ok {
			var terminal syscall.Termios
			_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&terminal)))
			if errno == 0 {
				writeOperatorDiagnostic(errOut, command, `upgrade requires JSON on stdin; pipe '{}' for defaults or '{"plan":true}' for a read-only plan`)
				return 1
			}
		}
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
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	return 0
}

// nowUTC is the lease timestamp shape: RFC 3339, UTC.
func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
