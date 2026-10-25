package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

// SessionVacateTarget is the core-derived destination for a session leaving
// its linked worktree. The destination always comes from the Project locator.
type SessionVacateTarget struct {
	WorkID               string
	ProjectID            string
	SourceDirectory      string
	DestinationDirectory string
	OccupantSessionRef   string
}

// ResolveSessionVacateTargetTx resolves the active worktree at source and the
// registered main checkout for the same Project in one transaction.
func ResolveSessionVacateTargetTx(ctx context.Context, transaction *Transaction, projectID, sourceDirectory string, sessionRef ...string) (SessionVacateTarget, error) {
	var target SessionVacateTarget
	tx, err := transactionSQL(transaction, "session_vacate")
	if err != nil {
		return target, err
	}
	if projectID == "" || sourceDirectory == "" {
		return target, newFailure(KindInvalidOperation, "session_vacate", "vacate requires the resolved Project and linked worktree", false, "run the operation from a linked worktree")
	}
	sourceDirectory = filepath.Clean(sourceDirectory)
	// The source row's session_ref comes from worktree_occupancy (CD-0178
	// D3); the per-session filter narrows the read to the calling session.
	err = tx.QueryRowContext(ctx, `
		SELECT c.work_id, c.project_id, e.path, pl.normalized_value, COALESCE((
			SELECT o.session_ref
			  FROM worktree_occupancy o
			 WHERE o.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id
			 ORDER BY o.recorded_at LIMIT 1
		), '')
		FROM worktree_entries e
		JOIN worktree_claims c ON c.op_id=e.claim_op_id
		JOIN project_locators pl ON pl.project_id=c.project_id AND pl.kind='canonical_path'
		WHERE c.project_id=? AND e.path=? AND e.state='active'
		  AND (? = '' OR NOT EXISTS (
			SELECT 1 FROM worktree_occupancy o2
			 WHERE o2.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id
		  )
		  OR EXISTS (
			SELECT 1 FROM worktree_occupancy o3
			 WHERE o3.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id
			   AND o3.session_ref = ?)
		  )
		ORDER BY pl.locator_id
		LIMIT 1`, projectID, sourceDirectory, firstSessionRef(sessionRef), firstSessionRef(sessionRef)).Scan(&target.WorkID, &target.ProjectID, &target.SourceDirectory, &target.DestinationDirectory, &target.OccupantSessionRef)
	if err == sql.ErrNoRows {
		return target, newFailure(KindProjectionNotFound, "session_vacate", "the session does not run in an active Concord worktree", false, "run the operation from its linked worktree")
	}
	if err != nil {
		return target, wrapFailure(KindUnavailable, "session_vacate", "cannot resolve the session worktree destination", true, "retry once the database is readable", err)
	}
	if filepath.Clean(target.SourceDirectory) == filepath.Clean(target.DestinationDirectory) {
		return target, newFailure(KindInvalidOperation, "session_vacate", "the session is already in the registered main checkout", false, "run the operation from a linked worktree")
	}
	return target, nil
}

type sessionVacatedPayload struct {
	WorkID               string `json:"work_id"`
	ProjectID            string `json:"project_id"`
	SessionRef           string `json:"session_ref"`
	SourceDirectory      string `json:"source_directory"`
	DestinationDirectory string `json:"destination_directory"`
	LandedDirectory      string `json:"landed_directory"`
}

func firstSessionRef(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	return refs[0]
}

// validateSessionVacatedPayload enforces the append-time shape. A version 2
// relocation request names no landing: the landing is the vacate-landing
// verb's evidence, never the requester's claim.
func validateSessionVacatedPayload(event Event, p sessionVacatedPayload) error {
	if p.LandedDirectory != "" {
		return newFailure(KindInvalidPayload, "validate_event", "a session vacate request records no landing", false, "record the verified landing through the adapter-only vacate-landing verb")
	}
	return nil
}

