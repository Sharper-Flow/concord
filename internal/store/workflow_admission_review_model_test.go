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
	accepted  string
	candidate bool
	changed   bool
	current   bool
	blocked   bool
}

var admissionReviewDispositions = []string{"", "continue", "correction_required", "revision_required", "ready_for_final_proof"}

func admissionReviewState(state admissionModelState) *WorkflowReviewState {
	r := state.review
	view := &WorkflowReviewState{Findings: []ReviewFinding{}, DependenciesCurrent: r.current, PublicationOutstanding: true}
	candidate := ReviewCandidate{ProjectID: "project:model", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	if r.candidate {
		view.Candidate = &candidate
	}
	dependencies := ReviewDependencies{Criteria: []ReviewCriterion{{PredicateID: "predicate:model", ContentDigest: "sha256:" + strings.Repeat("c", 64)}}, ArchitectureNotApplicable: true}
	// Duplicate active contracts leave no single criteria set, so admission
	// folds no standing Review dependencies.
	if state.contracts == 1 {
		view.Dependencies = &dependencies
	}
	if r.accepted != "" {
		view.Accepted = &WorkflowAcceptedReview{ReportRef: "report:model:review", ReportDigest: "sha256:" + strings.Repeat("d", 64), ActorRef: "actor:model:main",
			Report: ReviewBlock{Disposition: r.accepted, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: dependencies,
				Assessments: []ReviewAssessment{}, Findings: []ReviewFinding{}, RemainingObligations: []ReviewObligation{}}}
	}
	if r.changed && view.Candidate != nil {
		view.Candidate.Commit = strings.Repeat("e", 40)
	}
	if r.blocked {
		view.Findings = []ReviewFinding{{FindingID: "finding:model", InScope: true, Blocking: true, Status: "open"}}
	}
	return view
}

func admissionReviewVariants(definition WorkflowDefinition, state admissionModelState) []admissionModelState {
	if workflowDefinitionProducesDelivery(definition) && workflowStepIsDeliveryGate(workflowStep(definition, state.step)) {
		// Entry requires an applicable ready Review. The reachable walk
		// separately checks that the producer's delivery edge enforces it.
		state.review = admissionModelReview{accepted: "ready_for_final_proof", candidate: true, current: true}
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
					next := state
					next.review = admissionModelReview{accepted: disposition, current: current, changed: changed, candidate: true, blocked: blocked}
					variants = append(variants, next)
				}
			}
		}
	}
	return variants
}

func admissionPackReview(p *admissionModelKeyPacker, r admissionModelReview) {
	p.put(admissionKeyCode(r.accepted, admissionReviewDispositions), 3)
	for _, flag := range []bool{r.candidate, r.changed, r.current, r.blocked} {
		var bit uint8
		if flag {
			bit = 1
		}
		p.put(bit, 1)
	}
}

func admissionUnpackReview(p *admissionModelKeyPacker) admissionModelReview {
	r := admissionModelReview{accepted: admissionReviewDispositions[p.get(3)]}
	for _, flag := range []*bool{&r.candidate, &r.changed, &r.current, &r.blocked} {
		*flag = p.get(1) == 1
	}
	return r
}

