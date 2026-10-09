package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

// CD-0008 D1 worktree lifecycle. Worktrees and branches are native git
// resources bound to work; Concord records the durable claim, drives native
// creation through the GitRunner seam, verifies the result, and appends the
// verified locator as domain state. Possession grants no Product authority.

const (
	// worktreeSetPrefix derives the one optional worktree set per
	// implementation work item (CD-0008 D1: one canonical work_id, one
	// optional worktree_set_id).
	worktreeSetPrefix = "wts:"

	worktreeStatePending   = "pending"
	worktreeStateVerified  = "verified"
	worktreeStateReclaimed = "reclaimed"

	worktreeEntryActive    = "active"
	worktreeEntryReclaimed = "reclaimed"
)

var (
	worktreeBranchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	worktreeSHAPattern    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// WorktreeEntry is the folded, verified locator state for one Project's
// implementation worktree in a work item's set. Occupancy lives in a separate
// projection table, worktree_occupancy (CD-0178 D3): one row per session,
// with the host process identity and a legacy shape for rows recorded
// without one.
type WorktreeEntry struct {
	SetID        string          `json:"set_id"`
	ProjectID    string          `json:"project_id"`
	ClaimOpID    string          `json:"claim_op_id"`
	Branch       string          `json:"branch"`
	BaseSHA      string          `json:"base_sha"`
	Path         string          `json:"path"`
	RepositoryID string          `json:"repository_id"`
	State        string          `json:"state"`
	VerifiedAt   string          `json:"verified_at"`
	ReclaimedAt  string          `json:"reclaimed_at,omitempty"`
	GitFacts     json.RawMessage `json:"git_facts"`
}

// WorktreeOccupant is one live occupancy row for a worktree, projected from
// worktree_occupancy (CD-0178 D3). HostPID and HostPIDStart stay empty for
// legacy rows that carry no process identity: the process-liveness proof has
// nothing to read, so such a row releases only through session_vacate, an
// operator-approved removal, or the lease-set proof that its recording
// process ended (CD-0179).
type WorktreeOccupant struct {
	WorktreeID         string
	SessionRef         string
	RecordedAt         string
	HostPID            *int64
	HostPIDStart       *uint64
	HasProcessIdentity bool
}

// worktreeOccupancyID is the composite identity the worktree_occupancy table
// keys on. It is the worktree row locator (set, project, claim_op_id) joined
// with ':' so a column lookup stays reversible and a fold handler does not
// have to recompute the parts (CD-0178 D3).
func worktreeOccupancyID(setID, projectID, claimOpID string) string {
	return setID + ":" + projectID + ":" + claimOpID
}

type worktreeCreatedPayload struct {
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
	SetID            string `json:"set_id"`
	ProjectID        string `json:"project_id"`
	ClaimOpID        string `json:"claim_op_id"`
	Branch           string `json:"branch"`
	BaseSHA          string `json:"base_sha"`
	Path             string `json:"path"`
	RepositoryID     string `json:"repository_id"`
	// OccupantSessionRef is the recorded session that owns the claim at
	// creation time. The fold writes an occupancy row keyed on it: one that
	// carries the host process identity when HostPID is positive, and the
	// legacy shape otherwise. A later claim-landing for the same session in
	// the same worktree re-records the row with the landing's identity.
	// Liveness is read from worktree_occupancy (CD-0178 D3).
	OccupantSessionRef string `json:"occupant_session_ref"`
	// HostPID and HostPIDStart are the process identity of the OpenCode
	// process whose adapter recorded the claim. Both are zero on the legacy
	// shape. The core derived the start time from /proc at claim time and
	// the fold replays the recorded value without touching /proc.
	HostPID      int             `json:"host_pid,omitempty"`
	HostPIDStart uint64          `json:"host_pid_start,omitempty"`
	GitFacts     json.RawMessage `json:"git_facts"`
}

// marshalWorktreeCreated builds the one worktree_created payload every claim
// route records. The carrying session is the recorded occupant: the fold
// inserts its worktree_occupancy row with the process identity the caller
// supplied, or the legacy shape when no host pid is known. The occupancy
// fields are explicit parameters so a route cannot record a claim and
// silently omit them.
func marshalWorktreeCreated(expected int64, setID, projectID, claimOpID string, location WorktreeLocation, facts worktreeFacts, occupantSessionRef string, hostPID int, hostPIDStart uint64) []byte {
	payload, _ := json.Marshal(worktreeCreatedPayload{
		ExpectedVersion:    expected,
		ResultingVersion:   expected + 1,
		SetID:              setID,
		ProjectID:          projectID,
		ClaimOpID:          claimOpID,
		Branch:             location.Branch,
		BaseSHA:            location.BaseSHA,
		Path:               location.Path,
		RepositoryID:       facts.repositoryID,
		OccupantSessionRef: occupantSessionRef,
		HostPID:            hostPID,
		HostPIDStart:       hostPIDStart,
		GitFacts:           facts.raw(),
	})
	return payload
}

type worktreeReclaimedPayload struct {
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
	SetID            string `json:"set_id"`
	ProjectID        string `json:"project_id"`
	ClaimOpID        string `json:"claim_op_id,omitempty"`
	// ClaimIncarnation names the claim incarnation the reclaim closed. The
	// first incarnation leaves it empty so its payload keeps the legacy
	// shape; a reopened incarnation records its own count, which keeps
	// convergence scoped to one incarnation.
	ClaimIncarnation int `json:"claim_incarnation,omitempty"`
	// RequestID records the reclaim operation that produced the event. The
	// claim-scoped event identity lets later removal converge on this record.
	RequestID string          `json:"request_id,omitempty"`
	GitFacts  json.RawMessage `json:"git_facts"`
}

type worktreeOccupancyReleasedPayload struct {
	SetID            string `json:"set_id"`
	ProjectID        string `json:"project_id"`
	ClaimOpID        string `json:"claim_op_id"`
	ClaimIncarnation int    `json:"claim_incarnation,omitempty"`
	SessionRef       string `json:"session_ref"`
}

// worktreeRemovalSettledPayload is one per-ref outcome a native removal
// produced after its reclamation committed: a pinned deletion settled, or a
// ref protected with the bounded reason. The fold upserts the outcome row
// keyed by the claim generation, so the record survives directory removal
// and later claims (CD-0212 D3-D4).
type worktreeRemovalSettledPayload struct {
	SetID            string `json:"set_id"`
	ProjectID        string `json:"project_id"`
	ClaimOpID        string `json:"claim_op_id"`
	ClaimIncarnation int    `json:"claim_incarnation,omitempty"`
	Branch           string `json:"branch"`
	Tip              string `json:"tip,omitempty"`
	Phase            string `json:"phase"`
	Reason           string `json:"reason"`
}

func foldWorktreeCreated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p worktreeCreatedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.SetID == "" || p.ProjectID == "" || p.ClaimOpID == "" || p.Branch == "" || p.BaseSHA == "" || p.Path == "" || p.RepositoryID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "worktree creation payload is missing required fields", false, "supply set, project, claim, branch, base, path, and repository identity")
	}
	if p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "worktree creation version must advance by exactly one", false, "supply expected and resulting versions one apart")
	}
	if p.HostPID > 0 && p.HostPIDStart == 0 {
		return newFailure(KindInvalidPayload, "fold_event", "worktree creation payload carries a host pid without its start time", false, "record the process start time the core derived at claim time")
	}
	var state, existingClaim string
	if err := tx.QueryRowContext(ctx, `SELECT state,claim_op_id FROM worktree_entries WHERE set_id=? AND project_id=?`, p.SetID, p.ProjectID).Scan(&state, &existingClaim); err == nil {
		switch {
		case state == worktreeEntryActive && existingClaim == p.ClaimOpID:
			// The same claim appending twice is an idempotent replay. The
			// occupancy row is replayed together with the worktree row, so
			// returning here keeps both consistent.
			return nil
		case state == worktreeEntryReclaimed:
			// Reclamation freed the slot; the new verified claim replaces the
			// reclaimed row (CD-0008 D1: at most one ACTIVE per Project). Any
			// legacy occupancy row carried forward by the reclaimed slot is
			// stale; the fold releases it before the new claim takes effect
			// so a process-liveness check on the new entry starts clean.
			if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_occupancy WHERE worktree_id=?`, worktreeOccupancyID(p.SetID, p.ProjectID, existingClaim)); err != nil {
				return err
			}
		default:
			// Anything else tries to establish a second active worktree for
			// one Project.
			return newFailure(KindProjectionConflict, "fold_event", "worktree set already holds an active worktree for this Project", false, "reclaim the existing worktree before claiming another")
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	// Occupancy lives in worktree_occupancy, not in this projection. The fold
	// inserts the occupant's row when the claim carries a session_ref: with
	// the recorded host process identity when the payload carries one, and
	// the legacy shape otherwise. A later claim-landing for the same session
	// in the same worktree re-records the row (CD-0178 D3).
	if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts) VALUES(?,?,?,?,?,?,?, 'active', ?, NULL, ?)
		ON CONFLICT(set_id, project_id) DO UPDATE SET claim_op_id=excluded.claim_op_id, branch=excluded.branch, base_sha=excluded.base_sha, path=excluded.path, repository_id=excluded.repository_id, state='active', verified_at=excluded.verified_at, reclaimed_at=NULL, git_facts=excluded.git_facts`,
		p.SetID, p.ProjectID, p.ClaimOpID, p.Branch, p.BaseSHA, p.Path, p.RepositoryID, event.OccurredAt.Format(time.RFC3339Nano), string(p.GitFacts)); err != nil {
		return err
	}
	if p.OccupantSessionRef != "" {
		if p.HostPID > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity) VALUES(?,?,?,?,?,1)
				ON CONFLICT(worktree_id, session_ref) DO UPDATE SET recorded_at=excluded.recorded_at, host_pid=excluded.host_pid, host_pid_start=excluded.host_pid_start, has_process_identity=1`,
				worktreeOccupancyID(p.SetID, p.ProjectID, p.ClaimOpID), p.OccupantSessionRef, event.OccurredAt.Format(time.RFC3339Nano), p.HostPID, p.HostPIDStart); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity) VALUES(?,?,?,NULL,NULL,0)
				ON CONFLICT(worktree_id, session_ref) DO UPDATE SET recorded_at=excluded.recorded_at, host_pid=NULL, host_pid_start=NULL, has_process_identity=0`,
				worktreeOccupancyID(p.SetID, p.ProjectID, p.ClaimOpID), p.OccupantSessionRef, event.OccurredAt.Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
	}
	return bumpVersion(ctx, tx, "work_items", event, p.ExpectedVersion, p.ResultingVersion, "work item")
}

func foldWorktreeReclaimed(ctx context.Context, tx *sql.Tx, event Event) error {
	var p worktreeReclaimedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	return foldWorktreeReclaimedTx(ctx, tx, event, p, true)
}

// foldWorktreeReclaimedTx can repair a recorded reclaim without advancing the
// work version a second time when the event already advanced public history.
func foldWorktreeReclaimedTx(ctx context.Context, tx *sql.Tx, event Event, p worktreeReclaimedPayload, advanceVersion bool) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	if p.SetID == "" || p.ProjectID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "worktree reclamation payload is missing required fields", false, "supply set and project")
	}
	if p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "worktree reclamation version must advance by exactly one", false, "supply expected and resulting versions one apart")
	}
	var state, currentClaim, entryPath string
	err := tx.QueryRowContext(ctx, `SELECT state,claim_op_id,path FROM worktree_entries WHERE set_id=? AND project_id=?`, p.SetID, p.ProjectID).Scan(&state, &currentClaim, &entryPath)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "no worktree exists for this Project", false, "claim a worktree before reclaiming it")
	}
	if err != nil {
		return err
	}
	claimOpID := p.ClaimOpID
	if claimOpID == "" {
		// Payload version 1 events predate the claim generation field. They are
		// safe to replay only while no later generation has replaced the row.
		if event.Seq > 0 {
			var currentClaimCreatedAt sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT max(seq) FROM domain_events WHERE kind='work.worktree_created' AND subject_type=? AND subject_id=? AND json_extract(payload,'$.project_id')=? AND json_extract(payload,'$.claim_op_id')=?`, string(SubjectWorkItem), event.SubjectID, p.ProjectID, currentClaim).Scan(&currentClaimCreatedAt); err != nil {
				return err
			}
			if currentClaimCreatedAt.Valid && currentClaimCreatedAt.Int64 > event.Seq {
				return newFailure(KindProjectionConflict, "fold_event", "reclaim event has no claim generation and targets a later worktree claim", false, "emit a reclaim event for the active claim generation")
			}
		}
		// A fresh append cannot be disambiguated by ordering: the sequence is
		// assigned before the fold runs, so it always sorts after every
		// recorded claim, and a payload with no generation cannot prove which
		// generation it targets. When an earlier generation sits reclaimed
		// beside an active entry, refuse the append. A replay folds in log
		// order, where the created-before check above proves the target from
		// sequence position, so only the live path keeps this guard.
		if !isWorkflowReplay(ctx) {
			var reclaimedClaims int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_claims WHERE set_id=? AND project_id=? AND state=?`, p.SetID, p.ProjectID, worktreeStateReclaimed).Scan(&reclaimedClaims); err != nil {
				return err
			}
			if reclaimedClaims > 0 && state == worktreeEntryActive {
				return newFailure(KindProjectionConflict, "fold_event", "reclaim event has no claim generation and cannot target the active worktree", false, "emit a reclaim event for the active claim generation")
			}
		}
		claimOpID = currentClaim
	}
	if currentClaim != claimOpID {
		return newFailure(KindProjectionConflict, "fold_event", "reclaim event targets a stale worktree claim generation", false, "reclaim the active claim generation")
	}
	if state == worktreeEntryReclaimed {
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE worktree_entries SET state='reclaimed', reclaimed_at=?, git_facts=? WHERE set_id=? AND project_id=? AND claim_op_id=? AND state='active'`,
		event.OccurredAt.Format(time.RFC3339Nano), string(p.GitFacts), p.SetID, p.ProjectID, claimOpID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "no active worktree to reclaim for this Project", false, "claim a worktree before reclaiming it")
	}
	// The reclamation's per-ref outcomes start durable here, keyed by the
	// claim generation: every retained ref and every deletion the recorded
	// plan owes holds one row, so the retention and the debt survive
	// directory removal and later claims (CD-0212 D3-D4).
	if err := recordReclaimedRefOutcomesTx(ctx, tx, p, claimOpID, entryPath, event.OccurredAt); err != nil {
		return err
	}
	// The claim row is operational state maintained by live operations, which
	// include routes that revive a reclaimed claim. Replaying the log's
	// reclaims must not regress that newer operational truth, so only the
	// live reclaim keeps the claim row and the fold in lockstep.
	if !isWorkflowReplay(ctx) {
		if _, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET state=?, updated_at=? WHERE op_id=? AND state=?`,
			worktreeStateReclaimed, event.OccurredAt.Format(time.RFC3339Nano), claimOpID, worktreeStateVerified); err != nil {
			return err
		}
	}
	if !advanceVersion {
		return nil
	}
	return bumpVersion(ctx, tx, "work_items", event, p.ExpectedVersion, p.ResultingVersion, "work item")
}

// recordReclaimedRefOutcomesTx inserts the per-ref outcome rows one
// reclamation starts with: its retained refs and the tip-pinned deletions
// its recorded plan owes. The upsert keys on the claim generation and
// incarnation, so an earlier generation's rows never overwrite a later one's
// and vice versa.
func recordReclaimedRefOutcomesTx(ctx context.Context, tx *sql.Tx, p worktreeReclaimedPayload, claimOpID, entryPath string, occurredAt time.Time) error {
	var facts struct {
		RetainedRefs    []WorktreeRetainedRef    `json:"retained_refs"`
		BranchDeletions []WorktreeBranchDeletion `json:"branch_deletions"`
	}
	if len(p.GitFacts) > 0 {
		if err := json.Unmarshal(p.GitFacts, &facts); err != nil {
			// Every append-time surface records facts this fold can decode,
			// so facts that cannot decode are log corruption. Refusing typed
			// keeps the owed rows visible; folding on would commit a
			// reclamation whose per-ref debt silently never exists.
			return newFailure(KindInvalidPayload, "fold_event", "worktree reclamation git facts cannot decode: "+err.Error(), false, "the recorded reclamation facts are corrupt; inspect the reclaim event payload")
		}
	}
	recordedAt := occurredAt.Format(time.RFC3339Nano)
	for _, ref := range facts.RetainedRefs {
		if ref.Branch == "" {
			continue
		}
		reason := ref.Reason
		if reason == "" {
			reason = "retained by the reclamation"
		}
		if err := upsertWorktreeRefOutcomeTx(ctx, tx, worktreeRefOutcome{
			setID: p.SetID, projectID: p.ProjectID, claimOpID: claimOpID, claimIncarnation: p.ClaimIncarnation, branch: ref.Branch,
			phase: WorktreeRefPhaseRetainedUnproven, tip: ref.Tip, reason: reason, path: entryPath, recordedAt: recordedAt,
		}); err != nil {
			return err
		}
	}
	for _, deletion := range facts.BranchDeletions {
		if deletion.Branch == "" {
			continue
		}
		reason := deletion.Reason
		if reason == "" {
			reason = "pinned deletion the recorded plan owes"
		}
		if err := upsertWorktreeRefOutcomeTx(ctx, tx, worktreeRefOutcome{
			setID: p.SetID, projectID: p.ProjectID, claimOpID: claimOpID, claimIncarnation: p.ClaimIncarnation, branch: deletion.Branch,
			phase: WorktreeRefPhasePlanned, tip: deletion.ExpectedTip, reason: reason, path: entryPath, recordedAt: recordedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

// upsertWorktreeRefOutcomeTx writes one per-ref outcome row. The rows are
// fold-only projection state: the worktree_reclaimed fold inserts them, the
// worktree_removal_settled fold updates them, and nothing else writes them.
func upsertWorktreeRefOutcomeTx(ctx context.Context, tx *sql.Tx, row worktreeRefOutcome) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_ref_outcomes(set_id,project_id,claim_op_id,claim_incarnation,branch,phase,tip,reason,path,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(set_id,project_id,claim_op_id,claim_incarnation,branch) DO UPDATE SET phase=excluded.phase, tip=excluded.tip, reason=excluded.reason, recorded_at=excluded.recorded_at`,
		row.setID, row.projectID, row.claimOpID, row.claimIncarnation, row.branch, row.phase, row.tip, row.reason, row.path, row.recordedAt); err != nil {
		return err
	}
	return nil
}

// foldWorktreeRemovalSettled folds one per-ref native outcome behind its
// committed reclamation: a settled deletion retires the debt row, a
// protection records the ref it kept with the bounded reason. The
// admission and the write live in applyWorktreeRefOutcomeTx.
func foldWorktreeRemovalSettled(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p worktreeRemovalSettledPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	return applyWorktreeRefOutcomeTx(ctx, tx, p, event.OccurredAt.Format(time.RFC3339Nano))
}

// applyWorktreeRefOutcomeTx is the durable phase owner's admission and
// write: a row already holding the recorded phase converges, and any move
// to a different phase must be one the transition table admits from the
// row's recorded predecessor (CD-0212 D4).
func applyWorktreeRefOutcomeTx(ctx context.Context, tx *sql.Tx, p worktreeRemovalSettledPayload, recordedAt string) error {
	if p.SetID == "" || p.ProjectID == "" || p.ClaimOpID == "" || p.Branch == "" || p.Phase == "" {
		return newFailure(KindInvalidPayload, "fold_event", "worktree removal settlement payload is missing required fields", false, "supply set, project, claim, branch, and phase")
	}
	if !validWorktreeRefPhase(p.Phase) {
		return newFailure(KindInvalidPayload, "fold_event", "worktree removal settlement carries an unknown ref phase", false, "settle one of the seven recorded phases")
	}
	var current sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT phase FROM worktree_ref_outcomes WHERE set_id=? AND project_id=? AND claim_op_id=? AND claim_incarnation=? AND branch=?`,
		p.SetID, p.ProjectID, p.ClaimOpID, p.ClaimIncarnation, p.Branch).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current.Valid && current.String != p.Phase && !refPhaseTransitionAdmitted(current.String, p.Phase) {
		return newFailure(KindProjectionConflict, "fold_event",
			"the durable ref phase owner refuses to move branch "+p.Branch+" from "+current.String+" to "+p.Phase+": no recorded step admits that transition",
			false, "settle the branch through the predecessor phase its recorded plan admits")
	}
	reason := p.Reason
	if reason == "" {
		reason = "settled by the native removal"
	}
	return upsertWorktreeRefOutcomeTx(ctx, tx, worktreeRefOutcome{
		setID: p.SetID, projectID: p.ProjectID, claimOpID: p.ClaimOpID, claimIncarnation: p.ClaimIncarnation,
		branch: p.Branch, phase: p.Phase, tip: p.Tip, reason: reason, recordedAt: recordedAt,
	})
}

func foldWorktreeOccupancyReleased(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p worktreeOccupancyReleasedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.SetID == "" || p.ProjectID == "" || p.ClaimOpID == "" || p.SessionRef == "" {
		return newFailure(KindInvalidPayload, "fold_event", "worktree occupancy release payload is missing required fields", false, "supply set, project, claim, and session identity")
	}
	// The active-worktree check still guards the release: a release for an
	// already-reclaimed worktree has no row to delete and the fold returns.
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM worktree_entries WHERE set_id=? AND project_id=? AND claim_op_id=?`, p.SetID, p.ProjectID, p.ClaimOpID).Scan(&state); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "fold_event", "no worktree exists for this claim", false, "claim a worktree before releasing occupancy")
		}
		return err
	}
	if state != worktreeEntryActive {
		return nil
	}
	// The release deletes the (worktree_id, session_ref) row it names; the
	// projection permits only the released session or an empty slot, and the
	// payload names it so the fold refuses a mismatched target (CD-0178 D3).
	res, err := tx.ExecContext(ctx, `DELETE FROM worktree_occupancy WHERE worktree_id=? AND session_ref=?`, worktreeOccupancyID(p.SetID, p.ProjectID, p.ClaimOpID), p.SessionRef)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return newFailure(KindProjectionConflict, "fold_event", "occupancy release targets a session not recorded for this worktree", false, "release a session the worktree currently records")
	}
	return nil
}

// WorktreeSetID derives the one optional worktree set id for a work item.
func WorktreeSetID(workID string) string { return worktreeSetPrefix + workID }

// WorktreeEntries lists folded entries for a work item's set.
func (s *Store) WorktreeEntries(ctx context.Context, workID string) ([]WorktreeEntry, error) {
	return worktreeEntriesCore(ctx, s.db, workID)
}

func worktreeEntriesCore(ctx context.Context, q queryer, workID string) ([]WorktreeEntry, error) {
	rows, err := q.QueryContext(ctx, `SELECT set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts FROM worktree_entries WHERE set_id=? ORDER BY project_id`, WorktreeSetID(workID))
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worktree_entries", "cannot read worktree entries", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []WorktreeEntry
	for rows.Next() {
		var e WorktreeEntry
		var facts string
		var reclaimed sql.NullString
		if err := rows.Scan(&e.SetID, &e.ProjectID, &e.ClaimOpID, &e.Branch, &e.BaseSHA, &e.Path, &e.RepositoryID, &e.State, &e.VerifiedAt, &reclaimed, &facts); err != nil {
			return nil, err
		}
		e.ReclaimedAt = reclaimed.String
		e.GitFacts = json.RawMessage(facts)
		out = append(out, e)
	}
	return out, rows.Err()
}

func worktreeEntriesTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorktreeEntry, error) {
	return worktreeEntriesCore(ctx, tx, workID)
}

// WorktreeClaimRequest drives the durable claim operation. The claim is
// atomic: the pinned intent row and the verified locator event commit together
// or not at all, and an interruption is reconciled by retrying with the same
// OpID.
type WorktreeClaimRequest struct {
	OpID            string
	WorkID          string
	ProjectID       string
	BaseSHA         string
	PrincipalRef    string
	RequestID       string
	SessionRef      string
	ExpectedVersion int64
	Now             time.Time
	Runner          GitRunner
	// HostPID is the OpenCode process whose adapter records the claim. When
	// it is positive the core derives its start time from /proc and the
	// created occupancy row carries the process identity, so no claim made
	// through the agent surface leaves a row without one. Zero records the
	// pre-rule legacy shape, which the reclaim gate can release through the
	// host lease set.
	HostPID int
}

type WorktreeClaimResult struct {
	Entry      WorktreeEntry
	Reconciled bool
	// Created is non-nil when this operation created the native worktree
	// itself with `git worktree add`. The caller that owns the commit —
	// Store.ClaimWorktree's Commit, or the agent mutation envelope's —
	// compensates a post-claim failure from these facts. A tree that
	// pre-existed the claim stays nil and is never compensation's to remove.
	Created *WorktreeClaimCreation
}

// WorktreeClaimCreation records the native state one claim operation created:
// the worktree path, the branch it sits on, the pinned base, and whether the
// operation created the branch itself or adopted an existing one.
type WorktreeClaimCreation struct {
	RepoRoot      string
	Path          string
	Branch        string
	Base          string
	CreatedBranch bool
}

// WorktreeClaimNative is the native half of one claim: the located pinned
// intent and the verified git facts for the created or adopted tree. The
// caller probes it with no transaction open (CD-0195 D2); the claim's
// transaction re-validates the durable state against it and records the
// verified locator.
type WorktreeClaimNative struct {
	Location   WorktreeLocation
	Facts      worktreeFacts
	navigation *workContextNavigationProof
	// CreatedBranch records whether this operation created the branch itself,
	// as opposed to adopting one a prior attempt left behind. Compensation
	// may remove only what this operation created.
	CreatedBranch bool
}

func (s *Store) ClaimWorktree(ctx context.Context, req WorktreeClaimRequest) (_ WorktreeClaimResult, retErr error) {
	if s == nil || s.db == nil {
		return WorktreeClaimResult{}, newFailure(KindUnavailable, "worktree_claim", "store is not open", false, "open the authority database")
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	// The native half runs before the transaction opens (CD-0195 D2): the
	// location derives, the tree is created, and the creation verifies with
	// no write lock held. A failure after a creation compensates it here.
	native, created, err := s.PrepareWorktreeClaimNative(ctx, req)
	if err != nil {
		if created != nil {
			return WorktreeClaimResult{}, compensateClaimWorktree(ctx, runner, *created, err)
		}
		return WorktreeClaimResult{}, err
	}
	tx, err := s.beginDurableTx(ctx)
	if err != nil {
		cause := wrapFailure(KindUnavailable, "worktree_claim", "cannot begin claim", true, "retry once the database is writable", err)
		if created != nil {
			return WorktreeClaimResult{}, compensateClaimWorktree(ctx, runner, *created, cause)
		}
		return WorktreeClaimResult{}, cause
	}
	defer tx.finish(&retErr)
	out, err := claimWorktreeStoreTx(ctx, tx.Tx, req, native)
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			failure := wrapFailure(KindUnavailable, "worktree_claim", "cannot finish claim rollback", true, "reconcile the claim before removing its native state", errors.Join(err, rollbackErr))
			failure.EffectPossible = true
			return WorktreeClaimResult{}, failure
		}
		if created != nil {
			return WorktreeClaimResult{}, compensateClaimWorktree(ctx, runner, *created, err)
		}
		return WorktreeClaimResult{}, err
	}
	out.Created = created
	if err := tx.Commit(); err != nil {
		commitErr := wrapFailure(KindUnavailable, "worktree_claim", "cannot commit claim", true, "retry the same operation with the same op id", err)
		// A persisted claim may still name its native state after a commit or
		// restoration failure. Only a proven absence permits compensation.
		if commitErr.EffectPossible {
			return WorktreeClaimResult{}, commitErr
		}
		if created != nil {
			return WorktreeClaimResult{}, compensateClaimWorktree(ctx, runner, *created, commitErr)
		}
		return WorktreeClaimResult{}, commitErr
	}
	return out, nil
}

// ClaimWorktreeTx is the durable claim on an existing transaction, so the
// agent tool surface can compose it with its own idempotency envelope. The
// caller probes the native half with (*Store).PrepareWorktreeClaimNative
// before its transaction opens and compensates the reported creation when
// the transaction fails (CD-0195 D2).
func ClaimWorktreeTx(ctx context.Context, transaction *Transaction, req WorktreeClaimRequest, native WorktreeClaimNative) (WorktreeClaimResult, error) {
	tx, err := transactionSQL(transaction, "worktree_claim")
	if err != nil {
		return WorktreeClaimResult{}, err
	}
	if req.Now.IsZero() {
		req.Now = transaction.now()
	}
	transaction.navigation = mergeWorkContextNavigationProof(transaction.navigation, native.navigation)
	return claimWorktreeStoreTx(ctx, tx, req, native)
}

// PrepareWorktreeClaimNative derives the claim's pinned intent and creates
// the native worktree with no transaction open (CD-0195 D2). It reports the
// creation as soon as this operation's tree exists, so a caller whose later
// work fails can compensate exactly what the claim created; a tree that
// pre-existed the claim stays nil and is never compensation's to remove.
func (s *Store) PrepareWorktreeClaimNative(ctx context.Context, req WorktreeClaimRequest) (WorktreeClaimNative, *WorktreeClaimCreation, error) {
	native := WorktreeClaimNative{}
	if req.OpID == "" || req.WorkID == "" || req.ProjectID == "" || req.PrincipalRef == "" || req.RequestID == "" {
		return native, nil, newFailure(KindInvalidOperation, "worktree_claim", "claim operation is missing identity fields", false, "supply op, work, project, principal, and request ids")
	}
	if !worktreeSHAPattern.MatchString(req.BaseSHA) {
		return native, nil, newFailure(KindInvalidOperation, "worktree_claim", "base is not a full commit SHA", false, "pin the exact base commit SHA")
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	// The first creation owes the shared Git preflight: one bounded,
	// noninteractive fetch of the registered origin default branch, before
	// this operation resolves its location or creates any branch or
	// worktree. The caller's base SHA stays an exact pin; the fetch keeps
	// the remote-tracking cache current and never moves a pin. A retry whose
	// durable claim row already exists is an exact replay or recovery — and
	// so is a route that pinned its prepared intent to resolve its base, the
	// cross-Project resume claim — and keeps its stored base and owes no new
	// fetch. A refresh failure refuses before any native effect exists
	// (CD-0195 D2 keeps the fetch outside transactions).
	var claimState string
	claimErr := s.db.QueryRowContext(ctx, `SELECT state FROM worktree_claims WHERE op_id=?`, req.OpID).Scan(&claimState)
	if claimErr != nil && claimErr != sql.ErrNoRows {
		return native, nil, wrapFailure(KindUnavailable, "worktree_claim", "cannot read the claim operation", true, "retry once the database is readable", claimErr)
	}
	var location WorktreeLocation
	var locateErr error
	if claimErr == sql.ErrNoRows {
		// The first creation's base resolution shares the preflight window:
		// the default-ref read and the pinned-base probe run under the same
		// fixed deadline the fetch ran under (CD-0088 D2). The window closes
		// once the base is resolved, before any branch or worktree exists.
		repo, repoErr := s.ProjectCanonicalPath(ctx, req.ProjectID)
		if repoErr != nil {
			return native, nil, repoErr
		}
		preflight, preflightErr := startCreationPreflight(ctx, runner, repo)
		if preflightErr != nil {
			return native, nil, wrapFailure(KindGitUnreachable, "worktree_claim", "origin default branch refresh failed before claim; no branch or worktree was created", true, "restore access to the origin remote and retry the same claim", preflightErr)
		}
		location, locateErr = locateWorktree(preflight.ctx, s.db, filepath.Dir(s.Path()), req.ProjectID, req.WorkID, "", runner)
		deadlineTripped := preflight.tripped(ctx)
		preflight.close()
		if locateErr != nil && deadlineTripped {
			return native, nil, freshnessDeadlineRefusal("worktree_claim")
		}
	} else {
		location, locateErr = locateWorktree(ctx, s.db, filepath.Dir(s.Path()), req.ProjectID, req.WorkID, "", runner)
	}
	if locateErr != nil {
		return native, nil, locateErr
	}
	native.Location = location
	// Probe before creating or retrying. Git worktree creation is not
	// idempotent; the probe owns retry safety for an interrupted create.
	created, facts, probeErr := probeWorktree(ctx, runner, location.Repo, location.Path, location.Branch, req.BaseSHA)
	if probeErr != nil {
		return native, nil, probeErr
	}
	native.Facts = facts
	if created {
		native.navigation = prepareClaimNavigation(ctx, s, req, location.Path)
		return native, nil, nil
	}
	// A branch left by a prior failed claim can be adopted only when it
	// still points at the pinned base. A divergent branch is native state
	// that this claim must not overwrite.
	branchHead, branchExists, branchErr := worktreeBranchHead(ctx, runner, location.Repo, location.Branch)
	if branchErr != nil {
		return native, nil, branchErr
	}
	if branchExists {
		if branchHead != req.BaseSHA {
			return native, nil, newFailure(KindProjectionConflict, "worktree_claim", "existing branch does not match the pinned base", false, "resolve the existing branch before claiming this worktree")
		}
		if _, err := runner.Run(ctx, location.Repo, "worktree", "add", location.Path, location.Branch); err != nil {
			return native, nil, worktreeAddFailure(ctx, runner, location.Repo, location.Path, location.Branch, false, err)
		}
	} else {
		if _, err := runner.Run(ctx, location.Repo, "worktree", "add", location.Path, "-b", location.Branch, req.BaseSHA); err != nil {
			return native, nil, worktreeAddFailure(ctx, runner, location.Repo, location.Path, location.Branch, true, err)
		}
		native.CreatedBranch = true
	}
	// From this point the tree is this operation's own creation, so every
	// later failure is compensated from these facts.
	createdTree := &WorktreeClaimCreation{RepoRoot: location.Repo, Path: location.Path, Branch: location.Branch, Base: req.BaseSHA, CreatedBranch: native.CreatedBranch}
	created, facts, verifyErr := probeWorktree(ctx, runner, location.Repo, location.Path, location.Branch, req.BaseSHA)
	if verifyErr != nil {
		return native, createdTree, verifyErr
	}
	if !created {
		return native, createdTree, newFailure(KindGitUnreachable, "worktree_claim", "created worktree did not verify against the pinned intent", false, "contact_operator")
	}
	native.Facts = facts
	native.navigation = prepareClaimNavigation(ctx, s, req, location.Path)
	return native, createdTree, nil
}

// claimWorktreeStoreTx is the durable half of a claim: it re-validates the
// durable state against the probed native intent and records the verified
// locator. It runs SQL only — every git fact arrived in the native half
// (CD-0195 D2).
func claimWorktreeStoreTx(ctx context.Context, tx *sql.Tx, req WorktreeClaimRequest, native WorktreeClaimNative) (WorktreeClaimResult, error) {
	out := WorktreeClaimResult{}
	location := native.Location
	derivedBranch, derivedPath, repoRoot := location.Branch, location.Path, location.Repo
	if err := validateWorktreeProjectMembershipTx(ctx, tx, req.WorkID, req.ProjectID, "worktree_claim"); err != nil {
		return out, err
	}
	if err := refuseWhenSessionOccupiesAnotherWorktreeTx(ctx, tx, req.SessionRef, WorktreeSetID(req.WorkID)); err != nil {
		return out, err
	}
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	setID := WorktreeSetID(req.WorkID)

	// Phase 1: the durable claim, re-validated against the probed intent. An
	// existing row for this OpID reconciles with its pinned intent; the
	// pinned values win over later arguments so a retry can never redirect
	// the operation.
	var state, pinnedBranch, pinnedBase, pinnedPath string
	err := tx.QueryRowContext(ctx, `SELECT state,pinned_branch,pinned_base_sha,pinned_path FROM worktree_claims WHERE op_id=?`, req.OpID).Scan(&state, &pinnedBranch, &pinnedBase, &pinnedPath)
	switch {
	case err == nil:
		if pinnedBranch != derivedBranch || pinnedBase != req.BaseSHA || pinnedPath != derivedPath {
			return out, newFailure(KindInvalidOperation, "worktree_claim", "retry does not match the derived pinned intent", false, "retry with the same work and Project identity")
		}
		if state == worktreeStateVerified || state == worktreeStateReclaimed {
			// Idempotent replay: return the folded state without side effects.
			entry, readErr := worktreeEntryByClaim(ctx, tx, req.OpID)
			if readErr != nil {
				return out, readErr
			}
			if state == worktreeStateReclaimed && entry.State != worktreeEntryReclaimed {
				return out, newFailure(KindInvalidOperation, "worktree_claim", "claim is reclaimed but its entry is not", false, "contact_operator")
			}
			out.Entry = entry
			out.Reconciled = true
			return out, nil
		}
		out.Reconciled = true
	case err == sql.ErrNoRows:
		// A second active worktree for one Project is refused before git is
		// ever invoked (CD-0008 D1: at most one active per affected Project).
		// The native add ran before this transaction under CD-0195 D2, so a
		// refusal here is the caller's signal to compensate the creation.
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_claims WHERE work_id=? AND project_id=? AND state IN ('pending','verified')`, req.WorkID, req.ProjectID).Scan(&active); err != nil {
			return out, wrapFailure(KindUnavailable, "worktree_claim", "cannot read active claims", true, "retry once the database is readable", err)
		}
		if active > 0 {
			return out, newFailure(KindProjectionConflict, "worktree_claim", "work already holds an active worktree for this Project", false, "reclaim the existing worktree before claiming another")
		}
		// The branch slot is unique per repository: the branch name derives
		// from the work identity alone, so two Projects of one work item on
		// two repositories share it and both claims must hold. A lost race
		// against a committed slot holder is a typed conflict, not a retryable
		// storage failure.
		if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			req.OpID, req.WorkID, req.ProjectID, setID, repoRoot, derivedBranch, req.BaseSHA, derivedPath, worktreeStatePending, req.PrincipalRef, req.RequestID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			if isUniqueViolation(err) {
				return out, newFailure(KindProjectionConflict, "worktree_claim", "another active claim already pins this repository's branch or this native path", false, "reclaim the holding worktree before claiming again")
			}
			return out, wrapFailure(KindUnavailable, "worktree_claim", "cannot persist claim", true, "retry once the database is writable", err)
		}
	default:
		return out, wrapFailure(KindUnavailable, "worktree_claim", "cannot read claim", true, "retry once the database is readable", err)
	}

	// The occupancy row's process start time is derived from /proc here, on
	// the live path only: the fold replays the recorded value and never
	// re-derives it. A bounded single-file read stays inside the transaction
	// (CD-0195 D2 scope).
	var hostPIDStart uint64
	if req.HostPID > 0 {
		start, startErr := hostlease.ProcessStart(req.HostPID)
		if startErr != nil {
			return out, wrapFailure(KindUnavailable, "worktree_claim", "cannot read the host process start time", true, "retry once the host process is observable", startErr)
		}
		hostPIDStart = start
	}
	phase3Err := claimWorktreePhase3Tx(ctx, tx, req, setID, now, derivedBranch, req.BaseSHA, derivedPath, native.Facts, hostPIDStart)
	if phase3Err != nil {
		return out, phase3Err
	}
	entry, err := worktreeEntryByClaim(ctx, tx, req.OpID)
	if err != nil {
		return out, err
	}
	out.Entry = entry
	return out, nil
}

