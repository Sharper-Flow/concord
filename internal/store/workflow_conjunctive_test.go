package store

import (
	"context"
	"strings"
	"testing"
)

type conjunctiveVerdict struct {
	predicateID  string
	kind         string
	evidence     string
	incomparable bool
}

var conjunctiveVerdicts = []conjunctiveVerdict{{"predicate:present", "ok", "", false}, {"predicate:absent", "ok", "", false}}

// Every conjunct uses the same observed candidate and captured command receipt,
// covered by its own assessment in the exact accepted Review report.
func seedConjunctiveCompletion(t *testing.T, verdicts []conjunctiveVerdict) (*Store, Event, error) {
	t.Helper()
	const workID = "conjunctive-work"
	f := qBuildAcceptancePredicates(t, workID, "", false, []string{"review"},
		map[string]any{"predicate_id": "predicate:present", "ordinal": 0, "outcome_kind": "exists", "outcome_payload": map[string]any{"kind": "exists", "surface": "surface:one", "subjects": []string{"subject:one"}}},
		map[string]any{"predicate_id": "predicate:absent", "ordinal": 1, "outcome_kind": "absent", "outcome_payload": map[string]any{"kind": "absent", "surface": "surface:two", "subjects": []string{"subject:two"}, "distinguish_from": []string{"archived"}}},
	)
	for _, verdict := range verdicts {
		entry := map[string]any{"predicate_id": verdict.predicateID, "receipt_refs": []string{f.receipt}}
		// Caller verdicts and comparability claims cannot substitute for proof.
		if verdict.kind != "ok" {
			entry["verdict_kind"] = verdict.kind
		}
		if verdict.incomparable {
			entry["incomparable_with_approved"] = true
		}
		if err := runVerdictActionAs(t, f.store, workID, "record_verdict", mustJSONValue(map[string]any{"verdicts": []map[string]any{entry}}), 0, f.owner); err != nil {
			return f.store, Event{}, err
		}
	}
	if len(verdicts) == 2 {
		if err := f.run(t.Context(), "confirm_premise", "confirm", f.version(t.Context(), t), nil, nil); err != nil {
			return f.store, Event{}, err
		}
	}
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		return f.store, Event{}, err
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), f.store, workID, "release", actor); err != nil {
		return f.store, Event{}, err
	}
	bindCompletionClosure(t, f.store, workID, f.owner)
	version := f.version(t.Context(), t)
	completion := workflowEventWithActor("conjunctive-completion", WorkflowCompleted, workID, actor, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": actor,
		"premise_confirmed": len(verdicts) == 2, "evidence_count": 2, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking",
	})
	return f.store, completion, nil
}

func TestPinnedConjunctiveContractCompletes(t *testing.T) {
	t.Parallel()
	s, completion, err := seedConjunctiveCompletion(t, conjunctiveVerdicts)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatalf("conjunctive completion: %v", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_contract_predicates WHERE work_id='conjunctive-work' AND contract_version=1`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("approved conjunct count = %d, want 2: %v", count, err)
	}
}

// Missing proof refuses completion. A caller's outcome or comparability claim
// is refused before it can become recorded criterion proof (CD-0242 D8/D9).
func TestConjunctiveCompletionRefusesOnTheRecordedVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		verdicts []conjunctiveVerdict
		want     FailureKind
		clause   int
	}{
		{"missing absent verdict", conjunctiveVerdicts[:1], KindMissingEvidence, 4},
		{"caller relocated absence claim", []conjunctiveVerdict{conjunctiveVerdicts[0], {"predicate:absent", "outcome_mismatch", "", false}}, KindInvalidPayload, 0},
		{"caller comparability claim", []conjunctiveVerdict{conjunctiveVerdicts[0], {"predicate:absent", "ok", "", true}}, KindInvalidPayload, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, completion, err := seedConjunctiveCompletion(t, tc.verdicts)
			if err == nil {
				err = CompleteWorkflow(t.Context(), s, completion)
			}
			if !hasFailureKind(err, tc.want) {
				t.Fatalf("criterion proof/completion = %v, want %s", err, tc.want)
			}
			var failure *Failure
			if tc.clause != 0 && (!failureAs(err, &failure) || failure.Clause != tc.clause) {
				t.Fatalf("completion refusal = %v, want clause %d", err, tc.clause)
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id='conjunctive-work' AND kind=?`, WorkflowCompleted).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refused completion count = %d, want 0: %v", count, err)
			}
		})
	}
}

func TestConjunctiveVerdictForAnUnapprovedPredicateIsRefusedAtTheFold(t *testing.T) {
	t.Parallel()
	f := qBuildAcceptance(t, "unapproved-conjunct", "", false)
	state, err := loadWorkflowReviewState(t.Context(), f.store.db, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := f.version(t.Context(), t)
	event := workflowEventWithActor("unapproved-association", WorkflowVerdictRecorded, f.workID, actor, map[string]any{
		"work_id": f.workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1,
		"predicate_id": "predicate:extra", "verdict_kind": "ok", "verdict_actor_ref": actor,
		"evaluation_evidence": []string{f.receipt}, "candidate": state.Candidate,
	})
	err = applyWorkflowTestOperation(t.Context(), f.store, Operation{Events: []Event{event}, ExpectedVersions: workVersion(f.workID, version)})
	if !hasFailureKind(err, KindOutcomeMismatch) {
		t.Fatalf("unapproved criterion fold = %v, want outcome_mismatch", err)
	}
	if got := f.version(t.Context(), t); got != version {
		t.Fatalf("refused association changed version to %d, want %d", got, version)
	}
}
