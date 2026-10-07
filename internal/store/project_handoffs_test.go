package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
		seedRealGitOrigin(t, repo)
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
	return consumeHandoffAs(t, f, handoffID, f.targetSession)
}

// consumeHandoffAs lands the consuming session through the real verified
// claim landing first, then binds the handoff: the consume requires the
// verified work/Project/session occupancy the landing records, and a bind
// never precedes the landing.
func consumeHandoffAs(t *testing.T, f projectHandoffFixture, handoffID, sessionRef string) ProjectHandoffConsumptionResult {
	t.Helper()
	bindSessionToProjectWorktree(t, f.store, f.targetTree, sessionRef)
	out, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: handoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: sessionRef, Now: time.Unix(30, 0).UTC(),
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
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
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

// bindSessionToProjectWorktree drives the real verified claim landing
// (CD-0178 D3): the landing reads the host process start from /proc,
// records the session's occupancy row on the claimed worktree, and clears
// the session's other rows. The gate and the consume resolve the acting
// session's Project from this row and never from a caller-supplied
// identity. The same landing replays idempotently.
func bindSessionToProjectWorktree(t *testing.T, s *Store, tree, sessionRef string) {
	t.Helper()
	var workID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT c.work_id FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE e.path=? AND e.state='active' LIMIT 1`, tree).Scan(&workID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordWorktreeClaimLanding(context.Background(), WorktreeClaimLandingRequest{
		WorkID: workID, SessionRef: sessionRef, LandedDirectory: filepath.Clean(tree), HostPID: os.Getpid(),
	}); err != nil {
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
	// A contract replacement holds the gate closed on the consumed bind: a
	// bind authorizes execution only under the contract it was made under,
	// so the stale bind refuses closed and the fresh-handoff recovery is
	// the route back to admission.
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	err = runHandoffGate(t, f.store, f.workID, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "was consumed under contract version 1") || !strings.Contains(fmt.Sprint(err), "active contract is version 2") {
		t.Fatalf("err=%v, want the stale-bind gate refusal", err)
	}
	// The source-side recovery: a fresh addressed handoff under the active
	// contract supersedes the stale frontier, and consuming it reopens
	// admission for this session.
	req := f.recordRequest(f.sourceTree)
	req.BoundedJob = "fresh successor job"
	req.Now = time.Unix(40, 0).UTC()
	successor, recordErr := runRecordProjectHandoffTx(f, req)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	err = runHandoffGate(t, f.store, f.workID, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed") {
		t.Fatalf("err=%v, want the fresh frontier to hold the gate while unconsumed", err)
	}
	consumeHandoff(t, f, successor.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("post-recovery gate refused %v", err)
	}
}

// The consuming session's own replay is a bind check under the active
// contract, never a bypass: after a contract replacement the stale bind
// refuses closed, and the fresh successor handoff is the route back.
func TestProjectHandoffConsumeReplayRefusesAfterContractReplacement(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	first := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, first.HandoffID)
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	_, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: first.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(32, 0).UTC(),
	})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "recorded under contract version 1") || !strings.Contains(fmt.Sprint(err), "active contract is version 2") {
		t.Fatalf("err=%v, want the stale-bind replay refusal", err)
	}
	// The addressed-resolution form refuses the same way: the stale bind
	// cannot replay under the renewed contract.
	_, err = runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(33, 0).UTC(),
	})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "active contract is version 2") {
		t.Fatalf("err=%v, want the addressed stale-bind replay refusal", err)
	}
	// Recovery: a fresh successor handoff under the active contract records,
	// consumes, and replays as AlreadyConsumed under the live contract.
	req := f.recordRequest(f.sourceTree)
	req.BoundedJob = "fresh successor job"
	req.Now = time.Unix(40, 0).UTC()
	second, recordErr := runRecordProjectHandoffTx(f, req)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	consumed := consumeHandoff(t, f, second.HandoffID)
	if consumed.AlreadyConsumed {
		t.Fatalf("fresh consume replayed: %+v", consumed)
	}
	replay, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: second.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(41, 0).UTC(),
	})
	if err != nil || !replay.AlreadyConsumed || replay.HandoffID != second.HandoffID {
		t.Fatalf("replay=%+v err=%v, want the live-bind replay", replay, err)
	}
}

// The review-probe regression: after a contract replacement, the fresh
// successor handoff recorded and consumed under the renewed contract is the
// current frontier, and its live bind admits the receiving session — the
// superseded bind behind it never has to resurrect.
func TestManagedExecutionGateAdmitsAFreshConsumedFrontierAfterContractReplacement(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	first := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, first.HandoffID)
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	req := f.recordRequest(f.sourceTree)
	req.BoundedJob = "fresh successor job"
	req.Now = time.Unix(40, 0).UTC()
	second, err := runRecordProjectHandoffTx(f, req)
	if err != nil {
		t.Fatal(err)
	}
	consumeHandoff(t, f, second.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("gate refused after the fresh successor handoff was consumed: %v", err)
	}
}

// An unconsumed handoff under a superseded contract holds the gate closed:
// the stale consume refuses, so only a fresh addressed handoff under the
// active contract, consumed by the receiving session, reopens admission.
func TestManagedExecutionGateHoldsOnStaleUnconsumedHandoff(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	recordHandoff(t, f, f.sourceTree)
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	err := runHandoffGate(t, f.store, f.workID, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed under contract version 1") {
		t.Fatalf("err=%v, want the stale-unconsumed gate refusal", err)
	}
	// A fresh handoff under the active contract supersedes the stale one:
	// the frontier is now the fresh handoff, and the gate still refuses
	// while it stands unconsumed.
	req := f.recordRequest(f.sourceTree)
	req.BoundedJob = "fresh successor job"
	req.Now = time.Unix(40, 0).UTC()
	second, recordErr := runRecordProjectHandoffTx(f, req)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	err = runHandoffGate(t, f.store, f.workID, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed") || strings.Contains(fmt.Sprint(err), "contract version 1") {
		t.Fatalf("err=%v, want the fresh frontier to hold the gate without the stale version message", err)
	}
	consumeHandoff(t, f, second.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("post-consume gate refused %v", err)
	}
}

// A session whose Project placement cannot resolve on a handoff-bearing work
// refuses at the admission boundary, and ordinary handoff-free work stays
// admitted whatever the caller's placement.
func TestManagedExecutionGateRefusesUnplacedSessionOnHandoffBearingWork(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	// Handoff-free: an unplaced session is not this feature's subject, and
	// the gate leaves it alone.
	if err := runHandoffGate(t, f.store, f.workID, "session/unplaced"); err != nil {
		t.Fatalf("handoff-free gate refused %v", err)
	}
	recordHandoff(t, f, f.sourceTree)
	err := runHandoffGate(t, f.store, f.workID, "session/unplaced")
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "no verified placement") {
		t.Fatalf("err=%v, want the unplaced-session refusal", err)
	}
	// An unattributable caller refuses the same way: admission cannot bind
	// an addressed handoff to an identity it cannot prove.
	err = runHandoffGate(t, f.store, f.workID, "")
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "no verified placement") {
		t.Fatalf("err=%v, want the unattributable-caller refusal", err)
	}
	// The placed receiving session is gated only by the handoff addressed to
	// its own Project, and consuming it reopens admission.
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	recorded := mustReadPendingHandoffForProject(t, f, f.targetProject)
	consumeHandoff(t, f, recorded.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("post-consume gate refused %v", err)
	}
}

func mustReadPendingHandoffForProject(t *testing.T, f projectHandoffFixture, projectID string) *ProjectHandoff {
	t.Helper()
	handoff, err := ReadPendingProjectHandoffForProject(context.Background(), f.store, f.workID, projectID, "")
	if err != nil || handoff == nil {
		t.Fatalf("handoff=%v err=%v, want the addressed pending handoff", handoff, err)
	}
	return handoff
}

// A recorded landing released the rows its request's claim held; occupancy
// rows that stand afterwards belong to a later claim, and the old landing
// proves nothing about this retirement (CD-0190 D2).
func TestRetirementReadinessRefusesRenewedOccupancyAfterLanding(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recordHandoff(t, f, f.sourceTree)
	destination := projectMainCheckout(t, f.sourceTree)
	// The source session holds the occupancy row the verified landing
	// releases, so the landing is the real release fact.
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	vacateAndLandSession(t, f, f.sourceSession, f.sourceTree, destination)
	retirement, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementReady {
		t.Fatalf("retirement=%+v, want readiness from the verified facts", retirement)
	}
	// A later claim's occupancy rows stand: the landing of the old request
	// no longer proves this session released, so readiness is pending.
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	retirement, err = EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending || retirement.VacateLanded {
		t.Fatalf("retirement=%+v, want renewed occupancy to hold readiness", retirement)
	}
	found := false
	for _, blocker := range retirement.Blockers {
		if strings.Contains(blocker, "occupancy rows on this work after its recorded landing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers=%v, want the renewed-occupancy blocker", retirement.Blockers)
	}
}

// An ignored, uncommitted artifact survives neither `git status --porcelain`
// nor the session's worktree, so hash equality alone is not preservation:
// the probe requires the referenced bytes to be committed at HEAD.
func TestProjectHandoffRefusesIgnoredUncommittedArtifact(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	if err := os.WriteFile(filepath.Join(f.sourceTree, ".gitignore"), []byte("ephemeral.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.sourceTree, "add", ".gitignore")
	gitRunStore(t, f.sourceTree, "commit", "-m", "ignore the ephemeral artifact")
	ignored := filepath.Join(f.sourceTree, "ephemeral.txt")
	if err := os.WriteFile(ignored, []byte("unpreserved artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := fmt.Sprintf("sha256:%s:%s", artifactDigest(t, ignored), ignored)
	err := VerifyProjectHandoffArtifactPreservation(context.Background(), f.sourceTree, []string{ref})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "not committed at the source worktree's HEAD") {
		t.Fatalf("err=%v, want the uncommitted-artifact refusal", err)
	}
	// The same artifact committed at HEAD verifies as durable preservation.
	gitRunStore(t, f.sourceTree, "add", "-f", "ephemeral.txt")
	gitRunStore(t, f.sourceTree, "commit", "-m", "preserve the artifact")
	if err := VerifyProjectHandoffArtifactPreservation(context.Background(), f.sourceTree, []string{ref}); err != nil {
		t.Fatalf("committed artifact refused: %v", err)
	}
}

// An artifact reference whose path resolves through a symlink outside the
// claimed worktree refuses before any bytes are read: the immutable
// reference must live inside the claimed source worktree.
func TestProjectHandoffRefusesSymlinkEscapingArtifact(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.sourceTree, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.sourceTree, "add", "link.txt")
	gitRunStore(t, f.sourceTree, "commit", "-m", "link the artifact")
	ref := fmt.Sprintf("sha256:%s:%s", artifactDigest(t, outside), link)
	err := VerifyProjectHandoffArtifactPreservation(context.Background(), f.sourceTree, []string{ref})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "resolves through a symlink outside the claimed source worktree") {
		t.Fatalf("err=%v, want the symlink-escape refusal", err)
	}
}

// seedOrphanWorkerAttemptRow records the anomalous shape the retirement
// gate must treat as unknown attribution: a nonterminal worker_attempts
// projection row with no dispatch authorization event at all. The real
// dispatch chain always writes the authorization beside the row, so this
// shape is a damaged or legacy projection, and readiness must block on it.
func seedOrphanWorkerAttemptRow(t *testing.T, s *Store, workID, attemptID string) {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts
		(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at)
		VALUES (?,?,?,?,?,?,?,?,?, 'dispatched', ?)`, workID, attemptID, lane.ID, lane.Version, lane.Digest, lane.CapabilityClass, preferredModelForLane(lane), "1.0", "1.0", "2026-09-30T00:00:00Z"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// recordHandoffSessionActor records the session's workflow actor through the
// real WorkflowActorRecorded fold when the handoff fixture has not recorded
// it yet, so workflow_actors binds the actor ref to the authenticated
// session reference exactly as production does. It returns the ref.
func recordHandoffSessionActor(t *testing.T, f projectHandoffFixture, sessionRef string) string {
	t.Helper()
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: sessionRef, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	var recorded int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded == 1 {
		return actorRef
	}
	version := fixtureWorkVersion(t, f.store, f.workID)
	event := workflowEventWithActor("ph-actor-"+sessionRef, WorkflowActorRecorded, f.workID, actorRef, map[string]any{"work_id": f.workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef, "agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": "agent"})
	if err := applyWorkflowTestOperation(context.Background(), f.store, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.workID): version}}); err != nil {
		t.Fatal(err)
	}
	return actorRef
}

func fixtureWorkVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// seedSessionOwnedDispatchedAttempt drives the real dispatch authorization
// chain the retirement gate derives worker ownership from: the session's
// recorded workflow actor, the dispatch_worker action start on the pinned
// current step at the next per-step epoch, the dispatch_worker completion
// that binds the attempt in flight with its lane identity, and the
// worker.dispatched evidence event whose Actor column carries the
// authenticated principal/client actor the evidence boundary stamps
// (cmd/concord applyWorkerEvidence) — never the session reference. The
// completion fold creates the in_flight worker_attempts row and the evidence
// fold moves it to dispatched, so the projection rows the gate reads are the
// folds' own output.
func seedSessionOwnedDispatchedAttempt(t *testing.T, f projectHandoffFixture, sessionRef, attemptID string) {
	t.Helper()
	ctx := context.Background()
	actorRef := recordHandoffSessionActor(t, f, sessionRef)
	var currentStep string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, f.workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	var startedCount int64
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='workflow.action_started' AND subject_id=? AND json_extract(payload,'$.step_id')=?`, f.workID, currentStep).Scan(&startedCount); err != nil {
		t.Fatal(err)
	}
	epoch := startedCount + 1
	version := fixtureWorkVersion(t, f.store, f.workID)
	lane := BuiltinLaneDefinitions()[0]
	packetDigest := "sha256:" + strings.Repeat("c", 64)
	started := workflowEventWithActor("ph-dispatch-start-"+attemptID, WorkflowActionStarted, f.workID, actorRef, map[string]any{"work_id": f.workID, "expected_version": version, "resulting_version": version + 1, "step_id": currentStep, "action_id": "dispatch_worker", "attempt_epoch": epoch, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "dispatch:" + attemptID, "actor_ref": actorRef, "execution_model": preferredModelForLane(lane)})
	completed := workflowEventWithActor("ph-dispatch-done-"+attemptID, WorkflowActionCompleted, f.workID, actorRef, map[string]any{"work_id": f.workID, "expected_version": version + 1, "resulting_version": version + 2, "step_id": currentStep, "action_id": "dispatch_worker", "attempt_epoch": epoch, "worker_attempt_id": attemptID, "worker_lane_id": lane.ID, "worker_lane_version": lane.Version, "worker_lane_digest": lane.Digest, "worker_capability_class": lane.CapabilityClass, "worker_packet_digest": packetDigest, "worker_packet_predicate_ids": []any{}, "worker_worktree_identity": f.sourceTree, "result_evidence_refs": []any{}, "changed_refs": []any{f.workID}, "actor_ref": actorRef})
	// The dispatch completion carries its worker bindings on payload
	// version 3, the registered current version.
	completed.PayloadVersion = 3
	if err := applyWorkflowTestOperation(ctx, f.store, Operation{Events: []Event{started, completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.workID): version}}); err != nil {
		t.Fatal(err)
	}
	// The worker evidence stamps its Actor column with the authenticated
	// client/principal identity the worker-evidence boundary records, which
	// is never the dispatching session reference.
	dispatch := Event{EventID: "ph-dispatched-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: "client:concord-1:operator", OccurredAt: time.Unix(45, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketDigest: packetDigest, PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
	if err := ApplyOperation(ctx, f.store, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatal(err)
	}
}

func completeWorkerAttempt(t *testing.T, s *Store, workID, attemptID string) {
	t.Helper()
	lane := BuiltinLaneDefinitions()[0]
	var reportSchemaVersion string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT report_schema_version FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&reportSchemaVersion); err != nil {
		t.Fatal(err)
	}
	completed := Event{EventID: "ph-completed-" + attemptID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(40, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: reportSchemaVersion})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}
}

// projectMainCheckout resolves the registered main checkout a worktree's
// repository answers with: the canonical checkout the vacate destination
// names.
func projectMainCheckout(t *testing.T, worktree string) string {
	t.Helper()
	out, err := ExecGitRunner{}.Run(context.Background(), worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

// recordSessionVacateRequest commits the version 2 relocation request the
// way the session_vacate mutation writes it: the canonical payload shape,
// validated and folded by the core's own append authority, with no landing
// recorded by the requester.
func recordSessionVacateRequest(t *testing.T, f projectHandoffFixture, sessionRef, sourceTree, destination string) {
	t.Helper()
	request, err := json.Marshal(sessionVacatedPayload{WorkID: f.workID, ProjectID: f.sourceProject, SessionRef: sessionRef, SourceDirectory: filepath.Clean(sourceTree), DestinationDirectory: filepath.Clean(destination)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), f.store, Operation{Events: []Event{{
		EventID: "ph-vacate-" + sessionRef, Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: sessionRef, OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: 2, Payload: request,
	}}}); err != nil {
		t.Fatal(err)
	}
}

// vacateAndLandSession commits the relocation request and drives the real
// verified landing the CD-0190 release facts bind to: the landing reads the
// host process start from /proc, verifies the committed request's
// registered main checkout, and releases the session's occupancy rows in
// the landing's own transaction.
func vacateAndLandSession(t *testing.T, f projectHandoffFixture, sessionRef, sourceTree, destination string) {
	t.Helper()
	recordSessionVacateRequest(t, f, sessionRef, sourceTree, destination)
	landing, err := f.store.RecordSessionVacateLanding(context.Background(), SessionVacateLandingRequest{
		WorkID: f.workID, SessionRef: sessionRef, LandedDirectory: filepath.Clean(destination), HostPID: os.Getpid(), Now: time.Unix(51, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if landing.AlreadyRecorded {
		t.Fatalf("landing replayed: %+v", landing)
	}
	if len(landing.ReleasedSources) == 0 {
		t.Fatalf("landing released nothing: %+v, want the session's occupied source rows", landing)
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
	// The session's own open worker attempt blocks readiness: the dispatch
	// authorization chain attributes the attempt to this session's
	// authenticated actor.
	seedSessionOwnedDispatchedAttempt(t, f, f.sourceSession, "ph-attempt-1")
	retirement, err = EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.WorkersStopped || retirement.State != RetirementPending {
		t.Fatalf("retirement=%+v, want the open attempt to hold readiness", retirement)
	}
	completeWorkerAttempt(t, f.store, f.workID, "ph-attempt-1")
	// The real verified vacate landing completes the last gate: the request
	// and its /proc-readback landing release the session's occupancy rows.
	destination := projectMainCheckout(t, f.sourceTree)
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	vacateAndLandSession(t, f, f.sourceSession, f.sourceTree, destination)
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
	destination := projectMainCheckout(t, f.sourceTree)
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	vacateAndLandSession(t, f, f.sourceSession, f.sourceTree, destination)
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
	destination := projectMainCheckout(t, f.sourceTree)
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	vacateAndLandSession(t, f, f.sourceSession, f.sourceTree, destination)
	// An attempt whose projection row stands with no dispatch
	// authorization blocks readiness: unknown ownership never reports
	// optimistic readiness.
	seedOrphanWorkerAttemptRow(t, f.store, f.workID, "ph-attempt-unknown")
	retirement, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending || retirement.WorkersStopped {
		t.Fatalf("retirement=%+v, want unknown attribution to block readiness", retirement)
	}
	// Another session's positively identified open worker is not this
	// session's worker: the real dispatch chain attributes the attempt to
	// the foreign session's authenticated actor.
	completeWorkerAttempt(t, f.store, f.workID, "ph-attempt-unknown")
	seedSessionOwnedDispatchedAttempt(t, f, "session/foreign", "ph-attempt-foreign")
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
	recordSessionVacateRequest(t, f, f.sourceSession, f.sourceTree, "/nowhere/main")
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
// handoff renders again only to the session its own bind names.
func TestBootResumeSeesOnlyTheHandoffAddressedToTheProject(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	if handoff, err := storeReadPendingForProject(f, f.targetProject, f.targetSession); err != nil || handoff != nil {
		t.Fatalf("handoff=%v err=%v, want no addressed handoff before any record", handoff, err)
	}
	recorded := recordHandoff(t, f, f.sourceTree)
	handoff, err := storeReadPendingForProject(f, f.targetProject, f.targetSession)
	if err != nil || handoff == nil || handoff.HandoffID != recorded.HandoffID || handoff.BoundedJob == "" {
		t.Fatalf("handoff=%+v err=%v, want the addressed bounded job", handoff, err)
	}
	if other, err := storeReadPendingForProject(f, f.sourceProject, f.sourceSession); err != nil || other != nil {
		t.Fatalf("handoff=%v err=%v, want no handoff addressed to the source Project", other, err)
	}
	consumeHandoff(t, f, recorded.HandoffID)
	if handoff, err := storeReadPendingForProject(f, f.targetProject, ""); err != nil || handoff != nil {
		t.Fatalf("handoff=%v err=%v, want the consumed handoff to stop rendering for an identity-less read", handoff, err)
	}
	if handoff, err := storeReadPendingForProject(f, f.targetProject, "session/other"); err != nil || handoff != nil {
		t.Fatalf("handoff=%v err=%v, want the consumed handoff to stay hidden from a foreign session", handoff, err)
	}
}

// storeReadPendingForProject reads the boot/resume frontier as one caller:
// an empty session reference is the identity-less read, which renders
// recorded handoffs but never a consumed bind.
func storeReadPendingForProject(f projectHandoffFixture, projectID, sessionRef string) (*ProjectHandoff, error) {
	return ReadPendingProjectHandoffForProject(context.Background(), f.store, f.workID, projectID, sessionRef)
}

// TestBootResumeOmitsStaleHandoffsAfterContractReplacement pins the resume
// frontier: a handoff recorded under a superseded contract can never be
// consumed, so resume renders nothing for it — not before, not after a
// fresh successor handoff's bind consumed the current frontier.
func TestBootResumeOmitsStaleHandoffsAfterContractReplacement(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	first := recordHandoff(t, f, f.sourceTree)
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	if handoff, err := storeReadPendingForProject(f, f.targetProject, f.targetSession); err != nil || handoff != nil {
		t.Fatalf("handoff=%+v err=%v, want the stale handoff to stop rendering after the replacement", handoff, err)
	}
	// The fresh successor renders as the current frontier, and once its bind
	// consumes it, the stale superseded record does not resurface.
	req := f.recordRequest(f.sourceTree)
	req.BoundedJob = "fresh successor job"
	req.Now = time.Unix(40, 0).UTC()
	second, err := runRecordProjectHandoffTx(f, req)
	if err != nil {
		t.Fatal(err)
	}
	handoff := mustReadPendingHandoffForProject(t, f, f.targetProject)
	if handoff.HandoffID != second.HandoffID {
		t.Fatalf("handoff=%+v, want the fresh successor %q as the resume frontier", handoff, second.HandoffID)
	}
	consumeHandoff(t, f, second.HandoffID)
	if handoff, err := storeReadPendingForProject(f, f.targetProject, ""); err != nil || handoff != nil {
		t.Fatalf("handoff=%v err=%v, want the stale %q to stay omitted after the fresh bind", handoff, err, first.HandoffID)
	}
}

// TestBootResumeRendersNoJobBehindAConsumedFrontier pins the frontier
// ordinal: once the newest addressed handoff's bind is consumed, an older
// recorded handoff behind it is superseded — the boot renders no bounded job
// for it, and the consuming session stays admitted.
func TestBootResumeRendersNoJobBehindAConsumedFrontier(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	first := recordHandoff(t, f, f.sourceTree)
	req := f.recordRequest(f.sourceTree)
	req.BoundedJob = "successor repository job"
	req.Now = time.Unix(40, 0).UTC()
	second, err := runRecordProjectHandoffTx(f, req)
	if err != nil {
		t.Fatal(err)
	}
	consumeHandoff(t, f, second.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("gate refused the consuming session %v", err)
	}
	rendered, err := storeReadPendingForProject(f, f.targetProject, "")
	if err != nil {
		t.Fatal(err)
	}
	if rendered != nil {
		t.Fatalf("boot resurrected older handoff %s after the current frontier %s was consumed", first.HandoffID, second.HandoffID)
	}
}

// TestProjectHandoffLostResponseRecoveryRedeliversTheReceiverBind pins the
// lost-response recovery boundary: when the consume commits but its response
// is lost, the receiving session's fresh boot/resume read re-renders its own
// current consumed handoff, so the bounded job, changes, verification, and
// next action survive the lost response. A foreign or identity-less read
// renders nothing, the replayed consume resolves as the standing bind with
// no second event, and the admission gate keeps the recovered receiver
// admitted.
func TestProjectHandoffLostResponseRecoveryRedeliversTheReceiverBind(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, recorded.HandoffID)
	handoff, err := storeReadPendingForProject(f, f.targetProject, f.targetSession)
	if err != nil {
		t.Fatal(err)
	}
	if handoff == nil || handoff.HandoffID != recorded.HandoffID || handoff.State != ProjectHandoffConsumed {
		t.Fatalf("handoff=%+v, want the receiver's own consumed bind re-rendered", handoff)
	}
	if handoff.BoundedJob == "" || handoff.NextAction == "" || len(handoff.Changes) == 0 || len(handoff.Verification) == 0 {
		t.Fatalf("handoff=%+v, want the full bounded job context to survive the lost response", handoff)
	}
	if foreign, err := storeReadPendingForProject(f, f.targetProject, "session/other"); err != nil || foreign != nil {
		t.Fatalf("handoff=%v err=%v, want the consumed bind hidden from a foreign session", foreign, err)
	}
	replay, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(31, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.AlreadyConsumed {
		t.Fatalf("replay=%+v, want the standing bind to replay as AlreadyConsumed", replay)
	}
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("gate refused the recovered receiver %v", err)
	}
}

// TestRetirementReadinessRefusesStaleContractHandoffAfterVerifiedVacate pins
// the retirement contract boundary: readiness derives only from a handoff
// the work's active contract binds. After the real verified vacate landing
// completes, a contract replacement leaves the recorded handoff one the
// receiving session can never consume, so readiness drops back to pending
// with the stale contract named instead of reporting
// READY_TO_CLOSE_OR_REPLACE from it.
func TestRetirementReadinessRefusesStaleContractHandoffAfterVerifiedVacate(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recordHandoff(t, f, f.sourceTree)
	destination := projectMainCheckout(t, f.sourceTree)
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	vacateAndLandSession(t, f, f.sourceSession, f.sourceTree, destination)
	ready, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != RetirementReady {
		t.Fatalf("retirement=%+v, want readiness from the verified facts before the contract replacement", ready)
	}
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	stale, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if stale.State != RetirementPending || stale.RecordedHandoff {
		t.Fatalf("retirement=%+v, want the stale contract's handoff to lose readiness", stale)
	}
	found := false
	for _, blocker := range stale.Blockers {
		if strings.Contains(blocker, "contract version 1") && strings.Contains(blocker, "active contract is version 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers=%v, want the stale-contract blocker", stale.Blockers)
	}
	if !stale.ArtifactsPreserved || !stale.WorkersStopped || !stale.VacateLanded {
		t.Fatalf("retirement=%+v, want the preserved, stopped, and landed facts to stand", stale)
	}
}

// The retirement derivation reads in two snapshots — the phase-one handoff
// facts, then the git preservation probe, then the final transaction —
// because the probe cannot run inside a SQL transaction. The final
// transaction revalidates the phase-one snapshot, and each test here
// interleaves one committed identity change into exactly that window
// through the store's deterministic probe seam: a contract replacement, a
// newer addressed handoff, and a source-claim change each leave the
// snapshot obsolete, so readiness fails closed on the drift instead of
// emitting the stale facts as READY_TO_CLOSE_OR_REPLACE.

// retireWithProbeInterleave runs one retirement evaluation whose probe
// window fires interleave exactly once, then clears the seam.
func retireWithProbeInterleave(t *testing.T, f projectHandoffFixture, interleave func()) ProjectSessionRetirement {
	t.Helper()
	f.store.retireProbeInterleave = func() {
		f.store.retireProbeInterleave = nil
		interleave()
	}
	retirement, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	return retirement
}

func TestRetirementDriftRefusesReadinessOnContractReplacementBetweenProbeAndFinalRead(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recordHandoff(t, f, f.sourceTree)
	destination := projectMainCheckout(t, f.sourceTree)
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	vacateAndLandSession(t, f, f.sourceSession, f.sourceTree, destination)
	ready, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != RetirementReady {
		t.Fatalf("retirement=%+v, want readiness before any interleaved change", ready)
	}
	version := fixtureWorkVersion(t, f.store, f.workID)
	drifted := retireWithProbeInterleave(t, f, func() {
		seedReplacementProjectHandoffContract(t, f.store, f.workID, version)
	})
	if drifted.State != RetirementPending {
		t.Fatalf("retirement=%+v, want the interleaved contract replacement to refuse readiness", drifted)
	}
	found := false
	for _, blocker := range drifted.Blockers {
		if strings.Contains(blocker, "active workflow contract moved from version 1 to 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers=%v, want the contract-drift blocker", drifted.Blockers)
	}
}

func TestRetirementDriftRefusesReadinessOnNewerHandoffBetweenProbeAndFinalRead(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	first := recordHandoff(t, f, f.sourceTree)
	destination := projectMainCheckout(t, f.sourceTree)
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	vacateAndLandSession(t, f, f.sourceSession, f.sourceTree, destination)
	ready, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != RetirementReady {
		t.Fatalf("retirement=%+v, want readiness before any interleaved change", ready)
	}
	drifted := retireWithProbeInterleave(t, f, func() {
		req := f.recordRequest(f.sourceTree)
		req.BoundedJob = "successor job recorded mid-evaluation"
		req.Now = time.Unix(60, 0).UTC()
		req.ArtifactRefs = []string{f.artifactRef(t, f.sourceTree)}
		if _, err := runRecordProjectHandoffTx(f, req); err != nil {
			t.Fatal(err)
		}
	})
	if drifted.State != RetirementPending || !drifted.RecordedHandoff {
		t.Fatalf("retirement=%+v, want the interleaved successor handoff to refuse readiness on the stale snapshot", drifted)
	}
	found := false
	for _, blocker := range drifted.Blockers {
		if strings.Contains(blocker, "newer addressed handoff superseded the evaluated one") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers=%v, want the supersession-drift blocker", drifted.Blockers)
	}
	var newestID string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT handoff_id FROM project_handoffs WHERE work_id=? AND source_session_ref=? AND contract_version=1 ORDER BY `+projectHandoffEventOrder+` LIMIT 1`, f.workID, f.sourceSession).Scan(&newestID); err != nil {
		t.Fatal(err)
	}
	if newestID == first.HandoffID {
		t.Fatalf("newest handoff=%q, want the interleaved successor to own the frontier", newestID)
	}
}

