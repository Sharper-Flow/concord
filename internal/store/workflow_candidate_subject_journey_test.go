package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

type candidateJourney struct {
	fixture        workContextFixture
	s              *Store
	workID         string
	repo           string
	acceptor       WorkflowActor
	reviewReceipts []string
}

func (j *candidateJourney) commit(t *testing.T, author, message string) string {
	t.Helper()
	gitRunStore(t, j.repo, "-c", "user.email="+author, "-c", "user.name="+author, "add", ".")
	gitRunStore(t, j.repo, "-c", "user.email="+author, "-c", "user.name="+author, "commit", "-m", message)
	return j.head(t)
}

func (j *candidateJourney) head(t *testing.T) string {
	t.Helper()
	return runTestGit(t, j.repo, "rev-parse", "HEAD")
}

func (j *candidateJourney) action(t *testing.T, action string, payload any, actor WorkflowActor) error {
	t.Helper()
	if action == "record_review" || action == "confirm_premise" || action == "complete" {
		return j.terminalAction(t, action, mustJSONValue(payload), actor)
	}
	return runVerdictActionAs(t, j.s, j.workID, action, mustJSONValue(payload), 0, actor)
}

func (j *candidateJourney) terminalAction(t *testing.T, action string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(t.Context(), j.s, j.workID, action)
	if err != nil {
		return err
	}
	return j.terminalActionContext(t, ctx, action, payload, actor)
}

func (j *candidateJourney) terminalActionContext(t *testing.T, ctx context.Context, action string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	version := readWorkVersion(t, j.s, j.workID)
	id := fmt.Sprintf("%s-%s-%d", action, j.workID, version)
	return j.s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, WorkflowActionExecutionRequest{
			WorkID: j.workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: actor,
			EvidenceRefs: actionEvidenceRefs(action, payload), AcceptedInputsDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(id))), IdempotencyIdentity: id,
			OperationID: id, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id,
			RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		return err
	})
}

func (j *candidateJourney) effects(t *testing.T) string {
	t.Helper()
	return outsideRepairDatabaseSnapshot(t, j.s)
}
