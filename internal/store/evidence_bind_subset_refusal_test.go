package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// runBindAction submits one workflow action with an explicit top-level
// evidence-ref list and its parallel per-entry evidence kinds, mirroring the
// agent layer's evidenceLocators and evidenceKinds threading.
func runBindAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, version int64, evidenceRefs []string, evidenceKinds ...string) error {
	t.Helper()
	if version == 0 {
		version = verdictItemVersion(t, s, workID)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		return err
	}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	if _, err := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: evidenceRefs, EvidenceKinds: evidenceKinds, WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64) + fmt.Sprint(version), IdempotencyIdentity: action + "-" + workID + "-" + fmt.Sprint(version), OperationID: action + "-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: action + "-" + workID + "-" + fmt.Sprint(version), RequestID: "request:" + action + "-" + workID + "-" + fmt.Sprint(version), ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	}); err != nil {
		_ = leaveFold(context.Background(), tx)
		return err
	}
	_ = leaveFold(context.Background(), tx)
	return tx.Commit()
}

// One bind_evidence call that submits several evidence entries returns ok and
// durably binds only the first entry's locator (workflowEvidenceBindingEvents
// reads request.EvidenceRefs[0] alone). The unbound entries are silently
// dropped: the caller believes the evidence set is durable and completion
// later refuses refs it cannot find. A bind that cannot honor every submitted
// subject must refuse at the point of the mistake instead.
func TestBindEvidenceRefusesToBindOnlyASubsetOfSubmittedSubjects(t *testing.T) {
	t.Parallel()
	const workID = "evidence-bind-subset"
	s := seedMandateRecoveryItem(t, workID)
	const (
		firstRef  = "ci:run/111"
		secondRef = "ci:run/222"
		thirdRef  = "ci:run/333"
	)

	err := runBindAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification"}`), 0, []string{firstRef, secondRef, thirdRef})
	if err == nil {
		t.Fatal("bind_evidence admitted a call submitting several subjects while it can bind only one")
	}
	got := err.Error()
	if !strings.Contains(got, "bind_evidence") || !strings.Contains(got, "one") {
		t.Fatalf("refusal does not name the one-subject-per-call rule: %s", got)
	}

	// Whichever subject a follow-up verdict names must be either fully
	// bound or refused at bind time; nothing may be half-submitted.
	for _, ref := range []string{secondRef, thirdRef} {
		var bound int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, ref).Scan(&bound); err != nil {
			t.Fatal(err)
		}
		if bound != 0 {
			t.Fatalf("refusal still durably bound %s", ref)
		}
	}
}

// The reference rule the declared payload fields already enforce also bounds
// a subject derived from the evidence array. Without this an array entry
// outside ValidReference mints durable evidence that no verdict or completion
// reference can ever name.
func TestBindEvidenceRefusesArraySubjectOutsideTheReferenceRule(t *testing.T) {
	t.Parallel()
	const workID = "evidence-bind-array-domain"
	s := seedMandateRecoveryItem(t, workID)
	const invalidLocator = "go test ./internal/store/ -run TestThing"

	err := runBindAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification"}`), 0, []string{invalidLocator})
	if err == nil {
		t.Fatalf("bind_evidence admitted an array-derived subject the verdict cannot name: %q", invalidLocator)
	}
	if !strings.Contains(err.Error(), "reference rule") {
		t.Fatalf("array-subject refusal does not name the reference rule: %v", err)
	}
}

// One locator submitted under two kinds binds two durably bound events, one
// per kind, because the per-entry kinds travel with the submission instead of
// collapsing into the single payload evidence_kind.
func TestBindEvidenceBindsOneEventPerSubmittedKind(t *testing.T) {
	t.Parallel()
	const workID = "evidence-bind-per-kind"
	s := seedMandateRecoveryItem(t, workID)
	const subject = "ci:run/777"
	before := verdictItemVersion(t, s, workID)

	if err := runBindAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification"}`), 0, []string{subject}, "verification", "review"); err != nil {
		t.Fatalf("two-kind bind of one subject refused: %v", err)
	}
	if after := verdictItemVersion(t, s, workID); after <= before {
		t.Fatalf("two-kind bind did not advance the work version: %d to %d", before, after)
	}
	// accept_worker_result also mints a bound event for the attempt id, so
	// the count scopes to the subject this bind submitted.
	rows, err := s.DatabaseForTesting().Query(`SELECT json_extract(payload,'$.evidence_kind'), json_extract(payload,'$.immutable_subject_ref') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=? ORDER BY seq`, workID, WorkflowEvidenceBound, subject)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type binding struct {
		kind, subject string
	}
	var bound []binding
	for rows.Next() {
		var item binding
		if err := rows.Scan(&item.kind, &item.subject); err != nil {
			t.Fatal(err)
		}
		bound = append(bound, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(bound) != 2 {
		t.Fatalf("one locator under two kinds bound %d events (%v), want 2", len(bound), bound)
	}
	for i, want := range []string{"verification", "review"} {
		if bound[i].kind != want || bound[i].subject != subject {
			t.Fatalf("bound event %d = (%s, %s), want (%s, %s)", i, bound[i].kind, bound[i].subject, want, subject)
		}
	}
}
