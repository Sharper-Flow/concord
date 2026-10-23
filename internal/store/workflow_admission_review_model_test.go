package store

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Identities are symbolic here. The real-store conformance witness separately
// compares exact Project, commit, tree, dependency, actor and report identities.
type admissionModelReview struct {
	report, accepted string
	group            string
	candidate        bool
	changed          bool
	current          bool
	progressed       bool
	blocked          bool
}

var admissionReviewDispositions = []string{"", "continue", "correction_required", "revision_required", "ready_for_final_proof"}
var admissionReviewGroups = []string{"", "first_entry", "continue", "corrective"}

func admissionReviewCompletions(definition WorkflowDefinition, state, completed admissionModelState) []admissionModelState {
	if !state.review.candidate || !workflowDefinitionProducesDelivery(definition) || !LaneStepDispatchAllowed("review", workflowStep(definition, state.step).Kind) {
		return nil
	}
	var result []admissionModelState
	for _, disposition := range admissionReviewDispositions[1:] {
		review := completed
		review.review.report = disposition
		result = append(result, review)
	}
	return result
}

func admissionProducingDispatch(definition WorkflowDefinition, state, completed admissionModelState) []admissionModelState {
	folded := admissionWorkflowState(definition, state)
	folded.ReviewLane, folded.ReviewGroup = "implementation", "group:model"
	if !workflowAdmit(definition, folded, "dispatch_worker").Admitted {
		return nil
	}
	completed.review.progressed = true
	if completed.review.group == "" {
		completed.review.group = "first_entry"
	}
	if state.review.accepted == "continue" && state.review.current && !state.review.changed && state.review.group != "corrective" {
		completed.review.group = "continue"
	}
	return []admissionModelState{completed}
}

func admissionAcceptedJobVariants(state admissionModelState) []admissionModelState {
	if state.jobs != "required" {
		return []admissionModelState{state}
	}
	satisfied := state
	satisfied.jobs, satisfied.jobsUnsatisfied, satisfied.jobDebt = "satisfied", "", false
	return []admissionModelState{state, satisfied}
}

