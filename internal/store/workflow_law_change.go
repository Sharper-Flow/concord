package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// WorkflowLawChange records a reason and the law delta against the owning start.
type WorkflowLawChange struct {
	Version                    int                          `json:"version,omitempty"`
	Reason                     string                       `json:"reason"`
	RemovesUserVisibleBehavior bool                         `json:"removes_user_visible_behavior,omitempty"`
	TouchesSecurityOrDataSafe  bool                         `json:"touches_security_or_data_safety,omitempty"`
	LawIDs                     []string                     `json:"law_ids"`
	Removals                   []string                     `json:"removals,omitempty"`
	DomainIDs                  []string                     `json:"domain_ids,omitempty"`
	Crucial                    bool                         `json:"crucial,omitempty"`
	PredecessorSerious         bool                         `json:"predecessor_serious,omitempty"`
	Serious                    bool                         `json:"serious,omitempty"`
	EditsRegistry              bool                         `json:"edits_registry,omitempty"`
	LawBefore                  []workflowLawState           `json:"law_before"`
	RegistryBefore             *workflowLawRegistrySnapshot `json:"registry_before"`
}

// workflowLawState is one law's state as the projection holds it: absent, or
// its content hash, authority tier, and the child and root Domains it is homed
// in or applies to.
type workflowLawState struct {
	LawID       string   `json:"law_id"`
	Absent      bool     `json:"absent,omitempty"`
	ContentHash string   `json:"content_hash,omitempty"`
	Tier        string   `json:"tier,omitempty"`
	DomainIDs   []string `json:"domain_ids"`
}

func (state workflowLawState) differs(other workflowLawState) bool {
	return state.Absent != other.Absent || state.ContentHash != other.ContentHash || state.Tier != other.Tier || !slices.Equal(state.DomainIDs, other.DomainIDs)
}

const (
	workflowLawChangeReasonField    = "law_change_reason"
	workflowLawChangeRemovalsField  = "law_change_removals"
	workflowLawChangeReasonMaxBytes = 1024
	workflowLawChangeRemovalsMax    = 64
)

func workflowLawChangeFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionStringField(workflowLawChangeReasonField, false, workflowLawChangeReasonMaxBytes),
		actionArrayField(workflowLawChangeRemovalsField, false, workflowLawChangeRemovalsMax, "law_id"),
	}
}

// workflowContractChangesLaw reports whether a contract declares that it modifies or removes
// law, adds law, or changes the Domain registry. A mandate alone binds law the
// work must meet; it changes none.
func workflowContractChangesLaw(lawModifies []string, binding *WorkflowArchitectureBinding) bool {
	if len(lawModifies) != 0 {
		return true
	}
	return binding != nil && (len(binding.LawAdditions) != 0 || workflowContractEditsRegistry(binding))
}

// workflowContractEditsRegistry reports whether a contract changes the Domain
// registry itself: a Domain or a Domain relation.
func workflowContractEditsRegistry(binding *WorkflowArchitectureBinding) bool {
	return binding != nil && (len(binding.DomainModifies) != 0 || len(binding.DomainRelationModifies) != 0)
}

// workflowChangedLawIDs lists every law a contract changes, sorted and
// unique: each modified or removed law and each added law.
func workflowChangedLawIDs(lawModifies []string, binding *WorkflowArchitectureBinding) []string {
	changed := slices.Clone(lawModifies)
	if binding != nil {
		for _, addition := range binding.LawAdditions {
			changed = append(changed, addition.LawID)
		}
	}
	slices.Sort(changed)
	return slices.Compact(changed)
}

