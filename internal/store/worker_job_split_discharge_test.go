package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The automatic integration discharge (CD-0225 D3, slice 6b): the binding
// action whose pending verification binding first completes a split's
// integration records one discharge beside the split, once, and the fold
// proves it against the same log on replay.

// dischargeFixture seeds the ordinary split: the capped predecessor split
// into successor-a and successor-b, one allocated inventory path each.
func dischargeFixture(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	return splitStateFixture(t, workID)
}

// dischargeLeafBindings returns the two successors' current bindings in the
// derivation's leaf order.
func dischargeLeafBindings(t *testing.T, fixture workflowReturnRouteFixture, workID string) []WorkerJobBinding {
	t.Helper()
	return []WorkerJobBinding{
		latestSplitBinding(t, fixture, workID, "job:successor-a"),
		latestSplitBinding(t, fixture, workID, "job:successor-b"),
	}
}

// dischargeAcceptLeaf records one implementation-lane dispatch of the named
// successor through the recorded-history route and accepts its result.
func dischargeAcceptLeaf(t *testing.T, fixture workflowReturnRouteFixture, workID, attemptID string, job WorkerJobBinding) {
	t.Helper()
	splitStateAcceptLeaf(t, fixture, workID, attemptID, job)
}

// dischargePrimaryProject reads the work's primary Project, the scope every
// fixture leaf carries.
func dischargePrimaryProject(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var project string
	if err := s.db.QueryRow(`SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
		t.Fatalf("read the work's primary Project: %v", err)
	}
	return project
}

// dischargeSeedVerifyRun seeds one worktree.verify lease and its completed
// durable operation with the caller's outcome shape, so a qualification case
// controls exactly what the run proves.
func dischargeSeedVerifyRun(t *testing.T, s *Store, workID, digest string, command []string, acquired time.Time, project string, exitCode int64, tracked bool) string {
	t.Helper()
	leaseID := digest + ":worktree-verify:" + workID
	resultJSON, err := json.Marshal(WorktreeVerifyResult{WorkID: workID, ProjectID: project, Branch: "work/" + workID, Path: "/tmp/worktrees/" + workID, LeaseID: leaseID, Command: command, ExitCode: int(exitCode), TrackedFilesChanged: tracked})
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
		string(commandJSON), acquiredText, acquired.Add(time.Second).UTC().Format(time.RFC3339Nano), exitCode, "completed", string(resultJSON)); err != nil {
		t.Fatalf("seed the verify lease: %v", err)
	}
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

// runBindEvidenceAction runs one bind_evidence call, carrying the optional
// tooling manifest the agent resolves outside the transaction.
func runBindEvidenceAction(t *testing.T, s *Store, workID, subject string, tooling *ProjectToolingManifest) error {
	t.Helper()
	payload := mustJSONValue(map[string]any{"immutable_subject_ref": subject, "evidence_kind": "verification"})
	version := readWorkVersion(t, s, workID)
	actor := reviewGateAcceptor(workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs("bind_evidence", payload), WorkID: workID, ExpectedVersion: version, ActionID: "bind_evidence", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64) + fmt.Sprint(version), IdempotencyIdentity: "bind_evidence-" + workID + "-" + fmt.Sprint(version), OperationID: "bind_evidence-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "bind_evidence-" + workID + "-" + fmt.Sprint(version), RequestID: "request:bind-" + workID + "-" + fmt.Sprint(version), ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
		ProjectTooling: tooling,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// dischargeEvents reads the work's recorded discharge events beside their
// payloads.
func dischargeEvents(t *testing.T, s *Store, workID string) []WorkerJobSplitDischargedPayload {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq`, workID, WorkerJobSplitDischarged)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var payloads []WorkerJobSplitDischargedPayload
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var payload WorkerJobSplitDischargedPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatalf("recorded discharge is not one closed payload: %v", err)
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return payloads
}

