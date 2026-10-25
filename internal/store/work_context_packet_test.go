package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// CON-887 packet admission. The work pin carries the core reader's exact
// current work-context view, and the dispatch_worker guard refuses a packet
// that does not consume that view byte-for-byte, mirroring the correction
// admission one guard above it.

// workContextDispatchPacket builds the closed implement-lane packet the
// repair step of the break-fix fixture dispatches. The pinned definition
// dispatches only a selected ready worker-job revision, so the packet binds
// the recorded revision, and the work context is the reader's current view,
// which is the exact object the work pin carries.
// The caller records job with seedReadyWorkerJob before it reads view: the
// recording advances the work version the binding carries, and a ready
// oracle job adds its preparations to the current work context view.
func workContextDispatchPacket(t *testing.T, f workContextFixture, attemptID string, job WorkerJobBinding, view any) map[string]any {
	t.Helper()
	laneDigest := implementLaneIdentity()
	task, binding := recordedPacketInputs(t, f.store, f.workID, "implement")
	inputs := map[string]any{
		"task":        task,
		"binding":     binding,
		"worker_job":  recordedPacketJobForTest(t, f.store, f.workID, job, "implement"),
		"constraints": []string{"do-not-modify-product-truth"},
	}
	for member, value := range recordedPacketRecords(t, f.store, f.workID) {
		inputs[member] = value
	}
	delete(inputs, "work_context")
	if view != nil {
		inputs["work_context"] = view
	}
	return map[string]any{
		"schema_version": WorkerPacketSchemaVersion,
		"attempt_id":     attemptID,
		"lane_id":        "implement",
		"lane_digest":    laneDigest,
		"work_id":        f.workID,
		"step_id":        "repair",
		"inputs":         inputs,
	}
}

// seedReadyWorkerJob records one ready worker-job revision through the
// authoring action and returns its recorded binding.
func seedReadyWorkerJob(t *testing.T, f workContextFixture) WorkerJobBinding {
	t.Helper()
	job := WorkerJobBinding{JobID: "ctx-packet-job-" + f.workID, Revision: 1}
	recordWorkerJobRevisionForTest(t, f.store, f.workID, f.owner, &job)
	return job
}

