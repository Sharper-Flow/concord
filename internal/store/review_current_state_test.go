package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestReviewCurrentRequiresRecordedCandidate(t *testing.T) {
	for _, state := range []WorkflowReviewState{
		{},
		{Candidate: &reviewBlockFixture().OutputCandidate},
		{Accepted: &WorkflowAcceptedReview{Report: *reviewBlockFixture()}},
	} {
		if err := workflowAdmitReview(state); !hasFailureKind(err, KindMissingEvidence) {
			t.Fatalf("incomplete Review admitted: %+v, %v", state, err)
		}
	}
}

func TestReviewCurrentKeepsSharedAdmissionGuards(t *testing.T) {
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	base := WorkflowAdmissionState{Step: "execution", InstanceState: "running", Lifecycle: "in_progress", ActiveContracts: 1,
		Review: &WorkflowReviewState{}}
	if decision := workflowAdmit(entry.Definition, base, "record_review"); !decision.Admitted {
		t.Fatalf("ordinary Review was not admitted: %+v", decision.Failure)
	}
	for _, tc := range []struct {
		name string
		set  func(*WorkflowAdmissionState)
		kind FailureKind
	}{
		{"stale authority", func(s *WorkflowAdmissionState) {
			s.LawPinStaleError = newFailure(KindStaleRequiresReview, "test", "bound authority changed", false, "refresh the bound inputs")
		}, KindStaleRequiresReview},
		{"outside repair", func(s *WorkflowAdmissionState) { s.OutsideRepairActive = true }, KindOutsideRepairActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := base
			tc.set(&state)
			decision := workflowAdmit(entry.Definition, state, "record_review")
			if decision.Admitted || decision.Failure == nil || decision.Failure.Kind != tc.kind {
				t.Fatalf("ordinary Review bypassed %s: %+v", tc.name, decision)
			}
		})
	}
}

func TestReviewCurrentPlanRevisionCannotRestoreReadiness(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "missing Review"
		if accepted {
			name = "stale Review"
		}
		t.Run(name, func(t *testing.T) {
			f := seedWorkContextFixture(t, "review-plan-revision")
			t.Cleanup(func() { _ = f.store.Close() })
			repo, _ := reviewSubjectClaim(t, f.store, f.workID)
			var branch string
			if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "branch", "-m", branch)
			j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
			j.observeRun(t, "plan-basis")
			if accepted {
				acceptCurrentReviewForTest(t, f, "ready_for_final_proof")
			}
			before, err := ReadWorkPin(t.Context(), f.store, f.workID)
			if err != nil || before.Review == nil || before.Review.Dependencies == nil {
				t.Fatalf("current Review dependencies missing: %+v, %v", before.Review, err)
			}
			owner := mustActorRef(t, f.owner)
			event := workflowEventWithActor("design:replacement", WorkflowDesignRecorded, f.workID, owner, map[string]any{
				"work_id": f.workID, "expected_version": before.Version, "resulting_version": before.Version + 1,
				"approach":     "Keep the accepted value while changing its implementation plan.",
				"decisions":    []map[string]any{{"id": "decision:plan", "question": "Which implementation plan applies?", "choice": "Use the revised plan.", "rationale": "The inputs changed.", "rejected": []string{}}},
				"touched_refs": []string{"seed.txt"},
			})
			if err := applyWorkflowTestOperation(t.Context(), f.store, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.workID): before.Version}}); err != nil {
				t.Fatal(err)
			}
			after, err := ReadWorkPin(t.Context(), f.store, f.workID)
			if err != nil || after.Review == nil || after.Review.Dependencies == nil || after.Review.DependenciesCurrent || after.Review.Dependencies.ArchitectureDigest == before.Review.Dependencies.ArchitectureDigest || !reflect.DeepEqual(after.Review.Candidate, before.Review.Candidate) || !reflect.DeepEqual(after.Review.Accepted, before.Review.Accepted) {
				t.Fatalf("plan replacement retained readiness or rewrote recorded facts: %+v, %v", after.Review, err)
			}
			want := KindMissingEvidence
			if accepted {
				want = KindStaleRequiresReview
			}
			if err := workflowAdmitReview(*after.Review); !hasFailureKind(err, want) {
				t.Fatalf("plan replacement restored readiness: %v, want %s", err, want)
			}
		})
	}
}

