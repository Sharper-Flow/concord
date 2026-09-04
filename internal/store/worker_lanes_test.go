package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLaneRegistryIsGeneratedClosedAndDigestPinned(t *testing.T) {
	definitions := BuiltinLaneDefinitions()
	if len(definitions) != 5 {
		t.Fatalf("lane count = %d, want 5", len(definitions))
	}
	for _, definition := range definitions {
		if err := ValidateLaneDefinition(definition); err != nil {
			t.Fatalf("ValidateLaneDefinition(%s) = %v", definition.ID, err)
		}
		got, err := LaneDefinitionDigest(definition)
		if err != nil {
			t.Fatalf("LaneDefinitionDigest(%s) error = %v", definition.ID, err)
		}
		if got != definition.Digest {
			t.Fatalf("lane %s digest = %s, generated %s", definition.ID, got, definition.Digest)
		}
		if _, err := LookupLane(definition.ID, definition.Version, definition.Digest); err != nil {
			t.Fatalf("LookupLane(%s) error = %v", definition.ID, err)
		}
	}
	if _, err := LookupLane("unknown", 1, definitions[0].Digest); !hasFailureKind(err, KindLaneDefinitionNotRegistered) {
		t.Fatalf("unknown lane error = %v, want %s", err, KindLaneDefinitionNotRegistered)
	}
	if _, err := LookupLane(definitions[0].ID, 2, definitions[0].Digest); !hasFailureKind(err, KindLaneDefinitionNotRegistered) {
		t.Fatalf("unknown lane version error = %v, want %s", err, KindLaneDefinitionNotRegistered)
	}
	if _, err := LookupLane(definitions[0].ID, definitions[0].Version, "sha256:"+strings.Repeat("0", 64)); !hasFailureKind(err, KindLaneDefinitionDigestMismatch) {
		t.Fatalf("digest mismatch error = %v, want %s", err, KindLaneDefinitionDigestMismatch)
	}
	unpinned := definitions[0]
	unpinned.CapabilityClass = ""
	if err := ValidateLaneDefinition(unpinned); !hasFailureKind(err, KindLaneDefinitionInvalid) {
		t.Fatalf("unpinned lane error = %v, want %s", err, KindLaneDefinitionInvalid)
	}
	digestBytes, err := os.ReadFile("../../contracts/agent-lanes.digest")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(digestBytes)) != LaneRegistryManifestDigest {
		t.Fatalf("manifest digest file = %q, generated Go = %q", strings.TrimSpace(string(digestBytes)), LaneRegistryManifestDigest)
	}
	manifestBytes, err := os.ReadFile("../../contracts/agent-lanes.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != LaneRegistryManifestDigest {
		t.Fatalf("manifest canonical digest = %s, generated Go = %s", got, LaneRegistryManifestDigest)
	}
}

func TestWorkerEventsRejectUnknownLanePacketAndPayloadFields(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	unknown := workerDispatchEvent("worker-invalid", "dispatch-invalid", lane, map[string]any{
		"packet_schema_version": "9.0",
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{unknown}}); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("packet mismatch error = %v, want %s", err, KindInvalidPayload)
	}
	badDigest := workerDispatchEvent("worker-invalid", "dispatch-invalid-digest", lane, map[string]any{
		"lane_digest": "sha256:" + strings.Repeat("0", 64),
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{badDigest}}); !hasFailureKind(err, KindLaneDefinitionDigestMismatch) {
		t.Fatalf("digest mismatch error = %v, want %s", err, KindLaneDefinitionDigestMismatch)
	}
	unknownField := workerDispatchEvent("worker-invalid", "dispatch-invalid-field", lane, map[string]any{
		"unknown_field": true,
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{unknownField}}); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("unknown field error = %v, want %s", err, KindInvalidPayload)
	}
	if got := countRows(t, s, "worker_attempts"); got != 0 {
		t.Fatalf("invalid dispatch rows = %d, want 0", got)
	}
}