func TestRetirementDriftRefusesReadinessOnSourceClaimChangeBetweenProbeAndFinalRead(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recordHandoff(t, f, f.sourceTree)
	destination := projectMainCheckout(t, f.sourceTree)
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	vacateAndLandSession(t, f, f.sourceSession, f.sourceTree, destination)
	ready, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != RetirementReady {
		t.Fatalf("retirement=%+v, want readiness before any interleaved change", ready)
	}
	version := fixtureWorkVersion(t, f.store, f.workID)
	drifted := retireWithProbeInterleave(t, f, func() {
		if _, err := f.store.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
			WorkID: f.workID, ProjectID: f.sourceProject, DefaultRef: "origin/main", PrincipalRef: "principal-1",
			RequestID: "ph-reclaim-drift", ExpectedVersion: version, Now: time.Unix(62, 0).UTC(), Runner: ExecGitRunner{},
		}); err != nil {
			t.Fatal(err)
		}
	})
	if drifted.State != RetirementPending || !drifted.VacateLanded || !drifted.WorkersStopped {
		t.Fatalf("retirement=%+v, want the interleaved source-claim change to refuse readiness with the other facts standing", drifted)
	}
	found := false
	for _, blocker := range drifted.Blockers {
		if strings.Contains(blocker, "claimed source worktree changed while the retirement was evaluated") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers=%v, want the source-claim drift blocker", drifted.Blockers)
	}
}

