package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

func (s *Store) CreateResearchPack(ctx context.Context, req CreateResearchPackRequest) (ResearchPack, error) {
	return CreateResearchPack(ctx, s, req)
}

func CreateResearchPack(ctx context.Context, s *Store, req CreateResearchPackRequest) (ResearchPack, error) {
	var out ResearchPack
	if err := validateResearchIdentity(req.Identity); err != nil {
		return out, err
	}
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
	req.Revision = revision
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return out, err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return out, err
	}
	if replay {
		var result researchResult
		if err := json.Unmarshal(prior, &result); err != nil {
			_ = tx.Rollback()
			return out, researchUnavailable("cannot decode idempotent research result", err)
		}
		out, err = readResearchPackTx(ctx, tx, result.PackID, 1000)
		if err != nil {
			_ = tx.Rollback()
			return out, err
		}
		return out, tx.Commit()
	}
	packID := req.PackID
	if packID == "" {
		packID = newResearchID("pack")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id = ?`, req.OwnerWorkID).Scan(&lifecycle); err == sql.ErrNoRows {
		_ = tx.Rollback()
		return out, researchNotFound("owner work item does not exist")
	} else if err != nil {
		_ = tx.Rollback()
		return out, researchUnavailable("cannot read owner work item", err)
	}
	if isTerminalLifecycle(lifecycle) {
		_ = tx.Rollback()
		return out, researchInvalid("owner work item is terminal")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, packID, req.OwnerWorkID, 1, req.Freshness, 1, now, now); err != nil {
		_ = tx.Rollback()
		return out, researchConstraint("research pack already exists or owner is invalid", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES(?,?,?,?,?,?,?,?)`, packID, 1, revision.Question, revision.ScopeIn, revision.ScopeOut, revision.DoneWhen, revision.Method, now); err != nil {
		_ = tx.Rollback()
		return out, researchConstraint("cannot create initial research revision", err)
	}
	out, err = readResearchPackTx(ctx, tx, packID, 1000)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: packID, Revision: 1}); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return ResearchPack{}, researchUnavailable("cannot commit research pack", err)
	}
	return out, nil
}

func (s *Store) AppendResearchRevision(ctx context.Context, req AppendResearchRevisionRequest) (ResearchRevision, error) {
	return AppendResearchRevision(ctx, s, req)
}

func AppendResearchRevision(ctx context.Context, s *Store, req AppendResearchRevisionRequest) (ResearchRevision, error) {
	var out ResearchRevision
	if err := validateResearchIdentity(req.Identity); err != nil {
		return out, err
	}
	if req.PackID == "" || req.ExpectedVersion < 1 {
		return out, researchInvalid("pack_id and positive expected_version are required")
	}
	revision, err := normalizeRevision(req.Revision)
	if err != nil {
		return out, err
	}
	req.Revision = revision
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return out, err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return out, err
	}
	if replay {
		var result researchResult
		if err := json.Unmarshal(prior, &result); err != nil {
			_ = tx.Rollback()
			return out, researchUnavailable("cannot decode idempotent revision result", err)
		}
		out, err = readRevisionTx(ctx, tx, result.PackID, result.Revision)
		if err != nil {
			_ = tx.Rollback()
			return out, err
		}
		return out, tx.Commit()
	}
	pack, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	newRevision := pack.CurrentRevision + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES(?,?,?,?,?,?,?,?)`, req.PackID, newRevision, revision.Question, revision.ScopeIn, revision.ScopeOut, revision.DoneWhen, revision.Method, now); err != nil {
		_ = tx.Rollback()
		return out, researchConstraint("cannot append research revision", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE active_research_packs SET current_revision=?, freshness='current', expected_version=?, updated_at=? WHERE pack_id=? AND expected_version=?`, newRevision, req.ExpectedVersion+1, now, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return out, researchUnavailable("cannot advance research pack", err)
	}
	out, err = readRevisionTx(ctx, tx, req.PackID, newRevision)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: req.PackID, Revision: newRevision}); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return ResearchRevision{}, researchUnavailable("cannot commit research revision", err)
	}
	return out, nil
}