// TestNativeOracleDispatchRecordsAdmittedSubjectOnce proves the live
// dispatch_worker path records the admitted core subject exactly once: the
// completion carries the subject the admitted work-context view pinned and
// the digest of the exact packet that carried it, the authorized dispatch
// window exposes both values unchanged, and a tampered candidate view
// refuses typed, with no completion and no start.
func TestNativeOracleDispatchRecordsAdmittedSubjectOnce(t *testing.T) {
	fixture := seedWorkContextFixture(t, "ctx-dispatch-subject-once")
	defer fixture.store.Close()
	s := fixture.store
	bootstrapOracleFixtureSubject(t, s, fixture.workID)
	job := seedReadyWorkerJob(t, fixture)
	view, err := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
	if err != nil || view == nil || view.SubjectCommit == "" {
		t.Fatalf("admitted work context = %+v, error = %v, want a qualified subject", view, err)
	}
	tampered := *view
	tampered.SubjectCommit = strings.Repeat("b", 40)
	tamperedPacket := workContextDispatchPacket(t, fixture, "attempt-ctx-subject-tampered", job, &tampered)
	if err := dispatchWorkContextAttempt(t, fixture, tamperedPacket); err == nil || !strings.Contains(err.Error(), "does not consume the current work context") {
		t.Fatalf("tampered candidate view error = %v, want the typed consumption refusal", err)
	}
	if got := dispatchStartedCount(t, s, fixture.workID); got != 0 {
		t.Fatalf("tampered initial packet started %d dispatches", got)
	}
	attemptID := "attempt-ctx-subject-once"
	packet := workContextDispatchPacket(t, fixture, attemptID, job, view)
	if err := dispatchWorkContextAttempt(t, fixture, packet); err != nil {
		t.Fatalf("live dispatch refused: %v", err)
	}
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalJSON(packetBytes)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := nativeDigest(canonical)
	completionsWithSubject := func() int {
		t.Helper()
		var count int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_subject_commit') IS NOT NULL`, fixture.workID, WorkflowActionCompleted).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := completionsWithSubject(); got != 1 {
		t.Fatalf("dispatch completions recording an admitted subject = %d, want exactly one", got)
	}
	var recordedSubject, recordedDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_subject_commit'), json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_subject_commit') IS NOT NULL`, fixture.workID, WorkflowActionCompleted).Scan(&recordedSubject, &recordedDigest); err != nil {
		t.Fatal(err)
	}
	if recordedSubject != view.SubjectCommit || recordedDigest != wantDigest {
		t.Fatalf("recorded subject=%s digest=%s, want the admitted subject=%s with the packet digest=%s", recordedSubject, recordedDigest, view.SubjectCommit, wantDigest)
	}
	var window WorkerDispatchWindow
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		var windowErr error
		window, windowErr = FindAuthorizedDispatchWindowTx(context.Background(), tx.tx, fixture.workID, attemptID)
		return windowErr
	}); err != nil {
		t.Fatalf("authorized dispatch window read refused: %v", err)
	}
	if window.SubjectCommit != view.SubjectCommit || window.PacketDigest != wantDigest {
		t.Fatalf("dispatch window subject=%s digest=%s, want subject=%s digest=%s", window.SubjectCommit, window.PacketDigest, view.SubjectCommit, wantDigest)
	}
	if err := dispatchWorkContextAttempt(t, fixture, tamperedPacket); failureKind(err) != KindOperationConflict || !strings.Contains(err.Error(), "unresolved launch outcome") {
		t.Fatalf("another authorization before launch reconciliation = %v, want operation_conflict", err)
	}
	if got := completionsWithSubject(); got != 1 {
		t.Fatalf("dispatch completions after the tampered attempt = %d, want still the one admitted subject", got)
	}
	if got := dispatchStartedCount(t, s, fixture.workID); got != 1 {
		t.Fatalf("dispatch_worker starts = %d, want only the admitted attempt's start", got)
	}
}

// dispatchWorkContextAttempt dispatches one implement-lane attempt on the
// fixture's repair step and returns the dispatch error.
func dispatchWorkContextAttempt(t *testing.T, f workContextFixture, packet map[string]any) error {
	t.Helper()
	return dispatchBindingAttempt(t, f.store, cd0059DispatchSeed{workID: f.workID, ownerActor: f.owner}, packet["attempt_id"].(string), packet)
}

// declareSampleWorkContext records one declaration with readings and
// findings the packet admission can consume. The readings resolve against
// the fixture's real synthetic sources at dispatch.
func declareSampleWorkContext(t *testing.T, f workContextFixture, statement string) {
	t.Helper()
	if err := f.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{
			f.repoReading(workContextTestRoot, workContextRootRationale, ""),
			f.knowledgeReading(),
		},
		"finding_refs": []string{},
		"context_findings": []map[string]any{
			workContextFinding(workContextTestChild, "rejected_approach", statement, ""),
		},
	}); err != nil {
		t.Fatalf("record_work_context refused: %v", err)
	}
}

// The work pin carries the tx-scoped reader's exact current view: the same
// bytes the packet admission compares against, so a packet built from the pin
// consumes the current context.
func TestWorkPinCarriesCurrentWorkContextView(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-pin-view")
	defer fixture.store.Close()
	declareSampleWorkContext(t, fixture, "the unbounded log scan was rejected")
	view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil {
		t.Fatal("current work context view is absent after the declaration")
	}
	pin, err := ReadWorkPin(context.Background(), fixture.store, fixture.workID)
	if err != nil {
		t.Fatalf("work pin read refused: %v", err)
	}
	marshaled, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	viewJSON, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(marshaled, viewJSON) {
		t.Fatalf("work pin omits the current work context view:\n%s\nwant the exact view bytes:\n%s", marshaled, viewJSON)
	}
	if validErr := payloadschema.Validate("work_pin", marshaled); validErr != nil {
		t.Fatalf("work pin with work context fails the closed payload schema: %v", validErr)
	}
}

// A work item with no declaration and no terminal-report findings pins no
// work context: the typed absent view never materializes a packet member.
func TestWorkPinOmitsWorkContextWithoutCurrentView(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-pin-absent")
	defer fixture.store.Close()
	pin, err := ReadWorkPin(context.Background(), fixture.store, fixture.workID)
	if err != nil {
		t.Fatalf("work pin read refused: %v", err)
	}
	marshaled, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(marshaled, []byte(`"work_context"`)) {
		t.Fatalf("work pin carries work context without a current view:\n%s", marshaled)
	}
	if validErr := payloadschema.Validate("work_pin", marshaled); validErr != nil {
		t.Fatalf("work pin without work context fails the closed payload schema: %v", validErr)
	}
}

// A dispatch whose packet carries the reader's exact current view admits.
func TestDispatchAdmitsPacketConsumingCurrentWorkContext(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-dispatch-admit")
	defer fixture.store.Close()
	declareSampleWorkContext(t, fixture, "the unbounded log scan was rejected")
	job := seedReadyWorkerJob(t, fixture)
	view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-ctx-admit", job, view)); err != nil {
		t.Fatalf("dispatch refused a packet consuming the current work context: %v", err)
	}
	if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 1 {
		t.Fatalf("dispatch_worker started events = %d, want 1", got)
	}
}

// A packet goes stale exactly when a context source changes after it was
// built. A terminal report with findings changes the current view without
// advancing the work version, so the binding stays truthful and the refusal
// belongs to the work-context admission alone.
func TestDispatchRefusesStaleWorkContextPacket(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-dispatch-stale")
	defer fixture.store.Close()
	declareSampleWorkContext(t, fixture, "the first declaration stands")
	ownerRef, actorErr := WorkflowActorRef(fixture.owner)
	if actorErr != nil {
		t.Fatal(actorErr)
	}
	job := seedReadyWorkerJob(t, fixture)
	recordWorkerFixtureDispatchAuthorization(t, fixture.store, fixture.workID, "attempt-ctx-stale-report", "repair", ownerRef)
	view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	packet := workContextDispatchPacket(t, fixture, "attempt-ctx-stale", job, view)
	lane := BuiltinLaneDefinitions()[0]
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerDispatchEvent(fixture.workID, "attempt-ctx-stale-report", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	reported := []WorkerContextFinding{{Kind: "observation", Statement: "the terminal report adds a finding the packet never saw", SubjectRef: "internal/store/work_context.go", EvidenceRefs: []string{}, DomainID: workContextTestChild}}
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerCompletedContextFindingsEvent(fixture.workID, "ctx-complete-stale", "attempt-ctx-stale-report", preferredModelForLane(lane), lane, reported, CurrentEventPayloadVersion(WorkerCompleted), workerFixtureDirectory(t, fixture.store, fixture.workID))}}); err != nil {
		t.Fatalf("terminal report refused: %v", err)
	}
	startsBefore := dispatchStartedCount(t, fixture.store, fixture.workID)
	if startsBefore != 1 {
		t.Fatalf("dispatch_worker starts before refusal = %d, want 1 from setup", startsBefore)
	}
	err = dispatchWorkContextAttempt(t, fixture, packet)
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker packet does not consume the current work context") {
		t.Fatalf("stale packet error = %v, want %s refusing the stale work context", err, KindInvalidPayload)
	}
	if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != startsBefore {
		t.Fatalf("dispatch_worker starts after refusal = %d, want unchanged pre-refusal count %d", got, startsBefore)
	}
}

// A tampered finding statement refuses with the same admission: the packet
// must carry the reader's bytes, not a close copy.
func TestDispatchRefusesTamperedWorkContextPacket(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-dispatch-tampered")
	defer fixture.store.Close()
	declareSampleWorkContext(t, fixture, "the unbounded log scan was rejected")
	job := seedReadyWorkerJob(t, fixture)
	view, err := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
	if err != nil {
		t.Fatal(err)
	}
	tampered := *view
	tampered.Findings = append([]WorkContextFindingView(nil), view.Findings...)
	tampered.Findings[0].Statement = "the tampered statement the core never recorded"
	err = dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-ctx-tampered", job, &tampered))
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker packet does not consume the current work context") {
		t.Fatalf("tampered packet error = %v, want %s refusing the tampered work context", err, KindInvalidPayload)
	}
}

// A packet may not carry a work context the work item does not hold. On the
// oracle-capable break-fix pin the ready job that dispatch requires derives
// its native preparations into the current view, so dispatch always meets a
// current view; the refusal is asserted on the admission before any job
// exists, where the work item holds no view.
func TestDispatchRefusesWorkContextWithoutCurrentView(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-dispatch-without-view")
	defer fixture.store.Close()
	db := fixture.store.DatabaseForTesting()
	if view, err := readWorkContextView(context.Background(), db, fixture.workID); err != nil || view != nil {
		t.Fatalf("fixture holds a current view before any declaration or job: %+v %v", view, err)
	}
	packet, err := json.Marshal(map[string]any{"inputs": map[string]any{"work_context": map[string]any{
		"source_event_frontier": 1,
		"required_reading":      []any{},
		"findings":              []any{},
		"domain_groups":         []any{},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = admittedWorkerPacketWorkContext(context.Background(), db, fixture.workID, packet)
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker packet carries work context without a current work context") {
		t.Fatalf("fabricated context error = %v, want %s refusing the work context without a current view", err, KindInvalidPayload)
	}
}

// A current view the packet omits refuses: the packet must consume the view.
func TestDispatchRefusesPacketMissingWorkContext(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-dispatch-missing")
	defer fixture.store.Close()
	declareSampleWorkContext(t, fixture, "the unbounded log scan was rejected")
	err := dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-ctx-missing", seedReadyWorkerJob(t, fixture), nil))
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker packet does not consume the current work context") {
		t.Fatalf("missing context error = %v, want %s refusing the packet without the current work context", err, KindInvalidPayload)
	}
}

// A current view past its bounds refuses the dispatch with the reader's own
// strict overflow error and zero dispatch effects, so the overflow is never
// silently dropped or truncated into a packet.
func TestDispatchRefusesWorkContextViewOverflow(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-dispatch-overflow")
	defer fixture.store.Close()
	seedWorkContextOverflow(t, fixture)
	err := dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-ctx-overflow", seedReadyWorkerJob(t, fixture), nil))
	if !hasFailureKind(err, KindLimitExceeded) {
		t.Fatalf("overflow dispatch error = %v, want %s from the work context reader", err, KindLimitExceeded)
	}
	if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 0 {
		t.Fatalf("dispatch_worker starts = %d, want zero behind the overflow refusal", got)
	}
}

// seedWorkContextOverflow drives the current view past the 32-finding count
// bound with an already-admitted historical declaration whose 48 findings
// are optional context a later declaration can drop or replace.
func seedWorkContextOverflow(t *testing.T, fixture workContextFixture) {
	t.Helper()
	declareSixteen := func(statement string) {
		t.Helper()
		if err := fixture.recordWorkContext(t, map[string]any{
			"context_findings": func() []map[string]any {
				findings := make([]map[string]any, 16)
				for i := range findings {
					findings[i] = workContextFinding(workContextTestChild, "observation", statement, "")
				}
				return findings
			}(),
		}); err != nil {
			t.Fatalf("declaration %s refused: %v", statement, err)
		}
	}
	declareSixteen("first")
	var firstSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, fixture.workID, WorkflowWorkContextRecorded).Scan(&firstSeq); err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 16)
	for i := range refs {
		refs[i] = fmt.Sprintf("finding:%d:%d", firstSeq, i)
	}
	// The anchor selects all sixteen earlier findings beside its own
	// sixteen: the legal live view holds exactly 32.
	if err := fixture.recordWorkContext(t, map[string]any{
		"finding_refs": refs,
		"context_findings": func() []map[string]any {
			findings := make([]map[string]any, 16)
			for i := range findings {
				findings[i] = workContextFinding(workContextTestChild, "observation", "second", "")
			}
			return findings
		}(),
	}); err != nil {
		t.Fatalf("second declaration refused: %v", err)
	}
	var secondSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, fixture.workID, WorkflowWorkContextRecorded).Scan(&secondSeq); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		refs = append(refs, fmt.Sprintf("finding:%d:%d", secondSeq, i))
	}
	third := make([]map[string]any, 16)
	for i := range third {
		third[i] = workContextFinding(workContextTestChild, "observation", "third", "")
	}
	// The final over-bound declaration rides the historical route: the fold
	// admits it on the pinned workflow identity, never as a live action.
	recordHistoricalWorkContext(t, fixture, map[string]any{"finding_refs": refs, "context_findings": third})
}

// TestDispatchSubjectFreshnessCoreOwned proves the core dispatch admission
// owns subject freshness: when the admitted work-context view pins a
// qualified subject, the claimed worktree's live clean HEAD must equal it
// before any attempt, window, or start exists. A moved HEAD, a dirty or
// untracked tree, and an unreachable git subject all refuse with zero
// dispatch effects, and a subject-less non-oracle dispatch never probes.
// freshnessDispatch prepares the one sealed dispatch proof against the real
// claimed worktree and dispatches the packet through the action boundary
// with it, exactly as the authenticated mutation boundary does.
func freshnessDispatch(t *testing.T, f workContextFixture, attemptID string, packet map[string]any) error {
	t.Helper()
	preparation, err := PrepareWorkerDispatchFreshness(context.Background(), f.store, PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: dispatchSessionWorktree(t, f.store, f.workID)})
	if err != nil {
		return err
	}
	ctx := WithWorkerDispatchPreparation(context.Background(), preparation)
	packetBytes, marshalErr := json.Marshal(packet)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	payload, marshalErr := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	_, err = invokeWorkflowActionForCD0059(ctx, t, f.store, WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: readWorkVersion(t, f.store, f.workID), ActionID: "dispatch_worker",
		Payload: payload, SessionWorktree: dispatchSessionWorktree(t, f.store, f.workID),
		Actor: f.owner, AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-inputs"),
		IdempotencyIdentity: attemptID + "-op", OperationID: "op-" + attemptID, PrincipalRef: f.owner.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: attemptID + "-key", RequestID: "req-" + attemptID,
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	return err
}

func TestDispatchSubjectFreshnessCoreOwned(t *testing.T) {
	t.Run("a moved HEAD refuses the dispatch", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-freshness-moved")
		defer fixture.store.Close()
		s := fixture.store
		claimDir, head := reviewSubjectClaim(t, s, fixture.workID)
		job := seedReadyWorkerJob(t, fixture)
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil || view.SubjectCommit != head {
			t.Fatalf("admitted view subject = %q err = %v, want the qualified HEAD %q", viewSubject(view), viewErr, head)
		}
		run := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", claimDir}, args...)...) //nolint:gosec // test-only git argv, fixed binary
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("move claimed HEAD %v: %v: %s", args, err, stderr.String())
			}
		}
		if err := os.WriteFile(claimDir+"/moved.txt", []byte("the candidate moved on\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "--allow-empty", "-m", "the candidate moved past the qualified subject")
		err := freshnessDispatch(t, fixture, "attempt-freshness-moved", workContextDispatchPacket(t, fixture, "attempt-freshness-moved", job, view))
		var failure *Failure
		if !hasFailureKind(err, KindProjectionConflict) {
			t.Fatalf("moved-HEAD dispatch error = %v, want %s refusing the stale subject", err, KindProjectionConflict)
		}
		_ = failure
		if got := dispatchStartedCount(t, s, fixture.workID); got != 0 {
			t.Fatalf("dispatch_worker starts = %d, want 0 on the freshness refusal", got)
		}
	})

	t.Run("a dirty worktree refuses the dispatch", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-freshness-dirty")
		defer fixture.store.Close()
		s := fixture.store
		claimDir, head := reviewSubjectClaim(t, s, fixture.workID)
		job := seedReadyWorkerJob(t, fixture)
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil || view.SubjectCommit != head {
			t.Fatalf("admitted view subject = %q err = %v, want the qualified HEAD %q", viewSubject(view), viewErr, head)
		}
		if err := os.WriteFile(claimDir+"/dirty.txt", []byte("untracked bytes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := freshnessDispatch(t, fixture, "attempt-freshness-dirty", workContextDispatchPacket(t, fixture, "attempt-freshness-dirty", job, view))
		if !hasFailureKind(err, KindProjectionConflict) {
			t.Fatalf("dirty dispatch error = %v, want %s refusing the dirty tree", err, KindProjectionConflict)
		}
		if got := dispatchStartedCount(t, s, fixture.workID); got != 0 {
			t.Fatalf("dispatch_worker starts = %d, want 0 on the freshness refusal", got)
		}
	})

	t.Run("an unreachable git subject refuses the dispatch", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-freshness-unreachable")
		defer fixture.store.Close()
		s := fixture.store
		claimDir, head := reviewSubjectClaim(t, s, fixture.workID)
		if err := os.RemoveAll(claimDir + "/.git"); err != nil {
			t.Fatal(err)
		}
		job := seedReadyWorkerJob(t, fixture)
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil || view.SubjectCommit != head {
			t.Fatalf("admitted view subject = %q err = %v, want the qualified HEAD %q", viewSubject(view), viewErr, head)
		}
		err := freshnessDispatch(t, fixture, "attempt-freshness-unreachable", workContextDispatchPacket(t, fixture, "attempt-freshness-unreachable", job, view))
		if !hasFailureKind(err, KindGitUnreachable) {
			t.Fatalf("unreachable dispatch error = %v, want %s refusing the unobservable worktree", err, KindGitUnreachable)
		}
		if got := dispatchStartedCount(t, s, fixture.workID); got != 0 {
			t.Fatalf("dispatch_worker starts = %d, want 0 on the freshness refusal", got)
		}
	})
}

// A subject-less non-oracle dispatch never probes: absence of a qualified
// subject grants no freshness comparison and no native execution. The
// historical implementation pin dispatches with no work context at all.
func TestDispatchSubjectFreshnessSubjectLessProbesNothing(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	workID := "work-freshness-subjectless"
	seedGitClaimRepo(t, s, workID)
	seed := seedDispatchFixture(t, s, workID)
	attemptID := "attempt-freshness-subjectless"
	packet := dispatchWorkerPacket(t, s, workID, "execution", attemptID)
	if view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID); viewErr != nil || view != nil {
		t.Fatalf("fixture view = %+v err = %v, want no work context at all", view, viewErr)
	}
	seen := 0
	preparation, err := PrepareWorkerDispatchFreshness(context.Background(), s, PrepareWorkerDispatchRequest{
		WorkID: workID, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Runner: probeCountingRunner{count: &seen},
	})
	if err != nil {
		t.Fatalf("subject-less preparation refused: %v", err)
	}
	if seen != 0 {
		t.Fatalf("subject-less preparation ran %d git probe call(s), want 0", seen)
	}
	if err := dispatchBindingAttemptCtx(t, s, seed, attemptID, packet, WithWorkerDispatchPreparation(context.Background(), preparation)); err != nil {
		t.Fatalf("subject-less dispatch refused: %v", err)
	}
	if got := dispatchStartedCount(t, s, workID); got != 1 {
		t.Fatalf("dispatch_worker starts = %d, want 1", got)
	}
}

// probeCountingRunner counts the git calls one preparation makes.
type probeCountingRunner struct{ count *int }

func (r probeCountingRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	*r.count++
	return nil, nil
}

// dispatchBindingAttemptCtx dispatches one attempt through the action
// boundary with a caller-supplied context, so a sealed preparation rides it.
func dispatchBindingAttemptCtx(t *testing.T, s *Store, seed cd0059DispatchSeed, attemptID string, packet map[string]any, ctx context.Context) error {
	t.Helper()
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload, SessionWorktree: dispatchSessionWorktree(t, s, seed.workID),
		Actor: seed.ownerActor, AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-inputs"),
		IdempotencyIdentity: attemptID + "-op", OperationID: "op-" + attemptID, PrincipalRef: seed.ownerActor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: attemptID + "-key", RequestID: "req-" + attemptID,
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	return err
}

// A combined rejection that carries a retry prepares one proof before the
// outer transaction, and the derived retry inside the transaction reuses
// that same proof: an unchanged HEAD commits the rejection and its retry
// dispatch together, and a moved HEAD refuses the whole operation with the
// rejection uncommitted. Each subtest seeds its own work, because an
// admitted rejection advances the workflow step.
func TestDispatchSubjectFreshnessCombinedRejectRetrySharesProof(t *testing.T) {
	setup := func(t *testing.T, workID string) (*Store, string, string, func(t *testing.T, attemptID string) error) {
		t.Helper()
		fixture := seedWorkContextFixture(t, workID)
		s := fixture.store
		claimDir, head := reviewSubjectClaim(t, s, fixture.workID)
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil || view.SubjectCommit != head {
			t.Fatalf("admitted view subject = %q err = %v, want the qualified HEAD %q", viewSubject(view), viewErr, head)
		}
		lane := laneForID(t)
		job := reviewSubjectReadyRevision(t, fixture)
		view, viewErr = readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil || view.SubjectCommit != head {
			t.Fatalf("admitted view subject = %q err = %v, want the qualified HEAD %q", viewSubject(view), viewErr, head)
		}
		if err := dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-reject-retry-x", job, view)); err != nil {
			t.Fatalf("implement dispatch refused: %v", err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(fixture.workID, "attempt-reject-retry-x", lane, job)}}); err != nil {
			t.Fatal(err)
		}
		completion := Event{EventID: "complete-" + workID + "-x", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: fixture.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
			AttemptID: "attempt-reject-retry-x", ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
			EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: &job,
		})}
		prepared, prepErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: fixture.workID, AttemptID: "attempt-reject-retry-x", EventID: completion.EventID})
		if prepErr != nil {
			t.Fatalf("terminal delivery preparation refused: %v", prepErr)
		}
		workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, completion)
		rejectWithRetry := func(t *testing.T, attemptID string) error {
			t.Helper()
			reviewSubjectRecordOperator(t, s, fixture.workID)
			operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + fixture.workID + "-operator", ActorClass: ActorOperator}
			version := readWorkVersion(t, s, fixture.workID)
			payload, marshalErr := json.Marshal(map[string]any{
				"attempt_id": attemptID, "attempt_epoch": int64(1),
				"diagnosis": "the delivered change missed the recorded check", "strategy": "reproduce the check and repair the change",
				"predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"evidence:" + attemptID + "-reject"},
				"retry": map[string]any{"lane_id": "implement"},
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			preparation, prepErr := PrepareWorkerDispatchFreshness(context.Background(), s, PrepareWorkerDispatchRequest{WorkID: fixture.workID, SessionWorktree: claimDir})
			if prepErr != nil {
				return prepErr
			}
			ctx := WithWorkerDispatchPreparation(context.Background(), preparation)
			_, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
				WorkID: fixture.workID, ExpectedVersion: version, ActionID: "reject_worker_result", Payload: payload,
				SessionWorktree: claimDir, Actor: operator, AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-reject-inputs"),
				IdempotencyIdentity: attemptID + "-reject-op", OperationID: "op-" + attemptID + "-reject", PrincipalRef: operator.PrincipalRef,
				Tool: "concord_work_transition", IdempotencyKey: attemptID + "-reject-key", RequestID: "req-" + attemptID + "-reject",
				AcceptedScope: `{}`, ContractDigest: testManifestDigest,
			})
			return err
		}
		return s, fixture.workID, claimDir, rejectWithRetry
	}

	t.Run("an unchanged HEAD commits the rejection and its retry dispatch together", func(t *testing.T) {
		s, workID, _, rejectWithRetry := setup(t, "work-freshness-reject-retry-ok")
		if err := rejectWithRetry(t, "attempt-reject-retry-x"); err != nil {
			t.Fatalf("combined reject/retry refused an unchanged HEAD: %v", err)
		}
		var retryAttempts int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, "op-attempt-reject-retry-x-reject:retry").Scan(&retryAttempts); err != nil {
			t.Fatal(err)
		}
		if retryAttempts != 1 {
			t.Fatalf("retry attempts = %d, want the one derived retry dispatch the same proof authorized", retryAttempts)
		}
	})

	t.Run("a moved HEAD refuses the whole combined operation", func(t *testing.T) {
		s, workID, claimDir, rejectWithRetry := setup(t, "work-freshness-reject-retry-refuse")
		run := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", claimDir}, args...)...) //nolint:gosec // test-only git argv, fixed binary
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("move claimed HEAD %v: %v: %s", args, err, stderr.String())
			}
		}
		if err := os.WriteFile(claimDir+"/moved.txt", []byte("the candidate moved on\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "--allow-empty", "-m", "the candidate moved past the qualified subject")
		err := rejectWithRetry(t, "attempt-reject-retry-x")
		if err == nil || !hasFailureKind(err, KindProjectionConflict) {
			t.Fatalf("combined reject/retry error = %v, want %s refusing the whole operation", err, KindProjectionConflict)
		}
		var rejections, retryAttempts int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result'`, workID, WorkflowActionCompleted).Scan(&rejections); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id LIKE '%:retry'`, workID).Scan(&retryAttempts); err != nil {
			t.Fatal(err)
		}
		if rejections != 0 || retryAttempts != 0 {
			t.Fatalf("the refused combined operation committed %d rejection(s) and %d retry attempt(s), want zero effects", rejections, retryAttempts)
		}
	})
}

// probeHoldingRunner holds the delivery probe while the test observes store
// behavior, then answers the qualified subject with a clean tree, or lets
// the probe deadline expire when holdForDeadline is set.
type probeHoldingRunner struct {
	s               *Store
	release         chan struct{}
	readCompleted   chan error
	holdForDeadline bool
	head            string
}

func (r probeHoldingRunner) Run(probeCtx context.Context, _ string, args ...string) ([]byte, error) {
	go func() {
		var version int64
		r.readCompleted <- r.s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items LIMIT 1`).Scan(&version)
	}()
	if r.holdForDeadline {
		<-probeCtx.Done()
		return nil, probeCtx.Err()
	}
	select {
	case <-r.release:
	case <-probeCtx.Done():
		return nil, probeCtx.Err()
	}
	if len(args) > 0 && args[0] == "status" {
		return nil, nil
	}
	return []byte(r.head), nil
}

