package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Project-session handoff (CD-0182 amendment): a bounded typed work-scoped
// projection next to the shared-work continuity owners. One handoff records
// one Project-selected session's bounded repository job and its explicit
// receiving Project on the SAME work item and active contract, and the
// receiving side's core consume binds the record to the work-and-Project
// pair before managed execution (CD-0182 D5 amendment: the bind no longer
// names one receiving session, so a second landed coordinator session of
// the receiving Project neither records a fresh handoff nor opens a new
// session). The state lives beside the
// work, never in a second work item, a peer message, or a knowledge kind.
//
// Lifecycle: recorded -> consumed. Failed, unreadable, stale, and
// wrong-target conditions stay typed refusals with no state change, so a
// replay resolves from state and a stale handoff never authorizes execution.
// Retirement readiness is derived read-only from the recorded handoff,
// verified artifact preservation, stopped session-owned execution, and the
// verified vacate landing; it never completes or cancels the shared work and
// never terminates a host process or removes a worktree.

const (
	ProjectHandoffRecorded = "recorded"
	ProjectHandoffConsumed = "consumed"

	projectHandoffIDMax     = 256
	projectHandoffJobMax    = 4000
	projectHandoffActionMax = 1000
	projectHandoffListMax   = 16
	projectHandoffEntryMax  = 512

	// projectHandoffArtifactMaxBytes bounds one preserved artifact the
	// digest probe reads.
	projectHandoffArtifactMaxBytes = 8 << 20
)

// ProjectHandoff is the typed current projection of one handoff record.
type ProjectHandoff struct {
	HandoffID            string   `json:"handoff_id"`
	WorkID               string   `json:"work_id"`
	ContractVersion      int64    `json:"contract_version"`
	SourceProjectID      string   `json:"source_project_id"`
	TargetProjectID      string   `json:"target_project_id"`
	SourceSessionRef     string   `json:"source_session_ref"`
	BoundedJob           string   `json:"bounded_job"`
	Changes              []string `json:"changes"`
	Verification         []string `json:"verification"`
	ArtifactRefs         []string `json:"artifact_refs"`
	Blockers             []string `json:"blockers"`
	NextAction           string   `json:"next_action"`
	State                string   `json:"state"`
	ConsumedBySessionRef string   `json:"consumed_by_session_ref,omitempty"`
	ConsumedAt           string   `json:"consumed_at,omitempty"`
	RecordedAt           string   `json:"recorded_at"`
}

type projectHandoffRecordedPayload struct {
	WorkID           string   `json:"work_id"`
	HandoffID        string   `json:"handoff_id"`
	ContractVersion  int64    `json:"contract_version"`
	SourceProjectID  string   `json:"source_project_id"`
	TargetProjectID  string   `json:"target_project_id"`
	SourceSessionRef string   `json:"source_session_ref"`
	BoundedJob       string   `json:"bounded_job"`
	Changes          []string `json:"changes"`
	Verification     []string `json:"verification"`
	ArtifactRefs     []string `json:"artifact_refs"`
	Blockers         []string `json:"blockers"`
	NextAction       string   `json:"next_action"`
	RecordedAt       string   `json:"recorded_at"`
}

type projectHandoffConsumedPayload struct {
	HandoffID            string `json:"handoff_id"`
	ContractVersion      int64  `json:"contract_version"`
	TargetProjectID      string `json:"target_project_id"`
	ConsumedBySessionRef string `json:"consumed_by_session_ref"`
	ConsumedAt           string `json:"consumed_at"`
}

func validateProjectHandoffStrings(field string, values []string) error {
	if len(values) > projectHandoffListMax {
		return newFailure(KindLimitExceeded, "project_handoff", fmt.Sprintf("%s exceeds %d entries", field, projectHandoffListMax), false, "reduce_limit")
	}
	for _, value := range values {
		if len(value) == 0 || len(value) > projectHandoffEntryMax {
			return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("%s carries an empty or over-bound entry", field), false, "supply bounded non-empty entries")
		}
	}
	return nil
}

func validateProjectHandoffRecordedPayload(event Event, p projectHandoffRecordedPayload) error {
	if p.WorkID == "" || p.WorkID != event.SubjectID {
		return newFailure(KindInvalidPayload, "validate_event", "project handoff payload does not name its work item", false, "record the handoff on the shared work item")
	}
	if p.HandoffID == "" || len(p.HandoffID) > projectHandoffIDMax {
		return newFailure(KindInvalidPayload, "validate_event", "project handoff payload is missing or over-bounds handoff_id", false, "supply one bounded handoff id")
	}
	if p.ContractVersion <= 0 || p.SourceProjectID == "" || p.TargetProjectID == "" || p.SourceProjectID == p.TargetProjectID || p.SourceSessionRef == "" || p.BoundedJob == "" || len(p.BoundedJob) > projectHandoffJobMax || p.NextAction == "" || len(p.NextAction) > projectHandoffActionMax {
		return newFailure(KindInvalidPayload, "validate_event", "project handoff payload is missing required fields or out of bounds", false, "supply contract version, distinct source and target Projects, source session, bounded job, and next action")
	}
	if err := validateProjectHandoffStrings("changes", p.Changes); err != nil {
		return err
	}
	if err := validateProjectHandoffStrings("verification", p.Verification); err != nil {
		return err
	}
	if err := validateProjectHandoffStrings("artifact_refs", p.ArtifactRefs); err != nil {
		return err
	}
	return validateProjectHandoffStrings("blockers", p.Blockers)
}

func foldProjectHandoffRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p projectHandoffRecordedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if err := validateProjectHandoffRecordedPayload(event, p); err != nil {
		return err
	}
	encoded := func(values []string) (string, error) {
		raw, err := json.Marshal(values)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	changes, err := encoded(p.Changes)
	if err != nil {
		return err
	}
	verification, err := encoded(p.Verification)
	if err != nil {
		return err
	}
	artifactRefs, err := encoded(p.ArtifactRefs)
	if err != nil {
		return err
	}
	blockers, err := encoded(p.Blockers)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_handoffs
		(handoff_id,work_id,contract_version,source_project_id,target_project_id,source_session_ref,bounded_job,changes,verification,artifact_refs,blockers,next_action,state,recorded_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,'recorded',?)`,
		p.HandoffID, event.SubjectID, p.ContractVersion, p.SourceProjectID, p.TargetProjectID, p.SourceSessionRef, p.BoundedJob, changes, verification, artifactRefs, blockers, p.NextAction, p.RecordedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "project handoff id is already recorded", false, "record the handoff with a new deterministic id")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot record the project handoff projection", true, "retry once the database is writable", err)
	}
	return nil
}

func foldProjectHandoffConsumed(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p projectHandoffConsumedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.HandoffID == "" || p.ContractVersion <= 0 || p.TargetProjectID == "" || p.ConsumedBySessionRef == "" || p.ConsumedAt == "" {
		return newFailure(KindInvalidPayload, "fold_event", "project handoff consumed payload is missing required fields", false, "supply handoff id, contract version, target Project, consuming session, and time")
	}
	var state, recordedTarget string
	var recordedVersion int64
	err := tx.QueryRowContext(ctx, `SELECT state, target_project_id, contract_version FROM project_handoffs WHERE handoff_id=? AND work_id=?`, p.HandoffID, event.SubjectID).Scan(&state, &recordedTarget, &recordedVersion)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "the consumed event names no recorded project handoff", false, "consume a recorded handoff")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the project handoff projection", true, "retry once the database is readable", err)
	}
	if state != ProjectHandoffRecorded || recordedTarget != p.TargetProjectID || recordedVersion != p.ContractVersion {
		return newFailure(KindProjectionConflict, "fold_event", "the project handoff consumption does not match the recorded projection", false, "consume the recorded handoff under its recorded Project and contract")
	}
	_, err = tx.ExecContext(ctx, `UPDATE project_handoffs SET state='consumed', consumed_by_session_ref=?, consumed_at=? WHERE handoff_id=? AND work_id=? AND state='recorded'`, p.ConsumedBySessionRef, p.ConsumedAt, p.HandoffID, event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot bind the project handoff consumption", true, "retry once the database is writable", err)
	}
	return nil
}

// WorkID is validated against the event subject at append and fold time.

// RecordProjectHandoffRequest carries one source session's outgoing handoff.
// The artifact-preservation probe runs against SourceWorktree before the
// mutation transaction opens; the probe is a git/filesystem fact, never a
// caller assertion.
type RecordProjectHandoffRequest struct {
	WorkID           string
	SourceProjectID  string
	TargetProjectID  string
	SourceSessionRef string
	BoundedJob       string
	Changes          []string
	Verification     []string
	ArtifactRefs     []string
	Blockers         []string
	NextAction       string
	SourceWorktree   string
	Now              time.Time
}

// ProjectHandoffResult reports the durable handoff identity. AlreadyRecorded
// marks a same-content replay of a recorded handoff.
type ProjectHandoffResult struct {
	HandoffID       string `json:"handoff_id"`
	AlreadyRecorded bool   `json:"already_recorded"`
}

// artifactRefPattern admits sha256:<64 hex>:<relative path> immutable
// artifact references.
var artifactRefPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}:/.+$`)

