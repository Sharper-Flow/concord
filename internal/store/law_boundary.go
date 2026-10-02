package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

type LawConflict struct {
	SourceLawID string `json:"source_law_id"`
	TargetLawID string `json:"target_law_id"`
}

// QueryLawConflictsAtHome reads only the derived projection for one resolved
// Git home. It is intentionally bounded and cannot create rows or events.
func (s *Store) QueryLawConflictsAtHome(ctx context.Context, homeProjectID, homeLocatorID string, lawIDs []string) ([]LawConflict, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "query_law_conflicts", "store is not open", false, "open a store before checking law conflicts")
	}
	return queryLawConflictsAtHome(ctx, s.db, homeProjectID, homeLocatorID, lawIDs)
}

func queryLawConflictsAtHome(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, lawIDs []string) ([]LawConflict, error) {
	if len(lawIDs) > 32 {
		return nil, newFailure(KindInvalidPayload, "query_law_conflicts", "law conflict query exceeds the bounded list size", false, "supply at most 32 law IDs")
	}
	if len(lawIDs) == 0 {
		return []LawConflict{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(lawIDs)), ",")
	rows, err := q.QueryContext(ctx, `SELECT source_law_id,target_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(append([]any{homeProjectID, homeLocatorID}, stringArgs(lawIDs)...), stringArgs(lawIDs)...)...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "query_law_conflicts", "cannot read derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	defer rows.Close()
	conflicts := make([]LawConflict, 0)
	for rows.Next() {
		if len(conflicts) == 32 {
			_ = rows.Close()
			return nil, newFailure(KindInvalidPayload, "query_law_conflicts", "derived law conflict query exceeds the bounded result size", false, "reduce the law set or resolve conflicts before retrying")
		}
		var conflict LawConflict
		if err := rows.Scan(&conflict.SourceLawID, &conflict.TargetLawID); err != nil {
			return nil, wrapFailure(KindUnavailable, "query_law_conflicts", "cannot decode a derived law conflict", true, "retry once the knowledge projection is readable", err)
		}
		conflicts = append(conflicts, conflict)
	}
	return conflicts, rows.Err()
}

// CheckMandatedLawsAtHome performs the planning/completion law boundary check
// against an already resolved canonical home. Planning may use the explicit
// amendment path; completion never does.
func (s *Store) CheckMandatedLawsAtHome(ctx context.Context, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "check_mandated_laws", "store is not open", false, "open a store before checking mandated laws")
	}
	// CD-0200: a consequential boundary check over a registered source set
	// verifies every source's watermark before it reads the projections, so
	// stale source rows cannot pass as fresh law. The git probe must never
	// run inside an open transaction (CD-0195 D2), so the verification lives
	// on this read-handle entry; the transaction-scoped paths keep their
	// rebuild-time guarantees.
	if err := s.verifyKnowledgeSourceSetFreshness(ctx, homeProjectID, homeLocatorID); err != nil {
		return err
	}
	return checkMandatedLawsQuery(ctx, s.db, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

// knowledgeSourceSetProof is the record a pre-transaction source-set
// verification leaves behind. A git probe can never run inside an open
// transaction (CD-0195 D2) and the store pools one connection, so the
// consequential law boundary proves its freshness with this value in the
// call's context instead of re-probing: the transaction-scoped check refuses
// a mandated-law read over a registered source set that carries no proof,
// and it re-validates the proof against the set and the per-source verified
// revisions it records. A source whose head moved, or whose projection moved
// off the verified revision, between the verification and the transaction
// invalidates the proof.
type knowledgeSourceSetProof struct {
	productID string
	sources   []KnowledgeHome
	revisions map[string]string
}

type knowledgeSourceSetProofKey struct{}

func withKnowledgeSourceSetProof(ctx context.Context, proof *knowledgeSourceSetProof) context.Context {
	return context.WithValue(ctx, knowledgeSourceSetProofKey{}, proof)
}

func knowledgeSourceSetProofFrom(ctx context.Context) *knowledgeSourceSetProof {
	proof, _ := ctx.Value(knowledgeSourceSetProofKey{}).(*knowledgeSourceSetProof)
	return proof
}

// EstablishKnowledgeSourceSetProof freshens, rebuilds, and verifies every
// registered knowledge source of the Product the work's canonical law home
// belongs to, and returns the context that proves the verification to the
// transaction-scoped law boundary entries. It must run before the
// consequential transaction opens: a rebuild needs the connection the
// transaction would hold, and a git probe must never run inside one
// (CD-0195 D2). A one-source Product, and a work that resolves to no
// registered source set, verify trivially and return the context unchanged,
// so single-source Products keep their identical code path.
func (s *Store) EstablishKnowledgeSourceSetProof(ctx context.Context, workID string) (context.Context, error) {
	if s == nil || s.db == nil {
		return ctx, newFailure(KindUnavailable, "check_mandated_laws", "store is not open", false, "open a store before verifying mandated laws")
	}
	// A work whose law home does not resolve, and a Product with no unique
	// designated home or no registered source set, verify trivially here and
	// keep their refusal at the boundary clause that owns it — never ahead of
	// the gate's ordered clauses.
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, s.db, workID)
	if err != nil {
		return ctx, nil
	}
	productID, _, err := resolveKnowledgeSourceRole(ctx, s.db, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil || productID == "" {
		return ctx, nil
	}
	sources, err := resolveKnowledgeQuerySources(ctx, s.db, productID, "check_mandated_laws")
	if err != nil || len(sources) <= 1 {
		return ctx, nil
	}
	revisions := make(map[string]string, len(sources))
	for _, source := range sources {
		// Freshen first: a stale source rebuilds from its own head, and only
		// then does the strict verification decide. An unreachable source
		// refuses the consequential transaction outright — a law boundary has
		// no degraded form (CD-0200 D3).
		if err := s.EnsureKnowledgeIndexFresh(ctx, source); err != nil {
			return ctx, err
		}
		verified, _, err := validateKnowledgeHomeForQueryCore(ctx, s.db, source, false, "check_mandated_laws")
		if err != nil {
			return ctx, err
		}
		revisions[source.HomeProjectID+"/"+source.HomeLocatorID] = verified
	}
	return withKnowledgeSourceSetProof(ctx, &knowledgeSourceSetProof{productID: productID, sources: sources, revisions: revisions}), nil
}

// requireKnowledgeSourceSetProof refuses a transaction-scoped mandated-law
// read over a registered source set that carries no proof, or a proof that no
// longer matches the transaction's facts: the source set digest, the
// projection watermark each verified revision was read from, and each
// source's git head. The head re-read uses only small file reads
// (resolveKnowledgeHeadCheap), so the re-validation stays inside CD-0195 D2.
// The refusal is the completion gate's answer when the caller skipped the
// pre-transaction verification, or when a source changed after it.
func requireKnowledgeSourceSetProof(ctx context.Context, q queryer, homeProjectID, homeLocatorID string) error {
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil || productID == "" {
		return err
	}
	sources, err := resolveKnowledgeQuerySources(ctx, q, productID, "check_mandated_laws")
	if err != nil || len(sources) <= 1 {
		return err
	}
	proof := knowledgeSourceSetProofFrom(ctx)
	if proof == nil {
		return newFailure(KindInvalidOperation, "check_mandated_laws", "the registered knowledge source set was not verified before this transaction", false, "verify the Product's registered sources before the consequential transaction opens")
	}
	if proof.productID != productID || knowledgeSourceSetDigest(proof.sources) != knowledgeSourceSetDigest(sources) {
		return newFailure(KindInvalidOperation, "check_mandated_laws", "the registered knowledge source set changed after its verification", false, "verify the Product's registered sources again before the consequential transaction opens")
	}
	for _, source := range proof.sources {
		key := source.HomeProjectID + "/" + source.HomeLocatorID
		revision := proof.revisions[key]
		scanned, err := knowledgeIndexWatermark(ctx, q, source.HomeProjectID, source.HomeLocatorID, source.HeadRef)
		if err != nil {
			return err
		}
		if scanned != revision {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "the verified knowledge revision of registered source "+key+" is no longer the projection this transaction reads", false, "verify the Product's registered sources again before the consequential transaction opens")
		}
		current, headErr := resolveKnowledgeHeadCheap(source.RepoPath, source.HeadRef)
		if headErr != nil {
			return wrapFailure(KindInvalidOperation, "check_mandated_laws", "the git head of registered source "+key+" cannot be re-validated inside the transaction", false, "verify the Product's registered sources again before the consequential transaction opens", headErr)
		}
		if current != revision {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "registered source "+key+" moved to a new commit after its verification", false, "verify the Product's registered sources again before the consequential transaction opens")
		}
	}
	return nil
}

// verifyKnowledgeSourceSetFreshness refuses when any registered source of the
// Product the home belongs to is unreachable or carries a stale watermark.
// A home outside every source set, and a one-element set, verify trivially.
func (s *Store) verifyKnowledgeSourceSetFreshness(ctx context.Context, homeProjectID, homeLocatorID string) error {
	productID, _, err := resolveKnowledgeSourceRole(ctx, s.db, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil || productID == "" {
		return err
	}
	sources, err := resolveKnowledgeQuerySources(ctx, s.db, productID, "check_mandated_laws")
	if err != nil || len(sources) <= 1 {
		return err
	}
	for _, source := range sources {
		if _, _, err := validateKnowledgeHomeForQueryCore(ctx, s.db, source, false, "check_mandated_laws"); err != nil {
			return err
		}
	}
	return nil
}

func checkMandatedLawsTx(ctx context.Context, tx *sql.Tx, workID string, mandated, modified []string, allowAmendment bool) error {
	if len(mandated) == 0 {
		return validateLawModificationSubset(mandated, modified)
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	return checkMandatedLawsTxAtHome(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

func checkMandatedLawsTxAtHome(ctx context.Context, tx *sql.Tx, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	if tx == nil {
		return newFailure(KindUnavailable, "check_mandated_laws", "transaction is not open", false, "open a mutation transaction")
	}
	if err := requireKnowledgeSourceSetProof(ctx, tx, homeProjectID, homeLocatorID); err != nil {
		return err
	}
	return checkMandatedLawsQuery(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

func checkMandatedLawsQuery(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	if homeProjectID == "" || homeLocatorID == "" {
		return newFailure(KindUnknownScope, "check_mandated_laws", "canonical law home is incomplete", false, "resolve one canonical Git knowledge home")
	}
	if len(mandated) > 32 || len(modified) > 32 {
		return newFailure(KindInvalidPayload, "check_mandated_laws", "law mandate exceeds the bounded list size", false, "supply at most 32 law IDs")
	}
	if err := validateLawModificationSubset(mandated, modified); err != nil {
		return err
	}
	if len(mandated) == 0 {
		return nil
	}
	// CD-0200: a mandated law resolves across the Product's registered
	// source set, by bare ID (unique across the set) or by the qualified
	// project_id/law_id form (through its named source). A one-element set
	// with only bare IDs keeps the single-home path below unchanged.
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return err
	}
	anyQualified := false
	for _, id := range mandated {
		if _, _, qualified, parseErr := parseQualifiedKnowledgeID("check_mandated_laws", id); parseErr != nil {
			return parseErr
		} else if qualified {
			anyQualified = true
		}
	}
	var sources []KnowledgeHome
	if productID != "" {
		resolved, err := resolveKnowledgeQuerySources(ctx, q, productID, "check_mandated_laws")
		if err != nil {
			return err
		}
		sources = resolved
		if len(sources) > 1 || anyQualified {
			return checkMandatedLawsAcrossSources(ctx, q, sources, mandated, modified, allowAmendment)
		}
	} else if anyQualified {
		// A qualified reference outside every registered source set resolves
		// through the named Project's own canonical locator, beside the
		// resolved home the boundary was called with.
		sources := []KnowledgeHome{{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}}
		seenProjects := map[string]bool{homeProjectID: true}
		for _, id := range mandated {
			projectID, _, qualified, parseErr := parseQualifiedKnowledgeID("check_mandated_laws", id)
			if parseErr != nil {
				return parseErr
			}
			if !qualified || seenProjects[projectID] {
				continue
			}
			candidates, err := projectCanonicalHomeCandidates(ctx, q, projectID)
			if err != nil {
				return err
			}
			if len(candidates) == 0 {
				failure := newFailure(KindProjectionNotFound, "check_mandated_laws", "qualified mandate names a Project with no canonical-path knowledge locator: "+projectID, false, "designate the Project's canonical-path locator before mandating through it")
				failure.CandidateIDs = []string{id}
				return failure
			}
			seenProjects[projectID] = true
			sources = append(sources, candidates[0])
		}
		return checkMandatedLawsAcrossSources(ctx, q, sources, mandated, modified, allowAmendment)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(mandated)), ",")
	args := []any{homeProjectID, homeLocatorID}
	for _, id := range mandated {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT law_id,status,authority_tier FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id IN (`+placeholders+`) LIMIT 33`, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the derived law subjects", true, "retry once the knowledge projection is readable", err)
	}
	accepted := map[string]bool{}
	authority := map[string]string{}
	for rows.Next() {
		if len(accepted) == 32 {
			_ = rows.Close()
			return newFailure(KindInvalidPayload, "check_mandated_laws", "derived law subject query exceeds the bounded result size", false, "reduce the law mandate before retrying")
		}
		var id, status, tier string
		if err := rows.Scan(&id, &status, &tier); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law subject", true, "retry once the knowledge projection is readable", err)
		}
		if status == "accepted" {
			accepted[id] = true
		}
		authority[id] = tier
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law subjects", true, "retry once the knowledge projection is readable", err)
	}
	if err := rows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law subjects", true, "retry once the knowledge projection is readable", err)
	}
	missing := make([]string, 0)
	for _, id := range mandated {
		if !accepted[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) != 0 {
		failure := newFailure(KindProjectionNotFound, "check_mandated_laws", "a mandated law is unknown or not currently accepted: "+strings.Join(missing, ","), false, "publish and rebuild the accepted Git law projection")
		failure.CandidateIDs = missing
		return failure
	}
	conflictRows, err := q.QueryContext(ctx, `SELECT source_law_id,target_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(append([]any{homeProjectID, homeLocatorID}, stringArgs(mandated)...), stringArgs(mandated)...)...)
	if err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	modifiedSet := make(map[string]bool, len(modified))
	for _, id := range modified {
		modifiedSet[id] = true
	}
	conflicts := 0
	for conflictRows.Next() {
		if conflicts == 32 {
			_ = conflictRows.Close()
			return newFailure(KindInvalidPayload, "check_mandated_laws", "derived law conflict query exceeds the bounded result size", false, "reduce the law set or resolve conflicts before retrying")
		}
		var source, target string
		if err := conflictRows.Scan(&source, &target); err != nil {
			conflictRows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law conflict", true, "retry once the knowledge projection is readable", err)
		}
		conflicts++
		// The tier gates the law the contract revises, not the law it leaves
		// alone. A contract that brings a derived record into conformance with
		// an untouched legislated commitment resolves the conflict without
		// changing anything the operator legislated, so it carries a contract
		// revision line. Revising a legislated endpoint still meets the
		// refusal that sends the conflict to an operator checkpoint.
		revisesLegislated := (modifiedSet[source] && authority[source] != "derived") ||
			(modifiedSet[target] && authority[target] != "derived")
		if !allowAmendment || (!modifiedSet[source] && !modifiedSet[target]) || revisesLegislated {
			_ = conflictRows.Close()
			return newFailure(KindRelationConflict, "check_mandated_laws", fmt.Sprintf("mandated laws have an unresolved explicit conflict: %s and %s", source, target), false, "resolve the Git law conflict or declare and approve the amendment path")
		}
	}
	if err := conflictRows.Err(); err != nil {
		conflictRows.Close()
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	if err := conflictRows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	// CD-0200 D5: the same-home conflict rows are not the whole boundary. A
	// mandated law may declare, or be targeted by, a persisted cross-source
	// relation, and the endpoints must still resolve over the verified
	// current source set.
	if len(sources) > 0 {
		return revalidateKnowledgeCrossSourceRelations(ctx, q, sources, mandated)
	}
	return nil
}

// revalidateKnowledgeCrossSourceRelations refuses a mandated law whose
// persisted cross-source relation endpoints no longer resolve over the
// verified current source set (CD-0200 D5): a conflicts_with pair, a target
// source or endpoint law that left the set or its projection, or a non-home
// source declaring supersedes, refines, or subordinate_to toward shared-home
// law. The declaring home keeps the only row, so the scan runs over the
// verified set's partitions and both edge directions refuse.
func revalidateKnowledgeCrossSourceRelations(ctx context.Context, q queryer, sources []KnowledgeHome, lawIDs []string) error {
	if len(lawIDs) == 0 {
		return nil
	}
	lawPlaceholders := strings.TrimRight(strings.Repeat("?,", len(lawIDs)), ",")
	pairFilters := strings.TrimSuffix(strings.Repeat("(home_project_id=? AND home_locator_id=?) OR ", len(sources)), " OR ")
	args := []any{}
	args = append(args, stringArgs(lawIDs)...)
	args = append(args, stringArgs(lawIDs)...)
	for _, source := range sources {
		args = append(args, source.HomeProjectID, source.HomeLocatorID)
	}
	rows, err := q.QueryContext(ctx, `SELECT home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id FROM law_cross_source_relations WHERE (source_law_id IN (`+lawPlaceholders+`) OR target_law_id IN (`+lawPlaceholders+`)) AND (`+pairFilters+`) ORDER BY home_project_id,home_locator_id,source_law_id,kind,target_project_id,target_law_id LIMIT 33`, args...) //nolint:gosec // the fragments contain only generated question-mark placeholders and every identifier stays parameter-bound.
	if err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read derived cross-source law relations", true, "retry once the knowledge projection is readable", err)
	}
	// The pool holds one connection, so every row is read and the cursor is
	// closed before any endpoint query runs: a query beside an open result
	// would park on the pool forever.
	type crossSourceEdge struct {
		declaringHome, declaringLocator, sourceLaw, kind, targetProject, targetLaw string
	}
	edges := make([]crossSourceEdge, 0, 8)
	for rows.Next() {
		var edge crossSourceEdge
		if err := rows.Scan(&edge.declaringHome, &edge.declaringLocator, &edge.sourceLaw, &edge.kind, &edge.targetProject, &edge.targetLaw); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived cross-source law relation", true, "retry once the knowledge projection is readable", err)
		}
		if len(edges) == 32 {
			rows.Close()
			return newFailure(KindInvalidPayload, "check_mandated_laws", "derived cross-source relation query exceeds the bounded result size", false, "reduce the law set or resolve the relations before retrying")
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived cross-source law relations", true, "retry once the knowledge projection is readable", err)
	}
	if err := rows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived cross-source law relations", true, "retry once the knowledge projection is readable", err)
	}
	sourceByProject := make(map[string]KnowledgeHome, len(sources))
	for _, source := range sources {
		sourceByProject[source.HomeProjectID] = source
	}
	declaredRole := map[string]bool{}
	for _, edge := range edges {
		declaring := KnowledgeHome{HomeProjectID: edge.declaringHome, HomeLocatorID: edge.declaringLocator}
		if edge.kind == "conflicts_with" {
			return newFailure(KindRelationConflict, "check_mandated_laws", fmt.Sprintf("mandated laws have an unresolved cross-source conflict: %s and %s/%s", edge.sourceLaw, edge.targetProject, edge.targetLaw), false, "resolve the Git law conflict through an accepted amendment before the consequential boundary")
		}
		var declaredPresent int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, edge.declaringHome, edge.declaringLocator, edge.sourceLaw).Scan(&declaredPresent); err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "check_mandated_laws", "cross-source relation declaring law is unresolved: "+edge.sourceLaw, false, "publish and rebuild the declaring source before the consequential boundary")
		} else if err != nil {
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the cross-source declaring law", true, "retry once the knowledge projection is readable", err)
		}
		target, known := sourceByProject[edge.targetProject]
		if !known {
			return newFailure(KindProjectionNotFound, "check_mandated_laws", "cross-source relation target source left the registered source set: "+edge.targetProject+"/"+edge.targetLaw, false, "register the target Project again, or remove the relation from the declaring manifest")
		}
		var targetPresent int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, target.HomeProjectID, target.HomeLocatorID, edge.targetLaw).Scan(&targetPresent); err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "check_mandated_laws", "cross-source relation target law is unresolved: "+edge.targetProject+"/"+edge.targetLaw, false, "publish and rebuild the target source before the consequential boundary")
		} else if err != nil {
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the cross-source target law", true, "retry once the knowledge projection is readable", err)
		}
		declaringKey := edge.declaringHome + "/" + edge.declaringLocator
		if _, seen := declaredRole[declaringKey]; !seen {
			_, designated, roleErr := resolveKnowledgeSourceRole(ctx, q, declaring)
			if roleErr != nil {
				return roleErr
			}
			declaredRole[declaringKey] = !designated
		}
		if declaredRole[declaringKey] {
			targetProductID, targetDesignated, roleErr := resolveKnowledgeSourceRole(ctx, q, target)
			if roleErr != nil {
				return roleErr
			}
			if targetDesignated && targetProductID != "" {
				var sharedLocator string
				if err := q.QueryRowContext(ctx, `SELECT locator_id FROM product_knowledge_homes WHERE product_id=?`, targetProductID).Scan(&sharedLocator); err == nil && sharedLocator == target.HomeLocatorID && lawRelationKinds[edge.kind] && edge.kind != "conflicts_with" {
					return newFailure(KindRelationConflict, "check_mandated_laws", fmt.Sprintf("a non-home source may not declare %s toward shared-home law: %s/%s", edge.kind, edge.targetProject, edge.targetLaw), false, "amend the shared law through its authoring home, or remove the precedence declaration")
				}
			}
		}
	}
	return nil
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

// validateDerivedLawModification holds the tier-scoped half of the
// Product-truth boundary that CD-0041 D5 states and the CON-336 decision
// (obs:fb9642cbd1700ec6) resolved: a contract that does not change Product
// truth may revise derived law in-contract, and nothing else. It reads each
// modified law's authority_tier from the accepted Git law projection through
// the work's canonical law home, so the approve path and the fold answer with
// one query and cannot drift. A missing name or a non-derived tier is a
// refusal; the caller's recovery hint names the Product-changing route.
func validateDerivedLawModification(ctx context.Context, q queryer, workID string, modified []string) error {
	if len(modified) == 0 {
		return nil
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		return err
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(modified)), ",")
	args := []any{homeProjectID, homeLocatorID}
	for _, id := range modified {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT law_id,authority_tier FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id IN (`+placeholders+`) LIMIT 33`, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "check_derived_law_modification", "cannot read the law authority tiers", true, "retry once the law projection is readable", err)
	}
	tiers := map[string]string{}
	for rows.Next() {
		var id, tier string
		if err := rows.Scan(&id, &tier); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "check_derived_law_modification", "cannot decode a law authority tier", true, "retry once the law projection is readable", err)
		}
		tiers[id] = tier
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "check_derived_law_modification", "cannot finish reading law authority tiers", true, "retry once the law projection is readable", err)
	}
	if err := rows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "check_derived_law_modification", "cannot finish reading law authority tiers", true, "retry once the law projection is readable", err)
	}
	nonDerived := make([]string, 0)
	for _, id := range modified {
		tier, ok := tiers[id]
		if !ok {
			return newFailure(KindProjectionNotFound, "check_derived_law_modification", "a modified law is unknown to the accepted Git law projection: "+id, false, "publish and rebuild the accepted Git law projection")
		}
		if tier != "derived" {
			nonDerived = append(nonDerived, id)
		}
	}
	if len(nonDerived) != 0 {
		return newFailure(KindInvalidPayload, "check_derived_law_modification", "a contract that does not change Product truth may revise only derived law: "+strings.Join(nonDerived, ","), false, "leave law_modifies empty or select a Product-changing workflow")
	}
	return nil
}