// dischargeOperationEvents reads the work's events of one kind beside their
// expected versions in seq order, so a test can prove the version chain.
func dischargeKindVersions(t *testing.T, s *Store, workID, kind string) []int64 {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT json_extract(payload,'$.expected_version') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq`, workID, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var versions []int64
	for rows.Next() {
		var expected int64
		if err := rows.Scan(&expected); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, expected)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return versions
}

// All leaves accepted and one qualifying pending binding: the action records
// exactly one discharge beside the split, carrying the accepted leaves, the
// qualifying source, and the split's identity, and the predecessor stays
// replaced — never satisfied.
func TestWorkerJobSplitDischargeFollowsQualifyingBinding(t *testing.T) {
	const workID = "worker-job-discharge-happy"
	fixture := dischargeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	bindings := dischargeLeafBindings(t, fixture, workID)
	for index, job := range bindings {
		dischargeAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:discharge-%d", index), job)
	}
	project := dischargePrimaryProject(t, s, workID)
	opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("b", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), project, 0, false)
	version := readWorkVersion(t, s, workID)
	if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
		t.Fatalf("bind the qualifying verification evidence: %v", err)
	}
	discharges := dischargeEvents(t, s, workID)
	if len(discharges) != 1 {
		t.Fatalf("discharges = %d, want exactly one", len(discharges))
	}
	splitID := splitStateSplitEventID(t, fixture, workID)
	predecessor := latestSplitBinding(t, fixture, workID, splitPredecessorJobID)
	payload := discharges[0]
	if payload.SplitID != splitID || payload.Predecessor != predecessor {
		t.Fatalf("discharge identity = %s/%+v, want split %s beside predecessor %+v", payload.SplitID, payload.Predecessor, splitID, predecessor)
	}
	if len(payload.Leaves) != 2 {
		t.Fatalf("discharge leaves = %d, want both accepted leaves", len(payload.Leaves))
	}
	for index, leaf := range payload.Leaves {
		if leaf.Binding != bindings[index] || leaf.SatisfiedRef == "" {
			t.Fatalf("discharge leaf %d = %+v, want binding %+v beside an acceptance reference", index, leaf, bindings[index])
		}
	}
	if len(payload.Integration) != 1 || payload.Integration[0].OperationRef != opRef || payload.Integration[0].ProjectID != project || !slices.Equal(payload.Integration[0].Command, []string{"go", "test", "./internal/store/"}) {
		t.Fatalf("discharge integration = %+v, want the seeded run's operation, Project, and command", payload.Integration)
	}
	var bindingEventID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, opRef).Scan(&bindingEventID); err != nil {
		t.Fatal(err)
	}
	if payload.Integration[0].BindingEventID != bindingEventID {
		t.Fatalf("discharge source binding = %q, want the binding event %q", payload.Integration[0].BindingEventID, bindingEventID)
	}
	states := splitStatesForTest(t, fixture, workID)
	operationID := "bind_evidence-" + workID + "-" + fmt.Sprint(version)
	expectedID := operationID + ":" + workerJobSplitDischargeSuffix(splitID)
	if len(states) != 1 || states[0].DischargeID != expectedID {
		t.Fatalf("split states = %+v, want the one split discharged by %q", states, expectedID)
	}
	if !strings.HasPrefix(payload.Integration[0].BindingEventID, operationID+":semantic:") {
		t.Fatalf("discharge source binding = %q, want one the discharging operation minted", payload.Integration[0].BindingEventID)
	}
	var predecessorState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worker_job_revisions WHERE work_id=? AND job_id=?`, workID, splitPredecessorJobID).Scan(&predecessorState); err != nil || predecessorState == "satisfied" {
		t.Fatalf("predecessor state after discharge = %q, error = %v; want it unsatisfied and still replaced", predecessorState, err)
	}
	// The version chain runs binding, discharge, completion: the discharge
	// continues the operation and the generic completion adjusts after it.
	bindingVersions := dischargeKindVersions(t, s, workID, WorkflowEvidenceBound)
	dischargeVersions := dischargeKindVersions(t, s, workID, WorkerJobSplitDischarged)
	completionVersions := dischargeKindVersions(t, s, workID, WorkflowActionCompleted)
	if len(dischargeVersions) != 1 {
		t.Fatalf("discharge versions = %v, want the one discharge", dischargeVersions)
	}
	if !slices.Contains(bindingVersions, dischargeVersions[0]-1) {
		t.Fatalf("no binding at version %d directly before the discharge: %v", dischargeVersions[0]-1, bindingVersions)
	}
	if !slices.Contains(completionVersions, dischargeVersions[0]+1) {
		t.Fatalf("no completion at version %d directly after the discharge: %v", dischargeVersions[0]+1, completionVersions)
	}
	if readWorkVersion(t, s, workID) != version+3 {
		t.Fatalf("work version after bind = %d, want %d", readWorkVersion(t, s, workID), version+3)
	}
}

// A split with an unsatisfied required leaf records no discharge whatever
// the binding qualifies, and neither the final acceptance nor an earlier
// qualifying bind discharges by itself: the integration binding must ride
// the discharging action.
func TestWorkerJobSplitDischargeWaitsForEveryLeaf(t *testing.T) {
	const workID = "worker-job-discharge-partial"
	fixture := dischargeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	bindings := dischargeLeafBindings(t, fixture, workID)
	dischargeAcceptLeaf(t, fixture, workID, "attempt:discharge-partial-a", bindings[0])
	opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("c", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
		t.Fatalf("bind the qualifying verification evidence: %v", err)
	}
	if discharges := dischargeEvents(t, s, workID); len(discharges) != 0 {
		t.Fatalf("partial leaves recorded %d discharges, want none", len(discharges))
	}
	dischargeAcceptLeaf(t, fixture, workID, "attempt:discharge-partial-b", bindings[1])
	if discharges := dischargeEvents(t, s, workID); len(discharges) != 0 {
		t.Fatalf("the final acceptance recorded %d discharges, want none", len(discharges))
	}
	if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
		t.Fatalf("bind after the final acceptance: %v", err)
	}
	if discharges := dischargeEvents(t, s, workID); len(discharges) != 1 {
		t.Fatalf("qualifying bind after the final accept = %d discharges, want one", len(discharges))
	}
}

