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
// projection.
type WorkflowLawChange struct {
	Reason                     string   `json:"reason"`
	RemovesUserVisibleBehavior bool     `json:"removes_user_visible_behavior"`
	TouchesSecurityOrDataSafe  bool     `json:"touches_security_or_data_safety"`
	DomainIDs                  []string `json:"domain_ids"`
	Crucial                    bool     `json:"crucial"`
	Serious                    bool     `json:"serious"`
}

const (
	workflowLawChangeReasonField    = "law_change_reason"
	workflowLawChangeBehaviorField  = "law_change_removes_user_visible_behavior"
	workflowLawChangeSecurityField  = "law_change_touches_security_or_data_safety"
	workflowLawChangeReasonMaxBytes = 1024
)

// workflowLawChangeFields declares the consequence_v1 law-change inputs a
// contract or successor payload may carry. They are optional at the payload
// level: the constructor requires the reason exactly when the contract
// changes law.
func workflowLawChangeFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionStringField(workflowLawChangeReasonField, false, workflowLawChangeReasonMaxBytes),
		{Name: workflowLawChangeBehaviorField, ValueType: PayloadBoolean},
		{Name: workflowLawChangeSecurityField, ValueType: PayloadBoolean},
	}
}

// workflowContractChangesLaw reports whether a contract modifies or removes
// law, adds law, or changes the Domain registry. A mandate alone binds law the
// work must meet; it changes none.
func workflowContractChangesLaw(lawModifies []string, binding *WorkflowArchitectureBinding) bool {
	if len(lawModifies) != 0 {
		return true
	}
	return binding != nil && (len(binding.LawAdditions) != 0 || len(binding.DomainModifies) != 0 || len(binding.DomainRelationModifies) != 0)
}

