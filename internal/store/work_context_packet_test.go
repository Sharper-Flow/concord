package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
	if view != nil {
		inputs["work_context"] = view
	}
	return map[string]any{
		"schema_version": "1.1",
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
