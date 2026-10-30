package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCoordinatorReviewCanonicalResolutionFoldsRetainedFinding(t *testing.T) {
	state := WorkflowReviewState{Findings: []ReviewFinding{{FindingID: "local:one", Status: "open", InScope: true, Blocking: true}, {FindingID: "local:other", Status: "open", InScope: true, Blocking: true}}, findingSources: map[string]reviewFindingSource{"local:one": {CanonicalID: "finding:10:0"}, "local:other": {CanonicalID: "finding:11:0"}}}
	report := ReviewBlock{ResolvedFindings: []ReviewResolution{{FindingID: "finding:10:0", EvidenceRefs: []string{"verify:one"}}}}
	applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{Report: report}, workflowReviewFindingMap(state))
	findings := workflowReviewFindingMap(state)
	if findings["local:one"].Status != "closed" || len(findings["local:one"].ClosureRefs) != 1 || findings["local:other"].Status != "open" {
		t.Fatalf("canonical resolution changed wrong findings: %+v", state.Findings)
	}
	if _, fabricated := findings["finding:10:0"]; fabricated {
		t.Fatal("canonical resolution fabricated a second report-local finding")
	}
}

func TestCoordinatorReviewResolutionRejectsUnknownCanonicalID(t *testing.T) {
	state := WorkflowReviewState{Findings: []ReviewFinding{{FindingID: "local:one", Status: "open"}}, findingSources: map[string]reviewFindingSource{"local:one": {CanonicalID: "finding:10:0"}}}
	for _, id := range []string{"local:one", "finding:11:0"} {
		report := ReviewBlock{ResolvedFindings: []ReviewResolution{{FindingID: id, EvidenceRefs: []string{"verify:one"}}}}
		if err := validateReviewFindingChanges(&report, state); err == nil {
			t.Fatalf("resolution admitted unknown canonical identity %s", id)
		}
	}
	report := ReviewBlock{ResolvedFindings: []ReviewResolution{{FindingID: "finding:10:0", EvidenceRefs: []string{"review:judgment"}}}, Findings: state.Findings}
	if err := validateReviewFindingChanges(&report, state); err == nil {
		t.Fatal("resolution contradicted an explicitly open finding")
	}
}

func TestCoordinatorReviewPlainFindingClosesWithoutExecutableInventory(t *testing.T) {
	candidate := ReviewCandidate{ProjectID: "project:one", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	deps := ReviewDependencies{Criteria: []ReviewCriterion{{PredicateID: "predicate:one", ContentDigest: "sha256:" + strings.Repeat("c", 64)}}, ArchitectureNotApplicable: true}
	finding := ReviewFinding{FindingID: "local:one", Status: "open", InScope: true, Blocking: true, Severity: "P1", Confidence: "high", Detail: "The reported outcome differs from the accepted criterion.", EvidenceRefs: []string{"review:observation"}, ClosureRefs: []string{}}
	report := ReviewBlock{Disposition: "correction_required", InputCandidate: candidate, OutputCandidate: candidate, Dependencies: deps, Findings: []ReviewFinding{finding}, Assessments: []ReviewAssessment{}, RemainingObligations: []ReviewObligation{}}
	state := WorkflowReviewState{Dependencies: &deps}
	for _, closed := range []bool{false, true} {
		if closed {
			report.Disposition = "ready_for_final_proof"
			report.Findings[0].Status = "closed"
			report.Findings[0].ClosureRefs = []string{"review:corrected-outcome"}
		}
		if err := validateReviewBlock(&report); err != nil {
			t.Fatal(err)
		}
		if err := validateReviewFindingChanges(&report, state); err != nil {
			t.Fatalf("ordinary finding closed=%v: %v", closed, err)
		}
		applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{Report: report}, workflowReviewFindingMap(state))
		if !closed {
			if err := workflowAdmitReview(state); err == nil {
				t.Fatal("open blocking finding allowed final proof")
			}
		} else if err := workflowAdmitReview(state); err != nil {
			t.Fatalf("coordinator judgment did not close ordinary finding: %v", err)
		}
	}
	if len(state.Findings) != 1 || state.Findings[0].Oracle != nil || len(state.findingSources) != 0 {
		t.Fatalf("ordinary judgment manufactured oracle provenance: %+v", state)
	}
}

