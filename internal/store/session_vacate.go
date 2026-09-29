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

// upcastSessionVacatedV1 carries a version 1 event to the version 2 shape.
// The v1 payload already names every field v2 records, so only the version
// moves. The fold reads the stored version back through
// replaySourcePayloadVersion, so a recorded v1 event still performs the
// release it performed when the writer that recorded it folded it at commit
// time.
func upcastSessionVacatedV1(event Event) (Event, error) {
	event.PayloadVersion = 2
	return event, nil
}

// validateSessionVacatedPayload enforces the append-time shape. A version 2
// relocation request names no landing: the landing is the vacate-landing
// verb's evidence, never the requester's claim. Recorded v1 events keep the
// shape they were written with.
func validateSessionVacatedPayload(event Event, p sessionVacatedPayload) error {
	if event.replaySourcePayloadVersion == 0 && p.LandedDirectory != "" {
		return newFailure(KindInvalidPayload, "validate_event", "a session vacate request records no landing", false, "record the verified landing through the adapter-only vacate-landing verb")
	}
	return nil
}

// CountWorkSessionVacatesTx returns how many vacate operations the
// projection already records for one work item and session. A recorded event
// is the durable record of one vacate the core accepted, written before the
// adapter moves the host session; it is not proof the session landed. The
// count is therefore the ordinal of the next relocation request, not a count
// of confirmed host moves. Read-only work resume writes no event and no
// session-directory binding, so the recorded vacate history is the only
// projection that distinguishes repeated vacates of the same work item
// by the same session. It runs inside the caller's transaction so the read
// observes the caller's own uncommitted events.
func CountWorkSessionVacatesTx(ctx context.Context, transaction *Transaction, workID, sessionRef string) (int, error) {
	tx, err := transactionSQL(transaction, "session_vacate")
	if err != nil {
		return 0, err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE kind='work.session_vacated' AND subject_id=? AND json_extract(payload,'$.session_ref')=?`, workID, sessionRef).Scan(&count)
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
	// A version 1 event recorded the release in the same event the vacate
	// committed, so replay still performs it. A version 2 event records only
	// the relocation request: the release waits for the verified landing the
	// vacate-landing verb records, so a refused host move, a destination
	// mismatch, or an unreadable landing leaves every occupancy row standing.
	if event.replaySourcePayloadVersion == 1 {
		return releaseSessionWorktreeOccupancyTx(ctx, tx, "fold_event", p.WorkID, p.ProjectID, p.SourceDirectory, p.SessionRef)
	}
	return nil
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
	if s == nil || s.db == nil {
		return SessionVacateLandingResult{}, newFailure(KindUnavailable, "vacate-landing", "store is not open", false, "open the authority database")
	}
	if req.WorkID == "" || req.SessionRef == "" || req.LandedDirectory == "" {
		return SessionVacateLandingResult{}, newFailure(KindInvalidOperation, "vacate-landing", "landing is missing the work, session, or landed directory", false, "supply the work id, session ref, and verified landed path")
	}
	if req.HostPID <= 0 {
		return SessionVacateLandingResult{}, newFailure(KindInvalidOperation, "vacate-landing", "landing requires the host process pid", false, "supply the adapter's process.pid with the landing request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionVacateLandingResult{}, wrapFailure(KindUnavailable, "vacate-landing", "cannot begin landing", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	out, err := recordSessionVacateLandingTx(ctx, tx, req)
	if err != nil {
		return SessionVacateLandingResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SessionVacateLandingResult{}, wrapFailure(KindUnavailable, "vacate-landing", "cannot commit landing", true, "retry the same landing", err)
	}
	return out, nil
}

func recordSessionVacateLandingTx(ctx context.Context, tx *sql.Tx, req SessionVacateLandingRequest) (SessionVacateLandingResult, error) {
	out := SessionVacateLandingResult{WorkID: req.WorkID, SessionRef: req.SessionRef, LandedDirectory: filepath.Clean(req.LandedDirectory), HostPID: req.HostPID}
	// The kernel is the authority for the host process start time. Reading
	// here keeps hostlease imports out of every caller and lets the fold
	// replay the recorded value without touching /proc.
	pidStart, err := hostlease.ProcessStart(req.HostPID)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "vacate-landing", "cannot read the host process start time", true, "retry once the host process is observable", err)
	}
	out.HostPIDStart = pidStart
	requested, requestedSeq, requestedVersion, err := latestSessionVacateRequestTx(ctx, tx, req.WorkID, req.SessionRef)
	if err != nil {
		return out, err
	}
	out.ProjectID = requested.ProjectID
	out.DestinationDirectory = requested.DestinationDirectory
	if requested.DestinationDirectory == "" || filepath.Clean(requested.DestinationDirectory) != out.LandedDirectory {
		return out, newFailure(KindProjectionNotFound, "vacate-landing", fmt.Sprintf("the committed session vacate names %s as its registered main checkout, not %s", requested.DestinationDirectory, out.LandedDirectory), false, "land at the registered main checkout the committed vacate names")
	}
	// A landing binds to a pending version 2 request. A request that predates
	// the landing-verified release already released on fold, so no pending
	// request binds a landing here.
	if requestedVersion != 2 {
		return out, newFailure(KindInvalidOperation, "vacate-landing", fmt.Sprintf("the committed session vacate of %s predates the landing-verified release and already released on fold, so no pending request binds this landing", req.WorkID), false, "no recovery: the session holds no occupancy row from that request")
	}
	// The landing reads only the calling session's own rows: every active
	// worktree the session holds, in any work item, is stale the moment the
	// host readback names the registered main checkout (CD-0179).
	sources, err := sessionOccupiedSourcesTx(ctx, tx, req.WorkID, req.SessionRef, "", "")
	if err != nil {
		return out, err
	}
	landed, err := sessionVacateLandedAfterTx(ctx, tx, req.WorkID, req.SessionRef, requestedSeq)
	if err != nil {
		return out, err
	}
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
	}}}, newFoldScope(tx), false); err != nil {
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
// request of one session whose registered main checkout is the caller's
// current directory, and only while that request is pending: a version 2
// request with no recorded landing after it. A host move that landed without
// a recorded landing leaves such a request standing and the session outside
// every active worktree, so the replay from the verified destination is the
// recovery: the core resolves it to the pending request and appends nothing,
// and the adapter-only vacate-landing verb records the landing and releases
// the session's rows. A request the recorded landing already completed
// resolves the same way while the session holds no occupancy rows: the
// caller's readback-verified landing call then replays idempotently with no
// event, so an uncertain landing result recovers (CD-0190 D3). Once a later
// claim's rows stand, the completed request refuses with no event, because
// resolving it could append a landing that releases rows the later claim
// still holds (CD-0190 D2). A version 1 request that released its rows on
// fold refuses with no event for the same reason. It runs inside the
// caller's transaction so the read observes the caller's own uncommitted
// events.
func ResolveSessionVacateReplayTargetTx(ctx context.Context, transaction *Transaction, projectID, directory, sessionRef string) (SessionVacateReplayTarget, error) {
	var target SessionVacateReplayTarget
	tx, err := transactionSQL(transaction, "session_vacate")
	if err != nil {
		return target, err
	}
	if projectID == "" || directory == "" || sessionRef == "" {
		return target, newFailure(KindInvalidOperation, "session_vacate", "the vacate replay requires the resolved Project, session, and directory", false, "run the operation from a linked worktree or the verified destination")
	}
	var raw string
	var seq, payloadVersion int
	err = tx.QueryRowContext(ctx, `SELECT seq, payload_version, payload FROM domain_events WHERE kind='work.session_vacated' AND json_extract(payload,'$.session_ref')=? AND json_extract(payload,'$.project_id')=? AND json_extract(payload,'$.destination_directory')=? ORDER BY seq DESC LIMIT 1`, sessionRef, projectID, filepath.Clean(directory)).Scan(&seq, &payloadVersion, &raw)
	if err == sql.ErrNoRows {
		return target, newFailure(KindProjectionNotFound, "session_vacate", "no committed session vacate names this directory as its registered main checkout", false, "run session_vacate from a linked worktree")
	}
	if err != nil {
		return target, wrapFailure(KindUnavailable, "session_vacate", "cannot read the committed vacate request", true, "retry once the database is readable", err)
	}
	var p sessionVacatedPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return target, newFailure(KindInvalidPayload, "session_vacate", "the recorded vacate payload does not decode", false, "repair the recorded event")
	}
	if p.WorkID == "" || p.SourceDirectory == "" {
		return target, newFailure(KindInvalidPayload, "session_vacate", "the recorded vacate payload is missing required fields", false, "repair the recorded event")
	}
	if payloadVersion != 2 {
		return target, newFailure(KindInvalidOperation, "session_vacate", fmt.Sprintf("the committed session vacate of %s predates the landing-verified release and already released on fold, so no pending request replays here", p.WorkID), false, "no recovery: the session holds no occupancy row from that request")
	}
	landed, err := sessionVacateLandedAfterTx(ctx, tx, p.WorkID, sessionRef, seq)
	if err != nil {
		return target, err
	}
	if landed {
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
	return SessionVacateReplayTarget{WorkID: p.WorkID, ProjectID: p.ProjectID, SourceDirectory: p.SourceDirectory, DestinationDirectory: filepath.Clean(directory)}, nil
}

// sessionVacateLandedAfterTx reports whether a verified vacate landing of
// one work item and session stands recorded after the given event sequence.
// A landing recorded after a vacate request is the landing that completed it,
// so the request is no longer pending. It runs inside the caller's
// transaction so the read observes the caller's own uncommitted events.
func sessionVacateLandedAfterTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string, afterSeq int) (bool, error) {
	var landed int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events WHERE kind='work.session_vacate_landed' AND subject_id=? AND json_extract(payload,'$.session_ref')=? AND seq>?)`, workID, sessionRef, afterSeq).Scan(&landed)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "session_vacate", "cannot read the recorded landing events", true, "retry once the database is readable", err)
	}
	return landed == 1, nil
}