// TestConsumeResolvesTheAddressedHandoffWithoutAnIdentity pins the
// addressed-resolution consume: an empty handoff id binds the newest
// unconsumed handoff addressed to the consumer's own Project, from recorded
// state, never from caller-supplied identity.
func TestConsumeResolvesTheAddressedHandoffWithoutAnIdentity(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	// The addressed resolution never names the receiver's identity, and the
	// bind still requires the verified placement the real landing records.
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
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

// The receiving session's bind requires the verified placement a real claim
// landing records: an unplaced consume refuses inside the core transaction
// and records nothing, and the landing reopens the bind.
func TestProjectHandoffConsumeRefusesUnplacedReceiver(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	_, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(30, 0).UTC(),
	})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "no verified placement") {
		t.Fatalf("err=%v, want the unplaced-receiver refusal", err)
	}
	var state string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT state FROM project_handoffs WHERE handoff_id=?`, recorded.HandoffID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != ProjectHandoffRecorded {
		t.Fatalf("state=%q, want the refused consume to record nothing", state)
	}
	consumeHandoff(t, f, recorded.HandoffID)
}

// TestConsumeRefusesClaimOccupancyWithoutTheVerifiedLanding pins the
// receiver-admission boundary: claim admission records the session's
// occupancy row but no verified landing, so a consume bound on claim
// occupancy alone would authorize an unlanded session. The admission gate
// refuses the same session, and the real landing reopens the bind.
func TestConsumeRefusesClaimOccupancyWithoutTheVerifiedLanding(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	work := "work-review-placement"
	if err := ApplyOperation(ctx, f.store, Operation{Events: []Event{
		workCreatedEvent(work, "review-create"),
		operationEvent("review-member-source", "work_project.added", SubjectWorkItem, work, map[string]any{"work_id": work, "project_id": f.sourceProject, "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2}),
		operationEvent("review-member-target", "work_project.added", SubjectWorkItem, work, map[string]any{"work_id": work, "project_id": f.targetProject, "role": "secondary", "reason": "fixture", "expected_version": 2, "resulting_version": 3}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, work): 0}}); err != nil {
		t.Fatal(err)
	}
	seedProjectHandoffContract(t, f.store, work, 3)
	// The source Project claims its own worktree on THIS work: the record
	// binds artifact preservation to the named work's claimed source, so a
	// claim on a different work cannot stand in for it.
	reviewSourceVersion := fixtureWorkVersion(t, f.store, work)
	reviewSourceBase, err := (ExecGitRunner{}).Run(ctx, f.sourceTree, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	reviewSourceClaim, err := f.store.ClaimWorktree(ctx, WorktreeClaimRequest{OpID: "review-source-claim", WorkID: work, ProjectID: f.sourceProject, BaseSHA: strings.TrimSpace(string(reviewSourceBase)), PrincipalRef: "principal-1", RequestID: "review-source-claim", ExpectedVersion: reviewSourceVersion, Now: time.Unix(49, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	reviewSourceTree := reviewSourceClaim.Entry.Path
	base, err := (ExecGitRunner{}).Run(ctx, f.targetTree, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := f.store.ClaimWorktree(ctx, WorktreeClaimRequest{OpID: "review-claim", WorkID: work, ProjectID: f.targetProject, BaseSHA: strings.TrimSpace(string(base)), PrincipalRef: "principal-2", RequestID: "review-claim", SessionRef: f.targetSession, HostPID: os.Getpid(), ExpectedVersion: fixtureWorkVersion(t, f.store, work), Now: time.Unix(50, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	var landings int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id=?`, work).Scan(&landings); err != nil {
		t.Fatal(err)
	}
	if landings != 0 {
		t.Fatal("unexpected placement evidence")
	}
	req := f.recordRequest(reviewSourceTree)
	req.WorkID = work
	recorded, err := runRecordProjectHandoffTx(f, req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{WorkID: work, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(60, 0).UTC()})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "no verified placement") {
		t.Fatalf("err=%v, want the claim-occupancy consume to refuse without the verified landing", err)
	}
	if err := runHandoffGate(t, f.store, work, f.targetSession); failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "no verified placement") {
		t.Fatalf("gate err=%v, want claim occupancy alone to fail admission closed", err)
	}
	// The real verified claim landing records this session's occupancy on
	// the claimed worktree, and the bind then lands.
	bindSessionToProjectWorktree(t, f.store, claim.Entry.Path, f.targetSession)
	if _, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{WorkID: work, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(61, 0).UTC()}); err != nil {
		t.Fatalf("post-landing consume refused: %v", err)
	}
}

