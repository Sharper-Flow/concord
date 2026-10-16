package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestArchitectureNativeCapacity witnesses the verify-release capacity
// invariant: one verify release
// that would publish a subject receipt or preparation readiness measures the
// prospective post-release context against the work's declared allocation
// and withholds the publication when the allocation cannot hold it. The
// withheld release retains the truthful run receipt, outcome, streams, and
// lease cleanup, invents no readiness, and never converts a failed run into
// a passed one. Legacy work without a retained architecture declares no
// allocation and keeps its released receipt unchanged.

// nativeCapacityPlan projects the closed plan fixture onto the test work
// with one tunable work_context reserve.
func nativeCapacityPlan(workContext int) WorkflowDesignArchitecture {
	plan := projectionArchitecture()
	plan.Capacity = &WorkflowDesignArchitectureCapacity{
		DesignRecord: 6144, WorkerJob: 10240, WorkContext: workContext, LawContext: 1024,
		ProposalRecord: 512, WorkRecord: 2048, TaskPremise: 2048, OutcomePredicates: 1024,
	}
	return plan
}

func nativeCapacityRecordDesign(t *testing.T, s *Store, workID string, workContext int) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	plan := nativeCapacityPlan(workContext)
	if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "native-cap-design-"+workID, version, "capacity", &plan)); err != nil {
		t.Fatalf("capacity design refused on %s: %v", workID, err)
	}
}