// projectHandoffEventOrder is the one definition of "newest handoff" every
// selection read shares: the durable append sequence of the handoff's
// recorded event. Ordering on recorded_at text would let an RFC3339Nano
// rendering sort a whole-second timestamp ahead of a later fractional one
// ("...T00:00:40Z" sorts after "...T00:00:40.000000001Z"), so the boot
// frontier, the admission gate, the consume resolution, the continuity read,
// and the retirement facts all order on the event log's seq instead. Each
// handoff row folds from exactly one recorded event, so the correlate always
// resolves; the scalar reads NULL only for a row no event produced, and NULL
// sorts last under DESC.
const projectHandoffEventOrder = `(SELECT l.seq FROM domain_events l
		WHERE l.kind='work.project_handoff_recorded' AND l.subject_id=project_handoffs.work_id
		AND json_extract(l.payload,'$.handoff_id')=project_handoffs.handoff_id) DESC`

// projectHandoffIdentity derives the deterministic handoff id from the
// recorded content: a replay of the same addressed handoff resolves to the
// same id and no event, while a successor handoff with different content
// records under its own id. The digest alone carries the identity: the
// published tool-surface id bound is 128 characters, and a composite of the
// named identities can exceed it (a 128-character work id alone consumes
// the whole bound), so the id renders one stable prefix plus the content
// digest and always fits every accepted identity length.
func projectHandoffIdentity(req RecordProjectHandoffRequest, contractVersion int64) string {
	identity := struct {
		Work          string   `json:"work"`
		Source        string   `json:"source"`
		Target        string   `json:"target"`
		SourceSession string   `json:"source_session"`
		Contract      int64    `json:"contract"`
		Job           string   `json:"job"`
		Changes       []string `json:"changes"`
		Verification  []string `json:"verification"`
		ArtifactRefs  []string `json:"artifact_refs"`
		Blockers      []string `json:"blockers"`
		NextAction    string   `json:"next_action"`
	}{Work: req.WorkID, Source: req.SourceProjectID, Target: req.TargetProjectID, SourceSession: req.SourceSessionRef, Contract: contractVersion, Job: req.BoundedJob, Changes: req.Changes, Verification: req.Verification, ArtifactRefs: req.ArtifactRefs, Blockers: req.Blockers, NextAction: req.NextAction}
	raw, _ := json.Marshal(identity)
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("project-handoff-%s", hex.EncodeToString(digest[:16]))
}

// VerifyProjectHandoffArtifactPreservation is the exported probe the agent
// mutation planner runs in its own phase: the git/filesystem fact must be
// established BEFORE any transaction opens, never inside one.
func VerifyProjectHandoffArtifactPreservation(ctx context.Context, sourceWorktree string, artifactRefs []string) error {
	return verifyArtifactPreservation(ctx, sourceWorktree, artifactRefs)
}

// RecordProjectHandoffTx is the tx-scoped record the agent mutation effect
// runs inside its own transaction. The caller establishes the artifact-
// preservation probe before its transaction opens: the probe is a
// git/filesystem fact, and dirty or untracked unpreserved changes refuse
// before any write. The record lands on the SAME work item and active
// contract; a non-member target or a self-addressed record refuses, and the
// operation never commits, stashes, hides files, or adds a backup subsystem.
func RecordProjectHandoffTx(ctx context.Context, transaction *Transaction, req RecordProjectHandoffRequest) (ProjectHandoffResult, error) {
	var out ProjectHandoffResult
	tx, err := transactionSQL(transaction, "project_handoff")
	if err != nil {
		return out, err
	}
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	return recordProjectHandoffCore(ctx, tx, req, now)
}

func recordProjectHandoffCore(ctx context.Context, tx *sql.Tx, req RecordProjectHandoffRequest, now time.Time) (ProjectHandoffResult, error) {
	stamp := now.UTC().Format(time.RFC3339Nano)
	var out ProjectHandoffResult
	if req.SourceProjectID == req.TargetProjectID {
		return out, newFailure(KindInvalidOperation, "project_handoff", "a handoff addresses another Project, not its own", false, "name the receiving Project")
	}
	if err := preflightProjectHandoffTx(ctx, tx, req); err != nil {
		return out, err
	}
	// The preservation probe proved a git/filesystem fact about one
	// worktree before this transaction opened; this bind proves the probed
	// path is the shared work's OWN claimed source worktree, so a clean
	// unrelated linked worktree can never stand in for a claimed source
	// that carries unpreserved changes. Read and compare inside the same
	// transaction that records, so a claim moved between the probe and the
	// record refuses here.
	claimed, err := sessionClaimedWorktreeTx(ctx, tx, req.WorkID, req.SourceProjectID)
	if err != nil {
		return out, err
	}
	if claimed == "" {
		return out, newFailure(KindInvalidOperation, "project_handoff", "the source Project holds no claimed worktree on the work, so the handoff cannot bind artifact preservation to the shared work's own source claim", false, "claim the source worktree before recording the handoff")
	}
	if filepath.Clean(claimed) != filepath.Clean(req.SourceWorktree) {
		return out, newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("the recorded source worktree is not the work's claimed source worktree %s", claimed), false, "record from the session linked to the claimed source worktree")
	}
	contractVersion, err := activeWorkflowContractVersionInTx(ctx, tx, req.WorkID)
	if err != nil {
		return out, err
	}
	handoffID := projectHandoffIdentity(req, contractVersion)
	var existingState string
	scanErr := tx.QueryRowContext(ctx, `SELECT state FROM project_handoffs WHERE handoff_id=? AND work_id=?`, handoffID, req.WorkID).Scan(&existingState)
	if scanErr != nil && scanErr != sql.ErrNoRows {
		return out, wrapFailure(KindUnavailable, "project_handoff", "cannot read the recorded handoff id", true, "retry once the database is readable", scanErr)
	}
	payload, err := json.Marshal(projectHandoffRecordedPayload{
		WorkID: req.WorkID, HandoffID: handoffID, ContractVersion: contractVersion, SourceProjectID: req.SourceProjectID, TargetProjectID: req.TargetProjectID,
		SourceSessionRef: req.SourceSessionRef, BoundedJob: req.BoundedJob, Changes: req.Changes, Verification: req.Verification,
		ArtifactRefs: req.ArtifactRefs, Blockers: req.Blockers, NextAction: req.NextAction, RecordedAt: stamp,
	})
	if err != nil {
		return out, err
	}
	if scanErr == nil && existingState == ProjectHandoffConsumed {
		return out, newFailure(KindProjectionConflict, "project_handoff", "the receiving session already consumed the handoff at this ordinal; record the successor handoff from its own state", false, "read the consumed handoff and record the next one")
	}
	if scanErr == nil && existingState == ProjectHandoffRecorded {
		// Replay of a recorded handoff resolves from state with no event.
		out.HandoffID = handoffID
		out.AlreadyRecorded = true
		return out, nil
	}
	if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: handoffID + ":recorded", Kind: "work.project_handoff_recorded", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.SourceSessionRef, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}}, newFoldScope(tx), false); err != nil {
		return out, err
	}
	out.HandoffID = handoffID
	return out, nil
}