func (s *Store) AddResearchFinding(ctx context.Context, req ResearchFindingRequest) (ResearchFinding, error) {
	return addResearchFinding(ctx, s, req, false)
}
func (s *Store) UpdateResearchFinding(ctx context.Context, req ResearchFindingRequest) (ResearchFinding, error) {
	return addResearchFinding(ctx, s, req, true)
}
func AddResearchFinding(ctx context.Context, s *Store, req ResearchFindingRequest) (ResearchFinding, error) {
	return addResearchFinding(ctx, s, req, false)
}
func UpdateResearchFinding(ctx context.Context, s *Store, req ResearchFindingRequest) (ResearchFinding, error) {
	return addResearchFinding(ctx, s, req, true)
}

func addResearchFinding(ctx context.Context, s *Store, req ResearchFindingRequest, update bool) (ResearchFinding, error) {
	var out ResearchFinding
	if err := validateResearchIdentity(req.Identity); err != nil {
		return out, err
	}
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
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return out, err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return out, err
	}
	if replay {
		var result researchResult
		if err := json.Unmarshal(prior, &result); err != nil {
			_ = tx.Rollback()
			return out, researchUnavailable("cannot decode idempotent finding result", err)
		}
		out, err = readFindingTx(ctx, tx, result.PackID, result.Revision, result.ID)
		if err != nil {
			_ = tx.Rollback()
			return out, err
		}
		return out, tx.Commit()
	}
	pack, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	revision, err := resolveResearchRevision(req.Revision, pack)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := ensureRevisionMutable(ctx, tx, req.PackID, revision); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM active_research_findings WHERE pack_id=? AND revision=? AND finding_id=?`, req.PackID, revision, req.Finding.FindingID).Scan(&exists)
	if !update && err == nil {
		_ = tx.Rollback()
		return out, researchConflict("finding already exists")
	}
	if update && err == sql.ErrNoRows {
		_ = tx.Rollback()
		return out, researchNotFound("finding does not exist")
	}
	if err != nil && err != sql.ErrNoRows {
		_ = tx.Rollback()
		return out, researchUnavailable("cannot inspect finding", err)
	}
	if update {
		_, err = tx.ExecContext(ctx, `UPDATE active_research_findings SET kind=?,statement=?,confidence=?,freshness=?,status=? WHERE pack_id=? AND revision=? AND finding_id=?`, req.Finding.Kind, req.Finding.Statement, req.Finding.Confidence, req.Finding.Freshness, req.Finding.Status, req.PackID, revision, req.Finding.FindingID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status) VALUES(?,?,?,?,?,?,?,?)`, req.PackID, revision, req.Finding.FindingID, req.Finding.Kind, req.Finding.Statement, req.Finding.Confidence, req.Finding.Freshness, req.Finding.Status)
	}
	if err != nil {
		_ = tx.Rollback()
		return out, researchConstraint("cannot write finding", err)
	}
	if err := bumpResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	out, err = readFindingTx(ctx, tx, req.PackID, revision, req.Finding.FindingID)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: req.PackID, Revision: revision, ID: req.Finding.FindingID}); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return ResearchFinding{}, researchUnavailable("cannot commit finding", err)
	}
	return out, nil
}

func (s *Store) AddResearchSource(ctx context.Context, req ResearchSourceRequest) (ResearchSource, error) {
	return addResearchSource(ctx, s, req, false)
}
func (s *Store) UpdateResearchSource(ctx context.Context, req ResearchSourceRequest) (ResearchSource, error) {
	return addResearchSource(ctx, s, req, true)
}
func AddResearchSource(ctx context.Context, s *Store, req ResearchSourceRequest) (ResearchSource, error) {
	return addResearchSource(ctx, s, req, false)
}
func UpdateResearchSource(ctx context.Context, s *Store, req ResearchSourceRequest) (ResearchSource, error) {
	return addResearchSource(ctx, s, req, true)
}