func admissionReviewState(state admissionModelState) *WorkflowReviewState {
	r := state.review
	view := &WorkflowReviewState{Groups: map[string]workflowAssignmentGroup{}, Findings: []WorkerReviewFinding{},
		ExecutionProgressed: r.progressed, DependenciesCurrent: r.current, PublicationOutstanding: true}
	candidate := WorkerReviewCandidate{ProjectID: "project:model", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	if r.candidate {
		view.Candidate = &candidate
	}
	dependencies := WorkerReviewDependencies{Criteria: []WorkerReviewCriterion{{PredicateID: "predicate:model", ContentDigest: "sha256:" + strings.Repeat("c", 64)}}, ArchitectureNotApplicable: true}
	if state.contracts > 0 {
		view.Dependencies = &dependencies
	}
	if r.accepted != "" {
		view.Accepted = &WorkflowAcceptedReview{AttemptID: "attempt:model:review", ReportRef: "report:model:review", ReportDigest: "sha256:" + strings.Repeat("d", 64), ActorRef: "actor:model:main",
			Report: WorkerReviewBlock{Disposition: r.accepted, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: dependencies,
				Assessments: []WorkerReviewAssessment{}, Findings: []WorkerReviewFinding{}, RemainingObligations: []WorkerReviewObligation{}}}
	}
	if r.changed && view.Candidate != nil {
		view.Candidate.Commit = strings.Repeat("e", 40)
	}
	if r.group != "" {
		group := workflowAssignmentGroup{Permission: r.group}
		if r.group != "first_entry" {
			group.ReviewRef = "report:model:review"
		}
		if r.group == "corrective" {
			group.CorrectionRef = "correction:model"
		}
		view.Groups["group:model"] = group
		if r.group == "continue" && view.Accepted != nil {
			view.ContinueConsumed, view.GroupID = true, "group:model"
		}
	}
	if r.blocked {
		view.Findings = []WorkerReviewFinding{{FindingID: "finding:model", InScope: true, Blocking: true, Status: "open"}}
	}
	return view
}

func admissionReviewVariants(definition WorkflowDefinition, state admissionModelState) []admissionModelState {
	if workflowDefinitionProducesDelivery(definition) && workflowStepIsDeliveryGate(workflowStep(definition, state.step)) {
		// This step has no Review dispatch. Its only incoming execution
		// exit required Main's applicable ready Review; local intake never
		// enters it. The reachable walk separately checks that invariant.
		state.review = admissionModelReview{accepted: "ready_for_final_proof", candidate: true, current: true, progressed: true, group: "first_entry"}
		return []admissionModelState{state}
	}
	variants := []admissionModelState{state}
	if state.contracts == 0 {
		return variants
	}
	for _, disposition := range admissionReviewDispositions {
		for _, current := range []bool{false, true} {
			for _, changed := range []bool{false, true} {
				for _, blocked := range []bool{false, true} {
					for _, group := range admissionReviewGroups {
						if disposition == "" && group != "" && group != "first_entry" {
							continue // A group cannot consume absent Review authority.
						}
						next := state
						next.review = admissionModelReview{accepted: disposition, current: current, changed: changed, candidate: true, progressed: true, blocked: blocked, group: group}
						variants = append(variants, next)
					}
				}
			}
		}
	}
	return variants
}

func admissionPackReview(p *admissionModelKeyPacker, r admissionModelReview) {
	p.put(admissionKeyCode(r.report, admissionReviewDispositions), 3)
	p.put(admissionKeyCode(r.accepted, admissionReviewDispositions), 3)
	p.put(admissionKeyCode(r.group, admissionReviewGroups), 2)
	for _, flag := range []bool{r.candidate, r.changed, r.current, r.progressed, r.blocked} {
		var bit uint8
		if flag {
			bit = 1
		}
		p.put(bit, 1)
	}
}

func admissionUnpackReview(p *admissionModelKeyPacker) admissionModelReview {
	r := admissionModelReview{report: admissionReviewDispositions[p.get(3)], accepted: admissionReviewDispositions[p.get(3)], group: admissionReviewGroups[p.get(2)]}
	for _, flag := range []*bool{&r.candidate, &r.changed, &r.current, &r.progressed, &r.blocked} {
		*flag = p.get(1) == 1
	}
	return r
}

func TestAdmissionModelReviewKeyIsInjective(t *testing.T) {
	if got := reflect.TypeOf(admissionModelReview{}).NumField(); got != 8 {
		t.Fatalf("Review has %d fields; encode and exercise every field in the key", got)
	}
	steps := []string{""}
	seen := map[admissionModelKey]admissionModelReview{}
	for _, report := range admissionReviewDispositions {
		for _, accepted := range admissionReviewDispositions {
			for _, group := range admissionReviewGroups {
				for flags := range 32 {
					r := admissionModelReview{report: report, accepted: accepted, group: group, candidate: flags&1 != 0,
						changed: flags&2 != 0, current: flags&4 != 0, progressed: flags&8 != 0, blocked: flags&16 != 0}
					state := admissionModelState{debt: ReviewDebtNone, review: r}
					key := admissionModelKeyOf(steps, state)
					if prior, exists := seen[key]; exists {
						t.Fatalf("Review key collision: %+v and %+v", prior, r)
					}
					seen[key] = r
					if got := admissionModelStateOf(steps, key); got != state {
						t.Fatalf("Review key changed %+v into %+v", state, got)
					}
				}
			}
		}
	}
}

func TestAdmissionModelCurrentReviewControlsExecutionExit(t *testing.T) {
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		definition := mustBuiltinDefinition(t, ref).Definition
		step := "execution"
		if ref == "workflow.break_fix" {
			step = "repair"
		}
		for _, disposition := range admissionReviewDispositions {
			for _, candidate := range []bool{false, true} {
				for _, current := range []bool{false, true} {
					for _, changed := range []bool{false, true} {
						for _, blocked := range []bool{false, true} {
							state := admissionModelState{step: step, contracts: 1, started: true, debt: ReviewDebtNone,
								review: admissionModelReview{accepted: disposition, candidate: candidate, current: current, changed: changed, blocked: blocked}}
							folded := admissionWorkflowState(definition, state)
							want := disposition == "ready_for_final_proof" && candidate && current && !changed && !blocked
							if got := workflowAdmit(definition, folded, "record_delivery"); got.Admitted != want {
								t.Fatalf("%s: record_delivery admitted=%v, want %v: %v", state, got.Admitted, want, got.Failure)
							}
							// A terminal report is not Main's accepted authority.
							state.review.accepted, state.review.report = "", disposition
							if got := workflowAdmit(definition, admissionWorkflowState(definition, state), "record_delivery"); got.Admitted {
								t.Fatalf("unaccepted report opened execution exit: %s", state)
							}
						}
					}
				}
			}
		}
	}
}

func TestAdmissionModelLocalIntakeNeverEntersDelivery(t *testing.T) {
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		definition := mustBuiltinDefinition(t, ref).Definition
		for _, step := range definition.StepGraph.Steps {
			if !stepDeclaresAction(definition, step.ID, "accept_worker_result") {
				continue
			}
			for _, report := range admissionReviewDispositions {
				state := admissionModelState{step: step.ID, contracts: 1, attempt: "completed", debt: ReviewDebtNone,
					review: admissionModelReview{report: report}}
				for _, next := range admissionSuccessors(definition, state, "accept_worker_result") {
					if next.step != state.step {
						t.Fatalf("local intake advanced %s to %s", state, next)
					}
				}
			}
		}
	}
}

