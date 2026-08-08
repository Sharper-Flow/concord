package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func validateResearchIdentity(identity ResearchMutationIdentity) error {
	if identity.PrincipalRef == "" || identity.Tool == "" || identity.OperationKind == "" || identity.IdempotencyKey == "" {
		return researchInvalid("principal_ref, tool, operation_kind, and idempotency_key are required")
	}
	return nil
}

func canonicalRequestDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", researchUnavailable("cannot encode canonical research request", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func beginResearchMutation(ctx context.Context, s *Store, identity ResearchMutationIdentity, digest string) (*sql.Tx, []byte, bool, error) {
	if s == nil || s.db == nil {
		return nil, nil, false, researchUnavailable("store is not open", nil)
	}
	if err := reconcileTerminalResearchOwners(ctx, s); err != nil {
		return nil, nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, false, researchUnavailable("cannot begin research transaction", err)
	}
	var priorDigest, result string
	err = tx.QueryRowContext(ctx, `SELECT canonical_digest,result_event_ids FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, identity.PrincipalRef, identity.Tool, identity.OperationKind, identity.IdempotencyKey).Scan(&priorDigest, &result)
	if err == nil {
		if priorDigest != digest {
			_ = tx.Rollback()
			return nil, nil, false, newFailure(KindIdempotencyConflict, "research_mutation", "idempotency key was used for a different request", false, "use a new idempotency key")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, time.Now().UTC().Format(time.RFC3339Nano), identity.PrincipalRef, identity.Tool, identity.OperationKind, identity.IdempotencyKey); err != nil {
			_ = tx.Rollback()
			return nil, nil, false, researchUnavailable("cannot record idempotent replay", err)
		}
		return tx, []byte(result), true, nil
	}
	if err != sql.ErrNoRows {
		_ = tx.Rollback()
		return nil, nil, false, researchUnavailable("cannot inspect idempotency record", err)
	}
	return tx, nil, false, nil
}

func finishResearchMutation(ctx context.Context, tx *sql.Tx, identity ResearchMutationIdentity, digest string, result researchResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return researchUnavailable("cannot encode research result", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,replayed_count,first_observed_at,last_observed_at) VALUES(?,?,?,?,?,?,?,0,?,?)`, identity.PrincipalRef, identity.Tool, identity.OperationKind, identity.IdempotencyKey, digest, newResearchID("op"), string(data), now, now)
	if err != nil {
		return researchConstraint("cannot record research idempotency identity", err)
	}
	return nil
}

func normalizeRevision(input ResearchRevisionInput) (ResearchRevisionInput, error) {
	if input.Question == "" || input.Method == "" {
		return input, researchInvalid("revision question and method are required")
	}
	var err error
	if input.ScopeIn, err = canonicalJSON(input.ScopeIn); err != nil {
		return input, researchInvalid("scope_in must be valid JSON")
	}
	if input.ScopeOut, err = canonicalJSON(input.ScopeOut); err != nil {
		return input, researchInvalid("scope_out must be valid JSON")
	}
	if input.DoneWhen, err = canonicalJSON(input.DoneWhen); err != nil {
		return input, researchInvalid("done_when must be valid JSON")
	}
	return input, nil
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty json")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func validateFinding(f ResearchFinding) error {
	if f.FindingID == "" || f.Statement == "" || !validFindingKind(f.Kind) || !validResearchConfidence(f.Confidence) || !validResearchFreshness(f.Freshness) || !validFindingStatus(f.Status) {
		return researchInvalid("finding fields are invalid")
	}
	return nil
}
func validateSource(s ResearchSource) error {
	if s.SourceID == "" || s.Locator == "" || s.Title == "" || s.PublisherOrAuthor == "" || s.AccessedAt == "" || !validSourceKind(s.Kind) {
		return researchInvalid("source fields are invalid")
	}
	return nil
}
func validFindingKind(v ResearchFindingKind) bool {
	switch v {
	case FindingObservation, FindingInference, FindingHypothesis, FindingConclusion, FindingRecommendation:
		return true
	}
	return false
}
func validResearchConfidence(v ResearchConfidence) bool {
	return v == ConfidenceLow || v == ConfidenceMedium || v == ConfidenceHigh
}
func validFindingStatus(v ResearchFindingStatus) bool {
	return v == FindingActive || v == FindingContradicted || v == FindingSuperseded
}
func validSourceKind(v ResearchSourceKind) bool {
	switch v {
	case SourceOfficialDoc, SourceCode, SourceIssue, SourcePaper, SourceWeb, SourceLocalEvidence:
		return true
	}
	return false
}
func validResearchUseRole(v ResearchUseRole) bool {
	return v == UseContext || v == UseDesignInput || v == UseVerificationBasis || v == UseDecisionBasis
}
func validResearchFreshness(v ResearchFreshness) bool {
	return v == ResearchCurrent || v == ResearchStale || v == ResearchUnknown
}

