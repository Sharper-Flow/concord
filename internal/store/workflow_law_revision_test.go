package store

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWorkflowLawRevisionSameIDAmendmentRemainsCompatible(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "law-amendment")
	insertLawRevisionFixture(t, s, "law-amendment", "spec:stable", "sha256:"+strings.Repeat("a", 64))
	insertAcceptedLaw(t, s, "spec:stable", "sha256:"+strings.Repeat("b", 64))

	got, err := findStaleWorkflowLawRevision(context.Background(), s.DatabaseForTesting(), "p", "l", "law-amendment", 1, []string{"spec:stable"})
	if err != nil {
		t.Fatalf("same-ID amendment check: %v", err)
	}
	if got != nil {
		t.Fatalf("same-ID amendment became stale: %+v", got)
	}
}

func TestWorkflowLawRevisionSupersessionRefusesPinnedConsumer(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "law-supersession")
	insertLawRevisionFixture(t, s, "law-supersession", "spec:old", "sha256:"+strings.Repeat("a", 64))
	insertSupersededLaw(t, s, "spec:old", "sha256:"+strings.Repeat("a", 64))
	insertAcceptedLaw(t, s, "spec:new", "sha256:"+strings.Repeat("b", 64))
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('p','l','spec:new','supersedes','spec:old','commit'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	got, err := findStaleWorkflowLawRevision(context.Background(), s.DatabaseForTesting(), "p", "l", "law-supersession", 1, []string{"spec:old"})
	if err != nil {
		t.Fatalf("supersession check: %v", err)
	}
	if got == nil || got.OldLawID != "spec:old" || got.AcceptedSuccessorLawID != "spec:new" || got.AcceptedSuccessorContentHash == "" {
		t.Fatalf("supersession diagnosis = %+v, want old spec:old and successor spec:new", got)
	}
}

func TestWorkflowLawRevisionSupersessionRefusesLegacyUnpinnedConsumer(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "law-legacy")
	insertLawRevisionFixture(t, s, "law-legacy", "spec:legacy", "sha256:"+strings.Repeat("a", 64))
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM workflow_contract_law_revisions; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	insertSupersededLaw(t, s, "spec:legacy", "sha256:"+strings.Repeat("a", 64))
	insertAcceptedLaw(t, s, "spec:successor", "sha256:"+strings.Repeat("c", 64))
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('p','l','spec:successor','supersedes','spec:legacy','commit'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	got, err := findStaleWorkflowLawRevision(context.Background(), s.DatabaseForTesting(), "p", "l", "law-legacy", 1, []string{"spec:legacy"})
	if err != nil || got == nil {
		t.Fatalf("legacy supersession diagnosis = %+v err=%v", got, err)
	}
}

func TestWorkflowLawRevisionMissingProjectionFailsClosed(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "law-missing")
	insertLawRevisionFixture(t, s, "law-missing", "spec:missing", "sha256:"+strings.Repeat("a", 64))
	_, err := findStaleWorkflowLawRevision(context.Background(), s.DatabaseForTesting(), "p", "l", "law-missing", 1, []string{"spec:missing"})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "spec:missing" {
		t.Fatalf("missing law diagnosis = %v, want typed projection_not_found with candidate", err)
	}
}

func TestWorkflowLawRevisionSupersededWithoutSuccessorFailsClosed(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "law-no-successor")
	insertLawRevisionFixture(t, s, "law-no-successor", "spec:orphan", "sha256:"+strings.Repeat("a", 64))
	insertSupersededLaw(t, s, "spec:orphan", "sha256:"+strings.Repeat("a", 64))
	_, err := findStaleWorkflowLawRevision(context.Background(), s.DatabaseForTesting(), "p", "l", "law-no-successor", 1, []string{"spec:orphan"})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "spec:orphan" {
		t.Fatalf("orphan supersession diagnosis = %v, want typed projection_not_found with candidate", err)
	}
}

