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
// worktree, one active workflow contract, and one coordinator
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
	sourceRepo    string
	// finalWorkVersion is the shared work item's version once the fixture
	// finishes: 3 after the work op and 4 contract-seed events.
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
	treeW := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w", workID)
	treeE := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-e", workID)
	for _, tree := range []string{treeW, treeE} {
		if err := os.MkdirAll(filepath.Dir(tree), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitRunStore(t, repoW, "worktree", "add", treeW, "-b", "work/"+workID, "HEAD")
	gitRunStore(t, repoE, "worktree", "add", treeE, "-b", "work/"+workID, "HEAD")
	return projectHandoffFixture{
		store: s, workID: workID,
		sourceProject: "project-w", targetProject: "project-e",
		sourceSession: "session/source", targetSession: "session/target",
		sourceTree: treeW, targetTree: treeE, sourceRepo: repoW,
		finalWorkVersion: 7,
	}
}

// seedProjectHandoffLaw seeds the synthetic knowledge home the contract
// folds resolve law context against, keyed to the fixture's Project. The
// locator names its own directory, so the canonical-path unique index keeps
// the Project's claim locator and this law locator distinct.
func seedProjectHandoffLaw(t *testing.T, s *Store, productID, projectID string) {
	t.Helper()
	locatorPath := initKnowledgeRepo(t)
	rootDomainID := "product-root:" + productID
	writeKnowledgeFile(t, locatorPath, ".concord/docs/knowledge/domain-registry.json", fmt.Sprintf(`{"schema_version":"1.0","product_key":%q,"root_domain_id":%q,"domains":[{"domain_id":%q,"name":"Root","purpose":"Synthetic law","status":"current","architecture_relations":[]}]}`+"\n", productID, rootDomainID, rootDomainID))
	writeKnowledgeFile(t, locatorPath, ".concord/docs/knowledge/manifest.json", "{\"schema_version\":\"1.2\",\"supported_kinds\":[\"decision\",\"spec\"],\"indexed_kinds\":[\"decision\",\"spec\"]}\n")
	writeKnowledgeFile(t, locatorPath, ".concord/docs/spec.md", "# Synthetic test law\n")
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
	if _, err := s.DatabaseForTesting().Exec(fmt.Sprintf(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('%s','%s','%s'); DELETE FROM write_guard`, productID, projectID, "ph-law-locator")); err != nil {
		t.Fatal(err)
	}
	home := KnowledgeHome{HomeProjectID: projectID, HomeLocatorID: "ph-law-locator", RepoPath: locatorPath, HeadRef: "HEAD"}
	writeFixtureSpecLawSource(t, home, "spec:one")
}

// seedProjectHandoffContract initializes the shared work item's research
// workflow before recording its active contract.
func seedProjectHandoffContract(t *testing.T, s *Store, workID string, workVersion int64) {
	t.Helper()
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/source", ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := BuiltinWorkflowRegistry().Current("workflow.research")
	if !ok {
		t.Fatal("workflow.research v5 is not registered")
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: owner, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}
	setup := []Event{
		workflowEventWithActor("ph-contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": workVersion + 2, "resulting_version": workVersion + 3, "contract_version": 1, "premise": "cross-project handoff fixture", "decision_domains": []string{}, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}, "outcome_predicates": []map[string]any{{"predicate_id": "predicate:handoff-fixture-exit", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}}, "required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowActionCompletedFixture("ph-approval-"+workID, workID, ownerRef, workVersion+3, "frame", "approve_contract"),
	}
	setup[0].PayloadVersion = eventKindRegistry[WorkflowContractApproved].CurrentVersion
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): workVersion + 2}}); err != nil {
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
		"contract_version": int64(2), "premise": "cross-project handoff fixture v2", "decision_domains": []string{}, "outcome_kind": "outcome",
		"outcome_payload":   map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	}
	supersede := workflowEventWithActor("ph-contract-2-"+workID, WorkflowContractSuperseded, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": workVersion, "resulting_version": workVersion + 1, "previous_contract_version": int64(1), "new_contract_version": int64(2), "supersede_reason": "fixture contract replacement", "audit_evidence": []string{"audit:handoff-fixture"}, "successor_contract": successor,
	})
	supersede.PayloadVersion = eventKindRegistry[WorkflowContractSuperseded].CurrentVersion
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{supersede}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): workVersion}}); err != nil {
		t.Fatal(err)
	}
}

func (f projectHandoffFixture) artifactRef(t *testing.T, tree string) string {
	t.Helper()
	return fmt.Sprintf("sha256:%s:%s", artifactDigest(t, filepath.Join(tree, "tracked.txt")), filepath.Join(tree, "tracked.txt"))
}

func (f projectHandoffFixture) recordRequest() RecordProjectHandoffRequest {
	return RecordProjectHandoffRequest{
		WorkID: f.workID, SourceProjectID: f.sourceProject, TargetProjectID: f.targetProject,
		SourceSessionRef: f.sourceSession, BoundedJob: "verify the receiving repository's adapter surface",
		Changes:      []string{"adapter/opencode: opener route"},
		Verification: []string{"bun test adapter/opencode/ pass"},
		ArtifactRefs: nil,
		Blockers:     []string{}, NextAction: "consume the handoff and verify the opener route",
		Now: time.Unix(20, 0).UTC(),
	}
}

func recordHandoff(t *testing.T, f projectHandoffFixture, tree string) ProjectHandoffResult {
	t.Helper()
	req := f.recordRequest()
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

// consumeHandoffAs binds the handoff for the receiving Project.
func consumeHandoffAs(t *testing.T, f projectHandoffFixture, handoffID, sessionRef string) ProjectHandoffConsumptionResult {
	t.Helper()
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

func TestProjectHandoffConsumeAdmissionAndRetirementWithoutOwnershipRows(t *testing.T) {
	f := setupProjectHandoffFixture(t)
	assertWorktreeTablesAbsent(t, f.store.db)
	recorded := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, recorded.HandoffID)
	if err := f.store.Transact(t.Context(), func(tx *Transaction) error {
		return RefuseUnconsumedProjectHandoffTx(t.Context(), tx.tx, f.workID, f.targetProject, f.targetSession)
	}); err != nil {
		t.Fatalf("admission after consume: %v", err)
	}
	retirement, err := EvaluateProjectSessionRetirement(t.Context(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil || retirement.State != RetirementReady || !retirement.ArtifactsPreserved {
		t.Fatalf("retirement=%+v err=%v, want ready with preserved artifacts", retirement, err)
	}
	assertWorktreeTablesAbsent(t, f.store.db)
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
		req := f.recordRequest()
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
		req := f.recordRequest()
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

// CD-0182 D5: every session of the receiving Project reads the frontier's
// bounded job, before and after the consume. The continuity read selects the
// frontier by the reading session's Project; a session of another Project
// reads only the work's newest unconsumed handoff, and a contract
// replacement drops the stale frontier.
func TestContinuityRendersTheAddressedFrontierToTheReceivingProject(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	recorded := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, recorded.HandoffID)
	read := func(projectID string) *ProjectHandoff {
		t.Helper()
		snapshot, err := ReadWorkflowContinuity(ctx, f.store, ContinuityRequest{Work: f.workID, ProjectID: projectID})
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.PendingProjectHandoff
	}
	handoff := read(f.targetProject)
	if handoff == nil || handoff.HandoffID != recorded.HandoffID || handoff.State != ProjectHandoffConsumed {
		t.Fatalf("handoff=%+v, want the consumed frontier for the receiving Project", handoff)
	}
	if handoff.BoundedJob == "" || handoff.NextAction == "" || len(handoff.Changes) == 0 || len(handoff.Verification) == 0 {
		t.Fatalf("handoff=%+v, want the full bounded job", handoff)
	}
	if other := read(f.sourceProject); other != nil {
		t.Fatalf("handoff=%+v, want no consumed frontier for the source Project", other)
	}
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	if stale := read(f.targetProject); stale != nil {
		t.Fatalf("handoff=%+v, want no frontier under a replaced contract", stale)
	}
}

func TestProjectHandoffRefusesDirtySourceWorktree(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	if err := os.WriteFile(filepath.Join(f.sourceTree, "tracked.txt"), []byte("mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := f.recordRequest()
	req.ArtifactRefs = []string{f.artifactRef(t, f.sourceTree)}
	// The probe runs before any mutation transaction, exactly as the agent
	// mutation plan runs it; the tx-scoped record never probes inside a
	// transaction.
	err := VerifyProjectHandoffArtifactPreservation(context.Background(), f.sourceTree, req.ArtifactRefs)
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
	err := VerifyProjectHandoffArtifactPreservation(context.Background(), f.sourceTree, []string{fmt.Sprintf("sha256:%s:%s", strings.Repeat("0", 64), filepath.Join(f.sourceTree, "tracked.txt"))})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "does not match the worktree content") {
		t.Fatalf("err=%v, want the digest mismatch refusal", err)
	}
	// An artifact outside the source worktree refuses without a probe.
	err = VerifyProjectHandoffArtifactPreservation(context.Background(), f.sourceTree, []string{fmt.Sprintf("sha256:%s:%s", artifactDigest(t, filepath.Join(f.targetTree, "tracked.txt")), filepath.Join(f.targetTree, "tracked.txt"))})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "outside the source worktree") {
		t.Fatalf("err=%v, want the outside-worktree refusal", err)
	}
}

func TestProjectHandoffRefusesNonMemberTargetAndSelfAddress(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	req := f.recordRequest()
	req.TargetProjectID = "project-nowhere"
	req.ArtifactRefs = []string{f.artifactRef(t, f.sourceTree)}
	_, err := runRecordProjectHandoffTx(f, req)
	if failureKind(err) != KindProjectionNotFound {
		t.Fatalf("err=%v, want the membership refusal", err)
	}
	self := f.recordRequest()
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
	// A consume by a session of another Project of an already-consumed
	// handoff refuses on the addressed target; the shared bind of the
	// receiving Project's own sessions is TestProjectHandoffConsumeResolves
	// TheSharedBindForEverySessionOfTheProject's subject.
	consumeHandoff(t, f, recorded.HandoffID)
	_, err = runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.sourceProject, ConsumerSessionRef: f.sourceSession, Now: time.Unix(31, 0).UTC(),
	})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "addresses Project") {
		t.Fatalf("err=%v, want the wrong-Project refusal on the consumed bind", err)
	}
}

// CD-0182 D5 amendment: the consume binds the record to the work-and-Project
// pair, so a second session of the receiving Project resolves the
// standing bind as AlreadyConsumed instead of a refusal that prescribes a
// fresh addressed handoff for it, and a contract replacement refuses the
// stale bind equally for both sessions of the Project.
func TestProjectHandoffConsumeResolvesTheSharedBindForEverySessionOfTheProject(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, recorded.HandoffID)
	// A second session of the receiving Project resolves the shared bind
	// with no second consumed event.
	replay, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: "session/target-b", Now: time.Unix(31, 0).UTC(),
	})
	if err != nil || !replay.AlreadyConsumed || replay.HandoffID != recorded.HandoffID {
		t.Fatalf("replay=%+v err=%v, want the shared bind resolved as AlreadyConsumed", replay, err)
	}
	// The shared-bind replay keeps the Project boundary: a session that
	// resolves no Project cannot ride a consumed bind another session
	// recorded.
	_, unresolved := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: "", ConsumerSessionRef: "session/unresolved", Now: time.Unix(31, 0).UTC(),
	})
	if failureKind(unresolved) != KindInvalidOperation || !strings.Contains(fmt.Sprint(unresolved), "addresses Project") {
		t.Fatalf("err=%v, want the unresolved-Project replay refused", unresolved)
	}
	var events int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='work.project_handoff_consumed' AND subject_id=?`, f.workID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("consumed events=%d, want the shared bind to record no second event", events)
	}
	// A contract replacement refuses the stale bind for the consuming
	// session and the later session of the Project alike.
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	for _, session := range []string{f.targetSession, "session/target-b"} {
		_, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
			WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: session, Now: time.Unix(32, 0).UTC(),
		})
		if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "recorded under contract version 1") || !strings.Contains(fmt.Sprint(err), "active contract is version 2") {
			t.Fatalf("session %s err=%v, want the equal stale-bind consume refusal", session, err)
		}
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

func runHandoffGate(t *testing.T, s *Store, workID, projectID, sessionRef string) error {
	t.Helper()
	return s.Transact(context.Background(), func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "gate")
		if err != nil {
			return err
		}
		return RefuseUnconsumedProjectHandoffTx(context.Background(), tx, workID, projectID, sessionRef)
	})
}