// TestDispatchSubjectFreshnessC1Boundaries proves the C1 obligations for the
// dispatch surface: the probe phase holds no SQL connection, the fixed
// deadline refuses with zero effects, and state that changes between the
// pre-probe snapshot and the transactional revalidation refuses with zero
// effects — for the ordinary dispatch and the combined rejection with retry.
func TestDispatchSubjectFreshnessC1Boundaries(t *testing.T) {
	setup := func(t *testing.T, workID string) (*Store, string, string, string, func(t *testing.T, attemptID string, runner GitRunner) error) {
		t.Helper()
		fixture := seedWorkContextFixture(t, workID)
		s := fixture.store
		claimDir, head := reviewSubjectClaim(t, s, fixture.workID)
		fixtureSubject := head
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil || view.SubjectCommit != head {
			t.Fatalf("admitted view subject = %q err = %v, want the qualified HEAD %q", viewSubject(view), viewErr, head)
		}
		lane := laneForID(t)
		job := reviewSubjectReadyRevision(t, fixture)
		view, viewErr = readWorkContextView(context.Background(), s.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil || view.SubjectCommit != head {
			t.Fatalf("admitted view subject = %q err = %v, want the qualified HEAD %q", viewSubject(view), viewErr, head)
		}
		if err := dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-c1-x", job, view)); err != nil {
			t.Fatalf("implement dispatch refused: %v", err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(fixture.workID, "attempt-c1-x", lane, job)}}); err != nil {
			t.Fatal(err)
		}
		completion := Event{EventID: "complete-c1-x-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: fixture.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
			AttemptID: "attempt-c1-x", ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
			EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: &job,
		})}
		prepared, prepErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: fixture.workID, AttemptID: "attempt-c1-x", EventID: completion.EventID})
		if prepErr != nil {
			t.Fatalf("terminal delivery preparation refused: %v", prepErr)
		}
		workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, completion)
		rejectWithRetry := func(t *testing.T, attemptID string, runner GitRunner) error {
			t.Helper()
			reviewSubjectRecordOperator(t, s, fixture.workID)
			operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + fixture.workID + "-operator", ActorClass: ActorOperator}
			version := readWorkVersion(t, s, fixture.workID)
			payload, marshalErr := json.Marshal(map[string]any{
				"attempt_id": attemptID, "attempt_epoch": int64(1),
				"diagnosis": "the delivered change missed the recorded check", "strategy": "reproduce the check and repair the change",
				"predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"evidence:" + attemptID + "-reject"},
				"retry": map[string]any{"lane_id": "implement"},
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			preparation, prepErr := PrepareWorkerDispatchFreshness(context.Background(), s, PrepareWorkerDispatchRequest{WorkID: fixture.workID, SessionWorktree: claimDir, Runner: runner})
			if prepErr != nil {
				return prepErr
			}
			ctx := WithWorkerDispatchPreparation(context.Background(), preparation)
			_, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
				WorkID: fixture.workID, ExpectedVersion: version, ActionID: "reject_worker_result", Payload: payload,
				SessionWorktree: claimDir, Actor: operator, AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-reject-inputs"),
				IdempotencyIdentity: attemptID + "-reject-op", OperationID: "op-" + attemptID + "-reject", PrincipalRef: operator.PrincipalRef,
				Tool: "concord_work_transition", IdempotencyKey: attemptID + "-reject-key", RequestID: "req-" + attemptID + "-reject",
				AcceptedScope: `{}`, ContractDigest: testManifestDigest,
			})
			return err
		}
		return s, fixture.workID, claimDir, fixtureSubject, rejectWithRetry
	}

	t.Run("no sql connection is held while the subject probe runs", func(t *testing.T) {
		s, workID, claimDir, subject, _ := setup(t, "work-c1-hold-ordinary")
		release := make(chan struct{})
		runner := probeHoldingRunner{s: s, release: release, readCompleted: make(chan error, 1), head: subject}
		preparationErr := make(chan error, 1)
		go func() {
			_, err := PrepareWorkerDispatchFreshness(context.Background(), s, PrepareWorkerDispatchRequest{WorkID: workID, SessionWorktree: claimDir, Runner: runner})
			preparationErr <- err
		}()
		select {
		case readErr := <-runner.readCompleted:
			if readErr != nil {
				t.Fatalf("the independent SQL read failed while the probe held: %v", readErr)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the independent SQL read never completed while the probe held; a store snapshot or write transaction spans the subprocess")
		}
		select {
		case err := <-preparationErr:
			t.Fatalf("the preparation finished before the probe released: %v", err)
		default:
		}
		close(release)
		if err := <-preparationErr; err != nil {
			t.Fatalf("preparation refused after release: %v", err)
		}
		// The same holding shape covers the combined rejection's preparation.
		s2, _, _, subject2, rejectWithRetry2 := setup(t, "work-c1-hold-combined")
		release2 := make(chan struct{})
		runner2 := probeHoldingRunner{s: s2, release: release2, readCompleted: make(chan error, 1), head: subject2}
		rejectionErr := make(chan error, 1)
		go func() {
			rejectionErr <- rejectWithRetry2(t, "attempt-c1-x", runner2)
		}()
		select {
		case readErr := <-runner2.readCompleted:
			if readErr != nil {
				t.Fatalf("the independent SQL read failed while the combined probe held: %v", readErr)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the independent SQL read never completed while the combined probe held; a store snapshot or write transaction spans the subprocess")
		}
		close(release2)
		if err := <-rejectionErr; err != nil {
			t.Fatalf("the combined rejection refused after release: %v", err)
		}
	})

	t.Run("the fixed probe deadline refuses with zero effects", func(t *testing.T) {
		s, workID, claimDir, _, _ := setup(t, "work-c1-deadline-ordinary")
		baseline := dispatchStartedCount(t, s, workID)
		_, err := PrepareWorkerDispatchFreshness(context.Background(), s, PrepareWorkerDispatchRequest{WorkID: workID, SessionWorktree: claimDir, Runner: probeHoldingRunner{s: s, readCompleted: make(chan error, 1), holdForDeadline: true}})
		if !hasFailureKind(err, KindTimeout) {
			t.Fatalf("deadline preparation error = %v, want %s", err, KindTimeout)
		}
		if got := dispatchStartedCount(t, s, workID); got != baseline {
			t.Fatalf("dispatch_worker starts = %d, want the setup baseline %d with no refusal effects", got, baseline)
		}
		s2, workID2, _, _, rejectWithRetry2 := setup(t, "work-c1-deadline-combined")
		err = rejectWithRetry2(t, "attempt-c1-x", probeHoldingRunner{s: s2, readCompleted: make(chan error, 1), holdForDeadline: true})
		if !hasFailureKind(err, KindTimeout) {
			t.Fatalf("combined deadline error = %v, want %s", err, KindTimeout)
		}
		var rejections, retryAttempts int
		if err := s2.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result'`, workID2, WorkflowActionCompleted).Scan(&rejections); err != nil {
			t.Fatal(err)
		}
		if err := s2.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id LIKE '%:retry'`, workID2).Scan(&retryAttempts); err != nil {
			t.Fatal(err)
		}
		if rejections != 0 || retryAttempts != 0 {
			t.Fatalf("the deadline-refused combined operation committed %d rejection(s) and %d retry attempt(s), want zero effects", rejections, retryAttempts)
		}
	})

	t.Run("identity drift between the snapshot and the admission refuses with zero effects", func(t *testing.T) {
		s, workID, driftClaimDir, _, _ := setup(t, "work-c1-drift-ordinary")
		owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
		acceptCurrentReviewForTest(t, workContextFixture{store: s, workID: workID, owner: owner}, driftClaimDir, "continue")
		preparation, prepErr := PrepareWorkerDispatchFreshness(context.Background(), s, PrepareWorkerDispatchRequest{WorkID: workID, SessionWorktree: driftClaimDir})
		if prepErr != nil {
			t.Fatalf("preparation refused: %v", prepErr)
		}
		job := reviewSubjectReadyRevision(t, workContextFixture{store: s, workID: workID, owner: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}})
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
		if viewErr != nil || view == nil {
			t.Fatalf("admitted view = %+v err = %v, want a present view", view, viewErr)
		}
		// The work version moves after the sealed snapshot, and the packet is
		// rebuilt from the moved state, so the freshness revalidation itself
		// is the refusing guard, with zero dispatch effects.
		version := readWorkVersion(t, s, workID)
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor(workID+":c1-drift-bump", WorkflowActorRecorded, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef,
			"agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": string(owner.ActorClass),
		})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			t.Fatal(err)
		}
		movedView, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
		if viewErr != nil || movedView == nil {
			t.Fatalf("moved view = %+v err = %v, want a present view", movedView, viewErr)
		}
		baseline := dispatchStartedCount(t, s, workID)
		driftPacket := workContextDispatchPacket(t, workContextFixture{store: s, workID: workID, owner: owner}, "attempt-c1-drift", job, movedView)
		driftPacket["inputs"].(map[string]any)["binding"].(map[string]any)["work_version"] = readWorkVersion(t, s, workID)
		driftErr := dispatchBindingAttemptCtx(t, s, cd0059DispatchSeed{workID: workID, ownerActor: owner}, "attempt-c1-drift", driftPacket, WithWorkerDispatchPreparation(context.Background(), preparation))
		if driftErr == nil || !hasFailureKind(driftErr, KindProjectionConflict) || !strings.Contains(driftErr.Error(), "changed under the dispatch subject observation") {
			t.Fatalf("drift dispatch error = %v, want %s refusing the state changed under the sealed observation", driftErr, KindProjectionConflict)
		}
		if got := dispatchStartedCount(t, s, workID); got != baseline {
			t.Fatalf("dispatch_worker starts = %d, want the setup baseline %d with no refusal effects", got, baseline)
		}
	})
}

// ---- the aggregate dispatch context bound ----

// aggregateBoundStatement composes one legal finding statement of exactly
// 1024 bytes from a prefix the caller chooses. The escaped and multibyte
// prefixes make the boundary exercise canonical JSON escaping and UTF-8
// byte counting rather than rune counts.
func aggregateBoundStatement(prefix string) string {
	const want = 1024
	base := prefix + " the retained finding measures UTF-8 bytes, never runes"
	if len(base) > want {
		panic("aggregate statement prefix exceeds the 1024-byte finding bound")
	}
	return base + strings.Repeat("n", want-len(base))
}

// aggregateBoundFindings builds 16 legal findings whose statements carry the
// prefix content and whose subject and evidence references size the retained
// entries. Every entry stays inside its per-entry bound; only the combined
// population approaches the aggregate dispatch bound.
func aggregateBoundFindings(statementPrefix, evidenceRefPrefix string) []map[string]any {
	refs := []string{evidenceRefPrefix + "-a", evidenceRefPrefix + "-b"}
	out := make([]map[string]any, 0, 16)
	for i := 0; i < 16; i++ {
		out = append(out, map[string]any{
			"kind":          "observation",
			"statement":     aggregateBoundStatement(statementPrefix),
			"subject_ref":   strings.Repeat("s", 128),
			"evidence_refs": refs,
			"domain_id":     workContextTestChild,
		})
	}
	// The first finding carries the escaped and multibyte content, so the
	// boundary bytes are never a plain-ASCII-only construction.
	out[0]["statement"] = aggregateBoundStatement(`escaped "quotes" \backslash\ ünïcödé 多言語`)
	return out
}

// seedAggregateBoundDeclarations records the two retained declarations whose
// 32 findings size the current view near the aggregate bound.
func seedAggregateBoundDeclarations(t *testing.T, f workContextFixture, evidenceRefBytes int) {
	t.Helper()
	ref := strings.Repeat("e", evidenceRefBytes)
	first := map[string]any{
		"required_reading": []map[string]any{
			f.repoReading(workContextTestRoot, workContextRootRationale, ""),
			f.knowledgeReading(),
		},
		"finding_refs":     []string{},
		"context_findings": aggregateBoundFindings("finding", ref),
	}
	if err := f.recordWorkContext(t, first); err != nil {
		t.Fatalf("first aggregate declaration refused: %v", err)
	}
	var seqs []int64
	rows, err := f.store.DatabaseForTesting().Query(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq`, f.workID, WorkflowWorkContextRecorded)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 1 {
		t.Fatalf("aggregate fixture declarations = %d, want the one seeded declaration", len(seqs))
	}
	refs := make([]string, 0, 16)
	for ordinal := 0; ordinal < 16; ordinal++ {
		refs = append(refs, fmt.Sprintf("finding:%d:%d", seqs[0], ordinal))
	}
	second := map[string]any{
		"finding_refs":     refs,
		"context_findings": aggregateBoundFindings("second finding", ref),
	}
	if err := f.recordWorkContext(t, second); err != nil {
		t.Fatalf("second aggregate declaration refused: %v", err)
	}
}

// seedAggregateBoundContext seeds the full boundary fixture: the claimed
// worktree with its qualified subject, the ready job revision whose native
// preparations ride the view, and the two retained declarations. It returns
// the job binding the packets bind.
func seedAggregateBoundContext(t *testing.T, f workContextFixture, evidenceRefBytes int) WorkerJobBinding {
	t.Helper()
	reviewSubjectClaim(t, f.store, f.workID)
	job := reviewSubjectReadyRevision(t, f)
	seedAggregateBoundDeclarations(t, f, evidenceRefBytes)
	return job
}

// recordAggregateCheckpoint records one checkpoint_context action with the
// given bounded prose, through the same action route a coordinator uses.
func recordAggregateCheckpoint(t *testing.T, f workContextFixture, hypothesis, diagnosis, strategy string) {
	t.Helper()
	if err := runVerdictActionAs(t, f.store, f.workID, "checkpoint_context", json.RawMessage(mustJSONValue(map[string]any{
		"active_unit": "the bounded unit", "hypothesis": hypothesis, "diagnosis": diagnosis, "strategy": strategy,
		"touched_refs": []string{"internal/store/work_context.go"}, "evidence_refs": []string{"evidence:checkpoint"}, "pending_questions": []string{}, "pending_decisions": []string{},
	})), 0, f.owner); err != nil {
		t.Fatalf("checkpoint_context refused: %v", err)
	}
}

// aggregateBoundaryMembers reads the packet members from the same readers
// the packet builders use.
func aggregateBoundaryMembers(t *testing.T, f workContextFixture, correction any) map[string]any {
	t.Helper()
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := readLatestContextCheckpointTx(context.Background(), f.store.DatabaseForTesting(), f.workID, "workflow_action")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"work_context": view, "checkpoint": checkpoint, "correction": correction}
}