func TestWorkerCompletionMismatchIsDurableTypedFailureAndRebuildDeterministic(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[1]
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("worker-mismatch", "dispatch-mismatch", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	mismatch := workerCompleteEvent("worker-mismatch", "complete-mismatch", "dispatch-mismatch", "openai/fallback-model")
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{mismatch}}); err != nil {
		t.Fatal(err)
	}
	// CD-0058: Concord records the host readback verbatim. A completion that
	// names a different readback than the dispatch is accepted as a normal
	// completion; the dispatch and terminal readback are both recorded. The
	// store no longer asserts a model_identity_mismatch.
	var state, readback string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id=?`, "dispatch-mismatch").Scan(&state, &readback); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || readback != "openai/fallback-model" {
		t.Fatalf("readback projection = %s/%s, want completed/openai/fallback-model", state, readback)
	}
	before := workerProjectionSnapshot(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("rebuild changed worker projection:\n%s\nwant\n%s", after, before)
	}
}

func TestWorkerTerminalTransitionsAreSingleUseAndSubjectBound(t *testing.T) {
	t.Run("failed then completed", func(t *testing.T) {
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("terminal-failed-first", "terminal-failed-first-attempt", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		failed := workerFailedEvent("terminal-failed-first", "terminal-failed-first-failed", "terminal-failed-first-attempt", preferredModelForLane(lane))
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{failed}}); err != nil {
			t.Fatal(err)
		}
		before := workerProjectionSnapshot(t, s)
		beforeEvents := countRows(t, s, "domain_events")
		assertRejectedWorkerTerminal(t, s, workerCompleteEvent("terminal-failed-first", "terminal-failed-first-completed", "terminal-failed-first-attempt", preferredModelForLane(lane)), KindProjectionConflict, before, beforeEvents)
	})
	t.Run("completed then failed", func(t *testing.T) {
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("terminal-completed-first", "terminal-completed-first-attempt", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompleteEvent("terminal-completed-first", "terminal-completed-first-completed", "terminal-completed-first-attempt", preferredModelForLane(lane))}}); err != nil {
			t.Fatal(err)
		}
		before := workerProjectionSnapshot(t, s)
		beforeEvents := countRows(t, s, "domain_events")
		assertRejectedWorkerTerminal(t, s, workerFailedEvent("terminal-completed-first", "terminal-completed-first-failed", "terminal-completed-first-attempt", preferredModelForLane(lane)), KindProjectionConflict, before, beforeEvents)
	})
	t.Run("duplicate completed event identity differs", func(t *testing.T) {
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("terminal-duplicate", "terminal-duplicate-attempt", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompleteEvent("terminal-duplicate", "terminal-duplicate-first", "terminal-duplicate-attempt", preferredModelForLane(lane))}}); err != nil {
			t.Fatal(err)
		}
		before := workerProjectionSnapshot(t, s)
		beforeEvents := countRows(t, s, "domain_events")
		assertRejectedWorkerTerminal(t, s, workerCompleteEvent("terminal-duplicate", "terminal-duplicate-second", "terminal-duplicate-attempt", preferredModelForLane(lane)), KindProjectionConflict, before, beforeEvents)
	})
	t.Run("foreign completed subject", func(t *testing.T) {
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("terminal-foreign-owner", "terminal-foreign-attempt", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		before := workerProjectionSnapshot(t, s)
		beforeEvents := countRows(t, s, "domain_events")
		assertRejectedWorkerTerminal(t, s, workerCompleteEvent("terminal-foreign-subject", "terminal-foreign-completed", "terminal-foreign-attempt", preferredModelForLane(lane)), KindInvalidOperation, before, beforeEvents)
	})
	t.Run("foreign failed subject", func(t *testing.T) {
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("terminal-foreign-failed-owner", "terminal-foreign-failed-attempt", lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		before := workerProjectionSnapshot(t, s)
		beforeEvents := countRows(t, s, "domain_events")
		assertRejectedWorkerTerminal(t, s, workerFailedEvent("terminal-foreign-failed-subject", "terminal-foreign-failed-event", "terminal-foreign-failed-attempt", preferredModelForLane(lane)), KindInvalidOperation, before, beforeEvents)
	})
	t.Run("model mismatch is no longer a terminal kind", func(t *testing.T) {
		// CD-0058: the readback mismatch no longer records a typed failure. A
		// completion that names a different readback than the dispatch
		// succeeds as a normal completion; the readback column records what
		// the host reported. A second completion is still rejected because
		// the attempt is already terminal.
		s := openTemp(t)
		lane := BuiltinLaneDefinitions()[1]
		attemptID := "terminal-model-mismatch-attempt"
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("terminal-model-mismatch", attemptID, lane, nil)}}); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompleteEvent("terminal-model-mismatch", "terminal-model-mismatch-event", attemptID, "openai/fallback-model")}}); err != nil {
			t.Fatal(err)
		}
		before := workerProjectionSnapshot(t, s)
		beforeEvents := countRows(t, s, "domain_events")
		assertRejectedWorkerTerminal(t, s, workerCompleteEvent("terminal-model-mismatch", "terminal-model-recovery", attemptID, preferredModelForLane(lane)), KindProjectionConflict, before, beforeEvents)
		var state, readback string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&state, &readback); err != nil {
			t.Fatal(err)
		}
		if state != "completed" || readback != "openai/fallback-model" {
			t.Fatalf("post-mismatch terminal state=%q readback=%q", state, readback)
		}
	})
}

func assertRejectedWorkerTerminal(t *testing.T, s *Store, event Event, wantKind FailureKind, beforeProjection string, beforeEvents int) {
	t.Helper()
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
	if !hasFailureKind(err, wantKind) {
		t.Fatalf("terminal event failure=%v, want %s", err, wantKind)
	}
	if after := workerProjectionSnapshot(t, s); after != beforeProjection {
		t.Fatalf("rejected terminal event changed worker projection:\n%s\nwant\n%s", after, beforeProjection)
	}
	if got := countRows(t, s, "domain_events"); got != beforeEvents {
		t.Fatalf("rejected terminal event count=%d, want %d", got, beforeEvents)
	}
}

func workerFailedEvent(workID, eventID, attemptID, model string) Event {
	return Event{EventID: eventID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: model, FailureKind: WorkerFailureWorkerError, Detail: "bounded worker error"})}
}

func TestWorkerCompletedAndFailedEventsRetainD5Evidence(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[2]
	dispatchID := "dispatch-complete"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("worker-complete", dispatchID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompleteEvent("worker-complete", "complete", dispatchID, preferredModelForLane(lane))}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("worker-failed", "dispatch-failed", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	failed := Event{EventID: "failed", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: "worker-failed", Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: "dispatch-failed", ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureWorkerError, Detail: "bounded worker error"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{failed}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE lane_id=? AND lane_version=? AND lane_digest=? AND capability_class=? AND readback_model<>'' AND packet_schema_version=? AND report_schema_version=?`, lane.ID, lane.Version, lane.Digest, lane.CapabilityClass, WorkerPacketSchemaVersion, WorkerReportSchemaVersion).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("D5 evidence rows = %d, want 2", count)
	}
}