// deriveWorkflowLawChangeTx builds the law-change fact for one consequence_v1
// contract or successor. It returns nil for a legacy definition or a contract
// that changes no law, and refuses a law-changing contract without a reason.
// binding is the Product-changing contract's architecture binding, else nil.
func deriveWorkflowLawChangeTx(ctx context.Context, q queryer, definition WorkflowDefinition, workID string, fields map[string]json.RawMessage, lawModifies []string, binding *WorkflowArchitectureBinding, decisionDomains []string) (*WorkflowLawChange, error) {
	_, reasonPresent := fields[workflowLawChangeReasonField]
	_, behaviorPresent := fields[workflowLawChangeBehaviorField]
	_, securityPresent := fields[workflowLawChangeSecurityField]
	declared := reasonPresent || behaviorPresent || securityPresent
	if !workflowApprovalPolicyRequiresDecisionFact(definition) || !workflowContractChangesLaw(lawModifies, binding) {
		if declared {
			return nil, newFailure(KindInvalidPayload, "workflow_law_change", "law_change fields are admitted only on a consequence_v1 contract that changes law", false, "omit the law_change fields")
		}
		return nil, nil
	}
	reason, _ := workflowFieldString(fields, workflowLawChangeReasonField)
	if strings.TrimSpace(reason) == "" {
		return nil, newFailure(KindInvalidPayload, "workflow_law_change", "a contract that changes law must record why (CD-0230 D4)", false, "supply law_change_reason")
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
	return change, nil
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
// modified relation, and the Domains already recorded in prior. The
// Product-root Domain is not counted. A reference record is not law and is
// never in law_subjects, so it adds nothing.
func workflowLawChangeDomainsTx(ctx context.Context, q queryer, workID string, laws []string, binding *WorkflowArchitectureBinding, prior []string) ([]string, error) {
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return nil, err
	}
	declared := slices.Clone(prior)
	if binding != nil {
		for _, addition := range binding.LawAdditions {
			declared = append(declared, addition.HomeDomainID)
		}
		declared = append(declared, binding.DomainModifies...)
		for _, relation := range binding.DomainRelationModifies {
			declared = append(declared, relation.SourceDomainID, relation.TargetDomainID)
		}
	}
	set := map[string]struct{}{}
	for _, domainID := range declared {
		var child bool
		err := q.QueryRowContext(ctx, `SELECT parent_domain_id IS NOT NULL FROM domains WHERE product_id=? AND domain_id=?`, productID, domainID).Scan(&child)
		if err == sql.ErrNoRows {
			// A Domain the change adds is not in the registry yet; it is a
			// child, since a Product has one root.
			child = true
		} else if err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot read the Domain registry", true, "retry once the Domain registry is readable", err)
		}
		if child {
			set[domainID] = struct{}{}
		}
	}
	resolutions, err := resolveWorkflowChangedLawsTx(ctx, q, homeProjectID, homeLocatorID, laws)
	if err != nil {
		return nil, err
	}
	for _, resolution := range resolutions {
		rows, err := q.QueryContext(ctx, `SELECT h.domain_id FROM law_domain_homes h JOIN domains d ON d.product_id=h.product_id AND d.domain_id=h.domain_id
WHERE h.home_project_id=? AND h.home_locator_id=? AND h.law_id=? AND d.parent_domain_id IS NOT NULL
UNION SELECT a.domain_id FROM law_domain_applicability a JOIN domains d ON d.product_id=a.product_id AND d.domain_id=a.domain_id
WHERE a.home_project_id=? AND a.home_locator_id=? AND a.law_id=? AND d.parent_domain_id IS NOT NULL`,
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
			set[domainID] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_law_change", "cannot read the changed law's Domains", true, "retry once the law projection is readable", err)
		}
	}
	domains := make([]string, 0, len(set))
	for domainID := range set {
		domains = append(domains, domainID)
	}
	slices.Sort(domains)
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
func validateWorkflowLawChangeShape(change *WorkflowLawChange, decisionDomains []string) error {
	if change.Reason == "" || len(change.Reason) > workflowLawChangeReasonMaxBytes {
		return newFailure(KindInvalidPayload, "fold_event", "law_change reason must be 1 to 1024 bytes", false, "record why the law changes")
	}
	if change.DomainIDs == nil || !slices.IsSorted(change.DomainIDs) || len(slices.Compact(slices.Clone(change.DomainIDs))) != len(change.DomainIDs) {
		return newFailure(KindInvalidPayload, "fold_event", "law_change domain_ids must be a sorted unique list", false, "record the derived child Domains")
	}
	if change.Serious != workflowLawChangeSerious(change, decisionDomains) {
		return newFailure(KindInvalidPayload, "fold_event", "law_change seriousness does not follow its recorded facts", false, "record the derived seriousness")
	}
	return nil
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

// checkWorkflowLawChangeEnactmentTx is the completion half of CD-0230 D4 for a
// law change the operator did not approve. The child Domains the changed laws
// hold now, joined with those recorded at approval, must still number at most
// one, and each changed law that is accepted law must carry the derived tier,
// whatever tier it held before, including a D5 cohort default.
func checkWorkflowLawChangeEnactmentTx(ctx context.Context, tx *sql.Tx, workID string, change *WorkflowLawChange, decisionDomains, lawModifies []string, binding *WorkflowArchitectureBinding) error {
	if change == nil || change.Serious || len(decisionDomains) != 0 {
		return nil
	}
	changed := slices.Clone(lawModifies)
	if binding != nil {
		for _, addition := range binding.LawAdditions {
			changed = append(changed, addition.LawID)
		}
	}
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
		// A removed law is superseded, no longer law, and keeps its tier.
		if resolution, ok := resolutions[reference]; ok && resolution.status == "accepted" && resolution.tier != "derived" {
			return newFailure(KindMissingEvidence, "complete_workflow", fmt.Sprintf("law %s changed without the operator's approval must carry the derived tier, not %s (CD-0230 D4)", reference, resolution.tier), false, "record authority.tier derived on the changed law")
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
