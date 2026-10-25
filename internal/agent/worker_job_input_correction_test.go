package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestRecordWorkerJobInputCorrection(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	live, err := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	grant.Worktree = seedWorkerRetryMutationFixtureAt(t, s, grant, live)
	fields := map[string]any{
		"job_id": "job:input-correction", "objective": "Carry out the approved objective.",
		"stopping_condition": "The recorded checks pass.", "ready": true,
		"checks": []string{"go test ./..."}, "acceptance_oracle": agentOracleFieldsForWork(t, s, "work-1"),
	}
	addAgentJobCallerMembers(t, s, "work-1", fields)
	scope, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scope)
	version := workVersion(t, s, "work-1")
	key := "job-input-correction"
	invoke := func() Envelope {
		t.Helper()
		return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": "record_worker_job", "idempotency_key": key, "fields": fields,
		})}, env)
	}
	snapshot := func() [3]int64 {
		t.Helper()
		state := [3]int64{workVersion(t, s, "work-1")}
		for i, query := range []string{`SELECT last FROM store_sequences WHERE name='effect'`, `SELECT count(*) FROM worker_job_revisions`} {
			if err := s.DatabaseForTesting().QueryRow(query).Scan(&state[i+1]); err != nil {
				t.Fatal(err)
			}
		}
		return state
	}
	before := snapshot()
	refused := invoke()
	if refused.Error == nil || refused.Error.Kind != "invalid_input" || refused.Error.EffectState != EffectNone || refused.Error.RetrySafe || refused.Error.RecoveryAction.Kind != "correct_request" {
		t.Fatalf("root readiness correction refusal=%+v", refused.Error)
	}
	if !strings.Contains(refused.Error.Message, "readiness_evidence") || !strings.Contains(refused.Error.Message, "at least one") {
		t.Fatalf("missing field/bound diagnostic: %+v", refused.Error)
	}
	if _, err := json.Marshal(refused); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("refusal mutated version/events/revisions: %v -> %v", before, after)
	}
	fields["readiness_evidence"] = []string{"contract:work-1:1"}
	if corrected := invoke(); corrected.Outcome != OutcomeOK {
		t.Fatalf("correcting only root readiness evidence failed: %+v", corrected.Error)
	}
	version = workVersion(t, s, "work-1")
	key = "job-unqualified-evidence"
	fields["job_id"] = "job:unqualified"
	fields["acceptance_oracle"].(map[string]any)["controls"].([]any)[0].(map[string]any)["readiness_evidence_refs"] = []string{"evidence:unqualified"}
	before = snapshot()
	if unqualified := invoke(); unqualified.Error == nil || unqualified.Error.EffectState != EffectNone || !strings.Contains(unqualified.Error.Message, "qualified native preparation") {
		t.Fatalf("corrected root bypassed native qualification: %+v", unqualified.Error)
	}
	if after := snapshot(); after != before {
		t.Fatalf("unqualified evidence mutated state: %v -> %v", before, after)
	}
}

func TestStoreCorrectionProposalPreservesAuthorityAndEffects(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure store.Failure
		want    string
	}{
		{"operator denial", store.Failure{Kind: store.KindUnauthorized}, "contact_operator"},
		{"approval gate", store.Failure{Kind: store.FailureKind("approval_required")}, "request_approval"},
		{"possible effect", store.Failure{Kind: store.KindInvalidPayload, EffectPossible: true}, "reconcile_operation"},
		{"committed mutation", store.Failure{Kind: store.KindInvalidPayload, CommittedRefs: []store.SubjectCurrentVersion{{SubjectType: store.SubjectType("work"), SubjectID: "work-1", Version: 2}}}, "reconcile_operation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.failure.RecoveryAction = "correct_request"
			out := failureEnvelope(NewBase("correction-proposal", "concord_work_transition", "workflow_action"), &test.failure)
			if out.Error == nil || out.Error.RecoveryAction.Kind != test.want {
				t.Fatalf("unsafe correction proposal=%+v", out.Error)
			}
			if _, err := out.Encode(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