// aggregateBoundaryProbe measures the canonical combined object the
// aggregate bound defines, composed from the members the caller names: each
// present member is canonicalized through the shared encoder, the sorted
// member object is assembled with its names and delimiters, and its UTF-8
// bytes are counted. Absent members carry no value and contribute nothing.
func aggregateBoundaryProbe(t *testing.T, members map[string]any) int {
	t.Helper()
	object := map[string]json.RawMessage{}
	for name, value := range members {
		if value == nil {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := canonicalJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		object[name] = canonical
	}
	assembled, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return len(assembled)
}

// tuneAggregateCheckpointToTarget records checkpoints until the present
// members measure exactly target bytes. The checkpoint's bounded prose
// fields are the one-byte-granular pad: ASCII padding changes the canonical
// member by exactly its byte length, so the boundary is landed by
// construction, not by hoping fixture sizes align.
func tuneAggregateCheckpointToTarget(t *testing.T, f workContextFixture, correction any, target int) *ContextCheckpoint {
	t.Helper()
	const fieldMax = 4090
	parts := [3]string{"hypothesis", "diagnosis", "strategy"}
	record := func() {
		recordAggregateCheckpoint(t, f, parts[0], parts[1], parts[2])
	}
	record()
	for iteration := 0; iteration < 6; iteration++ {
		measured := aggregateBoundaryProbe(t, aggregateBoundaryMembers(t, f, correction))
		delta := target - measured
		if delta == 0 {
			checkpoint, err := readLatestContextCheckpointTx(context.Background(), f.store.DatabaseForTesting(), f.workID, "workflow_action")
			if err != nil {
				t.Fatal(err)
			}
			return checkpoint
		}
		if delta < 0 {
			// Digit-length drift on an already-padded record can overshoot
			// by a byte or two: fall back to the short probe strings and
			// re-approach.
			parts = [3]string{"hypothesis", "diagnosis", "strategy"}
			record()
			continue
		}
		for field := 0; field < 3 && delta > 0; field++ {
			add := min(delta, fieldMax-len(parts[field]))
			parts[field] += strings.Repeat("p", add)
			delta -= add
		}
		if delta > 0 {
			t.Fatalf("aggregate probe measured %d bytes; the bounded checkpoint prose cannot pad the remaining %d bytes to %d — recalibrate the seeded context size", measured, delta, target)
		}
		record()
	}
	t.Fatalf("checkpoint tuning did not converge on %d bytes", target)
	return nil
}

// aggregateDispatchPacket builds the closed implement-lane packet the repair
// step dispatches, carrying the given checkpoint and correction members
// (nil omits). The job revision is seeded by the caller because a fixture
// may dispatch more than one attempt.
func aggregateDispatchPacket(t *testing.T, f workContextFixture, attemptID string, job WorkerJobBinding, checkpoint, correction any) map[string]any {
	t.Helper()
	laneDigest := implementLaneIdentity()
	task, binding := recordedPacketInputs(t, f.store, f.workID, "implement")
	inputs := map[string]any{
		"task":        task,
		"binding":     binding,
		"worker_job":  recordedPacketJobForTest(t, f.store, f.workID, job, "implement"),
		"constraints": []string{"do-not-modify-product-truth"},
	}
	for member, value := range recordedPacketRecords(t, f.store, f.workID) {
		inputs[member] = value
	}
	delete(inputs, "work_context")
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if err != nil {
		t.Fatal(err)
	}
	if view != nil {
		inputs["work_context"] = view
	}
	if checkpoint != nil {
		inputs["checkpoint"] = checkpoint
	}
	if correction != nil {
		inputs["correction"] = correction
	}
	return map[string]any{
		"schema_version": WorkerPacketSchemaVersion,
		"attempt_id":     attemptID,
		"lane_id":        "implement",
		"lane_digest":    laneDigest,
		"work_id":        f.workID,
		"step_id":        "repair",
		"inputs":         inputs,
	}
}

// aggregateRejectResult records the operator's reject_worker_result for one
// completed attempt, with the retry member only when withRetry is true.
func aggregateRejectResult(t *testing.T, f workContextFixture, attemptID string, withRetry bool, diagnosis, strategy string) error {
	t.Helper()
	reviewSubjectRecordOperator(t, f.store, f.workID)
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + f.workID + "-operator", ActorClass: ActorOperator}
	payload := map[string]any{
		"attempt_id": attemptID, "attempt_epoch": int64(1),
		"diagnosis": diagnosis, "strategy": strategy,
		"predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"evidence:" + attemptID + "-reject"},
	}
	if withRetry {
		payload["retry"] = map[string]any{"lane_id": "implement"}
	}
	version := readWorkVersion(t, f.store, f.workID)
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	_, err = invokeWorkflowActionForCD0059(context.Background(), t, f.store, WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: version, ActionID: "reject_worker_result", Payload: payloadBytes,
		SessionWorktree: dispatchSessionWorktree(t, f.store, f.workID), Actor: operator,
		AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-reject-inputs"),
		IdempotencyIdentity:  attemptID + "-reject-op", OperationID: "op-" + attemptID + "-reject", PrincipalRef: operator.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: attemptID + "-reject-key", RequestID: "req-" + attemptID + "-reject",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	return err
}

// aggregateCommittedRejections counts the committed reject_worker_result
// completions and derived retry attempts, so a refusal can be proved atomic.
func aggregateCommittedRejections(t *testing.T, s *Store, workID string) (int, int) {
	t.Helper()
	var rejections, retryAttempts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result'`, workID, WorkflowActionCompleted).Scan(&rejections); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id LIKE '%:retry'`, workID).Scan(&retryAttempts); err != nil {
		t.Fatal(err)
	}
	return rejections, retryAttempts
}