// deriveWorkflowLawChangeTx builds the law-change fact for one consequence_v1
// contract or successor. It returns nil for a legacy definition or a contract
// with no declared or retained enacted law scope, and refuses a law-changing
// contract without a reason. binding is the Product-changing contract's
// architecture binding, else nil.
func deriveWorkflowLawChangeTx(ctx context.Context, q queryer, definition WorkflowDefinition, workID string, fields map[string]json.RawMessage, lawModifies []string, binding *WorkflowArchitectureBinding) (*WorkflowLawChange, error) {
	declared := false
	for _, field := range workflowLawChangeFields() {
		if _, present := fields[field.Name]; present {
			declared = true
		}
	}
	if !workflowDeclaresDecisionDomains(definition) {
		if declared {
			return nil, newFailure(KindInvalidPayload, "workflow_law_change", "law_change fields are admitted only on a consequence_v1 contract that changes law", false, "omit the law_change fields")
		}
		return nil, nil
	}
	change := &WorkflowLawChange{Version: 2}
	if err := deriveWorkflowLawChangeScopeTx(ctx, q, workID, change, lawModifies, binding); err != nil {
		return nil, err
	}
	if len(change.LawIDs) == 0 && !change.EditsRegistry {
		if declared {
			return nil, newFailure(KindInvalidPayload, "workflow_law_change", "law_change fields require a declared or enacted law change", false, "omit law_change fields when no law changes")
		}
		return nil, nil
	}
	reason, _ := workflowFieldString(fields, workflowLawChangeReasonField)
	if strings.TrimSpace(reason) == "" || len(reason) > workflowLawChangeReasonMaxBytes {
		return nil, newFailure(KindInvalidPayload, "workflow_law_change", "every contract of a work item that changes law must record why (CD-0230 D4)", false, "supply law_change_reason")
	}
	change.Reason = reason
	if raw, present := fields[workflowLawChangeRemovalsField]; present {
		if err := decodeWorkflowActionPayloadStrict(raw, &change.Removals); err != nil || change.Removals == nil {
			return nil, newFailure(KindInvalidPayload, "workflow_law_change", "law_change_removals must be a list of law IDs", false, "declare the scoped laws whose end state is absent")
		}
		slices.Sort(change.Removals)
	}
	if err := validateWorkflowLawChangeRemovals(change, "workflow_law_change"); err != nil {
		return nil, err
	}
	return change, nil
}

// A law change records declared scope and retains the still-enacted scope of
// the immediate predecessor, which already carries every earlier retained law.
// Unattributed inventory deltas stay visible to completion; they do not become
// this item's declarations or obstruct successor recovery.
func deriveWorkflowLawChangeScopeTx(ctx context.Context, q queryer, workID string, change *WorkflowLawChange, lawModifies []string, binding *WorkflowArchitectureBinding) error {
	before, after, err := readWorkflowLawBaselineTx(ctx, q, workID)
	if err != nil {
		return err
	}
	declared := workflowChangedLawIDs(lawModifies, binding)
	old := workflowLawInventoryMap(before)
	aliases, err := workflowDeclaredLawAliases(before, after, old, declared, binding)
	if err != nil {
		return err
	}
	prior, err := readWorkflowLawChangeHistoryTx(ctx, q, workID)
	if err != nil {
		return err
	}
	owned := map[string]string{}
	for _, previous := range prior {
		for _, reference := range previous.LawIDs {
			canonical, err := workflowLawInventoryReference(reference, before, after)
			if err != nil {
				return err
			}
			owned[canonical] = reference
		}
	}
	laws := slices.Clone(declared)
	for _, canonical := range workflowLawInventoryDifferences(before, after) {
		if _, declared := aliases[canonical]; declared {
			continue
		}
		if reference, approved := owned[canonical]; approved {
			laws = append(laws, reference)
		}
	}
	change.LawIDs = append([]string{}, uniqueSorted(laws)...)
	change.LawBefore = []workflowLawState{}
	for _, reference := range change.LawIDs {
		canonical, err := workflowLawInventoryReference(reference, before, after)
		if err != nil {
			return err
		}
		state, exists := old[canonical]
		if !exists {
			state = workflowLawState{Absent: true, DomainIDs: []string{}}
		}
		state.LawID = reference
		change.LawBefore = append(change.LawBefore, state)
	}
	change.RegistryBefore = before.Registry
	change.EditsRegistry = workflowContractEditsRegistry(binding)
	if !reflect.DeepEqual(before.Registry, after.Registry) {
		for _, previous := range prior {
			change.EditsRegistry = change.EditsRegistry || previous.EditsRegistry
		}
	}
	return nil
}