func workerDispatchEvent(workID, eventID string, lane LaneDefinition, overrides map[string]any) Event {
	payload := map[string]any{
		"attempt_id": eventID,
		"lane_id":    lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"capability_class":      lane.CapabilityClass,
		"readback_model":        preferredModelForLane(lane),
		"packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
		// CD-0067 D6: the dispatched event carries the canonical packet
		// digest the dispatch authorization recorded. The test helper
		// supplies a stable sha256 so v3 appends pass the validator; a
		// test that needs a different value can override packet_digest
		// through the overrides map.
		"packet_digest": "sha256:" + strings.Repeat("f", 64),
	}
	for key, value := range overrides {
		payload[key] = value
	}
	return Event{EventID: eventID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(payload)}
}

func workerCompleteEvent(workID, eventID, attemptID, model string) Event {
	return Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "readback_model": model, "report_schema_version": WorkerReportSchemaVersion})}
}

func workerProjectionSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,COALESCE(completed_at,''),COALESCE(failed_at,'') FROM worker_attempts ORDER BY attempt_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result strings.Builder
	for rows.Next() {
		var values [15]string
		var version int64
		if err := rows.Scan(&values[0], &values[1], &values[2], &version, &values[4], &values[5], &values[6], &values[7], &values[8], &values[9], &values[10], &values[11], &values[12], &values[13], &values[14]); err != nil {
			t.Fatal(err)
		}
		result.WriteString(strings.Join([]string{values[0], values[1], values[2], fmt.Sprintf("%d", version), values[4], values[5], values[6], values[7], values[8], values[9], values[10], values[11], values[12], values[13], values[14]}, "|"))
		result.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result.String()
}

