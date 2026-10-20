package store

// The published retry-window contract and the store producer must agree: the
// serialized WorkflowRetryWindow and WorkflowProgressAnchor values validate
// against the generated payload document, and the closed vocabularies and
// bounds refuse what the producer never writes (CD-0164 D5). Absent anchors
// serialize as explicit null, never a zero value.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func retryWindowAnchor(seq int64, qualification, obligation string) *WorkflowProgressAnchor {
	return &WorkflowProgressAnchor{
		EventID:       fmt.Sprintf("workflow-%064x:semantic:verdict:0", seq),
		Kind:          "workflow.verdict_recorded",
		Seq:           seq,
		Qualification: qualification,
		ObligationID:  obligation,
	}
}

func retryWindowFixture() *WorkflowRetryWindow {
	return &WorkflowRetryWindow{
		Population:                "distinct_failed_rejected_no_ship",
		Policy:                    WorkflowProgressPolicyObligationSatisfactionV1,
		Count:                     workflowCorrectionAttemptLimit,
		Limit:                     workflowCorrectionAttemptLimit,
		Prefix:                    90,
		DispatchCount:             workflowCorrectionAttemptLimit,
		DispatchLimit:             workflowCorrectionAttemptLimit,
		VerificationCount:         1,
		VerificationLimit:         workflowCorrectionAttemptLimit,
		NonprogressAnchor:         retryWindowAnchor(84, "comparable_predicate_satisfied", "predicate:"+strings.Repeat("a", 64)),
		WorkerSatisfactionAnchor:  retryWindowAnchor(60, "satisfying_worker_acceptance", "worker_result"),
		VerificationBaseline:      retryWindowAnchor(70, "comparable_healthy_verdict_baseline", ""),
		CorrectionEscalated:       true,
		BlockingPopulation:        "both",
		ConvergenceBasis:          "findings_shrinking",
		FundedConfirmingReviews:   1,
		UnresolvedObligations:     []string{"evidence:verification", "premise_confirmation"},
		UnresolvedObligationCount: 2,
	}
}

func TestWorkflowRetryWindowSerializedShapeMatchesPublishedContract(t *testing.T) {
	t.Parallel()
	valid := map[string]*WorkflowRetryWindow{
		"full obligation-satisfaction window": retryWindowFixture(),
		"legacy window without anchors": {
			Population:                   "distinct_failed_rejected_no_ship",
			Policy:                       "worker_acceptance",
			Count:                        1,
			Limit:                        workflowCorrectionAttemptLimit,
			Prefix:                       12,
			DispatchCount:                2,
			DispatchLimit:                workflowCorrectionAttemptLimit,
			VerificationCount:            0,
			VerificationLimit:            workflowCorrectionAttemptLimit,
			NonprogressAnchor:            nil,
			WorkerSatisfactionAnchor:     nil,
			VerificationBaseline:         nil,
			CorrectionEscalated:          false,
			BlockingPopulation:           "nonprogress",
			ConvergenceBasis:             "",
			FundedConfirmingReviews:      0,
			UnresolvedObligations:        []string{},
			UnresolvedObligationCount:    0,
			OmittedUnresolvedObligations: 0,
		},
	}
	for name, window := range valid {
		raw, err := json.Marshal(window)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := payloadschema.Validate("workflow_retry_window", raw); err != nil {
			t.Fatalf("%s: serialized retry window violates the published contract: %v", name, err)
		}
	}
	anchorRaw, err := json.Marshal(retryWindowAnchor(84, "required_premise_confirmed", "premise_confirmation"))
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("workflow_progress_anchor", anchorRaw); err != nil {
		t.Fatalf("serialized anchor violates the published contract: %v", err)
	}
	// Every closed qualification string the producer writes validates.
	for _, qualification := range []string{
		"satisfying_worker_acceptance",
		"required_evidence_bound",
		"comparable_predicate_satisfied",
		"required_premise_confirmed",
		"comparable_healthy_verdict_baseline",
	} {
		raw, err := json.Marshal(retryWindowAnchor(7, qualification, "evidence:verification"))
		if err != nil {
			t.Fatal(err)
		}
		if err := payloadschema.Validate("workflow_progress_anchor", raw); err != nil {
			t.Fatalf("qualification %s violates the published contract: %v", qualification, err)
		}
	}
	// An absent anchor serializes as an explicit null key, never an omitted
	// or zero-filled one.
	legacy, err := json.Marshal(valid["legacy window without anchors"])
	if err != nil {
		t.Fatal(err)
	}
	var shaped map[string]any
	if err := json.Unmarshal(legacy, &shaped); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nonprogress_anchor", "worker_satisfaction_anchor", "verification_baseline"} {
		value, present := shaped[key]
		if !present {
			t.Fatalf("absent anchor %s is omitted from the serialized window; want an explicit null", key)
		}
		if value != nil {
			t.Fatalf("absent anchor %s serialized as %v; want null", key, value)
		}
	}
}

func TestWorkflowRetryWindowClosedVocabularyRefusesForeignValues(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(*WorkflowRetryWindow){
		"unknown population":          func(w *WorkflowRetryWindow) { w.Population = "all_attempts" },
		"unknown policy":              func(w *WorkflowRetryWindow) { w.Policy = "coordinator_events" },
		"unknown blocking population": func(w *WorkflowRetryWindow) { w.BlockingPopulation = "neither" },
		"unknown qualification":       func(w *WorkflowRetryWindow) { w.NonprogressAnchor.Qualification = "step_advanced" },
		"unknown convergence basis":   func(w *WorkflowRetryWindow) { w.ConvergenceBasis = "operator_favor" },
		"negative count":              func(w *WorkflowRetryWindow) { w.Count = -1 },
		"negative dispatch count":     func(w *WorkflowRetryWindow) { w.DispatchCount = -1 },
		"negative verification count": func(w *WorkflowRetryWindow) { w.VerificationCount = -1 },
		"anchor without sequence":     func(w *WorkflowRetryWindow) { w.WorkerSatisfactionAnchor.Seq = 0 },
	}
	for name, mutate := range mutations {
		window := retryWindowFixture()
		mutate(window)
		raw, err := json.Marshal(window)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := payloadschema.Validate("workflow_retry_window", raw); err == nil {
			t.Fatalf("%s: the published contract admitted a value the producer never writes", name)
		}
	}
}

func TestWorkflowRetryWindowUnresolvedObligationsBoundWithExplicitOmission(t *testing.T) {
	t.Parallel()
	atLimit := retryWindowFixture()
	atLimit.UnresolvedObligations = make([]string, 0, WorkflowRetryWindowObligationLimit)
	for i := range WorkflowRetryWindowObligationLimit {
		atLimit.UnresolvedObligations = append(atLimit.UnresolvedObligations, fmt.Sprintf("evidence:verification-%02d", i))
	}
	atLimit.UnresolvedObligationCount = int64(len(atLimit.UnresolvedObligations))
	atLimit.OmittedUnresolvedObligations = 1
	raw, err := json.Marshal(atLimit)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("workflow_retry_window", raw); err != nil {
		t.Fatalf("window at the obligation limit violates the published contract: %v", err)
	}

	over := *atLimit
	over.UnresolvedObligations = append(append([]string{}, atLimit.UnresolvedObligations...), "evidence:one-too-many")
	over.UnresolvedObligationCount = int64(len(over.UnresolvedObligations))
	raw, err = json.Marshal(&over)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("workflow_retry_window", raw); err == nil {
		t.Fatal("the published contract admitted more unresolved obligations than the producer's explicit limit")
	}
}