// A consumed frontier admits only its own consumer: another receiving
// session of the same Project refuses until a fresh addressed handoff names
// it, and consuming that fresh handoff moves the admission to its consumer.
func TestManagedExecutionGateAdmitsOnlyTheConsumingSession(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	bindSessionToProjectWorktree(t, f.store, f.targetTree, "session/target-b")
	first := recordHandoff(t, f, f.sourceTree)
	err := runHandoffGate(t, f.store, f.workID, "session/target-b")
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed") {
		t.Fatalf("err=%v, want the unconsumed refusal for the second receiving session", err)
	}
	consumeHandoff(t, f, first.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("post-consume gate refused the consuming session %v", err)
	}
	err = runHandoffGate(t, f.store, f.workID, "session/target-b")
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "was consumed by receiving session "+f.targetSession) {
		t.Fatalf("err=%v, want the foreign-bind refusal naming the consuming session", err)
	}
	// A fresh addressed handoff reopens admission for exactly its consumer.
	req := f.recordRequest(f.sourceTree)
	req.BoundedJob = "successor job"
	req.Now = time.Unix(40, 0).UTC()
	second, err := runRecordProjectHandoffTx(f, req)
	if err != nil {
		t.Fatal(err)
	}
	consumeHandoffAs(t, f, second.HandoffID, "session/target-b")
	if err := runHandoffGate(t, f.store, f.workID, "session/target-b"); err != nil {
		t.Fatalf("post-consume gate refused the fresh consumer %v", err)
	}
	err = runHandoffGate(t, f.store, f.workID, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "was consumed by receiving session session/target-b") {
		t.Fatalf("err=%v, want the first consumer refused at the fresh frontier", err)
	}
}

