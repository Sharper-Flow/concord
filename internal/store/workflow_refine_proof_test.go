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

// The CD-0192 refine-exit proof: leaving refine on workflow.implementation
// v18+ and workflow.break_fix v16+ requires, in the current refine epoch, a
// bound verification evidence naming a completed worktree.verify durable
// operation whose lease ran green after the refine start, and whose argv is a
// declared tool when the Project's default ref declares a tooling manifest.

func refineProofFixture(t *testing.T, workID, definitionRef string, definitionVersion int64) workflowReturnRouteFixture {
	t.Helper()
	registered, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
	if !ok {
		t.Fatalf("workflow definition %s@%d is not registered", definitionRef, definitionVersion)
	}
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, "refine", []string{"verification"}, []string{"verification", "review", "artifact"})
}

func refineProofStartRefine(t *testing.T, fixture workflowReturnRouteFixture, workID string) {
	t.Helper()
	reviewGateStartStep(t, fixture.store, workID, "refine", "start_refine", fixture.owner)
}

func refineProofDelivery(t *testing.T, s *Store, workID string, tooling *ProjectToolingManifest) error {
	t.Helper()
	version := verdictItemVersion(t, s, workID)
	actor := reviewGateAcceptor(workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	payload := json.RawMessage(`{"delivery_artifact":"artifact:refine-proof-` + workID + `","delivery_state":"asserted"}`)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs("record_delivery", payload), WorkID: workID, ExpectedVersion: version, ActionID: "record_delivery", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64) + fmt.Sprint(version), IdempotencyIdentity: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version), OperationID: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version), RequestID: "request:refine-proof-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
		ProjectTooling: tooling,
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

// refineProofSeedVerifyRun seeds one verify lease and, when the run is green,
// the durable worktree.verify operation the production release path records.
func refineProofSeedVerifyRun(t *testing.T, s *Store, workID, digest string, command []string, acquired time.Time) string {
	return refineProofSeedVerifyRunForProject(t, s, workID, digest, command, acquired, "project-1")
}

// refineProofSeedVerifyRunForProject seeds the same green run under a named
// Project, so the integration facet's per-scope coverage can be exercised.
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
			(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,
			 accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,
			 result_kind,result_payload,evidence_refs,changed_refs,completed_at)
			VALUES(?,1,?,'worktree.verify',1,'','external_effect','sha256:`+digest+`','{}','principal/operator','request/verify','`+acquiredText+`','','completed',?,?, '[]', ?)`,
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
	version := verdictItemVersion(t, s, workID)
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

// refineProofSeedGreenRun seeds one green worktree_verify run acquired just
// after the current refine start and binds it as verification evidence, the
// proof the CD-0192 refine exit consumes on the current definitions.
func refineProofSeedGreenRun(t *testing.T, s *Store, workID, digest string) {
	t.Helper()
	var occurred string
	if err := s.DatabaseForTesting().QueryRow(`SELECT occurred_at FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='refine' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&occurred); err != nil {
		t.Fatalf("read the refine start: %v", err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		t.Fatalf("parse the refine start: %v", err)
	}
	ref := refineProofSeedVerifyRun(t, s, workID, digest, []string{"go", "vet", "./..."}, startedAt.Add(time.Second))
	refineProofBindVerification(t, s, workID, ref, ref)
}

