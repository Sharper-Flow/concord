package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The conflict application record is event-only: one work.conflict_applied
// event in domain_events carries the recorded rule ID and the settlement
// outcome. These tests pin that record through the generic append route (the
// closed validator and the fold) and through the live recorder. The v1
// payload's size members are retired history (CD-0242 D6): a v2 event, the
// only version a live write produces, records none of them.

// conflictApplicationPayload builds one closed v2 payload: rule_id,
// rule_source_hash, catalog_generation, outcome, settlement_ref. A nil
// override value removes the field, so a test can record an incomplete
// payload.
func conflictApplicationPayload(overrides map[string]any) map[string]any {
	payload := map[string]any{
		"rule_id":            "R1",
		"rule_source_hash":   "sha256:" + strings.Repeat("ab", 32),
		"catalog_generation": 1,
		"outcome":            "settled: rule R1 applied to the recorded size",
		"settlement_ref":     "settlement-r1",
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
	event := operationEvent(eventID, WorkConflictApplied, SubjectWorkItem, workID, conflictApplicationPayload(overrides))
	// The closed v2 shape is the only version a generic append records.
	event.PayloadVersion = 2
	return event
}

func storedConflictApplication(t *testing.T, s *Store, eventID string) (ConflictApplication, map[string]any) {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, eventID).Scan(&raw); err != nil {
		t.Fatalf("read the recorded conflict application %s: %v", eventID, err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("recorded payload %s is not a JSON object: %v", eventID, err)
	}
	var record ConflictApplication
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatalf("recorded payload %s does not decode: %v", eventID, err)
	}
	return record, fields
}

func countConflictEvents(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, WorkConflictApplied).Scan(&count); err != nil {
		t.Fatalf("count conflict application events: %v", err)
	}
	return count
}

func TestConflictApplicationRecordPersistsRuleAndOutcome(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "conflict")
	if err := applyWorkEvent(t, s, conflictAppliedEvent("conflict-1", "conflict", nil), nil); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	record, fields := storedConflictApplication(t, s, "conflict-1")
	if record.RuleID != "R1" || record.Outcome == "" {
		t.Fatalf("recorded pair %+v; want rule R1 and a nonblank outcome", record)
	}
	if record.RuleSourceHash == "" || record.CatalogGeneration != 1 || record.SettlementRef == "" {
		t.Fatalf("recorded application is incomplete: %+v", record)
	}
	if record.SizeAssessmentSequence != nil || record.ModelVersion != nil || record.Size != nil {
		t.Fatalf("recorded application carries size members %+v; a v2 record has none", record)
	}
	if len(fields) != 5 {
		t.Fatalf("recorded payload carries %d fields; the closed v2 shape has exactly 5", len(fields))
	}
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("domain_events holds %d conflict application events, want 1", got)
	}
}

func TestConflictApplicationDoesNotAdvanceWorkVersion(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "steady")
	if got := readWorkVersion(t, s, "steady"); got != 2 {
		t.Fatalf("work version after the capture = %d, want 2", got)
	}
	if err := applyWorkEvent(t, s, conflictAppliedEvent("steady-1", "steady", nil), nil); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	if got := readWorkVersion(t, s, "steady"); got != 2 {
		t.Fatalf("conflict application moved the work version to %d; the record must not advance it", got)
	}
}

