package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// This file binds AJ8-health-failure-rollback: the approved production change
// is applied, health fails, and the native authority rolls the change back.
// Concord never performs, probes, or verifies any of it (CD-0039 D9): it
// records the attributed reports and classifies the logical operation partial
// in the same transaction. The interesting half is what the caller is told —
// outcome partial, the native change durably rolled_back, the health failure
// and rollback result attributed — with no adapter-side domain inference.

func digestOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// nativeRunAction drives one reporting workflow action and returns its
// envelope, the resulting work version, and the durable operation ID.
func nativeRunAction(t *testing.T, s *store.Store, service *Service, env CallEnvelope, version int64, actionID string, report map[string]any) (Envelope, int64, string, json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	runID, _ := report["run_id"].(string)
	key := actionID
	if runID != "" {
		key = actionID + "-" + runID
	}
	input, _ := json.Marshal(map[string]any{
		"work_id":          "work-1",
		"expected_version": version,
		"action_id":        actionID,
		"idempotency_key":  "aj8-native-" + key,
		"fields":           report,
	})
	resp, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if err != nil {
		t.Fatalf("%s dispatch: %v", actionID, err)
	}
	next, err := workVersionForWorkflow(t, s)
	if err != nil {
		t.Fatal(err)
	}
	return resp, next, "workflow-" + mutationDigest("concord_work_transition", "workflow_action", env, input)[7:31], input
}

