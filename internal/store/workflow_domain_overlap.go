package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// WorkflowDomainRelationTuple is the canonical identity of a Domain relation
// write. It is intentionally separate from the ordinary work relation graph:
// ordinary work links are never overlap authority.
type WorkflowDomainRelationTuple struct {
	SourceDomainID string `json:"source_domain_id"`
	Kind           string `json:"kind"`
	TargetDomainID string `json:"target_domain_id"`
}

// WorkflowDomainOverlap names every bounded intersection between two active
// Product-changing contracts. The active contract versions are part of the
// identity; a later contract revision therefore makes an old resolution stale
// without rewriting its event history.
type WorkflowDomainOverlap struct {
	ProductID                     string                        `json:"product_id"`
	FromWorkID                    string                        `json:"from_work_id"`
	ToWorkID                      string                        `json:"to_work_id"`
	FromContractVersion           int64                         `json:"from_contract_version"`
	ToContractVersion             int64                         `json:"to_contract_version"`
	SharedAffectedDomainIDs       []string                      `json:"shared_affected_domain_ids"`
	SharedLawIDs                  []string                      `json:"shared_law_ids"`
	SharedDomainModifications     []string                      `json:"shared_domain_modifications"`
	SharedRelationTuples          []WorkflowDomainRelationTuple `json:"shared_relation_tuples"`
	OverlapClasses                []string                      `json:"overlap_classes"`
	ResolutionState               string                        `json:"resolution_state"`
	ResolutionKind                string                        `json:"resolution_kind,omitempty"`
	RecoveryActions               []string                      `json:"recovery_actions"`
	SharedAffectedDomainCount     int                           `json:"shared_affected_domain_count"`
	SharedLawCount                int                           `json:"shared_law_count"`
	SharedDomainModificationCount int                           `json:"shared_domain_modification_count"`
	SharedRelationTupleCount      int                           `json:"shared_relation_tuple_count"`
	DetailTruncated               bool                          `json:"detail_truncated"`
}

// DomainOverlapFailure is the typed recovery diagnosis for unresolved or stale
// Domain overlap. It carries no authority derived from heuristics or ordinary
// relations.
type DomainOverlapFailure struct {
	Overlaps         []WorkflowDomainOverlap `json:"overlaps"`
	TotalOverlaps    int                     `json:"total_overlaps"`
	ReturnedOverlaps int                     `json:"returned_overlaps"`
	Truncated        bool                    `json:"truncated"`
}

const (
	ResolutionCompatibleWith = "compatible_with"
	ResolutionDependsOn      = "depends_on"
	ResolutionBlocks         = "blocks"
	ResolutionMergedInto     = "merged_into"
	ResolutionSupersedes     = "supersedes"
)

var workflowOverlapRecoveryActions = []string{"wait", "resolve_overlap", "terminal_work", "supersede_contract"}

// Domain-overlap details are carried in an agent envelope, whose maximum list
// size is twenty. A global byte bound is applied after deriving the complete
// population; exact counts make truncation explicit.
const maxWorkflowOverlapDetailItems = 20
const maxWorkflowOverlapFailureBytes = 16384

type workflowOverlapFootprint struct {
	ProductID           string
	WorkID              string
	ContractVersion     int64
	RegistryHash        string
	AffectedDomains     []string
	LawWrites           []string
	DomainModifications []string
	Relations           []WorkflowDomainRelationTuple
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func intersectStrings(left, right []string) []string {
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		set[value] = struct{}{}
	}
	result := []string{}
	for _, value := range right {
		if _, ok := set[value]; ok {
			result = append(result, value)
		}
	}
	return sortedStrings(uniqueStringsStable(result))
}

func uniqueStringsStable(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func intersectDomainRelations(left, right []WorkflowDomainRelationTuple) []WorkflowDomainRelationTuple {
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		set[domainRelationTupleKey(value)] = struct{}{}
	}
	result := []WorkflowDomainRelationTuple{}
	for _, value := range right {
		if _, ok := set[domainRelationTupleKey(value)]; ok {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return domainRelationTupleKey(result[i]) < domainRelationTupleKey(result[j]) })
	return result
}

func domainRelationTupleKey(value WorkflowDomainRelationTuple) string {
	return value.SourceDomainID + "\x00" + value.Kind + "\x00" + value.TargetDomainID
}

