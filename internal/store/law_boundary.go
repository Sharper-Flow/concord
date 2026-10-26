package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// knowledgeSourceSetProof is the record a pre-transaction source-set
// verification leaves behind. A git probe can never run inside an open
// transaction (CD-0195 D2) and the store pools one connection, so the
// consequential law boundary proves its freshness with this value in the
// call's context instead of re-probing: the transaction-scoped check refuses
// a mandated-law read over a registered source set that carries no proof,
// and it re-validates the proof against the set and the per-source verified
// revisions it records. A source whose projection moved off the verified
// revision, or whose head moved past the verified head, between the
// verification and the transaction invalidates the proof.
type knowledgeSourceSetProof struct {
	productID string
	sources   []KnowledgeHome
	revisions map[string]knowledgeSourceVerifiedRevision
}

// knowledgeSourceVerifiedRevision keeps PM1 §3.3's two revisions apart. The
// scanned revision is the projection watermark the verified rows were read
// from; the verified head is the git head whose projected content the
// freshness probe matched at verification time. The head is allowed to have
// moved past the scanned revision by a commit that touches no projected
// content — that commit is itself part of the verified state, and the
// transaction admission compares each later read against the value verified
// here, not against the scanned revision alone.
type knowledgeSourceVerifiedRevision struct {
	scanned string
	head    string
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
	return s.establishKnowledgeSourceSetProofAtHome(ctx, homeProjectID, homeLocatorID)
}

func (s *Store) establishKnowledgeSourceSetProofAtHome(ctx context.Context, homeProjectID, homeLocatorID string) (context.Context, error) {
	productID, _, err := resolveKnowledgeSourceRole(ctx, s.db, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil || productID == "" {
		return ctx, nil
	}
	sources, err := resolveKnowledgeQuerySources(ctx, s.db, productID, "check_mandated_laws")
	if err != nil || len(sources) <= 1 {
		return ctx, nil
	}
	revisions := make(map[string]knowledgeSourceVerifiedRevision, len(sources))
	for _, source := range sources {
		key := source.HomeProjectID + "/" + source.HomeLocatorID
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
		// PM1 §3.3 separates the projection revision from the head: capture
		// both at verification time, so the admission below can admit a
		// content-fresh head that moved past the scanned revision and still
		// refuse a head that moved after this verification.
		verifiedHead, err := resolveKnowledgeHead(ctx, source)
		if err != nil {
			return ctx, err
		}
		// A commit that landed between the freshness probe and the head read
		// must not bind a proof the probe never verified: re-validate that
		// the captured head still freshens the same scanned revision before
		// recording it. A content commit fails the strict probe here; a
		// code-only commit rechecks clean and becomes part of the verified
		// head.
		if verifiedHead != verified {
			rechecked, _, err := validateKnowledgeHomeForQueryCore(ctx, s.db, source, false, "check_mandated_laws")
			if err != nil {
				return ctx, err
			}
			if rechecked != verified {
				return ctx, newFailure(KindInvalidOperation, "check_mandated_laws", "registered source "+key+" moved while its verification ran", false, "verify the Product's registered sources again before the consequential transaction opens")
			}
		}
		revisions[key] = knowledgeSourceVerifiedRevision{scanned: verified, head: verifiedHead}
	}
	return withKnowledgeSourceSetProof(ctx, &knowledgeSourceSetProof{productID: productID, sources: sources, revisions: revisions}), nil
}

// requireKnowledgeSourceSetProof refuses a transaction-scoped mandated-law
// read over a registered source set that carries no proof, or a proof that no
// longer matches the transaction's facts: the source set digest, the scanned
// projection watermark each verified revision was read from, and each
// source's git head compared against the head the verification itself
// verified (PM1 §3.3). A commit that touched no projected content before the
// verification is part of the verified head and admits the transaction; a
// source that rebuilt onto another revision, or committed again after the
// verification, invalidates the proof. The head re-read uses only small file
// reads (resolveKnowledgeHeadCheap), so the re-validation stays inside
// CD-0195 D2. The refusal is the completion gate's answer when the caller
// skipped the pre-transaction verification, or when a source changed after
// it.
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
	current := make(map[string]KnowledgeHome, len(sources))
	for _, source := range sources {
		current[source.HomeProjectID+"/"+source.HomeLocatorID] = source
	}
	for _, source := range proof.sources {
		key := source.HomeProjectID + "/" + source.HomeLocatorID
		// The proof binds each source's resolved canonical location with its
		// identity. A locator update between the verification and the
		// transaction moves the canonical authority while every verified
		// revision still matches the old repository's partition, so the
		// admission revalidates the resolved location before it trusts the
		// recorded revisions.
		if live, ok := current[key]; !ok || live.RepoPath != source.RepoPath || live.HeadRef != source.HeadRef {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "registered source "+key+" resolved to a different canonical location after its verification", false, "verify the Product's registered sources again before the consequential transaction opens")
		}
		verified := proof.revisions[key]
		scanned, err := knowledgeIndexWatermark(ctx, q, source.HomeProjectID, source.HomeLocatorID, source.HeadRef)
		if err != nil {
			return err
		}
		if scanned != verified.scanned {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "the verified knowledge revision of registered source "+key+" is no longer the projection this transaction reads", false, "verify the Product's registered sources again before the consequential transaction opens")
		}
		current, headErr := resolveKnowledgeHeadCheap(source.RepoPath, source.HeadRef)
		if headErr != nil {
			return wrapFailure(KindInvalidOperation, "check_mandated_laws", "the git head of registered source "+key+" cannot be re-validated inside the transaction", false, "verify the Product's registered sources again before the consequential transaction opens", headErr)
		}
		if current != verified.head {
			return newFailure(KindInvalidOperation, "check_mandated_laws", "registered source "+key+" moved to a new commit after its verification", false, "verify the Product's registered sources again before the consequential transaction opens")
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
		sources, err := qualifiedMandateSources(ctx, q, homeProjectID, homeLocatorID, mandated)
		if err != nil {
			return err
		}
		return checkMandatedLawsAcrossSources(ctx, q, sources, mandated, modified, allowAmendment)
	}
	return checkMandatedLawsAtSingleHome(ctx, q, homeProjectID, homeLocatorID, sources, mandated, modified, allowAmendment)
}