// claimWorktreePhase3Tx is the durable half of a claim: the verified locator
// event and the verified claim state fold and commit together. The git
// worktree the phases before it created exists outside the transaction, so a
// failure here rolls the durable record back while the native tree remains.
// hostPIDStart is the value the live path derived from /proc for the
// requesting host process; the payload records it so the fold replays the
// identity without touching /proc.
func claimWorktreePhase3Tx(ctx context.Context, tx *sql.Tx, req WorktreeClaimRequest, setID string, now time.Time, pinnedBranch, pinnedBase, pinnedPath string, facts worktreeFacts, hostPIDStart uint64) error {
	payload := marshalWorktreeCreated(req.ExpectedVersion, setID, req.ProjectID, req.OpID, WorktreeLocation{Branch: pinnedBranch, BaseSHA: pinnedBase, Path: pinnedPath}, facts, req.SessionRef, req.HostPID, hostPIDStart)
	if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: fmt.Sprintf("%s:worktree-created", req.OpID), Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.PrincipalRef, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion}}, newFoldScope(tx), false); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET state=?, updated_at=? WHERE op_id=?`, worktreeStateVerified, now.Format(time.RFC3339Nano), req.OpID); err != nil {
		return wrapFailure(KindUnavailable, "worktree_claim", "cannot complete claim", true, "retry the same operation with the same op id", err)
	}
	return nil
}

// compensateClaimWorktree removes the native worktree and branch a failed
// claim created, so the rolled-back transaction does not leave an unowned
// worktree behind. The removal runs only when the tree is clean and holds no
// commit beyond the pinned base, and the branch is removed only when this
// operation created it. A removal that cannot complete marks the failure
// effect-possible: the native tree then outlives the claim row. Compensation
// runs detached from the caller's context, because a budget deadline that
// fails the transaction must not stop the cleanup that keeps the no-effect
// classification honest.
func compensateClaimWorktree(ctx context.Context, runner GitRunner, created WorktreeClaimCreation, cause error) error {
	ctx = context.WithoutCancel(ctx)
	incomplete := func(reason error) error {
		var failure *Failure
		if errors.As(cause, &failure) {
			failure.EffectPossible = true
			return cause
		}
		detail := "claim failed after the native worktree was created and compensation could not remove it; the created worktree may outlive the rolled-back claim"
		if reason != nil {
			detail += ": " + reason.Error()
		}
		wrapped := wrapFailure(KindUnavailable, "worktree_claim", detail, true, "retry the same operation with the same op id", cause)
		wrapped.EffectPossible = true
		return wrapped
	}
	statusOut, statusErr := runner.Run(ctx, created.Path, "status", "--porcelain")
	if statusErr != nil || strings.TrimSpace(string(statusOut)) != "" {
		return incomplete(statusErr)
	}
	countOut, countErr := runner.Run(ctx, created.RepoRoot, "rev-list", "--count", created.Base+".."+created.Branch)
	if countErr != nil || strings.TrimSpace(string(countOut)) != "0" {
		return incomplete(countErr)
	}
	if _, rmErr := runner.Run(ctx, created.RepoRoot, "worktree", "remove", created.Path); rmErr != nil {
		return incomplete(rmErr)
	}
	// The branch pointer is proven redundant: the count above established
	// that nothing is reachable from the branch beyond the pinned base.
	if created.CreatedBranch {
		if _, brErr := runner.Run(ctx, created.RepoRoot, "branch", "-D", "--", created.Branch); brErr != nil {
			return incomplete(brErr)
		}
	}
	return cause
}

// worktreeAddFailure classifies a failed `git worktree add`. Git can leave a
// partial tree directory, or the branch it was asked to create, before it
// reports the error. The rolled-back claim cannot see that state, so the
// failure reports the effect possible when the path exists, when a branch this
// call asked git to create exists, or when either fact cannot be read. It
// reports no effect only when both facts prove nothing remains.
func worktreeAddFailure(ctx context.Context, runner GitRunner, repoRoot, path, branch string, newBranch bool, addErr error) error {
	cause := wrapFailure(KindGitUnreachable, "worktree_claim", "native worktree creation failed; the claim stays pending for reconciliation", true, "retry the same operation with the same op id", addErr)
	ctx = context.WithoutCancel(ctx)
	if _, statErr := os.Lstat(path); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
		cause.EffectPossible = true
		return cause
	}
	if newBranch {
		_, exists, branchErr := worktreeBranchHead(ctx, runner, repoRoot, branch)
		if branchErr != nil || exists {
			cause.EffectPossible = true
		}
	}
	return cause
}

// CompensateWorktreeClaimCreation removes the native worktree and branch a
// claim created, for a caller whose own transaction failed after
// ClaimWorktreeTx or ClaimWorktree reported the creation. The creation facts
// are the claim's report of what it itself created, so a caller never
// compensates a tree that pre-existed the claim. A removal that cannot
// complete marks the cause effect-possible instead of claiming no effect.
func CompensateWorktreeClaimCreation(ctx context.Context, runner GitRunner, created WorktreeClaimCreation, cause error) error {
	if runner == nil {
		runner = ExecGitRunner{}
	}
	return compensateClaimWorktree(ctx, runner, created, cause)
}

// WorktreeReclaimRequest reclaims a worktree from git facts: the tree must be
// clean, the branch must be durable in a remote ref, and no native remove may
// be forced. A stale Concord projection never overrides stronger git truth.
type WorktreeReclaimRequest struct {
	WorkID          string
	ProjectID       string
	DefaultRef      string
	PrincipalRef    string
	RequestID       string
	ExpectedVersion int64
	Now             time.Time
	Runner          GitRunner
	// RequireTerminal gates the request on terminal work (the CD-0096 D3
	// Destroy tier). The reclaim surface leaves it false.
	RequireTerminal bool
	// RequireUnstarted gates the request on unstarted work (CD-0118): the
	// item stays at needed and the worktree branch holds no commit beyond
	// the Project's default ref. The reclaim surface leaves it false.
	RequireUnstarted bool
	// OperatorApprovalRef names the operator approval consumed for a removal
	// the terminal gate would otherwise refuse (CD-0096 D3 Destroy). Empty
	// keeps the gate. The git safety gates are unaffected by this field.
	OperatorApprovalRef string
	// Destructive declares that the consumed approval also covers discarding
	// the git safety gates: the clean-tree and durable-branch checks are
	// skipped and the native remove is forced. It requires a non-empty
	// OperatorApprovalRef; the surface guarantees the pairing and the store
	// refuses the combination that would skip gates unapproved.
	Destructive bool
	// ReleaseOccupancy permits an operator-approved destroy to clear a stale
	// recorded occupant before the removal gate runs.
	ReleaseOccupancy bool
	// HostLeases is the live host lease set the caller read before the
	// transaction began. A legacy occupancy row (no process identity)
	// releases only when every lease in this set started after the row's
	// recorded_at, which proves the row's recording process ended; an
	// unreadable lease set releases nothing.
	HostLeases HostLeaseSet
}

// HostLeaseSet is the caller's observation of the live host lease set, kept
// as one value so an unreadable observation travels with the request instead
// of collapsing into an empty set. Err carries the read failure: a set with
// Err proves nothing, and the reclaim gate releases no legacy row on it.
type HostLeaseSet struct {
	Leases []hostlease.Lease
	Err    error
}

// ReadHostLeases reads the live host lease set for this store's data root.
// The caller runs it before the reclaim transaction opens: the read walks the
// filesystem and prunes stale lease files, and none of that belongs inside a
// write transaction.
func (s *Store) ReadHostLeases() HostLeaseSet {
	leases, err := hostlease.List(filepath.Dir(s.Path()))
	return HostLeaseSet{Leases: leases, Err: err}
}

// hostLeaseSetContextKey carries the caller's pre-transaction host lease
// observation into gates that run inside the transaction's folds.
type hostLeaseSetContextKey struct{}

// WithHostLeaseSet returns a context that carries set, the live host lease
// set the caller read before the transaction opened (CD-0179 D3). Gates
// running inside the transaction read the observation from here instead of
// walking the lease filesystem while the write transaction holds the pool's
// only connection.
func WithHostLeaseSet(ctx context.Context, set HostLeaseSet) context.Context {
	return context.WithValue(ctx, hostLeaseSetContextKey{}, set)
}

// hostLeaseSetFromContext returns the lease observation carried on ctx and
// whether any observation was carried at all. A caller that carried nothing
// releases no legacy row: the release rule needs the observation, not the
// absence of one.
func hostLeaseSetFromContext(ctx context.Context) (HostLeaseSet, bool) {
	set, ok := ctx.Value(hostLeaseSetContextKey{}).(HostLeaseSet)
	return set, ok
}

// hostLeaseWallStart resolves one live lease's process start as wall-clock
// time. It is a package variable so tests can pin the conversion.
var hostLeaseWallStart = hostlease.WallStart

// wallStartSkewBound is the conservative bound on hostlease.WallStart's
// approximation error. WallStart derives the start from /proc ticks, the
// system uptime, and the wall clock read in separate instants, so a
// deschedule between those reads can push the computed start later than the
// process's true start. The release comparison must hold WallStart to the
// precision it can prove: a lease whose computed start sits within this
// bound of the row's recorded_at proves nothing, and the row stays.
const wallStartSkewBound = time.Minute

// legacyOccupancyRowEnded applies the one release rule a legacy row admits
// (CD-0179): every live host lease started after the row's recorded_at, so
// no process alive today existed when the row was recorded and its recording
// process has ended. An unreadable lease set, an unreadable process start,
// a live lease that predates the row, or a lease whose computed start is
// within WallStart's skew of the row releases nothing and reports false.
func legacyOccupancyRowEnded(set HostLeaseSet, recordedAt string) bool {
	if set.Err != nil {
		return false
	}
	recorded, err := time.Parse(time.RFC3339Nano, recordedAt)
	if err != nil {
		return false
	}
	for _, lease := range set.Leases {
		started, err := hostLeaseWallStart(lease.PID)
		if err != nil {
			return false
		}
		if !started.After(recorded.Add(wallStartSkewBound)) {
			return false
		}
	}
	return true
}

// WorktreeNativeRemoval is the bounded native-removal plan one committed
// reclaim or destroy still owes (CD-0212 D4): the worktree remove and the
// proven, tip-pinned branch deletions. The transaction commits the
// reclamation event first, recording this plan in its facts; the caller runs
// the removal after that commit, with no transaction open (CD-0195 D2), and
// a retry re-derives the pending deletions from the recorded plan so a
// directory removal that outlived its branch deletion cannot lose it.
//
// The plan is the authority for every replay: the retry decodes it from the
// committed facts and never re-derives its force, its pinned tips, or its
// recorded live-HEAD identity from the retry's own arguments.
type WorktreeNativeRemoval struct {
	RepoRoot string
	Path     string
	Force    bool
	Op       string
	// LiveTip is the tip the committed plan recorded on the live HEAD. The
	// removal revalidates it, and the repository identity of the checkout,
	// before its destructive directory effect. A retry carries the recorded
	// tip, never a freshly observed one.
	LiveTip string
	// DefaultRef is the merge-target spelling the plan carried. The native
	// boundary re-derives the protected default set from the repository and
	// this spelling together, so a default that moved onto a planned branch
	// after the plan committed still protects it (CD-0212 D3-D4).
	DefaultRef string
	// BranchDeletions are the branch deletions the plan owes, each proven
	// durable at probe time and pinned to the tip then observed. A ref whose
	// tip changed is never deleted.
	BranchDeletions []WorktreeBranchDeletion
	// WorkID, ProjectID, ClaimOpID, and ClaimIncarnation name the committed
	// reclamation whose plan this removal executes. They key the durable
	// per-ref outcome settlement, so a later claim generation cannot erase
	// another generation's retentions or deletion debt.
	WorkID           string
	ProjectID        string
	ClaimOpID        string
	ClaimIncarnation int
	// PrincipalRef and RequestID name the request that committed the plan,
	// carried onto the settlement events its native outcomes append.
	PrincipalRef string
	RequestID    string
	// persistPhase, when set by FinishWorktreeNativeRemoval, records each
	// phase the run reaches as it reaches it — the authorization
	// (checkout_proven_absent, restoration_owed) before the native effect
	// it authorizes — through the same SQL-only settlement owner that
	// settles the run's collected outcomes. It is unexported so only the
	// store wires it.
	persistPhase func(WorktreeNativeOutcome) error
}

// recordPhase persists one phase record through the wired sink, if any. A
// nil sink (a direct RunWorktreeNativeRemoval caller) collects outcomes
// only; the caller then settles them itself.
func (r *WorktreeNativeRemoval) recordPhase(outcome WorktreeNativeOutcome) error {
	if r == nil || r.persistPhase == nil {
		return nil
	}
	return r.persistPhase(outcome)
}

// Durable per-ref phases (CD-0212 D3-D4, the worktree_ref_outcomes
// migration in schema.go). One reclamation
// records every retained ref and every deletion it owes as a row in
// worktree_ref_outcomes; the phase column is the one durable owner of the
// row's state. A native step moves a row only along
// worktreeRefPhaseTransitions, every step starts from its recorded
// predecessor phase, and settlement is never inferred from ref absence.
const (
	// WorktreeRefPhasePlanned records a tip-pinned deletion the plan owes.
	WorktreeRefPhasePlanned = "planned"
	// WorktreeRefPhaseCheckoutProvenAbsent records a complete pre-delete
	// observation that proved no worktree holds the ref: the authorization
	// the pinned deletion requires, persisted before the native effect.
	WorktreeRefPhaseCheckoutProvenAbsent = "checkout_proven_absent"
	// WorktreeRefPhaseDeleted records that the pinned deletion ran; the
	// post-deletion observation has not yet settled the row.
	WorktreeRefPhaseDeleted = "deleted"
	// WorktreeRefPhaseRestorationOwed records that an observation around
	// the pinned deletion found, or could not exclude, a checkout naming
	// the ref: the deletion ran or may have run, so the create-only
	// restoration at the immutable pinned tip is owed and durably pending.
	// It stays owed across directory removal, replay, later claims, and
	// audit, and absence never settles it.
	WorktreeRefPhaseRestorationOwed = "restoration_owed"
	// WorktreeRefPhaseRestored records a restoration that rebuilt the ref
	// at its immutable pinned tip for a checkout that holds it. A restored
	// ref remains retained — never deletion-settled by the restore — and
	// stays replayable: the checkout protection tracks a live condition
	// git itself owns, so a later replay re-derives it and converges the
	// pinned deletion once no worktree holds the ref (CD-0212 D4).
	WorktreeRefPhaseRestored = "restored"
	// WorktreeRefPhaseRetainedUnproven records a ref the plan or a native
	// boundary retained because its identity, ownership, or durability was
	// never proven deletable — an unproven live identity, an unobservable
	// inventory, a moved or symbolic ref, the protected default, or a ref
	// another actor already rebuilt. Terminal: replay never rebuilds
	// deletion authority over an unproven identity.
	WorktreeRefPhaseRetainedUnproven = "retained_unproven"
	// WorktreeRefPhaseSettled records a pinned deletion that completed and
	// whose complete post-deletion observation verified no worktree holds
	// the ref.
	WorktreeRefPhaseSettled = "settled"
)

// WorktreeRefStep names one native step of the per-ref phase machine. The
// step set, with worktreeRefPhaseTransitions, is the typed transition table
// the generated phase-pair matrix is derived from.
const (
	// WorktreeRefStepObserveCheckouts re-derives ownership: the complete
	// worktree inventory, the direct ref identity at its pinned tip, and
	// the protected default set, all observed inside the attempt.
	WorktreeRefStepObserveCheckouts = "observe_checkouts"
	// WorktreeRefStepDeletePinned is the pinned argv deletion
	// `update-ref --no-deref -d <ref> <expected-oid>`.
	WorktreeRefStepDeletePinned = "delete_pinned"
	// WorktreeRefStepObservePostDelete reads every listed worktree HEAD
	// again immediately after the deletion.
	WorktreeRefStepObservePostDelete = "observe_post_delete"
	// WorktreeRefStepRestoreExpected is the create-only argv restoration
	// `update-ref <ref> <expected-oid> <zero-oid>`.
	WorktreeRefStepRestoreExpected = "restore_expected"
)

// Typed refusal reasons the phase owner enumerates. Every refusal the
// native boundary returns carries one of these, and the generated matrix
// exercises each one from the phase pair that produces it.
const (
	// WorktreeRefRefusalTerminalPhase: the step cannot run because the row
	// sits at a terminal phase (restored, retained_unproven, settled).
	WorktreeRefRefusalTerminalPhase = "terminal_phase"
	// WorktreeRefRefusalStalePredecessor: the step's recorded predecessor
	// phase is not one the step may start from.
	WorktreeRefRefusalStalePredecessor = "stale_predecessor"
	// WorktreeRefRefusalHolderPresent: a pre-read HEAD names the ref, so
	// the deletion is refused while the row keeps the phase it had.
	WorktreeRefRefusalHolderPresent = "holder_present"
	// WorktreeRefRefusalMovedTip: the ref moved from the tip the plan
	// pinned; the changed identity is retained.
	WorktreeRefRefusalMovedTip = "moved_from_pinned_tip"
	// WorktreeRefRefusalSymbolicRef: the ref became a symbolic ref; a
	// no-deref deletion would still be refused as unproven direct identity.
	WorktreeRefRefusalSymbolicRef = "symbolic_ref"
	// WorktreeRefRefusalProtectedDefault: the ref is the repository's
	// protected default, or the default set could not be established.
	WorktreeRefRefusalProtectedDefault = "protected_default"
	// WorktreeRefRefusalInventoryUnknown: the complete worktree inventory
	// could not be observed; the observation proves nothing.
	WorktreeRefRefusalInventoryUnknown = "inventory_unproven"
)

// worktreeRefPhases is the ordered phase vocabulary of the durable owner.
var worktreeRefPhases = []string{
	WorktreeRefPhasePlanned,
	WorktreeRefPhaseCheckoutProvenAbsent,
	WorktreeRefPhaseDeleted,
	WorktreeRefPhaseRestorationOwed,
	WorktreeRefPhaseRestored,
	WorktreeRefPhaseRetainedUnproven,
	WorktreeRefPhaseSettled,
}

// worktreeRefPhaseTransitions is the owning typed transition table: for
// each native step, the phases it may start from and the phases it may
// record. observe_checkouts re-derives ownership from every pending phase
// — planned, checkout_proven_absent, deleted, restoration_owed, and the
// restored ref whose checkout protection tracks a live condition (a replay
// never trusts an earlier observation): for a ref still present at its
// pinned tip it records the checkout_proven_absent authorization, and for
// a ref observed absent it records the complete-inventory verdict —
// settled when no holder names it, restoration_owed when one does or the
// observation cannot run — because the deletion itself is already moot
// against an absent ref. Identity protections record retained_unproven
// from every pending phase and are terminal. delete_pinned starts only
// from a persisted checkout_proven_absent; observe_post_delete starts only
// from deleted; restore_expected starts only from restoration_owed.
// retained_unproven and settled admit no step, so a recorded settlement
// moving a row between phases the table does not admit — including any
// move out of those terminal phases — refuses. The generated matrix tests
// all 49 ordered phase pairs against this table through the durable
// settlement owner.
var worktreeRefPhaseTransitions = map[string]map[string][]string{
	WorktreeRefStepObserveCheckouts: {
		WorktreeRefPhasePlanned:              {WorktreeRefPhaseCheckoutProvenAbsent, WorktreeRefPhaseSettled, WorktreeRefPhaseRestorationOwed, WorktreeRefPhaseRetainedUnproven},
		WorktreeRefPhaseCheckoutProvenAbsent: {WorktreeRefPhaseCheckoutProvenAbsent, WorktreeRefPhaseSettled, WorktreeRefPhaseRestorationOwed, WorktreeRefPhaseRetainedUnproven},
		WorktreeRefPhaseDeleted:              {WorktreeRefPhaseCheckoutProvenAbsent, WorktreeRefPhaseSettled, WorktreeRefPhaseRestorationOwed, WorktreeRefPhaseRetainedUnproven},
		WorktreeRefPhaseRestorationOwed:      {WorktreeRefPhaseCheckoutProvenAbsent, WorktreeRefPhaseSettled, WorktreeRefPhaseRestorationOwed, WorktreeRefPhaseRetainedUnproven},
		WorktreeRefPhaseRestored:             {WorktreeRefPhaseCheckoutProvenAbsent, WorktreeRefPhaseSettled, WorktreeRefPhaseRestorationOwed, WorktreeRefPhaseRetainedUnproven},
	},
	WorktreeRefStepDeletePinned: {
		WorktreeRefPhaseCheckoutProvenAbsent: {WorktreeRefPhaseDeleted},
	},
	WorktreeRefStepObservePostDelete: {
		WorktreeRefPhaseDeleted: {WorktreeRefPhaseSettled, WorktreeRefPhaseRestorationOwed, WorktreeRefPhaseRetainedUnproven},
	},
	WorktreeRefStepRestoreExpected: {
		WorktreeRefPhaseRestorationOwed: {WorktreeRefPhaseRestored, WorktreeRefPhaseRetainedUnproven},
	},
}

// validWorktreeRefPhase reports whether phase is one of the seven phases
// the durable owner admits.
func validWorktreeRefPhase(phase string) bool {
	for _, candidate := range worktreeRefPhases {
		if candidate == phase {
			return true
		}
	}
	return false
}

// terminalRefPhase reports whether no native step may start from phase.
// An unproven or settled identity is terminal: replay never rebuilds
// deletion authority over an unproven identity, and a settled deletion is
// complete. A restored ref is not terminal — its protection tracks a live
// checkout, which a later replay re-derives.
func terminalRefPhase(phase string) bool {
	switch phase {
	case WorktreeRefPhaseRetainedUnproven, WorktreeRefPhaseSettled:
		return true
	}
	return false
}

// worktreeRefStepRefusal is the typed refusal for running step from
// fromPhase: the enumerated reason, or "" when the table permits the step.
func worktreeRefStepRefusal(step, fromPhase string) string {
	if !validWorktreeRefPhase(fromPhase) {
		return WorktreeRefRefusalStalePredecessor
	}
	if terminalRefPhase(fromPhase) {
		return WorktreeRefRefusalTerminalPhase
	}
	if _, ok := worktreeRefPhaseTransitions[step][fromPhase]; !ok {
		return WorktreeRefRefusalStalePredecessor
	}
	return ""
}

// worktreeRefStepTarget reports whether the owner table admits step moving
// a row from fromPhase to toPhase.
func worktreeRefStepTarget(step, fromPhase, toPhase string) bool {
	for _, target := range worktreeRefPhaseTransitions[step][fromPhase] {
		if target == toPhase {
			return true
		}
	}
	return false
}

// refPhaseTransitionAdmitted reports whether any step of the owning
// transition table admits moving a row from fromPhase to toPhase. The
// settlement fold validates a recorded phase move against this union: the
// fold carries no step of its own, so a move the whole table refuses is a
// settlement the durable owner never authorized — a stale predecessor, a
// jump over a required intermediate, or a move out of a terminal phase.
func refPhaseTransitionAdmitted(fromPhase, toPhase string) bool {
	for _, targets := range worktreeRefPhaseTransitions {
		for _, target := range targets[fromPhase] {
			if target == toPhase {
				return true
			}
		}
	}
	return false
}

// WorktreeNativeOutcome is one bounded per-item result the native removal
// produced: one phase the row moved to (or was refused at), with the tip
// observed and the bounded reason. The store persists each phase record
// durably as the run reaches it — the authorization before its native
// effect — in SQL-only settlements keyed by the claim generation.
type WorktreeNativeOutcome struct {
	Branch string `json:"branch"`
	Tip    string `json:"tip,omitempty"`
	Phase  string `json:"phase"`
	Reason string `json:"reason"`
	// Refusal carries the enumerated refusal reason when the outcome is a
	// typed refusal that kept the row at its recorded phase.
	Refusal string `json:"refusal,omitempty"`
}

// WorktreeBranchDeletion is one branch deletion the native removal owes. The
// expected tip is pinned at probe time and revalidated before the deletion
// runs, so a ref that moved after the plan was recorded survives.
type WorktreeBranchDeletion struct {
	Branch      string `json:"branch"`
	ExpectedTip string `json:"expected_tip"`
	Reason      string `json:"reason"`
	// Phase is the durable phase the owner last recorded for this branch.
	// A replay carries it so each native step starts from its recorded
	// predecessor; a fresh plan leaves it empty and the run starts at
	// planned.
	Phase string `json:"phase,omitempty"`
}

// WorktreeRetainedRef names a branch ref a reclamation proved it must not
// delete, with the tip observed when the reclaim retained it and the bounded
// reason. Retentions are recorded in the reclamation facts, so they survive
// directory removal and stay visible to the audit (CD-0212 D3).
type WorktreeRetainedRef struct {
	Branch string `json:"branch"`
	Tip    string `json:"tip,omitempty"`
	Reason string `json:"reason"`
}

// WorktreeReclaimResult reports a reclaim or destroy whose event committed on
// the caller's transaction. Removal is the native work the caller still owes
// after its own commit; nil means nothing native remains.
type WorktreeReclaimResult struct {
	Entry   WorktreeEntry
	Removal *WorktreeNativeRemoval
}

// WorktreeReclaimProbe carries the git facts one reclaim probed with no
// transaction open, plus the entry and repository root the probes ran
// against. The transaction re-validates the entry identity against the
// probed entry before it appends the reclamation event (CD-0195 D2).
type WorktreeReclaimProbe struct {
	Entry WorktreeEntry
	// RepoRoot is the canonical repository the probes ran against.
	RepoRoot string
	// AlreadyAbsent records that the native worktree is already gone, so the
	// event records that fact and no removal is owed.
	AlreadyAbsent bool
	// AlreadyConverged records a committed reclaim whose reclamation event is
	// the authority: the retry answers from the folded projection with no
	// new event, converging the deletions its recorded plan still owes.
	AlreadyConverged bool
	// DirectoryPending records a committed reclaim whose directory phase
	// failed: the checkout is still on disk, and the retry re-owes only its
	// removal under the recorded plan (CD-0212 D4). The recorded plan's
	// live-HEAD identity — never a fresh observation — authorizes the retry,
	// so a surviving directory whose content changed refuses instead of
	// repinning.
	DirectoryPending bool
	// ReplayForce records the force flag the committed plan carries, so a
	// replay of an approved forced removal keeps its recorded authority and
	// a replay of a safe removal stays safe whatever the retry declares.
	ReplayForce bool
	// LiveHead is the one immutable live-HEAD observation every content gate
	// read (CD-0212 D1): the checked-out branch or the detached tip, the
	// tip SHA, and the clean-tree fact.
	LiveHead worktreeLiveHead
	// DefaultRef is the canonical default endpoint the gates compared
	// against, resolved even when the caller named none.
	DefaultRef string
	// DurableVia records how the live content passed its gate:
	// remote_reachable, squash_merged (CD-0181), or unstarted.
	DurableVia string
	// Deletions are the proven, tip-pinned branch deletions the native
	// removal owes. On a converged retry they hold the pending deletions
	// re-derived from the recorded plan.
	Deletions []WorktreeBranchDeletion
	// Retained are the branch refs the reclaim proved it must not delete,
	// recorded in the reclamation facts.
	Retained []WorktreeRetainedRef
	// StoredRefAbsent records that the stored claim ref no longer exists, so
	// the reclamation owes no deletion for it.
	StoredRefAbsent bool
	// Facts are the git facts the reclamation event records. They are
	// composed from the fields above; a caller must not hand-edit them.
	Facts json.RawMessage
}

// WorktreeDestroyRequest drives the CD-0096 D3 Destroy tier: merged terminal
// work reclaims under the unchanged CD-0095 git gates; non-terminal work and
// any destructive removal refuse typed without a consumed operator approval.
type WorktreeDestroyRequest struct {
	WorkID          string
	ProjectID       string
	DefaultRef      string
	ExpectedVersion int64
	// OperatorApprovalRef names the consumed operator approval. Empty means
	// the safe path only.
	OperatorApprovalRef string
	// Destructive declares that the approval also covers discarding the git
	// safety gates.
	Destructive      bool
	ReleaseOccupancy bool
	PrincipalRef     string
	RequestID        string
	Now              time.Time
	Runner           GitRunner
}

// DestroyWorktree reclaims the work item's worktree under the Destroy tier's
// authority gates. The git probes run with no transaction open, the
// transaction validates and commits the reclamation event, and the native
// removal follows the commit (CD-0195 D2).
func (s *Store) DestroyWorktree(ctx context.Context, req WorktreeDestroyRequest) (WorktreeEntry, error) {
	reclaimReq := WorktreeReclaimRequest{
		WorkID: req.WorkID, ProjectID: req.ProjectID, DefaultRef: req.DefaultRef,
		PrincipalRef: req.PrincipalRef, RequestID: req.RequestID,
		ExpectedVersion: req.ExpectedVersion, Now: req.Now, Runner: req.Runner,
		RequireTerminal: true, OperatorApprovalRef: req.OperatorApprovalRef, Destructive: req.Destructive,
		ReleaseOccupancy: req.ReleaseOccupancy,
	}
	if s == nil || s.db == nil {
		return WorktreeEntry{}, newFailure(KindUnavailable, "worktree_destroy", "store is not open", false, "open the authority database")
	}
	// The lease set is read before the transaction opens: the legacy-row
	// release proof compares it against the row's recorded_at (CD-0179), and
	// a filesystem walk belongs outside the write transaction.
	reclaimReq.HostLeases = s.ReadHostLeases()
	runner := reclaimReq.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	probe, err := s.PrepareWorktreeReclaim(ctx, reclaimReq)
	if err != nil {
		return WorktreeEntry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorktreeEntry{}, wrapFailure(KindUnavailable, "worktree_destroy", "cannot begin destroy", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	entry, removal, err := reclaimWorktreeStoreTx(ctx, tx, reclaimReq, probe)
	if err != nil {
		return WorktreeEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorktreeEntry{}, wrapFailure(KindUnavailable, "worktree_destroy", "cannot commit destroy", true, "retry the same operation", err)
	}
	// The event is committed; the native removal follows it with no
	// transaction open. A retry of the same request converges a committed
	// reclaim whose native removal failed (CD-0195 D2), and the per-ref
	// outcomes the run produced settle durably behind it.
	if err := s.FinishWorktreeNativeRemoval(ctx, runner, removal); err != nil {
		return entry, err
	}
	return entry, nil
}

// DestroyWorktreeTx is the destroy on an existing transaction, so the agent
// tool surface can compose it with its idempotency envelope. The caller
// probes with PrepareWorktreeReclaim before its transaction opens and runs
// the returned removal after its commit.
func DestroyWorktreeTx(ctx context.Context, transaction *Transaction, req WorktreeReclaimRequest, probe WorktreeReclaimProbe) (WorktreeReclaimResult, error) {
	tx, err := transactionSQL(transaction, "worktree_destroy")
	if err != nil {
		return WorktreeReclaimResult{}, err
	}
	if req.Now.IsZero() {
		req.Now = transaction.now()
	}
	entry, removal, err := reclaimWorktreeStoreTx(ctx, tx, req, probe)
	return WorktreeReclaimResult{Entry: entry, Removal: removal}, err
}

// ReclaimWorktree reclaims the worktree on its own transaction (CD-0095).
// The git probes run with no transaction open, the transaction validates and
// commits the reclamation event, and the native removal follows the commit
// (CD-0195 D2).
func (s *Store) ReclaimWorktree(ctx context.Context, req WorktreeReclaimRequest) (WorktreeEntry, error) {
	if s == nil || s.db == nil {
		return WorktreeEntry{}, newFailure(KindUnavailable, "worktree_reclaim", "store is not open", false, "open the authority database")
	}
	// The lease set is read before the transaction opens: the legacy-row
	// release proof compares it against the row's recorded_at (CD-0179), and
	// a filesystem walk belongs outside the write transaction. A caller that
	// carries its own observation keeps it.
	if req.HostLeases.Leases == nil && req.HostLeases.Err == nil {
		req.HostLeases = s.ReadHostLeases()
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	probe, err := s.PrepareWorktreeReclaim(ctx, req)
	if err != nil {
		return WorktreeEntry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorktreeEntry{}, wrapFailure(KindUnavailable, "worktree_reclaim", "cannot begin reclaim", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	entry, removal, err := reclaimWorktreeStoreTx(ctx, tx, req, probe)
	if err != nil {
		return WorktreeEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorktreeEntry{}, wrapFailure(KindUnavailable, "worktree_reclaim", "cannot commit reclaim", true, "retry the same operation", err)
	}
	// The event is committed; the native removal follows it with no
	// transaction open. A retry of the same request converges a committed
	// reclaim whose native removal failed (CD-0195 D2), and the per-ref
	// outcomes the run produced settle durably behind it.
	if err := s.FinishWorktreeNativeRemoval(ctx, runner, removal); err != nil {
		return entry, err
	}
	return entry, nil
}

// FinishWorktreeNativeRemoval runs the bounded native removal a committed
// reclamation owes, then settles every per-ref outcome the run produced
// through one SQL-only transaction keyed by the claim generation (CD-0212
// D4). The settlement is idempotent, so a replay converges, and it appends
// no work-item version advance: the reclamation event already owns that.
// A removal without settlement identity, or a run that produced no
// outcomes, settles nothing.
func (s *Store) FinishWorktreeNativeRemoval(ctx context.Context, runner GitRunner, removal *WorktreeNativeRemoval) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "worktree_native_removal", "store is not open", false, "open the authority database")
	}
	// The phase records persist as the run reaches them — the authorized
	// predecessor before the native effect it authorizes — and the
	// collected outcomes settle again after the run through the same
	// idempotent owner, converging anything a failed inline record left.
	if removal != nil {
		removal.persistPhase = func(outcome WorktreeNativeOutcome) error {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return wrapFailure(KindUnavailable, "worktree_native_removal", "cannot begin removal phase record", true, "retry the same operation", err)
			}
			if err := settleWorktreeRefOutcomeTx(ctx, tx, removal, outcome); err != nil {
				tx.Rollback()
				return err
			}
			if err := tx.Commit(); err != nil {
				return wrapFailure(KindUnavailable, "worktree_native_removal", "cannot commit removal phase record", true, "retry the same operation", err)
			}
			return nil
		}
	}
	outcomes, runErr := RunWorktreeNativeRemoval(ctx, runner, removal)
	settleErr := s.settleWorktreeNativeOutcomes(ctx, removal, outcomes)
	if runErr != nil {
		return runErr
	}
	return settleErr
}

// settleWorktreeNativeOutcomes appends one settlement event per bounded
// outcome the native run produced, in one SQL-only transaction. Each event
// identity is deterministic in the claim generation, the branch, and the
// outcome kind, so a replay of the same outcome converges instead of
// appending a second record.
func (s *Store) settleWorktreeNativeOutcomes(ctx context.Context, removal *WorktreeNativeRemoval, outcomes []WorktreeNativeOutcome) error {
	if removal == nil || len(outcomes) == 0 || removal.WorkID == "" || removal.ProjectID == "" || removal.ClaimOpID == "" {
		return nil
	}
	for _, outcome := range outcomes {
		if outcome.Branch == "" || outcome.Phase == "" {
			continue
		}
		if !validWorktreeRefPhase(outcome.Phase) {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return wrapFailure(KindUnavailable, "worktree_native_removal", "cannot begin removal settlement", true, "retry the same operation", err)
		}
		if err := settleWorktreeRefOutcomeTx(ctx, tx, removal, outcome); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return wrapFailure(KindUnavailable, "worktree_native_removal", "cannot commit removal settlement", true, "retry the same operation", err)
		}
	}
	return nil
}

// settleWorktreeRefOutcomeTx records one per-ref native outcome through the
// durable owner. The row is fold state of the settlement log: a settlement
// the row already holds — same phase, tip, and reason — converges with no
// new event and no mutation, and every other record is one fresh
// chronological settlement event whose identity derives from the durable
// per-ref predecessor and the event sequence the log holds, appended and
// folded atomically in this SQL-only transaction. A move the transition
// table refuses from the row's current phase refuses typed before anything
// is appended, so a phase some earlier attempt recorded — or a stale probe
// carried — never authorizes an effect against a different predecessor
// (CD-0195 D2, CD-0212 D4). An event-id collision takes the ordinary
// append conflict path: an identical payload is the durable effect already
// recorded, a different one refuses typed.
func settleWorktreeRefOutcomeTx(ctx context.Context, tx *sql.Tx, removal *WorktreeNativeRemoval, outcome WorktreeNativeOutcome) error {
	setID := WorktreeSetID(removal.WorkID)
	settlement := worktreeRemovalSettledPayload{
		SetID: setID, ProjectID: removal.ProjectID, ClaimOpID: removal.ClaimOpID, ClaimIncarnation: removal.ClaimIncarnation,
		Branch: outcome.Branch, Tip: outcome.Tip, Phase: outcome.Phase, Reason: outcome.Reason,
	}
	if settlement.Reason == "" {
		settlement.Reason = "settled by the native removal"
	}
	var currentPhase, currentTip, currentReason sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT phase,tip,reason FROM worktree_ref_outcomes WHERE set_id=? AND project_id=? AND claim_op_id=? AND claim_incarnation=? AND branch=?`,
		settlement.SetID, settlement.ProjectID, settlement.ClaimOpID, settlement.ClaimIncarnation, settlement.Branch).Scan(&currentPhase, &currentTip, &currentReason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if currentPhase.Valid && currentPhase.String == settlement.Phase && currentTip.String == settlement.Tip && currentReason.String == settlement.Reason {
		return nil
	}
	if currentPhase.Valid && currentPhase.String != settlement.Phase && !refPhaseTransitionAdmitted(currentPhase.String, settlement.Phase) {
		return newFailure(KindProjectionConflict, "worktree_native_removal",
			"the durable ref phase owner refuses to move branch "+outcome.Branch+" from "+currentPhase.String+" to "+settlement.Phase+": no recorded step admits that transition",
			false, "settle the branch through the predecessor phase its recorded plan admits")
	}
	fromPhase := ""
	if currentPhase.Valid {
		fromPhase = currentPhase.String
	}
	base := worktreeRefSettlementEventBase(removal.WorkID, removal.ProjectID, removal.ClaimOpID, outcome.Branch)
	sequence, err := worktreeRefSettlementSequence(ctx, tx, base)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(settlement)
	_, err = applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: settledRefOutcomeEventID(base, fromPhase, settlement.Phase, sequence, removal.ClaimIncarnation),
		Kind:    "work.worktree_removal_settled", SubjectType: SubjectWorkItem, SubjectID: removal.WorkID,
		Actor: removal.PrincipalRef, OccurredAt: nowFromClock(nil), PayloadVersion: 1, Payload: payload,
	}}}, newFoldScope(tx), false)
	return err
}

