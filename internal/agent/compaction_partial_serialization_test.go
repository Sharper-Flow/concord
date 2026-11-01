package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

func requireCompactionWire(t *testing.T, response agentcontract.Envelope) agentcontract.Envelope {
	t.Helper()
	raw, err := response.Encode()
	if err != nil {
		t.Fatalf("Encode %s.%s: %v", response.Tool, response.Operation, err)
	}
	if err := payloadschema.ValidateEnvelope(raw); err != nil {
		t.Fatalf("envelope schema: %v", err)
	}
	var stdout bytes.Buffer
	if err := json.NewEncoder(&stdout).Encode(response); err != nil {
		t.Fatalf("JSON stdout encoder: %v", err)
	}
	decoder := json.NewDecoder(&stdout)
	var decoded agentcontract.Envelope
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decode stdout JSON: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout is not exactly one JSON value: %v", err)
	}
	return decoded
}

func TestPendingCompactionSerializesLegalPartial(t *testing.T) {
	for _, tc := range []struct {
		operation string
		step      string
		completed []string
	}{
		{"publish", "git_write", []string{"operation_claimed"}},
		{"publish", "commit_verification", []string{"operation_claimed", "git_write"}},
		{"publish", "sqlite_link", []string{"operation_claimed", "git_write", "commit_verification"}},
		{"publish", "operation_complete", []string{"operation_claimed", "git_write", "commit_verification", "sqlite_link"}},
		{"reconcile", "resolve_home", nil},
		{"reconcile", "git_proof", nil},
		{"reconcile", "resolve_work_version", []string{"operation_claimed", "git_proof"}},
		{"reconcile", "sqlite_link", []string{"operation_claimed", "git_proof"}},
		{"reconcile", "operation_complete", []string{"operation_claimed", "git_proof", "sqlite_link"}},
	} {
		t.Run(tc.operation+"/"+tc.step, func(t *testing.T) {
			claim := store.FenceResult{OpID: "original-compaction", AttemptEpoch: 7, ResultKind: store.ResultPending}
			cause := errors.New("fixture publication cause: exact diagnostic")
			response := pendingCompaction(agentcontract.NewBase("partial-request", "concord_work_compact", tc.operation), "work-fixture", claim, tc.step, tc.completed, cause)
			wire := requireCompactionWire(t, response)
			if wire.Outcome != agentcontract.OutcomePartial || wire.OperationRef == nil || wire.OperationRef.ID != claim.OpID || wire.OperationRef.Version != "7" || wire.OperationRef.State != agentcontract.OperationPartial || wire.OperationRef.CurrentStep != tc.step || wire.FailedStep != tc.step {
				t.Fatalf("lost partial operation diagnostics: %+v", wire)
			}
			completed := tc.completed
			if len(completed) == 0 {
				completed = []string{"operation_claimed"}
			}
			if !reflect.DeepEqual(wire.CompletedSteps, completed) || wire.Error == nil || wire.Error.Message != cause.Error() || wire.Error.EffectState != agentcontract.EffectPartial || wire.Error.RecoveryAction.Kind != "reconcile_operation" {
				t.Fatalf("lost completed steps, exact cause or recovery: %+v", wire)
			}
			var object map[string]any
			raw, err := response.Encode()
			if err != nil || json.Unmarshal(raw, &object) != nil {
				t.Fatalf("decode serialized partial for schema probe: %v", err)
			}
			object["operation_ref"].(map[string]any)["state"] = "pending"
			invalid, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			if err := payloadschema.ValidateEnvelope(invalid); err == nil {
				t.Fatal("schema admitted the original invalid partial/pending combination")
			}
		})
	}
}

