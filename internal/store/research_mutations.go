package store

import (
	"context"
	"database/sql"
	"time"
)

// CreateResearchPackWithinTx creates a research pack and its first revision on
// the caller's transaction. The caller owns idempotency and the commit.
func CreateResearchPackWithinTx(ctx context.Context, transaction *Transaction, req CreateResearchPackRequest) (ResearchPack, error) {
	tx, err := transactionSQL(transaction, "research_pack_create")
	if err != nil {
		return ResearchPack{}, err
	}
	return createResearchPackWithinRawTx(ctx, tx, req, transaction.now())
}

func createResearchPackWithinRawTx(ctx context.Context, tx *sql.Tx, req CreateResearchPackRequest, observedAt time.Time) (ResearchPack, error) {
	var out ResearchPack
	if req.OwnerWorkID == "" {
		return out, researchInvalid("owner_work_id is required")
	}
	if req.Freshness == "" {
		req.Freshness = ResearchCurrent
	}
	if !validResearchFreshness(req.Freshness) {
		return out, researchInvalid("freshness is not recognized")
	}
	revision, err := normalizeRevision(req.Revision)
	if err != nil {
		return out, err
	}
	packID := req.PackID
	if packID == "" {
		packID = newResearchID("pack")
	}
	now := observedAt.UTC().Format(time.RFC3339Nano)
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id = ?`, req.OwnerWorkID).Scan(&lifecycle); err == sql.ErrNoRows {
		return out, researchNotFound("owner work item does not exist")
	} else if err != nil {
		return out, researchUnavailable("cannot read owner work item", err)
	}
	if isTerminalLifecycle(lifecycle) {
		return out, researchInvalid("owner work item is terminal")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, packID, req.OwnerWorkID, 1, req.Freshness, 1, now, now); err != nil {
		return out, researchConstraint("research pack already exists or owner is invalid", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness) VALUES(?,?,?,?,?,?,?,?,?)`, packID, 1, revision.Question, revision.ScopeIn, revision.ScopeOut, revision.DoneWhen, revision.Method, now, req.Freshness); err != nil {
		return out, researchConstraint("cannot create initial research revision", err)
	}
	out, err = readResearchPackTx(ctx, tx, packID, 1000)
	if err != nil {
		return out, err
	}
	return out, nil
}

// AppendResearchRevisionWithinTx appends a revision on the caller's
// transaction. The caller owns idempotency and the commit.
func AppendResearchRevisionWithinTx(ctx context.Context, transaction *Transaction, req AppendResearchRevisionRequest) (ResearchRevision, error) {
	tx, err := transactionSQL(transaction, "research_revision_append")
	if err != nil {
		return ResearchRevision{}, err
	}
	return appendResearchRevisionWithinRawTx(ctx, tx, req, transaction.now())
}