func lockResearchPack(ctx context.Context, tx *sql.Tx, id string, expected int64) (ResearchPack, error) {
	var p ResearchPack
	err := tx.QueryRowContext(ctx, `SELECT pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at FROM active_research_packs WHERE pack_id=?`, id).Scan(&p.PackID, &p.OwnerWorkID, &p.CurrentRevision, &p.Freshness, &p.ExpectedVersion, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return p, researchNotFound("research pack does not exist")
	}
	if err != nil {
		return p, researchUnavailable("cannot read research pack", err)
	}
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, p.OwnerWorkID).Scan(&lifecycle); err == nil && isTerminalLifecycle(lifecycle) {
		var linked int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM archived_work WHERE id=?)`, p.OwnerWorkID).Scan(&linked); err != nil {
			return p, researchUnavailable("cannot inspect research compaction linkage", err)
		}
		if linked != 0 {
			var blocked string
			err := tx.QueryRowContext(ctx, `SELECT c.consumer_work_id FROM active_research_consumers c JOIN work_items w ON w.id=c.consumer_work_id WHERE c.pack_id=? AND c.required=1 AND w.lifecycle NOT IN ('completed','cancelled','superseded') LIMIT 1`, id).Scan(&blocked)
			if err == nil {
				return p, newFailure(KindResearchConsumerBlocked, "research_mutation", "required active consumer remains bound: "+blocked, false, "unbind, rebind, or terminalize every required active consumer")
			}
			if err != sql.ErrNoRows {
				return p, researchUnavailable("cannot inspect linked research consumers", err)
			}
		}
	} else if err != sql.ErrNoRows && err != nil {
		return p, researchUnavailable("cannot inspect research owner lifecycle", err)
	}
	if p.ExpectedVersion != expected {
		return p, newFailure(KindVersionConflict, "research_mutation", fmt.Sprintf("research pack %s has version %d, want %d", id, p.ExpectedVersion, expected), false, "reload the pack and retry with its current version")
	}
	return p, nil
}
func bumpResearchPack(ctx context.Context, tx *sql.Tx, id string, expected int64) error {
	res, err := tx.ExecContext(ctx, `UPDATE active_research_packs SET expected_version=expected_version+1,updated_at=? WHERE pack_id=? AND expected_version=?`, time.Now().UTC().Format(time.RFC3339Nano), id, expected)
	if err != nil {
		return researchUnavailable("cannot advance research pack version", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return newFailure(KindVersionConflict, "research_mutation", "research pack changed before its write", false, "reload the pack and retry")
	}
	return nil
}
func resolveResearchRevision(requested int64, p ResearchPack) (int64, error) {
	if requested == 0 {
		return p.CurrentRevision, nil
	}
	if requested < 1 {
		return 0, researchInvalid("revision must be positive")
	}
	return requested, nil
}
func ensureRevisionMutable(ctx context.Context, tx *sql.Tx, pack string, revision int64) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM active_research_revisions WHERE pack_id=? AND revision=?`, pack, revision).Scan(&n)
	if err == sql.ErrNoRows {
		return researchNotFound("research revision does not exist")
	}
	if err != nil {
		return researchUnavailable("cannot read research revision", err)
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM active_research_consumers WHERE pack_id=? AND revision=? LIMIT 1`, pack, revision).Scan(&n)
	if err == nil {
		return newFailure(KindResearchRevisionImmutable, "research_mutation", "consumed research revisions are immutable", false, "append a new revision before changing consumed content")
	}
	if err != sql.ErrNoRows {
		return researchUnavailable("cannot inspect research consumers", err)
	}
	return nil
}

// removeTerminalResearchBindings is folded in the consumer's terminal
// lifecycle transaction. Each affected pack advances once, in pack order,
// regardless of how many bindings that consumer held.
func removeTerminalResearchBindings(ctx context.Context, tx *sql.Tx, consumerWorkID string, occurredAt time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pack_id FROM active_research_consumers WHERE consumer_work_id=? ORDER BY pack_id`, consumerWorkID)
	if err != nil {
		return researchUnavailable("cannot find terminal consumer research bindings", err)
	}
	var packs []string
	for rows.Next() {
		var packID string
		if err := rows.Scan(&packID); err != nil {
			_ = rows.Close()
			return researchUnavailable("cannot decode terminal consumer research pack", err)
		}
		packs = append(packs, packID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return researchUnavailable("cannot read terminal consumer research bindings", err)
	}
	_ = rows.Close()
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_research_consumers WHERE consumer_work_id=?`, consumerWorkID); err != nil {
		return researchUnavailable("cannot remove terminal consumer research bindings", err)
	}
	when := occurredAt.UTC().Format(time.RFC3339Nano)
	for _, packID := range packs {
		if _, err := tx.ExecContext(ctx, `UPDATE active_research_packs SET expected_version=expected_version+1,updated_at=? WHERE pack_id=?`, when, packID); err != nil {
			return researchUnavailable("cannot advance affected research pack version", err)
		}
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM active_research_consumers WHERE consumer_work_id=?`, consumerWorkID).Scan(&remaining); err != nil {
		return researchUnavailable("cannot verify terminal consumer research cleanup", err)
	}
	if remaining != 0 {
		return newFailure(KindInvariantViolation, "research_mutation", "terminal consumer bindings remain after cleanup", false, "rebuild the active research consumer projection")
	}
	return nil
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func isTerminalLifecycle(v string) bool {
	return v == "completed" || v == "cancelled" || v == "superseded"
}
func newResearchID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		sum := sha256.Sum256([]byte(time.Now().UTC().String()))
		return prefix + "-" + hex.EncodeToString(sum[:12])
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
func researchInvalid(detail string) error {
	return newFailure(KindInvalidOperation, "research_mutation", detail, false, "supply valid research fields")
}
func researchReadBounded(detail string) error {
	return newFailure(KindInvalidOperation, "research_read", detail, false, "request a narrower research slice or a larger supported bound")
}
func researchNotFound(detail string) error {
	return newFailure(KindProjectionNotFound, "research_mutation", detail, false, "reload the active research context")
}
func researchConflict(detail string) error {
	return newFailure(KindProjectionConflict, "research_mutation", detail, false, "use a distinct identity or update the existing item")
}
func researchConstraint(detail string, err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		return newFailure(KindProjectionNotFound, "research_mutation", detail, false, "create the referenced work or revision first")
	}
	if isUniqueViolation(err) {
		return researchConflict(detail)
	}
	return researchUnavailable(detail, err)
}
func researchUnavailable(detail string, err error) error {
	return wrapFailure(KindUnavailable, "research_mutation", detail, true, "retry once the database is readable and writable", err)
}