func TestWorkflowLawRevisionRecontractsThroughProductionRoutes(t *testing.T) {
	workID := "law-recontract-route"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includePremise: true})
	attachWorkflowLawPin(t, s, workID, "spec:one", "sha256:"+strings.Repeat("a", 64))
	cutoverLawProjection(t, s, "spec:one", "spec:two")
	version := readWorkVersion(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/executor", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "supersede_contract", Payload: mustJSONValue(map[string]any{"contract_version": 2, "premise": "continue under successor law", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:successor-law", "immutable_subject_ref": "commit:successor-law", "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{"spec:two"}, "law_modifies": []string{}, "rigor_class": "prototype/internal", "supersede_reason": "move to the accepted successor law", "audit_evidence": []string{"evidence:law-cutover"}}), Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "recontract-recover", OperationID: "recontract-recover", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "recontract-recover", RequestID: "request:recontract-recover", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 3, 0, time.UTC),
	})
	_ = leaveFold(context.Background(), tx)
	if err != nil {
		tx.Rollback()
		t.Fatalf("production stale-contract recovery route: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if result.ResultingVersion == 0 {
		t.Fatal("successor contract approval did not produce a workflow result")
	}
	var pinnedHash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=2 AND law_id='spec:two'`, workID).Scan(&pinnedHash); err != nil || pinnedHash != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("successor contract pin=%q err=%v", pinnedHash, err)
	}
	var activeCount, oldConfirmationCount, successorConfirmationCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil || activeCount != 1 {
		t.Fatalf("active contract count=%d err=%v, want exactly one", activeCount, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, workID).Scan(&oldConfirmationCount); err != nil || oldConfirmationCount != 1 {
		t.Fatalf("prior premise confirmations=%d err=%v, want one historical confirmation", oldConfirmationCount, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=2`, workID).Scan(&successorConfirmationCount); err != nil || successorConfirmationCount != 0 {
		t.Fatalf("successor premise confirmations=%d err=%v, want zero", successorConfirmationCount, err)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("recontracted workflow step=%q, want unchanged execution step", got)
	}
	version = result.ResultingVersion
	tx, err = s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	startResult, err := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "start_execution", Payload: mustJSONValue(map[string]any{}), Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "recontract-start", OperationID: "recontract-start", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "recontract-start", RequestID: "request:recontract-start", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 4, 0, time.UTC)})
	_ = leaveFold(context.Background(), tx)
	if err != nil {
		tx.Rollback()
		t.Fatalf("ordinary post-recovery action: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if startResult.ResultingVersion <= version || currentStep(t, s, workID) != "execution" {
		t.Fatalf("post-recovery action result=%d step=%q", startResult.ResultingVersion, currentStep(t, s, workID))
	}
}

func TestWorkflowLawRevisionRecoveryActionIsStaleOnlyAndApprovalRequired(t *testing.T) {
	staleWork := "law-recovery-action-stale"
	staleStore, _ := seedCompletionGateCase(t, staleWork, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	attachWorkflowLawPin(t, staleStore, staleWork, "spec:one", "sha256:"+strings.Repeat("a", 64))
	cutoverLawProjection(t, staleStore, "spec:one", "spec:two")
	_, action, err := WorkflowActionDefinitionFor(context.Background(), staleStore, BuiltinWorkflowRegistry(), staleWork, "supersede_contract")
	if err != nil {
		t.Fatalf("stale recovery action definition: %v", err)
	}
	if action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
		t.Fatalf("stale recovery action=%+v, want operator approval-required action", action)
	}

	currentWork := "law-recovery-action-current"
	currentStore, _ := seedCompletionGateCase(t, currentWork, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	attachWorkflowLawPin(t, currentStore, currentWork, "spec:one", "sha256:"+strings.Repeat("a", 64))
	if _, _, err := WorkflowActionDefinitionFor(context.Background(), currentStore, BuiltinWorkflowRegistry(), currentWork, "supersede_contract"); err == nil {
		t.Fatal("non-stale workflow exposed the contract recovery action")
	} else {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidOperation {
			t.Fatalf("non-stale recovery error=%v, want invalid_operation", err)
		}
	}
}

func TestWorkflowLawRevisionRecoveryRequiresAcceptedSuccessorPin(t *testing.T) {
	workID := "law-recovery-requires-successor-pin"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	attachWorkflowLawPin(t, s, workID, "spec:one", "sha256:"+strings.Repeat("a", 64))
	cutoverLawProjection(t, s, "spec:one", "spec:two")
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/executor", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	currentVersion := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: currentVersion, ActionID: "supersede_contract", Payload: mustJSONValue(map[string]any{"contract_version": 2, "premise": "incorrectly omit the accepted successor", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:successor-law", "immutable_subject_ref": "commit:successor-law", "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype/internal", "supersede_reason": "move to the accepted successor law", "audit_evidence": []string{"evidence:law-cutover"}}), Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "recontract-missing-successor", OperationID: "recontract-missing-successor", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "recontract-missing-successor", RequestID: "request:recontract-missing-successor", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 3, 0, time.UTC),
	})
	_ = leaveFold(context.Background(), tx)
	_ = tx.Rollback()
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
		t.Fatalf("recovery without successor pin error=%v, want invalid_payload", err)
	}
}

