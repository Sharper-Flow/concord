package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Conflict receipts bind the exact rule, effective size, and settlement outcome
// accepted through generic append and the live recorder.

// conflictApplicationPayload builds one closed v1 payload: rule_id,
// rule_source_hash, catalog_generation, size_assessment_sequence,
// model_version, size, outcome, settlement_ref. A nil override value removes
// the field, so a test can record an incomplete payload.
func conflictApplicationPayload(overrides map[string]any) map[string]any {
	payload := map[string]any{
		"rule_id":                  "R1",
		"rule_source_hash":         "sha256:" + strings.Repeat("ab", 32),
		"catalog_generation":       1,
		"size_assessment_sequence": 1,
		"model_version":            WorkSizeModelVersion,
		"size":                     string(WorkSizeSmall),
		"outcome":                  "settled: rule R1 applied to the recorded size",
		"settlement_ref":           "settlement-r1",
	}
	for field, value := range overrides {
		if value == nil {
			delete(payload, field)
			continue
		}
		payload[field] = value
	}
	return payload
}

func conflictAppliedEvent(eventID, workID string, overrides map[string]any) Event {
	return operationEvent(eventID, WorkConflictApplied, SubjectWorkItem, workID, conflictApplicationPayload(overrides))
}

// seedSizedWork records one legitimate model-owned size (sequence 1) for a
// fresh work item through the ordinary work.sized fold.
func seedSizedWork(t *testing.T, s *Store, id string) WorkSize {
	t.Helper()
	seedWork(t, s, id)
	inputs := workSizeInputsFixture()
	size, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
	if err != nil {
		t.Fatalf("decide the fixture size: %v", err)
	}
	if err := applyWorkEvent(t, s, workSizedEvent("size-"+id, id, WorkSizeModelVersion, size, inputs), nil); err != nil {
		t.Fatalf("size %s: %v", id, err)
	}
	return size
}

func conflictReceiptDigest(t *testing.T, s *Store, eventID string) string {
	t.Helper()
	var digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=? AND kind=?`, eventID, WorkConflictApplied).Scan(&digest); err != nil {
		t.Fatalf("read the recorded conflict application %s: %v", eventID, err)
	}
	return digest
}

func assertConflictReceiptPayload(t *testing.T, s *Store, eventID string, expected map[string]any) {
	t.Helper()
	want, err := effectPayloadDigest(mustJSONValue(expected))
	if got := conflictReceiptDigest(t, s, eventID); err != nil || got != want {
		t.Fatalf("conflict %s payload digest=%q, want exact closed payload %q: %v", eventID, got, want, err)
	}
}

func liveConflictPayload(request ConflictApplicationRequest, size WorkSize, sequence int) map[string]any {
	return conflictApplicationPayload(map[string]any{"rule_id": request.RuleID, "rule_source_hash": conflictRuleSourceHash, "catalog_generation": conflictRuleCatalogGeneration, "size_assessment_sequence": sequence, "size": string(size), "outcome": request.Outcome, "settlement_ref": request.SettlementRef})
}

func countConflictEvents(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, WorkConflictApplied).Scan(&count); err != nil {
		t.Fatalf("count conflict application events: %v", err)
	}
	return count
}

func TestConflictApplicationRecordPersistsRuleSizeAndOutcome(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "conflict")
	if err := applyWorkEvent(t, s, conflictAppliedEvent("conflict-1", "conflict", nil), nil); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	assertConflictReceiptPayload(t, s, "conflict-1", conflictApplicationPayload(nil))
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("effect_receipts holds %d conflict applications, want 1", got)
	}
}

func TestConflictApplicationV2RecordsEffectiveSize(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "conflict-v2")
	var eventID string
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		var err error
		eventID, err = RecordConflictApplicationTx(context.Background(), tx, ConflictApplicationRequest{WorkID: "conflict-v2", RuleID: "A2-3", Outcome: "settled", SettlementRef: "settlement-v2", Actor: "operator", OccurredAt: time.Unix(30, 0).UTC()})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT payload_version FROM effect_receipts WHERE event_id=?`, eventID).Scan(&version); err != nil || version != 2 {
		t.Fatalf("conflict payload version=%d err=%v", version, err)
	}
	assertConflictReceiptPayload(t, s, eventID, liveConflictPayload(ConflictApplicationRequest{RuleID: "A2-3", Outcome: "settled", SettlementRef: "settlement-v2"}, WorkSizeSmall, 1))
}