// aggregateAssemblyDispatch runs the dispatch admission's own completion
// assembly — the unit the aggregate bound lives in — over a real transaction
// on the fixture store, carrying the sealed freshness preparation the live
// boundary prepares and the derived session identity the raw action route
// computes. The store's whole-action-payload input bound refuses oversized
// packets earlier on the full route; that bound owns a distinct population,
// and the assembly reaches the aggregate admission with every other packet
// admission live. It returns the appended completion events on success, or
// the refusal error; the caller owns the transaction outcome.
func aggregateAssemblyDispatch(t *testing.T, f workContextFixture, attemptID string, packet map[string]any) ([]Event, error) {
	t.Helper()
	subject, subjectErr := readCurrentOracleSubject(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if subjectErr != nil {
		return nil, subjectErr
	}
	preparation, prepErr := PrepareWorkerDispatchFreshness(context.Background(), f.store, PrepareWorkerDispatchRequest{
		WorkID: f.workID, SessionWorktree: dispatchSessionWorktree(t, f.store, f.workID),
		Runner: qualifiedSubjectRunner{head: subject},
	})
	if prepErr != nil {
		return nil, prepErr
	}
	ctx := WithWorkerDispatchPreparation(context.Background(), preparation)
	sessionWorktree := dispatchSessionWorktree(t, f.store, f.workID)
	canonicalPath, canonicalErr := canonicalWorkerWorktreePath(sessionWorktree)
	if canonicalErr != nil {
		t.Fatalf("session worktree identity cannot be resolved: %v", canonicalErr)
	}
	version := readWorkVersion(t, f.store, f.workID)
	packetBytes, marshalErr := json.Marshal(packet)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	payload, marshalErr := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	entry, entryErr := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if entryErr != nil {
		t.Fatal(entryErr)
	}
	tx, err := f.store.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	request := WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: version, ActionID: "dispatch_worker", Payload: payload,
		SessionWorktree: sessionWorktree, SessionWorktreeIdentity: workerWorktreeIdentity(canonicalPath), Actor: f.owner,
		AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-inputs"),
		IdempotencyIdentity:  attemptID + "-op", OperationID: "op-" + attemptID, PrincipalRef: f.owner.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: attemptID + "-key", RequestID: "req-" + attemptID,
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	}
	events, _, err := appendGenericWorkflowCompletion(workflowActionAssemblyInput{
		ctx: ctx, tx: tx, entry: entry, request: request,
		currentStep: "repair", payload: payload,
		evidenceRefs: []string{"evidence:" + attemptID}, eventActor: "client:concord-1:principal/operator",
	}, 1, []Event{})
	if err != nil {
		return nil, err
	}
	return events, nil
}