func qualifiedMandateSources(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, mandated []string) ([]KnowledgeHome, error) {
	sources := []KnowledgeHome{{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}}
	seenProjects := map[string]bool{homeProjectID: true}
	for _, id := range mandated {
		projectID, _, qualified, parseErr := parseQualifiedKnowledgeID("check_mandated_laws", id)
		if parseErr != nil {
			return nil, parseErr
		}
		if !qualified || seenProjects[projectID] {
			continue
		}
		candidates, err := projectCanonicalHomeCandidates(ctx, q, projectID)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			failure := newFailure(KindProjectionNotFound, "check_mandated_laws", "qualified mandate names a Project with no canonical-path knowledge locator: "+projectID, false, "designate the Project's canonical-path locator before mandating through it")
			failure.CandidateIDs = []string{id}
			return nil, failure
		}
		seenProjects[projectID] = true
		sources = append(sources, candidates[0])
	}
	return sources, nil
}

func checkMandatedLawsAtSingleHome(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, sources []KnowledgeHome, mandated, modified []string, allowAmendment bool) error {
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
		return mandatedLawProjectionFailure(ctx, q, []KnowledgeHome{{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}}, missing)
	}
	if err := checkSingleHomeLawConflicts(ctx, q, homeProjectID, homeLocatorID, mandated, modified, authority, allowAmendment); err != nil {
		return err
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

// law_subjects contains only accepted law. The knowledge index retains other
// manifest records, so an absent law subject alone proves neither a missing
// record nor projection lag. Diagnose from those same projected rows without
// probing Git inside the consequential transaction.
func mandatedLawProjectionFailure(ctx context.Context, q queryer, sources []KnowledgeHome, missing []string) error {
	absent := []string{}
	nonaccepted := []string{}
	for _, reference := range missing {
		projectID, lawID, qualified, err := parseQualifiedKnowledgeID("check_mandated_laws", reference)
		if err != nil {
			return err
		}
		found := false
		for _, source := range sources {
			if qualified && source.HomeProjectID != projectID {
				continue
			}
			var kind, status string
			err := q.QueryRowContext(ctx, `SELECT type,outcome_tag FROM archived_work WHERE home_project_id=? AND home_locator_id=? AND id=?`, source.HomeProjectID, source.HomeLocatorID, lawID).Scan(&kind, &status)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot inspect a mandated knowledge record", true, "retry once the knowledge projection is readable", err)
			}
			found = true
			identity := reference
			if len(sources) > 1 {
				identity = source.HomeProjectID + "/" + lawID
			}
			nonaccepted = append(nonaccepted, identity+" ("+kind+", "+status+")")
		}
		if !found {
			absent = append(absent, reference)
		}
	}
	details := []string{}
	if len(absent) > 0 {
		details = append(details, "mandated IDs have no law record in the checked Git knowledge projection: "+strings.Join(absent, ","))
	}
	if len(nonaccepted) > 0 {
		details = append(details, "mandated records are not accepted law in the checked projection: "+strings.Join(nonaccepted, ","))
	}
	failure := newFailure(KindProjectionNotFound, "check_mandated_laws", strings.Join(details, "; "), false, "verify the mandate IDs and record kinds against the knowledge manifest at the checked watermark; select accepted law records or revise the mandate through the normal contract approval route")
	failure.CandidateIDs = missing
	return failure
}

