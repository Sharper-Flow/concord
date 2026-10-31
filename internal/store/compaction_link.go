package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// orderedStrings renders a sorted copy of a string slice for identity
// digests and page payloads.
func orderedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

// CompactionBoundary selects whether a compaction link revalidates law and
// architecture before folding. The zero value runs the guard, so a caller that
// says nothing is protected and an exemption must be written explicitly.
type CompactionBoundary int

const (
	// CompactionBoundaryRevalidated runs the CD-0222 D7 boundary check in the
	// transaction that folds the link.
	CompactionBoundaryRevalidated CompactionBoundary = iota
	// CompactionBoundaryRecoveryExempt skips that check. Reconciling an
	// orphaned note is the closed recovery choice for a pending compaction, so
	// guarding it would refuse the only way out of that condition.
	CompactionBoundaryRecoveryExempt
)

// CompactionLinkRequest is the proof-backed PM6 linkage input. The helper
// verifies the committed blob before constructing the domain event.
type CompactionLinkRequest struct {
	EventID         string
	WorkID          string
	ExpectedVersion int64
	Actor           string
	OccurredAt      time.Time
	Home            KnowledgeHome
	CommitOID       string
	NotePath        string
	ExpectedHash    string
	Reason          string
	Boundary        CompactionBoundary
	// WithinTx projects the result after the link is applied and before commit.
	// An identical relink runs it in a transaction without repeating the event.
	WithinTx func(*Transaction) error
}

// compactionLinkPayload is the current (v3) link payload. expected_version
// records the work version the publish was pinned to. A link changes no
// work-item version, so the payload claims no resulting version.
type compactionLinkPayload struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	Title           string   `json:"title"`
	CompletedAt     string   `json:"completed_at"`
	OutcomeTag      string   `json:"outcome_tag"`
	LessonTags      []string `json:"lesson_tags"`
	TerminalState   string   `json:"terminal_state"`
	Priority        int64    `json:"priority"`
	Summary         string   `json:"summary"`
	SuccessorID     string   `json:"successor_work_id,omitempty"`
	ProductIDs      []string `json:"product_ids"`
	ProjectIDs      []string `json:"project_ids"`
	DomainIDs       []string `json:"domain_ids"`
	TagIDs          []string `json:"tag_ids"`
	HomeProjectID   string   `json:"home_project_id"`
	HomeLocatorID   string   `json:"home_locator_id"`
	NotePath        string   `json:"note_path"`
	CommitOID       string   `json:"commit_oid"`
	ContentHash     string   `json:"content_hash"`
	Reason          string   `json:"reason"`
	ExpectedVersion int64    `json:"expected_version"`
}

