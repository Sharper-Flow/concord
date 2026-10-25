package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// reviewBlockFixture is one well-formed typed review block, exactly as an
// adapter would forward it after admitting agent-lane-report.v1 (CD-0197).
func reviewBlockFixture() *WorkerReviewBlock {
	return &WorkerReviewBlock{
		Disposition:          "correction_required",
		InputCandidate:       WorkerReviewCandidate{ProjectID: "proj-test", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)},
		OutputCandidate:      WorkerReviewCandidate{ProjectID: "proj-test", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)},
		Dependencies:         WorkerReviewDependencies{Criteria: []WorkerReviewCriterion{{PredicateID: "predicate:primary", ContentDigest: "sha256:" + strings.Repeat("a", 64)}}, ArchitectureNotApplicable: true},
		Assessments:          []WorkerReviewAssessment{},
		RemainingObligations: []WorkerReviewObligation{},
		Findings: []WorkerReviewFinding{
			{FindingID: "finding:primary", Severity: "P1", Confidence: "high", Detail: "the pinned digest is not checked at the admission boundary", InScope: true, Blocking: true, Status: "open", EvidenceRefs: []string{}, ClosureRefs: []string{}},
			{FindingID: "finding:secondary", Severity: "P2", Confidence: "low", Detail: "the doc names two obligations for one field", InScope: false, Blocking: false, Status: "open", EvidenceRefs: []string{}, ClosureRefs: []string{}},
		},
	}
}

// reviewCompleteEventV3 builds a current-version completion carrying reported
// evidence and the optional typed review block. A completion that carries the
// block discharges severity through it, so its evidence covers the lane
// without a separate severity entry (CD-0197 D2); a blockless completion
// carries the older covering evidence. The event id and the attempt identity
// are the same value, matching the workerDispatchEvent fixture.
func reviewCompleteEventV3(workID, attemptID string, lane LaneDefinition, review *WorkerReviewBlock, directory string) Event {
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
	payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: directory, Evidence: evidence, EvidenceOrigin: WorkerEvidenceReported, Review: review}
	return Event{EventID: attemptID + "-complete", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(payload)}
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
	actorRef := authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	before := workerProjectionSnapshot(t, s)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, nil, workerFixtureDirectory(t, s, workID))}})
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
	recordWorkerFixtureDispatchAuthorization(t, s, workID, second, "execution", actorRef)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, second, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, second, lane, reviewBlockFixture(), workerFixtureDirectory(t, s, workID))}}); err != nil {
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT terminal_payload FROM worker_attempts WHERE work_id=? AND attempt_id=? AND terminal_event_id=? AND terminal_kind=?`, workID, second, second+"-complete", WorkerCompleted).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var durable WorkerCompletedPayload
	if err := json.Unmarshal(stored, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.Review == nil || durable.Review.Disposition != "correction_required" || len(durable.Review.Findings) != 2 {
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
	authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, nil, workerFixtureDirectory(t, s, workID))}}); err != nil {
		t.Fatalf("completion without a review block was refused on a lane that requires none: %v", err)
	}
	var stored []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT terminal_payload FROM worker_attempts WHERE work_id=? AND attempt_id=? AND terminal_event_id=? AND terminal_kind=?`, workID, attemptID, attemptID+"-complete", WorkerCompleted).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), `"review":`) {
		t.Fatalf("payload without a review block carries the field anyway: %s", stored)
	}
}

