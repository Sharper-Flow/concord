package store

import (
	"context"
	"reflect"
	"testing"
)

// A context-bearing oracle definition must keep the outside-repair read fence:
// held and reconciled work carries no managed context or definition authority.
// Resume exposes the unchanged context through the same transaction reader.
func TestOutsideRepairDraftContextOracleReadFence(t *testing.T) {
	for _, mode := range []string{"resume", "completed"} {
		t.Run(mode, func(t *testing.T) {
			fixture := seedWorkContextFixture(t, "outside-context-"+mode)
			s, work := fixture.store, fixture.workID
			defer s.Close()
			declareSampleWorkContext(t, fixture, "retain the bounded reader across outside repair")
			before, err := ReadWorkPin(context.Background(), s, work)
			if err != nil || before.WorkContext == nil {
				t.Fatalf("context-bearing managed pin = %+v, err=%v", before, err)
			}
			if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
				t.Fatal(err)
			}
			assertReadFence := func() {
				t.Helper()
				pin, err := ReadWorkPin(context.Background(), s, work)
				if err != nil {
					t.Fatal(err)
				}
				if pin.WorkContext != nil || pin.WorkflowDefinitionVersion != nil || pin.WorkflowDefinitionDigest != nil || pin.Obligations != nil || len(pin.NextValidIntents) != 0 {
					t.Fatalf("outside-repair pin exposes managed enrichment: %+v", pin)
				}
				continuity, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: work})
				if err != nil {
					t.Fatal(err)
				}
				if continuity.WorkContext != nil || len(continuity.StepActions) != 0 || continuity.PendingOperatorDecision != nil || continuity.RestartAvailable {
					t.Fatalf("outside-repair continuity exposes managed enrichment: %+v", continuity)
				}
			}
			assertReadFence()
			kind := WorkflowOutsideRepairResumed
			if mode == "completed" {
				kind = WorkflowOutsideRepairReconciled
			}
			if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, mode), kind, outsideRepairSampleEvidence()); err != nil {
				t.Fatal(err)
			}
			if mode == "completed" {
				assertReadFence()
				return
			}
			after, err := ReadWorkPin(context.Background(), s, work)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.WorkContext, after.WorkContext) || after.WorkflowDefinitionVersion == nil || *after.WorkflowDefinitionVersion != *before.WorkflowDefinitionVersion {
				t.Fatalf("resume changed managed context or pin: before=%+v after=%+v", before, after)
			}
			continuity, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: work})
			if err != nil || !reflect.DeepEqual(after.WorkContext, continuity.WorkContext) {
				t.Fatalf("resume pin and continuity context differ: %+v, err=%v", continuity, err)
			}
		})
	}
}