// A pending binding that fails one qualification keeps the split
// undischarged: stale, failed, mutating, foreign-Project, undeclared, and
// unrelated subjects each prove no integration.
func TestWorkerJobSplitDischargeRequiresQualifyingRuns(t *testing.T) {
	for _, tc := range []struct {
		name           string
		acquired       time.Time
		exitCode       int64
		tracked        bool
		project        string
		command        []string
		subject        string
		toolingCommand string
	}{
		{name: "stale run", acquired: time.Unix(5, 0), project: "project", command: []string{"go", "test", "./internal/store/"}},
		{name: "failed run", acquired: time.Unix(30, 0), exitCode: 1, project: "project", command: []string{"go", "test", "./internal/store/"}},
		{name: "mutating run", acquired: time.Unix(30, 0), tracked: true, project: "project", command: []string{"go", "test", "./internal/store/"}},
		{name: "foreign project", acquired: time.Unix(30, 0), project: "project-elsewhere", command: []string{"go", "test", "./internal/store/"}},
		{name: "undeclared command", acquired: time.Unix(30, 0), project: "project", command: []string{"make", "check"}, toolingCommand: "go vet ./..."},
		{name: "unrelated subject", acquired: time.Unix(30, 0), project: "project", command: []string{"go", "test", "./internal/store/"}, subject: "evidence:unrelated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "worker-job-discharge-" + strings.ReplaceAll(tc.name, " ", "-")
			fixture := dischargeFixture(t, workID)
			s := workerJobSizeStore(fixture)
			for index, job := range dischargeLeafBindings(t, fixture, workID) {
				dischargeAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:%s-%d", workID, index), job)
			}
			project := tc.project
			if project == "project" {
				project = dischargePrimaryProject(t, s, workID)
			}
			opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("d", 64), tc.command, tc.acquired, project, tc.exitCode, tc.tracked)
			subject := tc.subject
			if subject == "" {
				subject = opRef
			}
			var tooling *ProjectToolingManifest
			if tc.toolingCommand != "" {
				tooling = refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: tc.toolingCommand})
			}
			if err := runBindEvidenceAction(t, s, workID, subject, tooling); err != nil {
				t.Fatalf("bind the verification evidence: %v", err)
			}
			if discharges := dischargeEvents(t, s, workID); len(discharges) != 0 {
				t.Fatalf("%s recorded %d discharges, want none", tc.name, len(discharges))
			}
		})
	}
}

// A qualifying run bound before the last acceptance sits behind the anchor:
// it never discharges, and an unrelated binding after the acceptance still
// records none. A fresh qualifying binding after the anchor does.
func TestWorkerJobSplitDischargeAnchorsIntegrationAfterTheLastAcceptance(t *testing.T) {
	const workID = "worker-job-discharge-anchor"
	fixture := dischargeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	bindings := dischargeLeafBindings(t, fixture, workID)
	dischargeAcceptLeaf(t, fixture, workID, "attempt:discharge-anchor-a", bindings[0])
	early := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("f", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	if err := runBindEvidenceAction(t, s, workID, early, nil); err != nil {
		t.Fatalf("bind the early verification evidence: %v", err)
	}
	dischargeAcceptLeaf(t, fixture, workID, "attempt:discharge-anchor-b", bindings[1])
	if err := runBindEvidenceAction(t, s, workID, "evidence:unrelated", nil); err != nil {
		t.Fatalf("bind the unrelated evidence: %v", err)
	}
	if discharges := dischargeEvents(t, s, workID); len(discharges) != 0 {
		t.Fatalf("an unrelated action discharged on %d earlier bindings, want none", len(discharges))
	}
	late := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("7", 64), []string{"go", "vet", "./..."}, time.Unix(40, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	if err := runBindEvidenceAction(t, s, workID, late, nil); err != nil {
		t.Fatalf("bind the late verification evidence: %v", err)
	}
	discharges := dischargeEvents(t, s, workID)
	if len(discharges) != 1 {
		t.Fatalf("discharges after the anchored bind = %d, want one", len(discharges))
	}
	if len(discharges[0].Integration) != 1 || discharges[0].Integration[0].OperationRef != late {
		t.Fatalf("discharge integration = %+v, want only the run bound after the last acceptance", discharges[0].Integration)
	}
}

// dischargeReplayFixture records the split, both accepted leaves, the green
// verify run, and the discharging bind through registered workflow routes
// only, so the complete log replays.
func dischargeReplayFixture(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, "repair",
		[]string{"verification"}, []string{"verification", "review", "artifact"})
	s := workerJobSizeStore(fixture)
	bootstrapOracleFixtureSubject(t, s, workID)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitFields(t, workID, fixture, predecessor, nil)); err != nil {
		t.Fatalf("ordinary split refused: %v", err)
	}
	for index, job := range dischargeLeafBindings(t, fixture, workID) {
		dischargeReplayAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:replay-%d", index), job)
	}
	opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("9", 64), []string{"go", "test", "./internal/store/"}, time.Unix(50, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
		t.Fatalf("bind the qualifying verification evidence: %v", err)
	}
	return fixture
}

