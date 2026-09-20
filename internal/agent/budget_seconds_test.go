package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// CD-0038 D1/D3/D4/D6: the seconds budget is a shared input, refuses against
// the declared ceiling before any effect, becomes a real deadline when
// accepted, and must agree with the legacy millisecond field when both are
// sent.

func budgetOpFor(t *testing.T) ContractOperation {
	t.Helper()
	return contractOpFor(t, "concord_work_transition", "workflow_action")
}

func TestApplyBudgetParsesRequestedSecondsAndCeiling(t *testing.T) {
	t.Parallel()
	op := budgetOpFor(t)
	if op.SupportedBudgetSeconds != 30 {
		t.Fatalf("workflow_action ceiling must be the TS1-fixed 30, got %d", op.SupportedBudgetSeconds)
	}
	_, _, budget, failure := applyBudget(context.Background(), op, []byte(`{"requested_budget_seconds":10}`))
	if failure != nil {
		t.Fatalf("within-ceiling budget refused: %v", failure)
	}
	if budget.RequestedSeconds != 10 || budget.SupportedSeconds != 30 || budget.CeilingRefused {
		t.Fatalf("budget not parsed: %#v", budget)
	}
}

func TestApplyBudgetUniformCeilingCoversTheSurface(t *testing.T) {
	t.Parallel()
	byID := map[string]int{}
	for _, op := range ContractOperations {
		byID[op.ID] = op.SupportedBudgetSeconds
	}
	// CD-0038 D2: every operation shares the uniform 300 ceiling except the
	// evidence-fixed exceptions the contract generator pins by name.
	evidenceFixed := map[string]int{
		"concord_work_transition.workflow_action": 30,
		"concord_work_transition.worktree_verify": 1800,
	}
	for id, ceiling := range byID {
		if fixed, ok := evidenceFixed[id]; ok {
			if ceiling != fixed {
				t.Fatalf("%s declares %d; the evidence-fixed ceiling is %d", id, ceiling, fixed)
			}
			continue
		}
		if ceiling != 300 {
			t.Fatalf("%s declares %d; the uniform surface ceiling is 300", id, ceiling)
		}
	}
}

func TestWorktreeVerifyAdmitsThe1800SecondBudget(t *testing.T) {
	t.Parallel()
	// CON-317: the measured 367.08s verification lane fixes the
	// worktree_verify ceiling at 1800 under the CD-0038 D2 evidence clause.
	// The legacy max_millis cap stays where it was.
	op := contractOpFor(t, "concord_work_transition", "worktree_verify")
	if op.SupportedBudgetSeconds != 1800 {
		t.Fatalf("worktree_verify ceiling = %d, want the CON-317-evidenced 1800", op.SupportedBudgetSeconds)
	}
	ctx, cancel, budget, failure := applyBudget(context.Background(), op, []byte(`{"requested_budget_seconds":1800}`))
	defer cancel()
	if failure != nil {
		t.Fatalf("the measured lane's budget refused: %v", failure)
	}
	if budget.CeilingRefused {
		t.Fatalf("1800s marked over-ceiling: %#v", budget)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("accepted budget installed no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 1800*time.Second+100*time.Millisecond {
		t.Fatalf("deadline is not the accepted budget: %v", remaining)
	}
	_, _, over, _ := applyBudget(context.Background(), op, []byte(`{"requested_budget_seconds":1801}`))
	if !over.CeilingRefused {
		t.Fatalf("1801s not marked over-ceiling: %#v", over)
	}
	_, _, _, legacy := applyBudget(context.Background(), op, []byte(`{"budget":{"max_millis":300001}}`))
	if legacy == nil || legacy.kind != "budget_refused" {
		t.Fatalf("legacy millisecond bound moved: %#v", legacy)
	}
}

func TestApplyBudgetMarksOverCeilingWithoutActing(t *testing.T) {
	t.Parallel()
	_, _, budget, failure := applyBudget(context.Background(), budgetOpFor(t), []byte(`{"requested_budget_seconds":60}`))
	if failure != nil {
		t.Fatalf("ceiling flag must not be an admission failure at parse time: %v", failure)
	}
	if !budget.CeilingRefused || budget.RequestedSeconds != 60 {
		t.Fatalf("over-ceiling budget not marked: %#v", budget)
	}
}

func TestApplyBudgetInstallsSecondsDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel, _, failure := applyBudget(context.Background(), budgetOpFor(t), []byte(`{"requested_budget_seconds":2}`))
	defer cancel()
	if failure != nil {
		t.Fatalf("accepted budget refused: %v", failure)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("requested_budget_seconds did not install a context deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 2*time.Second+100*time.Millisecond {
		t.Fatalf("deadline is not the accepted budget: %v", remaining)
	}
}

func TestApplyBudgetOmissionMeansNoDeadline(t *testing.T) {
	t.Parallel()
	// CD-0038 D4: omission is not a request for the maximum. Internal bounds
	// remain; the operation itself runs without a Concord-installed deadline.
	ctx, cancel, _, failure := applyBudget(context.Background(), budgetOpFor(t), []byte(`{}`))
	defer cancel()
	if failure != nil {
		t.Fatalf("omitted budget refused: %v", failure)
	}
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("omitted budget installed a deadline")
	}
}