func TestCompactionPublishReconcileSerializedRecovery(t *testing.T) {
	s, service, grant, _, home := agentJobsCompactionFixture(t)
	holdCompactionCleanup(t, s)
	reviewed := cancelledPublicationContent(t, newCancelledNotePublication(t, s, grant, home, "reviewed-content"))
	reviewCancelledReceipt(t, s, reviewed.ContentDigest)
	publication := newCancelledNotePublication(t, s, grant, home, "partial-publication")
	content := cancelledPublicationContent(t, publication)
	if _, err := s.DatabaseForTesting().Exec(`CREATE TRIGGER refuse_fixture_compaction BEFORE INSERT ON archived_work
		WHEN NEW.id='work-cancelled' BEGIN SELECT RAISE(ABORT,'fixture locator write blocked'); END`); err != nil {
		t.Fatal(err)
	}
	pending := dispatchMutation(t, s, service, publication.request(), publication.env)
	wire := requireCompactionWire(t, pending)
	wantID := "mutation-" + publication.digest[7:31]
	assertPartial := func(response agentcontract.Envelope, completed []string) {
		t.Helper()
		if response.Outcome != agentcontract.OutcomePartial || response.OperationRef == nil || response.OperationRef.ID != wantID || response.OperationRef.Version != "1" || response.OperationRef.State != agentcontract.OperationPartial || response.OperationRef.CurrentStep != "sqlite_link" || response.FailedStep != "sqlite_link" || !reflect.DeepEqual(response.CompletedSteps, completed) {
			t.Fatalf("partial lost original operation or actual steps: %+v", response)
		}
		if response.Error == nil || response.Error.EffectState != agentcontract.EffectPartial || response.Error.RecoveryAction.Kind != "reconcile_operation" || !strings.Contains(response.Error.Message, "fixture locator write blocked") {
			t.Fatalf("partial lost cause or reconcile recovery: %+v", response.Error)
		}
	}
	assertPartial(wire, []string{"operation_claimed", "git_write", "commit_verification"})
	if wire.Error.Message != pending.Error.Message {
		t.Fatal("serialization changed the exact publication cause")
	}
	note, err := store.FindVerifiedWorkNote(context.Background(), home, content.WorkID, content.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := exec.Command("git", "-C", home.RepoPath, "show", note.CommitOID+":"+note.NotePath).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != content.Content || publicationContentHash(string(blob)) != content.ContentDigest {
		t.Fatalf("committed blob differs from exact publication content: %q", blob)
	}
	if archivedCancelledWorkCount(t, s) != 0 || durableOperationCompleted(t, s, content.WorkID) {
		t.Fatal("failed link recorded a locator or settled the operation")
	}
	beforeReceipts := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts`)
	beforeHead := gitRun(t, home.RepoPath, "rev-parse", "HEAD")
	beforeNotes := gitRun(t, home.RepoPath, "ls-tree", "-r", "HEAD", "--", ".concord/docs/work")
	env := agentJobsMutationEnvelope(t, s, grant, "proj-api")
	discovery, err := json.Marshal(map[string]any{"work_id": content.WorkID, "expected_work_version": content.ExpectedVersion, "idempotency_key": "discover-partial-publication"})
	if err != nil {
		t.Fatal(err)
	}
	failed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: discovery}, env)
	reconciledPartial := requireCompactionWire(t, failed)
	assertPartial(reconciledPartial, []string{"operation_claimed", "git_proof"})
	if reconciledPartial.Error.Message != failed.Error.Message {
		t.Fatal("serialization changed the exact reconciliation cause")
	}
	if _, err := s.DatabaseForTesting().Exec(`DROP TRIGGER refuse_fixture_compaction`); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"operation_id": reconciledPartial.OperationRef.ID, "expected_operation_version": 1, "idempotency_key": "settle-partial-publication"})
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: input}
	settled := requireCompactionWire(t, dispatchMutation(t, s, service, request, env))
	if settled.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("typed recovery did not settle original publication: %+v", settled)
	}
	step, err := store.Step(context.Background(), s, wantID)
	if err != nil || step.ResultKind != store.ResultCompleted || step.AttemptEpoch != 1 {
		t.Fatalf("original operation not completed at its original epoch: %+v, %v", step, err)
	}
	locator, commit, ok := archivedWorkLocator(t, s)
	if !ok || locator != note.NotePath || commit != note.CommitOID {
		t.Fatalf("recovery changed the committed proof: %s %s, want %s %s", locator, commit, note.NotePath, note.CommitOID)
	}
	counts := func() []int {
		t.Helper()
		return []int{
			archivedCancelledWorkCount(t, s),
			countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts WHERE kind='compaction_link.published' AND subject_id='work-cancelled'`),
			countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM durable_operations WHERE work_id='work-cancelled'`),
		}
	}
	if got := counts(); !reflect.DeepEqual(got, []int{1, 1, 1}) {
		t.Fatalf("recovery did not write exactly one publication: %v", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts`); got != beforeReceipts+1 {
		t.Fatalf("recovery wrote more than its publication receipt: %d, want %d", got, beforeReceipts+1)
	}
	for _, replayRequest := range []InvokeRequest{request, publication.request()} {
		replay := requireCompactionWire(t, dispatchMutation(t, s, service, replayRequest, env))
		if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || !bytes.Equal(replay.Result, settled.Result) || !reflect.DeepEqual(replay.ChangedRefs, settled.ChangedRefs) {
			t.Fatalf("replay changed the settled response: %+v", replay)
		}
		if got := counts(); !reflect.DeepEqual(got, []int{1, 1, 1}) {
			t.Fatalf("replay duplicated a publication write: %v", got)
		}
		if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts`); got != beforeReceipts+1 {
			t.Fatalf("replay wrote another effect receipt: %d, want %d", got, beforeReceipts+1)
		}
	}
	if head := gitRun(t, home.RepoPath, "rev-parse", "HEAD"); head != beforeHead {
		t.Fatalf("reconciliation or replay wrote a new commit: %s -> %s", beforeHead, head)
	}
	if notes := gitRun(t, home.RepoPath, "ls-tree", "-r", "HEAD", "--", ".concord/docs/work"); notes != beforeNotes {
		t.Fatal("reconciliation or replay duplicated or changed a note")
	}
	if dirty := gitRun(t, home.RepoPath, "status", "--porcelain"); dirty != "" {
		t.Fatalf("recovery left another write in the knowledge home: %s", dirty)
	}
	// The public knowledge resolve family is retired; the retained proof is the
	// verified committed note itself (compaction/work surface, exact bytes).
	recovered, proofErr := store.FindVerifiedWorkNote(context.Background(), home, content.WorkID, content.ContentDigest)
	if proofErr != nil {
		t.Fatalf("recovered publication does not verify at the committed home: %v", proofErr)
	}
	if recovered.NotePath != note.NotePath || recovered.CommitOID != note.CommitOID || recovered.ContentHash != content.ContentDigest {
		t.Fatalf("recovery changed the exact canonical proof: %+v, want %s %s", recovered, note.NotePath, note.CommitOID)
	}
	if home.HomeProjectID == "" || home.HomeLocatorID == "" {
		t.Fatal("recovery lost the recorded home identity")
	}
}

func TestCompactionReconcileSerializesPartialCause(t *testing.T) {
	s, service, grant, key, home := agentJobsCompactionFixture(t)
	service.publicationObserver = func(phase string) error {
		if phase == "verify_commit" {
			return errors.New("fixture stopped after committed proof")
		}
		return nil
	}
	pending := publishCancelledNote(t, s, service, grant, key, home)
	if pending.Outcome != agentcontract.OutcomePartial || pending.OperationRef == nil {
		t.Fatalf("failed to create pending publication: %+v", pending)
	}
	if _, err := s.DatabaseForTesting().Exec(`CREATE TRIGGER refuse_fixture_reconcile BEFORE INSERT ON archived_work
		WHEN NEW.id='work-cancelled' BEGIN SELECT RAISE(ABORT,'fixture reconcile write blocked'); END`); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"operation_id": pending.OperationRef.ID, "expected_operation_version": 1, "idempotency_key": "reconcile-partial-wire"})
	if err != nil {
		t.Fatal(err)
	}
	env := agentJobsMutationEnvelope(t, s, grant, "proj-api")
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: input}, env)
	wire := requireCompactionWire(t, response)
	if wire.Outcome != agentcontract.OutcomePartial || wire.OperationRef == nil || wire.OperationRef.ID != pending.OperationRef.ID || wire.OperationRef.State != agentcontract.OperationPartial || wire.FailedStep != "sqlite_link" || !reflect.DeepEqual(wire.CompletedSteps, []string{"operation_claimed", "git_proof"}) || wire.Error == nil || wire.Error.Message != response.Error.Message || !strings.Contains(wire.Error.Message, "fixture reconcile write blocked") || wire.Error.RecoveryAction.Kind != "reconcile_operation" {
		t.Fatalf("reconcile serialization lost its original operation or diagnostic: %+v", wire)
	}
}

func TestCompactionReconcileMissingVersionRetainsProofStep(t *testing.T) {
	s, service, grant, key, home := agentJobsCompactionFixture(t)
	service.publicationObserver = func(phase string) error {
		if phase == "verify_commit" {
			return errors.New("fixture stopped after committed proof")
		}
		return nil
	}
	pending := publishCancelledNote(t, s, service, grant, key, home)
	if pending.Outcome != agentcontract.OutcomePartial || pending.OperationRef == nil {
		t.Fatalf("failed to create pending publication: %+v", pending)
	}
	step, err := store.Step(t.Context(), s, pending.OperationRef.ID)
	if err != nil {
		t.Fatal(err)
	}
	var scope map[string]any
	if err := json.Unmarshal([]byte(step.AcceptedScopeSnapshot), &scope); err != nil {
		t.Fatal(err)
	}
	delete(scope, "work_version")
	raw, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	step.AcceptedScopeSnapshot = string(raw)
	m := compactionMutation{runtime: runtime{Store: s, Authority: service}, base: agentcontract.NewBase("missing-version", "concord_work_compact", "reconcile"), grant: grant}
	response, err := m.reconcilePending(t.Context(), compactReconcileInput{OperationID: step.OpID, WorkID: "work-cancelled"}, step)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response.CompletedSteps, []string{"operation_claimed", "git_proof"}) {
		t.Fatalf("verified proof lost from completed steps: %v", response.CompletedSteps)
	}
	wire := requireCompactionWire(t, response)
	if wire.FailedStep != "resolve_work_version" || wire.Error == nil || wire.Error.Message != "durable publication did not retain terminal work version" {
		t.Fatalf("lost missing-version diagnostic: %+v", wire)
	}
}
