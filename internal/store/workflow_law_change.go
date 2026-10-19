package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// WorkflowLawChange is a consequence_v1 contract's record of the law it
// changes (CD-0230 D4). The agent supplies the reason and two of the P12
// declarations; the UI/UX declaration is the contract's ui_ux decision
// domain. The core derives the child Domains and the size from recorded state
// and writes them into the event, so replay never re-reads the live law
// projection. A serious change also pins the exact text the operator
// approves: the after-state content hash of every modified, removed, or added
// law, and the Domain registry's after-state content hash when the change
// edits the registry. Completion refuses an enacted state that differs.
type WorkflowLawChange struct {
	Reason                     string                `json:"reason"`
	RemovesUserVisibleBehavior bool                  `json:"removes_user_visible_behavior"`
	TouchesSecurityOrDataSafe  bool                  `json:"touches_security_or_data_safety"`
	DomainIDs                  []string              `json:"domain_ids"`
	Crucial                    bool                  `json:"crucial"`
	Serious                    bool                  `json:"serious"`
	TextPins                   []WorkflowLawRevision `json:"text_pins,omitempty"`
	RegistryContentHash        string                `json:"registry_content_hash,omitempty"`
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

// workflowContractChangesLaw reports whether a contract modifies or removes
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
// that changes no law, and refuses a law-changing contract without a reason.
// binding is the Product-changing contract's architecture binding, else nil.
func deriveWorkflowLawChangeTx(ctx context.Context, q queryer, definition WorkflowDefinition, workID string, fields map[string]json.RawMessage, lawModifies []string, binding *WorkflowArchitectureBinding, decisionDomains []string) (*WorkflowLawChange, error) {
	declared := false
	for _, field := range workflowLawChangeFields() {
		if _, present := fields[field.Name]; present {
			declared = true
		}
	}
	if !workflowApprovalPolicyRequiresDecisionFact(definition) || !workflowContractChangesLaw(lawModifies, binding) {
		if declared {
			return nil, newFailure(KindInvalidPayload, "workflow_law_change", "law_change fields are admitted only on a consequence_v1 contract that changes law", false, "omit the law_change fields")
		}
		return nil, nil
	}
	reason, _ := workflowFieldString(fields, workflowLawChangeReasonField)
	if strings.TrimSpace(reason) == "" || len(reason) > workflowLawChangeReasonMaxBytes {
		return nil, newFailure(KindInvalidPayload, "workflow_law_change", "a contract that changes law must record why (CD-0230 D4)", false, "supply law_change_reason")
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
	var err error
	if change.DomainIDs, err = workflowLawChangeDomainsTx(ctx, q, workID, lawModifies, binding, nil); err != nil {
		return nil, err
	}
	if change.Crucial, err = workflowWorkIsCrucial(ctx, q, workID); err != nil {
		return nil, err
	}
	change.Serious = workflowLawChangeSerious(change, decisionDomains)
	if change.TextPins, change.RegistryContentHash, err = workflowLawChangeSubmittedPins(fields); err != nil {
		return nil, err
	}
	if err := validateWorkflowLawChangePins(change, lawModifies, binding, "workflow_law_change"); err != nil {
		return nil, err
	}
	return change, nil
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
// change pins every changed law exactly once, sorted by law ID, and pins the
// registry exactly when it edits the registry. A non-serious change carries
// no pins: no operator approves its text, and completion holds it to the
// derived tier instead.
func validateWorkflowLawChangePins(change *WorkflowLawChange, lawModifies []string, binding *WorkflowArchitectureBinding, op string) error {
	if !change.Serious {
		if change.TextPins != nil || change.RegistryContentHash != "" {
			return newFailure(KindInvalidPayload, op, "law_change text pins are recorded only on a serious law change; a non-serious change carries the derived tier instead", false, "omit law_change_text_pins and law_change_registry_content_hash")
		}
		return nil
	}
	changed := workflowChangedLawIDs(lawModifies, binding)
	pinned := make([]string, 0, len(change.TextPins))
	for _, pin := range change.TextPins {
		if err := validateContentHash(pin.ContentHash); err != nil {
			return newFailure(KindInvalidPayload, op, "law_change text pin for "+pin.LawID+" carries no sha256 content hash", false, "pin the sha256 content hash of the approved law text")
		}
		pinned = append(pinned, pin.LawID)
	}
	if !slices.Equal(pinned, changed) {
		return newFailure(KindInvalidPayload, op, fmt.Sprintf("a serious law change must pin the approved text of exactly each changed law: changed [%s], pinned [%s] (CD-0230 D4)", strings.Join(changed, ", "), strings.Join(pinned, ", ")), false, "draft the law text first, then pin each changed law's content hash once")
	}
	if workflowContractEditsRegistry(binding) != (change.RegistryContentHash != "") {
		return newFailure(KindInvalidPayload, op, "a serious law change pins the Domain registry content hash exactly when it edits the registry (CD-0230 D4)", false, "pin the drafted registry's content hash only for a registry edit")
	}
	if change.RegistryContentHash != "" {
		if err := validateContentHash(change.RegistryContentHash); err != nil {
			return newFailure(KindInvalidPayload, op, "law_change_registry_content_hash is not a sha256 content hash", false, "pin the drafted registry's sha256 content hash")
		}
	}
	return nil
}

func workflowWorkIsCrucial(ctx context.Context, q queryer, workID string) (bool, error) {
	size, sized, err := readWorkSize(ctx, q, workID)
	if err != nil {
		return false, err
	}
	return sized && size.Size == WorkSizeCrucial, nil
}

// workflowLawChangeSerious applies P12 to one law change: more than one child
// Domain, a declared removal of user-visible behavior, a declared security or
// data-safety change, a UI/UX change, or crucial size (Appendix A2 row 3).
func workflowLawChangeSerious(change *WorkflowLawChange, decisionDomains []string) bool {
	return len(change.DomainIDs) > 1 || change.RemovesUserVisibleBehavior || change.TouchesSecurityOrDataSafe || change.Crucial || slices.Contains(decisionDomains, "ui_ux")
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
		rows, err := q.QueryContext(ctx, `SELECT domain_id FROM law_domain_homes WHERE home_project_id=? AND home_locator_id=? AND law_id=?
UNION SELECT domain_id FROM law_domain_applicability WHERE home_project_id=? AND home_locator_id=? AND law_id=?`,
			resolution.source.HomeProjectID, resolution.source.HomeLocatorID, resolution.lawID,
			resolution.source.HomeProjectID, resolution.source.HomeLocatorID, resolution.lawID)
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot read the changed law's Domains", true, "retry once the law projection is readable", err)
		}
		for rows.Next() {
			var domainID string
			if err := rows.Scan(&domainID); err != nil {
				_ = rows.Close()
				return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot decode the changed law's Domains", true, "retry once the law projection is readable", err)
			}
			domains = append(domains, domainID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot finish reading the changed law's Domains", true, "retry once the law projection is readable", err)
		}
		if err := rows.Close(); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot read the changed law's Domains", true, "retry once the law projection is readable", err)
		}
	}
	domains = slices.DeleteFunc(domains, func(domainID string) bool { return domainID == root })
	slices.Sort(domains)
	return append([]string{}, slices.Compact(domains)...), nil
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
	if change.DomainIDs == nil || !slices.IsSorted(change.DomainIDs) || len(slices.Compact(slices.Clone(change.DomainIDs))) != len(change.DomainIDs) {
		return newFailure(KindInvalidPayload, "fold_event", "law_change domain_ids must be a sorted unique list", false, "record the derived child Domains")
	}
	if change.Serious != workflowLawChangeSerious(change, decisionDomains) {
		return newFailure(KindInvalidPayload, "fold_event", "law_change seriousness does not follow its recorded facts", false, "record the derived seriousness")
	}
	return validateWorkflowLawChangePins(change, lawModifies, binding, "fold_event")
}

// validateLiveWorkflowLawChangeTx re-derives the counted facts from current
// state at a live fold and refuses a recorded fact that differs, so no writer
// can lower the count the core derives. Replay folds recorded bytes instead.
func validateLiveWorkflowLawChangeTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, workID string, p workflowContractApprovedPayload) error {
	changesLaw := workflowApprovalPolicyRequiresDecisionFact(definition) && workflowContractChangesLaw(p.LawModifies, p.ArchitectureBinding)
	if !changesLaw {
		if p.LawChange != nil {
			return newFailure(KindInvalidPayload, "fold_event", "law_change is recorded only on a consequence_v1 contract that changes law", false, "omit law_change")
		}
		return nil
	}
	if p.LawChange == nil {
		return newFailure(KindInvalidPayload, "fold_event", "a consequence_v1 contract that changes law must record law_change", false, "record the law change with its reason")
	}
	domains, err := workflowLawChangeDomainsTx(ctx, tx, workID, p.LawModifies, p.ArchitectureBinding, nil)
	if err != nil {
		return err
	}
	crucial, err := workflowWorkIsCrucial(ctx, tx, workID)
	if err != nil {
		return err
	}
	if !slices.Equal(domains, p.LawChange.DomainIDs) || p.LawChange.Crucial != crucial {
		return newFailure(KindInvalidPayload, "fold_event", "law_change does not match the recorded law Domains and work size", false, "derive law_change from current state")
	}
	return nil
}