// nativeCapacityOrdinaryFixture seeds one work item, one real git worktree
// entry, and nothing else, so the first green verify publishes the first
// subject receipt the context projection can read.
func nativeCapacityOrdinaryFixture(t *testing.T, workID string) (*Store, string) {
	t.Helper()
	s := openTemp(t)
	t.Cleanup(func() { s.Close() })
	seedWork(t, s, workID)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "init", "-b", "main")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "add", ".")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "subject")
	head, err := (ExecGitRunner{}).Run(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(head))
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1);INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?,'project','native-cap-claim','main',?,?,?,'active','2026-10-15T00:00:00Z','{}');DELETE FROM fold_guard`, WorktreeSetID(workID), commit, repo, repo); err != nil {
		t.Fatal(err)
	}
	return s, commit
}

func nativeCapacityOrdinaryRequest(workID, leaseID string) WorktreeVerifyRequest {
	r := WorktreeVerifyRequest{WorkID: workID, ProjectID: "project", LeaseID: leaseID, RequestID: "request-" + leaseID, PrincipalRef: "principal/operator", Owner: SessionWorktreeOwner{ClientRef: "client/oracle", AgentRef: "agent/native", SessionRef: "session/native"}, Runner: ExecGitRunner{}, Command: []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, Now: time.Now().UTC()}
	r.SupportedBudgetSeconds = 30
	return r
}

func nativeCapacityDurableRows(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var rows int64
	if err := s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE work_id=? AND workflow_type_ref='worktree.verify'`, workID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func nativeCapacityLeaseRow(t *testing.T, s *Store, leaseID string) (state, outcome string, exitCode int64) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT state,outcome,exit_code FROM worktree_verify_leases WHERE lease_id=?`, leaseID).Scan(&state, &outcome, &exitCode); err != nil {
		t.Fatal(err)
	}
	return state, outcome, exitCode
}

func TestArchitectureNativeCapacityLegacyReceiptUnchanged(t *testing.T) {
	const work = "native-cap-legacy"
	s, _ := nativeCapacityOrdinaryFixture(t, work)
	r, err := s.VerifyWorktree(context.Background(), nativeCapacityOrdinaryRequest(work, "native-cap-legacy-lease"))
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("legacy verify: %+v err=%v", r, err)
	}
	if rows := nativeCapacityDurableRows(t, s, work); rows != 1 {
		t.Fatalf("legacy authority rows = %d, want 1", rows)
	}
	view, err := readWorkContextView(context.Background(), s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || view.SubjectCommit == "" {
		t.Fatalf("legacy context subject absent: %+v", view)
	}
}

func TestArchitectureNativeCapacityOrdinaryReceiptGrowsWithinAllocation(t *testing.T) {
	const work = "native-cap-ordinary-fit"
	s, commit := nativeCapacityOrdinaryFixture(t, work)
	// The context view is nil before the first receipt: the member is absent
	// and any reserve admits the design.
	nativeCapacityRecordDesign(t, s, work, 4096)
	r, err := s.VerifyWorktree(context.Background(), nativeCapacityOrdinaryRequest(work, "native-cap-fit-lease"))
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("fitting verify: %+v err=%v", r, err)
	}
	if rows := nativeCapacityDurableRows(t, s, work); rows != 1 {
		t.Fatalf("authority rows = %d, want 1", rows)
	}
	view, err := readWorkContextView(context.Background(), s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || view.SubjectCommit != commit {
		t.Fatalf("receipt growth did not publish the subject: %+v want %s", view, commit)
	}
}

func TestArchitectureNativeCapacityOrdinaryRefusalWithholdsAuthority(t *testing.T) {
	const work = "native-cap-ordinary-over"
	s, _ := nativeCapacityOrdinaryFixture(t, work)
	// Reserve zero admits the design while the member is absent and refuses
	// the subject publication the first green release would add.
	nativeCapacityRecordDesign(t, s, work, 0)
	r, err := s.VerifyWorktree(context.Background(), nativeCapacityOrdinaryRequest(work, "native-cap-over-lease"))
	if failureKind(err) != KindLimitExceeded {
		t.Fatalf("over-budget verify typed refusal = %v, want %q (result %+v)", err, KindLimitExceeded, r)
	}
	if !strings.Contains(err.Error(), "work_context") {
		t.Fatalf("refusal does not name the over-allocated slot: %v", err)
	}
	if r.ExitCode != 0 {
		t.Fatalf("refused result lost the truthful exit code: %+v", r)
	}
	state, outcome, exitCode := nativeCapacityLeaseRow(t, s, "native-cap-over-lease")
	if state != "released" || outcome != "completed" || exitCode != 0 {
		t.Fatalf("withheld lease row = %s/%s/%d, want released/completed/0", state, outcome, exitCode)
	}
	if rows := nativeCapacityDurableRows(t, s, work); rows != 0 {
		t.Fatalf("over-budget authority rows = %d, want 0", rows)
	}
	view, err := readWorkContextView(context.Background(), s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	if view != nil {
		t.Fatalf("over-budget context published: %+v", view)
	}
	// The recorded outcome replays equivalently: the same typed capacity
	// failure, no new launch, and still no authority row.
	r2, err2 := s.VerifyWorktree(context.Background(), nativeCapacityOrdinaryRequest(work, "native-cap-over-lease"))
	if failureKind(err2) != KindLimitExceeded {
		t.Fatalf("replay typed failure = %v, want %q (result %+v)", err2, KindLimitExceeded, r2)
	}
	if r2.ExitCode != 0 {
		t.Fatalf("replay lost the truthful exit code: %+v", r2)
	}
	if rows := nativeCapacityDurableRows(t, s, work); rows != 0 {
		t.Fatalf("replay authority rows = %d, want 0", rows)
	}
}

// nativeCapacityDemoteJob records a later not-ready revision of one fixture
// job, so exactly one ready job remains and the context projection selects
// its preparation receipt.
func nativeCapacityDemoteJob(t *testing.T, f *nativeOracleFixture) {
	t.Helper()
	const jobID = "job:b"
	b := f.bundle
	b.Control.ReadinessEvidenceRefs = []string{f.prepare.OperationRef}
	oracle := &AcceptanceOracle{Owners: []OracleOwner{b.Owner}, Cases: b.Cases, Controls: []OracleControl{b.Control}}
	version := readWorkVersion(t, f.s, f.work)
	next := version + 1
	job := WorkerJobRecordedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: f.work, ExpectedVersion: &version, ResultingVersion: &next}, JobID: jobID, Revision: 2, ContractVersion: 1, Objective: "superseded revision", StoppingCondition: "the superseded revision stops", ProjectScope: "project", PathScope: []string{"pkg"}, PredicateIDs: b.Control.PredicateIDs, Checks: []string{}, Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{}, AcceptanceOracle: oracle}
	job.Digest = DeriveWorkerJobDigest(job)
	raw, _ := json.Marshal(job)
	actorRef, _ := WorkflowActorRef(f.actor)
	event := workflowEventWithActor("native-cap-demote-"+jobID, WorkerJobRecorded, f.work, actorRef, map[string]any{})
	event.Payload = raw
	event.PayloadVersion = 1
	if err := applyWorkflowTestOperationDirect(context.Background(), f.s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.work): version}}); err != nil {
		t.Fatalf("demotion of %s refused: %v", jobID, err)
	}
}

func TestArchitectureNativeCapacityPreparationReceiptAddsContext(t *testing.T) {
	f := newNativeOracleFixture(t)
	nativeCapacityDemoteJob(t, f)
	// The single-ready work now projects the preparation receipt; the design
	// with an adequate reserve is admitted over it.
	nativeCapacityRecordDesign(t, f.s, f.work, 8192)
	view, err := readWorkContextView(context.Background(), f.s.db, f.work)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || len(view.OraclePreparations) != 1 || view.OraclePreparations[0].RunRef != f.prepare.OperationRef {
		t.Fatalf("preparation receipt did not add context: %+v", view)
	}
	if view.SubjectCommit != f.head {
		t.Fatalf("context subject = %q, want %s", view.SubjectCommit, f.head)
	}
}

func TestArchitectureNativeCapacityExecutePublishesWithinAllocation(t *testing.T) {
	f := newNativeOracleFixture(t)
	nativeCapacityDemoteJob(t, f)
	nativeCapacityRecordDesign(t, f.s, f.work, 8192)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 {
		t.Fatalf("execute: %+v err=%v", r, err)
	}
	if rows := nativeCapacityDurableRows(t, f.s, f.work); rows != 2 {
		t.Fatalf("authority rows = %d, want 2 (fixture subject plus execute)", rows)
	}
	r2, err2 := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err2 != nil || r2.Oracle == nil || r2.Oracle.Qualification != "pass" {
		t.Fatalf("replay: %+v err=%v", r2, err2)
	}
	if rows := nativeCapacityDurableRows(t, f.s, f.work); rows != 2 {
		t.Fatalf("replay authority rows = %d, want 2", rows)
	}
}

func TestArchitectureNativeCapacityOverBudgetWithholdsReadinessAndAuthority(t *testing.T) {
	f := newNativeOracleFixture(t)
	nativeCapacityDemoteJob(t, f)
	// Reclaim the entry so the current subject leaves the projection, pin
	// the reserve to exactly the preparations-only member, then restore it:
	// the restored subject over-allocates the slot before any release runs.
	if _, err := f.s.db.Exec(`UPDATE worktree_entries SET state='reclaimed' WHERE set_id=? AND project_id='project'`, WorktreeSetID(f.work)); err != nil {
		t.Fatal(err)
	}
	view, err := readWorkContextView(context.Background(), f.s.db, f.work)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || len(view.OraclePreparations) != 1 || view.SubjectCommit != "" {
		t.Fatalf("preparations-only view = %+v", view)
	}
	member, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	nativeCapacityRecordDesign(t, f.s, f.work, len(member)+16)
	if _, err := f.s.db.Exec(`UPDATE worktree_entries SET state='active' WHERE set_id=? AND project_id='project'`, WorktreeSetID(f.work)); err != nil {
		t.Fatal(err)
	}

	// A passing preparation must not publish readiness while the work's
	// current projection is over its allocation. This witnesses the
	// release-time defense over measured current state, not new preparation
	// growth: through sanctioned routes no release adds the first projected
	// preparation, because the unique-ready selection cannot name an
	// unreleased receipt — every readiness transition is a folded operation
	// whose post-fold projection would refuse.
	prepare := &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	rp, errp := f.s.VerifyWorktree(context.Background(), f.request("prepare2", prepare))
	if failureKind(errp) != KindLimitExceeded {
		t.Fatalf("over-budget prepare typed refusal = %v, want %q (result %+v)", errp, KindLimitExceeded, rp)
	}
	if rp.Oracle == nil || rp.Oracle.Qualification != "unavailable" {
		t.Fatalf("withheld prepare receipt = %+v, want truthful unavailable qualification", rp.Oracle)
	}
	state, outcome, exitCode := nativeCapacityLeaseRow(t, f.s, "native-prepare2")
	if state != "released" || outcome != "completed" || exitCode != 0 {
		t.Fatalf("withheld prepare lease = %s/%s/%d, want released/completed/0", state, outcome, exitCode)
	}
	if prep, err := readOraclePreparationReceiptTx(context.Background(), f.s.db, f.work, "project", rp.OperationRef); err != nil || prep != nil {
		t.Fatalf("over-budget readiness invented: %+v err=%v", prep, err)
	}
	selected, err := readSelectedOraclePreparations(context.Background(), f.s.db, f.work)
	if err != nil || len(selected) != 1 || selected[0].RunRef != f.prepare.OperationRef {
		t.Fatalf("projected preparations changed: %+v err=%v", selected, err)
	}

	// A passing execute must not append durable authority for the
	// over-budget subject, and retains the run's truthful metadata, exit,
	// and complete streams.
	rx, errx := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if failureKind(errx) != KindLimitExceeded {
		t.Fatalf("over-budget execute typed refusal = %v, want %q (result %+v)", errx, KindLimitExceeded, rx)
	}
	state, outcome, exitCode = nativeCapacityLeaseRow(t, f.s, "native-execute")
	if state != "released" || outcome != "completed" || exitCode != 0 {
		t.Fatalf("withheld execute lease = %s/%s/%d, want released/completed/0", state, outcome, exitCode)
	}
	var stdoutLength, stderrLength int64
	if err := f.s.db.QueryRow(`SELECT length(stdout_blob),length(stderr_blob) FROM worktree_verify_leases WHERE lease_id='native-execute'`).Scan(&stdoutLength, &stderrLength); err != nil {
		t.Fatal(err)
	}
	if stdoutLength <= 0 || stderrLength <= 0 {
		t.Fatalf("withheld run lost its streams: stdout=%d stderr=%d", stdoutLength, stderrLength)
	}
	if rows := nativeCapacityDurableRows(t, f.s, f.work); rows != 1 {
		t.Fatalf("over-budget authority rows = %d, want 1 (fixture subject only)", rows)
	}
	// The withheld receipt replays equivalently: the same typed capacity
	// failure, unavailable qualification, no invented authority, no second
	// launch.
	rx2, errx2 := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if failureKind(errx2) != KindLimitExceeded {
		t.Fatalf("replay typed failure = %v, want %q (result %+v)", errx2, KindLimitExceeded, rx2)
	}
	if rx2.Oracle == nil || rx2.Oracle.Qualification != "unavailable" {
		t.Fatalf("replay qualification: %+v", rx2.Oracle)
	}
	if rows := nativeCapacityDurableRows(t, f.s, f.work); rows != 1 {
		t.Fatalf("replay authority rows = %d, want 1", rows)
	}
}

func TestArchitectureNativeCapacityMeasurementHoldsAtRelease(t *testing.T) {
	f := newNativeOracleFixture(t)
	nativeCapacityDemoteJob(t, f)
	nativeCapacityRecordDesign(t, f.s, f.work, 8192)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("race", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" {
		t.Fatalf("execution fixture failed: %+v err=%v", r, err)
	}
	var planRaw, digest string
	capture := &nativeStreamCapture{complete: true}
	if err := f.s.db.QueryRow(`SELECT native_plan_json,native_plan_sha256,stdout_blob,stderr_blob FROM worktree_verify_leases WHERE lease_id=?`, r.LeaseID).Scan(&planRaw, &digest, &capture.stdout, &capture.stderr); err != nil {
		t.Fatal(err)
	}
	var plan nativeOraclePlan
	if err := json.Unmarshal([]byte(planRaw), &plan); err != nil {
		t.Fatal(err)
	}
	req := f.request("race", &f.a)
	req.Command = r.Command
	req.nativePlanJSON, req.nativePlanSHA256 = planRaw, digest
	req.nativeCommandJSON = workflowJSON(req.Command)
	req.nativeAcceptedInputsDigest = nativeDigest([]byte(req.nativeCommandJSON + "\x00" + digest))
	req.nativeEvidenceRefsJSON = workflowJSON([]string{r.OperationRef})
	raw, err := marshalNativeVerifyRecord(&r, capture)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := r
	unavailableOracle := *r.Oracle
	unavailable.Oracle = &unavailableOracle
	unavailable.Oracle.Qualification = "unavailable"
	unavailableRaw, err := marshalNativeVerifyRecord(&unavailable, capture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`DELETE FROM durable_operations WHERE op_id=?`, r.OperationRef); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET state='held',outcome='running' WHERE lease_id=?`, r.LeaseID); err != nil {
		t.Fatal(err)
	}
	// The measurement the release must stay bound to: it fits the allocation
	// on its own snapshot, and the helper returns the snapshot's dependency
	// stamp beside the withheld variant it would publish if the stamp or the
	// allocation no longer holds.
	stamp, _, variant, variantRaw, capErr := f.s.architectureCapacityVerifyRefusal(context.Background(), req, r, capture, unavailable, unavailableRaw)
	if capErr != nil || stamp == (nativeOracleReleaseStamp{}) {
		t.Fatalf("fitting measurement: stamp=%+v err=%v", stamp, capErr)
	}
	// The race: a real sanctioned operation lands after the measurement and
	// before the release. It changes no release validity input, only the
	// measured dependency identity.
	version := readWorkVersion(t, f.s, f.work)
	if err := applyWorkflowTestOperationDirect(context.Background(), f.s, Operation{Events: []Event{capacityWritesIntentEvent(t, f.work, version, "race intent")}, ExpectedVersions: workVersion(f.work, version)}); err != nil {
		t.Fatal(err)
	}
	entry, err := activeWorktreeEntryForProject(context.Background(), f.s.db, "test", f.work, "project")
	if err != nil {
		t.Fatal(err)
	}
	req.nativeCapacityStamp = stamp
	req.nativeCapacityUnavailable = variant
	req.nativeCapacityRaw = variantRaw
	req.nativeCapacityOver = capErr
	result, relErr := f.s.releaseNativeOracle(context.Background(), req, entry, oracleGitSubjectSnapshot{head: plan.SubjectCommit, clean: true}, plan.WorktreeIdentity, plan, r, raw, unavailable, unavailableRaw, capture, false)
	if failureKind(relErr) != KindProjectionConflict {
		t.Fatalf("stale measurement release error = %v, want projection_conflict (result %+v)", relErr, result)
	}
	if result.Oracle == nil || result.Oracle.Qualification != "unavailable" {
		t.Fatalf("stale measurement published: %+v", result.Oracle)
	}
	var green int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil {
		t.Fatal(err)
	}
	if green != 0 {
		t.Fatalf("stale measurement appended authority: %d rows", green)
	}
}