func TestConflictApplicationDoesNotAdvanceWorkVersion(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "steady")
	if got := readWorkVersion(t, s, "steady"); got != 2 {
		t.Fatalf("work version after sizing = %d, want 2", got)
	}
	if err := applyWorkEvent(t, s, conflictAppliedEvent("steady-1", "steady", nil), nil); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	if got := readWorkVersion(t, s, "steady"); got != 2 {
		t.Fatalf("conflict application moved the work version to %d; the record must not advance it", got)
	}
}

func TestConflictApplicationRefusesAnUnsizedWorkItem(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "unsized")
	err := applyWorkEvent(t, s, conflictAppliedEvent("unsized-1", "unsized", nil), nil)
	assertFailureKind(t, err, KindProjectionNotFound)
	assertFoldGuardEmpty(t, s)
	if got := countConflictEvents(t, s); got != 0 {
		t.Fatalf("a refused application left %d event row(s); missing size refuses without effects", got)
	}
}

func TestConflictApplicationRefusesAMissingRecordedField(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "incomplete")
	for _, field := range []string{"rule_id", "size", "outcome"} {
		err := applyWorkEvent(t, s, conflictAppliedEvent("incomplete-"+field, "incomplete", map[string]any{field: nil}), nil)
		assertFailureKind(t, err, KindInvalidPayload)
	}
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationRefusesAnUnknownRuleID(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "unknownrule")
	for _, rule := range []string{"P99", "A2-10"} {
		err := applyWorkEvent(t, s, conflictAppliedEvent("unknownrule-"+rule, "unknownrule", map[string]any{"rule_id": rule}), nil)
		assertFailureKind(t, err, KindInvalidPayload)
	}
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationRefusesABlankOutcome(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "blank")
	err := applyWorkEvent(t, s, conflictAppliedEvent("blank-1", "blank", map[string]any{"outcome": ""}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationRefusesAForgedSizeModelOrAssessment(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "forged")
	err := applyWorkEvent(t, s, conflictAppliedEvent("forged-size", "forged", map[string]any{"size": string(WorkSizeCrucial)}), nil)
	assertFailureKind(t, err, KindInvariantViolation)
	err = applyWorkEvent(t, s, conflictAppliedEvent("forged-model", "forged", map[string]any{"model_version": WorkSizeModelVersion + 1}), nil)
	assertFailureKind(t, err, KindInvariantViolation)
	err = applyWorkEvent(t, s, conflictAppliedEvent("forged-sequence", "forged", map[string]any{"size_assessment_sequence": 2}), nil)
	assertFailureKind(t, err, KindProjectionNotFound)
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationRefusesABlankOrMalformedRuleSourceHash(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "hash")
	err := applyWorkEvent(t, s, conflictAppliedEvent("hash-blank", "hash", map[string]any{"rule_source_hash": ""}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	err = applyWorkEvent(t, s, conflictAppliedEvent("hash-text", "hash", map[string]any{"rule_source_hash": "not-a-digest"}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationRefusesUnknownPayloadFields(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "closed")
	err := applyWorkEvent(t, s, conflictAppliedEvent("closed-1", "closed", map[string]any{"size_next": string(WorkSizeSmall)}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
}

// Event identity is the append layer's semantic identity: an identical retry of
// the same event_id is reported as a duplicate, and the same event_id carrying
// a divergent outcome refuses as an idempotency conflict.
func TestConflictApplicationEventIdentityRefusesDuplicateAndDivergentReplay(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "identity")
	if err := applyWorkEvent(t, s, conflictAppliedEvent("identity-1", "identity", nil), nil); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	err := applyWorkEvent(t, s, conflictAppliedEvent("identity-1", "identity", nil), nil)
	assertFailureKind(t, err, KindDuplicateEvent)
	err = applyWorkEvent(t, s, conflictAppliedEvent("identity-1", "identity", map[string]any{"outcome": "settled: a divergent outcome"}), nil)
	assertFailureKind(t, err, KindIdempotencyConflict)
	assertFoldGuardEmpty(t, s)
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("effect_receipts holds %d conflict applications after refused replays, want 1", got)
	}
}

func TestConflictApplicationRollsBackTheWholeOperation(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "rollback")
	operation := Operation{Events: []Event{
		conflictAppliedEvent("rollback-1", "rollback", nil),
		conflictAppliedEvent("rollback-2", "rollback", map[string]any{"outcome": ""}),
	}}
	err := ApplyOperation(context.Background(), s, operation)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
	if got := countConflictEvents(t, s); got != 0 {
		t.Fatalf("effect_receipts keeps %d conflict application row(s) after a refused operation; the transaction must roll back", got)
	}
	if got := readWorkVersion(t, s, "rollback"); got != 2 {
		t.Fatalf("refused operation left the work version at %d, want 2", got)
	}
}

// The synthetic later work.sized event is a fold fixture, not a public resize
// route: the record must keep the assessment its payload references,
// never against the current generated table or the latest assessment.
func TestConflictApplicationSurvivesLaterAssessmentAndReopen(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedSizedWork(t, s, "durable")
	if err := applyWorkEvent(t, s, conflictAppliedEvent("durable-1", "durable", nil), nil); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	assertConflictReceiptPayload(t, s, "durable-1", conflictApplicationPayload(nil))
	before := conflictReceiptDigest(t, s, "durable-1")
	later := workSizeInputsFixture()
	later.Declared.EstimatedProductionLines = 61
	if err := applyWorkEvent(t, s, workSizedEvent("durable-later", "durable", WorkSizeModelVersion, WorkSizeLarger, later), nil); err != nil {
		t.Fatalf("record the later assessment: %v", err)
	}
	after := conflictReceiptDigest(t, s, "durable-1")
	if after != before {
		t.Fatalf("later assessment changed the recorded application: %+v, want %+v", after, before)
	}
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("effect_receipts holds %d conflict applications, want 1", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	again := conflictReceiptDigest(t, reopened, "durable-1")
	if again != before {
		t.Fatalf("reopened store reads %+v, want the recorded %+v", again, before)
	}
}

func TestConflictApplicationKeepsTheHistoricalSizeAfterALaterAssessment(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "history")
	if err := applyWorkEvent(t, s, conflictAppliedEvent("history-1", "history", nil), nil); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	later := workSizeInputsFixture()
	later.Declared.EstimatedProductionLines = 61
	if err := applyWorkEvent(t, s, workSizedEvent("history-later", "history", WorkSizeModelVersion, WorkSizeLarger, later), nil); err != nil {
		t.Fatalf("record the later assessment: %v", err)
	}
	current, ok, err := readWorkSize(context.Background(), s.DatabaseForTesting(), "history")
	if err != nil || !ok || current.Size != WorkSizeLarger || current.Sequence != 2 {
		t.Fatalf("current size %+v ok=%v err=%v; want larger at sequence 2", current, ok, err)
	}
	assertConflictReceiptPayload(t, s, "history-1", conflictApplicationPayload(nil))
}

// conflictApplicationRequest builds one trusted-caller settlement request.
// The second argument is the caller-computed settlement reference; the
// recorder derives the event identity from the settlement inputs and the
// stored assessment sequence. A nil adjust keeps the complete request; an
// adjust mutates fields in place, including blanking them.
func conflictApplicationRequest(workID, settlementRef string, adjust func(*ConflictApplicationRequest)) ConflictApplicationRequest {
	request := ConflictApplicationRequest{
		WorkID:        workID,
		Actor:         "operator",
		OccurredAt:    time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		RuleID:        "R1",
		Outcome:       "settled: rule R1 applied through the recorder",
		SettlementRef: settlementRef,
	}
	if adjust != nil {
		adjust(&request)
	}
	return request
}

func recordConflictApplication(t *testing.T, s *Store, request ConflictApplicationRequest) ([]string, error) {
	t.Helper()
	var id string
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		var err error
		id, err = RecordConflictApplicationTx(context.Background(), tx, request)
		return err
	})
	if err != nil {
		return nil, err
	}
	return []string{id}, nil
}

func TestRecordConflictApplicationTxRecordsTheLiveBinding(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "recorder")
	ids, err := recordConflictApplication(t, s, conflictApplicationRequest("recorder", "settlement-recorder", nil))
	if err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("recorded event IDs %v, want the single derived identity", ids)
	}
	assertConflictReceiptPayload(t, s, ids[0], liveConflictPayload(conflictApplicationRequest("recorder", "settlement-recorder", nil), WorkSizeSmall, 1))
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("effect_receipts holds %d conflict applications, want 1", got)
	}
	if got := readWorkVersion(t, s, "recorder"); got != 2 {
		t.Fatalf("recorder moved the work version to %d; the record must not advance it", got)
	}
}

