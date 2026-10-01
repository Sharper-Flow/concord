package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Project-session handoff fixture (CD-0182 amendment): one shared work
// item across two Projects in two distinct repositories, each with its own
// claimed worktree, one active workflow contract, and one coordinator
// session per repository.
type projectHandoffFixture struct {
	store         *Store
	workID        string
	sourceProject string
	targetProject string
	sourceSession string
	targetSession string
	sourceTree    string
	targetTree    string
	// finalWorkVersion is the shared work item's version once the fixture
	// finishes: 3 after the work op, +4 contract-seed events, +2 claims.
	finalWorkVersion int64
}

func artifactDigest(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

func setupProjectHandoffFixture(t *testing.T) projectHandoffFixture {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	const workID = "work-h"
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		locatorProductEvent("product-w"),
		locatorProjectEvent("project-w"),
		locatorProjectEvent("project-e"),
		operationEvent("ph-membership-w", "product_project.added", SubjectProduct, "product-w", map[string]any{"product_id": "product-w", "project_id": "project-w", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2}),
		operationEvent("ph-membership-e", "product_project.added", SubjectProduct, "product-w", map[string]any{"product_id": "product-w", "project_id": "project-e", "role": "secondary", "reason": "fixture", "expected_version": 2, "resulting_version": 3}),
	}, ExpectedVersions: map[SubjectRef]int64{
		VersionRef(SubjectProduct, "product-w"): 0,
		VersionRef(SubjectProject, "project-w"): 0,
		VersionRef(SubjectProject, "project-e"): 0,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		workCreatedEvent(workID, "ph-create-"+workID),
		operationEvent("ph-work-member-w", "work_project.added", SubjectWorkItem, workID, map[string]any{"work_id": workID, "project_id": "project-w", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2}),
		operationEvent("ph-work-member-e", "work_project.added", SubjectWorkItem, workID, map[string]any{"work_id": workID, "project_id": "project-e", "role": "secondary", "reason": "fixture", "expected_version": 2, "resulting_version": 3}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
		t.Fatal(err)
	}
	repoW, repoE := t.TempDir(), t.TempDir()
	for _, repo := range []string{repoW, repoE} {
		gitRunStore(t, repo, "init", "-b", "main")
		gitRunStore(t, repo, "config", "user.email", "concord@example.invalid")
		gitRunStore(t, repo, "config", "user.name", "Concord Handoff Test")
		if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRunStore(t, repo, "add", "tracked.txt")
		gitRunStore(t, repo, "commit", "-m", "base")
		gitRunStore(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
		gitRunStore(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	}
	normalizedW, err := NormalizeProjectLocator(LocatorCanonicalPath, repoW)
	if err != nil {
		t.Fatal(err)
	}
	normalizedE, err := NormalizeProjectLocator(LocatorCanonicalPath, repoE)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		operationEvent("ph-locator-w", "project.locator_added", SubjectProject, "project-w", map[string]any{"project_id": "project-w", "locator_id": "path-w", "kind": string(LocatorCanonicalPath), "value": repoW, "normalized_value": normalizedW, "expected_version": 1, "resulting_version": 2}),
		operationEvent("ph-locator-e", "project.locator_added", SubjectProject, "project-e", map[string]any{"project_id": "project-e", "locator_id": "path-e", "kind": string(LocatorCanonicalPath), "value": repoE, "normalized_value": normalizedE, "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{
		VersionRef(SubjectProject, "project-w"): 1,
		VersionRef(SubjectProject, "project-e"): 1,
	}}); err != nil {
		t.Fatal(err)
	}
	seedProjectHandoffLaw(t, s, "product-w", "project-w")
	seedProjectHandoffContract(t, s, workID, 3)
	baseOutW, err := ExecGitRunner{}.Run(ctx, repoW, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	baseOutE, err := ExecGitRunner{}.Run(ctx, repoE, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	claimW, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "ph-op-w", WorkID: workID, ProjectID: "project-w",
		BaseSHA: strings.TrimSpace(string(baseOutW)), PrincipalRef: "principal-1", RequestID: "ph-req-w",
		ExpectedVersion: 7, Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimE, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "ph-op-e", WorkID: workID, ProjectID: "project-e",
		BaseSHA: strings.TrimSpace(string(baseOutE)), PrincipalRef: "principal-2", RequestID: "ph-req-e",
		ExpectedVersion: 8, Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return projectHandoffFixture{
		store: s, workID: workID,
		sourceProject: "project-w", targetProject: "project-e",
		sourceSession: "session/source", targetSession: "session/target",
		sourceTree: claimW.Entry.Path, targetTree: claimE.Entry.Path,
		finalWorkVersion: 9,
	}
}

// seedProjectHandoffLaw seeds the synthetic knowledge home the contract
// folds resolve law context against, keyed to the fixture's Project. The
// locator names its own directory, so the canonical-path unique index keeps
// the Project's claim locator and this law locator distinct.
func seedProjectHandoffLaw(t *testing.T, s *Store, productID, projectID string) {
	t.Helper()
	locatorPath := t.TempDir()
	normalized, err := NormalizeProjectLocator(LocatorCanonicalPath, locatorPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{
		Events:           []Event{operationEvent("ph-law-locator", "project.locator_added", SubjectProject, projectID, map[string]any{"project_id": projectID, "locator_id": "ph-law-locator", "kind": string(LocatorCanonicalPath), "value": locatorPath, "normalized_value": normalized, "expected_version": 2, "resulting_version": 3})},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, projectID): 2},
	}); err != nil {
		t.Fatal(err)
	}
	// The seed statements inline their literals: a multi-statement Exec
	// binds parameters to the first statement only on this driver, and the
	// law_subjects home-pair trigger then reads the wrong values.
	if _, err := s.DatabaseForTesting().Exec(fmt.Sprintf(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('%s','%s','%s'); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('%s','%s','%s','spec','accepted','.concord/docs/spec.md','Synthetic test law','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','test'); DELETE FROM fold_guard`, productID, projectID, "ph-law-locator", projectID, "ph-law-locator", "spec:one")); err != nil {
		t.Fatal(err)
	}
}

// seedProjectHandoffContract records one active workflow contract on the
// shared work item, mirroring the laneless research seed's event chain.
func seedProjectHandoffContract(t *testing.T, s *Store, workID string, workVersion int64) {
	t.Helper()
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/source", ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.research", 5)
	if !ok {
		t.Fatal("workflow.research v5 is not registered")
	}
	setup := []Event{
		workflowEventWithActor("ph-actor-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": workVersion, "resulting_version": workVersion + 1, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("ph-definition-"+workID, WorkflowDefinitionSelected, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": workVersion + 1, "resulting_version": workVersion + 2, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
		workflowEventWithActor("ph-contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": workVersion + 2, "resulting_version": workVersion + 3, "contract_version": 1, "premise": "cross-project handoff fixture", "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}, "outcome_predicates": []map[string]any{{"predicate_id": "predicate:handoff-fixture-exit", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}}, "required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowActionCompletedFixture("ph-approval-"+workID, workID, ownerRef, workVersion+3, "frame", "approve_contract"),
	}
	setup[2].PayloadVersion = 3
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): workVersion}}); err != nil {
		t.Fatal(err)
	}
}

// seedReplacementProjectHandoffContract supersedes the active contract with
// version 2, so the active contract version moves past a handoff recorded
// under version 1.
func seedReplacementProjectHandoffContract(t *testing.T, s *Store, workID string, workVersion int64) {
	t.Helper()
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/source", ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	successor := map[string]any{
		"contract_version": int64(2), "premise": "cross-project handoff fixture v2", "outcome_kind": "outcome",
		"outcome_payload":   map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	}
	supersede := workflowEventWithActor("ph-contract-2-"+workID, WorkflowContractSuperseded, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": workVersion, "resulting_version": workVersion + 1, "previous_contract_version": int64(1), "new_contract_version": int64(2), "supersede_reason": "fixture contract replacement", "audit_evidence": []string{"audit:handoff-fixture"}, "successor_contract": successor,
	})
	supersede.PayloadVersion = 2
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{supersede}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): workVersion}}); err != nil {
		t.Fatal(err)
	}
}

func (f projectHandoffFixture) artifactRef(t *testing.T, tree string) string {
	t.Helper()
	return fmt.Sprintf("sha256:%s:%s", artifactDigest(t, filepath.Join(tree, "tracked.txt")), filepath.Join(tree, "tracked.txt"))
}

func (f projectHandoffFixture) recordRequest(tree string) RecordProjectHandoffRequest {
	return RecordProjectHandoffRequest{
		WorkID: f.workID, SourceProjectID: f.sourceProject, TargetProjectID: f.targetProject,
		SourceSessionRef: f.sourceSession, BoundedJob: "verify the receiving repository's adapter surface",
		Changes:      []string{"adapter/opencode: opener route"},
		Verification: []string{"bun test adapter/opencode/ pass"},
		ArtifactRefs: nil,
		Blockers:     []string{}, NextAction: "consume the handoff and verify the opener route",
		SourceWorktree: tree, Now: time.Unix(20, 0).UTC(),
	}
}

func recordHandoff(t *testing.T, f projectHandoffFixture, tree string) ProjectHandoffResult {
	t.Helper()
	req := f.recordRequest(tree)
	req.ArtifactRefs = []string{f.artifactRef(t, tree)}
	out, err := runRecordProjectHandoffTx(f, req)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func consumeHandoff(t *testing.T, f projectHandoffFixture, handoffID string) ProjectHandoffConsumptionResult {
	t.Helper()
	out, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: handoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(30, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// runRecordProjectHandoffTx and runConsumeProjectHandoffTx drive the
// tx-scoped forms through the store's own transaction scope, the same core
// the agent mutation effects run.
func runRecordProjectHandoffTx(f projectHandoffFixture, req RecordProjectHandoffRequest) (ProjectHandoffResult, error) {
	var out ProjectHandoffResult
	err := f.store.Transact(context.Background(), func(tx *Transaction) error {
		var txErr error
		out, txErr = RecordProjectHandoffTx(context.Background(), tx, req)
		return txErr
	})
	return out, err
}

func runConsumeProjectHandoffTx(f projectHandoffFixture, req ConsumeProjectHandoffRequest) (ProjectHandoffConsumptionResult, error) {
	var out ProjectHandoffConsumptionResult
	err := f.store.Transact(context.Background(), func(tx *Transaction) error {
		var txErr error
		out, txErr = ConsumeProjectHandoffTx(context.Background(), tx, req)
		return txErr
	})
	return out, err
}

func TestProjectHandoffRecordsAndConsumesAcrossTwoProjectWorktrees(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	if recorded.AlreadyRecorded {
		t.Fatalf("first record replayed: %+v", recorded)
	}
	var state, consumer string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT state, consumed_by_session_ref FROM project_handoffs WHERE handoff_id=?`, recorded.HandoffID).Scan(&state, &consumer); err != nil {
		t.Fatal(err)
	}
	if state != ProjectHandoffRecorded || consumer != "" {
		t.Fatalf("state=%q consumer=%q, want recorded and unbound", state, consumer)
	}
	// Same-record replays resolve from state with no second event.
	replayedRecord, err := runRecordProjectHandoffTx(f, func() RecordProjectHandoffRequest {
		req := f.recordRequest(f.sourceTree)
		req.ArtifactRefs = []string{f.artifactRef(t, f.sourceTree)}
		return req
	}())
	if err != nil {
		t.Fatal(err)
	}
	if !replayedRecord.AlreadyRecorded || replayedRecord.HandoffID != recorded.HandoffID {
		t.Fatalf("record replay=%+v, want the same handoff id flagged replayed", replayedRecord)
	}
	consumed := consumeHandoff(t, f, recorded.HandoffID)
	if consumed.AlreadyConsumed {
		t.Fatalf("first consume replayed: %+v", consumed)
	}
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT state, consumed_by_session_ref FROM project_handoffs WHERE handoff_id=?`, recorded.HandoffID).Scan(&state, &consumer); err != nil {
		t.Fatal(err)
	}
	if state != ProjectHandoffConsumed || consumer != f.targetSession {
		t.Fatalf("state=%q consumer=%q, want consumed by the receiving session", state, consumer)
	}
	replayedConsume := consumeHandoff(t, f, recorded.HandoffID)
	if !replayedConsume.AlreadyConsumed {
		t.Fatalf("consume replay=%+v, want AlreadyConsumed", replayedConsume)
	}
	// A same-content re-record after consumption refuses: the successor
	// handoff records from its own state, never over the consumed bind.
	_, err = runRecordProjectHandoffTx(f, func() RecordProjectHandoffRequest {
		req := f.recordRequest(f.sourceTree)
		req.ArtifactRefs = []string{f.artifactRef(t, f.sourceTree)}
		return req
	}())
	if failureKind(err) != KindProjectionConflict || !strings.Contains(fmt.Sprint(err), "already consumed") {
		t.Fatalf("err=%v, want the consumed-replay refusal", err)
	}
	// The continuity snapshot carries the pending handoff only while it
	// stands unconsumed.
	snapshot, err := ReadWorkflowContinuity(context.Background(), f.store, ContinuityRequest{Work: f.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PendingProjectHandoff != nil {
		t.Fatalf("consumed handoff rendered pending: %+v", snapshot.PendingProjectHandoff)
	}
}

func TestProjectHandoffPendingHandoffRidesTheContinuitySnapshot(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	snapshot, err := ReadWorkflowContinuity(context.Background(), f.store, ContinuityRequest{Work: f.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PendingProjectHandoff == nil || snapshot.PendingProjectHandoff.HandoffID != recorded.HandoffID {
		t.Fatalf("pending handoff=%+v, want %q", snapshot.PendingProjectHandoff, recorded.HandoffID)
	}
	if snapshot.PendingProjectHandoff.BoundedJob == "" || snapshot.PendingProjectHandoff.TargetProjectID != f.targetProject {
		t.Fatalf("pending handoff=%+v, want the bounded job and receiving Project", snapshot.PendingProjectHandoff)
	}
}

func TestProjectHandoffRefusesDirtySourceWorktree(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	if err := os.WriteFile(filepath.Join(f.sourceTree, "tracked.txt"), []byte("mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := f.recordRequest(f.sourceTree)
	req.ArtifactRefs = []string{f.artifactRef(t, f.sourceTree)}
	// The probe runs before any mutation transaction, exactly as the agent
	// mutation plan runs it; the tx-scoped record never probes inside a
	// transaction.
	err := VerifyProjectHandoffArtifactPreservation(context.Background(), req.SourceWorktree, req.ArtifactRefs)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "unpreserved changes") {
		t.Fatalf("err=%v, want the unpreserved-changes refusal", err)
	}
	var rows int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM project_handoffs`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("rows=%d, want the refused record to write no handoff", rows)
	}
}

func TestProjectHandoffRefusesUnverifiedArtifactDigest(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	req := f.recordRequest(f.sourceTree)
	err := VerifyProjectHandoffArtifactPreservation(context.Background(), req.SourceWorktree, []string{fmt.Sprintf("sha256:%s:%s", strings.Repeat("0", 64), filepath.Join(f.sourceTree, "tracked.txt"))})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "does not match the worktree content") {
		t.Fatalf("err=%v, want the digest mismatch refusal", err)
	}
	// An artifact outside the claimed worktree refuses without a probe.
	err = VerifyProjectHandoffArtifactPreservation(context.Background(), req.SourceWorktree, []string{fmt.Sprintf("sha256:%s:%s", artifactDigest(t, filepath.Join(f.targetTree, "tracked.txt")), filepath.Join(f.targetTree, "tracked.txt"))})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "outside the claimed source worktree") {
		t.Fatalf("err=%v, want the outside-worktree refusal", err)
	}
}