// dischargeReplayAcceptLeaf records one successor's implementation dispatch
// through registered workflow starts and completions — the route a full-log
// replay can fold — then accepts its result.
func dischargeReplayAcceptLeaf(t *testing.T, fixture workflowReturnRouteFixture, workID, attemptID string, job WorkerJobBinding) {
	t.Helper()
	s := workerJobSizeStore(fixture)
	actor, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0)+1 FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')=?`, workID, WorkflowActionStarted, "repair").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	var directory string
	if err := s.db.QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&directory); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	started := workflowTypedEvent(workID+":discharge-start:"+attemptID, WorkflowActionStarted, workID, actor, time.Unix(20, 0).UTC(), version, map[string]any{
		"step_id": "repair", "action_id": "dispatch_worker", "attempt_epoch": epoch, "actor_ref": actor,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("2", 64), "idempotency_identity": "discharge-dispatch-" + attemptID,
	})
	authorized := workflowTypedEvent(workID+":discharge-authorized:"+attemptID, WorkflowActionCompleted, workID, actor, time.Unix(20, 0).UTC(), version+1, map[string]any{
		"step_id": "repair", "action_id": "dispatch_worker", "attempt_epoch": epoch, "actor_ref": actor,
		"worker_attempt_id": attemptID, "worker_job": job, "changed_refs": []string{workID}, "result_evidence_refs": []string{},
		"worker_worktree_identity": workerWorktreeIdentity(directory),
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{started, authorized}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record dispatch authorization: %v", err)
	}
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, &job); err != nil {
		t.Fatal(err)
	}
	tree := strings.Repeat("3", 40)
	completed := reportBindingCompletion(t, s, workID, attemptID, reviewGateLane(t, "implementation"), &job, func(p *WorkerCompletedPayload) {
		p.ObservedDelivery = &WorkerObservedDelivery{Commit: tree, State: "clean", Base: testJobSizeBaseCommit(), Paths: []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 1}}}
		p.ProductionSize = &WorkerProductionSize{Base: testJobSizeBaseCommit(), Tree: tree, Count: 1}
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("record the completed report of %s: %v", attemptID, err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}), 0, fixture.owner); err != nil {
		t.Fatalf("accept %s: %v", attemptID, err)
	}
}

// applyRawDischarge appends one authored discharge payload under the named
// producing operation through the workflow-authority test route — the event
// id shape the fold derives the operation prefix from, and the path a
// replayed or forged log reaches the fold through.
func applyRawDischarge(t *testing.T, fixture workflowReturnRouteFixture, workID, operationID, actor string, occurred time.Time, payload WorkerJobSplitDischargedPayload) error {
	t.Helper()
	s := workerJobSizeStore(fixture)
	version := readWorkVersion(t, s, workID)
	resulting := version + 1
	payload.WorkflowVersionFields = WorkflowVersionFields{WorkID: workID, ExpectedVersion: &version, ResultingVersion: &resulting}
	event := Event{EventID: operationID + ":" + workerJobSplitDischargeSuffix(payload.SplitID), Kind: WorkerJobSplitDischarged, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor, OccurredAt: occurred, PayloadVersion: 1, Payload: mustJSONValue(payload)}
	return applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
}

// dischargeForgedPayload authors a discharge from the derived state, with
// the caller's mutations applied.
func dischargeForgedPayload(t *testing.T, fixture workflowReturnRouteFixture, workID string, mutate func(payload *WorkerJobSplitDischargedPayload)) WorkerJobSplitDischargedPayload {
	t.Helper()
	states := splitStatesForTest(t, fixture, workID)
	if len(states) != 1 {
		t.Fatalf("states = %d, want the one ordinary split", len(states))
	}
	payload := WorkerJobSplitDischargedPayload{
		workerJobSplitDischargePayload: workerJobSplitDischargePayload{SplitID: states[0].SplitID, Predecessor: states[0].Split.Predecessor},
		Leaves:                         workerJobSplitDischargeLeaves(states[0]),
	}
	if mutate != nil {
		mutate(&payload)
	}
	return payload
}