func readWorkflowOverlapFootprintTx(ctx context.Context, tx *sql.Tx, workID string) (workflowOverlapFootprint, error) {
	var footprint workflowOverlapFootprint
	footprint.WorkID = workID
	err := tx.QueryRowContext(ctx, `
		SELECT b.product_id,b.domain_registry_content_hash,c.contract_version
		FROM workflow_contracts c
		JOIN workflow_architecture_bindings b ON b.work_id=c.work_id AND b.contract_version=c.contract_version
		JOIN work_items w ON w.id=c.work_id
		WHERE c.work_id=? AND c.superseded_by IS NULL
		  AND w.lifecycle NOT IN ('completed','cancelled','superseded')
		ORDER BY c.contract_version DESC LIMIT 1`, workID).Scan(&footprint.ProductID, &footprint.RegistryHash, &footprint.ContractVersion)
	if err == sql.ErrNoRows {
		return footprint, nil
	}
	if err != nil {
		return footprint, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read active workflow Domain footprint", true, "retry once the workflow projection is readable", err)
	}
	if err := readOverlapStringListTx(ctx, tx, `SELECT domain_id FROM workflow_contract_affected_domains WHERE work_id=? AND contract_version=? ORDER BY domain_id`, &footprint.AffectedDomains, workID, footprint.ContractVersion); err != nil {
		return footprint, err
	}
	if err := readOverlapStringListTx(ctx, tx, `SELECT law_id FROM workflow_contract_law_additions WHERE work_id=? AND contract_version=? ORDER BY law_id`, &footprint.LawWrites, workID, footprint.ContractVersion); err != nil {
		return footprint, err
	}
	var modifications []string
	if err := readOverlapStringListTx(ctx, tx, `SELECT law_id FROM workflow_contract_law_modifications WHERE work_id=? AND contract_version=? ORDER BY law_id`, &modifications, workID, footprint.ContractVersion); err != nil {
		return footprint, err
	}
	footprint.LawWrites = append(footprint.LawWrites, modifications...)
	footprint.LawWrites = sortedStrings(uniqueStringsStable(footprint.LawWrites))
	if err := readOverlapStringListTx(ctx, tx, `SELECT domain_id FROM workflow_contract_domain_modifications WHERE work_id=? AND contract_version=? ORDER BY domain_id`, &footprint.DomainModifications, workID, footprint.ContractVersion); err != nil {
		return footprint, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT source_domain_id,kind,target_domain_id FROM workflow_contract_domain_relation_modifications WHERE work_id=? AND contract_version=? ORDER BY source_domain_id,kind,target_domain_id`, workID, footprint.ContractVersion)
	if err != nil {
		return footprint, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read Domain relation footprint", true, "retry once the workflow projection is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tuple WorkflowDomainRelationTuple
		if err := rows.Scan(&tuple.SourceDomainID, &tuple.Kind, &tuple.TargetDomainID); err != nil {
			return footprint, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot decode Domain relation footprint", true, "retry once the workflow projection is readable", err)
		}
		footprint.Relations = append(footprint.Relations, tuple)
	}
	if err := rows.Err(); err != nil {
		return footprint, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot enumerate Domain relation footprint", true, "retry once the workflow projection is readable", err)
	}
	return footprint, nil
}

func readWorkflowDomainOverlapCandidatesTx(ctx context.Context, tx *sql.Tx, workID string) (workflowOverlapFootprint, []workflowOverlapFootprint, error) {
	self, err := readWorkflowOverlapFootprintTx(ctx, tx, workID)
	if err != nil || self.ProductID == "" {
		return self, []workflowOverlapFootprint{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c.work_id FROM workflow_contracts c JOIN workflow_architecture_bindings b ON b.work_id=c.work_id AND b.contract_version=c.contract_version JOIN work_items w ON w.id=c.work_id WHERE c.superseded_by IS NULL AND w.lifecycle NOT IN ('completed','cancelled','superseded') AND b.product_id=? AND c.work_id<>? ORDER BY c.work_id`, self.ProductID, workID)
	if err != nil {
		return self, nil, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot enumerate active Product-changing workflows", true, "retry once the workflow projection is readable", err)
	}
	defer rows.Close()
	others := []workflowOverlapFootprint{}
	for rows.Next() {
		var otherID string
		if err := rows.Scan(&otherID); err != nil {
			return self, nil, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot decode active workflow identity", true, "retry once the workflow projection is readable", err)
		}
		other, err := readWorkflowOverlapFootprintTx(ctx, tx, otherID)
		if err != nil {
			return self, nil, err
		}
		others = append(others, other)
	}
	if err := rows.Err(); err != nil {
		return self, nil, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot enumerate active workflow overlap", true, "retry once the workflow projection is readable", err)
	}
	return self, others, nil
}

