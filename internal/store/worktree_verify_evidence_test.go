package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// CD-0096 D3 meets the acceptance checkpoint's verification requirement: the
// verify tier's green run claims a completed durable producer operation, so
// bind_evidence can name a core-run verification through the one
// durable-operation authority. A red or mutated run claims no authority, so
// the gate still refuses unbound verification.

// verifyEvidenceFixture seeds one Project with a path locator, a work item in
// it, an active worktree claim, and the fixture workflow instance parked at
// execution with a contract-required verification kind nothing binds. The
// shape follows seedItemAtAcceptanceRequiring, minus the lane attempt.
func verifyEvidenceFixture(t *testing.T, workID string) (*Store, *fakeWorktreeGit) {
	ctx := context.Background()
	s := openTemp(t)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-w"): 0, VersionRef(SubjectProject, "project-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: workID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Verify evidence fixture","priority":1}`)},
		{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	if err := s.AddProjectLocator(ctx, "project-w", ProjectLocator{ID: "path-w", Kind: LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(repoRoot)

	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	lane := BuiltinLaneDefinitions()[0]
	setup := []Event{
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("reviewer-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef, "agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "ref": workflowWorkerFixtureRef, "version": 2, "digest": workflowFixtureDefinition(t, 2).Digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, ownerRef, 5, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, ownerRef, 6, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, ownerRef, 7, "design", "record_design"),
		workflowEventWithActor("contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 9, "resulting_version": 10, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": ownerRef, "execution_model": preferredModelForLane(lane)}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{OpID: "wt-op-" + workID, WorkID: workID, ProjectID: "project-w", BaseSHA: git.branches["main"], PrincipalRef: "principal-1", RequestID: "req-claim-" + workID, ExpectedVersion: readWorkVersion(t, s, workID), Now: time.Unix(10, 0).UTC(), Runner: git})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Entry.State != worktreeEntryActive {
		t.Fatalf("worktree claim state=%q, want active", claim.Entry.State)
	}
	if err := os.MkdirAll(claim.Entry.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	return s, git
}

// verifyLeaseID matches the id produced by the worktree verify mutation.
func verifyLeaseID(workID string) string {
	return "sha256:" + strings.Repeat("a", 64) + ":worktree-verify:" + workID
}

func verifyRequestID(workID string) string { return "req-v-" + workID }

// verifyOperationRef states the durable-operation identity a green verify
// run claims, including the compact production-shaped lease convention.
func verifyOperationRef(leaseID string) string { return worktreeVerifyOperationRef(leaseID) }

// verifyEvidenceRun drives one worktree-verify run for the fixture work.
func verifyEvidenceRun(git *fakeWorktreeGit, workID, leaseID string, exitCode int) WorktreeVerifyRequest {
	return WorktreeVerifyRequest{
		Owner: SessionWorktreeOwner{ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1"}, WorkID: workID, ProjectID: "project-w",
		Command: []string{"go", "test", "./..."}, LeaseID: leaseID, PrincipalRef: "principal-1", RequestID: verifyRequestID(workID),
		Now: time.Unix(20, 0).UTC(), Runner: git,
		RunCommand: func(_ context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
			return exitCode, []byte("verify output"), false, nil
		},
	}
}

func durableVerifyOperationCount(t *testing.T, s *Store, leaseID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, verifyOperationRef(leaseID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A green worktree-verify run claims a completed durable producer operation,
// the bind route names it, and the acceptance gate accepts the item. The
// run's authority carries the whole chain: binding, outstanding-requirement
// calculation, premise confirmation, and completion.
func footprintProofReceiptPackageScope(t *testing.T, s *Store) {
	t.Helper()
	var home string
	if err := s.db.QueryRow(`SELECT locator_value FROM project_locators WHERE project_id='project' AND locator_id='workflow-law-locator'`).Scan(&home); err != nil {
		t.Fatal(err)
	}
	packageMap := `{"schema_version":"1.0","packages":[{"path":"internal/store","domain_ids":["root"]}]}` + "\n"
	writeKnowledgeFile(t, home, knowledgePackageMapPath, packageMap)
	gitRunStore(t, home, "init", "-b", "main")
	gitRunStore(t, home, "config", "user.email", "concord@example.invalid")
	gitRunStore(t, home, "config", "user.name", "Concord Receipt Test")
	commit := commitKnowledgeRepo(t, home, "synthetic receipt package ownership")
	footprintProofGuard(t, s,
		`INSERT INTO knowledge_package_domains(home_project_id,home_locator_id,package_path,domain_id,map_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','internal/store','root','`+nativeDigest([]byte(packageMap))+`','`+commit+`')`,
		`UPDATE law_subjects SET scanned_commit_oid='`+commit+`',content_hash='`+nativeDigest([]byte("# Synthetic test law\n"))+`' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator'`)
}

func TestGreenVerifyRunBindsAsVerificationEvidence(t *testing.T) {
	ctx := context.Background()
	f, repo, refs := pReceiptFixture(t, "verify-evidence-route", 1, 1, func(f workContextFixture) {
		footprintProofReceiptPackageScope(t, f.store)
		authoritative, err := workflowEvidenceKindBound(ctx, f.store.db, f.workID, "verification", 0)
		if err != nil {
			t.Fatal(err)
		}
		if authoritative {
			t.Fatal("verification evidence is bound before any verify run ran")
		}
	})
	s, workID := f.store, f.workID
	footprintProofSyntheticScope(t, s, workID)
	ref := refs[0][0]
	if len(ref) != 87 {
		t.Fatalf("compact verify operation reference length=%d, want 87", len(ref))
	}
	var operations int
	if err := s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, ref).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 1 {
		t.Fatal("the green run claimed no durable producer operation")
	}
	var resultKind, evidenceRefs string
	if err := s.DatabaseForTesting().QueryRow(`SELECT result_kind,evidence_refs FROM durable_operations WHERE op_id=? AND work_id=? AND principal_ref=? AND request_id='request:p:0:0'`, ref, workID, f.owner.PrincipalRef).Scan(&resultKind, &evidenceRefs); err != nil {
		t.Fatal(err)
	}
	if resultKind != "completed" || !strings.Contains(evidenceRefs, ref) {
		t.Fatalf("verify authority result_kind=%q evidence_refs=%q", resultKind, evidenceRefs)
	}
	if got := countEvidenceBinding(t, s, workID, "verification", ref); got != 1 {
		t.Fatalf("verify-run binding count=%d, want 1", got)
	}
	authoritativeAfter, err := workflowEvidenceKindBound(ctx, s.DatabaseForTesting(), workID, "verification", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !authoritativeAfter {
		t.Fatal("the bound verify run does not satisfy the verification authority check")
	}

	var definitionRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&definitionRef); err != nil {
		t.Fatal(err)
	}
	pinned, ok := BuiltinWorkflowRegistry().Current(definitionRef)
	if !ok {
		t.Fatalf("pinned definition %s is not registered", definitionRef)
	}
	outstanding, err := outstandingWorkflowEvidenceRequirementsForWork(ctx, s.DatabaseForTesting(), workID, pinned.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(outstanding) != 0 {
		t.Fatalf("outstanding evidence requirements after the bind = %v, want none", outstanding)
	}

	seedComparisonObservation(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
		t.Fatalf("associate reviewed verify receipt: %v", err)
	}
	if err := pConfirmReceiptPremise(t, f, repo); err != nil {
		t.Fatalf("confirm with reviewed verify receipt: %v", err)
	}
	bindCompletionClosure(t, s, workID, f.owner)
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	if err := j.terminalAction(t, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), f.owner); err != nil {
		t.Fatalf("complete with reviewed verify receipt: %v", err)
	}
	var state string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, f.workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("instance_state=%q, want completed", state)
	}
}

// A red verify run is history, never authority: it claims no producer
// operation, a binding naming it refuses at the fold, and the gate still
// refuses the unbound verification requirement.
func TestRedVerifyRunNamesNoAuthority(t *testing.T) {
	ctx := context.Background()
	workID := "verify-evidence-red-run"
	s, git := verifyEvidenceFixture(t, workID)
	defer s.Close()

	leaseID := verifyLeaseID(workID)
	result, err := s.VerifyWorktree(ctx, verifyEvidenceRun(git, workID, leaseID, 1))
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 {
		t.Fatalf("red run result=%+v", result)
	}
	if got := durableVerifyOperationCount(t, s, leaseID); got != 0 {
		t.Fatalf("red run durable operations=%d, want 0", got)
	}

	bindPayload := json.RawMessage(`{"evidence_kind":"verification","evidence_ref":"` + verifyOperationRef(leaseID) + `","immutable_subject_ref":"` + verifyOperationRef(leaseID) + `","producer_id":"principal-1","producer_run_ref":"` + verifyOperationRef(leaseID) + `","producer_watermark":"` + verifyRequestID(workID) + `"}`)
	bindErr := runVerdictAction(t, s, workID, "bind_evidence", bindPayload)
	requireRecoveryFailure(t, bindErr, KindInvariantViolation, "bind naming a red verify run")
	if got := countEvidenceBinding(t, s, workID, "verification", verifyOperationRef(leaseID)); got != 0 {
		t.Fatalf("refused red-run binding count=%d, want 0", got)
	}
	authoritative, err := workflowEvidenceKindBound(ctx, s.DatabaseForTesting(), workID, "verification", 0)
	if err != nil {
		t.Fatal(err)
	}
	if authoritative {
		t.Fatal("the red run satisfies the verification authority check")
	}
}

// The verify tier writes operational authority only: no workflow event, no
// step or state change, no definition pin change, and no attempt-epoch
// movement. The event log a rebuild replays is byte-identical, and the
// context-checkpoint epoch join still reads the workflow attempt alone.
func TestVerifyAuthorityLeavesWorkflowReplayUntouched(t *testing.T) {
	ctx := context.Background()
	workID := "verify-evidence-replay"
	s, git := verifyEvidenceFixture(t, workID)
	defer s.Close()

	var eventsBefore int
	var workflowBefore string
	db := s.DatabaseForTesting()
	if err := db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT current_step||'|'||instance_state||'|'||definition_ref||'|'||definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&workflowBefore); err != nil {
		t.Fatal(err)
	}

	if _, err := s.VerifyWorktree(ctx, verifyEvidenceRun(git, workID, verifyLeaseID(workID), 0)); err != nil {
		t.Fatal(err)
	}

	var eventsAfter int
	var workflowAfter string
	if err := db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT current_step||'|'||instance_state||'|'||definition_ref||'|'||definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&workflowAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("verify run wrote %d workflow events, want %d", eventsAfter, eventsBefore)
	}
	if workflowAfter != workflowBefore {
		t.Fatalf("verify run moved workflow state %q to %q", workflowBefore, workflowAfter)
	}

	// The lease operation claims epoch 1, so MAX(attempt_epoch) still reads
	// the workflow attempt that context checkpoints bind.
	var maxEpoch int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=?`, workID).Scan(&maxEpoch); err != nil {
		t.Fatal(err)
	}
	if maxEpoch != 1 {
		t.Fatalf("MAX(attempt_epoch) after the verify run = %d, want 1", maxEpoch)
	}
}
