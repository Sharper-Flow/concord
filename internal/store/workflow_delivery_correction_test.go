package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The delivery correction admits one typed, append-only correction of a
// completed delivery assertion, attributed to the recorded coordinator actor
// and carrying no approval claim. The original event log stays immutable, the
// terminal instance stays closed, and the read surfaces show the effective
// assertion beside the unchanged original.
const (
	deliveryCorrectionApprovalRef  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	deliveryCorrectionApprovalHold = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	deliveryCorrectionDigest       = "sha256:" + deliveryCorrectionApprovalHold
	deliveryCorrectionConsequence  = "recovery"
	deliveryCorrectionWrongPath    = "file:internal/store/impl.go"
	deliveryCorrectionMergeRef     = "https://github.com/Sharper-Flow/concord/pull/1339"
)

func deliveryCorrectionScopeJSON(workID string) string {
	return `{"work_ids":["` + workID + `"]}`
}

func deliveryCorrectionVersionsJSON(version int64) string {
	return `{"work":` + jsonInt(version) + `}`
}

func jsonInt(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// completedDeliveryFixture seeds one work item whose workflow instance is
// completed through the current writer with a delivery assertion that carries a
// repository file path instead of merge evidence, the defect shape the
// correction exists to repair. It returns the store, the target assertion
// event id, the work item version the correction must expect, the assertion's
// stored payload version, and its sequence.
func completedDeliveryFixture(t *testing.T, workID string) (*Store, string, int64, int, int64) {
	t.Helper()
	s := completedDeliveryFixtureWithAssertions(t, workID, 1)
	targetEventID, seq, payloadVersion := deliveryAssertionEvent(t, s, workID, 0)
	return s, targetEventID, readWorkVersion(t, s, workID), payloadVersion, seq
}

// completedDeliveryFixtureWithAssertions seeds the completed item with a
// chosen number of delivery assertions, so the stale-target refusal can name
// an assertion that is no longer current. Repeated historical assertions come
// from a real prefix; the ordinary single-assertion case uses current writers.
func completedDeliveryFixtureWithAssertions(t *testing.T, workID string, assertions int) *Store {
	t.Helper()
	var s *Store
	if assertions > 1 {
		s = historicalRepeatedDeliveryFixture(t, workID, assertions)
	} else {
		f := qCurrentAcceptanceWithDelivery(t, workID, deliveryCorrectionWrongPath)
		s = f.store
		ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.run(ctx, "confirm_premise", "confirm", readWorkVersion(t, s, workID), nil, nil); err != nil {
			t.Fatal(err)
		}
		bindCompletionClosure(t, s, workID, f.owner)
		if err := f.run(ctx, "complete", "complete", readWorkVersion(t, s, workID), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES('client/concord-1','active','principal/operator','[]','[]','[]','2026-09-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return s
}

func historicalRepeatedDeliveryFixture(t *testing.T, work string, assertions int) *Store {
	t.Helper()
	c := evidenceCorpusForWork(t, work)
	var terminal Event
	for _, e := range c.events {
		if e.Kind == WorkflowCompleted {
			terminal = e
		}
	}
	var events []Event
	count := 0
	var version int64
	for _, e := range c.events {
		if e.Kind == WorkflowDeliveryCorrected || e.Kind == WorkflowCompleted && e.EventID != terminal.EventID {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if e.Kind == WorkflowActionCompleted && p["delivery_state"] == "asserted" {
			if count >= assertions {
				continue
			}
			p["delivery_artifact"] = deliveryCorrectionWrongPath
			if count > 0 {
				p["delivery_artifact"] = deliveryCorrectionWrongPath + "/" + jsonInt(int64(count))
			}
			count++
		}
		if _, bumps := p["expected_version"]; bumps && e.Kind != "compaction_link.published" {
			if version == 0 {
				version = int64(p["expected_version"].(float64))
			}
			p["expected_version"], p["resulting_version"] = version, version+1
			version++
		}
		e.Payload = mustJSONValue(p)
		events = append(events, e)
	}
	if count != assertions {
		t.Fatalf("historical delivery corpus has %d assertions, want %d", count, assertions)
	}
	c.events = events
	s := c.prefix(t, 141)
	if _, err := s.db.Exec(`INSERT INTO fold_guard VALUES(1); UPDATE work_items SET lifecycle='completed',version=? WHERE id=?; UPDATE workflow_instances SET instance_state='completed',completed_at=? WHERE work_id=?; DELETE FROM fold_guard`, version, work, terminal.OccurredAt.Format(time.RFC3339Nano), work); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), s.db); err != nil {
		t.Fatal(err)
	}
	return s
}

// workflowCompletedFixtureEvent builds the terminal completion event at its
// registered payload version.
func workflowCompletedFixtureEvent(id, workID, actor, verdictRef string, expected int64) Event {
	event := workflowEventWithActor(id, WorkflowCompleted, workID, actor, map[string]any{"work_id": workID, "expected_version": expected, "resulting_version": expected + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": verdictRef, "premise_confirmed": false, "evidence_count": 0, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking"})
	event.PayloadVersion = 2
	return event
}

// deliveryAssertionEvent reads the delivery assertion at a chosen offset from
// the latest, returning its event id, sequence, and stored payload version.
func deliveryAssertionEvent(t *testing.T, s *Store, workID string, offsetFromLatest int) (string, int64, int) {
	t.Helper()
	var eventID string
	var seq int64
	var payloadVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id,effect_seq,payload_version FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=? AND delivery_assertion=1 ORDER BY effect_seq DESC LIMIT 1 OFFSET ?`, workID, WorkflowActionCompleted, offsetFromLatest).Scan(&eventID, &seq, &payloadVersion); err != nil {
		t.Fatal(err)
	}
	return eventID, seq, payloadVersion
}

func deliveryCorrectionRequest(workID string, version int64, targetEventID string, targetSeq int64, targetPayloadVersion int) WorkflowDeliveryCorrectionRequest {
	return WorkflowDeliveryCorrectionRequest{
		WorkID: workID, ExpectedVersion: version,
		TargetEventID: targetEventID, TargetSeq: targetSeq, TargetPayloadVersion: targetPayloadVersion,
		Reason:           "the asserted delivery named repository paths before the pull request merged",
		DeliveryArtifact: deliveryCorrectionMergeRef, DeliveryState: "asserted",
		EventID:    "correction-" + workID,
		OccurredAt: time.Unix(20, 0).UTC(),
	}
}

func runDeliveryCorrection(t *testing.T, s *Store, request WorkflowDeliveryCorrectionRequest) error {
	t.Helper()
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := ApplyWorkflowDeliveryCorrectionTx(context.Background(), tx, request)
		return err
	})
	return err
}

func requireDeliveryCorrectionRefusal(t *testing.T, err error, wantKind FailureKind, wantDetail string) {
	t.Helper()
	if err == nil {
		t.Fatalf("delivery correction was admitted, want %s refusal naming %q", wantKind, wantDetail)
	}
	failure := &Failure{}
	if !failureAs(err, &failure) || failure.Kind != wantKind || !strings.Contains(failure.Detail, wantDetail) {
		t.Fatalf("delivery correction error=%v, want %s naming %q", err, wantKind, wantDetail)
	}
}

// TestMergeEvidenceSchemaAlignsWithFoldRule proves payload schema, Go
// admission, and the Bun validator agree. Replay keeps legacy rules so stricter
// admission does not make stored events unreadable.
// proves check:merge-evidence-schema-alignment.
func TestMergeEvidenceSchemaAlignsWithFoldRule(t *testing.T) {
	t.Parallel()
	document := payloadschema.Document()
	defs, _ := document["$defs"].(map[string]any)
	mergeEvidence, _ := defs["merge_evidence"].(map[string]any)
	reference, _ := defs["reference"].(map[string]any)
	if mergeEvidence == nil {
		t.Fatal("generated payload schema declares no $defs/merge_evidence")
	}
	if reference == nil {
		t.Fatal("generated payload schema declares no $defs/reference")
	}
	if mergeEvidence["$ref"] != "#/$defs/reference" {
		t.Fatalf("merge_evidence ref = %v, want the shared reference constraints", mergeEvidence["$ref"])
	}
	for _, site := range []struct{ def, field string }{
		{"work_transition_correct_delivery_input", "delivery_artifact"},
		{"workflow_delivery_correction", "artifact"},
	} {
		object, _ := defs[site.def].(map[string]any)
		properties, _ := object["properties"].(map[string]any)
		property, _ := properties[site.field].(map[string]any)
		if property["$ref"] != "#/$defs/merge_evidence" {
			t.Fatalf("%s.%s = %v, want the shared $defs/merge_evidence", site.def, site.field, property["$ref"])
		}
	}
	type admissionCase struct {
		name       string
		definition string
		value      string
		want       bool
	}
	cases := []admissionCase{
		{name: "reference lower bound", definition: "reference", value: "a", want: false},
		{name: "reference valid length", definition: "reference", value: "ab", want: true},
		{name: "reference form feed", definition: "reference", value: "a\fb", want: false},
		{name: "reference non-breaking space", definition: "reference", value: "a\u00a0b", want: false},
		{name: "reference byte order mark", definition: "reference", value: "a\ufeffb", want: true},
		{name: "reference maximum bytes", definition: "reference", value: strings.Repeat("a", 128), want: true},
		{name: "reference over maximum bytes", definition: "reference", value: strings.Repeat("a", 129), want: false},
		{name: "normal merge URL", definition: "merge_evidence", value: deliveryCorrectionMergeRef, want: true},
		{name: "URL reference form feed", definition: "merge_evidence", value: "https://example.test/a\fb", want: false},
		{name: "URL reference non-breaking space", definition: "merge_evidence", value: "https://example.test/a\u00a0b", want: false},
		{name: "URL reference byte order mark", definition: "merge_evidence", value: "https://example.test/a\ufeffb", want: true},
		{name: "empty authority", definition: "merge_evidence", value: "https://", want: false},
		{name: "empty authority before path", definition: "merge_evidence", value: "https:///path", want: false},
		{name: "non-numeric port", definition: "merge_evidence", value: "https://example.test:bad", want: false},
		{name: "at sign in query", definition: "merge_evidence", value: "https://example.test?user@example.test", want: true},
		{name: "maximum merge URL reference bytes", definition: "merge_evidence", value: "https://" + strings.Repeat("a", 120), want: true},
		{name: "over maximum merge URL reference bytes", definition: "merge_evidence", value: "https://" + strings.Repeat("a", 121), want: false},
		{name: "userinfo", definition: "merge_evidence", value: "https://user:pass@example.test/path", want: false},
		{name: "ASCII control", definition: "merge_evidence", value: "https://example.test/a\x01b", want: false},
		{name: "non-https scheme", definition: "merge_evidence", value: "http://example.test/path", want: false},
		{name: "repository path", definition: "merge_evidence", value: deliveryCorrectionWrongPath, want: false},
		{name: "bracketed IPv6 host", definition: "merge_evidence", value: "https://[2001:db8::1]/merge", want: true},
		{name: "bracketed IPv6 with port", definition: "merge_evidence", value: "https://[2001:db8::1]:443/merge", want: true},
		{name: "numeric port", definition: "merge_evidence", value: "https://example.test:443/merge", want: true},
		{name: "malformed bracketed host", definition: "merge_evidence", value: "https://[broken/merge", want: false},
		{name: "at sign in path", definition: "merge_evidence", value: "https://example.test/a@b", want: true},
		{name: "at sign in fragment", definition: "merge_evidence", value: "https://example.test/merge#user@example.test", want: true},
		{name: "DEL control", definition: "merge_evidence", value: "https://example.test/a\x7fb", want: false},
		{name: "missing host", definition: "merge_evidence", value: "https:///merge", want: false},
		{name: "empty port", definition: "merge_evidence", value: "https://example.test:/merge", want: false},
		{name: "malformed percent escape", definition: "merge_evidence", value: "https://example.test/%zz", want: true},
	}
	for _, whitespace := range []rune{
		'\u0009', '\u000a', '\u000b', '\u000c', '\u000d', '\u0020', '\u0085', '\u00a0', '\u1680',
		'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a',
		'\u2028', '\u2029', '\u202f', '\u205f', '\u3000',
	} {
		cases = append(cases, admissionCase{
			name: fmt.Sprintf("reference whitespace U+%04X", whitespace), definition: "reference",
			value: "a" + string(whitespace) + "b", want: false,
		})
	}
	probes := make([]map[string]any, 0, len(cases))
	for _, tc := range cases {
		probes = append(probes, map[string]any{
			"id": tc.name, "mode": "definition", "definition": tc.definition, "value": tc.value,
		})
	}
	bunVerdicts := outsideRepairPublishedVerdicts(t, probes)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			definition := reference
			goAdmission := ValidReference(tc.value)
			if tc.definition == "merge_evidence" {
				definition = mergeEvidence
				goAdmission = validateMergeEvidenceAdmission(tc.value, "$") == nil
			}
			schemaErr := payloadschema.ValidateValue(tc.value, definition, document, "$")
			bunAdmission, ok := bunVerdicts[tc.name]
			if !ok {
				t.Fatalf("Bun validator returned no verdict")
			}
			want := tc.want
			if (schemaErr == nil) != want {
				t.Errorf("payload schema admitted=%v, want %v: %v", schemaErr == nil, want, schemaErr)
			}
			if goAdmission != want {
				t.Errorf("Go admission=%v, want %v", goAdmission, want)
			}
			if bunAdmission.Admit != want {
				t.Errorf("Bun admission=%v failures=%v, want %v", bunAdmission.Admit, bunAdmission.Failures, want)
			}
		})
	}
}

// TestTerminalDeliveryCorrectionAdmission proves the admission gate: the
// correction is admitted only with the target identity, version, reason, and
// merge evidence, attributed to the recorded coordinator actor and carrying
// no approval claim, on a completed instance whose assertion is not already
// corrected. proves check:terminal-delivery-correction-admission.
func TestTerminalDeliveryCorrectionAdmission(t *testing.T) {
	t.Parallel()
	const workID = "delivery-correction-admission"
	s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)

	request := unsignedDeliveryCorrectionRequest(t, s, workID, version, targetEventID, targetSeq, targetPayloadVersion)
	request.DeliveryArtifact = "https://example.test/%zz"
	if _, err := url.Parse(request.DeliveryArtifact); err == nil {
		t.Fatal("legacy URL parser accepted the malformed percent escape witness")
	}
	if err := runDeliveryCorrection(t, s, request); err != nil {
		t.Fatalf("unsigned delivery correction refused: %v", err)
	}
	var instanceState, currentStep, lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState, &currentStep); err != nil {
		t.Fatal(err)
	}
	if instanceState != "completed" || currentStep != "release" {
		t.Fatalf("correction moved the closed instance to %q at %q", instanceState, currentStep)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" {
		t.Fatalf("correction reopened the work item lifecycle to %q", lifecycle)
	}
	if got := readWorkVersion(t, s, workID); got != version+1 {
		t.Fatalf("correction version=%d, want the single correction advance (%d)", got, version+1)
	}
	assertion, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if assertion.DeliveryAssertion == nil || assertion.DeliveryAssertion.Correction == nil || assertion.DeliveryAssertion.Correction.Artifact != request.DeliveryArtifact {
		t.Fatalf("merge artifact = %+v, want %q", assertion.DeliveryAssertion, request.DeliveryArtifact)
	}

	// One correction overlays one assertion.
	second := unsignedDeliveryCorrectionRequest(t, s, workID, readWorkVersion(t, s, workID), targetEventID, targetSeq, targetPayloadVersion)
	second.EventID = "correction-" + workID + "-second"
	requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, second), KindInvalidOperation, "already corrected")
}

// TestTerminalDeliveryCorrectionAdmissionRefusals holds the named refusals:
// incomplete fields, a mistargeted identity or version, a non-assertion
// target, a live instance, a restated artifact, a missing or unrecorded
// coordinator actor, and separately supplied approval claim fields, every
// refusal landing before any event or version effect.
func TestTerminalDeliveryCorrectionAdmissionRefusals(t *testing.T) {
	t.Parallel()
	const workID = "delivery-correction-refusals"
	s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
	valid := unsignedDeliveryCorrectionRequest(t, s, workID, version, targetEventID, targetSeq, targetPayloadVersion)
	eventsBefore := countWorkflowSubjectEvents(t, s, workID)
	var nonAssertion string
	if err := s.db.QueryRow(`SELECT r.event_id FROM effect_receipts r JOIN workflow_phase_exits x ON x.work_id=r.subject_id AND x.exit_seq=r.effect_seq WHERE r.subject_id=? AND r.kind=? AND r.delivery_assertion=0 AND x.action_id='confirm_premise'`, workID, WorkflowActionCompleted).Scan(&nonAssertion); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		mutate    func(request WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest
		wantKind  FailureKind
		wantPiece string
	}{
		{name: "missing reason", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest { r.Reason = ""; return r }, wantKind: KindInvalidPayload, wantPiece: "incomplete or unbounded fields"},
		{name: "wrong target identity", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.TargetSeq += 40
			return r
		}, wantKind: KindInvalidOperation, wantPiece: "does not match the recorded assertion"},
		{name: "wrong target version", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.TargetPayloadVersion++
			return r
		}, wantKind: KindInvalidOperation, wantPiece: "does not match the recorded assertion"},
		{name: "target is not an assertion", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.TargetEventID = nonAssertion
			return r
		}, wantKind: KindInvalidOperation, wantPiece: "not a recorded delivery assertion"},
		{name: "target event is not recorded", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.TargetEventID = "absent-" + workID
			return r
		}, wantKind: KindProjectionNotFound, wantPiece: "not recorded"},
		{name: "restated artifact", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.DeliveryArtifact = deliveryCorrectionWrongPath
			return r
		}, wantKind: KindInvalidPayload, wantPiece: "delivery_artifact"},
		{name: "repository path merge evidence", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.DeliveryArtifact = "file:internal/store/other.go"
			return r
		}, wantKind: KindInvalidPayload, wantPiece: "delivery_artifact"},
		{name: "missing actor", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.Actor = ""
			return r
		}, wantKind: KindUnauthorized, wantPiece: "requires an authenticated actor"},
		{name: "actor is not recorded", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.Actor = "principal:absent-coordinator"
			return r
		}, wantKind: KindUnauthorized, wantPiece: "requires a recorded coordinator actor"},
	}
	for _, item := range cases {
		request := item.mutate(valid)
		requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, request), item.wantKind, item.wantPiece)
	}
	if after := countWorkflowSubjectEvents(t, s, workID); after != eventsBefore {
		t.Fatalf("the refused corrections appended events: before=%d after=%d", eventsBefore, after)
	}
	if after := readWorkVersion(t, s, workID); after != version {
		t.Fatalf("the refused corrections moved the version to %d, want %d", after, version)
	}
	if rows := routineApprovalActorRows(t, s); rows != 0 {
		t.Fatalf("the refused corrections left %d approval-derived workflow actor rows", rows)
	}
	countEvents := func() int {
		t.Helper()
		var count int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type=? AND subject_id=?`, string(SubjectWorkItem), workID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	invalidArtifacts := []string{
		"https://example.test/a\fb",
		"https://example.test/a\u00a0b",
		"https://",
		"https:///path",
		"https://example.test:bad",
		"https://example.test:/path",
		"https://example.test/a\x7fb",
	}
	for i, artifact := range invalidArtifacts {
		before := countEvents()
		request := valid
		request.DeliveryArtifact = artifact
		request.EventID = fmt.Sprintf("correction-%s-invalid-%d", workID, i)
		err := runDeliveryCorrection(t, s, request)
		requireDeliveryCorrectionRefusal(t, err, KindInvalidPayload, "delivery_artifact")
		if after := countEvents(); after != before {
			t.Fatalf("invalid artifact %q appended events: before=%d after=%d", artifact, before, after)
		}
	}

	// A live instance corrects delivery through record_delivery, not a
	// correction event.
	live := valid
	live.EventID = "correction-" + workID + "-live"
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET instance_state='running' WHERE work_id=?;
		DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, live), KindInvalidOperation, "only to a completed workflow instance")
}

func TestTerminalDeliveryCorrectionRefusalNamesReferenceByteBound(t *testing.T) {
	t.Parallel()
	const workID = "delivery-correction-reference-byte-bound"
	s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
	request := unsignedDeliveryCorrectionRequest(t, s, workID, version, targetEventID, targetSeq, targetPayloadVersion)
	request.DeliveryArtifact = "https://" + strings.Repeat("é", 61)
	before := countWorkflowSubjectEvents(t, s, workID)
	err := runDeliveryCorrection(t, s, request)
	failure := &Failure{}
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "x-maxBytes at $.delivery_artifact") {
		t.Fatalf("oversized delivery_artifact error=%v, want the field-named 128-byte schema failure", err)
	}
	if after := countWorkflowSubjectEvents(t, s, workID); after != before {
		t.Fatalf("oversized delivery_artifact appended events: before=%d after=%d", before, after)
	}
}