func TestAdmissionModelRejectsSeededUnreviewedDeliveryEntry(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	seeded := admissionModelState{step: "delivery", contracts: 1, started: true, artifact: true, observed: true, debt: ReviewDebtNone}
	if admissionHasExit(definition, seeded) {
		t.Fatal("an invented delivery entry without its required preceding Review has an exit")
	}
	graph := admissionReachableGraph(definition, seeded)
	if admissionLiveGraphStates(graph)[0] {
		t.Fatal("the reachable detector concealed an illegal unreviewed delivery entry")
	}
}

func TestAdmissionModelGateReturnFollowsInapplicableReview(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	for name, review := range map[string]admissionModelReview{
		"applicable":    {accepted: "ready_for_final_proof", candidate: true, current: true, progressed: true, group: "first_entry"},
		"staleDeps":     {accepted: "ready_for_final_proof", candidate: true, current: false, progressed: true, group: "first_entry"},
		"changedOutput": {accepted: "ready_for_final_proof", candidate: true, current: true, changed: true, progressed: true, group: "first_entry"},
		"noAcceptance":  {candidate: true, current: true, progressed: true, group: "first_entry"},
	} {
		t.Run(name, func(t *testing.T) {
			state := admissionModelState{step: "delivery", contracts: 1, started: true, debt: ReviewDebtNone, review: review}
			folded := admissionWorkflowState(definition, state)
			want := name != "applicable" && name != "noAcceptance"
			if folded.CorrectionRequestRecovery != want {
				t.Fatalf("%s: gate correction recovery=%v, want %v", name, folded.CorrectionRequestRecovery, want)
			}
			moves := admissionModelMoves(definition, state)
			if slices.Contains(moves, "request_correction") != want {
				t.Fatalf("%s: request_correction admitted=%v, want %v: %v", name, slices.Contains(moves, "request_correction"), want, moves)
			}
			if !want {
				return
			}
			// The return's successor must hold the fresh-review route: the
			// producer step's admitted dispatch completes a Review report the
			// delivery assertion then requires.
			returned := false
			for _, successor := range admissionSuccessors(definition, state, "request_correction") {
				if successor.step != "repair" {
					continue
				}
				returned = true
				if !slices.Contains(admissionModelMoves(definition, successor), "dispatch_worker") {
					t.Fatalf("%s: the gate return's successor admits no dispatch: %s", name, successor)
				}
				fresh := false
				for _, completed := range admissionSuccessors(definition, successor, "dispatch_worker") {
					if completed.review.report != "" {
						fresh = true
					}
				}
				if !fresh {
					t.Fatalf("%s: the gate return's successor dispatch completes no Review report: %s", name, successor)
				}
			}
			if !returned {
				t.Fatalf("%s: the gate return has no producer-step successor", name)
			}
		})
	}
}

func TestAdmissionModelImplementationDispatchUsesGroupAuthority(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	for _, disposition := range admissionReviewDispositions {
		for _, group := range admissionReviewGroups {
			state := admissionModelState{step: "repair", contracts: 1, debt: ReviewDebtNone,
				review: admissionModelReview{accepted: disposition, group: group, candidate: true, current: true, progressed: true}}
			completed := state
			completed.attempt = "completed"
			got := admissionProducingDispatch(definition, state, completed)
			want := disposition == "continue" || group == "first_entry" && disposition == "" || group == "corrective" || group == "continue"
			if (len(got) != 0) != want {
				t.Fatalf("%s: implementation admitted=%v, want %v", state, len(got) != 0, want)
			}
			if disposition == "continue" && group != "corrective" && want && got[0].review.group != "continue" {
				t.Fatalf("new group did not consume continue: %s", got[0])
			}
		}
	}
}

func TestAdmissionModelReviewCurrencyFollowsDependencyContent(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	before := admissionModelState{step: "repair", contracts: 1, debt: ReviewDebtNone,
		review: admissionModelReview{accepted: "ready_for_final_proof", candidate: true, current: true}}
	changed, unchanged := false, false
	for _, next := range admissionSupersedeSuccessors(definition, before) {
		if next.contracts != 1 {
			continue
		}
		if next.review.current {
			unchanged = true
		} else {
			changed = true
		}
		if next.review.accepted != before.review.accepted || !next.review.candidate {
			t.Fatalf("supersession erased the attributed report or candidate: %s", next)
		}
	}
	if !changed || !unchanged {
		t.Fatalf("Review dependency-content classes lost: changed=%v unchanged=%v", changed, unchanged)
	}
}