// CountWorkSessionVacatesTx returns the number of accepted vacate requests
// for one work item and session, not confirmed host moves. The current counter
// supplies the next relocation identity's ordinal, including requests that
// replaced an earlier request. The caller's transaction sees its own writes.
func CountWorkSessionVacatesTx(ctx context.Context, transaction *Transaction, workID, sessionRef string) (int, error) {
	tx, err := transactionSQL(transaction, "session_vacate")
	if err != nil {
		return 0, err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT vacate_count FROM session_relocation_counts WHERE work_id=? AND session_ref=?),0)`, workID, sessionRef).Scan(&count)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "session_vacate", "cannot count the recorded vacate events", true, "retry once the database is readable", err)
	}
	return count, nil
}

func foldSessionVacated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p sessionVacatedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.WorkID == "" || p.WorkID != event.SubjectID || p.ProjectID == "" || p.SessionRef == "" || p.SourceDirectory == "" {
		return newFailure(KindInvalidPayload, "fold_event", "session vacated payload is missing required fields", false, "supply work, project, session, and source directory")
	}
	return writeSessionVacateRequestTx(ctx, tx, event, p)
}

// sessionVacateLandedPayload is the durable record of one verified vacate
// landing. The host moved the session and read its directory back as the
// registered main checkout the committed vacate request names before this
// event exists, so the event is evidence of a landing, never an intention to
// land. HostPID is the OpenCode process that recorded the landing;
// HostPIDStart is the value the core derived from /proc at record time so the
// recorded identity survives the host process later dying.
type sessionVacateLandedPayload struct {
	WorkID               string   `json:"work_id"`
	ProjectID            string   `json:"project_id"`
	SessionRef           string   `json:"session_ref"`
	SourceDirectories    []string `json:"source_directories"`
	DestinationDirectory string   `json:"destination_directory"`
	LandedDirectory      string   `json:"landed_directory"`
	HostPID              int      `json:"host_pid"`
	HostPIDStart         uint64   `json:"host_pid_start"`
}

// SessionVacateLandingRequest records the verified landing of a session at
// the registered main checkout a committed session vacate names. The
// adapter-only vacate-landing verb is the caller, mirroring claim-landing:
// the host proves the landing by readback, and the landing releases the
// session's occupancy rows in the same transaction that records it.
type SessionVacateLandingRequest struct {
	WorkID          string
	SessionRef      string
	LandedDirectory string
	HostPID         int
	Now             time.Time
}

// SessionVacateLandingResult names the verified landing. ReleasedSources
// holds the active rows whose occupancy the landing cleared, in any work
// item; an empty list means the session held no rows to release.
type SessionVacateLandingResult struct {
	WorkID               string   `json:"work_id"`
	ProjectID            string   `json:"project_id"`
	SessionRef           string   `json:"session_ref"`
	DestinationDirectory string   `json:"destination_directory"`
	LandedDirectory      string   `json:"landed_directory"`
	ReleasedSources      []string `json:"released_sources,omitempty"`
	AlreadyRecorded      bool     `json:"already_recorded"`
	HostPID              int      `json:"host_pid"`
	HostPIDStart         uint64   `json:"host_pid_start"`
}

// RecordSessionVacateLanding records a verified vacate landing and releases
// the calling session's occupancy rows in one transaction. The landing
// verifies against the committed relocation request: a session vacate names
// the registered main checkout the core derived before the adapter moved the
// host session, so a landing anywhere else refuses and the refusal reports
// the committed request truthfully. A refused move, a destination mismatch,
// or an unreadable landing records nothing, so the CD-0096 D3 removal gate
// never sees a live session's worktree as empty. The landing binds to the
// pending request it completes (CD-0190 D2): the core records a landing only
// while the latest committed request stands with no recorded landing after
// it. A request its recorded landing already completed replays as
// AlreadyRecorded with no event while the session holds no rows, and refuses
// once a later claim's rows stand, so a landing never releases rows the
// session claimed after that landing. The host pid is the only identity the
// adapter carries; the core reads pid_start itself through
// hostlease.ProcessStart and never trusts a caller-supplied start time.
func (s *Store) RecordSessionVacateLanding(ctx context.Context, req SessionVacateLandingRequest) (SessionVacateLandingResult, error) {
	return recordLanding(s, ctx, "vacate-landing", req.WorkID, req.SessionRef, req.LandedDirectory, req.HostPID, func(ctx context.Context, tx *sql.Tx) (SessionVacateLandingResult, error) {
		return recordSessionVacateLandingTx(ctx, tx, req)
	})
}

func recordSessionVacateLandingTx(ctx context.Context, tx *sql.Tx, req SessionVacateLandingRequest) (SessionVacateLandingResult, error) {
	out := SessionVacateLandingResult{WorkID: req.WorkID, SessionRef: req.SessionRef, LandedDirectory: filepath.Clean(req.LandedDirectory), HostPID: req.HostPID}
	// The kernel is the authority for the host process start time.
	pidStart, err := hostlease.ProcessStart(req.HostPID)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "vacate-landing", "cannot read the host process start time", true, "retry once the host process is observable", err)
	}
	out.HostPIDStart = pidStart
	latest, err := latestSessionVacateRequestTx(ctx, tx, req.WorkID, req.SessionRef)
	if err != nil {
		return out, err
	}
	requested := latest.Payload
	out.ProjectID = requested.ProjectID
	out.DestinationDirectory = requested.DestinationDirectory
	if requested.DestinationDirectory == "" || filepath.Clean(requested.DestinationDirectory) != out.LandedDirectory {
		return out, newFailure(KindProjectionNotFound, "vacate-landing", fmt.Sprintf("the committed session vacate names %s as its registered main checkout, not %s", requested.DestinationDirectory, out.LandedDirectory), false, "land at the registered main checkout the committed vacate names")
	}
	// The landing reads only the calling session's own rows: every active
	// worktree the session holds, in any work item, is stale the moment the
	// host readback names the registered main checkout (CD-0179).
	sources, err := sessionOccupiedSourcesTx(ctx, tx, req.WorkID, req.SessionRef, "", "")
	if err != nil {
		return out, err
	}
	landed := latest.Landed
	recorded, err := countSessionVacateLandingsTx(ctx, tx, req.WorkID, req.SessionRef)
	if err != nil {
		return out, err
	}
	// Replay is read from state, not from a derived event id: when the
	// request's own landing already stands recorded, the landing replays
	// idempotently with no event while the session holds no rows. Rows the
	// session still holds then belong to a later claim, so the completed
	// request refuses instead of releasing them (CD-0190 D2).
	if landed {
		if len(sources) == 0 {
			out.AlreadyRecorded = true
			return out, nil
		}
		return out, newFailure(KindInvalidOperation, "vacate-landing", fmt.Sprintf("the committed session vacate of %s already completed its verified landing; the occupancy rows the session holds belong to a later claim", req.WorkID), false, "release the later claim's rows through their own verified landing or vacate")
	}
	out.ReleasedSources = sources
	// One verified landing is the unit of identity: the recorded landing
	// count for this work item and session is the ordinal of this event,
	// mirroring the vacate ordinal in CD-0120 D4 and the claim-landing
	// ordinal.
	eventID := fmt.Sprintf("%s:session-vacate-landed:%s:%d", req.WorkID, req.SessionRef, recorded+1)
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	payload, err := json.Marshal(sessionVacateLandedPayload{WorkID: req.WorkID, ProjectID: requested.ProjectID, SessionRef: req.SessionRef, SourceDirectories: sources, DestinationDirectory: requested.DestinationDirectory, LandedDirectory: out.LandedDirectory, HostPID: req.HostPID, HostPIDStart: pidStart})
	if err != nil {
		return out, err
	}
	if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: eventID, Kind: "work.session_vacate_landed", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.SessionRef, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}}, newWriteScope(tx), false); err != nil {
		return out, err
	}
	return out, nil
}

// SessionVacateReplayTarget names the committed relocation request that a
// session_vacate replay from the verified destination resolves to.
type SessionVacateReplayTarget struct {
	WorkID               string
	ProjectID            string
	SourceDirectory      string
	DestinationDirectory string
}

// ResolveSessionVacateReplayTargetTx resolves the newest committed vacate
// request of one session that names the caller's current directory as its
// registered main checkout or as its source worktree, and only while that
// request is pending: no verified landing has completed it.
// A host move that landed without a recorded landing leaves such a request
// standing and the session outside every active worktree, so the replay from
// the verified destination is the recovery: the core resolves it to the
// pending request and appends nothing, and the adapter-only vacate-landing
// verb records the landing and releases the session's rows. The source
// match serves the replay that arrives while the session still runs in the
// worktree the request asks it to leave — a commit whose host move never ran,
// or an adapter restart that lost the remembered destination — so the
// replay resolves the pending request and the adapter performs the move and
// landing from its answer. A request the recorded landing already completed
// resolves the same way while the session holds no occupancy rows: the
// caller's readback-verified landing call then replays idempotently with no
// event, so an uncertain landing result recovers (CD-0190 D3). Once a later
// claim's rows stand, the completed request refuses with no event, because
// resolving it could append a landing that releases rows the later claim
// still holds (CD-0190 D2). It runs inside the caller's transaction so the
// read observes the caller's own uncommitted state.
func ResolveSessionVacateReplayTargetTx(ctx context.Context, transaction *Transaction, projectID, directory, sessionRef string) (SessionVacateReplayTarget, error) {
	var target SessionVacateReplayTarget
	tx, err := transactionSQL(transaction, "session_vacate")
	if err != nil {
		return target, err
	}
	if projectID == "" || directory == "" || sessionRef == "" {
		return target, newFailure(KindInvalidOperation, "session_vacate", "the vacate replay requires the resolved Project, session, and directory", false, "run the operation from a linked worktree or the verified destination")
	}
	cleaned := filepath.Clean(directory)
	request, err := scanSessionVacateRequest(tx.QueryRowContext(ctx, `SELECT `+sessionVacateRequestColumns+` FROM session_vacate_requests WHERE session_ref=? AND project_id=? AND (destination_directory=? OR source_directory=?) ORDER BY request_seq DESC LIMIT 1`, sessionRef, projectID, cleaned, cleaned))
	if err == sql.ErrNoRows {
		return target, newFailure(KindProjectionNotFound, "session_vacate", "no committed session vacate names this directory as its registered main checkout or source worktree", false, "run session_vacate from a linked worktree")
	}
	if err != nil {
		return target, wrapFailure(KindUnavailable, "session_vacate", "cannot read the committed vacate request", true, "retry once the database is readable", err)
	}
	p := request.Payload
	if request.Landed {
		// The request's own landing already stands. The session holds no
		// stale row from it, so the replay resolves as a completed replay
		// while every row is gone, and refuses once a later claim's rows
		// stand: those rows belong to the later claim, whose own verified
		// landing or vacate releases them (CD-0190 D2).
		rows, err := sessionOccupiedSourcesTx(ctx, tx, p.WorkID, sessionRef, "", "")
		if err != nil {
			return target, err
		}
		if len(rows) > 0 {
			return target, newFailure(KindInvalidOperation, "session_vacate", fmt.Sprintf("the committed session vacate of %s already completed its verified landing; the occupancy rows the session holds belong to a later claim", p.WorkID), false, "release the later claim's rows through their own verified landing or vacate")
		}
	}
	// The destination always answers from the committed request, never from
	// the caller's directory: a source-match replay still owes the move to
	// the registered main checkout the request names.
	return SessionVacateReplayTarget{WorkID: p.WorkID, ProjectID: p.ProjectID, SourceDirectory: p.SourceDirectory, DestinationDirectory: filepath.Clean(p.DestinationDirectory)}, nil
}

// PendingSessionVacateSourceRequestTx resolves the still-pending vacate
// request of one session whose source worktree is the caller's
// directory. session_vacate called again re-reads the committed request's
// state and resolves it, appends nothing, whatever idempotency key the retry
// carries (CD-0190 D3), so the planner answers a new-key retry through this
// resolution instead of recording a second relocation request for one move.
// A request its recorded landing already completed keeps the fresh-request
// path, so a re-occupied worktree records its own new request (CD-0190 D2).
// It runs inside the caller's transaction so the read observes the caller's
// own uncommitted state.
func PendingSessionVacateSourceRequestTx(ctx context.Context, transaction *Transaction, projectID, sourceDirectory, sessionRef string) (SessionVacateReplayTarget, error) {
	var target SessionVacateReplayTarget
	tx, err := transactionSQL(transaction, "session_vacate")
	if err != nil {
		return target, err
	}
	if projectID == "" || sourceDirectory == "" || sessionRef == "" {
		return target, newFailure(KindProjectionNotFound, "session_vacate", "no pending session vacate names this directory as its source worktree", false, "run the operation from a linked worktree")
	}
	cleaned := filepath.Clean(sourceDirectory)
	request, err := scanSessionVacateRequest(tx.QueryRowContext(ctx, `SELECT `+sessionVacateRequestColumns+` FROM session_vacate_requests WHERE session_ref=? AND project_id=? AND source_directory=? ORDER BY request_seq DESC LIMIT 1`, sessionRef, projectID, cleaned))
	if err == sql.ErrNoRows {
		return target, newFailure(KindProjectionNotFound, "session_vacate", "no pending session vacate names this directory as its source worktree", false, "run the operation from a linked worktree")
	}
	if err != nil {
		return target, wrapFailure(KindUnavailable, "session_vacate", "cannot read the committed vacate request", true, "retry once the database is readable", err)
	}
	p := request.Payload
	if p.DestinationDirectory == "" {
		return target, newFailure(KindInvalidPayload, "session_vacate", "the committed vacate request names no registered main checkout", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	if request.Landed {
		return target, newFailure(KindProjectionNotFound, "session_vacate", "no pending session vacate names this directory as its source worktree", false, "run the operation from a linked worktree")
	}
	return SessionVacateReplayTarget{WorkID: p.WorkID, ProjectID: p.ProjectID, SourceDirectory: filepath.Clean(p.SourceDirectory), DestinationDirectory: filepath.Clean(p.DestinationDirectory)}, nil
}

// ResolveSessionVacateReplayTx resolves a same-key session_vacate replay
// from the caller's current directory state-driven, appending nothing. The
// replay re-reads the committed request's state on every call, so a cached
// success can never answer for a state that moved on: a pending request
// resolves from the verified destination or from its source worktree, a
// request whose landing completed resolves while the session holds no rows,
// and everything else refuses. When the directory names no request of this
// session but the session's own occupancy rows stand in an active worktree,
// the replay refuses with the recovery that releases those rows, because
// they belong to the claim that recorded them and no pending request of
// this replay releases them (CD-0190 D2). replayWorkID is the work item the
// replayed idempotency record committed its request for; it grounds the
// refusal's wording when the record names one. It runs inside the caller's
// transaction so the read observes the caller's own uncommitted state.
func ResolveSessionVacateReplayTx(ctx context.Context, transaction *Transaction, projectID, directory, sessionRef, replayWorkID string) (SessionVacateReplayTarget, error) {
	target, err := ResolveSessionVacateReplayTargetTx(ctx, transaction, projectID, directory, sessionRef)
	if err == nil {
		return target, nil
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound {
		return target, err
	}
	occupied, occupiedErr := ResolveSessionVacateTargetTx(ctx, transaction, projectID, directory, sessionRef)
	if occupiedErr == nil && occupied.OccupantSessionRef == sessionRef {
		return target, sessionVacateReplayOccupiedFailure(ctx, transaction, replayWorkID, sessionRef)
	}
	return target, err
}

// sessionVacateReplayOccupiedFailure refuses a vacate replay whose session
// holds occupancy rows in an active worktree the replay resolves no pending
// request for. The rows belong to the claim that recorded them, so the
// refusal appends nothing and names the recovery that releases them. The
// wording reads the replayed request's own state, so it never claims a
// completion that did not happen.
func sessionVacateReplayOccupiedFailure(ctx context.Context, transaction *Transaction, workID, sessionRef string) error {
	tx, err := transactionSQL(transaction, "session_vacate")
	if err != nil {
		return err
	}
	if workID != "" {
		latest, reqErr := latestSessionVacateRequestTx(ctx, tx, workID, sessionRef)
		if reqErr == nil {
			if latest.Landed {
				return newFailure(KindInvalidOperation, "session_vacate", fmt.Sprintf("the committed session vacate of %s already completed its verified landing; the occupancy rows the session holds belong to a later claim", workID), false, "release the later claim's rows through their own verified landing or vacate")
			}
			return newFailure(KindInvalidOperation, "session_vacate", fmt.Sprintf("the committed session vacate of %s still waits for its verified landing, and the occupancy rows the session holds belong to a later claim; recording that landing would release rows the later claim holds", workID), false, "release the later claim's rows through their own verified landing or vacate, then replay this request from the verified destination")
		}
	}
	return newFailure(KindInvalidOperation, "session_vacate", "the occupancy rows the session holds belong to the claim that recorded them, and this replay resolves no pending request that releases them", false, "release the later claim's rows through their own verified landing or vacate")
}

// latestSessionVacateRequestTx reads the newest standing vacate request of
// one work item and session and whether a verified landing completed it. A
// write caller passes its transaction so the read observes that caller's
// uncommitted state.
func latestSessionVacateRequestTx(ctx context.Context, q queryer, workID, sessionRef string) (sessionVacateRequest, error) {
	request, err := scanSessionVacateRequest(q.QueryRowContext(ctx, `SELECT `+sessionVacateRequestColumns+` FROM session_vacate_requests WHERE work_id=? AND session_ref=? ORDER BY request_seq DESC LIMIT 1`, workID, sessionRef))
	if err == sql.ErrNoRows {
		return request, newFailure(KindProjectionNotFound, "vacate-landing", "no committed session vacate names this work item and session", false, "run session_vacate from the linked worktree before recording its landing")
	}
	if err != nil {
		return request, wrapFailure(KindUnavailable, "vacate-landing", "cannot read the committed vacate request", true, "retry once the database is readable", err)
	}
	return request, nil
}

// countSessionVacateLandingsTx reads the current count of accepted vacate
// landings for one work item and session: the next landing identity's ordinal.
func countSessionVacateLandingsTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT vacate_landing_count FROM session_relocation_counts WHERE work_id=? AND session_ref=?),0)`, workID, sessionRef).Scan(&count)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "vacate-landing", "cannot count the recorded landing events", true, "retry once the database is readable", err)
	}
	return count, nil
}

// foldSessionVacateLanded applies the verified release: the host readback
// named the registered main checkout the committed request
// names, so every occupancy row the session still holds, in any work item,
// is stale and releases (CD-0179). The registered main checkout holds no
// worktree row of its own, so nothing is recorded where the session landed.
// The fold trusts the recorded host_pid_start: the value was derived from
// /proc at record time and survives a process that later died.
func foldSessionVacateLanded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p sessionVacateLandedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.WorkID == "" || p.WorkID != event.SubjectID || p.ProjectID == "" || p.SessionRef == "" || p.DestinationDirectory == "" || p.LandedDirectory == "" {
		return newFailure(KindInvalidPayload, "fold_event", "session vacate landed payload is missing required fields", false, "supply work, project, session, destination, and landed directory")
	}
	if p.HostPID <= 0 {
		return newFailure(KindInvalidPayload, "fold_event", "session vacate landed payload is missing host_pid", false, "supply the host process pid the adapter recorded")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worktree_occupancy WHERE session_ref=?`, p.SessionRef); err != nil {
		return err
	}
	return writeSessionVacateLandingTx(ctx, tx, event, p.SessionRef)
}