func TestWorkflowLawRevisionAllowsTerminalLifecycleMutationAfterCutover(t *testing.T) {
	workID := "law-terminal-route"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	cutoverLawProjection(t, s, "spec:one", "spec:two")
	version := readWorkVersion(t, s, workID)
	var currentLifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&currentLifecycle); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workTransitionEvent("law-terminal-cancel", workID, currentLifecycle, "cancelled", version, version+1)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("terminal lifecycle route after cutover: %v", err)
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil || lifecycle != "cancelled" {
		t.Fatalf("terminal lifecycle=%q err=%v, want cancelled", lifecycle, err)
	}
}

func TestWorkflowLawRevisionCutoverCommitsBeforeCrossConnectionAcceptance(t *testing.T) {
	workID := "law-cross-connection-order"
	s1, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	attachWorkflowLawPin(t, s1, workID, "spec:one", "sha256:"+strings.Repeat("a", 64))
	s2, err := Open(context.Background(), s1.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	locked := make(chan struct{})
	release := make(chan struct{})
	cutoverErr := make(chan error, 1)
	crossVersion := readWorkVersion(t, s1, workID)
	go func() {
		tx, beginErr := s1.DatabaseForTesting().BeginTx(context.Background(), nil)
		if beginErr != nil {
			cutoverErr <- beginErr
			return
		}
		if foldErr := enterFold(context.Background(), tx); foldErr != nil {
			tx.Rollback()
			cutoverErr <- foldErr
			return
		}
		if _, execErr := tx.Exec(`UPDATE law_subjects SET status='superseded' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`); execErr != nil {
			tx.Rollback()
			cutoverErr <- execErr
			return
		}
		if _, execErr := tx.Exec(`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','docs/spec-two.md','Synthetic successor law','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test')`); execErr != nil {
			tx.Rollback()
			cutoverErr <- execErr
			return
		}
		if _, execErr := tx.Exec(`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','supersedes','spec:one','test')`); execErr != nil {
			tx.Rollback()
			cutoverErr <- execErr
			return
		}
		close(locked)
		<-release
		_ = leaveFold(context.Background(), tx)
		cutoverErr <- tx.Commit()
	}()
	select {
	case <-locked:
	case err := <-cutoverErr:
		t.Fatalf("cutover before lock: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("cutover did not reach its transaction lock")
	}

	acceptanceDone := make(chan error, 1)
	acceptanceStarted := make(chan struct{})
	go func() {
		owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID, ActorClass: ActorOperator}
		close(acceptanceStarted)
		tx, beginErr := s2.DatabaseForTesting().BeginTx(context.Background(), nil)
		if beginErr != nil {
			acceptanceDone <- beginErr
			return
		}
		if foldErr := enterFold(context.Background(), tx); foldErr != nil {
			tx.Rollback()
			acceptanceDone <- foldErr
			return
		}
		_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: crossVersion, ActionID: "accept_worker_result", Payload: mustJSONValue(map[string]any{"attempt_id": "attempt:cross", "attempt_epoch": 1}), Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "cross-accept", OperationID: "cross-accept", PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "cross-accept", RequestID: "request:cross-accept", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 4, 0, time.UTC)})
		_ = leaveFold(context.Background(), tx)
		if actionErr == nil {
			tx.Rollback()
			acceptanceDone <- nil
			return
		}
		tx.Rollback()
		acceptanceDone <- actionErr
	}()
	select {
	case <-acceptanceStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("cross-connection acceptance did not start before cutover release")
	}
	close(release)
	if err := <-cutoverErr; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acceptanceDone:
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindStaleLawRevision {
			t.Fatalf("cross-connection acceptance=%v, want stale_law_revision after committed cutover", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cross-connection acceptance did not finish")
	}
}