func checkSingleHomeLawConflicts(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, mandated, modified []string, authority map[string]string, allowAmendment bool) error {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(mandated)), ",")
	conflictRows, err := q.QueryContext(ctx, `SELECT source_law_id,target_law_id,declarer_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(append([]any{homeProjectID, homeLocatorID}, stringArgs(mandated)...), stringArgs(mandated)...)...)
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
		var declarer sql.NullString
		if err := conflictRows.Scan(&source, &target, &declarer); err != nil {
			conflictRows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law conflict", true, "retry once the knowledge projection is readable", err)
		}
		conflicts++
		sourceEnd := conflictEnd{lawID: source, modified: modifiedSet[source], derived: authority[source] == "derived"}
		targetEnd := conflictEnd{lawID: target, modified: modifiedSet[target], derived: authority[target] == "derived"}
		if err := explicitConflictRefusal(sourceEnd, targetEnd, declarer.String, allowAmendment); err != nil {
			_ = conflictRows.Close()
			return err
		}
	}
	if err := conflictRows.Err(); err != nil {
		conflictRows.Close()
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	if err := conflictRows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	return nil
}

type mandatedCrossSourceEdge struct {
	declaringHome, declaringLocator, sourceLaw, kind, targetProject, targetLaw string
}

// revalidateKnowledgeCrossSourceRelations refuses a mandated law whose
// persisted cross-source relation endpoints no longer resolve over the
// verified current source set (CD-0200 D5): a conflicts_with pair, a target
// source or endpoint law that left the set or its projection, or
// a non-home source declaring refines or subordinate_to toward
// shared-home law. The declaring home keeps the only row, so the scan runs
// over the verified set's partitions and both edge directions refuse. The
// persisted hierarchical graph over source-qualified nodes must also stay
// acyclic (CD-0015): the rebuild refusal and source-set freshness keep a
// cycle from persisting, and this read refuses one that somehow reached the
// rows instead of answering from it.
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
	edges := make([]mandatedCrossSourceEdge, 0, 8)
	for rows.Next() {
		var edge mandatedCrossSourceEdge
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
	if err := validateMandatedCrossSourceEdges(ctx, q, sources, edges); err != nil {
		return err
	}
	// CD-0015's acyclic invariant over source-qualified nodes also holds on
	// the persisted rows this boundary answers from: the rebuild refusal and
	// source-set freshness keep a cycle from persisting, and this read
	// refuses one that somehow reached the rows instead of answering from it.
	graph := map[string][]string{}
	if err := appendFederatedPersistedRelationEdges(ctx, q, sources, KnowledgeHome{}, graph); err != nil {
		return err
	}
	if relationGraphHasCycle(graph) {
		return newFailure(KindCycleDetected, "check_mandated_laws", "persisted law relations contain a cycle across the registered source set", false, "rebuild the registered sources and remove the cycle from the authored law graphs")
	}
	return nil
}

func validateMandatedCrossSourceEdges(ctx context.Context, q queryer, sources []KnowledgeHome, edges []mandatedCrossSourceEdge) error {
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
			if err := validateMandatedCrossSourcePrecedence(ctx, q, target, edge); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateMandatedCrossSourcePrecedence(ctx context.Context, q queryer, target KnowledgeHome, edge mandatedCrossSourceEdge) error {
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
// Product-truth boundary that CD-0222 D5 states: a contract that does not change
// Product truth may revise derived law in-contract, and nothing else. It reads each
// modified law's authority_tier from the accepted Git law projection through
// the work's canonical law home, so the approve path and the fold answer with
// one query and cannot drift. Over a registered source set the modified
// references resolve through the same source-qualified federated identity as
// the mandated-law boundary (CD-0200 D6), so a derived law a registered
// source holds is revisable by its bare or qualified reference. A missing
// name or a non-derived tier is a refusal; the caller's recovery hint names
// the Product-changing route.
func validateDerivedLawModification(ctx context.Context, q queryer, workID string, modified []string) error {
	if len(modified) == 0 {
		return nil
	}
	if len(modified) > 32 {
		return newFailure(KindInvalidPayload, "check_derived_law_modification", "law_modifies exceeds the bounded list size", false, "supply at most 32 law IDs")
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		return err
	}
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return err
	}
	anyQualified := false
	for _, id := range modified {
		if _, _, qualified, parseErr := parseQualifiedKnowledgeID("check_derived_law_modification", id); parseErr != nil {
			return parseErr
		} else if qualified {
			anyQualified = true
		}
	}
	if productID != "" {
		sources, err := resolveKnowledgeQuerySources(ctx, q, productID, "check_derived_law_modification")
		if err != nil {
			return err
		}
		if len(sources) > 1 || anyQualified {
			return validateDerivedLawModificationAcrossSources(ctx, q, sources, modified)
		}
	} else if anyQualified {
		// A qualified reference outside every registered source set resolves
		// through the named Project's own canonical locator, beside the
		// resolved home the boundary was called with.
		sources := []KnowledgeHome{{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}}
		seenProjects := map[string]bool{homeProjectID: true}
		for _, id := range modified {
			projectID, _, qualified, parseErr := parseQualifiedKnowledgeID("check_derived_law_modification", id)
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
				failure := newFailure(KindProjectionNotFound, "check_derived_law_modification", "qualified modification names a Project with no canonical-path knowledge locator: "+projectID, false, "designate the Project's canonical-path locator before modifying through it")
				failure.CandidateIDs = []string{id}
				return failure
			}
			seenProjects[projectID] = true
			sources = append(sources, candidates[0])
		}
		return validateDerivedLawModificationAcrossSources(ctx, q, sources, modified)
	}
	return validateDerivedLawModificationAtHome(ctx, q, homeProjectID, homeLocatorID, modified)
}

func validateDerivedLawModificationAtHome(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, modified []string) error {
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
	return refuseNonDerivedLawModification(modified, tiers)
}

// validateDerivedLawModificationAcrossSources resolves each modified
// reference through the shared federated identity resolver and refuses a
// non-derived tier. The tier is read from the projection of the source the
// reference resolved through, so a registered source's derived law answers
// from that source's rows.
func validateDerivedLawModificationAcrossSources(ctx context.Context, q queryer, sources []KnowledgeHome, modified []string) error {
	resolutions, missing, err := resolveFederatedLawReferences(ctx, q, "check_derived_law_modification", sources, modified)
	if err != nil {
		return err
	}
	if len(missing) != 0 {
		return newFailure(KindProjectionNotFound, "check_derived_law_modification", "a modified law is unknown to the accepted Git law projection: "+strings.Join(missing, ","), false, "publish and rebuild the accepted Git law projection")
	}
	tiers := make(map[string]string, len(modified))
	for reference, r := range resolutions {
		tiers[reference] = r.tier
	}
	return refuseNonDerivedLawModification(modified, tiers)
}

func refuseNonDerivedLawModification(modified []string, tiers map[string]string) error {
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

// federatedLawResolution is one reference's resolved law over the verified
// source set: the source that holds it, the bare law ID that resolved there,
// and the derived projection status and authority tier that resolution read.
type federatedLawResolution struct {
	source KnowledgeHome
	status string
	tier   string
	hash   string
	lawID  string
}

// resolveFederatedLawReferences resolves each reference — a bare ID, or the
// qualified project_id/law_id form (CD-0200 D4) — over the verified source
// set: a bare ID held by two sources refuses as ambiguous instead of picking
// one, and a qualified reference resolves only through its named source. A
// reference with no row in any source's projection returns in the missing
// list; each caller owns the status-dependent refusals its boundary states.
func resolveFederatedLawReferences(ctx context.Context, q queryer, op string, sources []KnowledgeHome, references []string) (map[string]federatedLawResolution, []string, error) {
	resolved := make(map[string]federatedLawResolution, len(references))
	missing := make([]string, 0)
	for _, reference := range references {
		projectID, lawID, qualified, err := parseQualifiedKnowledgeID(op, reference)
		if err != nil {
			return nil, nil, err
		}
		var match *federatedLawResolution
		for _, source := range sources {
			if qualified && source.HomeProjectID != projectID {
				continue
			}
			var status, tier, hash string
			scanErr := q.QueryRowContext(ctx, `SELECT status,authority_tier,content_hash FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, source.HomeProjectID, source.HomeLocatorID, lawID).Scan(&status, &tier, &hash)
			if scanErr == sql.ErrNoRows {
				continue
			}
			if scanErr != nil {
				return nil, nil, wrapFailure(KindUnavailable, op, "cannot read the derived law subjects of source "+source.HomeProjectID, true, "retry once the knowledge projection is readable", scanErr)
			}
			if match != nil {
				failure := newFailure(KindKnowledgeAmbiguous, op, "law reference is held by more than one registered source: "+lawID, false, "qualify the reference as project_id/law_id or remove the duplicate law")
				failure.CandidateIDs = []string{source.HomeProjectID + "/" + lawID}
				return nil, nil, failure
			}
			match = &federatedLawResolution{source: source, status: status, tier: tier, hash: hash, lawID: lawID}
		}
		if match == nil {
			missing = append(missing, reference)
		} else {
			resolved[reference] = *match
		}
	}
	return resolved, missing, nil
}