func addResearchSource(ctx context.Context, s *Store, req ResearchSourceRequest, update bool) (ResearchSource, error) {
	var out ResearchSource
	if err := validateResearchIdentity(req.Identity); err != nil {
		return out, err
	}
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
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return out, err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return out, err
	}
	if replay {
		var result researchResult
		if err := json.Unmarshal(prior, &result); err != nil {
			_ = tx.Rollback()
			return out, researchUnavailable("cannot decode idempotent source result", err)
		}
		out, err = readSourceTx(ctx, tx, result.PackID, result.Revision, result.ID)
		if err != nil {
			_ = tx.Rollback()
			return out, err
		}
		return out, tx.Commit()
	}
	pack, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	revision, err := resolveResearchRevision(req.Revision, pack)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := ensureRevisionMutable(ctx, tx, req.PackID, revision); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM active_research_sources WHERE pack_id=? AND revision=? AND source_id=?`, req.PackID, revision, req.Source.SourceID).Scan(&exists)
	if !update && err == nil {
		_ = tx.Rollback()
		return out, researchConflict("source already exists")
	}
	if update && err == sql.ErrNoRows {
		_ = tx.Rollback()
		return out, researchNotFound("source does not exist")
	}
	if err != nil && err != sql.ErrNoRows {
		_ = tx.Rollback()
		return out, researchUnavailable("cannot inspect source", err)
	}
	if update {
		_, err = tx.ExecContext(ctx, `UPDATE active_research_sources SET kind=?,locator=?,title=?,publisher_or_author=?,published_at=?,accessed_at=? WHERE pack_id=? AND revision=? AND source_id=?`, req.Source.Kind, req.Source.Locator, req.Source.Title, req.Source.PublisherOrAuthor, nullableString(req.Source.PublishedAt), req.Source.AccessedAt, req.PackID, revision, req.Source.SourceID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at) VALUES(?,?,?,?,?,?,?,?,?)`, req.PackID, revision, req.Source.SourceID, req.Source.Kind, req.Source.Locator, req.Source.Title, req.Source.PublisherOrAuthor, nullableString(req.Source.PublishedAt), req.Source.AccessedAt)
	}
	if err != nil {
		_ = tx.Rollback()
		return out, researchConstraint("cannot write source", err)
	}
	if err := bumpResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	out, err = readSourceTx(ctx, tx, req.PackID, revision, req.Source.SourceID)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: req.PackID, Revision: revision, ID: req.Source.SourceID}); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return ResearchSource{}, researchUnavailable("cannot commit source", err)
	}
	return out, nil
}

func (s *Store) BindResearchConsumer(ctx context.Context, req BindResearchConsumerRequest) (ResearchConsumer, error) {
	return BindResearchConsumer(ctx, s, req)
}
func BindResearchConsumer(ctx context.Context, s *Store, req BindResearchConsumerRequest) (ResearchConsumer, error) {
	var out ResearchConsumer
	if err := validateResearchIdentity(req.Identity); err != nil {
		return out, err
	}
	if req.PackID == "" || req.Revision < 1 || req.ExpectedVersion < 1 || req.Consumer.ConsumerWorkID == "" {
		return out, researchInvalid("pack, revision, expected_version, and consumer are required")
	}
	if !validResearchUseRole(req.Consumer.UseRole) {
		return out, researchInvalid("consumer use_role is not recognized")
	}
	req.Consumer.PackID, req.Consumer.Revision = req.PackID, req.Revision
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return out, err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return out, err
	}
	if replay {
		var result researchResult
		if err := json.Unmarshal(prior, &result); err != nil {
			_ = tx.Rollback()
			return out, researchUnavailable("cannot decode idempotent consumer result", err)
		}
		if result.Consumer != nil {
			out = *result.Consumer
		} else {
			out, err = readConsumerTx(ctx, tx, result.PackID, result.Revision, result.ID)
			if err != nil {
				_ = tx.Rollback()
				return out, err
			}
		}
		return out, tx.Commit()
	}
	if _, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, req.Consumer.ConsumerWorkID).Scan(&lifecycle); err == sql.ErrNoRows {
		_ = tx.Rollback()
		return out, researchNotFound("consumer work item does not exist")
	} else if err != nil {
		_ = tx.Rollback()
		return out, researchUnavailable("cannot read consumer work item", err)
	}
	if isTerminalLifecycle(lifecycle) {
		_ = tx.Rollback()
		return out, researchInvalid("terminal work cannot be an active consumer")
	}
	var revisionExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM active_research_revisions WHERE pack_id=? AND revision=?`, req.PackID, req.Revision).Scan(&revisionExists); err == sql.ErrNoRows {
		_ = tx.Rollback()
		return out, researchNotFound("research revision does not exist")
	} else if err != nil {
		_ = tx.Rollback()
		return out, researchUnavailable("cannot read research revision", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	accepted := req.Consumer.AcceptedAt
	if accepted == "" {
		accepted = now
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES(?,?,?,?,?,?)`, req.PackID, req.Revision, req.Consumer.ConsumerWorkID, req.Consumer.UseRole, boolInt(req.Consumer.Required), accepted)
	if err != nil {
		_ = tx.Rollback()
		return out, researchConstraint("consumer binding already exists or is invalid", err)
	}
	if err := bumpResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	out, err = readConsumerTx(ctx, tx, req.PackID, req.Revision, req.Consumer.ConsumerWorkID)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: req.PackID, Revision: req.Revision, ID: req.Consumer.ConsumerWorkID}); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return ResearchConsumer{}, researchUnavailable("cannot commit consumer binding", err)
	}
	return out, nil
}

