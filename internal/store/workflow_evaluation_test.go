package store

import (
	"context"
	"strings"
	"testing"
)

func TestWorkflowEvaluationAuthoritySurvivesLeaseRotation(t *testing.T) {
	const workID = "evaluation-authority"
	s, owner := seedItemAtAcceptance(t, workID, true)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	var laneRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&laneRef); err != nil {
		t.Fatal(err)
	}
	if laneRef == ownerRef {
		t.Fatal("fixture did not rotate the lease")
	}
	for _, tc := range []struct {
		actor string
		want  WorkflowEvaluationAuthority
	}{
		{ownerRef, WorkflowOperatorRequired},
		{laneRef, WorkflowWorkerEvaluator},
		{DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/independent", "session/independent"), WorkflowIndependentEvaluator},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			got, err := s.WorkflowEvaluationAuthority(context.Background(), workID, tc.actor)
			if err != nil || got != tc.want {
				t.Fatalf("authority=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
	worker := workflowActorForRef(t, s, laneRef)
	version := verdictItemVersion(t, s, workID)
	if err := runOperatorVerdict(t, s, workID, worker, operatorVerdictActor(t, workID)); err == nil || !strings.Contains(err.Error(), "worker actors cannot submit operator decisions") {
		t.Fatalf("worker submitted operator verdict: %v", err)
	}
	if got := verdictItemVersion(t, s, workID); got != version {
		t.Fatalf("refused worker verdict changed version: %d -> %d", version, got)
	}
	for _, action := range []string{"record_verdict", "confirm_premise", "complete"} {
		t.Run("worker_"+action, func(t *testing.T) {
			tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			operator := operatorVerdictActor(t, workID)
			g := workflowActionGuardContext{ctx: context.Background(), tx: tx, actorRef: laneRef, request: WorkflowActionExecutionRequest{WorkID: workID, ActionID: action, Actor: worker, OperatorActor: &operator}}
			if err := guardOperatorPremiseActor(&g); err == nil || !strings.Contains(err.Error(), "worker actors cannot submit operator decisions") {
				t.Fatalf("worker submitted %s: %v", action, err)
			}
		})
	}
}

func TestWorkflowEvaluationAuthorityReadFailureIsNotIndependent(t *testing.T) {
	s := openTemp(t)
	if err := s.DatabaseForTesting().Close(); err != nil {
		t.Fatal(err)
	}
	if authority, err := s.WorkflowEvaluationAuthority(context.Background(), "work-unavailable", "actor-unavailable"); err == nil || authority != "" {
		t.Fatalf("unreadable authority=%q err=%v", authority, err)
	}
}
