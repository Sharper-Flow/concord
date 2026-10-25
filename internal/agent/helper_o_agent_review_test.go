package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// oAgentFixture keeps Review setup local to the O journeys. Reads use the
// store projection; writes and native preparation use the agent boundary.
type oAgentFixtureState struct {
	*spineFixture
	preparationRef string
	preparationKey string
}

func oAgentFixture(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID string) *oAgentFixtureState {
	t.Helper()
	path, branch := oracleFixtureActiveClaimFor(t, s, workID)
	fixtureClaimSubject(t, path)
	if gitRun(t, path, "branch", "--show-current") != branch {
		gitRun(t, path, "branch", "-m", branch)
	}
	// Refine acceptance resolves checks from the Project's default ref, not
	// from the claimed candidate or the knowledge repository's mere presence.
	var canonical string
	if err := s.DatabaseForTesting().QueryRow(`SELECT normalized_value FROM project_locators WHERE kind=? AND project_id='project-1' ORDER BY locator_id LIMIT 1`, store.LocatorCanonicalPath).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	fixtureClaimSubject(t, canonical)
	manifest := filepath.Join(canonical, ".concord/tooling.v1.json")
	if _, err := os.Stat(manifest); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifest, []byte(`{"schema_version":"1.0","project":"fixture","tools":[{"id":"go-test","invocation":"go test ./..."},{"id":"go-vet","invocation":"go vet ./..."}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, canonical, "add", ".concord/tooling.v1.json")
		gitRun(t, canonical, "commit", "-qm", "fixture declared checks")
	}
	if err := exec.Command("git", "-C", canonical, "remote", "get-url", "origin").Run(); err != nil {
		seedClaimOrigin(t, canonical)
	} else {
		gitRun(t, canonical, "push", "-q", "origin", "HEAD:main")
		gitRun(t, canonical, "fetch", "-q", "origin")
		gitRun(t, canonical, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	}
	env.Directory, env.Worktree = path, path
	env.ScopeVersion = mustScopeVersion(t, s)
	return &oAgentFixtureState{spineFixture: &spineFixture{s: s, service: service, env: env, work: workID, path: path, base: gitRun(t, path, "rev-parse", "HEAD"), request: int(workVersion(t, s, workID) * 100)}}
}

func oAgentAction(t *testing.T, f *oAgentFixtureState, action string, fields map[string]any) Envelope {
	t.Helper()
	response := f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": workVersion(t, f.s, f.work), "action_id": action,
		"fields": fields, "idempotency_key": fmt.Sprintf("o-agent-%s-%d", action, f.request),
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("O %s on %s refused: %+v", action, f.work, response.Error)
	}
	return response
}

// oAgentNativeJob prepares a self-contained real Go control. No receipt or
// Review projection is inserted: the host producer owns preparation bytes.
func oAgentNativeJob(t *testing.T, f *oAgentFixtureState) {
	t.Helper()
	oAgentControlRepository(t, f)
	f.base = gitRun(t, f.path, "rev-parse", "HEAD")
	spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "command": []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, "idempotency_key": fmt.Sprintf("o-subject-%d", f.request),
	}))
	snapshot, err := store.ReadWorkflowContinuity(context.Background(), f.s, store.ContinuityRequest{Work: f.work})
	if err != nil || snapshot.Contract == nil {
		t.Fatalf("O native job contract: %v", err)
	}
	var domain string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT root_domain_id FROM domain_registries WHERE product_id='product-1'`).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	owner := store.OracleOwner{OwnerID: "owner:o-agent-value", DomainID: domain,
		Mechanism:  store.OracleMechanism{ProjectID: "project-1", Path: "o_fixture/value.go", EntryPoint: "Value"},
		Obligation: "The fixture value satisfies the approved check.", PredicateIDs: []string{"predicate:primary"}}
	control := store.OracleControl{ControlID: "control:o-agent-value", OwnerID: owner.OwnerID, PredicateIDs: owner.PredicateIDs,
		CaseIDs: []string{"case:o-agent-value"}, Cwd: "o_fixture", Argv: []string{"go", "test", "-count=1", "-run", "^(TestValue)$", "."},
		RecipeSource:   store.WorkContextReadingSource{Kind: store.WorkContextSourceRepositoryFile, ProjectID: "project-1", Path: "o_fixture/oracle.json", CommitOID: f.base},
		ExpectedResult: store.OracleExpectedResultPass, RequiredEvidenceRole: store.OracleEvidenceRoleReported}
	cases := []store.OracleCase{{CaseID: "case:o-agent-value", OwnerID: owner.OwnerID, EntryPoint: "Value", InputClass: "fixture value", ExpectedState: "value is seven", ControlIDs: []string{control.ControlID}}}
	var bundle map[string]any
	if err := json.Unmarshal(retryJSON(store.NativeOracleControlBundle{Owner: owner, Control: control, Cases: cases}), &bundle); err != nil {
		t.Fatal(err)
	}
	delete(bundle["control"].(map[string]any), "readiness_evidence_refs")
	// A new job revision on unchanged inputs consumes the same qualified
	// preparation; preparation is not a per-consumer acceptance execution.
	key := fmt.Sprintf("%s:%d", f.base, snapshot.Contract.Version)
	if f.preparationRef == "" || f.preparationKey != key {
		prepared := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
			"work_id": f.work, "idempotency_key": fmt.Sprintf("o-prepare-%d", f.request),
			"oracle": map[string]any{"phase": "prepare", "expected_contract_version": snapshot.Contract.Version, "control_bundle": bundle},
		}))
		var result store.WorktreeVerifyResult
		if err := json.Unmarshal(prepared.Result, &result); err != nil || result.Oracle == nil || result.Oracle.Qualification != "ready" {
			t.Fatalf("O native preparation: %s (%v)", prepared.Result, err)
		}
		f.preparationRef, f.preparationKey = result.OperationRef, key
	}
	control.ReadinessEvidenceRefs = []string{f.preparationRef}
	spineOK(t, oAgentAction(t, f, "record_worker_job", map[string]any{
		"job_id": "job:o-agent-native", "objective": "Check the approved fixture value.", "stopping_condition": "The value check passes.",
		"ready": true, "checks": []string{"go test ./..."}, "path_scope": []string{"o_fixture/value.go"},
		"readiness_evidence": []string{f.preparationRef},
		"acceptance_oracle":  store.AcceptanceOracle{Owners: []store.OracleOwner{owner}, Controls: []store.OracleControl{control}, Cases: cases},
	}))
}

