package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func revisionRecoveryInvoke(t *testing.T, f *oAgentFixtureState, action, key string, fields map[string]any) agentcontract.Envelope {
	t.Helper()
	f.env.RequestID = "request:" + key
	if fields == nil {
		fields = map[string]any{}
	}
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": f.work, "expected_version": workVersion(t, f.s, f.work), "action_id": action, "idempotency_key": key, "fields": fields,
	})}
	response, err := Invoke(context.Background(), f.s, f.service, mustMarshalInvoke(t, request, f.env))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := response.Encode(); err != nil {
		t.Fatal(err)
	}
	return response
}

func prepareRevisionRecoveryOracle(t *testing.T, f *oAgentFixtureState, oracle store.AcceptanceOracle, key string) store.AcceptanceOracle {
	t.Helper()
	spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "command": []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, "idempotency_key": key + "-subject",
	}))
	var fresh store.AcceptanceOracle
	if err := json.Unmarshal(retryJSON(oracle), &fresh); err != nil {
		t.Fatal(err)
	}
	for i, control := range fresh.Controls {
		bundle, err := oracleFixtureControlBundle(&fresh, control.ControlID)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal(retryJSON(bundle), &input); err != nil {
			t.Fatal(err)
		}
		delete(input["control"].(map[string]any), "readiness_evidence_refs")
		response := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
			"work_id": f.work, "idempotency_key": fmt.Sprintf("%s-prepare-%d", key, i),
			"oracle": map[string]any{"phase": "prepare", "expected_contract_version": 1, "control_bundle": input},
		}))
		var result store.WorktreeVerifyResult
		if err := json.Unmarshal(response.Result, &result); err != nil || result.Oracle == nil || result.Oracle.Qualification != "ready" {
			t.Fatalf("prepare unchanged control: %s (%v)", response.Result, err)
		}
		fresh.Controls[i].ReadinessEvidenceRefs = []string{result.OperationRef}
	}
	return fresh
}