// activeWorkflowContractVersionInTx resolves the work's active workflow
// contract version inside the caller's transaction. A work with no active
// contract reports the typed not-found the callers turn into their own
// refusal wording.
func activeWorkflowContractVersionInTx(ctx context.Context, tx *sql.Tx, workID string) (int64, error) {
	return activeWorkflowContractVersion(ctx, tx, workID, "project_handoff")
}

func preflightProjectHandoffTx(ctx context.Context, tx *sql.Tx, req RecordProjectHandoffRequest) error {
	exists, err := workExistsCore(ctx, tx, req.WorkID)
	if err != nil {
		return err
	}
	if !exists {
		return newFailure(KindProjectionNotFound, "project_handoff", "work item is not recorded", false, "reread_entities")
	}
	var members int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM work_projects WHERE work_id=? AND project_id IN (?,?)`, req.WorkID, req.SourceProjectID, req.TargetProjectID).Scan(&members); err != nil {
		return wrapFailure(KindUnavailable, "project_handoff", "cannot read the work's Project memberships", true, "retry once the database is readable", err)
	}
	if members != 2 {
		return newFailure(KindProjectionNotFound, "project_handoff", "both the source and the receiving Project must be members of the shared work", false, "record the membership with set_memberships before the handoff")
	}
	if _, contractErr := activeWorkflowContractVersion(ctx, tx, req.WorkID, "project_handoff"); contractErr == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "project_handoff", "the work holds no active workflow contract to bind the handoff", false, "start the workflow before recording a handoff")
	} else if contractErr != nil {
		return contractErr
	}
	return nil
}

// readFileBounded reads one artifact file with a bounded size, so a
// mis-referenced path cannot pull unbounded bytes into the digest check. The
// read opens through an os.Root on the claimed worktree, so a symlink swapped
// in after the caller's containment check still cannot reach outside it.
func readFileBounded(worktree, relative string) (content []byte, err error) {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); err == nil {
			err = closeErr
		}
	}()
	file, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	content, err = io.ReadAll(io.LimitReader(file, projectHandoffArtifactMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > projectHandoffArtifactMaxBytes {
		return nil, fmt.Errorf("artifact exceeds %d bytes", projectHandoffArtifactMaxBytes)
	}
	return content, nil
}

// verifyArtifactPreservation probes the actual claimed source worktree with
// git before any mutation transaction. A dirty or untracked worktree refuses
// with the unpreserved paths named: closing the session would lose them, and
// the core never auto-commits, stashes, or hides them. Each immutable
// artifact reference is verified as durable committed content, never as a
// hash-only claim: the path must be canonical and resolve inside the claimed
// worktree with no escaping symlink, the recorded digest must equal the
// sha256 of the file's bytes, AND the same bytes must be committed at the
// worktree's HEAD — `git status --porcelain` never lists ignored files, so
// an ignored uncommitted artifact would survive the status probe and vanish
// with the worktree. A stale, fabricated, uncommitted, or ignored reference
// refuses.
func verifyArtifactPreservation(ctx context.Context, sourceWorktree string, artifactRefs []string) error {
	if sourceWorktree == "" {
		return newFailure(KindInvalidOperation, "project_handoff", "artifact preservation requires the actual claimed source worktree", false, "supply the source worktree directory the session claimed")
	}
	if !filepath.IsAbs(sourceWorktree) {
		return newFailure(KindInvalidOperation, "project_handoff", "the source worktree path is not absolute", false, "supply the absolute claimed worktree path")
	}
	status, err := ExecGitRunner{}.Run(ctx, sourceWorktree, "status", "--porcelain")
	if err != nil {
		return wrapFailure(KindUnavailable, "project_handoff", "cannot probe the source worktree with git", true, "retry once the claimed worktree is readable", err)
	}
	dirty := []string{}
	for _, line := range strings.Split(string(status), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(dirty) >= projectHandoffListMax {
			return newFailure(KindInvalidOperation, "project_handoff", "the source worktree carries more unpreserved changes than one handoff can name", false, "commit or remove the changes, then record the handoff")
		}
		dirty = append(dirty, line)
	}
	if len(dirty) > 0 {
		return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("the source worktree carries unpreserved changes: %s", strings.Join(dirty, "; ")), false, "commit the candidate or remove the changes before recording the handoff; the core never commits, stashes, or hides files")
	}
	resolvedTree, resolveErr := filepath.EvalSymlinks(sourceWorktree)
	if resolveErr != nil {
		return newFailure(KindProjectionNotFound, "project_handoff", "the claimed source worktree does not resolve on this machine", false, "probe the claimed source worktree the session verified")
	}
	treePrefix := resolvedTree + string(filepath.Separator)
	for _, ref := range artifactRefs {
		parts := strings.SplitN(ref, ":", 3)
		if len(parts) != 3 {
			return newFailure(KindInvalidOperation, "project_handoff", "an artifact reference does not decode", false, "reference preserved artifacts as sha256:<digest>:<absolute path>")
		}
		claimed, path := parts[1], parts[2]
		// Canonicalize before any file access: a traversal or a symlink that
		// escapes the claimed worktree refuses before its bytes are read.
		if filepath.Clean(path) != path || !filepath.IsAbs(path) {
			return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("artifact reference %s does not name a canonical absolute path", ref), false, "reference artifacts by their canonical absolute path inside the claimed source worktree")
		}
		if !strings.HasPrefix(path, sourceWorktree+string(filepath.Separator)) {
			return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("artifact reference %s names a path outside the claimed source worktree", ref), false, "reference artifacts inside the claimed source worktree")
		}
		resolvedPath, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return newFailure(KindProjectionNotFound, "project_handoff", fmt.Sprintf("artifact reference %s names no readable file under the claimed source worktree", ref), false, "reference committed artifacts that exist in the worktree")
		}
		if !strings.HasPrefix(resolvedPath, treePrefix) {
			return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("artifact reference %s resolves through a symlink outside the claimed source worktree", ref), false, "reference artifacts that live inside the claimed source worktree")
		}
		relative := strings.TrimPrefix(path, sourceWorktree+string(filepath.Separator))
		content, readErr := readFileBounded(sourceWorktree, relative)
		if readErr != nil {
			return newFailure(KindProjectionNotFound, "project_handoff", fmt.Sprintf("artifact reference %s names no readable file under the claimed source worktree", ref), false, "reference committed artifacts that exist in the worktree")
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		if digest != claimed {
			return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("artifact reference %s does not match the worktree content", ref), false, "reference the artifact's actual content digest")
		}
		committed, headErr := ExecGitRunner{}.Run(ctx, sourceWorktree, "show", "HEAD:"+filepath.ToSlash(relative))
		if headErr != nil {
			return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("artifact reference %s is not committed at the source worktree's HEAD; an uncommitted or ignored artifact would not survive session retirement", ref), false, "commit the artifact or reference committed content; the core never commits, stashes, or hides files")
		}
		if fmt.Sprintf("%x", sha256.Sum256(committed)) != claimed {
			return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("artifact reference %s does not match the committed content at the source worktree's HEAD", ref), false, "reference the artifact's committed content digest")
		}
	}
	return nil
}

// ConsumeProjectHandoffRequest names the authenticated receiving session and
// its Project. The core binds the consume to these identities: a caller that
// names another session's identity is the adapter's authenticated call
// context, and a wrong-Project consume refuses here.
type ConsumeProjectHandoffRequest struct {
	WorkID             string
	HandoffID          string
	ConsumerProjectID  string
	ConsumerSessionRef string
	Now                time.Time
}

// ProjectHandoffConsumptionResult reports the consume. AlreadyConsumed marks
// a replay by the same receiving session while the bind stands.
type ProjectHandoffConsumptionResult struct {
	HandoffID       string `json:"handoff_id"`
	AlreadyConsumed bool   `json:"already_consumed"`
}

// ConsumeProjectHandoffTx is the tx-scoped consume the agent mutation effect
// runs inside its own transaction. The consume binds a recorded handoff to
// the work item and its addressed receiving Project under the active
// contract (CD-0182 D5 amendment): any session of the receiving Project may
// bind it, the standing bind resolves for every later session of that
// Project, and a second coordinator session needs no fresh handoff of its
// own. The verified receiver placement gates the bind and the replay alike:
// only a session whose verified landing records it in the receiving Project
// resolves the shared bind. A missing, wrong-target, or stale handoff
// refuses with no event, so managed-execution admission can fail closed on
// the recorded state. A replay by any placed session of the receiving
// Project resolves as AlreadyConsumed with no event.
func ConsumeProjectHandoffTx(ctx context.Context, transaction *Transaction, req ConsumeProjectHandoffRequest) (ProjectHandoffConsumptionResult, error) {
	var out ProjectHandoffConsumptionResult
	tx, err := transactionSQL(transaction, "project_handoff")
	if err != nil {
		return out, err
	}
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	return consumeProjectHandoffCore(ctx, tx, req, now)
}

func consumeProjectHandoffCore(ctx context.Context, tx *sql.Tx, req ConsumeProjectHandoffRequest, now time.Time) (ProjectHandoffConsumptionResult, error) {
	var out ProjectHandoffConsumptionResult
	stamp := now.UTC().Format(time.RFC3339Nano)
	// An empty handoff id is the addressed-resolution form: the core resolves
	// the newest unconsumed handoff addressed to the consumer's own Project
	// and binds that one. The consuming session never names its identity or
	// target; resolution runs from the recorded state inside this
	// transaction.
	handoff, err := func() (ProjectHandoff, error) {
		if req.HandoffID != "" {
			return readProjectHandoffTx(ctx, tx, req.WorkID, req.HandoffID)
		}
		// The addressed resolution reads the newest handoff addressed to the
		// consumer's Project in any state, so a standing bind replays as
		// AlreadyConsumed after placement and contract validation. The
		// recorded-event ordering matches the admission gate, the
		// boot frontier, the continuity read, and the retirement facts, so
		// every read of "the current handoff" agrees on one durable order.
		var id string
		idErr := tx.QueryRowContext(ctx, `SELECT handoff_id FROM project_handoffs WHERE work_id=? AND target_project_id=? ORDER BY `+projectHandoffEventOrder+` LIMIT 1`, req.WorkID, req.ConsumerProjectID).Scan(&id)
		if idErr == sql.ErrNoRows {
			return ProjectHandoff{}, newFailure(KindProjectionNotFound, "project_handoff", "no recorded project handoff addresses this Project", false, "wait for the source session to record the addressed handoff")
		}
		if idErr != nil {
			return ProjectHandoff{}, wrapFailure(KindUnavailable, "project_handoff", "cannot read the addressed handoff", true, "retry once the database is readable", idErr)
		}
		req.HandoffID = id
		return readProjectHandoffTx(ctx, tx, req.WorkID, id)
	}()
	if err != nil {
		return out, err
	}
	if handoff.TargetProjectID != req.ConsumerProjectID {
		return out, newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("the handoff addresses Project %s, not %s", handoff.TargetProjectID, req.ConsumerProjectID), false, "consume the handoff addressed to the session's Project")
	}
	// The verified receiver placement gates the bind and the shared-bind
	// replay alike (CD-0182 D5 amendment): the consuming session must hold
	// the occupancy row a recorded claim landing established on this work
	// for the addressed Project (CD-0178 D3), before any consume — a fresh
	// bind or a replay of the standing one — resolves. Claim admission
	// records an occupancy row without a landing, so occupancy alone is not
	// placement, and no consume can precede the verified landing.
	placedProject, placeErr := verifiedSessionPlacementTx(ctx, tx, req.WorkID, req.ConsumerSessionRef)
	if placeErr != nil {
		return out, placeErr
	}
	if placedProject == "" {
		return out, newFailure(KindInvalidOperation, "project_handoff", "the receiving session holds no verified placement on this work for the addressed Project; the consume binds only after a verified claim landing records this session's occupancy", false, "replay work_start so the verified landing records this session's placement, then consume")
	}
	if placedProject != req.ConsumerProjectID {
		return out, newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("the receiving session's verified placement names Project %s, not %s", placedProject, req.ConsumerProjectID), false, "consume from the session's own landed Project context")
	}
	if handoff.State == ProjectHandoffConsumed {
		// The bind names the work-and-Project pair (CD-0182 D5
		// amendment): every placed session of the receiving Project
		// resolves the standing bind — the session that recorded the
		// consume and a later landed session alike — so a second
		// coordinator session never needs a fresh addressed handoff of
		// its own. The replay is still a bind check, not a bypass: a
		// bind authorizes execution only under the contract it was made
		// under, and after a contract replacement the stale bind refuses
		// closed for every session of the Project alike.
		version, contractErr := activeWorkflowContractVersion(ctx, tx, req.WorkID, "project_handoff")
		if contractErr == sql.ErrNoRows {
			return out, newFailure(KindInvalidOperation, "project_handoff", "the work holds no active workflow contract, so the recorded bind cannot authorize execution", false, "start the workflow, then have the source session record a fresh addressed handoff")
		}
		if contractErr != nil {
			return out, contractErr
		}
		if version != handoff.ContractVersion {
			return out, newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("the handoff was recorded under contract version %d, but the active contract is version %d", handoff.ContractVersion, version), false, "have the source session record a fresh addressed handoff under the active contract, then consume it")
		}
		out.HandoffID = req.HandoffID
		out.AlreadyConsumed = true
		return out, nil
	}
	version, contractErr := activeWorkflowContractVersion(ctx, tx, req.WorkID, "project_handoff")
	if contractErr == sql.ErrNoRows {
		return out, newFailure(KindProjectionNotFound, "project_handoff", "the work holds no active workflow contract", false, "start the workflow before consuming a handoff")
	}
	if contractErr != nil {
		return out, contractErr
	}
	if version != handoff.ContractVersion {
		return out, newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("the handoff was recorded under contract version %d, but the active contract is version %d", handoff.ContractVersion, version), false, "have the source session record a fresh handoff under the active contract")
	}
	payload, err := json.Marshal(projectHandoffConsumedPayload{HandoffID: req.HandoffID, ContractVersion: handoff.ContractVersion, TargetProjectID: req.ConsumerProjectID, ConsumedBySessionRef: req.ConsumerSessionRef, ConsumedAt: stamp})
	if err != nil {
		return out, err
	}
	if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: req.HandoffID + ":consumed", Kind: "work.project_handoff_consumed", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.ConsumerSessionRef, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}}, newFoldScope(tx), false); err != nil {
		return out, err
	}
	out.HandoffID = req.HandoffID
	return out, nil
}

func readProjectHandoffTx(ctx context.Context, tx *sql.Tx, workID, handoffID string) (ProjectHandoff, error) {
	var h ProjectHandoff
	var changes, verification, artifactRefs, blockers string
	err := tx.QueryRowContext(ctx, `SELECT handoff_id,work_id,contract_version,source_project_id,target_project_id,source_session_ref,bounded_job,changes,verification,artifact_refs,blockers,next_action,state,consumed_by_session_ref,consumed_at,recorded_at
		FROM project_handoffs WHERE work_id=? AND handoff_id=?`, workID, handoffID).Scan(&h.HandoffID, &h.WorkID, &h.ContractVersion, &h.SourceProjectID, &h.TargetProjectID, &h.SourceSessionRef, &h.BoundedJob, &changes, &verification, &artifactRefs, &blockers, &h.NextAction, &h.State, &h.ConsumedBySessionRef, &h.ConsumedAt, &h.RecordedAt)
	if err == sql.ErrNoRows {
		return h, newFailure(KindProjectionNotFound, "project_handoff", "no recorded project handoff carries this id on the work", false, "read the work's recorded handoffs")
	}
	if err != nil {
		return h, wrapFailure(KindUnavailable, "project_handoff", "cannot read the project handoff", true, "retry once the database is readable", err)
	}
	decode := func(raw string, target *[]string) error {
		if json.Unmarshal([]byte(raw), target) != nil {
			return newFailure(KindInvariantViolation, "project_handoff", "the handoff projection contains malformed arrays", false, "rebuild projections from the event log")
		}
		return nil
	}
	if err := decode(changes, &h.Changes); err != nil {
		return h, err
	}
	if err := decode(verification, &h.Verification); err != nil {
		return h, err
	}
	if err := decode(artifactRefs, &h.ArtifactRefs); err != nil {
		return h, err
	}
	if err := decode(blockers, &h.Blockers); err != nil {
		return h, err
	}
	return h, nil
}

// verifiedSessionPlacementTx resolves the Project of the session's verified
// placement on one work: an occupancy row a recorded claim landing
// established (CD-0178 D3) for the session's current claim generation, and
// that still stands. Claim admission records an occupancy row without a
// landing, and a vacate landing releases the row, so neither state alone
// proves the session was placed and has not left. The landing must name the
// claim the occupancy row stands on: a landing recorded for a superseded
// claim — after a reclaim, or a vacate plus re-claim — proves nothing about
// the current claim. It returns an empty Project when no verified placement
// stands. It runs inside the caller's transaction.
func verifiedSessionPlacementTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string) (string, error) {
	var projectID string
	err := tx.QueryRowContext(ctx, `SELECT c.project_id
		FROM worktree_occupancy o
		JOIN worktree_entries e ON e.set_id || ':' || e.project_id || ':' || e.claim_op_id = o.worktree_id AND e.state='active'
		JOIN worktree_claims c ON c.op_id=e.claim_op_id
		WHERE o.session_ref=? AND c.work_id=?
		  AND EXISTS (SELECT 1 FROM domain_events l
		              WHERE l.kind='work.session_claim_landed' AND l.subject_id=c.work_id
		                AND json_extract(l.payload,'$.session_ref')=o.session_ref
		                AND json_extract(l.payload,'$.project_id')=c.project_id
		                AND json_extract(l.payload,'$.claim_op_id')=e.claim_op_id)`,
		sessionRef, workID).Scan(&projectID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "project_handoff", "cannot read the session's verified placement", true, "retry once the database is readable", err)
	}
	return projectID, nil
}

// RefuseUnconsumedProjectHandoffTx is the core managed-execution admission
// gate. It reads the current addressed frontier: the newest handoff another
// session recorded for the acting session's Project. A frontier that stands
// unconsumed refuses managed execution until a session of the receiving
// Project consumes it — and a frontier recorded under a superseded contract
// cannot be consumed, so the source session records a fresh addressed
// handoff whose recording supersedes the stale one. A consumed frontier
// admits every session with a verified placement in the receiving Project,
// and only under the contract the bind was made under (CD-0182 D5
// amendment): the bind names the work-and-Project pair, not one receiving
// session, so a second landed coordinator session dispatches without a
// fresh handoff or a new session, and after a contract replacement the
// stale bind refuses closed equally for the consuming session and a later
// session of the Project. A session whose Project placement cannot resolve
// on a handoff-bearing work refuses: the gate cannot bind an addressed
// handoff to a Project it cannot prove. A work with no handoffs is
// unaffected. It runs inside the caller's transaction so the read observes
// the caller's own uncommitted events.
func RefuseUnconsumedProjectHandoffTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string) error {
	// Ordinary handoff-free work stays admitted whatever the caller's
	// identities or placement.
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM project_handoffs WHERE work_id=?`, workID).Scan(&total); err != nil {
		return wrapFailure(KindUnavailable, "project_handoff", "cannot read the work's handoff records", true, "retry once the database is readable", err)
	}
	if total == 0 {
		return nil
	}
	// The acting session's Project resolves from the core's own placement
	// evidence: the occupancy row a recorded claim landing established
	// (CD-0178 D3) joined to the claimed worktree. A caller-supplied Project
	// never decides admission, and claim occupancy without its landing is
	// not placement. On a handoff-bearing work an unresolvable placement
	// refuses: the session could be the addressed receiver skipping its
	// consume, so admission fails closed until a verified landing records
	// its occupancy.
	projectID, placeErr := verifiedSessionPlacementTx(ctx, tx, workID, sessionRef)
	if placeErr != nil {
		return placeErr
	}
	if projectID == "" {
		return newFailure(KindInvalidOperation, "project_handoff", "the work carries Project handoffs, but this session holds no verified placement on it; managed execution refuses until a verified landing records this session's occupancy", false, "replay work_start so the verified landing records this session's placement")
	}
	var frontierID, frontierSource string
	var frontierVersion int64
	var frontierState string
	frontierErr := tx.QueryRowContext(ctx, `SELECT handoff_id,source_session_ref,contract_version,state FROM project_handoffs
		WHERE work_id=? AND target_project_id=? AND source_session_ref<>?
		ORDER BY `+projectHandoffEventOrder+` LIMIT 1`, workID, projectID, sessionRef).Scan(&frontierID, &frontierSource, &frontierVersion, &frontierState)
	if frontierErr == sql.ErrNoRows {
		// No handoff from another session addresses this Project: the
		// session is not the addressed receiver of anything.
		return nil
	}
	if frontierErr != nil {
		return wrapFailure(KindUnavailable, "project_handoff", "cannot read the work's addressed handoff frontier", true, "retry once the database is readable", frontierErr)
	}
	if frontierState != ProjectHandoffRecorded {
		// The frontier's bind names the work-and-Project pair (CD-0182 D5
		// amendment): consumed by ANY session of this Project, it admits
		// every session holding a verified placement in it — the acting
		// session's placement resolved above — so no remedy prescribes a
		// fresh handoff for a second session or a new session. A bind
		// still authorizes managed execution only under the contract it
		// was made under, so a contract replacement refuses the stale bind
		// closed for the consuming session and a later session alike; the
		// source session's fresh addressed handoff under the active
		// contract is the route back to admission.
		version, contractErr := activeWorkflowContractVersion(ctx, tx, workID, "project_handoff")
		if contractErr != nil && contractErr != sql.ErrNoRows {
			return contractErr
		}
		if contractErr == sql.ErrNoRows {
			return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("handoff %s recorded by %s was consumed under contract version %d, but the work holds no active workflow contract; the stale bind no longer authorizes managed execution", frontierID, frontierSource, frontierVersion), false, "start the workflow, then have the source session record a fresh addressed handoff")
		}
		if version != frontierVersion {
			return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("handoff %s recorded by %s was consumed under contract version %d, but the active contract is version %d; the stale bind no longer authorizes managed execution, so the source session records a fresh addressed handoff that supersedes it", frontierID, frontierSource, frontierVersion, version), false, "have the source session record a fresh addressed handoff under the active contract, then consume it")
		}
		return nil
	}
	version, contractErr := activeWorkflowContractVersion(ctx, tx, workID, "project_handoff")
	if contractErr != nil && contractErr != sql.ErrNoRows {
		return contractErr
	}
	if contractErr == nil && version != frontierVersion {
		return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("handoff %s recorded by %s stands unconsumed under contract version %d, but the active contract is version %d; a stale handoff cannot be consumed, so the source session must record a fresh addressed handoff that supersedes it", frontierID, frontierSource, frontierVersion, version), false, "have the source session record a fresh addressed handoff under the active contract, then consume it")
	}
	return newFailure(KindInvalidOperation, "project_handoff", fmt.Sprintf("handoff %s recorded by %s stands unconsumed; managed execution refuses until a verified session of the receiving Project consumes the addressed handoff", frontierID, frontierSource), false, "consume the addressed Project handoff before managed execution")
}

