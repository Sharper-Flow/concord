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
	if len(definitions) != 4 {
		t.Fatalf("lane count = %d, want 4", len(definitions))
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
	unpinned.PinnedModel = ""
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
	var state, failureKind, readback string
	if err := s.DB().QueryRow(`SELECT lifecycle_state,failure_kind,readback_model FROM worker_attempts WHERE attempt_id=?`, "dispatch-mismatch").Scan(&state, &failureKind, &readback); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || failureKind != string(KindModelIdentityMismatch) || readback != "openai/fallback-model" {
		t.Fatalf("mismatch projection = %s/%s/%s", state, failureKind, readback)
	}
	if err := ValidateWorkerCompletion(lane.PinnedModel, "openai/fallback-model"); !hasFailureKind(err, KindModelIdentityMismatch) {
		t.Fatalf("pre-fold mismatch = %v, want %s", err, KindModelIdentityMismatch)
	}
	before := workerProjectionSnapshot(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("rebuild changed worker projection:\n%s\nwant\n%s", after, before)
	}
}

func TestWorkerCompletedAndFailedEventsRetainD5Evidence(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[2]
	dispatchID := "dispatch-complete"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("worker-complete", dispatchID, lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompleteEvent("worker-complete", "complete", dispatchID, lane.PinnedModel)}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchEvent("worker-failed", "dispatch-failed", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	failed := Event{EventID: "failed", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: "worker-failed", Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: "dispatch-failed", ReadbackModel: lane.PinnedModel, FailureKind: WorkerFailureWorkerError, Detail: "bounded worker error"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{failed}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM worker_attempts WHERE lane_id=? AND lane_version=? AND lane_digest=? AND capability_class=? AND routing_policy_version=? AND resolved_model=? AND readback_model<>'' AND packet_schema_version=? AND report_schema_version=?`, lane.ID, lane.Version, lane.Digest, lane.CapabilityClass, "routing-v1", lane.PinnedModel, WorkerPacketSchemaVersion, WorkerReportSchemaVersion).Scan(&count); err != nil {
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
		"capability_class": lane.CapabilityClass, "routing_policy_version": "routing-v1",
		"resolved_model": lane.PinnedModel, "packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
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
	rows, err := s.DB().Query(`SELECT work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,resolved_model,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,COALESCE(completed_at,''),COALESCE(failed_at,'') FROM worker_attempts ORDER BY attempt_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result strings.Builder
	for rows.Next() {
		var values [17]string
		var version int64
		if err := rows.Scan(&values[0], &values[1], &values[2], &version, &values[4], &values[5], &values[6], &values[7], &values[8], &values[9], &values[10], &values[11], &values[12], &values[13], &values[14], &values[15], &values[16]); err != nil {
			t.Fatal(err)
		}
		result.WriteString(strings.Join([]string{values[0], values[1], values[2], fmt.Sprintf("%d", version), values[4], values[5], values[6], values[7], values[8], values[9], values[10], values[11], values[12], values[13], values[14], values[15], values[16]}, "|"))
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
