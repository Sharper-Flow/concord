package store

import (
	"bytes"
	"context"
	"encoding/json"
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
	laneVersion, laneDigest := implementLaneIdentity()
	task, binding := recordedPacketInputs(t, f.store, f.workID, "implement")
	inputs := map[string]any{
		"task":        task,
		"binding":     binding,
		"worker_job":  recordedPacketJobForTest(t, f.store, f.workID, job),
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
		"lane_version":   laneVersion,
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
	tampered := *view
	tampered.SubjectCommit = strings.Repeat("b", 40)
	tamperedPacket := workContextDispatchPacket(t, fixture, "attempt-ctx-subject-tampered", job, &tampered)
	if err := dispatchWorkContextAttempt(t, fixture, tamperedPacket); err == nil || !strings.Contains(err.Error(), "does not consume the current work context") {
		t.Fatalf("tampered candidate view error = %v, want the typed consumption refusal", err)
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
// findings the packet admission can consume.
func declareSampleWorkContext(t *testing.T, f workContextFixture, statement string) {
	t.Helper()
	if err := f.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{
			workContextRepoReading(workContextTestRoot, workContextRootRationale),
			workContextKnowledgeReading(workContextTestChild),
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
	job := seedReadyWorkerJob(t, fixture)
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
	if err := ApplyOperation(context.Background(), fixture.store, Operation{Events: []Event{workerCompletedContextFindingsEvent(fixture.workID, "ctx-complete-stale", "attempt-ctx-stale-report", preferredModelForLane(lane), lane, reported, WorkerEvidenceEventPayloadVersion(WorkerCompleted))}}); err != nil {
		t.Fatalf("terminal report refused: %v", err)
	}
	err = dispatchWorkContextAttempt(t, fixture, packet)
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker packet does not consume the current work context") {
		t.Fatalf("stale packet error = %v, want %s refusing the stale work context", err, KindInvalidPayload)
	}
	if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 0 {
		t.Fatalf("dispatch_worker started events = %d, want 0 on refusal", got)
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

// A current view past its bounds leaves the pin readable without the member,
// and the dispatch refuses with the reader's own overflow error, so the
// overflow is never silently dropped or truncated into a packet.
func TestDispatchRefusesWorkContextViewOverflow(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ctx-dispatch-overflow")
	defer fixture.store.Close()
	seedWorkContextOverflow(t, fixture)
	pin, err := ReadWorkPin(context.Background(), fixture.store, fixture.workID)
	if err != nil {
		t.Fatalf("work pin read refused on the overflowed view: %v", err)
	}
	marshaled, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(marshaled, []byte(`"work_context"`)) {
		t.Fatalf("work pin carries work context past the view bound:\n%s", marshaled)
	}
	err = dispatchWorkContextAttempt(t, fixture, workContextDispatchPacket(t, fixture, "attempt-ctx-overflow", seedReadyWorkerJob(t, fixture), nil))
	if !hasFailureKind(err, KindLimitExceeded) {
		t.Fatalf("overflow dispatch error = %v, want %s from the work context reader", err, KindLimitExceeded)
	}
}

// seedWorkContextOverflow drives the work-wide current view past the
// 32-finding bound using declarations alone: three declarations whose
// combined selection and new findings hold 48 distinct findings.
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
	declareSixteen("second")
	// The third declaration selects every earlier finding beside its own
	// sixteen: 32 selected plus 16 new is 48 work-wide, past the view bound.
	var seqs []int64
	rows, err := fixture.store.DatabaseForTesting().Query(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq`, fixture.workID, WorkflowWorkContextRecorded)
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
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	refs := make([]string, 0, 32)
	for _, seq := range seqs {
		for ordinal := 0; ordinal < 16; ordinal++ {
			refs = append(refs, fmt.Sprintf("finding:%d:%d", seq, ordinal))
		}
	}
	if err := fixture.recordWorkContext(t, map[string]any{
		"finding_refs": refs,
		"context_findings": func() []map[string]any {
			findings := make([]map[string]any, 16)
			for i := range findings {
				findings[i] = workContextFinding(workContextTestChild, "observation", "third", "")
			}
			return findings
		}(),
	}); err != nil {
		t.Fatalf("third declaration refused: %v", err)
	}
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
		lane := laneForID(t, "implement")
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
		completion := Event{EventID: "complete-" + workID + "-x", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: fixture.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
			AttemptID: "attempt-reject-retry-x", ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
			EvidenceOrigin: WorkerEvidenceReported, Evidence: workerEvidenceForLane(lane), WorkerJob: &job,
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
		lane := laneForID(t, "implement")
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
		completion := Event{EventID: "complete-c1-x-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: fixture.workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
			AttemptID: "attempt-c1-x", ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
			EvidenceOrigin: WorkerEvidenceReported, Evidence: workerEvidenceForLane(lane), WorkerJob: &job,
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
		preparation, prepErr := PrepareWorkerDispatchFreshness(context.Background(), s, PrepareWorkerDispatchRequest{WorkID: workID, SessionWorktree: driftClaimDir})
		if prepErr != nil {
			t.Fatalf("preparation refused: %v", prepErr)
		}
		job := reviewSubjectReadyRevision(t, workContextFixture{store: s, workID: workID, owner: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}})
		view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
		if viewErr != nil || view == nil {
			t.Fatalf("admitted view = %+v err = %v, want a present view", view, viewErr)
		}
		owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
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
