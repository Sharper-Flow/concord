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

// WorkflowDomainOverlap names every bounded write intersection between two
// active Product-changing contracts. The active contract versions are part of
// the identity; a later contract revision therefore makes an old resolution
// stale without rewriting its event history.
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

// workflowOverlapRecoveryAction is the envelope-level recovery action the
// refusal carries. It must name something the caller can act on. The routes
// out of a blocking pair are the four in workflowOverlapRecoveryActions, and
// approval is not among them: CD-0145 D1 leaves a shared write to the existing
// resolution choices, and the refusal attaches no approval reference for an
// approval assertion to cite.
const workflowOverlapRecoveryAction = "reconcile_operation"

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

// readWorkflowOverlapFootprintTx reads the Domain footprint a contract carries.
// It reads the subject's prospective footprint, so it admits any nonterminal
// item: an item entering execution has to be checked against the claim it is
// about to take, not the empty one it holds while it waits. Which footprints
// count as live claims is decided where peers are enumerated (CD-0144, as
// amended by CD-0183).
func readWorkflowOverlapFootprintTx(ctx context.Context, tx *sql.Tx, workID string) (workflowOverlapFootprint, error) {
	var footprint workflowOverlapFootprint
	footprint.WorkID = workID
	// A footprint is the Domain claim one approved contract carries. An item
	// with no single approved contract carries no claim: it cannot reach an
	// implementation-bearing action, so it can take no Domain a peer would
	// contend for. Both the absent and the ambiguous projection therefore
	// yield an empty footprint.
	//
	// This read runs for the subject and for every peer. Refusing here on an
	// ambiguous peer would let one unrepaired item block every claim in the
	// Product, including the claim its own operator recovery needs.
	activeVersions, activeErr := activeWorkflowContractVersions(ctx, tx, workID)
	if activeErr != nil {
		return footprint, activeErr
	}
	if len(activeVersions) != 1 {
		return footprint, nil
	}
	activeVersion := activeVersions[0]
	err := tx.QueryRowContext(ctx, `
		SELECT b.product_id,b.domain_registry_content_hash,c.contract_version
		FROM workflow_contracts c
		JOIN workflow_architecture_bindings b ON b.work_id=c.work_id AND b.contract_version=c.contract_version
		JOIN work_items w ON w.id=c.work_id
		WHERE c.work_id=? AND c.contract_version=? AND c.superseded_by IS NULL
		  AND w.lifecycle NOT IN ('completed','cancelled','superseded')
		`, workID, activeVersion).Scan(&footprint.ProductID, &footprint.RegistryHash, &footprint.ContractVersion)
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

// readWorkflowDomainOverlapCandidatesTx pairs the subject against the items
// that actually hold Domains. CD-0144 put exclusivity at execution start;
// CD-0183 moves the marker off the lifecycle: the first workflow action moves
// a needed item to in_progress, so the lifecycle no longer names execution.
// A peer is an item whose workflow instance carries the durable
// execution-start fact the fold that begins an external-effect step sets. An
// in_progress item that has not started execution claims nothing and blocks
// nobody.
func readWorkflowDomainOverlapCandidatesTx(ctx context.Context, tx *sql.Tx, workID string) (workflowOverlapFootprint, []workflowOverlapFootprint, error) {
	self, err := readWorkflowOverlapFootprintTx(ctx, tx, workID)
	if err != nil || self.ProductID == "" {
		return self, []workflowOverlapFootprint{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c.work_id FROM workflow_contracts c JOIN workflow_architecture_bindings b ON b.work_id=c.work_id AND b.contract_version=c.contract_version JOIN workflow_instances i ON i.work_id=c.work_id JOIN work_items w ON w.id=c.work_id WHERE c.superseded_by IS NULL AND i.execution_started_at IS NOT NULL AND w.lifecycle NOT IN ('completed','cancelled','superseded') AND b.product_id=? AND c.work_id<>? ORDER BY c.work_id`, self.ProductID, workID)
	if err != nil {
		return self, nil, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot enumerate active Product-changing workflows", true, "retry once the workflow projection is readable", err)
	}
	defer rows.Close()
	var otherIDs []string
	for rows.Next() {
		var otherID string
		if err := rows.Scan(&otherID); err != nil {
			return self, nil, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot decode active workflow identity", true, "retry once the workflow projection is readable", err)
		}
		otherIDs = append(otherIDs, otherID)
	}
	if err := rows.Err(); err != nil {
		return self, nil, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot enumerate active workflow overlap", true, "retry once the workflow projection is readable", err)
	}
	if err := rows.Close(); err != nil {
		return self, nil, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot close active workflow overlap", true, "retry once the workflow projection is readable", err)
	}
	others := []workflowOverlapFootprint{}
	for _, otherID := range otherIDs {
		other, err := readWorkflowOverlapFootprintTx(ctx, tx, otherID)
		if err != nil {
			return self, nil, err
		}
		others = append(others, other)
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
	if len(sharedLaw) == 0 && len(sharedDomainModifications) == 0 && len(sharedRelations) == 0 {
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
	// The pinned continuity projection validates against the generated
	// envelope schema, which types every shared-item list as an array. A nil
	// slice marshals as null and fails that validation, so an overlap that
	// shares no laws, modifications, or relations carries empty arrays.
	if sharedLaw == nil {
		sharedLaw = []string{}
	}
	if sharedDomainModifications == nil {
		sharedDomainModifications = []string{}
	}
	if sharedRelations == nil {
		sharedRelations = []WorkflowDomainRelationTuple{}
	}
	return WorkflowDomainOverlap{ProductID: from.ProductID, FromWorkID: from.WorkID, ToWorkID: to.WorkID, FromContractVersion: from.ContractVersion, ToContractVersion: to.ContractVersion, SharedAffectedDomainIDs: sharedDomains, SharedLawIDs: sharedLaw, SharedDomainModifications: sharedDomainModifications, SharedRelationTuples: sharedRelations, OverlapClasses: classes, RecoveryActions: append([]string(nil), workflowOverlapRecoveryActions...)}, true
}

// StaleDomainRegistryPin names the work item whose own approved contract pin
// a Domain registry rescan stranded. currentWorkflowDomainRegistryCheckTx
// attaches it to every stale-pin refusal it raises, so the one contract
// recovery predicate can tell the subject's own stale pin from a peer's
// without matching refusal text. It never crosses the agent envelope.
type StaleDomainRegistryPin struct {
	WorkID          string `json:"work_id"`
	ContractVersion int64  `json:"contract_version"`
	PinnedHash      string `json:"pinned_hash"`
	CurrentHash     string `json:"current_hash"`
}

// staleWorkflowDomainRegistryPinFailure raises the registry-staleness refusal
// with the store-internal marker naming the checked footprint's own work item.
func staleWorkflowDomainRegistryPinFailure(footprint workflowOverlapFootprint, currentHash, detail string) *Failure {
	failure := newFailure(KindStaleRequiresReview, "workflow_domain_overlap", detail, false, "reread and approve a current workflow contract")
	failure.StaleDomainRegistryPin = &StaleDomainRegistryPin{WorkID: footprint.WorkID, ContractVersion: footprint.ContractVersion, PinnedHash: footprint.RegistryHash, CurrentHash: currentHash}
	return failure
}

// staleRegistryPinRefusalNamesWork reports whether the refusal is a stale
// Domain registry pin whose marker names workID itself. A missing registry
// (unknown_scope) and a marker naming any other item never match.
func staleRegistryPinRefusalNamesWork(err error, workID string) bool {
	var failure *Failure
	if !failureAs(err, &failure) {
		return false
	}
	return failure.Kind == KindStaleRequiresReview && failure.StaleDomainRegistryPin != nil && failure.StaleDomainRegistryPin.WorkID == workID
}

// workflowStalePinAdmission names the stale-pin refusals one boundary check
// admits. The default holds every refusal (CD-0041 D7).
type workflowStalePinAdmission int

const (
	// workflowStalePinAdmitNone is the default boundary: a stale pin refuses
	// whatever action runs behind it, whoever the marker names.
	workflowStalePinAdmitNone workflowStalePinAdmission = iota
	// workflowStalePinAdmitOwnMarker admits a stale-pin refusal whose marker
	// names the checked work item itself. Admission under a stale pin belongs
	// to the marker's named subject only, so a peer's stale pin still refuses.
	workflowStalePinAdmitOwnMarker
)

// workflowActionStalePinAdmission reports the stale-pin admission the D7
// boundary owes one workflow action. Attempt disposition records facts about
// attempts made under the pinned contract, so the subject's own stale pin
// admits it and the item settles its attempt before it self-heals through
// the contract re-pin. Contract-judgment actions (record_verdict, complete)
// keep the full refusal, so terminal completion still requires the re-pin.
func workflowActionStalePinAdmission(actionID string) workflowStalePinAdmission {
	switch actionID {
	case "accept_worker_result", "accept_worker_evidence", "reject_worker_result", "record_worker_failure":
		return workflowStalePinAdmitOwnMarker
	default:
		return workflowStalePinAdmitNone
	}
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
		return staleWorkflowDomainRegistryPinFailure(footprint, registryHash, "workflow Domain registry pin is stale")
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
			return staleWorkflowDomainRegistryPinFailure(footprint, registryHash, "workflow Domain membership is stale: "+domainID)
		}
	}
	return nil
}

// checkWorkflowDomainOverlapTxAdmitting is the consequential mutation boundary
// check. It derives active overlap from current projections in the caller's
// write transaction and never treats a heuristic or ordinary relation as
// authority. The admission names the stale-pin refusals this call passes;
// every production caller holds the default full refusal.
func checkWorkflowDomainOverlapTxAdmitting(ctx context.Context, tx *sql.Tx, workID string, admission workflowStalePinAdmission) error {
	if tx == nil {
		return newFailure(KindUnavailable, "workflow_domain_overlap", "transaction is not open", false, "open a mutation transaction")
	}
	self, others, err := readWorkflowDomainOverlapCandidatesTx(ctx, tx, workID)
	if err != nil || self.WorkID == "" || self.ProductID == "" {
		return err
	}
	if err := currentWorkflowDomainRegistryCheckTx(ctx, tx, self); err != nil {
		// Admission under a stale pin belongs to the marker's named subject
		// only. The check names the subject it read, so the admitting class
		// passes a refusal whose marker names workID itself, and a peer's
		// stale pin together with a missing registry still refuse here.
		if !(admission == workflowStalePinAdmitOwnMarker && staleRegistryPinRefusalNamesWork(err, workID)) {
			return err
		}
	}
	exempt, err := workflowSelfRepairExemptTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if exempt {
		return nil
	}
	failures := []WorkflowDomainOverlap{}
	for _, other := range others {
		overlap, ok := workflowDomainOverlapPair(self, other)
		if !ok {
			continue
		}
		// A recorded resolution is evaluated before the peer pin check, so a
		// resolved overlap never vetoes the subject's admission, whatever
		// hash the peer's own contract still pins.
		overlap.ResolutionState, overlap.ResolutionKind, err = currentWorkflowOverlapResolutionTx(ctx, tx, overlap)
		if err != nil {
			return err
		}
		if allowed := (overlap.ResolutionState == "current" || overlap.ResolutionState == "sequenced") && overlapAllowsWork(overlap, workID); allowed {
			continue
		}
		if err := currentWorkflowDomainRegistryCheckTx(ctx, tx, other); err != nil {
			return err
		}
		failures = append(failures, overlap)
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
	failure := newFailure(KindDomainOverlap, "workflow_domain_overlap", "active Product-changing workflows have unresolved Domain overlap", false, workflowOverlapRecoveryAction)
	failure.DomainOverlap = &DomainOverlapFailure{Overlaps: failures, TotalOverlaps: len(failures)}
	boundWorkflowDomainOverlapFailure(failure.DomainOverlap)
	return failure
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
	// The agent envelope refuses a domain_overlap error that carries more than
	// maxWorkflowOverlapDetailItems overlaps. Many small overlaps fit inside the
	// byte budget, so the count bound must run before it or the refusal cannot
	// be delivered. TotalOverlaps keeps the true population.
	if len(failure.Overlaps) > maxWorkflowOverlapDetailItems {
		failure.Overlaps = failure.Overlaps[:maxWorkflowOverlapDetailItems]
		failure.Truncated = true
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
	return applyOperationTx(ctx, sqlTx, Operation{Events: []Event{{EventID: request.EventID, Kind: WorkflowOverlapResolved, SubjectType: SubjectWorkItem, SubjectID: request.FromWorkID, Actor: request.Actor, OccurredAt: request.OccurredAt.UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.FromWorkID): request.FromExpectedVersion, VersionRef(SubjectWorkItem, request.ToWorkID): request.ToExpectedVersion}}, newFoldScope(sqlTx), true)
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

// workflowOverlapReplayProductID resolves the Product a replayed resolution
// records, from the item's contract bindings. The active-version footprint is
// the live surface; replay only needs the Product identity the log carries.
func workflowOverlapReplayProductID(ctx context.Context, tx *sql.Tx, workID string) string {
	var productID string
	_ = tx.QueryRowContext(ctx, `SELECT product_id FROM workflow_architecture_bindings WHERE work_id=? ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&productID)
	return productID
}

func foldWorkflowOverlapResolved(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload workflowOverlapResolvedPayload
	if err := decodeWorkflowPayload(event, &payload); err != nil {
		return err
	}
	// A depends_on resolution records pure sequencing: the declarer waits,
	// the peer proceeds, and no other item's admission changes, so it is the
	// one kind the fold accepts without an operator approval reference.
	// Every other kind changes another item's admission or identity and
	// requires the approval the plan layer demands.
	approvalRequired := payload.ResolutionKind != ResolutionDependsOn
	if err := workflowBase(event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	if payload.ToWorkID == "" || payload.ToWorkID == event.SubjectID || payload.ExpectedVersion == nil || payload.ResultingVersion == nil || payload.FromContractVersion <= 0 || payload.ToContractVersion <= 0 || payload.ToExpectedVersion <= 0 || payload.ToResultingVersion != payload.ToExpectedVersion+1 || payload.ResolutionKind == "" || !validateWorkflowOverlapResolutionKind(payload.ResolutionKind) || !workflowString(payload.Reason, 4096) || (approvalRequired && payload.ApprovalRef == "") {
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
	if err := validateWorkVersion(event.SubjectID, from.version, fromExpected, fromResulting); err != nil {
		return err
	}
	if err := validateWorkVersion(payload.ToWorkID, to.version, payload.ToExpectedVersion, payload.ToResultingVersion); err != nil {
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
	// The version pin is a live-path freshness guard: mid-replay the contract
	// projections are the log's own prefix, and supersessions still to fold
	// can leave the active versions ambiguous. Replay resolves the Product
	// from the item's contract bindings instead.
	if !isWorkflowReplay(ctx) {
		if left.ProductID == "" || right.ProductID == "" || left.ProductID != right.ProductID || left.ContractVersion != payload.FromContractVersion || right.ContractVersion != payload.ToContractVersion {
			return overlapResolutionFailure("overlap resolution is not pinned to both current Product contract versions")
		}
	} else {
		if left.ProductID == "" {
			left.ProductID = workflowOverlapReplayProductID(ctx, tx, event.SubjectID)
		}
		if right.ProductID == "" {
			right.ProductID = workflowOverlapReplayProductID(ctx, tx, payload.ToWorkID)
		}
	}
	overlap, ok := workflowDomainOverlapPair(left, right)
	if !ok && !isWorkflowReplay(ctx) {
		return newFailure(KindInvalidOperation, "workflow_domain_overlap", "overlap resolution names a pair with no current derived overlap", false, "reread the active Domain footprints")
	}
	resolutionFromWorkID, resolutionToWorkID := event.SubjectID, payload.ToWorkID
	resolutionFromContractVersion, resolutionToContractVersion := payload.FromContractVersion, payload.ToContractVersion
	if payload.ResolutionKind == ResolutionCompatibleWith {
		if ok {
			resolutionFromWorkID, resolutionToWorkID = overlap.FromWorkID, overlap.ToWorkID
			resolutionFromContractVersion, resolutionToContractVersion = overlap.FromContractVersion, overlap.ToContractVersion
		} else {
			// The pair no longer derives under the current overlap rule, so
			// replay records the resolution from its payload in the sorted
			// pair order every recorded compatible_with resolution carries.
			if payload.ToWorkID < event.SubjectID {
				resolutionFromWorkID, resolutionToWorkID = payload.ToWorkID, event.SubjectID
				resolutionFromContractVersion, resolutionToContractVersion = payload.ToContractVersion, payload.FromContractVersion
			}
		}
	}
	// The registry comparison pins the event against the current Git-derived
	// registry. The declarer's own stale pin still refuses the recording, so
	// the marker's named subject re-pins its own contract first (CD-0041 D7).
	// The peer's stale pin does not veto the recording: resolve_overlap is
	// the D7-exempt recovery route, and the peer's stale pin opens the peer's
	// recovery route, not a veto over this pair. A replay re-derives the
	// recorded resolution from the log, so the current registry, which the
	// log never carries, is not its authority.
	if !isWorkflowReplay(ctx) {
		if err := currentWorkflowDomainRegistryCheckTx(ctx, tx, left); err != nil {
			return err
		}
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

// InspectWorkflowDomainOverlap reports an unresolved Domain overlap for one
// work item. It opens the transaction the boundary check requires, so a
// caller outside this package observes the same derivation a mutation
// boundary applies rather than a separate untransacted one. Inspection holds
// the default admission: a stale pin refuses whatever it would refuse for a
// caller, whoever the marker names.
func InspectWorkflowDomainOverlap(ctx context.Context, s *Store, workID string) error {
	return s.Transact(ctx, func(transaction *Transaction) error {
		return checkWorkflowDomainOverlapTxAdmitting(ctx, transaction.tx, workID, workflowStalePinAdmitNone)
	})
}
