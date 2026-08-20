package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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

// canonicalJSON returns the canonical form of raw so two semantically equal
// JSON values compare byte-for-byte. The rules are:
//
//   - object keys are ordered lexicographically at every depth. Go's
//     map[string]any marshalling sorts keys, so unmarshalling into a map and
//     re-marshalling produces the canonical order;
//   - numeric literals are preserved verbatim rather than passing through
//     float64. Decoding with json.Decoder.UseNumber keeps each literal as a
//     json.Number whose MarshalJSON returns the original bytes, so two
//     payloads that differ only in a large integer beyond float64's exact
//     range stay distinct;
//   - insignificant whitespace is removed by the marshaller's default
//     compact encoding;
//   - on duplicate keys within one object, the last occurrence wins. The
//     map[string]any unmarshaller keeps only the last value, so the canonical
//     form matches what a caller that decoded and re-encoded would see.
//
// Input carrying more than one JSON value is rejected. A caller comparing
// canonical forms must not have trailing content silently discarded, because
// two inputs differing only after the first value would compare equal.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty json")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("json carries more than one value")
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
		f := newFailure(KindVersionConflict, "research_mutation", fmt.Sprintf("research pack %s has version %d, want %d", id, p.ExpectedVersion, expected), false, "reload the pack and retry with its current version")
		// Research packs do not use SubjectRef; the typed current version is
		// still surfaced via the generic SubjectCurrentVersion carrier keyed by
		// subject_type "research_pack" so higher layers do not have to regex.
		f.CurrentVersions = []SubjectCurrentVersion{{SubjectType: "research_pack", SubjectID: id, Version: p.ExpectedVersion, Exists: true}}
		return p, f
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

// researchBriefRestated reports whether an appended revision changes the research
// brief rather than merely opening a mutable successor. Consumed revisions are
// immutable, so a researcher who learns one more thing must append; that case
// leaves the brief identical and must not be treated as a new question. Scope
// bodies are canonicalized on input, so comparing them as text is exact.
func researchBriefRestated(prior ResearchRevision, next ResearchRevisionInput) bool {
	return prior.Question != next.Question ||
		prior.ScopeIn != string(next.ScopeIn) ||
		prior.ScopeOut != string(next.ScopeOut) ||
		prior.DoneWhen != string(next.DoneWhen) ||
		prior.Method != next.Method
}

// copyResearchRevisionContent carries findings, sources, and their citation links
// into a successor revision. Without it an append yields an empty revision, so the
// only legal way to add one finding to a consumed revision would discard every
// finding already gathered.
//
// When the brief is restated, copied findings degrade to unknown freshness. That
// is not staleness: nobody has yet assessed whether they survive the new question,
// and unknown is the state that says exactly that. An unchanged brief preserves
// each finding's assessed freshness, because nothing about it was reopened.
func copyResearchRevisionContent(ctx context.Context, tx *sql.Tx, pack string, from, to int64, restated bool) error {
	freshness := "freshness"
	if restated {
		freshness = "'unknown'"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode)
        SELECT pack_id,?,finding_id,kind,statement,confidence,`+freshness+`,status,scope_mode FROM active_research_findings WHERE pack_id=? AND revision=?`, to, pack, from); err != nil {
		return researchConstraint("cannot carry research findings into the new revision", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at)
        SELECT pack_id,?,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at FROM active_research_sources WHERE pack_id=? AND revision=?`, to, pack, from); err != nil {
		return researchConstraint("cannot carry research sources into the new revision", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id)
        SELECT pack_id,?,finding_id,scope_kind,scope_id FROM active_research_finding_scopes WHERE pack_id=? AND revision=?`, to, pack, from); err != nil {
		return researchConstraint("cannot carry finding scope into the new revision", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id)
        SELECT pack_id,?,finding_id,source_id FROM active_research_finding_sources WHERE pack_id=? AND revision=?`, to, pack, from); err != nil {
		return researchConstraint("cannot carry research citations into the new revision", err)
	}
	return nil
}

