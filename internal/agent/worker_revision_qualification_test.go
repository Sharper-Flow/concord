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

// These are counterfactual requests against an isolated synthetic store, not
// retries admitted by the recovery action of an earlier refused request.
func TestMovedSubjectWorkerRevisionQualification(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_transition", "worker_dispatch"})
	_, failedAttempt, path := seedFailedWorkerRetryMutation(t, s, service, grant)
	grant.Worktree = path
	f := oAgentClaimFixture(t, s, service, mutationEnvelope(grant, mustScopeVersion(t, s)), "work-1")
	original := f.read(t).Pinned.Jobs
	if len(original) != 1 || original[0].AcceptanceOracle == nil {
		t.Fatalf("failed fixture ready jobs: %+v", original)
	}
	job := original[0]
	oldOracle := *job.AcceptanceOracle
	oldSubject := gitRun(t, path, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(path, "candidate.txt"), []byte("moved clean candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, path, "add", "candidate.txt")
	gitRun(t, path, "commit", "-qm", "fixture moved candidate")
	currentSubject := gitRun(t, path, "rev-parse", "HEAD")
	if oldSubject == currentSubject || gitRun(t, path, "status", "--porcelain") != "" {
		t.Fatal("fixture did not move to a different clean subject")
	}
	spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "command": []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, "idempotency_key": "qualify-moved-subject",
	}))
	var freshOracle store.AcceptanceOracle
	if err := json.Unmarshal(retryJSON(oldOracle), &freshOracle); err != nil {
		t.Fatal(err)
	}
	for i, control := range freshOracle.Controls {
		control.ReadinessEvidenceRefs = nil
		bundle := store.NativeOracleControlBundle{Owner: freshOracle.Owners[0], Control: control, Cases: freshOracle.Cases}
		var input map[string]any
		if err := json.Unmarshal(retryJSON(bundle), &input); err != nil {
			t.Fatal(err)
		}
		delete(input["control"].(map[string]any), "readiness_evidence_refs")
		prepared := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
			"work_id": f.work, "idempotency_key": fmt.Sprintf("prepare-moved-%d", i),
			"oracle": map[string]any{"phase": "prepare", "expected_contract_version": 1, "control_bundle": input},
		}))
		var result store.WorktreeVerifyResult
		if err := json.Unmarshal(prepared.Result, &result); err != nil || result.Oracle == nil || result.Oracle.Qualification != "ready" {
			t.Fatalf("fresh unchanged bundle preparation: %s (%v)", prepared.Result, err)
		}
		freshOracle.Controls[i].ReadinessEvidenceRefs = []string{result.OperationRef}
	}
	// Preserve every recipe commit and obligation. Only candidate-bound refs vary.
	normalized := freshOracle
	normalized.Controls = append([]store.OracleControl(nil), freshOracle.Controls...)
	for i := range normalized.Controls {
		normalized.Controls[i].ReadinessEvidenceRefs = oldOracle.Controls[i].ReadinessEvidenceRefs
	}
	if !reflect.DeepEqual(oldOracle, normalized) {
		t.Fatal("fresh preparation changed an oracle obligation")
	}
	fields := func(oracle store.AcceptanceOracle, repair bool) map[string]any {
		out := map[string]any{
			"job_id": job.JobID, "objective": job.Objective, "stopping_condition": job.StoppingCondition,
			"path_scope": job.PathScope, "predicate_ids": job.PredicateIDs, "checks": job.Checks,
			"prerequisites": job.Prerequisites, "unresolved_refs": job.UnresolvedRefs,
			"ready": true, "readiness_evidence": []string{"contract:work-1:1"}, "acceptance_oracle": oracle,
		}
		if job.ReservedIntegration != "" {
			out["reserved_integration"] = job.ReservedIntegration
		}
		if job.DirectCallerAllowance != nil {
			out["direct_caller_allowance"] = job.DirectCallerAllowance
		}
		if repair {
			out["repair_revisions"] = []int64{job.Revision}
			out["repair_reason"] = "synthetic caller selected checks repair for a code correction"
		}
		return out
	}
	invoke := func(t *testing.T, action, key string, values map[string]any) agentcontract.Envelope {
		t.Helper()
		f.env.RequestID = "request:" + key
		request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
			"work_id": f.work, "expected_version": workVersion(t, s, f.work), "action_id": action, "idempotency_key": key, "fields": values,
		})}
		response, err := Invoke(context.Background(), s, service, mustMarshalInvoke(t, request, f.env))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := response.Encode(); err != nil {
			t.Fatal(err)
		}
		return response
	}
	snapshot := func(t *testing.T) [5]int64 {
		t.Helper()
		state := [5]int64{workVersion(t, s, f.work)}
		for i, query := range []string{
			`SELECT last FROM store_sequences WHERE name='effect'`,
			`SELECT count(*) FROM worker_job_revisions`, `SELECT count(*) FROM worker_attempts`, `SELECT count(*) FROM worker_authorizations`,
		} {
			if err := s.DatabaseForTesting().QueryRow(query).Scan(&state[i+1]); err != nil {
				t.Fatal(err)
			}
		}
		return state
	}
	refusal := func(t *testing.T, response agentcontract.Envelope, kind, recovery, message, detail string, before [5]int64) {
		t.Helper()
		if response.Error == nil || response.Error.Kind != kind || response.Error.EffectState != agentcontract.EffectNone || response.Error.RetrySafe || response.Error.RecoveryAction.Kind != recovery || !strings.Contains(response.Error.Message, message) || response.Error.Details["recovery_detail"] != detail {
			t.Fatalf("unexpected qualification refusal: %+v", response.Error)
		}
		if after := snapshot(t); after != before {
			t.Fatalf("refusal changed version/effects/jobs/attempts/authorizations: %v -> %v", before, after)
		}
		t.Logf("%s/%s/none retry_safe=false message=%q detail=%q unchanged_state=%v", kind, recovery, response.Error.Message, detail, before)
	}
	t.Run("checks_repair_old_refs", func(t *testing.T) {
		before := snapshot(t)
		refusal(t, invoke(t, "record_worker_job", "checks-repair-old", fields(oldOracle, true)), "invalid_input", "reread_entities", "is not its exact qualified native preparation", "prepare the exact owner/case/control bundle before recording the ready job", before)
	})
	t.Run("checks_repair_refreshed_refs_only", func(t *testing.T) {
		before := snapshot(t)
		refusal(t, invoke(t, "record_worker_job", "checks-repair-fresh", fields(freshOracle, true)), "invalid_input", "reread_entities", "a checks repair carries the named revisions' byte-identical acceptance oracle", "record the repair with the named revisions' frozen oracle", before)
	})
	t.Run("old_revision_dispatch_projection", func(t *testing.T) {
		before := snapshot(t)
		packet := retryMutationPacket(t, s, "attempt:qualification-dispatch", f.read(t).Pinned.Pin.Correction)
		refusal(t, invoke(t, "dispatch_worker", "dispatch-old-revision", map[string]any{"attempt_id": "attempt:qualification-dispatch", "worker_packet": packet}), "missing_evidence", "provide_evidence", "job preparation is absent, foreign, or stale for its bundle and candidate", "prepare the exact current candidate and bundle before dispatch", before)
	})
	t.Run("ordinary_same_job_revision", func(t *testing.T) {
		before := snapshot(t)
		response := invoke(t, "record_worker_job", "ordinary-fresh-revision", fields(freshOracle, false))
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("ordinary revision failed: %+v", response.Error)
		}
		after := snapshot(t)
		if after[0] <= before[0] || after[2] != before[2]+1 || after[3] != before[3] || after[4] != before[4] {
			t.Fatalf("ordinary revision effects: %v -> %v", before, after)
		}
		state := f.read(t)
		if state.Pinned.Contract.Version != 1 || len(state.Pinned.Jobs) != 1 || state.Pinned.Jobs[0].JobID != job.JobID || state.Pinned.Jobs[0].Revision != job.Revision+1 || state.Pinned.Jobs[0].Digest == job.Digest || !reflect.DeepEqual(state.Pinned.Jobs[0].AcceptanceOracle, &freshOracle) {
			t.Fatalf("ordinary revision did not retain same job/contract and exact refreshed oracle: %+v", state.Pinned)
		}
		var terminal string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, f.work, failedAttempt).Scan(&terminal); err != nil || terminal != "failed" {
			t.Fatalf("original failed attempt changed: state=%s err=%v", terminal, err)
		}
		t.Logf("ordinary revision admitted same job %s@%d contract=1; failed attempt preserved; state %v -> %v", job.JobID, job.Revision+1, before, after)
	})
	t.Run("revised_job_dispatch_projection", func(t *testing.T) {
		before := snapshot(t)
		packet := retryMutationPacket(t, s, "attempt:qualification-revised", f.read(t).Pinned.Pin.Correction)
		response := invoke(t, "dispatch_worker", "dispatch-revised-job", map[string]any{"attempt_id": "attempt:qualification-revised", "worker_packet": packet})
		refusal(t, response, "missing_evidence", "provide_evidence", "a changed worker-job revision requires a current accepted Review", "use typed dispatch_worker with lane_id=review for the existing scoped job and run a native concord-review Task subagent under Main's own session (finish an already dispatched undispositioned attempt only if it meets this gate's required coverage and disposition; do not dispatch twice), then have Main accept its exact Review report covering the revised job before implementation dispatch or execution exit; no separate agent or RTE session is required", before)
	})
}
