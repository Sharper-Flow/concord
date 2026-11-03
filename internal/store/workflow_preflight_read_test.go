package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func TestWorkflowPreflightReadsCommittedStateWhileWriterIsActive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	const workID = "preflight-read"
	seedStepWork(t, s, workID)
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: stepFixtureActor(), Now: time.Unix(5, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	var eventCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}

	writer, err := sql.Open(driverName, dataSourceName(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writeTx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writeTx.Rollback() }()

	request := WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: version,
		ActionID: "record_intake", Actor: secondSessionActor(),
		Payload: json.RawMessage(`{"outcome":"proceed","open_questions_found":false}`),
	}
	inspect := func(request WorkflowActionPreflightRequest) error {
		_, err := WorkflowActionDefinitionFor(ctx, s, request)
		return err
	}
	if err := inspect(request); err != nil {
		t.Fatalf("preflight while another connection holds the write lock: %v", err)
	}
	stale := request
	stale.ExpectedVersion = version + 1
	assertFailureKind(t, inspect(stale), KindVersionConflict)
	malformed := request
	malformed.Payload = json.RawMessage(`{}`)
	assertFailureKind(t, inspect(malformed), KindInvalidPayload)

	// Inspection must not record the previously unseen actor or any event.
	var afterEvents, actors int
	actorRef, err := WorkflowActorRef(request.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&actors); err != nil {
		t.Fatal(err)
	}
	var afterState string
	if err := s.db.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&afterState); err != nil {
		t.Fatal(err)
	}
	if afterEvents != eventCount || actors != 0 || afterState != state || readWorkVersion(t, s, workID) != version {
		t.Fatalf("preflight changed committed state: events=%d (want %d), new actors=%d", afterEvents, eventCount, actors)
	}

	// Admission is advisory: the owning write boundary must reread state.
	if err := writeTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		actor := stepFixtureActor()
		_, err := ApplyWorkflowActionTx(ctx, tx, WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: request.ActionID, Payload: request.Payload, Actor: actor, OperationID: "preflight-advance", IdempotencyIdentity: "preflight-advance", PrincipalRef: actor.PrincipalRef, RequestID: "preflight-advance", AcceptedInputsDigest: testManifestDigest, ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC()})
		return err
	}); err != nil {
		t.Fatalf("advance state after inspection: %v", err)
	}
	mutated := false
	err = AuthorizeWorkflowActionAtBoundaryTx(ctx, s, request, nil, func(*Transaction) error {
		mutated = true
		return nil
	})
	assertFailureKind(t, err, KindVersionConflict)
	if mutated {
		t.Fatal("the owning boundary mutated from stale preflight evidence")
	}
}