func workflowDeclaredLawAliases(before, after *workflowLawBaseline, old map[string]workflowLawState, declared []string, binding *WorkflowArchitectureBinding) (map[string]string, error) {
	additions := map[string]bool{}
	if binding != nil {
		for _, addition := range binding.LawAdditions {
			canonical, err := workflowLawInventoryReference(addition.LawID, before, after)
			if err != nil {
				return nil, err
			}
			additions[canonical] = true
		}
	}
	aliases := map[string]string{}
	for _, reference := range declared {
		canonical, err := workflowLawInventoryReference(reference, before, after)
		if err != nil {
			return nil, err
		}
		if previous, exists := aliases[canonical]; exists && previous != reference {
			return nil, newFailure(KindInvalidPayload, "workflow_law_change", "one law is declared through multiple source aliases", false, "declare each source law once")
		}
		_, existed := old[canonical]
		if !existed && !additions[canonical] {
			return nil, newFailure(KindOutcomeMismatch, "workflow_law_change", "law "+reference+" was absent at the owning start and requires a declared law_additions entry", false, "declare the new law and its home in a Product-changing contract")
		}
		if existed && additions[canonical] {
			return nil, newFailure(KindInvalidPayload, "workflow_law_change", "law addition already existed at the owning start: "+reference, false, "declare an existing law through law_modifies")
		}
		aliases[canonical] = reference
	}
	return aliases, nil
}

func readWorkflowLawChangeHistoryTx(ctx context.Context, q queryer, workID string) ([]WorkflowLawChange, error) {
	rows, err := q.QueryContext(ctx, `SELECT law_change FROM workflow_contracts WHERE work_id=?1 AND law_change IS NOT NULL AND contract_version=(SELECT max(contract_version) FROM workflow_contracts WHERE work_id=?1)`, workID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read the item's approved law scope")
	}
	defer func() { _ = rows.Close() }()
	var changes []WorkflowLawChange
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, workflowProjectionError(err, "cannot read a prior law change")
		}
		var change WorkflowLawChange
		if err := json.Unmarshal([]byte(encoded), &change); err != nil {
			return nil, workflowProjectionError(err, "cannot decode a prior law change")
		}
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil {
		return nil, workflowProjectionError(err, "cannot finish reading the item's approved law scope")
	}
	return changes, nil
}

func validateWorkflowLawChangeRemovals(change *WorkflowLawChange, op string) error {
	if len(change.Removals) > workflowLawChangeRemovalsMax || !slices.IsSorted(change.Removals) || len(slices.Compact(slices.Clone(change.Removals))) != len(change.Removals) {
		return newFailure(KindInvalidPayload, op, "law_change removals must be a sorted unique list", false, "declare each removed law once")
	}
	for _, id := range change.Removals {
		if !slices.Contains(change.LawIDs, id) || !slices.ContainsFunc(change.LawBefore, func(state workflowLawState) bool { return state.LawID == id && !state.Absent }) {
			return newFailure(KindInvalidPayload, op, "law_change removal must name a scoped law present at the owning start: "+id, false, "declare the existing law through law_modifies before removing it")
		}
	}
	return nil
}

// resolveWorkflowChangedLawsTx resolves changed law references through the
// same Project-qualified identity as the mandated-law boundary, against the
// verified law-folder snapshot. A law the committed folder no longer holds,
// such as one the change removed, resolves to nothing; the approval path has
// already refused an unknown modified law.
func resolveWorkflowChangedLawsTx(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, laws []string) (map[string]lawReferenceResolution, error) {
	if len(laws) == 0 {
		return nil, nil
	}
	sources := []KnowledgeHome{{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}}
	resolutions, _, err := resolveLawReferences(ctx, q, "workflow_law_change", sources, laws)
	return resolutions, err
}