func TestCoordinatorReviewFindingIdentityAndHistoricalMetadata(t *testing.T) {
	old := ReviewFinding{FindingID: "local:one", Status: "open", InScope: true, Blocking: true, Oracle: &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:one"}}
	state := WorkflowReviewState{Findings: []ReviewFinding{old}, findingSources: map[string]reviewFindingSource{"local:one": {CanonicalID: "finding:10:0", EventID: "report:original"}}}
	applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{Report: ReviewBlock{Findings: []ReviewFinding{}}}, workflowReviewFindingMap(state))
	if len(state.Findings) != 1 || state.Findings[0].Status != "open" || !state.Findings[0].Blocking {
		t.Fatalf("omitting a finding silently cleared its blocker: %+v", state)
	}
	for _, tc := range []struct {
		name    string
		finding ReviewFinding
		valid   bool
	}{
		{"ordinary-new", ReviewFinding{FindingID: "local:new", Status: "open"}, true},
		{"unknown-closed", ReviewFinding{FindingID: "local:new", Status: "closed"}, false},
		{"new-oracle", ReviewFinding{FindingID: "local:new", Status: "open", Oracle: old.Oracle}, false},
		{"changed-oracle", ReviewFinding{FindingID: old.FindingID, Status: "open", Oracle: &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:other"}}, false},
		{"retained-oracle", old, true},
		{"plain-historical-closure", ReviewFinding{FindingID: old.FindingID, Status: "closed", ClosureRefs: []string{"review:judgment"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := ReviewBlock{Findings: []ReviewFinding{tc.finding}}
			if err := validateReviewFindingChanges(&report, state); (err == nil) != tc.valid {
				t.Fatalf("finding validation=%v, valid=%v", err, tc.valid)
			}
		})
	}
	closed := ReviewBlock{Findings: []ReviewFinding{{FindingID: old.FindingID, Status: "closed", ClosureRefs: []string{"review:judgment"}}}}
	applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{Report: closed}, workflowReviewFindingMap(state))
	if !oracleEntryIdentical(state.Findings[0].Oracle, old.Oracle) || !state.Findings[0].Blocking || !state.Findings[0].InScope || state.findingSources[old.FindingID].EventID != "report:original" {
		t.Fatalf("historical finding lost its original metadata or obligation: %+v", state)
	}
}

func TestCoordinatorReviewCandidatesRequireExactObservedInputAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ReviewBlock, **WorktreeVerifyResult, **WorkflowSubjectPreparation, **ReviewCandidate)
		valid  bool
	}{
		{"unchanged", func(*ReviewBlock, **WorktreeVerifyResult, **WorkflowSubjectPreparation, **ReviewCandidate) {}, true},
		{"corrected", func(r *ReviewBlock, _ **WorktreeVerifyResult, _ **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			r.InputCandidate.Commit = strings.Repeat("d", 40)
		}, true},
		{"foreign-project", func(r *ReviewBlock, _ **WorktreeVerifyResult, _ **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			r.InputCandidate.ProjectID = "project:other"
		}, false},
		{"missing-receipt", func(_ *ReviewBlock, c **WorktreeVerifyResult, _ **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			*c = nil
		}, false},
		{"foreign-receipt", func(_ *ReviewBlock, c **WorktreeVerifyResult, _ **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			(*c).ProjectID = "project:other"
		}, false},
		{"old-receipt", func(_ *ReviewBlock, c **WorktreeVerifyResult, _ **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			(*c).SubjectRef = "commit:" + strings.Repeat("d", 40)
		}, false},
		{"missing-observation", func(_ *ReviewBlock, _ **WorktreeVerifyResult, p **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			*p = nil
		}, false},
		{"foreign-observation", func(_ *ReviewBlock, _ **WorktreeVerifyResult, p **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			(*p).workID = "work:other"
		}, false},
		{"wrong-observed-tree", func(_ *ReviewBlock, _ **WorktreeVerifyResult, p **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			(*p).candidate.Tree = strings.Repeat("e", 40)
		}, false},
		{"same-commit-other-tree", func(r *ReviewBlock, _ **WorktreeVerifyResult, _ **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			r.InputCandidate.Tree = strings.Repeat("e", 40)
		}, false},
		{"unretained-input", func(r *ReviewBlock, _ **WorktreeVerifyResult, _ **WorkflowSubjectPreparation, old **ReviewCandidate) {
			r.InputCandidate.Commit = strings.Repeat("d", 40)
			*old = nil
		}, false},
		{"different-retained-input", func(r *ReviewBlock, _ **WorktreeVerifyResult, _ **WorkflowSubjectPreparation, _ **ReviewCandidate) {
			r.InputCandidate.Commit = strings.Repeat("e", 40)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := ReviewCandidate{ProjectID: "project:one", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
			report := ReviewBlock{InputCandidate: candidate, OutputCandidate: candidate}
			observed := candidate
			prepared := &WorkflowSubjectPreparation{workID: "work:one", candidate: &observed}
			current := &WorktreeVerifyResult{ProjectID: candidate.ProjectID, SubjectRef: "commit:" + candidate.Commit}
			retained := &ReviewCandidate{ProjectID: candidate.ProjectID, Commit: strings.Repeat("d", 40), Tree: candidate.Tree}
			tc.change(&report, &current, &prepared, &retained)
			err := validateReviewCandidates(&report, current, retained)
			if err == nil {
				err = validateRecordedReviewObservation(WithWorkflowSubjectPreparation(t.Context(), prepared), "work:one", &report)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("candidate admission=%v, valid=%v", err, tc.valid)
			}
		})
	}
}