func revisionRecoveryState(t *testing.T, f *oAgentFixtureState) [5]int64 {
	t.Helper()
	state := [5]int64{workVersion(t, f.s, f.work)}
	for i, query := range []string{`SELECT last FROM store_sequences WHERE name='effect'`, `SELECT count(*) FROM worker_job_revisions`, `SELECT count(*) FROM worker_attempts`, `SELECT count(*) FROM worker_authorizations`} {
		if err := f.s.DatabaseForTesting().QueryRow(query).Scan(&state[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func TestThreeControlFailedRevisionRecovery(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_transition", "worker_dispatch"})
	grant.Worktree = seedWorkerRetryMutationFixture(t, s, grant)
	f := oAgentFixture(t, s, service, mutationEnvelope(grant, mustScopeVersion(t, s)), "work-1")
	spineOK(t, revisionRecoveryInvoke(t, f, "start_execution", "recovery-start", nil))
	oracle := oAgentPreparedOracle(t, f, agentContractPredicates(t, s, f.work))
	for i := 1; i < 3; i++ {
		control := oracle.Controls[0]
		control.ControlID = fmt.Sprintf("control:recovery-%d", i)
		caseID := fmt.Sprintf("case:recovery-%d", i)
		control.CaseIDs = []string{caseID}
		control.RecipeSource.Path = fmt.Sprintf("recovery-%d.json", i)
		manifest := fmt.Sprintf(`{"kind":"go_top_level_v1","package_cwd":"o_fixture","test_files":["o_fixture/value_test.go"],"fixture_files":["o_fixture/oracle.json"],"cases":{%q:["TestValue"]}}`, caseID)
		if err := os.WriteFile(filepath.Join(f.path, control.RecipeSource.Path), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		oracle.Controls = append(oracle.Controls, control)
		entry := oracle.Cases[0]
		entry.CaseID, entry.ControlIDs = caseID, []string{control.ControlID}
		oracle.Cases = append(oracle.Cases, entry)
	}
	gitRun(t, f.path, "add", "recovery-1.json", "recovery-2.json")
	gitRun(t, f.path, "commit", "-qm", "fixture three controls")
	for i := range oracle.Controls {
		oracle.Controls[i].RecipeSource.CommitOID = gitRun(t, f.path, "rev-parse", "HEAD")
	}
	oracle = prepareRevisionRecoveryOracle(t, f, oracle, "three-control-original")
	var oracleFields map[string]any
	if err := json.Unmarshal(retryJSON(oracle), &oracleFields); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"job_id": "job:revision-recovery", "objective": "Carry out the unchanged approved objective.", "stopping_condition": "The recorded value check passes.",
		"checks": []string{"go test ./..."}, "ready": true, "readiness_evidence": []string{"contract:work-1:1"}, "acceptance_oracle": oracleFields,
	}
	addAgentJobCallerMembers(t, s, f.work, fields)
	fields["path_scope"] = append(fields["path_scope"].([]string), "candidate.txt")
	failures := make([]string, 0, 2)
	bindings := make([]store.WorkerJobBinding, 0, 2)
	for revision := 1; revision <= 2; revision++ {
		spineOK(t, revisionRecoveryInvoke(t, f, "record_worker_job", fmt.Sprintf("record-original-%d", revision), fields))
		oAgentReviewJob(t, f, "job:revision-recovery", "continue", false)
		attempt := fmt.Sprintf("attempt:recovery-failed-%d", revision)
		_, binding, _ := f.startWorker(t, "implement", attempt)
		failure := agentWorkerFailedAttributedEvent("failed-recovery-"+attempt, store.WorkerFailedPayload{
			AttemptID: attempt, ReadbackModel: "openai/test-model", FailureKind: store.WorkerFailureWorkerError, Detail: "synthetic implementation failure",
		})
		if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
			_, err := store.ApplyWorkerEvidenceOperationTx(context.Background(), tx, failure)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		failures = append(failures, attempt)
		bindings = append(bindings, *binding)
	}
	correction := f.read(t).Pinned.Pin.Correction
	if correction == nil || correction.FailedAttemptID != failures[1] {
		t.Fatalf("fixture lacks revision2 failed-attempt debt: %+v", correction)
	}
	// A Review owns the candidate change. Native preparation cannot replace
	// Main's accepted candidate by itself.
	inputCandidate := store.WorkerReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, f.path, "rev-parse", "HEAD"), Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	dependencies := *f.read(t).Pinned.Pin.Review.Dependencies
	const movingReview = "attempt:recovery-moving-review"
	reviewLane, reviewJob, reviewEpoch := f.startWorker(t, "review", movingReview)
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, f.work, movingReview).Scan(&reviewEpoch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.path, "candidate.txt"), []byte("moved candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "candidate.txt")
	gitRun(t, f.path, "commit", "-qm", "fixture moved candidate")
	outputCandidate := store.WorkerReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, f.path, "rev-parse", "HEAD"), Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	f.finishWorker(t, reviewLane, movingReview, reviewJob, &store.WorkerReviewBlock{
		Disposition: "continue", InputCandidate: inputCandidate, OutputCandidate: outputCandidate, Dependencies: dependencies,
		Assessments: []store.WorkerReviewAssessment{}, Findings: []store.WorkerReviewFinding{}, RemainingObligations: []store.WorkerReviewObligation{},
	})
	fresh := prepareRevisionRecoveryOracle(t, f, oracle, "three-control-current")
	spineOK(t, revisionRecoveryInvoke(t, f, "accept_worker_result", "accept-moving-review", map[string]any{"attempt_id": movingReview, "attempt_epoch": reviewEpoch}))
	fields["repair_revisions"], fields["repair_reason"] = []int64{1, 2}, "the caller selected checks repair for a code correction"
	checkCorrection := func(t *testing.T, response agentcontract.Envelope, field, remedy string, before [5]int64) {
		t.Helper()
		if response.Error == nil || response.Error.Kind != "invalid_input" || response.Error.EffectState != agentcontract.EffectNone || response.Error.RetrySafe || response.Error.RecoveryAction.Kind != "correct_request" || !strings.Contains(response.Error.Message, field) || !strings.Contains(response.Error.Message, remedy) {
			t.Fatalf("missing executable field correction: %+v", response.Error)
		}
		if after := revisionRecoveryState(t, f); after != before {
			t.Fatalf("refusal changed version/effects/jobs/attempts/authorizations: %v -> %v", before, after)
		}
	}
	oldRoute := t.Run("old_refs_names_current_preparation_and_ordinary_revision", func(t *testing.T) {
		before := revisionRecoveryState(t, f)
		checkCorrection(t, revisionRecoveryInvoke(t, f, "record_worker_job", "correct-revision", fields), "readiness_evidence_refs", "ordinary", before)
	})
	fields["acceptance_oracle"] = fresh
	freshRoute := t.Run("fresh_refs_names_repair_classification_correction", func(t *testing.T) {
		before := revisionRecoveryState(t, f)
		checkCorrection(t, revisionRecoveryInvoke(t, f, "record_worker_job", "correct-revision", fields), "repair_revisions", "repair_reason", before)
	})
	if !oldRoute || !freshRoute {
		return
	}
	delete(fields, "repair_revisions")
	delete(fields, "repair_reason")
	before := revisionRecoveryState(t, f)
	spineOK(t, revisionRecoveryInvoke(t, f, "record_worker_job", "correct-revision", fields))
	after := revisionRecoveryState(t, f)
	if after != [5]int64{before[0] + 2, before[1] + 2, before[2] + 1, before[3], before[4]} {
		t.Fatalf("correction created an unintended effect: %v -> %v", before, after)
	}
	state := f.read(t)
	if len(state.Pinned.Jobs) != 1 || state.Pinned.Jobs[0].Revision != 3 || state.Pinned.Jobs[0].JobID != "job:revision-recovery" || state.Pinned.Contract.Version != 1 || len(state.Pinned.Jobs[0].AcceptanceOracle.Controls) != 3 {
		t.Fatalf("correction did not record same-job revision3: %+v", state.Pinned)
	}
	packet := retryMutationPacket(t, s, "attempt:recovery-before-review", state.Pinned.Pin.Correction)
	refused := revisionRecoveryInvoke(t, f, "dispatch_worker", "must-review-revision3", map[string]any{"attempt_id": "attempt:recovery-before-review", "worker_packet": packet})
	if refused.Error == nil || refused.Error.Kind != "missing_evidence" || refused.Error.RecoveryAction.Kind != "provide_evidence" || refused.Error.EffectState != agentcontract.EffectNone || revisionRecoveryState(t, f) != after || !strings.Contains(refused.Error.Message, "accepted Review") {
		t.Fatalf("correction bypassed Review: %+v", refused.Error)
	}
	oAgentReviewJob(t, f, "job:revision-recovery", "continue", false)
	if current := f.read(t).Pinned.Pin.Correction; !reflect.DeepEqual(current, correction) {
		t.Fatalf("permission-only Review changed failed debt: before=%+v after=%+v", correction, current)
	}
	for i, binding := range bindings {
		var digest, state, terminal string
		if err := s.DatabaseForTesting().QueryRow(`SELECT digest,state FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, f.work, binding.JobID, binding.Revision).Scan(&digest, &state); err != nil || digest != binding.Digest || state != "recorded" {
			t.Fatalf("failed revision %d changed or satisfied: %s %s (%v)", i+1, digest, state, err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, f.work, failures[i]).Scan(&terminal); err != nil || terminal != "failed" {
			t.Fatalf("failed attempt %d changed: %s (%v)", i+1, terminal, err)
		}
	}
	const implementation = "attempt:recovery-implementation3"
	packet = retryMutationPacket(t, s, implementation, correction)
	var canonicalPacket map[string]any
	if err := json.Unmarshal(retryJSON(packet), &canonicalPacket); err != nil {
		t.Fatal(err)
	}
	spineOK(t, revisionRecoveryInvoke(t, f, "dispatch_worker", "dispatch-implementation3", map[string]any{"attempt_id": implementation, "worker_packet": packet}))
	var authorization struct {
		WorkerJob    store.WorkerJobBinding `json:"worker_job"`
		PacketDigest string                 `json:"worker_packet_digest"`
	}
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, f.work, implementation).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &authorization) != nil || authorization.WorkerJob.Revision != 3 || authorization.WorkerJob.JobID != "job:revision-recovery" || authorization.PacketDigest != oracleFixtureDigest(retryJSON(canonicalPacket)) {
		t.Fatalf("implementation did not bind revision3 and the exact packet carrying failed correction context: %s (%v)", raw, err)
	}
}