// worktreeRefSettlementEventBase derives the one per-ref settlement event
// identity prefix from the raw work identity the event records: the claim
// generation and the branch every settlement event for this ref shares,
// whatever phase it holds. The same owner derives the invocation sequence
// and the event identity, so a count can never range over keys the append
// never wrote.
func worktreeRefSettlementEventBase(workID, projectID, claimOpID, branch string) string {
	return fmt.Sprintf("%s:%s:%s:worktree-removal-settled:%s:", workID, projectID, claimOpID, branch)
}

// worktreeRefSettlementSequence derives the next per-ref settlement
// invocation from the durable log: the count of settlement events this
// claim generation's branch already holds.
func worktreeRefSettlementSequence(ctx context.Context, tx *sql.Tx, base string) (int, error) {
	var sequence int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM domain_events WHERE event_id >= ? AND event_id < ?`, base, base+"\xff").Scan(&sequence); err != nil {
		return 0, wrapFailure(KindUnavailable, "worktree_native_removal", "cannot derive the settlement sequence", true, "retry the same operation", err)
	}
	return sequence, nil
}

// settledRefOutcomeEventID derives one settlement transition's identity:
// the per-ref base, the durable predecessor the transition starts from,
// the phase it records, and the per-ref invocation sequence, scoped to the
// claim incarnation like every other worktree event identity.
func settledRefOutcomeEventID(base, fromPhase, toPhase string, sequence int, incarnation int) string {
	return claimIncarnationEventID(fmt.Sprintf("%s%s>%s:%d", base, fromPhase, toPhase, sequence), incarnation)
}

// ReclaimWorktreeTx is the reclaim on an existing transaction, so the agent
// tool surface can compose it with its idempotency envelope. The caller
// probes with PrepareWorktreeReclaim before its transaction opens and runs
// the returned removal after its commit.
func ReclaimWorktreeTx(ctx context.Context, transaction *Transaction, req WorktreeReclaimRequest, probe WorktreeReclaimProbe) (WorktreeReclaimResult, error) {
	tx, err := transactionSQL(transaction, "worktree_reclaim")
	if err != nil {
		return WorktreeReclaimResult{}, err
	}
	if req.Now.IsZero() {
		req.Now = transaction.now()
	}
	entry, removal, err := reclaimWorktreeStoreTx(ctx, tx, req, probe)
	return WorktreeReclaimResult{Entry: entry, Removal: removal}, err
}

// PrepareWorktreeReclaim derives the reclaim's git facts with no transaction
// open (CD-0195 D2): it reads the entry and repository root, probes the
// native worktree, and refuses on every git-side gate before any store write
// is attempted. Callers pass the probe to ReclaimWorktreeTx or
// DestroyWorktreeTx and re-validate the store state inside their own
// transaction.
func (s *Store) PrepareWorktreeReclaim(ctx context.Context, req WorktreeReclaimRequest) (WorktreeReclaimProbe, error) {
	if err := validateWorktreeReclaimIdentity(req); err != nil {
		return WorktreeReclaimProbe{}, err
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	op := reclaimOperation(req)
	if err := worktreeProjectMember(ctx, s.db, req.WorkID, req.ProjectID, op); err != nil {
		return WorktreeReclaimProbe{}, err
	}
	entries, err := worktreeEntriesCore(ctx, s.db, req.WorkID)
	if err != nil {
		return WorktreeReclaimProbe{}, err
	}
	var entry WorktreeEntry
	for _, candidate := range entries {
		if candidate.ProjectID == req.ProjectID {
			entry = candidate
			break
		}
	}
	if entry.ProjectID == "" || (entry.State != worktreeEntryActive && entry.State != worktreeEntryReclaimed) {
		return WorktreeReclaimProbe{}, newFailure(KindProjectionNotFound, op, "no active worktree for this Project", false, "claim a worktree before reclaiming it")
	}
	repoRoot, err := worktreeRepoRootTx(ctx, s.db, WorktreeClaimRequest{ProjectID: req.ProjectID})
	if err != nil {
		return WorktreeReclaimProbe{}, err
	}
	// The durable per-ref outcome rows of this claim generation retire the
	// recorded deletions already settled or protected, so a replay never
	// re-attempts what a prior native run finished or protected (CD-0212 D4).
	priorOutcomes, err := worktreeRefOutcomeRows(ctx, s.db, WorktreeSetID(req.WorkID), req.ProjectID, entry.ClaimOpID)
	if err != nil {
		return WorktreeReclaimProbe{}, err
	}
	return probeWorktreeReclaim(ctx, runner, req, op, entry, repoRoot, priorOutcomes)
}

// worktreeRefOutcomeRows reads the durable per-ref outcome rows one claim
// generation holds.
func worktreeRefOutcomeRows(ctx context.Context, q queryer, setID, projectID, claimOpID string) ([]worktreeRefOutcome, error) {
	rows, err := q.QueryContext(ctx, `SELECT branch,phase,tip,reason FROM worktree_ref_outcomes WHERE set_id=? AND project_id=? AND claim_op_id=? ORDER BY branch`, setID, projectID, claimOpID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worktree_reclaim", "cannot read recorded ref outcomes", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []worktreeRefOutcome
	for rows.Next() {
		var row worktreeRefOutcome
		if err := rows.Scan(&row.branch, &row.phase, &row.tip, &row.reason); err != nil {
			return nil, err
		}
		row.setID, row.projectID, row.claimOpID = setID, projectID, claimOpID
		out = append(out, row)
	}
	return out, rows.Err()
}

// worktreeLiveHead is the one immutable live-HEAD content observation every
// reclaim, destroy, and audit gate reads (CD-0212 D1): the branch the live
// worktree has checked out (empty when detached), the live tip SHA, and the
// clean-tree fact. The observation is captured once per operation, before
// any effect, so every gate consumes the same head and no gate can drift to
// a different one mid-operation.
type worktreeLiveHead struct {
	Branch   string
	Detached bool
	Tip      string
	Clean    bool
}

// committish names the git revision the content gates read for this
// observation: the observed tip, always. A branch name is mutable — the ref
// can move between the observation and any later probe — so a gate that read
// the name could measure content the worktree no longer holds. The observed
// tip is the immutable half of the observation; label carries the branch
// name for diagnostics. A durable stored branch is not proof that a
// different or detached live HEAD is durable, so the gates run against this
// revision.
func (h worktreeLiveHead) committish() string {
	return h.Tip
}

// label names the live head in bounded refusal details and facts.
func (h worktreeLiveHead) label() string {
	if h.Detached {
		return "detached HEAD " + h.Tip
	}
	return "live branch " + h.Branch
}

// liveHeadFacts is the recorded shape of one live-HEAD observation.
type liveHeadFacts struct {
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Tip      string `json:"tip"`
}

// worktreeReclaimFacts is the bounded fact payload a reclamation event
// records: the live-HEAD observation, the gate the live content passed, the
// stored claim branch when the checkout drifted from it, the retained refs,
// and the bounded native-removal plan whose branch deletions a retry must
// still converge (CD-0212 D3-D4).
type worktreeReclaimFacts struct {
	CleanTree        bool                     `json:"clean_tree,omitempty"`
	DefaultRef       string                   `json:"default_ref,omitempty"`
	StoredBranch     string                   `json:"stored_branch,omitempty"`
	StoredRefAbsent  bool                     `json:"stored_ref_absent,omitempty"`
	AlreadyAbsent    bool                     `json:"already_absent,omitempty"`
	Forced           bool                     `json:"forced,omitempty"`
	OperatorOverride string                   `json:"operator_override,omitempty"`
	LiveHead         *liveHeadFacts           `json:"live_head,omitempty"`
	SquashMerged     bool                     `json:"squash_merged,omitempty"`
	RemoteReachable  bool                     `json:"remote_reachable,omitempty"`
	CommitsBeyond    int                      `json:"commits_beyond,omitempty"`
	RetainedRefs     []WorktreeRetainedRef    `json:"retained_refs,omitempty"`
	BranchDeletions  []WorktreeBranchDeletion `json:"branch_deletions,omitempty"`
}

// worktreeRefOutcome is one durable per-ref outcome row the reclamation
// projection holds (CD-0212 D3-D4): a retention, an owed deletion, a settled
// deletion, or a protection. The rows key on the claim generation, so a later
// claim cannot erase an earlier generation's retentions or deletion debt.
type worktreeRefOutcome struct {
	setID            string
	projectID        string
	claimOpID        string
	claimIncarnation int
	branch           string
	phase            string
	tip              string
	reason           string
	path             string
	recordedAt       string
}

// recordedRemovalPlan is the bounded native-removal plan decoded from the
// facts a committed reclamation recorded. One decoded plan drives both the
// first native execution and every replay.
type recordedRemovalPlan struct {
	Forced          bool                     `json:"forced"`
	AlreadyAbsent   bool                     `json:"already_absent"`
	DefaultRef      string                   `json:"default_ref"`
	LiveHead        *liveHeadFacts           `json:"live_head"`
	BranchDeletions []WorktreeBranchDeletion `json:"branch_deletions"`
	RetainedRefs    []WorktreeRetainedRef    `json:"retained_refs"`
}

// decodeRecordedRemovalPlan decodes the plan a committed reclamation recorded.
// Empty or legacy facts decode to a zero plan: nothing is owed and nothing is
// authorized. Unparsable facts refuse typed, because a retry cannot honor a
// plan it cannot read.
func decodeRecordedRemovalPlan(facts json.RawMessage, op string) (recordedRemovalPlan, error) {
	var plan recordedRemovalPlan
	if len(facts) == 0 {
		return plan, nil
	}
	if err := json.Unmarshal(facts, &plan); err != nil {
		return recordedRemovalPlan{}, newFailure(KindInvalidPayload, op, "recorded reclaim facts are unparsable", false, "repair the recorded reclamation facts")
	}
	return plan, nil
}

// probeWorktreeReclaim runs the reclaim's git probes with no transaction
// open. Every refusal here is git-derived and leaves no store effect.
func probeWorktreeReclaim(ctx context.Context, runner GitRunner, req WorktreeReclaimRequest, op string, entry WorktreeEntry, repoRoot string, priorOutcomes []worktreeRefOutcome) (WorktreeReclaimProbe, error) {
	probe := WorktreeReclaimProbe{Entry: entry, RepoRoot: repoRoot}
	// A committed reclaim whose native removal also completed answers from
	// the folded projection; a surviving directory falls through to the
	// normal gates, which is what converges a failed removal on retry.
	branchOut, probeErr := runner.Run(ctx, entry.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if entry.State == worktreeEntryReclaimed {
		// The reclamation event is already durable, so its recorded plan is
		// the authority a retry replays (CD-0212 D4). The pinned deletions
		// re-derive from the recorded facts — never a fresh plan — and the
		// durable outcome rows retire the deletions already settled or
		// protected. A directory phase that failed re-owes only the removal,
		// under the live-HEAD identity the plan recorded: a surviving
		// directory whose content changed refuses instead of repinning, and
		// the recorded force flag replays without the retry's own authority.
		plan, planErr := decodeRecordedRemovalPlan(entry.GitFacts, op)
		if planErr != nil {
			return WorktreeReclaimProbe{}, planErr
		}
		probe.AlreadyConverged = true
		probe.Deletions = pendingRecordedDeletions(plan.BranchDeletions, priorOutcomes)
		probe.ReplayForce = plan.Forced
		// The recorded default spelling — never the retry's own — feeds the
		// boundary's default-ownership revalidation when the plan still owes
		// deletions (CD-0212 D4).
		probe.DefaultRef = plan.DefaultRef
		if probeErr == nil {
			// The directory survived a failed removal phase. Its current
			// live HEAD must still match the identity the plan recorded: a
			// freshly observed tip authorizes nothing, so content that
			// arrived after the committed plan keeps the refusal boundary.
			current := worktreeLiveHead{Branch: strings.TrimSpace(string(branchOut))}
			if current.Branch == "HEAD" {
				current.Detached, current.Branch = true, ""
			}
			tipOut, tipErr := runner.Run(ctx, entry.Path, "rev-parse", "HEAD")
			if tipErr != nil {
				return WorktreeReclaimProbe{}, wrapFailure(KindGitUnreachable, op, "cannot read the live HEAD tip of the worktree", true, "retry once the worktree is reachable", tipErr)
			}
			current.Tip = strings.TrimSpace(string(tipOut))
			recorded := worktreeLiveHead{}
			if plan.LiveHead != nil {
				recorded = worktreeLiveHead{Branch: plan.LiveHead.Branch, Detached: plan.LiveHead.Detached, Tip: plan.LiveHead.Tip}
			}
			if current.Tip == "" || recorded.Tip == "" || current.Tip != recorded.Tip || current.Detached != recorded.Detached || current.Branch != recorded.Branch {
				return WorktreeReclaimProbe{}, newFailure(KindProjectionConflict, op,
					fmt.Sprintf("the surviving worktree changed after the recorded removal plan (recorded %s, now %s); the retry refuses to remove unproven content", recorded.label(), current.label()),
					false, "inspect the worktree content and reclaim it through its gates once accounted for")
			}
			probe.LiveHead = recorded
			probe.DirectoryPending = true
		}
		return probe, nil
	}
	if probeErr != nil {
		probe.AlreadyAbsent = true
		probe.Facts = jsonMustMarshal(worktreeReclaimFacts{AlreadyAbsent: true, Forced: req.Destructive, OperatorOverride: approvalFactRef(req)})
		return probe, nil
	}
	// One immutable live-HEAD observation feeds every gate below: the branch
	// (or detached marker), the tip, and the clean-tree fact.
	live := worktreeLiveHead{Branch: strings.TrimSpace(string(branchOut))}
	if live.Branch == "HEAD" {
		live.Detached, live.Branch = true, ""
	}
	tipOut, tipErr := runner.Run(ctx, entry.Path, "rev-parse", "HEAD")
	if tipErr != nil {
		return probe, wrapFailure(KindGitUnreachable, op, "cannot read the live HEAD tip of the worktree", true, "retry once the worktree is reachable", tipErr)
	}
	live.Tip = strings.TrimSpace(string(tipOut))
	probe.LiveHead = live
	// A destructive removal runs under its consumed operator approval: the
	// clean-tree and durable-branch gates are skipped and the native remove
	// is forced (CD-0096 D3 Destroy). Identity pinning still applies, and a
	// stored ref is still proven and pinned before any deletion.
	deletions, retained, planErr := planStoredRefDeletion(ctx, runner, req, op, entry, repoRoot, live, true, "")
	if planErr != nil {
		return WorktreeReclaimProbe{}, planErr
	}
	probe.Deletions, probe.Retained = deletions, retained
	if req.Destructive {
		probe.Facts = probe.reclaimFacts(req)
		return probe, nil
	}
	statusOut, statusErr := runner.Run(ctx, entry.Path, "status", "--porcelain")
	if statusErr != nil {
		return probe, wrapFailure(KindGitUnreachable, op, "cannot read worktree status", true, "retry once the worktree is reachable", statusErr)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		recovery := "commit or discard the changes before reclaiming"
		if req.RequireTerminal {
			recovery = "commit or discard the changes, or obtain an operator-approved destructive destroy"
		}
		return probe, newFailure(KindInvalidOperation, op, "worktree tree is dirty", false, recovery)
	}
	live.Clean = true
	probe.LiveHead = live
	// Every non-destructive reclaim compares the live head against a
	// canonical default endpoint: the RequireUnstarted commit count, the
	// durability check, and the unpublished-lesson gate all read it. Derive
	// the endpoint before any effect, and refuse a repository that cannot
	// name its default branch instead of reclaiming with a skipped
	// comparison.
	defaultRef := req.DefaultRef
	if defaultRef == "" {
		refOut, refErr := runner.Run(ctx, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD")
		if refErr != nil || strings.TrimSpace(string(refOut)) == "" {
			return probe, newFailure(KindGitUnreachable, op, "cannot resolve the default branch", false, "set origin/HEAD or supply the merge target ref")
		}
		defaultRef = strings.TrimPrefix(strings.TrimSpace(string(refOut)), "refs/remotes/")
	}
	probe.DefaultRef = defaultRef
	// The content gates read the live head's revision, never the stored
	// claim branch alone: the checkout, not the claim row, is the content a
	// removal would lose (CD-0212 D1-D2).
	if req.RequireUnstarted {
		if err := branchHasNoCommitsBeyond(ctx, runner, repoRoot, live.committish(), live.label(), defaultRef, op); err != nil {
			return probe, err
		}
		probe.DurableVia = "unstarted"
	} else {
		contained, durableErr := branchIsDurable(ctx, runner, repoRoot, live.committish(), live.label(), defaultRef, op)
		if durableErr != nil {
			return probe, durableErr
		}
		if contained {
			probe.DurableVia = "squash_merged"
		} else {
			probe.DurableVia = "remote_reachable"
		}
	}
	// A branch whose lesson records the default ref does not hold is
	// prepared delivery the reclaim would delete. The gate runs after
	// durability, so a pushed branch refuses exactly like an unpushed one,
	// and a merged lesson (the shard exists on the default endpoint) never
	// blocks a reclaim.
	lessons, lessonErr := branchUnpublishedLessonRecords(ctx, runner, repoRoot, live.committish(), defaultRef, op)
	if lessonErr != nil {
		return probe, lessonErr
	}
	if len(lessons) > 0 {
		return probe, newFailure(KindWorktreeUnpublishedLesson, op, worktreeUnpublishedLessonDetail(live.label(), len(lessons), lessons, defaultRef), false, "merge the branch's pull request or supersede the lesson before reclaiming")
	}
	// Directory removal is proven; the stored claim ref's deletion is a
	// separate decision with its own proof (CD-0212 D3).
	deletions, retained, planErr = planStoredRefDeletion(ctx, runner, req, op, entry, repoRoot, live, false, probe.DurableVia)
	if planErr != nil {
		return WorktreeReclaimProbe{}, planErr
	}
	probe.Deletions, probe.Retained = deletions, retained
	probe.StoredRefAbsent = storedRefAbsent(deletions, retained, entry)
	probe.Facts = probe.reclaimFacts(req)
	return probe, nil
}

// approvalFactRef returns the operator approval reference a forced fact
// records, empty on the safe path.
func approvalFactRef(req WorktreeReclaimRequest) string {
	if req.Destructive {
		return req.OperatorApprovalRef
	}
	return ""
}

// storedRefAbsent reports whether the stored claim ref was observed missing:
// no deletion was planned and no retention was recorded for it.
func storedRefAbsent(deletions []WorktreeBranchDeletion, retained []WorktreeRetainedRef, entry WorktreeEntry) bool {
	for _, d := range deletions {
		if d.Branch == entry.Branch {
			return false
		}
	}
	for _, r := range retained {
		if r.Branch == entry.Branch {
			return false
		}
	}
	return true
}

// reclaimFacts composes the bounded facts the reclamation event records from
// the probe's observations and plan. The composition is the probe's own, so
// a caller cannot record a plan its gates did not prove.
func (p WorktreeReclaimProbe) reclaimFacts(req WorktreeReclaimRequest) json.RawMessage {
	facts := worktreeReclaimFacts{
		DefaultRef:       p.DefaultRef,
		StoredRefAbsent:  p.StoredRefAbsent,
		AlreadyAbsent:    p.AlreadyAbsent,
		Forced:           req.Destructive,
		OperatorOverride: approvalFactRef(req),
		RetainedRefs:     p.Retained,
		BranchDeletions:  p.Deletions,
	}
	if !p.AlreadyAbsent && !req.Destructive && p.LiveHead.Tip != "" {
		facts.CleanTree = p.LiveHead.Clean
	}
	if p.LiveHead.Tip != "" {
		facts.LiveHead = &liveHeadFacts{Branch: p.LiveHead.Branch, Detached: p.LiveHead.Detached, Tip: p.LiveHead.Tip}
	}
	if p.LiveHead.Branch != p.Entry.Branch {
		facts.StoredBranch = p.Entry.Branch
	}
	switch p.DurableVia {
	case "squash_merged":
		facts.SquashMerged = true
	case "remote_reachable":
		facts.RemoteReachable = true
	case "unstarted":
		facts.CommitsBeyond = 0
	}
	return jsonMustMarshal(facts)
}

// factsFor composes the reclamation facts the event records from the probe's
// own observations and the request that will actually commit. The agent
// surface captures its probe before the approval tail supplies the consumed
// operator reference, so composing at the append boundary is what keeps the
// committed forced facts naming the approval they consumed (CD-0212 D2).
// Every planned fact still comes from the probe; the request contributes
// only the destructive flag and the approval reference, which the
// transaction re-validates.
func (p WorktreeReclaimProbe) factsFor(req WorktreeReclaimRequest) json.RawMessage {
	if p.AlreadyAbsent {
		return jsonMustMarshal(worktreeReclaimFacts{AlreadyAbsent: true, Forced: req.Destructive, OperatorOverride: approvalFactRef(req)})
	}
	return p.reclaimFacts(req)
}

// planStoredRefDeletion separates directory-removal proof from stored-ref
// deletion (CD-0212 D3). The stored claim branch is a deletion candidate
// only under its own proof: the live checkout's durability covers it when
// the live head is that branch; otherwise the ref is proven independently
// against the tip the plan pins, and retained whenever the proof, the ref,
// or the ownership fails. The default-ref ownership guard runs before every
// candidate — including the checked-out claim branch — and a divergent live
// branch ref is retained, because no authority here owns its deletion.
//
// The protected default set is established independently of the caller's
// merge target: the normalized caller ref, the repository's origin/HEAD
// target, and the repository's own checked-out HEAD each contribute, and
// accepted ref spellings normalize before comparison. When the set cannot be
// established at all, deletion permission fails closed: candidate refs are
// retained with the reason recorded, and a proven-durable live removal is
// never blocked by that unknown.
func planStoredRefDeletion(ctx context.Context, runner GitRunner, req WorktreeReclaimRequest, op string, entry WorktreeEntry, repoRoot string, live worktreeLiveHead, destructive bool, liveDurableVia string) ([]WorktreeBranchDeletion, []WorktreeRetainedRef, error) {
	var deletions []WorktreeBranchDeletion
	var retained []WorktreeRetainedRef
	if !live.Detached && live.Branch != "" && live.Branch != entry.Branch {
		retained = append(retained, WorktreeRetainedRef{Branch: live.Branch, Tip: live.Tip, Reason: "live branch differs from the stored claim; no deletion authority"})
	}
	protected, defaultsKnown := protectedDefaultBranches(ctx, runner, repoRoot, req.DefaultRef)
	// The ownership guard runs before every deletion candidate, including the
	// checked-out claim branch itself: the default ref is never deleted,
	// whatever the claim row or the live checkout says (CD-0212 D3).
	if protected[entry.Branch] {
		resolved := resolveBranchRef(ctx, runner, repoRoot, entry.Branch)
		if resolved.err != nil {
			return nil, nil, resolved.err
		}
		if resolved.exists {
			retained = append(retained, WorktreeRetainedRef{Branch: entry.Branch, Tip: resolved.tip, Reason: "the default ref is never deleted"})
		}
		return deletions, retained, nil
	}
	if !live.Detached && live.Branch == entry.Branch {
		if !defaultsKnown {
			// Ownership failed closed: the claim branch may itself be the
			// unestablished default, so the ref is retained, while the
			// proven-durable live removal proceeds unblocked.
			return deletions, append(retained, WorktreeRetainedRef{Branch: entry.Branch, Tip: live.Tip, Reason: "cannot establish the protected default; the ref was retained"}), nil
		}
		reason := "live head is the stored claim branch, durable: " + liveDurableVia
		if destructive {
			reason = "operator-approved destructive removal of the checked-out claim branch"
		}
		return append(deletions, WorktreeBranchDeletion{Branch: entry.Branch, ExpectedTip: live.Tip, Reason: reason}), retained, nil
	}
	resolved := resolveBranchRef(ctx, runner, repoRoot, entry.Branch)
	if resolved.err != nil {
		return nil, nil, resolved.err
	}
	if !resolved.exists {
		return deletions, retained, nil
	}
	if resolved.symbolic {
		return deletions, append(retained, WorktreeRetainedRef{Branch: entry.Branch, Tip: resolved.tip, Reason: "the stored claim ref is a symbolic ref; no deletion authority"}), nil
	}
	if !defaultsKnown {
		return deletions, append(retained, WorktreeRetainedRef{Branch: entry.Branch, Tip: resolved.tip, Reason: "cannot establish the protected default; the ref was retained"}), nil
	}
	if destructive {
		return append(deletions, WorktreeBranchDeletion{Branch: entry.Branch, ExpectedTip: resolved.tip, Reason: "operator-approved destructive removal of the stored claim branch"}), retained, nil
	}
	// The independent proof runs against the tip the plan just pinned, not
	// the mutable branch name it read it from: a ref that moved between the
	// two probes must not be deleted under proof of content it no longer
	// holds (CD-0212 D3).
	contained, durableErr := branchIsDurable(ctx, runner, repoRoot, resolved.tip, "stored branch "+entry.Branch, req.DefaultRef, op)
	if durableErr != nil {
		return deletions, append(retained, WorktreeRetainedRef{Branch: entry.Branch, Tip: resolved.tip, Reason: "stored claim content is not proven durable; the ref was retained"}), nil
	}
	via := "remote_reachable"
	if contained {
		via = "squash_merged"
	}
	return append(deletions, WorktreeBranchDeletion{Branch: entry.Branch, ExpectedTip: resolved.tip, Reason: "stored claim ref is durable: " + via}), retained, nil
}

// normalizeBranchRef normalizes one accepted ref spelling to its short
// branch name: refs/remotes/origin/<b>, refs/remotes/<b>, refs/heads/<b>,
// and origin/<b> all reduce to <b>. A spelling a branch name cannot hold —
// empty, still prefixed refs/, leading dash or slash, a trailing slash or
// .lock, or any character git forbids in one ref component — returns empty,
// which leaves default ownership unestablished and fails deletion closed.
func normalizeBranchRef(ref string) string {
	name := strings.TrimSpace(ref)
	for _, prefix := range []string{"refs/remotes/origin/", "refs/remotes/", "refs/heads/"} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
			break
		}
	}
	name = strings.TrimPrefix(name, "origin/")
	if name == "" || len(name) > 128 || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") || strings.Contains(name, "..") {
		return ""
	}
	if strings.ContainsAny(name, " \t\n\r~^:?*[\\") || name == "refs" || strings.HasPrefix(name, "refs/") {
		return ""
	}
	return name
}

// protectedDefaultBranches establishes the repository's protected default
// branches independently of any single caller input: the normalized caller
// merge target, the origin/HEAD target, and the repository's own checked-out
// HEAD each contribute a short name. known reports whether any observation
// actually established a default. Absence is not establishment: a caller ref
// that cannot normalize, a probe that failed outright (not one that merely
// found no such ref), a symbolic target no branch name can hold, and a set
// where no observation contributed anything at all — no caller ref, a missing
// origin/HEAD, a detached repository HEAD — each leave the protected set
// unestablished, and deletion permission fails closed against it: an empty
// set nothing established proves no default, so it can never authorize
// deleting the repository's default by accident.
func protectedDefaultBranches(ctx context.Context, runner GitRunner, repoRoot, callerDefaultRef string) (protected map[string]bool, known bool) {
	protected = map[string]bool{}
	established := 0
	if callerDefaultRef != "" {
		short := normalizeBranchRef(callerDefaultRef)
		if short == "" {
			return protected, false
		}
		protected[short] = true
		established++
	}
	for _, ref := range []string{"refs/remotes/origin/HEAD", "HEAD"} {
		out, err := runner.Run(ctx, repoRoot, "symbolic-ref", "--quiet", ref)
		if err != nil {
			if !gitProbeFoundRefAbsent(err) {
				return protected, false
			}
			continue
		}
		short := normalizeBranchRef(strings.TrimSpace(string(out)))
		if short == "" {
			return protected, false
		}
		protected[short] = true
		established++
	}
	return protected, established > 0
}

// gitProbeFoundRefAbsent reports whether a failed git probe is the typed
// absence of the ref it named (exit status 1) rather than a probe that could
// not run. Only absence proves the ref contributes nothing; every other
// failure leaves the question open.
func gitProbeFoundRefAbsent(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

// branchRefResolution is one local branch ref resolved by its exact
// refs/heads path: the observed tip, whether the ref is itself symbolic, and
// whether it exists at all.
type branchRefResolution struct {
	tip      string
	symbolic bool
	exists   bool
	err      error
}

// resolveBranchRef resolves a local branch ref by its exact refs/heads path,
// never by its short name: a tag that shadows the branch name cannot bend
// the resolution, because the full ref path admits no ambiguity. A symbolic
// ref resolves to its target's tip and is reported symbolic, so a caller can
// refuse to delete through it. show-ref reports a missing ref with exit
// status 1 and every other failure with a different status; only exit 1 is
// absence, so native deletion debt is never marked converged by a probe that
// could not run (CD-0212 D4).
func resolveBranchRef(ctx context.Context, runner GitRunner, repoRoot, branch string) branchRefResolution {
	full := "refs/heads/" + branch
	_, symErr := runner.Run(ctx, repoRoot, "symbolic-ref", "--quiet", full)
	if symErr == nil {
		tipOut, tipErr := runner.Run(ctx, repoRoot, "rev-parse", "--verify", full)
		if tipErr != nil {
			return branchRefResolution{symbolic: true, exists: true, err: wrapFailure(KindGitUnreachable, "worktree_reclaim", "cannot resolve the tip of branch "+branch, true, "retry once the repository is reachable", tipErr)}
		}
		return branchRefResolution{tip: strings.TrimSpace(string(tipOut)), symbolic: true, exists: true}
	}
	if !gitProbeFoundRefAbsent(symErr) {
		return branchRefResolution{err: wrapFailure(KindGitUnreachable, "worktree_reclaim", "cannot inspect branch "+branch, true, "retry once the repository is reachable", symErr)}
	}
	if _, err := runner.Run(ctx, repoRoot, "show-ref", "--verify", "--quiet", full); err != nil {
		if gitProbeFoundRefAbsent(err) {
			return branchRefResolution{}
		}
		return branchRefResolution{err: wrapFailure(KindGitUnreachable, "worktree_reclaim", "cannot inspect branch "+branch, true, "retry once the repository is reachable", err)}
	}
	tipOut, tipErr := runner.Run(ctx, repoRoot, "rev-parse", "--verify", full)
	if tipErr != nil {
		return branchRefResolution{exists: true, err: wrapFailure(KindGitUnreachable, "worktree_reclaim", "cannot resolve the tip of branch "+branch, true, "retry once the repository is reachable", tipErr)}
	}
	return branchRefResolution{tip: strings.TrimSpace(string(tipOut)), exists: true}
}

// pendingRecordedDeletions filters the recorded plan's pinned deletions
// against the durable outcome rows of the same claim generation: a deletion
// already settled, or protected by a sticky identity protection, replays no
// native attempt. Everything else is still owed — including a recovery debt,
// whose replay resolves the restoration or verifies the absence before the
// plan converges, and a restored ref whose checkout protection a later
// replay re-derives — and the native boundary classifies it against git when
// the removal runs.
// pendingRecordedDeletions returns the recorded deletions the durable
// phase owner still owes, each carrying its recorded phase so the native
// run resumes the recorded predecessor instead of re-deriving it. A row at
// a terminal phase — settled or retained_unproven — owes nothing; every
// pending phase stays owed until the owner table moves it.
func pendingRecordedDeletions(recorded []WorktreeBranchDeletion, prior []worktreeRefOutcome) []WorktreeBranchDeletion {
	recorded2 := map[string]string{}
	for _, outcome := range prior {
		recorded2[outcome.branch] = outcome.phase
	}
	var pending []WorktreeBranchDeletion
	for _, deletion := range recorded {
		phase, seen := recorded2[deletion.Branch]
		if seen {
			if terminalRefPhase(phase) {
				continue
			}
			deletion.Phase = phase
		}
		pending = append(pending, deletion)
	}
	return pending
}

// RunWorktreeNativeRemoval removes the worktree and finishes the branch
// deletions one committed reclaim still owes, following the bounded plan its
// event recorded. It runs with no transaction open: the caller commits the
// reclamation event first (CD-0195 D2). Before every destructive native
// effect it revalidates the identity the plan pinned: the checkout must
// still belong to the probed repository at the recorded tip, and a branch is
// deleted only when it is still the direct ref the plan pinned, still at its
// pinned tip, and checked out nowhere (CD-0212 D4). Every per-item result is
// reported as one bounded outcome — settled, protected, or recovery owed —
// for the caller's durable settlement; a failed probe leaves the item owed
// and records nothing, while a post-deletion uncertainty records the
// restoration debt it cannot resolve.
func RunWorktreeNativeRemoval(ctx context.Context, runner GitRunner, removal *WorktreeNativeRemoval) ([]WorktreeNativeOutcome, error) {
	if removal == nil {
		return nil, nil
	}
	if runner == nil {
		runner = ExecGitRunner{}
	}
	var outcomes []WorktreeNativeOutcome
	if removal.Path != "" {
		if err := revalidateNativeWorktree(ctx, runner, removal); err != nil {
			return outcomes, err
		}
		args := []string{"worktree", "remove"}
		if removal.Force {
			args = append(args, "--force")
		}
		if _, err := runner.Run(ctx, removal.RepoRoot, append(args, removal.Path)...); err != nil {
			return outcomes, wrapFailure(KindGitUnreachable, removal.Op, "reclaimed in Concord but native removal failed", true, "remove the worktree manually; the projection already records reclamation", err)
		}
	}
	// The pinned stdin transaction below is what keeps the forced deletion
	// honest: the durability gate probed remote refs and squash containment,
	// while git's own ancestry checks cannot answer that question, so the
	// deletion carries its own proof — the tip the plan pinned — into the
	// mutation itself.
	var protected error
	for _, deletion := range removal.BranchDeletions {
		outcome, err := deleteBranchPinned(ctx, runner, removal, deletion)
		if outcome.Branch != "" {
			outcomes = append(outcomes, outcome)
		}
		var failure *Failure
		switch {
		case err == nil:
		case errors.As(err, &failure) && (failure.Kind == KindProjectionConflict || failure.Kind == KindInvalidOperation):
			// A protected ref is reported and the removal continues: one
			// retained branch must not mask the deletions the plan still
			// owes, and a protected ref must not mask an independently safe
			// pending directory either.
			if protected == nil {
				protected = err
			}
		default:
			return outcomes, err
		}
	}
	return outcomes, protected
}

// pinnedDeletionRun tracks one branch deletion's durable phase through a
// native run, so every phase record the run makes starts from the phase the
// row holds and moves only along the owning transition table
// (worktreeRefPhaseTransitions). Each record precedes the native effect it
// authorizes, and the run's in-memory phase follows the durable record.
type pinnedDeletionRun struct {
	removal  *WorktreeNativeRemoval
	deletion WorktreeBranchDeletion
	phase    string
}

// recordPhase persists one table-admitted phase move: the step must start
// from the run's recorded predecessor phase and admit the target. The
// settlement owner validates the same table against the durable row, so a
// phase a stale probe carried can never authorize what the row refuses.
func (r *pinnedDeletionRun) recordPhase(step, toPhase, tip, reason string) error {
	if refusal := worktreeRefStepRefusal(step, r.phase); refusal != "" {
		return newFailure(KindProjectionConflict, r.removal.Op,
			"branch "+r.deletion.Branch+" sits at phase "+r.phase+" ("+refusal+"); the "+step+" step cannot start from it",
			false, "the durable phase owner refuses this step; inspect the recorded phase")
	}
	if !worktreeRefStepTarget(step, r.phase, toPhase) {
		return newFailure(KindProjectionConflict, r.removal.Op,
			"branch "+r.deletion.Branch+" cannot move from phase "+r.phase+" to "+toPhase+" through the "+step+" step",
			false, "the durable phase owner admits no such transition; inspect the recorded phase")
	}
	if err := r.removal.recordPhase(WorktreeNativeOutcome{Branch: r.deletion.Branch, Tip: tip, Phase: toPhase, Reason: reason}); err != nil {
		return err
	}
	r.phase = toPhase
	return nil
}

// restorationOwedRun shapes one durable restoration debt and persists it:
// an observation around the pinned deletion found, or could not exclude, a
// checkout naming the ref, so the recovery at the immutable pinned tip is
// still owed. The debt stays pending across directory removal, replay,
// later claims, and audit; ref absence never settles it. A record that
// cannot persist keeps the outcome — the settlement pass records it after
// the run — and carries the failure in the reason.
func (r *pinnedDeletionRun) restorationOwedRun(step, reason string) WorktreeNativeOutcome {
	outcome := WorktreeNativeOutcome{Branch: r.deletion.Branch, Tip: r.deletion.ExpectedTip, Phase: WorktreeRefPhaseRestorationOwed, Reason: reason}
	if err := r.recordPhase(step, WorktreeRefPhaseRestorationOwed, r.deletion.ExpectedTip, reason); err != nil {
		outcome.Reason = reason + "; the phase record could not be persisted: " + err.Error()
		return outcome
	}
	return outcome
}

// deleteBranchPinned advances one planned branch deletion along the
// durable phase owner (CD-0212 D4), resuming from the phase the recorded
// plan carried. Each step starts only from its recorded predecessor in
// worktreeRefPhaseTransitions: ownership is re-derived inside the attempt
// (the complete worktree inventory, the direct ref identity at its pinned
// tip, and the protected default set), the authorization
// (checkout_proven_absent) is persisted before the pinned argv deletion
// `update-ref --no-deref -d <ref> <expected-tip>` runs, the deleted phase
// is persisted before the post-deletion observation reads every listed
// worktree HEAD again, and a restoration is persisted as restoration_owed
// before the create-only argv restore rebuilds the ref at exactly the
// pinned tip. Every refusal is one of the enumerated typed reasons and
// keeps the row at the phase it holds; every uncertain observation yields
// restoration_owed or retained_unproven, never settled, and an absent ref
// settles only when a complete inventory verifies no worktree holds it.
func deleteBranchPinned(ctx context.Context, runner GitRunner, removal *WorktreeNativeRemoval, deletion WorktreeBranchDeletion) (WorktreeNativeOutcome, error) {
	if deletion.ExpectedTip == "" {
		return WorktreeNativeOutcome{}, newFailure(KindProjectionConflict, removal.Op, "branch "+deletion.Branch+" has no pinned tip in the recorded plan; the ref was retained and not deleted", false, "re-run the reclaim probe so the plan records the tip it proved")
	}
	phase := deletion.Phase
	if phase == "" {
		phase = WorktreeRefPhasePlanned
	}
	run := &pinnedDeletionRun{removal: removal, deletion: deletion, phase: phase}
	if refusal := worktreeRefStepRefusal(WorktreeRefStepObserveCheckouts, phase); refusal != "" {
		return WorktreeNativeOutcome{}, newFailure(KindProjectionConflict, removal.Op, "branch "+deletion.Branch+" sits at phase "+phase+" ("+refusal+"); the observe_checkouts step cannot start from it", false, "the durable phase owner refuses this step; inspect the recorded phase")
	}
	retain := func(tip, reason, refusal string, err error) (WorktreeNativeOutcome, error) {
		outcome := WorktreeNativeOutcome{Branch: deletion.Branch, Tip: tip, Phase: WorktreeRefPhaseRetainedUnproven, Reason: reason, Refusal: refusal}
		if recordErr := run.recordPhase(WorktreeRefStepObserveCheckouts, WorktreeRefPhaseRetainedUnproven, tip, reason); recordErr != nil {
			// The same contract restorationOwedRun carries: a record that
			// cannot persist keeps the outcome — the settlement pass records
			// it after the run — and carries the failure in the reason, so
			// neither the retention nor its reason is lost behind the
			// persist error.
			outcome.Reason = reason + "; the phase record could not be persisted: " + recordErr.Error()
			return outcome, err
		}
		return outcome, err
	}
	resolved := resolveBranchRef(ctx, runner, removal.RepoRoot, deletion.Branch)
	if resolved.err != nil {
		return WorktreeNativeOutcome{}, resolved.err
	}
	if !resolved.exists {
		// An absent ref is not settlement: an earlier attempt may have
		// deleted it while another worktree held it checked out, leaving
		// restoration debt. Only a complete inventory that observes no
		// holder converges the debt (CD-0212 D4).
		return classifyAbsentPinnedDeletion(ctx, runner, run)
	}
	if resolved.symbolic {
		return retain(resolved.tip, "the ref became a symbolic ref; deleting through it would delete its target", WorktreeRefRefusalSymbolicRef,
			newFailure(KindProjectionConflict, removal.Op, "branch "+deletion.Branch+" became a symbolic ref after the reclaim pinned it; the ref and its target were retained", false, "inspect the symbolic ref and delete it by hand if its content is disposable"))
	}
	if resolved.tip != deletion.ExpectedTip {
		return retain(resolved.tip, "the ref moved from the tip the reclaim pinned", WorktreeRefRefusalMovedTip,
			newFailure(KindProjectionConflict, removal.Op, "branch "+deletion.Branch+" moved from the tip the reclaim pinned; the ref was retained and not deleted", false, "inspect the branch and delete it by hand if its new content is disposable"))
	}
	// The default ownership is re-derived here, not trusted from the plan:
	// the repository's default may have moved onto this branch after the
	// plan committed, and the default ref is never deleted whatever the
	// plan proved about the tip (CD-0212 D3). The default protection tracks
	// a live condition git itself owns, so it keeps the row at its recorded
	// pending phase — the refusal reason settles over it and a later replay
	// re-derives the ownership — instead of recording a terminal identity
	// protection.
	protected, defaultsKnown := protectedDefaultBranches(ctx, runner, removal.RepoRoot, removal.DefaultRef)
	if protected[deletion.Branch] {
		return WorktreeNativeOutcome{Branch: deletion.Branch, Tip: resolved.tip, Phase: run.phase, Reason: "the branch is the repository's default ref; the default ref is never deleted", Refusal: WorktreeRefRefusalProtectedDefault},
			newFailure(KindProjectionConflict, removal.Op, "branch "+deletion.Branch+" is the repository's default ref; it was retained and not deleted", false, "inspect the default ref: the reclaim proved the content durable, but the default ref is never deleted")
	}
	if !defaultsKnown {
		// Fail closed exactly as the plan-time guard does: no observation
		// established a protected default, so the ref may itself be the
		// unestablished default. The ref is kept at its recorded pending
		// phase and the outcome is reported without failing the removal —
		// a proven-durable directory or another debt is never masked by
		// the unknown, and a later replay re-derives the default set.
		return WorktreeNativeOutcome{Branch: deletion.Branch, Tip: resolved.tip, Phase: run.phase, Reason: "cannot establish the protected default; the ref was retained", Refusal: WorktreeRefRefusalProtectedDefault}, nil
	}
	if err := refuseWhenBranchCheckedOut(ctx, runner, removal.RepoRoot, deletion.Branch, removal.Op); err != nil {
		var failure *Failure
		if errors.As(err, &failure) && failure.Kind == KindInvalidOperation {
			// A pre-read holder refuses the deletion but retires nothing:
			// the row keeps its recorded phase, so the debt stays owed and
			// a later replay converges once the holder releases the ref.
			return WorktreeNativeOutcome{Branch: deletion.Branch, Tip: resolved.tip, Phase: phase, Reason: "the branch is checked out in a worktree of this repository", Refusal: WorktreeRefRefusalHolderPresent}, err
		}
		// The pre-deletion observation itself could not run or could not be
		// completed: it cannot exclude a worktree holding the ref, so the
		// actual uncertainty is durable — a recovery phase with the
		// failed-observation reason, never a silent pending row that still
		// carries its original durability reason (CD-0212 D4).
		return run.restorationOwedRun(WorktreeRefStepObserveCheckouts, "cannot inspect the worktree checkouts before the pinned deletion; the observation could not exclude a holder and the state at the pinned tip is uncertain"),
			err
	}
	// The authorization precedes its native effect: the row records
	// checkout_proven_absent before the pinned deletion runs.
	if err := run.recordPhase(WorktreeRefStepObserveCheckouts, WorktreeRefPhaseCheckoutProvenAbsent, deletion.ExpectedTip, "complete inventory proved no worktree holds the ref at its pinned tip"); err != nil {
		return WorktreeNativeOutcome{}, err
	}
	full := "refs/heads/" + deletion.Branch
	if _, err := runner.Run(ctx, removal.RepoRoot, "update-ref", "--no-deref", "-d", full, deletion.ExpectedTip); err != nil {
		after := resolveBranchRef(ctx, runner, removal.RepoRoot, deletion.Branch)
		if after.err != nil {
			return WorktreeNativeOutcome{}, after.err
		}
		if !after.exists {
			// The ref vanished at the boundary; only a complete inventory
			// that observes no holder may converge the debt.
			return classifyAbsentPinnedDeletion(ctx, runner, run)
		}
		if after.symbolic {
			return retain(after.tip, "the ref became a symbolic ref; deleting through it would delete its target", WorktreeRefRefusalSymbolicRef,
				newFailure(KindProjectionConflict, removal.Op, "branch "+deletion.Branch+" became a symbolic ref after the reclaim pinned it; the ref and its target were retained", false, "inspect the symbolic ref and delete it by hand if its content is disposable"))
		}
		if after.tip != deletion.ExpectedTip {
			return retain(after.tip, "the ref moved from the tip the reclaim pinned", WorktreeRefRefusalMovedTip,
				newFailure(KindProjectionConflict, removal.Op, "branch "+deletion.Branch+" moved from the tip the reclaim pinned; the ref was retained and not deleted", false, "inspect the branch and delete it by hand if its new content is disposable"))
		}
		return WorktreeNativeOutcome{}, wrapFailure(KindGitUnreachable, removal.Op, "reclaimed in Concord but branch deletion failed", true, "delete the branch manually; the projection already records reclamation", err)
	}
	// The deletion ran; the phase records it before the post-deletion
	// observation, so an interruption between them resumes as uncertainty
	// rather than inferred settlement.
	if err := run.recordPhase(WorktreeRefStepDeletePinned, WorktreeRefPhaseDeleted, deletion.ExpectedTip, "the pinned deletion ran at its pinned tip"); err != nil {
		return WorktreeNativeOutcome{}, err
	}
	// update-ref enforces no checkout guard of its own, so ownership is
	// observed once more on this side of the deletion, from the complete
	// inventory: a worktree that gained the branch between the pre-check and
	// the mutation would otherwise strand an unborn HEAD.
	return settleDeletedBranchUnderCheckouts(ctx, runner, run)
}

// classifyAbsentPinnedDeletion resolves an absent ref the recorded plan
// pinned: verified absence — a complete inventory that observes no worktree
// holding the branch — converges the debt, while a checkout that still names
// the absent branch owes its restoration at the immutable pinned tip, and
// that debt is persisted before the restore runs so the restore step starts
// from its recorded predecessor. An observation that cannot run leaves
// durable restoration debt; it never settles an absence it could not verify
// (CD-0212 D4).
func classifyAbsentPinnedDeletion(ctx context.Context, runner GitRunner, run *pinnedDeletionRun) (WorktreeNativeOutcome, error) {
	held, err := observeBranchCheckouts(ctx, runner, run.removal.RepoRoot, run.deletion.Branch, run.removal.Op)
	if err != nil {
		return run.restorationOwedRun(WorktreeRefStepObserveCheckouts, "cannot inspect worktree checkouts after the pinned deletion; the restoration at the pinned tip is still owed"), err
	}
	if held {
		// The restoration debt is persisted before the restore runs.
		if recordErr := run.recordPhase(WorktreeRefStepObserveCheckouts, WorktreeRefPhaseRestorationOwed, run.deletion.ExpectedTip, "a worktree holds the deleted branch checked out; the restoration at the pinned tip is owed"); recordErr != nil {
			return WorktreeNativeOutcome{}, recordErr
		}
		return restoreDeletedBranchAtPinnedTip(ctx, runner, run)
	}
	outcome := WorktreeNativeOutcome{Branch: run.deletion.Branch, Tip: run.deletion.ExpectedTip, Phase: WorktreeRefPhaseSettled, Reason: "the ref no longer exists and no worktree holds it; the pinned deletion converged"}
	if recordErr := run.recordPhase(WorktreeRefStepObserveCheckouts, WorktreeRefPhaseSettled, run.deletion.ExpectedTip, outcome.Reason); recordErr != nil {
		return WorktreeNativeOutcome{}, recordErr
	}
	return outcome, nil
}

// settleDeletedBranchUnderCheckouts observes the complete worktree inventory
// after a successful pinned deletion. No holder settles the deletion; a
// holder owes the restoration at the pinned tip and, once restored, leaves
// the ref retained; an unobservable inventory leaves durable restoration
// debt.
func settleDeletedBranchUnderCheckouts(ctx context.Context, runner GitRunner, run *pinnedDeletionRun) (WorktreeNativeOutcome, error) {
	held, err := observeBranchCheckouts(ctx, runner, run.removal.RepoRoot, run.deletion.Branch, run.removal.Op)
	if err != nil {
		return run.restorationOwedRun(WorktreeRefStepObservePostDelete, "cannot inspect worktree checkouts after the pinned deletion; the restoration at the pinned tip is still owed"), err
	}
	if held {
		// The restoration debt is persisted before the restore runs.
		if recordErr := run.recordPhase(WorktreeRefStepObservePostDelete, WorktreeRefPhaseRestorationOwed, run.deletion.ExpectedTip, "a worktree holds the deleted branch checked out; the restoration at the pinned tip is owed"); recordErr != nil {
			return WorktreeNativeOutcome{}, recordErr
		}
		return restoreDeletedBranchAtPinnedTip(ctx, runner, run)
	}
	outcome := WorktreeNativeOutcome{Branch: run.deletion.Branch, Tip: run.deletion.ExpectedTip, Phase: WorktreeRefPhaseSettled, Reason: "the pinned deletion ran at its pinned tip"}
	if recordErr := run.recordPhase(WorktreeRefStepObservePostDelete, WorktreeRefPhaseSettled, run.deletion.ExpectedTip, outcome.Reason); recordErr != nil {
		return WorktreeNativeOutcome{}, recordErr
	}
	return outcome, nil
}

// restoreDeletedBranchAtPinnedTip restores a branch the pinned deletion
// removed while another worktree held it checked out, through a create-only
// argv update — `update-ref --no-deref <ref> <pinned-tip> <zero-oid>` —
// whose zero old-value refuses to overwrite anything a concurrent actor
// already rebuilt, so a foreign or concurrently recreated ref is preserved
// exactly as it stands. A restoration that cannot run leaves durable
// restoration debt at the immutable pinned tip; a ref another actor already
// rebuilt leaves the identity retained unproven.
func restoreDeletedBranchAtPinnedTip(ctx context.Context, runner GitRunner, run *pinnedDeletionRun) (WorktreeNativeOutcome, error) {
	full := "refs/heads/" + run.deletion.Branch
	zero := strings.Repeat("0", len(run.deletion.ExpectedTip))
	if _, err := runner.Run(ctx, run.removal.RepoRoot, "update-ref", "--no-deref", full, run.deletion.ExpectedTip, zero); err != nil {
		after := resolveBranchRef(ctx, runner, run.removal.RepoRoot, run.deletion.Branch)
		if after.err != nil {
			return run.restorationOwedRun(WorktreeRefStepRestoreExpected, "the raced branch could not be restored at its pinned tip and its state is unobservable; the restoration is still owed"), after.err
		}
		if after.exists {
			outcome := WorktreeNativeOutcome{Branch: run.deletion.Branch, Tip: after.tip, Phase: WorktreeRefPhaseRetainedUnproven, Reason: "the branch was checked out at the deletion boundary; the ref already stands rebuilt", Refusal: WorktreeRefRefusalInventoryUnknown}
			if recordErr := run.recordPhase(WorktreeRefStepRestoreExpected, WorktreeRefPhaseRetainedUnproven, after.tip, outcome.Reason); recordErr != nil {
				return WorktreeNativeOutcome{}, recordErr
			}
			return outcome, newFailure(KindInvalidOperation, run.removal.Op, "branch "+run.deletion.Branch+" was checked out at the deletion boundary and was already rebuilt by another actor", false, "inspect the branch: the recorded plan no longer owes its deletion")
		}
		return run.restorationOwedRun(WorktreeRefStepRestoreExpected, "the raced branch could not be restored at its pinned tip; the restoration is still owed"),
			wrapFailure(KindGitUnreachable, run.removal.Op, "reclaimed in Concord but the raced branch could not be restored at its pinned tip", true, "restore branch "+run.deletion.Branch+" at "+run.deletion.ExpectedTip+" by hand; a worktree holds it checked out", err)
	}
	// A restored checked-out ref remains retained, never deletion-settled.
	outcome := WorktreeNativeOutcome{Branch: run.deletion.Branch, Tip: run.deletion.ExpectedTip, Phase: WorktreeRefPhaseRestored, Reason: "the branch was checked out at the deletion boundary and was restored at its pinned tip"}
	if recordErr := run.recordPhase(WorktreeRefStepRestoreExpected, WorktreeRefPhaseRestored, run.deletion.ExpectedTip, outcome.Reason); recordErr != nil {
		return WorktreeNativeOutcome{}, recordErr
	}
	return outcome, newFailure(KindInvalidOperation, run.removal.Op, "branch "+run.deletion.Branch+" was checked out at the deletion boundary; it was restored at its pinned tip and retained", false, "check out another branch in that worktree, then retry the removal")
}

// refuseWhenBranchCheckedOut refuses the deletion of a branch any worktree
// of the repository still has checked out. git branch -D refuses this shape
// itself; the pinned compare-and-delete path must refuse it just as
// forcefully, because deleting the ref would strand that worktree's HEAD.
// The refusal reads the complete worktree inventory: an observation that
// cannot run, or a malformed or incomplete inventory, refuses rather than
// guessing, because an unknown checkout state is never proof a branch is
// checked out nowhere.
func refuseWhenBranchCheckedOut(ctx context.Context, runner GitRunner, repoRoot, branch, op string) error {
	held, err := observeBranchCheckouts(ctx, runner, repoRoot, branch, op)
	if err != nil {
		return err
	}
	if held {
		return newFailure(KindInvalidOperation, op, "branch "+branch+" is checked out in a worktree of this repository and was retained", false, "check out another branch in that worktree, then retry the removal")
	}
	return nil
}

// worktreeCheckoutEntry is one complete entry of the porcelain worktree
// inventory: the worktree's path and the symbolic identity of its HEAD — the
// full branch ref an attached HEAD names, or the detached or bare marker.
type worktreeCheckoutEntry struct {
	path     string
	branch   string
	detached bool
	bare     bool
}

// worktreeInventory is the validated porcelain inventory of every worktree
// the repository holds. Completeness is structural: every entry carries a
// known HEAD disposition, so an inventory that names a worktree without
// observing its HEAD's identity refuses instead of guessing (CD-0212 D4).
type worktreeInventory struct {
	entries []worktreeCheckoutEntry
}

// isWorktreeOID reports whether the porcelain HEAD attribute carries an
// object name: 40 or 64 lowercase hex digits, all-zero for an unborn HEAD.
func isWorktreeOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// parseWorktreeInventory parses and validates the complete porcelain output
// of `git worktree list --porcelain`, observing the symbolic identity of
// every listed HEAD. The parse is whole or nothing, and the record grammar
// is the one real git emits: records are separated and terminated by truly
// empty boundary lines — a whitespace-only line is content, not a boundary
// — every attribute a complete record carries once, a worktree path appears
// in exactly one record, an attribute before any worktree line or after a
// boundary closed its record, a worktree without a path, an entry whose
// HEAD disposition is missing or contradictory, a non-bare entry without
// its HEAD attribute, a bare entry claiming a HEAD or a branch, a HEAD that
// is not an object name, and an unknown attribute each refuse, because a
// malformed successful inventory is an unknown observation — never proof
// that a branch is checked out nowhere.
func parseWorktreeInventory(out []byte) (*worktreeInventory, error) {
	malformed := errors.New("store: worktree_reclaim: the worktree inventory is malformed or incomplete")
	inv := &worktreeInventory{}
	var current *worktreeCheckoutEntry
	attrSeen := map[string]bool{}
	paths := map[string]bool{}
	// boundary reports whether the previous line closed a record: the
	// first record needs no preceding separator, every later one does, and
	// the output must end on the terminating boundary after the last
	// record. The trailing newline of the final line is the byte artifact
	// of line splitting, not a boundary, so it is stripped before the
	// split and only a genuine empty line closes a record.
	boundary := true
	complete := func() error {
		if current == nil {
			return nil
		}
		switch {
		case current.bare:
			// A bare record's disposition is its bareness alone: it carries
			// no HEAD, branch, or detached attribute.
			if current.branch != "" || current.detached || attrSeen["HEAD"] {
				return malformed
			}
		case current.branch != "":
			if current.detached || !attrSeen["HEAD"] {
				return malformed
			}
		case current.detached:
			if !attrSeen["HEAD"] {
				return malformed
			}
		default:
			// Neither bare, nor attached, nor detached: the entry never
			// observed its HEAD's identity.
			return malformed
		}
		inv.entries = append(inv.entries, *current)
		current = nil
		attrSeen = map[string]bool{}
		return nil
	}
	for _, raw := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			// The empty line is the explicit record boundary porcelain
			// promises: it closes the record it terminates, so an
			// attribute that follows one belongs to no record.
			if err := complete(); err != nil {
				return nil, err
			}
			boundary = true
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		if key == "worktree" {
			if current != nil {
				// A record was still open: the separating boundary never
				// came, so this is not a recognized record start.
				return nil, malformed
			}
			if value == "" || paths[value] {
				return nil, malformed
			}
			paths[value] = true
			current = &worktreeCheckoutEntry{path: value}
			boundary = false
			continue
		}
		if current == nil {
			return nil, malformed
		}
		boundary = false
		switch key {
		case "HEAD":
			if attrSeen[key] || !isWorktreeOID(value) {
				return nil, malformed
			}
			attrSeen[key] = true
		case "branch":
			if value == "" || !strings.HasPrefix(value, "refs/") || current.branch != "" {
				return nil, malformed
			}
			current.branch = value
		case "bare":
			if value != "" || current.bare {
				return nil, malformed
			}
			current.bare = true
		case "detached":
			if value != "" || current.detached {
				return nil, malformed
			}
			current.detached = true
		case "locked", "prunable":
			// Presentational attributes with an optional free-text value,
			// carried once by a complete record.
			if attrSeen[key] {
				return nil, malformed
			}
			attrSeen[key] = true
		default:
			return nil, malformed
		}
	}
	if !boundary {
		// The output did not end on the terminating boundary of its last
		// record, or held no complete record at all.
		return nil, malformed
	}
	if err := complete(); err != nil {
		return nil, err
	}
	if len(inv.entries) == 0 {
		return nil, malformed
	}
	return inv, nil
}

// observeListedWorktreeHead reads one listed worktree's symbolic HEAD live:
// `symbolic-ref --quiet HEAD` names the full ref an attached HEAD holds —
// including an unborn one, whose branch the ref itself no longer records —
// exit status 1 is the established detached marker, and every other failure
// is a failed observation that leaves the checkout state unknown. Only the
// live read turns the inventory's branch line into an observed identity the
// list-to-observation window cannot slip past (CD-0212 D4).
func observeListedWorktreeHead(ctx context.Context, runner GitRunner, path, op string) (string, error) {
	out, err := runner.Run(ctx, path, "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		ref := strings.TrimSpace(string(out))
		if !strings.HasPrefix(ref, "refs/") {
			return "", wrapFailure(KindGitUnreachable, op, "cannot observe the symbolic HEAD of worktree "+path, true, "retry once the worktree inventory is observable", nil)
		}
		return ref, nil
	}
	if gitProbeFoundRefAbsent(err) {
		return "", nil
	}
	return "", wrapFailure(KindGitUnreachable, op, "cannot observe the symbolic HEAD of worktree "+path, true, "retry once the worktree inventory is observable", err)
}

// observeBranchCheckouts reads and validates the complete worktree
// inventory, then reads every listed non-bare worktree's symbolic HEAD
// live, and reports whether any observed HEAD holds the branch checked out.
// Every listed HEAD is read — holders accumulate instead of ending the
// observation at the first one — so a failed later observation is never
// hidden by an earlier holder, and the live reads close the
// list-to-observation window in both directions: a checkout that gained
// the branch after the inventory listed it is still observed — an unborn
// HEAD names its branch symbolically — and a listed branch line keeps
// refusing until a later observation re-derives the state. A probe that
// cannot run, a successful inventory that does not parse completely, and a
// listed HEAD that cannot be observed each leave the question open as a
// typed unreachable failure: only a complete observation may prove a
// branch checked out nowhere (CD-0212 D4).
func observeBranchCheckouts(ctx context.Context, runner GitRunner, repoRoot, branch, op string) (bool, error) {
	out, err := runner.Run(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false, wrapFailure(KindGitUnreachable, op, "cannot inspect worktree checkouts", true, "retry once the repository is reachable", err)
	}
	inv, err := parseWorktreeInventory(out)
	if err != nil {
		return false, wrapFailure(KindGitUnreachable, op, "the worktree inventory is malformed or incomplete", true, "retry once git reports a complete worktree inventory", err)
	}
	full := "refs/heads/" + branch
	held := false
	for _, entry := range inv.entries {
		if entry.bare {
			// A bare record is an established bare disposition, not a
			// checkout: it holds no branch.
			continue
		}
		live, obsErr := observeListedWorktreeHead(ctx, runner, entry.path, op)
		if obsErr != nil {
			return false, obsErr
		}
		if entry.branch == full || live == full {
			held = true
		}
	}
	return held, nil
}

// revalidateNativeWorktree revalidates the native identity before the
// destructive directory effect: the checkout must still belong to the
// repository the reclaim probed and still sit at the live tip the probe
// observed (CD-0212 D4).
func revalidateNativeWorktree(ctx context.Context, runner GitRunner, removal *WorktreeNativeRemoval) error {
	commonOut, err := runner.Run(ctx, removal.Path, "rev-parse", "--git-common-dir")
	if err != nil {
		return wrapFailure(KindGitUnreachable, removal.Op, "cannot revalidate the worktree before removal", true, "retry once the worktree is reachable", err)
	}
	common := strings.TrimSpace(string(commonOut))
	if !filepath.IsAbs(common) {
		common = filepath.Join(removal.Path, common)
	}
	canonicalCommon, commonErr := normalizePath(filepath.Dir(common))
	canonicalRoot, rootErr := normalizePath(removal.RepoRoot)
	if commonErr != nil || rootErr != nil || canonicalCommon != canonicalRoot {
		return newFailure(KindProjectionConflict, removal.Op, "the worktree no longer belongs to the repository the reclaim probed", false, "inspect the worktree drift with the audit read before removing it by hand")
	}
	if removal.LiveTip != "" {
		tipOut, tipErr := runner.Run(ctx, removal.Path, "rev-parse", "HEAD")
		if tipErr != nil {
			return wrapFailure(KindGitUnreachable, removal.Op, "cannot revalidate the live HEAD before removal", true, "retry once the worktree is reachable", tipErr)
		}
		if strings.TrimSpace(string(tipOut)) != removal.LiveTip {
			return newFailure(KindProjectionConflict, removal.Op, "the worktree HEAD moved after the reclaim probe", false, "inspect the worktree and retry the removal once its content is accounted for")
		}
	}
	return nil
}

// reclaimWorktreeStoreTx is the durable half of a reclaim or destroy: it
// re-validates the store state against the probed entry, appends the verified
// reclamation event, and reports the native removal the caller owes after
// its own commit. It runs SQL only — every git fact arrived in the probe
// (CD-0195 D2).
func reclaimWorktreeStoreTx(ctx context.Context, tx *sql.Tx, req WorktreeReclaimRequest, probe WorktreeReclaimProbe) (WorktreeEntry, *WorktreeNativeRemoval, error) {
	var out WorktreeEntry
	if err := validateWorktreeReclaimIdentity(req); err != nil {
		return out, nil, err
	}
	if err := validateWorktreeProjectMembershipTx(ctx, tx, req.WorkID, req.ProjectID, "worktree_reclaim"); err != nil {
		return out, nil, err
	}
	op := reclaimOperation(req)
	// CD-0096 D3 Destroy: merged terminal work reclaims without approval.
	// Non-terminal work refuses typed unless the operator approved this
	// exact removal.
	if req.RequireTerminal {
		var lifecycle string
		err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, req.WorkID).Scan(&lifecycle)
		if err == sql.ErrNoRows {
			return out, nil, newFailure(KindUnknownScope, op, "work item does not exist", false, "select one existing work item")
		}
		if err != nil {
			return out, nil, wrapFailure(KindUnavailable, op, "cannot read the work item", true, "retry once the database is readable", err)
		}
		if !isTerminalLifecycle(lifecycle) && req.OperatorApprovalRef == "" {
			return out, nil, newFailure(KindInvalidTransition, op, "work item is "+lifecycle+", so its worktree is not merged terminal work", false, "complete or cancel the work first, or obtain an operator-approved destroy")
		}
		if req.Destructive && req.OperatorApprovalRef == "" {
			return out, nil, newFailure(KindInvalidOperation, op, "destructive removal requires the operator approval it would consume", false, "obtain an operator-approved destructive destroy")
		}
	}
	// CD-0118: unstarted work reclaims without approval when the worktree
	// holds nothing a merge could lose. Work that has moved past needed, or
	// an operator-approved removal, are the only ways past this gate.
	if req.RequireUnstarted {
		var lifecycle string
		err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, req.WorkID).Scan(&lifecycle)
		if err == sql.ErrNoRows {
			return out, nil, newFailure(KindUnknownScope, op, "work item does not exist", false, "select one existing work item")
		}
		if err != nil {
			return out, nil, wrapFailure(KindUnavailable, op, "cannot read the work item", true, "retry once the database is readable", err)
		}
		if lifecycle != "needed" && req.OperatorApprovalRef == "" {
			return out, nil, newFailure(KindInvalidTransition, op, "work item is "+lifecycle+", so its worktree is not unstarted work", false, "start, complete, or cancel the work first, or obtain an operator-approved destroy")
		}
	}

	entries, err := worktreeEntriesTx(ctx, tx, req.WorkID)
	if err != nil {
		return out, nil, err
	}
	var entry WorktreeEntry
	for _, candidate := range entries {
		if candidate.ProjectID == req.ProjectID {
			entry = candidate
			break
		}
	}
	if entry.ProjectID == "" {
		return out, nil, newFailure(KindProjectionNotFound, op, "no active worktree for this Project", false, "claim a worktree before reclaiming it")
	}
	if entry.State != worktreeEntryActive && entry.State != worktreeEntryReclaimed {
		return out, nil, newFailure(KindProjectionNotFound, op, "no active worktree for this Project", false, "claim a worktree before reclaiming it")
	}
	// The probes ran against the pre-transaction entry; this identity check
	// is the cheap re-validation that keeps the committed event honest about
	// the worktree it names.
	if entry.Path != probe.Entry.Path || entry.Branch != probe.Entry.Branch || entry.ClaimOpID != probe.Entry.ClaimOpID {
		return out, nil, wrapFailure(KindProjectionConflict, op, "worktree changed under the reclaim probe", true, "retry the same operation", nil)
	}
	// Every event this reclaim appends derives its identity from the claim's
	// incarnation, so a claim row a bootstrap reopen revived records its own
	// events instead of re-deriving its first incarnation's.
	incarnation, err := claimIncarnationTx(ctx, tx, entry.ClaimOpID)
	if err != nil {
		return out, nil, err
	}
	if probe.AlreadyConverged {
		// The reclamation event is already durable. A deletion the recorded
		// plan still owes converges here, and a directory phase that failed
		// re-owes only the removal — with no new event and no re-planned
		// deletions (CD-0212 D4). The replayed removal keeps the recorded
		// plan's force flag and pinned live-HEAD tip; the retry's own
		// destructive declaration grants nothing.
		if len(probe.Deletions) == 0 && !probe.DirectoryPending {
			return entry, nil, nil
		}
		removal := &WorktreeNativeRemoval{
			RepoRoot: probe.RepoRoot, Op: op, BranchDeletions: probe.Deletions, DefaultRef: probe.DefaultRef,
			WorkID: req.WorkID, ProjectID: entry.ProjectID, ClaimOpID: entry.ClaimOpID, ClaimIncarnation: incarnation,
			PrincipalRef: req.PrincipalRef, RequestID: req.RequestID,
		}
		if probe.DirectoryPending {
			removal.Path = entry.Path
			removal.Force = probe.ReplayForce
			removal.LiveTip = probe.LiveHead.Tip
		}
		return entry, removal, nil
	}
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	setID := WorktreeSetID(req.WorkID)
	// A stale projection reconciles against stronger git truth: the probe
	// proved the native worktree already gone, so reclamation records that
	// fact instead of demanding unreachable probes.
	if probe.AlreadyAbsent {
		if err := appendReclaimedTx(ctx, tx, req, setID, entry.ClaimOpID, incarnation, now, probe.factsFor(req)); err != nil {
			return out, nil, err
		}
		final, err := worktreeEntryAfterReclaimTx(ctx, tx, req.WorkID, req.ProjectID)
		return final, nil, err
	}
	// Recorded occupancy is the worktree_occupancy projection: one row per
	// session, with the host process identity recorded by the core from /proc
	// at landing time (CD-0178 D3). The kernel proves whether a process is
	// alive, so a row whose host process started where it now starts stays
	// and blocks removal; a row whose host process ended releases; a legacy
	// row (no process identity) is released when every live host lease
	// started after the row's recorded_at, which proves its recording process
	// ended, and otherwise stays for session_vacate or an operator-approved
	// removal. Operator-approved reclamation
	// (ReleaseOccupancy with OperatorApprovalRef) clears every row of the
	// worktree, and the destructive path appends the
	// approval to the recorded event so an audit trail names the live
	// occupants it released.
	occupants, err := worktreeOccupancyRowsTx(ctx, tx, setID, entry.ProjectID, entry.ClaimOpID)
	if err != nil {
		return out, nil, err
	}
	if err := reclaimOccupancyGateTx(ctx, tx, req, op, entry, setID, occupants, incarnation, now); err != nil {
		return out, nil, err
	}
	if err := appendReclaimedTx(ctx, tx, req, setID, entry.ClaimOpID, incarnation, now, probe.factsFor(req)); err != nil {
		return out, nil, err
	}
	removal := &WorktreeNativeRemoval{
		RepoRoot: probe.RepoRoot, Path: entry.Path, Force: req.Destructive, Op: op, LiveTip: probe.LiveHead.Tip, BranchDeletions: probe.Deletions, DefaultRef: probe.DefaultRef,
		WorkID: req.WorkID, ProjectID: entry.ProjectID, ClaimOpID: entry.ClaimOpID, ClaimIncarnation: incarnation,
		PrincipalRef: req.PrincipalRef, RequestID: req.RequestID,
	}
	final, err := worktreeEntryAfterReclaimTx(ctx, tx, req.WorkID, req.ProjectID)
	return final, removal, err
}

func reclaimOperation(req WorktreeReclaimRequest) string {
	if req.RequireTerminal {
		return "worktree_destroy"
	}
	return "worktree_reclaim"
}

// validateWorktreeReclaimIdentity holds the identity bounds both the probe
// and the transaction re-check, so a malformed request refuses before any
// filesystem or SQL work.
func validateWorktreeReclaimIdentity(req WorktreeReclaimRequest) error {
	if req.WorkID == "" || req.ProjectID == "" || req.PrincipalRef == "" || req.RequestID == "" {
		return newFailure(KindInvalidOperation, "worktree_reclaim", "reclaim operation is missing identity fields", false, "supply work, project, principal, and request ids")
	}
	return nil
}

// worktreeProjectMember is the plain-read membership check the
// pre-transaction probe runs; the transaction re-validates through its own
// handle.
func worktreeProjectMember(ctx context.Context, q queryer, workID, projectID, operation string) error {
	var member bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_projects WHERE work_id=? AND project_id=?)`, workID, projectID).Scan(&member); err != nil {
		return wrapFailure(KindUnavailable, operation, "cannot read work Project membership", true, "retry once the database is readable", err)
	}
	if !member {
		return newFailure(KindUnknownScope, operation, "work item does not hold Project "+projectID, false, "use a Project the work item belongs to")
	}
	return nil
}

