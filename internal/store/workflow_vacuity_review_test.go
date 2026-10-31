package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// approveWithGroundTruthLabel runs approve_contract at the planning step of a
// fresh instance of a seeded work item pinned to definition. A non-empty label
// rides in the predicate's outcome payload when inPredicate is set, and in the
// action's payload field otherwise.
func approveWithGroundTruthLabel(t *testing.T, s *Store, definition RegisteredDefinition, workID, kind, label string, inPredicate bool) error {
	t.Helper()
	ctx := context.Background()
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: owner, Now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "planning", ownerRef); err != nil {
		t.Fatal(err)
	}
	outcomePayload := map[string]any{"kind": kind, "surface": "git:tree", "subjects": []string{"path:subject"}}
	if kind == "absent" {
		outcomePayload["distinguish_from"] = []string{"archived"}
	}
	fields := map[string]any{
		"contract_version": 1,
		"premise":          "change the subject path",
		"decision_domains": []string{},
		"architecture_binding": WorkflowArchitectureBinding{
			HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		},
	}
	switch {
	case label != "" && inPredicate:
		outcomePayload["ground_truth"] = label
	case label != "":
		fields["payload"] = map[string]any{"ground_truth": label}
	}
	fields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:subject", "ordinal": 0, "outcome_kind": kind, "outcome_payload": outcomePayload}}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "vacuity-label-" + workID
	version := readWorkVersion(t, s, workID)
	tx2, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback()
	_, actionErr := applyWorkflowActionRawTx(ctx, tx2, newWriteScope(tx2), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: encoded, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
		RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC),
	})
	return actionErr
}

func failureKindOf(err error) string {
	if err == nil {
		return ""
	}
	var failure *Failure
	if failureAs(err, &failure) {
		return string(failure.Kind)
	}
	return err.Error()
}

// CD-0236 D1: approval reads no caller-supplied ground-truth label. The
// current definition admits the unlabelled request and refuses both label
// carriers. CD-0242 D14 retains no older definition to bypass that schema.
func TestApprovalDecisionIgnoresGroundTruthLabels(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	cases := []struct{ kind, label string }{{"exists", "subject-present"}, {"absent", "subject-absent"}}
	for _, tc := range cases {
		for _, prefix := range []string{"work-vacuity-live-predicate-", "work-vacuity-live-action-", "work-vacuity-live-unlabelled-"} {
			seedWork(t, s, prefix+tc.kind)
		}
	}
	seedWorkflowLaw(t, s)

	live, err := LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if err := approveWithGroundTruthLabel(t, s, live, "work-vacuity-live-unlabelled-"+tc.kind, tc.kind, "", false); err != nil {
			t.Fatalf("current-pin approve_contract without a label = %v, want it admitted", err)
		}
		for _, inPredicate := range []bool{true, false} {
			prefix := "work-vacuity-live-action-"
			if inPredicate {
				prefix = "work-vacuity-live-predicate-"
			}
			labelled := approveWithGroundTruthLabel(t, s, live, prefix+tc.kind, tc.kind, tc.label, inPredicate)
			if kind := failureKindOf(labelled); kind != string(KindInvalidPayload) {
				t.Errorf("current-pin approve_contract with %s label %q (in predicate: %t) = %v, want the payload schema to refuse it (%s)", tc.kind, tc.label, inPredicate, labelled, KindInvalidPayload)
			}
		}
	}
}
