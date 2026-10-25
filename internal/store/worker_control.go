package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
)

// WorkerControlRequest binds a worker-selected control to the host-held packet.
// The worker supplies ControlID only; the host supplies every other member.
type WorkerControlRequest struct {
	WorkID           string          `json:"work_id"`
	ProjectID        string          `json:"project_id"`
	AttemptID        string          `json:"attempt_id"`
	WorktreeIdentity string          `json:"worktree_identity"`
	Packet           json.RawMessage `json:"packet"`
	ControlID        string          `json:"control_id"`
}

// WorkerControlPlan carries producer inputs, not permission to execute them.
// The authenticated execution boundary supplies the actual calling agent's
// authority separately; no terminal lane actor exists during a live Task.
type WorkerControlPlan struct {
	Oracle        NativeOracleRequest `json:"oracle"`
	SubjectCommit string              `json:"subject_commit"`
	WorktreePath  string              `json:"worktree_path"`
}

// ResolveWorkerControlTx lets the execution admission revalidate the exact
// binding in its own SQL-only authorization transaction. No native stage or
// Git command may run while that transaction holds the store connection.
func ResolveWorkerControlTx(ctx context.Context, tx *Transaction, req WorkerControlRequest) (WorkerControlPlan, error) {
	q, err := transactionSQL(tx, "worker_control")
	if err != nil {
		return WorkerControlPlan{}, err
	}
	var plan WorkerControlPlan
	if err := resolveWorkerControl(ctx, q, req, &plan); err != nil {
		return WorkerControlPlan{}, err
	}
	return plan, nil
}

func workerControlUnauthorized() error {
	return newFailure(KindUnauthorizedDispatch, "worker_control", "control requires the exact live authorized attempt, packet, worktree, job, preparation, and lane capability", false, "use the original authorized worker and its recorded control")
}

func resolveWorkerControl(ctx context.Context, q queryer, req WorkerControlRequest, plan *WorkerControlPlan) error {
	if req.WorkID == "" || req.ProjectID == "" || req.AttemptID == "" || req.WorktreeIdentity == "" || len(req.Packet) == 0 || !ValidReference(req.ControlID) {
		return newFailure(KindInvalidPayload, "worker_control", "control resolution requires host-held identities and one recorded control id", false, "select a control from the authorized worker job")
	}
	if err := requireNoOutsideRepairTx(ctx, q, req.WorkID, "worker_control"); err != nil {
		return err
	}
	window, err := findAuthorizedDispatchWindowBefore(ctx, q, req.WorkID, req.AttemptID, 0)
	if err != nil {
		return err
	}
	if window.WorktreeIdentity != req.WorktreeIdentity || !worktreeSHAPattern.MatchString(window.SubjectCommit) {
		return workerControlUnauthorized()
	}
	if err := ValidateRecoveryPacket(req.Packet, window.PacketDigest, req.WorkID, req.AttemptID); err != nil {
		return err
	}
	entry, err := activeDispatchWorktreeEntry(ctx, q, req.WorkID, req.ProjectID, window.WorktreeIdentity)
	if err != nil {
		return err
	}
	if workerWorktreeIdentity(entry.Path) != req.WorktreeIdentity {
		return workerControlUnauthorized()
	}
	dispatch, _, err := readNativeOracleDispatchBindingTx(ctx, q, req.WorkID, req.AttemptID)
	if err != nil {
		return err
	}
	if dispatch.WorkerPacketDigest != window.PacketDigest || dispatch.WorkerJob == nil {
		return workerControlUnauthorized()
	}
	lane, err := LookupLane(dispatch.WorkerLaneID, dispatch.WorkerLaneDigest)
	if err != nil {
		return err
	}
	if !slices.Contains(lane.Capabilities, "run_tests") && !slices.Contains(lane.Capabilities, "run_targeted_checks") && !slices.Contains(lane.Capabilities, "run_validators") {
		return workerControlUnauthorized()
	}
	oracle := NativeOracleRequest{Phase: "execute", AttemptID: req.AttemptID, AttemptEpoch: window.AttemptEpoch, WorkerPacketDigest: window.PacketDigest, WorkerJobBinding: dispatch.WorkerJob, ControlID: req.ControlID}
	verify := WorktreeVerifyRequest{WorkID: req.WorkID, ProjectID: req.ProjectID, Oracle: &oracle}
	bundle, version, _, err := readNativeOracleDispatchBundleTx(ctx, q, verify, window.SubjectCommit, req.WorktreeIdentity)
	if err != nil {
		return err
	}
	if bundle.Control.RecipeSource.ProjectID != req.ProjectID || bundle.Owner.Mechanism.ProjectID != req.ProjectID || len(bundle.Control.ReadinessEvidenceRefs) == 0 {
		return workerControlUnauthorized()
	}
	// Declared order chooses one already-qualified preparation. An invalid
	// selected receipt refuses rather than falling through to another runner.
	oracle.PreparationRunRef = bundle.Control.ReadinessEvidenceRefs[0]
	preparation, err := readOraclePreparationReceiptTx(ctx, q, req.WorkID, req.ProjectID, oracle.PreparationRunRef)
	if err != nil {
		return err
	}
	if preparation == nil || preparation.ContractVersion != version || preparation.SubjectCommit != window.SubjectCommit || preparation.BundleDigest != nativeBundleDigest(bundle) {
		return workerControlUnauthorized()
	}
	*plan = WorkerControlPlan{Oracle: oracle, SubjectCommit: window.SubjectCommit, WorktreePath: entry.Path}
	return nil
}

// The authorized attempt already pins its native claim identity. A lane child
// executes there, without borrowing its member caller's authenticated tuple.
func activeDispatchWorktreeEntry(ctx context.Context, q queryer, workID, projectID, identity string) (WorktreeEntry, error) {
	entries, err := worktreeEntriesCore(ctx, q, workID)
	if err != nil { return WorktreeEntry{}, err }
	for _, entry := range entries {
		if entry.ProjectID == projectID && entry.State == worktreeEntryActive && workerWorktreeIdentity(filepath.Clean(entry.Path)) == identity {
			return entry, nil
		}
	}
	return WorktreeEntry{}, workerControlUnauthorized()
}

func worktreeVerifyEntry(ctx context.Context, q queryer, req WorktreeVerifyRequest) (WorktreeEntry, error) {
	if req.NativeAuthorization == nil {
		return activeWorktreeEntryForProject(ctx, q, "worktree_verify", req.WorkID, req.ProjectID)
	}
	window, err := findAuthorizedDispatchWindowBefore(ctx, q, req.WorkID, req.Oracle.AttemptID, 0)
	if err != nil { return WorktreeEntry{}, err }
	return activeDispatchWorktreeEntry(ctx, q, req.WorkID, req.ProjectID, window.WorktreeIdentity)
}
