package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Refine exit requires a passing unchanged-tree producer receipt covering the
// current integrated candidate. Stage chronology cannot stale unchanged proof.

func refineProofStartRefine(t *testing.T, fixture workflowReturnRouteFixture, workID string) {
	t.Helper()
	reviewGateStartStep(t, fixture.store, workID, "refine", "start_refine", fixture.owner)
}

// refineProofCurrentFixture seeds the refine-proof work on the family's
// current definition with verification, review, and artifact evidence bound.
func refineProofCurrentFixture(t *testing.T, workID, definitionRef string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, definitionRef), "refine", []string{"verification"}, []string{"verification", "review", "artifact"})
}

// refineProofAcceptReadyReview records Main's accepted ready Review of the
// claimed candidate through the real dispatch, terminal, and acceptance
// owners. Leaving execution needs it before any refine proof is read
// (CD-0242 D5).
func refineProofAcceptReadyReview(t *testing.T, fixture workflowReturnRouteFixture, workID string) {
	t.Helper()
	reviewSubjectClaim(t, fixture.store, workID)
	f := workContextFixture{store: fixture.store, workID: workID, owner: fixture.owner, operator: fixture.operator}
	acceptCurrentReviewForTest(t, f, workerFixtureDirectory(t, fixture.store, workID), "ready_for_final_proof")
}

// refineProofCandidateFixture seeds the refine-proof work without a
// pre-bound verification, so every bound verification a test reads is one it
// produced.
func refineProofCandidateFixture(t *testing.T, workID, definitionRef string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, definitionRef), "refine", []string{"verification"}, []string{"review", "artifact"})
}

// refineProofCandidateRun runs one green worktree_verify through the real
// producer on the claimed worktree at head and binds its receipt through
// bind_evidence.
func refineProofCandidateRun(t *testing.T, s *Store, workID, name, head string, command []string) {
	t.Helper()
	git := checksRepairClaimGit(t, s, workID, workerFixtureDirectory(t, s, workID), head)
	req := verifyRequest(git, "refine-proof-"+name+"-"+workID, command, func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil })
	req.WorkID, req.ProjectID, req.PrincipalRef = workID, "project", "principal/operator"
	run, err := s.VerifyWorktree(t.Context(), req)
	if err != nil || run.SubjectRef != "commit:"+head {
		t.Fatalf("verify %s: %+v err=%v", name, run, err)
	}
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "evidence_ref": run.OperationRef}), 0, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("bind verify %s: %v", name, err)
	}
}

// refineProofCandidate is the accepted Review's integrated candidate commit.
func refineProofCandidate(t *testing.T, s *Store, workID string) string {
	t.Helper()
	candidate := issue1013Pin(t, s, workID).Review.Candidate
	if candidate == nil {
		t.Fatal("no integrated candidate after the accepted Review")
	}
	return candidate.Commit
}

func refineProofDelivery(t *testing.T, s *Store, workID string, tooling *ProjectToolingManifest) error {
	t.Helper()
	resolved := ResolveWorkflowFootprint(context.Background(), s, BuiltinWorkflowRegistry(), workID, "record_delivery", nil)
	version := readWorkVersion(t, s, workID)
	actor := reviewGateAcceptor(workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	payload := json.RawMessage(`{"delivery_artifact":"artifact:refine-proof-` + workID + `","delivery_state":"asserted"}`)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs("record_delivery", payload), WorkID: workID, ExpectedVersion: version, ActionID: "record_delivery", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64) + fmt.Sprint(version), IdempotencyIdentity: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version), OperationID: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version), RequestID: "request:refine-proof-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
		ProjectTooling: tooling, ResolvedFootprint: resolved,
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

func refineProofRequireMissingEvidence(t *testing.T, err error, wantContains string) {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("record_delivery = %v, want a missing-evidence refusal", err)
	}
	if !strings.Contains(failure.Detail, wantContains) {
		t.Fatalf("refusal %q does not name %q", failure.Detail, wantContains)
	}
}