// ProjectSessionRetirement is the derived, read-only readiness result. State
// is retirementReady or retirementPending; retirementReady is emitted only
// from verified facts, never from an opener result, a vacate-request commit,
// or global work completion.
type ProjectSessionRetirement struct {
	WorkID             string   `json:"work_id"`
	ProjectID          string   `json:"project_id"`
	SessionRef         string   `json:"session_ref"`
	State              string   `json:"state"`
	RecordedHandoff    bool     `json:"recorded_handoff"`
	ArtifactsPreserved bool     `json:"artifacts_preserved"`
	WorkersStopped     bool     `json:"workers_stopped"`
	VacateLanded       bool     `json:"vacate_landed"`
	Blockers           []string `json:"blockers"`
}

const (
	RetirementReady   = "ready_to_close_or_replace"
	RetirementPending = "pending"
)

// EvaluateProjectSessionRetirement derives READY_TO_CLOSE_OR_REPLACE for one
// Project-selected session from four verified facts: the session recorded an
// addressed handoff on the work, its changed artifacts are preserved in the
// claimed source worktree, its own open execution (dispatch-attributed
// worker attempts and nonterminal workflow actions) stopped, and its
// verified vacate landing released its occupancy (CD-0190). Unknown worker
// attribution and liveness block readiness; another session's positively
// identified worker is not this session's worker. The evaluation writes no
// event, completes no work, cancels nothing, removes no worktree, and
// terminates no process.
//
// The git/filesystem preservation probe must run outside any SQL
// transaction, so the derivation reads in two snapshots: the phase-one
// handoff facts, then the probe, then the final transaction. The final
// transaction revalidates the phase-one snapshot against the committed
// state — the active contract version, the selected handoff identity and
// frontier, the preserved artifact references, and the claimed source
// worktree — because the probe proved a fact about THAT selection and THAT
// claim only. A contract replacement, a newer handoff, or a source-claim
// change between the phases leaves the snapshot obsolete, and readiness
// fails closed on the drift instead of emitting stale facts.
func EvaluateProjectSessionRetirement(ctx context.Context, s *Store, workID, projectID, sessionRef string) (ProjectSessionRetirement, error) {
	out := ProjectSessionRetirement{WorkID: workID, ProjectID: projectID, SessionRef: sessionRef, State: RetirementPending, Blockers: []string{}}
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "project_retirement", "store is not open", false, "open the authority database")
	}
	if workID == "" || projectID == "" || sessionRef == "" {
		return out, newFailure(KindInvalidOperation, "project_retirement", "the retirement read requires the work, Project, and session identities", false, "read the retirement state with the session's authenticated identities")
	}
	// Phase one: the read transaction gathers the recorded facts, including
	// the claimed source worktree path the preservation probe needs.
	var handoffFacts projectRetirementHandoffFacts
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return readRetirementHandoffFactsTx(ctx, transaction, workID, projectID, sessionRef, &handoffFacts)
	})
	if err != nil {
		return out, err
	}
	out.RecordedHandoff = handoffFacts.recorded
	if !out.RecordedHandoff {
		if handoffFacts.contractBlocker != "" {
			out.Blockers = append(out.Blockers, handoffFacts.contractBlocker)
		} else {
			out.Blockers = append(out.Blockers, "record the addressed Project handoff before retirement")
		}
	}
	artifactRefs := handoffFacts.artifactRefs
	sourceWorktree := handoffFacts.sourceWorktree
	// Phase two: git/filesystem probes run OUTSIDE the SQL transaction.
	if sourceWorktree != "" {
		if probeErr := verifyArtifactPreservation(ctx, sourceWorktree, artifactRefs); probeErr != nil {
			out.Blockers = append(out.Blockers, preservationBlocker(probeErr))
		} else {
			out.ArtifactsPreserved = true
		}
	} else {
		out.Blockers = append(out.Blockers, "the session holds no active claimed worktree to verify artifact preservation against")
	}
	if s.retireProbeInterleave != nil {
		s.retireProbeInterleave()
	}
	// Phase three: the remaining facts are projection reads, and the same
	// final transaction revalidates the phase-one snapshot the probe proved.
	var execution projectRetirementExecutionFacts
	var recheck projectRetirementHandoffFacts
	err = s.Transact(ctx, func(transaction *Transaction) error {
		return readRetirementFinalFactsTx(ctx, transaction, workID, projectID, sessionRef, &execution, &recheck)
	})
	if err != nil {
		return out, err
	}
	if drift := retirementHandoffSnapshotDrift(handoffFacts, recheck); drift != "" {
		out.Blockers = append(out.Blockers, drift+"; re-run the retirement read")
	}
	out.WorkersStopped = execution.workersStopped
	out.VacateLanded = execution.vacateLanded
	if execution.blocker != "" {
		out.Blockers = append(out.Blockers, execution.blocker)
	}
	if !out.WorkersStopped {
		out.Blockers = append(out.Blockers, "the session's own open worker attempts or nonterminal workflow actions must stop before readiness")
	}
	if len(out.Blockers) == 0 && out.RecordedHandoff && out.ArtifactsPreserved && out.WorkersStopped && out.VacateLanded {
		out.State = RetirementReady
	}
	return out, nil
}