func TestAdmissionModelReviewKeyIsInjective(t *testing.T) {
	if got := reflect.TypeOf(admissionModelReview{}).NumField(); got != 5 {
		t.Fatalf("Review has %d fields; encode and exercise every field in the key", got)
	}
	steps := []string{""}
	seen := map[admissionModelKey]admissionModelReview{}
	for _, accepted := range admissionReviewDispositions {
		for flags := range 16 {
			r := admissionModelReview{accepted: accepted, candidate: flags&1 != 0,
				changed: flags&2 != 0, current: flags&4 != 0, blocked: flags&8 != 0}
			state := admissionModelState{review: r}
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
							state := admissionModelState{step: step, contracts: 1, started: true,
								review: admissionModelReview{accepted: disposition, candidate: candidate, current: current, changed: changed, blocked: blocked}}
							folded := admissionWorkflowState(definition, state)
							want := disposition == "ready_for_final_proof" && candidate && current && !changed && !blocked
							if got := workflowAdmit(definition, folded, "record_delivery"); got.Admitted != want {
								t.Fatalf("%s: record_delivery admitted=%v, want %v: %v", state, got.Admitted, want, got.Failure)
							}
							// An unrecorded report grants no delivery authority.
							state.review.accepted = ""
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

func TestAdmissionModelRejectsSeededUnreviewedDeliveryEntry(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	seeded := admissionModelState{step: "delivery", contracts: 1, started: true, artifact: true, observed: true}
	if admissionHasExit(definition, seeded) {
		t.Fatal("an invented delivery entry without its required preceding Review has an exit")
	}
	graph := admissionReachableGraph(definition, seeded)
	if admissionLiveGraphStates(graph)[0] {
		t.Fatal("the reachable detector concealed an illegal unreviewed delivery entry")
	}
}

func TestAdmissionModelRecordedReviewKeepsExecutionStepAndBindsCandidate(t *testing.T) {
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		definition := mustBuiltinDefinition(t, ref).Definition
		step := "execution"
		if ref == "workflow.break_fix" {
			step = "repair"
		}
		state := admissionModelState{step: step, contracts: 1,
			review: admissionModelReview{accepted: "continue", candidate: true, changed: true, blocked: true}}
		if decision := workflowAdmit(definition, admissionWorkflowState(definition, state), "record_review"); !decision.Admitted {
			t.Fatalf("%s: stale candidate admits no fresh Review: %v", ref, decision.Failure)
		}
		seen := map[string]bool{}
		for _, next := range admissionSuccessors(definition, state, "record_review") {
			if next.step != state.step || !next.review.candidate || !next.review.current || next.review.changed {
				t.Fatalf("%s: recorded Review advanced or failed to bind its candidate: %s", ref, next)
			}
			if next.review.accepted == "" || seen[next.review.accepted] {
				t.Fatalf("%s: absent or duplicate Review disposition: %s", ref, next)
			}
			seen[next.review.accepted] = true
			if next.review.accepted == "ready_for_final_proof" && next.review.blocked {
				t.Fatalf("%s: ready Review kept an unresolved blocking finding: %s", ref, next)
			}
		}
		for _, disposition := range admissionReviewDispositions[1:] {
			if !seen[disposition] {
				t.Fatalf("%s: record_review lost disposition %s", ref, disposition)
			}
		}
	}
}

func TestAdmissionModelGateReturnFollowsInapplicableReview(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	for name, review := range map[string]admissionModelReview{
		"applicable":    {accepted: "ready_for_final_proof", candidate: true, current: true},
		"staleDeps":     {accepted: "ready_for_final_proof", candidate: true, current: false},
		"changedOutput": {accepted: "ready_for_final_proof", candidate: true, current: true, changed: true},
		"noAcceptance":  {candidate: true, current: true},
	} {
		t.Run(name, func(t *testing.T) {
			state := admissionModelState{step: "delivery", contracts: 1, started: true, review: review}
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
			// The return must permit a fresh recorded Review at the producer.
			returned := false
			for _, successor := range admissionSuccessors(definition, state, "request_correction") {
				if successor.step != "repair" {
					continue
				}
				returned = true
				if !slices.Contains(admissionModelMoves(definition, successor), "record_review") {
					t.Fatalf("%s: the gate return's successor admits no Review: %s", name, successor)
				}
				fresh := false
				for _, completed := range admissionSuccessors(definition, successor, "record_review") {
					if completed.review.accepted != "" && completed.review.current && !completed.review.changed {
						fresh = true
					}
				}
				if !fresh {
					t.Fatalf("%s: the gate return's successor records no current Review: %s", name, successor)
				}
			}
			if !returned {
				t.Fatalf("%s: the gate return has no producer-step successor", name)
			}
		})
	}
}

func TestAdmissionModelReviewCurrencyFollowsDependencyContent(t *testing.T) {
	before := admissionModelState{step: "repair", contracts: 1,
		review: admissionModelReview{accepted: "ready_for_final_proof", candidate: true, current: true}}
	changed, unchanged := false, false
	for _, next := range admissionContractUpdateSuccessors(before) {
		if next.contracts != 1 {
			continue
		}
		if next.review.current {
			unchanged = true
		} else {
			changed = true
		}
		if next.review.accepted != before.review.accepted || !next.review.candidate {
			t.Fatalf("contract revision erased the attributed report or candidate: %s", next)
		}
	}
	if !changed || !unchanged {
		t.Fatalf("Review dependency-content classes lost: changed=%v unchanged=%v", changed, unchanged)
	}
}
