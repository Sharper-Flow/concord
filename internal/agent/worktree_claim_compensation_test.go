package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The agent worktree_claim mutation owns the commit: the claim's git worktree
// is created inside the transaction, and every failure after the effect —
// enrichment, result validation, idempotency, the commit itself — rolls the
// durable claim back. The effect must therefore report the creation, and the
// plan's cleanup must remove the tree and branch it created when the
// transaction fails, instead of stranding the claim's native state.
func TestWorktreeClaimEffectReportsCreationAndCleanupCompensates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, repoRoot, baseSHA := worktreeDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	env := mutationEnvelope(grant, scopeVersion)
	r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_transition", Operation: "worktree_claim"}
	op, ok := ValidateContractOperation("concord_work_transition", "worktree_claim")
	if !ok {
		t.Fatal("worktree_claim is not a registered contract operation")
	}
	raw, err := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-1", "base_sha": baseSHA, "expected_version": 2, "idempotency_key": "claim-compensation-1"})
	if err != nil {
		t.Fatal(err)
	}
	plan := newMutationPlan(env, op)
	if _, _, handled := r.planWorktreeClaim(ctx, Envelope{}, raw, "sha256:"+strings.Repeat("c", 64), grant, op, plan); handled {
		t.Fatal("planWorktreeClaim must defer to the effect, not answer directly")
	}
	if plan.effect == nil || plan.nativeCleanup == nil {
		t.Fatalf("plan must carry an effect and a native cleanup: effect=%v cleanup=%v", plan.effect != nil, plan.nativeCleanup != nil)
	}
	rollbackCause := errors.New("cannot commit claim")
	transactErr := s.Transact(ctx, func(tx *store.Transaction) error {
		if _, _, _, effectErr := plan.effect(ctx, tx, grant); effectErr != nil {
			return effectErr
		}
		return rollbackCause
	})
	if !errors.Is(transactErr, rollbackCause) {
		t.Fatalf("transact error=%v, want the caller-owned rollback", transactErr)
	}
	if plan.nativeCreation == nil || plan.nativeCreation.Path != worktreePath || plan.nativeCreation.Branch != "work/work-1" || plan.nativeCreation.Base != baseSHA || plan.nativeCreation.RepoRoot != repoRoot || !plan.nativeCreation.CreatedBranch {
		t.Fatalf("creation report=%+v, want the claimed tree and branch", plan.nativeCreation)
	}
	if listing := gitRun(t, repoRoot, "worktree", "list", "--porcelain"); !strings.Contains(listing, "work-1") {
		t.Fatalf("claim effect did not create the native worktree:\n%s", listing)
	}
	if err := plan.nativeCleanup(ctx, transactErr); err != rollbackCause {
		var failure *store.Failure
		if errors.As(err, &failure) && failure.EffectPossible {
			t.Fatalf("cleanup could not prove removal: %v", err)
		}
		t.Fatalf("cleanup error=%v, want the cause returned unchanged", err)
	}
	if listing := gitRun(t, repoRoot, "worktree", "list", "--porcelain"); strings.Contains(listing, "work-1") {
		t.Fatalf("cleanup left the created worktree in place:\n%s", listing)
	}
	if out := gitRun(t, repoRoot, "branch", "--list", "work/work-1"); out != "" {
		t.Fatalf("cleanup left the created branch in place: %s", out)
	}
}

// A refusal that lands after the effect — here a result payload that fails
// closed-schema validation, standing in for the enrichment, validation,
// idempotency, and commit failures that share the window — must invoke the
// native cleanup and leave no durable state behind.
func TestExecuteMutationCompensatesNativeCreationWhenTheResultIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _, _ := worktreeDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_transition", Operation: "worktree_claim", Reader: grant}
	raw, err := json.Marshal(map[string]any{"work_id": "work-1", "project_id": "project-1", "base_sha": strings.Repeat("b", 40), "expected_version": 2, "idempotency_key": "cleanup-window-1"})
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("d", 64)
	scope := map[string]any{"work_ids": []string{"work-1"}, "project_ids": []string{"project-1"}}
	versions := map[string]any{"work": 2}
	cleanupCalls := 0
	cleanup := func(ctx context.Context, cause error) error {
		cleanupCalls++
		return cause
	}
	probeKey := store.MutationIdempotencyKey{PrincipalRef: "principal-1", Tool: "concord_work_transition", OperationKind: "worktree_claim", IdempotencyKey: "cleanup-window-probe"}
	effect := func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []ChangedRef, error) {
		// A durable write in the effect's transaction; the refusal after the
		// effect must roll it back with the rest of the claim.
		if err := store.InsertMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyInsert{
			Key: probeKey, CanonicalDigest: digest, OperationID: "mutation-cleanup-window-probe",
			ResultEventIDs: "[]", ResultPayload: "{}", ChangedRefs: "[]", AuthorizedScopeSnapshot: "{}",
			ObservedAt: fixedTime(),
		}); err != nil {
			return nil, nil, nil, err
		}
		// The enriched result fails closed-schema validation: the mutation is
		// refused after the effect and before the commit.
		return json.RawMessage(`{}`), nil, nil, nil
	}
	response, err := r.executeMutation(ctx, Envelope{}, raw, digest, scope, versions, "lifecycle", "", false, nil, nil, effect, cleanup)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError {
		t.Fatalf("schema-invalid result must refuse the mutation: outcome=%+v", response.Outcome)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly one compensation attempt", cleanupCalls)
	}
	if _, found, err := s.LookupMutationIdempotency(ctx, probeKey); err != nil || found {
		t.Fatalf("refused result left durable state behind: found=%v err=%v", found, err)
	}
}