// A landing recorded for a superseded claim never authorizes the new claim
// generation. After the reclaim plus re-claim of the same derived worktree,
// the session's earlier landing proves nothing about the current claim, so
// the admission gate refuses closed until the re-claimed worktree records
// its own verified landing.
func TestAdmissionGateRefusesSupersededClaimLanding(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	recordHandoff(t, f, f.sourceTree)
	if err := runHandoffGate(t, f.store, f.workID, f.sourceSession); err != nil {
		t.Fatalf("source session gate refused before the reclaim %v", err)
	}
	// Capture the claim base before the reclaim retires the claimed
	// worktree, then supersede the claim generation: reclaim plus a fresh
	// claim at the same derived path, the fresh claim carrying the session's
	// occupancy row from creation (CD-0179).
	mainCheckout := projectMainCheckout(t, f.sourceTree)
	baseOut, err := (ExecGitRunner{}).Run(ctx, mainCheckout, "rev-parse", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	version := fixtureWorkVersion(t, f.store, f.workID)
	if _, err := f.store.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: f.workID, ProjectID: f.sourceProject, DefaultRef: "origin/main", PrincipalRef: "principal-1",
		RequestID: "ph-reclaim-w", ExpectedVersion: version, Now: time.Unix(45, 0).UTC(), Runner: ExecGitRunner{},
		ReleaseOccupancy: true, OperatorApprovalRef: "approval:superseded-claim-test",
	}); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := f.store.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "ph-op-w-2", WorkID: f.workID, ProjectID: f.sourceProject,
		BaseSHA: strings.TrimSpace(string(baseOut)), PrincipalRef: "principal-1", RequestID: "ph-req-w-2",
		SessionRef: f.sourceSession, ExpectedVersion: version + 1, Now: time.Unix(46, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Entry.Path != f.sourceTree {
		t.Fatalf("re-claimed path=%s, want the derived path %s", reclaimed.Entry.Path, f.sourceTree)
	}
	// The only landing in the log names the superseded claim: the gate
	// refuses, and the re-claim records no landing of its own.
	err = runHandoffGate(t, f.store, f.workID, f.sourceSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "no verified placement") {
		t.Fatalf("gate err=%v, want the superseded-claim landing refused", err)
	}
	var landings int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id=?`, f.workID).Scan(&landings); err != nil {
		t.Fatal(err)
	}
	if landings != 1 {
		t.Fatalf("landing event count=%d, want only the superseded claim's landing", landings)
	}
	// The re-claimed worktree's own verified landing names the new claim
	// generation and reopens admission.
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	var rawPayload string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id=? ORDER BY seq DESC LIMIT 1`, f.workID).Scan(&rawPayload); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		ClaimOpID string `json:"claim_op_id"`
	}
	if err := json.Unmarshal([]byte(rawPayload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ClaimOpID != reclaimed.Entry.ClaimOpID {
		t.Fatalf("newest landing claim op=%q, want the re-claimed generation %q", payload.ClaimOpID, reclaimed.Entry.ClaimOpID)
	}
	if err := runHandoffGate(t, f.store, f.workID, f.sourceSession); err != nil {
		t.Fatalf("post-landing gate refused %v", err)
	}
}

// A claim landing after a vacate plus re-claim records its own first
// landing. The vacate released every row the session held, so the re-claimed
// worktree's landing finds one held row, no occupied source, and only the
// vacated claim's landing in the log: replaying that landing instead would
// leave the new claim's placement unprovable and the addressed consume
// unverifiable.
func TestReclaimedClaimAfterVacateRecordsItsOwnFirstLanding(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	// The session vacates to the Project's registered main checkout: the
	// verified vacate landing releases its occupancy rows, so it holds no
	// row anywhere.
	destination := projectMainCheckout(t, f.targetTree)
	request, err := json.Marshal(sessionVacatedPayload{WorkID: f.workID, ProjectID: f.targetProject, SessionRef: f.targetSession, SourceDirectory: filepath.Clean(f.targetTree), DestinationDirectory: filepath.Clean(destination)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, f.store, Operation{Events: []Event{{
		EventID: "ph-vacate-target", Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: f.targetSession, OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: 2, Payload: request,
	}}}); err != nil {
		t.Fatal(err)
	}
	vacateLanding, err := f.store.RecordSessionVacateLanding(ctx, SessionVacateLandingRequest{WorkID: f.workID, SessionRef: f.targetSession, LandedDirectory: filepath.Clean(destination), HostPID: os.Getpid(), Now: time.Unix(51, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if vacateLanding.AlreadyRecorded || len(vacateLanding.ReleasedSources) == 0 {
		t.Fatalf("vacate landing=%+v, want the target row released", vacateLanding)
	}
	// The session re-claims the target Project's worktree and lands again.
	baseOut, err := (ExecGitRunner{}).Run(ctx, destination, "rev-parse", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	version := fixtureWorkVersion(t, f.store, f.workID)
	if _, err := f.store.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: f.workID, ProjectID: f.targetProject, DefaultRef: "origin/main", PrincipalRef: "principal-2",
		RequestID: "ph-reclaim-e", ExpectedVersion: version, Now: time.Unix(52, 0).UTC(), Runner: ExecGitRunner{},
	}); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := f.store.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "ph-op-e-2", WorkID: f.workID, ProjectID: f.targetProject,
		BaseSHA: strings.TrimSpace(string(baseOut)), PrincipalRef: "principal-2", RequestID: "ph-req-e-2",
		SessionRef: f.targetSession, ExpectedVersion: version + 1, Now: time.Unix(52, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: f.workID, SessionRef: f.targetSession, LandedDirectory: reclaimed.Entry.Path, HostPID: os.Getpid(), Now: time.Unix(53, 0).UTC()})
	if err != nil || second.AlreadyRecorded {
		t.Fatalf("second landing=%+v err=%v, want the re-claim's own first landing recorded", second, err)
	}
	var landings int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id=?`, f.workID).Scan(&landings); err != nil {
		t.Fatal(err)
	}
	if landings != 2 {
		t.Fatalf("landing event count=%d, want the vacated claim's and the re-claim's own landing", landings)
	}
	var rawSecond string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id=? ORDER BY seq DESC LIMIT 1`, f.workID).Scan(&rawSecond); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		ClaimOpID string `json:"claim_op_id"`
	}
	if err := json.Unmarshal([]byte(rawSecond), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ClaimOpID != reclaimed.Entry.ClaimOpID {
		t.Fatalf("newest landing claim op=%q, want the re-claimed generation %q", payload.ClaimOpID, reclaimed.Entry.ClaimOpID)
	}
	// The fresh landing resolves the placement: the addressed handoff binds
	// to the consuming session, and the admission gate admits it.
	recorded := recordHandoff(t, f, f.sourceTree)
	if _, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(60, 0).UTC()}); err != nil {
		t.Fatalf("post-reclaim consume refused: %v", err)
	}
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("post-reclaim gate refused %v", err)
	}
}