func countWorkflowSubjectEvents(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type=? AND subject_id=?`, string(SubjectWorkItem), workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestTerminalDeliveryCorrectionTargetsCurrentAssertionOnly refuses a
// correction that names an assertion which a later assertion already replaced.
func TestTerminalDeliveryCorrectionTargetsCurrentAssertionOnly(t *testing.T) {
	t.Parallel()
	const workID = "delivery-correction-stale-target"
	s := completedDeliveryFixtureWithAssertions(t, workID, 2)
	staleEventID, staleSeq, stalePayloadVersion := deliveryAssertionEvent(t, s, workID, 1)
	request := unsignedDeliveryCorrectionRequest(t, s, workID, readWorkVersion(t, s, workID), staleEventID, staleSeq, stalePayloadVersion)
	requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, request), KindInvalidOperation, "not the current delivery assertion")
}

// TestTerminalDeliveryCorrectionReadReplay proves the read surface shows the
// effective assertion beside the unchanged original, that the original event
// bytes and the closed instance survive the correction, and that a full
// replay from the event log re-derives the same admission and the same read.
// proves check:terminal-delivery-correction-read-replay.
func TestTerminalDeliveryCorrectionReadReplay(t *testing.T) {
	t.Parallel()
	const workID = "delivery-correction-read-replay"
	s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)

	before, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if before.DeliveryAssertion == nil || before.DeliveryAssertion.Correction != nil || before.DeliveryAssertion.Artifact != deliveryCorrectionWrongPath {
		t.Fatalf("read before correction = %+v, want the bare wrong assertion", before.DeliveryAssertion)
	}
	if before.DeliveryAssertion.EffectiveArtifact != deliveryCorrectionWrongPath {
		t.Fatalf("effective artifact without a correction = %q, want the asserted artifact", before.DeliveryAssertion.EffectiveArtifact)
	}
	// The read supplies the exact target identity the correction admission
	// consumes: event id, sequence, and stored payload version.
	target := before.DeliveryAssertion
	if target.EventID != targetEventID || target.Seq != targetSeq || target.TargetPayloadVersion != targetPayloadVersion {
		t.Fatalf("read target identity = (%q,%d,%d), want (%q,%d,%d)", target.EventID, target.Seq, target.TargetPayloadVersion, targetEventID, targetSeq, targetPayloadVersion)
	}
	var originalPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_delivery_state WHERE work_id=? AND assertion_id=?`, workID, targetEventID).Scan(&originalPayload); err != nil {
		t.Fatal(err)
	}

	if err := runDeliveryCorrection(t, s, unsignedDeliveryCorrectionRequest(t, s, workID, version, targetEventID, targetSeq, targetPayloadVersion)); err != nil {
		t.Fatalf("unsigned delivery correction refused: %v", err)
	}

	after, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	assertion := after.DeliveryAssertion
	if assertion == nil || assertion.Correction == nil {
		t.Fatalf("read after correction = %+v, want the assertion with its correction", assertion)
	}
	if assertion.EventID != targetEventID || assertion.Artifact != deliveryCorrectionWrongPath || assertion.State != "asserted" || assertion.TargetPayloadVersion != targetPayloadVersion {
		t.Fatalf("original assertion changed: %+v", assertion)
	}
	correction := assertion.Correction
	if correction.Artifact != deliveryCorrectionMergeRef || correction.EvidenceSource != "coordinator_asserted" || correction.Reason == "" {
		t.Fatalf("correction read = %+v, want the merge evidence under coordinator provenance with no approval claim", correction)
	}
	if assertion.EffectiveArtifact != deliveryCorrectionMergeRef {
		t.Fatalf("effective artifact = %q, want the corrected merge evidence", assertion.EffectiveArtifact)
	}
	var replayedPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_delivery_state WHERE work_id=? AND assertion_id=?`, workID, targetEventID).Scan(&replayedPayload); err != nil {
		t.Fatal(err)
	}
	if originalPayload != replayedPayload {
		t.Fatal("the original assertion event payload was rewritten")
	}
	var eventCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=? AND subject_id=?`, WorkflowDeliveryCorrected, workID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("correction event count=%d, want exactly one appended correction", eventCount)
	}

}