func appendResearchRevisionWithinRawTx(ctx context.Context, tx *sql.Tx, req AppendResearchRevisionRequest, observedAt time.Time) (ResearchRevision, error) {
	var out ResearchRevision
	if req.PackID == "" || req.ExpectedVersion < 1 {
		return out, researchInvalid("pack_id and positive expected_version are required")
	}
	revision, err := normalizeRevision(req.Revision)
	if err != nil {
		return out, err
	}
	req.Revision = revision
	pack, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion)
	if err != nil {
		return out, err
	}
	if err := ensureResearchPackAuthorable(ctx, tx, pack); err != nil {
		return out, err
	}
	priorRevision, err := readRevisionTx(ctx, tx, req.PackID, pack.CurrentRevision)
	if err != nil {
		return out, err
	}
	now := observedAt.UTC().Format(time.RFC3339Nano)
	newRevision := pack.CurrentRevision + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness) VALUES(?,?,?,?,?,?,?,?,?)`, req.PackID, newRevision, revision.Question, revision.ScopeIn, revision.ScopeOut, revision.DoneWhen, revision.Method, now, "current"); err != nil {
		return out, researchConstraint("cannot append research revision", err)
	}
	restated := researchBriefRestated(priorRevision, revision)
	if err := copyResearchRevisionContent(ctx, tx, req.PackID, pack.CurrentRevision, newRevision, restated); err != nil {
		return out, err
	}
	// Pack freshness column is a display summary; the new revision
	// is fresh by construction and every other revision's authoritative state is
	// deliberately untouched, so an append can never silently un-stale pinned
	// content.
	if _, err := tx.ExecContext(ctx, `UPDATE active_research_packs SET current_revision=?, freshness='current', expected_version=?, updated_at=? WHERE pack_id=? AND expected_version=?`, newRevision, req.ExpectedVersion+1, now, req.PackID, req.ExpectedVersion); err != nil {
		return out, researchUnavailable("cannot advance research pack", err)
	}
	out, err = readRevisionTx(ctx, tx, req.PackID, newRevision)
	if err != nil {
		return out, err
	}
	return out, nil
}

func recordResearchFindingWithinRawTx(ctx context.Context, tx *sql.Tx, req ResearchFindingRequest, observedAt time.Time) (ResearchFinding, error) {
	var out ResearchFinding
	if err := validateFinding(req.Finding); err != nil {
		return out, err
	}
	if req.PackID == "" || req.ExpectedVersion < 1 {
		return out, researchInvalid("pack_id and positive expected_version are required")
	}
	if req.Finding.PackID == "" {
		req.Finding.PackID = req.PackID
	}
	if req.Finding.PackID != req.PackID {
		return out, researchInvalid("finding pack_id does not match request")
	}
	if err := validateResearchScopes(&req.Finding.Scopes); err != nil {
		return out, err
	}
	pack, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion)
	if err != nil {
		return out, err
	}
	if err := ensureResearchPackAuthorable(ctx, tx, pack); err != nil {
		return out, err
	}
	if err := validateResearchScopeReferences(ctx, tx, req.Finding.Scopes); err != nil {
		return out, err
	}
	revision, err := resolveResearchRevision(req.Revision, pack)
	if err != nil {
		return out, err
	}
	if err := ensureRevisionMutable(ctx, tx, req.PackID, revision); err != nil {
		return out, err
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM active_research_findings WHERE pack_id=? AND revision=? AND finding_id=?`, req.PackID, revision, req.Finding.FindingID).Scan(&exists)
	if err != nil && err != sql.ErrNoRows {
		return out, researchUnavailable("cannot inspect finding", err)
	}
	update := err == nil
	if update {
		// Clear scope before changing mode: the database guard makes home with any
		// explicit row structurally impossible, including during an update.
		if _, err := tx.ExecContext(ctx, `DELETE FROM active_research_finding_scopes WHERE pack_id=? AND revision=? AND finding_id=?`, req.PackID, revision, req.Finding.FindingID); err != nil {
			return out, researchUnavailable("cannot clear finding scope", err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE active_research_findings SET kind=?,statement=?,confidence=?,freshness=?,status=?,scope_mode=? WHERE pack_id=? AND revision=? AND finding_id=?`, req.Finding.Kind, req.Finding.Statement, req.Finding.Confidence, req.Finding.Freshness, req.Finding.Status, req.Finding.Scopes.Mode, req.PackID, revision, req.Finding.FindingID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES(?,?,?,?,?,?,?,?,?)`, req.PackID, revision, req.Finding.FindingID, req.Finding.Kind, req.Finding.Statement, req.Finding.Confidence, req.Finding.Freshness, req.Finding.Status, req.Finding.Scopes.Mode)
	}
	if err != nil {
		return out, researchConstraint("cannot write finding", err)
	}
	// Citations are declared on the finding and are the only write path into
	// active_research_finding_sources. They are replaced wholesale so an update
	// cannot leave a link the caller no longer claims; the composite foreign key
	// refuses a source that is absent from this revision.
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_research_finding_sources WHERE pack_id=? AND revision=? AND finding_id=?`, req.PackID, revision, req.Finding.FindingID); err != nil {
		return out, researchUnavailable("cannot clear finding citations", err)
	}
	if err := writeResearchFindingScopes(ctx, tx, req.PackID, revision, req.Finding.FindingID, req.Finding.Scopes); err != nil {
		return out, err
	}
	for _, sourceID := range req.Finding.SourceIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,?,?,?)`, req.PackID, revision, req.Finding.FindingID, sourceID); err != nil {
			return out, researchConstraint("cannot cite a source that is absent from this revision", err)
		}
	}
	if err := bumpResearchPack(ctx, tx, req.PackID, req.ExpectedVersion, observedAt); err != nil {
		return out, err
	}
	out, err = readFindingTx(ctx, tx, req.PackID, revision, req.Finding.FindingID)
	if err != nil {
		return out, err
	}
	return out, nil
}