func TestReviewCurrentDispositionAdmission(t *testing.T) {
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition = disposition
			report.Findings = []ReviewFinding{}
			report.RemainingObligations = []ReviewObligation{}
			state := WorkflowReviewState{Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{Report: *report}, DependenciesCurrent: true, Findings: []ReviewFinding{}}
			before := string(mustJSONValue(state))
			err := workflowAdmitReview(state)
			if (err == nil) != (disposition == "ready_for_final_proof") {
				t.Fatalf("exit admitted=%v for %s: %v", err == nil, disposition, err)
			}
			if err != nil {
				failure := workflowFailureOf(err)
				if failure.RecoveryAction == "" || failure.RecoveryAction == "contact_operator" {
					t.Fatalf("refusal has no engineering recovery: %+v", failure)
				}
			}
			if string(mustJSONValue(state)) != before {
				t.Fatal("admission mutated current state")
			}
		})
	}
}

func TestReviewCurrentAdmissionNegativeCases(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*WorkflowReviewState)
		admitted bool
	}{
		{"unrecorded ready", func(s *WorkflowReviewState) { s.Accepted = nil }, false},
		{"new candidate", func(s *WorkflowReviewState) {
			candidate := *s.Candidate
			candidate.Commit = strings.Repeat("c", 40)
			s.Candidate = &candidate
		}, false},
		{"changed depended-on revision", func(s *WorkflowReviewState) { s.DependenciesCurrent = false }, false},
		{"open in-scope blocker", func(s *WorkflowReviewState) { s.Findings = reviewBlockFixture().Findings }, false},
		{"out-of-scope follow-up", func(s *WorkflowReviewState) {
			s.Findings = []ReviewFinding{{FindingID: "finding:follow-up", InScope: false, Blocking: true, Status: "open"}}
		}, true},
		{"remaining obligation", func(s *WorkflowReviewState) {
			s.Accepted.Report.RemainingObligations = []ReviewObligation{{ObligationID: "obligation:proof", OwnerRef: "owner:one", SubjectRef: "predicate:primary", InputDigest: nativeDigest([]byte("input")), NeededProof: "Capture the declared control."}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition = "ready_for_final_proof"
			report.RemainingObligations = []ReviewObligation{}
			state := WorkflowReviewState{Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{Report: *report}, DependenciesCurrent: true, Findings: []ReviewFinding{}}
			tc.change(&state)
			before := string(mustJSONValue(state))
			if err := workflowAdmitReview(state); (err == nil) != tc.admitted {
				t.Fatalf("admitted=%v, want %v: %v", err == nil, tc.admitted, err)
			}
			if string(mustJSONValue(state)) != before {
				t.Fatal("refusal or admission changed state")
			}
		})
	}
}

