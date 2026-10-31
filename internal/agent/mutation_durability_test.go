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

func TestMutationDurabilityUsesDeclaredBaseline(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	_, registered := agentcontract.ValidateContractOperation("concord_work_transition", "correct_delivery")
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
		"idempotency_key":        "durability-baseline-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := mutationDigest("concord_work_transition", "correct_delivery", env, raw)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "scope_version": scopeVersion, "work_ids": []string{"work-1"}}
	effect := func(ctx context.Context, tx *store.Transaction, _ Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		// The committed effect rides the executor's own idempotency boundary;
		// an empty changed-ref list and a closed-schema result payload are the
		// whole domain surface this witness needs.
		return mutationPayload(nil, nil), nil, nil, nil
	}
	before := s.DurableCommits()
	r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_transition", Operation: "correct_delivery", Reader: grant}
	response, err := r.executeMutation(ctx, agentcontract.NewBase("durability-baseline", "concord_work_transition", "correct_delivery"), raw, digest, scope, nil, nil, effect, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeOK || response.Error != nil || response.Replayed {
		t.Fatalf("durable mutation did not commit: outcome=%+v error=%+v replayed=%t", response.Outcome, response.Error, response.Replayed)
	}
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", OperationKind: "correct_delivery", IdempotencyKey: "durability-baseline-1"}
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
		t.Fatalf("durable mutation advanced the durable counter by %d, want exactly 1", after-before)
	}
}