// The fold refuses a discharge whose record disagrees with the log it folds
// from: a missing or altered leaf, an altered source, a foreign split or
// predecessor, unsatisfied leaves, and a second discharge for one split.
func TestWorkerJobSplitDischargeFoldRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(payload *WorkerJobSplitDischargedPayload)
		want   string
	}{
		{"missing leaf", func(p *WorkerJobSplitDischargedPayload) { p.Leaves = p.Leaves[:1] }, "leaf bindings differ"},
		{"altered leaf reference", func(p *WorkerJobSplitDischargedPayload) { p.Leaves[0].SatisfiedRef = "evidence:stranger" }, "leaf bindings differ"},
		{"foreign split", func(p *WorkerJobSplitDischargedPayload) { p.SplitID = "record_worker_job:stranger:semantic" }, "holds no record of"},
		{"foreign predecessor", func(p *WorkerJobSplitDischargedPayload) { p.Predecessor.Digest = "sha256:" + strings.Repeat("c", 64) }, "did not record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "worker-job-discharge-fold-" + strings.ReplaceAll(tc.name, " ", "-")
			fixture := dischargeFixture(t, workID)
			s := workerJobSizeStore(fixture)
			for index, job := range dischargeLeafBindings(t, fixture, workID) {
				dischargeAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:%s-%d", workID, index), job)
			}
			opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("4", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
			payload := dischargeForgedPayload(t, fixture, workID, func(p *WorkerJobSplitDischargedPayload) {
				p.Integration = []WorkerJobSplitDischargeSource{{BindingEventID: "bind_evidence-" + workID + "-1:semantic:evidence:0", OperationRef: opRef, ProjectID: dischargePrimaryProject(t, s, workID), Command: []string{"go", "test", "./internal/store/"}}}
				tc.mutate(p)
			})
			err := applyRawDischarge(t, fixture, workID, "discharge-forged-"+workID, "worker:test", time.Unix(60, 0).UTC(), payload)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s fold = %v, want %q", tc.name, err, tc.want)
			}
		})
	}
	// An altered retained run needs a real earlier binding to point at: one
	// bound run of a foreign Project never discharges, and a forged record
	// that claims the covered Project or another command for it refuses.
	for _, tc := range []struct {
		name    string
		project string
		command []string
	}{
		{"altered source project", "primary", nil},
		{"altered source command", "project-elsewhere", []string{"make", "check"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "worker-job-discharge-fold-" + strings.ReplaceAll(tc.name, " ", "-")
			fixture := dischargeFixture(t, workID)
			s := workerJobSizeStore(fixture)
			for index, job := range dischargeLeafBindings(t, fixture, workID) {
				dischargeAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:%s-%d", workID, index), job)
			}
			opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("6", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), "project-elsewhere", 0, false)
			if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
				t.Fatalf("bind the foreign verification evidence: %v", err)
			}
			var bindingEventID string
			if err := s.DatabaseForTesting().QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, opRef).Scan(&bindingEventID); err != nil {
				t.Fatal(err)
			}
			source := WorkerJobSplitDischargeSource{BindingEventID: bindingEventID, OperationRef: opRef, ProjectID: "project-elsewhere", Command: []string{"go", "test", "./internal/store/"}}
			if tc.project == "primary" {
				source.ProjectID = dischargePrimaryProject(t, s, workID)
			}
			if tc.command != nil {
				source.Command = tc.command
			}
			payload := dischargeForgedPayload(t, fixture, workID, func(p *WorkerJobSplitDischargedPayload) {
				p.Integration = []WorkerJobSplitDischargeSource{source}
			})
			err := applyRawDischarge(t, fixture, workID, "discharge-forged-"+workID, "worker:test", time.Unix(60, 0).UTC(), payload)
			if err == nil || !strings.Contains(err.Error(), "retains a run different from") {
				t.Fatalf("%s fold = %v, want the retained-run refusal", tc.name, err)
			}
		})
	}
	t.Run("unsatisfied leaves", func(t *testing.T) {
		const workID = "worker-job-discharge-fold-unsatisfied"
		fixture := dischargeFixture(t, workID)
		bindings := dischargeLeafBindings(t, fixture, workID)
		dischargeAcceptLeaf(t, fixture, workID, "attempt:fold-unsatisfied-a", bindings[0])
		payload := dischargeForgedPayload(t, fixture, workID, func(p *WorkerJobSplitDischargedPayload) {
			p.Integration = []WorkerJobSplitDischargeSource{{BindingEventID: "bind_evidence-" + workID + "-1:semantic:evidence:0", OperationRef: "worktree_verify:unsatisfied", ProjectID: dischargePrimaryProject(t, workerJobSizeStore(fixture), workID), Command: []string{"go", "test"}}}
		})
		err := applyRawDischarge(t, fixture, workID, "discharge-forged-"+workID, "worker:test", time.Unix(60, 0).UTC(), payload)
		if err == nil || !strings.Contains(err.Error(), "remain unsatisfied") {
			t.Fatalf("unsatisfied-leaf fold = %v, want the unsatisfied refusal", err)
		}
	})
	t.Run("once only", func(t *testing.T) {
		const workID = "worker-job-discharge-fold-once"
		fixture := dischargeFixture(t, workID)
		s := workerJobSizeStore(fixture)
		for index, job := range dischargeLeafBindings(t, fixture, workID) {
			dischargeAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:fold-once-%d", index), job)
		}
		opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("5", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
		if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
			t.Fatalf("bind the qualifying verification evidence: %v", err)
		}
		payload := dischargeForgedPayload(t, fixture, workID, func(p *WorkerJobSplitDischargedPayload) {
			p.Integration = dischargeEvents(t, s, workID)[0].Integration
		})
		err := applyRawDischarge(t, fixture, workID, "discharge-forged-"+workID, "worker:test", time.Unix(60, 0).UTC(), payload)
		if err == nil || !strings.Contains(err.Error(), "second discharge") {
			t.Fatalf("second discharge fold = %v, want the once-only refusal", err)
		}
	})
}

