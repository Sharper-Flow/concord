package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// reviewBlockFixture is one well-formed typed review block, exactly as an
// adapter would forward it after admitting agent-lane-report.v1 (CD-0197).
func reviewBlockFixture() *WorkerReviewBlock {
	return &WorkerReviewBlock{
		Verdict: "no_ship",
		Findings: []WorkerReviewFinding{
			{Severity: "P1", Confidence: "high", Detail: "the pinned digest is not checked at the admission boundary"},
			{Severity: "P2", Confidence: "low", Detail: "the doc names two obligations for one field"},
		},
	}
}

// reviewCompleteEventV3 builds a current-version completion carrying reported
// evidence and the optional typed review block. A completion that carries the
// block discharges severity through it, so its evidence covers the lane
// without a separate severity entry (CD-0197 D2); a blockless completion
// carries the older covering evidence. The event id and the attempt identity
// are the same value, matching the workerDispatchEvent fixture.
func reviewCompleteEventV3(workID, attemptID string, lane LaneDefinition, review *WorkerReviewBlock) Event {
	evidence := laneCoveringEvidence(lane)
	if review != nil {
		withoutSeverity := make([]WorkerReportEvidence, 0, len(evidence))
		for _, entry := range evidence {
			if entry.Obligation == "severity" {
				continue
			}
			withoutSeverity = append(withoutSeverity, entry)
		}
		evidence = withoutSeverity
	}
	payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, Evidence: evidence, EvidenceOrigin: WorkerEvidenceReported, Review: review}
	return Event{EventID: attemptID + "-complete", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 3, Payload: mustJSONValue(payload)}
}

// The review lane requires the typed review block (CD-0197): a live
// completion without it is refused by the fold with the attempt left
// dispatched, and a completion carrying a valid block folds and records the
// block as reported.
func TestReviewLaneCompletionRequiresTheTypedReviewBlock(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[3]
	if lane.ID != "review" {
		t.Fatalf("fixture lane = %s, want review", lane.ID)
	}
	workID := "review-block-required"
	attemptID := "review-block-required-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	before := workerProjectionSnapshot(t, s)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, nil)}})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("blockless completion error = %v, want %s", err, KindInvalidPayload)
	}
	if detail := failureDetail(t, err); !strings.Contains(detail, "typed review block the review lane requires") {
		t.Fatalf("failure detail = %q, want it to name the required block", detail)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("refused completion changed the worker projection:\n%s\nwant\n%s", after, before)
	}
	// A fresh attempt carrying the valid block folds, and the block is
	// durable in the stored payload exactly as reported.
	second := "review-block-required-attempt-2"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, second, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, second, lane, reviewBlockFixture())}}); err != nil {
		t.Fatalf("completion carrying the typed review block was refused: %v", err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, second).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("lifecycle_state = %q, want completed", state)
	}
	var stored []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, second+"-complete").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var durable WorkerCompletedPayload
	if err := json.Unmarshal(stored, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.Review == nil || durable.Review.Verdict != "no_ship" || len(durable.Review.Findings) != 2 {
		t.Fatalf("durable review = %+v, want the reported block recorded as reported", durable.Review)
	}
	if durable.Review.Findings[0].Severity != "P1" || durable.Review.Findings[0].Confidence != "high" {
		t.Fatalf("durable finding = %+v, want the reported severity and confidence recorded", durable.Review.Findings[0])
	}
}

// A lane that requires no report block completes without one, exactly as
// before CD-0197.
func TestNonReviewLaneCompletionWithoutReviewBlockIsUnchanged(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[1]
	if len(lane.RequiredReportBlocks) != 0 {
		t.Fatalf("lane %s requires %v, want none", lane.ID, lane.RequiredReportBlocks)
	}
	workID := "no-review-block"
	attemptID := "no-review-block-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatalf("completion without a review block was refused on a lane that requires none: %v", err)
	}
	var stored []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, attemptID+"-complete").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), `"review":`) {
		t.Fatalf("payload without a review block carries the field anyway: %s", stored)
	}
}