// retirementHandoffSnapshotDrift compares the phase-one handoff snapshot the
// preservation probe proved against the current committed state the final
// transaction read, and names the identity that moved. An equal snapshot
// names no drift. Every compared field is one the probe's validity binds to:
// the active contract version and the selected handoff identity decide
// whether the evaluated record still stands eligible, the artifact
// references are the digests the probe verified, and the claimed source
// worktree is the tree the probe ran in.
func retirementHandoffSnapshotDrift(snapshot, current projectRetirementHandoffFacts) string {
	if snapshot.contractVersion != current.contractVersion {
		return fmt.Sprintf("the work's active workflow contract moved from version %d to %d while the retirement was evaluated", snapshot.contractVersion, current.contractVersion)
	}
	if snapshot.handoffID != current.handoffID {
		return "a newer addressed handoff superseded the evaluated one while the retirement was evaluated"
	}
	if snapshot.recorded != current.recorded {
		return "the addressed handoff record changed while the retirement was evaluated"
	}
	if !slices.Equal(snapshot.artifactRefs, current.artifactRefs) {
		return "the evaluated handoff's preserved artifact references changed while the retirement was evaluated"
	}
	if snapshot.sourceWorktree != current.sourceWorktree {
		return "the claimed source worktree changed while the retirement was evaluated"
	}
	return ""
}