// A nested lineage discharges children before parents in one operation: the
// outer split's leaves are the nested successors beside the unreplaced one,
// and each split's discharge carries its own leaf set.
func TestWorkerJobSplitDischargeNestedSplitsDischargeChildrenFirst(t *testing.T) {
	const workID = "worker-job-discharge-nested"
	fixture := dischargeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	intermediate := latestSplitBinding(t, fixture, workID, "job:successor-a")
	nested := splitFields(t, workID, fixture, intermediate, func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		successors[0]["job_id"], successors[1]["job_id"] = "job:leaf-c", "job:leaf-d"
		allocation[0]["job_id"], allocation[1]["job_id"] = "job:leaf-c", "job:leaf-d"
		return successors, allocation
	})
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, nested); err != nil {
		t.Fatalf("nested split refused: %v", err)
	}
	for index, jobID := range []string{"job:leaf-c", "job:leaf-d", "job:successor-b"} {
		dischargeAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:discharge-nested-%d", index), latestSplitBinding(t, fixture, workID, jobID))
	}
	opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("8", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	version := readWorkVersion(t, s, workID)
	if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
		t.Fatalf("bind the qualifying verification evidence: %v", err)
	}
	var dischargeSeqs []struct {
		seq      int64
		splitID  string
		expected int64
	}
	rows, err := s.DatabaseForTesting().Query(`SELECT seq,json_extract(payload,'$.split_id'),json_extract(payload,'$.expected_version') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq`, workID, WorkerJobSplitDischarged)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var record struct {
			seq      int64
			splitID  string
			expected int64
		}
		if err := rows.Scan(&record.seq, &record.splitID, &record.expected); err != nil {
			t.Fatal(err)
		}
		dischargeSeqs = append(dischargeSeqs, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(dischargeSeqs) != 2 {
		t.Fatalf("discharges = %d, want the nested and the outer split", len(dischargeSeqs))
	}
	if dischargeSeqs[0].expected != version+1 || dischargeSeqs[1].expected != version+2 {
		t.Fatalf("discharge versions = %d, %d; want the children's first at %d", dischargeSeqs[0].expected, dischargeSeqs[1].expected, version+1)
	}
	states := splitStatesForTest(t, fixture, workID)
	if len(states) != 2 {
		t.Fatalf("states = %d, want both splits", len(states))
	}
	for index, state := range states {
		if !strings.HasSuffix(state.DischargeID, ":"+workerJobSplitDischargeSuffix(state.SplitID)) {
			t.Fatalf("split %d discharged by %q, want the closed suffix of its own split", index, state.DischargeID)
		}
	}
	if states[0].DischargeID == states[1].DischargeID || strings.TrimSuffix(states[0].DischargeID, ":"+workerJobSplitDischargeSuffix(states[0].SplitID)) != strings.TrimSuffix(states[1].DischargeID, ":"+workerJobSplitDischargeSuffix(states[1].SplitID)) {
		t.Fatalf("nested discharges %q and %q, want distinct ids of one discharging operation", states[0].DischargeID, states[1].DischargeID)
	}
}

// applyRawVerificationBinding appends one verification binding through the
// workflow-authority test route under the caller's operation event id,
// actor, and time — the binding a foreign operation contributed.
func applyRawVerificationBinding(t *testing.T, s *Store, workID, eventID, opRef, actor string, occurred time.Time) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor(eventID, WorkflowEvidenceBound, workID, actor, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "verification",
		"immutable_subject_ref": opRef, "producer_id": "principal/operator", "producer_run_ref": opRef,
		"producer_watermark": "request/verify", "observed_at": occurred.UTC().Format(time.RFC3339Nano),
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("bind the foreign verification evidence: %v", err)
	}
}

