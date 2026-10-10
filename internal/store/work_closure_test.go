package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// proposalEvent builds a typed proposal event carrying the excluded-scope
// list beside its sibling prose lists.
func proposalEvent(eventID, workID string, expected int64, outOfScope []string) Event {
	payload := map[string]any{
		"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
		"problem":        "The closure receipt shows only the status table, so the operator cannot see what shipped.",
		"affected":       []string{"The closure receipt surface."},
		"stakes":         "The operator reads the receipt at every close.",
		"user_outcomes":  []string{"The receipt names the delivered change."},
		"constraints":    []string{},
		"open_questions": []string{},
	}
	if outOfScope != nil {
		payload["out_of_scope"] = outOfScope
	}
	return workflowEvent(eventID, WorkflowProposalRecorded, workID, payload)
}

// CD-0202: the closure read carries the proposal problem, the effective
// delivery artifact, the excluded scope, and the raised follow-ups beside the
// pin, in one read, for the receipt alone.
func TestReadWorkClosureCarriesTheClosureFacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "work-closure-facts"
	s, _, version, _, _ := completedDeliveryFixture(t, workID)
	defer s.Close()
	if err := applyWorkflowTestOperationDirect(ctx, s, Operation{Events: []Event{proposalEvent("closure-proposal-"+workID, workID, version, []string{"The launcher stays untouched.", "No release automation changes."})}, ExpectedVersions: workVersion(workID, version)}); err != nil {
		t.Fatalf("proposal event refused: %v", err)
	}
	const firstFollowUp = "work-closure-follow-1"
	const secondFollowUp = "work-closure-follow-2"
	seedWork(t, s, firstFollowUp)
	seedWork(t, s, secondFollowUp)
	// The second item's relation lands first: rows follow work-item creation,
	// not relation creation.
	secondVersion := deliveryWorkVersion(t, s, secondFollowUp)
	if err := applyWorkEvent(t, s, relationAddedEvent("closure-rel-2", "raised_from", secondFollowUp, workID, secondVersion, secondVersion+1), workVersion(secondFollowUp, secondVersion)); err != nil {
		t.Fatalf("second raised_from refused: %v", err)
	}
	firstVersion := deliveryWorkVersion(t, s, firstFollowUp)
	if err := applyWorkEvent(t, s, relationAddedEvent("closure-rel-1", "raised_from", firstFollowUp, workID, firstVersion, firstVersion+1), workVersion(firstFollowUp, firstVersion)); err != nil {
		t.Fatalf("first raised_from refused: %v", err)
	}
	if _, err := s.RecordLinearIssueLink(ctx, LinearIssueLink{WorkID: firstFollowUp, RemoteIssueUUID: "remote-closure-1", HumanKey: "CON-777", URL: "https://linear.app/example/issue/CON-777"}); err != nil {
		t.Fatalf("link refused: %v", err)
	}

	closure, err := ReadWorkClosure(ctx, s, workID)
	if err != nil {
		t.Fatalf("closure read refused: %v", err)
	}
	if closure.Pin.WorkID != workID || closure.Pin.Lifecycle != "completed" {
		t.Fatalf("closure pin = %+v, want the completed pin", closure.Pin)
	}
	if closure.Problem != "The closure receipt shows only the status table, so the operator cannot see what shipped." {
		t.Fatalf("closure problem = %q", closure.Problem)
	}
	if len(closure.OutOfScope) != 2 || closure.OutOfScope[0] != "The launcher stays untouched." || closure.OutOfScope[1] != "No release automation changes." {
		t.Fatalf("closure out_of_scope = %v", closure.OutOfScope)
	}
	if closure.DeliveryArtifact == "" {
		t.Fatal("closure carries no delivery artifact")
	}
	if len(closure.FollowUps) != 2 {
		t.Fatalf("closure follow-ups = %+v, want two", closure.FollowUps)
	}
	if closure.FollowUps[0].WorkID != firstFollowUp || closure.FollowUps[0].LinearIssueKey != "CON-777" || closure.FollowUps[0].Title == "" {
		t.Fatalf("first follow-up = %+v, want the confirmed Linear key and the title", closure.FollowUps[0])
	}
	if closure.FollowUps[1].WorkID != secondFollowUp || closure.FollowUps[1].LinearIssueKey != "" {
		t.Fatalf("second follow-up = %+v, want the work ID fallback without a key", closure.FollowUps[1])
	}
}