// Every handoff selection read — the boot frontier, the admission gate, the
// addressed consume resolution, the continuity read, and the retirement
// facts — orders on the recorded event's durable sequence. The pathological
// pair below pins it: the older handoff's whole-second RFC3339Nano stamp
// sorts after the later handoff's fractional stamp as text, so any
// recorded_at text ordering would render the older job as the newest.
func TestProjectHandoffSelectionsOrderByRecordedEventOrder(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	first := f.recordRequest(f.sourceTree)
	first.Now = time.Unix(40, 0).UTC()
	first.ArtifactRefs = []string{"sha256:" + strings.Repeat("a", 64) + ":/first-artifact.txt"}
	firstRec, err := runRecordProjectHandoffTx(f, first)
	if err != nil {
		t.Fatal(err)
	}
	second := f.recordRequest(f.sourceTree)
	second.BoundedJob = "the later recorded repository job"
	second.ArtifactRefs = []string{"sha256:" + strings.Repeat("b", 64) + ":/second-artifact.txt"}
	second.Now = time.Unix(40, 1).UTC()
	secondRec, err := runRecordProjectHandoffTx(f, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstRec.HandoffID == secondRec.HandoffID {
		t.Fatalf("distinct requests resolved one id %s", firstRec.HandoffID)
	}
	// The boot frontier renders the later-recorded handoff.
	rendered, err := storeReadPendingForProject(f, f.targetProject, f.targetSession)
	if err != nil {
		t.Fatal(err)
	}
	if rendered == nil || rendered.HandoffID != secondRec.HandoffID {
		t.Fatalf("boot handoff=%+v, want the later-recorded %s, not the text-larger %s", rendered, secondRec.HandoffID, firstRec.HandoffID)
	}
	// The admission gate's frontier is the later-recorded handoff: the
	// placed receiver refuses until it consumes.
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	gateErr := runHandoffGate(t, f.store, f.workID, f.targetSession)
	if failureKind(gateErr) != KindInvalidOperation || !strings.Contains(fmt.Sprint(gateErr), "stands unconsumed") {
		t.Fatalf("gate err=%v, want the unconsumed frontier refusal", gateErr)
	}
	// The retirement facts read the later-recorded handoff's artifacts.
	var facts projectRetirementHandoffFacts
	if err := f.store.Transact(ctx, func(transaction *Transaction) error {
		return readRetirementHandoffFactsTx(ctx, transaction, f.workID, f.sourceProject, f.sourceSession, &facts)
	}); err != nil {
		t.Fatal(err)
	}
	if !facts.recorded || len(facts.artifactRefs) != 1 || facts.artifactRefs[0] != second.ArtifactRefs[0] {
		t.Fatalf("retirement facts=%+v, want the later-recorded handoff's artifact refs", facts)
	}
	// The continuity read names the later-recorded handoff.
	var pending *ProjectHandoff
	if err := f.store.Transact(ctx, func(transaction *Transaction) error {
		tx, txErr := transactionSQL(transaction, "continuity_test")
		if txErr != nil {
			return txErr
		}
		pending, txErr = PendingProjectHandoffTx(ctx, tx, f.workID)
		return txErr
	}); err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.HandoffID != secondRec.HandoffID {
		t.Fatalf("continuity handoff=%+v, want the later-recorded %s", pending, secondRec.HandoffID)
	}
	// The addressed consume resolution binds the later-recorded handoff,
	// and the gate then admits the consuming receiver.
	consumed, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{WorkID: f.workID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(60, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if consumed.AlreadyConsumed || consumed.HandoffID != secondRec.HandoffID {
		t.Fatalf("consume=%+v, want the later-recorded %s bound", consumed, secondRec.HandoffID)
	}
	if err := runHandoffGate(t, f.store, f.workID, f.targetSession); err != nil {
		t.Fatalf("post-consume gate refused %v", err)
	}
}

// TestRecordProjectHandoffBindsPreservationToTheClaimedSourceWorktree pins
// the record-side bind: the probed path must BE the shared work's claimed
// source worktree, and once no active claim stands there is nothing to bind
// preservation to, so the record refuses closed.
func TestRecordProjectHandoffBindsPreservationToTheClaimedSourceWorktree(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	// A clean unrelated directory is not the work's claimed source worktree:
	// the record refuses even though a probe of that tree would pass.
	unrelated := t.TempDir()
	_, err := runRecordProjectHandoffTx(f, f.recordRequest(unrelated))
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "claimed source worktree") {
		t.Fatalf("err=%v, want the claimed-source bind refusal", err)
	}
	// The claimed source itself records.
	if _, err := runRecordProjectHandoffTx(f, f.recordRequest(f.sourceTree)); err != nil {
		t.Fatalf("claimed-source record failed %v", err)
	}
	// Retiring the claim leaves no active source claim: the record refuses
	// instead of trusting the supplied path.
	version := fixtureWorkVersion(t, f.store, f.workID)
	if _, err := f.store.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{
		WorkID: f.workID, ProjectID: f.sourceProject, DefaultRef: "origin/main", PrincipalRef: "principal-1",
		RequestID: "ph-bind-reclaim", ExpectedVersion: version, Now: time.Unix(50, 0).UTC(), Runner: ExecGitRunner{},
		ReleaseOccupancy: true, OperatorApprovalRef: "approval:bind-reclaim",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = runRecordProjectHandoffTx(f, f.recordRequest(f.sourceTree))
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "holds no claimed worktree") {
		t.Fatalf("err=%v, want the no-claim refusal", err)
	}
}

// TestProjectHandoffIdentityStaysWithinThePublishedIDBound records a
// handoff whose work, Project, and session identities all carry the
// published tool-surface id bound (128 characters) and pins the generated
// handoff id inside the same bound and pattern, with deterministic replay
// resolving the same id.
func TestProjectHandoffIdentityStaysWithinThePublishedIDBound(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	longWork := "work-" + strings.Repeat("w", 118) // 128-char derived branch bound: "work/"+id
	longSource := "project-" + strings.Repeat("s", 119)
	longTarget := "project-" + strings.Repeat("t", 119)
	repo := t.TempDir()
	gitRunStore(t, repo, "init", "-b", "main")
	gitRunStore(t, repo, "config", "user.email", "concord@example.invalid")
	gitRunStore(t, repo, "config", "user.name", "Concord Handoff Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "add", "tracked.txt")
	gitRunStore(t, repo, "commit", "-m", "base")
	seedRealGitOrigin(t, repo)
	normalized, err := NormalizeProjectLocator(LocatorCanonicalPath, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		locatorProductEvent("product-id"),
		locatorProjectEvent(longSource),
		locatorProjectEvent(longTarget),
		operationEvent("ph-id-member-s", "product_project.added", SubjectProduct, "product-id", map[string]any{"product_id": "product-id", "project_id": longSource, "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2}),
		operationEvent("ph-id-member-t", "product_project.added", SubjectProduct, "product-id", map[string]any{"product_id": "product-id", "project_id": longTarget, "role": "secondary", "reason": "fixture", "expected_version": 2, "resulting_version": 3}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-id"): 0, VersionRef(SubjectProject, longSource): 0, VersionRef(SubjectProject, longTarget): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		workCreatedEvent(longWork, "ph-id-create"),
		operationEvent("ph-id-work-member-s", "work_project.added", SubjectWorkItem, longWork, map[string]any{"work_id": longWork, "project_id": longSource, "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2}),
		operationEvent("ph-id-work-member-t", "work_project.added", SubjectWorkItem, longWork, map[string]any{"work_id": longWork, "project_id": longTarget, "role": "secondary", "reason": "fixture", "expected_version": 2, "resulting_version": 3}),
		operationEvent("ph-id-locator-s", "project.locator_added", SubjectProject, longSource, map[string]any{"project_id": longSource, "locator_id": "path-id", "kind": string(LocatorCanonicalPath), "value": repo, "normalized_value": normalized, "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, longWork): 0, VersionRef(SubjectProject, longSource): 1}}); err != nil {
		t.Fatal(err)
	}
	seedProjectHandoffLaw(t, s, "product-id", longSource)
	seedProjectHandoffContract(t, s, longWork, fixtureWorkVersion(t, s, longWork))
	baseOut, err := ExecGitRunner{}.Run(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "ph-id-claim", WorkID: longWork, ProjectID: longSource,
		BaseSHA: strings.TrimSpace(string(baseOut)), PrincipalRef: "principal-1", RequestID: "ph-id-req",
		ExpectedVersion: fixtureWorkVersion(t, s, longWork), Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := RecordProjectHandoffRequest{
		WorkID: longWork, SourceProjectID: longSource, TargetProjectID: longTarget,
		SourceSessionRef: "session/" + strings.Repeat("n", 120),
		BoundedJob:       "verify the receiving repository's adapter surface",
		Changes:          []string{"adapter/opencode: opener route"}, Verification: []string{"bun test adapter/opencode/ pass"},
		ArtifactRefs: nil, Blockers: []string{}, NextAction: "consume the handoff and verify the opener route",
		SourceWorktree: claim.Entry.Path, Now: time.Unix(20, 0).UTC(),
	}
	var first ProjectHandoffResult
	if err := s.Transact(ctx, func(tx *Transaction) error {
		var txErr error
		first, txErr = RecordProjectHandoffTx(ctx, tx, req)
		return txErr
	}); err != nil {
		t.Fatal(err)
	}
	if len(first.HandoffID) > 128 {
		t.Fatalf("handoff id length=%d, want within the published 128-character id bound", len(first.HandoffID))
	}
	if matched, _ := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`, first.HandoffID); !matched {
		t.Fatalf("handoff id %q violates the published id pattern", first.HandoffID)
	}
	replayReq := req
	replayReq.Now = time.Unix(21, 0).UTC()
	var replay ProjectHandoffResult
	if err := s.Transact(ctx, func(tx *Transaction) error {
		var txErr error
		replay, txErr = RecordProjectHandoffTx(ctx, tx, replayReq)
		return txErr
	}); err != nil {
		t.Fatal(err)
	}
	if replay.HandoffID != first.HandoffID || !replay.AlreadyRecorded {
		t.Fatalf("replay=%+v first=%+v, want the same id resolved from state", replay, first)
	}
}
