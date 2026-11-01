package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// BuiltinWorkflowContractUpdateFields declares the complete contract content
// revise_intent accepts. Version identity is separate from the work version.
func BuiltinWorkflowContractUpdateFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionIntegerField("contract_version", true),
		actionPremiseField(),
		actionArrayField("outcome_predicates", true, 8, "workflow_action_outcome_predicates"),
		actionEnumListField("required_evidence", true, 0, 7, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"),
		actionListField("route_conventions", true, 0, 16),
		actionLawListField("spec_mandate", true),
		actionLawListField("law_modifies", true),
		actionEnumField("rigor_class", true, "prototype_internal", "prototype_trusted", "prototype_public", "prototype_safety_critical", "production_internal", "production_trusted", "production_public", "production_safety_critical", "critical_internal", "critical_trusted", "critical_public", "critical_safety_critical"),
		actionObjectField("architecture_binding", false, "architecture_binding"),
	}
}

func WorkflowContractUpdatePayload(definition WorkflowDefinition) WorkflowPayloadDefinition {
	return WorkflowPayloadDefinition{Closed: true, Fields: workflowDecisionFactPayloadFields(definition, BuiltinWorkflowContractUpdateFields())}
}

func reviseWorkflowContractTx(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, raw json.RawMessage) error {
	registered, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID)
	if err != nil {
		return err
	}
	previous, err := activeWorkflowContractVersion(ctx, tx, event.SubjectID, "revise_intent")
	if err == sql.ErrNoRows {
		return newFailure(KindInvalidOperation, "revise_intent", "contract update requires an existing contract", false, "approve the initial contract through the workflow")
	}
	if err != nil {
		return err
	}
	fields, err := workflowActionObject(raw)
	if err != nil {
		return err
	}
	declaration := WorkflowContractUpdatePayload(registered.Definition).Fields
	allowed := make(map[string]WorkflowPayloadField, len(declaration))
	for _, field := range declaration {
		allowed[field.Name] = field
	}
	if err := validateWorkflowPayloadStructure("revise_intent", declaration, allowed, fields); err != nil {
		return err
	}
	for name, value := range fields {
		field := allowed[name]
		if !validateWorkflowPayloadValue(field, value) {
			return newFailure(KindInvalidPayload, "revise_intent", "contract update contains an invalid field: "+name, false, "supply the complete contract")
		}
		if err := validateWorkflowPayloadSchema(payloadschema.Document(), field, value); err != nil {
			return err
		}
	}
	if workflowFieldInt(fields, "contract_version", 0) != previous+1 {
		return newFailure(KindInvalidPayload, "revise_intent", "contract update must name the next retained version", false, "reread the current contract and supply its next version")
	}
	content, err := workflowContractContent(ctx, tx, registered.Definition, event.SubjectID, fields, nil)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return err
	}
	contractEvent := event
	contractEvent.Payload = encoded
	var contract workflowContractApprovedPayload
	if err := decodeWorkflowPayload(contractEvent, &contract); err != nil {
		return err
	}
	if err := normalizeWorkflowContractApproval(ctx, &contract); err != nil {
		return err
	}
	var contractFields map[string]json.RawMessage
	if err := json.Unmarshal(contractEvent.Payload, &contractFields); err != nil {
		return err
	}
	if err := validateWorkflowContractFacts(ctx, tx, contractEvent, registered, contractFields, contract); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	if err := persistWorkflowContractApprovalTx(ctx, tx, contractEvent, registered, contract); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_contracts SET superseded_by=? WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, contract.ContractVersion, event.SubjectID, previous); err != nil {
		return workflowProjectionError(err, "cannot retain the previous contract version")
	}
	return refreshWorkflowHealthySequence(ctx, tx, event.SubjectID, contract.ContractVersion, true)
}
