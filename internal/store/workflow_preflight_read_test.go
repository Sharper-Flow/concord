package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func TestWorkflowPreflightReadsCommittedStateWhileWriterIsActive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	const workID = "preflight-read"
	seedStepWork(t, s, workID)
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Ref == "workflow.implementation" {
			initializeStepWorkflow(t, s, workID, definition)
			break
		}
	}
	version := readWorkVersion(t, s, workID)
	step := readInstanceStep(t, s, workID)
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
		ActionID: "record_proposal", Actor: secondSessionActor(),
		Payload: json.RawMessage(`{"problem":"A bounded problem.","affected":["A bounded system."],"stakes":"A bounded consequence.","user_outcomes":["A bounded outcome."]}`),
	}
	inspect := func(request WorkflowActionPreflightRequest) error {
		return WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), request)
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
	if afterEvents != eventCount || actors != 0 || readInstanceStep(t, s, workID) != step || readWorkVersion(t, s, workID) != version {
		t.Fatalf("preflight changed committed state: events=%d (want %d), new actors=%d", afterEvents, eventCount, actors)
	}

	// Admission is advisory: the owning write boundary must reread state.
	if err := writeTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, request.ActionID, request.Payload, 0, stepFixtureActor()); err != nil {
		t.Fatalf("advance state after inspection: %v", err)
	}
	mutated := false
	err = AuthorizeWorkflowActionAtBoundaryTx(ctx, s, BuiltinWorkflowRegistry(), request, nil, func(*Transaction) error {
		mutated = true
		return nil
	})
	assertFailureKind(t, err, KindVersionConflict)
	if mutated {
		t.Fatal("the owning boundary mutated from stale preflight evidence")
	}
}
