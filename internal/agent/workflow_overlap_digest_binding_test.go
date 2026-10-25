package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestMutationDigestWireProtocol(t *testing.T) {
	for _, test := range []struct {
		name, raw, canonical string
	}{
		{"plain", `{"from_work_id":"from","to_work_id":"to","reason":"same"}`, `{"Tool":"concord_work_relate","Operation":"resolve_overlap","Product":"product-1","Project":"project-1","Input":{"from_work_id":"from","reason":"same","to_work_id":"to"}}`},
		{"authorization handles do not change intent", `{"reason":"same","approval":{"approval_ref":"opaque"},"idempotency_key":"key","to_work_id":"to","from_work_id":"from"}`, `{"Tool":"concord_work_relate","Operation":"resolve_overlap","Product":"product-1","Project":"project-1","Input":{"from_work_id":"from","reason":"same","to_work_id":"to"}}`},
		{"explicit budget remains intent", `{"from_work_id":"from","to_work_id":"to","reason":"same","requested_budget_seconds":5}`, `{"Tool":"concord_work_relate","Operation":"resolve_overlap","Product":"product-1","Project":"project-1","Input":{"from_work_id":"from","reason":"same","requested_budget_seconds":5,"to_work_id":"to"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1"}
			want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(test.canonical)))
			if got := mutationDigest("concord_work_relate", "resolve_overlap", env, []byte(test.raw)); got != want {
				t.Fatalf("canonical mutation digest=%s, want %s for %s", got, want, test.canonical)
			}
		})
	}
}

// overlapDigestBaseline is the pre-transaction population an expected
// refusal must leave exactly unchanged.
type overlapDigestBaseline struct {
	events, overlapEvents, resolutions, relations, approvals int
	fromVersion, toVersion                                   int64
	fromLifecycle, toLifecycle                               string
	challengeUsed                                            int
	challengeState                                           string
}

// overlapChallengeState reads the challenge's lifecycle and use count, so a
// refusal must show a challenge that never moved.
func overlapChallengeState(t *testing.T, s *store.Store, challengeRef string) (string, int) {
	t.Helper()
	var state string
	var used int
	if err := s.DatabaseForTesting().QueryRow(`SELECT status, used_count FROM agent_approval_challenges WHERE challenge_ref=?`, challengeRef).Scan(&state, &used); err != nil {
		t.Fatal(err)
	}
	return state, used
}

func captureOverlapDigestBaseline(t *testing.T, s *store.Store, challengeRef string) overlapDigestBaseline {
	t.Helper()
	db := s.DatabaseForTesting()
	var captured overlapDigestBaseline
	if err := db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&captured.events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='workflow.overlap_resolved'`).Scan(&captured.overlapEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM workflow_overlap_resolutions`).Scan(&captured.resolutions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM relations`).Scan(&captured.relations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM agent_approvals`).Scan(&captured.approvals); err != nil {
		t.Fatal(err)
	}
	captured.fromVersion = workVersion(t, s, "work-1")
	captured.toVersion = workVersion(t, s, "work-2")
	captured.fromLifecycle = workLifecycle(t, s, "work-1")
	captured.toLifecycle = workLifecycle(t, s, "work-2")
	captured.challengeState, captured.challengeUsed = overlapChallengeState(t, s, challengeRef)
	return captured
}

func assertOverlapDigestNoEffect(t *testing.T, s *store.Store, baseline overlapDigestBaseline, challengeRef string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if got := countRows(t, db, `SELECT last FROM store_sequences WHERE name='effect'`); got != baseline.events {
		t.Errorf("the refused request left %d domain events, want the baseline %d", got, baseline.events)
	}
	if got := countRows(t, db, `SELECT count(*) FROM effect_receipts WHERE kind='workflow.overlap_resolved'`); got != baseline.overlapEvents {
		t.Errorf("the refused request left %d resolution events, want the baseline %d", got, baseline.overlapEvents)
	}
	if got := countRows(t, db, `SELECT count(*) FROM workflow_overlap_resolutions`); got != baseline.resolutions {
		t.Errorf("the refused request left %d resolutions, want the baseline %d", got, baseline.resolutions)
	}
	if got := countRows(t, db, `SELECT count(*) FROM relations`); got != baseline.relations {
		t.Errorf("the refused request left %d relations, want the baseline %d", got, baseline.relations)
	}
	if got := countRows(t, db, `SELECT count(*) FROM agent_approvals`); got != baseline.approvals {
		t.Errorf("the refused request left %d approval rows, want the baseline %d: the rolled-back consent must not survive", got, baseline.approvals)
	}
	if got := workVersion(t, s, "work-1"); got != baseline.fromVersion {
		t.Errorf("the refused request moved work-1 to v%d, want %d", got, baseline.fromVersion)
	}
	if got := workVersion(t, s, "work-2"); got != baseline.toVersion {
		t.Errorf("the refused request moved work-2 to v%d, want %d", got, baseline.toVersion)
	}
	if got := workLifecycle(t, s, "work-1"); got != baseline.fromLifecycle {
		t.Errorf("the refused request left work-1 lifecycle %q, want %q", got, baseline.fromLifecycle)
	}
	if got := workLifecycle(t, s, "work-2"); got != baseline.toLifecycle {
		t.Errorf("the refused request left work-2 lifecycle %q, want %q", got, baseline.toLifecycle)
	}
	if state, used := overlapChallengeState(t, s, challengeRef); used != baseline.challengeUsed || state != baseline.challengeState {
		t.Errorf("the refused request left the challenge status=%q used_count=%d, want status=%q used_count=%d", state, used, baseline.challengeState, baseline.challengeUsed)
	}
}

func TestWorkflowOverlapExactDigestBinding(t *testing.T) {
	t.Parallel()
	op, registered := ValidateContractOperation("concord_work_relate", "resolve_overlap")
	if !registered {
		t.Fatal("resolve_overlap is not a registered contract operation")
	}
	consequence := string(op.Consequence)
	for _, testCase := range []struct {
		name          string
		kind          string
		reason        string
		reverse       bool
		budget        int
		requestBudget int
		wantRefusal   bool
	}{
		{name: "unchanged original request settles exactly once"},
		{name: "changed reason with the unchanged consumed binding", reason: "a reason the consumed approval never bound", wantRefusal: true},
		{name: "changed destructive kind with the unchanged consumed binding", kind: store.ResolutionSupersedes, wantRefusal: true},
		{name: "reversed direction with the unchanged consumed binding", reverse: true, wantRefusal: true},
		{name: "original explicit positive budget settles exactly once", budget: 5, requestBudget: 5},
		{name: "changed effect budget with the unchanged consumed binding", budget: 5, requestBudget: 7, wantRefusal: true},
		{name: "negative request budget with the unchanged consumed binding", requestBudget: -1, wantRefusal: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, service, privateKey, env := routineCoordinationFixture(t)
			original := routineOverlapExactBinding(t, s, store.ResolutionMergedInto)
			input := routineOverlapResolveInput(t, original, "overlap-digest-binding")
			// The explicit effect budget is part of the canonical input the
			// digest binds (the wire-protocol vectors pin that), so it joins
			// the input before the challenge and assertion are minted.
			if testCase.budget > 0 {
				budgeted := map[string]any{}
				if err := json.Unmarshal(input, &budgeted); err != nil {
					t.Fatal(err)
				}
				budgeted["requested_budget_seconds"] = testCase.budget
				budgetedInput, err := json.Marshal(budgeted)
				if err != nil {
					t.Fatal(err)
				}
				input = budgetedInput
			}
			challengeRef := routineOverlapChallenge(t, s, service, env, original, input)
			digest := mutationDigest("concord_work_relate", "resolve_overlap", env, input)
			var minted struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(input, &minted); err != nil {
				t.Fatal(err)
			}
			scope, versions := routineOverlapApprovalBindings(t, s, env, original)
			scopeJSON, err := json.Marshal(scope)
			if err != nil {
				t.Fatal(err)
			}
			versionsJSON, err := json.Marshal(versions)
			if err != nil {
				t.Fatal(err)
			}
			assertion := signedHostApproval(privateKey, challengeRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
			// The host probe runs before the transaction (CD-0195 D2), and the
			// invocation and check mirror the production consumption call at
			// internal/agent/mutations.go:4988-4996.
			host, hostErr := service.ProbeProjectHost(ctx, env.Directory, env.Worktree)
			if hostErr != nil {
				t.Fatalf("fixture host probe failed: %v", hostErr)
			}
			inv := Invocation{ClientRef: env.ClientRef, PrincipalRef: env.PrincipalRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: digest, RequiredCapability: op.Capability, RequiredOperation: "resolve_overlap", ProductID: env.SelectedProductID}
			check := ApprovalCheck{ApprovalRef: challengeRef, OperationDigest: digest, Scope: scope, Versions: versions, Consequence: consequence, ClientRef: env.ClientRef, SessionRef: env.SessionRef}

			baseline := captureOverlapDigestBaseline(t, s, challengeRef)
			if baseline.approvals != 0 || baseline.resolutions != 0 || baseline.overlapEvents != 0 || baseline.challengeUsed != 0 || baseline.challengeState != "active" {
				t.Fatalf("fixture baseline is not a minted unconsumed challenge over an unresolved pair: approvals=%d resolutions=%d events=%d used=%d status=%s", baseline.approvals, baseline.resolutions, baseline.overlapEvents, baseline.challengeUsed, baseline.challengeState)
			}

			request := store.WorkflowDomainOverlapResolutionRequest{
				EventID:                 digest + ":overlap",
				FromWorkID:              original.fromWorkID,
				ToWorkID:                original.toWorkID,
				FromExpectedVersion:     original.fromVersion,
				ToExpectedVersion:       original.toVersion,
				FromContractVersion:     original.fromContractVersion,
				ToContractVersion:       original.toContractVersion,
				ResolutionKind:          original.kind,
				Reason:                  minted.Reason,
				ApprovalOperationDigest: digest,
				ApprovalScopeJSON:       string(scopeJSON),
				ApprovalVersionsJSON:    string(versionsJSON),
				ApprovalConsequence:     consequence,
				Actor:                   env.PrincipalRef,
				OccurredAt:              fixedTime(),
			}
			// The production planner threads the actual parsed budget from
			// runtime.Budget; the direct store call mirrors that metadata.
			request.RequestedBudgetSeconds = testCase.requestBudget
			if testCase.kind != "" {
				request.ResolutionKind = testCase.kind
			}
			if testCase.reason != "" {
				request.Reason = testCase.reason
			}
			if testCase.reverse {
				// The swapped pins are the fixture's own current endpoint and
				// contract versions, never invented equality.
				request.FromWorkID, request.ToWorkID = original.toWorkID, original.fromWorkID
				request.FromExpectedVersion, request.ToExpectedVersion = original.toVersion, original.fromVersion
				request.FromContractVersion, request.ToContractVersion = original.toContractVersion, original.fromContractVersion
			}

			stage := ""
			txErr := s.Transact(ctx, func(tx *store.Transaction) error {
				stage = "authorize"
				if _, err := service.AuthorizeTx(ctx, tx, host, inv); err != nil {
					return err
				}
				stage = "consume"
				consumedRef, err := service.AcceptHostApprovalTx(ctx, tx, host, inv, *assertion, check)
				if err != nil {
					return err
				}
				request.ApprovalRef = consumedRef
				stage = "resolve"
				_, err = store.ResolveWorkflowDomainOverlapTx(ctx, tx, request)
				return err
			})
			if txErr != nil && stage != "resolve" {
				t.Fatalf("fixture %s step failed before the direct store call: %v", stage, txErr)
			}
			if txErr != nil {
				if !testCase.wantRefusal {
					t.Fatalf("unchanged original request refused: %v", txErr)
				}
				var failure *store.Failure
				if !errors.As(txErr, &failure) {
					t.Fatalf("the altered %s request refused with an untyped error, want the typed unauthorized refusal: %v", request.ResolutionKind, txErr)
				}
				if failure.Kind != store.KindUnauthorized {
					t.Fatalf("the altered %s request refused with kind %q, want unauthorized: %v", request.ResolutionKind, failure.Kind, txErr)
				}
				assertOverlapDigestNoEffect(t, s, baseline, challengeRef)
				return
			}
			if testCase.wantRefusal {
				committed := captureOverlapDigestBaseline(t, s, challengeRef)
				t.Fatalf("the altered %s request with reason %q committed with err=nil under the consumed binding digest %s: resolutions=%d overlap events=%d approvals=%d endpoint versions v%d->v%d and v%d->v%d, lifecycles %q/%q",
					request.ResolutionKind, request.Reason, digest,
					committed.resolutions, committed.overlapEvents, committed.approvals,
					baseline.fromVersion, committed.fromVersion, baseline.toVersion, committed.toVersion,
					committed.fromLifecycle, committed.toLifecycle)
			}
			// The positive control: the unchanged request settles exactly once
			// through the consumed binding, under the original digest format.
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
				t.Fatalf("the unchanged resolution left %d resolution rows, want exactly one", got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts WHERE kind='workflow.overlap_resolved'`); got != 1 {
				t.Fatalf("the unchanged resolution left %d resolution events, want exactly one", got)
			}
			var rowRef, rowKind, rowReason string
			if err := s.DatabaseForTesting().QueryRow(`SELECT approval_ref, resolution_kind, reason FROM workflow_overlap_resolutions`).Scan(&rowRef, &rowKind, &rowReason); err != nil {
				t.Fatal(err)
			}
			if rowKind != original.kind || rowReason != minted.Reason {
				t.Fatalf("the unchanged resolution recorded kind %q reason %q, want %q %q", rowKind, rowReason, original.kind, minted.Reason)
			}
			var eventRef, eventDigest string
			if err := s.DatabaseForTesting().QueryRow(`SELECT r.approval_ref,a.operation_digest FROM workflow_overlap_resolutions r JOIN effect_receipts e ON e.event_id=r.resolution_id JOIN agent_approvals a ON a.approval_ref=r.approval_ref WHERE e.kind='workflow.overlap_resolved'`).Scan(&eventRef, &eventDigest); err != nil {
				t.Fatal(err)
			}
			if eventDigest != digest {
				t.Fatalf("the settled event digest %q is not the original request digest %q", eventDigest, digest)
			}
			if rowRef == "" || rowRef != eventRef || rowRef == challengeRef {
				t.Fatalf("the unchanged resolution persisted approval row=%q event=%q challenge=%q", rowRef, eventRef, challengeRef)
			}
			if got := workLifecycle(t, s, original.fromWorkID); got != "superseded" {
				t.Fatalf("the unchanged merged_into left the terminal work %q, want superseded", got)
			}
			if got := workLifecycle(t, s, original.toWorkID); got != "in_progress" {
				t.Fatalf("the unchanged merged_into left the survivor work %q, want in_progress", got)
			}
			if got := workVersion(t, s, original.fromWorkID); got != original.fromVersion+1 {
				t.Fatalf("the unchanged resolution left the from endpoint at v%d, want %d", got, original.fromVersion+1)
			}
			if got := workVersion(t, s, original.toWorkID); got != original.toVersion+1 {
				t.Fatalf("the unchanged resolution left the to endpoint at v%d, want %d", got, original.toVersion+1)
			}
			if used := countRows(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approvals`); used != 1 {
				t.Fatalf("the settled unchanged resolution approval used_count=%d, want the consumed one-use row", used)
			}
			if state, used := overlapChallengeState(t, s, challengeRef); state != "consumed" || used != 1 {
				t.Fatalf("the settled unchanged resolution left the challenge status=%q used_count=%d, want consumed/1", state, used)
			}
		})
	}
}

// TestRoutineCoordinationExplicitBudgetResolvesThroughRealDispatch: the
// planner threads the actual parsed runtime budget — applyBudget's integer,
// not test-supplied direct-store metadata — into the destructive binding, so
// a genuine challenge minted over an input carrying
// requested_budget_seconds:5 settles the identical budgeted request through
// the full approved dispatch and leaves exactly one consumed resolution.
func TestRoutineCoordinationExplicitBudgetResolvesThroughRealDispatch(t *testing.T) {
	t.Parallel()
	s, service, privateKey, env := routineCoordinationFixture(t)
	binding := routineOverlapExactBinding(t, s, store.ResolutionMergedInto)
	input := routineOverlapResolveInput(t, binding, "overlap-budget-dispatch")
	budgeted := map[string]any{}
	if err := json.Unmarshal(input, &budgeted); err != nil {
		t.Fatal(err)
	}
	budgeted["requested_budget_seconds"] = 5
	budgetedInput, err := json.Marshal(budgeted)
	if err != nil {
		t.Fatal(err)
	}
	challengeRef := routineOverlapChallenge(t, s, service, env, binding, budgetedInput)
	withApproval, err := injectApproval(budgetedInput, challengeRef)
	if err != nil {
		t.Fatal(err)
	}
	scope, versions := routineOverlapApprovalBindings(t, s, env, binding)
	digest := mutationDigest("concord_work_relate", "resolve_overlap", env, withApproval)
	approved := routineOverlapApprovedDispatch(t, s, service, privateKey, env, challengeRef, digest, scope, versions, withApproval)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("the budgeted destructive resolution outcome=%s error=%+v", approved.Outcome, approved.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
		t.Fatalf("the budgeted resolution left %d resolution rows, want exactly one", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 1 {
		t.Fatalf("the budgeted resolution left %d approval rows, want the single consumed approval", got)
	}
	if used := countRows(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approvals`); used != 1 {
		t.Fatalf("the budgeted resolution approval used_count=%d, want the consumed one-use row", used)
	}
}