// TestDispatchContextAggregateBound proves the whole-dispatch-context bound:
// the canonical JSON object of the packet's present work_context, checkpoint,
// and correction members — member names and delimiters included — may reach
// exactly 65,536 UTF-8 bytes and no more. The boundary is built from
// individually legal retained entries (32 retained findings, the ready job's
// native oracle preparations, bounded checkpoint prose), never one oversized
// invalid string, and carries escaped and multibyte content so the count is
// canonical UTF-8 bytes rather than rune counts or the raw packet's
// whitespace. An over-bound dispatch refuses whole with zero dispatch
// effects.
func TestDispatchContextAggregateBound(t *testing.T) {
	t.Run("exactly 65536 bytes admits", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-agg-boundary-exact")
		defer fixture.store.Close()
		job := seedAggregateBoundContext(t, fixture, 120)
		checkpoint := tuneAggregateCheckpointToTarget(t, fixture, nil, 65536)
		events, err := aggregateAssemblyDispatch(t, fixture, "attempt-agg-exact", aggregateDispatchPacket(t, fixture, "attempt-agg-exact", job, checkpoint, nil))
		if err != nil {
			t.Fatalf("dispatch refused an aggregate exactly at the 65536-byte bound: %v", err)
		}
		if len(events) != 1 || events[0].Kind != WorkflowActionCompleted {
			t.Fatalf("assembly appended %d event(s), want the one dispatch completion at exactly the bound", len(events))
		}
	})

	t.Run("one byte over refuses whole with zero dispatch effects", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-agg-boundary-over")
		defer fixture.store.Close()
		job := seedAggregateBoundContext(t, fixture, 120)
		checkpoint := tuneAggregateCheckpointToTarget(t, fixture, nil, 65537)
		events, err := aggregateAssemblyDispatch(t, fixture, "attempt-agg-over", aggregateDispatchPacket(t, fixture, "attempt-agg-over", job, checkpoint, nil))
		var failure *Failure
		if err == nil || !errors.As(err, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, "65537") || !strings.Contains(failure.Detail, "65536") {
			t.Fatalf("over-bound dispatch error = %v, want %s naming the measured 65537 bytes and the 65536-byte bound", err, KindLimitExceeded)
		}
		if len(events) != 0 {
			t.Fatalf("assembly appended %d event(s), want zero on the aggregate refusal", len(events))
		}
		view, viewErr := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
		if viewErr != nil || view == nil || len(view.Findings) != 32 {
			t.Fatalf("retained view after refusal = %d findings err %v, want the whole untrimmed 32-finding view", len(viewFindings(view)), viewErr)
		}
	})

	t.Run("a packet with no context members admits", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		workID := "work-agg-absent-members"
		seedGitClaimRepo(t, s, workID)
		seed := seedDispatchFixture(t, s, workID)
		if view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID); viewErr != nil || view != nil {
			t.Fatalf("fixture view = %+v err = %v, want no context members at all", view, viewErr)
		}
		if err := dispatchBindingAttemptCtx(t, s, seed, "attempt-agg-absent", dispatchWorkerPacket(t, s, workID, "execution", "attempt-agg-absent"), context.Background()); err != nil {
			t.Fatalf("dispatch refused a packet with no context members: %v", err)
		}
		if got := dispatchStartedCount(t, s, workID); got != 1 {
			t.Fatalf("dispatch_worker starts = %d, want 1 without context members", got)
		}
	})

	t.Run("all three members measure together at the bound", func(t *testing.T) {
		setup := func(t *testing.T, workID string, target int) ([]Event, error) {
			t.Helper()
			fixture := seedWorkContextFixture(t, workID)
			defer fixture.store.Close()
			_, _ = reviewSubjectClaim(t, fixture.store, fixture.workID)
			job := reviewSubjectReadyRevision(t, fixture)
			seedAggregateBoundDeclarations(t, fixture, 40)
			recordAggregateCheckpoint(t, fixture, "hypothesis", "diagnosis", "strategy")
			if err := dispatchWorkContextAttempt(t, fixture, aggregateDispatchPacket(t, fixture, "attempt-agg-a1", job, latestFixtureCheckpoint(t, fixture), nil)); err != nil {
				t.Fatalf("first dispatch refused before the correction existed: %v", err)
			}
			completeAggregateAttempt(t, fixture, "attempt-agg-a1", job)
			if err := aggregateRejectResult(t, fixture, "attempt-agg-a1", false, strings.Repeat("d", 4000), strings.Repeat("s", 4000)); err != nil {
				t.Fatalf("rejection without the retry member refused: %v", err)
			}
			correction, err := workflowCorrectionContextForDispatch(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID, "repair", "attempt-agg-a2")
			if err != nil {
				t.Fatal(err)
			}
			if correction == nil {
				t.Fatal("the rejection correction is not open for the next attempt; the all-three case cannot be built")
			}
			checkpoint := tuneAggregateCheckpointToTarget(t, fixture, correction, target)
			return aggregateAssemblyDispatch(t, fixture, "attempt-agg-a2", aggregateDispatchPacket(t, fixture, "attempt-agg-a2", job, checkpoint, correction))
		}
		events, err := setup(t, "ctx-agg-all-three-exact", 65536)
		if err != nil {
			t.Fatalf("dispatch refused an all-three aggregate exactly at the bound: %v", err)
		}
		if len(events) != 1 || events[0].Kind != WorkflowActionCompleted {
			t.Fatalf("assembly appended %d event(s), want the one dispatch completion at exactly the all-three bound", len(events))
		}
		events, err = setup(t, "ctx-agg-all-three-over", 65537)
		var failure *Failure
		if err == nil || !errors.As(err, &failure) || failure.Kind != KindLimitExceeded {
			t.Fatalf("all-three over-bound dispatch error = %v, want %s refusing the whole request", err, KindLimitExceeded)
		}
		if len(events) != 0 {
			t.Fatalf("assembly appended %d event(s), want zero on the all-three aggregate refusal", len(events))
		}
	})

	t.Run("a full-route dispatch at exactly 65536 admits", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-agg-full-route-exact")
		defer fixture.store.Close()
		job := seedAggregateBoundContext(t, fixture, 120)
		checkpoint := tuneAggregateCheckpointToTarget(t, fixture, nil, 65536)
		if err := dispatchWorkContextAttempt(t, fixture, aggregateDispatchPacket(t, fixture, "attempt-agg-full-route", job, checkpoint, nil)); err != nil {
			t.Fatalf("the full dispatch route refused an aggregate exactly at the 65536-byte bound: %v", err)
		}
		if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 1 {
			t.Fatalf("dispatch_worker starts = %d, want 1 for the admitted full-route dispatch", got)
		}
	})

	t.Run("a known-valid smaller context passes the same full route", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-agg-full-route-control")
		defer fixture.store.Close()
		job := seedAggregateBoundContext(t, fixture, 120)
		recordAggregateCheckpoint(t, fixture, "hypothesis", "diagnosis", "strategy")
		if measured := aggregateBoundaryProbe(t, aggregateBoundaryMembers(t, fixture, nil)); measured >= 65536 {
			t.Fatalf("control fixture aggregate = %d bytes, want clearly under the bound", measured)
		}
		if err := dispatchWorkContextAttempt(t, fixture, aggregateDispatchPacket(t, fixture, "attempt-agg-control", job, latestFixtureCheckpoint(t, fixture), nil)); err != nil {
			t.Fatalf("the full dispatch route refused a known-valid smaller context: %v", err)
		}
		if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 1 {
			t.Fatalf("dispatch_worker starts = %d, want 1 for the admitted control", got)
		}
	})
}