func mustJSONValue(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func hasFailureKind(err error, want FailureKind) bool {
	var failure *Failure
	return errors.As(err, &failure) && failure.Kind == want
}

// preferredModelForLane is a test fixture: a literal model string keyed by the
// lane's capability class. The store no longer carries a routing policy, so
// the previous lookup-based helper had to be re-homed; this stand-in keeps
// D6 evidence (workflow_actors.execution_model) populatable and gives worker
// dispatch fixtures a stable readback_model value.
func preferredModelForLane(lane LaneDefinition) string {
	switch lane.CapabilityClass {
	case "review":
		return "zai-coding-plan/glm-5.3"
	default:
		return "openai/gpt-5.6-luna"
	}
}

// CD-0017 D4/acceptance: a generic host agent is never a Concord lane. The
// adapter denies delegation in generated frontmatter, but the store is the
// authority — an unregistered identifier must fail closed before any mutation.
func TestGenericHostAgentsAreNotDispatchableLanes(t *testing.T) {
	s := openTemp(t)
	definitions := BuiltinLaneDefinitions()
	registered := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		registered[definition.ID] = struct{}{}
	}
	for _, generic := range []string{"general", "explore", "build", "plan", "subagent"} {
		if _, exists := registered[generic]; exists {
			t.Fatalf("generic host agent %q is registered as a Concord lane", generic)
		}
		if _, err := LookupLane(generic, definitions[0].Version, definitions[0].Digest); !hasFailureKind(err, KindLaneDefinitionNotRegistered) {
			t.Fatalf("LookupLane(%s) error = %v, want %s", generic, err, KindLaneDefinitionNotRegistered)
		}
		event := workerDispatchEvent("worker-generic", "dispatch-generic-"+generic, definitions[0], map[string]any{
			"lane_id": generic,
		})
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}}); err == nil {
			t.Fatalf("dispatch of generic host agent %q was accepted", generic)
		}
	}
	if got := countRows(t, s, "worker_attempts"); got != 0 {
		t.Fatalf("generic dispatch rows = %d, want 0", got)
	}
}