func validateWorktreeProjectMembershipTx(ctx context.Context, tx *sql.Tx, workID, projectID, operation string) error {
	var member bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_projects WHERE work_id=? AND project_id=?)`, workID, projectID).Scan(&member); err != nil {
		return wrapFailure(KindUnavailable, operation, "cannot read work Project membership", true, "retry once the database is readable", err)
	}
	if !member {
		return newFailure(KindUnknownScope, operation, "work item does not hold Project "+projectID, false, "use a Project the work item belongs to")
	}
	return nil
}

// releaseSessionWorktreeOccupancyTx releases a session's recorded occupancy
// on the named worktree (CD-0178 D3). The vacate fold appends an
// occupancy_released event whose payload names the same identity, and this
// helper confirms the row exists before the event lands. The worktree's
// claim_op_id resolves the worktree_occupancy row's composite key, since
// path and project alone name no row.
// releaseSessionWorktreeOccupancyTx releases a session's recorded occupancy
// when it vacates a worktree (CD-0178 D3, amended by CD-0179). The host runs
// a session in one directory at a time, so the release is not scoped to the
// vacated worktree: every occupancy row the session holds, in any work item,
// releases with the vacate. Rows another session holds stay: releasing
// someone else's occupancy is not this call's effect.
func releaseSessionWorktreeOccupancyTx(ctx context.Context, tx *sql.Tx, operation, workID, projectID, sourceDirectory, sessionRef string) error {
	var claimOpID string
	err := tx.QueryRowContext(ctx, `SELECT claim_op_id FROM worktree_entries WHERE set_id=? AND project_id=? AND path=? AND state='active'`, WorktreeSetID(workID), projectID, filepath.Clean(sourceDirectory)).Scan(&claimOpID)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, operation, "the worktree is not active", false, "use the active linked worktree")
	}
	if err != nil {
		return err
	}
	worktreeID := worktreeOccupancyID(WorktreeSetID(workID), projectID, claimOpID)
	held, err := sessionHoldsOccupancyTx(ctx, tx, worktreeID, sessionRef)
	if err != nil {
		return err
	}
	if !held {
		// The calling session holds no row here. It may still hold stale rows
		// elsewhere, and the vacate releases those; but a session with no row
		// anywhere refuses while other sessions hold rows on this worktree:
		// a silent no-op would report a release the projection never
		// recorded.
		var mine int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_occupancy WHERE session_ref=?`, sessionRef).Scan(&mine); err != nil {
			return wrapFailure(KindUnavailable, operation, "cannot read worktree occupancy", true, "retry once the database is readable", err)
		}
		if mine == 0 {
			var others int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_occupancy WHERE worktree_id=?`, worktreeID).Scan(&others); err != nil {
				return wrapFailure(KindUnavailable, operation, "cannot read worktree occupancy", true, "retry once the database is readable", err)
			}
			if others > 0 {
				return newFailure(KindWorktreeOwnershipConflict, operation, "session vacate does not own the recorded worktree occupancy", false, "release the worktree from a session recorded as an occupant")
			}
		}
	}
	// One delete clears the vacated worktree's row and every other stale row
	// of this session, in this or any other work item.
	_, err = tx.ExecContext(ctx, `DELETE FROM worktree_occupancy WHERE session_ref=?`, sessionRef)
	return err
}

// refuseWhenSessionOccupiesAnotherWorktreeTx keeps the occupancy projection
// truthful across the claim (CD-0178 D3). A claim cannot clear a recorded row
// safely: the clear would commit with this transaction, while the host
// relocation that makes it true runs afterwards and outside it. A refused
// relocation would then leave a live session in a directory recorded as
// empty, which is the stranding the gate exists to prevent. Occupancy
// therefore moves only through a verified landing: a sibling Project of the
// same work item is admitted, and the claim commit leaves every source row
// occupied until claim-landing records the verified transfer. Another work
// item's worktree still refuses, because a claim there has no landing record
// that could make the move true, so the session must vacate first.
func refuseWhenSessionOccupiesAnotherWorktreeTx(ctx context.Context, tx *sql.Tx, sessionRef, destinationSetID string) error {
	if sessionRef == "" {
		return nil
	}
	// Resolve the session's other active worktree rows through the
	// worktree_occupancy projection: a row whose worktree_id starts with the
	// destination's set_id belongs to this work item's other Projects and is
	// admissible; anything else belongs to a different work item and
	// refuses. The set_id prefix is the prefix the worktree_occupancy row's
	// composite key carries, and a row outside it has no claim event of
	// its own to clear it.
	rows, err := tx.QueryContext(ctx, `
		SELECT e.path
		  FROM worktree_occupancy o
		  JOIN worktree_entries e ON e.set_id || ':' || e.project_id || ':' || e.claim_op_id = o.worktree_id
		 WHERE e.state='active'
		   AND o.session_ref=?
		   AND substr(o.worktree_id, 1, length(?)) <> ?
		 ORDER BY e.path LIMIT 1`, sessionRef, destinationSetID, destinationSetID)
	if err != nil {
		return wrapFailure(KindUnavailable, "worktree_claim", "cannot read session occupancy", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return rows.Err()
	}
	var occupiedPath string
	if err := rows.Scan(&occupiedPath); err != nil {
		return err
	}
	return newFailure(KindWorktreeOwnershipConflict, "worktree_claim", fmt.Sprintf("the calling session still occupies another work item's active worktree at %s", occupiedPath), false, "vacate the occupied worktree with session_vacate before claiming another")
}

// sessionClaimLandedPayload is the durable record of one verified claim
// landing. The host moved the session and read its directory back as the
// claimed path before this event exists, so the event is evidence of a
// landing, never an intention to land. HostPID is the OpenCode process that
// recorded the landing; HostPIDStart is the value the core derived from
// /proc at record time so the recorded identity survives the host process
// later dying (CD-0178 D3). ClaimOpID names the claim generation the landing
// verified, so a placement derived from the event binds to its own claim and
// never to a landing a reclaim or a vacate plus re-claim superseded.
type sessionClaimLandedPayload struct {
	WorkID            string   `json:"work_id"`
	ProjectID         string   `json:"project_id"`
	ClaimOpID         string   `json:"claim_op_id"`
	SessionRef        string   `json:"session_ref"`
	SourceDirectories []string `json:"source_directories"`
	LandedDirectory   string   `json:"landed_directory"`
	HostPID           int      `json:"host_pid"`
	HostPIDStart      uint64   `json:"host_pid_start"`
}

// WorktreeClaimLandingRequest records the verified landing of a session in a
// claimed worktree. The adapter-only claim-landing verb is the
// caller: the host proves the landing by readback, and the landing adds the
// session's own occupancy row whatever rows other sessions hold (CD-0178
// D3). HostPID carries the OpenCode process the adapter ran
// in; the core derives pid_start itself through hostlease.ProcessStart and
// never trusts a caller-supplied start time (CD-0178 D3).
type WorktreeClaimLandingRequest struct {
	WorkID          string
	SessionRef      string
	LandedDirectory string
	HostPID         int
	Now             time.Time
}

// WorktreeClaimLandingResult names the transferred landing. ReleasedSources
// holds the other active rows of the same work item whose occupancy the
// landing cleared.
type WorktreeClaimLandingResult struct {
	WorkID          string   `json:"work_id"`
	ProjectID       string   `json:"project_id"`
	SessionRef      string   `json:"session_ref"`
	LandedDirectory string   `json:"landed_directory"`
	ReleasedSources []string `json:"released_sources,omitempty"`
	AlreadyRecorded bool     `json:"already_recorded"`
	HostPID         int      `json:"host_pid"`
	HostPIDStart    uint64   `json:"host_pid_start"`
}

// RecordWorktreeClaimLanding transfers the calling session's occupancy onto
// the claimed worktree in one transaction: the destination row must be active
// and occupied by this session, or record no occupant — the shape a resumed
// session lands in, because the read that derives its worktree records
// nothing (CD-0104 D1) — and every other active row the session occupies, in
// this or any other work item, clears: the host runs a session in one
// directory at a time, so the verified landing proves those rows stale
// (CD-0179). One durable event names the session, work item, source paths,
// landed path, and the host process identity the core recorded. A
// destination that is absent, inactive, or occupied by another session
// refuses before any effect. The same landing replays idempotently. The host
// pid is the only identity the adapter carries; the core reads pid_start
// itself through hostlease.ProcessStart and stores the result alongside it
// (CD-0178 D3).
func (s *Store) RecordWorktreeClaimLanding(ctx context.Context, req WorktreeClaimLandingRequest) (WorktreeClaimLandingResult, error) {
	return recordLanding(s, ctx, "claim-landing", req.WorkID, req.SessionRef, req.LandedDirectory, req.HostPID, func(ctx context.Context, tx *sql.Tx) (WorktreeClaimLandingResult, error) {
		return recordWorktreeClaimLandingTx(ctx, tx, req)
	})
}

func recordWorktreeClaimLandingTx(ctx context.Context, tx *sql.Tx, req WorktreeClaimLandingRequest) (WorktreeClaimLandingResult, error) {
	out := WorktreeClaimLandingResult{WorkID: req.WorkID, SessionRef: req.SessionRef, LandedDirectory: filepath.Clean(req.LandedDirectory), HostPID: req.HostPID}
	// The kernel is the authority for the host process start time. Reading
	// here keeps hostlease imports out of every caller and lets the fold
	// replay the recorded value without touching /proc.
	pidStart, err := hostlease.ProcessStart(req.HostPID)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "claim-landing", "cannot read the host process start time", true, "retry once the host process is observable", err)
	}
	out.HostPIDStart = pidStart
	var projectID, claimOpID string
	err = tx.QueryRowContext(ctx, `SELECT project_id, claim_op_id FROM worktree_entries WHERE set_id=? AND path=? AND state='active'`, WorktreeSetID(req.WorkID), out.LandedDirectory).Scan(&projectID, &claimOpID)
	if err == sql.ErrNoRows {
		return out, newFailure(KindProjectionNotFound, "claim-landing", "the landed path is not an active worktree of this work item", false, "land in the derived path the claim returned")
	}
	if err != nil {
		return out, wrapFailure(KindUnavailable, "claim-landing", "cannot read the claimed worktree", true, "retry once the database is readable", err)
	}
	// The landing reads only the calling session's own row. CD-0178 D3: a
	// worktree holds one occupancy row per session, and a landing adds a row
	// and never refuses because another session holds one.
	held, err := sessionHoldsOccupancyTx(ctx, tx, worktreeOccupancyID(WorktreeSetID(req.WorkID), projectID, claimOpID), req.SessionRef)
	if err != nil {
		return out, err
	}
	out.ProjectID = projectID
	sources, err := sessionOccupiedSourcesTx(ctx, tx, req.WorkID, req.SessionRef, projectID, out.LandedDirectory)
	if err != nil {
		return out, err
	}
	// Replay is read from state, not from a derived event id: the claimed
	// path per Project is deterministic, so an id naming the path would own
	// the replay for the work item's whole life, and after a reclaim and a
	// new claim at the same derived path a real second transfer would be
	// skipped while the session's sources stay occupied under a success
	// report. The replay is a recorded landing, never a bare occupancy row:
	// a claim carries its session's row from creation (CD-0179), and that
	// row proves nothing about where the session runs until this landing
	// verifies it. The replay is scoped to this claim generation: a landing
	// recorded for a superseded claim — after a reclaim, or a vacate plus
	// re-claim that left the session with no occupied source row — proves
	// nothing about the current claim, so a re-claim in this or another
	// Project records its own first landing. When this session already holds
	// the destination row, no other row of this work item, and a recorded
	// landing for this claim stands, the projection already holds the
	// landing and the same landing replays idempotently with no event.
	claimRecorded, err := countSessionClaimLandingsForClaimTx(ctx, tx, req.WorkID, req.SessionRef, claimOpID)
	if err != nil {
		return out, err
	}
	if held && len(sources) == 0 && claimRecorded > 0 {
		out.AlreadyRecorded = true
		return out, nil
	}
	out.ReleasedSources = sources
	// One transfer is the unit of identity: the recorded landing count for
	// this work item and session is the ordinal of this event, so a second
	// landing at the same derived path records its own transfer. The count
	// runs inside this transaction, mirroring the vacate ordinal in
	// CD-0120 D4.
	recorded, err := countSessionClaimLandingsTx(ctx, tx, req.WorkID, req.SessionRef)
	if err != nil {
		return out, err
	}
	eventID := fmt.Sprintf("%s:session-claim-landed:%s:%d", req.WorkID, req.SessionRef, recorded+1)
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	payload, err := json.Marshal(sessionClaimLandedPayload{WorkID: req.WorkID, ProjectID: projectID, ClaimOpID: claimOpID, SessionRef: req.SessionRef, SourceDirectories: sources, LandedDirectory: out.LandedDirectory, HostPID: req.HostPID, HostPIDStart: pidStart})
	if err != nil {
		return out, err
	}
	if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: eventID, Kind: "work.session_claim_landed", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.SessionRef, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}}, newFoldScope(tx), false); err != nil {
		return out, err
	}
	return out, nil
}

// sessionHoldsOccupancyTx reports whether the named session holds an
// occupancy row on one worktree. CD-0178 D3 makes the row set per session:
// the landing and the vacate read the calling session's own row, and
// another session's row is never a gate input for them.
func sessionHoldsOccupancyTx(ctx context.Context, tx *sql.Tx, worktreeID, sessionRef string) (bool, error) {
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_occupancy WHERE worktree_id=? AND session_ref=?`, worktreeID, sessionRef).Scan(&held); err != nil {
		return false, wrapFailure(KindUnavailable, "worktree_occupancy", "cannot read worktree occupancy", true, "retry once the database is readable", err)
	}
	return held > 0, nil
}

