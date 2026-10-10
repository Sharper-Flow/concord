package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// CON-887 typed terminal-report retention. The optional context_findings
// array rides the worker.completed and worker.failed terminal payloads as
// worker-claimed content: a finding records no acceptance, joins no
// obligation vocabulary, and its subject_ref is the worker's claim about
// what the finding concerns — never dispatch-owned subject identity.

// contextFindingFixture is one fully-admissible finding with byte-exact
// field content a test can override per case.
func contextFindingFixture() WorkerContextFinding {
	return WorkerContextFinding{
		Kind:         "observation",
		Statement:    "the bounded read is the only admission route",
		SubjectRef:   "internal/store/worker_lanes.go",
		EvidenceRefs: []string{"internal/store/worker_lanes_test.go"},
		DomainID:     workContextTestChild,
	}
}

func TestValidateWorkerContextFindingsClosedShape(t *testing.T) {
	t.Parallel()
	kinds := []string{"observation", "inference", "hypothesis", "rejected_approach", "open_question", "contradiction", "direction"}
	// Absent and empty arrays are both legal on both terminal statuses.
	if err := ValidateWorkerContextFindings(nil); err != nil {
		t.Fatalf("absent findings refused: %v", err)
	}
	if err := ValidateWorkerContextFindings([]WorkerContextFinding{}); err != nil {
		t.Fatalf("empty findings refused: %v", err)
	}
	// Every closed kind admits, and sixteen entries stay inside the count.
	allKinds := make([]WorkerContextFinding, 0, len(kinds))
	for _, kind := range kinds {
		allKinds = append(allKinds, WorkerContextFinding{Kind: kind, Statement: "s", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"})
	}
	if err := ValidateWorkerContextFindings(allKinds); err != nil {
		t.Fatalf("every closed kind refused: %v", err)
	}
	// Byte bounds hold for multi-byte UTF-8, not code points.
	if err := ValidateWorkerContextFindings([]WorkerContextFinding{{Kind: "observation", Statement: strings.Repeat("é", 512), SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}); err != nil {
		t.Fatalf("1024-byte multi-byte statement refused: %v", err)
	}
	if err := ValidateWorkerContextFindings([]WorkerContextFinding{{Kind: "observation", Statement: strings.Repeat("🎉", 256), SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}); err != nil {
		t.Fatalf("1024-byte astral statement refused: %v", err)
	}
	fullRefs := make([]string, 8)
	for i := range fullRefs {
		fullRefs[i] = strings.Repeat("r", 256)
	}
	if err := ValidateWorkerContextFindings([]WorkerContextFinding{{Kind: "direction", Statement: "s", SubjectRef: strings.Repeat("s", 128), EvidenceRefs: fullRefs, DomainID: strings.Repeat("d", 256)}}); err != nil {
		t.Fatalf("at-bound subject_ref and evidence refs refused: %v", err)
	}

	refusals := []struct {
		name     string
		findings []WorkerContextFinding
	}{
		{"seventeen entries", func() []WorkerContextFinding {
			entries := make([]WorkerContextFinding, 17)
			for i := range entries {
				entries[i] = contextFindingFixture()
			}
			return entries
		}()},
		{"kind outside the closed vocabulary", []WorkerContextFinding{{Kind: "vibes", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"empty statement", []WorkerContextFinding{{Kind: "observation", Statement: "", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"statement past 1024 ASCII bytes", []WorkerContextFinding{{Kind: "observation", Statement: strings.Repeat("x", 1025), SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"statement past 1024 UTF-8 bytes in fewer code points", []WorkerContextFinding{{Kind: "observation", Statement: strings.Repeat("é", 513), SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"statement past bytes with astral characters", []WorkerContextFinding{{Kind: "observation", Statement: strings.Repeat("🎉", 257), SubjectRef: "r", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"empty subject_ref", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "", EvidenceRefs: []string{}, DomainID: "d"}}},
		{"subject_ref past 128 bytes", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: strings.Repeat("s", 129), EvidenceRefs: []string{}}}},
		{"subject_ref past 128 UTF-8 bytes", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: strings.Repeat("é", 65), EvidenceRefs: []string{}}}},
		{"absent evidence_refs array", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: nil, DomainID: "d"}}},
		{"nine evidence refs", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}, DomainID: "d"}}},
		{"empty evidence ref", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{""}, DomainID: "d"}}},
		{"evidence ref past 256 bytes", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{strings.Repeat("r", 257)}, DomainID: "d"}}},
		{"empty domain_id", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: ""}}},
		{"domain_id past 256 bytes", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: strings.Repeat("d", 257)}}},
		{"evidence ref past 256 UTF-8 bytes", []WorkerContextFinding{{Kind: "observation", Statement: "s", SubjectRef: "r", EvidenceRefs: []string{strings.Repeat("é", 129)}, DomainID: "d"}}},
	}
	for _, refusal := range refusals {
		if err := ValidateWorkerContextFindings(refusal.findings); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("%s error = %v, want %s", refusal.name, err, KindInvalidPayload)
		}
	}
}

