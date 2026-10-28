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
	s, actorRef := seedOverlapProjection(t, leader, follower, false)
	definition, ok := BuiltinWorkflowRegistry().Current("workflow.implementation")
	if !ok {
		t.Fatal("current implementation workflow is unavailable")
	}
	// Seed both approved contracts at their shared architectural design step.
	// Each keeps its recorded execution start, so the leader is a peer that
	// holds its Domains; a contract with no execution start blocks nobody
	// (CD-0144 D3). All later step changes go through workflow-action admission.
	overlapFixtureExec(t, s, `UPDATE workflow_instances SET definition_ref=?,definition_digest=?,current_step='design' WHERE work_id IN (?,?)`, definition.Definition.Ref, definition.Digest, leader, follower)
	var started int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE work_id IN (?,?) AND execution_started_at IS NOT NULL`, leader, follower).Scan(&started); err != nil || started != 2 {
		t.Fatalf("design-step fixture execution starts = %d err=%v, want 2", started, err)
	}
	actor := WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:overlap", AgentRef: "agent:operator", SessionRef: "session:overlap", ActorClass: ActorOperator}
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
	action := func(workID, name string, payload json.RawMessage) error {
		return runVerdictActionAs(t, s, workID, name, payload, 0, actor)
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
	design := json.RawMessage(`{"approach":"Sequence the shared Domain design before independent execution.","decisions":[{"id":"decision:shared-domain","question":"Where does the shared behavior belong?","choice":"Keep it in the declared child Domain.","rationale":"Both contracts retain the same approved Domain boundary.","rejected":[]}],"touched_refs":["domain:child"]}`)
	assertHeld := func() {
		t.Helper()
		version := readWorkVersion(t, s, follower)
		receipts := sequenceWitnessEventCount(t, s, follower)
		err := action(follower, "record_design", design)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap {
			t.Fatalf("held follower action = %v, want domain_overlap", err)
		}
		if readWorkVersion(t, s, follower) != version || sequenceWitnessEventCount(t, s, follower) != receipts {
			t.Fatal("refused follower action changed its version or effect receipts")
		}
		assertStep(follower, "design")
	}

	resolve("step-release-initial", ResolutionCompatibleWith, "Independent preparation can proceed before the shared design change")
	for _, workID := range []string{leader, follower} {
		if err := action(workID, "record_work_context", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("independent preparation for %s: %v", workID, err)
		}
	}
	resolve("step-release-hold", ResolutionDependsOn, "The follower waits until the leader completes the shared Domain design step")
	assertHeld()
	if err := action(leader, "record_design", design); err != nil {
		t.Fatalf("leader completes shared design: %v", err)
	}
	assertStep(leader, "execution")
	assertHeld()
	if err := action(leader, "record_work_context", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("leader's next step must remain usable while the follower waits: %v", err)
	}

	resolve("step-release-resume", ResolutionCompatibleWith, "The leader completed design; the remaining steps can proceed independently")
	if err := action(follower, "record_design", design); err != nil {
		t.Fatalf("released follower completes shared design: %v", err)
	}
	for _, workID := range []string{leader, follower} {
		assertStep(workID, "execution")
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