func TestWorkflowLawRevisionCutoverCommitsBeforeCrossProcessAcceptance(t *testing.T) {
	workID := "law-cross-process-order"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	attachWorkflowLawPin(t, s, workID, "spec:one", "sha256:"+strings.Repeat("a", 64))
	version := readWorkVersion(t, s, workID)

	cutover := exec.Command(os.Args[0], "-test.run=^TestWorkflowLawRevisionCrossProcessWorker$", "-test.v=false")
	cutover.Env = append(os.Environ(), "CONCORD_LAW_RACE_ROLE=cutover", "CONCORD_LAW_RACE_DB="+s.Path(), "CONCORD_LAW_RACE_WORK="+workID)
	cutoverOut, err := cutover.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cutoverIn, err := cutover.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cutover.Stderr = os.Stderr
	if err := cutover.Start(); err != nil {
		t.Fatal(err)
	}
	cutoverScanner := bufio.NewScanner(cutoverOut)
	t.Cleanup(func() { _ = cutover.Process.Kill() })
	if !cutoverScanner.Scan() || cutoverScanner.Text() != "cutover=locked" {
		t.Fatalf("cutover worker did not establish its transaction lock: %q", cutoverScanner.Text())
	}

	acceptance := exec.Command(os.Args[0], "-test.run=^TestWorkflowLawRevisionCrossProcessWorker$", "-test.v=false")
	acceptance.Env = append(os.Environ(), "CONCORD_LAW_RACE_ROLE=acceptance", "CONCORD_LAW_RACE_DB="+s.Path(), "CONCORD_LAW_RACE_WORK="+workID, fmt.Sprintf("CONCORD_LAW_RACE_VERSION=%d", version))
	acceptanceOut, err := acceptance.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	acceptance.Stderr = os.Stderr
	if err := acceptance.Start(); err != nil {
		t.Fatal(err)
	}
	acceptanceScanner := bufio.NewScanner(acceptanceOut)
	t.Cleanup(func() { _ = acceptance.Process.Kill() })
	if !acceptanceScanner.Scan() || acceptanceScanner.Text() != "acceptance=ready" {
		t.Fatalf("acceptance worker did not become ready before cutover commit: %q", acceptanceScanner.Text())
	}

	if _, err := fmt.Fprintln(cutoverIn, "release"); err != nil {
		t.Fatal(err)
	}
	if !cutoverScanner.Scan() || cutoverScanner.Text() != "cutover=committed" {
		t.Fatalf("cutover worker did not commit: %q", cutoverScanner.Text())
	}
	if err := cutover.Wait(); err != nil {
		t.Fatalf("cutover worker: %v", err)
	}
	if !acceptanceScanner.Scan() || acceptanceScanner.Text() != "acceptance=stale_law_revision" {
		t.Fatalf("acceptance worker result = %q, want stale_law_revision", acceptanceScanner.Text())
	}
	if err := acceptance.Wait(); err != nil {
		t.Fatalf("acceptance worker: %v", err)
	}
}

