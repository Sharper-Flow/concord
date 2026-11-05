package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func seedItemAtAcceptance(t *testing.T, workID string) (*Store, WorkflowActor) {
	t.Helper()
	var kinds []string
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", kinds)
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.store, workID)
	event := workflowEventWithActor("reviewer-actor-"+workID, WorkflowActorRecorded, workID, reviewerRef, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef, "agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent"})
	if err := applyWorkflowTestOperation(t.Context(), f.store, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return f.store, f.owner
}

// verdictReviewer reads the non-executing reviewer actor from the test's
// isolated store, so verdict actions can be run by an agent that executed nothing.
func verdictReviewer(t *testing.T, s *Store, workID string) WorkflowActor {
	t.Helper()
	var reviewer WorkflowActor
	if err := s.DatabaseForTesting().QueryRow(`SELECT principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE agent_ref=? AND session_ref=?`, "agent/reviewer", "session/"+workID+"-reviewer").Scan(&reviewer.PrincipalRef, &reviewer.ClientRef, &reviewer.AgentRef, &reviewer.SessionRef, &reviewer.ActorClass); err != nil {
		t.Fatalf("reviewer for %s: %v", workID, err)
	}
	return reviewer
}

// actionEvidenceRefs supplies the immutable subject refs that the
// evidence-authority fold requires for evidence-producing actions.
func actionEvidenceRefs(action string, payload json.RawMessage) []string {
	if action != "bind_evidence" {
		return nil
	}
	var fields struct {
		ImmutableSubjectRef string `json:"immutable_subject_ref"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil || fields.ImmutableSubjectRef == "" {
		return nil
	}
	return []string{fields.ImmutableSubjectRef}
}

func runVerdictAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage) error {
	return runVerdictActionAs(t, s, workID, action, payload, 0, WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent})
}

// runVerdictActionAs runs one action under the supplied authenticated actor.
func runVerdictActionAs(t *testing.T, s *Store, workID, action string, payload json.RawMessage, version int64, owner WorkflowActor) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(context.Background(), s, workID, action)
	if err != nil {
		return err
	}
	if version == 0 {
		version = readWorkVersion(t, s, workID)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	identity := fmt.Sprintf("%s-%x-%d", action, sha256.Sum256([]byte(workID)), version)
	if _, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(fmt.Sprintf("%s-%s-%d", action, workID, version)))), IdempotencyIdentity: identity, OperationID: identity,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: identity, RequestID: "request:" + identity, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// seedVerifiedNativeRunCapture records and verifies one native-run observation.
func seedVerifiedNativeRunCapture(t *testing.T, s *Store, workID string) string {
	t.Helper()
	ctx := context.Background()
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	report, err := buildNativeRunEvent("native-run-"+workID, workID, actor, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), version, "start", "run-"+workID, "go test ./internal/store/", "started", "https://evidence.invalid/runs/"+workID, "sha256:"+strings.Repeat("c", 64), "2026-09-14T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{report}}, newWriteScope(tx)); err != nil {
		t.Fatalf("record native-run report: %v", err)
	}
	var observationID string
	if err := tx.QueryRow(`SELECT observation_id FROM workflow_native_runs WHERE work_id=? AND run_id=? AND phase='start'`, workID, "run-"+workID).Scan(&observationID); err != nil {
		t.Fatalf("read the reported run's observation identity: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return appendExternalObservationVerificationTx(ctx, tx, workID, "principal/operator", time.Date(2026, 9, 14, 0, 1, 0, 0, time.UTC), ExternalObservationVerification{
			ObservationID: observationID, VerificationMethod: VerifyTrustedClientReport,
			VerifiedAt: "2026-09-14T00:01:00Z", VerifyingAuthorityRef: "client/concord-1", Result: VerificationMatched,
		})
	}); err != nil {
		t.Fatalf("verify the native-run capture: %v", err)
	}
	return observationID
}
