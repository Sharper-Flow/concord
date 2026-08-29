package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// workerCompleteEventV2 builds a current-version completion carrying reported
// evidence exactly as the adapter would after parsing agent-lane-report.v1.
func workerCompleteEventV2(workID, eventID, attemptID, model string, origin string, evidence []WorkerReportEvidence) Event {
	payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: model, ReportSchemaVersion: WorkerReportSchemaVersion, Evidence: evidence, EvidenceOrigin: origin}
	return Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(payload)}
}

// laneCoveringEvidence discharges every obligation the lane declares, once.
func laneCoveringEvidence(lane LaneDefinition) []WorkerReportEvidence {
	evidence := make([]WorkerReportEvidence, 0, len(lane.EvidenceObligations))
	for _, obligation := range lane.EvidenceObligations {
		evidence = append(evidence, WorkerReportEvidence{Obligation: obligation, Detail: "discharged " + obligation})
	}
	return evidence
}

func failureDetail(t *testing.T, err error) string {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error %v is not a typed store failure", err)
	}
	return failure.Detail
}

// CD-0056 D4: a completion whose evidence covers its lane's declared
// obligations folds, and the discharged evidence stays durable in the event.
func TestWorkerCompletionCoveringLaneObligationsFoldsAndRetainsEvidence(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[1]
	attemptID := "evidence-covered-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-covered", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	evidence := append(laneCoveringEvidence(lane), WorkerReportEvidence{Obligation: lane.EvidenceObligations[0], Detail: "a second fact for the same obligation"})
	complete := workerCompleteEventV2("evidence-covered", "evidence-covered-complete", attemptID, preferredModelForLane(lane), WorkerEvidenceReported, evidence)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{complete}}); err != nil {
		t.Fatalf("covered completion was refused: %v", err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("lifecycle_state = %q, want completed", state)
	}
	var stored []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, "evidence-covered-complete").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var durable WorkerCompletedPayload
	if err := json.Unmarshal(stored, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.EvidenceOrigin != WorkerEvidenceReported || len(durable.Evidence) != len(evidence) {
		t.Fatalf("durable evidence = %s/%d entries, want reported/%d", durable.EvidenceOrigin, len(durable.Evidence), len(evidence))
	}
}

// CD-0056 D4: an undischarged obligation is not a completion. The refusal
// names the missing obligation and leaves the attempt row untouched.
func TestWorkerCompletionLeavingObligationUndischargedIsRefusedAndProjectionUnchanged(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "evidence-undischarged-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-undischarged", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	missing := lane.EvidenceObligations[len(lane.EvidenceObligations)-1]
	partial := laneCoveringEvidence(lane)[:len(lane.EvidenceObligations)-1]
	before := workerProjectionSnapshot(t, s)
	beforeEvents := countRows(t, s, "domain_events")
	complete := workerCompleteEventV2("evidence-undischarged", "evidence-undischarged-complete", attemptID, preferredModelForLane(lane), WorkerEvidenceReported, partial)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{complete}})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("undischarged completion error = %v, want %s", err, KindInvalidPayload)
	}
	if detail := failureDetail(t, err); !strings.Contains(detail, missing) {
		t.Fatalf("failure detail = %q, want it to name the undischarged obligation %q", detail, missing)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("refused completion changed the worker projection:\n%s\nwant\n%s", after, before)
	}
	if got := countRows(t, s, "domain_events"); got != beforeEvents {
		t.Fatalf("refused completion event count = %d, want %d", got, beforeEvents)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "dispatched" {
		t.Fatalf("lifecycle_state = %q, want dispatched", state)
	}
}

// CD-0056 D4: a report may not name an obligation its dispatching lane does
// not declare. The vocabulary is closed, but membership is not authority.
func TestWorkerCompletionNamingObligationOutsideItsLaneIsRefused(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[3]
	foreign := BuiltinLaneDefinitions()[0].EvidenceObligations[0]
	attemptID := "evidence-foreign-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-foreign", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	evidence := append(laneCoveringEvidence(lane), WorkerReportEvidence{Obligation: foreign, Detail: "an obligation this lane never declared"})
	complete := workerCompleteEventV2("evidence-foreign", "evidence-foreign-complete", attemptID, preferredModelForLane(lane), WorkerEvidenceReported, evidence)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{complete}})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("foreign obligation error = %v, want %s", err, KindInvalidPayload)
	}
	if detail := failureDetail(t, err); !strings.Contains(detail, foreign) {
		t.Fatalf("failure detail = %q, want it to name the undeclared obligation %q", detail, foreign)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "dispatched" {
		t.Fatalf("lifecycle_state = %q, want dispatched", state)
	}
}