func TestConflictApplicationRecordsEffectiveSize(t *testing.T) {
	for _, sizes := range []struct{ model, operator WorkSize }{
		{WorkSizeSmall, WorkSizeCrucial},
		{WorkSizeLarger, WorkSizeSmall},
	} {
		t.Run(string(sizes.model)+"-to-"+string(sizes.operator), func(t *testing.T) {
			s := openTemp(t)
			workID := "effective-size"
			seedWork(t, s, workID)
			inputs := workSizeInputsFixture()
			if sizes.model == WorkSizeLarger {
				inputs.Declared.EstimatedProductionLines = 61
			}
			if err := applyWorkEvent(t, s, workSizedEvent("model-size", workID, WorkSizeModelVersion, sizes.model, inputs), nil); err != nil {
				t.Fatal(err)
			}
			beforeIDs, err := recordConflictApplication(t, s, conflictApplicationRequest(workID, "before-override", nil))
			if err != nil {
				t.Fatal(err)
			}
			assertConflictReceiptPayload(t, s, beforeIDs[0], liveConflictPayload(conflictApplicationRequest(workID, "before-override", nil), sizes.model, 1))
			before := conflictReceiptDigest(t, s, beforeIDs[0])
			beforeEntries := currentConflictApplications(t, s, workID)
			if len(beforeEntries) != 1 || beforeEntries[0].ConflictApplication == nil || beforeEntries[0].ConflictApplication.Size != sizes.model {
				t.Fatalf("current settlement before override = %+v", beforeEntries)
			}
			beforeApplication := *beforeEntries[0].ConflictApplication
			insertWorkSizeOverrideApproval(t, s, workID, 1)
			if err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, workID, "operator-size", sizes.operator)); err != nil {
				t.Fatal(err)
			}
			afterIDs, err := recordConflictApplication(t, s, conflictApplicationRequest(workID, "after-override", nil))
			if err != nil {
				t.Fatal(err)
			}
			assertConflictReceiptPayload(t, s, afterIDs[0], liveConflictPayload(conflictApplicationRequest(workID, "after-override", nil), sizes.operator, 1))
			// A generic append cannot substitute the model answer for the
			// operator size governing this event position.
			err = applyWorkEvent(t, s, conflictAppliedEvent("wrong-effective-size", workID, map[string]any{"size": string(sizes.model)}), nil)
			assertFailureKind(t, err, KindInvariantViolation)
			storedBefore := conflictReceiptDigest(t, s, beforeIDs[0])
			if storedBefore != before {
				t.Fatalf("override changed the earlier settlement: %+v, want %+v", storedBefore, before)
			}
			entries := currentConflictApplications(t, s, workID)
			if len(entries) != 2 || entries[0].ConflictApplication == nil || *entries[0].ConflictApplication != beforeApplication || entries[1].ConflictApplication == nil || entries[1].ConflictApplication.Size != sizes.operator || entries[1].ConflictApplication.SizeAssessmentSequence != 1 {
				t.Fatalf("current settlements lost model/override applicability: %+v", entries)
			}
		})
	}
}