func TestCoordinatorReviewPreflightDefersObservationToAtomicFold(t *testing.T) {
	j, _ := retainedReviewFixture(t, "review-preflight-observation")
	report := currentReviewBlockForTest(t, j.s, j.workID, "", "continue")
	preflight := func() error {
		return WorkflowActionPreflightWithRegistry(t.Context(), j.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: j.workID, ExpectedVersion: readWorkVersion(t, j.s, j.workID), ActionID: "record_review",
			Payload: mustJSONValue(map[string]any{"review": report}), Actor: j.fixture.owner,
		})
	}
	before := j.effects(t)
	if err := preflight(); err != nil {
		t.Fatalf("SQL-only inspection requires a premature Git observation: %v", err)
	}
	if j.effects(t) != before {
		t.Fatal("Review inspection changed durable state")
	}
	tree := report.OutputCandidate.Tree
	report.InputCandidate.Tree, report.OutputCandidate.Tree = strings.Repeat("e", 40), strings.Repeat("e", 40)
	if err := preflight(); err != nil {
		t.Fatalf("inspection compared a tree before native observation: %v", err)
	}
	if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("live Review admitted a tree the native probe did not observe: %v", err)
	}
	if j.effects(t) != before {
		t.Fatal("refused Review changed durable state")
	}
	report.InputCandidate.Tree, report.OutputCandidate.Tree = tree, tree
	if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report); err != nil {
		t.Fatalf("live Review refused the exact native observation: %v", err)
	}
}