func TestWorkflowLawRevisionCrossProcessWorker(t *testing.T) {
	role := os.Getenv("CONCORD_LAW_RACE_ROLE")
	if role == "" {
		return
	}
	if role == "acceptance" {
		fmt.Println("acceptance=ready")
	}
	s, err := Open(context.Background(), os.Getenv("CONCORD_LAW_RACE_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	workID := os.Getenv("CONCORD_LAW_RACE_WORK")

	switch role {
	case "cutover":
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enterFold(context.Background(), tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE law_subjects SET status='superseded' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','docs/spec-two.md','Synthetic successor law','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test')`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','supersedes','spec:one','test')`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		fmt.Println("cutover=locked")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := leaveFold(context.Background(), tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		fmt.Println("cutover=committed")
	case "acceptance":
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enterFold(context.Background(), tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID, ActorClass: ActorOperator}
		_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: mustEnvInt64(t, "CONCORD_LAW_RACE_VERSION"), ActionID: "accept_worker_result", Payload: mustJSONValue(map[string]any{"attempt_id": "attempt:cross-process", "attempt_epoch": 1}), Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "cross-process-accept", OperationID: "cross-process-accept", PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "cross-process-accept", RequestID: "request:cross-process-accept", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 4, 0, time.UTC)})
		_ = leaveFold(context.Background(), tx)
		tx.Rollback()
		var failure *Failure
		if !failureAs(actionErr, &failure) || failure.Kind != KindStaleLawRevision {
			t.Fatalf("acceptance error = %v, want stale_law_revision", actionErr)
		}
		fmt.Println("acceptance=stale_law_revision")
	default:
		t.Fatalf("unknown race worker role %q", role)
	}
}

func mustEnvInt64(t *testing.T, name string) int64 {
	t.Helper()
	var value int64
	if _, err := fmt.Sscan(os.Getenv(name), &value); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return value
}

func attachWorkflowLawPin(t *testing.T, s *Store, workID, lawID, hash string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,?,?,?); DELETE FROM fold_guard`, workID, 1, lawID, hash); err != nil {
		t.Fatal(err)
	}
}

func cutoverLawProjection(t *testing.T, s *Store, oldID, successorID string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE law_subjects SET status='superseded' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id=?`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator',?,'spec','accepted','docs/spec-two.md','Synthetic successor law','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test')`, successorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project','workflow-law-locator',?,'supersedes',?,'test')`, successorID, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowLawRevisionPinsFoldAndRebuildWithoutConsultingCurrentLaw(t *testing.T) {
	s := openTemp(t)
	workID := "law-pinned-rebuild"
	seedWork(t, s, workID)
	insertAcceptedLaw(t, s, "spec:one", "sha256:"+strings.Repeat("a", 64))
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/law-pinned")
	events := []Event{
		workflowEventWithActor("law-pinned-actor", WorkflowActorRecorded, workID, actor, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/law-pinned", "actor_class": "agent"}),
		workflowEventWithActor("law-pinned-definition", WorkflowDefinitionSelected, workID, actor, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowFixtureRef, "version": 1, "digest": workflowFixtureDigest(t), "work_kind": workflowFixtureWorkKind}),
	}
	contract := workflowEventWithActor("law-pinned-contract", WorkflowContractApproved, workID, actor, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "pin the accepted law", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law-pin", "immutable_subject_ref": "commit:law-pin", "expected_result": "pass"}, "required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "law_revisions": []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}}, "law_boundary_version": 1, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"})
	contract.PayloadVersion = 2
	events = append(events, contract)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=?`, workID).Scan(&hash); err != nil || hash != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("folded law pin = %q err=%v", hash, err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=?`, workID).Scan(&hash); err != nil || hash != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("rebuilt law pin = %q err=%v", hash, err)
	}
}