func TestRecordConflictApplicationTxRetryReturnsTheOriginalRecord(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "retry")
	first, err := recordConflictApplication(t, s, conflictApplicationRequest("retry", "retry-1", nil))
	if err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	again, err := recordConflictApplication(t, s, conflictApplicationRequest("retry", "retry-1", nil))
	if err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if len(again) != 1 || again[0] != first[0] {
		t.Fatalf("retry returned %v, want the original %v", again, first)
	}
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("effect_receipts holds %d conflict applications after the retry, want the original 1", got)
	}
}

func TestRecordConflictApplicationTxRefusesADivergentRetry(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "divergent")
	first, err := recordConflictApplication(t, s, conflictApplicationRequest("divergent", "settlement-divergent", nil))
	if err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	_, err = recordConflictApplication(t, s, conflictApplicationRequest("divergent", "settlement-divergent", func(r *ConflictApplicationRequest) {
		r.Outcome = "settled: a changed outcome"
	}))
	assertFailureKind(t, err, KindIdempotencyConflict)
	assertConflictReceiptPayload(t, s, first[0], liveConflictPayload(conflictApplicationRequest("divergent", "settlement-divergent", nil), WorkSizeSmall, 1))
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("effect_receipts holds %d conflict applications after the divergent retry, want 1", got)
	}
}