func TestReviewCurrentCandidateChangesOnlyOnRecordedReview(t *testing.T) {
	j, input := retainedReviewFixture(t, "review-current-candidate")
	finding := reviewBlockFixture().Findings[0]
	completeCurrentReviewForTest(t, j.fixture, "correction_required", func(report *ReviewBlock) { report.Findings = []ReviewFinding{finding} })
	read := func() WorkflowReviewState {
		t.Helper()
		state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	baseline := read()
	descendantCommit(t, j, "repair")
	repair := currentReviewBlockForTest(t, j.s, j.workID, "ready_for_final_proof")
	repair.InputCandidate = input
	finding.Status = "closed"
	finding.ClosureRefs = []string{"worktree_verify:repair"}
	repair.Findings = []ReviewFinding{finding}
	if before := read(); !reflect.DeepEqual(before.Accepted, baseline.Accepted) || !reflect.DeepEqual(before.Candidate, baseline.Candidate) || before.blocker() == "" {
		t.Fatalf("draft repair changed authority or closed its finding: %+v", before)
	}
	beforeEffects := j.effects(t)
	beforeValidation := read()
	if err := validateRecordedReview(t.Context(), j.s.db, j.workID, repair); err != nil {
		t.Fatal(err)
	}
	if after := read(); !reflect.DeepEqual(after, beforeValidation) || j.effects(t) != beforeEffects {
		t.Fatal("read-only Review validation promoted its draft")
	}
	if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, repair); err != nil {
		t.Fatal(err)
	}
	state := read()
	if state.Candidate == nil || *state.Candidate != repair.OutputCandidate || state.Accepted == nil || state.Accepted.ReportDigest != reviewDigest(repair) || state.Accepted.ActorRef != mustActorRef(t, j.fixture.owner) || state.blocker() != "" {
		t.Fatalf("recorded repair not installed atomically: %+v", state)
	}
	if err := workflowAdmitReview(state); err != nil {
		t.Fatal(err)
	}
	late := reviewBlockFixture()
	late.InputCandidate, late.OutputCandidate, late.Dependencies = input, repair.OutputCandidate, repair.Dependencies
	beforeEffects = j.effects(t)
	if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, late); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("late old-subject Review=%v, want stale refusal", err)
	}
	if after := read(); !reflect.DeepEqual(after, state) || j.effects(t) != beforeEffects {
		t.Fatalf("refused late Review changed current authority: %+v", after)
	}
	finding.Status, finding.ClosureRefs = "open", []string{}
	completeCurrentReviewForTest(t, j.fixture, "correction_required", func(report *ReviewBlock) { report.Findings = []ReviewFinding{finding} })
	completeCurrentReviewForTest(t, j.fixture, "continue")
	state = read()
	if state.blocker() != finding.FindingID {
		t.Fatal("omitting a current blocking finding closed it")
	}
	if err := workflowAdmitReview(state); !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "open in-scope blocking finding") {
		t.Fatalf("omitted blocker no longer holds exit: %v", err)
	}
	tx, err := j.s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before, err := readWorkflowReviewFacts(t.Context(), tx, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowRecordedReviewTx(t.Context(), tx, Event{SubjectID: j.workID}, workflowActionCompletedPayload{ActionID: "record_note", Review: repair}); err != nil {
		t.Fatal(err)
	}
	after, err := readWorkflowReviewFacts(t.Context(), tx, j.workID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("non-Review action acquired Review authority: %+v, %v", after, err)
	}
}

func TestReviewCurrentCanonicalReportSchema(t *testing.T) {
	report := reviewBlockFixture()
	if err := payloadschema.Validate("workflow_review_report", mustJSONValue(report)); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(mustJSONValue(report), &value); err != nil {
		t.Fatal(err)
	}
	value["verdict"] = "ship"
	if err := payloadschema.Validate("workflow_review_report", mustJSONValue(value)); err == nil {
		t.Fatal("canonical report admitted the retired verdict field")
	}
	delete(value, "verdict")
	for _, field := range []string{"input_candidate", "output_candidate", "dependencies", "assessments", "remaining_obligations"} {
		saved := value[field]
		delete(value, field)
		if err := payloadschema.Validate("workflow_review_report", mustJSONValue(value)); err == nil {
			t.Fatalf("missing %s was admitted", field)
		}
		value[field] = saved
	}
}

