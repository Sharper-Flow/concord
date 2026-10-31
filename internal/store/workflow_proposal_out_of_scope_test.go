package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The current implementation definition admits bounded out_of_scope prose.
// Retired definitions are not executable pins.
func TestRecordProposalOutOfScopeAcrossPinnedAndCurrentVersions(t *testing.T) {
	t.Parallel()
	current, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"problem":"The recorded problem.","affected":["The affected party."],"stakes":"The recorded stakes.","user_outcomes":["The expected outcome."],"out_of_scope":["The launcher.","The release automation."]}`)
	if err := validateWorkflowActionPayload(current.Definition, "record_proposal", payload); err != nil {
		t.Fatalf("the current version refused the out_of_scope list: %v", err)
	}
	tooWide, marshalErr := json.Marshal(map[string]any{
		"problem": "The recorded problem.", "affected": []string{"The affected party."},
		"stakes": "The recorded stakes.", "user_outcomes": []string{"The expected outcome."},
		"out_of_scope": seventeenProseEntries(),
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err := validateWorkflowActionPayload(current.Definition, "record_proposal", tooWide); err == nil {
		t.Fatal("the current version admitted more than sixteen out_of_scope entries")
	}

	s := openTemp(t)
	defer s.Close()
	const workID = "work-out-of-scope"
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, current.Definition)
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	actor := stepFixtureActor()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "record_proposal", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:out-of-scope", IdempotencyIdentity: "out-of-scope", OperationID: "out-of-scope",
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "out-of-scope",
		RequestID: "request:out-of-scope", ContractDigest: testManifestDigest, Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	})
	if actionErr != nil {
		_ = tx.Rollback()
		t.Fatal(actionErr)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT out_of_scope FROM workflow_proposal_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var outOfScope []string
	if err := json.Unmarshal([]byte(raw), &outOfScope); err != nil {
		t.Fatal(err)
	}
	if len(outOfScope) != 2 || outOfScope[0] != "The launcher." || outOfScope[1] != "The release automation." {
		t.Fatalf("persisted out_of_scope = %v", outOfScope)
	}
}

// seventeenProseEntries returns seventeen distinct prose entries, each inside
// the per-entry bound, so the list test exceeds only the count bound.
func seventeenProseEntries() []string {
	entries := make([]string, 0, 17)
	for index := 0; index < 17; index++ {
		entries = append(entries, "The excluded scope entry "+jsonInt(int64(index))+".")
	}
	return entries
}