func TestRecordConflictApplicationTxRefusesAnUnsizedWorkItem(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "unsized")
	_, err := recordConflictApplication(t, s, conflictApplicationRequest("unsized", "settlement-unsized", nil))
	assertFailureKind(t, err, KindProjectionNotFound)
	if got := countConflictEvents(t, s); got != 0 {
		t.Fatalf("a refused application left %d event row(s); missing size refuses without effects", got)
	}
}

// The derived identity is deterministic, bounded, and changes with every
// settlement input: the same declared intent under another principal, work,
// settlement reference, rule, or stored assessment sequence is a distinct
// settlement.
func TestConflictApplicationEventIdentityDerivesFromSettlementInputs(t *testing.T) {
	base := conflictApplicationRequest("identity", "settlement-identity", nil)
	first := conflictApplicationEventID(base, 1)
	if first == "" || len(first) > 128 || !strings.HasPrefix(first, "conflict-") {
		t.Fatalf("derived identity %q, want a bounded conflict- prefixed event ID", first)
	}
	if again := conflictApplicationEventID(base, 1); again != first {
		t.Fatalf("identity is not deterministic: %q then %q", first, again)
	}
	varied := []ConflictApplicationRequest{
		func() ConflictApplicationRequest { request := base; request.Actor = "other-principal"; return request }(),
		func() ConflictApplicationRequest { request := base; request.WorkID = "other-work"; return request }(),
		func() ConflictApplicationRequest {
			request := base
			request.SettlementRef = "settlement-other"
			return request
		}(),
		func() ConflictApplicationRequest { request := base; request.RuleID = "A2-3"; return request }(),
	}
	for _, request := range varied {
		if derived := conflictApplicationEventID(request, 1); derived == first {
			t.Fatalf("identity %q collides after changing the settlement inputs %+v", first, request)
		}
	}
	if derived := conflictApplicationEventID(base, 2); derived == first {
		t.Fatalf("identity %q collides across stored assessment sequences", first)
	}
}

func TestConflictApplicationIdentityPreservesFieldBoundaries(t *testing.T) {
	left := ConflictApplicationRequest{Actor: "actor", WorkID: "work-1", SettlementRef: "tag\x00work-2\x00settled", RuleID: "R1"}
	right := ConflictApplicationRequest{Actor: "actor\x00work-1\x00tag", WorkID: "work-2", SettlementRef: "settled", RuleID: "R1"}
	if conflictApplicationEventID(left, 1) == conflictApplicationEventID(right, 1) {
		t.Fatal("distinct settlement identity fields produced the same event ID")
	}
}