// A binding the same actor recorded at the same timestamp under a foreign
// operation is not operation identity: a discharge whose event derives from
// one operation refuses sources another operation minted, however equal the
// actor and clock are.
func TestWorkerJobSplitDischargeRefusesForeignOperationBindings(t *testing.T) {
	const workID = "worker-job-discharge-foreign-operation"
	fixture := dischargeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	for index, job := range dischargeLeafBindings(t, fixture, workID) {
		dischargeAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:foreign-op-%d", index), job)
	}
	opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("a", 64), []string{"go", "test", "./internal/store/"}, time.Unix(50, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	applyRawVerificationBinding(t, s, workID, "bind-foreign-operation:semantic:evidence:0", opRef, "worker:foreign", time.Unix(9, 0).UTC())
	payload := dischargeForgedPayload(t, fixture, workID, func(p *WorkerJobSplitDischargedPayload) {
		p.Integration = []WorkerJobSplitDischargeSource{{BindingEventID: "bind-foreign-operation:semantic:evidence:0", OperationRef: opRef, ProjectID: dischargePrimaryProject(t, s, workID), Command: []string{"go", "test", "./internal/store/"}}}
	})
	err := applyRawDischarge(t, fixture, workID, "discharge-forgery-operation", "worker:foreign", time.Unix(9, 0).UTC(), payload)
	if err == nil || !strings.Contains(err.Error(), "no source binding the discharging operation minted") {
		t.Fatalf("foreign-operation discharge = %v, want the operation-identity refusal", err)
	}
}

// A run bound at or before the split's integration anchor never discharges,
// even while green, freshly timestamped, and cited by the discharging
// operation itself: the anchor's seq bounds the sources as it bounds the
// writer's derivation.
func TestWorkerJobSplitDischargeRefusesBindingsAtOrBeforeTheAnchor(t *testing.T) {
	const workID = "worker-job-discharge-anchor-seq"
	fixture := dischargeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	bindings := dischargeLeafBindings(t, fixture, workID)
	dischargeAcceptLeaf(t, fixture, workID, "attempt:anchor-seq-a", bindings[0])
	opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("e", 64), []string{"go", "test", "./internal/store/"}, time.Unix(50, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	version := readWorkVersion(t, s, workID)
	if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
		t.Fatalf("bind the early verification evidence: %v", err)
	}
	dischargeAcceptLeaf(t, fixture, workID, "attempt:anchor-seq-b", bindings[1])
	var bindingEventID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, opRef).Scan(&bindingEventID); err != nil {
		t.Fatal(err)
	}
	payload := dischargeForgedPayload(t, fixture, workID, func(p *WorkerJobSplitDischargedPayload) {
		p.Integration = []WorkerJobSplitDischargeSource{{BindingEventID: bindingEventID, OperationRef: opRef, ProjectID: dischargePrimaryProject(t, s, workID), Command: []string{"go", "test", "./internal/store/"}}}
	})
	actor, err := WorkflowActorRef(reviewGateAcceptor(workID))
	if err != nil {
		t.Fatal(err)
	}
	err = applyRawDischarge(t, fixture, workID, "bind_evidence-"+workID+"-"+fmt.Sprint(version), actor, time.Unix(9, 0).UTC(), payload)
	if err == nil || !strings.Contains(err.Error(), "at or before the split's integration anchor") {
		t.Fatalf("pre-anchor binding discharge = %v, want the anchor refusal", err)
	}
}

// dischargeShapeVersion is a version pointer for the payload-shape fixture.
func dischargeShapeVersion(value int64) *int64 { return &value }

// The discharge's closed shape: the derived leaves and at least one source
// are present, and no run or binding appears twice.
func TestWorkerJobSplitDischargePayloadShape(t *testing.T) {
	source := WorkerJobSplitDischargeSource{BindingEventID: "bind:x:semantic:evidence:0", OperationRef: "worktree_verify:lease", ProjectID: "project", Command: []string{"go", "test"}}
	leaf := WorkerJobSplitDischargeLeaf{Binding: WorkerJobBinding{JobID: "job:leaf", Revision: 1, Digest: "sha256:" + strings.Repeat("b", 64)}, SatisfiedRef: "evidence:accept"}
	base := func() WorkerJobSplitDischargedPayload {
		return WorkerJobSplitDischargedPayload{
			WorkflowVersionFields:          WorkflowVersionFields{WorkID: "work", ExpectedVersion: dischargeShapeVersion(1), ResultingVersion: dischargeShapeVersion(2)},
			workerJobSplitDischargePayload: workerJobSplitDischargePayload{SplitID: "split", Predecessor: WorkerJobBinding{JobID: "job:pred", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64)}},
			Leaves:                         []WorkerJobSplitDischargeLeaf{leaf},
			Integration:                    []WorkerJobSplitDischargeSource{source},
		}
	}
	event := Event{EventID: "op:" + workerJobSplitDischargeSuffix("split"), Kind: WorkerJobSplitDischarged, SubjectType: SubjectWorkItem, SubjectID: "work"}
	for _, tc := range []struct {
		name   string
		mutate func(payload *WorkerJobSplitDischargedPayload)
		want   string
	}{
		{"missing leaves", func(p *WorkerJobSplitDischargedPayload) { p.Leaves = nil }, "must carry its derived required leaves"},
		{"missing sources", func(p *WorkerJobSplitDischargedPayload) { p.Integration = nil }, "must carry at least one qualifying verification binding"},
		{"duplicate binding", func(p *WorkerJobSplitDischargedPayload) { p.Integration = append(p.Integration, source) }, "twice"},
		{"duplicate run", func(p *WorkerJobSplitDischargedPayload) {
			p.Integration = append(p.Integration, WorkerJobSplitDischargeSource{BindingEventID: "bind:x:semantic:evidence:1", OperationRef: source.OperationRef, ProjectID: "project", Command: source.Command})
		}, "through two bindings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := base()
			tc.mutate(&payload)
			err := validateWorkerJobSplitDischargedPayload(event, payload)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s shape = %v, want %q", tc.name, err, tc.want)
			}
		})
	}
	if err := validateWorkerJobSplitDischargedPayload(event, base()); err != nil {
		t.Fatalf("well-shaped discharge refused: %v", err)
	}
}

