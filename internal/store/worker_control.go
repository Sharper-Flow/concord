package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
// The authenticated execution boundary must authorize the actual calling agent
// separately and retain the recorded lane actor for execution attribution.
type WorkerControlPlan struct {
	Oracle        NativeOracleRequest `json:"oracle"`
	Actor         WorkflowActor       `json:"actor"`
	SubjectCommit string              `json:"subject_commit"`
}

// ResolveWorkerControl reads the exact recorded control without executing it,
// acquiring a lease, consuming a nonce, or recording an event.
func (s *Store) ResolveWorkerControl(ctx context.Context, req WorkerControlRequest) (WorkerControlPlan, error) {
	var plan WorkerControlPlan
	err := s.readSnapshot(ctx, func(q queryer) error {
		return resolveWorkerControl(ctx, q, req, &plan)
	})
	if err != nil {
		return WorkerControlPlan{}, err
	}
	return plan, nil
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
	return newFailure(KindUnauthorizedDispatch, "worker_control", "control requires the exact active dispatched attempt, packet, worktree, job, preparation, and lane actor", false, "use the original authorized worker and its recorded control")
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
	dispatch, actor, err := readWorkerControlActor(ctx, q, req, window.StartSeq)
	if err != nil {
		return err
	}
	if dispatch.PacketDigest != window.PacketDigest {
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
	*plan = WorkerControlPlan{Oracle: oracle, Actor: actor, SubjectCommit: window.SubjectCommit}
	return nil
}

func readWorkerControlActor(ctx context.Context, q queryer, req WorkerControlRequest, startSeq int64) (WorkerDispatchedPayload, WorkflowActor, error) {
	var dispatch WorkerDispatchedPayload
	var actor WorkflowActor
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT d.payload FROM domain_events d JOIN worker_attempts a ON a.work_id=d.subject_id AND a.attempt_id=json_extract(d.payload,'$.attempt_id')
 AND a.lane_id=json_extract(d.payload,'$.lane_id') AND a.lane_version=json_extract(d.payload,'$.lane_version') AND a.lane_digest=json_extract(d.payload,'$.lane_digest') AND a.capability_class=json_extract(d.payload,'$.capability_class')
 WHERE d.subject_type='work_item' AND d.subject_id=? AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=? AND d.seq>? AND a.lifecycle_state='dispatched' ORDER BY d.seq DESC LIMIT 1`, req.WorkID, WorkerDispatched, req.AttemptID, startSeq).Scan(&raw)
	if err == sql.ErrNoRows {
		return dispatch, actor, workerControlUnauthorized()
	}
	if err != nil {
		return dispatch, actor, err
	}
	if json.Unmarshal(raw, &dispatch) != nil || dispatch.LaneActorRef == "" || dispatch.WorkerJob == nil || dispatch.Terminal != "" {
		return dispatch, actor, workerControlUnauthorized()
	}
	lane, err := LookupLane(dispatch.LaneID, dispatch.LaneVersion, dispatch.LaneDigest)
	if err != nil || (!slices.Contains(lane.Capabilities, "run_tests") && !slices.Contains(lane.Capabilities, "run_targeted_checks")) {
		return dispatch, actor, workerControlUnauthorized()
	}
	err = q.QueryRowContext(ctx, `SELECT actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, dispatch.LaneActorRef).Scan(&actor.ActorRef, &actor.PrincipalRef, &actor.ClientRef, &actor.AgentRef, &actor.SessionRef, &actor.ActorClass)
	if err == sql.ErrNoRows {
		return dispatch, actor, workerControlUnauthorized()
	}
	if err != nil {
		return dispatch, actor, err
	}
	actor.Model = dispatch.ReadbackModel
	if ValidateWorkflowActor(actor) != nil || ValidateWorkflowActorModel(actor.Model) != nil || actor.ActorClass != ActorAgent || actor.AgentRef != "agent/lane:"+dispatch.LaneID || actor.SessionRef != "session/"+req.AttemptID || dispatch.PacketDigest == "" {
		return dispatch, actor, workerControlUnauthorized()
	}
	return dispatch, actor, nil
}