// The same settlement reference and rule applied after a later recorded
// assessment is a distinct settlement: the stored sequence participates in
// the identity, so it records at sequence 2 while the first keeps its
// historical copy.
func TestRecordConflictApplicationTxRecordsAcrossAssessmentSequences(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "sequence")
	first, err := recordConflictApplication(t, s, conflictApplicationRequest("sequence", "settlement-seq", nil))
	if err != nil {
		t.Fatalf("record the first settlement: %v", err)
	}
	later := workSizeInputsFixture()
	later.Declared.EstimatedProductionLines = 61
	if err := applyWorkEvent(t, s, workSizedEvent("sequence-later", "sequence", WorkSizeModelVersion, WorkSizeLarger, later), nil); err != nil {
		t.Fatalf("record the later assessment: %v", err)
	}
	second, err := recordConflictApplication(t, s, conflictApplicationRequest("sequence", "settlement-seq", nil))
	if err != nil {
		t.Fatalf("record the settlement at the later sequence: %v", err)
	}
	if len(second) != 1 || second[0] == first[0] {
		t.Fatalf("recorded identities %v then %v, want a distinct second settlement at the later sequence", first, second)
	}
	request := conflictApplicationRequest("sequence", "settlement-seq", nil)
	assertConflictReceiptPayload(t, s, second[0], liveConflictPayload(request, WorkSizeLarger, 2))
	assertConflictReceiptPayload(t, s, first[0], liveConflictPayload(request, WorkSizeSmall, 1))
	if got := countConflictEvents(t, s); got != 2 {
		t.Fatalf("effect_receipts holds %d conflict applications, want 2", got)
	}
}

func TestRecordConflictApplicationTxRefusesAnUnknownLiveRule(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "liverule")
	_, err := recordConflictApplication(t, s, conflictApplicationRequest("liverule", "liverule-1", func(r *ConflictApplicationRequest) {
		r.RuleID = "P99"
	}))
	assertFailureKind(t, err, KindInvalidPayload)
	if got := countConflictEvents(t, s); got != 0 {
		t.Fatalf("a refused application left %d event row(s)", got)
	}
}

func TestRecordConflictApplicationTxRefusesAnIncompleteRequest(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "incomplete")
	_, err := recordConflictApplication(t, s, conflictApplicationRequest("incomplete", "incomplete-1", func(r *ConflictApplicationRequest) {
		r.Actor = ""
	}))
	assertFailureKind(t, err, KindInvalidOperation)
	_, err = recordConflictApplication(t, s, conflictApplicationRequest("incomplete", "incomplete-1", func(r *ConflictApplicationRequest) {
		r.OccurredAt = time.Time{}
	}))
	assertFailureKind(t, err, KindInvalidOperation)
	_, err = recordConflictApplication(t, s, conflictApplicationRequest("incomplete", "incomplete-1", func(r *ConflictApplicationRequest) {
		r.Outcome = ""
	}))
	assertFailureKind(t, err, KindInvalidPayload)
	if got := countConflictEvents(t, s); got != 0 {
		t.Fatalf("refused requests left %d event row(s)", got)
	}
}

// Whitespace-only text passes a bare == "" check, so both routes must share the
// package's TrimSpace nonblank rule. Each subtest exercises one field on one
// route independently: the frozen v1 validator through the generic append
// route, the live recorder through the trusted-caller request.
func TestConflictApplicationRefusesWhitespaceOnlyFieldsOnBothRoutes(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "whitespace")
	for _, field := range []string{"outcome", "settlement_ref"} {
		err := applyWorkEvent(t, s, conflictAppliedEvent("whitespace-frozen-"+field, "whitespace", map[string]any{field: " \t\n"}), nil)
		assertFailureKind(t, err, KindInvalidPayload)
	}
	live := []struct {
		eventID string
		adjust  func(*ConflictApplicationRequest)
	}{
		{"whitespace-live-outcome", func(r *ConflictApplicationRequest) { r.Outcome = " \t\n" }},
		{"whitespace-live-ref", func(r *ConflictApplicationRequest) { r.SettlementRef = " " }},
	}
	for _, tc := range live {
		_, err := recordConflictApplication(t, s, conflictApplicationRequest("whitespace", tc.eventID, tc.adjust))
		assertFailureKind(t, err, KindInvalidPayload)
	}
	if got := countConflictEvents(t, s); got != 0 {
		t.Fatalf("whitespace-only applications left %d event row(s)", got)
	}
}