// conflictEnd is one endpoint of an explicit conflict as the amendment path
// sees it: whether the contract revises it and whether its tier is derived.
// lawID is the name law_modifies takes: the bare law ID at a single home, the
// mandate reference across sources.
type conflictEnd struct {
	lawID    string
	modified bool
	derived  bool
}

// explicitConflictRefusal decides one explicit conflict between mandated
// laws. Completion carries no amendment allowance and refuses every conflict.
// At approval only the record that declares the conflict can withdraw it, so
// the contract must revise that record (CD-0015) and may resolve it inside
// the contract only under the tier rule (CD-0159 D4). An empty declarer is a
// row an older writer projected without one: either end may have declared
// it, so the conflict passes only when it passes with either end declaring.
func explicitConflictRefusal(source, target conflictEnd, declarer string, allowAmendment bool) error {
	detail := fmt.Sprintf("mandated laws have an unresolved explicit conflict: %s and %s", source.lawID, target.lawID)
	if !allowAmendment {
		return newFailure(KindRelationConflict, "check_mandated_laws", detail, false, "resolve the Git law conflict or declare and approve the amendment path")
	}
	switch declarer {
	case source.lawID:
		return declaredConflictRefusal(detail, source, target)
	case target.lawID:
		return declaredConflictRefusal(detail, target, source)
	}
	if conflictResolvesInsideContract(source, target) && conflictResolvesInsideContract(target, source) {
		return nil
	}
	return newFailure(KindRelationConflict, "check_mandated_laws", detail+"; the knowledge projection does not record which record declared it", false, "rebuild the knowledge index so it records the declaring record, or revise both ends in law_modifies when both are derived")
}