func TestConflictApplicationRefusesAMissingRecordedField(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "incomplete")
	for _, field := range []string{"rule_id", "outcome"} {
		err := applyWorkEvent(t, s, conflictAppliedEvent("incomplete-"+field, "incomplete", map[string]any{field: nil}), nil)
		assertFailureKind(t, err, KindInvalidPayload)
	}
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationRefusesAnUnknownRuleID(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "unknownrule")
	for _, rule := range []string{"P99", "A2-10"} {
		err := applyWorkEvent(t, s, conflictAppliedEvent("unknownrule-"+rule, "unknownrule", map[string]any{"rule_id": rule}), nil)
		assertFailureKind(t, err, KindInvalidPayload)
	}
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationRefusesABlankOutcome(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "blank")
	err := applyWorkEvent(t, s, conflictAppliedEvent("blank-1", "blank", map[string]any{"outcome": ""}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
}

// The retired v1 size members replay: a recorded v1 payload validates and
// folds, while a live write that claims size members refuses.
func TestConflictApplicationRetiresSizeMembers(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "retired")
	historical := conflictApplicationPayload(map[string]any{
		"size_assessment_sequence": 1,
		"model_version":            2,
		"size":                     "small",
	})
	if err := applyWorkEvent(t, s, operationEvent("retired-v1", WorkConflictApplied, SubjectWorkItem, "retired", historical), nil); err != nil {
		t.Fatalf("record a v1-shaped application: %v", err)
	}
	err := applyWorkEvent(t, s, conflictAppliedEvent("retired-live-size", "retired", map[string]any{"size": "small"}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	err = applyWorkEvent(t, s, conflictAppliedEvent("retired-v1-forged", "retired", map[string]any{
		"size_assessment_sequence": 0,
		"model_version":            2,
		"size":                     "small",
	}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationRefusesABlankOrMalformedRuleSourceHash(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "hash")
	err := applyWorkEvent(t, s, conflictAppliedEvent("hash-blank", "hash", map[string]any{"rule_source_hash": ""}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	err = applyWorkEvent(t, s, conflictAppliedEvent("hash-text", "hash", map[string]any{"rule_source_hash": "not-a-digest"}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationRefusesUnknownPayloadFields(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "closed")
	err := applyWorkEvent(t, s, conflictAppliedEvent("closed-1", "closed", map[string]any{"size_next": "small"}), nil)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
}

// Event identity is the append layer's semantic identity: an identical retry of
// the same event_id is reported as a duplicate, and the same event_id carrying
// a divergent outcome refuses as an idempotency conflict.
func TestConflictApplicationEventIdentityRefusesDuplicateAndDivergentReplay(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "identity")
	if err := applyWorkEvent(t, s, conflictAppliedEvent("identity-1", "identity", nil), nil); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	err := applyWorkEvent(t, s, conflictAppliedEvent("identity-1", "identity", nil), nil)
	assertFailureKind(t, err, KindDuplicateEvent)
	err = applyWorkEvent(t, s, conflictAppliedEvent("identity-1", "identity", map[string]any{"outcome": "settled: a divergent outcome"}), nil)
	assertFailureKind(t, err, KindIdempotencyConflict)
	assertFoldGuardEmpty(t, s)
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("domain_events holds %d conflict application events after refused replays, want 1", got)
	}
}

func TestConflictApplicationRollsBackTheWholeOperation(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "rollback")
	operation := Operation{Events: []Event{
		conflictAppliedEvent("rollback-1", "rollback", nil),
		conflictAppliedEvent("rollback-2", "rollback", map[string]any{"outcome": ""}),
	}}
	err := ApplyOperation(context.Background(), s, operation)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
	if got := countConflictEvents(t, s); got != 0 {
		t.Fatalf("domain_events keeps %d conflict application row(s) after a refused operation; the transaction must roll back", got)
	}
	if got := readWorkVersion(t, s, "rollback"); got != 2 {
		t.Fatalf("refused operation left the work version at %d, want 2", got)
	}
}

func TestConflictApplicationSurvivesRebuildAndReopen(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedWork(t, s, "durable")
	if err := applyWorkEvent(t, s, conflictAppliedEvent("durable-1", "durable", nil), nil); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	before, _ := storedConflictApplication(t, s, "durable-1")
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	after, _ := storedConflictApplication(t, s, "durable-1")
	if after != before {
		t.Fatalf("rebuild changed the recorded application: %+v, want %+v", after, before)
	}
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("domain_events holds %d conflict application events after rebuild, want 1", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	again, _ := storedConflictApplication(t, reopened, "durable-1")
	if again != before {
		t.Fatalf("reopened store reads %+v, want the recorded %+v", again, before)
	}
}

// conflictApplicationRequest builds one trusted-caller settlement request.
// The second argument is the caller-computed settlement reference; the
// recorder derives the event identity from the settlement inputs. A nil
// adjust keeps the complete request; an adjust mutates fields in place,
// including blanking them.
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
	seedWork(t, s, "recorder")
	ids, err := recordConflictApplication(t, s, conflictApplicationRequest("recorder", "settlement-recorder", nil))
	if err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("recorded event IDs %v, want the single derived identity", ids)
	}
	record, fields := storedConflictApplication(t, s, ids[0])
	if record.RuleID != "R1" || record.Outcome == "" {
		t.Fatalf("recorded pair %+v; want rule R1 and a nonblank outcome", record)
	}
	// The recorder freezes its own reads: the live table's source hash and
	// catalog generation.
	if record.RuleSourceHash != conflictRuleSourceHash || record.CatalogGeneration != conflictRuleCatalogGeneration {
		t.Fatalf("recorded binding hash %q generation %d; want the live table's %q/%d",
			record.RuleSourceHash, record.CatalogGeneration, conflictRuleSourceHash, conflictRuleCatalogGeneration)
	}
	if record.SizeAssessmentSequence != nil || record.ModelVersion != nil || record.Size != nil {
		t.Fatalf("recorded size copy %+v; the live recorder records no size members", record)
	}
	if len(fields) != 5 {
		t.Fatalf("recorded payload carries %d fields; the closed v2 shape has exactly 5", len(fields))
	}
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("domain_events holds %d conflict application events, want 1", got)
	}
	if got := readWorkVersion(t, s, "recorder"); got != 2 {
		t.Fatalf("recorder moved the work version to %d; the record must not advance it", got)
	}
}

func TestRecordConflictApplicationTxRetryReturnsTheOriginalRecord(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "retry")
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
		t.Fatalf("domain_events holds %d conflict application events after the retry, want the original 1", got)
	}
}

func TestRecordConflictApplicationTxRefusesADivergentRetry(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "divergent")
	first, err := recordConflictApplication(t, s, conflictApplicationRequest("divergent", "settlement-divergent", nil))
	if err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	_, err = recordConflictApplication(t, s, conflictApplicationRequest("divergent", "settlement-divergent", func(r *ConflictApplicationRequest) {
		r.Outcome = "settled: a changed outcome"
	}))
	assertFailureKind(t, err, KindIdempotencyConflict)
	record, _ := storedConflictApplication(t, s, first[0])
	if record.Outcome != "settled: rule R1 applied through the recorder" {
		t.Fatalf("divergent retry rewrote the recorded outcome to %q", record.Outcome)
	}
	if got := countConflictEvents(t, s); got != 1 {
		t.Fatalf("domain_events holds %d conflict application events after the divergent retry, want 1", got)
	}
}