// countSessionClaimLandingsTx returns how many landing events the projection
// already records for one work item and session. It runs inside the caller's
// transaction so the read observes the caller's own uncommitted events, and
// the count is the ordinal of the next transfer event.
func countSessionClaimLandingsTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id=? AND json_extract(payload,'$.session_ref')=?`, workID, sessionRef).Scan(&count)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "claim-landing", "cannot count the recorded landing events", true, "retry once the database is readable", err)
	}
	return count, nil
}

// countSessionClaimLandingsForClaimTx returns how many landing events the
// projection already records for one claim generation — the claim op the
// landed entry carries. A landing recorded for a superseded claim proves
// nothing about the current claim, so this count, not the work-wide count,
// decides whether a landing replays. It runs inside the caller's
// transaction.
func countSessionClaimLandingsForClaimTx(ctx context.Context, tx *sql.Tx, workID, sessionRef, claimOpID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id=? AND json_extract(payload,'$.session_ref')=? AND json_extract(payload,'$.claim_op_id')=?`, workID, sessionRef, claimOpID).Scan(&count)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "claim-landing", "cannot count the claim's recorded landing events", true, "retry once the database is readable", err)
	}
	return count, nil
}

// sessionOccupiedSourcesTx lists the active rows the session occupies outside
// the landing destination, in stable path order, before the landing clears
// them (CD-0178 D3, amended by CD-0179). The host runs a session in one
// directory at a time, so the scan is not scoped to the landing's work item:
// every active worktree the session holds, in any work item, is a stale
// source of the transfer, and the destination row itself is excluded by
// identity.
func sessionOccupiedSourcesTx(ctx context.Context, tx *sql.Tx, workID, sessionRef, landedProjectID, landedDirectory string) ([]string, error) {
	// Resolve the destination's claim_op_id so the source query can exclude
	// the row the landing is transferring into. The directory alone is not
	// a worktree_occupancy key.
	var landedClaimOpID string
	if err := tx.QueryRowContext(ctx, `SELECT claim_op_id FROM worktree_entries WHERE set_id=? AND project_id=? AND path=? AND state='active'`, WorktreeSetID(workID), landedProjectID, landedDirectory).Scan(&landedClaimOpID); err != nil && err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, "claim-landing", "cannot resolve the destination claim", true, "retry once the database is readable", err)
	}
	destinationID := ""
	if landedClaimOpID != "" {
		destinationID = worktreeOccupancyID(WorktreeSetID(workID), landedProjectID, landedClaimOpID)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT e.path
		  FROM worktree_occupancy o
		  JOIN worktree_entries e ON e.set_id || ':' || e.project_id || ':' || e.claim_op_id = o.worktree_id
		 WHERE e.state='active'
		   AND o.session_ref=?
		   AND (? = '' OR o.worktree_id <> ?)
		 ORDER BY e.path`, sessionRef, destinationID, destinationID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "claim-landing", "cannot read the session's occupied worktrees", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var sources []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		sources = append(sources, path)
	}
	return sources, rows.Err()
}

// foldSessionClaimLanded re-applies the verified transfer during rebuild: the
// landed row holds the session — a claim-carried landing finds it held
// already, and a resumed landing records the session its host move verified —
// and the session's occupancy on every other active row, in any work item,
// clears (CD-0178 D3, amended by CD-0179). The fold trusts the recorded
// host_pid_start: the value was derived from /proc at record time and
// survives a process that later died, so replay neither re-derives nor
// re-reads it.
func foldSessionClaimLanded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p sessionClaimLandedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.WorkID == "" || p.WorkID != event.SubjectID || p.ProjectID == "" || p.SessionRef == "" || p.LandedDirectory == "" {
		return newFailure(KindInvalidPayload, "fold_event", "session claim landed payload is missing required fields", false, "supply work, project, session, and landed directory")
	}
	if p.HostPID <= 0 {
		return newFailure(KindInvalidPayload, "fold_event", "session claim landed payload is missing host_pid", false, "supply the host process pid the adapter recorded")
	}
	// Resolve the destination worktree row: the landing event names the
	// Project and the directory, but the worktree_occupancy row's
	// composite key needs the claim_op_id the durable claim recorded.
	var claimOpID string
	if err := tx.QueryRowContext(ctx, `SELECT claim_op_id FROM worktree_entries WHERE set_id=? AND project_id=? AND path=? AND state='active'`, WorktreeSetID(p.WorkID), p.ProjectID, filepath.Clean(p.LandedDirectory)).Scan(&claimOpID); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "fold_event", "landed path has no active worktree row to attach occupancy to", false, "rebuild the worktree row before re-folding")
		}
		return err
	}
	// A recorded claim op binds the landing to the claim generation it
	// verified: the landed path's current claim op must still be that
	// generation, because a reclaim that supersedes the claim folds after
	// the landing it retires. Payloads recorded before the field existed
	// carry no claim op and keep the path resolution alone.
	if p.ClaimOpID != "" && p.ClaimOpID != claimOpID {
		return newFailure(KindInvalidPayload, "fold_event", "session claim landed payload names a claim generation the landed path no longer carries", false, "rebuild projections from the event log")
	}
	// Insert (or replace) the destination occupancy row with the recorded
	// host identity. A prior legacy row from worktree_created for the same
	// session is upgraded to carry pid and pid_start; the conflict update
	// keeps the recorded_at timestamp the fold is replaying so the
	// projection never appears to recede.
	destinationID := worktreeOccupancyID(WorktreeSetID(p.WorkID), p.ProjectID, claimOpID)
	if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity) VALUES(?,?,?,?,?,1)
		ON CONFLICT(worktree_id, session_ref) DO UPDATE SET recorded_at=excluded.recorded_at, host_pid=excluded.host_pid, host_pid_start=excluded.host_pid_start, has_process_identity=1`,
		destinationID, p.SessionRef, event.OccurredAt.Format(time.RFC3339Nano), p.HostPID, p.HostPIDStart); err != nil {
		return err
	}
	// Clear the session's other occupancy rows, in any work item: the host
	// runs a session in one directory at a time, so every row outside the
	// landing destination is stale the moment the landing is verified
	// (CD-0179). The destination row is excluded by identity.
	if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_occupancy
		 WHERE session_ref=?
		   AND worktree_id <> ?`,
		p.SessionRef, destinationID); err != nil {
		return err
	}
	return nil
}

// worktreeRepoRootTx resolves the repository to create from the Project's
// canonical_path locator. It reads through the claim's own transaction; the
// outer write lock makes a second connection's read deadlock on SQLite's
// single writer.
func worktreeRepoRootTx(ctx context.Context, tx queryer, req WorktreeClaimRequest) (string, error) {
	var normalized string
	err := tx.QueryRowContext(ctx, `SELECT normalized_value FROM project_locators WHERE kind=? AND project_id=? ORDER BY locator_id LIMIT 1`, LocatorCanonicalPath, req.ProjectID).Scan(&normalized)
	if err == sql.ErrNoRows {
		return "", newFailure(KindUnknownScope, "worktree_claim", "Project has no canonical_path locator", false, "register the repository's canonical path locator")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "worktree_claim", "cannot read Project locators", true, "retry once the database is readable", err)
	}
	return normalized, nil
}

// ValidateWorktreeClaimIntent validates a derived claim locator for callers
// that need a preflight check before native worktree creation.
func ValidateWorktreeClaimIntent(branch, baseSHA, path string) error {
	if !worktreeBranchPattern.MatchString(branch) {
		return newFailure(KindInvalidOperation, "worktree_claim", "branch is not a bounded git ref name", false, "supply a plain branch name without spaces or shell characters")
	}
	if !worktreeSHAPattern.MatchString(baseSHA) {
		return newFailure(KindInvalidOperation, "worktree_claim", "base is not a full commit SHA", false, "pin the exact base commit SHA")
	}
	if !filepath.IsAbs(path) {
		return newFailure(KindInvalidOperation, "worktree_claim", "worktree path must be absolute", false, "supply an absolute filesystem path")
	}
	return nil
}

func worktreeEntryByClaim(ctx context.Context, q queryer, opID string) (WorktreeEntry, error) {
	var e WorktreeEntry
	var facts string
	var reclaimed sql.NullString
	err := q.QueryRowContext(ctx, `SELECT set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts FROM worktree_entries WHERE claim_op_id=?`, opID).
		Scan(&e.SetID, &e.ProjectID, &e.ClaimOpID, &e.Branch, &e.BaseSHA, &e.Path, &e.RepositoryID, &e.State, &e.VerifiedAt, &reclaimed, &facts)
	e.ReclaimedAt = reclaimed.String
	if err == sql.ErrNoRows {
		return e, newFailure(KindProjectionNotFound, "worktree_claim", "verified claim has no folded entry", false, "contact_operator")
	}
	if err != nil {
		return e, wrapFailure(KindUnavailable, "worktree_claim", "cannot read worktree entry", true, "retry once the database is readable", err)
	}
	e.GitFacts = json.RawMessage(facts)
	return e, nil
}

type worktreeFacts struct {
	branch       string
	headSHA      string
	repositoryID string
}

func worktreeBranchHead(ctx context.Context, runner GitRunner, repoRoot, branch string) (string, bool, error) {
	ref := "refs/heads/" + branch
	if _, err := runner.Run(ctx, repoRoot, "show-ref", "--verify", "--quiet", ref); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, wrapFailure(KindGitUnreachable, "worktree_claim", "cannot inspect the existing branch", true, "retry once the repository is reachable", err)
	}
	out, err := runner.Run(ctx, repoRoot, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", false, wrapFailure(KindGitUnreachable, "worktree_claim", "cannot resolve the existing branch", true, "retry once the repository is reachable", err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

func (f worktreeFacts) raw() json.RawMessage {
	payload, _ := json.Marshal(map[string]any{"branch": f.branch, "head_sha": f.headSHA})
	return payload
}

// probeWorktree reports whether path is a worktree of repoRoot on branch with
// base as an ancestor of its head. It fails closed on unreachable git.
func probeWorktree(ctx context.Context, runner GitRunner, repoRoot, path, branch, base string) (bool, worktreeFacts, error) {
	branchOut, branchErr := runner.Run(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
	if branchErr != nil {
		return false, worktreeFacts{}, nil
	}
	if strings.TrimSpace(string(branchOut)) != branch {
		return false, worktreeFacts{}, nil
	}
	headOut, headErr := runner.Run(ctx, path, "rev-parse", "HEAD")
	if headErr != nil {
		return false, worktreeFacts{}, nil
	}
	head := strings.TrimSpace(string(headOut))
	commonOut, commonErr := runner.Run(ctx, path, "rev-parse", "--git-common-dir")
	if commonErr != nil {
		return false, worktreeFacts{}, newFailure(KindGitUnreachable, "worktree_claim", "cannot determine worktree topology during verification", true, "retry once the worktree is reachable")
	}
	common := strings.TrimSpace(string(commonOut))
	if !filepath.IsAbs(common) {
		common = filepath.Join(path, common)
	}
	commonRoot := filepath.Dir(common)
	rootOut, rootErr := runner.Run(ctx, repoRoot, "rev-parse", "--show-toplevel")
	if rootErr != nil {
		return false, worktreeFacts{}, newFailure(KindGitUnreachable, "worktree_claim", "cannot resolve the repository root during verification", true, "retry once the repository is reachable")
	}
	root := strings.TrimSpace(string(rootOut))
	canonicalCommon, ccErr := normalizePath(commonRoot)
	canonicalRoot, crErr := normalizePath(root)
	if ccErr != nil || crErr != nil || canonicalCommon != canonicalRoot {
		return false, worktreeFacts{}, nil
	}
	if _, err := runner.Run(ctx, repoRoot, "merge-base", "--is-ancestor", base, head); err != nil {
		return false, worktreeFacts{}, nil
	}
	return true, worktreeFacts{branch: branch, headSHA: head, repositoryID: canonicalRoot}, nil
}

// branchIsDurable reports whether the named revision's content survives the
// reclaim. The remote-ref count stays the first way to pass: every commit
// reachable from the revision is also reachable from a local remote-tracking
// ref. The second way is squash containment (CD-0181): the default ref holds
// a commit whose git patch-id --stable equals the revision's net diff from
// its merge base, the shape a squash merge leaves behind after the remote
// head branch is deleted. It protects the sole copy of a commit without
// requiring the branch to merge cleanly into main. label names the revision
// in the refusal detail, so a live head and a stored ref each report as
// themselves.
func branchIsDurable(ctx context.Context, runner GitRunner, repoRoot, committish, label, defaultRef, op string) (bool, error) {
	countOut, err := runner.Run(ctx, repoRoot, "rev-list", "--count", committish, "--not", "--remotes")
	if err != nil {
		return false, wrapFailure(KindGitUnreachable, op, "cannot count commits not reachable from remote refs for "+committish, true, "retry once the repository is reachable", err)
	}
	count, parseErr := strconv.Atoi(strings.TrimSpace(string(countOut)))
	if parseErr != nil || count < 0 {
		return false, newFailure(KindGitUnreachable, op, "local Git returned an invalid durable commit count for "+committish, false, "repair the repository refs before reclaiming")
	}
	if count == 0 {
		return false, nil
	}
	if defaultRef == "" {
		// The squash containment needs the merge target. Resolution is
		// best-effort here: without it the count alone decides, which is
		// the refusal the pre-CD-0181 gate produced.
		if refOut, refErr := runner.Run(ctx, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD"); refErr == nil && strings.TrimSpace(string(refOut)) != "" {
			defaultRef = strings.TrimPrefix(strings.TrimSpace(string(refOut)), "refs/remotes/")
		}
	}
	if branchSquashContained(ctx, runner, repoRoot, committish, defaultRef) {
		return true, nil
	}
	return false, newFailure(KindInvalidOperation, op, label+" holds "+strconv.Itoa(count)+" commit(s) not reachable from remote refs", false, "push or otherwise preserve the commits before reclaiming")
}

// branchSquashContained reports whether the default ref holds a commit whose
// git patch-id --stable equals the branch's net diff from its merge base
// (CD-0181). Both sides of the comparison are fixed commits, so the answer
// cannot decay. Every probe that cannot run, and an empty net diff, refuses:
// the check can only relax the durable count, never replace it.
func branchSquashContained(ctx context.Context, runner GitRunner, repoRoot, branch, defaultRef string) bool {
	stdinRunner, ok := runner.(StdinGitRunner)
	if !ok || defaultRef == "" {
		return false
	}
	baseOut, err := runner.Run(ctx, repoRoot, "merge-base", defaultRef, branch)
	if err != nil {
		return false
	}
	mergeBase := strings.TrimSpace(string(baseOut))
	diffOut, err := runner.Run(ctx, repoRoot, "diff", mergeBase, branch)
	if err != nil || len(diffOut) == 0 {
		return false
	}
	pidOut, err := stdinRunner.RunStdin(ctx, repoRoot, diffOut, "patch-id", "--stable")
	if err != nil {
		return false
	}
	pid := firstDiffField(pidOut)
	if pid == "" {
		return false
	}
	revOut, err := runner.Run(ctx, repoRoot, "rev-list", "--no-merges", mergeBase+".."+defaultRef)
	if err != nil {
		return false
	}
	patchOut, err := stdinRunner.RunStdin(ctx, repoRoot, revOut, "diff-tree", "--patch", "--stdin")
	if err != nil {
		return false
	}
	idsOut, err := stdinRunner.RunStdin(ctx, repoRoot, patchOut, "patch-id", "--stable")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(idsOut), "\n") {
		if field := firstDiffField([]byte(line)); field == pid {
			return true
		}
	}
	return false
}

// firstDiffField returns the first whitespace-separated field of a git
// patch-id output line: the patch-id itself, with the commit-id the
// diff-tree input carries as the second field.
func firstDiffField(out []byte) string {
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// branchHasNoCommitsBeyond reports whether the named revision holds no commit
// the default ref does not already hold. The unstarted tier's safety question
// is narrower than the merged question: nothing may exist to lose, so tree
// identity — which a branch of commit-and-revert pairs can satisfy while
// still holding commits — cannot answer it. label names the revision in the
// refusal detail.
func branchHasNoCommitsBeyond(ctx context.Context, runner GitRunner, repoRoot, committish, label, defaultRef, op string) error {
	countOut, err := runner.Run(ctx, repoRoot, "rev-list", "--count", defaultRef+".."+committish)
	if err != nil {
		return wrapFailure(KindGitUnreachable, op, "cannot count commits beyond "+defaultRef, true, "retry once the repository is reachable", err)
	}
	count := strings.TrimSpace(string(countOut))
	if count != "0" {
		return newFailure(KindInvalidOperation, op, label+" holds "+count+" commit(s) beyond "+defaultRef, false, "merge or remove the commits before reclaiming, or obtain an operator-approved destroy")
	}
	return nil
}

func jsonMustMarshal(v any) json.RawMessage {
	out, _ := json.Marshal(v)
	return out
}

// reclaimedEventID is the one derivation of the reclaimed event's stable
// identity. The identity binds the event to one claim generation and
// incarnation, so a reclaim retry of the same incarnation re-derives the same
// event_id while a later incarnation derives its own.
func reclaimedEventID(workID, projectID, claimOpID string, incarnation int) string {
	return claimIncarnationEventID(fmt.Sprintf("%s:%s:%s:worktree-reclaimed", workID, projectID, claimOpID), incarnation)
}

// occupancyReleasedEventID derives the occupancy release event's stable
// identity under the same incarnation scoping as reclaimedEventID, with
// the released session_ref appended so two sessions released from the same
// worktree under the same incarnation still receive distinct event ids
// (CD-0178 D3).
func occupancyReleasedEventID(workID, projectID, claimOpID, sessionRef string, incarnation int) string {
	return claimIncarnationEventID(fmt.Sprintf("%s:%s:%s:worktree-occupancy-released:%s", workID, projectID, claimOpID, sessionRef), incarnation)
}

// claimIncarnationEventID scopes one base event identity to a claim
// incarnation. The first incarnation keeps the legacy identity byte for byte;
// a reopened incarnation appends its own :i<N> suffix so its events never
// re-derive an earlier incarnation's identity.
func claimIncarnationEventID(base string, incarnation int) string {
	if incarnation <= 0 {
		return base
	}
	return fmt.Sprintf("%s:i%d", base, incarnation)
}

// claimIncarnationTx reads the claim row's incarnation count. Rows written
// before the column existed, and a reclaim of an entry whose claim row is
// absent, hold the first incarnation.
func claimIncarnationTx(ctx context.Context, q queryer, claimOpID string) (int, error) {
	var incarnation int
	err := q.QueryRowContext(ctx, `SELECT incarnation FROM worktree_claims WHERE op_id=?`, claimOpID).Scan(&incarnation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return incarnation, err
}

func appendReclaimedTx(ctx context.Context, tx *sql.Tx, req WorktreeReclaimRequest, setID, claimOpID string, incarnation int, now time.Time, facts json.RawMessage) error {
	payload, _ := json.Marshal(worktreeReclaimedPayload{ExpectedVersion: req.ExpectedVersion, ResultingVersion: req.ExpectedVersion + 1, SetID: setID, ProjectID: req.ProjectID, ClaimOpID: claimOpID, ClaimIncarnation: incarnation, RequestID: req.RequestID, GitFacts: facts})
	_, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: reclaimedEventID(req.WorkID, req.ProjectID, claimOpID, incarnation), Kind: "work.worktree_reclaimed", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.PrincipalRef, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion}}, newFoldScope(tx), false)
	if err != nil {
		return convergeRecordedReclaimTx(ctx, tx, req, setID, claimOpID, incarnation, err)
	}
	return err
}

// convergeRecordedReclaimTx resolves a reclaim whose derived event_id is
// already recorded for the same claim generation and incarnation. The stored
// row is the durable reclaim record and its worktree projection never folded,
// so every live reclaim of that incarnation re-derives the same identity and
// the append refuses the payload divergence forever. The fold runs against
// the stored event instead, and the reclaim proceeds to its native removal.
// The log row itself is never touched.
//
// The interpretation is deliberately narrow: only a work.worktree_reclaimed
// event for this work item whose payload names this set, Project, claim
// generation, and claim incarnation converges. The stored effect is the
// durable reclaim record, so the fold may proceed for a later native removal.
// Any other collision — another kind, another subject, another claim
// generation, or another incarnation — keeps the divergent-reuse refusal
// classifyEventIDConflict produced.
//
// When the stored payload's resulting_version is at or below the work item's
// current version, the fold repairs the projection without advancing the
// version: that advance is already public history. Above it, the stored
// advance is still pending and the fold's own version rule applies.
func convergeRecordedReclaimTx(ctx context.Context, tx *sql.Tx, req WorktreeReclaimRequest, setID, claimOpID string, incarnation int, appendErr error) error {
	var failure *Failure
	if !failureAs(appendErr, &failure) || (failure.Kind != KindIdempotencyConflict && failure.Kind != KindDuplicateEvent) {
		return appendErr
	}
	eventID := reclaimedEventID(req.WorkID, req.ProjectID, claimOpID, incarnation)
	var kind, subjectType, subjectID string
	var payloadVersion int
	var payload []byte
	var occurredAt string
	err := tx.QueryRowContext(ctx,
		`SELECT kind,subject_type,subject_id,payload_version,payload,occurred_at FROM domain_events WHERE event_id=?`, eventID).
		Scan(&kind, &subjectType, &subjectID, &payloadVersion, &payload, &occurredAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return appendErr
		}
		return err
	}
	storedAt, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return newFailure(KindInvalidPayload, "converge_recorded_reclaim", "stored reclaim event has an unparsable occurred_at", false, "repair the stored event timestamp")
	}
	stored := Event{EventID: eventID, Kind: kind, SubjectType: SubjectType(subjectType), SubjectID: subjectID, PayloadVersion: payloadVersion, Payload: payload, OccurredAt: storedAt}
	if stored.Kind != "work.worktree_reclaimed" || stored.SubjectType != SubjectWorkItem || stored.SubjectID != req.WorkID {
		return appendErr
	}
	var p worktreeReclaimedPayload
	if err := decodePayload(stored, &p); err != nil {
		return appendErr
	}
	if p.SetID != setID || p.ProjectID != req.ProjectID || p.ClaimOpID != claimOpID || p.ClaimIncarnation != incarnation {
		return appendErr
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, req.WorkID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return appendErr
		}
		return err
	}
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	foldErr := foldWorktreeReclaimedTx(ctx, tx, stored, p, current < p.ResultingVersion)
	leaveErr := leaveFold(ctx, tx)
	if foldErr != nil {
		return foldErr
	}
	return leaveErr
}

// releaseWorktreeOccupancyTx is the single-row release the reclaim path
// runs when process liveness proves the row's host process has ended, or
// when an operator-approved removal consumes the row. The release gate reads
// process liveness only (CD-0178 D3): the kernel is the authority, and the
// kernel-derived pid_start is what makes that decision honest. The release
// event names the recorded session.
func releaseWorktreeOccupancyRowTx(ctx context.Context, tx *sql.Tx, req WorktreeReclaimRequest, setID, projectID, claimOpID, sessionRef string, incarnation int, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM worktree_entries WHERE set_id=? AND project_id=? AND claim_op_id=? AND state='active'`, setID, projectID, claimOpID); err != nil {
		return err
	}
	payload, _ := json.Marshal(worktreeOccupancyReleasedPayload{SetID: setID, ProjectID: projectID, ClaimOpID: claimOpID, ClaimIncarnation: incarnation, SessionRef: sessionRef})
	eventID := occupancyReleasedEventID(req.WorkID, projectID, claimOpID, sessionRef, incarnation)
	if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID:        eventID,
		Kind:           "work.worktree_occupancy_released",
		SubjectType:    SubjectWorkItem,
		SubjectID:      req.WorkID,
		Actor:          req.PrincipalRef,
		OccurredAt:     now,
		PayloadVersion: 1,
		Payload:        payload,
	}}}, newFoldScope(tx), false); err != nil {
		return err
	}
	return nil
}

