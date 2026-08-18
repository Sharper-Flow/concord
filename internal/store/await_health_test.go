package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Issue #87: waiting must be distinguishable from will-never-complete.
// Four cases: within bound, beyond bound, completed-late, never-completable
// (no bound declared — honest output is unknown, never alarm).

func awaitFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("p-await"), locatorProjectEvent("pr-await"), locatorMembershipEvent("p-await", "pr-await")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "p-await"): 0, VersionRef(SubjectProject, "pr-await"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "await-work-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-await", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Await","priority":1}`)},
		{EventID: "await-work-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-await", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"pr-await","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func addAwaitCondition(t *testing.T, s *Store, conditionID string, boundSeconds int64, recordedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	var currentVersion int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-await'`).Scan(&currentVersion); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"work_id": "work-await", "expected_version": currentVersion, "resulting_version": currentVersion + 1,
		"condition_id": conditionID, "await_type": "pr_merge", "await_ref": "pr:42",
		"resolution_authority": "durable_operation:op-await",
	}
	if boundSeconds > 0 {
		payload["expected_within_seconds"] = boundSeconds
	}
	raw, _ := json.Marshal(payload)
	// The resolution authority must be a recorded durable operation on this
	// work item — the same engine rule production add_condition satisfies.
	authorityOp := "op-await-" + conditionID
	evidenceRef := "evidence:merged-late:" + conditionID
	evidenceJSON, _ := json.Marshal([]string{evidenceRef})
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,principal_ref,request_id,observed_at,completed_at) VALUES(?,1,'work-await','workflow.ops_runbook',1,'execute','external_effect',?,?,'completed','{}',?, 'principal/op','req-await',?,?)`,
		authorityOp, "sha256:await", "{}", string(evidenceJSON), recordedAt.Format(time.RFC3339Nano), recordedAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	payload["resolution_authority"] = "durable_operation:" + authorityOp
	raw, _ = json.Marshal(payload)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{{EventID: "cond-" + conditionID, Kind: WorkflowConditionAdded, SubjectType: SubjectWorkItem, SubjectID: "work-await", Actor: "operator", OccurredAt: recordedAt, PayloadVersion: 1, Payload: raw}}}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestAwaitHealthDistinguishesWaitingFromNeverCompletable(t *testing.T) {
	s := awaitFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	// Case 1: within bound — waiting.
	addAwaitCondition(t, s, "cond-bounded", 3600, now.Add(-10*time.Minute))
	// Case 2: beyond bound — overdue, unverified (never resolved-by-clock).
	addAwaitCondition(t, s, "cond-overdue", 60, now.Add(-2*time.Hour))
	// Case 4: never-completable shape — no bound declared: unknown, not alarm.
	addAwaitCondition(t, s, "cond-nobound", 0, now.Add(-48*time.Hour))

	health, err := s.AwaitHealthForWork(ctx, "work-await", now)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AwaitHealth{}
	for _, h := range health {
		byID[h.ConditionID] = h
	}
	if len(byID) != 3 {
		t.Fatalf("health=%+v", health)
	}
	if h := byID["cond-bounded"]; h.Overdue || h.Health != AwaitHealthWaiting || h.ExpectedWithinSeconds != 3600 || h.AgeSeconds < 550 {
		t.Fatalf("within bound must be waiting: %+v", h)
	}
	if h := byID["cond-overdue"]; !h.Overdue || h.Health != AwaitHealthOverdue {
		t.Fatalf("beyond bound must be overdue: %+v", h)
	}
	if h := byID["cond-nobound"]; h.Overdue || h.Health != AwaitHealthWaiting || h.ExpectedWithinSeconds != 0 {
		t.Fatalf("no declared bound must read waiting/unknown: %+v", h)
	}

	// The workflow read projection carries the same split. It needs a
	// workflow instance for the read to resolve.
	definition, defErr := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if defErr != nil {
		t.Fatal(defErr)
	}
	initTx, txErr := s.DatabaseForTesting().BeginTx(ctx, nil)
	if txErr != nil {
		t.Fatal(txErr)
	}
	if err := initializeWorkflowRawTx(ctx, initTx, WorkflowInitializationRequest{WorkID: "work-await", Definition: definition, Actor: WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: ActorAgent}, Now: now}); err != nil {
		_ = initTx.Rollback()
		t.Fatal(err)
	}
	if err := initTx.Commit(); err != nil {
		t.Fatal(err)
	}
	projection, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: "work-await", Limit: 10, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.OverdueAwaits) != 1 || projection.OverdueAwaits[0] != "cond-overdue" {
		t.Fatalf("overdue awaits=%v", projection.OverdueAwaits)
	}

	// Case 3: completed-late — resolution after the bound is a normal
	// resolution; the clock never fabricates or forbids it.
	resolver := lateResolver{conditionID: "cond-overdue"}
	if err := ResolveWorkflowCondition(ctx, s, "work-await", "cond-overdue", resolver, now); err != nil {
		t.Fatalf("late resolution refused: %v", err)
	}
	health, err = s.AwaitHealthForWork(ctx, "work-await", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range health {
		if h.ConditionID == "cond-overdue" {
			t.Fatal("resolved await must leave the open-health projection")
		}
	}
	// Never-completable stays visible, still honestly "no bound declared":
	// the operator sees the age, not a fabricated verdict.
	if h := byID["cond-nobound"]; h.Overdue {
		t.Fatal("elapsed time alone must never create a verdict")
	}
}

type lateResolver struct{ conditionID string }

func (l lateResolver) Resolve(_ context.Context, condition ExternalCondition, _ time.Time) (Resolution, error) {
	return Resolution{ResolutionEvidence: []string{"evidence:merged-late:" + condition.ConditionID}, ResolvedByEvent: "resolved:" + condition.ConditionID, ActorRef: "actor:resolver"}, nil
}

func TestOverdueAwaitsInProductListsStalledWaits(t *testing.T) {
	s := awaitFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	addAwaitCondition(t, s, "cond-stalled", 30, now.Add(-3*time.Hour))
	addAwaitCondition(t, s, "cond-fresh", 3600, now.Add(-5*time.Minute))

	overdue, err := s.OverdueAwaitsInProduct(ctx, "p-await", now, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(overdue) != 1 || overdue[0].ConditionID != "cond-stalled" || !overdue[0].Overdue {
		t.Fatalf("overdue=%+v", overdue)
	}

	// The portfolio row counts it: the stalled item no longer presents as
	// merely active.
	page, err := s.QueryProductRows(ctx, ProductRowRequest{Product: "p-await", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Rows {
		if row.ActionCounts.Values != nil && row.ActionCounts.Values.OverdueAwaits == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("portfolio row does not surface the overdue await")
	}
}