func validateLawModificationSubset(mandated, modified []string) error {
	mandate := make(map[string]bool, len(mandated))
	for _, id := range mandated {
		if id == "" || mandate[id] {
			return newFailure(KindInvalidPayload, "check_mandated_laws", "law mandate contains an empty or duplicate ID", false, "supply a unique bounded law mandate")
		}
		mandate[id] = true
	}
	seen := map[string]bool{}
	for _, id := range modified {
		if id == "" || seen[id] || !mandate[id] {
			return newFailure(KindInvalidPayload, "check_mandated_laws", "law_modifies must be a subset of spec_mandate", false, "declare every modified law in spec_mandate")
		}
		seen[id] = true
	}
	return nil
}

// checkMandatedLawsAcrossSources is the federated mandated-law boundary check
// (CD-0200). Each mandated reference — a bare ID, or the qualified
// project_id/law_id form — must resolve to exactly one accepted law subject
// over the verified source set: a bare ID held by two sources refuses as
// ambiguous instead of picking one, and a qualified reference resolves only
// through its named source. Conflicts read per source over the bare law IDs
// that resolved there, because a cross-source conflicts_with pair refuses at
// the declaring source's rebuild and can never project.
func checkMandatedLawsAcrossSources(ctx context.Context, q queryer, sources []KnowledgeHome, mandated, modified []string, allowAmendment bool) error {
	type resolution struct {
		source KnowledgeHome
		status string
		tier   string
		lawID  string
	}
	resolved := make(map[string]resolution, len(mandated))
	missing := make([]string, 0)
	for _, reference := range mandated {
		projectID, lawID, qualified, err := parseQualifiedKnowledgeID("check_mandated_laws", reference)
		if err != nil {
			return err
		}
		var match *resolution
		for _, source := range sources {
			if qualified && source.HomeProjectID != projectID {
				continue
			}
			var status, tier string
			scanErr := q.QueryRowContext(ctx, `SELECT status,authority_tier FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, source.HomeProjectID, source.HomeLocatorID, lawID).Scan(&status, &tier)
			if scanErr == sql.ErrNoRows {
				continue
			}
			if scanErr != nil {
				return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the derived law subjects of source "+source.HomeProjectID, true, "retry once the knowledge projection is readable", scanErr)
			}
			if match != nil {
				failure := newFailure(KindKnowledgeAmbiguous, "check_mandated_laws", "mandated law is held by more than one registered source: "+lawID, false, "qualify the reference as project_id/law_id or remove the duplicate law")
				failure.CandidateIDs = []string{source.HomeProjectID + "/" + lawID}
				return failure
			}
			match = &resolution{source: source, status: status, tier: tier, lawID: lawID}
		}
		if match == nil || match.status != "accepted" {
			missing = append(missing, reference)
		} else {
			resolved[reference] = *match
		}
	}
	if len(missing) != 0 {
		failure := newFailure(KindProjectionNotFound, "check_mandated_laws", "a mandated law is unknown or not currently accepted: "+strings.Join(missing, ","), false, "publish and rebuild the accepted Git law projection")
		failure.CandidateIDs = missing
		return failure
	}
	modifiedSet := make(map[string]bool, len(modified))
	for _, id := range modified {
		modifiedSet[id] = true
	}
	// Each source's conflict query runs over the bare law IDs that resolved
	// in that source, so a qualified mandate endpoint matches its declared
	// relation row.
	type sourceLawSet struct {
		home          KnowledgeHome
		idToReference map[string]string
	}
	perSource := make(map[string]*sourceLawSet, len(resolved))
	for reference, r := range resolved {
		key := r.source.HomeProjectID + "/" + r.source.HomeLocatorID
		set := perSource[key]
		if set == nil {
			set = &sourceLawSet{home: r.source, idToReference: map[string]string{}}
			perSource[key] = set
		}
		set.idToReference[r.lawID] = reference
	}
	for _, set := range perSource {
		lawIDs := make([]string, 0, len(set.idToReference))
		for lawID := range set.idToReference {
			lawIDs = append(lawIDs, lawID)
		}
		sort.Strings(lawIDs)
		placeholders := strings.TrimRight(strings.Repeat("?,", len(lawIDs)), ",")
		args := []any{set.home.HomeProjectID, set.home.HomeLocatorID}
		for _, id := range lawIDs {
			args = append(args, id)
		}
		conflictRows, err := q.QueryContext(ctx, `SELECT source_law_id,target_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(args, stringArgs(lawIDs)...)...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every law ID stays parameter-bound.
		if err != nil {
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read derived law conflicts", true, "retry once the knowledge projection is readable", err)
		}
		conflicts := 0
		for conflictRows.Next() {
			if conflicts == 32 {
				_ = conflictRows.Close()
				return newFailure(KindInvalidPayload, "check_mandated_laws", "derived law conflict query exceeds the bounded result size", false, "reduce the law set or resolve conflicts before retrying")
			}
			var sourceLaw, targetLaw string
			if err := conflictRows.Scan(&sourceLaw, &targetLaw); err != nil {
				conflictRows.Close()
				return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law conflict", true, "retry once the knowledge projection is readable", err)
			}
			sourceReference, sourceKnown := set.idToReference[sourceLaw]
			targetReference, targetKnown := set.idToReference[targetLaw]
			if !sourceKnown || !targetKnown {
				continue
			}
			conflicts++
			revisesLegislated := (modifiedSet[sourceReference] && resolved[sourceReference].tier != "derived") ||
				(modifiedSet[targetReference] && resolved[targetReference].tier != "derived")
			if !allowAmendment || (!modifiedSet[sourceReference] && !modifiedSet[targetReference]) || revisesLegislated {
				_ = conflictRows.Close()
				return newFailure(KindRelationConflict, "check_mandated_laws", fmt.Sprintf("mandated laws have an unresolved explicit conflict: %s and %s", sourceLaw, targetLaw), false, "resolve the Git law conflict or declare and approve the amendment path")
			}
		}
		if err := conflictRows.Err(); err != nil {
			conflictRows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
		}
		if err := conflictRows.Close(); err != nil {
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
		}
	}
	// CD-0200 D5: same-home conflicts are not the whole boundary here either.
	// The resolved bare IDs are what cross-source rows store, so the
	// endpoint revalidation runs over them against the verified set.
	bareIDs := make([]string, 0, len(resolved))
	seenBare := make(map[string]bool, len(resolved))
	for _, r := range resolved {
		if !seenBare[r.lawID] {
			seenBare[r.lawID] = true
			bareIDs = append(bareIDs, r.lawID)
		}
	}
	return revalidateKnowledgeCrossSourceRelations(ctx, q, sources, bareIDs)
}

// workflowLawHome resolves the workflow's canonical Git law home over whichever
// read handle the caller already holds, per the store's queryer contract. The
// law home follows the work item's primary Project membership: the primary
// Project's Product owns the contract, so a secondary membership in another
// Product must never widen the home choice or make the workflow unrecordable.
func workflowLawHome(ctx context.Context, q queryer, workID string) (string, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT ph.project_id,ph.locator_id FROM product_knowledge_homes ph JOIN product_projects pp ON pp.product_id=ph.product_id JOIN work_projects wp ON wp.project_id=pp.project_id WHERE wp.work_id=? AND wp.role='primary' ORDER BY ph.project_id,ph.locator_id`, workID)
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
	}
	defer rows.Close()
	var homes [][2]string
	for rows.Next() {
		var project, locator string
		if err := rows.Scan(&project, &locator); err != nil {
			return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
		}
		if len(homes) == 0 || homes[len(homes)-1][0] != project || homes[len(homes)-1][1] != locator {
			homes = append(homes, [2]string{project, locator})
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
	}
	if len(homes) == 1 {
		return homes[0][0], homes[0][1], nil
	}
	if len(homes) > 1 {
		candidates := make([]string, 0, len(homes))
		for _, home := range homes {
			candidates = append(candidates, home[0]+"/"+home[1])
		}
		return "", "", newAmbiguousScopeFailure("check_mandated_laws", "workflow resolves to multiple canonical Git law homes", "resolve one Product knowledge home", candidates)
	}
	var project, locator string
	err = q.QueryRowContext(ctx, `SELECT wp.project_id,pl.locator_id FROM work_projects wp JOIN project_locators pl ON pl.project_id=wp.project_id AND pl.kind='canonical_path' WHERE wp.work_id=? AND wp.role='primary'`, workID).Scan(&project, &locator)
	if err == sql.ErrNoRows {
		return "", "", newFailure(KindUnknownScope, "check_mandated_laws", "workflow has no canonical Git law home", false, "designate a Product home or primary Project locator")
	}
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the primary Git law home", true, "retry once the workflow scope is readable", err)
	}
	return project, locator, nil
}