func (s *Store) UnbindResearchConsumer(ctx context.Context, req UnbindResearchConsumerRequest) (ResearchConsumer, error) {
	return UnbindResearchConsumer(ctx, s, req)
}
func UnbindResearchConsumer(ctx context.Context, s *Store, req UnbindResearchConsumerRequest) (ResearchConsumer, error) {
	var out ResearchConsumer
	if err := validateResearchIdentity(req.Identity); err != nil {
		return out, err
	}
	if req.PackID == "" || req.Revision < 1 || req.ExpectedVersion < 1 || req.ConsumerWorkID == "" {
		return out, researchInvalid("pack, revision, expected_version, and consumer are required")
	}
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return out, err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return out, err
	}
	if replay {
		var result researchResult
		if err := json.Unmarshal(prior, &result); err != nil {
			_ = tx.Rollback()
			return out, researchUnavailable("cannot decode idempotent unbind result", err)
		}
		out, err = readConsumerTx(ctx, tx, result.PackID, result.Revision, result.ID)
		if err != nil {
			_ = tx.Rollback()
			return out, err
		}
		return out, tx.Commit()
	}
	if _, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	out, err = readConsumerTx(ctx, tx, req.PackID, req.Revision, req.ConsumerWorkID)
	if err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_research_consumers WHERE pack_id=? AND revision=? AND consumer_work_id=?`, req.PackID, req.Revision, req.ConsumerWorkID); err != nil {
		_ = tx.Rollback()
		return out, researchUnavailable("cannot remove consumer binding", err)
	}
	var stillBound int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM active_research_consumers WHERE pack_id=? AND revision=? AND consumer_work_id=?)`, req.PackID, req.Revision, req.ConsumerWorkID).Scan(&stillBound); err != nil {
		_ = tx.Rollback()
		return out, researchUnavailable("cannot verify consumer unbind", err)
	}
	if stillBound != 0 {
		_ = tx.Rollback()
		return out, newFailure(KindInvariantViolation, "research_mutation", "consumer unbind postcondition did not hold", false, "retry the consumer unbind")
	}
	if err := bumpResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: req.PackID, Revision: req.Revision, ID: req.ConsumerWorkID, Consumer: &out}); err != nil {
		_ = tx.Rollback()
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return ResearchConsumer{}, researchUnavailable("cannot commit consumer unbind", err)
	}
	return out, nil
}