func readWorkflowUnresolvedDomainOverlapsTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorkflowDomainOverlap, error) {
	self, others, err := readWorkflowDomainOverlapCandidatesTx(ctx, tx, workID)
	if err != nil || self.ProductID == "" {
		return []WorkflowDomainOverlap{}, err
	}
	overlaps := []WorkflowDomainOverlap{}
	for _, other := range others {
		overlap, ok := workflowDomainOverlapPair(self, other)
		if !ok {
			continue
		}
		overlap.ResolutionState, overlap.ResolutionKind, err = currentWorkflowOverlapResolutionTx(ctx, tx, overlap)
		if err != nil {
			return nil, err
		}
		if overlap.ResolutionState != "current" && overlap.ResolutionState != "sequenced" {
			overlaps = append(overlaps, overlap)
		}
	}
	failure := &DomainOverlapFailure{Overlaps: overlaps}
	boundWorkflowDomainOverlapFailure(failure)
	if len(failure.Overlaps) > maxWorkflowOverlapDetailItems {
		failure.Overlaps = failure.Overlaps[:maxWorkflowOverlapDetailItems]
	}
	return failure.Overlaps, nil
}

func readOverlapStringListTx(ctx context.Context, tx *sql.Tx, query string, target *[]string, args ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read workflow footprint", true, "retry once the workflow projection is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot decode workflow footprint", true, "retry once the workflow projection is readable", err)
		}
		*target = append(*target, value)
	}
	if err := rows.Err(); err != nil {
		return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot enumerate workflow footprint", true, "retry once the workflow projection is readable", err)
	}
	return nil
}