// declaredConflictRefusal applies the tier rule to a conflict whose declaring
// record is known, and names that record when the contract cannot resolve it.
func declaredConflictRefusal(detail string, declarer, other conflictEnd) error {
	if conflictResolvesInsideContract(declarer, other) {
		return nil
	}
	var recovery string
	switch {
	case !declarer.derived:
		recovery = "the declaring record is legislated: take the conflict to an operator checkpoint to clarify intent, evolve the law, or accept a scope reduction"
	case !declarer.modified && other.modified && !other.derived:
		recovery = "add the declaring record to law_modifies, with its home Domain among the affected Domains when the contract changes the Product, and leave the legislated end unrevised, or take the conflict to an operator checkpoint"
	case !declarer.modified:
		recovery = "add the declaring record to law_modifies, with its home Domain among the affected Domains when the contract changes the Product"
	default:
		recovery = "the contract revises legislated law: revise only derived law, or take the conflict to an operator checkpoint"
	}
	failure := newFailure(KindRelationConflict, "check_mandated_laws", detail+", declared by "+declarer.lawID, false, recovery)
	failure.CandidateIDs = []string{declarer.lawID}
	return failure
}

// conflictResolvesInsideContract is the tier rule for an explicit conflict
// with a known declaring record. The contract must revise the declaring
// record, that record must be derived, and the other end may be revised only
// if it is derived too: revising legislated law keeps the operator's
// legislative moment.
func conflictResolvesInsideContract(declarer, other conflictEnd) bool {
	if !declarer.modified || !declarer.derived {
		return false
	}
	if other.modified {
		return other.derived
	}
	// The other end stays untouched, whatever its tier. When it is
	// legislated, the derived declaring record is brought into conformance
	// with an untouched legislated commitment, which CD-0159 D4 resolves
	// inside the contract. That rule lives here and nowhere else.
	return true
}