// workerJobIntegrationGreenRun seeds the integration evidence the job-capable
// delivery admission requires (CD-0205 D3): one green worktree_verify run of
// the fixture's Project acquired after the current refine start and after
// every recorded worker-job acceptance, then bound as verification evidence.
func workerJobIntegrationGreenRun(t *testing.T, s *Store, workID, digest string) {
	t.Helper()
	var occurred string
	if err := s.DatabaseForTesting().QueryRow(`SELECT occurred_at FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='refine' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&occurred); err != nil {
		t.Fatalf("read the refine start: %v", err)
	}
	anchor, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		t.Fatalf("parse the refine start: %v", err)
	}
	srows, err := s.DatabaseForTesting().Query(`SELECT DISTINCT COALESCE(project_scope,''),e.occurred_at FROM worker_job_revisions j LEFT JOIN domain_events e ON e.subject_type='work_item' AND e.subject_id=j.work_id AND e.event_id=j.satisfied_result_ref WHERE j.work_id=? AND j.revision=(SELECT MAX(l.revision) FROM worker_job_revisions l WHERE l.work_id=j.work_id AND l.job_id=j.job_id)`, workID)
	if err != nil {
		t.Fatalf("read the required job scopes: %v", err)
	}
	defer func() { _ = srows.Close() }()
	scopes := []string{}
	anyScope := false
	for srows.Next() {
		var scope, accepted string
		var acceptedAny any
		acceptedAny = &accepted
		if err := srows.Scan(&scope, &acceptedAny); err != nil {
			t.Fatal(err)
		}
		if scope == "" {
			anyScope = true
		} else {
			scopes = append(scopes, scope)
		}
		if at, err := time.Parse(time.RFC3339Nano, accepted); err == nil && at.After(anchor) {
			anchor = at
		}
	}
	if err := srows.Err(); err != nil {
		t.Fatal(err)
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
	fixture := refineProofFixture(t, workID, "workflow.implementation", 18)
	refineProofStartRefine(t, fixture, workID)

	// No binding in the epoch: the exit refuses and names the epoch gap.
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, fixture.store, workID, nil), "no verification evidence is bound in the current refine epoch")

	// A binding that names a workflow operation id proves nothing. The
	// seeded workflow.test authority carries the binding's producer identity
	// and records the reference among its evidence.
	seedWorkflowAuthority(t, fixture.store, "return-authority-refine-proof-"+workID, workID, "principal/operator", "request/verify", []string{"evidence:return-route-verification"})
	refineProofBindVerification(t, fixture.store, workID, "evidence:return-route-verification", "return-authority-refine-proof-"+workID)
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, fixture.store, workID, nil), "names no completed worktree.verify durable operation")

	// The green run in the epoch admits the exit.
	afterStart := refineProofSeedVerifyRun(t, fixture.store, workID, strings.Repeat("4", 64), []string{"go", "vet", "./..."}, time.Unix(10, 0))
	refineProofBindVerification(t, fixture.store, workID, afterStart, afterStart)
	if err := refineProofDelivery(t, fixture.store, workID, nil); err != nil {
		t.Fatalf("record_delivery with the epoch's green run: %v", err)
	}
	reviewGateRequireStep(t, fixture.store, workID, "delivery")
}

func TestRefineExitRefusesAStaleVerifyRun(t *testing.T) {
	const workID = "refine-proof-stale-run"
	fixture := refineProofFixture(t, workID, "workflow.implementation", 18)
	refineProofStartRefine(t, fixture, workID)

	// A green run acquired before the current refine start cannot prove this
	// epoch: a replayed lease keeps its original acquire time, so the lease
	// an earlier epoch ran stays stale however green it was.
	beforeStart := refineProofSeedVerifyRun(t, fixture.store, workID, strings.Repeat("2", 64), []string{"go", "vet", "./..."}, time.Unix(8, 0))
	refineProofBindVerification(t, fixture.store, workID, beforeStart, beforeStart)
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, fixture.store, workID, nil), "acquired at or before the current refine start")

	// The run acquired inside the epoch is the proof.
	afterStart := refineProofSeedVerifyRun(t, fixture.store, workID, strings.Repeat("3", 64), []string{"go", "vet", "./..."}, time.Unix(10, 0))
	refineProofBindVerification(t, fixture.store, workID, afterStart, afterStart)
	if err := refineProofDelivery(t, fixture.store, workID, nil); err != nil {
		t.Fatalf("record_delivery with the epoch's green run: %v", err)
	}
	reviewGateRequireStep(t, fixture.store, workID, "delivery")
}

func TestRefineExitRefusesWithoutGreenVerifyRunOnBreakFix(t *testing.T) {
	const workID = "refine-proof-break-fix"
	fixture := refineProofFixture(t, workID, "workflow.break_fix", 16)
	refineProofStartRefine(t, fixture, workID)
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, fixture.store, workID, nil), "no verification evidence is bound in the current refine epoch")
	runRef := refineProofSeedVerifyRun(t, fixture.store, workID, strings.Repeat("5", 64), []string{"python3", "scripts/check-json.py"}, time.Unix(10, 0))
	refineProofBindVerification(t, fixture.store, workID, runRef, runRef)
	if err := refineProofDelivery(t, fixture.store, workID, nil); err != nil {
		t.Fatalf("record_delivery with the epoch's green run: %v", err)
	}
	reviewGateRequireStep(t, fixture.store, workID, "delivery")
}

func TestDeclaredToolInvocationAdmitsRefineExitAndUndeclaredArgvRefuses(t *testing.T) {
	const workID = "refine-proof-declared-invocation"
	fixture := refineProofFixture(t, workID, "workflow.implementation", 18)
	refineProofStartRefine(t, fixture, workID)
	runRef := refineProofSeedVerifyRun(t, fixture.store, workID, strings.Repeat("6", 64), []string{"go", "vet", "./..."}, time.Unix(10, 0))
	refineProofBindVerification(t, fixture.store, workID, runRef, runRef)

	// A manifest that declares another tool refuses and names its declared
	// checks.
	undeclared := refineProofTooling(ProjectDeclaredTool{ID: "go-test-race", Invocation: "go test -race ./..."})
	err := refineProofDelivery(t, fixture.store, workID, undeclared)
	refineProofRequireMissingEvidence(t, err, "not a tool the Project declares")
	refineProofRequireMissingEvidence(t, err, "declared checks: go-test-race (go test -race ./...)")

	// The exact declared split admits the exit.
	declared := refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."}, ProjectDeclaredTool{ID: "go-test-race", Invocation: "go test -race ./..."})
	if err := refineProofDelivery(t, fixture.store, workID, declared); err != nil {
		t.Fatalf("record_delivery with the declared invocation: %v", err)
	}
	reviewGateRequireStep(t, fixture.store, workID, "delivery")
}

func TestProjectWithoutManifestPassesOnGreenRunRefusesWithoutRun(t *testing.T) {
	const workID = "refine-proof-no-manifest"
	fixture := refineProofFixture(t, workID, "workflow.implementation", 18)
	refineProofStartRefine(t, fixture, workID)

	// No run, no manifest: the refusal states the Project declares none.
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, fixture.store, workID, nil), "the Project declares no checks in .concord/tooling.v1.json")

	// Any green run passes when the Project declares no manifest.
	runRef := refineProofSeedVerifyRun(t, fixture.store, workID, strings.Repeat("7", 64), []string{"bin/oc-test", "conformance"}, time.Unix(10, 0))
	refineProofBindVerification(t, fixture.store, workID, runRef, runRef)
	if err := refineProofDelivery(t, fixture.store, workID, nil); err != nil {
		t.Fatalf("record_delivery without a manifest: %v", err)
	}
	reviewGateRequireStep(t, fixture.store, workID, "delivery")
}

func TestRefineProofGateKeepsEarlierVersionsOpen(t *testing.T) {
	const workID = "refine-proof-earlier-version"
	fixture := refineProofFixture(t, workID, "workflow.break_fix", 14)
	refineProofStartRefine(t, fixture, workID)

	// v14 carries no refine-exit proof: an unrefined exit advances as it
	// always did, and the passed manifest changes nothing.
	declared := refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."})
	if err := refineProofDelivery(t, fixture.store, workID, declared); err != nil {
		t.Fatalf("record_delivery on v14 without a run: %v", err)
	}
	reviewGateRequireStep(t, fixture.store, workID, "delivery")
}

func TestRefineProofStartRecorded(t *testing.T) {
	const workID = "refine-proof-start-recorded"
	fixture := refineProofFixture(t, workID, "workflow.implementation", 18)
	refineProofStartRefine(t, fixture, workID)

	// The current refine start bounds the epoch in event history: the start
	// event predates every binding the guard accepts.
	var startSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT MAX(seq) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='refine'`, workID, WorkflowActionStarted).Scan(&startSeq); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>?`, workID, WorkflowActionStarted, startSeq).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d workflow action starts follow the refine start", count)
	}
}

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
	runner := refineProofFakeRunner{responses: map[string][]byte{
		"symbolic-ref refs/remotes/origin/HEAD":           []byte("refs/remotes/origin/main\n"),
		"ls-tree origin/main -- .concord/tooling.v1.json": []byte("100644 blob 1111111111111111111111111111111111111111\t.concord/tooling.v1.json\n"),
		"show origin/main:.concord/tooling.v1.json":       []byte(`{"schema_version":"1.0","project":"concord","tools":[{"id":"default-ref-check","invocation":"bin/check default","tier":"fast"}]}`),
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
	const workID = "refine-proof-required"
	fixture := refineProofFixture(t, workID, "workflow.implementation", 21)

	// Before refine starts, the instance sits on refine but the gate asks
	// nothing: the resolution requirement follows the pinned shape and step.
	// Both entry routes resolve at the admitting refinement step (CD-0198 D4).
	for _, action := range []string{"record_delivery", "accept_worker_result"} {
		required, err := RefineProofManifestRequired(context.Background(), fixture.store, workID, action)
		if err != nil {
			t.Fatal(err)
		}
		if !required {
			t.Fatalf("a v21 instance at refine must resolve the tooling manifest for %s", action)
		}
	}
	other, err := RefineProofManifestRequired(context.Background(), fixture.store, workID, "bind_evidence")
	if err != nil || other {
		t.Fatalf("bind_evidence tooling requirement = (%v, %v), want (false, nil)", other, err)
	}

	// An earlier pinned version resolves nothing for either action.
	const earlierID = "refine-proof-required-earlier"
	earlier := refineProofFixture(t, earlierID, "workflow.implementation", 17)
	for _, action := range []string{"record_delivery", "accept_worker_result"} {
		requiredEarlier, err := RefineProofManifestRequired(context.Background(), earlier.store, earlierID, action)
		if err != nil {
			t.Fatal(err)
		}
		if requiredEarlier {
			t.Fatalf("a v17 instance must not resolve the tooling manifest for %s", action)
		}
	}

	// The plain accept at the admitting step resolves the manifest, because
	// its refusal runs the same admission machinery the delivery exit runs.
	const breakFixID = "refine-proof-required-break-fix"
	breakFix := refineProofFixture(t, breakFixID, "workflow.break_fix", 19)
	requiredBreakFix, err := RefineProofManifestRequired(context.Background(), breakFix.store, breakFixID, "accept_worker_result")
	if err != nil || !requiredBreakFix {
		t.Fatalf("break_fix v19 accept_worker_result tooling requirement = (%v, %v), want (true, nil)", requiredBreakFix, err)
	}
}

// seedWorkCanonicalPath gives the work item's seeded primary Project a
// canonical_path locator, which is the only locator the tooling resolution
// reads.
func seedWorkCanonicalPath(t *testing.T, s *Store, canonicalPath string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator:refine-proof','project','canonical_path',?,?,'2026-09-29T00:00:00Z','2026-09-29T00:00:00Z')`, canonicalPath, canonicalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}