// preservationBlocker reduces a typed preservation refusal to the blocker
// text the readiness result carries; the refusal's recovery stays in the
// recorded handoff flow, and readiness never names a path the session can
// act on differently.
func preservationBlocker(err error) string {
	var failure *Failure
	if failureAs(err, &failure) {
		return "artifact preservation is unverified: " + failure.Detail
	}
	return "artifact preservation is unverified"
}

// projectRetirementHandoffFacts carries the phase-one read: whether the
// session recorded an addressed handoff on the work under the ACTIVE
// contract, the active contract version the read resolved, the newest
// eligible record's handoff id and immutable artifact references, the
// claimed source worktree path the preservation probe needs, and the
// stale-contract blocker text when the session's records all predate the
// active contract. Every field except the blocker is snapshot identity: the
// final transaction revalidates them, because the preservation probe proved
// a fact about exactly this selection and this claimed source.
type projectRetirementHandoffFacts struct {
	recorded        bool
	contractVersion int64
	handoffID       string
	artifactRefs    []string
	sourceWorktree  string
	contractBlocker string
}

// readRetirementHandoffFactsTx is the tx-taking wrapper the transaction
// scope rule owns the phase-one read through. Retirement eligibility binds
// to the work's active contract: a handoff recorded under a superseded
// contract is one the receiving session can never consume, so readiness
// never derives from it. After a contract replacement the source session
// records a fresh addressed handoff under the active contract; until then
// the fact reads as unrecorded with the stale contract named, and
// readiness fails closed.
func readRetirementHandoffFactsTx(ctx context.Context, transaction *Transaction, workID, projectID, sessionRef string, facts *projectRetirementHandoffFacts) error {
	tx, err := transactionSQL(transaction, "project_retirement")
	if err != nil {
		return err
	}
	version, contractErr := activeWorkflowContractVersion(ctx, tx, workID, "project_handoff")
	switch {
	case contractErr == sql.ErrNoRows:
		facts.contractVersion = 0
	case contractErr != nil:
		return contractErr
	default:
		facts.contractVersion = version
	}
	var recorded int
	if scanErr := tx.QueryRowContext(ctx, `SELECT count(*) FROM project_handoffs WHERE work_id=? AND source_project_id=? AND source_session_ref=?`, workID, projectID, sessionRef).Scan(&recorded); scanErr != nil {
		return wrapFailure(KindUnavailable, "project_retirement", "cannot read the recorded handoffs", true, "retry once the database is readable", scanErr)
	}
	if recorded > 0 {
		if facts.contractVersion == 0 {
			facts.contractBlocker = "the work holds no active workflow contract, so the recorded handoff cannot stand as the addressed handoff retirement derives from"
		} else {
			var handoffID, refs string
			scanErr := tx.QueryRowContext(ctx, `SELECT handoff_id,artifact_refs FROM project_handoffs WHERE work_id=? AND source_project_id=? AND source_session_ref=? AND contract_version=? ORDER BY `+projectHandoffEventOrder+` LIMIT 1`, workID, projectID, sessionRef, facts.contractVersion).Scan(&handoffID, &refs)
			switch {
			case scanErr == sql.ErrNoRows:
				var staleVersion int64
				if staleErr := tx.QueryRowContext(ctx, `SELECT contract_version FROM project_handoffs WHERE work_id=? AND source_project_id=? AND source_session_ref=? ORDER BY `+projectHandoffEventOrder+` LIMIT 1`, workID, projectID, sessionRef).Scan(&staleVersion); staleErr != nil {
					return wrapFailure(KindUnavailable, "project_retirement", "cannot read the recorded handoffs", true, "retry once the database is readable", staleErr)
				}
				facts.contractBlocker = fmt.Sprintf("the newest recorded handoff stands under contract version %d, but the active contract is version %d; a handoff the receiver can no longer consume cannot prove retirement readiness, so record a fresh addressed handoff under the active contract", staleVersion, facts.contractVersion)
			case scanErr != nil:
				return wrapFailure(KindUnavailable, "project_retirement", "cannot read the handoff's artifact references", true, "retry once the database is readable", scanErr)
			default:
				facts.recorded = true
				facts.handoffID = handoffID
				if json.Unmarshal([]byte(refs), &facts.artifactRefs) != nil {
					return newFailure(KindInvariantViolation, "project_retirement", "the handoff projection contains malformed artifact references", false, "rebuild projections from the event log")
				}
			}
		}
	}
	facts.sourceWorktree, err = sessionClaimedWorktreeTx(ctx, tx, workID, projectID)
	return err
}

