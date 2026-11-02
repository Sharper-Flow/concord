package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func runIssue933OperatorAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, owner WorkflowActor) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(context.Background(), s, workID, action)
	if err != nil {
		return err
	}
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operationID := fmt.Sprintf("issue933-%s-%s-%d", action, workID, version)
	_, err = applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: testManifestDigest, Now: time.Unix(20, version).UTC(),
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

func contractCorrectionEffectSnapshot(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var version, events, operations int64
	var step string
	if err := s.db.QueryRow(`SELECT w.version,i.current_step,(SELECT count(*) FROM effect_receipts WHERE subject_id=w.id),(SELECT count(*) FROM durable_operations WHERE work_id=w.id) FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&version, &step, &events, &operations); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("version=%d step=%s events=%d operations=%d", version, step, events, operations)
}

func pContractSuccessor(t *testing.T, s *Store, id, premise string) map[string]any {
	t.Helper()
	version, err := activeWorkflowContractVersion(t.Context(), s.db, id, "test")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := readWorkflowContractVersion(t.Context(), s.db, id, version)
	if err != nil {
		t.Fatal(err)
	}
	predicates := make([]any, len(contract.OutcomePredicates))
	for i, predicate := range contract.OutcomePredicates {
		var outcome map[string]any
		decoder := json.NewDecoder(strings.NewReader(predicate.OutcomePayload))
		decoder.UseNumber()
		if err := decoder.Decode(&outcome); err != nil {
			t.Fatal(err)
		}
		predicates[i] = map[string]any{"predicate_id": predicate.PredicateID, "ordinal": predicate.Ordinal, "outcome_kind": predicate.OutcomeKind, "outcome_payload": outcome}
	}
	payload := map[string]any{"contract_version": version + 1, "premise": premise, "outcome_predicates": predicates, "required_evidence": contract.RequiredEvidence, "route_conventions": contract.RouteConventions, "spec_mandate": contract.SpecMandate, "law_modifies": contract.LawModifies, "rigor_class": contract.RigorClass}
	if contract.DecisionDomains != nil {
		payload["decision_domains"] = contract.DecisionDomains
	}
	if contract.ArchitectureBinding != nil {
		payload["architecture_binding"] = contract.ArchitectureBinding
	}
	return payload
}

func issue1013Preflight(t *testing.T, s *Store, workID, action string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	return WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: action, Payload: payload, Actor: actor,
	})
}

func issue1013Pin(t *testing.T, s *Store, workID string) WorkPin {
	t.Helper()
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatalf("read work pin: %v", err)
	}
	return pin
}

func issue1013HasIntent(pin WorkPin, actionID string) bool {
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == actionID {
			return true
		}
	}
	return false
}

func latestStepStartEpoch(t *testing.T, s *Store, workID, stepID string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(attempt_epoch),0) FROM workflow_step_epochs WHERE work_id=? AND step_id=?`, workID, stepID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func seedDuplicateContractProjection(t *testing.T, s *Store, workID, ownerRef string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,definition_ref,definition_digest)
SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,?,spec_mandate,law_modifies,law_boundary_version,rigor_class,definition_ref,definition_digest FROM workflow_contracts WHERE work_id=? AND contract_version=1;
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1;
DELETE FROM write_guard`, ownerRef, workID, workID); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("duplicate fixture active contracts = %d, want 2", active)
	}
}