// The aggregate bound is the compact JSON serialization of the whole array.
// An over-bound array is refused whole; a finding is never truncated to fit.
func TestValidateWorkerContextFindingsAggregateBytesRefuseNeverTruncate(t *testing.T) {
	t.Parallel()
	atBound := make([]WorkerContextFinding, 12)
	for i := range atBound {
		atBound[i] = WorkerContextFinding{
			Kind:         "observation",
			Statement:    "finding " + strings.Repeat(string(rune('a'+i%26)), 1) + ": " + strings.Repeat("x", 1010),
			SubjectRef:   strings.Repeat("s", 128),
			EvidenceRefs: []string{},
			DomainID:     "d",
		}
	}
	encoded, err := json.Marshal(atBound)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > WorkerContextFindingsMaxArrayBytes {
		t.Fatalf("fixture serialized to %d bytes; the at-bound case must fit %d", len(encoded), WorkerContextFindingsMaxArrayBytes)
	}
	if err := ValidateWorkerContextFindings(atBound); err != nil {
		t.Fatalf("at-bound findings refused: %v", err)
	}

	over := make([]WorkerContextFinding, 16)
	for i := range over {
		over[i] = WorkerContextFinding{
			Kind:         "observation",
			Statement:    "finding " + strings.Repeat(string(rune('a'+i%26)), 1) + ": " + strings.Repeat("x", 1010),
			SubjectRef:   strings.Repeat("s", 128),
			EvidenceRefs: []string{},
			DomainID:     "d",
		}
	}
	before, err := json.Marshal(over)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) <= WorkerContextFindingsMaxArrayBytes {
		t.Fatalf("fixture serialized to %d bytes; the over-bound case must exceed %d", len(before), WorkerContextFindingsMaxArrayBytes)
	}
	if err := ValidateWorkerContextFindings(over); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("over-bound findings error = %v, want %s", err, KindInvalidPayload)
	}
	after, err := json.Marshal(over)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("the aggregate refusal rewrote or truncated the findings")
	}
}

// workerCompletedContextFindingsEvent builds one current-version completion
// event whose payload carries the supplied findings beside full reported
// evidence for the lane.
func workerCompletedContextFindingsEvent(workID, eventID, attemptID, model string, lane LaneDefinition, findings []WorkerContextFinding, version int) Event {
	evidence := make([]WorkerReportEvidence, 0, len(lane.EvidenceObligations))
	for _, obligation := range lane.EvidenceObligations {
		evidence = append(evidence, WorkerReportEvidence{Obligation: obligation, Detail: "discharged " + obligation})
	}
	return Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: version, Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: model, ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceReported, Evidence: evidence, ContextFindings: findings,
	})}
}