func TestReviewCurrentFindingRetainsItsObligationBinding(t *testing.T) {
	for _, change := range []string{"unchanged", "omit oracle", "different owner", "different criterion", "different control", "additional control"} {
		t.Run(change, func(t *testing.T) {
			original := reviewBlockFixture()
			original.Findings[0].Oracle = &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:original", PredicateIDs: []string{"predicate:primary"}, ControlIDs: []string{"control:original"}}
			var changed ReviewBlock
			if err := json.Unmarshal(mustJSONValue(original), &changed); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "omit oracle":
				changed.Findings[0].Oracle = nil
			case "different owner":
				changed.Findings[0].Oracle.OwnerID = "owner:unrelated"
			case "different criterion":
				changed.Findings[0].Oracle.PredicateIDs = []string{"predicate:unrelated"}
			case "different control":
				changed.Findings[0].Oracle.ControlIDs = []string{"control:unrelated"}
			case "additional control":
				changed.Findings[0].Oracle.ControlIDs = append(changed.Findings[0].Oracle.ControlIDs, "control:additional")
			}
			state := WorkflowReviewState{Findings: original.Findings}
			before := string(mustJSONValue(state))
			err := validateReviewFindingChanges(&changed, state)
			allowed := change == "unchanged" || change == "omit oracle"
			if (err == nil) != allowed || !allowed && !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("changed historical metadata admission=%v", err)
			}
			if string(mustJSONValue(state)) != before {
				t.Fatal("validation mutated historical metadata")
			}
			applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{Report: changed}, workflowReviewFindingMap(state))
			if len(state.Findings) != len(original.Findings) || !reflect.DeepEqual(state.Findings[0].Oracle, original.Findings[0].Oracle) {
				t.Fatalf("fold replaced original obligation: %+v", state.Findings)
			}
		})
	}
}

func TestReviewCurrentProofAssociationHasNoCallerVerdict(t *testing.T) {
	for _, extra := range []string{`"verdict_kind":"ok",`, `"evaluation_evidence":["forged"],`, `"incomparable_with_approved":false,`} {
		raw := []byte(`{"predicate_id":"predicate:primary",` + extra + `"receipt_refs":["worktree_verify:captured"]}`)
		if err := payloadschema.Validate("workflow_verdict_batch_entry", raw); err == nil {
			t.Fatalf("caller proof authority was admitted: %s", raw)
		}
	}
	if err := payloadschema.Validate("workflow_verdict_batch_entry", []byte(`{"predicate_id":"predicate:primary","receipt_refs":["worktree_verify:captured"]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestCrucialReviewExitRefusesHighOrMediumFinding(t *testing.T) {
	for _, tc := range []struct {
		name, severity, status     string
		crucial, inScope, admitted bool
	}{
		{"crucial open in-scope P0", "P0", "open", true, true, false},
		{"crucial open in-scope P1", "P1", "open", true, true, false},
		{"crucial open in-scope P2", "P2", "open", true, true, false},
		{"crucial open in-scope P3", "P3", "open", true, true, true},
		{"crucial out-of-scope P1", "P1", "open", true, false, true},
		{"crucial closed P1", "P1", "closed", true, true, true},
		{"larger open in-scope P1", "P1", "open", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := reviewBlockFixture()
			report.Disposition, report.RemainingObligations = "ready_for_final_proof", []ReviewObligation{}
			state := WorkflowReviewState{Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{Report: *report}, DependenciesCurrent: true,
				Findings: []ReviewFinding{{FindingID: "finding:" + tc.severity, Severity: tc.severity, InScope: tc.inScope, Status: tc.status}}, Crucial: tc.crucial}
			err := workflowAdmitReview(state)
			if (err == nil) != tc.admitted {
				t.Fatalf("admitted=%v, want %v: %v", err == nil, tc.admitted, err)
			}
			if err != nil && (!strings.Contains(err.Error(), "Crucial") || !strings.Contains(err.Error(), tc.severity)) {
				t.Fatalf("refusal does not identify the Crucial severity rule: %v", err)
			}
		})
	}
}

func TestReviewStateReadsCrucialFromEffectiveSize(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const workID = "review-effective-size"
	seedWork(t, s, workID)
	read := func() bool {
		t.Helper()
		state, err := loadWorkflowReviewState(t.Context(), s.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		return state.Crucial
	}
	if read() {
		t.Fatal("an unsized item is crucial")
	}
	inputs := workSizeInputsFixture()
	inputs.Declared.Security = true
	if err := applyWorkEvent(t, s, workSizedEvent("review-effective-size:sized", workID, WorkSizeModelVersion, WorkSizeCrucial, inputs), nil); err != nil {
		t.Fatal(err)
	}
	if !read() {
		t.Fatal("a crucial model size is not crucial")
	}
}
