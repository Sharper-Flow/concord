package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
)

// WorkPin is the one point-in-time projection that a caller needs to prepare
// the next operation for a work item.
type WorkPin struct {
	WorkID                  string                    `json:"work_id"`
	Version                 int64                     `json:"version"`
	Lifecycle               string                    `json:"lifecycle"`
	WorkflowType            string                    `json:"workflow_type"`
	Step                    string                    `json:"step"`
	Attempt                 *WorkPinAttempt           `json:"attempt"`
	PendingOperatorDecision *WorkflowOperatorQuestion `json:"pending_operator_decision"`
	Watermark               string                    `json:"watermark"`
	NextValidIntents        []WorkPinIntent           `json:"next_valid_intents"`
}

type WorkPinAttempt struct {
	ID    string `json:"id"`
	Epoch int64  `json:"epoch"`
	Lane  string `json:"lane"`
	State string `json:"state"`
}

type WorkPinIntent struct {
	Tool            string   `json:"tool"`
	Operation       string   `json:"operation"`
	ReasonCode      string   `json:"reason_code"`
	ActionID        string   `json:"action_id"`
	RequiredFields  []string `json:"required_fields"`
	ExpectedVersion int64    `json:"expected_version"`
}

// ReadWorkPin opens one read transaction and derives a complete pin.
func ReadWorkPin(ctx context.Context, s *Store, workID string) (WorkPin, error) {
	var pin WorkPin
	if s == nil || s.db == nil {
		return pin, newFailure(KindUnavailable, "work_pin", "store is not open", false, "open the authority database")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot open a consistent work pin snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	return ReadWorkPinTx(ctx, tx, workID)
}

// ReadWorkPinTransactionTx adapts the opaque store transaction used by agent
// mutations to the SQL transaction-scoped pin reader.
func ReadWorkPinTransactionTx(ctx context.Context, transaction *Transaction, workID string) (WorkPin, error) {
	tx, err := transactionSQL(transaction, "work_pin")
	if err != nil {
		return WorkPin{}, err
	}
	return ReadWorkPinTx(ctx, tx, workID)
}

// ReadWorkPinTx derives a pin from the transaction supplied by its caller.
// Callers that hold a mutation transaction must use this function.
func ReadWorkPinTx(ctx context.Context, tx *sql.Tx, workID string) (WorkPin, error) {
	var pin WorkPin
	if tx == nil {
		return pin, newFailure(KindUnavailable, "work_pin", "transaction is not open", false, "supply an active read transaction")
	}
	if len(workID) < 2 || len(workID) > 128 {
		return pin, newFailure(KindInvalidOperation, "work_pin", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	if err := tx.QueryRowContext(ctx, `SELECT version,lifecycle FROM work_items WHERE id=?`, workID).Scan(&pin.Version, &pin.Lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return pin, newFailure(KindProjectionNotFound, "work_pin", "work item is not recorded", false, "reread_entities")
		}
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot read work item", true, "retry once the database is readable", err)
	}
	pin.WorkID = workID
	var definition WorkflowReadDefinition
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&definition.Ref, &definition.Version, &definition.Digest, &pin.Step); err != nil {
		if err == sql.ErrNoRows {
			return pin, newFailure(KindProjectionNotFound, "work_pin", "workflow instance is not recorded", false, "reread_entities")
		}
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot read workflow instance", true, "retry once the database is readable", err)
	}
	pin.WorkflowType = definition.Ref
	registered, err := verifyReadWorkflowDefinition(definition)
	if err != nil {
		return pin, err
	}
	pin.NextValidIntents = workPinIntents(registered.Definition, pin.Step, pin.Version)

	var contract WorkflowReadContract
	var required, routes, mandate, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT contract_version,premise,required_evidence,route_conventions,spec_mandate,law_modifies,rigor_class FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&contract.Version, &contract.Premise, &required, &routes, &mandate, &modifies, &contract.RigorClass); err == nil {
		if jsonErr := decodeWorkflowPinContract(&contract, required, routes, mandate, modifies); jsonErr != nil {
			return pin, jsonErr
		}
		contract.OutcomePredicates, err = readWorkflowContractPredicates(ctx, tx, workID, contract.Version)
		if err != nil {
			return pin, err
		}
		pin.PendingOperatorDecision, err = workflowOperatorQuestionTx(workID, pin.Step, pin.Version, definition, contract)
		if err != nil {
			return pin, err
		}
	} else if err != sql.ErrNoRows {
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot read workflow contract", true, "retry once the database is readable", err)
	}

	var attempt WorkPinAttempt
	var epoch sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT a.attempt_id,a.lane_id,a.lifecycle_state,COALESCE((SELECT json_extract(e.payload,'$.attempt_epoch') FROM domain_events e WHERE e.subject_type='work_item' AND e.subject_id=a.work_id AND e.kind=? AND json_extract(e.payload,'$.action_id')='dispatch_worker' AND json_extract(e.payload,'$.worker_attempt_id')=a.attempt_id ORDER BY e.seq DESC LIMIT 1),(SELECT json_extract(e.payload,'$.attempt_epoch') FROM domain_events e WHERE e.subject_type='work_item' AND e.subject_id=a.work_id AND e.kind=? AND json_extract(e.payload,'$.step_id')=(SELECT current_step FROM workflow_instances WHERE work_id=a.work_id) ORDER BY e.seq DESC LIMIT 1),0) FROM worker_attempts a WHERE a.work_id=? ORDER BY a.dispatched_at DESC,a.attempt_id DESC LIMIT 1`, WorkflowActionCompleted, WorkflowActionStarted, workID).Scan(&attempt.ID, &attempt.Lane, &attempt.State, &epoch); err == nil {
		if !epoch.Valid || epoch.Int64 < 1 {
			return pin, newFailure(KindInvariantViolation, "work_pin", "worker attempt has no valid workflow epoch", false, "rebuild the worker and workflow projections from the event log")
		}
		attempt.Epoch = epoch.Int64
		pin.Attempt = &attempt
	} else if err != sql.ErrNoRows {
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot read worker attempt", true, "retry once the database is readable", err)
	}
	var watermark int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&watermark); err != nil {
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot read work watermark", true, "retry once the database is readable", err)
	}
	pin.Watermark = "seq:" + strconv.FormatInt(watermark, 10)
	return pin, nil
}

func decodeWorkflowPinContract(contract *WorkflowReadContract, required, routes, mandate, modifies string) error {
	if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandate), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
		return newFailure(KindInvariantViolation, "work_pin", "workflow contract projection contains malformed arrays", false, "rebuild projections from the event log")
	}
	contract.RequiredEvidence = nonNilStrings(contract.RequiredEvidence)
	contract.RouteConventions = nonNilStrings(contract.RouteConventions)
	contract.SpecMandate = nonNilStrings(contract.SpecMandate)
	contract.LawModifies = nonNilStrings(contract.LawModifies)
	return nil
}

func workPinIntents(definition WorkflowDefinition, stepID string, version int64) []WorkPinIntent {
	step := workflowStep(definition, stepID)
	if step == nil {
		return []WorkPinIntent{}
	}
	actions := make(map[string]WorkflowActionDefinition, len(definition.ActionDefinitions))
	for _, action := range definition.ActionDefinitions {
		actions[action.ID] = action
	}
	intents := make([]WorkPinIntent, 0, len(step.Actions))
	for _, actionID := range step.Actions {
		action, ok := actions[actionID]
		if !ok {
			continue
		}
		fields := make([]string, 0, len(action.Payload.Fields))
		for _, field := range action.Payload.Fields {
			if field.Required {
				fields = append(fields, field.Name)
			}
		}
		intents = append(intents, WorkPinIntent{Tool: "concord_work_transition", Operation: "workflow_action", ReasonCode: "declared_step_action", ActionID: action.ID, RequiredFields: fields, ExpectedVersion: version})
	}
	return intents
}