// TestMutationDurabilityDeclarations enumerates every registered contract
// operation against the closed durability declaration table. Read operations
// must refuse with a typed durability invariant error. Each registered
// mutation must retain its initial and replay baselines. No declaration may
// exist for an operation the manifest does not register as a mutation.
// Conditional planner facts override the initial baseline in the executor.
func TestMutationDurabilityDeclarations(t *testing.T) {
	t.Parallel()

	durableInitial := map[string]bool{
		"concord_work_transition.remove":                   true,
		"concord_work_transition.worktree_claim":           true,
		"concord_work_relate.resolve_overlap":              true,
		"concord_work_compact.publish":                     true,
		"concord_work_transition.correct_delivery":         true,
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
// resolution kind, the membership replacement boundary. Cases plan with
// authenticated input shapes and inspect
// the produced plan without executing effects. Capture and destroy facts
// need store state, so TestMutationDurabilityStoreBoundPlannerFacts
// witnesses them through dispatch.
func TestMutationDurabilityConditionalPlans(t *testing.T) {
	t.Parallel()

	// Terminal overlap planning reads the captured governing requirements;
	// use the real canonical-source fixture rather than an absent store.
	s, _, _ := routineCoordinationFixture(t)
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
		plan := newMutationPlan(envelope)
		answer, planErr, handled := r.planLifecycle(context.Background(), agentcontract.NewBase("durability-plan", "concord_work_transition", "lifecycle"), raw, digest, Authority{}, lifecycleOp, plan)
		if handled || planErr != nil || answer.Outcome == agentcontract.OutcomeError {
			t.Fatalf("lifecycle %s planner refused: handled=%t err=%v answer=%+v", tc.target, handled, planErr, answer)
		}
		if got := plannedFact(t, plan); got != tc.durable {
			t.Errorf("lifecycle target %s planned initial durability %t, want %t", tc.target, got, tc.durable)
		}
	}

	overlapOp := registeredOp(t, "concord_work_relate", "resolve_overlap")
	for _, tc := range []struct {
		name    string
		kind    string
		durable bool
	}{
		{"depends_on", "depends_on", false},
		{"blocks", "blocks", true},
		{"merged_into", "merged_into", true},
		{"supersedes", "supersedes", true},
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
		plan := newMutationPlan(envelope)
		answer, planErr, handled := r.planResolveOverlap(context.Background(), agentcontract.NewBase("durability-plan", "concord_work_relate", "resolve_overlap"), raw, digest, Authority{}, overlapOp, plan)
		if handled || planErr != nil || answer.Outcome == agentcontract.OutcomeError {
			t.Fatalf("overlap %s planner refused: handled=%t err=%v answer=%+v", tc.kind, handled, planErr, answer)
		}
		if got := plannedFact(t, plan); got != tc.durable {
			t.Errorf("overlap resolution %s planned initial durability %t, want %t", tc.kind, got, tc.durable)
		}
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
	plan := newMutationPlan(envelope)
	answer, planErr, handled := r.planSetMemberships(context.Background(), agentcontract.NewBase("durability-plan", "concord_work_relate", "set_memberships"), membershipsRaw, digest, Authority{}, membershipsOp, plan)
	if handled || planErr != nil || answer.Outcome == agentcontract.OutcomeError {
		t.Fatalf("set_memberships planner refused: handled=%t err=%v answer=%+v", handled, planErr, answer)
	}
	if !plannedFact(t, plan) {
		t.Error("membership replacement did not raise its durable effect boundary")
	}
}

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
		if refused.Error == nil || refused.Error.Kind != "unauthorized" || refused.Error.EffectState != agentcontract.EffectNone {
			t.Fatalf("governing cut without cross_scope did not refuse before effects: outcome=%s err=%+v", refused.Outcome, refused.Error)
		}
		if after := s.DurableCommits(); after != before {
			t.Fatalf("governing-scope refusal moved the durable counter %d -> %d", before, after)
		}
	})
	t.Run("worktree_destroy non-terminal", func(t *testing.T) {
		t.Parallel()
		s, service, grant, _, _, _ := tiersFixture(t)
		before := s.DurableCommits()
		occupied := authorityInvoke(t, s, service, grant, "worktree_destroy", map[string]any{"work_id": "work-1", "expected_version": 3, "default_ref": "main", "idempotency_key": "durability-destroy-occupied"})
		if occupied.Error == nil || occupied.Error.Kind != "unauthorized" {
			t.Fatalf("recorded occupancy destroy = %+v, want occupancy refusal", occupied.Error)
		}
		if after := s.DurableCommits(); after != before {
			t.Fatalf("occupancy refusal moved durable counter %d -> %d", before, after)
		}
		if lifecycle, version := readWorkFromStore(t, s, "work-1"); lifecycle != "needed" || version != 3 {
			t.Fatalf("occupancy refusal changed work to %s v%d", lifecycle, version)
		}
		vacateLinkedWorktree(t, s, service, grant, filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1"), "durability-nonterminal-vacate")
		removed := authorityInvoke(t, s, service, grant, "worktree_destroy", map[string]any{"work_id": "work-1", "expected_version": 3, "default_ref": "main", "idempotency_key": "durability-destroy-1"})
		if removed.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("clean unoccupied non-terminal destroy = %+v", removed.Error)
		}
		if lifecycle, version := readWorkFromStore(t, s, "work-1"); lifecycle != "needed" || version != 4 {
			t.Fatalf("nonterminal removal changed disposition or lost version: %s v%d", lifecycle, version)
		}
		if after := s.DurableCommits(); after != before {
			t.Fatalf("non-terminal destroy moved durable counter %d -> %d", before, after)
		}
	})
	t.Run("worktree_destroy destructive", func(t *testing.T) {
		t.Parallel()
		s, service, grant, _, _, _ := tiersFixture(t)
		completeWork(t, s, "work-1")
		vacateLinkedWorktree(t, s, service, grant, filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1"), "durability-destructive-vacate")
		before := s.DurableCommits()
		removed := authorityInvoke(t, s, service, grant, "worktree_destroy", map[string]any{"work_id": "work-1", "expected_version": 4, "default_ref": "main", "destructive": true, "idempotency_key": "durability-destructive-1"})
		if removed.Outcome != agentcontract.OutcomeOK || removed.Error != nil {
			t.Fatalf("destructive destroy outcome=%s err=%+v, want removal", removed.Outcome, removed.Error)
		}
		if after := s.DurableCommits(); after != before {
			t.Fatalf("destructive destroy moved durable counter %d -> %d", before, after)
		}
	})
}
