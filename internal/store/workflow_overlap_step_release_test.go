package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// The coordinator identifies the shared step and explicitly replaces its hold
// after that step. Step completion alone must not silently release the pair.
func TestWorkflowDomainOverlapCoordinatorReleasesAfterStepBeforeTerminal(t *testing.T) {
	t.Parallel()
	const leader, follower = "step-release-leader", "step-release-follower"
	ctx := context.Background()
	s, definition, binding, _, _, _ := architectureValidationFixture(t, leader)
	seedWork(t, s, follower)
	overlapFixtureExec(t, s, `UPDATE work_items SET intent_json=json_set(intent_json,'$.governing_requirements',json('[]')) WHERE id IN (?,?)`, leader, follower)
	binding.VerificationObligations = []WorkflowVerificationObligation{}
	actor := livenessActor()
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	action := func(workID, name string, payload json.RawMessage) error {
		return runVerdictActionAs(t, s, workID, name, payload, 0, actor)
	}
	design := json.RawMessage(`{"approach":"Sequence the shared Domain write before independent refinement.","decisions":[{"id":"decision:shared-domain","question":"Where does the shared behavior belong?","choice":"Keep it in the declared child Domain.","rationale":"Both contracts retain the same approved Domain boundary.","rejected":[]}],"touched_refs":["domain:child"]}`)
	for _, workID := range []string{leader, follower} {
		livenessInitialize(t, s, workID, definition)
		for _, move := range []struct {
			name    string
			payload json.RawMessage
		}{
			{"record_proposal", json.RawMessage(`{"problem":"Two changes write the same Domain.","affected":["domain:child"],"stakes":"Avoid concurrent shared writes without holding independent work.","user_outcomes":["Both changes finish their shared step in sequence."]}`)},
			{"record_alignment", json.RawMessage(`{"searched":"The current shared Domain work.","outcome":"none_found"}`)},
			{"approve_contract", mustJSONValue(map[string]any{
				"premise":          "Complete the shared Domain write before independent refinement.",
				"decision_domains": []string{}, "required_evidence": []string{},
				"spec_mandate": []string{}, "law_modifies": []string{},
				"law_change_reason":    "Keep the shared Domain boundary consistent across the two changes.",
				"architecture_binding": binding,
				"outcome_predicates": []map[string]any{{
					"predicate_id": "predicate:shared-write", "ordinal": 0, "outcome_kind": "check",
					"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:shared-write", "immutable_subject_ref": "commit:shared-write", "expected_result": "pass"},
				}},
			})},
			{"record_design", design},
		} {
			if err := action(workID, move.name, move.payload); err != nil {
				t.Fatalf("prepare %s through %s: %v", workID, move.name, err)
			}
		}
	}
	var started int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE work_id IN (?,?) AND execution_started_at IS NOT NULL`, leader, follower).Scan(&started); err != nil || started != 0 {
		t.Fatalf("pre-execution start facts = %d err=%v, want 0", started, err)
	}
	resolve := func(eventID, kind, reason string) {
		t.Helper()
		fromVersion, toVersion := readWorkVersion(t, s, follower), readWorkVersion(t, s, leader)
		if err := s.Transact(ctx, func(tx *Transaction) error {
			_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
				EventID: eventID, FromWorkID: follower, ToWorkID: leader,
				FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
				FromContractVersion: 1, ToContractVersion: 1,
				ResolutionKind: kind, Reason: reason, Actor: actorRef,
			})
			return err
		}); err != nil {
			t.Fatalf("resolve %s: %v", kind, err)
		}
	}
	assertStep := func(workID, step string) {
		t.Helper()
		if got := currentStep(t, s, workID); got != step {
			t.Fatalf("%s step = %q, want %q", workID, got, step)
		}
		var lifecycle string
		if err := s.db.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		if lifecycle != "in_progress" {
			t.Fatalf("%s lifecycle = %q, want in_progress", workID, lifecycle)
		}
	}
	delivery := json.RawMessage(`{"delivery_artifact":"artifact:shared-write","delivery_state":"asserted"}`)
	assertHeld := func() {
		t.Helper()
		version := readWorkVersion(t, s, follower)
		receipts := sequenceWitnessEventCount(t, s, follower)
		err := action(follower, "record_delivery", delivery)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap {
			t.Fatalf("held follower action = %v, want domain_overlap", err)
		}
		if readWorkVersion(t, s, follower) != version || sequenceWitnessEventCount(t, s, follower) != receipts {
			t.Fatal("refused follower action changed its version or effect receipts")
		}
		assertStep(follower, "execution")
	}

	resolve("step-release-initial", ResolutionCompatibleWith, "Independent preparation can proceed before the shared Domain write")
	for _, workID := range []string{leader, follower} {
		if err := action(workID, "start_execution", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("start shared write for %s: %v", workID, err)
		}
		journey := pReturnClaim(t, workflowReturnRouteFixture{store: s, owner: actor}, workID)
		run := journey.observeRun(t, "shared-write-proof-"+workID)
		qAcceptCriterionReview(t, journey.fixture, journey.repo, run.OperationRef)
		if err := action(workID, "record_work_context", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("independent preparation for %s: %v", workID, err)
		}
	}
	resolve("step-release-hold", ResolutionDependsOn, "The follower waits until the leader completes the shared Domain write step")
	assertHeld()
	if err := action(leader, "record_delivery", delivery); err != nil {
		t.Fatalf("leader completes shared write: %v", err)
	}
	assertStep(leader, "refine")
	assertHeld()
	if err := action(leader, "record_work_context", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("leader's next step must remain usable while the follower waits: %v", err)
	}

	resolve("step-release-resume", ResolutionCompatibleWith, "The leader completed the shared write; the remaining steps can proceed independently")
	if err := action(follower, "record_delivery", delivery); err != nil {
		t.Fatalf("released follower completes shared write: %v", err)
	}
	for _, workID := range []string{leader, follower} {
		assertStep(workID, "refine")
		if err := action(workID, "record_work_context", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("independent continuation for %s: %v", workID, err)
		}
	}
	var invalidated, active, relationCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE invalidated_seq IS NOT NULL),count(*) FILTER (WHERE invalidated_seq IS NULL) FROM workflow_overlap_resolutions`).Scan(&invalidated, &active); err != nil {
		t.Fatal(err)
	}
	if invalidated != 2 || active != 1 {
		t.Fatalf("resolution history: invalidated=%d active=%d, want 2 and 1", invalidated, active)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE resolution_id='step-release-hold'`).Scan(&relationCount); err != nil || relationCount != 0 {
		t.Fatalf("stale sequencing relation count=%d err=%v", relationCount, err)
	}
}
