package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// WorkflowLawChange is a consequence_v1 contract's record of the law it
// changes (CD-0230 D4). The agent supplies the reason and two of the P12
// declarations; the UI/UX declaration is the contract's ui_ux decision
// domain. The core derives the changed laws, the child Domains, and the size
// from recorded state and writes them into the event, so replay never re-reads
// the live law projection.
//
// The before-state belongs to the work item, not to one contract version. On
// a Product-changing definition the item's first law-change approval snapshots
// the Domain registry; any other definition edits no registry and records no
// registry before-state. The first approval that names a law snapshots that
// law. Every successor carries both. A law is changed only when it differs
// from its before-state. LawIDs holds the laws the contract declares and every
// other law the item changed, so a successor drops a declared law only while
// that law is unchanged. Seriousness carries forward once the item has made a
// change. A serious change also pins the exact text the operator approves: the
// after-state content hash of every law in LawIDs, and the Domain registry's
// after-state content hash when the item edits the registry. Completion
// refuses an enacted state that differs.
type WorkflowLawChange struct {
	Reason                     string   `json:"reason"`
	RemovesUserVisibleBehavior bool     `json:"removes_user_visible_behavior"`
	TouchesSecurityOrDataSafe  bool     `json:"touches_security_or_data_safety"`
	LawIDs                     []string `json:"law_ids"`
	DomainIDs                  []string `json:"domain_ids"`
	// Crucial decodes the recorded crucial-size fact of a law change made
	// while the size system existed (CD-0242 D6); no new record sets it and
	// seriousness no longer derives from it.
	Crucial             bool                         `json:"crucial"`
	PredecessorSerious  bool                         `json:"predecessor_serious,omitempty"`
	Serious             bool                         `json:"serious"`
	EditsRegistry       bool                         `json:"edits_registry,omitempty"`
	TextPins            []WorkflowLawRevision        `json:"text_pins,omitempty"`
	RegistryContentHash string                       `json:"registry_content_hash,omitempty"`
	Approval            *workflowLawChangeApproval   `json:"approval,omitempty"`
	LawBefore           []workflowLawState           `json:"law_before"`
	RegistryBefore      *workflowLawRegistrySnapshot `json:"registry_before"`
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
	if change == nil || !change.Serious {
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
	if !change.Serious {
		if change.Approval != nil {
			return newFailure(KindInvalidPayload, "fold_event", "a non-serious law change carries no operator approval proof", false, "record the reason without claiming approval")
		}
		return nil
	}
	proof := change.Approval
	if proof == nil || proof.ApprovalRef == "" || !validDigest(proof.OperationDigest) || proof.ScopeJSON == "" || proof.VersionsJSON == "" || proof.Consequence == "" {
		return newFailure(KindApprovalRequired, "fold_event", "a serious law change requires the consumed operator approval binding", false, "request approval for the exact proposed law text")
	}
	if _, err := WorkflowActorRef(proof.Operator); err != nil || proof.Operator.ActorClass != ActorOperator || proof.Operator.AgentRef != "approval:"+proof.ApprovalRef {
		return newFailure(KindUnauthorized, "fold_event", "a serious law change requires the consumed approval's operator identity", false, "use the verified operator approval identity")
	}
	return nil
}

const (
	workflowLawChangeReasonField       = "law_change_reason"
	workflowLawChangeBehaviorField     = "law_change_removes_user_visible_behavior"
	workflowLawChangeSecurityField     = "law_change_touches_security_or_data_safety"
	workflowLawChangeTextPinsField     = "law_change_text_pins"
	workflowLawChangeRegistryHashField = "law_change_registry_content_hash"
	workflowLawChangeReasonMaxBytes    = 1024
	workflowLawChangeTextPinsMax       = 64
	workflowLawChangeTextPinSchema     = "workflow_law_text_pin"
)

// workflowLawChangeFields declares the consequence_v1 law-change inputs a
// contract or successor payload may carry. They are optional at the payload
// level: the constructor requires the reason and both P12 declarations
// exactly when the contract changes law, and the text pins exactly when the
// change is serious.
func workflowLawChangeFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionStringField(workflowLawChangeReasonField, false, workflowLawChangeReasonMaxBytes),
		{Name: workflowLawChangeBehaviorField, ValueType: PayloadBoolean},
		{Name: workflowLawChangeSecurityField, ValueType: PayloadBoolean},
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
	var baseline *WorkflowLawChange
	if workflowApprovalPolicyRequiresDecisionFact(definition) {
		var err error
		if baseline, err = readWorkflowLawChangeBaselineTx(ctx, q, workID); err != nil {
			return nil, err
		}
	}
	if !workflowApprovalPolicyRequiresDecisionFact(definition) || (!workflowContractChangesLaw(lawModifies, binding) && baseline == nil) {
		if declared {
			return nil, newFailure(KindInvalidPayload, "workflow_law_change", "law_change fields are admitted only on a consequence_v1 contract that changes law", false, "omit the law_change fields")
		}
		return nil, nil
	}
	reason, _ := workflowFieldString(fields, workflowLawChangeReasonField)
	if strings.TrimSpace(reason) == "" || len(reason) > workflowLawChangeReasonMaxBytes {
		return nil, newFailure(KindInvalidPayload, "workflow_law_change", "every contract of a work item that changes law must record why (CD-0230 D4)", false, "supply law_change_reason")
	}
	for _, name := range []string{workflowLawChangeBehaviorField, workflowLawChangeSecurityField} {
		raw := strings.TrimSpace(string(fields[name]))
		if raw != "true" && raw != "false" {
			return nil, newFailure(KindInvalidPayload, "workflow_law_change", "a law change requires an explicit boolean declaration for "+name, false, "declare each P12 test as true or false")
		}
	}
	change := &WorkflowLawChange{
		Reason:                     reason,
		RemovesUserVisibleBehavior: workflowFieldBool(fields, workflowLawChangeBehaviorField),
		TouchesSecurityOrDataSafe:  workflowFieldBool(fields, workflowLawChangeSecurityField),
	}
	productChanging := definition.ChangesProductTruth != nil && *definition.ChangesProductTruth
	if err := deriveWorkflowLawChangeCountTx(ctx, q, workID, change, baseline, lawModifies, binding, productChanging); err != nil {
		return nil, err
	}
	change.Serious = workflowLawChangeSerious(change, decisionDomains)
	var err error
	if change.TextPins, change.RegistryContentHash, err = workflowLawChangeSubmittedPins(fields); err != nil {
		return nil, err
	}
	if err := validateWorkflowLawChangePins(change, "workflow_law_change"); err != nil {
		return nil, err
	}
	return change, nil
}

