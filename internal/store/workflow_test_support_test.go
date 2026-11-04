package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

const testManifestDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func mustActorRef(t *testing.T, actor WorkflowActor) string {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// Synthetic projection fixtures bind their synthetic home to a real committed
// law folder, so a pre-transaction verification reads a real snapshot from it.
// Source proof is established separately by the caller before opening its
// workflow transaction.
func bindWorkflowFixtureKnowledgeSource(t *testing.T, home KnowledgeHome) {
	t.Helper()
	writeFixtureLawSource(t, home, "spec:one")
}

func ReadWorkflowProjection(ctx context.Context, s *Store, request WorkflowReadRequest) (WorkflowReadProjection, error) {
	var out WorkflowReadProjection
	if len(request.WorkID) < 2 || len(request.WorkID) > 128 {
		return out, newFailure(KindInvalidOperation, "workflow_read", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	// The production read prepares the work's law proof before its read
	// snapshot opens (PrepareWorkLawProof); this helper does the same.
	ctx, err := s.PrepareWorkLawProof(ctx, request.WorkID)
	if err != nil {
		return out, err
	}
	err = s.readSnapshot(ctx, func(q queryer) error {
		out, err = readWorkflowProjectionTx(ctx, q, request)
		return err
	})
	if err == nil && out.WorkID == "" {
		return out, newFailure(KindProjectionNotFound, "workflow_read", "workflow instance is not recorded", false, "reread_entities")
	}
	return out, err
}

// testApprovalPayload fills the approval defaults a test leaves out.
func testApprovalPayload(t testing.TB, s *Store, workID, actionID string, payload json.RawMessage) json.RawMessage {
	t.Helper()
	if actionID == "record_proposal" {
		var fields map[string]any
		if json.Unmarshal(payload, &fields) == nil && len(fields) == 0 {
			return json.RawMessage(`{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`)
		}
	}
	if actionID != "approve_contract" {
		return payload
	}
	var fields map[string]any
	if json.Unmarshal(payload, &fields) != nil || fields == nil {
		return payload
	}
	plan, err := readWorkflowPlanTx(context.Background(), s.db, workID)
	if err != nil || plan == nil {
		t.Fatalf("read workflow plan for %s: plan=%v err=%v", workID, plan, err)
	}
	if _, present := fields["legislative_subjects"]; !present {
		fields["legislative_subjects"] = []string{}
	}
	if _, present := fields["premise"]; !present {
		fields["premise"] = "The operator stated this premise."
	}
	if _, present := fields["decision_domains"]; !present {
		fields["decision_domains"] = []string{}
	}
	if plan.Type != WorkflowStepPlanResearch {
		if _, present := fields["architecture_binding"]; !present {
			fields["architecture_binding"] = WorkflowArchitectureBinding{
				HomeDomainID: "root", AffectedDomainIDs: []string{"root", "product-root:product"},
				DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{},
				LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
			}
		}
	}
	encoded, _ := json.Marshal(fields)
	return encoded
}

// applyWorkflowTestOperation models the owning workflow route for white-box
// fold tests. Production callers must use the dispatcher or initialization and
// completion entry points; generic ApplyOperation intentionally rejects the
// reserved event families.
func applyWorkflowTestOperation(ctx context.Context, s *Store, operation Operation) error {
	return applyWorkflowTestOperationDirect(ctx, s, operation)
}

func applyWorkflowTestOperationDirect(ctx context.Context, s *Store, operation Operation) error {
	// Model the owning contract route without replacing a proof supplied by a
	// caller that is exercising a source change after verification.
	if knowledgeSourceSetProofFrom(ctx) == nil {
		for _, event := range operation.Events {
			if event.Kind == WorkflowDefinitionSelected || event.Kind == WorkflowContractApproved || event.Kind == WorkflowContractSuperseded {
				var err error
				ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, event.SubjectID)
				if err != nil {
					return err
				}
				break
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	_, err = applyWorkflowOperationTx(ctx, tx, operation, newWriteScope(tx))
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func shiftWorkflowTestEventVersions(event Event, shift int64) Event {
	if shift == 0 {
		return event
	}
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) != nil {
		return event
	}
	if expected, ok := payload["expected_version"].(float64); ok {
		payload["expected_version"] = int64(expected) + shift
	}
	if resulting, ok := payload["resulting_version"].(float64); ok {
		payload["resulting_version"] = int64(resulting) + shift
	}
	event.Payload, _ = json.Marshal(payload)
	return event
}

func ptrTime(value time.Time) *time.Time { return &value }

// CompleteWorkflow drives the ordered completion gate directly, outside the
// workflow action boundary that shipped completion runs through. It owns one
// durable transaction from the first gate clause through terminal metadata
// and the reserved completion append.
func CompleteWorkflow(ctx context.Context, s *Store, event Event) (retErr error) {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "complete_workflow", "store is not open", false, "open a store before completing a workflow")
	}
	// A replay of the exact completion identity is an idempotent no-op.  This
	// check happens before opening the mutation transaction and never treats a
	// different event identity as a replay.
	var existingKind string
	err := s.db.QueryRowContext(ctx, `SELECT kind FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&existingKind)
	if err == nil && existingKind == WorkflowCompleted {
		replay, err := s.beginDurableTx(ctx)
		if err != nil {
			return err
		}
		defer replay.finish(&retErr)
		return replay.Commit()
	}
	// CD-0200: the completion's law boundary verifies the Product's complete
	// designated knowledge home before this transaction opens. The verification
	// probes git and rebuilds through the pool, and the transaction-scoped
	// boundary clause proves it from the returned context.
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, event.SubjectID)
	if err != nil {
		return err
	}
	tx, err := s.beginDurableTx(ctx)
	if err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot begin workflow completion", true, "retry once the database is writable", err)
	}
	defer tx.finish(&retErr)
	scope, err := beginWrite(ctx, tx.Tx)
	if err != nil {
		return err
	}
	if err := event.validate(); err != nil {
		return err
	}
	if err := validateRegisteredEvent(event); err != nil {
		return err
	}
	var payload workflowCompletedPayload
	if err := decodeWorkflowPayload(event, &payload); err != nil {
		return err
	}
	if err := workflowBase(event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	if err := completeWorkflowInScopeTx(ctx, tx.Tx, event, scope, payload); err != nil {
		return err
	}
	if len(scope.eventIDs) < 2 || scope.eventIDs[len(scope.eventIDs)-2] != event.EventID || scope.eventIDs[len(scope.eventIDs)-1] != event.EventID+":lifecycle" {
		return fmt.Errorf("completion receipts omit or reorder completion and derived lifecycle: %v", scope.eventIDs)
	}
	if err := scope.close(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot commit workflow completion", true, "retry once the database is writable", err)
	}
	return nil
}