type mandatedSourceLawSet struct {
	home          KnowledgeHome
	idToReference map[string]string
}

// checkMandatedLawsAcrossSources is the federated mandated-law boundary check
// (CD-0200). Each mandated reference must resolve to exactly one accepted law
// subject over the verified source set. Conflicts read per source over the
// bare law IDs that resolved there, because a cross-source conflicts_with
// pair refuses at the declaring source's rebuild and can never project.
func checkMandatedLawsAcrossSources(ctx context.Context, q queryer, sources []KnowledgeHome, mandated, modified []string, allowAmendment bool) error {
	resolutions, missing, err := resolveFederatedLawReferences(ctx, q, "check_mandated_laws", sources, mandated)
	if err != nil {
		return err
	}
	resolved := make(map[string]federatedLawResolution, len(mandated))
	for _, reference := range mandated {
		if r, ok := resolutions[reference]; ok && r.status == "accepted" {
			resolved[reference] = r
		} else if ok {
			missing = append(missing, reference)
		}
	}
	if len(missing) != 0 {
		return mandatedLawProjectionFailure(ctx, q, sources, missing)
	}
	modifiedSet := make(map[string]bool, len(modified))
	for _, id := range modified {
		modifiedSet[id] = true
	}
	// Each source's conflict query runs over the bare law IDs that resolved
	// in that source, so a qualified mandate endpoint matches its declared
	// relation row.
	perSource := make(map[string]*mandatedSourceLawSet, len(resolved))
	for reference, r := range resolved {
		key := r.source.HomeProjectID + "/" + r.source.HomeLocatorID
		set := perSource[key]
		if set == nil {
			set = &mandatedSourceLawSet{home: r.source, idToReference: map[string]string{}}
			perSource[key] = set
		}
		set.idToReference[r.lawID] = reference
	}
	for _, set := range perSource {
		if err := checkMandatedSourceLawConflicts(ctx, q, set, modifiedSet, resolved, allowAmendment); err != nil {
			return err
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

func checkMandatedSourceLawConflicts(ctx context.Context, q queryer, set *mandatedSourceLawSet, modifiedSet map[string]bool, resolved map[string]federatedLawResolution, allowAmendment bool) error {
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
	conflictRows, err := q.QueryContext(ctx, `SELECT source_law_id,target_law_id,declarer_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(args, stringArgs(lawIDs)...)...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every law ID stays parameter-bound.
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
		var declarer sql.NullString
		if err := conflictRows.Scan(&sourceLaw, &targetLaw, &declarer); err != nil {
			conflictRows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law conflict", true, "retry once the knowledge projection is readable", err)
		}
		sourceReference, sourceKnown := set.idToReference[sourceLaw]
		targetReference, targetKnown := set.idToReference[targetLaw]
		if !sourceKnown || !targetKnown {
			continue
		}
		conflicts++
		sourceEnd := conflictEnd{lawID: sourceReference, modified: modifiedSet[sourceReference], derived: resolved[sourceReference].tier == "derived"}
		targetEnd := conflictEnd{lawID: targetReference, modified: modifiedSet[targetReference], derived: resolved[targetReference].tier == "derived"}
		if err := explicitConflictRefusal(sourceEnd, targetEnd, set.idToReference[declarer.String], allowAmendment); err != nil {
			_ = conflictRows.Close()
			return err
		}
	}
	if err := conflictRows.Err(); err != nil {
		conflictRows.Close()
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	if err := conflictRows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	return nil
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