func TestProjectHandoffRefusesNonMemberTargetAndSelfAddress(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	req := f.recordRequest(f.sourceTree)
	req.TargetProjectID = "project-nowhere"
	req.ArtifactRefs = []string{f.artifactRef(t, f.sourceTree)}
	_, err := runRecordProjectHandoffTx(f, req)
	if failureKind(err) != KindProjectionNotFound {
		t.Fatalf("err=%v, want the membership refusal", err)
	}
	self := f.recordRequest(f.sourceTree)
	self.TargetProjectID = f.sourceProject
	self.ArtifactRefs = []string{f.artifactRef(t, f.sourceTree)}
	_, err = runRecordProjectHandoffTx(f, self)
	if failureKind(err) != KindInvalidOperation {
		t.Fatalf("err=%v, want the self-address refusal", err)
	}
}

func TestProjectHandoffConsumeRefusesWrongTargetProject(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	_, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.sourceProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(30, 0).UTC(),
	})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "addresses Project") {
		t.Fatalf("err=%v, want the wrong-target refusal", err)
	}
	// A consume by a foreign receiving session of an already-consumed
	// handoff refuses; the binding survives.
	consumeHandoff(t, f, recorded.HandoffID)
	_, err = runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: "session/other", Now: time.Unix(31, 0).UTC(),
	})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "already consumed by another receiving session") {
		t.Fatalf("err=%v, want the foreign-session refusal", err)
	}
}