func workerFailedContextFindingsEvent(workID, eventID, attemptID, model, failureKind string, findings []WorkerContextFinding, version int) Event {
	return Event{EventID: eventID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: version, Payload: mustJSONValue(WorkerFailedPayload{
		AttemptID: attemptID, ReadbackModel: model, FailureKind: failureKind, Detail: "bounded worker error", ContextFindings: findings,
	})}
}

// TestWorkerTerminalEventsAdmitTypedContextFindings is the CON-887 retention
// admission: both terminal statuses at their current registry versions carry
// the optional typed findings, the fold takes the attempt terminal, and the
// stored event payload retains the findings bytes verbatim.
func TestWorkerTerminalEventsAdmitTypedContextFindings(t *testing.T) {
	t.Parallel()
	if got := WorkerEvidenceEventPayloadVersion(WorkerCompleted); got != 6 {
		t.Fatalf("worker.completed current payload version = %d, want 6", got)
	}
	if got := WorkerEvidenceEventPayloadVersion(WorkerFailed); got != 2 {
		t.Fatalf("worker.failed current payload version = %d, want 2", got)
	}
	fixture := seedWorkContextFixture(t, "work-findings-admit")
	defer fixture.store.Close()
	s := fixture.store
	lane := BuiltinLaneDefinitions()[0]
	model := preferredModelForLane(lane)
	findings := []WorkerContextFinding{contextFindingFixture(), {Kind: "direction", Statement: "the reader joins declarations without parsing narrative", SubjectRef: "internal/store/fold.go", EvidenceRefs: []string{"a", "b"}, DomainID: workContextTestRoot, ProductWideRationale: workContextRootRationale}}

	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(fixture.workID, "attempt-findings-complete", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	completion := workerCompletedContextFindingsEvent(fixture.workID, "complete-findings", "attempt-findings-complete", model, lane, findings, WorkerEvidenceEventPayloadVersion(WorkerCompleted))
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completion}}); err != nil {
		t.Fatalf("completion with typed findings refused: %v", err)
	}

	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(fixture.workID, "attempt-findings-failed", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	failure := workerFailedContextFindingsEvent(fixture.workID, "failed-findings", "attempt-findings-failed", model, WorkerFailureWorkerError, findings, WorkerEvidenceEventPayloadVersion(WorkerFailed))
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{failure}}); err != nil {
		t.Fatalf("worker_error failure with typed findings refused: %v", err)
	}

	for _, row := range []struct{ attempt, want string }{{"attempt-findings-complete", "completed"}, {"attempt-findings-failed", "failed"}} {
		var state string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, row.attempt).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != row.want {
			t.Fatalf("attempt %s lifecycle_state = %q, want %q", row.attempt, state, row.want)
		}
	}
	var storedFindings string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.context_findings') FROM domain_events WHERE event_id=?`, "complete-findings").Scan(&storedFindings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(storedFindings, "the bounded read is the only admission route") {
		t.Fatalf("stored completion findings missing: %s", storedFindings)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.context_findings') FROM domain_events WHERE event_id=?`, "failed-findings").Scan(&storedFindings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(storedFindings, "internal/store/fold.go") {
		t.Fatalf("stored failure findings missing: %s", storedFindings)
	}
}

// Findings are claims, not acceptance: a completion carrying findings must
// still discharge every declared obligation, and a findings entry cannot
// discharge one.
func TestWorkerCompletedFindingsAreClaimsNotObligationDischarge(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "work-findings-claims")
	defer fixture.store.Close()
	s := fixture.store
	lane := BuiltinLaneDefinitions()[0]
	model := preferredModelForLane(lane)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("work-findings-claims", "attempt-findings-claims", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	short := workerCompletedContextFindingsEvent("work-findings-claims", "complete-findings-short", "attempt-findings-claims", model, lane, []WorkerContextFinding{contextFindingFixture()}, WorkerEvidenceEventPayloadVersion(WorkerCompleted))
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(short.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Evidence = payload.Evidence[:1]
	short.Payload = mustJSONValue(payload)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{short}})
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "undischarged") {
		t.Fatalf("findings beside undischarged obligations error = %v, want an undischarged-obligation refusal", err)
	}
}