// Every size cell records, and valid A2 rules record through the live check
// even where their size cell cites no R/P rule.
func TestRecordConflictApplicationTxRecordsAllThreeSizesWithValidA2Rules(t *testing.T) {
	s := openTemp(t)
	cases := []struct {
		size   WorkSize
		rule   string
		adjust func(*WorkSizeInputs)
	}{
		{WorkSizeSmall, "A2-3", func(*WorkSizeInputs) {}},
		{WorkSizeLarger, "P3", func(in *WorkSizeInputs) { in.Declared.EstimatedProductionLines = 61 }},
		{WorkSizeCrucial, "A2-9", func(in *WorkSizeInputs) { in.Declared.Security = true }},
	}
	for _, tc := range cases {
		workID := "sizes-" + string(tc.size)
		seedWork(t, s, workID)
		inputs := workSizeInputsFixture()
		tc.adjust(&inputs)
		if err := applyWorkEvent(t, s, workSizedEvent("size-"+workID, workID, WorkSizeModelVersion, tc.size, inputs), nil); err != nil {
			t.Fatalf("size %s: %v", workID, err)
		}
		ids, err := recordConflictApplication(t, s, conflictApplicationRequest(workID, workID+"-conflict", func(r *ConflictApplicationRequest) { r.RuleID = tc.rule }))
		if err != nil {
			t.Fatalf("record %s against %s work: %v", tc.rule, tc.size, err)
		}
		request := conflictApplicationRequest(workID, workID+"-conflict", func(r *ConflictApplicationRequest) { r.RuleID = tc.rule })
		assertConflictReceiptPayload(t, s, ids[0], liveConflictPayload(request, tc.size, 1))
	}
	if got := countConflictEvents(t, s); got != len(cases) {
		t.Fatalf("effect_receipts holds %d conflict applications, want %d", got, len(cases))
	}
}

// The recorder's bounds and UTF-8 checks are live: they run before
// json.Marshal, whose encoder would silently replace invalid bytes with
// U+FFFD. The bound accepts exactly 4096 bytes.
func TestRecordConflictApplicationTxEnforcesLiveBoundsAndUTF8(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "bounds")
	_, err := recordConflictApplication(t, s, conflictApplicationRequest("bounds", "bounds-outcome-4097", func(r *ConflictApplicationRequest) {
		r.Outcome = strings.Repeat("x", conflictOutcomeMaxBytes+1)
	}))
	assertFailureKind(t, err, KindInvalidPayload)
	_, err = recordConflictApplication(t, s, conflictApplicationRequest("bounds", "bounds-ref-257", func(r *ConflictApplicationRequest) {
		r.SettlementRef = strings.Repeat("x", conflictSettlementRefMaxBytes+1)
	}))
	assertFailureKind(t, err, KindInvalidPayload)
	_, err = recordConflictApplication(t, s, conflictApplicationRequest("bounds", "bounds-utf8", func(r *ConflictApplicationRequest) {
		r.Outcome = "settled: \xff"
	}))
	assertFailureKind(t, err, KindInvalidPayload)
	if got := countConflictEvents(t, s); got != 0 {
		t.Fatalf("refused bounds cases left %d event row(s)", got)
	}
	if _, err := recordConflictApplication(t, s, conflictApplicationRequest("bounds", "bounds-outcome-4096", func(r *ConflictApplicationRequest) {
		r.Outcome = strings.Repeat("x", conflictOutcomeMaxBytes)
	})); err != nil {
		t.Fatalf("record at the %d-byte bound: %v", conflictOutcomeMaxBytes, err)
	}
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("effect_receipts holds %d conflict applications, want the one record at the bound", got)
	}
}
