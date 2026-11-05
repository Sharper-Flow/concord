package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func reviseContractRecord(t *testing.T, s *Store, workID string, owner WorkflowActor, contract map[string]any, expected int64) error {
	t.Helper()
	actor, err := WorkflowActorRef(owner)
	if err != nil {
		return err
	}
	var kind string
	if err := s.db.QueryRowContext(t.Context(), `SELECT kind FROM work_items WHERE id=?`, workID).Scan(&kind); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"title": "Updated plan", "kind": kind,
		"reason": "revise the plan", "expected_version": expected, "resulting_version": expected + 1, "contract": contract,
	})
	if err != nil {
		return err
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		return err
	}
	err = ApplyOperation(ctx, s, Operation{
		Events:           []Event{{EventID: fmt.Sprintf("record-update-%s-%d", workID, expected), Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor, OccurredAt: time.Unix(40, expected).UTC(), PayloadVersion: 1, Payload: payload}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected},
	})
	return err
}

func contractRecordUpdate(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	return pContractSuccessor(t, s, workID, "An ordinary revised plan.")
}

func TestContractRecordUpdatePreservesProofAndProvenance(t *testing.T) {
	for _, scenario := range []string{"unchanged", "legislative-subject"} {
		t.Run(scenario, func(t *testing.T) {
			workID := "record-update-" + scenario
			f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "implement")
			s := f.store
			previous, err := activeWorkflowContractVersion(t.Context(), s.db, workID, "test")
			if err != nil {
				t.Fatal(err)
			}
			bindingsSnapshot := contractRecordBindingSnapshot(t, s, workID)
			if bindingsSnapshot == "" {
				t.Fatal("fixture carries no stored evidence bindings")
			}
			var bindingsBefore int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, workID).Scan(&bindingsBefore); err != nil {
				t.Fatal(err)
			}
			before, err := readWorkflowContractVersion(t.Context(), s.db, workID, previous)
			if err != nil {
				t.Fatal(err)
			}
			step := workflowPlanFixtureFrontier(t, s, workID)
			expected := readWorkVersion(t, s, workID)
			contract := contractRecordUpdate(t, s, workID)
			if scenario == "legislative-subject" {
				contract["legislative_subjects"] = []string{"spec:one"}
			}
			if err := reviseContractRecord(t, s, workID, f.owner, contract, expected); err != nil {
				t.Fatal(err)
			}
			active, err := activeWorkflowContractVersion(t.Context(), s.db, workID, "test")
			if err != nil || active != previous+1 {
				t.Fatalf("active contract = %d, %v; want next retained version %d", active, err, previous+1)
			}
			updated, err := readWorkflowContractVersion(t.Context(), s.db, workID, active)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "legislative-subject" && !reflect.DeepEqual(updated.LegislativeSubjects, []string{"spec:one"}) {
				t.Fatalf("revised legislative subjects = %v", updated.LegislativeSubjects)
			}
			if got := readWorkVersion(t, s, workID); got != expected+1 {
				t.Fatalf("ordinary update advanced work version to %d, want %d", got, expected+1)
			}
			if got := workflowPlanFixtureFrontier(t, s, workID); got != step {
				t.Fatalf("ordinary update reset step %s to %s", step, got)
			}
			after, err := readWorkflowContractVersion(t.Context(), s.db, workID, previous)
			if err != nil {
				t.Fatal(err)
			}
			after.SupersededBy = before.SupersededBy
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("historical contract content changed: before=%+v after=%+v", before, after)
			}
			var bindingsAfter int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, workID).Scan(&bindingsAfter); err != nil || bindingsAfter != bindingsBefore {
				t.Fatalf("stored bindings = %d, %v; want %d", bindingsAfter, err, bindingsBefore)
			}
			if got := contractRecordBindingSnapshot(t, s, workID); got != bindingsSnapshot {
				t.Fatal("ordinary update changed stored binding bytes")
			}
		})
	}
}

func contractRecordBindingSnapshot(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var snapshot string
	if err := s.db.QueryRow(`SELECT COALESCE(group_concat(hex(payload),'|'),'') FROM (SELECT payload FROM workflow_evidence_bindings WHERE work_id=? ORDER BY binding_seq)`, workID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestContractRecordUpdateRefusalsAreAtomic(t *testing.T) {
	for _, mutant := range []string{"malformed", "missing premise", "unknown field", "null", "current version", "skipped version", "stale work version"} {
		t.Run(mutant, func(t *testing.T) {
			workID := "record-refusal-" + fmt.Sprint(len(mutant))
			f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanFix, "plan", nil)
			defer f.store.Close()
			expected := readWorkVersion(t, f.store, workID)
			contract := contractRecordUpdate(t, f.store, workID)
			switch mutant {
			case "missing premise":
				delete(contract, "premise")
			case "unknown field":
				contract["unrecognized"] = "field"
			case "null":
				contract = nil
			case "malformed":
				contract["legislative_subjects"] = []any{"spec:one", "spec:one"}
			case "current version":
				contract["contract_version"] = 1
			case "skipped version":
				contract["contract_version"] = 3
			case "stale work version":
				expected--
			}
			before := contractRecordEffectSnapshot(t, f.store, workID)
			if err := reviseContractRecord(t, f.store, workID, f.owner, contract, expected); err == nil {
				t.Fatal("invalid record update succeeded")
			}
			if after := contractRecordEffectSnapshot(t, f.store, workID); after != before {
				t.Fatalf("refused update had effects: %s -> %s", before, after)
			}
			version, err := activeWorkflowContractVersion(t.Context(), f.store.db, workID, "test")
			if err != nil || version != 1 {
				t.Fatalf("refused update changed active contract to %d, %v", version, err)
			}
		})
	}
}

func contractRecordEffectSnapshot(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var content string
	if err := s.db.QueryRow(`SELECT json_object('intent',intent_json,'contracts',(SELECT count(*) FROM workflow_contracts WHERE work_id=w.id),'actors',(SELECT count(*) FROM workflow_actors)) FROM work_items w WHERE id=?`, workID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	return contractCorrectionEffectSnapshot(t, s, workID) + content
}