// validateResearchScopes mirrors the durable-knowledge scope invariant in
// knowledge_manifest.go: mode is closed, and home carries no explicit IDs. An
// empty mode defaults to home so a caller declaring nothing gets the meaning it
// intends rather than a rejected write.
func validateResearchScopes(scopes *ResearchScopes) error {
	if scopes.Mode == "" {
		scopes.Mode = "home"
	}
	if scopes.Mode != "home" && scopes.Mode != "explicit" {
		return researchInvalid("scope mode must be home or explicit")
	}
	declared := 0
	for _, pair := range scopes.byKind() {
		seen := map[string]struct{}{}
		if len(*pair.values) > 100 {
			return researchInvalid("scope ID arrays must contain at most 100 entries")
		}
		for _, id := range *pair.values {
			if id == "" || utf8.RuneCountInString(id) > 256 || strings.TrimSpace(id) != id {
				return researchInvalid("scope IDs must be non-empty, bounded, and clean")
			}
			if _, exists := seen[id]; exists {
				return researchInvalid("scope IDs must be unique within their kind")
			}
			seen[id] = struct{}{}
			declared++
		}
	}
	if scopes.Mode == "home" && declared > 0 {
		return researchInvalid("home scope cannot carry explicit scope IDs")
	}
	if scopes.Mode == "explicit" && declared == 0 {
		return researchInvalid("explicit scope must declare at least one scope ID")
	}
	return nil
}

// validateResearchScopeReferences validates the two scope kinds Concord owns as
// entities. Components and tags intentionally remain opaque declared identifiers:
// durable knowledge uses the same vocabulary, and neither has a canonical entity
// registry to join yet. Treating those as unknown would be an unvalidated join.
func validateResearchScopeReferences(ctx context.Context, tx *sql.Tx, scopes ResearchScopes) error {
	for _, ref := range []struct {
		table string
		ids   []string
	}{
		{"products", scopes.ProductIDs},
		{"projects", scopes.ProjectIDs},
	} {
		for _, id := range ref.ids {
			var found int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+ref.table+` WHERE id=?`, id).Scan(&found)
			if err == sql.ErrNoRows {
				return researchInvalid(ref.table[:len(ref.table)-1] + " scope ID does not exist")
			}
			if err != nil {
				return researchUnavailable("cannot validate scope reference", err)
			}
		}
	}
	return nil
}

// writeResearchFindingScopes replaces a finding's declared scope wholesale, so an
// update cannot leave behind a scope the caller no longer claims.
func writeResearchFindingScopes(ctx context.Context, tx *sql.Tx, pack string, revision int64, findingID string, scopes ResearchScopes) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_research_finding_scopes WHERE pack_id=? AND revision=? AND finding_id=?`, pack, revision, findingID); err != nil {
		return researchUnavailable("cannot clear finding scope", err)
	}
	for _, pair := range scopes.byKind() {
		for _, id := range *pair.values {
			if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES(?,?,?,?,?)`, pack, revision, findingID, pair.kind, id); err != nil {
				return researchConstraint("cannot declare finding scope", err)
			}
		}
	}
	return nil
}

// readResearchFindingScopes loads the declared scope for one finding.
func readResearchFindingScopes(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, pack string, revision int64, findingID, mode string) (ResearchScopes, error) {
	scopes := ResearchScopes{Mode: mode}
	rows, err := q.QueryContext(ctx, `SELECT scope_kind, scope_id FROM active_research_finding_scopes WHERE pack_id=? AND revision=? AND finding_id=? ORDER BY scope_kind, scope_id`, pack, revision, findingID)
	if err != nil {
		return scopes, researchUnavailable("cannot read finding scope", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return scopes, researchUnavailable("cannot scan finding scope", err)
		}
		for _, pair := range scopes.byKind() {
			if pair.kind == kind {
				*pair.values = append(*pair.values, id)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return scopes, researchUnavailable("cannot read finding scope", err)
	}
	return scopes, nil
}