// validateWorkflowLawChangeShape holds a recorded fact to its closed form. It
// reads nothing, so replay folds recorded bytes under it.
func validateWorkflowLawChangeShape(change *WorkflowLawChange, lawModifies []string, binding *WorkflowArchitectureBinding) error {
	if strings.TrimSpace(change.Reason) == "" || len(change.Reason) > workflowLawChangeReasonMaxBytes {
		return newFailure(KindInvalidPayload, "fold_event", "law_change reason must be 1 to 1024 bytes", false, "record why the law changes")
	}
	for name, ids := range map[string][]string{"law_ids": change.LawIDs} {
		if ids == nil || !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			return newFailure(KindInvalidPayload, "fold_event", "law_change "+name+" must be a sorted unique list", false, "record the derived "+name)
		}
	}
	for _, lawID := range workflowChangedLawIDs(lawModifies, binding) {
		if _, found := slices.BinarySearch(change.LawIDs, lawID); !found {
			return newFailure(KindInvalidPayload, "fold_event", "law_change law_ids omits the declared law "+lawID, false, "record every law the contract declares")
		}
	}
	if err := validateWorkflowLawBeforeStates(change.LawBefore); err != nil {
		return err
	}
	for _, lawID := range change.LawIDs {
		if !slices.ContainsFunc(change.LawBefore, func(state workflowLawState) bool { return state.LawID == lawID }) {
			return newFailure(KindInvalidPayload, "fold_event", "law_change records no before-state for "+lawID, false, "record the before-state of every counted law")
		}
	}
	if workflowContractEditsRegistry(binding) && !change.EditsRegistry {
		return newFailure(KindInvalidPayload, "fold_event", "law_change edits_registry omits the contract's declared registry edit", false, "record the derived registry edit")
	}
	if change.RegistryBefore != nil {
		if err := validateWorkflowLawRegistrySnapshot(change.RegistryBefore); err != nil {
			return err
		}
	}
	if change.Version == 2 {
		if change.Serious || change.Crucial || change.PredecessorSerious || change.RemovesUserVisibleBehavior || change.TouchesSecurityOrDataSafe || len(change.DomainIDs) != 0 {
			return newFailure(KindInvalidPayload, "fold_event", "current law_change carries retired seriousness facts", false, "record law scope and the contract's decision classes")
		}
	} else if change.Version != 0 {
		return newFailure(KindInvalidPayload, "fold_event", "unknown law_change version", false, "use the current law-change shape")
	}
	return validateWorkflowLawChangeRemovals(change, "fold_event")
}

func validateWorkflowLawBeforeStates(before []workflowLawState) error {
	if before == nil || !slices.IsSortedFunc(before, func(a, b workflowLawState) int { return strings.Compare(a.LawID, b.LawID) }) {
		return newFailure(KindInvalidPayload, "fold_event", "law_change law_before must be a list sorted by law_id", false, "record the derived before-states")
	}
	for index, state := range before {
		if index > 0 && before[index-1].LawID == state.LawID {
			return newFailure(KindInvalidPayload, "fold_event", "law_change law_before names "+state.LawID+" twice", false, "record one before-state per law")
		}
		if state.DomainIDs == nil || !slices.IsSorted(state.DomainIDs) || len(slices.Compact(slices.Clone(state.DomainIDs))) != len(state.DomainIDs) {
			return newFailure(KindInvalidPayload, "fold_event", "law_change before-state Domains of "+state.LawID+" must be a sorted unique list", false, "record the derived before-state")
		}
		if state.Absent != (state.ContentHash == "") || state.Absent && (state.Tier != "" || len(state.DomainIDs) != 0) {
			return newFailure(KindInvalidPayload, "fold_event", "law_change before-state of "+state.LawID+" is neither absent nor a content hash", false, "record the derived before-state")
		}
		if !state.Absent {
			if err := validateContentHash(state.ContentHash); err != nil {
				return newFailure(KindInvalidPayload, "fold_event", "law_change before-state of "+state.LawID+" carries no sha256 content hash", false, "record the derived before-state")
			}
		}
	}
	return nil
}