// deriveWorkflowLawChangeCountTx fills the derived facts of one law change
// from the item's baseline and current state: the before-states (inherited,
// or snapshot now for each newly named law and, on a Product-changing
// definition, the registry at the item's first approval), the laws declared or
// changed, whether the item edits the registry, the counted child Domains, the
// size, and the inherited seriousness.
func deriveWorkflowLawChangeCountTx(ctx context.Context, q queryer, workID string, change, baseline *WorkflowLawChange, lawModifies []string, binding *WorkflowArchitectureBinding, productChanging bool) error {
	declared := workflowChangedLawIDs(lawModifies, binding)
	before := map[string]workflowLawState{}
	if baseline != nil {
		for _, state := range baseline.LawBefore {
			before[state.LawID] = state
		}
		change.RegistryBefore = baseline.RegistryBefore
	}
	tracked := slices.Clone(declared)
	for lawID := range before {
		tracked = append(tracked, lawID)
	}
	states, err := readWorkflowLawStatesTx(ctx, q, workID, uniqueSorted(tracked))
	if err != nil {
		return err
	}
	for _, lawID := range declared {
		if _, known := before[lawID]; !known {
			before[lawID] = states[lawID]
		}
	}
	if productChanging && change.RegistryBefore == nil {
		if change.RegistryBefore, err = readWorkflowLawRegistrySnapshot(ctx, q, workID); err != nil {
			return err
		}
	}
	registryChanged, err := workflowLawRegistryDiffTx(ctx, q, workID, change.RegistryBefore)
	if err != nil {
		return err
	}
	made := len(registryChanged) != 0
	laws := declared
	change.LawBefore = make([]workflowLawState, 0, len(before))
	var prior []string
	for _, lawID := range uniqueSorted(tracked) {
		state := before[lawID]
		change.LawBefore = append(change.LawBefore, state)
		if state.differs(states[lawID]) {
			made = true
			laws = append(laws, lawID)
		}
	}
	change.LawIDs = uniqueSorted(laws)
	for _, lawID := range change.LawIDs {
		prior = append(prior, before[lawID].DomainIDs...)
	}
	change.EditsRegistry = workflowContractEditsRegistry(binding) || len(registryChanged) != 0
	change.PredecessorSerious = baseline != nil && baseline.Serious && made
	domains, err := workflowLawChangeDomainsTx(ctx, q, workID, change.LawIDs, binding, prior)
	if err != nil {
		return err
	}
	change.DomainIDs = uniqueSorted(append(domains, registryChanged...))
	return nil
}

