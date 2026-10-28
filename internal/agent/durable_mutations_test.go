package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func durableCommitCount(t *testing.T, s *store.Store) uint64 {
	t.Helper()
	return s.DurableCommits()
}

func TestOrdinaryObservationKeepsNormalDurability(t *testing.T) {
	ctx := context.Background()
	s, service, grant := crossProductPolicyFixture(t, []agentcontract.Capability{"product_read", "work_define", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	before := durableCommitCount(t, s)
	request := InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: json.RawMessage(`{"work_id":"work-1","statement":"ordinary progress","idempotency_key":"normal-observation"}`)}
	result, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || result.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("ordinary observation=%+v err=%v", result, err)
	}
	if after := durableCommitCount(t, s); after != before {
		t.Fatalf("ordinary observation changed durability marker: %d -> %d", before, after)
	}
	replay, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("ordinary replay=%+v err=%v", replay, err)
	}
	if after := durableCommitCount(t, s); after != before {
		t.Fatalf("ordinary replay changed durability count: %d -> %d", before, after)
	}
}

func TestDerivedApprovalAuthorityCommitsDurably(t *testing.T) {
	ctx := context.Background()
	// CD-0243 D3/D7 retain security approval durability, not observation consent.
	s, service, grant, key := productProjectLinkFixture(t, []agentcontract.Capability{"work_relate", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := productProjectLinkRequest("", "project-2")
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: input}
	before := durableCommitCount(t, s)
	challenge, err := Dispatch(ctx, s, service, request, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("derived challenge=%+v err=%v", challenge, err)
	}
	if after := durableCommitCount(t, s); after != before+1 {
		t.Fatalf("challenge did not advance the durable marker: %d -> %d", before, after)
	}
	if count := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); count != 1 {
		t.Fatalf("scope replan minted %d challenges, want one", count)
	}
	ref := challenge.Error.Details["approval_ref"].(string)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1", "product-2"}, "project_id": "project-2", "role": "secondary", "scope_version": scopeVersion}
	env.HostApproval = signedHostApproval(key, ref, mutationDigest(request.Tool, request.Operation, env, input), scope, map[string]any{"product": 2}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "durable-access-approval")
	request.Input = productProjectLinkRequest(ref, "project-2")
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Error != nil || approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approved access grant=%+v err=%v", approved, err)
	}
	if after := durableCommitCount(t, s); after != before+2 {
		t.Fatalf("approval consumption did not commit a durable boundary: want count %d, got %d", before+2, after)
	}
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("approved replay=%+v err=%v", replay, err)
	}
	if after := durableCommitCount(t, s); after != before+3 {
		t.Fatalf("approval replay did not commit durably: want count %d, got %d", before+3, after)
	}
}

func TestMutationReplayDurabilityBindings(t *testing.T) {
	for _, tc := range []struct {
		name        string
		legacy      bool
		binding     any
		conditional bool
		wantDurable bool
		wantError   string
	}{
		{name: "ordinary", binding: false},
		{name: "durable", binding: true, wantDurable: true},
		{name: "legacy ordinary", legacy: true},
		{name: "legacy conditional", legacy: true, conditional: true, wantDurable: true},
		{name: "malformed string", binding: "true", wantError: "invariant_violation"},
		{name: "malformed null", binding: nil, wantError: "invariant_violation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant := crossProductPolicyFixture(t, []agentcontract.Capability{"product_read", "work_define", "cross_scope"})
			scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			request := InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: json.RawMessage(`{"work_id":"work-1","statement":"replay binding","idempotency_key":"replay-binding"}`)}
			result, err := Dispatch(ctx, s, service, request, env)
			if err != nil || result.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("initial observation=%+v err=%v", result, err)
			}
			key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: request.Tool, OperationKind: request.Operation, IdempotencyKey: "replay-binding"}
			record, found, err := s.LookupMutationIdempotency(ctx, key)
			if err != nil || !found {
				t.Fatalf("idempotency record: found=%t err=%v", found, err)
			}
			var snapshot map[string]any
			if err := json.Unmarshal([]byte(record.AuthorizedScopeSnapshot), &snapshot); err != nil {
				t.Fatal(err)
			}
			if tc.legacy {
				delete(snapshot, "durable_commit")
			} else {
				snapshot["durable_commit"] = tc.binding
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DatabaseForTesting().ExecContext(ctx, `UPDATE idempotency_records SET authorized_scope_snapshot=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, string(encoded), key.PrincipalRef, key.Tool, key.OperationKind, key.IdempotencyKey); err != nil {
				t.Fatal(err)
			}
			op, registered := agentcontract.ValidateContractOperation(request.Tool, request.Operation)
			if !registered {
				t.Fatal("observation operation is not registered")
			}
			if tc.conditional {
				op.Approval = agentcontract.ApprovalClass("conditional")
			}
			before := s.DurableCommits()
			r := runtime{Store: s, Authority: service, Tool: request.Tool, Operation: request.Operation, Envelope: env}
			replay, handled, err := r.replayMutationBeforeScope(ctx, agentcontract.NewBase("binding-replay", request.Tool, request.Operation), request.Input, grant, op)
			if err != nil || !handled {
				t.Fatalf("replay: handled=%t err=%v", handled, err)
			}
			if tc.wantError != "" {
				if replay.Error == nil || replay.Error.Kind != tc.wantError {
					t.Fatalf("replay error=%+v, want %s", replay.Error, tc.wantError)
				}
			} else if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
				t.Fatalf("replay=%+v", replay)
			}
			want := before
			if tc.wantDurable {
				want++
			}
			if got := s.DurableCommits(); got != want {
				t.Fatalf("durable commits=%d, want %d", got, want)
			}
		})
	}
}

func TestDurableCommitFailureEnvelopePreservesPossibleEffect(t *testing.T) {
	base := agentcontract.NewBase("durable-failure", "concord_work_relate", "client_policy_grant_request")
	err := &store.Failure{Kind: store.KindUnavailable, Op: "durable_transaction", Detail: "cannot finish the durable commit", RetrySafe: true, EffectPossible: true}
	response := failureEnvelope(base, err)
	if response.Error == nil || response.Error.Kind != "unreachable" || response.Error.EffectState != agentcontract.EffectPossible {
		t.Fatalf("durable commit failure lost its possible effect: %+v", response)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("possible-effect response is not deliverable: %v", err)
	}
}

func TestProductProjectApprovalCommitsDurably(t *testing.T) {
	ctx := context.Background()
	s, service, grant, key := productProjectLinkFixture(t, []agentcontract.Capability{"work_relate", "cross_scope"})
	before := durableCommitCount(t, s)
	ref, env, _ := productProjectLinkChallenge(t, s, service, grant, key)
	if after := durableCommitCount(t, s); after != before+1 {
		t.Fatalf("Product membership challenge did not commit durably: %d -> %d", before, after)
	}
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: productProjectLinkRequest(ref, "project-2")}
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approved Product membership=%+v err=%v", approved, err)
	}
	if after := durableCommitCount(t, s); after != before+2 {
		t.Fatalf("Product membership approval did not commit durably: want count %d, got %d", before+2, after)
	}
}
