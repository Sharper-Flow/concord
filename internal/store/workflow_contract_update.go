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
		actionContractVersionField(true),
		actionContractLawListField("spec_mandate", true),
		actionContractLawListField("law_modifies", true),
		actionListField("legislative_subjects", true, 0, 800),
		actionObjectField("architecture_binding", "architecture_binding"),
	}
}

func reviseWorkflowContractTx(ctx context.Context, tx *sql.Tx, event Event, raw json.RawMessage) error {
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if plan == nil {
		return newFailure(KindInvalidOperation, "revise_intent", "contract update requires a workflow type", false, "pick a workflow type first")
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
	declaration := BuiltinWorkflowContractUpdateFields()
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
	if workflowContractVersionField(fields, 0) != previous+1 {
		return newFailure(KindInvalidPayload, "revise_intent", "contract update must name the next retained version", false, "reread the current contract and supply its next version")
	}
	fields["consequence_class"] = json.RawMessage(`"internal_sqlite"`)
	fields["law_boundary_version"] = json.RawMessage(`1`)
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	contractEvent := event
	contractEvent.Payload = encoded
	var contract workflowContractApprovedPayload
	if err := decodeWorkflowPayload(contractEvent, &contract); err != nil {
		return err
	}
	if err := normalizeWorkflowContractApproval(&contract); err != nil {
		return err
	}
	var contractFields map[string]json.RawMessage
	if err := json.Unmarshal(contractEvent.Payload, &contractFields); err != nil {
		return err
	}
	if err := validateWorkflowContractFacts(ctx, tx, contractEvent, plan.Type, contractFields, contract); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	if err := persistWorkflowContractApprovalTx(ctx, tx, contractEvent, plan.Type, contract); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_contracts SET superseded_by=? WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, contract.ContractVersion, event.SubjectID, previous); err != nil {
		return workflowProjectionError(err, "cannot retain the previous contract version")
	}
	return nil
}