func currentWorkflowOverlapResolutionTx(ctx context.Context, tx *sql.Tx, overlap WorkflowDomainOverlap) (string, string, error) {
	var state, kind string
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN resolution_kind IN ('depends_on','blocks') THEN 'sequenced' ELSE 'current' END,resolution_kind FROM workflow_overlap_resolutions WHERE product_id=? AND from_work_id=? AND to_work_id=? AND from_contract_version=? AND to_contract_version=? AND invalidated_seq IS NULL ORDER BY event_seq DESC LIMIT 1`, overlap.ProductID, overlap.FromWorkID, overlap.ToWorkID, overlap.FromContractVersion, overlap.ToContractVersion).Scan(&state, &kind)
	if err == nil {
		return state, kind, nil
	}
	if err != sql.ErrNoRows {
		return "", "", wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read overlap resolution", true, "retry once the overlap projection is readable", err)
	}
	// Directed sequence resolutions are stored in their operator-supplied
	// direction. Read the reverse orientation as the equivalent canonical kind
	// for the pairwise check.
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN resolution_kind IN ('depends_on','blocks') THEN 'sequenced' ELSE 'current' END,resolution_kind FROM workflow_overlap_resolutions WHERE product_id=? AND from_work_id=? AND to_work_id=? AND from_contract_version=? AND to_contract_version=? AND invalidated_seq IS NULL ORDER BY event_seq DESC LIMIT 1`, overlap.ProductID, overlap.ToWorkID, overlap.FromWorkID, overlap.ToContractVersion, overlap.FromContractVersion).Scan(&state, &kind)
	if err == nil {
		if kind == ResolutionDependsOn {
			kind = ResolutionBlocks
		} else if kind == ResolutionBlocks {
			kind = ResolutionDependsOn
		}
		return state, kind, nil
	}
	if err != sql.ErrNoRows {
		return "", "", wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read reverse overlap resolution", true, "retry once the overlap projection is readable", err)
	}
	var stale int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_overlap_resolutions WHERE product_id=? AND ((from_work_id=? AND to_work_id=?) OR (from_work_id=? AND to_work_id=?))`, overlap.ProductID, overlap.FromWorkID, overlap.ToWorkID, overlap.ToWorkID, overlap.FromWorkID).Scan(&stale); err != nil {
		return "", "", wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot inspect overlap resolution history", true, "retry once the overlap projection is readable", err)
	}
	if stale > 0 {
		return "stale", "", nil
	}
	return "unresolved", "", nil
}

func workflowDomainOverlapPair(left, right workflowOverlapFootprint) (WorkflowDomainOverlap, bool) {
	from, to := left, right
	if to.WorkID < from.WorkID {
		from, to = to, from
	}
	sharedDomains := intersectStrings(from.AffectedDomains, to.AffectedDomains)
	sharedLaw := intersectStrings(from.LawWrites, to.LawWrites)
	sharedDomainModifications := intersectStrings(from.DomainModifications, to.DomainModifications)
	sharedRelations := intersectDomainRelations(from.Relations, to.Relations)
	if len(sharedDomains) == 0 {
		return WorkflowDomainOverlap{}, false
	}
	classes := []string{"architecture"}
	if len(sharedLaw) > 0 {
		classes = append(classes, "law_write")
	}
	if len(sharedDomainModifications) > 0 {
		classes = append(classes, "domain_write")
	}
	if len(sharedRelations) > 0 {
		classes = append(classes, "domain_relation_write")
	}
	return WorkflowDomainOverlap{ProductID: from.ProductID, FromWorkID: from.WorkID, ToWorkID: to.WorkID, FromContractVersion: from.ContractVersion, ToContractVersion: to.ContractVersion, SharedAffectedDomainIDs: sharedDomains, SharedLawIDs: sharedLaw, SharedDomainModifications: sharedDomainModifications, SharedRelationTuples: sharedRelations, OverlapClasses: classes, RecoveryActions: append([]string(nil), workflowOverlapRecoveryActions...)}, true
}

func currentWorkflowDomainRegistryCheckTx(ctx context.Context, tx *sql.Tx, footprint workflowOverlapFootprint) error {
	var registryHash string
	if err := tx.QueryRowContext(ctx, `SELECT content_hash FROM domain_registries WHERE product_id=?`, footprint.ProductID).Scan(&registryHash); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindUnknownScope, "workflow_domain_overlap", "Product has no current Domain registry", false, "rebuild the current Product Domain registry")
		}
		return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read current Domain registry", true, "retry once the Domain projection is readable", err)
	}
	if registryHash != footprint.RegistryHash {
		return newFailure(KindStaleRequiresReview, "workflow_domain_overlap", "workflow Domain registry pin is stale", false, "reread and approve a current workflow contract")
	}
	for _, domainID := range footprint.AffectedDomains {
		var status, hash string
		if err := tx.QueryRowContext(ctx, `SELECT status,registry_content_hash FROM domains WHERE product_id=? AND domain_id=?`, footprint.ProductID, domainID).Scan(&status, &hash); err != nil {
			if err == sql.ErrNoRows {
				return newFailure(KindUnknownScope, "workflow_domain_overlap", "workflow names an unknown Domain: "+domainID, false, "rebuild the current Product Domain registry")
			}
			return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read current Domain membership", true, "retry once the Domain projection is readable", err)
		}
		if status != "current" || hash != registryHash {
			return newFailure(KindStaleRequiresReview, "workflow_domain_overlap", "workflow Domain membership is stale: "+domainID, false, "reread and approve a current workflow contract")
		}
	}
	return nil
}

// CheckWorkflowDomainOverlapTx is the consequential mutation boundary check.
// It derives active overlap from current projections in the caller's write
// transaction and never treats a heuristic or ordinary relation as authority.
func CheckWorkflowDomainOverlapTx(ctx context.Context, tx *sql.Tx, workID string) error {
	if tx == nil {
		return newFailure(KindUnavailable, "workflow_domain_overlap", "transaction is not open", false, "open a mutation transaction")
	}
	self, others, err := readWorkflowDomainOverlapCandidatesTx(ctx, tx, workID)
	if err != nil || self.WorkID == "" || self.ProductID == "" {
		return err
	}
	if err := currentWorkflowDomainRegistryCheckTx(ctx, tx, self); err != nil {
		return err
	}
	failures := []WorkflowDomainOverlap{}
	for _, other := range others {
		overlap, ok := workflowDomainOverlapPair(self, other)
		if !ok {
			continue
		}
		if err := currentWorkflowDomainRegistryCheckTx(ctx, tx, other); err != nil {
			return err
		}
		overlap.ResolutionState, overlap.ResolutionKind, err = currentWorkflowOverlapResolutionTx(ctx, tx, overlap)
		if err != nil {
			return err
		}
		allowed := (overlap.ResolutionState == "current" || overlap.ResolutionState == "sequenced") && overlapAllowsWork(overlap, workID)
		if !allowed {
			failures = append(failures, overlap)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	sort.Slice(failures, func(i, j int) bool {
		if failures[i].FromWorkID == failures[j].FromWorkID {
			return failures[i].ToWorkID < failures[j].ToWorkID
		}
		return failures[i].FromWorkID < failures[j].FromWorkID
	})
	failure := newFailure(KindDomainOverlap, "workflow_domain_overlap", "active Product-changing workflows have unresolved Domain overlap", false, "request_approval")
	failure.DomainOverlap = &DomainOverlapFailure{Overlaps: failures, TotalOverlaps: len(failures)}
	boundWorkflowDomainOverlapFailure(failure.DomainOverlap)
	return failure
}

// CheckWorkflowDomainOverlapTransactionTx adapts the overlap guard alone to a
// caller-owned transaction. A consequential boundary owes both D7 halves and
// uses CheckWorkflowConsequentialBoundaryTx instead; this is for callers that
// want the overlap condition on its own.
func CheckWorkflowDomainOverlapTransactionTx(ctx context.Context, transaction *Transaction, workID string) error {
	tx, err := transactionSQL(transaction, "workflow_domain_overlap")
	if err != nil {
		return err
	}
	return CheckWorkflowDomainOverlapTx(ctx, tx, workID)
}

func boundWorkflowDomainOverlapFailure(failure *DomainOverlapFailure) {
	if failure == nil {
		return
	}
	failure.TotalOverlaps = len(failure.Overlaps)
	for i := range failure.Overlaps {
		detail := &failure.Overlaps[i]
		detail.SharedAffectedDomainCount = len(detail.SharedAffectedDomainIDs)
		detail.SharedLawCount = len(detail.SharedLawIDs)
		detail.SharedDomainModificationCount = len(detail.SharedDomainModifications)
		detail.SharedRelationTupleCount = len(detail.SharedRelationTuples)
		if len(detail.SharedAffectedDomainIDs) > maxWorkflowOverlapDetailItems {
			detail.SharedAffectedDomainIDs = detail.SharedAffectedDomainIDs[:maxWorkflowOverlapDetailItems]
			detail.DetailTruncated = true
		}
		if len(detail.SharedLawIDs) > maxWorkflowOverlapDetailItems {
			detail.SharedLawIDs = detail.SharedLawIDs[:maxWorkflowOverlapDetailItems]
			detail.DetailTruncated = true
		}
		if len(detail.SharedDomainModifications) > maxWorkflowOverlapDetailItems {
			detail.SharedDomainModifications = detail.SharedDomainModifications[:maxWorkflowOverlapDetailItems]
			detail.DetailTruncated = true
		}
		if len(detail.SharedRelationTuples) > maxWorkflowOverlapDetailItems {
			detail.SharedRelationTuples = detail.SharedRelationTuples[:maxWorkflowOverlapDetailItems]
			detail.DetailTruncated = true
		}
	}
	for {
		failure.ReturnedOverlaps = len(failure.Overlaps)
		encoded, _ := json.Marshal(failure)
		if len(encoded) <= maxWorkflowOverlapFailureBytes {
			break
		}
		failure.Truncated = true
		if len(failure.Overlaps) > 1 {
			failure.Overlaps = failure.Overlaps[:len(failure.Overlaps)-1]
			continue
		}
		if len(failure.Overlaps) == 0 {
			break
		}
		detail := &failure.Overlaps[0]
		trimmed := false
		for _, list := range []*[]string{&detail.SharedAffectedDomainIDs, &detail.SharedLawIDs, &detail.SharedDomainModifications} {
			if len(*list) > 1 {
				*list = (*list)[:len(*list)-1]
				detail.DetailTruncated = true
				trimmed = true
				break
			}
		}
		if !trimmed && len(detail.SharedRelationTuples) > 1 {
			detail.SharedRelationTuples = detail.SharedRelationTuples[:len(detail.SharedRelationTuples)-1]
			detail.DetailTruncated = true
			trimmed = true
		}
		if !trimmed {
			break
		}
	}
	failure.ReturnedOverlaps = len(failure.Overlaps)
	for _, detail := range failure.Overlaps {
		if detail.DetailTruncated {
			failure.Truncated = true
		}
	}
}

func overlapAllowsWork(overlap WorkflowDomainOverlap, workID string) bool {
	switch overlap.ResolutionKind {
	case ResolutionCompatibleWith:
		return true
	case ResolutionDependsOn:
		return workID == overlap.ToWorkID
	case ResolutionBlocks:
		return workID == overlap.FromWorkID
	default:
		return false
	}
}

// CheckWorkflowDomainOverlap runs the same check in an owned transaction.
func CheckWorkflowDomainOverlap(ctx context.Context, s *Store, workID string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "workflow_domain_overlap", "store is not open", false, "open the authority database")
	}
	return s.Transact(ctx, func(transaction *Transaction) error {
		return CheckWorkflowDomainOverlapTransactionTx(ctx, transaction, workID)
	})
}

// WorkflowDomainOverlapResolutionRequest is the operator-approved resolution
// payload consumed by the relation surface.
type WorkflowDomainOverlapResolutionRequest struct {
	EventID             string
	FromWorkID          string
	ToWorkID            string
	FromExpectedVersion int64
	ToExpectedVersion   int64
	FromContractVersion int64
	ToContractVersion   int64
	ResolutionKind      string
	Reason              string
	ApprovalRef         string
	Actor               string
	OccurredAt          time.Time
}

// ResolveWorkflowDomainOverlapTx appends the immutable resolution event. The
// fold performs all current-version, overlap, relation, and terminal checks in
// this same transaction.
func ResolveWorkflowDomainOverlapTx(ctx context.Context, tx *Transaction, request WorkflowDomainOverlapResolutionRequest) (ApplyOperationResult, error) {
	sqlTx, err := transactionSQL(tx, "workflow_domain_overlap_resolution")
	if err != nil {
		return ApplyOperationResult{}, err
	}
	if request.OccurredAt.IsZero() {
		request.OccurredAt = tx.now()
	}
	payload, err := json.Marshal(map[string]any{
		"work_id": request.FromWorkID, "expected_version": request.FromExpectedVersion, "resulting_version": request.FromExpectedVersion + 1,
		"to_work_id": request.ToWorkID, "to_expected_version": request.ToExpectedVersion, "to_resulting_version": request.ToExpectedVersion + 1,
		"from_contract_version": request.FromContractVersion, "to_contract_version": request.ToContractVersion,
		"resolution_kind": request.ResolutionKind, "reason": request.Reason, "approval_ref": request.ApprovalRef,
	})
	if err != nil {
		return ApplyOperationResult{}, err
	}
	return applyOperationTx(ctx, sqlTx, Operation{Events: []Event{{EventID: request.EventID, Kind: WorkflowOverlapResolved, SubjectType: SubjectWorkItem, SubjectID: request.FromWorkID, Actor: request.Actor, OccurredAt: request.OccurredAt.UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.FromWorkID): request.FromExpectedVersion, VersionRef(SubjectWorkItem, request.ToWorkID): request.ToExpectedVersion}}, true, true)
}

func validateWorkflowOverlapResolutionKind(kind string) bool {
	return kind == ResolutionCompatibleWith || kind == ResolutionDependsOn || kind == ResolutionBlocks || kind == ResolutionMergedInto || kind == ResolutionSupersedes
}

func workflowOverlapResolutionRelationKind(kind string) string {
	switch kind {
	case ResolutionCompatibleWith, ResolutionDependsOn, ResolutionBlocks, ResolutionMergedInto, ResolutionSupersedes:
		return kind
	default:
		return ""
	}
}

func overlapResolutionFailure(detail string) error {
	return newFailure(KindInvalidPayload, "workflow_domain_overlap", detail, false, "supply a current operator-approved overlap resolution")
}

func workflowOverlapResolutionProjectionError(err error) error {
	if err == nil {
		return nil
	}
	if isConstraintViolation(err) {
		return newFailure(KindProjectionConflict, "workflow_domain_overlap", "overlap resolution projection conflicts with current history", false, "reread the active overlap and resolve it again")
	}
	return wrapFailure(KindUnavailable, "workflow_domain_overlap", fmt.Sprintf("cannot update overlap resolution projection: %v", err), true, "retry once the database is writable", err)
}

func invalidateWorkflowOverlapResolutionsForWorkTx(ctx context.Context, tx *sql.Tx, eventID string, workIDs ...string) error {
	if len(workIDs) == 0 {
		return nil
	}
	seq, err := eventSequenceTx(ctx, tx, eventID)
	if err != nil {
		return err
	}
	for _, workID := range workIDs {
		if workID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM relations WHERE resolution_id IN (
			SELECT resolution_id FROM workflow_overlap_resolutions
			WHERE (from_work_id=? OR to_work_id=?) AND invalidated_seq IS NULL
		)`, workID, workID); err != nil {
			return workflowOverlapResolutionProjectionError(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_overlap_resolutions SET invalidated_seq=? WHERE (from_work_id=? OR to_work_id=?) AND invalidated_seq IS NULL`, seq, workID, workID); err != nil {
			return workflowOverlapResolutionProjectionError(err)
		}
	}
	return nil
}

func invalidateWorkflowOverlapResolutionPairTx(ctx context.Context, tx *sql.Tx, eventID, fromWorkID, toWorkID string) error {
	seq, err := eventSequenceTx(ctx, tx, eventID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM relations WHERE resolution_id IN (
		SELECT resolution_id FROM workflow_overlap_resolutions
		WHERE invalidated_seq IS NULL
		  AND ((from_work_id=? AND to_work_id=?) OR (from_work_id=? AND to_work_id=?))
	)`, fromWorkID, toWorkID, toWorkID, fromWorkID); err != nil {
		return workflowOverlapResolutionProjectionError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_overlap_resolutions SET invalidated_seq=?
		WHERE invalidated_seq IS NULL
		  AND ((from_work_id=? AND to_work_id=?) OR (from_work_id=? AND to_work_id=?))`, seq, fromWorkID, toWorkID, toWorkID, fromWorkID); err != nil {
		return workflowOverlapResolutionProjectionError(err)
	}
	return nil
}

func eventSequenceTx(ctx context.Context, tx *sql.Tx, eventID string) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM domain_events WHERE event_id=?`, eventID).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read authoritative event sequence", true, "retry once the event log is readable", err)
	}
	return seq, nil
}