// reclaimOccupancyGateTx applies the occupancy release rules one reclaim or
// removal must satisfy before it may take the worktree (CD-0178 D3, CD-0179
// D3). A row whose recorded process is live, and a legacy row the lease-set
// proof has not ended, stay and block removal by naming the sessions that
// would be stranded. Rows whose process ended release; a legacy row releases
// when every live host lease started after the row was recorded, which
// proves its recording process ended. An unreadable lease set releases
// nothing. Operator-approved reclamation (ReleaseOccupancy with
// OperatorApprovalRef) clears every row the liveness pass left, and each
// recorded release event names its session, so the removal record names
// every live occupant the approval consumed.
func reclaimOccupancyGateTx(ctx context.Context, tx *sql.Tx, req WorktreeReclaimRequest, op string, entry WorktreeEntry, setID string, occupants []WorktreeOccupant, incarnation int, now time.Time) error {
	if len(occupants) == 0 {
		return nil
	}
	liveOccupants := []string{}
	for _, occ := range occupants {
		if occ.HasProcessIdentity {
			start, err := hostlease.ProcessStart(int(*occ.HostPID))
			if err == nil && start == *occ.HostPIDStart {
				liveOccupants = append(liveOccupants, occ.SessionRef)
				continue
			}
			// Process is no longer live as recorded. Release the row.
			if err := releaseWorktreeOccupancyRowTx(ctx, tx, req, setID, entry.ProjectID, entry.ClaimOpID, occ.SessionRef, incarnation, now); err != nil {
				return err
			}
			continue
		}
		// Legacy row without process identity (CD-0179): released when
		// every live host lease started after the row's recorded_at,
		// which proves its recording process ended. An unreadable lease
		// set releases nothing, and so does any live lease that
		// predates the row. Only session_vacate or an operator-approved
		// removal releases such a row while that proof is missing.
		if legacyOccupancyRowEnded(req.HostLeases, occ.RecordedAt) {
			if err := releaseWorktreeOccupancyRowTx(ctx, tx, req, setID, entry.ProjectID, entry.ClaimOpID, occ.SessionRef, incarnation, now); err != nil {
				return err
			}
			continue
		}
		liveOccupants = append(liveOccupants, occ.SessionRef)
	}
	if len(liveOccupants) == 0 {
		return nil
	}
	if req.ReleaseOccupancy && req.OperatorApprovalRef != "" {
		for _, sessionRef := range liveOccupants {
			if err := releaseWorktreeOccupancyRowTx(ctx, tx, req, setID, entry.ProjectID, entry.ClaimOpID, sessionRef, incarnation, now); err != nil {
				return err
			}
		}
		return nil
	}
	return newRouteFailure(KindWorktreeOwnershipConflict, op,
		fmt.Sprintf("sessions %s occupy worktree %s; removing it would strand those sessions", strings.Join(liveOccupants, ", "), entry.Path),
		false, "session_vacate", "worktree_reclaim")
}

