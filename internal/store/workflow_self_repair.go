package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const workflowSelfRepairMaxEvidence = 32

// WorkflowSelfRepair is an operator-approved contract classification for the
// bounded case where a Concord workflow defect blocks the operation needed to
// repair that defect. It disables Domain-overlap admission for the classified
// work only. Read surfaces continue to report the unresolved overlaps.
type WorkflowSelfRepair struct {
	RefusalKind      string   `json:"refusal_kind"`
	BlockedOperation string   `json:"blocked_operation"`
	EvidenceRefs     []string `json:"evidence_refs"`
}

func (repair *WorkflowSelfRepair) UnmarshalJSON(data []byte) error {
	type selfRepairAlias WorkflowSelfRepair
	var decoded selfRepairAlias
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("self_repair contains trailing data")
	}
	if decoded.EvidenceRefs == nil {
		return fmt.Errorf("self_repair evidence_refs must be explicitly present and non-null")
	}
	if err := validateWorkflowSelfRepairShape(WorkflowSelfRepair(decoded)); err != nil {
		return err
	}
	*repair = WorkflowSelfRepair(decoded)
	return nil
}

func parseWorkflowSelfRepair(raw json.RawMessage) (*WorkflowSelfRepair, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var repair WorkflowSelfRepair
	if err := json.Unmarshal(raw, &repair); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_self_repair", "self_repair is not a strict typed object: "+err.Error(), false, "supply one typed refusal and its evidence")
	}
	return &repair, nil
}

func validateWorkflowSelfRepairShape(repair WorkflowSelfRepair) error {
	if !TypedErrorKindAllowed(repair.RefusalKind) || !ValidReference(repair.BlockedOperation) || len(repair.EvidenceRefs) < 1 || len(repair.EvidenceRefs) > workflowSelfRepairMaxEvidence {
		return newFailure(KindInvalidPayload, "workflow_self_repair", "self_repair does not name one typed refusal, blocked operation, and bounded evidence set", false, "supply a typed refusal kind, operation reference, and 1-32 evidence references")
	}
	seen := make(map[string]bool, len(repair.EvidenceRefs))
	for _, ref := range repair.EvidenceRefs {
		if !ValidReference(ref) || seen[ref] {
			return newFailure(KindInvalidPayload, "workflow_self_repair", "self_repair evidence references are invalid or duplicated", false, "supply unique workflow evidence references")
		}
		seen[ref] = true
	}
	return nil
}

func validateWorkflowSelfRepairAuthorityTx(ctx context.Context, tx *sql.Tx, workID, actorRef string, repair *WorkflowSelfRepair) error {
	if repair == nil {
		return nil
	}
	productID, err := workflowBindingProductIDTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	var productKey string
	if err := tx.QueryRowContext(ctx, `SELECT product_key FROM domain_registries WHERE product_id=?`, productID).Scan(&productKey); err != nil {
		return wrapFailure(KindUnavailable, "workflow_self_repair", "cannot read the Product identity for self-repair", true, "retry once the Domain registry is readable", err)
	}
	if productKey != "concord" {
		return newFailure(KindUnauthorized, "workflow_self_repair", "self_repair is available only for Concord's own workflow defects", false, "remove self_repair outside the Concord Product")
	}
	registered, policyErr := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), workID)
	if policyErr != nil {
		return policyErr
	}
	return validateWorkflowSelfRepairAuthorityForDefinitionTx(ctx, tx, workID, actorRef, registered.Definition)
}

// validateWorkflowSelfRepairAuthorityForDefinitionTx is the self-repair
// authority body minus the verified-definition resolution: the caller supplies
// the verified definition, so consequence_v1 seams can drive the same owner a
// live fold drives. Legacy keeps the recorded operator requirement.
// consequence_v1 accepts the authenticated non-worker coordinator — the real
// recorded agent-class actor, never a worker — and the classification itself
// grants no overlap exemption.
func validateWorkflowSelfRepairAuthorityForDefinitionTx(ctx context.Context, tx *sql.Tx, workID, actorRef string, definition WorkflowDefinition) error {
	if definition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1 {
		authority, authorityErr := workflowEvaluationAuthority(ctx, tx, workID, actorRef)
		if authorityErr != nil {
			return authorityErr
		}
		if authority == WorkflowWorkerEvaluator {
			return newFailure(KindUnauthorized, "workflow_self_repair", "worker actors cannot classify contract self-repair", false, "return the worker report to the coordinator")
		}
		return requireActorClass(ctx, tx, actorRef, string(ActorAgent))
	}
	var actorClass string
	if err := tx.QueryRowContext(ctx, `SELECT actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&actorClass); err != nil {
		return wrapFailure(KindUnavailable, "workflow_self_repair", "cannot read the self-repair approving actor", true, "retry once the workflow actor projection is readable", err)
	}
	if actorClass != string(ActorOperator) {
		return newFailure(KindUnauthorized, "workflow_self_repair", "self_repair requires an operator-approved contract", false, "request operator approval for the classified contract")
	}
	return nil
}

func readWorkflowSelfRepair(ctx context.Context, q queryer, workID string, contractVersion int64) (*WorkflowSelfRepair, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT self_repair_json FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&raw); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_self_repair", "cannot read the workflow self-repair classification", true, "retry once the workflow projection is readable", err)
	}
	repair, err := parseWorkflowSelfRepair(json.RawMessage(raw))
	if err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_self_repair", "stored self_repair classification is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	return repair, nil
}

func workflowSelfRepairExemptTx(ctx context.Context, q queryer, workID string) (bool, error) {
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_self_repair")
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var raw, definitionRef, definitionDigest string
	var definitionVersion int64
	err = q.QueryRowContext(ctx, `SELECT self_repair_json,COALESCE(definition_ref,''),COALESCE(definition_version,0),COALESCE(definition_digest,'') FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, contractVersion).Scan(&raw, &definitionRef, &definitionVersion, &definitionDigest)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_self_repair", "cannot read the active self-repair classification", true, "retry once the workflow projection is readable", err)
	}
	repair, err := parseWorkflowSelfRepair(json.RawMessage(raw))
	if err != nil {
		return false, newFailure(KindInvariantViolation, "workflow_self_repair", "stored self_repair classification is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	if repair == nil {
		return false, nil
	}
	definition, err := verifyWorkflowDefinitionPinForFold(BuiltinWorkflowRegistry(), WorkflowDefinitionPin{Ref: definitionRef, Version: definitionVersion, Digest: definitionDigest})
	if err != nil {
		return false, err
	}
	return workflowSelfRepairOverlapExempt(definition.Definition), nil
}

// workflowSelfRepairOverlapExempt answers whether one classified contract
// keeps CD-0150's blanket overlap exemption. The exemption derives from
// operator-authorized supersession: a legacy classification keeps it, and
// consequence_v1 supersedes routinely, so its classifications exempt nothing
// and a real intersection resolves through the dedicated version-pinned
// overlap route.
func workflowSelfRepairOverlapExempt(definition WorkflowDefinition) bool {
	return definition.ApprovalPolicy != WorkflowApprovalPolicyConsequenceV1
}