// Only the worker-reported failure kind retains findings. The diagnostic and
// host failure kinds — invalid_report, readback, fallback_blocked, and
// abandonment — never carry worker claims, and an absent or empty array
// stays valid on every kind.
func TestWorkerFailedFindingsReservedForWorkerError(t *testing.T) {
	t.Parallel()
	findings := []WorkerContextFinding{contextFindingFixture()}
	empty := []WorkerContextFinding{}
	for _, kind := range []string{WorkerFailureInvalidReport, WorkerFailureFallbackBlocked, WorkerFailureModelIdentity, WorkerFailureModelReadbackMissing, WorkerFailureModelReadbackAmbiguous} {
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("work-fail-kind", "attempt-fail-kind", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		readback := preferredModelForLane(lane)
		if modelReadbackFailureKind(kind) {
			readback = ""
		}
		event := workerFailedContextFindingsEvent("work-fail-kind", "failed-kind-"+kind, "attempt-fail-kind", readback, kind, findings, WorkerEvidenceEventPayloadVersion(WorkerFailed))
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
		if !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("%s with findings error = %v, want %s", kind, err, KindInvalidPayload)
		}
		// An empty array on the same diagnostic kind stays valid.
		emptyEvent := workerFailedContextFindingsEvent("work-fail-kind", "failed-kind-empty-"+kind, "attempt-fail-kind", readback, kind, empty, WorkerEvidenceEventPayloadVersion(WorkerFailed))
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{emptyEvent}}); err != nil {
			t.Fatalf("%s with an empty findings array refused: %v", kind, err)
		}
	}
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("work-fail-abandoned", "attempt-fail-abandoned", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	abandoned := workerFailedContextFindingsEvent("work-fail-abandoned", "failed-abandoned-findings", "attempt-fail-abandoned", preferredModelForLane(lane), WorkerFailureAbandoned, findings, WorkerEvidenceEventPayloadVersion(WorkerFailed))
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{abandoned}}); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("abandoned with findings error = %v, want %s", err, KindInvalidPayload)
	}
}

