package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func reviseContractRecord(t *testing.T, s *Store, workID string, owner WorkflowActor, contract map[string]any, expected int64, registry DefinitionRegistry) error {
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
		"title": "Updated plan", "value_statement": "Retain valid proof", "kind": kind, "tags": []string{},
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
		Registry:         registry,
	})
	return err
}

func contractRecordUpdate(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	return pContractSuccessor(t, s, workID, "An ordinary revised plan.")
}

func TestContractRecordUpdatePreservesProofAndProvenance(t *testing.T) {
	for _, scenario := range []string{"unchanged", "empty-requirements", "criterion", "subject", "ancestry-unchanged", "ancestry-criterion", "ancestry-subject"} {
		t.Run(scenario, func(t *testing.T) {
			change := strings.TrimPrefix(scenario, "ancestry-")
			f, _, refs := pReceiptFixture(t, "record-update-"+scenario, 1, 1)
			s := f.store
			if err := runVerdictActionAs(t, s, f.workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
				t.Fatal(err)
			}
			proof, err := latestWorkflowVerdicts(t.Context(), s.db, f.workID, 1)
			if err != nil || len(proof) != 1 {
				t.Fatalf("initial proof = %+v, %v", proof, err)
			}
			// Historical projections can carry the retired convention and a
			// sequence after accepted proof. Neither is a current proof cutoff.
			if strings.HasPrefix(scenario, "ancestry-") {
				if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
					UPDATE workflow_contracts SET route_conventions='["complete_step_correction"]',supersession_seq=(SELECT max(effect_seq)+1 FROM effect_receipts WHERE subject_id=?) WHERE work_id=? AND contract_version=1;
					DELETE FROM write_guard`, f.workID, f.workID); err != nil {
					t.Fatal(err)
				}
				intermediate := contractRecordUpdate(t, s, f.workID)
				intermediate["route_conventions"] = []string{}
				if err := reviseContractRecord(t, s, f.workID, f.owner, intermediate, readWorkVersion(t, s, f.workID), nil); err != nil {
					t.Fatal(err)
				}
			}
			previous, err := activeWorkflowContractVersion(t.Context(), s.db, f.workID, "test")
			if err != nil {
				t.Fatal(err)
			}
			bindingsSnapshot := contractRecordBindingSnapshot(t, s, f.workID)
			if bindingsSnapshot == "" {
				t.Fatal("fixture carries no stored evidence bindings")
			}
			var bindingsBefore int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, f.workID).Scan(&bindingsBefore); err != nil {
				t.Fatal(err)
			}
			before, err := readWorkflowContractVersion(t.Context(), s.db, f.workID, previous)
			if err != nil {
				t.Fatal(err)
			}
			step := currentStep(t, s, f.workID)
			expected := readWorkVersion(t, s, f.workID)
			contract := contractRecordUpdate(t, s, f.workID)
			if change == "empty-requirements" {
				contract["required_evidence"] = []string{}
			}
			predicate := contract["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)
			if change == "criterion" {
				predicate["check_ref"] = "check:revised"
			}
			if change == "subject" {
				predicate["immutable_subject_ref"] = "commit:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			}
			if err := reviseContractRecord(t, s, f.workID, f.owner, contract, expected, nil); err != nil {
				t.Fatal(err)
			}
			active, err := activeWorkflowContractVersion(t.Context(), s.db, f.workID, "test")
			if err != nil || active != previous+1 {
				t.Fatalf("active contract = %d, %v; want next retained version %d", active, err, previous+1)
			}
			updated, err := readWorkflowContractVersion(t.Context(), s.db, f.workID, active)
			if err != nil {
				t.Fatal(err)
			}
			if change == "empty-requirements" && len(updated.RequiredEvidence) != 0 {
				t.Fatalf("complete update defaulted an explicitly empty evidence list: %v", updated.RequiredEvidence)
			}
			if got := readWorkVersion(t, s, f.workID); got != expected+1 {
				t.Fatalf("ordinary update advanced work version to %d, want %d", got, expected+1)
			}
			if got := currentStep(t, s, f.workID); got != step {
				t.Fatalf("ordinary update reset step %s to %s", step, got)
			}
			after, err := readWorkflowContractVersion(t.Context(), s.db, f.workID, previous)
			if err != nil {
				t.Fatal(err)
			}
			after.SupersededBy = before.SupersededBy
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("historical contract content changed: before=%+v after=%+v", before, after)
			}
			var bindingsAfter int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, f.workID).Scan(&bindingsAfter); err != nil || bindingsAfter != bindingsBefore {
				t.Fatalf("stored bindings = %d, %v; want %d", bindingsAfter, err, bindingsBefore)
			}
			if got := contractRecordBindingSnapshot(t, s, f.workID); got != bindingsSnapshot {
				t.Fatal("ordinary update changed stored binding bytes")
			}
			current, err := latestWorkflowVerdicts(t.Context(), s.db, f.workID, active)
			if err != nil {
				t.Fatal(err)
			}
			unchanged := change == "unchanged" || change == "empty-requirements"
			if unchanged && !reflect.DeepEqual(current, proof) {
				t.Fatalf("unchanged criterion lost valid proof: %+v", current)
			}
			if !unchanged && len(current) != 0 {
				t.Fatalf("changed %s gained old proof: %+v", change, current)
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
	for _, mutant := range []string{"malformed", "missing premise", "unknown field", "null", "current version", "skipped version", "stale work version", "unregistered pin"} {
		t.Run(mutant, func(t *testing.T) {
			f := seedWorkContextFixture(t, "record-refusal-"+fmt.Sprint(len(mutant)))
			defer f.store.Close()
			expected := readWorkVersion(t, f.store, f.workID)
			contract := contractRecordUpdate(t, f.store, f.workID)
			var registry DefinitionRegistry
			switch mutant {
			case "unregistered pin":
				registry = NewWorkflowDefinitionRegistry()
			case "missing premise":
				delete(contract, "premise")
			case "unknown field":
				contract["unrecognized"] = "field"
			case "null":
				contract = nil
			case "malformed":
				contract["outcome_predicates"] = []any{}
			case "current version":
				contract["contract_version"] = 1
			case "skipped version":
				contract["contract_version"] = 3
			case "stale work version":
				expected--
			}
			before := contractRecordEffectSnapshot(t, f.store, f.workID)
			if err := reviseContractRecord(t, f.store, f.workID, f.owner, contract, expected, registry); err == nil {
				t.Fatal("invalid record update succeeded")
			}
			if after := contractRecordEffectSnapshot(t, f.store, f.workID); after != before {
				t.Fatalf("refused update had effects: %s -> %s", before, after)
			}
			version, err := activeWorkflowContractVersion(t.Context(), f.store.db, f.workID, "test")
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

func TestContractRecordUpdatePreservesOutsideRepairHold(t *testing.T) {
	for _, withContract := range []bool{true, false} {
		t.Run(fmt.Sprintf("contract=%t", withContract), func(t *testing.T) {
			f := seedWorkContextFixture(t, fmt.Sprintf("record-outside-hold-%t", withContract))
			defer f.store.Close()
			req := outsideRepairTestRequest(t, f.store, f.workID, "hold")
			req.Actor = f.owner
			if err := f.store.TransactDurable(t.Context(), func(tx *Transaction) error {
				_, err := SetOutsideRepairDispositionTx(t.Context(), tx, req)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			before := outsideRepairDatabaseSnapshot(t, f.store)
			expected := readWorkVersion(t, f.store, f.workID)
			if withContract {
				err := reviseContractRecord(t, f.store, f.workID, f.owner, contractRecordUpdate(t, f.store, f.workID), expected, nil)
				assertFailureKind(t, err, KindOutsideRepairActive)
				if after := outsideRepairDatabaseSnapshot(t, f.store); after != before {
					t.Fatal("held contract update changed durable state")
				}
				return
			}
			payload := map[string]any{
				"title": "Updated intent", "value_statement": "Keep the hold", "kind": "task", "tags": []string{},
				"reason": "update intent only", "expected_version": expected, "resulting_version": expected + 1,
			}
			if err := applyWorkEvent(t, f.store, workflowEvent(f.workID+"-intent", "work.intent_revised", f.workID, payload), workVersion(f.workID, expected)); err != nil {
				t.Fatal(err)
			}
			active, err := outsideRepairActiveTx(t.Context(), f.store.db, f.workID)
			if err != nil || !active {
				t.Fatalf("intent-only update cleared the hold: %t, %v", active, err)
			}
		})
	}
}