func TestWorkflowLawRevisionKeepsRawCompletionButRefusesWorkflowAcceptanceAfterCutover(t *testing.T) {
	workID := "law-stale-in-flight"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	claim, err := ClaimStep(context.Background(), s, ClaimRequest{
		OpID: "law-stale-in-flight-op", WorkID: workID, WorkflowTypeRef: workflowFixtureRef, WorkflowTypeVersion: 1,
		StepID: "execution", StepKind: StepInternalSQLite, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64),
		AcceptedScopeSnapshot: `{}`, PrincipalRef: "principal/in-flight", Tool: "workflow-test", IdempotencyKey: "law-stale-in-flight-claim",
		RequestID: "request:law-stale-in-flight", ContractDigest: testManifestDigest, ObservedAt: time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("claim before cutover: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE law_subjects SET status='superseded' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'; INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','docs/spec-two.md','Synthetic successor law','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test'); INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','supersedes','spec:one','test'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	if _, err = CompleteStep(context.Background(), s, CompleteRequest{
		OpID: claim.OpID, AttemptEpoch: claim.AttemptEpoch, ResultKind: ResultCompleted, PrincipalRef: "principal/in-flight", Tool: "workflow-test",
		IdempotencyKey: "law-stale-in-flight-complete", RequestID: "request:law-stale-in-flight-complete", ObservedAt: time.Date(2026, 8, 17, 0, 0, 1, 0, time.UTC),
	}); err != nil {
		t.Fatalf("raw durable completion must remain recordable after cutover: %v", err)
	}
	var resultKind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT result_kind FROM durable_operations WHERE op_id=? AND attempt_epoch=?`, claim.OpID, claim.AttemptEpoch).Scan(&resultKind); err != nil || resultKind != string(ResultCompleted) {
		t.Fatalf("raw completion result=%q err=%v, want completed", resultKind, err)
	}
	var failure *Failure
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID, ActorClass: ActorOperator}
	currentVersion := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: currentVersion, ActionID: "accept_worker_result", Payload: mustJSONValue(map[string]any{"attempt_id": "attempt:stale", "attempt_epoch": 1}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "accept-stale", OperationID: "accept-stale", PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "accept-stale", RequestID: "request:accept-stale", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 17, 0, 0, 2, 0, time.UTC),
	})
	_ = leaveFold(context.Background(), tx)
	_ = tx.Rollback()
	if !failureAs(err, &failure) || failure.Kind != KindStaleLawRevision {
		t.Fatalf("workflow result acceptance error=%+v, want stale_law_revision", err)
	}
}

func insertLawRevisionFixture(t *testing.T, s *Store, workID, lawID, hash string) {
	t.Helper()
	specMandate := "[\"" + lawID + "\"]"
	actorRef := "actor:" + strings.Repeat("a", 64)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal/law','client/law','agent/law','session/law','agent','now')`, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'law test','check','{"kind":"check"}','internal_sqlite','[]','[]','now',?,?, '[]',1,'test')`, workID, actorRef, specMandate); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,?,?,?)`, workID, 1, lawID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func insertAcceptedLaw(t *testing.T, s *Store, lawID, hash string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l',?,'spec','accepted','docs/law.md',?,?, 'commit'); DELETE FROM fold_guard`, lawID, lawID, hash); err != nil {
		t.Fatal(err)
	}
}

func insertSupersededLaw(t *testing.T, s *Store, lawID, hash string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l',?,'spec','superseded','docs/law.md',?,?, 'commit'); DELETE FROM fold_guard`, lawID, lawID, hash); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowLawStalenessTxRunsOverlapOnEmptyMandate(t *testing.T) {
	s, _ := seedOverlapProjection(t, "law-staleness-overlap-left", "law-staleness-overlap-right", false)
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = checkWorkflowLawRevisionStalenessTx(ctx, tx, "law-staleness-overlap-left")
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindDomainOverlap {
		t.Fatalf("empty-mandate boundary check error=%v, want domain_overlap from the overlap half", err)
	}
}

func TestWorkflowContractStalenessDBIgnoresOverlapAdvisoryOnly(t *testing.T) {
	s, _ := seedOverlapProjection(t, "law-staleness-advisory-left", "law-staleness-advisory-right", false)
	err := workflowContractStalenessDB(context.Background(), s.DatabaseForTesting(), "law-staleness-advisory-left")
	if err != nil {
		t.Fatalf("advisory staleness predicate error=%v, want nil: the predicate answers staleness only and must not surface Domain overlap", err)
	}
}