// readWorkflowLawStatesTx reads the current state of each law: absent when
// the projection holds no revision of it.
func readWorkflowLawStatesTx(ctx context.Context, q queryer, workID string, laws []string) (map[string]workflowLawState, error) {
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	resolutions, err := resolveWorkflowChangedLawsTx(ctx, q, homeProjectID, homeLocatorID, laws)
	if err != nil {
		return nil, err
	}
	states := make(map[string]workflowLawState, len(laws))
	for _, lawID := range laws {
		resolution, present := resolutions[lawID]
		if !present {
			states[lawID] = workflowLawState{LawID: lawID, Absent: true, DomainIDs: []string{}}
			continue
		}
		domains, err := readWorkflowLawDomainsTx(ctx, q, resolution)
		if err != nil {
			return nil, err
		}
		states[lawID] = workflowLawState{LawID: lawID, ContentHash: resolution.hash, Tier: resolution.tier, DomainIDs: domains}
	}
	return states, nil
}

// workflowLawRegistryDiffTx lists the child Domains whose registry entry
// or relation differs from the recorded before-state.
func workflowLawRegistryDiffTx(ctx context.Context, q queryer, workID string, before *workflowLawRegistrySnapshot) ([]string, error) {
	if before == nil {
		return nil, nil
	}
	after, err := readWorkflowLawRegistrySnapshot(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	return workflowLawRegistryChangedDomains(before, after), nil
}

// readWorkflowLawChangeBaselineTx joins the law changes recorded on the
// item's active contracts, the predecessors a successor supersedes, and
// returns nil when none recorded one. Each before-state is the one recorded
// first, so active contracts that disagree on one are an invariant violation.
func readWorkflowLawChangeBaselineTx(ctx context.Context, q queryer, workID string) (*WorkflowLawChange, error) {
	rows, err := q.QueryContext(ctx, `SELECT law_change FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL AND law_change IS NOT NULL ORDER BY contract_version`, workID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read the item's recorded law change")
	}
	defer func() { _ = rows.Close() }()
	var baseline *WorkflowLawChange
	before := map[string]workflowLawState{}
	disagree := newFailure(KindInvariantViolation, "workflow_law_change", "the item's active contracts record different before-states", false, "rebuild projections from the event log")
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, workflowProjectionError(err, "cannot read the item's recorded law change")
		}
		var recorded WorkflowLawChange
		if err := json.Unmarshal([]byte(encoded), &recorded); err != nil {
			return nil, newFailure(KindInvariantViolation, "workflow_law_change", "the item's recorded law change is malformed", false, "rebuild projections from the event log")
		}
		if baseline == nil {
			baseline = &WorkflowLawChange{RegistryBefore: recorded.RegistryBefore}
		} else if !reflect.DeepEqual(baseline.RegistryBefore, recorded.RegistryBefore) {
			return nil, disagree
		}
		baseline.Serious = baseline.Serious || recorded.Serious
		for _, state := range recorded.LawBefore {
			if known, found := before[state.LawID]; found && !reflect.DeepEqual(known, state) {
				return nil, disagree
			}
			before[state.LawID] = state
		}
	}
	if err := rows.Err(); err != nil {
		return nil, workflowProjectionError(err, "cannot finish reading the item's recorded law change")
	}
	if baseline != nil {
		for _, lawID := range slices.Sorted(maps.Keys(before)) {
			baseline.LawBefore = append(baseline.LawBefore, before[lawID])
		}
	}
	return baseline, nil
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