// worktreeOccupancyRowsTx reads every occupancy row the named worktree holds,
// in recorded_at order. The caller decides whether each row stays (live
// process, legacy row without operator approval) or releases (dead process,
// operator-approved release path). CD-0178 D3.
func worktreeOccupancyRowsTx(ctx context.Context, tx *sql.Tx, setID, projectID, claimOpID string) ([]WorktreeOccupant, error) {
	worktreeID := worktreeOccupancyID(setID, projectID, claimOpID)
	rows, err := tx.QueryContext(ctx, `SELECT session_ref, recorded_at, host_pid, host_pid_start, has_process_identity FROM worktree_occupancy WHERE worktree_id=? ORDER BY recorded_at, session_ref`, worktreeID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worktree_occupancy", "cannot read worktree occupancy", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []WorktreeOccupant
	for rows.Next() {
		var occ WorktreeOccupant
		occ.WorktreeID = worktreeID
		var hostPID sql.NullInt64
		var hostPIDStart sql.NullInt64
		var hasIdentity int
		if err := rows.Scan(&occ.SessionRef, &occ.RecordedAt, &hostPID, &hostPIDStart, &hasIdentity); err != nil {
			return nil, err
		}
		if hostPID.Valid {
			v := hostPID.Int64
			occ.HostPID = &v
		}
		if hostPIDStart.Valid {
			v, err := occupancyPIDStart(hostPIDStart.Int64)
			if err != nil {
				return nil, err
			}
			occ.HostPIDStart = &v
		}
		occ.HasProcessIdentity = hasIdentity == 1
		out = append(out, occ)
	}
	return out, rows.Err()
}

func worktreeEntryAfterReclaimTx(ctx context.Context, tx *sql.Tx, workID, projectID string) (WorktreeEntry, error) {
	entries, err := worktreeEntriesTx(ctx, tx, workID)
	if err != nil {
		return WorktreeEntry{}, err
	}
	for _, candidate := range entries {
		if candidate.ProjectID == projectID {
			return candidate, nil
		}
	}
	return WorktreeEntry{}, newFailure(KindUnavailable, "worktree_reclaim", "reclaimed entry is not readable", true, "retry the read")
}

// Worktree drift classes (issue #675). Each class names one divergence
// between the filesystem under the Concord worktree root, the durable claim
// rows, and the folded work lifecycle.
const (
	// WorktreeDriftOrphan: a directory exists under the worktree root with no
	// active claim pinned to it and no active entry at its path.
	WorktreeDriftOrphan = "orphan"
	// WorktreeDriftStaleClaim: a verified claim's pinned worktree no longer
	// exists on disk.
	WorktreeDriftStaleClaim = "stale_claim"
	// WorktreeDriftStrandedNeeded: a work item at needed holds an active
	// worktree entry whose path no longer exists, so no driver will notice.
	WorktreeDriftStrandedNeeded = "stranded_needed"
	// WorktreeDriftTerminalPresent: the worktree is on disk and still
	// claimed, and its work item is terminal. Nothing is wrong with it and
	// nothing will ever use it again. It is the shape a merged branch leaves
	// behind, and the one class whose named action the audit can perform
	// itself, because reclaiming it is a store decision under store gates.
	WorktreeDriftTerminalPresent = "terminal_present"
	// WorktreeDriftUnstartedPresent: a work item at needed holds an active,
	// verified worktree that holds nothing a merge could lose — a clean tree
	// with no commit beyond the Project's default ref (CD-0118). The checkout
	// cost is real and the work has not started, so the audit names the same
	// reclaim the terminal class names, under its own narrower gate.
	WorktreeDriftUnstartedPresent   = "unstarted_present"
	WorktreeDriftUncommittedContent = "uncommitted_content"
	WorktreeDriftUnpushedContent    = "unpushed_content"
	// WorktreeDriftUnpublishedLesson: the worktree's branch carries a lesson
	// record shard the default ref does not hold. The lesson is prepared
	// delivery a merge could still lose, and the branch may already be
	// pushed, so no content-risk class sees it. The row names inspect, not
	// reclaim: the reclaim gate refuses the worktree while the record stays
	// unpublished.
	WorktreeDriftUnpublishedLesson = "unpublished_lesson"
	// WorktreeDriftRetainedRef: a completed reclamation retained a branch ref
	// it proved it must not delete (CD-0212 D3). The retention is recorded in
	// the reclamation facts and stays visible after the directory is gone,
	// because the operator, not the audit, decides the retained ref's
	// disposal. The row names inspect.
	WorktreeDriftRetainedRef = "retained_ref"
)

// Typed recovery actions. Where a Concord operation owns the recovery, the
// action names that operation; orphan removal has no typed owner and names
// the host action.
const (
	WorktreeRecoveryRemoveOrphan = "remove_worktree"
	WorktreeRecoveryReclaim      = "worktree_reclaim"
	WorktreeRecoveryClaim        = "worktree_claim"
	WorktreeRecoveryInspect      = "worktree_inspect"
)

// worktreeIDNamePattern mirrors the agent surface's shared id definition
// (contracts/agent-tool-surface-payloads.schema.json $defs/id). An orphan
// directory whose name cannot be a work id reports no work_id rather than a
// value the surface would refuse.
var worktreeIDNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// WorktreeDrift is one classified divergence with its typed recovery action.
type WorktreeDrift struct {
	Class          string `json:"class"`
	ProjectID      string `json:"project_id"`
	WorkID         string `json:"work_id,omitempty"`
	Path           string `json:"path"`
	ClaimState     string `json:"claim_state,omitempty"`
	Lifecycle      string `json:"lifecycle,omitempty"`
	RecoveryAction string `json:"recovery_action"`
	Risk           string `json:"risk,omitempty"`
	// CommitsAhead is the commit count the branch holds beyond the
	// Project's default ref. It is the unstarted class's gate input and is
	// set only on rows whose classification derived it.
	CommitsAhead int `json:"commits_ahead,omitempty"`
	// UnpushedCommits counts branch commits unreachable from local remotes.
	UnpushedCommits int `json:"unpushed_commits,omitempty"`
	// ClaimAgeSeconds is the age of the claim at audit time. It is an
	// operator display fact; no gate reads it.
	ClaimAgeSeconds int64 `json:"claim_age_seconds,omitempty"`
	// HeadBranch names the live branch the audit's one immutable live-HEAD
	// observation read (CD-0212 D1). It is empty when the live HEAD is
	// detached, and it differs from the stored claim branch exactly when the
	// checkout drifted from the claim.
	HeadBranch string `json:"head_branch,omitempty"`
	// HeadDetached records that the live-HEAD observation read a detached
	// HEAD rather than a branch checkout.
	HeadDetached bool `json:"head_detached,omitempty"`
	// RetainedBranch names one branch ref a completed reclamation retained
	// (CD-0212 D3): the ref survived the reclaim with its branch, tip, and
	// reason recorded in the reclamation facts, and the operator decides its
	// disposal by hand.
	RetainedBranch string `json:"retained_branch,omitempty"`
	// RetainedTip is the tip the reclamation observed when it retained the
	// ref named by RetainedBranch.
	RetainedTip string `json:"retained_tip,omitempty"`
}

// WorktreeAudit is one page of one audit pass. The caller pages through
// NextCursor until it is empty to reach the complete classification; no page
// drops a classified row that a cursor cannot still deliver (CD-0185).
type WorktreeAudit struct {
	Root  string          `json:"root"`
	Drift []WorktreeDrift `json:"drift"`
	// NextCursor is the inner continuation of the page after this one, empty
	// when the classification is exhausted. It is envelope state, not payload:
	// the read handler signs it before the caller sees it.
	NextCursor string `json:"-"`
}

// WorktreeAuditRequest drives one audit pass. The Runner, Now, and DefaultRef
// inputs exist for the same reason the reclaim request carries them: the
// unstarted class derives its gate facts from git, and a pass that will act on
// those facts must derive them through the same runner it will reclaim with.
type WorktreeAuditRequest struct {
	ProductID string
	Limit     int
	// Cursor is the continuation the previous page returned: the decimal
	// offset into the deterministic classification order. Empty starts the
	// first page.
	Cursor     string
	Runner     GitRunner
	Now        time.Time
	DefaultRef string
}

// WorktreeAudit enumerates on-disk worktrees under the Concord worktree root
// (the database directory's worktrees/<project_id>/<work_id> convention owned
// by LocateWorktree) against active claims, folded entries, and work
// lifecycle, classifying every divergence (issue #675). It names the typed
// recovery action for each drift row and repairs nothing.
//
// A pending claim is intent, not verified fact — its worktree may simply not
// be created yet, and retrying the claim reconciles it — so only verified
// claims and active entries are audited. A stranded_needed row co-occurs with
// its stale_claim row by design: the claim and the work item are different
// subjects, and recovering the work requires both actions in order.
//
// Content-risk classes derive status and local remote reachability from Git for
// every present active entry. The unstarted class derives a separate gate from
// a clean tree and no commit beyond the Project's default ref. A project whose
// default ref cannot be resolved contributes no unstarted rows to a read, while
// a pass that would act on the class refuses typed instead. An unreachable
// repository refuses the pass typed rather than degrading to an unclassified row.
//
// Output is bounded by the page limit and ordered deterministically (class,
// project, path), so one page of the classification is stable for the caller
// and the next page continues at the offset the cursor names. The limit
// bounds this read's report only; classification itself always runs over
// every entry.
func (s *Store) WorktreeAudit(ctx context.Context, req WorktreeAuditRequest) (WorktreeAudit, error) {
	if s == nil || s.db == nil {
		return WorktreeAudit{}, newFailure(KindUnavailable, "worktree_audit", "store is not open", false, "open the authority database")
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	limit := req.Limit
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	audit, err := worktreeAudit(ctx, s.db, filepath.Join(filepath.Dir(s.Path()), "worktrees"), req.ProductID, runner, now, req.DefaultRef, false)
	if err != nil {
		return WorktreeAudit{}, err
	}
	offset := 0
	if req.Cursor != "" {
		parsed, parseErr := strconv.Atoi(req.Cursor)
		if parseErr != nil || parsed < 0 || parsed > len(audit.Drift) {
			return WorktreeAudit{}, newFailure(KindInvalidCursor, "worktree_audit", "audit page cursor does not name a position in the current classification", false, "restart the audit from its first page")
		}
		offset = parsed
	}
	end := offset + limit
	if end > len(audit.Drift) {
		end = len(audit.Drift)
	} else {
		audit.NextCursor = strconv.Itoa(end)
	}
	audit.Drift = audit.Drift[offset:end]
	return audit, nil
}

// worktreeAudit classifies every drift row for one Product. It applies no
// limit: the callers own the limit, and a reclaim pass must classify every
// row before its limit consumes anything (CD-0179).
// auditClaim is one pending or verified worktree claim row the audit reads.
type auditClaim struct {
	workID, projectID, path, state, observedAt string
}

// collectAuditQuery runs one audit read query and collects its rows. A query
// or iteration failure is unavailable; a scan failure returns the raw error,
// which is already a typed failure from the scanner.
func collectAuditQuery[T any](ctx context.Context, db *sql.DB, cannotRead string, query string, args []any, scan func(*sql.Rows) (T, error)) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worktree_audit", cannotRead, true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func worktreeAudit(ctx context.Context, db *sql.DB, root string, productID string, runner GitRunner, now time.Time, defaultRefOverride string, refRequired bool) (WorktreeAudit, error) {
	if productID == "" {
		return WorktreeAudit{}, newFailure(KindUnknownScope, "worktree_audit", "worktree audit requires one Product scope", false, "select one Product before auditing worktrees")
	}
	auditRows, err := readWorktreeAuditRows(ctx, db, productID)
	if err != nil {
		return WorktreeAudit{}, err
	}
	claimedPaths := map[string]bool{}
	for _, c := range auditRows.claims {
		claimedPaths[c.path] = true
	}
	enteredPaths := map[string]bool{}
	for _, e := range auditRows.entries {
		enteredPaths[e.path] = true
	}
	orphanDrift, err := worktreeOrphanDrift(root, auditRows.projects, claimedPaths, enteredPaths)
	if err != nil {
		return WorktreeAudit{}, err
	}
	drift := append([]WorktreeDrift{}, orphanDrift...)
	// One immutable live-HEAD observation per present active entry feeds every
	// classification and every gate below (CD-0212 D1), so the content
	// classes, the lesson class, and the unstarted class all read the same
	// head and report the branch that actually prevents removal.
	heads, err := observeAuditLiveHeads(ctx, runner, auditRows.entries)
	if err != nil {
		return WorktreeAudit{}, err
	}
	contentRows, contentRiskPaths, err := classifyWorktreeContent(ctx, db, runner, defaultRefOverride, auditRows.entries, auditRows.lifecycleByWorkID, heads)
	if err != nil {
		return WorktreeAudit{}, err
	}
	drift = append(drift, contentRows...)
	// Unpublished lesson records: the rows ride after the content classes,
	// and they do not set content-risk paths, so a terminal worktree stays
	// classified and the reclaim pass attempts it and reports the typed
	// refusal the gate produces.
	lessonRows, err := classifyUnpublishedLessonWorktrees(ctx, db, runner, defaultRefOverride, auditRows.entries, auditRows.lifecycleByWorkID, heads)
	if err != nil {
		return WorktreeAudit{}, err
	}
	drift = append(drift, lessonRows...)
	stateDrift, err := worktreeStateDrift(auditRows.claims, auditRows.entries, auditRows.strandedIDs, auditRows.terminalLifecycle, contentRiskPaths, heads)
	if err != nil {
		return WorktreeAudit{}, err
	}
	drift = append(drift, stateDrift...)
	// Retained refs of reclaimed entries: recorded durably in the
	// reclamation facts, reported here so the retention stays visible after
	// directory removal (CD-0212 D3). The rows are report-only in every
	// pass that acts.
	drift = append(drift, worktreeRetainedRefDrift(auditRows.retentions)...)
	// Unstarted present: needed work whose active worktree holds nothing a
	// merge could lose (CD-0118). The gate facts come from git, probed only
	// for candidates: a dirty tree or a branch with commits beyond the
	// default ref is work in flight, not drift, and stays unclassified.
	claimObservedAt := map[string]string{}
	for _, c := range auditRows.claims {
		if c.state == worktreeStateVerified {
			claimObservedAt[c.workID] = c.observedAt
		}
	}
	unstarted, err := classifyUnstartedWorktrees(ctx, db, runner, now, defaultRefOverride, refRequired, auditRows.entries, auditRows.strandedIDs, claimObservedAt, contentRiskPaths, heads)
	if err != nil {
		return WorktreeAudit{}, err
	}
	drift = append(drift, unstarted...)
	return WorktreeAudit{Root: root, Drift: drift}, nil
}

// observeAuditLiveHeads captures the one immutable live-HEAD observation for
// every present active entry (CD-0212 D1). An unreachable worktree refuses
// the pass typed, exactly as the status probe did before the observation
// carried it.
func observeAuditLiveHeads(ctx context.Context, runner GitRunner, entries []worktreeAuditEntry) (map[string]worktreeLiveHead, error) {
	heads := map[string]worktreeLiveHead{}
	for _, e := range entries {
		present, err := pathExistsForAudit(e.path)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		head, err := observeWorktreeLiveHead(ctx, runner, e.path, "worktree_audit")
		if err != nil {
			return nil, err
		}
		heads[e.path] = head
	}
	return heads, nil
}

// observeWorktreeLiveHead reads one worktree's live HEAD once: the checked
// out branch (empty when detached), the tip SHA, and the clean-tree fact.
func observeWorktreeLiveHead(ctx context.Context, runner GitRunner, path, op string) (worktreeLiveHead, error) {
	branchOut, branchErr := runner.Run(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
	if branchErr != nil {
		return worktreeLiveHead{}, wrapFailure(KindGitUnreachable, op, "cannot read the live HEAD branch of the worktree at "+path, true, "retry once the worktree is reachable", branchErr)
	}
	head := worktreeLiveHead{Branch: strings.TrimSpace(string(branchOut))}
	if head.Branch == "HEAD" {
		head.Detached, head.Branch = true, ""
	}
	tipOut, tipErr := runner.Run(ctx, path, "rev-parse", "HEAD")
	if tipErr != nil {
		return worktreeLiveHead{}, wrapFailure(KindGitUnreachable, op, "cannot read the live HEAD tip of the worktree at "+path, true, "retry once the worktree is reachable", tipErr)
	}
	head.Tip = strings.TrimSpace(string(tipOut))
	statusOut, statusErr := runner.Run(ctx, path, "status", "--porcelain")
	if statusErr != nil {
		return worktreeLiveHead{}, wrapFailure(KindGitUnreachable, op, "cannot read worktree status at "+path, true, "retry once the worktree is reachable", statusErr)
	}
	head.Clean = strings.TrimSpace(string(statusOut)) == ""
	return head, nil
}

// worktreeAuditRows holds every projection row one audit pass reads.
type worktreeAuditRows struct {
	projects          []string
	claims            []auditClaim
	entries           []worktreeAuditEntry
	strandedIDs       map[string]bool
	terminalLifecycle map[string]string
	lifecycleByWorkID map[string]string
	// retentions are the per-ref outcome rows of reclaimed claim
	// generations (CD-0212 D3-D4): retained refs, native protections, and
	// deletions still owed, read so the whole record stays visible after
	// directory removal and after a later claim.
	retentions []worktreeRefOutcome
}

// readWorktreeAuditRows reads the Product's Projects, its pending and
// verified claims, its active entries, and the lifecycle sets the drift
// classes key on.
func readWorktreeAuditRows(ctx context.Context, db *sql.DB, productID string) (worktreeAuditRows, error) {
	var out worktreeAuditRows
	projects, err := collectAuditQuery(ctx, db, "cannot read Product Projects", `SELECT project_id FROM product_projects WHERE product_id=? ORDER BY project_id`, []any{productID}, func(r *sql.Rows) (string, error) {
		var id string
		if err := r.Scan(&id); err != nil {
			return "", err
		}
		return id, nil
	})
	if err != nil {
		return out, err
	}
	out.projects = projects
	claims, err := collectAuditQuery(ctx, db, "cannot read worktree claims", `SELECT c.work_id,c.project_id,c.pinned_path,c.state,c.observed_at FROM worktree_claims c JOIN product_projects pp ON pp.project_id=c.project_id WHERE pp.product_id=? AND c.state IN ('pending','verified') ORDER BY c.pinned_path`, []any{productID}, func(r *sql.Rows) (auditClaim, error) {
		var c auditClaim
		if err := r.Scan(&c.workID, &c.projectID, &c.path, &c.state, &c.observedAt); err != nil {
			return c, err
		}
		return c, nil
	})
	if err != nil {
		return out, err
	}
	out.claims = claims
	entries, err := collectAuditQuery(ctx, db, "cannot read worktree entries", `SELECT e.set_id,e.project_id,e.path,e.branch FROM worktree_entries e JOIN product_projects pp ON pp.project_id=e.project_id WHERE pp.product_id=? AND e.state='active' ORDER BY e.path`, []any{productID}, func(r *sql.Rows) (worktreeAuditEntry, error) {
		var setID string
		var e worktreeAuditEntry
		if err := r.Scan(&setID, &e.projectID, &e.path, &e.branch); err != nil {
			return e, err
		}
		e.workID = strings.TrimPrefix(setID, worktreeSetPrefix)
		return e, nil
	})
	if err != nil {
		return out, err
	}
	out.entries = entries
	stranded, err := collectAuditQuery(ctx, db, "cannot read work lifecycle", `SELECT w.id FROM work_items w JOIN worktree_entries e ON e.set_id=?||w.id JOIN product_projects pp ON pp.project_id=e.project_id WHERE pp.product_id=? AND e.state='active' AND w.lifecycle='needed'`, []any{worktreeSetPrefix, productID}, func(r *sql.Rows) (string, error) {
		var id string
		if err := r.Scan(&id); err != nil {
			return "", err
		}
		return id, nil
	})
	if err != nil {
		return out, err
	}
	out.strandedIDs = map[string]bool{}
	for _, id := range stranded {
		out.strandedIDs[id] = true
	}
	// Terminal work with an active entry: the same join, the other side of
	// the lifecycle. The lifecycle rides along so the row can say which.
	terminal, err := collectAuditQuery(ctx, db, "cannot read terminal work", `SELECT w.id, w.lifecycle FROM work_items w JOIN worktree_entries e ON e.set_id=?||w.id JOIN product_projects pp ON pp.project_id=e.project_id WHERE pp.product_id=? AND e.state='active' AND w.lifecycle IN `+terminalLifecycleSQLList(), []any{worktreeSetPrefix, productID}, func(r *sql.Rows) ([2]string, error) { //nolint:gosec // the IN list is the closed terminal set, never caller input
		var pair [2]string
		if err := r.Scan(&pair[0], &pair[1]); err != nil {
			return pair, err
		}
		return pair, nil
	})
	if err != nil {
		return out, err
	}
	out.terminalLifecycle = map[string]string{}
	for _, pair := range terminal {
		out.terminalLifecycle[pair[0]] = pair[1]
	}
	lifecycles, err := collectAuditQuery(ctx, db, "cannot read worktree lifecycle", `SELECT DISTINCT w.id,w.lifecycle FROM work_items w JOIN worktree_entries e ON e.set_id=?||w.id JOIN product_projects pp ON pp.project_id=e.project_id WHERE pp.product_id=? AND e.state='active' ORDER BY w.id`, []any{worktreeSetPrefix, productID}, func(r *sql.Rows) ([2]string, error) {
		var pair [2]string
		if err := r.Scan(&pair[0], &pair[1]); err != nil {
			return pair, err
		}
		return pair, nil
	})
	if err != nil {
		return out, err
	}
	out.lifecycleByWorkID = map[string]string{}
	for _, pair := range lifecycles {
		out.lifecycleByWorkID[pair[0]] = pair[1]
	}
	// Per-ref outcome rows of reclaimed claim generations (CD-0212 D3-D4):
	// every retained ref, every native protection, and every deletion still
	// owed, read so all of it stays visible after directory removal and
	// after a later claim replaced the entry row. Settled deletions carry no
	// report: their debt is gone.
	outcomes, err := collectAuditQuery(ctx, db, "cannot read worktree ref outcomes", `SELECT o.set_id,o.project_id,o.path,o.branch,o.phase,o.tip,o.reason FROM worktree_ref_outcomes o JOIN product_projects pp ON pp.project_id=o.project_id WHERE pp.product_id=? AND o.phase != 'settled' ORDER BY o.path,o.branch`, []any{productID}, func(r *sql.Rows) (worktreeRefOutcome, error) {
		var row worktreeRefOutcome
		var setID string
		if err := r.Scan(&setID, &row.projectID, &row.path, &row.branch, &row.phase, &row.tip, &row.reason); err != nil {
			return row, err
		}
		row.setID = setID
		return row, nil
	})
	if err != nil {
		return out, err
	}
	out.retentions = outcomes
	return out, nil
}

// worktreeRetainedRefDrift reports the retained-ref rows one audit pass owes
// (CD-0212 D3-D4): one row per ref a reclaimed claim generation retained, a
// native run protected, or a deletion it still owes. The rows read the store
// only — the directory is gone and the repository may be elsewhere — so the
// classification names the recorded outcome and never re-proves it. The
// operator, not the audit, decides a retained or protected ref's disposal;
// an owed deletion names the retry that converges it.
func worktreeRetainedRefDrift(retentions []worktreeRefOutcome) []WorktreeDrift {
	var drift []WorktreeDrift
	for _, row := range retentions {
		risk := row.reason
		switch row.phase {
		case WorktreeRefPhasePlanned, WorktreeRefPhaseCheckoutProvenAbsent, WorktreeRefPhaseDeleted:
			risk = row.reason + " (the recorded plan still owes this deletion; a retry of the reclaim converges it)"
		case WorktreeRefPhaseRestorationOwed:
			risk = row.reason + " (the pinned deletion ran or its observation could not exclude a holder; the restoration at the pinned tip is still owed, and a retry of the reclaim resolves it)"
		case WorktreeRefPhaseRestored:
			risk = row.reason + " (the restored ref remains retained; a later replay converges the deletion once no worktree holds it)"
		case WorktreeRefPhaseRetainedUnproven:
			risk = row.reason + " (the protection is durable: the recorded plan never re-attempts this deletion)"
		}
		drift = append(drift, WorktreeDrift{
			Class: WorktreeDriftRetainedRef, ProjectID: row.projectID, WorkID: strings.TrimPrefix(row.setID, worktreeSetPrefix), Path: row.path,
			ClaimState: worktreeEntryReclaimed, RecoveryAction: WorktreeRecoveryInspect, Risk: risk,
			RetainedBranch: row.branch, RetainedTip: row.tip,
		})
	}
	return drift
}

// worktreeOrphanDrift reports on-disk directories under the Product's
// Projects that no claim and no entry owns.
func worktreeOrphanDrift(root string, projects []string, claimedPaths, enteredPaths map[string]bool) ([]WorktreeDrift, error) {
	drift := []WorktreeDrift{}
	// Orphan: on disk, claimed by nobody. Classes are appended in a fixed
	// order over sorted inputs, so the result needs no explicit sort.
	for _, projectID := range projects {
		projectDir := filepath.Join(root, projectID)
		dirs, err := os.ReadDir(projectDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, wrapFailure(KindUnavailable, "worktree_audit", "cannot enumerate worktrees under "+projectDir, true, "retry once the worktree root is readable", err)
		}
		for _, dir := range dirs {
			if !dir.IsDir() {
				continue
			}
			path := filepath.Join(projectDir, dir.Name())
			if claimedPaths[path] || enteredPaths[path] {
				continue
			}
			row := WorktreeDrift{Class: WorktreeDriftOrphan, ProjectID: projectID, Path: path, RecoveryAction: WorktreeRecoveryRemoveOrphan}
			if worktreeIDNamePattern.MatchString(dir.Name()) {
				row.WorkID = dir.Name()
			}
			drift = append(drift, row)
		}
	}
	return drift, nil
}

// worktreeStateDrift reports the three disk-state classes: stale verified
// claims whose path vanished, stranded needed work whose active entry points
// at nothing, and terminal work whose worktree is still present with no
// content risk on it. Terminal-present rows carry the live head the pass
// observed, so a checkout that drifted from the claim reports as itself.
func worktreeStateDrift(claims []auditClaim, entries []worktreeAuditEntry, strandedIDs map[string]bool, terminalLifecycle map[string]string, contentRiskPaths map[string]bool, heads map[string]worktreeLiveHead) ([]WorktreeDrift, error) {
	drift := []WorktreeDrift{}
	// Stale claim: the verified locator points at a path the disk no longer
	// holds. ReclaimWorktree reconciles exactly this shape (already_absent).
	for _, c := range claims {
		if c.state != worktreeStateVerified {
			continue
		}
		present, err := pathExistsForAudit(c.path)
		if err != nil {
			return nil, err
		}
		if present {
			continue
		}
		drift = append(drift, WorktreeDrift{Class: WorktreeDriftStaleClaim, ProjectID: c.projectID, WorkID: c.workID, Path: c.path, ClaimState: worktreeStateVerified, RecoveryAction: WorktreeRecoveryReclaim})
	}
	// Stranded needed work: the entry still says active, the disk disagrees,
	// and nothing is driving the work item to notice.
	for _, e := range entries {
		if !strandedIDs[e.workID] {
			continue
		}
		present, err := pathExistsForAudit(e.path)
		if err != nil {
			return nil, err
		}
		if present {
			continue
		}
		drift = append(drift, WorktreeDrift{Class: WorktreeDriftStrandedNeeded, ProjectID: e.projectID, WorkID: e.workID, Path: e.path, Lifecycle: "needed", RecoveryAction: WorktreeRecoveryClaim})
	}
	// Terminal present: the entry is active, the disk agrees, and the work
	// is finished. Reclaim is the named action, and the only safe one.
	for _, e := range entries {
		lifecycle, terminal := terminalLifecycle[e.workID]
		if !terminal {
			continue
		}
		present, err := pathExistsForAudit(e.path)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		if contentRiskPaths[e.path] {
			continue
		}
		head := heads[e.path]
		drift = append(drift, WorktreeDrift{Class: WorktreeDriftTerminalPresent, ProjectID: e.projectID, WorkID: e.workID, Path: e.path, Lifecycle: lifecycle, RecoveryAction: WorktreeRecoveryReclaim, HeadBranch: head.Branch, HeadDetached: head.Detached})
	}
	return drift, nil
}

// Outcomes of one row in a reclaim pass.
const (
	WorktreeAuditReclaimed = "reclaimed"
	WorktreeAuditRefused   = "refused"
)

// WorktreeAuditReclaimRequest drives one reclaim pass over a Product's
// terminal-present worktrees. Each row runs the direct reclaim with the stored
// occupancy projection and the supplied git runner (CD-0178 D3): the audit
// pass reads the durable worktree_occupancy rows and refuses every worktree
// with a live one.
type WorktreeAuditReclaimRequest struct {
	ProductID    string
	DefaultRef   string
	PrincipalRef string
	RequestID    string
	Now          time.Time
	Runner       GitRunner
	Limit        int
}

// WorktreeAuditReclaimRow is the outcome of one terminal-present worktree.
// A refusal carries the typed kind and detail the reclaim gate produced, so
// the caller can tell a dirty tree from an occupied one from an unmerged
// head without parsing prose.
type WorktreeAuditReclaimRow struct {
	ProjectID string `json:"project_id"`
	WorkID    string `json:"work_id"`
	Path      string `json:"path"`
	Lifecycle string `json:"lifecycle"`
	Outcome   string `json:"outcome"`
	// Version is the work item's version after a reclamation, the same bump
	// a direct reclaim returns; zero on a refused row.
	Version     int64  `json:"version,omitempty"`
	RefusalKind string `json:"refusal_kind,omitempty"`
	Detail      string `json:"detail,omitempty"`
	// RetainedRefs reports the branch refs the reclamation proved it must
	// not delete and retained instead (CD-0212 D3): the unproven stored
	// claim ref, a divergent live branch, or the default ref. It is empty on
	// a refused row and on a reclaim that owed and completed every deletion.
	RetainedRefs []WorktreeAuditRetainedRef `json:"retained_refs,omitempty"`
}

// WorktreeAuditRetainedRef is one retained branch ref a reclaimed row reports:
// the ref's name, the tip observed when the reclaim retained it, and the
// bounded reason retention was owed.
type WorktreeAuditRetainedRef struct {
	Branch string `json:"branch"`
	Tip    string `json:"tip,omitempty"`
	Reason string `json:"reason"`
}

// WorktreeAuditReclaimResult is one pass: what the audit reported for the
// classes it can only report, and what happened to each row it could act on.
type WorktreeAuditReclaimResult struct {
	Root       string                    `json:"root"`
	ReportOnly []WorktreeDrift           `json:"report_only"`
	Rows       []WorktreeAuditReclaimRow `json:"rows"`
}

// WorktreeAuditReclaim performs the one safe action the audit names. It runs
// the audit, then reclaims each terminal-present worktree through the same
// gates a direct reclaim runs (clean tree, branch durable by remote-ref count
// or squash containment (CD-0181), no recorded occupant) and each
// unstarted-present worktree through the CD-0118
// gate (clean tree, no commit beyond the default ref, no recorded occupant).
// Every other class is returned as report-only, because its
// named action is not a store decision.
//
// The pass classifies every drift row before the limit applies (CD-0179): the
// limit bounds reclaim attempts only, so report-only rows never crowd a
// reclaimable row out of the pass, and a reclaimable row beyond the limit is
// classified and reported unattempted rather than silently dropped.
//
// Each row reclaims in its own transaction. Rows are independent, and one
// refusal must not roll back another row's reclamation; the pass is a loop
// over direct reclaims, not one large write. A refused row is reported with
// its typed kind and left on disk. The pass is idempotent: a second run over
// the same state reclaims nothing and reports the same refusals.
func (s *Store) WorktreeAuditReclaim(ctx context.Context, req WorktreeAuditReclaimRequest) (WorktreeAuditReclaimResult, error) {
	if s == nil || s.db == nil {
		return WorktreeAuditReclaimResult{}, newFailure(KindUnavailable, "worktree_audit_reclaim", "store is not open", false, "open the authority database")
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	limit := req.Limit
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	audit, err := worktreeAudit(ctx, s.db, filepath.Join(filepath.Dir(s.Path()), "worktrees"), req.ProductID, runner, req.Now, req.DefaultRef, true)
	if err != nil {
		return WorktreeAuditReclaimResult{}, err
	}
	// The lease set is read once, before the first reclaim opens its
	// transaction: the legacy-row release proof inside each reclaim compares
	// this snapshot against the row's recorded_at (CD-0179).
	leases := s.ReadHostLeases()
	out := WorktreeAuditReclaimResult{Root: audit.Root, ReportOnly: []WorktreeDrift{}, Rows: []WorktreeAuditReclaimRow{}}
	attempts := []WorktreeDrift{}
	for _, drift := range audit.Drift {
		reclaimable := drift.Class == WorktreeDriftTerminalPresent || drift.Class == WorktreeDriftUnstartedPresent
		if !reclaimable || len(attempts) >= limit {
			// Report-only classes, and reclaimable rows beyond the limit,
			// are classified and reported without an attempt.
			out.ReportOnly = append(out.ReportOnly, drift)
			continue
		}
		attempts = append(attempts, drift)
	}
	for _, drift := range attempts {
		requireTerminal, requireUnstarted := drift.Class == WorktreeDriftTerminalPresent, drift.Class == WorktreeDriftUnstartedPresent
		row := WorktreeAuditReclaimRow{ProjectID: drift.ProjectID, WorkID: drift.WorkID, Path: drift.Path, Lifecycle: drift.Lifecycle}
		version, err := currentWorkVersion(ctx, s.db, drift.WorkID)
		if err != nil {
			return out, err
		}
		entry, reclaimErr := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
			WorkID: drift.WorkID, ProjectID: drift.ProjectID, DefaultRef: req.DefaultRef,
			PrincipalRef: req.PrincipalRef, RequestID: req.RequestID + ":" + drift.WorkID,
			ExpectedVersion: version, Now: req.Now, Runner: runner, RequireTerminal: requireTerminal, RequireUnstarted: requireUnstarted,
			HostLeases: leases,
		})
		if reclaimErr == nil {
			// The row reports the work item's true post-reclaim version: a
			// converged reclaim whose stored advance is already public
			// history repairs the projection without another bump.
			after, versionErr := currentWorkVersion(ctx, s.db, drift.WorkID)
			if versionErr != nil {
				return out, versionErr
			}
			row.Outcome, row.Version = WorktreeAuditReclaimed, after
			// The retained refs the reclamation recorded stay visible on the
			// row after the directory is gone (CD-0212 D3).
			row.RetainedRefs = retainedRefsFromFacts(entry.GitFacts)
			out.Rows = append(out.Rows, row)
			continue
		}
		var failure *Failure
		if !errors.As(reclaimErr, &failure) {
			return out, reclaimErr
		}
		row.Outcome, row.RefusalKind, row.Detail = WorktreeAuditRefused, string(failure.Kind), failure.Detail
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// retainedRefsFromFacts reads the retained refs a reclamation event recorded,
// so a reporting surface can show them after the directory is gone. Facts
// that carry none decode to nil.
func retainedRefsFromFacts(facts json.RawMessage) []WorktreeAuditRetainedRef {
	if len(facts) == 0 {
		return nil
	}
	var payload struct {
		RetainedRefs []WorktreeAuditRetainedRef `json:"retained_refs"`
	}
	if json.Unmarshal(facts, &payload) != nil {
		return nil
	}
	return payload.RetainedRefs
}

// worktreeAuditRepoRoot resolves one Project's repository root for the
// unstarted classification's git probes, through the same locator the claim
// and reclaim paths use.
// classifyUnstartedWorktrees derives the unstarted_present rows for one
// audit pass (CD-0118). Git is probed only for candidates: needed work with
// an active entry whose path exists. A dirty tree or a live head with commits
// beyond the default ref is not unstarted drift. Content-risk classification
// reports those commits separately. A read whose default ref is underivable
// skips the project's candidates; a pass that will act on the class refuses
// typed instead (refRequired). The gate reads the live-HEAD observation's
// revision, so a drifted or detached checkout counts as itself (CD-0212 D1).
func classifyUnstartedWorktrees(ctx context.Context, db *sql.DB, runner GitRunner, now time.Time, defaultRefOverride string, refRequired bool, entries []worktreeAuditEntry, strandedIDs map[string]bool, claimObservedAt map[string]string, contentRiskPaths map[string]bool, heads map[string]worktreeLiveHead) ([]WorktreeDrift, error) {
	repoRoots := map[string]string{}
	defaultRefs := map[string]string{}
	var rows []WorktreeDrift
	for _, e := range entries {
		if !strandedIDs[e.workID] {
			continue
		}
		head, observed := heads[e.path]
		if !observed {
			continue
		}
		if contentRiskPaths[e.path] {
			continue
		}
		repoRoot, ok := repoRoots[e.projectID]
		if !ok {
			var err error
			repoRoot, err = worktreeAuditRepoRoot(ctx, db, e.projectID)
			if err != nil {
				return nil, err
			}
			repoRoots[e.projectID] = repoRoot
		}
		defaultRef, ok := defaultRefs[e.projectID]
		if !ok {
			resolved, resErr := worktreeAuditDefaultRef(ctx, runner, repoRoot, defaultRefOverride)
			if resErr != nil {
				if !refRequired {
					// A read cannot evaluate the class without the
					// ref and must not refuse the whole page (issue
					// #831): the project contributes no rows.
					defaultRefs[e.projectID] = ""
					continue
				}
				return nil, resErr
			}
			defaultRef = resolved
			defaultRefs[e.projectID] = defaultRef
		}
		if defaultRef == "" {
			continue
		}
		if !head.Clean {
			continue
		}
		countOut, countErr := runner.Run(ctx, repoRoot, "rev-list", "--count", defaultRef+".."+head.committish())
		if countErr != nil {
			return nil, wrapFailure(KindGitUnreachable, "worktree_audit", "cannot count commits beyond "+defaultRef+" for "+head.committish(), true, "retry once the repository is reachable", countErr)
		}
		if strings.TrimSpace(string(countOut)) != "0" {
			continue
		}
		row := WorktreeDrift{Class: WorktreeDriftUnstartedPresent, ProjectID: e.projectID, WorkID: e.workID, Path: e.path, ClaimState: worktreeStateVerified, Lifecycle: "needed", CommitsAhead: 0, RecoveryAction: WorktreeRecoveryReclaim, HeadBranch: head.Branch, HeadDetached: head.Detached}
		if observed := claimObservedAt[e.workID]; observed != "" {
			if claimed, parseErr := time.Parse(time.RFC3339Nano, observed); parseErr == nil {
				if age := int64(now.Sub(claimed).Seconds()); age > 0 {
					row.ClaimAgeSeconds = age
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// classifyWorktreeContent derives content risk from the live-HEAD
// observation each entry's gates share (CD-0212 D1). Remote tracking refs are
// local observations and do not require network access. Branch commits the
// default ref already holds as one squash merge (CD-0181) carry no content
// risk: the class names content a reclaim could lose, and a squash-contained
// branch loses none.
func classifyWorktreeContent(ctx context.Context, db *sql.DB, runner GitRunner, defaultRefOverride string, entries []worktreeAuditEntry, lifecycleByWorkID map[string]string, heads map[string]worktreeLiveHead) ([]WorktreeDrift, map[string]bool, error) {
	repoRoots := map[string]string{}
	defaultRefs := map[string]string{}
	rows := []WorktreeDrift{}
	riskPaths := map[string]bool{}
	for _, entry := range entries {
		head, observed := heads[entry.path]
		if !observed {
			continue
		}
		repoRoot, ok := repoRoots[entry.projectID]
		if !ok {
			var err error
			repoRoot, err = worktreeAuditRepoRoot(ctx, db, entry.projectID)
			if err != nil {
				return nil, nil, err
			}
			repoRoots[entry.projectID] = repoRoot
		}
		lifecycle := lifecycleByWorkID[entry.workID]
		if !head.Clean {
			rows = append(rows, WorktreeDrift{Class: WorktreeDriftUncommittedContent, ProjectID: entry.projectID, WorkID: entry.workID, Path: entry.path, ClaimState: worktreeStateVerified, Lifecycle: lifecycle, RecoveryAction: WorktreeRecoveryInspect, Risk: "uncommitted changes", HeadBranch: head.Branch, HeadDetached: head.Detached})
			riskPaths[entry.path] = true
		}
		countOut, countErr := runner.Run(ctx, repoRoot, "rev-list", "--count", head.committish(), "--not", "--remotes")
		if countErr != nil {
			return nil, nil, wrapFailure(KindGitUnreachable, "worktree_audit", "cannot count unpushed commits for "+head.committish(), true, "retry once the repository is reachable", countErr)
		}
		unpushed, parseErr := strconv.Atoi(strings.TrimSpace(string(countOut)))
		if parseErr != nil || unpushed < 0 {
			return nil, nil, newFailure(KindGitUnreachable, "worktree_audit", "local Git returned an invalid unpushed commit count for "+head.committish(), false, "repair the repository refs before auditing worktrees")
		}
		if unpushed > 0 {
			defaultRef, resolved := defaultRefs[entry.projectID]
			if !resolved {
				defaultRef = ""
				if resolvedRef, resErr := worktreeAuditDefaultRef(ctx, runner, repoRoot, defaultRefOverride); resErr == nil {
					defaultRef = resolvedRef
				}
				// An unresolvable default ref keeps the count alone
				// authoritative, which reports the row fail-closed.
				defaultRefs[entry.projectID] = defaultRef
			}
			if !branchSquashContained(ctx, runner, repoRoot, head.committish(), defaultRef) {
				rows = append(rows, WorktreeDrift{Class: WorktreeDriftUnpushedContent, ProjectID: entry.projectID, WorkID: entry.workID, Path: entry.path, ClaimState: worktreeStateVerified, Lifecycle: lifecycle, RecoveryAction: WorktreeRecoveryInspect, Risk: "unpushed commits", UnpushedCommits: unpushed, HeadBranch: head.Branch, HeadDetached: head.Detached})
				riskPaths[entry.path] = true
			}
		}
	}
	return rows, riskPaths, nil
}

// branchUnpublishedLessonRecords returns the lesson record shards the branch
// tree adds beyond the default ref: paths under the record tree present on
// the branch and absent from the default endpoint, whose committed kind is
// lesson. The endpoint diff, not a commit range, decides presence, so a
// merged (including squash-merged) lesson never counts and a
// pushed-then-abandoned branch does. The audit classification and the
// reclaim gate probe through this one function, so both surfaces see the
// same facts.
func branchUnpublishedLessonRecords(ctx context.Context, runner GitRunner, repoRoot, branch, defaultRef, op string) ([]string, error) {
	out, err := runner.Run(ctx, repoRoot, "diff", "--name-only", "--diff-filter=A", defaultRef, branch, "--", knowledgeRecordTree+"/")
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, op, "cannot compare "+branch+" against "+defaultRef+" for unpublished lesson records", true, "retry once the repository is reachable", err)
	}
	var lessons []string
	for _, recordPath := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		recordPath = strings.TrimSpace(recordPath)
		if recordPath == "" {
			continue
		}
		blob, blobErr := runner.Run(ctx, repoRoot, "show", branch+":"+recordPath)
		if blobErr != nil {
			return nil, wrapFailure(KindGitUnreachable, op, "cannot read "+recordPath+" on "+branch, true, "retry once the repository is reachable", blobErr)
		}
		var shard struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(blob, &shard) != nil || shard.Kind != "lesson" {
			continue
		}
		lessons = append(lessons, recordPath)
	}
	return lessons, nil
}

// worktreeUnpublishedLessonDetail renders the bounded refusal detail: the
// subject always, the count always, and up to the first three record paths.
func worktreeUnpublishedLessonDetail(subject string, count int, lessons []string, defaultRef string) string {
	shown := lessons
	if len(shown) > 3 {
		shown = shown[:3]
	}
	return fmt.Sprintf("%s carries %d lesson record(s) absent from %s: %s", subject, count, defaultRef, strings.Join(shown, ", "))
}

// classifyUnpublishedLessonWorktrees derives the unpublished_lesson rows for
// one audit pass. The signal is a lesson record shard the default ref does
// not hold: prepared delivery a merge could still lose, visible whether or
// not the branch is pushed. The comparison reads the live-HEAD observation's
// revision (CD-0212 D1); a project whose default ref cannot be resolved
// contributes no rows, because the comparison that names an unpublished
// lesson needs the default endpoint, and the read must stay deliverable
// (issue #831).
func classifyUnpublishedLessonWorktrees(ctx context.Context, db *sql.DB, runner GitRunner, defaultRefOverride string, entries []worktreeAuditEntry, lifecycleByWorkID map[string]string, heads map[string]worktreeLiveHead) ([]WorktreeDrift, error) {
	repoRoots := map[string]string{}
	defaultRefs := map[string]string{}
	var rows []WorktreeDrift
	for _, entry := range entries {
		head, observed := heads[entry.path]
		if !observed {
			continue
		}
		repoRoot, ok := repoRoots[entry.projectID]
		if !ok {
			var err error
			repoRoot, err = worktreeAuditRepoRoot(ctx, db, entry.projectID)
			if err != nil {
				return nil, err
			}
			repoRoots[entry.projectID] = repoRoot
		}
		defaultRef, ok := defaultRefs[entry.projectID]
		if !ok {
			resolved, resErr := worktreeAuditDefaultRef(ctx, runner, repoRoot, defaultRefOverride)
			if resErr != nil {
				defaultRefs[entry.projectID] = ""
				continue
			}
			defaultRef = resolved
			defaultRefs[entry.projectID] = defaultRef
		}
		if defaultRef == "" {
			continue
		}
		lessons, lessonErr := branchUnpublishedLessonRecords(ctx, runner, repoRoot, head.committish(), defaultRef, "worktree_audit")
		if lessonErr != nil {
			return nil, lessonErr
		}
		if len(lessons) == 0 {
			continue
		}
		rows = append(rows, WorktreeDrift{
			Class: WorktreeDriftUnpublishedLesson, ProjectID: entry.projectID, WorkID: entry.workID,
			Path: entry.path, ClaimState: worktreeStateVerified, Lifecycle: lifecycleByWorkID[entry.workID],
			RecoveryAction: WorktreeRecoveryInspect,
			Risk:           fmt.Sprintf("%d lesson record(s) absent from %s", len(lessons), defaultRef),
			HeadBranch:     head.Branch, HeadDetached: head.Detached,
		})
	}
	return rows, nil
}

type worktreeAuditEntry struct {
	workID, projectID, path, branch string
}

func pathExistsForAudit(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, wrapFailure(KindUnavailable, "worktree_audit", "cannot inspect worktree path "+path, true, "retry once the worktree root is readable", err)
	}
	return true, nil
}

func worktreeAuditRepoRoot(ctx context.Context, q queryer, projectID string) (string, error) {
	root, err := worktreeRepoRootTx(ctx, q, WorktreeClaimRequest{ProjectID: projectID})
	if err != nil {
		return "", err
	}
	return root, nil
}

// worktreeAuditDefaultRef resolves the ref the unstarted class counts
// commits against: the caller's override, else the repository's origin HEAD.
func worktreeAuditDefaultRef(ctx context.Context, runner GitRunner, repoRoot, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	refOut, refErr := runner.Run(ctx, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD")
	if refErr != nil || strings.TrimSpace(string(refOut)) == "" {
		return "", newFailure(KindGitUnreachable, "worktree_audit", "cannot resolve the default branch", false, "set origin/HEAD or supply the merge target ref")
	}
	return strings.TrimPrefix(strings.TrimSpace(string(refOut)), "refs/remotes/"), nil
}

func currentWorkVersion(ctx context.Context, q queryer, workID string) (int64, error) {
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		return 0, wrapFailure(KindUnavailable, "worktree_audit_reclaim", "cannot read work version", true, "retry once the database is readable", err)
	}
	return version, nil
}

// SessionWorktreeOwner identifies the agent session that holds a verify
// lease. The tuple matches the authority grant identity; principal_ref is
// derived from the registered client (CD-0080 D1) and needs no separate
// column for ownership.
type SessionWorktreeOwner struct {
	ClientRef  string `json:"client_ref"`
	AgentRef   string `json:"agent_ref"`
	SessionRef string `json:"session_ref"`
}

func validateSessionWorktreeOwner(owner SessionWorktreeOwner) error {
	for label, value := range map[string]string{"client ref": owner.ClientRef, "agent ref": owner.AgentRef, "session ref": owner.SessionRef} {
		if len(value) < 2 || len(value) > 128 {
			return newFailure(KindInvalidOperation, "worktree_verify", "session identity is missing a bounded "+label, false, "supply the client, agent, and session identity of the calling session")
		}
	}
	return nil
}

func sessionOwnerLabel(owner SessionWorktreeOwner) string {
	return owner.ClientRef + "/" + owner.AgentRef + "/" + owner.SessionRef
}

// Cross-worktree tiers (CD-0096 D3): Inspect, Verify, and Destroy. Each tier
// resolves its subject through the calling session's Project and the work
// item's folded worktree entry — never through a caller path (CD-0096 D2) —
// so a worktree outside the session's Project is not reachable as a target
// at all.

const (
	// WorktreeInspectModeStatus reads `git status --porcelain`.
	WorktreeInspectModeStatus = "status"
	// WorktreeInspectModeDiff reads the worktree's diff against HEAD.
	WorktreeInspectModeDiff = "diff"
	// WorktreeInspectModeFile reads one file's content.
	WorktreeInspectModeFile = "file"

	defaultWorktreeInspectBytes  = 16384
	defaultWorktreeVerifyBytes   = 16384
	maxWorktreeVerifyCommandArgs = 16
	maxWorktreeVerifyCommandPart = 256
)

// activeWorktreeEntryForProject resolves the work item's active worktree
// entry inside the calling session's Project. The Project selector is the
// same-Project tier boundary: an entry in any other Project is invisible.
func activeWorktreeEntryForProject(ctx context.Context, q queryer, op, workID, projectID string) (WorktreeEntry, error) {
	if workID == "" || projectID == "" {
		return WorktreeEntry{}, newFailure(KindUnknownScope, op, "worktree tier access requires the work item and the session's Project", false, "supply the work identity and run from a registered Project")
	}
	entries, err := worktreeEntriesCore(ctx, q, workID)
	if err != nil {
		return WorktreeEntry{}, err
	}
	for _, candidate := range entries {
		if candidate.ProjectID == projectID && candidate.State == worktreeEntryActive {
			return candidate, nil
		}
	}
	return WorktreeEntry{}, newFailure(KindProjectionNotFound, op, "work item holds no active worktree in Project "+projectID, false, "claim or retarget the canonical worktree before tiered access")
}

// probeWorktreeReachable refuses typed when the folded entry points at a
// tree the host cannot reach, or at a branch the tree no longer checks out.
// Both are drift, not inspection subjects.
func probeWorktreeReachable(ctx context.Context, runner GitRunner, op string, entry WorktreeEntry) error {
	branchOut, err := runner.Run(ctx, entry.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return wrapFailure(KindGitUnreachable, op, "worktree at "+entry.Path+" is not reachable on disk", true, "reconcile the worktree drift with the audit read before tiered access", err)
	}
	if strings.TrimSpace(string(branchOut)) != entry.Branch {
		return newFailure(KindProjectionConflict, op, "worktree at "+entry.Path+" is on branch "+strings.TrimSpace(string(branchOut))+", the stored claim says "+entry.Branch, false, "reconcile the worktree claim before tiered access")
	}
	return nil
}

// WorktreeInspectRequest drives the CD-0096 Inspect tier: a read-only view
// of files, Git status, and diffs of one active same-Project worktree. The
// persistent effective target is never a factor and never changes.
type WorktreeInspectRequest struct {
	WorkID    string
	ProjectID string
	Mode      string
	// Path is the relative file selector for file mode. It is a selector
	// inside the derived worktree, never a worktree path (CD-0096 D2).
	Path     string
	Runner   GitRunner
	MaxBytes int
}

// WorktreeInspectResult is the bounded content of one inspection.
type WorktreeInspectResult struct {
	WorkID    string `json:"work_id"`
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// InspectWorktree reads files, Git status, or a diff from the work item's
// active worktree in the calling session's Project (CD-0096 D3 Inspect). It
// is a pure read: no lease is taken and no persistent state changes.
func (s *Store) InspectWorktree(ctx context.Context, req WorktreeInspectRequest) (WorktreeInspectResult, error) {
	if s == nil || s.db == nil {
		return WorktreeInspectResult{}, newFailure(KindUnavailable, "worktree_inspect", "store is not open", false, "open the authority database")
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultWorktreeInspectBytes
	}
	switch req.Mode {
	case WorktreeInspectModeStatus, WorktreeInspectModeDiff:
		if req.Path != "" {
			return WorktreeInspectResult{}, newFailure(KindInvalidOperation, "worktree_inspect", "path applies to file mode only", false, "omit the path or select file mode")
		}
	case WorktreeInspectModeFile:
		if err := safeWorktreeRelativePath(req.Path); err != nil {
			return WorktreeInspectResult{}, err
		}
	default:
		return WorktreeInspectResult{}, newFailure(KindInvalidOperation, "worktree_inspect", "mode must be status, diff, or file", false, "select one inspection mode")
	}
	entry, err := activeWorktreeEntryForProject(ctx, s.db, "worktree_inspect", req.WorkID, req.ProjectID)
	if err != nil {
		return WorktreeInspectResult{}, err
	}
	if err := probeWorktreeReachable(ctx, runner, "worktree_inspect", entry); err != nil {
		return WorktreeInspectResult{}, err
	}
	result := WorktreeInspectResult{WorkID: req.WorkID, ProjectID: req.ProjectID, Branch: entry.Branch, Path: entry.Path, Mode: req.Mode}
	switch req.Mode {
	case WorktreeInspectModeStatus:
		out, runErr := runner.Run(ctx, entry.Path, "status", "--porcelain")
		if runErr != nil {
			return WorktreeInspectResult{}, wrapFailure(KindGitUnreachable, "worktree_inspect", "cannot read worktree status", true, "retry once the worktree is reachable", runErr)
		}
		result.Content, result.Truncated = boundText(string(out), maxBytes)
	case WorktreeInspectModeDiff:
		out, runErr := runner.Run(ctx, entry.Path, "diff", "HEAD")
		if runErr != nil {
			return WorktreeInspectResult{}, wrapFailure(KindGitUnreachable, "worktree_inspect", "cannot read the worktree diff", true, "retry once the worktree is reachable", runErr)
		}
		result.Content, result.Truncated = boundText(string(out), maxBytes)
	case WorktreeInspectModeFile:
		content, readErr := readBoundedFile(filepath.Join(entry.Path, filepath.FromSlash(req.Path)), maxBytes)
		if readErr != nil {
			return WorktreeInspectResult{}, readErr
		}
		result.Content, result.Truncated = content, len(content) > maxBytes
	}
	return result, nil
}

// safeWorktreeRelativePath accepts one bounded relative selector. Absolute
// paths, parent traversal, and unclean forms refuse typed, because a
// selector that escapes the derived worktree reads a file no tier granted.
func safeWorktreeRelativePath(rel string) error {
	if rel == "" || len(rel) > 512 || strings.ContainsRune(rel, 0) {
		return newFailure(KindInvalidOperation, "worktree_inspect", "file selector must be a bounded relative path", false, "supply one relative path inside the worktree")
	}
	if filepath.IsAbs(rel) || rel != filepath.Clean(filepath.FromSlash(rel)) {
		return newFailure(KindInvalidOperation, "worktree_inspect", "file selector must be a clean relative path", false, "supply one relative path without absolute or parent segments")
	}
	for _, element := range strings.Split(filepath.ToSlash(rel), "/") {
		if element == ".." {
			return newFailure(KindInvalidOperation, "worktree_inspect", "file selector must not traverse outside the worktree", false, "supply one relative path without parent segments")
		}
	}
	return nil
}

// boundText keeps the first maxBytes of text and reports truncation.
func boundText(text string, maxBytes int) (string, bool) {
	if len(text) > maxBytes {
		return text[:maxBytes], true
	}
	return text, false
}

// readBoundedFile reads at most maxBytes plus one byte, so the caller can
// report truncation without buffering the whole file.
func readBoundedFile(path string, maxBytes int) (string, error) {
	file, err := os.Open(path) //nolint:gosec // the path is the derived worktree joined to a validated clean relative selector.
	if err != nil {
		if os.IsNotExist(err) {
			return "", newFailure(KindProjectionNotFound, "worktree_inspect", "file selector does not exist in the worktree", false, "inspect status first and select an existing file")
		}
		return "", wrapFailure(KindUnavailable, "worktree_inspect", "cannot read the selected file", true, "retry once the worktree is readable", err)
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, maxBytes+1)
	read, err := io.ReadFull(file, buffer)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", wrapFailure(KindUnavailable, "worktree_inspect", "cannot read the selected file", true, "retry once the worktree is readable", err)
	}
	return string(buffer[:read]), nil
}

// WorktreeVerifyRequest drives the CD-0096 Verify tier: the requested
// command runs in the work item's active worktree in the calling session's
// Project, under an exclusive lease, and completion refuses when tracked
// files changed while the lease was held.
type WorktreeVerifyRequest struct {
	Owner     SessionWorktreeOwner
	WorkID    string
	ProjectID string
	Command   []string
	// LeaseID scopes the lease and its pinned command. Retrying an
	// interrupted verify with the same LeaseID resumes only the command the
	// lease first pinned.
	LeaseID        string
	PrincipalRef   string
	RequestID      string
	Now            time.Time
	Runner         GitRunner
	RunCommand     func(ctx context.Context, dir string, command []string, maxOutput int) (int, []byte, bool, error)
	MaxOutputBytes int
}

// WorktreeVerifyResult is the bounded record of one leased run. OperationRef
// names the durable operation a green run recorded, so a caller can bind the
// run as verification evidence (CD-0192).
type WorktreeVerifyResult struct {
	WorkID              string   `json:"work_id"`
	ProjectID           string   `json:"project_id"`
	Branch              string   `json:"branch"`
	Path                string   `json:"path"`
	LeaseID             string   `json:"lease_id"`
	OperationRef        string   `json:"operation_ref"`
	SubjectRef          string   `json:"subject_ref,omitempty"`
	Command             []string `json:"command"`
	ExitCode            int      `json:"exit_code"`
	Output              string   `json:"output"`
	OutputTruncated     bool     `json:"output_truncated"`
	TrackedFilesChanged bool     `json:"tracked_files_changed"`
}

// VerifyWorktree acquires the exclusive verify lease, runs the command, and
// compares tracked-file state across the lease. The lease acquire and its
// release each own one transaction; the command runs between them, so no
// transaction spans the external effect.
func (s *Store) VerifyWorktree(ctx context.Context, req WorktreeVerifyRequest) (WorktreeVerifyResult, error) {
	if s == nil || s.db == nil {
		return WorktreeVerifyResult{}, newFailure(KindUnavailable, "worktree_verify", "store is not open", false, "open the authority database")
	}
	if req.LeaseID == "" || req.PrincipalRef == "" || req.RequestID == "" {
		return WorktreeVerifyResult{}, newFailure(KindInvalidOperation, "worktree_verify", "verify operation is missing identity fields", false, "supply lease, principal, and request ids")
	}
	if err := validateSessionWorktreeOwner(req.Owner); err != nil {
		return WorktreeVerifyResult{}, retitleFailure(err, "worktree_verify")
	}
	if err := validateWorktreeVerifyCommand(req.Command); err != nil {
		return WorktreeVerifyResult{}, err
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	runCommand := req.RunCommand
	if runCommand == nil {
		runCommand = RunWorktreeVerifyCommand
	}
	maxOutput := req.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultWorktreeVerifyBytes
	}
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	commandJSON, _ := json.Marshal(req.Command)

	// Lease phase: resolve the subject, probe it, and snapshot tracked state
	// before any transaction opens, because every git read here is a
	// subprocess and no write transaction may span one (CD-0195 D2). The
	// transaction re-validates the entry identity and pins the lease with
	// SQL only.
	entry, err := activeWorktreeEntryForProject(ctx, s.db, "worktree_verify", req.WorkID, req.ProjectID)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if err := probeWorktreeReachable(ctx, runner, "worktree_verify", entry); err != nil {
		return WorktreeVerifyResult{}, err
	}
	before, err := snapshotTrackedFiles(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	beforeSubject, err := snapshotOracleGitSubject(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	acquireTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorktreeVerifyResult{}, wrapFailure(KindUnavailable, "worktree_verify", "cannot begin verify", true, "retry the same operation with the same lease id", err)
	}
	defer acquireTx.Rollback()
	liveEntry, err := activeWorktreeEntryForProject(ctx, acquireTx, "worktree_verify", req.WorkID, req.ProjectID)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if liveEntry.Path != entry.Path || liveEntry.Branch != entry.Branch || liveEntry.ClaimOpID != entry.ClaimOpID {
		return WorktreeVerifyResult{}, wrapFailure(KindProjectionConflict, "worktree_verify", "worktree changed under the verify probe", true, "retry the same operation with the same lease id", nil)
	}
	if err := acquireVerifyLeaseTx(ctx, acquireTx, req, liveEntry, string(commandJSON), now); err != nil {
		var completed *worktreeVerifyCompleted
		if errors.As(err, &completed) {
			// The lease already reached its durable outcome (the crash
			// window between release and the caller's idempotency record).
			// Report the recorded result; never run the command twice.
			return completed.result, completed.failure
		}
		return WorktreeVerifyResult{}, err
	}
	if err := acquireTx.Commit(); err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot commit the verify lease", true, "retry the same operation with the same lease id", err), SubjectCurrentVersion{SubjectType: "worktree_verify_lease", SubjectID: req.LeaseID, Version: 1})
	}
	leaseRef := SubjectCurrentVersion{SubjectType: "worktree_verify_lease", SubjectID: req.LeaseID, Version: 1}
	// From here to the committed release the lease is durably held: every
	// exit from this window must leave it released, or the one-held index
	// refuses every later verify on the worktree. The deferred release
	// covers cancellation, the intervening error returns, and panic; the
	// normal path marks the window closed once its own release commits.
	released := false
	defer releaseAbandonedVerifyLease(s, ctx, req.LeaseID, &released)

	exitCode, output, truncated, runErr := runCommand(ctx, entry.Path, req.Command, maxOutput)
	cancelErr := ctx.Err()
	if cancelErr == nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
		cancelErr = runErr
	}
	if runErr != nil {
		// A command that cannot run is a run outcome, not a store failure:
		// the release below still records and reports it.
		exitCode = -1
		output = append([]byte(runErr.Error()+"\n"), output...)
	}
	if cancelErr != nil {
		exitCode = -1
	}

	// The after snapshot runs before the release transaction opens: a git
	// subprocess must never span a write transaction (CD-0195 D2). Ending
	// the run's authority must not cancel its comparison or durable release.
	finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeVerifyFinalizeTimeout)
	defer finalizeCancel()
	after, err := snapshotTrackedFiles(finalizeCtx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(err, leaseRef)
	}
	afterSubject, err := snapshotOracleGitSubject(finalizeCtx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(err, leaseRef)
	}
	releaseTx, err := s.db.BeginTx(finalizeCtx, nil)
	if err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot begin verify release", true, "retry the same operation with the same lease id", err), leaseRef)
	}
	defer releaseTx.Rollback()
	changed := before != after || beforeSubject.head != afterSubject.head || before.head != beforeSubject.head || after.head != afterSubject.head
	outcome := "completed"
	if changed {
		outcome = "refused_mutated"
	}
	if cancelErr != nil {
		outcome = "aborted"
	}
	boundedOutput, outputTruncated := boundText(string(output), maxOutput)
	if outputTruncated {
		truncated = true
	}
	result := WorktreeVerifyResult{WorkID: req.WorkID, ProjectID: req.ProjectID, Branch: entry.Branch, Path: entry.Path, LeaseID: req.LeaseID, OperationRef: worktreeVerifyOperationRef(req.LeaseID), Command: req.Command, ExitCode: exitCode, Output: boundedOutput, OutputTruncated: truncated, TrackedFilesChanged: changed}
	result.SubjectRef = oracleVerifySubject(beforeSubject, afterSubject, changed)
	resultJSON, _ := json.Marshal(result)
	releasedAt := nowFromClock(nil)
	if _, err := releaseTx.ExecContext(finalizeCtx, `UPDATE worktree_verify_leases SET state='released', released_at=?, exit_code=?, outcome=?, result_json=? WHERE lease_id=? AND state='held'`,
		releasedAt.Format(time.RFC3339Nano), exitCode, outcome, string(resultJSON), req.LeaseID); err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot release the verify lease", true, "retry the same operation with the same lease id", err), leaseRef)
	}
	if cancelErr == nil && !changed && exitCode == 0 {
		// Only a passing run may stand as verification authority. A failed or
		// mutated run stays history in worktree_verify_leases and names no
		// producer operation, so the completion gate cannot consume it.
		if err := recordWorktreeVerifyAuthorityTx(ctx, releaseTx, req, string(commandJSON), now, releasedAt, string(resultJSON)); err != nil {
			return WorktreeVerifyResult{}, annotateCommittedEffect(err, leaseRef)
		}
	}
	if err := releaseTx.Commit(); err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot commit the verify release", true, "retry the same operation with the same lease id", err), leaseRef)
	}
	released = true
	if cancelErr != nil {
		return result, annotateCommittedEffect(cancelErr, leaseRef)
	}
	if changed {
		return result, annotateCommittedEffect(newFailure(KindWorktreeVerifyMutated, "worktree_verify",
			"tracked files changed in "+entry.Path+" while the verify command ran; a verifier that edits its subject verifies nothing (CD-0096 D3)", false, "reconcile_operation"), leaseRef)
	}
	return result, nil
}