// readRetirementFinalFactsTx is the tx-taking wrapper the transaction scope
// rule owns the phase-three read through: the execution facts and the
// phase-one snapshot revalidation read in one transaction, so the drift
// comparison observes the same committed state the stopped-worker and
// landing facts derive from.
func readRetirementFinalFactsTx(ctx context.Context, transaction *Transaction, workID, projectID, sessionRef string, execution *projectRetirementExecutionFacts, recheck *projectRetirementHandoffFacts) error {
	if err := readRetirementExecutionFactsTx(ctx, transaction, workID, sessionRef, execution); err != nil {
		return err
	}
	return readRetirementHandoffFactsTx(ctx, transaction, workID, projectID, sessionRef, recheck)
}

// projectRetirementExecutionFacts carries the phase-three read: whether the
// session's own execution stopped and where its vacate landing stands.
type projectRetirementExecutionFacts struct {
	workersStopped bool
	vacateLanded   bool
	blocker        string
}

// readRetirementExecutionFactsTx is the tx-taking wrapper the transaction
// scope rule owns the phase-three read through.
func readRetirementExecutionFactsTx(ctx context.Context, transaction *Transaction, workID, sessionRef string, facts *projectRetirementExecutionFacts) error {
	tx, err := transactionSQL(transaction, "project_retirement")
	if err != nil {
		return err
	}
	if facts.workersStopped, err = sessionOwnedExecutionStoppedTx(ctx, tx, workID, sessionRef); err != nil {
		return err
	}
	facts.vacateLanded, facts.blocker, err = sessionVacateLandingStateTx(ctx, tx, workID, sessionRef)
	return err
}

// sessionClaimedWorktreeTx resolves the active claimed worktree path of one
// work and Project, or an empty string when none is active. It runs inside
// the caller's transaction.
func sessionClaimedWorktreeTx(ctx context.Context, tx *sql.Tx, workID, projectID string) (string, error) {
	var path string
	err := tx.QueryRowContext(ctx, `SELECT e.path FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id
		WHERE c.work_id=? AND c.project_id=? AND e.state='active' AND c.state IN ('pending','verified') ORDER BY e.path LIMIT 1`, workID, projectID).Scan(&path)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "project_retirement", "cannot read the claimed worktree", true, "retry once the database is readable", err)
	}
	return path, nil
}

// ReadSessionClaimedWorktree resolves the active claimed worktree path of
// one work and Project outside any mutation transaction: the handoff record
// planner reads it before the git/filesystem probe opens, and an empty path
// means the Project holds no claim the probe could bind to. The record
// transaction re-checks the same binding tx-scoped, so a claim that moves
// between this read and the record refuses there.
func ReadSessionClaimedWorktree(ctx context.Context, s *Store, workID, projectID string) (string, error) {
	var path string
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return readSessionClaimedWorktreeTx(ctx, transaction, workID, projectID, &path)
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

func readSessionClaimedWorktreeTx(ctx context.Context, transaction *Transaction, workID, projectID string, out *string) error {
	tx, err := transactionSQL(transaction, "project_handoff")
	if err != nil {
		return err
	}
	var readErr error
	*out, readErr = sessionClaimedWorktreeTx(ctx, tx, workID, projectID)
	return readErr
}

// sessionOwnedExecutionStoppedTx derives whether the session's own open,
// in-flight, or refused execution stopped. A nonterminal attempt is one the
// dispatch authorization bound in flight (lifecycle_state 'in_flight') or
// whose worker evidence recorded (lifecycle_state 'dispatched'); 'completed'
// and 'failed' are terminal. Ownership is derived from the dispatch
// authorization chain, never from name equality: the dispatch_worker
// completion binds the attempt to the authorizing workflow actor, and the
// workflow_actors record binds that actor to its authenticated session. The
// worker.dispatched event's Actor column carries the authenticated
// principal/client actor the evidence boundary stamps, and workflow actor
// refs are hashes, so neither is ever compared with the session reference.
// An attempt with no dispatch authorization, an authorization that names no
// actor, or an actor with no recorded session blocks readiness as unknown
// attribution, and a positively identified attempt of ANOTHER session never
// blocks this session's readiness. Nonterminal workflow actions the
// session's actors started (a started action with no later completed or
// failed event for the same step and attempt epoch) also block. It runs
// inside the caller's transaction.
func sessionOwnedExecutionStoppedTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string) (bool, error) {
	return workExecutionStoppedTx(ctx, tx, workID, sessionRef)
}