// refineProofSeedVerifyRunForProject seeds one green verify lease and the
// durable worktree.verify operation the production release path records, under
// a named Project so the integration facet's per-scope coverage can be exercised.
func refineProofSeedVerifyRunForProject(t *testing.T, s *Store, workID, digest string, command []string, acquired time.Time, project string) string {
	t.Helper()
	leaseID := digest + ":worktree-verify:" + workID
	outcome := "completed"
	resultJSON, err := json.Marshal(WorktreeVerifyResult{WorkID: workID, ProjectID: project, Branch: "work/" + workID, Path: "/tmp/worktrees/" + workID, LeaseID: leaseID, Command: command, ExitCode: 0, TrackedFilesChanged: false})
	if err != nil {
		t.Fatal(err)
	}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	acquiredText := acquired.UTC().Format(time.RFC3339Nano)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,released_at,exit_code,outcome,result_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		leaseID, workID, project, "/tmp/worktrees/"+workID, "released", "client/concord-1", "agent/owner", "session/"+workID, "principal/operator",
		string(commandJSON), acquiredText, acquired.Add(time.Second).UTC().Format(time.RFC3339Nano), 0, outcome, string(resultJSON)); err != nil {
		t.Fatalf("seed the verify lease: %v", err)
	}
	// Only a passing run may stand as verification authority; mirror the
	// production release fold.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations
			(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,
			 accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,
			 result_kind,result_payload,evidence_refs,changed_refs,completed_at)
			VALUES(?,1,?,'worktree.verify','','','external_effect','sha256:`+digest+`','{}','principal/operator','request/verify','`+acquiredText+`','','completed',?,?, '[]', ?)`,
		worktreeVerifyOperationRef(leaseID), workID, string(resultJSON), workflowJSON([]string{worktreeVerifyOperationRef(leaseID)}), acquired.Add(time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed the verify authority: %v", err)
	}
	return worktreeVerifyOperationRef(leaseID)
}

// refineProofBindVerification binds one verification evidence. The fold
// validates the binding's producer operation, so the caller passes a
// producerRunRef that names a completed durable operation for this work:
// the verify run's own operation ref, or a seeded workflow.test authority.
func refineProofBindVerification(t *testing.T, s *Store, workID, ref, producerRunRef string) {
	t.Helper()
	ownerRef, err := WorkflowActorRef(reviewGateAcceptor(workID))
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor(workID+":refine-proof-bind:"+ref, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "verification",
		"immutable_subject_ref": ref, "producer_id": "principal/operator", "producer_run_ref": producerRunRef,
		"producer_watermark": "request/verify", "observed_at": "2026-09-29T00:00:00Z",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("bind the verification evidence: %v", err)
	}
}

func refineProofTooling(entries ...ProjectDeclaredTool) *ProjectToolingManifest {
	return &ProjectToolingManifest{Project: "concord", Declared: entries}
}

// refineProofSeedGreenRun binds one green worktree_verify run after the current
// instance's timing anchor, with the refine start retained in its owning row.
func refineProofSeedGreenRun(t *testing.T, s *Store, workID, digest string) {
	t.Helper()
	var occurred string
	if workflowCurrentStartAtStep(t, s, workID, "refine") == nil {
		t.Fatal("the refine proof fixture has no current refine start")
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(last_checkpoint_at,execution_started_at,started_at) FROM workflow_instances WHERE work_id=?`, workID).Scan(&occurred); err != nil {
		t.Fatalf("read the current refine instance time: %v", err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		t.Fatalf("parse the refine start: %v", err)
	}
	// The proof run's Project must match the work's primary Project, the
	// same scope the recorded worker-job revisions carry. Otherwise the
	// integration facet's per-Project coverage refuses the proof, and the
	// recorded-job fold (CD-0205 D3) reads JobsIntegrated as false.
	var project string
	if err := s.DatabaseForTesting().QueryRow(`SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
		t.Fatalf("read the work primary Project: %v", err)
	}
	ref := refineProofSeedVerifyRunForProject(t, s, workID, digest, []string{"go", "vet", "./..."}, startedAt.Add(time.Second), project)
	refineProofBindVerification(t, s, workID, ref, ref)
}

// workerJobIntegrationGreenRun seeds the integration evidence the job-capable
// delivery admission requires (CD-0205 D3): one green worktree_verify run for
// each current Project scope, acquired after the instance's timing anchor and
// the retained completed-result population, then bound as verification evidence.
func workerJobIntegrationGreenRun(t *testing.T, s *Store, workID, digest string) {
	t.Helper()
	var occurred string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(last_checkpoint_at,execution_started_at,started_at) FROM workflow_instances WHERE work_id=?`, workID).Scan(&occurred); err != nil {
		t.Fatalf("read the delivery-bearing instance time: %v", err)
	}
	anchor, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		t.Fatalf("parse the delivery-bearing step start: %v", err)
	}
	srows, err := s.DatabaseForTesting().Query(`SELECT DISTINCT COALESCE(project_scope,'') FROM worker_job_revisions j WHERE j.work_id=? AND j.revision=(SELECT MAX(l.revision) FROM worker_job_revisions l WHERE l.work_id=j.work_id AND l.job_id=j.job_id)`, workID)
	if err != nil {
		t.Fatalf("read the required job scopes: %v", err)
	}
	defer func() { _ = srows.Close() }()
	scopes := []string{}
	anyScope := false
	for srows.Next() {
		var scope string
		if err := srows.Scan(&scope); err != nil {
			t.Fatal(err)
		}
		if scope == "" {
			anyScope = true
		} else {
			scopes = append(scopes, scope)
		}
	}
	if err := srows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := srows.Close(); err != nil {
		t.Fatal(err)
	}
	// The integration epoch the admission derives also opens after every
	// required job's recorded result population (CD-0205 D3): seed the run
	// after the latest recorded completion too, including the pending final
	// job a combined acceptance counts as satisfied.
	var completed string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(terminal_occurred_at),'') FROM worker_attempts WHERE work_id=? AND terminal_kind=?`, workID, WorkerCompleted).Scan(&completed); err != nil {
		t.Fatalf("read the required job result population: %v", err)
	}
	if at, err := time.Parse(time.RFC3339Nano, completed); err == nil && at.After(anchor) {
		anchor = at
	}
	seed := len(scopes)
	if seed == 0 && anyScope {
		seed = 1
	}
	for i := 0; i < seed; i++ {
		scope := "project-1"
		if len(scopes) > 0 {
			scope = scopes[i]
		}
		ref := refineProofSeedVerifyRunForProject(t, s, workID, digest+fmt.Sprintf("-%02d", i), []string{"go", "vet", "./..."}, anchor.Add(time.Second), scope)
		refineProofBindVerification(t, s, workID, ref, ref)
	}
}

func TestRefineExitRefusesWithoutGreenVerifyRunInEpoch(t *testing.T) {
	const workID = "refine-proof-refusal"
	c := acceptDeliveryUnprovedCandidateAt(t, workID, "workflow.implementation", false)
	s := c.f.store

	// The corrective candidate has no applicable producer receipt.
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, nil), "no passing producer verification covers the current integrated candidate")

	// A binding that names a workflow operation id proves nothing. The
	// seeded workflow.test authority carries the binding's producer identity
	// and records the reference among its evidence.
	seedWorkflowAuthority(t, s, "return-authority-refine-proof-"+workID, workID, "principal/operator", "request/verify", []string{"evidence:return-route-verification"})
	refineProofBindVerification(t, s, workID, "evidence:return-route-verification", "return-authority-refine-proof-"+workID)
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, nil), "no passing unchanged-tree producer receipt")

	// The green run in the epoch admits the exit.
	acceptDeliveryBindFresh(t, c, "exit")
	if err := refineProofDelivery(t, s, workID, nil); err != nil {
		t.Fatalf("record_delivery with the candidate's green run: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineExitRefusesAStaleVerifyRun(t *testing.T) {
	const workID = "refine-proof-stale-run"
	c := acceptDeliveryUnprovedCandidateAt(t, workID, "workflow.implementation", false)
	s := c.f.store
	c.j.bindVerification(t, c.early)
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, nil), "verification does not cover the current integrated candidate")

	acceptDeliveryBindFresh(t, c, "corrected")
	if err := refineProofDelivery(t, s, workID, nil); err != nil {
		t.Fatalf("record_delivery with corrected candidate proof: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineExitRefusesWithoutGreenVerifyRunOnBreakFix(t *testing.T) {
	const workID = "refine-proof-break-fix"
	c := acceptDeliveryUnprovedCandidateAt(t, workID, "workflow.break_fix", false)
	s := c.f.store
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, nil), "no passing producer verification covers the current integrated candidate")
	acceptDeliveryBindFresh(t, c, "exit")
	if err := refineProofDelivery(t, s, workID, nil); err != nil {
		t.Fatalf("record_delivery with the candidate's green run: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestDeclaredToolInvocationAdmitsRefineExitAndUndeclaredArgvRefuses(t *testing.T) {
	const workID = "refine-proof-declared-invocation"
	c := acceptDeliveryCandidateAt(t, workID, "workflow.implementation", "refine", false)
	s := c.f.store
	refineProofCandidateRun(t, s, workID, "declared", refineProofCandidate(t, s, workID), []string{"go", "vet", "./..."})

	// A manifest that declares another tool refuses and names its declared
	// checks.
	undeclared := refineProofTooling(ProjectDeclaredTool{ID: "go-test-race", Invocation: "go test -race ./..."})
	err := refineProofDelivery(t, s, workID, undeclared)
	refineProofRequireMissingEvidence(t, err, "not a tool the Project declares")
	refineProofRequireMissingEvidence(t, err, "declared checks: go-test-race (go test -race ./...)")

	// The exact declared split admits the exit.
	declared := refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."}, ProjectDeclaredTool{ID: "go-test-race", Invocation: "go test -race ./..."})
	if err := refineProofDelivery(t, s, workID, declared); err != nil {
		t.Fatalf("record_delivery with the declared invocation: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestProjectWithoutManifestPassesOnGreenRunRefusesWithoutRun(t *testing.T) {
	const workID = "refine-proof-no-manifest"
	c := acceptDeliveryUnprovedCandidateAt(t, workID, "workflow.implementation", false)
	s := c.f.store

	// No run, no manifest: the refusal states the Project declares none.
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, nil), "the Project declares no checks in .concord/tooling.v1.json")

	// Any green run passes when the Project declares no manifest.
	acceptDeliveryBindFresh(t, c, "exit")
	if err := refineProofDelivery(t, s, workID, nil); err != nil {
		t.Fatalf("record_delivery without a manifest: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineProofStartRecorded(t *testing.T) {
	const workID = "refine-proof-start-recorded"
	fixture := refineProofCurrentFixture(t, workID, "workflow.implementation")
	refineProofStartRefine(t, fixture, workID)

	// The current refine start bounds the epoch in event history: the start
	// event predates every binding the guard accepts.
	var startSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT latest_start_seq FROM workflow_step_epochs WHERE work_id=? AND step_id='refine'`, workID).Scan(&startSeq); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=? AND effect_seq>?`, workID, WorkflowActionStarted, startSeq).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d workflow action starts follow the refine start", count)
	}
}

// refineProofPinnedCommit and refineProofPinnedBlob are the immutable OID
// identities the resolution fake pins: the default ref resolves to the commit,
// and the manifest blob is read by its OID, never by a mutable ref or path.
const (
	refineProofPinnedCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	refineProofPinnedBlob   = "1111111111111111111111111111111111111111"
)

type refineProofFakeRunner struct {
	responses map[string][]byte
	absent    bool
}

func (r refineProofFakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	if r.absent && len(args) > 0 && args[0] == "ls-tree" {
		// The manifest is absent at the ref: ls-tree exits 0 with no output.
		return nil, nil
	}
	if out, ok := r.responses[key]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("unexpected git invocation: %s", key)
}

func TestRefineToolingResolutionReadsTheDefaultRefOnly(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "refine-proof-tooling")

	// The working tree carries a different declaration than the default ref.
	// The resolution resolves the Project's canonical path at this working
	// tree and must still read only the ref: git is faked, so the only way
	// the working-tree file could leak is an os read, which the resolution
	// never performs.
	repo := t.TempDir()
	if err := os.MkdirAll(repo+"/.concord", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo+"/.concord/tooling.v1.json", []byte(`{"schema_version":"1.0","project":"concord","tools":[{"id":"working-tree-check","invocation":"bin/working-tree-check","tier":"fast"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	seedWorkCanonicalPath(t, s, repo)
	// The resolution pins the default ref to one immutable commit OID before
	// any blob read (rev-parse), probes the manifest's presence on that OID
	// (ls-tree), and reads the blob by OID (cat-file). The working tree's
	// manifest stays unread: git is faked, so the only way it could leak is an
	// os read, which the resolution never performs.
	runner := refineProofFakeRunner{responses: map[string][]byte{
		"symbolic-ref refs/remotes/origin/HEAD":                                  []byte("refs/remotes/origin/main\n"),
		"rev-parse --verify origin/main^{commit}":                                []byte(refineProofPinnedCommit + "\n"),
		"ls-tree " + refineProofPinnedCommit + " -- .concord/tooling.v1.json":    []byte("100644 blob " + refineProofPinnedBlob + "\t.concord/tooling.v1.json\n"),
		"ls-tree -z " + refineProofPinnedCommit + " -- .concord/tooling.v1.json": []byte("100644 blob " + refineProofPinnedBlob + "\t.concord/tooling.v1.json\x00"),
		"cat-file blob " + refineProofPinnedBlob:                                 []byte(`{"schema_version":"1.0","project":"concord","tools":[{"id":"default-ref-check","invocation":"bin/check default","tier":"fast"}]}`),
	}}

	tooling, err := resolveWorkProjectTooling(ctx, s, "refine-proof-tooling", runner)
	if err != nil {
		t.Fatalf("resolve the tooling manifest: %v", err)
	}
	if tooling == nil || len(tooling.Declared) != 1 || tooling.Declared[0].ID != "default-ref-check" {
		t.Fatalf("resolved tooling = %+v, want the default ref's declaration", tooling)
	}
	if ProjectToolingInvocationDeclared(tooling, []string{"bin", "working-tree-check"}) {
		t.Fatal("the working tree's declaration leaked into the resolution")
	}
	if !ProjectToolingInvocationDeclared(tooling, []string{"bin/check", "default"}) {
		t.Fatal("the default ref's declaration does not split to its argv")
	}

	// No manifest at the default ref: the resolution reports none and the
	// working tree's manifest stays unread.
	runner.absent = true
	missing, err := resolveWorkProjectTooling(ctx, s, "refine-proof-tooling", runner)
	if err != nil || missing != nil {
		t.Fatalf("resolution with an absent manifest = (%v, %v), want (nil, nil)", missing, err)
	}
}

func TestParseProjectToolingManifest(t *testing.T) {
	parsed, err := ParseProjectToolingManifest([]byte(`{"schema_version":"1.0","project":"concord","tools":[{"id":"go-vet","invocation":"go vet ./...","tier":"fast"}]}`))
	if err != nil {
		t.Fatalf("parse the manifest: %v", err)
	}
	if parsed.Project != "concord" || len(parsed.Declared) != 1 || parsed.Declared[0].Invocation != "go vet ./..." {
		t.Fatalf("parsed manifest = %+v", parsed)
	}
	for name, raw := range map[string]string{
		"not json":       `{`,
		"old schema":     `{"schema_version":"0.9","project":"concord","tools":[{"id":"x","invocation":"x"}]}`,
		"no project":     `{"schema_version":"1.0","tools":[]}`,
		"no tools":       `{"schema_version":"1.0","project":"concord","tools":[]}`,
		"blank argv":     `{"schema_version":"1.0","project":"concord","tools":[{"id":"x","invocation":"  \t "}]}`,
		"missing invoc.": `{"schema_version":"1.0","project":"concord","tools":[{"id":"x"}]}`,
	} {
		if _, err := ParseProjectToolingManifest([]byte(raw)); err == nil {
			t.Fatalf("%s parsed without a refusal", name)
		}
	}
}

func TestRefineProofManifestRequiredTracksTheGate(t *testing.T) {
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		workID := "refine-proof-required-" + strings.TrimPrefix(ref, "workflow.")
		fixture := refineProofCurrentFixture(t, workID, ref)

		// The instance sits on refine: both entry routes resolve the
		// manifest at the admitting refinement step (CD-0198 D4), and
		// bind_evidence resolves nothing.
		for _, action := range []string{"record_delivery", "accept_worker_result"} {
			required, err := RefineProofManifestRequired(context.Background(), fixture.store, workID, action)
			if err != nil {
				t.Fatal(err)
			}
			if !required {
				t.Fatalf("a %s instance at refine must resolve the tooling manifest for %s", ref, action)
			}
		}
		other, err := RefineProofManifestRequired(context.Background(), fixture.store, workID, "bind_evidence")
		if err != nil || other {
			t.Fatalf("%s bind_evidence tooling requirement = (%v, %v), want (false, nil)", ref, other, err)
		}
	}
}

// seedWorkCanonicalPath gives the work item's seeded primary Project a
// canonical_path locator, which is the only locator the tooling resolution
// reads.
func seedWorkCanonicalPath(t *testing.T, s *Store, canonicalPath string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator:refine-proof','project','canonical_path',?,?,'2026-09-29T00:00:00Z','2026-09-29T00:00:00Z')`, canonicalPath, canonicalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}