// latestSessionVacateRequestTx reads the newest vacate event of one work item
// and session with its event sequence and payload version: the sequence
// orders the request against the landing events that could complete it, and
// the version separates a pending relocation request from a request that
// already released on fold. It runs inside the caller's transaction so the
// read observes the caller's own uncommitted events.
func latestSessionVacateRequestTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string) (sessionVacatedPayload, int, int, error) {
	var raw string
	var seq, payloadVersion int
	var p sessionVacatedPayload
	err := tx.QueryRowContext(ctx, `SELECT seq, payload_version, payload FROM domain_events WHERE kind='work.session_vacated' AND subject_id=? AND json_extract(payload,'$.session_ref')=? ORDER BY seq DESC LIMIT 1`, workID, sessionRef).Scan(&seq, &payloadVersion, &raw)
	if err == sql.ErrNoRows {
		return p, 0, 0, newFailure(KindProjectionNotFound, "vacate-landing", "no committed session vacate names this work item and session", false, "run session_vacate from the linked worktree before recording its landing")
	}
	if err != nil {
		return p, 0, 0, wrapFailure(KindUnavailable, "vacate-landing", "cannot read the committed vacate request", true, "retry once the database is readable", err)
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, 0, 0, newFailure(KindInvalidPayload, "vacate-landing", "the recorded vacate payload does not decode", false, "repair the recorded event")
	}
	return p, seq, payloadVersion, nil
}

// countSessionVacateLandingsTx returns how many vacate landing events the
// projection already records for one work item and session. It runs inside
// the caller's transaction so the read observes the caller's own uncommitted
// events, and the count is the ordinal of the next landing event.
func countSessionVacateLandingsTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE kind='work.session_vacate_landed' AND subject_id=? AND json_extract(payload,'$.session_ref')=?`, workID, sessionRef).Scan(&count)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "vacate-landing", "cannot count the recorded landing events", true, "retry once the database is readable", err)
	}
	return count, nil
}

// foldSessionVacateLanded re-applies the verified release during rebuild: the
// host readback named the registered main checkout the committed request
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
	_, err := tx.ExecContext(ctx, `DELETE FROM worktree_occupancy WHERE session_ref=?`, p.SessionRef)
	return err
}