func TestManagedExecutionRefusesUntilHandoffConsumed(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	// Before any handoff, managed execution is unaffected.
	if err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession); err != nil {
		t.Fatalf("no handoff recorded yet: gate refused %v", err)
	}
	recorded := recordHandoff(t, f, f.sourceTree)
	// The source session keeps acting on its own handoff.
	if err := runHandoffGate(t, f.store, f.workID, f.sourceProject, f.sourceSession); err != nil {
		t.Fatalf("source session gate refused %v", err)
	}
	// A session of another Project is not gated by a handoff that does not
	// address its Project: the wrong-Project edge refuses the wrong target
	// at consume, and the admission gate never deadlocks the bystander.
	bystander := "session/bystander"
	if err := runHandoffGate(t, f.store, f.workID, f.sourceProject, bystander); err != nil {
		t.Fatalf("unaddressed bystander gate refused %v", err)
	}
	// The receiving session is refused until it consumes.
	err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed") {
		t.Fatalf("err=%v, want the unconsumed-handoff refusal", err)
	}
	consumeHandoff(t, f, recorded.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession); err != nil {
		t.Fatalf("post-consume gate refused %v", err)
	}
	// A contract replacement holds the gate closed on the consumed bind: a
	// bind authorizes execution only under the contract it was made under,
	// so the stale bind refuses closed and the fresh-handoff recovery is
	// the route back to admission.
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	err = runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "was consumed under contract version 1") || !strings.Contains(fmt.Sprint(err), "active contract is version 2") {
		t.Fatalf("err=%v, want the stale-bind gate refusal", err)
	}
	// The source-side recovery: a fresh addressed handoff under the active
	// contract supersedes the stale frontier, and consuming it reopens
	// admission for this session.
	req := f.recordRequest()
	req.BoundedJob = "fresh successor job"
	req.Now = time.Unix(40, 0).UTC()
	successor, recordErr := runRecordProjectHandoffTx(f, req)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	err = runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed") {
		t.Fatalf("err=%v, want the fresh frontier to hold the gate while unconsumed", err)
	}
	consumeHandoff(t, f, successor.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession); err != nil {
		t.Fatalf("post-recovery gate refused %v", err)
	}
}