func recordResearchSourceWithinRawTx(ctx context.Context, tx *sql.Tx, req ResearchSourceRequest, observedAt time.Time) (ResearchSource, error) {
	var out ResearchSource
	if err := validateSource(req.Source); err != nil {
		return out, err
	}
	if req.PackID == "" || req.ExpectedVersion < 1 {
		return out, researchInvalid("pack_id and positive expected_version are required")
	}
	if req.Source.PackID == "" {
		req.Source.PackID = req.PackID
	}
	if req.Source.PackID != req.PackID {
		return out, researchInvalid("source pack_id does not match request")
	}
	pack, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion)
	if err != nil {
		return out, err
	}
	if err := ensureResearchPackAuthorable(ctx, tx, pack); err != nil {
		return out, err
	}
	revision, err := resolveResearchRevision(req.Revision, pack)
	if err != nil {
		return out, err
	}
	if err := ensureRevisionMutable(ctx, tx, req.PackID, revision); err != nil {
		return out, err
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM active_research_sources WHERE pack_id=? AND revision=? AND source_id=?`, req.PackID, revision, req.Source.SourceID).Scan(&exists)
	if err != nil && err != sql.ErrNoRows {
		return out, researchUnavailable("cannot inspect source", err)
	}
	update := err == nil
	if update {
		_, err = tx.ExecContext(ctx, `UPDATE active_research_sources SET kind=?,locator=?,title=?,publisher_or_author=?,published_at=?,accessed_at=? WHERE pack_id=? AND revision=? AND source_id=?`, req.Source.Kind, req.Source.Locator, req.Source.Title, req.Source.PublisherOrAuthor, nullableString(req.Source.PublishedAt), req.Source.AccessedAt, req.PackID, revision, req.Source.SourceID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at) VALUES(?,?,?,?,?,?,?,?,?)`, req.PackID, revision, req.Source.SourceID, req.Source.Kind, req.Source.Locator, req.Source.Title, req.Source.PublisherOrAuthor, nullableString(req.Source.PublishedAt), req.Source.AccessedAt)
	}
	if err != nil {
		return out, researchConstraint("cannot write source", err)
	}
	if err := bumpResearchPack(ctx, tx, req.PackID, req.ExpectedVersion, observedAt); err != nil {
		return out, err
	}
	out, err = readSourceTx(ctx, tx, req.PackID, revision, req.Source.SourceID)
	if err != nil {
		return out, err
	}
	return out, nil
}

// SetResearchFreshnessWithinTx records review evidence on the caller's
// transaction. It never compares timestamps or infers a state from revision
// age. The caller owns idempotency and the commit.
func SetResearchFreshnessWithinTx(ctx context.Context, transaction *Transaction, req SetResearchFreshnessRequest) error {
	tx, err := transactionSQL(transaction, "research_freshness")
	if err != nil {
		return err
	}
	return setResearchFreshnessWithinRawTx(ctx, tx, req, transaction.now())
}

func setResearchFreshnessWithinRawTx(ctx context.Context, tx *sql.Tx, req SetResearchFreshnessRequest, observedAt time.Time) error {
	if req.PackID == "" || req.ExpectedVersion < 1 || !validResearchFreshness(req.Freshness) {
		return researchInvalid("pack_id, expected_version, and a closed freshness value are required")
	}
	// Freshness is set on the revision consumers pin; 0 means
	// the pack's current revision.
	if req.Revision < 0 {
		return researchInvalid("revision must be a positive pin or 0 for the current revision")
	}
	if _, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		return err
	}
	targetRevision := req.Revision
	if targetRevision == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT current_revision FROM active_research_packs WHERE pack_id=?`, req.PackID).Scan(&targetRevision); err != nil {
			return researchUnavailable("cannot resolve the current revision", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE active_research_revisions SET freshness=? WHERE pack_id=? AND revision=?`, req.Freshness, req.PackID, targetRevision); err != nil {
		return researchUnavailable("cannot write research freshness", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE active_research_packs SET freshness=(SELECT freshness FROM active_research_revisions WHERE pack_id=? AND revision=current_revision),expected_version=expected_version+1,updated_at=? WHERE pack_id=? AND expected_version=?`, req.PackID, observedAt.UTC().Format(time.RFC3339Nano), req.PackID, req.ExpectedVersion); err != nil {
		return researchUnavailable("cannot write research freshness summary", err)
	}
	var got string
	if err := tx.QueryRowContext(ctx, `SELECT freshness FROM active_research_revisions WHERE pack_id=? AND revision=?`, req.PackID, targetRevision).Scan(&got); err != nil {
		return researchUnavailable("cannot verify research freshness", err)
	}
	if got != string(req.Freshness) {
		return newFailure(KindInvariantViolation, "research_mutation", "research freshness postcondition did not hold", false, "retry the freshness review")
	}
	return nil
}

// RecordResearchFindingWithinTx records a finding: update when the finding
// already exists in the revision, add otherwise. Deterministic on stored
// state and idempotent under the caller's replay semantics.
func RecordResearchFindingWithinTx(ctx context.Context, transaction *Transaction, req ResearchFindingRequest) (ResearchFinding, error) {
	tx, err := transactionSQL(transaction, "research_finding_write")
	if err != nil {
		return ResearchFinding{}, err
	}
	return recordResearchFindingWithinRawTx(ctx, tx, req, transaction.now())
}

// RecordResearchSourceWithinTx records a source with the same upsert rule.
func RecordResearchSourceWithinTx(ctx context.Context, transaction *Transaction, req ResearchSourceRequest) (ResearchSource, error) {
	tx, err := transactionSQL(transaction, "research_source_write")
	if err != nil {
		return ResearchSource{}, err
	}
	return recordResearchSourceWithinRawTx(ctx, tx, req, transaction.now())
}
