package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestContractApprovalRefusalNamesInvalidMandate(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const workID = "contract-refusal-mandate"
	seedWork(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:test", ClientRef: "client:test", AgentRef: "agent:test", SessionRef: "session:test", ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	version := readWorkVersion(t, s, workID)
	fields := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "legislative_subjects": []string{}, "spec_mandate": []string{strings.Repeat("a", 129)}, "law_modifies": []string{}, "law_boundary_version": 1, "consequence_class": "internal_sqlite"}
	event := workflowEventWithActor("contract-refusal-mandate-event", WorkflowContractApproved, workID, DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), fields)
	event.PayloadVersion = CurrentEventPayloadVersion(WorkflowContractApproved)
	err = applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
	if err == nil || !strings.Contains(err.Error(), "spec_mandate") || !strings.Contains(err.Error(), "128 code points") {
		t.Fatalf("refusal should name spec_mandate and the 128 code points bound: %v", err)
	}
}

func TestWorkflowRefusalsExplainRecovery(t *testing.T) {
	t.Run("unreviewed step", func(t *testing.T) {
		const workID = "recovery-unreviewed-step"
		fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanFix, "diagnose")
		if err := runVerdictActionAs(t, fixture.store, workID, "record_diagnosis", diagnosisPayload("reproduce", "searched", "none_found", nil, "cause"), 0, fixture.owner); err != nil {
			t.Fatal(err)
		}
		payload := json.RawMessage(`{"step_id":"diagnose","note":"diagnosis complete","reviewer":"concord-review","result":"pass","commit":"commit:one","link":"https://example.com/review"}`)
		err := runVerdictActionAs(t, fixture.store, workID, "complete_step", payload, 0, fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || !strings.Contains(failure.RecoveryAction, "omit") || !strings.Contains(failure.RecoveryAction, "reviewer") {
			t.Fatalf("unreviewed-step recovery = %v", err)
		}
	})
	t.Run("reviewed step", func(t *testing.T) {
		step := WorkflowPlanStep{ID: "review", Reviewed: true, Attempts: []WorkflowPlanAttempt{{Note: "review complete", CompletedAt: "2026-01-01T00:00:00Z"}}}
		err := validateStepPlanAttemptsShape(step, errors.New)
		var failure *Failure
		if !errors.As(err, &failure) || failure.RecoveryAction != workflowStepReviewTeaching {
			t.Fatalf("reviewed-step recovery = %v", err)
		}
	})
	t.Run("missing architecture binding", func(t *testing.T) {
		_, err := workflowBoundContractContent(context.Background(), nil, WorkflowStepPlanChange, "recovery-binding", map[string]json.RawMessage{}, map[string]any{}, []string{}, []string{})
		var failure *Failure
		if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "requires architecture_binding") || !strings.Contains(failure.RecoveryAction, "concord_domain.list") {
			t.Fatalf("missing-binding recovery = %v", err)
		}
	})
	t.Run("related work", func(t *testing.T) {
		const workID = "recovery-related-work"
		fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanFix, "diagnose")
		payload := json.RawMessage(`{"reproduction":"reproduce","searched":"searched","outcome":"related_found","related_ids":["ZZ-42"],"root_cause":"cause"}`)
		err := runVerdictActionAs(t, fixture.store, workID, "record_diagnosis", payload, 0, fixture.owner)
		var failure *Failure
		if !errors.As(err, &failure) || !strings.Contains(failure.RecoveryAction, "concord_work_browse.list") || !strings.Contains(failure.RecoveryAction, "linear_issue_key") {
			t.Fatalf("related-work recovery = %v", err)
		}
	})
}