// The consuming session's own replay is a bind check under the active
// contract, never a bypass: after a contract replacement the stale bind
// refuses closed, and the fresh successor handoff is the route back.
func TestProjectHandoffConsumeReplayRefusesAfterContractReplacement(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
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
	req := f.recordRequest()
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
	first := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, first.HandoffID)
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	req := f.recordRequest()
	req.BoundedJob = "fresh successor job"
	req.Now = time.Unix(40, 0).UTC()
	second, err := runRecordProjectHandoffTx(f, req)
	if err != nil {
		t.Fatal(err)
	}
	consumeHandoff(t, f, second.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession); err != nil {
		t.Fatalf("gate refused after the fresh successor handoff was consumed: %v", err)
	}
}

// An unconsumed handoff under a superseded contract holds the gate closed:
// the stale consume refuses, so only a fresh addressed handoff under the
// active contract, consumed by the receiving session, reopens admission.
func TestManagedExecutionGateHoldsOnStaleUnconsumedHandoff(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recordHandoff(t, f, f.sourceTree)
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed under contract version 1") {
		t.Fatalf("err=%v, want the stale-unconsumed gate refusal", err)
	}
	// A fresh handoff under the active contract supersedes the stale one:
	// the frontier is now the fresh handoff, and the gate still refuses
	// while it stands unconsumed.
	req := f.recordRequest()
	req.BoundedJob = "fresh successor job"
	req.Now = time.Unix(40, 0).UTC()
	second, recordErr := runRecordProjectHandoffTx(f, req)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	err = runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession)
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed") || strings.Contains(fmt.Sprint(err), "contract version 1") {
		t.Fatalf("err=%v, want the fresh frontier to hold the gate without the stale version message", err)
	}
	consumeHandoff(t, f, second.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession); err != nil {
		t.Fatalf("post-consume gate refused %v", err)
	}
}