// The review block is closed in both directions at the store boundary: the
// verdict couplings, the closed severity and confidence vocabularies, and the
// detail bound are all checked before an event can land, and a refusal leaves
// the attempt dispatched.
func TestWorkerCompletionReviewBlockShapeIsClosed(t *testing.T) {
	t.Parallel()
	lane := BuiltinLaneDefinitions()[3]
	tooMany := &WorkerReviewBlock{Verdict: "ship", Findings: make([]WorkerReviewFinding, 65)}
	for index := range tooMany.Findings {
		tooMany.Findings[index] = WorkerReviewFinding{Severity: "P3", Confidence: "low", Detail: "a finding"}
	}
	largest := &WorkerReviewBlock{Verdict: "ship"}
	for index := 0; index < 64; index++ {
		largest.Findings = append(largest.Findings, WorkerReviewFinding{Severity: "P3", Confidence: "medium", Detail: strings.Repeat("x", 512)})
	}
	tests := []struct {
		name       string
		review     *WorkerReviewBlock
		wantDetail string
	}{
		{name: "verdict outside the closed set", review: &WorkerReviewBlock{Verdict: "conditional", Findings: []WorkerReviewFinding{{Severity: "P1", Confidence: "low", Detail: "a finding"}}}, wantDetail: "ship or no_ship"},
		{name: "findings array absent", review: &WorkerReviewBlock{Verdict: "ship"}, wantDetail: "findings array"},
		{name: "more than 64 findings", review: tooMany, wantDetail: "at most 64 findings"},
		{name: "no_ship with zero findings", review: &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{}}, wantDetail: "at least one finding"},
		{name: "ship with a P0 finding", review: &WorkerReviewBlock{Verdict: "ship", Findings: []WorkerReviewFinding{{Severity: "P0", Confidence: "high", Detail: "a blocker"}, {Severity: "P2", Confidence: "low", Detail: "a finding"}}}, wantDetail: "cannot carry a P0 finding"},
		{name: "severity outside the closed scale", review: &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{Severity: "S1", Confidence: "low", Detail: "a finding"}}}, wantDetail: "P0, P1, P2, or P3"},
		{name: "confidence outside the closed scale", review: &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{Severity: "P1", Confidence: "certain", Detail: "a finding"}}}, wantDetail: "low, medium, or high"},
		{name: "empty finding detail", review: &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{Severity: "P1", Confidence: "low", Detail: ""}}}, wantDetail: "between 1 and 512 UTF-8 bytes"},
		{name: "oversized finding detail", review: &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{Severity: "P1", Confidence: "low", Detail: strings.Repeat("x", 513)}}}, wantDetail: "between 1 and 512 UTF-8 bytes"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTemp(t)
			workID := "review-block-shape"
			attemptID := "review-block-shape-attempt"
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
				t.Fatal(err)
			}
			err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, testCase.review)}})
			if !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("shape error = %v, want %s", err, KindInvalidPayload)
			}
			if detail := failureDetail(t, err); !strings.Contains(detail, testCase.wantDetail) {
				t.Fatalf("failure detail = %q, want it to name %q", detail, testCase.wantDetail)
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
	// The store admits every size the report schema admits: 64 findings with
	// 512-byte details is the largest review block the schema allows.
	s := openTemp(t)
	workID := "review-block-bound"
	attemptID := "review-block-bound-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, largest)}}); err != nil {
		t.Fatalf("completion at the schema bound was refused: %v", err)
	}
}

// CD-0197 D2: severity has one source. A completion that carries the typed
// review block is refused when it also reports a free-text severity entry,
// and the refused completion leaves the attempt dispatched.
func TestReviewLaneCompletionRefusesAFreeTextSeverityEntryBesideTheBlock(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[3]
	if lane.ID != "review" {
		t.Fatalf("fixture lane = %s, want review", lane.ID)
	}
	workID := "review-severity-one-source"
	attemptID := "review-severity-one-source-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	before := workerProjectionSnapshot(t, s)
	payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, Evidence: laneCoveringEvidence(lane), EvidenceOrigin: WorkerEvidenceReported, Review: reviewBlockFixture()}
	complete := Event{EventID: attemptID + "-complete", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 3, Payload: mustJSONValue(payload)}
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{complete}})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("severity-beside-block completion error = %v, want %s", err, KindInvalidPayload)
	}
	if detail := failureDetail(t, err); !strings.Contains(detail, "free-text severity entry beside the typed review block") {
		t.Fatalf("failure detail = %q, want it to name the one-source rule", detail)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("refused completion changed the worker projection:\n%s\nwant\n%s", after, before)
	}
}

// A current review completion discharges severity through the block alone:
// evidence without a severity entry folds, and the stored completion replays
// through a rebuild, twice, because the block that discharges severity is
// carried in the stored payload.
func TestReviewLaneBlockOnlyCompletionFoldsAndReplays(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[3]
	workID := "review-block-only"
	attemptID := "review-block-only-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, reviewBlockFixture())}}); err != nil {
		t.Fatalf("block-only completion was refused: %v", err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild refused the stored block-only completion: %v", err)
	}
	before := workerProjectionSnapshot(t, s)
	if !strings.Contains(before, "|completed|") {
		t.Fatalf("replayed completion projection = %q, want a completed attempt", before)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("second rebuild changed the worker projection:\n%s\nwant\n%s", after, before)
	}
}

// A live append cannot dodge the requirement by claiming a pre-CD-0197
// payload version: the register boundary upcasts before the fold, and the
// fold holds a live reported completion to the required block.
func TestLiveV2CompletionForTheReviewLaneStillRequiresTheBlock(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[3]
	workID := "review-block-version-gate"
	attemptID := "review-block-version-gate-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"attempt_id": attemptID, "readback_model": preferredModelForLane(lane),
		"report_schema_version": WorkerReportSchemaVersion,
		"evidence_origin":       WorkerEvidenceReported, "evidence": laneCoveringEvidence(lane),
	}
	legacy := Event{EventID: attemptID + "-v2-complete", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(payload)}
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{legacy}})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("v2 live completion error = %v, want %s", err, KindInvalidPayload)
	}
	if detail := failureDetail(t, err); !strings.Contains(detail, "typed review block") {
		t.Fatalf("failure detail = %q, want it to name the required block", detail)
	}
}

