package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// The action transaction refuses a missing or changed boundary snapshot.
func TestStartExecutionStoreGuardRefusesMissingOrMismatchedTakeover(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t)
	if first := f.action(t, f.owner, "start_execution", "owner-start"); first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("owner start_execution: %+v", first.Error)
	}
	ctx := context.Background()
	actor := store.WorkflowActor{PrincipalRef: f.otherGrant.PrincipalRef, ClientRef: f.otherGrant.ClientRef, AgentRef: f.otherGrant.AgentRef, SessionRef: f.otherGrant.SessionRef, ActorClass: store.ActorAgent}
	for _, tc := range []struct {
		name     string
		approved *store.WorkflowStartTakeover
		want     store.FailureKind
	}{
		{name: "unapproved", want: store.KindOperationConflict},
		{name: "other-epoch", approved: &store.WorkflowStartTakeover{HeldEpoch: 2, HolderActorRef: actorRefOf(f.ownerGrant)}, want: store.KindOperationConflict},
	} {
		operationID := "direct-takeover-" + tc.name
		request := store.WorkflowActionExecutionRequest{WorkID: "work-1", ExpectedVersion: workVersion(t, f.s, "work-1"), ActionID: "start_execution", Payload: json.RawMessage(`{}`), Actor: actor, SessionWorktree: f.otherGrant.Worktree, ExpectedTakeover: tc.approved, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: operationID, OperationID: operationID, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID, ContractDigest: agentcontract.ManifestDigest, Now: fixedTime()}
		err := f.s.Transact(ctx, func(tx *store.Transaction) error {
			_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), request)
			return err
		})
		var failure *store.Failure
		if !errors.As(err, &failure) || failure.Kind != tc.want {
			t.Fatalf("%s direct start_execution = %v, want %s", tc.name, err, tc.want)
		}
	}
	if epoch, startActor := latestExecutionStart(t, f.s); epoch != 1 || startActor != actorRefOf(f.ownerGrant) {
		t.Fatalf("refused direct takeover moved the attempt: epoch %d actor %s", epoch, startActor)
	}
}