// A session that resolves no member Project on a handoff-bearing work refuses
// at the admission boundary, and ordinary handoff-free work stays admitted
// whatever the caller's Project.
func TestManagedExecutionGateRefusesUnresolvedProjectOnHandoffBearingWork(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	// Handoff-free: a session with no Project is not this feature's subject,
	// and the gate leaves it alone.
	if err := runHandoffGate(t, f.store, f.workID, "", "session/unresolved"); err != nil {
		t.Fatalf("handoff-free gate refused %v", err)
	}
	recorded := recordHandoff(t, f, f.sourceTree)
	err := runHandoffGate(t, f.store, f.workID, "", "session/unresolved")
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "resolves no Project") {
		t.Fatalf("err=%v, want the unresolved-Project refusal", err)
	}
	// A Project that is not a member of the work refuses the same way:
	// admission cannot bind an addressed handoff to a Project outside it.
	err = runHandoffGate(t, f.store, f.workID, "project-outside", "session/outside")
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "not a member of the work") {
		t.Fatalf("err=%v, want the non-member Project refusal", err)
	}
	// The receiving session is gated only by the handoff addressed to its
	// own Project, and consuming it reopens admission.
	consumeHandoff(t, f, recorded.HandoffID)
	if err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession); err != nil {
		t.Fatalf("post-consume gate refused %v", err)
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
// source worktree refuses before any bytes are read: the immutable
// reference must live inside the source worktree.
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
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "resolves through a symlink outside the source worktree") {
		t.Fatalf("err=%v, want the symlink-escape refusal", err)
	}
}