func terminalizeWorkflowOverlapWork(ctx context.Context, tx *sql.Tx, event Event, workID string, current, resulting int64) error {
	terminalEvent := event
	terminalEvent.SubjectID = workID
	if err := updateWorkLifecycle(ctx, tx, terminalEvent, "superseded", current, resulting); err != nil {
		return err
	}
	if err := removeTerminalResearchBindings(ctx, tx, workID, event.OccurredAt); err != nil {
		return err
	}
	if err := foldTerminalReleasesResourceClaims(ctx, tx, terminalEvent); err != nil {
		return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot release terminal overlap resource claims", true, "retry once the database is writable", err)
	}
	return nil
}

func foldWorkflowOverlapResolved(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload workflowOverlapResolvedPayload
	if err := decodeWorkflowPayload(event, &payload); err != nil {
		return err
	}
	if err := workflowBase(event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	if payload.ToWorkID == "" || payload.ToWorkID == event.SubjectID || payload.ExpectedVersion == nil || payload.ResultingVersion == nil || payload.FromContractVersion <= 0 || payload.ToContractVersion <= 0 || payload.ToExpectedVersion <= 0 || payload.ToResultingVersion != payload.ToExpectedVersion+1 || payload.ResolutionKind == "" || !validateWorkflowOverlapResolutionKind(payload.ResolutionKind) || !workflowString(payload.Reason, 4096) || payload.ApprovalRef == "" {
		return overlapResolutionFailure("overlap resolution has invalid endpoint, version, kind, or reason")
	}
	fromExpected, fromResulting := *payload.ExpectedVersion, *payload.ResultingVersion
	from, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	to, err := readWork(ctx, tx, payload.ToWorkID)
	if err != nil {
		return err
	}
	if err := validateWorkVersion(event, from.version, fromExpected, fromResulting); err != nil {
		return err
	}
	if err := validateWorkVersion(event, to.version, payload.ToExpectedVersion, payload.ToResultingVersion); err != nil {
		return err
	}
	left, err := readWorkflowOverlapFootprintTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	right, err := readWorkflowOverlapFootprintTx(ctx, tx, payload.ToWorkID)
	if err != nil {
		return err
	}
	if left.ProductID == "" || right.ProductID == "" || left.ProductID != right.ProductID || left.ContractVersion != payload.FromContractVersion || right.ContractVersion != payload.ToContractVersion {
		return overlapResolutionFailure("overlap resolution is not pinned to both current Product contract versions")
	}
	overlap, ok := workflowDomainOverlapPair(left, right)
	if !ok {
		return newFailure(KindInvalidOperation, "workflow_domain_overlap", "overlap resolution names a pair with no current derived overlap", false, "reread the active Domain footprints")
	}
	resolutionFromWorkID, resolutionToWorkID := event.SubjectID, payload.ToWorkID
	resolutionFromContractVersion, resolutionToContractVersion := payload.FromContractVersion, payload.ToContractVersion
	if payload.ResolutionKind == ResolutionCompatibleWith {
		resolutionFromWorkID, resolutionToWorkID = overlap.FromWorkID, overlap.ToWorkID
		resolutionFromContractVersion, resolutionToContractVersion = overlap.FromContractVersion, overlap.ToContractVersion
	}
	if err := currentWorkflowDomainRegistryCheckTx(ctx, tx, left); err != nil {
		return err
	}
	if err := currentWorkflowDomainRegistryCheckTx(ctx, tx, right); err != nil {
		return err
	}
	if err := invalidateWorkflowOverlapResolutionPairTx(ctx, tx, event.EventID, event.SubjectID, payload.ToWorkID); err != nil {
		return err
	}
	if payload.ResolutionKind == ResolutionSupersedes {
		var existingSuccessor string
		err := tx.QueryRowContext(ctx, `SELECT work_id_from FROM relations WHERE work_id_to=? AND kind='supersedes'`, payload.ToWorkID).Scan(&existingSuccessor)
		if err == nil {
			return newFailure(KindSupersessionSecondSuccessor, "workflow_domain_overlap", "supersession target already has a direct successor", false, "reopen the target and replace its existing successor explicitly")
		}
		if err != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot inspect supersession successors", true, "retry once the relation projection is readable", err)
		}
	}
	if payload.ResolutionKind == ResolutionMergedInto {
		var existingTarget string
		err := tx.QueryRowContext(ctx, `SELECT work_id_to FROM relations WHERE work_id_from=? AND kind='merged_into'`, event.SubjectID).Scan(&existingTarget)
		if err == nil {
			return newFailure(KindRelationConflict, "workflow_domain_overlap", "merged work already has a direct target", false, "restore the merged work before choosing another target")
		}
		if err != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot inspect merger targets", true, "retry once the relation projection is readable", err)
		}
	}
	if relationKind := workflowOverlapResolutionRelationKind(payload.ResolutionKind); relationKind != "" {
		if cycle, err := relationWouldCycle(ctx, tx, resolutionFromWorkID, resolutionToWorkID, relationKind); err != nil {
			return err
		} else if cycle {
			failure := newFailure(KindCycleDetected, "workflow_domain_overlap", "overlap resolution would create a relation cycle", false, "choose a non-cyclic resolution direction")
			failure.Violations = []string{relationKind + ":" + resolutionFromWorkID + "->" + resolutionToWorkID}
			return failure
		}
	}
	seq, err := eventSequenceTx(ctx, tx, event.EventID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,approval_ref,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, event.EventID, seq, left.ProductID, resolutionFromWorkID, resolutionToWorkID, resolutionFromContractVersion, resolutionToContractVersion, payload.ResolutionKind, payload.Reason, payload.ApprovalRef, event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return workflowOverlapResolutionProjectionError(err)
	}
	if relationKind := workflowOverlapResolutionRelationKind(payload.ResolutionKind); relationKind != "" {
		if err := insertRelation(ctx, tx, event, relationPayload{From: resolutionFromWorkID, To: resolutionToWorkID, Kind: relationKind, Reason: payload.Reason}); err != nil {
			return err
		}
	}
	if payload.ResolutionKind == ResolutionMergedInto || payload.ResolutionKind == ResolutionSupersedes {
		terminalID := payload.ToWorkID
		if payload.ResolutionKind == ResolutionMergedInto {
			terminalID = event.SubjectID
		}
		terminalWork, terminalVersion := to, payload.ToExpectedVersion
		if terminalID == event.SubjectID {
			terminalWork, terminalVersion = from, fromExpected
		}
		if err := terminalizeWorkflowOverlapWork(ctx, tx, event, terminalID, terminalWork.version, terminalVersion+1); err != nil {
			return err
		}
	}
	if payload.ResolutionKind != ResolutionMergedInto && payload.ResolutionKind != ResolutionSupersedes {
		if err := updateWorkVersionByID(ctx, tx, event.SubjectID, from.version, fromResulting, event.OccurredAt); err != nil {
			return err
		}
		if err := updateWorkVersionByID(ctx, tx, payload.ToWorkID, to.version, payload.ToResultingVersion, event.OccurredAt); err != nil {
			return err
		}
	} else {
		otherID, otherWork, otherVersion := payload.ToWorkID, to, payload.ToResultingVersion
		if payload.ResolutionKind == ResolutionSupersedes {
			otherID, otherWork, otherVersion = event.SubjectID, from, fromResulting
		}
		if err := updateWorkVersionByID(ctx, tx, otherID, otherWork.version, otherVersion, event.OccurredAt); err != nil {
			return err
		}
	}
	_ = overlap
	return nil
}