// The live guard refuses altered own scope or a substituted start snapshot.
func validateLiveWorkflowLawChangeTx(ctx context.Context, tx *sql.Tx, workID string, p workflowContractApprovedPayload) error {
	if p.DecisionDomains == nil && p.LawChange == nil {
		return nil
	}
	expected := WorkflowLawChange{}
	if err := deriveWorkflowLawChangeScopeTx(ctx, tx, workID, &expected, p.LawModifies, p.ArchitectureBinding); err != nil {
		return err
	}
	if p.LawChange == nil {
		if len(expected.LawIDs) != 0 || expected.EditsRegistry {
			return newFailure(KindInvalidPayload, "fold_event", "a successor must carry the law change its predecessors recorded; narrowing a successor never erases a change already made", false, "record the item's cumulative law change")
		}
		return nil
	}
	recorded := p.LawChange
	if recorded.Version != 2 || !slices.Equal(expected.LawIDs, recorded.LawIDs) || expected.EditsRegistry != recorded.EditsRegistry {
		return newFailure(KindInvalidPayload, "fold_event", "law_change does not match the item's start baseline and current law scope", false, "derive law_change from the item's baseline and current state")
	}
	if !reflect.DeepEqual(expected.RegistryBefore, recorded.RegistryBefore) || !reflect.DeepEqual(expected.LawBefore, recorded.LawBefore) {
		return newFailure(KindInvalidPayload, "fold_event", "law_change before-state differs from the owning-start baseline", false, "carry the before-states recorded at the item's owning start")
	}
	return nil
}

// Every enacted law and registry delta must lie inside the active scope.
// A merged edit is excluded only by a completed peer's recorded enactment or
// by Git proof that the reviewed candidate's history never made it.
// Law revisions must carry the derived tier. A declared removal must be absent
// from the verified inventory.
func checkWorkflowLawChangeEnactmentTx(ctx context.Context, tx *sql.Tx, workID string, change *WorkflowLawChange, binding *WorkflowArchitectureBinding) error {
	before, after, declaredLaws, err := readWorkflowLawAttributedBaselineTx(ctx, tx, workID, change)
	if err != nil {
		return err
	}
	for _, id := range workflowLawInventoryDifferences(before, after) {
		if !declaredLaws[id] {
			return newFailure(KindOutcomeMismatch, "complete_workflow", "law "+id+" changed outside the active contract (CD-0230 D4)", false, "wait for the actual owner's verified completion; for this item's own change, restore the law or supersede the contract to declare it")
		}
	}
	if err := checkWorkflowLawRegistryScope(before.Registry, after.Registry, binding); err != nil {
		return err
	}
	if change == nil {
		return nil
	}
	current := workflowLawInventoryMap(after)
	for _, reference := range change.Removals {
		canonical, err := workflowLawInventoryReference(reference, before, after)
		if err != nil {
			return err
		}
		if _, present := current[canonical]; present {
			return newFailure(KindOutcomeMismatch, "complete_workflow", "law declared for removal is still present in the Git projection: "+reference, false, "publish and rebuild the declared law removal")
		}
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	resolutions, err := resolveWorkflowChangedLawsTx(ctx, tx, homeProjectID, homeLocatorID, change.LawIDs)
	if err != nil {
		return err
	}
	for _, reference := range change.LawIDs {
		if slices.Contains(change.Removals, reference) {
			continue
		}
		resolution, ok := resolutions[reference]
		if !ok {
			return newFailure(KindMissingEvidence, "complete_workflow", "the enacted law revision is missing from the Git projection: "+reference, false, "publish and rebuild the enacted law revision, or supersede the contract to declare its intended law_change_removals")
		}
		if resolution.tier != "derived" {
			return newFailure(KindMissingEvidence, "complete_workflow", fmt.Sprintf("law %s must carry the derived tier, not %s (CD-0230 D4)", reference, resolution.tier), false, "record authority.tier derived on the changed law")
		}
	}
	return nil
}

// productBinding returns the architecture binding only for a Product-changing
// contract; any other contract changes no registry and adds no law.
func productBinding(productChanging bool, binding *WorkflowArchitectureBinding) *WorkflowArchitectureBinding {
	if !productChanging {
		return nil
	}
	return binding
}