func bindAJ8HealthFailureRollback(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	ctx := context.Background()

	if approval := sc.InitialState["approval"]; approval != "valid" {
		t.Fatalf("AJ8-health-failure-rollback expects a valid approval, got %+v", approval)
	}
	if authority := sc.InitialState["native_authority"]; authority != "routing-provider" {
		t.Fatalf("AJ8-health-failure-rollback expects the routing-provider authority, got %+v", authority)
	}
	if health := sc.InitialState["health_after_apply"]; health != "failed" {
		t.Fatalf("AJ8-health-failure-rollback expects failed health, got %+v", health)
	}
	if rollback := sc.InitialState["rollback"]; rollback != "declared" {
		t.Fatalf("AJ8-health-failure-rollback expects a declared rollback, got %+v", rollback)
	}

	// The change is approved: the consent gate is genuinely consumed before
	// the run reports anything.
	s, service, env, version, privateKey := seedOpsRunbookAtApprovalGate(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	version = consumeOpsRunbookConsent(t, s, service, env, version, privateKey, scopeVersion)

	const runID = "run-routing-prod-1"
	subjectRef := "routing://prod"
	subjectDigest := digestOf(subjectRef + ":v2")
	asserted := fixedTime().Format(time.RFC3339)

	// The run applies the approved change and reports it started.
	start, version, startOpID, _ := nativeRunAction(t, s, service, env, version, "start_run", map[string]any{
		"run_id": runID, "native_subject_ref": subjectRef, "subject_digest": subjectDigest,
		"status": "started", "asserted_at": asserted,
		"evidence_ref": "routing-provider://runs/" + runID, "evidence_digest": digestOf(runID + ":start"),
		"capture_method": "trusted_client_report",
	})
	if start.Outcome != OutcomeOK {
		t.Fatalf("start_run failed: outcome=%s err=%+v", start.Outcome, start.Error)
	}

	// The run declares it is awaiting the authority's health report — the
	// declared remote-state condition of the apply-then-verify contract —
	// which advances the runbook from execution to the health step. The
	// condition's resolution authority is the started run's durable operation,
	// so the report resolves against the run that produced it.
	awaited, version, _, _ := nativeRunAction(t, s, service, env, version, "add_condition", map[string]any{
		"condition_id":         "condition:await-health-" + runID,
		"await_type":           "remote_work_state",
		"await_ref":            "routing-provider://health/" + runID,
		"resolution_authority": "durable_operation:" + startOpID,
	})
	if awaited.Outcome != OutcomeOK {
		t.Fatalf("awaiting the health report failed: outcome=%s err=%+v", awaited.Outcome, awaited.Error)
	}
	if step := workflowCurrentStep(t, s, "work-1"); step != "health" {
		t.Fatalf("awaiting the health report did not reach the health step: %q", step)
	}

	// Health verification fails. Recording the failure is itself a successful
	// action; the durable classification turns partial because the approved
	// logical operation can no longer succeed.
	health, version, _, _ := nativeRunAction(t, s, service, env, version, "record_health", map[string]any{
		"run_id": runID, "native_subject_ref": subjectRef, "subject_digest": subjectDigest,
		"status": "failed", "asserted_at": asserted,
		"observation_ref": "routing-provider://health/" + runID, "observation_digest": digestOf(runID + ":health"),
		"evidence_ref": "routing-provider://health/" + runID, "evidence_digest": digestOf(runID + ":health"),
		"capture_method": "trusted_client_report",
	})
	if health.Outcome != OutcomePartial {
		t.Fatalf("failed health did not classify partial: outcome=%s err=%+v", health.Outcome, health.Error)
	}
	if health.Error == nil || health.Error.Kind != "operation_conflict" || health.Error.EffectState != EffectPartial {
		t.Fatalf("failed health partial shape is wrong: %+v", health.Error)
	}

	// The declared rollback runs and reports the change rolled back. The
	// classification stays partial: the run's steps completed, but the
	// approved change did not succeed — partial does not claim the rollback
	// was incomplete (CD-0039 D7).
	rollback, _, _, rollbackInput := nativeRunAction(t, s, service, env, version, "rollback_run", map[string]any{
		"run_id": runID, "native_subject_ref": subjectRef, "subject_digest": subjectDigest,
		"status": "rolled_back", "asserted_at": asserted,
		"evidence_ref": "routing-provider://rollback/" + runID, "evidence_digest": digestOf(runID + ":rollback"),
		"capture_method": "trusted_client_report",
	})
	if rollback.Outcome != OutcomePartial {
		t.Fatalf("rollback did not classify partial: outcome=%s err=%+v", rollback.Outcome, rollback.Error)
	}
	if rollback.Error == nil || rollback.Error.Kind != "operation_conflict" || rollback.Error.RecoveryAction.Kind != "reconcile_operation" || rollback.Error.EffectState != EffectPartial {
		t.Fatalf("rollback partial shape is wrong: %+v", rollback.Error)
	}
	if rollback.OperationRef == nil || rollback.OperationRef.State != OperationPartial {
		t.Fatalf("rollback partial lost its durable operation reference: %+v", rollback.OperationRef)
	}
	if _, encodeErr := rollback.Encode(); encodeErr != nil {
		t.Fatalf("partial native outcome does not satisfy the envelope contract: %v", encodeErr)
	}

	// The durable projection is the attributed ground truth: rolled_back with
	// reporter, subject, evidence, and both times (CD-0039 D4). The binding
	// must prove attribution and evidence are present even though the corpus
	// asserts only the status.
	runs, err := s.NativeRunsForWork(ctx, "work-1", 20)
	if err != nil {
		t.Fatal(err)
	}
	byPhase := map[string]store.NativeRunRow{}
	for _, run := range runs {
		byPhase[string(run.Phase)] = run
	}
	rollbackRow, hasRollback := byPhase["rollback"]
	if !hasRollback || rollbackRow.Status != "rolled_back" {
		t.Fatalf("native rollback projection=%+v", byPhase)
	}
	healthRow, hasHealth := byPhase["health"]
	if !hasHealth || healthRow.Status != "failed" {
		t.Fatalf("native health projection=%+v", byPhase)
	}
	for _, row := range []store.NativeRunRow{healthRow, rollbackRow} {
		if row.ReportingAuthorityRef == "" || row.EvidenceRef == "" || row.EvidenceDigest == "" || row.AssertedAt == "" || row.ObservationID == "" {
			t.Fatalf("native run row dropped attribution or evidence: %+v", row)
		}
		if row.VerificationState != store.VerificationUnverified {
			t.Fatalf("native run row rendered as %s before any verification", row.VerificationState)
		}
	}

	// An identical retry — the same bytes under the same idempotency key —
	// returns the same durable operation rather than a second effect
	// (CD-0039 D7).
	retryResp, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: rollbackInput}, env)
	if err != nil {
		t.Fatalf("rollback retry dispatch: %v", err)
	}
	if !retryResp.Replayed {
		t.Fatalf("identical rollback retry did not replay: outcome=%s", retryResp.Outcome)
	}
	if after, err := s.NativeRunsForWork(ctx, "work-1", 20); err != nil || len(after) != len(runs) {
		t.Fatalf("retry appended native records: %d -> %d (err=%v)", len(runs), len(after), err)
	}

	// The domain result carries the failure and rollback the corpus reads.
	var resultPayload map[string]any
	if err := json.Unmarshal(rollback.Result, &resultPayload); err != nil {
		t.Fatal(err)
	}
	healthFailure, _ := resultPayload["health_failure"].(map[string]any)
	if len(healthFailure) == 0 {
		t.Fatalf("partial result carries no health failure: %s", string(rollback.Result))
	}
	rollbackResult, _ := resultPayload["rollback_result"].(map[string]any)
	if len(rollbackResult) == 0 {
		t.Fatalf("partial result carries no rollback result: %s", string(rollback.Result))
	}

	obs := envelopeToObservation(rollback)
	obs.State = map[string]any{
		"native_change": map[string]any{
			"status":                  rollbackRow.Status,
			"reporting_authority_ref": rollbackRow.ReportingAuthorityRef,
			"evidence_ref":            rollbackRow.EvidenceRef,
			"observation_id":          rollbackRow.ObservationID,
		},
	}
	obs.Communication["outcome"] = string(rollback.Outcome)
	obs.Communication["health_failure"] = healthFailure["status"]
	obs.Communication["rollback_result"] = rollbackResult["status"]
	obs.Communication["completed_steps"] = []any{"start_run", "record_health", "rollback_run"}
	obs.Communication["recovery_action"] = rollback.Error.RecoveryAction.Kind
	obs.Communication["operation_id"] = rollback.OperationRef.ID
	// The consent gate for the whole run was consumed before any report.
	obs.Effects["approval_authority_consumed"] = true
	obs.Effects["adapter_domain_logic"] = probedAbsent{
		Evidence: "the partial outcome and rolled_back status were derived inside the core from typed attributed reports in the same transaction; the native-run projection and durable partial classification exist independent of any adapter, and the adapter surface was not extended to interpret provider output",
	}
	return obs
}