// validateWorkflowLawChangePins holds the pins to the change they approve. It
// reads nothing, so the constructor and replay apply one rule. A serious
// change pins every changed law of the item exactly once, sorted by law ID,
// and pins the registry exactly when the item's change edits the registry. A
// non-serious change carries no pins: no operator approves its text, and
// completion holds it to the derived tier instead.
func validateWorkflowLawChangePins(change *WorkflowLawChange, op string) error {
	if !change.Serious {
		if change.TextPins != nil || change.RegistryContentHash != "" {
			return newFailure(KindInvalidPayload, op, "law_change text pins are recorded only on a serious law change; a non-serious change carries the derived tier instead", false, "omit law_change_text_pins and law_change_registry_content_hash")
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
		return newFailure(KindInvalidPayload, op, fmt.Sprintf("a serious law change must pin the approved text of exactly each changed law: changed [%s], pinned [%s] (CD-0230 D4)", strings.Join(change.LawIDs, ", "), strings.Join(pinned, ", ")), false, "draft the law text first, then pin each changed law's content hash once")
	}
	if change.EditsRegistry != (change.RegistryContentHash != "") {
		return newFailure(KindInvalidPayload, op, "a serious law change pins the Domain registry content hash exactly when it edits the registry (CD-0230 D4)", false, "pin the drafted registry's content hash only for a registry edit")
	}
	if change.RegistryContentHash != "" {
		if err := validateContentHash(change.RegistryContentHash); err != nil {
			return newFailure(KindInvalidPayload, op, "law_change_registry_content_hash is not a sha256 content hash", false, "pin the drafted registry's sha256 content hash")
		}
	}
	return nil
}

// workflowLawChangeSerious applies P12 to one law change: more than one child
// Domain, a declared removal of user-visible behavior, a declared security or
// data-safety change, a UI/UX change, a recorded crucial-size fact on a
// stored law change, or a predecessor whose change was serious: narrowing a
// successor never erases the consent obligation of a change already made.
func workflowLawChangeSerious(change *WorkflowLawChange, decisionDomains []string) bool {
	return len(change.DomainIDs) > 1 || change.RemovesUserVisibleBehavior || change.TouchesSecurityOrDataSafe || change.Crucial || change.PredecessorSerious || slices.Contains(decisionDomains, "ui_ux")
}

// workflowLawChangeDomainsTx counts the child Domains of a law change from
// recorded state (CD-0230 D4): each changed law's home and applicability, each
// added law's declared home, each modified registry Domain, both ends of each
// modified relation, and the Domains already recorded in prior. The registry's
// root Domain is not counted. A reference record is not law and is never in
// law_subjects, so it adds nothing.
func workflowLawChangeDomainsTx(ctx context.Context, q queryer, workID string, laws []string, binding *WorkflowArchitectureBinding, prior []string) ([]string, error) {
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return nil, err
	}
	var root string
	if err := q.QueryRowContext(ctx, `SELECT root_domain_id FROM domain_registries WHERE product_id=?`, productID).Scan(&root); err != nil && err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot read the Domain registry root", true, "retry once the Domain registry is readable", err)
	}
	domains := slices.Clone(prior)
	if binding != nil {
		for _, addition := range binding.LawAdditions {
			domains = append(domains, addition.HomeDomainID)
		}
		domains = append(domains, binding.DomainModifies...)
		for _, relation := range binding.DomainRelationModifies {
			domains = append(domains, relation.SourceDomainID, relation.TargetDomainID)
		}
	}
	resolutions, err := resolveWorkflowChangedLawsTx(ctx, q, homeProjectID, homeLocatorID, laws)
	if err != nil {
		return nil, err
	}
	for _, resolution := range resolutions {
		lawDomains, err := readWorkflowLawDomainsTx(ctx, q, resolution)
		if err != nil {
			return nil, err
		}
		domains = append(domains, lawDomains...)
	}
	domains = slices.DeleteFunc(domains, func(domainID string) bool { return domainID == root })
	slices.Sort(domains)
	return append([]string{}, slices.Compact(domains)...), nil
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
// reads nothing, so replay folds recorded bytes under it. productChanging is
// the pinned definition's Product-changing flag.
func validateWorkflowLawChangeShape(change *WorkflowLawChange, decisionDomains, lawModifies []string, binding *WorkflowArchitectureBinding, productChanging bool) error {
	if strings.TrimSpace(change.Reason) == "" || len(change.Reason) > workflowLawChangeReasonMaxBytes {
		return newFailure(KindInvalidPayload, "fold_event", "law_change reason must be 1 to 1024 bytes", false, "record why the law changes")
	}
	for name, ids := range map[string][]string{"domain_ids": change.DomainIDs, "law_ids": change.LawIDs} {
		if ids == nil || !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			return newFailure(KindInvalidPayload, "fold_event", "law_change "+name+" must be a sorted unique list", false, "record the derived "+name)
		}
	}
	for _, lawID := range workflowChangedLawIDs(lawModifies, binding) {
		if _, found := slices.BinarySearch(change.LawIDs, lawID); !found {
			return newFailure(KindInvalidPayload, "fold_event", "law_change law_ids omits the declared law "+lawID, false, "record every law the contract declares")
		}
	}
	if change.LawBefore == nil || !slices.IsSortedFunc(change.LawBefore, func(a, b workflowLawState) int { return strings.Compare(a.LawID, b.LawID) }) {
		return newFailure(KindInvalidPayload, "fold_event", "law_change law_before must be a list sorted by law_id", false, "record the derived before-states")
	}
	for index, state := range change.LawBefore {
		if index > 0 && change.LawBefore[index-1].LawID == state.LawID {
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
	for _, lawID := range change.LawIDs {
		if !slices.ContainsFunc(change.LawBefore, func(state workflowLawState) bool { return state.LawID == lawID }) {
			return newFailure(KindInvalidPayload, "fold_event", "law_change records no before-state for "+lawID, false, "record the before-state of every counted law")
		}
	}
	if len(change.LawBefore) == 0 && change.RegistryBefore == nil {
		return newFailure(KindInvalidPayload, "fold_event", "law_change records no law and no registry before-state", false, "omit law_change on a contract whose item changes no law")
	}
	if change.Serious != workflowLawChangeSerious(change, decisionDomains) {
		return newFailure(KindInvalidPayload, "fold_event", "law_change seriousness does not follow its recorded facts", false, "record the derived seriousness")
	}
	if workflowContractEditsRegistry(binding) && !change.EditsRegistry {
		return newFailure(KindInvalidPayload, "fold_event", "law_change edits_registry omits the contract's declared registry edit", false, "record the derived registry edit")
	}
	if !productChanging {
		if change.RegistryBefore != nil || change.EditsRegistry {
			return newFailure(KindInvalidPayload, "fold_event", "law_change on a definition that does not change Product truth records no registry before-state and no registry edit", false, "omit the registry facts")
		}
	} else if change.RegistryBefore == nil {
		return newFailure(KindInvalidPayload, "fold_event", "law_change must record the registry before-state of the item's first law-change approval", false, "derive the registry before-state from the Product projection")
	} else if err := validateWorkflowLawRegistrySnapshot(change.RegistryBefore); err != nil {
		return err
	}
	if err := validateWorkflowLawChangePins(change, "fold_event"); err != nil {
		return err
	}
	return validateWorkflowLawChangeApproval(change)
}

// validateLiveWorkflowLawChangeTx re-derives the counted facts from the item's
// baseline and current state at a live fold and refuses a recorded fact that
// differs, so no writer can lower the count the core derives or drop a change
// a predecessor recorded. Replay folds recorded bytes instead.
func validateLiveWorkflowLawChangeTx(ctx context.Context, tx *sql.Tx, workID string, p workflowContractApprovedPayload, productChanging bool) error {
	baseline, err := readWorkflowLawChangeBaselineTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if p.LawChange == nil {
		if baseline != nil {
			return newFailure(KindInvalidPayload, "fold_event", "a successor must carry the law change its predecessors recorded; narrowing a successor never erases a change already made", false, "record the item's cumulative law change")
		}
		return nil
	}
	expected := WorkflowLawChange{}
	if err := deriveWorkflowLawChangeCountTx(ctx, tx, workID, &expected, baseline, p.LawModifies, p.ArchitectureBinding, productChanging); err != nil {
		return err
	}
	recorded := p.LawChange
	if !slices.Equal(expected.LawIDs, recorded.LawIDs) || !slices.Equal(expected.DomainIDs, recorded.DomainIDs) || expected.PredecessorSerious != recorded.PredecessorSerious || expected.EditsRegistry != recorded.EditsRegistry {
		return newFailure(KindInvalidPayload, "fold_event", "law_change does not match the item's recorded law change and law Domains", false, "derive law_change from the item's baseline and current state")
	}
	if !reflect.DeepEqual(expected.RegistryBefore, recorded.RegistryBefore) || !reflect.DeepEqual(expected.LawBefore, recorded.LawBefore) {
		return newFailure(KindInvalidPayload, "fold_event", "law_change before-state differs from the one the item recorded first", false, "carry the before-states recorded at the item's first law-change approval")
	}
	if recorded.Serious {
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

// checkWorkflowLawChangeEnactmentTx is the completion half of CD-0230 D4.
// Every enacted difference from the item's before-state must lie inside the
// active contract: each changed law is in LawIDs, and on a Product-changing
// definition each changed registry Domain is one the contract's binding
// declares. No merged edit of another item is excluded, because nothing
// records which item made it. A serious change must then enact exactly the
// text the operator approved. A non-serious change must still be non-serious
// once enacted: the child Domains the counted laws
// hold now, joined with those recorded at approval and every registry
// difference, number at most one, and each counted law revision carries the
// derived tier, whatever tier it held before, including a D5 cohort default.
func checkWorkflowLawChangeEnactmentTx(ctx context.Context, tx *sql.Tx, workID string, change *WorkflowLawChange, binding *WorkflowArchitectureBinding) error {
	if change == nil {
		return nil
	}
	tracked := make([]string, 0, len(change.LawBefore))
	for _, state := range change.LawBefore {
		tracked = append(tracked, state.LawID)
	}
	states, err := readWorkflowLawStatesTx(ctx, tx, workID, tracked)
	if err != nil {
		return err
	}
	for _, before := range change.LawBefore {
		if _, counted := slices.BinarySearch(change.LawIDs, before.LawID); !counted && before.differs(states[before.LawID]) {
			return newFailure(KindOutcomeMismatch, "complete_workflow", "law "+before.LawID+" changed, but the active contract no longer declares it (CD-0230 D4)", false, "restore the law, or supersede the contract to declare it")
		}
	}
	registryChanged, err := workflowLawRegistryDiffTx(ctx, tx, workID, change.RegistryBefore)
	if err != nil {
		return err
	}
	var declared []string
	if binding != nil {
		declared = append(declared, binding.DomainModifies...)
		for _, relation := range binding.DomainRelationModifies {
			declared = append(declared, relation.SourceDomainID, relation.TargetDomainID)
		}
	}
	if undeclared := slices.DeleteFunc(slices.Clone(registryChanged), func(domainID string) bool { return slices.Contains(declared, domainID) }); len(undeclared) != 0 {
		return newFailure(KindOutcomeMismatch, "complete_workflow", fmt.Sprintf("the enacted Domain registry changes %s, which the active contract does not declare (CD-0230 D4)", strings.Join(undeclared, ", ")), false, "restore the registry entries, or supersede the contract to declare them")
	}
	if change.Serious {
		return checkWorkflowLawChangePinsEnactedTx(ctx, tx, workID, change)
	}
	domains, err := workflowLawChangeDomainsTx(ctx, tx, workID, change.LawIDs, nil, change.DomainIDs)
	if err != nil {
		return err
	}
	if domains = uniqueSorted(append(domains, registryChanged...)); len(domains) > 1 {
		return newFailure(KindApprovalRequired, "complete_workflow", fmt.Sprintf("the enacted law change affects child Domains %s, so it is serious and needs the operator's approval of its exact text (CD-0230 D4)", strings.Join(domains, ", ")), false, "supersede the contract with the operator's approval of the exact text")
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

// workflowProposedLawChangeSeriousTx answers the consent boundary's law-change
// fact for one approve_contract or supersede_contract payload: whether the
// contract the constructor would build makes a serious law change. It derives
// the same fact the constructor records, from the same fields.
func workflowProposedLawChangeSeriousTx(ctx context.Context, q queryer, definition WorkflowDefinition, workID, role string, fields map[string]json.RawMessage) (bool, error) {
	if !workflowApprovalPolicyRequiresDecisionFact(definition) {
		return false, nil
	}
	productChanging := definition.ChangesProductTruth != nil && *definition.ChangesProductTruth
	var binding *WorkflowArchitectureBinding
	if productChanging {
		parsed, err := parseWorkflowArchitectureBinding(fields["architecture_binding"])
		if err != nil {
			return false, err
		}
		binding = parsed
	}
	decisionDomains, err := workflowSubmittedDecisionDomains(definition, role, fields)
	if err != nil {
		return false, err
	}
	change, err := deriveWorkflowLawChangeTx(ctx, q, definition, workID, fields, workflowFieldStrings(fields, "law_modifies"), binding, decisionDomains)
	if err != nil {
		return false, err
	}
	return change != nil && change.Serious, nil
}