// The review block is closed in both directions at the store boundary: the
// candidate bindings, the closed severity and confidence vocabularies, and the
// detail bound are all checked before an event can land, and a refusal leaves
// the attempt dispatched.
func TestWorkerCompletionReviewBlockShapeIsClosed(t *testing.T) {
	t.Parallel()
	lane := BuiltinLaneDefinitions()[3]
	largest := reviewBlockFixture()
	largest.Findings = []WorkerReviewFinding{}
	for index := 0; index < 64; index++ {
		largest.Findings = append(largest.Findings, WorkerReviewFinding{FindingID: fmt.Sprintf("finding:bounded:%d", index), Severity: "P3", Confidence: "medium", Detail: strings.Repeat("x", 512), Status: "open", EvidenceRefs: []string{}, ClosureRefs: []string{}})
	}
	tests := []struct {
		name       string
		mutate     func(*WorkerReviewBlock)
		wantDetail string
	}{
		{name: "disposition outside the closed set", mutate: func(r *WorkerReviewBlock) { r.Disposition = "conditional" }, wantDetail: "disposition must be"},
		{name: "retired ship disposition", mutate: func(r *WorkerReviewBlock) { r.Disposition = "ship" }, wantDetail: "disposition must be"},
		{name: "unknown disposition", mutate: func(r *WorkerReviewBlock) { r.Disposition = "no_ship" }, wantDetail: "disposition must be"},
		{name: "findings array absent", mutate: func(r *WorkerReviewBlock) { r.Findings = nil }, wantDetail: "findings array"},
		{name: "more than 64 findings", mutate: func(r *WorkerReviewBlock) { r.Findings = make([]WorkerReviewFinding, 65) }, wantDetail: "at most 64 findings"},
		{name: "severity outside the closed scale", mutate: func(r *WorkerReviewBlock) { r.Findings[0].Severity = "S1" }, wantDetail: "P0, P1, P2, or P3"},
		{name: "confidence outside the closed scale", mutate: func(r *WorkerReviewBlock) { r.Findings[0].Confidence = "certain" }, wantDetail: "low, medium, or high"},
		{name: "empty finding detail", mutate: func(r *WorkerReviewBlock) { r.Findings[0].Detail = "" }, wantDetail: "between 1 and 512 UTF-8 bytes"},
		{name: "oversized finding detail", mutate: func(r *WorkerReviewBlock) { r.Findings[0].Detail = strings.Repeat("x", 513) }, wantDetail: "between 1 and 512 UTF-8 bytes"},
		{name: "missing input candidate", mutate: func(r *WorkerReviewBlock) { r.InputCandidate = WorkerReviewCandidate{} }, wantDetail: "input and output"},
		{name: "missing obligation set", mutate: func(r *WorkerReviewBlock) { r.RemainingObligations = nil }, wantDetail: "remaining_obligations"},
		{name: "unsupported closure", mutate: func(r *WorkerReviewBlock) { r.Findings[0].Status = "closed" }, wantDetail: "closure requires evidence"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTemp(t)
			workID := "review-block-shape"
			attemptID := "review-block-shape-attempt"
			seedWork(t, s, workID)
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
				t.Fatal(err)
			}
			review := reviewBlockFixture()
			testCase.mutate(review)
			err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, review, workerFixtureDirectory(t, s, workID))}})
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
	authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, largest, workerFixtureDirectory(t, s, workID))}}); err != nil {
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
	authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	before := workerProjectionSnapshot(t, s)
	payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, workID), Evidence: laneCoveringEvidence(lane), EvidenceOrigin: WorkerEvidenceReported, Review: reviewBlockFixture()}
	complete := Event{EventID: attemptID + "-complete", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(payload)}
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

// A current review completion discharges severity through the block alone.
func TestReviewLaneBlockOnlyCompletionFolds(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[3]
	workID := "review-block-only"
	attemptID := "review-block-only-attempt"
	authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, lane, reviewBlockFixture(), workerFixtureDirectory(t, s, workID))}}); err != nil {
		t.Fatalf("block-only completion was refused: %v", err)
	}
	before := workerProjectionSnapshot(t, s)
	if !strings.Contains(before, "|completed|") {
		t.Fatalf("completion projection = %q, want a completed attempt", before)
	}
}

func TestStaleReviewLaneDigestRefusesWithoutChangingEvidence(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	current := BuiltinLaneDefinitions()[3]
	legacyDigest := "sha256:49d6fac9d7ebcb95915dd3021e6e2cbd151a569a56221930c0d7a94232736e15"
	if _, err := LookupLane(current.ID, legacyDigest); !hasFailureKind(err, KindLaneDefinitionDigestMismatch) {
		t.Fatalf("stale review digest error = %v, want revalidation", err)
	}
	workID := "review-block-legacy-digest"
	attemptID := "review-block-legacy-digest-attempt"
	authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent(workID, attemptID, current, nil)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worker_attempts SET lane_digest=? WHERE attempt_id=?; DELETE FROM fold_guard`, legacyDigest, attemptID); err != nil {
		t.Fatal(err)
	}
	before := workerProjectionSnapshot(t, s)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{reviewCompleteEventV3(workID, attemptID, current, reviewBlockFixture(), workerFixtureDirectory(t, s, workID))}})
	if !hasFailureKind(err, KindLaneDefinitionDigestMismatch) {
		t.Fatalf("completion under a stale digest error = %v, want revalidation", err)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("revalidation refusal changed original evidence: before %s; after %s", before, after)
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
		payload := WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, workID), EvidenceOrigin: WorkerEvidenceLegacyUnavailable, Review: review}
		return Event{EventID: attemptID + "-complete", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(payload)}
	}
	attemptID := "review-block-legacy-origin-attempt"
	actorRef := authorizeWorkerFixtureDispatch(t, s, workID, attemptID)
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
	recordWorkerFixtureDispatchAuthorization(t, s, workID, second, "execution", actorRef)
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