// CD-0056 D6: evidence shape is total in both directions. Neither a missing
// origin, nor a reported completion with nothing to report, nor a legacy
// completion carrying evidence, nor an obligation outside the closed
// vocabulary is a valid payload.
func TestWorkerCompletionEvidenceShapeIsClosedInBothDirections(t *testing.T) {
	lane := BuiltinLaneDefinitions()[1]
	covered := laneCoveringEvidence(lane)
	tests := []struct {
		name     string
		origin   string
		evidence []WorkerReportEvidence
	}{
		{name: "absent origin", origin: "", evidence: covered},
		{name: "unknown origin", origin: "assumed", evidence: covered},
		{name: "reported without evidence", origin: WorkerEvidenceReported},
		{name: "legacy with evidence", origin: WorkerEvidenceLegacyUnavailable, evidence: covered},
		{name: "obligation outside the vocabulary", origin: WorkerEvidenceReported, evidence: []WorkerReportEvidence{{Obligation: "vibes", Detail: "not a declared obligation"}}},
		{name: "empty detail", origin: WorkerEvidenceReported, evidence: []WorkerReportEvidence{{Obligation: covered[0].Obligation, Detail: ""}}},
		{name: "duplicate obligation and detail pair", origin: WorkerEvidenceReported, evidence: []WorkerReportEvidence{covered[0], covered[0]}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTemp(t)
			attemptID := "evidence-shape-attempt"
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-shape", attemptID, lane, nil)}}); err != nil {
				t.Fatal(err)
			}
			complete := workerCompleteEventV2("evidence-shape", "evidence-shape-complete", attemptID, preferredModelForLane(lane), testCase.origin, testCase.evidence)
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{complete}}); !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("shape error = %v, want %s", err, KindInvalidPayload)
			}
			if got := countRows(t, s, "worker_attempts"); got != 1 {
				t.Fatalf("worker attempt rows = %d, want 1", got)
			}
			var state string
			if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != "dispatched" {
				t.Fatalf("lifecycle_state = %q, want dispatched", state)
			}
		})
	}
}

// CD-0056 D6: a stored v1 completion carries no evidence and none can be
// invented, so it upcasts to a visibly legacy v2 payload, skips coverage, and
// replays to the same projection.
func TestWorkerCompletedV1UpcastsToLegacyUnavailableAndReplaysIdentically(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "evidence-legacy-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-legacy", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	legacy := workerCompleteEvent("evidence-legacy", "evidence-legacy-complete", attemptID, preferredModelForLane(lane))
	if legacy.PayloadVersion != 1 {
		t.Fatalf("fixture payload version = %d, want 1", legacy.PayloadVersion)
	}
	upcast, err := upcastWorkerCompletedV1(legacy)
	if err != nil {
		t.Fatalf("upcastWorkerCompletedV1 error = %v", err)
	}
	if upcast.PayloadVersion != 2 {
		t.Fatalf("upcast payload version = %d, want 2", upcast.PayloadVersion)
	}
	var upcastPayload WorkerCompletedPayload
	if err := json.Unmarshal(upcast.Payload, &upcastPayload); err != nil {
		t.Fatal(err)
	}
	if upcastPayload.EvidenceOrigin != WorkerEvidenceLegacyUnavailable || len(upcastPayload.Evidence) != 0 {
		t.Fatalf("upcast evidence = %s/%d entries, want %s/0", upcastPayload.EvidenceOrigin, len(upcastPayload.Evidence), WorkerEvidenceLegacyUnavailable)
	}
	again, err := upcastWorkerCompletedV1(legacy)
	if err != nil {
		t.Fatalf("second upcast error = %v", err)
	}
	if string(again.Payload) != string(upcast.Payload) {
		t.Fatalf("upcast is not deterministic:\n%s\n%s", again.Payload, upcast.Payload)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{legacy}}); err != nil {
		t.Fatalf("legacy completion was refused: %v", err)
	}
	before := workerProjectionSnapshot(t, s)
	if !strings.Contains(before, "|completed|") {
		t.Fatalf("legacy completion projection = %q, want a completed attempt", before)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("replay changed the worker projection:\n%s\nwant\n%s", after, before)
	}
}

// CD-0056 D2 and D8: every built-in lane declares obligations drawn from the
// closed vocabulary, and binding evidence to that vocabulary changes no lane
// digest.
func TestBuiltinLaneObligationsAreClosedAndLaneDigestsAreUnchanged(t *testing.T) {
	wantDigests := map[string]string{
		"research":  "sha256:3969ceda54cc6be1532877e6d5b1dc5530c280ff77835f43286c4a0ad37e861b",
		"implement": "sha256:ec541caf3d4df2d5fe70602cf65e747f19e5ac525b001fdd86ea7cf921b737fc",
		"design":    "sha256:50b73594e743bf14dc4ba2fdd8294bb7de64f695ea571a2fff572b5329c649a5",
		"review":    "sha256:49d6fac9d7ebcb95915dd3021e6e2cbd151a569a56221930c0d7a94232736e15",
		"verify":    "sha256:7999bab09a266d4e5bcda060e0cc75786f7c0678acbde09df7f30dd19fd9eff2",
	}
	definitions := BuiltinLaneDefinitions()
	if len(definitions) != len(wantDigests) {
		t.Fatalf("lane count = %d, want %d", len(definitions), len(wantDigests))
	}
	for _, definition := range definitions {
		for _, obligation := range definition.EvidenceObligations {
			if !ValidLaneEvidenceObligation(obligation) {
				t.Fatalf("lane %s declares obligation %q outside the closed vocabulary", definition.ID, obligation)
			}
		}
		if definition.Digest != wantDigests[definition.ID] {
			t.Fatalf("lane %s digest = %s, want %s", definition.ID, definition.Digest, wantDigests[definition.ID])
		}
	}
	if len(laneEvidenceObligationVocabulary) != 12 {
		t.Fatalf("obligation vocabulary size = %d, want 12", len(laneEvidenceObligationVocabulary))
	}
	invalid := definitions[0]
	invalid.EvidenceObligations = []string{"not_an_obligation"}
	if err := ValidateLaneDefinition(invalid); !hasFailureKind(err, KindLaneDefinitionInvalid) {
		t.Fatalf("undeclared obligation error = %v, want %s", err, KindLaneDefinitionInvalid)
	}
}