func oAgentControlRepository(t *testing.T, f *oAgentFixtureState) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.path, "o_fixture/oracle.json")); os.IsNotExist(err) {
		for name, content := range map[string]string{
			"go.mod":                  "module example.invalid/o-fixture\n\ngo 1.26.0\ntoolchain " + goruntime.Version() + "\n",
			"o_fixture/value.go":      "package fixture\nfunc Value() int { return 7 }\n",
			"o_fixture/value_test.go": "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != 7 { t.Fatal(\"wrong value\") } }\n",
			"o_fixture/oracle.json":   `{"kind":"go_top_level_v1","package_cwd":"o_fixture","test_files":["o_fixture/value_test.go"],"fixture_files":[],"cases":{"case:o-agent-value":["TestValue"]}}`,
		} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(f.path, name)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.path, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		gitRun(t, f.path, "add", "go.mod", "o_fixture")
		gitRun(t, f.path, "commit", "-qm", "fixture native control")
	}
}

// oAgentReview accepts an unchanged-candidate Review of the explicit current
// job. Assessments cite either this dispatch's native execution or the actual
// command receipts the caller asks Review to assess on this candidate.
func oAgentReview(t *testing.T, f *oAgentFixtureState, disposition string, prove bool, receipts ...string) {
	t.Helper()
	snapshot, err := store.ReadWorkflowContinuity(context.Background(), f.s, store.ContinuityRequest{Work: f.work})
	if err != nil || len(snapshot.ReadyWorkerJobs) == 0 {
		t.Fatalf("O Review ready job: %v (%d jobs)", err, len(snapshot.ReadyWorkerJobs))
	}
	selected := snapshot.ReadyWorkerJobs[len(snapshot.ReadyWorkerJobs)-1]
	selected.CallerExclusions = nil
	job := &store.WorkerJobBinding{JobID: selected.JobID, Revision: selected.Revision, Digest: selected.Digest}
	lane := deliveryRecoveryLane(t, "review")
	attempt := fmt.Sprintf("attempt:o-agent-review-%d", workVersion(t, f.s, f.work))
	pin, err := store.ReadWorkPin(context.Background(), f.s, f.work)
	if err != nil || pin.Review == nil || pin.Review.Dependencies == nil {
		t.Fatalf("O Review dependencies: %v", err)
	}
	inputs := map[string]any{"worker_job": retryJSON(selected)}
	if pin.Correction != nil {
		inputs["correction"] = pin.Correction
	}
	packet := bindPacketToRecordedState(t, f.s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion,
		"attempt_id": attempt, "lane_id": lane.ID, "lane_digest": lane.Digest,
		"work_id": f.work, "step_id": snapshot.WorkflowStep, "inputs": inputs})
	authorized := spineOK(t, oAgentAction(t, f, "dispatch_worker", map[string]any{"attempt_id": attempt, "worker_packet": packet}))
	var authorization struct {
		PacketDigest string `json:"worker_packet_digest"`
	}
	if err := json.Unmarshal(authorized.Result, &authorization); err != nil || authorization.PacketDigest == "" {
		t.Fatalf("O Review authorization: %s (%v)", authorized.Result, err)
	}
	var epoch int64
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, f.work, store.WorkflowActionCompleted, attempt).Scan(&epoch); err != nil || epoch == 0 {
		t.Fatalf("O Review epoch: %d (%v)", epoch, err)
	}
	dispatch := store.Event{EventID: "o-host-dispatched-" + attempt, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: f.work,
		Actor: "worker:test", OccurredAt: f.service.Now(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{
			AttemptID: attempt, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
			PacketDigest: authorization.PacketDigest, ReadbackModel: "openai/test-model", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
	if err := f.s.Transact(context.Background(), func(tx *store.Transaction) error {
		pair, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, f.env.PrincipalRef, f.env.ClientRef)
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(context.Background(), tx, pair)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	candidate := store.WorkerReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, f.path, "rev-parse", "HEAD"), Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	report := &store.WorkerReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: *pin.Review.Dependencies,
		Assessments: []store.WorkerReviewAssessment{}, Findings: []store.WorkerReviewFinding{}, RemainingObligations: []store.WorkerReviewObligation{}}
	if prove {
		control := selected.AcceptanceOracle.Controls[0]
		executed := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
			"work_id": f.work, "idempotency_key": fmt.Sprintf("o-execute-%d", f.request), "oracle": map[string]any{
				"phase": "execute", "attempt_id": attempt, "attempt_epoch": epoch, "worker_packet_digest": authorization.PacketDigest,
				"worker_job_binding": job, "control_id": control.ControlID, "preparation_run_ref": control.ReadinessEvidenceRefs[0],
			},
		}))
		var result store.WorktreeVerifyResult
		if err := json.Unmarshal(executed.Result, &result); err != nil || result.ExitCode != 0 || result.Oracle == nil || result.Oracle.Qualification != "pass" {
			t.Fatalf("O native execution: %s (%v)", executed.Result, err)
		}
		report.Assessments = []store.WorkerReviewAssessment{{PredicateID: "predicate:primary", Assessment: "supported", ReceiptRefs: []string{result.OperationRef}}}
	}
	if len(receipts) > 0 {
		report.Assessments = []store.WorkerReviewAssessment{{PredicateID: "predicate:primary", Assessment: "supported", ReceiptRefs: receipts}}
	}
	f.finishWorker(t, lane, attempt, job, report)
	spineOK(t, oAgentAction(t, f, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}))
}

func oAgentVerify(t *testing.T, f *oAgentFixtureState, command []string) string {
	t.Helper()
	response := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "command": command, "idempotency_key": fmt.Sprintf("o-verify-%d", f.request),
	}))
	var result store.WorktreeVerifyResult
	if err := json.Unmarshal(response.Result, &result); err != nil || result.ExitCode != 0 || result.TrackedFilesChanged || result.OperationRef == "" {
		t.Fatalf("O candidate verification: %s (%v)", response.Result, err)
	}
	pin, err := store.ReadWorkPin(context.Background(), f.s, f.work)
	if err != nil || pin.WorkContext == nil || pin.WorkContext.SubjectCommit != gitRun(t, f.path, "rev-parse", "HEAD") {
		t.Fatalf("O verification subject: %+v (%v)", pin.WorkContext, err)
	}
	return result.OperationRef
}