// CD-0034: host prompt provenance is declared at dispatch — validated closed,
// required for v3 evidence at the CLI boundary, and upcasted with an honest
// legacy marker for v1/v2 history. Provenance is unaffected by CD-0058; only
// the routing-policy fields left the dispatch payload.
func TestWorkerHostProvenanceValidation(t *testing.T) {
	valid := &WorkerHostProvenance{Digest: "sha256:" + strings.Repeat("a", 64), Sources: []WorkerHostProvenanceSource{
		{Kind: "agent_definition", Path: "/agents/concord-research.md", SHA256: "sha256:" + strings.Repeat("b", 64)},
		{Kind: "agents_md", Path: "/repo/AGENTS.md", SHA256: "sha256:" + strings.Repeat("c", 64)},
		{Kind: "unenumerated"},
	}}
	if err := ValidateWorkerHostProvenance(valid); err != nil {
		t.Fatalf("valid provenance refused: %v", err)
	}
	if err := ValidateWorkerHostProvenance(nil); err != nil {
		t.Fatalf("nil provenance (payload < v3) must pass validation itself: %v", err)
	}
	bad := []struct {
		name string
		p    *WorkerHostProvenance
	}{
		{"bad digest", &WorkerHostProvenance{Digest: "sha256:short", Sources: valid.Sources}},
		{"no sources", &WorkerHostProvenance{Digest: valid.Digest, Sources: nil}},
		{"unknown kind", &WorkerHostProvenance{Digest: valid.Digest, Sources: []WorkerHostProvenanceSource{{Kind: "mystery", Path: "/x", SHA256: valid.Digest}}}},
		{"unenumerated with hash", &WorkerHostProvenance{Digest: valid.Digest, Sources: []WorkerHostProvenanceSource{{Kind: "unenumerated", Path: "/x", SHA256: valid.Digest}}}},
		{"enumerated without hash", &WorkerHostProvenance{Digest: valid.Digest, Sources: []WorkerHostProvenanceSource{{Kind: "agents_md", Path: "/x"}}}},
		{"duplicate source", &WorkerHostProvenance{Digest: valid.Digest, Sources: []WorkerHostProvenanceSource{{Kind: "agents_md", Path: "/x", SHA256: valid.Digest}, {Kind: "agents_md", Path: "/x", SHA256: valid.Digest}}}},
		// Two unenumerated surfaces with one name are one source named twice.
		// Three distinct names are three sources; that distinction is what
		// the name exists to carry (CD-0034 "recorded by name").
		{"duplicate unenumerated name", &WorkerHostProvenance{Digest: valid.Digest, Sources: []WorkerHostProvenanceSource{{Kind: "unenumerated", Path: "provider_behavioral_hints"}, {Kind: "unenumerated", Path: "provider_behavioral_hints"}}}},
	}
	for _, tc := range bad {
		if err := ValidateWorkerHostProvenance(tc.p); err == nil {
			t.Fatalf("%s must refuse", tc.name)
		}
	}
	named := &WorkerHostProvenance{Digest: valid.Digest, Sources: []WorkerHostProvenanceSource{
		{Kind: "unenumerated", Path: "provider_behavioral_hints"},
		{Kind: "unenumerated", Path: "output_voice_overlays"},
		{Kind: "unenumerated", Path: "mcp_tool_definition_prompt"},
	}}
	if err := ValidateWorkerHostProvenance(named); err != nil {
		t.Fatalf("three distinctly named unenumerated sources refused: %v", err)
	}
}

// CD-0034: a v3 dispatch carries host provenance into durable evidence. The
// fold accepts a v3 payload without provenance because the CLI boundary is the
// gate that requires it.
func TestWorkerDispatchV3CarriesProvenanceIntoDurableEvidence(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	provenance := map[string]any{
		"digest": "sha256:" + strings.Repeat("d", 64),
		"sources": []map[string]any{
			{"kind": "agent_definition", "path": "/agents/concord-research.md", "sha256": "sha256:" + strings.Repeat("e", 64)},
			{"kind": "unenumerated"},
		},
	}
	withProv := workerDispatchEvent("work-prov", "attempt-prov", lane, map[string]any{"host_provenance": provenance})
	withProv.PayloadVersion = 3
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{withProv}}); err != nil {
		t.Fatalf("v3 dispatch with provenance refused: %v", err)
	}
	var storedPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id='attempt-prov'`).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(storedPayload, strings.Repeat("d", 16)) {
		t.Fatalf("provenance not durable in evidence: %s", storedPayload)
	}

	noProv := workerDispatchEvent("work-prov", "attempt-noprov", lane, nil)
	noProv.PayloadVersion = 3
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{noProv}}); err != nil {
		t.Fatalf("v3 without provenance should be accepted at fold level (CLI gates it): %v", err)
	}
}