// latestFixtureCheckpoint reads the fixture's latest checkpoint as the
// packet member.
func latestFixtureCheckpoint(t *testing.T, f workContextFixture) *ContextCheckpoint {
	t.Helper()
	checkpoint, err := readLatestContextCheckpointTx(context.Background(), f.store.DatabaseForTesting(), f.workID, "workflow_action")
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint == nil {
		t.Fatal("fixture holds no latest checkpoint")
	}
	return checkpoint
}

// completeAggregateAttempt records the completed implement result for the
// fixture's dispatched attempt through the one terminal derivation seam, the
// same route the rejection fixtures use.
func completeAggregateAttempt(t *testing.T, f workContextFixture, attemptID string, job WorkerJobBinding) {
	t.Helper()
	lane := laneForID(t)
	if err := ApplyOperation(context.Background(), f.store, Operation{Events: []Event{implementDispatchEvidenceEvent(f.workID, attemptID, lane, job)}}); err != nil {
		t.Fatal(err)
	}
	completion := Event{EventID: "complete-" + attemptID + "-" + f.workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: &job,
	})}
	prepared, prepErr := PrepareWorkerTerminalDelivery(context.Background(), f.store, PrepareWorkerTerminalDeliveryRequest{WorkID: f.workID, AttemptID: attemptID, EventID: completion.EventID})
	if prepErr != nil {
		t.Fatalf("terminal delivery preparation refused: %v", prepErr)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), f.store, completion)
}