// upcastWorkerCompletedV2 is the bytes unchanged at version 3, and a stored
// v2 completion that predates the requirement replays unchanged: the fold
// forgives the missing block on replay, and a rebuild reaches the same
// projection twice.
func TestWorkerCompletedV2ReplaysWithoutTheReviewBlock(t *testing.T) {
	t.Parallel()
	lane := BuiltinLaneDefinitions()[3]
	workID := "review-block-legacy"
	attemptID := "review-block-legacy-attempt"
	payload := map[string]any{
		"attempt_id": attemptID, "readback_model": preferredModelForLane(lane),
		"report_schema_version": WorkerReportSchemaVersion,
		"evidence_origin":       WorkerEvidenceReported, "evidence": laneCoveringEvidence(lane),
	}
	legacy := Event{EventID: attemptID + "-complete", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(payload)}
	upcast, err := upcastWorkerCompletedV2(legacy)
	if err != nil {
		t.Fatalf("upcastWorkerCompletedV2 error = %v", err)
	}
	if upcast.PayloadVersion != 3 || string(upcast.Payload) != string(legacy.Payload) {
		t.Fatalf("upcast = v%d %s, want v3 with the same bytes", upcast.PayloadVersion, upcast.Payload)
	}
	s := openTemp(t)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	// The v2 completion predates the requirement, so it is inserted as the
	// stored event a real pre-CD-0197 database carries and replayed through
	// the rebuild, which folds under the replay context.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		legacy.EventID, legacy.Kind, string(legacy.SubjectType), legacy.SubjectID, legacy.Actor, legacy.OccurredAt.UTC().Format(time.RFC3339Nano), 2, string(legacy.Payload)); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild refused the stored v2 completion: %v", err)
	}
	before := workerProjectionSnapshot(t, s)
	if !strings.Contains(before, "|completed|") {
		t.Fatalf("replayed completion projection = %q, want a completed attempt", before)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("second rebuild changed the worker projection:\n%s\nwant\n%s", after, before)
	}
}

// A dispatch recorded under the pre-CD-0197 review digest still completes:
// the legacy digest resolves to the current definition, and the completion is
// held to the requirement the current definition carries.
func TestLegacyReviewLaneDigestCompletesWithTheRequiredBlock(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	current := BuiltinLaneDefinitions()[3]
	legacyDigest := "sha256:49d6fac9d7ebcb95915dd3021e6e2cbd151a569a56221930c0d7a94232736e15"
	if _, err := LookupLane(current.ID, current.Version, legacyDigest); err != nil {
		t.Fatalf("pre-CD-0197 review digest no longer resolves: %v", err)
	}
	workID := "review-block-legacy-digest"
	attemptID := "review-block-legacy-digest-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, current, map[string]any{"lane_digest": legacyDigest})}}); err != nil {
		t.Fatal(err)
	}
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, current, nil)}})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("blockless completion under a legacy digest error = %v, want %s", err, KindInvalidPayload)
	}
	second := "review-block-legacy-digest-attempt-2"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, second, current, map[string]any{"lane_digest": legacyDigest})}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, second, current, reviewBlockFixture())}}); err != nil {
		t.Fatalf("completion under a legacy digest was refused: %v", err)
	}
}

// The review-block requirement binds every live completion, whatever evidence
// origin the completion claims: a live review completion that declares
// legacy_unavailable carries no evidence to cover, but it still cannot make
// the attempt terminal without the typed block the lane requires.
func TestLiveLegacyUnavailableReviewCompletionStillRequiresTheBlock(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[3]
	if lane.ID != "review" {
		t.Fatalf("fixture lane = %s, want review", lane.ID)
	}
	workID := "review-block-legacy-origin"
	legacyComplete := func(attemptID string, review *WorkerReviewBlock) Event {
		payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, EvidenceOrigin: WorkerEvidenceLegacyUnavailable, Review: review}
		return Event{EventID: attemptID + "-complete", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 3, Payload: mustJSONValue(payload)}
	}
	attemptID := "review-block-legacy-origin-attempt"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	before := workerProjectionSnapshot(t, s)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{legacyComplete(attemptID, nil)}})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("blockless legacy_unavailable completion error = %v, want %s", err, KindInvalidPayload)
	}
	if detail := failureDetail(t, err); !strings.Contains(detail, "typed review block the review lane requires") {
		t.Fatalf("failure detail = %q, want it to name the required block", detail)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("refused completion changed the worker projection:\n%s\nwant\n%s", after, before)
	}
	second := "review-block-legacy-origin-attempt-2"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, second, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{legacyComplete(second, reviewBlockFixture())}}); err != nil {
		t.Fatalf("legacy_unavailable completion carrying the typed review block was refused: %v", err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, second).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("lifecycle_state = %q, want completed", state)
	}
}