// The complete replayable log rebuilds to the same discharge: the fold
// re-proves the leaves, the retained runs, and the coverage through the
// rebuild's retained verify-lease snapshot.
func TestWorkerJobSplitDischargeRebuildsFromLog(t *testing.T) {
	const workID = "worker-job-discharge-rebuild"
	fixture := dischargeReplayFixture(t, workID)
	s := workerJobSizeStore(fixture)
	live := dischargeEvents(t, s, workID)
	if len(live) != 1 {
		t.Fatalf("discharges = %d, want the one automatic discharge", len(live))
	}
	states := splitStatesForTest(t, fixture, workID)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild from the log: %v", err)
	}
	rebuilt := dischargeEvents(t, s, workID)
	if len(rebuilt) != 1 || rebuilt[0].SplitID != live[0].SplitID || rebuilt[0].Predecessor != live[0].Predecessor {
		t.Fatalf("rebuilt discharges differ: live %+v, rebuilt %+v", live, rebuilt)
	}
	if !slices.Equal(rebuilt[0].Leaves, live[0].Leaves) || !slices.EqualFunc(rebuilt[0].Integration, live[0].Integration, func(first, second WorkerJobSplitDischargeSource) bool {
		return first.BindingEventID == second.BindingEventID && first.OperationRef == second.OperationRef && first.ProjectID == second.ProjectID && slices.Equal(first.Command, second.Command)
	}) {
		t.Fatalf("rebuilt discharge content differs:\nlive:    %+v\nrebuilt: %+v", live[0], rebuilt[0])
	}
	after := splitStatesForTest(t, fixture, workID)
	if len(after) != len(states) || after[0].DischargeID != states[0].DischargeID {
		t.Fatalf("rebuilt split states differ: live %+v, rebuilt %+v", states, after)
	}
}

// The discharge derivation is gated on the size-capable pin: a definition
// without the production_size_max member derives no discharge, and
// RefineProofManifestRequired resolves the manifest for bind_evidence only
// where a split can exist.
func TestWorkerJobSplitDischargeGateAndToolingResolution(t *testing.T) {
	t.Run("size-inactive definition derives nothing", func(t *testing.T) {
		registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 27)
		if !ok {
			t.Fatal("workflow.break_fix v27 is not registered")
		}
		if workflowJobSizeActive(registered.Definition) {
			t.Fatal("control: break_fix v27 declares the size members")
		}
		events, err := workerJobSplitDischargeEventsTx(context.Background(), nil, registered.Definition, WorkflowActionExecutionRequest{WorkID: "worker-job-discharge-gate"}, "repair", "actor:test", []Event{workflowTypedEvent("gate:evidence", WorkflowEvidenceBound, "worker-job-discharge-gate", "actor:test", time.Unix(1, 0).UTC(), 1, map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "evidence:gate"})}, 1)
		if err != nil || events != nil {
			t.Fatalf("size-inactive derivation = %v, %v; want nil, nil", events, err)
		}
	})
	t.Run("bind_evidence tooling resolution", func(t *testing.T) {
		const workID = "worker-job-discharge-tooling"
		fixture := workerJobSizeFixture(t, workID)
		s := workerJobSizeStore(fixture)
		required, err := RefineProofManifestRequired(context.Background(), s, workID, "bind_evidence")
		if err != nil || required {
			t.Fatalf("bind_evidence before the split required = %v, %v; want false", required, err)
		}
		if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitFields(t, workID, fixture, recordSplitPredecessor(t, workID, fixture), nil)); err != nil {
			t.Fatalf("ordinary split refused: %v", err)
		}
		required, err = RefineProofManifestRequired(context.Background(), s, workID, "bind_evidence")
		if err != nil || !required {
			t.Fatalf("bind_evidence on the split pin required = %v, %v; want true", required, err)
		}
	})
	t.Run("size-inactive pin resolves nothing", func(t *testing.T) {
		const workID = "worker-job-discharge-tooling-legacy"
		fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 27, "repair")
		required, err := RefineProofManifestRequired(context.Background(), workerJobSizeStore(fixture), workID, "bind_evidence")
		if err != nil || required {
			t.Fatalf("legacy bind_evidence required = %v, %v; want false", required, err)
		}
	})
}
