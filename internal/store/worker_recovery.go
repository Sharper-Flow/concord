package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// WorkerRecoveryContext carries recorded identity, never a new dispatch grant.
// The original packet lives in the host transcript; its core-recorded digest
// is checked at the existing signed worker-dispatch boundary.
type WorkerRecoveryContext struct {
	PacketDigest       string                  `json:"packet_digest"`
	Worktree           string                  `json:"worker_worktree"`
	CoordinatorSession string                  `json:"coordinator_session"`
	AttemptEpoch       int64                   `json:"attempt_epoch"`
	DispatchEventID    string                  `json:"dispatch_event_id"`
	TerminalEventID    string                  `json:"terminal_event_id,omitempty"`
	LifecycleState     string                  `json:"lifecycle_state"`
	Dispatch           WorkerDispatchedPayload `json:"dispatch"`
}

// ValidateWorkerEvidenceReplayWindow checks the original window's identity and
// live claim without asking whether the already-recorded attempt consumed it.
// It never opens a window or authorizes another attempt.
func ValidateWorkerEvidenceReplayWindow(ctx context.Context, transaction *Transaction, workID, attemptID, packetDigest string) error {
	tx, err := transactionSQL(transaction, "worker_recovery")
	if err != nil {
		return err
	}
	window, err := FindAuthorizedDispatchWindowTx(ctx, tx, workID, attemptID)
	if err != nil {
		return err
	}
	if packetDigest == "" {
		// Terminal assertions carry no packet field. Read it from their own
		// recorded dispatch rather than accepting a caller-supplied substitute.
		// An in_flight binding closed by abandonment (CON-791) has no worker
		// dispatch evidence; its authorization window then carries the only
		// core-recorded digest.
		if err := tx.QueryRowContext(ctx, `SELECT json_extract(payload,'$.packet_digest') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? ORDER BY seq DESC LIMIT 1`, SubjectWorkItem, workID, WorkerDispatched, attemptID).Scan(&packetDigest); err != nil {
			if err != sql.ErrNoRows {
				return err
			}
			packetDigest = window.PacketDigest
		}
	}
	if window.PacketDigest == "" || window.PacketDigest != packetDigest {
		return newFailure(KindUnauthorizedDispatch, "worker_recovery", "reconciliation packet digest differs from the original authorization", false, "read the original attempt packet")
	}
	return validateWorkerDispatchWorktreeIdentity(ctx, tx, workID, window.WorktreeIdentity)
}

func WorkerRecoveryContextTx(ctx context.Context, transaction *Transaction, workID, attemptID, projectID, sessionWorktree, actor string) (WorkerRecoveryContext, error) {
	var result WorkerRecoveryContext
	tx, err := transactionSQL(transaction, "worker_recovery")
	if err != nil {
		return result, err
	}
	window, err := FindAuthorizedDispatchWindowTx(ctx, tx, workID, attemptID)
	if err != nil {
		return result, err
	}
	path, err := activeWorkerClaimedWorktree(ctx, tx, workID, sessionWorktree)
	if err != nil {
		return result, err
	}
	if window.WorktreeIdentity == "" || workerWorktreeIdentity(path) != window.WorktreeIdentity {
		return result, newFailure(KindUnauthorizedDispatch, "worker_recovery", "recovery worktree differs from the authorized dispatch", false, "return to the original claimed worktree")
	}
	entries, err := worktreeEntriesCore(ctx, tx, workID)
	if err != nil {
		return result, err
	}
	matchedProject := false
	for _, entry := range entries {
		if entry.State == worktreeEntryActive && entry.ProjectID == projectID && entry.Path == path {
			matchedProject = true
		}
	}
	if !matchedProject {
		return result, newFailure(KindUnauthorizedDispatch, "worker_recovery", "recovery Project does not own the original claimed worktree", false, "recover in the original Project")
	}
	attempt, err := workerAttemptByIDCore(ctx, tx, attemptID)
	if err != nil {
		return result, err
	}
	if attempt.WorkID != workID || attempt.LifecycleState == "failed" {
		return result, newFailure(KindInvalidTransition, "worker_recovery", "retained-report recovery requires the matching dispatched or completed attempt", false, "inspect the original attempt outcome")
	}
	var payload []byte
	var recordedActor string
	if err := tx.QueryRowContext(ctx, `SELECT event_id,actor,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? ORDER BY seq DESC LIMIT 1`, SubjectWorkItem, workID, WorkerDispatched, attemptID).Scan(&result.DispatchEventID, &recordedActor, &payload); err != nil {
		return result, err
	}
	if recordedActor != actor {
		return result, newFailure(KindUnauthorizedDispatch, "worker_recovery", "recovery caller differs from the original evidence actor", false, "use the original authorized client and principal")
	}
	if err := json.Unmarshal(payload, &result.Dispatch); err != nil {
		return result, err
	}
	if result.Dispatch.HostProvenance == nil || result.Dispatch.PacketDigest != window.PacketDigest || result.Dispatch.ReadbackModel == "" {
		return result, newFailure(KindInvariantViolation, "worker_recovery", "recorded dispatch lacks matching packet, provenance, or model identity", false, "restore the original recorded proof; do not reconstruct provenance from changed files")
	}
	if err := tx.QueryRowContext(ctx, `SELECT a.session_ref FROM domain_events d JOIN workflow_actors a ON a.actor_ref=json_extract(d.payload,'$.actor_ref') WHERE d.subject_type=? AND d.subject_id=? AND d.kind=? AND json_extract(d.payload,'$.action_id')='dispatch_worker' AND json_extract(d.payload,'$.worker_attempt_id')=? ORDER BY d.seq DESC LIMIT 1`, SubjectWorkItem, workID, WorkflowActionCompleted, attemptID).Scan(&result.CoordinatorSession); err != nil {
		return result, err
	}
	if attempt.LifecycleState == "completed" {
		if err := tx.QueryRowContext(ctx, `SELECT event_id FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? ORDER BY seq DESC LIMIT 1`, SubjectWorkItem, workID, WorkerCompleted, attemptID).Scan(&result.TerminalEventID); err != nil {
			return result, err
		}
	}
	result.PacketDigest, result.Worktree, result.AttemptEpoch, result.LifecycleState = window.PacketDigest, path, window.AttemptEpoch, attempt.LifecycleState
	return result, nil
}

// ValidateRecoveryPacket uses the same canonical encoder as authorization. The
// adapter cannot substitute a packet or implement a second JSON digest codec.
func ValidateRecoveryPacket(raw json.RawMessage, expectedDigest, workID, attemptID string) error {
	var identity struct {
		WorkID    string `json:"work_id"`
		AttemptID string `json:"attempt_id"`
	}
	if json.Unmarshal(raw, &identity) != nil || identity.WorkID != workID || identity.AttemptID != attemptID {
		return newFailure(KindUnauthorizedDispatch, "worker_recovery", "retained packet has a different work or attempt identity", false, "read the original host Task")
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	if expectedDigest == "" || "sha256:"+hex.EncodeToString(sum[:]) != expectedDigest {
		return newFailure(KindUnauthorizedDispatch, "worker_recovery", "retained packet differs from the core-authorized packet digest", false, "read the original host Task")
	}
	return nil
}