// PublishCompactionLink verifies the committed note before appending the link
// event and archived projection in the same SQLite transaction. The law-folder
// snapshot the CD-0222 D7 boundary revalidates against is read before that
// transaction opens: a git probe must never run inside one (CD-0195 D2).
func PublishCompactionLink(ctx context.Context, s *Store, req CompactionLinkRequest) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "publish_compaction_link", "store is not open", false, "open a store before publishing a compaction link")
	}
	if req.WorkID == "" || req.EventID == "" || req.Actor == "" || req.OccurredAt.IsZero() || req.ExpectedVersion < 1 || req.Reason == "" {
		return newFailure(KindInvalidOperation, "publish_compaction_link", "compaction link is missing bounded operation fields", false, "supply work, event, actor, time, version, and reason")
	}
	commit := req.CommitOID
	if commit == "" {
		var err error
		commit, err = resolveKnowledgeHead(ctx, req.Home)
		if err != nil {
			return err
		}
	}
	note, err := VerifyCommittedNote(ctx, req.Home.RepoPath, commit, req.NotePath, req.ExpectedHash)
	if err != nil {
		return err
	}
	if note.Kind != "work_note" || note.ID != req.WorkID {
		return newFailure(KindInvalidNoteProof, "publish_compaction_link", "verified note identity does not match the work", false, "publish the canonical work note for the requested work ID")
	}
	verifiedCtx, proofErr := s.EstablishKnowledgeSourceSetProof(ctx, req.WorkID)
	if proofErr != nil {
		return proofErr
	}
	var lifecycle string
	if err := s.db.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id = ?`, req.WorkID).Scan(&lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindUnknownScope, "publish_compaction_link", "work item does not exist", false, "publish a note for a known live work item")
		}
		return wrapFailure(KindUnavailable, "publish_compaction_link", "cannot inspect work lifecycle", true, "retry once the database is readable", err)
	}
	if !isTerminalLifecycle(lifecycle) {
		return newFailure(KindInvalidOperation, "publish_compaction_link", "work item is not terminal", false, "transition the work to a terminal state before linking its note")
	}
	if note.TerminalState != lifecycle {
		return newFailure(KindInvalidNoteProof, "publish_compaction_link", "note terminal_state does not match live work", false, "publish a note whose terminal state matches the work projection")
	}
	var existing struct {
		homeProject, homeLocator, notePath, commitOID, contentHash string
	}
	err = s.db.QueryRowContext(ctx, `SELECT home_project_id,home_locator_id,note_path,commit_oid,content_hash FROM archived_work WHERE id = ?`, req.WorkID).Scan(&existing.homeProject, &existing.homeLocator, &existing.notePath, &existing.commitOID, &existing.contentHash)
	if err == nil {
		if existing.homeProject == req.Home.HomeProjectID && existing.homeLocator == req.Home.HomeLocatorID && existing.notePath == note.NotePath && existing.commitOID == note.CommitOID && existing.contentHash == note.ContentHash {
			// Publication is proof-backed archive, not destruction (CD-0217):
			// an identical relink is idempotent and research is never a side
			// effect. Only an explicit research_retire batch deletes packs.
			return s.Transact(verifiedCtx, func(tx *Transaction) error {
				return replayCompactionLinkTx(ctx, tx, req, note.ContentHash)
			})
		}
		return newFailure(KindCompactionConflict, "publish_compaction_link", "work already has a different canonical locator", false, "resolve the competing canonical note before retrying")
	}
	if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "publish_compaction_link", "cannot inspect existing compaction linkage", true, "retry once the database is readable", err)
	}
	scopeDomainIDs := append([]string{}, note.DomainIDs...)
	payload := compactionLinkPayload{
		ID: note.ID, Type: note.Kind, Title: note.Title, CompletedAt: note.CompletedAt,
		OutcomeTag: note.OutcomeTag, LessonTags: note.LessonTags, TerminalState: note.TerminalState,
		Priority: note.Priority, Summary: note.Summary, SuccessorID: note.SuccessorID,
		ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs, DomainIDs: scopeDomainIDs,
		TagIDs: note.TagIDs, HomeProjectID: req.Home.HomeProjectID, HomeLocatorID: req.Home.HomeLocatorID,
		NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash,
		Reason: req.Reason, ExpectedVersion: req.ExpectedVersion,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "publish_compaction_link", "cannot encode the bounded compaction payload", false, "repair the note metadata", err)
	}
	operation := Operation{
		Events:           []Event{{EventID: req.EventID, Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.Actor, OccurredAt: req.OccurredAt, PayloadVersion: 3, Payload: payloadBytes}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion},
	}
	if err := s.Transact(verifiedCtx, func(tx *Transaction) error {
		return publishCompactionLinkTx(ctx, tx, req, operation, note.ContentHash)
	}); err != nil {
		return err
	}
	return nil
}

func replayCompactionLinkTx(ctx context.Context, tx *Transaction, req CompactionLinkRequest, contentHash string) error {
	if err := checkReviewedClosureContentHashQ(ctx, tx.tx, req.WorkID, contentHash); err != nil {
		return err
	}
	if req.WithinTx != nil {
		return req.WithinTx(tx)
	}
	return nil
}

// publishCompactionLinkTx runs the CD-0222 D7 boundary check and the link fold
// in the caller's transaction, so a law cutover cannot land between them.
//
// The subject of a compaction link is terminal, so the Domain-overlap half of
// the check has an empty footprint by construction and the law-revision half is
// what can refuse here. That refusal keeps output authorized under a superseded
// law revision out of Product truth when the external git write is still in flight.
func publishCompactionLinkTx(ctx context.Context, tx *Transaction, req CompactionLinkRequest, operation Operation, contentHash string) error {
	if err := checkReviewedClosureContentHashQ(ctx, tx.tx, req.WorkID, contentHash); err != nil {
		return err
	}
	if req.Boundary == CompactionBoundaryRevalidated {
		if err := CheckWorkflowConsequentialBoundaryTx(ctx, tx, req.WorkID); err != nil {
			return err
		}
	}
	_, err := ApplyOperationTx(ctx, tx, operation)
	if err != nil {
		return err
	}
	if req.WithinTx != nil {
		return req.WithinTx(tx)
	}
	return nil
}

func foldCompactionLinkPublished(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload compactionLinkPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.ID == "" || payload.ID != event.SubjectID || payload.Type != "work_note" || payload.Title == "" || payload.CompletedAt == "" || payload.OutcomeTag == "" || payload.Summary == "" || payload.HomeProjectID == "" || payload.HomeLocatorID == "" || payload.NotePath == "" || payload.CommitOID == "" || payload.ContentHash == "" || payload.Reason == "" || payload.ExpectedVersion < 1 {
		return newFailure(KindInvalidPayload, "fold_event", "compaction_link.published payload is incomplete", false, "supply the complete verified PM6 linkage payload")
	}
	if err := validateCompactionScopeArrays(payload); err != nil {
		return err
	}
	if err := validateCommitOID(payload.CommitOID); err != nil {
		return err
	}
	if err := validateNotePath(payload.NotePath); err != nil {
		return err
	}
	if err := validateContentHash(payload.ContentHash); err != nil {
		return err
	}
	if payload.TerminalState != "completed" && payload.TerminalState != "cancelled" && payload.TerminalState != "superseded" {
		return newFailure(KindInvalidPayload, "fold_event", "compaction link has an invalid terminal state", false, "use a PM4 terminal state")
	}
	var existing struct {
		HomeProjectID string
		HomeLocatorID string
		NotePath      string
		CommitOID     string
		ContentHash   string
	}
	err := tx.QueryRowContext(ctx, `SELECT home_project_id,home_locator_id,note_path,commit_oid,content_hash FROM archived_work WHERE id = ? AND home_project_id = ? AND home_locator_id = ?`, payload.ID, payload.HomeProjectID, payload.HomeLocatorID).Scan(
		&existing.HomeProjectID, &existing.HomeLocatorID, &existing.NotePath, &existing.CommitOID, &existing.ContentHash)
	if err == nil {
		if existing.HomeProjectID != payload.HomeProjectID || existing.HomeLocatorID != payload.HomeLocatorID || existing.NotePath != payload.NotePath || existing.CommitOID != payload.CommitOID || existing.ContentHash != payload.ContentHash {
			return newFailure(KindCompactionConflict, "fold_event", "work already has a different canonical locator", false, "resolve the competing canonical note before retrying")
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect the archived work projection", true, "retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO archived_work (id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,successor_work_id,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, payload.ID, payload.Type, payload.Title, payload.CompletedAt, payload.OutcomeTag, marshalStrings(payload.LessonTags), payload.TerminalState, payload.Priority, payload.Summary, nullableText(payload.SuccessorID), payload.HomeProjectID, payload.HomeLocatorID, payload.NotePath, payload.CommitOID, payload.ContentHash); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot write archived work projection", true, "retry once the database is writable", err)
	}
	for table, values := range map[string][]string{"archived_work_products": payload.ProductIDs, "archived_work_projects": payload.ProjectIDs, "archived_work_domains": payload.DomainIDs, "archived_work_tags": payload.TagIDs} {
		column := map[string]string{"archived_work_products": "product_id", "archived_work_projects": "project_id", "archived_work_domains": "domain_id", "archived_work_tags": "tag_id"}[table]
		for _, value := range values {
			if value == "" {
				return newFailure(KindInvalidPayload, "fold_event", "compaction scope contains an empty ID", false, "supply non-empty frozen scope IDs")
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (home_project_id, home_locator_id, work_id, "+column+") VALUES (?, ?, ?, ?)", payload.HomeProjectID, payload.HomeLocatorID, payload.ID, value); err != nil { //nolint:gosec // table and column come from the closed maps above and both values stay parameter-bound.
				return wrapFailure(KindUnavailable, "fold_event", "cannot write archived scope projection", true, "retry once the database is writable", err)
			}
		}
	}
	return nil
}

func validateCompactionScopeArrays(payload compactionLinkPayload) error {
	arrays := []struct {
		name   string
		values []string
	}{
		{"lesson_tags", payload.LessonTags},
		{"product_ids", payload.ProductIDs},
		{"project_ids", payload.ProjectIDs},
		{"domain_ids", payload.DomainIDs},
		{"tag_ids", payload.TagIDs},
	}
	for _, array := range arrays {
		if array.values == nil || len(array.values) > maxManifestArray {
			return newFailure(KindInvalidPayload, "fold_event", array.name+" must be an explicit bounded array", false, "supply at most 64 unique non-empty values")
		}
		seen := make(map[string]struct{}, len(array.values))
		for _, value := range array.values {
			if value == "" || utf8.RuneCountInString(value) > maxManifestID || strings.TrimSpace(value) != value {
				return newFailure(KindInvalidPayload, "fold_event", array.name+" contains an invalid value", false, "supply bounded clean values")
			}
			if _, duplicate := seen[value]; duplicate {
				return newFailure(KindInvalidPayload, "fold_event", array.name+" contains a duplicate value", false, "supply unique values")
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}
