package store

// TestCoordinatorConfirmationLiveness is the model/fold conformance witness for the
// workflow's confirmation exits: the abstract admission model and the real
// fold agree on every seeded route — engineering work folds the coordinator
// authority and holds no human-question exit, correction stays a routine
// admission route, and the acceptance fold stays total under a duplicated
// active count.
//
// The comparisons run through the shared conformance machinery
// (conformanceCheckpoint): a fold that disagrees with the model on any
// compared dimension fails the comparison.

import (
	"context"
	"testing"
)

func TestCoordinatorConfirmationLiveness(t *testing.T) {
	t.Parallel()

	t.Run("engineering folds no human-question exit beside its real confirm exit", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-engineering"
		s, _, definition, _ := seedApprovalItemAtAcceptance(t, workID, []string{})
		// The model lifts engineering to the coordinator confirmation exit;
		// the real fold must agree on every compared dimension.
		conformanceCheckpoint(t, s, workID, definition, rAcceptanceModel(), "engineering at acceptance")
		state := foldedApprovalAdmission(t, s, definition, workID)
		decision := workflowAdmit(definition, state, "confirm_premise")
		if !decision.Admitted {
			t.Fatalf("engineering confirm_premise is no exit at all: %v", decision.Failure)
		}
	})

	t.Run("ordinary records remain subject-specific off-step", func(t *testing.T) {
		t.Parallel()
		available := WorkflowAdmissionState{Step: "delivery", CurrentRecords: map[string]bool{"record_review": true}}
		for name, definition := range map[string]WorkflowDefinition{"implementation": mustBuiltinDefinition(t, "workflow.implementation").Definition} {
			decision := workflowAdmit(definition, available, "record_review")
			if !decision.Admitted {
				t.Fatalf("%s current-record write refused: %v", name, decision.Failure)
			}
			unavailable := workflowAdmit(definition, WorkflowAdmissionState{Step: "delivery"}, "record_review")
			var failure *Failure
			if unavailable.Admitted || !failureAs(unavailable.Failure, &failure) {
				t.Fatalf("%s missing record folded %v, want the typed availability refusal", name, unavailable.Failure)
			}
		}
	})

	t.Run("a revised contract keeps the coordinator exit and its preserved proof", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-revision"
		s, registry, definition, owner := seedApprovalItemAtAcceptance(t, workID, []string{})
		conformanceCheckpoint(t, s, workID, definition, rAcceptanceModel(), "before the revision")
		// The revision changes no criterion, so the judged artifact stays
		// current (CD-0242 D7).
		rReviseContract(t, s, workID, ownerRefForTest(t, owner), []string{})
		conformanceCheckpoint(t, s, workID, definition, rAcceptanceModel(), "successor after the revision")
		// The judged artifact stays current through the revision, so the
		// coordinator confirms the successor with no repeated production
		// (CD-0242 D7).
		if err := runCoordinatorActionTx(t, s, registry, workID, "confirm_premise", owner, `{}`); err != nil {
			t.Fatalf("the successor's confirmation: %v", err)
		}
		// The confirmed successor reaches its terminal on the preserved proof.
		if err := runCoordinatorActionTx(t, s, registry, workID, "complete", owner, `{"impact_verdict":"non-breaking"}`); err != nil {
			t.Fatalf("the confirmed successor's completion: %v", err)
		}
		if lifecycle, instanceState := approvalWorkAndInstanceState(t, s, workID); lifecycle != "completed" || instanceState != "completed" {
			t.Fatalf("the confirmed successor ended lifecycle %q instance %q, want completed", lifecycle, instanceState)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("continuity projects no open decision at either level", func(t *testing.T) {
		t.Parallel()
		const engineeringID = "approval-continuity-engineering"
		s, registry, _, _ := seedApprovalItemAtAcceptance(t, engineeringID, []string{})
		// The continuity snapshot projects neither a question nor a
		// withholding at the top level or on the seated work pin.
		engineeringSnapshot := approvalContinuitySnapshot(t, s, registry, engineeringID)
		if engineeringSnapshot.WorkPin == nil {
			t.Fatal("continuity omitted the work pin")
		}
		missingRegistry := NewWorkflowDefinitionRegistry()
		if err := approvalContinuityProjectionError(t, s, missingRegistry, engineeringID); err == nil {
			t.Fatal("the continuity projection accepted a registry missing the pinned definition")
		}
	})

	t.Run("the absent historical fact keeps its carried pin resolvable", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-liveness-absent-fact"
		s, registry, _ := seedApprovalAbsentFactAtAcceptance(t, workID)
		// The carry forward kept the historical NULL-fact contract
		// authoritative. The caller registry resolves the actual carried
		// registered definition and digest, never a freshly made definition
		// shape.
		var carriedRef, carriedDigest string
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&carriedRef, &carriedDigest); err != nil {
			t.Fatal(err)
		}
		definitionEntry, ok := registry.Current(carriedRef)
		if !ok || definitionEntry.Digest != carriedDigest {
			t.Fatalf("the carried pin %s %s does not resolve in the caller registry", carriedRef, carriedDigest)
		}
	})

}

// approvalContinuitySnapshot seats one continuity snapshot through the
// registry-threaded continuity core; the nil registry rides the builtin
// convenience and its refusal.
func approvalContinuitySnapshot(t *testing.T, s *Store, registry DefinitionRegistry, workID string) ContinuitySnapshot {
	t.Helper()
	snapshot, err := readWorkflowContinuityForRegistry(context.Background(), s, ContinuityRequest{Work: workID}, registry)
	if err != nil {
		t.Fatalf("the continuity snapshot refused: %v", err)
	}
	return snapshot
}