func TestProjectHandoffConsumeRefusesStaleContractVersion(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	_, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(30, 0).UTC(),
	})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "active contract is version 2") {
		t.Fatalf("err=%v, want the stale-contract refusal", err)
	}
	var state string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT state FROM project_handoffs WHERE handoff_id=?`, recorded.HandoffID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != ProjectHandoffRecorded {
		t.Fatalf("state=%q, want the stale consume to record nothing", state)
	}
}

// bindSessionToProjectWorktree records the occupancy row the verified
// landing writes (CD-0178 D3), the core's own session-to-Project placement
// evidence. The gate resolves the acting session's Project from it and
// never from a caller-supplied identity.
func bindSessionToProjectWorktree(t *testing.T, s *Store, tree, sessionRef string) {
	t.Helper()
	var worktreeID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT set_id || ':' || project_id || ':' || claim_op_id FROM worktree_entries WHERE path=? AND state='active'`, tree).Scan(&worktreeID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity) VALUES(?,?,?,1,1,1)`, worktreeID, sessionRef, "2026-09-30T00:00:00Z"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func runHandoffGate(t *testing.T, s *Store, workID, sessionRef string) error {
	t.Helper()
	return s.Transact(context.Background(), func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "gate")
		if err != nil {
			return err
		}
		return RefuseUnconsumedProjectHandoffTx(context.Background(), tx, workID, sessionRef)
	})
}

func TestManagedExecutionRefusesUntilHandoffConsumed(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	// Before any handoff, managed execution is unaffected.
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("no handoff recorded yet: gate refused %v", err)
	}
	// The sessions bind to their claimed worktrees: the gate resolves each
	// session's Project from its own verified placement.
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	recorded := recordHandoff(t, f, f.sourceTree)
	// The source session keeps acting on its own handoff.
	if err := runHandoffGate(t, f.store, f.workID, f.sourceSession); err != nil {
		t.Fatalf("source session gate refused %v", err)
	}
	// A session of another Project is not gated by a handoff that does not
	// address its Project: the wrong-Project edge refuses the wrong target
	// at consume, and the admission gate never deadlocks the bystander.
	bystander := "session/bystander"
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, bystander)
	if err := runHandoffGate(t, f.store, f.workID, bystander); err != nil {
		t.Fatalf("unaddressed bystander gate refused %v", err)
	}
	// The receiving session is refused until it consumes.
	err := runHandoffGate(t, f.store, f.workID, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed") {
		t.Fatalf("err=%v, want the unconsumed-handoff refusal", err)
	}
	consumeHandoff(t, f, recorded.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("post-consume gate refused %v", err)
	}
	// A contract replacement makes the consumed handoff stale, and the gate
	// refuses again until a fresh addressed handoff stands.
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	err = runHandoffGate(t, f.store, f.workID, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "active contract is version 2") {
		t.Fatalf("err=%v, want the stale-consumed gate refusal", err)
	}
}

// seedOpenWorkerAttempt records one nonterminal worker attempt and, when it
// names a dispatch actor, its dispatch attribution inside the fold-guarded
// write path. An attempt without an attribution event is the unknown-
// ownership shape: the projection row exists and no dispatch authorization
// names who ran it.
func seedOpenWorkerAttempt(t *testing.T, s *Store, workID, attemptID, dispatchActor string) {
	t.Helper()
	ctx := context.Background()
	lane := BuiltinLaneDefinitions()[0]
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	now := "2026-09-30T00:00:00Z"
	if _, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts
		(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at)
		VALUES (?,?,?,?,?,?,?,?,?, 'dispatched', ?)`, workID, attemptID, lane.ID, lane.Version, lane.Digest, lane.CapabilityClass, preferredModelForLane(lane), "1.0", "1.0", now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if dispatchActor != "" {
		payload, _ := json.Marshal(map[string]any{"attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "capability_class": lane.CapabilityClass, "packet_schema_version": "1.0", "report_schema_version": "1.0"})
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
			"ph-dispatch-"+attemptID, "worker.dispatched", "work_item", workID, dispatchActor, now, 4, string(payload)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func completeWorkerAttempt(t *testing.T, s *Store, workID, attemptID string) {
	t.Helper()
	lane := BuiltinLaneDefinitions()[0]
	completed := Event{EventID: "ph-completed-" + attemptID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(40, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}
}

// seedSessionVacateAndLanding records the version 2 relocation request and
// its verified landing for the source session, the CD-0190 release facts.
func seedSessionVacateAndLanding(t *testing.T, s *Store, workID, sessionRef, sourceTree, destination string) {
	t.Helper()
	vacated, err := json.Marshal(map[string]any{"work_id": workID, "project_id": "project-w", "session_ref": sessionRef, "source_directory": filepath.Clean(sourceTree), "destination_directory": filepath.Clean(destination), "landed_directory": ""})
	if err != nil {
		t.Fatal(err)
	}
	landed, err := json.Marshal(map[string]any{"work_id": workID, "project_id": "project-w", "session_ref": sessionRef, "source_directories": []string{filepath.Clean(sourceTree)}, "destination_directory": filepath.Clean(destination), "landed_directory": filepath.Clean(destination), "host_pid": 1, "host_pid_start": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "ph-vacate-" + sessionRef, Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: sessionRef, OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: 2, Payload: vacated},
		{EventID: "ph-vacate-landed-" + sessionRef, Kind: "work.session_vacate_landed", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: sessionRef, OccurredAt: time.Unix(51, 0).UTC(), PayloadVersion: 1, Payload: landed},
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestRetirementReadinessRequiresEveryVerifiedFact(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	// Nothing recorded yet: pending with the handoff blocker first.
	retirement, err := EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending || !strings.Contains(retirement.Blockers[0], "record the addressed Project handoff") {
		t.Fatalf("retirement=%+v, want the handoff blocker", retirement)
	}
	recordHandoff(t, f, f.sourceTree)
	retirement, err = EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending || !retirement.RecordedHandoff || !retirement.ArtifactsPreserved {
		t.Fatalf("retirement=%+v, want preserved artifacts and the open blockers", retirement)
	}
	if len(retirement.Blockers) != 1 || !strings.Contains(retirement.Blockers[0], "session_vacate has not recorded") {
		t.Fatalf("blockers=%v, want the vacate blocker", retirement.Blockers)
	}
	// The session's own open worker attempt blocks readiness.
	seedOpenWorkerAttempt(t, f.store, f.workID, "ph-attempt-1", f.sourceSession)
	retirement, err = EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.WorkersStopped || retirement.State != RetirementPending {
		t.Fatalf("retirement=%+v, want the open attempt to hold readiness", retirement)
	}
	completeWorkerAttempt(t, f.store, f.workID, "ph-attempt-1")
	// The verified vacate landing completes the last gate.
	locators, err := f.store.ProjectLocators(ctx, f.sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	seedSessionVacateAndLanding(t, f.store, f.workID, f.sourceSession, f.sourceTree, locators[0].NormalizedValue)
	retirement, err = EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementReady {
		t.Fatalf("retirement=%+v, want READY_TO_CLOSE_OR_REPLACE from the verified facts", retirement)
	}
	if !retirement.RecordedHandoff || !retirement.ArtifactsPreserved || !retirement.WorkersStopped || !retirement.VacateLanded {
		t.Fatalf("retirement=%+v, want every fact verified", retirement)
	}
	// Retirement derives from facts; it never writes.
	var events int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, f.workID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	retirement, err = EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	var after int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, f.workID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if events != after || retirement.State != RetirementReady {
		t.Fatalf("retirement=%+v events %d->%d, want a read-only derivation", retirement, events, after)
	}
}

func TestRetirementDoesNotCompleteOrCancelTheWork(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, recorded.HandoffID)
	locators, err := f.store.ProjectLocators(context.Background(), f.sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	seedSessionVacateAndLanding(t, f.store, f.workID, f.sourceSession, f.sourceTree, locators[0].NormalizedValue)
	retirement, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementReady {
		t.Fatalf("retirement=%+v, want readiness", retirement)
	}
	var lifecycle string
	var version int64
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT lifecycle, version FROM work_items WHERE id=?`, f.workID).Scan(&lifecycle, &version); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "in_progress" || version != f.finalWorkVersion {
		t.Fatalf("lifecycle=%q version=%d, want the shared work untouched by retirement", lifecycle, version)
	}
	var state string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, f.workID).Scan(&state); err != nil {
		if err != sql.ErrNoRows {
			t.Fatal(err)
		}
		state = ""
	}
	if state == "completed" || state == "cancelled" {
		t.Fatalf("instance_state=%q, want retirement to leave the workflow state alone", state)
	}
}

func TestRetirementUnknownWorkerAttributionBlocksReadiness(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, recorded.HandoffID)
	locators, err := f.store.ProjectLocators(context.Background(), f.sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	seedSessionVacateAndLanding(t, f.store, f.workID, f.sourceSession, f.sourceTree, locators[0].NormalizedValue)
	// An attempt whose dispatch event carries no actor attribution blocks
	// readiness: unknown ownership never reports optimistic readiness.
	seedOpenWorkerAttempt(t, f.store, f.workID, "ph-attempt-unknown", "")
	retirement, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending || retirement.WorkersStopped {
		t.Fatalf("retirement=%+v, want unknown attribution to block readiness", retirement)
	}
	// Another session's positively identified open worker is not this
	// session's worker.
	completeWorkerAttempt(t, f.store, f.workID, "ph-attempt-unknown")
	seedOpenWorkerAttempt(t, f.store, f.workID, "ph-attempt-foreign", "session/foreign")
	retirement, err = EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementReady {
		t.Fatalf("retirement=%+v, want the foreign session's worker to leave this session ready", retirement)
	}
}

func TestRetirementPendingVacateAndMissingHandoffBlockReadiness(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, recorded.HandoffID)
	// A committed vacate request whose verified landing never recorded
	// (the failed-move state) blocks readiness.
	vacated, err := json.Marshal(map[string]any{"work_id": f.workID, "project_id": f.sourceProject, "session_ref": f.sourceSession, "source_directory": filepath.Clean(f.sourceTree), "destination_directory": "/nowhere/main", "landed_directory": ""})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), f.store, Operation{Events: []Event{{
		EventID: "ph-vacate-pending", Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: f.sourceSession, OccurredAt: time.Unix(60, 0).UTC(), PayloadVersion: 2, Payload: vacated,
	}}}); err != nil {
		t.Fatal(err)
	}
	retirement, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending || retirement.VacateLanded {
		t.Fatalf("retirement=%+v, want the pending landing to block readiness", retirement)
	}
	found := false
	for _, blocker := range retirement.Blockers {
		if strings.Contains(blocker, "waits for its verified landing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers=%v, want the pending-landing blocker", retirement.Blockers)
	}
}

// TestBootResumeSeesOnlyTheHandoffAddressedToTheProject pins the
// Project-selected boot/resume visibility: the addressed handoff renders,
// the handoff addressed to the other Project does not, and a consumed
// handoff stops rendering.
func TestBootResumeSeesOnlyTheHandoffAddressedToTheProject(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	if handoff, err := storeReadPendingForProject(f, f.targetProject); err != nil || handoff != nil {
		t.Fatalf("handoff=%v err=%v, want no addressed handoff before any record", handoff, err)
	}
	recorded := recordHandoff(t, f, f.sourceTree)
	handoff, err := storeReadPendingForProject(f, f.targetProject)
	if err != nil || handoff == nil || handoff.HandoffID != recorded.HandoffID || handoff.BoundedJob == "" {
		t.Fatalf("handoff=%+v err=%v, want the addressed bounded job", handoff, err)
	}
	if other, err := storeReadPendingForProject(f, f.sourceProject); err != nil || other != nil {
		t.Fatalf("handoff=%v err=%v, want no handoff addressed to the source Project", other, err)
	}
	consumeHandoff(t, f, recorded.HandoffID)
	if handoff, err := storeReadPendingForProject(f, f.targetProject); err != nil || handoff != nil {
		t.Fatalf("handoff=%v err=%v, want the consumed handoff to stop rendering", handoff, err)
	}
}

func storeReadPendingForProject(f projectHandoffFixture, projectID string) (*ProjectHandoff, error) {
	return ReadPendingProjectHandoffForProject(context.Background(), f.store, f.workID, projectID)
}

// TestConsumeResolvesTheAddressedHandoffWithoutAnIdentity pins the
// addressed-resolution consume: an empty handoff id binds the newest
// unconsumed handoff addressed to the consumer's own Project, from recorded
// state, never from caller-supplied identity.
func TestConsumeResolvesTheAddressedHandoffWithoutAnIdentity(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	resolved, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(30, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.HandoffID != recorded.HandoffID || resolved.AlreadyConsumed {
		t.Fatalf("resolved=%+v, want the addressed handoff freshly bound", resolved)
	}
	// With the bind standing, the addressed resolution reports the same
	// replay the named consume would.
	replay, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(31, 0).UTC(),
	})
	if err != nil || !replay.AlreadyConsumed || replay.HandoffID != recorded.HandoffID {
		t.Fatalf("replay=%+v err=%v, want AlreadyConsumed", replay, err)
	}
	// A Project no handoff addresses refuses as typed not-found.
	if _, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, ConsumerProjectID: f.sourceProject, ConsumerSessionRef: f.sourceSession, Now: time.Unix(32, 0).UTC(),
	}); failureKind(err) != KindProjectionNotFound {
		t.Fatalf("err=%v, want the unaddressed not-found refusal", err)
	}
}