// The legacy upcasters are deterministic and legacy-preserving: a stored
// completion v4 and failure v1 — the shapes earlier binaries recorded —
// upcast with their bytes unchanged, fold, and rebuild to the same
// projection, while findings bytes on a below-boundary replayed event are
// refused as fabricated.
func TestWorkerTerminalContextFindingsUpcastAndRebuildDeterministic(t *testing.T) {
	t.Parallel()
	t.Run("upcasters keep legacy bytes unchanged", func(t *testing.T) {
		legacyCompletion := Event{EventID: "legacy-complete-v4", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "work-legacy", Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 4, Payload: mustJSONValue(map[string]any{"attempt_id": "attempt-legacy", "readback_model": "openai/gpt-5.6-luna", "report_schema_version": WorkerReportSchemaVersionLegacy})}
		upcast, err := upcastEvent(legacyCompletion)
		if err != nil {
			t.Fatal(err)
		}
		if upcast.PayloadVersion != 6 {
			t.Fatalf("upcast completion version = %d, want 6", upcast.PayloadVersion)
		}
		if string(upcast.Payload) != string(legacyCompletion.Payload) {
			t.Fatalf("v4 completion upcast rewrote bytes:\n%s\nwant\n%s", upcast.Payload, legacyCompletion.Payload)
		}
		legacyFailure := Event{EventID: "legacy-failed-v1", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: "work-legacy", Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(map[string]any{"attempt_id": "attempt-legacy", "readback_model": "openai/gpt-5.6-luna", "failure_kind": WorkerFailureWorkerError, "detail": "bounded worker error"})}
		upcast, err = upcastEvent(legacyFailure)
		if err != nil {
			t.Fatal(err)
		}
		if upcast.PayloadVersion != 2 {
			t.Fatalf("upcast failure version = %d, want 2", upcast.PayloadVersion)
		}
		if string(upcast.Payload) != string(legacyFailure.Payload) {
			t.Fatalf("v1 failure upcast rewrote bytes:\n%s\nwant\n%s", upcast.Payload, legacyFailure.Payload)
		}
	})
	t.Run("legacy events fold and rebuild to the same projection", func(t *testing.T) {
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[0]
		model := preferredModelForLane(lane)
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("work-rebuild", "attempt-rebuild", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		legacyCompletion := Event{EventID: "rebuild-complete-v4", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "work-rebuild", Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 4, Payload: mustJSONValue(map[string]any{"attempt_id": "attempt-rebuild", "readback_model": model, "report_schema_version": WorkerReportSchemaVersion, "evidence_origin": WorkerEvidenceLegacyUnavailable})}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{legacyCompletion}}); err != nil {
			t.Fatalf("legacy v4 completion refused on append: %v", err)
		}
		before := workerProjectionSnapshot(t, s)
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if after := workerProjectionSnapshot(t, s); after != before {
			t.Fatalf("rebuild changed the legacy completion projection:\n%s\nwant\n%s", after, before)
		}
	})
	t.Run("current-version findings fold and rebuild identically", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "work-rebuild-findings")
		defer fixture.store.Close()
		s := fixture.store
		lane := BuiltinLaneDefinitions()[0]
		model := preferredModelForLane(lane)
		findings := []WorkerContextFinding{contextFindingFixture()}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("work-rebuild-findings", "attempt-rebuild-findings", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompletedContextFindingsEvent("work-rebuild-findings", "rebuild-complete-findings", "attempt-rebuild-findings", model, lane, findings, WorkerEvidenceEventPayloadVersion(WorkerCompleted))}}); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("work-rebuild-findings", "attempt-rebuild-findings-f", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerFailedContextFindingsEvent("work-rebuild-findings", "rebuild-failed-findings", "attempt-rebuild-findings-f", model, WorkerFailureWorkerError, findings, WorkerEvidenceEventPayloadVersion(WorkerFailed))}}); err != nil {
			t.Fatal(err)
		}
		before := workerProjectionSnapshot(t, s)
		var storedCompletion, storedFailure string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id='rebuild-complete-findings'`).Scan(&storedCompletion); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id='rebuild-failed-findings'`).Scan(&storedFailure); err != nil {
			t.Fatal(err)
		}
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if after := workerProjectionSnapshot(t, s); after != before {
			t.Fatalf("rebuild changed the findings projection:\n%s\nwant\n%s", after, before)
		}
		var afterCompletion, afterFailure string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id='rebuild-complete-findings'`).Scan(&afterCompletion); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id='rebuild-failed-findings'`).Scan(&afterFailure); err != nil {
			t.Fatal(err)
		}
		if afterCompletion != storedCompletion || afterFailure != storedFailure {
			t.Fatal("rebuild rewrote the stored terminal payload bytes")
		}
	})
	t.Run("findings bytes on a below-boundary replayed completion refuse", func(t *testing.T) {
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[0]
		model := preferredModelForLane(lane)
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("work-fabricated", "attempt-fabricated", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		fabricated := workerCompletedContextFindingsEvent("work-fabricated", "fabricated-complete", "attempt-fabricated", model, lane, []WorkerContextFinding{contextFindingFixture()}, 4)
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{fabricated}})
		if !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("fabricated v4 findings error = %v, want %s", err, KindInvalidPayload)
		}
	})
	t.Run("findings bytes on a below-boundary replayed failure refuse", func(t *testing.T) {
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("work-fabricated-f", "attempt-fabricated-f", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		fabricated := workerFailedContextFindingsEvent("work-fabricated-f", "fabricated-failed", "attempt-fabricated-f", preferredModelForLane(lane), WorkerFailureWorkerError, []WorkerContextFinding{contextFindingFixture()}, 1)
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{fabricated}})
		if !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("fabricated v1 findings error = %v, want %s", err, KindInvalidPayload)
		}
	})
}
