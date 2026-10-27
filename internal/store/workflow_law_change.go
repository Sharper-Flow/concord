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
// Consent follows the contract's decision domains, never size or seriousness.
// Version zero's retired descriptive fields remain decodable, not authoritative
// for new consent or a substitute for a verified owning-start baseline.
type WorkflowLawChange struct {
	Version                    int                          `json:"version,omitempty"`
	Reason                     string                       `json:"reason"`
	RemovesUserVisibleBehavior bool                         `json:"removes_user_visible_behavior,omitempty"`
	TouchesSecurityOrDataSafe  bool                         `json:"touches_security_or_data_safety,omitempty"`
	LawIDs                     []string                     `json:"law_ids"`
	DomainIDs                  []string                     `json:"domain_ids,omitempty"`
	Crucial                    bool                         `json:"crucial,omitempty"`
	PredecessorSerious         bool                         `json:"predecessor_serious,omitempty"`
	Serious                    bool                         `json:"serious,omitempty"`
	EditsRegistry              bool                         `json:"edits_registry,omitempty"`
	TextPins                   []WorkflowLawRevision        `json:"text_pins,omitempty"`
	RegistryContentHash        string                       `json:"registry_content_hash,omitempty"`
	Approval                   *workflowLawChangeApproval   `json:"approval,omitempty"`
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

// The consent proof comes from the existing consumed approval, not from the
// action payload. The coordinator still authors the contract; this tuple
// records the human who approved its text without substituting the executor.
type workflowLawChangeApproval struct {
	workflowApprovalBinding
	Operator WorkflowActor `json:"operator"`
}

func bindWorkflowLawChangeApprovalTx(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, change *WorkflowLawChange) error {
	if change == nil || len(change.TextPins) == 0 && change.RegistryContentHash == "" {
		return nil
	}
	binding := workflowApprovalBinding{
		ApprovalRef: request.OperatorApprovalRef, OperationDigest: request.ApprovalOperationDigest,
		ScopeJSON: request.ApprovalScopeJSON, VersionsJSON: request.ApprovalVersionsJSON, Consequence: request.ApprovalConsequence,
	}
	operator, err := workflowOperatorFromConsumedApprovalTx(ctx, tx, binding, "workflow_law_change")
	if err != nil {
		return err
	}
	change.Approval = &workflowLawChangeApproval{workflowApprovalBinding: binding, Operator: operator.WorkflowActor}
	return nil
}

func validateWorkflowLawChangeApproval(change *WorkflowLawChange) error {
	if change.Approval == nil && len(change.TextPins) == 0 && change.RegistryContentHash == "" {
		return nil
	}
	proof := change.Approval
	if proof == nil || proof.ApprovalRef == "" || !validDigest(proof.OperationDigest) || proof.ScopeJSON == "" || proof.VersionsJSON == "" || proof.Consequence == "" {
		return newFailure(KindApprovalRequired, "fold_event", "a classified law change requires the consumed operator approval binding", false, "request approval for the exact proposed law text")
	}
	if _, err := WorkflowActorRef(proof.Operator); err != nil || proof.Operator.ActorClass != ActorOperator || proof.Operator.AgentRef != "approval:"+proof.ApprovalRef {
		return newFailure(KindUnauthorized, "fold_event", "a classified law change requires the consumed approval's operator identity", false, "use the verified operator approval identity")
	}
	return nil
}

const (
	workflowLawChangeReasonField       = "law_change_reason"
	workflowLawChangeTextPinsField     = "law_change_text_pins"
	workflowLawChangeRegistryHashField = "law_change_registry_content_hash"
	workflowLawChangeReasonMaxBytes    = 1024
	workflowLawChangeTextPinsMax       = 64
	workflowLawChangeTextPinSchema     = "workflow_law_text_pin"
)

func workflowLawChangeFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionStringField(workflowLawChangeReasonField, false, workflowLawChangeReasonMaxBytes),
		actionItemArrayField(workflowLawChangeTextPinsField, false, 1, workflowLawChangeTextPinsMax, workflowLawChangeTextPinSchema),
		actionStringField(workflowLawChangeRegistryHashField, false, 71),
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
// contract or successor. It returns nil for a legacy definition, or for a
// contract that changes no law on an item whose active contracts recorded no
// law change, and refuses a law-changing contract without a reason. binding
// is the Product-changing contract's architecture binding, else nil.
func deriveWorkflowLawChangeTx(ctx context.Context, q queryer, definition WorkflowDefinition, workID string, fields map[string]json.RawMessage, lawModifies []string, binding *WorkflowArchitectureBinding, decisionDomains []string) (*WorkflowLawChange, error) {
	declared := false
	for _, field := range workflowLawChangeFields() {
		if _, present := fields[field.Name]; present {
			declared = true
		}
	}
	if !workflowApprovalPolicyRequiresDecisionFact(definition) {
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
	consent, err := classifyConsequenceV1Decision(definition, "approve_contract", "proposed", decisionDomains)
	if err != nil {
		return nil, err
	}
	if change.TextPins, change.RegistryContentHash, err = workflowLawChangeSubmittedPins(fields); err != nil {
		return nil, err
	}
	if err := validateWorkflowLawChangePins(change, "workflow_law_change", consent.Consent == WorkflowConsentRequired); err != nil {
		return nil, err
	}
	return change, nil
}

// All changed source laws must join the declared scope. No other item's edit
// is excluded without stored attribution. The start snapshot is never refreshed.
func deriveWorkflowLawChangeScopeTx(ctx context.Context, q queryer, workID string, change *WorkflowLawChange, lawModifies []string, binding *WorkflowArchitectureBinding) error {
	before, after, err := readWorkflowLawBaselineTx(ctx, q, workID)
	if err != nil {
		return err
	}
	declared := workflowChangedLawIDs(lawModifies, binding)
	old := workflowLawInventoryMap(before)
	additions := map[string]bool{}
	if binding != nil {
		for _, addition := range binding.LawAdditions {
			canonical, err := workflowLawInventoryReference(addition.LawID, before, after)
			if err != nil {
				return err
			}
			additions[canonical] = true
		}
	}
	aliases := map[string]string{}
	for _, reference := range declared {
		canonical, err := workflowLawInventoryReference(reference, before, after)
		if err != nil {
			return err
		}
		if previous, exists := aliases[canonical]; exists && previous != reference {
			return newFailure(KindInvalidPayload, "workflow_law_change", "one law is declared through multiple source aliases", false, "declare each source law once")
		}
		_, existed := old[canonical]
		if !existed && !additions[canonical] {
			return newFailure(KindOutcomeMismatch, "workflow_law_change", "law "+reference+" was absent at the owning start and requires a declared law_additions entry", false, "declare the new law and its home in a Product-changing contract")
		}
		if existed && additions[canonical] {
			return newFailure(KindInvalidPayload, "workflow_law_change", "law addition already existed at the owning start: "+reference, false, "declare an existing law through law_modifies")
		}
		aliases[canonical] = reference
	}
	laws := slices.Clone(declared)
	for _, canonical := range workflowLawInventoryDifferences(before, after) {
		reference, exists := aliases[canonical]
		if !exists {
			return newFailure(KindOutcomeMismatch, "workflow_law_change", "law "+canonical+" changed outside the declared law scope", false, "wait for the actual owner's verified completion; for this item's own change, restore the law or declare it in law_modifies or law_additions")
		}
		laws = append(laws, reference)
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
	change.EditsRegistry = workflowContractEditsRegistry(binding) || !reflect.DeepEqual(before.Registry, after.Registry)
	return nil
}

// workflowLawChangeSubmittedPins decodes the submitted text pins, sorted by
// law ID, and the registry content hash. Absent fields decode to nothing.
func workflowLawChangeSubmittedPins(fields map[string]json.RawMessage) ([]WorkflowLawRevision, string, error) {
	var pins []WorkflowLawRevision
	if raw, present := fields[workflowLawChangeTextPinsField]; present {
		if err := decodeWorkflowActionPayloadStrict(raw, &pins); err != nil || len(pins) == 0 {
			return nil, "", newFailure(KindInvalidPayload, "workflow_law_change", "law_change_text_pins must be a non-empty list of {law_id, content_hash} pins", false, "pin each changed law's approved text by its content hash")
		}
		slices.SortFunc(pins, func(a, b WorkflowLawRevision) int { return strings.Compare(a.LawID, b.LawID) })
	}
	registryHash, _ := workflowFieldString(fields, workflowLawChangeRegistryHashField)
	return pins, registryHash, nil
}

// Classified changes pin exactly their law and registry text. Agent-owned
// changes carry no approval pins and must enact the derived tier instead.
func validateWorkflowLawChangePins(change *WorkflowLawChange, op string, needsApproval bool) error {
	if !needsApproval {
		if change.TextPins != nil || change.RegistryContentHash != "" {
			return newFailure(KindInvalidPayload, op, "law_change text pins require an applicable operator decision class; an agent-owned change carries the derived tier instead", false, "omit law_change_text_pins and law_change_registry_content_hash")
		}
		return nil
	}
	pinned := make([]string, 0, len(change.TextPins))
	for _, pin := range change.TextPins {
		if err := validateContentHash(pin.ContentHash); err != nil {
			return newFailure(KindInvalidPayload, op, "law_change text pin for "+pin.LawID+" carries no sha256 content hash", false, "pin the sha256 content hash of the approved law text")
		}
		pinned = append(pinned, pin.LawID)
	}
	if !slices.Equal(pinned, change.LawIDs) {
		return newFailure(KindInvalidPayload, op, fmt.Sprintf("a classified law change must pin the approved text of exactly each changed law: changed [%s], pinned [%s] (CD-0230 D4)", strings.Join(change.LawIDs, ", "), strings.Join(pinned, ", ")), false, "draft the law text first, then pin each changed law's content hash once")
	}
	if change.EditsRegistry != (change.RegistryContentHash != "") {
		return newFailure(KindInvalidPayload, op, "a classified law change pins the Domain registry content hash exactly when it edits the registry (CD-0230 D4)", false, "pin the drafted registry's content hash only for a registry edit")
	}
	if change.RegistryContentHash != "" {
		if err := validateContentHash(change.RegistryContentHash); err != nil {
			return newFailure(KindInvalidPayload, op, "law_change_registry_content_hash is not a sha256 content hash", false, "pin the drafted registry's sha256 content hash")
		}
	}
	return nil
}

// readWorkflowLawDomainsTx reads the Domains one resolved law is homed in or
// applies to, sorted and unique, the root included.
func readWorkflowLawDomainsTx(ctx context.Context, q queryer, resolution federatedLawResolution) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT domain_id FROM law_domain_homes WHERE home_project_id=? AND home_locator_id=? AND law_id=?
UNION SELECT domain_id FROM law_domain_applicability WHERE home_project_id=? AND home_locator_id=? AND law_id=? ORDER BY domain_id`,
		resolution.source.HomeProjectID, resolution.source.HomeLocatorID, resolution.lawID,
		resolution.source.HomeProjectID, resolution.source.HomeLocatorID, resolution.lawID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot read the changed law's Domains", true, "retry once the law projection is readable", err)
	}
	defer func() { _ = rows.Close() }()
	domains := []string{}
	for rows.Next() {
		var domainID string
		if err := rows.Scan(&domainID); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot decode the changed law's Domains", true, "retry once the law projection is readable", err)
		}
		domains = append(domains, domainID)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot finish reading the changed law's Domains", true, "retry once the law projection is readable", err)
	}
	return domains, nil
}

// resolveWorkflowChangedLawsTx resolves changed law references through the
// same federated identity as the mandated-law boundary. A law the projection
// no longer holds, such as one the change removed, resolves to nothing; the
// approval path has already refused an unknown modified law.
func resolveWorkflowChangedLawsTx(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, laws []string) (map[string]federatedLawResolution, error) {
	if len(laws) == 0 {
		return nil, nil
	}
	sources, err := workflowLawSourceSetForHomeTx(ctx, q, homeProjectID, homeLocatorID)
	if err != nil {
		return nil, err
	}
	if sources == nil {
		sources = []KnowledgeHome{{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}}
	}
	resolutions, _, err := resolveFederatedLawReferences(ctx, q, "workflow_law_change", sources, laws)
	return resolutions, err
}

// validateWorkflowLawChangeShape holds a recorded fact to its closed form. It
// reads nothing, so replay folds recorded bytes under it.
func validateWorkflowLawChangeShape(change *WorkflowLawChange, decisionDomains, lawModifies []string, binding *WorkflowArchitectureBinding) error {
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
	needsApproval := change.Approval != nil
	if change.Version == 2 {
		if change.Serious || change.Crucial || change.PredecessorSerious || change.RemovesUserVisibleBehavior || change.TouchesSecurityOrDataSafe || len(change.DomainIDs) != 0 {
			return newFailure(KindInvalidPayload, "fold_event", "current law_change carries retired seriousness facts", false, "record law scope and the contract's decision classes")
		}
		consent, err := classifyConsequenceV1Decision(WorkflowDefinition{}, "approve_contract", "proposed", decisionDomains)
		if err != nil {
			return err
		}
		needsApproval = consent.Consent == WorkflowConsentRequired
		if !needsApproval && change.Approval != nil {
			return newFailure(KindInvalidPayload, "fold_event", "agent-owned law change cannot claim operator consent", false, "omit the approval proof")
		}
	} else if change.Version != 0 {
		return newFailure(KindInvalidPayload, "fold_event", "unknown law_change version", false, "use the current law-change shape")
	}
	if err := validateWorkflowLawChangePins(change, "fold_event", needsApproval); err != nil {
		return err
	}
	return validateWorkflowLawChangeApproval(change)
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

// The live guard refuses stale scope or a substituted start snapshot.
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
	if recorded.Approval != nil {
		proof := recorded.Approval
		operator, err := workflowOperatorFromConsumedApprovalTx(ctx, tx, proof.workflowApprovalBinding, "workflow_law_change")
		if err != nil {
			return err
		}
		if operator.WorkflowActor != proof.Operator {
			return newFailure(KindUnauthorized, "fold_event", "law_change operator identity differs from the consumed approval", false, "record the verified operator identity")
		}
	}
	return nil
}

// Every enacted law and registry delta must lie inside the active scope.
// No merged edit is excluded without recorded attribution. Operator-approved
// text must match its pins; other law revisions must carry the derived tier.
func checkWorkflowLawChangeEnactmentTx(ctx context.Context, tx *sql.Tx, workID string, change *WorkflowLawChange, binding *WorkflowArchitectureBinding) error {
	before, after, err := readWorkflowLawBaselineTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	declaredLaws := map[string]bool{}
	if change != nil {
		for _, reference := range change.LawIDs {
			canonical, err := workflowLawInventoryReference(reference, before, after)
			if err != nil {
				return err
			}
			declaredLaws[canonical] = true
		}
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
	if change.Approval != nil {
		return checkWorkflowLawChangePinsEnactedTx(ctx, tx, workID, change)
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
		resolution, ok := resolutions[reference]
		if !ok {
			return newFailure(KindMissingEvidence, "complete_workflow", "the enacted law revision is missing from the Git projection: "+reference, false, "publish and rebuild the enacted law revision")
		}
		if resolution.tier != "derived" {
			return newFailure(KindMissingEvidence, "complete_workflow", fmt.Sprintf("law %s, counted in a law change without the operator's approval of its exact text, must carry the derived tier, not %s (CD-0230 D4)", reference, resolution.tier), false, "record authority.tier derived on the changed law")
		}
	}
	return nil
}

// checkWorkflowLawChangePinsEnactedTx compares the approved pins with the Git
// projection. A difference means the enacted text is not the approved text,
// so the change needs a successor contract and a new approval.
func checkWorkflowLawChangePinsEnactedTx(ctx context.Context, tx *sql.Tx, workID string, change *WorkflowLawChange) error {
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	if len(change.TextPins) != 0 {
		sources, err := workflowLawSourceSetForHomeTx(ctx, tx, homeProjectID, homeLocatorID)
		if err != nil {
			return err
		}
		for _, pin := range change.TextPins {
			subject, err := resolveWorkflowLawSubjectTx(ctx, tx, sources, homeProjectID, homeLocatorID, pin.LawID)
			if err != nil {
				var failure *Failure
				if failureAs(err, &failure) && failure.Kind == KindProjectionNotFound {
					return newFailure(KindMissingEvidence, "complete_workflow", "the approved law revision is missing from the Git projection: "+pin.LawID, false, "publish and rebuild the approved law revision")
				}
				return err
			}
			if subject.hash != pin.ContentHash {
				return newFailure(KindApprovalRequired, "complete_workflow", fmt.Sprintf("the enacted text of law %s (%s) is not the text the operator approved (%s) (CD-0230 D4)", pin.LawID, subject.hash, pin.ContentHash), false, "enact the approved text, or supersede the contract with the operator's approval of the new text")
			}
		}
	}
	if change.RegistryContentHash == "" {
		return nil
	}
	productID, _, err := resolveKnowledgeSourceRole(ctx, tx, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return err
	}
	var registryHash string
	if err := tx.QueryRowContext(ctx, `SELECT content_hash FROM domain_registries WHERE product_id=?`, productID).Scan(&registryHash); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindMissingEvidence, "complete_workflow", "the approved Domain registry is missing from the Git projection", false, "publish and rebuild the approved Domain registry")
		}
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot read the enacted Domain registry", true, "retry once the Domain registry is readable", err)
	}
	if registryHash != change.RegistryContentHash {
		return newFailure(KindApprovalRequired, "complete_workflow", fmt.Sprintf("the enacted Domain registry (%s) is not the registry the operator approved (%s) (CD-0230 D4)", registryHash, change.RegistryContentHash), false, "enact the approved registry, or supersede the contract with the operator's approval of the new registry")
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