// TestDispatchContextAggregateMeasurement pins the measurement the aggregate
// bound defines, at the population the behavioral boundary tests cannot
// reach with recorded state alone: canonical UTF-8 bytes of the sorted
// member object, absent and null members contributing nothing, raw packet
// whitespace uncounted, and nested oracle preparation and receipt bytes
// included.
func TestDispatchContextAggregateMeasurement(t *testing.T) {
	t.Parallel()
	measure := func(t *testing.T, packet string) int {
		t.Helper()
		measured, err := measureDispatchContextAggregateBytes(json.RawMessage(packet))
		if err != nil {
			t.Fatalf("measurement refused: %v", err)
		}
		return measured
	}
	compact := func(inputs string) string {
		return `{"inputs":` + inputs + `}`
	}

	t.Run("absent members measure the two-byte empty object", func(t *testing.T) {
		if got := measure(t, compact(`{}`)); got != 2 {
			t.Fatalf("memberless measurement = %d, want the two bytes of the empty object", got)
		}
		if got := measure(t, compact(`{"task":"carry the objective"}`)); got != 2 {
			t.Fatalf("non-context measurement = %d, want 2; only the three context members are measured", got)
		}
	})

	t.Run("a present null member measures its canonical name and value", func(t *testing.T) {
		if got := measure(t, compact(`{"work_context":null}`)); got != len(`{"work_context":null}`) {
			t.Fatalf("present-null measurement = %d, want the %d canonical bytes of the present key; a present key is never absence", got, len(`{"work_context":null}`))
		}
		mixed := `{"checkpoint":{"s":"x"},"work_context":null}`
		if got := measure(t, compact(`{"work_context":null,"checkpoint":{"s":"x"}}`)); got != len(mixed) {
			t.Fatalf("null-plus-one measurement = %d, want the %d canonical bytes of both present members", got, len(mixed))
		}
	})

	t.Run("the closed packet schema refuses a null member the owner owns", func(t *testing.T) {
		registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
		if err != nil {
			t.Fatal(err)
		}
		laneDigest := implementLaneIdentity()
		for _, member := range []string{"work_context", "checkpoint", "correction"} {
			packet := map[string]any{
				"schema_version": WorkerPacketSchemaVersion, "attempt_id": "attempt-null-member",
				"lane_id": "implement", "lane_digest": laneDigest,
				"work_id": "work-null-member", "step_id": "repair",
				"inputs": map[string]any{member: nil, "task": "carry the objective", "binding": map[string]any{"objective_source": "work_question", "work_version": 1, "contract_version": nil, "assigned_result": "files_touched"}},
			}
			packetBytes, marshalErr := json.Marshal(packet)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			payload, marshalErr := json.Marshal(map[string]any{"attempt_id": "attempt-null-member", "worker_packet": json.RawMessage(packetBytes)})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			validateErr := validateWorkflowActionPayload(registered.Definition, "dispatch_worker", payload)
			if !hasFailureKind(validateErr, KindInvalidPayload) {
				t.Fatalf("schema owner accepted a present null %s member: %v", member, validateErr)
			}
		}
	})

	t.Run("one member measures its canonical bytes", func(t *testing.T) {
		if got := measure(t, compact(`{"checkpoint":{"a":"b"}}`)); got != len(`{"checkpoint":{"a":"b"}}`) {
			t.Fatalf("one-member measurement = %d, want the member object with its name and delimiters", got)
		}
	})

	t.Run("raw packet whitespace is not measured; canonical bytes are", func(t *testing.T) {
		pretty := `{"inputs":{"checkpoint": { "a"  :  "b" } }}`
		if got := measure(t, pretty); got != len(`{"checkpoint":{"a":"b"}}`) {
			t.Fatalf("pretty-printed measurement = %d, want the canonical member bytes without the raw whitespace", got)
		}
	})

	t.Run("multibyte content measures UTF-8 bytes, never runes", func(t *testing.T) {
		statement := strings.Repeat("é", 10)
		want := `{"work_context":{"statement":"` + statement + `"}}`
		if got := measure(t, compact(`{"work_context":{"statement":"`+statement+`"}}`)); got != len(want) {
			t.Fatalf("multibyte measurement = %d, want the %d UTF-8 bytes of the canonical member", got, len(want))
		}
		if got := measure(t, compact(`{"work_context":{"statement":"`+statement+`"}}`)); got == len([]rune(want)) {
			t.Fatalf("multibyte measurement equals the rune count %d; the bound must count UTF-8 bytes", got)
		}
	})

	t.Run("escaped content counts the canonical escaping", func(t *testing.T) {
		want := `{"correction":{"diagnosis":"a \"quoted\" \\path\u003cb"}}`
		if got := measure(t, compact(`{"correction":{"diagnosis":"a \"quoted\" \\path<b"}}`)); got != len(want) {
			t.Fatalf("escaped measurement = %d, want the %d canonical bytes including HTML escaping", got, len(want))
		}
	})

	t.Run("nested oracle preparation and receipt bytes are measured", func(t *testing.T) {
		nested := `{"oracle_preparations":[{"recipe":"go test ./...","digest":"sha256:abcd"}],"oracle_receipts":[{"control":"control:check","state":"clean","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`
		want := `{"work_context":` + nested + `}`
		if got := measure(t, compact(`{"work_context":`+nested+`}`)); got != len(want) {
			t.Fatalf("nested measurement = %d, want the %d bytes including every nested preparation and receipt", got, len(want))
		}
	})
}

// viewFindings keeps the nil-safe finding count the refusal assertions read.
func viewFindings(view *WorkContextView) []WorkContextFindingView {
	if view == nil {
		return nil
	}
	return view.Findings
}

// TestRejectRetryContextAggregateBound proves the derived retry dispatch a
// combined rejection carries answers to the same aggregate bound through the
// one dispatch owner: an over-bound retry refuses the whole combined
// operation atomically, so the rejection commits nothing, while a rejection
// without the retry member commits at the same over-bound state — the bound
// is a dispatch capacity, not a rejection wall.
func TestRejectRetryContextAggregateBound(t *testing.T) {
	setup := func(t *testing.T, workID string) (workContextFixture, func(t *testing.T, withRetry bool) error) {
		t.Helper()
		fixture := seedWorkContextFixture(t, workID)
		job := seedAggregateBoundContext(t, fixture, 180)
		recordAggregateCheckpoint(t, fixture, "hypothesis", "diagnosis", "strategy")
		if err := dispatchWorkContextAttempt(t, fixture, aggregateDispatchPacket(t, fixture, "attempt-rr-a1", job, latestFixtureCheckpoint(t, fixture), nil)); err != nil {
			t.Fatalf("first dispatch refused before the rejection existed: %v", err)
		}
		completeAggregateAttempt(t, fixture, "attempt-rr-a1", job)
		reject := func(t *testing.T, withRetry bool) error {
			t.Helper()
			return aggregateRejectResult(t, fixture, "attempt-rr-a1", withRetry, strings.Repeat("d", 4000), strings.Repeat("s", 4000))
		}
		return fixture, reject
	}

	t.Run("an over-bound derived retry refuses the combined operation atomically", func(t *testing.T) {
		fixture, reject := setup(t, "ctx-agg-retry-over")
		defer fixture.store.Close()
		// The rejection records the operator actor it cites before it runs;
		// baseline the version after that recording so the assertion below
		// measures the combined operation alone.
		reviewSubjectRecordOperator(t, fixture.store, fixture.workID)
		versionBefore := readWorkVersion(t, fixture.store, fixture.workID)
		err := reject(t, true)
		var failure *Failure
		if err == nil || !errors.As(err, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.Detail, "65536") || !strings.Contains(failure.Detail, "bytes of canonical JSON") {
			t.Fatalf("combined reject/retry over-bound refusal = %v, want %s naming the measured canonical bytes and the 65536-byte bound", err, KindLimitExceeded)
		}
		rejections, retryAttempts := aggregateCommittedRejections(t, fixture.store, fixture.workID)
		if rejections != 0 || retryAttempts != 0 {
			t.Fatalf("the refused combined operation committed %d rejection(s) and %d retry attempt(s), want zero effects", rejections, retryAttempts)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != versionBefore {
			t.Fatalf("work version after the refused combined operation = %d, want %d", got, versionBefore)
		}
	})

	t.Run("a rejection without the retry member commits at the same state", func(t *testing.T) {
		fixture, reject := setup(t, "ctx-agg-retry-control")
		defer fixture.store.Close()
		if err := reject(t, false); err != nil {
			t.Fatalf("rejection without the retry member refused: %v", err)
		}
		rejections, retryAttempts := aggregateCommittedRejections(t, fixture.store, fixture.workID)
		if rejections != 1 || retryAttempts != 0 {
			t.Fatalf("committed rejections = %d retry attempts = %d, want the one rejection and no retry", rejections, retryAttempts)
		}
	})
}
