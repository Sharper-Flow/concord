package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The plan prefix is synthetic; Review and verification bindings use the
// actual dispatcher and retain their own candidate and actor identities.
func deliveryPlanFixture(t *testing.T, workID string, required []string, disposition string) (workflowReturnRouteFixture, string) {
	t.Helper()
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", required, nil)
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	commit := runTestGit(t, repo, "rev-parse", "HEAD")
	if disposition != "" {
		report := currentReviewBlockForTest(t, f.store, workID, disposition)
		request := planAdmissionRequest(workID, "record_review", readWorkVersion(t, f.store, workID), f.owner, mustJSONValue(map[string]any{"review": report}))
		if err := planAdmissionDispatch(t, f.store, request); err != nil {
			t.Fatalf("record Review: %v", err)
		}
	}
	moveWorkflowPlanFixtureFrontier(t, f.store, workID, "review")
	return f, commit
}

func bindDeliveryReport(t *testing.T, f workflowReturnRouteFixture, workID, ref, commit, result string) {
	t.Helper()
	payload := mustJSONValue(map[string]any{
		"evidence_kind": "verification", "immutable_subject_ref": ref,
		"verification_report": VerificationReport{Source: "local", Commit: commit, Result: result, RunRef: "run:" + ref},
	})
	request := planAdmissionRequest(workID, "bind_evidence", readWorkVersion(t, f.store, workID), f.owner, payload)
	if err := planAdmissionDispatch(t, f.store, request); err != nil {
		t.Fatalf("bind %s: %v", ref, err)
	}
}

func TestWorkflowPlanDeliverySharedAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, review string
		required     []string
		reports      []string
		want         FailureKind
	}{
		{"missing-review", "", []string{"verification"}, nil, KindMissingEvidence},
		{"review-not-ready", "continue", []string{"verification"}, nil, KindMissingEvidence},
		{"missing-proof", "ready_for_final_proof", []string{"verification"}, nil, KindMissingEvidence},
		{"failed-proof", "ready_for_final_proof", []string{"verification"}, []string{"fail"}, KindMissingEvidence},
		{"failed-then-passing", "ready_for_final_proof", []string{"verification"}, []string{"fail", "pass"}, ""},
		{"passing-then-failed", "ready_for_final_proof", []string{"verification"}, []string{"pass", "fail"}, KindMissingEvidence},
		{"passing-proof", "ready_for_final_proof", []string{"verification"}, []string{"pass"}, ""},
		{"verification-not-required", "ready_for_final_proof", []string{}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "plan-delivery-" + tc.name
			f, commit := deliveryPlanFixture(t, workID, tc.required, tc.review)
			for i, result := range tc.reports {
				bindDeliveryReport(t, f, workID, fmt.Sprintf("report:%d", i), commit, result)
			}
			pin, err := ReadWorkPin(t.Context(), f.store, workID)
			if err != nil || pin.Step != "review" || !workPinContainsAction(pin.NextValidIntents, "record_delivery") || !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
				t.Fatalf("plan pin=%+v err=%v", pin, err)
			}
			payload := json.RawMessage(`{"delivery_artifact":"artifact:plan-delivery","delivery_state":"asserted"}`)
			request := planAdmissionRequest(workID, "record_delivery", pin.Version, f.owner, payload)
			var eventCount, operationCount int
			if err := f.store.db.QueryRow(`SELECT (SELECT count(*) FROM effect_receipts WHERE subject_id=?),(SELECT count(*) FROM durable_operations WHERE work_id=?)`, workID, workID).Scan(&eventCount, &operationCount); err != nil {
				t.Fatal(err)
			}
			preflight := planAdmissionPreflight(t, f.store, request)
			dispatch := planAdmissionDispatch(t, f.store, request)
			if tc.want != "" {
				first := planAdmissionFailure(t, preflight, tc.want)
				second := planAdmissionFailure(t, dispatch, tc.want)
				var afterEvents, afterOperations int
				if err := f.store.db.QueryRow(`SELECT (SELECT count(*) FROM effect_receipts WHERE subject_id=?),(SELECT count(*) FROM durable_operations WHERE work_id=?)`, workID, workID).Scan(&afterEvents, &afterOperations); err != nil {
					t.Fatal(err)
				}
				if first.Detail != second.Detail || readWorkVersion(t, f.store, workID) != pin.Version || afterEvents != eventCount || afterOperations != operationCount {
					t.Fatalf("refusal parity or effects: preflight=%v dispatch=%v", first, second)
				}
				if tc.name == "missing-proof" {
					bindDeliveryReport(t, f, workID, "report:recovery", commit, "pass")
					request = planAdmissionRequest(workID, "record_delivery", readWorkVersion(t, f.store, workID), f.owner, payload)
					if err := planAdmissionPreflight(t, f.store, request); err != nil {
						t.Fatalf("delivery preflight after recovery: %v", err)
					}
					if err := planAdmissionDispatch(t, f.store, request); err != nil {
						t.Fatalf("delivery dispatch after recovery: %v", err)
					}
				}
				return
			}
			if preflight != nil || dispatch != nil {
				t.Fatalf("delivery preflight=%v dispatch=%v", preflight, dispatch)
			}
			var actor string
			if err := f.store.db.QueryRow(`SELECT actor FROM effect_receipts WHERE subject_id=? AND kind=? ORDER BY effect_seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&actor); err != nil || actor != mustActorRef(t, f.owner) {
				t.Fatalf("delivery actor=%q err=%v", actor, err)
			}
			if got := workflowPlanFixtureFrontier(t, f.store, workID); got != "review" {
				t.Fatalf("delivery changed frontier to %q", got)
			}
		})
	}
}

func TestWorkflowPlanDeliveryMalformedProofKeepsRecovery(t *testing.T) {
	const workID = "plan-delivery-malformed"
	f, _ := deliveryPlanFixture(t, workID, []string{"verification"}, "ready_for_final_proof")
	// Corrupt only this isolated projection to exercise fail-closed recovery.
	if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_evidence_bindings(work_id,binding_seq,binding_id,actor,payload) VALUES(?,999999,'binding:malformed',?,'{"evidence_kind":"verification","immutable_subject_ref":"report:malformed","verification_report":{"source":"local"}}'); DELETE FROM write_guard`, workID, mustActorRef(t, f.owner)); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.store, workID)
	request := planAdmissionRequest(workID, "record_delivery", version, f.owner, json.RawMessage(`{"delivery_artifact":"artifact:malformed","delivery_state":"asserted"}`))
	first := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindInvariantViolation)
	second := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindInvariantViolation)
	if first.Detail != second.Detail || readWorkVersion(t, f.store, workID) != version {
		t.Fatalf("malformed proof parity: %v / %v", first, second)
	}
	bind := planAdmissionRequest(workID, "bind_evidence", version, f.owner, json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"artifact:recovery"}`))
	if err := planAdmissionPreflight(t, f.store, bind); err != nil {
		t.Fatalf("malformed proof hid binding recovery: %v", err)
	}
}

func TestWorkflowPlanDeliveryAmbiguousContractUpdateIsAtomic(t *testing.T) {
	const workID = "plan-delivery-duplicate-contract"
	f, _ := deliveryPlanFixture(t, workID, []string{}, "ready_for_final_proof")
	contract := pContractSuccessor(t, f.store, workID, "Retain the current delivery proof.")
	duplicateActiveWorkflowContract(context.Background(), t, f.store, workID)
	before := lawUpgradeSnapshot(t, f.store)
	version := readWorkVersion(t, f.store, workID)
	err := reviseContractRecord(t, f.store, workID, f.owner, contract, version)
	_ = planAdmissionFailure(t, err, KindInvariantViolation)
	if after := lawUpgradeSnapshot(t, f.store); !reflect.DeepEqual(before, after) || readWorkVersion(t, f.store, workID) != version {
		t.Fatal("refused ambiguous contract update changed application tables")
	}
}

func TestWorkflowPlanDeliveryRejectsWrongCandidate(t *testing.T) {
	const workID = "plan-delivery-wrong-candidate"
	f, commit := deliveryPlanFixture(t, workID, []string{"verification"}, "ready_for_final_proof")
	other := strings.Repeat("a", len(commit))
	if other == commit {
		other = strings.Repeat("b", len(commit))
	}
	bindDeliveryReport(t, f, workID, "report:other", other, "pass")
	request := planAdmissionRequest(workID, "record_delivery", readWorkVersion(t, f.store, workID), f.owner, json.RawMessage(`{"delivery_artifact":"artifact:wrong","delivery_state":"asserted"}`))
	first := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindStaleRequiresReview)
	second := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindStaleRequiresReview)
	if first.Detail != second.Detail || readWorkVersion(t, f.store, workID) != request.ExpectedVersion {
		t.Fatalf("wrong-candidate refusal: %v / %v", first, second)
	}
}