func TestCoordinatorCorrectionDoesNotRequireWorkerFindingAuthoring(t *testing.T) {
	db := nativeTaskRetirementDB(t)
	state := workflowReviewFacts{Findings: []ReviewFinding{{FindingID: "local:one", Status: "open", Blocking: true, Oracle: &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:one"}}}, Sources: map[string]reviewFindingSource{"local:one": {CanonicalID: "finding:7:0"}}}
	if _, err := db.Exec(`UPDATE workflow_review_state SET state=? WHERE work_id='work:one'`, workflowJSON(state)); err != nil {
		t.Fatal(err)
	}
	if err := validateCorrectionOpenFindingsTx(t.Context(), db, "work:one", nil, false); err != nil {
		t.Fatalf("optional historical metadata required new canonical authoring: %v", err)
	}
	if err := validateCorrectionOpenFindingsTx(t.Context(), db, "work:one", []string{"finding:7:0"}, true); err != nil {
		t.Fatal(err)
	}
	if err := validateCorrectionOpenFindingsTx(t.Context(), db, "work:one", []string{"finding:8:0"}, true); err == nil {
		t.Fatal("explicit correction reference replaced the retained blocker")
	}
}

func TestCoordinatorReviewSecondPassRetainsSizeProof(t *testing.T) {
	state := workSizeProofFacts{}
	for _, event := range []struct{ action, id string }{{"record_review", "review:first"}, {"checkpoint_refine", "refine:one"}, {"record_review", "review:second"}} {
		if err := advanceWorkSizeProofWindow(&state, WorkflowActionCompleted, event.action, event.id, event.action == "record_review", func() (bool, error) { return true, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if len(state.Proofs) != 1 || state.Proofs[0] != "review:second" {
		t.Fatalf("recorded Review lost second-pass proof: %+v", state)
	}
	completed := workflowActionCompletedPayload{ActionID: "record_review", Review: &ReviewBlock{Disposition: "correction_required", Findings: []ReviewFinding{{Severity: "P1"}}}}
	raw, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	if qualifies, err := workSizeReviewFindsInScope(string(raw), 10); err != nil || !qualifies {
		t.Fatalf("coordinator Review size proof=%v err=%v", qualifies, err)
	}
}

func TestCoordinatorCorrectionFactsHaveNoWorkerConsumer(t *testing.T) {
	request := Event{Kind: WorkflowActionCompleted, EventID: "correction:one", Seq: 10, Payload: []byte(`{"action_id":"request_correction","correction_diagnosis":"reported failure","correction_strategy":"correct the candidate","correction_predicate_ids":["predicate:one"],"correction_evidence_refs":["verify:one"]}`)}
	state, changed, err := correctionStateForEvent(request)
	if err != nil || !changed || len(state.Records) != 1 || state.Records[0].SourceID != request.EventID {
		t.Fatalf("correction record=%+v changed=%v err=%v", state, changed, err)
	}
	request.Seq++
	request.Payload = []byte(`{"action_id":"record_review","review":{"disposition":"correction_required"}}`)
	if _, changed, err := correctionStateForEvent(request); err != nil || changed {
		t.Fatalf("unchanged corrective Review consumed its unfinished direction: %v %v", changed, err)
	}
	request.Payload = []byte(`{"action_id":"record_review","review":{"disposition":"ready_for_final_proof"}}`)
	state, changed, err = correctionStateForEvent(request)
	if err != nil || !changed || state.Boundary != request.Seq || len(state.Records) != 0 {
		t.Fatalf("recorded Review did not consume correction context: %+v %v %v", state, changed, err)
	}
	request.Payload = []byte(`{"action_id":"dispatch_worker"}`)
	if _, changed, err := correctionStateForEvent(request); err != nil || changed {
		t.Fatalf("retired worker action consumed correction: %v %v", changed, err)
	}
}

func TestCoordinatorReviewActionExists(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	for _, action := range definition.ActionDefinitions {
		if action.ID == "record_review" {
			return
		}
	}
	t.Fatal("coordinator cannot record a subject-bound Review without a worker attempt")
}

func TestCoordinatorReviewOracleFindingShape(t *testing.T) {
	for _, tc := range []struct {
		name    string
		finding ReviewOracleFinding
		valid   bool
	}{
		{"follow-up", ReviewOracleFinding{Classification: OracleClassificationFollowUp}, true},
		{"unknown-classification", ReviewOracleFinding{Classification: "accepted"}, false},
		{"invalid-owner", ReviewOracleFinding{Classification: OracleClassificationFollowUp, OwnerID: "case:wrong"}, false},
		{"duplicate-predicates", ReviewOracleFinding{Classification: OracleClassificationFollowUp, PredicateIDs: []string{"predicate:one", "predicate:one"}}, false},
		{"wrong-control-kind", ReviewOracleFinding{Classification: OracleClassificationFollowUp, ControlIDs: []string{"case:wrong"}}, false},
		{"unbounded-evidence", ReviewOracleFinding{Classification: OracleClassificationFollowUp, EvidenceRefs: []string{strings.Repeat("a", 257)}}, false},
		{"valid-lineage", ReviewOracleFinding{Classification: OracleClassificationFollowUp, ContinuesFindingID: "finding:1:0"}, true},
		{"invalid-lineage", ReviewOracleFinding{Classification: OracleClassificationFollowUp, ContinuesFindingID: "finding:one"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateReviewOracleFinding(&tc.finding); (err == nil) != tc.valid {
				t.Fatalf("finding shape=%v, valid=%v", err, tc.valid)
			}
		})
	}
}

func TestCoordinatorReviewResolutionShapeAndAggregateBound(t *testing.T) {
	for _, tc := range []struct {
		name       string
		resolution ReviewResolution
		valid      bool
	}{
		{"receipt", ReviewResolution{FindingID: "finding:1:0", EvidenceRefs: []string{"native:one"}}, true},
		{"judged-case", ReviewResolution{FindingID: "finding:1:0", JudgedCaseIDs: []string{"case:one"}}, true},
		{"missing-proof", ReviewResolution{FindingID: "finding:1:0"}, false},
		{"invalid-identity", ReviewResolution{FindingID: "finding:one", EvidenceRefs: []string{"native:one"}}, false},
		{"duplicate-proof", ReviewResolution{FindingID: "finding:1:0", EvidenceRefs: []string{"native:one", "native:one"}}, false},
		{"duplicate-case", ReviewResolution{FindingID: "finding:1:0", JudgedCaseIDs: []string{"case:one", "case:one"}}, false},
		{"wrong-case-kind", ReviewResolution{FindingID: "finding:1:0", JudgedCaseIDs: []string{"control:one"}}, false},
		{"explicit-empty-proof", ReviewResolution{FindingID: "finding:1:0", EvidenceRefs: []string{}, JudgedCaseIDs: []string{"case:one"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateReviewResolutions([]ReviewResolution{tc.resolution}); (err == nil) != tc.valid {
				t.Fatalf("resolution shape=%v, valid=%v", err, tc.valid)
			}
		})
	}
	resolutions := make([]ReviewResolution, 32)
	for i := range resolutions {
		resolutions[i] = ReviewResolution{FindingID: fmt.Sprintf("finding:%d:0", i+1), EvidenceRefs: []string{strings.Repeat("a", 200), strings.Repeat("b", 200)}}
	}
	var failure *Failure
	if err := validateReviewResolutions(resolutions); !failureAs(err, &failure) || failure.Kind != KindLimitExceeded {
		t.Fatalf("oversized resolution array=%v, want limit_exceeded", err)
	}
}

func TestCoordinatorReviewOnlyReadyDispositionAdvances(t *testing.T) {
	definition := WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{{ID: "record_review", ExecutionMode: ActionAdvance}},
		StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{{ID: "execution", Kind: WorkflowStepExternalEffect, Actions: []string{"record_review"}}}}}
	if workflowCompletedActionAdvances(definition, workflowActionCompletedPayload{StepID: "execution", ActionID: "record_review"}) {
		t.Fatal("missing Review advanced the step")
	}
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			completion := workflowActionCompletedPayload{StepID: "execution", ActionID: "record_review", Review: &ReviewBlock{Disposition: disposition}}
			if advances := workflowCompletedActionAdvances(definition, completion); advances != (disposition == "ready_for_final_proof") {
				t.Fatalf("disposition %s advances=%v", disposition, advances)
			}
			holding := cloneWorkflowDefinition(definition)
			holding.ActionDefinitions[0].ExecutionMode = ActionHold
			if workflowCompletedActionAdvances(holding, completion) {
				t.Fatal("a holding action advanced the step")
			}
		})
	}
}

func TestCoordinatorReviewReadinessPreservesSubjectAndFindingGates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*WorkflowReviewState)
		want   FailureKind
	}{
		{"ready", func(*WorkflowReviewState) {}, ""},
		{"missing-report", func(s *WorkflowReviewState) { s.Accepted = nil }, KindMissingEvidence},
		{"missing-candidate", func(s *WorkflowReviewState) { s.Candidate = nil }, KindMissingEvidence},
		{"wrong-project", func(s *WorkflowReviewState) { s.Accepted.Report.OutputCandidate.ProjectID = "project-b" }, KindStaleRequiresReview},
		{"wrong-commit", func(s *WorkflowReviewState) { s.Accepted.Report.OutputCandidate.Commit = strings.Repeat("c", 40) }, KindStaleRequiresReview},
		{"wrong-tree", func(s *WorkflowReviewState) { s.Accepted.Report.OutputCandidate.Tree = strings.Repeat("d", 40) }, KindStaleRequiresReview},
		{"stale-dependencies", func(s *WorkflowReviewState) { s.DependenciesCurrent = false }, KindStaleRequiresReview},
		{"continue", func(s *WorkflowReviewState) { s.Accepted.Report.Disposition = "continue" }, KindMissingEvidence},
		{"correction-required", func(s *WorkflowReviewState) { s.Accepted.Report.Disposition = "correction_required" }, KindMissingEvidence},
		{"revision-required", func(s *WorkflowReviewState) { s.Accepted.Report.Disposition = "revision_required" }, KindMissingEvidence},
		{"remaining-obligation", func(s *WorkflowReviewState) {
			s.Accepted.Report.RemainingObligations = []ReviewObligation{{ObligationID: "obligation:one"}}
		}, KindMissingEvidence},
		{"open-blocker", func(s *WorkflowReviewState) { s.Findings[0].Blocking = true }, KindMissingEvidence},
		{"crucial-medium-finding", func(s *WorkflowReviewState) { s.Crucial = true }, KindMissingEvidence},
		{"closed-crucial-finding", func(s *WorkflowReviewState) { s.Crucial = true; s.Findings[0].Status = "closed" }, ""},
		{"out-of-scope-crucial-finding", func(s *WorkflowReviewState) { s.Crucial = true; s.Findings[0].InScope = false }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := ReviewCandidate{ProjectID: "project-a", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
			state := WorkflowReviewState{
				Candidate: &candidate, DependenciesCurrent: true,
				Accepted: &WorkflowAcceptedReview{Report: ReviewBlock{Disposition: "ready_for_final_proof", OutputCandidate: candidate}},
				Findings: []ReviewFinding{{FindingID: "finding:one", InScope: true, Severity: "P2", Status: "open"}},
			}
			tc.mutate(&state)
			err := workflowAdmitReview(state)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != tc.want {
				t.Fatalf("readiness=%v, want %s", err, tc.want)
			}
		})
	}
}