// The derived identity is deterministic, bounded, and changes with every
// settlement input: the same declared intent under another principal, work,
// settlement reference, or rule is a distinct settlement.
func TestConflictApplicationEventIdentityDerivesFromSettlementInputs(t *testing.T) {
	base := conflictApplicationRequest("identity", "settlement-identity", nil)
	first := conflictApplicationEventID(base)
	if first == "" || len(first) > 128 || !strings.HasPrefix(first, "conflict-") {
		t.Fatalf("derived identity %q, want a bounded conflict- prefixed event ID", first)
	}
	if again := conflictApplicationEventID(base); again != first {
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
		if derived := conflictApplicationEventID(request); derived == first {
			t.Fatalf("identity %q collides after changing the settlement inputs %+v", first, request)
		}
	}
}

func TestConflictApplicationIdentityPreservesFieldBoundaries(t *testing.T) {
	left := ConflictApplicationRequest{Actor: "actor", WorkID: "work-1", SettlementRef: "tag\x00work-2\x00settled", RuleID: "R1"}
	right := ConflictApplicationRequest{Actor: "actor\x00work-1\x00tag", WorkID: "work-2", SettlementRef: "settled", RuleID: "R1"}
	if conflictApplicationEventID(left) == conflictApplicationEventID(right) {
		t.Fatal("distinct settlement identity fields produced the same event ID")
	}
}

func TestRecordConflictApplicationTxRefusesAnUnknownLiveRule(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "liverule")
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
	seedWork(t, s, "incomplete")
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
// route independently: the closed validator through the generic append
// route, the live recorder through the trusted-caller request.
func TestConflictApplicationRefusesWhitespaceOnlyFieldsOnBothRoutes(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "whitespace")
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

// The recorder's bounds and UTF-8 checks are live: they run before
// json.Marshal, whose encoder would silently replace invalid bytes with
// U+FFFD. The bound accepts exactly 4096 bytes.
func TestRecordConflictApplicationTxEnforcesLiveBoundsAndUTF8(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "bounds")
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
		t.Fatalf("domain_events holds %d conflict application events, want the one record at the bound", got)
	}
}
