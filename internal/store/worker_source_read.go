package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

const WorkerReadingPageMaxBytes = 4096

const workerReadingOp = "worker_read"

// WorkerReadingRequest is host-constructed from the in-flight dispatch.
// Only Ordinal, Offset, and Length are worker-selected. Packet is the exact
// held packet, never a caller-authored source or repository path.
type WorkerReadingRequest struct {
	WorkID           string          `json:"work_id"`
	AttemptID        string          `json:"attempt_id"`
	WorktreeIdentity string          `json:"worktree_identity"`
	Packet           json.RawMessage `json:"packet"`
	Ordinal          int             `json:"ordinal"`
	Offset           int             `json:"offset"`
	Length           int             `json:"length"`
	Runner           GitRunner       `json:"-"`
}

type WorkerReadingPage struct {
	Ordinal        int    `json:"ordinal"`
	SourceIdentity string `json:"source_identity"`
	ContentHash    string `json:"content_hash"`
	Offset         int    `json:"offset"`
	NextOffset     int    `json:"next_offset"`
	TotalBytes     int    `json:"total_bytes"`
	EOF            bool   `json:"eof"`
	Data           []byte `json:"data"`
}

type workerReadingSnapshot struct {
	window  WorkerDispatchWindow
	witness dispatchReadingWitness
}

func workerReadingUnauthorized() error {
	return newFailure(KindUnauthorizedDispatch, workerReadingOp, "source read requires the exact active authorized attempt, packet, and worktree", false, "use the original authorized worker")
}

// sampleWorkerReading is SQL-only. The packet hash binds its original reading
// set; reading current context instead would silently add fresh Concord data.
func sampleWorkerReading(ctx context.Context, q queryer, req WorkerReadingRequest, snapshot *workerReadingSnapshot) error {
	if err := requireNoOutsideRepairTx(ctx, q, req.WorkID, workerReadingOp); err != nil {
		return err
	}
	window, err := findAuthorizedDispatchWindowBefore(ctx, q, req.WorkID, req.AttemptID, 0)
	if err != nil {
		return err
	}
	if window.WorktreeIdentity == "" || window.WorktreeIdentity != req.WorktreeIdentity || !window.ContextFrontierPresent {
		return workerReadingUnauthorized()
	}
	if err := ValidateRecoveryPacket(req.Packet, window.PacketDigest, req.WorkID, req.AttemptID); err != nil {
		return err
	}
	var packet struct {
		LaneID     string `json:"lane_id"`
		LaneDigest string `json:"lane_digest"`
		Inputs     struct {
			WorkContext *WorkContextView `json:"work_context"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(req.Packet, &packet); err != nil {
		return workerReadingUnauthorized()
	}
	lane, err := LookupLane(packet.LaneID, packet.LaneDigest)
	if err != nil {
		return err
	}
	if !slices.Contains(lane.Capabilities, "read_repository") {
		return workerReadingUnauthorized()
	}
	var active bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempts WHERE work_id=? AND attempt_id=? AND lane_id=? AND lane_digest=? AND capability_class=? AND lifecycle_state IN ('in_flight','dispatched'))`, req.WorkID, req.AttemptID, packet.LaneID, packet.LaneDigest, lane.CapabilityClass).Scan(&active); err != nil {
		return wrapFailure(KindUnavailable, workerReadingOp, "cannot read worker attempt liveness", true, "retry once the store is readable", err)
	}
	if !active {
		return workerReadingUnauthorized()
	}
	view := packet.Inputs.WorkContext
	if view == nil || view.SourceEventFrontier != window.ContextFrontier || req.Ordinal >= len(view.RequiredReading) {
		return workerReadingUnauthorized()
	}
	witnesses, err := sampleReadingIdentities(ctx, q, req.WorkID, view.RequiredReading, true)
	if err != nil {
		return err
	}
	snapshot.window, snapshot.witness = window, witnesses[req.Ordinal]
	return nil
}

func revalidateWorkerReading(ctx context.Context, q queryer, req WorkerReadingRequest, before workerReadingSnapshot) error {
	var after workerReadingSnapshot
	if err := sampleWorkerReading(ctx, q, req, &after); err != nil {
		return err
	}
	if before.window != after.window || !before.witness.sampledEqual(after.witness) {
		return newFailure(KindStaleRequiresReview, workerReadingOp, "source registration or dispatch changed while the pinned reading was read", false, "reconcile the changed source or attempt before another read")
	}
	if before.witness.arm == WorkContextSourceDomainShape {
		snapshot := DomainShapeSnapshot{}
		for _, item := range before.witness.shape.Facts {
			snapshot.Facts = append(snapshot.Facts, item.Fact)
		}
		return validateDomainShapeSnapshotModelTx(ctx, q, before.witness.shape.ProductID, snapshot)
	}
	return nil
}

// ReadWorkerReading returns a byte page, not workflow context or evidence.
// Both snapshots close before Git or a return. Immutable object reads retain
// the existing committed-read bounds, no-fetch policy, and knowledge proof.
func (s *Store) ReadWorkerReading(ctx context.Context, req WorkerReadingRequest) (WorkerReadingPage, error) {
	var empty WorkerReadingPage
	if req.WorkID == "" || req.AttemptID == "" || req.WorktreeIdentity == "" || len(req.Packet) == 0 || req.Ordinal < 0 || req.Offset < 0 || req.Length < 1 || req.Length > WorkerReadingPageMaxBytes {
		return empty, newFailure(KindInvalidPayload, workerReadingOp, "source read requires a bound packet, a nonnegative ordinal and offset, and length from 1 to 4096 bytes", false, "supply a bounded page of a declared reading")
	}
	ctx, cancel := context.WithTimeout(ctx, workerDispatchReadingResolutionDeadline)
	defer cancel()
	var before workerReadingSnapshot
	if err := s.readSnapshot(ctx, func(q queryer) error {
		return sampleWorkerReading(ctx, q, req, &before)
	}); err != nil {
		return empty, err
	}
	runner := req.Runner
	if runner == nil {
		runner = CommittedReadGitRunner{}
	}
	content, err := readDispatchReadingContent(ctx, runner, before.witness)
	if err != nil {
		return empty, err
	}
	if before.witness.arm == WorkContextSourceDomainShape {
		var selected DomainShapeResult
		if err := json.Unmarshal(content, &selected); err != nil {
			return empty, err
		}
		before.witness.shape = &selected
	}
	if err := s.readSnapshot(ctx, func(q queryer) error {
		return revalidateWorkerReading(ctx, q, req, before)
	}); err != nil {
		return empty, err
	}
	if req.Offset > len(content) {
		return empty, newFailure(KindInvalidPayload, workerReadingOp, "source offset exceeds the pinned blob length", false, "select an offset inside the source")
	}
	end := req.Offset + min(req.Length, len(content)-req.Offset)
	sum := sha256.Sum256(content)
	return WorkerReadingPage{
		Ordinal: req.Ordinal, SourceIdentity: before.witness.identity,
		ContentHash: "sha256:" + hex.EncodeToString(sum[:]), Offset: req.Offset,
		NextOffset: end, TotalBytes: len(content), EOF: end == len(content),
		Data: append([]byte{}, content[req.Offset:end]...),
	}, nil
}