func TestApplyBudgetRejectsNonPositiveSeconds(t *testing.T) {
	t.Parallel()
	_, _, _, failure := applyBudget(context.Background(), budgetOpFor(t), []byte(`{"requested_budget_seconds":-5}`))
	if failure == nil || failure.kind != "invalid_input" {
		t.Fatalf("negative seconds must be invalid_input, got %#v", failure)
	}
}

func TestApplyBudgetEnforcesMillisecondAgreement(t *testing.T) {
	t.Parallel()
	// CD-0038 D6: both denominations may be sent only when they express one
	// exact duration. No rounding, no preference rule.
	_, _, _, mismatch := applyBudget(context.Background(), budgetOpFor(t), []byte(`{"requested_budget_seconds":30,"budget":{"max_millis":29999}}`))
	if mismatch == nil || mismatch.kind != "invalid_input" {
		t.Fatalf("disagreeing denominations accepted: %#v", mismatch)
	}
	ctx, cancel, budget, agreed := applyBudget(context.Background(), budgetOpFor(t), []byte(`{"requested_budget_seconds":30,"budget":{"max_millis":30000}}`))
	defer cancel()
	if agreed != nil || budget.MaxMillis != 30000 || budget.RequestedSeconds != 30 {
		t.Fatalf("agreeing denominations refused: %#v %v", budget, agreed)
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("agreed budget installed no deadline")
	}
}

func TestApplyBudgetKeepsLegacyMillisecondBound(t *testing.T) {
	t.Parallel()
	_, _, _, failure := applyBudget(context.Background(), budgetOpFor(t), []byte(`{"budget":{"max_millis":300001}}`))
	if failure == nil || failure.kind != "budget_refused" {
		t.Fatalf("legacy millisecond bound lost: %#v", failure)
	}
}

func TestBudgetRefusalCarriesTheTypedCeiling(t *testing.T) {
	t.Parallel()
	r := runtime{Tool: "concord_work_transition", Operation: "workflow_action", Budget: budgetInput{CeilingRefused: true, RequestedSeconds: 60, SupportedSeconds: 30}}
	out := r.budgetRefusal(NewBase("request", r.Tool, r.Operation), "requested_budget_seconds 60 exceeds supported 30")
	if out.Error == nil || out.Error.Kind != "budget_refused" || out.Error.SupportedBudgetSeconds != 30 {
		t.Fatalf("refusal envelope wrong: %#v", out.Error)
	}
	if out.Error.RecoveryAction.Kind != "adjust_budget" || out.Error.EffectState != EffectNone {
		t.Fatalf("refusal recovery shape wrong: %#v", out.Error)
	}
	if _, err := out.Encode(); err != nil {
		t.Fatalf("refusal envelope violates the law it implements: %v", err)
	}
}

func TestValidateErrorRequiresTypedCeilingOnEveryBudgetRefusal(t *testing.T) {
	t.Parallel()
	base := TypedError{Kind: "budget_refused", RecoveryAction: RecoveryAction{Kind: "adjust_budget"}, EffectState: EffectNone}
	if err := validateError(base); err == nil {
		t.Fatal("budget_refused without the typed ceiling passed validation")
	}
	base.SupportedBudgetSeconds = 300
	if err := validateError(base); err != nil {
		t.Fatalf("budget_refused with the typed ceiling rejected: %v", err)
	}
}

func TestRequestedBudgetSecondsJoinsTheCanonicalDigest(t *testing.T) {
	t.Parallel()
	// CD-0038 D1: changing the requested budget changes the canonical request.
	// The digest is over the raw input, so the field participates structurally;
	// this pins that property before anything learns to strip it.
	one := mutationDigest("concord_work_transition", "workflow_action", CallEnvelope{ClientRef: "client", SessionRef: "session", AgentRef: "agent"}, json.RawMessage(`{"work_id":"w","expected_version":1,"action_id":"a","idempotency_key":"k","requested_budget_seconds":30}`))
	two := mutationDigest("concord_work_transition", "workflow_action", CallEnvelope{ClientRef: "client", SessionRef: "session", AgentRef: "agent"}, json.RawMessage(`{"work_id":"w","expected_version":1,"action_id":"a","idempotency_key":"k","requested_budget_seconds":20}`))
	if one == two {
		t.Fatal("different budgets produced the same canonical digest")
	}
}