// An empty session selects all execution on the work. Session retirement uses
// the same derivation with its ownership filter; outside repair holds all of it.
func workExecutionStoppedTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT attempt_id FROM worker_attempts WHERE work_id=? AND lifecycle_state IN ('in_flight','dispatched')`, workID)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "project_retirement", "cannot read the work's open worker attempts", true, "retry once the database is readable", err)
	}
	attempts := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, wrapFailure(KindUnavailable, "project_retirement", "cannot decode the open worker attempts", true, "retry once the database is readable", err)
		}
		attempts = append(attempts, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, wrapFailure(KindUnavailable, "project_retirement", "cannot finish the open worker attempts read", true, "retry once the database is readable", err)
	}
	rows.Close()
	if sessionRef == "" && len(attempts) != 0 {
		return false, nil
	}
	for _, attemptID := range attempts {
		// The dispatch authorization WorkerAttemptID/ActorRef pair is the
		// only core ownership evidence: read the authorizing actor from the
		// dispatch_worker completion, then resolve its authenticated session
		// through workflow_actors.
		var actorRef string
		scanErr := tx.QueryRowContext(ctx, `SELECT json_extract(payload,'$.actor_ref') FROM domain_events
			WHERE kind='workflow.action_completed' AND subject_id=?
			AND json_extract(payload,'$.action_id')='dispatch_worker'
			AND json_extract(payload,'$.worker_attempt_id')=?
			ORDER BY seq DESC LIMIT 1`, workID, attemptID).Scan(&actorRef)
		if scanErr == sql.ErrNoRows {
			return false, nil
		}
		if scanErr != nil {
			return false, wrapFailure(KindUnavailable, "project_retirement", "cannot read the attempt's dispatch attribution", true, "retry once the database is readable", scanErr)
		}
		if actorRef == "" {
			return false, nil
		}
		var ownerSession string
		actorErr := tx.QueryRowContext(ctx, `SELECT session_ref FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&ownerSession)
		if actorErr == sql.ErrNoRows {
			return false, nil
		}
		if actorErr != nil {
			return false, wrapFailure(KindUnavailable, "project_retirement", "cannot resolve the dispatch actor's authenticated session", true, "retry once the database is readable", actorErr)
		}
		if ownerSession == sessionRef {
			return false, nil
		}
	}
	// The session's own nonterminal workflow actions. The started action's
	// actor_ref is a workflow actor hash, so the session binds through the
	// workflow_actors record, and a started action whose step and attempt
	// epoch has no later completed or failed event is still open, whoever
	// else acted on the work since.
	var open int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events f
		LEFT JOIN workflow_actors a ON a.actor_ref=json_extract(f.payload,'$.actor_ref')
		WHERE f.kind='workflow.action_started' AND f.subject_id=? AND (?='' OR a.session_ref=?)
		  AND NOT EXISTS (
		    SELECT 1 FROM domain_events c
		    WHERE c.subject_id=f.subject_id AND c.kind IN ('workflow.action_completed','workflow.action_failed')
		      AND c.seq>f.seq
		      AND json_extract(c.payload,'$.step_id')=json_extract(f.payload,'$.step_id')
		      AND json_extract(c.payload,'$.attempt_epoch')=json_extract(f.payload,'$.attempt_epoch')
		  )`, workID, sessionRef, sessionRef).Scan(&open)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "project_retirement", "cannot read the session's nonterminal workflow actions", true, "retry once the database is readable", err)
	}
	return open == 0, nil
}

// sessionVacateLandingStateTx derives the verified vacate landing fact for
// one work and session: landed true only when the latest committed
// relocation request stands completed by a recorded landing (CD-0190 D2)
// AND the session holds no occupancy rows on this work now. A recorded
// landing released the rows its request's claim held, so rows that stand
// after it belong to a later claim, and the old landing proves nothing about
// this retirement. A pending request, a version 1 release, no request at
// all, or renewed occupancy reports the blocker text instead. It runs inside
// the caller's transaction.
func sessionVacateLandingStateTx(ctx context.Context, tx *sql.Tx, workID, sessionRef string) (bool, string, error) {
	_, seq, version, err := latestSessionVacateRequestTx(ctx, tx, workID, sessionRef)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindProjectionNotFound {
			return false, "session_vacate has not recorded a relocation request for this session on this work", nil
		}
		return false, "", err
	}
	if version != 2 {
		return false, "the recorded session vacate predates the landing-verified release and cannot prove this session's landing", nil
	}
	landed, err := sessionVacateLandedAfterTx(ctx, tx, workID, sessionRef, seq)
	if err != nil {
		return false, "", err
	}
	if !landed {
		return false, "the committed session vacate still waits for its verified landing", nil
	}
	var occupied int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_occupancy o
		JOIN worktree_entries e ON e.set_id || ':' || e.project_id || ':' || e.claim_op_id = o.worktree_id AND e.state='active'
		JOIN worktree_claims c ON c.op_id=e.claim_op_id
		WHERE o.session_ref=? AND c.work_id=?`, sessionRef, workID).Scan(&occupied); err != nil {
		return false, "", wrapFailure(KindUnavailable, "project_retirement", "cannot read the session's current occupancy", true, "retry once the database is readable", err)
	}
	if occupied != 0 {
		return false, "the session holds occupancy rows on this work after its recorded landing; the rows belong to a later claim, whose own verified landing or vacate releases them", nil
	}
	return true, "", nil
}

// PendingProjectHandoffTx reads the newest unconsumed handoff on the work
// for the continuity projection. Read-time visibility only: no gate consumes
// it here. It runs inside the caller's transaction.
func PendingProjectHandoffTx(ctx context.Context, tx *sql.Tx, workID string) (*ProjectHandoff, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT handoff_id FROM project_handoffs WHERE work_id=? AND state='recorded' ORDER BY `+projectHandoffEventOrder+` LIMIT 1`, workID).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "project_handoff", "cannot read the pending handoff", true, "retry once the database is readable", err)
	}
	handoff, err := readProjectHandoffTx(ctx, tx, workID, id)
	if err != nil {
		return nil, err
	}
	return &handoff, nil
}

// PendingProjectHandoffForProjectTx reads the boot/resume frontier: the
// newest handoff on the work addressed to one Project under the ACTIVE
// contract version, whatever its state. A frontier that still stands
// recorded renders as the bounded repository job the receiving session must
// consume before managed execution. A consumed frontier renders to every
// session whose verified placement names the receiving Project (CD-0182 D5
// amendment): the bind names the work-and-Project pair, so the session that
// recorded the consume and a later landed session of the Project both read
// the bounded job and context — a replay after a lost consume response
// recovers it, and a second coordinator session reads the job it dispatches
// under. A consumed bind read without a resolvable placement, or by a
// session of another Project, renders nothing; a bind under a superseded
// contract never reaches this read because the frontier
// query filters on the active contract. An older recorded handoff behind
// the frontier is superseded by recorded event order — the boot never
// resurrects it, and
// a handoff recorded under a superseded contract can never be consumed, so
// resume renders nothing for it. Visibility here never consumes or
// authorizes. It runs inside the caller's transaction.
func PendingProjectHandoffForProjectTx(ctx context.Context, tx *sql.Tx, workID, projectID, sessionRef string) (*ProjectHandoff, error) {
	version, contractErr := activeWorkflowContractVersion(ctx, tx, workID, "project_handoff")
	if contractErr == sql.ErrNoRows {
		// No active workflow contract: no handoff can bind, so the boot
		// renders no bounded job.
		return nil, nil
	}
	if contractErr != nil {
		return nil, contractErr
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT handoff_id FROM project_handoffs WHERE work_id=? AND target_project_id=? AND contract_version=? ORDER BY `+projectHandoffEventOrder+` LIMIT 1`, workID, projectID, version).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "project_handoff", "cannot read the addressed handoff", true, "retry once the database is readable", err)
	}
	handoff, err := readProjectHandoffTx(ctx, tx, workID, id)
	if err != nil {
		return nil, err
	}
	if handoff.State == ProjectHandoffRecorded {
		return &handoff, nil
	}
	if handoff.State == ProjectHandoffConsumed && sessionRef != "" {
		// The consumed frontier renders to every session whose verified
		// placement names the receiving Project — the same admission the
		// managed-execution gate grants — not only the session whose
		// consume event the bind recorded. An unplaced session and a
		// session of another Project render nothing.
		placed, placeErr := verifiedSessionPlacementTx(ctx, tx, workID, sessionRef)
		if placeErr != nil {
			return nil, placeErr
		}
		if placed == projectID {
			return &handoff, nil
		}
	}
	return nil, nil
}

// ReadPendingProjectHandoffForProject is the transaction-taking wrapper the
// boot/resume flow reads the addressed bounded job through. The session
// reference is the authenticated receiving session the resume carries; it
// decides whose consumed bind renders again, and an empty reference renders
// recorded handoffs only.
func ReadPendingProjectHandoffForProject(ctx context.Context, s *Store, workID, projectID, sessionRef string) (*ProjectHandoff, error) {
	var handoff *ProjectHandoff
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return readPendingProjectHandoffForProjectTx(ctx, transaction, workID, projectID, sessionRef, &handoff)
	})
	if err != nil {
		return nil, err
	}
	return handoff, nil
}

func readPendingProjectHandoffForProjectTx(ctx context.Context, transaction *Transaction, workID, projectID, sessionRef string, out **ProjectHandoff) error {
	tx, err := transactionSQL(transaction, "project_handoff")
	if err != nil {
		return err
	}
	*out, err = PendingProjectHandoffForProjectTx(ctx, tx, workID, projectID, sessionRef)
	return err
}
