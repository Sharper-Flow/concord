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
func workerCompleteEventV2(workID, eventID, attemptID, model string, origin string, evidence []WorkerReportEvidence, directory string) Event {
	payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: model, ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: directory, Evidence: evidence, EvidenceOrigin: origin}
	return Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(payload)}
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
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[1]
	attemptID := "evidence-covered-attempt"
	authorizeWorkerFixtureDispatch(t, s, "evidence-covered", attemptID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-covered", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	evidence := append(laneCoveringEvidence(lane), WorkerReportEvidence{Obligation: lane.EvidenceObligations[0], Detail: "a second fact for the same obligation"})
	complete := workerCompleteEventV2("evidence-covered", "evidence-covered-complete", attemptID, preferredModelForLane(lane), WorkerEvidenceReported, evidence, workerFixtureDirectory(t, s, "evidence-covered"))
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
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "evidence-undischarged-attempt"
	authorizeWorkerFixtureDispatch(t, s, "evidence-undischarged", attemptID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-undischarged", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	missing := lane.EvidenceObligations[len(lane.EvidenceObligations)-1]
	partial := laneCoveringEvidence(lane)[:len(lane.EvidenceObligations)-1]
	before := workerProjectionSnapshot(t, s)
	beforeEvents := countRows(t, s, "effect_receipts")
	complete := workerCompleteEventV2("evidence-undischarged", "evidence-undischarged-complete", attemptID, preferredModelForLane(lane), WorkerEvidenceReported, partial, workerFixtureDirectory(t, s, "evidence-undischarged"))
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
	if got := countRows(t, s, "effect_receipts"); got != beforeEvents {
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
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[3]
	foreign := BuiltinLaneDefinitions()[0].EvidenceObligations[0]
	attemptID := "evidence-foreign-attempt"
	authorizeWorkerFixtureDispatch(t, s, "evidence-foreign", attemptID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-foreign", attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	evidence := append(laneCoveringEvidence(lane), WorkerReportEvidence{Obligation: foreign, Detail: "an obligation this lane never declared"})
	complete := workerCompleteEventV2("evidence-foreign", "evidence-foreign-complete", attemptID, preferredModelForLane(lane), WorkerEvidenceReported, evidence, workerFixtureDirectory(t, s, "evidence-foreign"))
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
	t.Parallel()
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
			seedWork(t, s, "evidence-shape")
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("evidence-shape", attemptID, lane, nil)}}); err != nil {
				t.Fatal(err)
			}
			complete := workerCompleteEventV2("evidence-shape", "evidence-shape-complete", attemptID, preferredModelForLane(lane), testCase.origin, testCase.evidence, workerFixtureDirectory(t, s, "evidence-shape"))
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

// CD-0056 D2 and D8: every built-in lane declares obligations drawn from the
// closed vocabulary, and the generated digests stay pinned: a manifest change
// moves them only through regeneration. CD-0242 D14 admits only current
// content identities, not historical digest aliases.
func TestBuiltinLaneObligationsAreClosedAndMatchGeneratedDigests(t *testing.T) {
	t.Parallel()
	wantDigests := map[string]string{
		"research":  "sha256:7a5f8b9ca70df1dd58855d4584de6cb12f0a662dea8097a746fd9375bc2b7ea3",
		"implement": "sha256:ff4bca834c3bf4f2fffdf5501ca5e83d866707c0039e62e49c36390763ef9b06",
		"design":    "sha256:a6736729ec64977a3c013306ad44cf3a00f6070801ec73d5adc17bb2668f8aee",
		"review":    "sha256:0da2486ab33fd6d845d0b7f2ad68dfa2cfd54483acc7973d621d005fbc871fed",
		"verify":    "sha256:fa59a8e0c232d6f6cccbff0117278314add4d7d1c34015d7e872756bdc14f150",
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
