package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// TestMutationDurabilityDeclaresEffectIndependentlyOfConsent pins the
// durability contract the mutation executor must own: a registered mutation
// whose contract carries a durable commit class keeps that class even when
// the consent requirement is absent. The call passes requiresApproval=false
// with no supplied approval — exactly the shape a later consent change
// produces — for concord_work_transition.correct_delivery. The executor
// resolves the operation's declared baseline before any effect, so this run
// opens the durable transaction and the durable-commit counter advances by
// exactly one even though no approval is required or consumed.
func TestMutationDurabilityDeclaresEffectIndependentlyOfConsent(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	op, registered := agentcontract.ValidateContractOperation("concord_work_transition", "correct_delivery")
	if !registered {
		t.Fatal("correct_delivery is not a registered contract operation")
	}
	raw, err := json.Marshal(map[string]any{
		"work_id":                "work-1",
		"expected_version":       2,
		"target_event_id":        "fixture-work-membership",
		"target_seq":             1,
		"target_payload_version": 1,
		"reason":                 "durability declaration witness",
		"delivery_artifact":      "https://delivery.invalid/correction",
		"delivery_state":         "asserted",
		"idempotency_key":        "durability-consent-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := mutationDigest("concord_work_transition", "correct_delivery", env, raw)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "scope_version": scopeVersion, "work_ids": []string{"work-1"}}
	versions := map[string]any{"work": 2}
	effect := func(ctx context.Context, tx *store.Transaction, _ Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		// The committed effect rides the executor's own idempotency boundary;
		// an empty changed-ref list and a closed-schema result payload are the
		// whole domain surface this witness needs.
		return mutationPayload(nil, nil), nil, nil, nil
	}
	before := s.DurableCommits()
	r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_transition", Operation: "correct_delivery", Reader: grant}
	response, err := r.executeMutation(ctx, agentcontract.NewBase("durability-consent", "concord_work_transition", "correct_delivery"), raw, digest, scope, versions, string(op.Consequence), "", false, nil, nil, nil, effect, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeOK || response.Error != nil || response.Replayed {
		t.Fatalf("consent-free mutation did not commit: outcome=%+v error=%+v replayed=%t", response.Outcome, response.Error, response.Replayed)
	}
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", OperationKind: "correct_delivery", IdempotencyKey: "durability-consent-1"}
	record, found, err := s.LookupMutationIdempotency(ctx, key)
	if err != nil || !found {
		t.Fatalf("committed mutation left no idempotency record: found=%t err=%v", found, err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(record.AuthorizedScopeSnapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	if committed, ok := snapshot["durable_commit"].(bool); !ok || !committed {
		t.Fatalf("stored durability receipt does not record a durable commit: %v", snapshot["durable_commit"])
	}
	if after := s.DurableCommits(); after != before+1 {
		t.Fatalf("consent-free run of a previously durable operation advanced the durable counter by %d, want exactly 1", after-before)
	}
}

// TestMutationDurabilityDeclarations enumerates every registered contract
// operation against the closed durability declaration table. Read operations
// must refuse with a typed durability invariant error. Each registered
// mutation must retain its independent initial and replay baselines. Consent
// changes do not downgrade the durable removal, publication, repair, relation,
// or access-grant consequences. No declaration may exist for an operation
// the manifest does not register as a mutation. Conditional planner facts override the initial
// baseline in the executor.
func TestMutationDurabilityDeclarations(t *testing.T) {
	t.Parallel()

	durableInitial := map[string]bool{
		"concord_work_transition.remove":                   true,
		"concord_work_transition.worktree_claim":           true,
		"concord_work_relate.resolve_overlap":              true,
		"concord_work_compact.publish":                     true,
		"concord_work_transition.correct_delivery":         true,
		"concord_work_relate.product_project_add":          true,
		"concord_work_relate.client_policy_grant_request":  true,
		"concord_work_transition.outside_repair":           true,
		"concord_work_transition.outside_repair_reconcile": true,
	}
	replayBaseline := func(op agentcontract.ContractOperation) bool {
		return durableInitial[op.ID] ||
			op.ID == "concord_work_transition.workflow_action" ||
			op.ID == "concord_work_compact.reconcile"
	}

	declared := map[string]bool{}
	for id := range mutationDurabilityDeclarations {
		declared[id] = true
	}
	var missing []string
	for _, op := range agentcontract.ContractOperations {
		switch op.Kind {
		case agentcontract.OperationRead:
			if _, err := mutationDurabilityFor(op); err == nil {
				t.Errorf("lookup served mutation durability for read operation %s", op.ID)
			} else if !errors.As(err, new(*mutationDurabilityError)) {
				t.Errorf("read operation %s refusal is not a typed durability invariant error: %v", op.ID, err)
			}
		case agentcontract.OperationMutation:
			delete(declared, op.ID)
			want := mutationDurability{initial: durableInitial[op.ID], replay: replayBaseline(op)}
			got, err := mutationDurabilityFor(op)
			if err != nil {
				missing = append(missing, op.ID)
				continue
			}
			if got != want {
				t.Errorf("mutation %s declares %+v, independent consequence baseline is %+v", op.ID, got, want)
			}
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		t.Fatalf("registered mutations with no durability declaration: %v", missing)
	}
	var orphaned []string
	for id := range declared {
		orphaned = append(orphaned, id)
	}
	if len(orphaned) != 0 {
		sort.Strings(orphaned)
		t.Fatalf("durability declarations for operations the manifest does not register as mutations: %v", orphaned)
	}

	// Consent metadata cannot move the selection: the lookup keys by exact
	// operation identity, so a copy whose Approval class is altered in any
	// direction, including a class no manifest ever assigns, resolves to the
	// same declaration.
	for _, op := range agentcontract.ContractOperations {
		if op.Kind != agentcontract.OperationMutation {
			continue
		}
		baseline, err := mutationDurabilityFor(op)
		if err != nil {
			t.Fatalf("registered mutation %s has no declaration: %v", op.ID, err)
		}
		for _, class := range []agentcontract.ApprovalClass{agentcontract.ApprovalClass("none"), agentcontract.ApprovalClass("conditional"), agentcontract.ApprovalClass("required"), agentcontract.ApprovalClass("revoked")} {
			altered := op
			altered.Approval = class
			got, err := mutationDurabilityFor(altered)
			if err != nil {
				t.Fatalf("lookup refused %s with altered approval %q: %v", op.ID, class, err)
			}
			if got != baseline {
				t.Fatalf("altered approval %q on %s moved durability from %+v to %+v", class, op.ID, baseline, got)
			}
		}
	}

	// A mutation the manifest does not register refuses as a typed invariant
	// failure instead of defaulting to the ordinary transaction.
	unknown := agentcontract.ContractOperation{ID: "concord_work_transition.absent", Tool: "concord_work_transition", Operation: "absent", Kind: agentcontract.OperationMutation}
	_, err := mutationDurabilityFor(unknown)
	if err == nil {
		t.Fatal("lookup served a durability declaration for an unregistered mutation")
	}
	if !errors.As(err, new(*mutationDurabilityError)) {
		t.Fatalf("unknown mutation refusal is not a typed durability invariant error: %v", err)
	}
}

// TestMutationDurabilityConditionalPlans pins the initial-durability facts
// the planners produce for operations whose transaction class the declared
// baseline alone cannot carry. The executor replaces its baseline with a
// supplied fact, so each case must show the fact comes from the operation's
// actual consequence branch — a terminal lifecycle target, the overlap
// resolution kind, the membership replacement boundary — and never from
// consent metadata. Cases plan with authenticated input shapes and inspect
// the produced plan without executing effects. Capture and destroy facts
// need store state, so TestMutationDurabilityStoreBoundPlannerFacts
// witnesses them through dispatch.
func TestMutationDurabilityConditionalPlans(t *testing.T) {
	t.Parallel()

	// Terminal overlap planning reads the captured governing requirements;
	// use the real canonical-source fixture rather than an absent store.
	s, _, _, _ := routineCoordinationFixture(t)
	r := runtime{Store: s}
	envelope := CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1", ScopeVersion: "scope-version-1"}
	digest := "sha256:" + strings.Repeat("b", 64)
	registeredOp := func(t *testing.T, tool, operation string) agentcontract.ContractOperation {
		t.Helper()
		contractOp, registered := agentcontract.ValidateContractOperation(tool, operation)
		if !registered {
			t.Fatalf("%s.%s is not a registered contract operation", tool, operation)
		}
		return contractOp
	}
	plannedFact := func(t *testing.T, plan *mutationPlan) bool {
		t.Helper()
		if plan.plannedInitialDurability == nil {
			t.Fatal("planner produced no initial-durability fact")
		}
		return *plan.plannedInitialDurability
	}

	lifecycleOp := registeredOp(t, "concord_work_transition", "lifecycle")
	evidence := []map[string]string{{"kind": "verification", "authority": "check", "locator_kind": "check_ref", "locator": "check:durability-witness"}}
	for _, tc := range []struct {
		name    string
		target  string
		durable bool
	}{
		{"needed", "needed", false},
		{"in_progress", "in_progress", false},
		{"completed", "completed", true},
		{"cancelled", "cancelled", true},
	} {
		raw, err := json.Marshal(map[string]any{
			"work_id": "work-1", "expected_version": 2, "target": tc.target,
			"reason": "durability plan witness", "idempotency_key": "durability-plan-lifecycle-" + tc.name,
			"evidence": evidence,
		})
		if err != nil {
			t.Fatal(err)
		}
		plan := newMutationPlan(envelope, lifecycleOp)
		answer, planErr, handled := r.planLifecycle(context.Background(), agentcontract.NewBase("durability-plan", "concord_work_transition", "lifecycle"), raw, digest, Authority{}, lifecycleOp, plan)
		if handled || planErr != nil || answer.Outcome == agentcontract.OutcomeError {
			t.Fatalf("lifecycle %s planner refused: handled=%t err=%v answer=%+v", tc.target, handled, planErr, answer)
		}
		if got := plannedFact(t, plan); got != tc.durable {
			t.Errorf("lifecycle target %s planned initial durability %t, want %t", tc.target, got, tc.durable)
		}
		// CD-0243 D3/D7 retire terminal status as an independent consent trigger.
		if plan.requiresApproval {
			t.Errorf("ordinary lifecycle target %s unexpectedly requires approval", tc.target)
		}
	}

	// Consent never feeds the fact: the same terminal plan carrying an
	// approval reference produces the identical durability fact.
	approvedRaw, err := json.Marshal(map[string]any{
		"work_id": "work-1", "expected_version": 2, "target": "completed",
		"reason": "durability plan witness", "idempotency_key": "durability-plan-lifecycle-approved",
		"evidence": evidence, "approval": map[string]any{"approval_ref": "approval-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	approvedPlan := newMutationPlan(envelope, lifecycleOp)
	approvedAnswer, approvedErr, approvedHandled := r.planLifecycle(context.Background(), agentcontract.NewBase("durability-plan", "concord_work_transition", "lifecycle"), approvedRaw, digest, Authority{}, lifecycleOp, approvedPlan)
	if approvedHandled || approvedErr != nil || approvedAnswer.Outcome == agentcontract.OutcomeError {
		t.Fatalf("approved lifecycle planner refused: handled=%t err=%v answer=%+v", approvedHandled, approvedErr, approvedAnswer)
	}
	if !plannedFact(t, approvedPlan) {
		t.Error("terminal lifecycle plan with an attached approval lost its durable fact")
	}
	if approvedPlan.approval != "approval-1" {
		t.Errorf("approval reference %q was not carried on the plan", approvedPlan.approval)
	}

	overlapOp := registeredOp(t, "concord_work_relate", "resolve_overlap")
	for _, tc := range []struct {
		name     string
		kind     string
		durable  bool
		approval bool
	}{
		{"compatible_with", "compatible_with", true, false},
		{"depends_on", "depends_on", false, false},
		{"blocks", "blocks", true, false},
		{"merged_into", "merged_into", true, false},
		{"supersedes", "supersedes", true, false},
	} {
		raw, err := json.Marshal(map[string]any{
			"from_work_id": "work-1", "to_work_id": "work-2",
			"from_expected_version": workVersion(t, s, "work-1"), "to_expected_version": workVersion(t, s, "work-2"),
			"from_contract_version": 1, "to_contract_version": 1,
			"resolution_kind": tc.kind, "reason": "durability plan witness",
			"idempotency_key": "durability-plan-overlap-" + tc.name,
		})
		if err != nil {
			t.Fatal(err)
		}
		plan := newMutationPlan(envelope, overlapOp)
		answer, planErr, handled := r.planResolveOverlap(context.Background(), agentcontract.NewBase("durability-plan", "concord_work_relate", "resolve_overlap"), raw, digest, Authority{}, overlapOp, plan)
		if handled || planErr != nil || answer.Outcome == agentcontract.OutcomeError {
			t.Fatalf("overlap %s planner refused: handled=%t err=%v answer=%+v", tc.kind, handled, planErr, answer)
		}
		if got := plannedFact(t, plan); got != tc.durable {
			t.Errorf("overlap resolution %s planned initial durability %t, want %t", tc.kind, got, tc.durable)
		}
		if plan.requiresApproval != tc.approval {
			t.Errorf("overlap resolution %s approval requirement %t does not follow its kind boundary, want %t", tc.kind, plan.requiresApproval, tc.approval)
		}
	}

	// CD-0243 D3/D7 retire merger and supersession as consent triggers.
	// An explicitly supplied claim is still validated, without changing durability.
	approvedOverlapRaw, err := json.Marshal(map[string]any{
		"from_work_id": "work-1", "to_work_id": "work-2",
		"from_expected_version": workVersion(t, s, "work-1"), "to_expected_version": workVersion(t, s, "work-2"),
		"from_contract_version": 1, "to_contract_version": 1,
		"resolution_kind": "merged_into", "reason": "durability plan witness",
		"idempotency_key": "durability-plan-overlap-approved",
		"approval":        map[string]any{"approval_ref": "approval-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	approvedOverlapPlan := newMutationPlan(envelope, overlapOp)
	approvedOverlapAnswer, approvedOverlapErr, approvedOverlapHandled := r.planResolveOverlap(context.Background(), agentcontract.NewBase("durability-plan", "concord_work_relate", "resolve_overlap"), approvedOverlapRaw, digest, Authority{}, overlapOp, approvedOverlapPlan)
	if approvedOverlapHandled || approvedOverlapErr != nil || approvedOverlapAnswer.Outcome == agentcontract.OutcomeError {
		t.Fatalf("approved overlap planner refused: handled=%t err=%v answer=%+v", approvedOverlapHandled, approvedOverlapErr, approvedOverlapAnswer)
	}
	if !plannedFact(t, approvedOverlapPlan) {
		t.Error("destructive overlap plan with an attached approval lost its durable fact")
	}
	if !approvedOverlapPlan.requiresApproval {
		t.Error("destructive overlap plan with an attached approval lost its consent requirement")
	}
	if approvedOverlapPlan.approval != "approval-1" {
		t.Errorf("approval reference %q was not carried on the plan", approvedOverlapPlan.approval)
	}

	membershipsOp := registeredOp(t, "concord_work_relate", "set_memberships")
	membershipsRaw, err := json.Marshal(map[string]any{
		"work_id": "work-1", "expected_version": 2,
		"memberships":     []map[string]string{{"project_id": "project-1", "role": "primary"}},
		"idempotency_key": "durability-plan-memberships",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := newMutationPlan(envelope, membershipsOp)
	answer, planErr, handled := r.planSetMemberships(context.Background(), agentcontract.NewBase("durability-plan", "concord_work_relate", "set_memberships"), membershipsRaw, digest, Authority{}, membershipsOp, plan)
	if handled || planErr != nil || answer.Outcome == agentcontract.OutcomeError {
		t.Fatalf("set_memberships planner refused: handled=%t err=%v answer=%+v", handled, planErr, answer)
	}
	if !plannedFact(t, plan) {
		t.Error("membership replacement did not raise its durable effect boundary")
	}
	// CD-0243 D3: ordinary membership coordination is agent-owned.
	if plan.requiresApproval {
		t.Error("ordinary membership replacement unexpectedly requires approval")
	}
}

// TestMutationDurabilityStoreBoundPlannerFacts checks that governing scope
// cuts persist their challenges, while worktree removal adds no consent write.
func TestMutationDurabilityStoreBoundPlannerFacts(t *testing.T) {
	t.Parallel()
	t.Run("capture governing conflict", func(t *testing.T) {
		t.Parallel()
		s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
		if err := pm1fixture.SeedGoverningRequirement(context.Background(), s, "proj-web", "audit_required", "accepted audit obligation"); err != nil {
			t.Fatalf("seed governing requirement: %v", err)
		}
		env := agentJobsMutationEnvelope(t, s, grant, "proj-web")
		input := []byte(`{"title":"Durable cut","value_statement":"witness the governing durability fact","kind":"task","project_ids":["proj-web"],"idempotency_key":"durability-governing-1"}`)
		before := s.DurableCommits()
		refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
		if refused.Error == nil || refused.Error.Details["approval_ref"] == nil {
			t.Fatalf("governing conflict minted no challenge: outcome=%s err=%+v", refused.Outcome, refused.Error)
		}
		if after := s.DurableCommits(); after != before+1 {
			t.Fatalf("governing-conflict challenge moved the durable counter %d -> %d, want +1", before, after)
		}
	})
	t.Run("worktree_destroy non-terminal", func(t *testing.T) {
		t.Parallel()
		s, service, grant, _, _, _ := tiersFixture(t)
		// CD-0243 D7 retains occupancy admission, not nonterminal consent.
		before := s.DurableCommits()
		challenges := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`)
		occupied := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_destroy", map[string]any{"work_id": "work-1", "expected_version": 3, "default_ref": "main", "idempotency_key": "durability-destroy-occupied"})
		if occupied.Error == nil || occupied.Error.Kind != "unauthorized" {
			t.Fatalf("recorded occupancy destroy = %+v, want occupancy refusal", occupied.Error)
		}
		if after := s.DurableCommits(); after != before {
			t.Fatalf("occupancy refusal moved durable counter %d -> %d", before, after)
		}
		if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != challenges {
			t.Fatalf("occupancy refusal minted %d challenges", got-challenges)
		}
		if lifecycle, version := readWorkFromStore(t, s, "work-1"); lifecycle != "needed" || version != 3 {
			t.Fatalf("occupancy refusal changed work to %s v%d", lifecycle, version)
		}
		vacateLinkedWorktree(t, s, service, grant, filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1"), "durability-nonterminal-vacate")
		removed := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_destroy", map[string]any{"work_id": "work-1", "expected_version": 3, "default_ref": "main", "idempotency_key": "durability-destroy-1"})
		if removed.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("clean unoccupied non-terminal destroy = %+v", removed.Error)
		}
		if lifecycle, version := readWorkFromStore(t, s, "work-1"); lifecycle != "needed" || version != 4 {
			t.Fatalf("nonterminal removal changed disposition or lost version: %s v%d", lifecycle, version)
		}
		if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != challenges {
			t.Fatalf("ordinary non-terminal destroy minted %d challenges", got-challenges)
		}
	})
	t.Run("worktree_destroy destructive", func(t *testing.T) {
		t.Parallel()
		s, service, grant, _, _, _ := tiersFixture(t)
		completeWork(t, s, "work-1")
		vacateLinkedWorktree(t, s, service, grant, filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1"), "durability-destructive-vacate")
		before := s.DurableCommits()
		challenges := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`)
		removed := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_destroy", map[string]any{"work_id": "work-1", "expected_version": 4, "default_ref": "main", "destructive": true, "idempotency_key": "durability-destructive-1"})
		if removed.Outcome != agentcontract.OutcomeOK || removed.Error != nil {
			t.Fatalf("destructive destroy outcome=%s err=%+v, want removal", removed.Outcome, removed.Error)
		}
		if after := s.DurableCommits(); after != before {
			t.Fatalf("destructive destroy added a consent durability write: %d -> %d", before, after)
		}
		if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != challenges {
			t.Fatalf("destructive destroy minted %d challenges", got-challenges)
		}
	})
}