func TestArchitectureNativeCapacityMalformedRefusalRefusesReplay(t *testing.T) {
	const work = "native-cap-malformed"
	s, _ := nativeCapacityOrdinaryFixture(t, work)
	nativeCapacityRecordDesign(t, s, work, 0)
	if _, err := s.VerifyWorktree(context.Background(), nativeCapacityOrdinaryRequest(work, "native-cap-malformed-lease")); failureKind(err) != KindLimitExceeded {
		t.Fatalf("refusal fixture: %v", err)
	}
	for name, corrupt := range map[string]string{
		"null":      `json('null')`,
		"malformed": `json('{"kind":42,"detail":"broken"}')`,
	} {
		if _, err := s.db.Exec(`UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.internal_capacity_refusal', `+corrupt+`) WHERE lease_id=?`, "native-cap-malformed-lease"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.VerifyWorktree(context.Background(), nativeCapacityOrdinaryRequest(work, "native-cap-malformed-lease")); failureKind(err) != KindInvariantViolation {
			t.Fatalf("%s retained refusal replay = %v, want %q", name, err, KindInvariantViolation)
		}
	}
}

func TestArchitectureNativeCapacityLegacyReleaseUnarmed(t *testing.T) {
	f := newNativeOracleFixture(t)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("legacy-race", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" {
		t.Fatalf("legacy execution fixture failed: %+v err=%v", r, err)
	}
	var planRaw, digest string
	capture := &nativeStreamCapture{complete: true}
	if err := f.s.db.QueryRow(`SELECT native_plan_json,native_plan_sha256,stdout_blob,stderr_blob FROM worktree_verify_leases WHERE lease_id=?`, r.LeaseID).Scan(&planRaw, &digest, &capture.stdout, &capture.stderr); err != nil {
		t.Fatal(err)
	}
	var plan nativeOraclePlan
	if err := json.Unmarshal([]byte(planRaw), &plan); err != nil {
		t.Fatal(err)
	}
	req := f.request("legacy-race", &f.a)
	req.Command = r.Command
	req.nativePlanJSON, req.nativePlanSHA256 = planRaw, digest
	req.nativeCommandJSON = workflowJSON(req.Command)
	req.nativeAcceptedInputsDigest = nativeDigest([]byte(req.nativeCommandJSON + "\x00" + digest))
	req.nativeEvidenceRefsJSON = workflowJSON([]string{r.OperationRef})
	raw, err := marshalNativeVerifyRecord(&r, capture)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := r
	unavailableOracle := *r.Oracle
	unavailable.Oracle = &unavailableOracle
	unavailable.Oracle.Qualification = "unavailable"
	unavailableRaw, err := marshalNativeVerifyRecord(&unavailable, capture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`DELETE FROM durable_operations WHERE op_id=?`, r.OperationRef); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET state='held',outcome='running' WHERE lease_id=?`, r.LeaseID); err != nil {
		t.Fatal(err)
	}
	// No retained architecture: the measurement must come back unarmed.
	stamp, measured, variant, variantRaw, capErr := f.s.architectureCapacityVerifyRefusal(context.Background(), req, r, capture, unavailable, unavailableRaw)
	if capErr != nil || measured || variantRaw != nil || stamp != (nativeOracleReleaseStamp{}) {
		t.Fatalf("legacy measurement armed: measured=%v raw=%v stamp=%+v err=%v", measured, variantRaw != nil, stamp, capErr)
	}
	req.nativeCapacityStamp, req.nativeCapacityUnavailable, req.nativeCapacityRaw, req.nativeCapacityOver = stamp, variant, variantRaw, capErr
	// A real sanctioned operation lands after the measurement; the legacy
	// release must still publish under the unchanged native freshness guard.
	version := readWorkVersion(t, f.s, f.work)
	if err := applyWorkflowTestOperationDirect(context.Background(), f.s, Operation{Events: []Event{capacityWritesIntentEvent(t, f.work, version, "legacy race intent")}, ExpectedVersions: workVersion(f.work, version)}); err != nil {
		t.Fatal(err)
	}
	entry, err := activeWorktreeEntryForProject(context.Background(), f.s.db, "test", f.work, "project")
	if err != nil {
		t.Fatal(err)
	}
	result, relErr := f.s.releaseNativeOracle(context.Background(), req, entry, oracleGitSubjectSnapshot{head: plan.SubjectCommit, clean: true}, plan.WorktreeIdentity, plan, r, raw, unavailable, unavailableRaw, capture, false)
	if relErr != nil {
		t.Fatalf("legacy release refused: %v", relErr)
	}
	if result.Oracle == nil || result.Oracle.Qualification != "pass" {
		t.Fatalf("legacy release did not publish: %+v", result.Oracle)
	}
	var green int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil {
		t.Fatal(err)
	}
	if green != 1 {
		t.Fatalf("legacy authority rows = %d, want 1", green)
	}
}

func TestArchitectureNativeCapacityWriterKeepsReceiptOnFailure(t *testing.T) {
	injected, err := injectInternalCapacityRefusal([]byte(`{`), capacityStalenessFailure())
	if err == nil || injected != nil {
		t.Fatalf("unparsable receipt: injected=%q err=%v, want nil bytes and an error", injected, err)
	}
	passthrough, err := injectInternalCapacityRefusal([]byte(`{"exit_code":0}`), nil)
	if err != nil || string(passthrough) != `{"exit_code":0}` {
		t.Fatalf("nil refusal must pass the receipt through unchanged: %q %v", passthrough, err)
	}
}

func TestArchitectureNativeCapacityFailedRunRetainsTruthWhenOver(t *testing.T) {
	f := newNativeOracleFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "pkg", "value.go"), []byte("package oracle\nfunc Value() int { return 8 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "capfail")
	o := f.authorize(t, "capfail", f.actor)
	// Over-budget state: reclaim the entry so the subject leaves the
	// projection, pin the reserve to the absent member, restore it.
	if _, err := f.s.db.Exec(`UPDATE worktree_entries SET state='reclaimed' WHERE set_id=? AND project_id='project'`, WorktreeSetID(f.work)); err != nil {
		t.Fatal(err)
	}
	nativeCapacityRecordDesign(t, f.s, f.work, 0)
	if _, err := f.s.db.Exec(`UPDATE worktree_entries SET state='active' WHERE set_id=? AND project_id='project'`, WorktreeSetID(f.work)); err != nil {
		t.Fatal(err)
	}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("capfail-execute", &o))
	if err != nil {
		t.Fatalf("failing run returned a store error: %v", err)
	}
	if r.Oracle == nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 {
		t.Fatalf("failed run lost its truth: qualification=%+v exit=%d", r.Oracle, r.ExitCode)
	}
	state, outcome, exitCode := nativeCapacityLeaseRow(t, f.s, "native-capfail-execute")
	if state != "released" || outcome != "completed" || exitCode == 0 {
		t.Fatalf("failed run lease = %s/%s/%d, want released/completed/nonzero", state, outcome, exitCode)
	}
	var stdoutLength, stderrLength int64
	if err := f.s.db.QueryRow(`SELECT length(stdout_blob),length(stderr_blob) FROM worktree_verify_leases WHERE lease_id=?`, "native-capfail-execute").Scan(&stdoutLength, &stderrLength); err != nil {
		t.Fatal(err)
	}
	if stdoutLength <= 0 || stderrLength <= 0 {
		t.Fatalf("failed run lost its streams: stdout=%d stderr=%d", stdoutLength, stderrLength)
	}
	// Both commitAndPrepare subject receipts are published; the failed run
	// must add none of its own.
	if rows := nativeCapacityDurableRows(t, f.s, f.work); rows != 2 {
		t.Fatalf("failed run authority rows = %d, want 2 (subject receipts only)", rows)
	}
	var failedAuthority int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&failedAuthority); err != nil {
		t.Fatal(err)
	}
	if failedAuthority != 0 {
		t.Fatalf("failed run published authority: %d rows", failedAuthority)
	}
}

func TestArchitectureNativeCapacityUnknownAllocationRefuses(t *testing.T) {
	f := newNativeOracleFixture(t)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("unknown-alloc", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" {
		t.Fatalf("execution fixture failed: %+v err=%v", r, err)
	}
	var planRaw, digest string
	capture := &nativeStreamCapture{complete: true}
	if err := f.s.db.QueryRow(`SELECT native_plan_json,native_plan_sha256,stdout_blob,stderr_blob FROM worktree_verify_leases WHERE lease_id=?`, r.LeaseID).Scan(&planRaw, &digest, &capture.stdout, &capture.stderr); err != nil {
		t.Fatal(err)
	}
	var plan nativeOraclePlan
	if err := json.Unmarshal([]byte(planRaw), &plan); err != nil {
		t.Fatal(err)
	}
	req := f.request("unknown-alloc", &f.a)
	req.Command = r.Command
	req.nativePlanJSON, req.nativePlanSHA256 = planRaw, digest
	req.nativeCommandJSON = workflowJSON(req.Command)
	req.nativeAcceptedInputsDigest = nativeDigest([]byte(req.nativeCommandJSON + "\x00" + digest))
	req.nativeEvidenceRefsJSON = workflowJSON([]string{r.OperationRef})
	raw, err := marshalNativeVerifyRecord(&r, capture)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := r
	unavailableOracle := *r.Oracle
	unavailable.Oracle = &unavailableOracle
	unavailable.Oracle.Qualification = "unavailable"
	unavailableRaw, err := marshalNativeVerifyRecord(&unavailable, capture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`DELETE FROM durable_operations WHERE op_id=?`, r.OperationRef); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET state='held',outcome='running' WHERE lease_id=?`, r.LeaseID); err != nil {
		t.Fatal(err)
	}
	// A retained allocation that cannot build a profile must refuse the
	// release, never disarm it: found=false with an error is a corrupt
	// allocation, not a legacy pin.
	if _, err := f.s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1);INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at,architecture,revision,digest) VALUES(?,1,'corrupt allocation','[{"a":1}]','["a"]','2026-10-15T00:00:00Z',json('{}'),1,'sha256:`+strings.Repeat("0", 64)+`');DELETE FROM fold_guard`, f.work); err != nil {
		t.Fatal(err)
	}
	// The unknown allocation is truthfully armed: measured, with the withheld
	// variant bytes that persist the typed refusal for replay.
	stamp, measured, variant, variantRaw, capErr := f.s.architectureCapacityVerifyRefusal(context.Background(), req, r, capture, unavailable, unavailableRaw)
	if capErr == nil || !measured || variantRaw == nil {
		t.Fatalf("unknown retained allocation not armed: measured=%v raw=%v err=%v", measured, variantRaw != nil, capErr)
	}
	req.nativeCapacityStamp, req.nativeCapacityUnavailable, req.nativeCapacityRaw, req.nativeCapacityOver = stamp, variant, variantRaw, capErr
	entry, err := activeWorktreeEntryForProject(context.Background(), f.s.db, "test", f.work, "project")
	if err != nil {
		t.Fatal(err)
	}
	result, relErr := f.s.releaseNativeOracle(context.Background(), req, entry, oracleGitSubjectSnapshot{head: plan.SubjectCommit, clean: true}, plan.WorktreeIdentity, plan, r, raw, unavailable, unavailableRaw, capture, false)
	if relErr == nil {
		t.Fatalf("unknown retained allocation released without refusal: %+v", result.Oracle)
	}
	if result.Oracle == nil || result.Oracle.Qualification != "unavailable" {
		t.Fatalf("unknown allocation published: %+v", result.Oracle)
	}
	var green int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil {
		t.Fatal(err)
	}
	if green != 0 {
		t.Fatalf("unknown allocation appended authority: %d rows", green)
	}
	// The same lease replays the exact same typed refusal: truthful
	// unavailable receipt, no authority, no second execution.
	r2, err2 := f.s.VerifyWorktree(context.Background(), f.request("unknown-alloc", &f.a))
	if failureKind(err2) != failureKind(capErr) {
		t.Fatalf("unknown-allocation replay failure = %v, want the original %v (result %+v)", err2, failureKind(capErr), r2)
	}
	if r2.Oracle == nil || r2.Oracle.Qualification != "unavailable" {
		t.Fatalf("replay receipt = %+v, want truthful unavailable", r2.Oracle)
	}
	if f.launches != 1 {
		t.Fatalf("replay launched the program again: %d launches", f.launches)
	}
	if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil {
		t.Fatal(err)
	}
	if green != 0 {
		t.Fatalf("replay appended authority: %d rows", green)
	}
}

func TestArchitectureNativeCapacityStampFingerprintsReadyReceiptsByAllocation(t *testing.T) {
	f := newNativeOracleFixture(t)
	prepare := &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	legacyReq := f.request("stamp-legacy-prepare", prepare)
	// Legacy work (no retained architecture): the stamp's preparations
	// component was empty before the fingerprint existed and must stay
	// HEAD-equivalent across another real ready preparation landing.
	before, err := readNativeOracleReleaseStamp(context.Background(), f.s.db, legacyReq)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.VerifyWorktree(context.Background(), f.request("stamp-second-prepare", prepare))
	if err != nil || second.Oracle == nil || second.Oracle.Qualification != "ready" {
		t.Fatalf("second preparation: %+v err=%v", second.Oracle, err)
	}
	after, err := readNativeOracleReleaseStamp(context.Background(), f.s.db, legacyReq)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("legacy prepare stamp moved on a ready receipt: before=%+v after=%+v", before, after)
	}
	// With a retained architecture the unbound stamp MUST fingerprint every
	// ready preparation receipt, so a landing invalidates the proof.
	nativeCapacityRecordDesign(t, f.s, f.work, 8192)
	beforeArch, err := readNativeOracleReleaseStamp(context.Background(), f.s.db, legacyReq)
	if err != nil {
		t.Fatal(err)
	}
	third, err := f.s.VerifyWorktree(context.Background(), f.request("stamp-third-prepare", prepare))
	if err != nil || third.Oracle == nil || third.Oracle.Qualification != "ready" {
		t.Fatalf("third preparation: %+v err=%v", third.Oracle, err)
	}
	afterArch, err := readNativeOracleReleaseStamp(context.Background(), f.s.db, legacyReq)
	if err != nil {
		t.Fatal(err)
	}
	if afterArch == beforeArch || afterArch.Preparations == beforeArch.Preparations {
		t.Fatalf("architecture-present unbound stamp ignored a ready receipt: %+v", beforeArch)
	}
}