func TestRetirementReadinessRequiresEveryVerifiedFact(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	// Nothing recorded yet: pending with the handoff blocker first.
	retirement, err := EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending || !strings.Contains(retirement.Blockers[0], "record the addressed Project handoff") {
		t.Fatalf("retirement=%+v, want the handoff blocker", retirement)
	}
	recordHandoff(t, f, f.sourceTree)
	retirement, err = EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementReady || !retirement.RecordedHandoff || !retirement.ArtifactsPreserved || len(retirement.Blockers) != 0 {
		t.Fatalf("retirement=%+v, want readiness without a Concord vacate request", retirement)
	}
	assertWorktreeTablesAbsent(t, f.store.db)
	raw, err := json.Marshal(retirement)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "vacate_landed") {
		t.Fatalf("retirement=%s, want no vacate landing projection", raw)
	}
	if err := os.WriteFile(filepath.Join(f.sourceTree, "tracked.txt"), []byte("unpreserved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	retirement, err = EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending || retirement.ArtifactsPreserved || !strings.Contains(strings.Join(retirement.Blockers, "; "), "unpreserved changes") {
		t.Fatalf("retirement=%+v, want unpreserved artifacts to block readiness", retirement)
	}
	if err := os.WriteFile(filepath.Join(f.sourceTree, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	retirement, err = EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementReady {
		t.Fatalf("retirement=%+v, want READY_TO_CLOSE_OR_REPLACE from the verified facts", retirement)
	}
	if !retirement.RecordedHandoff || !retirement.ArtifactsPreserved {
		t.Fatalf("retirement=%+v, want every fact verified", retirement)
	}
	// Retirement derives from facts; it never writes.
	var events int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, f.workID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	retirement, err = EvaluateProjectSessionRetirement(ctx, f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	var after int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, f.workID).Scan(&after); err != nil {
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
	retirement, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
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

func TestRetirementUnfinishedActionBlocksReadiness(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, recorded.HandoffID)
	// An unmatched action start with no known actor must block retirement.
	execInFold(t, f.store, `INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload)
		VALUES('work-h','retirement-test',1,900000000,'retirement-test-start','{}')`)
	retirement, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending {
		t.Fatalf("retirement=%+v, want unknown action attribution to block readiness", retirement)
	}
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: f.sourceSession, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	execInFold(t, f.store, fmt.Sprintf(`UPDATE workflow_step_epochs SET latest_start_payload=json_object('actor_ref','%s') WHERE work_id='work-h' AND step_id='retirement-test'`, actorRef))
	retirement, err = EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	if retirement.State != RetirementPending {
		t.Fatalf("retirement=%+v, want this session's unfinished action to block readiness", retirement)
	}
	foreign := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/foreign", ActorClass: ActorAgent}
	foreignRef, err := WorkflowActorRef(foreign)
	if err != nil {
		t.Fatal(err)
	}
	execInFold(t, f.store,
		fmt.Sprintf(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES('%s','principal/operator','client/concord-1','agent/coordinator','session/foreign','agent','2026-09-30T00:00:00Z')`, foreignRef),
		fmt.Sprintf(`UPDATE workflow_step_epochs SET latest_start_payload=json_object('actor_ref','%s') WHERE work_id='work-h' AND step_id='retirement-test'`, foreignRef))
	retirement, err = EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil || retirement.State != RetirementReady {
		t.Fatalf("retirement=%+v err=%v, want another session's action to leave readiness intact", retirement, err)
	}
	execInFold(t, f.store,
		fmt.Sprintf(`UPDATE workflow_step_epochs SET latest_start_payload=json_object('actor_ref','%s') WHERE work_id='work-h' AND step_id='retirement-test'`, actorRef),
		`INSERT INTO workflow_step_completions(work_id,step_id,completion_seq) VALUES('work-h','retirement-test',900000001)`)
	retirement, err = EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil || retirement.State != RetirementReady {
		t.Fatalf("retirement=%+v err=%v, want readiness after the action settles", retirement, err)
	}
}

// TestProjectHandoffLostResponseRecoveryReplaysTheReceiverBind pins the
// lost-response recovery boundary: when the consume commits but its response
// is lost, the replayed consume resolves as the standing bind with no second
// event, and the admission gate keeps the recovered receiver admitted.
func TestProjectHandoffLostResponseRecoveryReplaysTheReceiverBind(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	consumeHandoff(t, f, recorded.HandoffID)
	replay, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession, Now: time.Unix(31, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.AlreadyConsumed {
		t.Fatalf("replay=%+v, want the standing bind to replay as AlreadyConsumed", replay)
	}
	if err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession); err != nil {
		t.Fatalf("gate refused the recovered receiver %v", err)
	}
}

// Readiness derives only from a handoff the work's active contract binds.
// A contract replacement makes the recorded handoff unconsumable, so
// readiness drops back to pending with the stale contract named.
func TestRetirementReadinessRefusesStaleContractHandoff(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recordHandoff(t, f, f.sourceTree)
	ready, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != RetirementReady {
		t.Fatalf("retirement=%+v, want readiness from the verified facts before the contract replacement", ready)
	}
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	stale, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
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
	if !stale.ArtifactsPreserved {
		t.Fatalf("retirement=%+v, want the preserved and stopped facts to stand", stale)
	}
}

// The retirement derivation reads in two snapshots — the phase-one handoff
// facts, then the git preservation probe, then the final transaction —
// because the probe cannot run inside a SQL transaction. The final
// transaction revalidates the phase-one snapshot, and each test here
// interleaves one committed identity change into exactly that window
// through the store's deterministic probe seam: a contract replacement, a
// and a newer addressed handoff each leave the
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
	retirement, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	return retirement
}

func TestRetirementDriftRefusesReadinessOnContractReplacementBetweenProbeAndFinalRead(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recordHandoff(t, f, f.sourceTree)
	ready, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != RetirementReady {
		t.Fatalf("retirement=%+v, want readiness before any interleaved change", ready)
	}
	version := readWorkVersion(t, f.store, f.workID)
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
	ready, err := EvaluateProjectSessionRetirement(context.Background(), f.store, f.workID, f.sourceProject, f.sourceSession, f.sourceTree)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != RetirementReady {
		t.Fatalf("retirement=%+v, want readiness before any interleaved change", ready)
	}
	drifted := retireWithProbeInterleave(t, f, func() {
		req := f.recordRequest()
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

// TestConsumeResolvesTheAddressedHandoffWithoutAnIdentity pins the
// addressed-resolution consume: an empty handoff id binds the newest
// unconsumed handoff addressed to the consumer's own Project, from recorded
// state, never from caller-supplied identity.
func TestConsumeResolvesTheAddressedHandoffWithoutAnIdentity(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	// The addressed resolution never names the receiver's identity.
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

// The receiving bind requires the calling session's resolved Project to be
// the addressed one: an empty or foreign Project refuses inside the core
// transaction and records nothing, and the addressed receiving Project binds.
func TestProjectHandoffConsumeRefusesUnresolvedOrForeignProject(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	for _, project := range []string{"", "project-outside", f.sourceProject} {
		_, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
			WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: project, ConsumerSessionRef: "session/caller", Now: time.Unix(30, 0).UTC(),
		})
		if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "addresses Project "+f.targetProject) {
			t.Fatalf("project %q err=%v, want the addressed-Project refusal", project, err)
		}
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

// CD-0182 D5 amendment: the frontier bind names the work-and-Project pair,
// so a consumed frontier admits every coordinator session of the receiving
// Project — not only the session that
// recorded the consume — and a contract replacement refuses the stale bind
// equally for the consuming session and a later session of the Project.
// Under the superseded per-session bind the second session was refused with
// "was consumed by receiving session ... not this session", a remedy that
// prescribed a fresh addressed handoff and a new session.
func TestManagedExecutionGateAdmitsEverySessionOfTheReceivingProject(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	first := recordHandoff(t, f, f.sourceTree)
	// While the frontier stands unconsumed, both receiving sessions refuse.
	for _, session := range []string{f.targetSession, "session/target-b"} {
		err := runHandoffGate(t, f.store, f.workID, f.targetProject, session)
		if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "stands unconsumed") {
			t.Fatalf("session %s err=%v, want the unconsumed-frontier refusal", session, err)
		}
	}
	// One consume binds the frontier to the work-and-Project pair, and
	// every session of the receiving Project dispatches under it.
	consumeHandoff(t, f, first.HandoffID)
	for _, session := range []string{f.targetSession, "session/target-b"} {
		if err := runHandoffGate(t, f.store, f.workID, f.targetProject, session); err != nil {
			t.Fatalf("session %s gate refused under the shared bind: %v", session, err)
		}
	}
	// A session with no resolved Project stays refused: the gate cannot
	// bind an addressed handoff to a Project it cannot prove.
	err := runHandoffGate(t, f.store, f.workID, "", "session/unresolved")
	if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "resolves no Project") {
		t.Fatalf("err=%v, want the unresolved-Project refusal", err)
	}
	// A wrong-Project session stays refused at the consume boundary: the
	// frontier addresses the receiving Project, not the source one.
	_, wrongProject := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: first.HandoffID, ConsumerProjectID: f.sourceProject, ConsumerSessionRef: f.sourceSession, Now: time.Unix(34, 0).UTC(),
	})
	if failureKind(wrongProject) != KindInvalidOperation || !strings.Contains(fmt.Sprint(wrongProject), "addresses Project") {
		t.Fatalf("err=%v, want the wrong-Project consume refusal", wrongProject)
	}
	// A contract replacement refuses the stale bind equally for the
	// consuming session and the later session of the same Project.
	seedReplacementProjectHandoffContract(t, f.store, f.workID, f.finalWorkVersion)
	for _, session := range []string{f.targetSession, "session/target-b"} {
		err := runHandoffGate(t, f.store, f.workID, f.targetProject, session)
		if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "consumed under contract version 1") || !strings.Contains(fmt.Sprint(err), "active contract is version 2") {
			t.Fatalf("session %s err=%v, want the equal stale-bind refusal", session, err)
		}
		var failure *Failure
		if !failureAs(err, &failure) || !strings.Contains(failure.RecoveryAction, "source session") {
			t.Fatalf("session %s recovery=%v, want the source-side recovery that names no per-session handoff", session, failure.RecoveryAction)
		}
	}
	// The recovery stays the source-side fresh handoff under the active
	// contract: consuming it reopens admission for every session.
	req := f.recordRequest()
	req.BoundedJob = "successor job"
	req.Now = time.Unix(40, 0).UTC()
	second, recordErr := runRecordProjectHandoffTx(f, req)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	consumeHandoffAs(t, f, second.HandoffID, "session/target-b")
	for _, session := range []string{f.targetSession, "session/target-b"} {
		if err := runHandoffGate(t, f.store, f.workID, f.targetProject, session); err != nil {
			t.Fatalf("session %s gate refused under the fresh shared bind: %v", session, err)
		}
	}
}

// The widened bind never crosses Projects: a session whose directory resolves
// the source Project acquires no bind of the receiving Project — the consume
// refuses on the addressed Project — while the source Project's own
// execution, the Project that recorded the frontier, stays admitted whether
// the frontier stands unconsumed or consumed.
func TestManagedExecutionGateKeepsTheWrongProjectBindRefusedAndSourceExecutionAdmitted(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	recorded := recordHandoff(t, f, f.sourceTree)
	// The source Project's sessions stay admitted while the frontier
	// stands unconsumed: the handoff addresses the receiving Project.
	for _, session := range []string{f.sourceSession, "session/source-b"} {
		if err := runHandoffGate(t, f.store, f.workID, f.sourceProject, session); err != nil {
			t.Fatalf("source session %s gated by the receiving frontier: %v", session, err)
		}
	}
	// A session of the source Project cannot bind the frontier: the handoff
	// addresses the receiving Project, not the caller's.
	_, wrongProject := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
		WorkID: f.workID, HandoffID: recorded.HandoffID, ConsumerProjectID: f.sourceProject, ConsumerSessionRef: "session/source-b", Now: time.Unix(33, 0).UTC(),
	})
	if failureKind(wrongProject) != KindInvalidOperation || !strings.Contains(fmt.Sprint(wrongProject), "addresses Project "+f.targetProject) {
		t.Fatalf("err=%v, want the wrong-Project refusal on the bind", wrongProject)
	}
	// After the receiving Project consumes, the source sessions stay
	// admitted.
	consumeHandoff(t, f, recorded.HandoffID)
	for _, session := range []string{f.sourceSession, "session/source-b"} {
		if err := runHandoffGate(t, f.store, f.workID, f.sourceProject, session); err != nil {
			t.Fatalf("source session %s gated by the consumed frontier: %v", session, err)
		}
	}
}

// Every handoff selection read — the admission gate, the addressed consume
// resolution, the continuity read, and the retirement
// facts — orders on the recorded event's durable sequence. The pathological
// pair below pins it: the older handoff's whole-second RFC3339Nano stamp
// sorts after the later handoff's fractional stamp as text, so any
// recorded_at text ordering would render the older job as the newest.
func TestProjectHandoffSelectionsOrderByRecordedEventOrder(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	first := f.recordRequest()
	first.Now = time.Unix(40, 0).UTC()
	first.ArtifactRefs = []string{"sha256:" + strings.Repeat("a", 64) + ":/first-artifact.txt"}
	firstRec, err := runRecordProjectHandoffTx(f, first)
	if err != nil {
		t.Fatal(err)
	}
	second := f.recordRequest()
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
	// The admission gate's frontier is the later-recorded handoff: the
	// receiver refuses until it consumes.
	gateErr := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession)
	if failureKind(gateErr) != KindInvalidOperation || !strings.Contains(fmt.Sprint(gateErr), "stands unconsumed") {
		t.Fatalf("gate err=%v, want the unconsumed frontier refusal", gateErr)
	}
	// The retirement facts read the later-recorded handoff's artifacts.
	var facts projectRetirementHandoffFacts
	if err := f.store.Transact(ctx, func(transaction *Transaction) error {
		return readRetirementHandoffFactsTx(ctx, transaction.tx, f.workID, f.sourceProject, f.sourceSession, &facts)
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
	if err := runHandoffGate(t, f.store, f.workID, f.targetProject, f.targetSession); err != nil {
		t.Fatalf("post-consume gate refused %v", err)
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
	seedProjectHandoffContract(t, s, longWork, readWorkVersion(t, s, longWork))
	req := RecordProjectHandoffRequest{
		WorkID: longWork, SourceProjectID: longSource, TargetProjectID: longTarget,
		SourceSessionRef: "session/" + strings.Repeat("n", 120),
		BoundedJob:       "verify the receiving repository's adapter surface",
		Changes:          []string{"adapter/opencode: opener route"}, Verification: []string{"bun test adapter/opencode/ pass"},
		ArtifactRefs: nil, Blockers: []string{}, NextAction: "consume the handoff and verify the opener route",
		Now: time.Unix(20, 0).UTC(),
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