func (s *Store) PruneResearchRevisions(ctx context.Context, req ResearchPackMutationRequest) (int, error) {
	return PruneResearchRevisions(ctx, s, req)
}
func PruneResearchRevisions(ctx context.Context, s *Store, req ResearchPackMutationRequest) (int, error) {
	if err := validateResearchIdentity(req.Identity); err != nil {
		return 0, err
	}
	if req.PackID == "" || req.ExpectedVersion < 1 {
		return 0, researchInvalid("pack_id and positive expected_version are required")
	}
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return 0, err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return 0, err
	}
	if replay {
		var result researchResult
		if err := json.Unmarshal(prior, &result); err != nil {
			_ = tx.Rollback()
			return 0, researchUnavailable("cannot decode prune result", err)
		}
		_ = tx.Commit()
		return int(result.Count), nil
	}
	pack, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM active_research_revisions WHERE pack_id=? AND revision < ? AND NOT EXISTS (SELECT 1 FROM active_research_consumers c WHERE c.pack_id=active_research_revisions.pack_id AND c.revision=active_research_revisions.revision)`, req.PackID, pack.CurrentRevision)
	if err != nil {
		_ = tx.Rollback()
		return 0, researchUnavailable("cannot prune research revisions", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return 0, researchUnavailable("cannot verify pruned revisions", err)
	}
	if count > 0 {
		if err := bumpResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	var currentExists, missingConsumed, unreferencedOld int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM active_research_revisions WHERE pack_id=? AND revision=?)`, req.PackID, pack.CurrentRevision).Scan(&currentExists); err != nil {
		_ = tx.Rollback()
		return 0, researchUnavailable("cannot verify current revision after pruning", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM active_research_consumers c WHERE c.pack_id=? AND NOT EXISTS(SELECT 1 FROM active_research_revisions r WHERE r.pack_id=c.pack_id AND r.revision=c.revision))`, req.PackID).Scan(&missingConsumed); err != nil {
		_ = tx.Rollback()
		return 0, researchUnavailable("cannot verify consumed revisions after pruning", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM active_research_revisions r WHERE r.pack_id=? AND r.revision < ? AND NOT EXISTS(SELECT 1 FROM active_research_consumers c WHERE c.pack_id=r.pack_id AND c.revision=r.revision))`, req.PackID, pack.CurrentRevision).Scan(&unreferencedOld); err != nil {
		_ = tx.Rollback()
		return 0, researchUnavailable("cannot verify old revisions after pruning", err)
	}
	if currentExists == 0 || missingConsumed != 0 || unreferencedOld != 0 {
		_ = tx.Rollback()
		return 0, newFailure(KindInvariantViolation, "research_mutation", "revision pruning postcondition did not hold", false, "retry pruning after repairing revision bindings")
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: req.PackID, Count: int(count)}); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, researchUnavailable("cannot commit revision pruning", err)
	}
	return int(count), nil
}

func (s *Store) DeleteResearchPack(ctx context.Context, req ResearchPackMutationRequest) error {
	return DeleteResearchPack(ctx, s, req)
}
func DeleteResearchPack(ctx context.Context, s *Store, req ResearchPackMutationRequest) error {
	if err := validateResearchIdentity(req.Identity); err != nil {
		return err
	}
	if req.PackID == "" || req.ExpectedVersion < 1 {
		return researchInvalid("pack_id and positive expected_version are required")
	}
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return err
	}
	if replay {
		_ = prior
		return tx.Commit()
	}
	if _, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return err
	}
	var blocked string
	err = tx.QueryRowContext(ctx, `SELECT c.consumer_work_id FROM active_research_consumers c JOIN work_items w ON w.id=c.consumer_work_id WHERE c.pack_id=? AND c.required=1 AND w.lifecycle NOT IN ('completed','cancelled','superseded') LIMIT 1`, req.PackID).Scan(&blocked)
	if err == nil {
		_ = tx.Rollback()
		return newFailure(KindResearchConsumerBlocked, "delete_research_pack", "required active consumer remains bound: "+blocked, false, "unbind, rebind, or terminalize every required active consumer")
	}
	if err != sql.ErrNoRows {
		_ = tx.Rollback()
		return researchUnavailable("cannot inspect active research consumers", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_research_packs WHERE pack_id=?`, req.PackID); err != nil {
		_ = tx.Rollback()
		return researchUnavailable("cannot delete research pack", err)
	}
	var stillExists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM active_research_packs WHERE pack_id=?)`, req.PackID).Scan(&stillExists); err != nil {
		_ = tx.Rollback()
		return researchUnavailable("cannot verify research deletion", err)
	}
	if stillExists != 0 {
		_ = tx.Rollback()
		return newFailure(KindInvariantViolation, "research_mutation", "research deletion postcondition did not hold", false, "retry the research deletion")
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: req.PackID}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return researchUnavailable("cannot commit research deletion", err)
	}
	return nil
}