// worktreeVerifyFinalizeTimeout bounds both normal and abandoned release.
// These phases must finish even when the caller cancels the command.
const worktreeVerifyFinalizeTimeout = 5 * time.Second

// releaseAbandonedVerifyLease releases the lease on every exit from the
// window between the committed acquire and the committed release: context
// cancellation, an intervening error return, or a panic. The release is
// unconditional because a held lease refuses every later verify on the
// worktree, and the run it names never reached its durable outcome, so the
// abandoned row records outcome 'aborted' — which no completion gate can
// consume as verification authority. When even this release fails, the lease
// stays held and recoverable through the owner-process reclaim in
// acquireVerifyLeaseTx. Defers run after the release transaction's own
// rollback defer, so no other transaction is open here.
func releaseAbandonedVerifyLease(s *Store, ctx context.Context, leaseID string, released *bool) {
	if *released {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeVerifyFinalizeTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(releaseCtx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(releaseCtx, `UPDATE worktree_verify_leases SET state='released', released_at=?, outcome='aborted' WHERE lease_id=? AND state='held'`,
		nowFromClock(nil).Format(time.RFC3339Nano), leaseID); err != nil {
		return
	}
	_ = tx.Commit()
}

// verifyLeaseOwnerProcess is the process identity of the invoke that
// acquired a verify lease. A pid alone is not unique across reboot or
// wraparound; the process start time read from procfs pins the identity, so
// a pid reused by a later process cannot make an older lease look live or a
// live one look dead.
type verifyLeaseOwnerProcess struct {
	pid     int
	started string
}

// currentProcessIdentity observes this process's identity from procfs. An
// empty start time means the identity could not be observed; the reclaim
// reads that as "cannot prove the holder dead" and keeps the refusal.
func currentProcessIdentity() verifyLeaseOwnerProcess {
	pid := os.Getpid()
	started, ok := procfsStartTime(pid)
	if !ok {
		return verifyLeaseOwnerProcess{pid: pid}
	}
	return verifyLeaseOwnerProcess{pid: pid, started: started}
}

// procfsStartTime reads field 22 (starttime, in clock ticks) of
// /proc/<pid>/stat. comm (field 2) may contain spaces and parentheses, so
// the fields after the final ') ' are fields 3..N and starttime is index 19
// of that remainder.
func procfsStartTime(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	_, rest, ok := strings.Cut(string(data), ") ")
	if !ok {
		return "", false
	}
	fields := strings.Fields(rest)
	if len(fields) < 20 {
		return "", false
	}
	return fields[19], true
}

// verifyOwnerProcessGone reports whether the recorded owner process no
// longer exists. pid plus start time is unique on Linux, the sole release
// platform: a readable stat with a different start time is a different,
// later process holding the recycled pid, so the recorded owner is gone. An
// unobservable identity (no procfs, unparseable stat) stays alive, and the
// acquire keeps its refusal rather than guessing.
func verifyOwnerProcessGone(identity verifyLeaseOwnerProcess) bool {
	if identity.pid <= 0 || identity.started == "" {
		return false
	}
	started, ok := procfsStartTime(identity.pid)
	if !ok {
		return true
	}
	return started != identity.started
}

// worktreeVerifyOperationRef is the durable-operation identity of one
// worktree-verify run. Production lease ids append the work identity after the
// digest, but durable_operations already stores work_id. Keep only the digest
// in the operation reference so producer_run_ref stays within its 128-byte
// workflow bound.
func worktreeVerifyOperationRef(leaseID string) string {
	if digest, _, ok := strings.Cut(leaseID, ":worktree-verify:"); ok {
		return "worktree_verify:" + digest
	}
	return "worktree_verify:" + leaseID
}

// recordWorktreeVerifyAuthorityTx claims the green run's durable producer
// operation in the lease release transaction, so the run and its authority
// commit together or not at all. The binding authority checks read only
// durable_operations, and the run claims epoch 1 so it never raises the
// work's MAX(attempt_epoch) that context checkpoints bind. workflow_type_ref
// names the verify tier rather than a workflow family, step_id stays empty
// because the tier runs outside the step graph, and the closed step_kind
// enum's external_effect member is the honest class for a command run.
func recordWorktreeVerifyAuthorityTx(ctx context.Context, tx *sql.Tx, req WorktreeVerifyRequest, commandJSON string, acquired, released time.Time, resultJSON string) error {
	commandDigest := sha256.Sum256([]byte(commandJSON))
	_, err := tx.ExecContext(ctx, `INSERT INTO durable_operations
		(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,
		 accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,
		 result_kind,result_payload,evidence_refs,changed_refs,completed_at)
		VALUES(?, 1, ?, 'worktree.verify', 1, '', 'external_effect', ?, '{}', ?, ?, ?, '', 'completed', ?, ?, '[]', ?)`,
		worktreeVerifyOperationRef(req.LeaseID), req.WorkID,
		"sha256:"+hex.EncodeToString(commandDigest[:]), req.PrincipalRef, req.RequestID,
		acquired.UTC().Format(time.RFC3339Nano),
		resultJSON, workflowJSON([]string{worktreeVerifyOperationRef(req.LeaseID)}),
		released.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return wrapFailure(KindUnavailable, "worktree_verify", "cannot record the verify run's evidence authority", true, "retry the same operation with the same lease id", err)
	}
	return nil
}

// worktreeVerifyCompleted carries the durable outcome of an already-released
// lease back through the acquire error path, so a same-lease retry reports
// the recorded result instead of running the command again.
type worktreeVerifyCompleted struct {
	result  WorktreeVerifyResult
	failure error
}

// annotateCommittedEffect preserves the typed effect boundary when a lease
// transaction or the external command crossed the point where no effect is
// still provable. Pre-effect failures pass through unchanged.
func annotateCommittedEffect(err error, ref SubjectCurrentVersion) error {
	var failure *Failure
	if !errors.As(err, &failure) {
		return err
	}
	failure.EffectPossible = true
	failure.CommittedRefs = append(failure.CommittedRefs, ref)
	return failure
}

func (c *worktreeVerifyCompleted) Error() string {
	return "verify lease already reached its outcome"
}

// acquireVerifyLeaseTx pins the exclusive lease. A foreign held lease for the
// worktree refuses typed, naming the holder. The same lease id resumes only
// under its original owner and pinned command, so an interrupted retry can
// never redirect the run, and a released lease reports its recorded outcome.
func acquireVerifyLeaseTx(ctx context.Context, tx *sql.Tx, req WorktreeVerifyRequest, entry WorktreeEntry, commandJSON string, now time.Time) error {
	var state, pinnedJSON, clientRef, agentRef, sessionRef, outcome, resultJSON string
	err := tx.QueryRowContext(ctx, `SELECT state,command_json,client_ref,agent_ref,session_ref,outcome,coalesce(result_json,'') FROM worktree_verify_leases WHERE lease_id=?`, req.LeaseID).
		Scan(&state, &pinnedJSON, &clientRef, &agentRef, &sessionRef, &outcome, &resultJSON)
	switch {
	case err == nil:
		if state != "held" {
			if outcome == "aborted" {
				// The window abandoned this run before it recorded an
				// outcome: nothing is replayable and the id is spent.
				return newFailure(KindInvalidOperation, "worktree_verify",
					"verify lease "+req.LeaseID+" was abandoned before it recorded an outcome", false, "retry with a new lease id (a new idempotency key)")
			}
			var recorded WorktreeVerifyResult
			var failure error
			if json.Unmarshal([]byte(resultJSON), &recorded) == nil {
				if outcome == "refused_mutated" {
					failure = annotateCommittedEffect(newFailure(KindWorktreeVerifyMutated, "worktree_verify",
						"tracked files changed in "+entry.Path+" while the verify command ran; a verifier that edits its subject verifies nothing (CD-0096 D3)", false, "reconcile_operation"), SubjectCurrentVersion{SubjectType: "worktree_verify_lease", SubjectID: req.LeaseID, Version: 1})
				}
				return &worktreeVerifyCompleted{result: recorded, failure: failure}
			}
			return newFailure(KindUnavailable, "worktree_verify", "released verify lease carries no readable outcome", true, "retry the read of the lease outcome")
		}
		if clientRef != req.Owner.ClientRef || agentRef != req.Owner.AgentRef || sessionRef != req.Owner.SessionRef {
			return newFailure(KindWorktreeLeaseHeld, "worktree_verify",
				"verify lease "+req.LeaseID+" is held by session "+clientRef+"/"+agentRef+"/"+sessionRef, true, "retry_same_request")
		}
		if pinnedJSON != commandJSON {
			return newFailure(KindInvalidOperation, "worktree_verify", "retry does not match the pinned command", false, "retry with the same command or a new idempotency key")
		}
		return nil
	case err == sql.ErrNoRows:
		owner := currentProcessIdentity()
		insertLease := func() error {
			_, insertErr := tx.ExecContext(ctx, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome,owner_pid,owner_started) VALUES(?,?,?,?, 'held', ?,?,?,?,?,?, 'running', ?, ?)`,
				req.LeaseID, req.WorkID, req.ProjectID, entry.Path, req.Owner.ClientRef, req.Owner.AgentRef, req.Owner.SessionRef, req.PrincipalRef, commandJSON, now.Format(time.RFC3339Nano), owner.pid, owner.started)
			return insertErr
		}
		if insertErr := insertLease(); insertErr != nil {
			// The one-held partial index refused: name the actual holder,
			// and reclaim the lease when that holder's process is provably
			// gone (crash, SIGKILL). A live holder keeps the typed refusal.
			holderLeaseID, holder, holderOwner, held, holderErr := heldWorktreeVerifyLeaseTx(ctx, tx, entry.Path)
			if holderErr != nil {
				return holderErr
			}
			if held && verifyOwnerProcessGone(holderOwner) {
				if _, reclaimErr := tx.ExecContext(ctx, `UPDATE worktree_verify_leases SET state='released', released_at=?, outcome='aborted' WHERE lease_id=? AND state='held'`,
					now.Format(time.RFC3339Nano), holderLeaseID); reclaimErr != nil {
					return wrapFailure(KindUnavailable, "worktree_verify", "cannot reclaim the verify lease of a dead holder process", true, "retry the same operation with the same lease id", reclaimErr)
				}
				if insertErr := insertLease(); insertErr != nil {
					return wrapFailure(KindUnavailable, "worktree_verify", "cannot persist the reclaimed verify lease", true, "retry the same operation with the same lease id", insertErr)
				}
				return nil
			}
			if held {
				return newFailure(KindWorktreeLeaseHeld, "worktree_verify",
					"worktree "+entry.Path+" holds an active verify lease held by session "+sessionOwnerLabel(holder), true, "retry_same_request")
			}
			return wrapFailure(KindUnavailable, "worktree_verify", "cannot persist the verify lease", true, "retry the same operation with the same lease id", insertErr)
		}
		return nil
	default:
		return wrapFailure(KindUnavailable, "worktree_verify", "cannot read the verify lease", true, "retry once the database is readable", err)
	}
}

// heldWorktreeVerifyLeaseTx reports the lease and the session holding the
// worktree's active verify lease, with the holder's recorded process
// identity, for the typed contention refusal and the dead-owner reclaim.
func heldWorktreeVerifyLeaseTx(ctx context.Context, tx *sql.Tx, path string) (string, SessionWorktreeOwner, verifyLeaseOwnerProcess, bool, error) {
	var leaseID string
	var holder SessionWorktreeOwner
	var owner verifyLeaseOwnerProcess
	err := tx.QueryRowContext(ctx, `SELECT lease_id,client_ref,agent_ref,session_ref,owner_pid,owner_started FROM worktree_verify_leases WHERE path=? AND state='held'`, path).
		Scan(&leaseID, &holder.ClientRef, &holder.AgentRef, &holder.SessionRef, &owner.pid, &owner.started)
	if err == sql.ErrNoRows {
		return "", SessionWorktreeOwner{}, verifyLeaseOwnerProcess{}, false, nil
	}
	if err != nil {
		return "", SessionWorktreeOwner{}, verifyLeaseOwnerProcess{}, false, wrapFailure(KindUnavailable, "worktree_verify", "cannot read held verify leases", true, "retry once the database is readable", err)
	}
	return leaseID, holder, owner, true, nil
}

// ActiveWorktreeVerifyLease is one held verify lease of the reading
// session, carried by the pinned continuity projection (CD-0096 D5).
type ActiveWorktreeVerifyLease struct {
	LeaseID    string   `json:"lease_id"`
	WorkID     string   `json:"work_id"`
	ProjectID  string   `json:"project_id"`
	Path       string   `json:"path"`
	Command    []string `json:"command"`
	AcquiredAt string   `json:"acquired_at"`
}

// heldWorktreeVerifyLeasesByOwnerTx reads the session's held verify leases,
// newest first, bounded. The continuity re-pin carries them so an
// interrupted verify stays visible to the session that pinned it.
func heldWorktreeVerifyLeasesByOwnerTx(ctx context.Context, tx *sql.Tx, owner SessionWorktreeOwner) ([]ActiveWorktreeVerifyLease, error) {
	rows, err := tx.QueryContext(ctx, `SELECT lease_id,work_id,project_id,path,command_json,acquired_at FROM worktree_verify_leases WHERE client_ref=? AND agent_ref=? AND session_ref=? AND state='held' ORDER BY acquired_at DESC LIMIT 4`,
		owner.ClientRef, owner.AgentRef, owner.SessionRef)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worktree_verify", "cannot read held verify leases", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	leases := []ActiveWorktreeVerifyLease{}
	for rows.Next() {
		var lease ActiveWorktreeVerifyLease
		var commandJSON string
		if err := rows.Scan(&lease.LeaseID, &lease.WorkID, &lease.ProjectID, &lease.Path, &commandJSON, &lease.AcquiredAt); err != nil {
			return nil, wrapFailure(KindUnavailable, "worktree_verify", "cannot decode held verify lease", true, "retry once the database is readable", err)
		}
		lease.Command = []string{}
		_ = json.Unmarshal([]byte(commandJSON), &lease.Command)
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worktree_verify", "cannot enumerate held verify leases", true, "retry once the database is readable", err)
	}
	return leases, nil
}

// worktreeSnapshot is the tracked-file state of one worktree: the porcelain
// status (worktree and index) and the head commit. Two equal snapshots mean
// no tracked file changed between them.
type worktreeSnapshot struct {
	status string
	head   string
}

func snapshotTrackedFiles(ctx context.Context, runner GitRunner, path string) (worktreeSnapshot, error) {
	statusOut, err := runner.Run(ctx, path, "status", "--porcelain")
	if err != nil {
		return worktreeSnapshot{}, wrapFailure(KindGitUnreachable, "worktree_verify", "cannot read worktree status", true, "retry once the worktree is reachable", err)
	}
	headOut, err := runner.Run(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return worktreeSnapshot{}, wrapFailure(KindGitUnreachable, "worktree_verify", "cannot read the worktree head", true, "retry once the worktree is reachable", err)
	}
	return worktreeSnapshot{status: string(statusOut), head: strings.TrimSpace(string(headOut))}, nil
}

// validateWorktreeVerifyCommand accepts a bounded argv. A shell command
// string is never accepted; the values run as separate arguments.
func validateWorktreeVerifyCommand(command []string) error {
	if len(command) < 1 || len(command) > maxWorktreeVerifyCommandArgs {
		return newFailure(KindInvalidOperation, "worktree_verify", "verify command must be one to sixteen argv values", false, "supply the command and its arguments as separate values")
	}
	for _, part := range command {
		if len(part) < 1 || len(part) > maxWorktreeVerifyCommandPart {
			return newFailure(KindInvalidOperation, "worktree_verify", "verify command values must be one to 256 bytes", false, "supply bounded command and argument values")
		}
	}
	return nil
}

// cappedOutput drains a command stream while recording at most limit bytes.
type cappedOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	if w.buffer.Len() >= w.limit {
		w.truncated = true
		return len(p), nil
	}
	room := w.limit - w.buffer.Len()
	if len(p) > room {
		w.buffer.Write(p[:room])
		w.truncated = true
	} else {
		w.buffer.Write(p)
	}
	return len(p), nil
}

// RunWorktreeVerifyCommand executes argv in dir under the caller's context,
// returning the exit code and bounded combined output. A command that cannot
// start reports exit code -1 with the start failure as output. TERM gives a
// wrapper its cleanup window; the owned process group is killed before return.
func RunWorktreeVerifyCommand(ctx context.Context, dir string, command []string, maxOutput int) (int, []byte, bool, error) {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...) //nolint:gosec // argv values stay separate and no shell is invoked; the tier grants the command.
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = worktreeVerifyFinalizeTimeout
	out := &cappedOutput{limit: maxOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		_, _ = out.Write([]byte(err.Error()))
		return -1, out.buffer.Bytes(), out.truncated, nil
	}
	err := cmd.Wait()
	// The command owns this group. It must leave no in-group background
	// writers behind when the lease moves to its after snapshot. Wrappers
	// that create separate groups own their TERM cleanup themselves.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if ctx.Err() != nil {
		return -1, out.buffer.Bytes(), out.truncated, ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), out.buffer.Bytes(), out.truncated, nil
		}
		_, _ = out.Write([]byte("\n" + err.Error()))
		return -1, out.buffer.Bytes(), out.truncated, nil
	}
	return 0, out.buffer.Bytes(), out.truncated, nil
}

// retitleFailure rebrands a typed failure from another operation label so a
// reused validator reports the operation the caller invoked.
func retitleFailure(err error, op string) error {
	var failure *Failure
	if errors.As(err, &failure) {
		branded := *failure
		branded.Op = op
		return &branded
	}
	return err
}

// occupancyPIDStart converts a stored host_pid_start to the kernel's unsigned
// start time. The store writes only values read from /proc, so a negative
// value is a corrupt row, and the read refuses it rather than wrapping it into
// a start time that could match a live process.
func occupancyPIDStart(stored int64) (uint64, error) {
	if stored < 0 {
		return 0, fmt.Errorf("worktree_occupancy host_pid_start %d is negative", stored)
	}
	return uint64(stored), nil
}