// A correction overlays the shipped row: the closure carries the effective
// artifact, never the superseded assertion.
func TestReadWorkClosureCarriesTheEffectiveDeliveryArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "work-closure-effective"
	s, targetEventID, version, payloadVersion, targetSeq := completedDeliveryFixture(t, workID)
	defer s.Close()
	request := deliveryCorrectionRequest(workID, version, targetEventID, targetSeq, payloadVersion)
	correctionErr := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowDeliveryCorrectionTx(ctx, tx, request)
		return err
	})
	if correctionErr != nil {
		t.Fatalf("delivery correction refused: %v", correctionErr)
	}
	closure, err := ReadWorkClosure(ctx, s, workID)
	if err != nil {
		t.Fatalf("closure read refused: %v", err)
	}
	if closure.DeliveryArtifact != deliveryCorrectionMergeRef {
		t.Fatalf("closure artifact = %q, want the corrected merge evidence %q", closure.DeliveryArtifact, deliveryCorrectionMergeRef)
	}
}

// A completed item without a proposal, a delivery assertion, or raised
// relations reads with every closure section empty and the pin intact: the
// receipt keeps the bytes the pin alone produced.
func TestReadWorkClosureDegradesToThePinAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "work-closure-bare"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
	defer s.Close()
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatalf("workflow completion refused: %v", err)
	}
	closure, err := ReadWorkClosure(ctx, s, workID)
	if err != nil {
		t.Fatalf("closure read refused: %v", err)
	}
	if closure.Pin.WorkID != workID || closure.Pin.Lifecycle != "completed" {
		t.Fatalf("closure pin = %+v, want the completed pin", closure.Pin)
	}
	if closure.Problem != "" || closure.DeliveryArtifact != "" || closure.OutOfScope != nil || len(closure.FollowUps) != 0 {
		t.Fatalf("bare closure = %+v, want every section empty", closure)
	}
}

// CD-0202: the current implementation definition declares the optional
// out_of_scope prose list on record_proposal, a released pin refuses the
// undeclared field, and a recorded out_of_scope list survives a rebuild.
func TestRecordProposalOutOfScopeAcrossPinnedAndCurrentVersions(t *testing.T) {
	t.Parallel()
	current, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 22)
	if !ok {
		t.Fatal("workflow.implementation v22 is not registered")
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
	pinned, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 21)
	if !ok {
		t.Fatal("workflow.implementation v21 is not registered")
	}
	if err := validateWorkflowActionPayload(pinned.Definition, "record_proposal", payload); err == nil {
		t.Fatal("the released pin admitted the out_of_scope field; pinned instances would change behavior")
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
	_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
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
	closure, err := ReadWorkClosure(context.Background(), s, workID)
	if err != nil {
		t.Fatalf("closure read refused: %v", err)
	}
	if len(closure.OutOfScope) != 2 || closure.OutOfScope[0] != "The launcher." || closure.OutOfScope[1] != "The release automation." {
		t.Fatalf("persisted out_of_scope = %v", closure.OutOfScope)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := ReadWorkClosure(context.Background(), s, workID)
	if err != nil {
		t.Fatalf("rebuilt closure read refused: %v", err)
	}
	if len(rebuilt.OutOfScope) != 2 || rebuilt.OutOfScope[0] != closure.OutOfScope[0] {
		t.Fatalf("out_of_scope changed after replay: %v vs %v", rebuilt.OutOfScope, closure.OutOfScope)
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