func (s *Store) SetResearchFreshness(ctx context.Context, req SetResearchFreshnessRequest) error {
	return SetResearchFreshness(ctx, s, req)
}

// SetResearchFreshness records review evidence explicitly. It never compares
// timestamps or infers a state from revision age.
func SetResearchFreshness(ctx context.Context, s *Store, req SetResearchFreshnessRequest) error {
	if err := validateResearchIdentity(req.Identity); err != nil {
		return err
	}
	if req.PackID == "" || req.ExpectedVersion < 1 || !validResearchFreshness(req.Freshness) {
		return researchInvalid("pack_id, expected_version, and a closed freshness value are required")
	}
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return err
	}
	if replay {
		_ = prior
		return tx.Commit()
	}
	if _, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE active_research_packs SET freshness=?,expected_version=expected_version+1,updated_at=? WHERE pack_id=? AND expected_version=?`, req.Freshness, time.Now().UTC().Format(time.RFC3339Nano), req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return researchUnavailable("cannot write research freshness", err)
	}
	var got string
	if err := tx.QueryRowContext(ctx, `SELECT freshness FROM active_research_packs WHERE pack_id=?`, req.PackID).Scan(&got); err != nil {
		_ = tx.Rollback()
		return researchUnavailable("cannot verify research freshness", err)
	}
	if got != string(req.Freshness) {
		_ = tx.Rollback()
		return newFailure(KindInvariantViolation, "research_mutation", "research freshness postcondition did not hold", false, "retry the freshness review")
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: req.PackID}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return researchUnavailable("cannot commit research freshness", err)
	}
	return nil
}

func (s *Store) BindResearchFindingSource(ctx context.Context, req ResearchFindingSourceRequest) error {
	return BindResearchFindingSource(ctx, s, req)
}
func BindResearchFindingSource(ctx context.Context, s *Store, req ResearchFindingSourceRequest) error {
	if err := validateResearchIdentity(req.Identity); err != nil {
		return err
	}
	if req.PackID == "" || req.Revision < 1 || req.ExpectedVersion < 1 || req.FindingID == "" || req.SourceID == "" {
		return researchInvalid("pack, revision, expected_version, finding, and source are required")
	}
	digest, err := canonicalRequestDigest(req)
	if err != nil {
		return err
	}
	tx, prior, replay, err := beginResearchMutation(ctx, s, req.Identity, digest)
	if err != nil {
		return err
	}
	if replay {
		_ = prior
		return tx.Commit()
	}
	if _, err := lockResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := ensureRevisionMutable(ctx, tx, req.PackID, req.Revision); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES(?,?,?,?)`, req.PackID, req.Revision, req.FindingID, req.SourceID); err != nil {
		_ = tx.Rollback()
		return researchConstraint("cannot bind finding source", err)
	}
	if err := bumpResearchPack(ctx, tx, req.PackID, req.ExpectedVersion); err != nil {
		_ = tx.Rollback()
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM active_research_finding_sources WHERE pack_id=? AND revision=? AND finding_id=? AND source_id=?`, req.PackID, req.Revision, req.FindingID, req.SourceID).Scan(&n); err != nil {
		_ = tx.Rollback()
		return researchUnavailable("cannot verify finding source binding", err)
	}
	if err := finishResearchMutation(ctx, tx, req.Identity, digest, researchResult{PackID: req.PackID, Revision: req.Revision, ID: req.FindingID + "/" + req.SourceID}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return researchUnavailable("cannot commit finding source binding", err)
	}
	return nil
}