// checkWorkflowLawChangeEnactmentTx is the completion half of CD-0230 D4. A
// serious change must enact exactly the text the operator approved. A
// non-serious change must still be non-serious once enacted: the work is not
// crucial, the child Domains the changed laws hold now, joined with those
// recorded at approval, number at most one, and each changed law revision
// carries the derived tier, whatever tier it held before, including a D5
// cohort default.
func checkWorkflowLawChangeEnactmentTx(ctx context.Context, tx *sql.Tx, workID string, change *WorkflowLawChange, lawModifies []string, binding *WorkflowArchitectureBinding) error {
	if change == nil {
		return nil
	}
	if change.Serious {
		return checkWorkflowLawChangePinsEnactedTx(ctx, tx, workID, change)
	}
	crucial, err := workflowWorkIsCrucial(ctx, tx, workID)
	if err != nil {
		return err
	}
	if crucial {
		return newFailure(KindApprovalRequired, "complete_workflow", "every law change at crucial size requires the operator's approval of its exact text (CD-0230 D4)", false, "supersede the contract with the operator's approval of the exact text")
	}
	changed := workflowChangedLawIDs(lawModifies, binding)
	domains, err := workflowLawChangeDomainsTx(ctx, tx, workID, changed, nil, change.DomainIDs)
	if err != nil {
		return err
	}
	if len(domains) > 1 {
		return newFailure(KindApprovalRequired, "complete_workflow", fmt.Sprintf("the enacted law change affects child Domains %s, so it is serious and needs the operator's approval of its exact text (CD-0230 D4)", strings.Join(domains, ", ")), false, "supersede the contract with the operator's approval of the exact text")
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	resolutions, err := resolveWorkflowChangedLawsTx(ctx, tx, homeProjectID, homeLocatorID, changed)
	if err != nil {
		return err
	}
	for _, reference := range changed {
		resolution, ok := resolutions[reference]
		if !ok {
			return newFailure(KindMissingEvidence, "complete_workflow", "the enacted law revision is missing from the Git projection: "+reference, false, "publish and rebuild the enacted law revision")
		}
		if resolution.tier != "derived" {
			return newFailure(KindMissingEvidence, "complete_workflow", fmt.Sprintf("law %s changed without the operator's approval must carry the derived tier, not %s (CD-0230 D4)", reference, resolution.tier), false, "record authority.tier derived on the changed law")
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
