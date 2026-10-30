package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// Migration fixtures retain source bytes without running a historical fold.
func insertHistoricalEvent(t *testing.T, s *Store, event Event) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, event.EventID, event.Kind, string(event.SubjectType), event.SubjectID, event.Actor, event.OccurredAt.Format(time.RFC3339Nano), event.PayloadVersion, event.Payload); err != nil {
		t.Fatal(err)
	}
}

func storedEventPayload(t *testing.T, s *Store, eventID string) []byte {
	t.Helper()
	var payload []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, eventID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func legacyNativeRunWriterPayload(t *testing.T, runID string, mutate func(map[string]any)) []byte {
	t.Helper()
	const subjectRef = "/service/checkout"
	sum := sha256.Sum256([]byte(subjectRef))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	payload := map[string]any{
		"work_id": "work-w", "run_id": runID, "native_subject_ref": subjectRef, "subject_digest": digest, "phase": "start", "status": "started",
		"evidence_ref": "run-legacy-evidence", "evidence_digest": "legacy-evidence-digest", "asserted_at": time.Unix(50, 0).UTC().Format(time.RFC3339Nano),
		"reporting_authority_ref": "client-legacy", "actor_ref": "agent:legacy", "capture_method": "trusted_client_report",
		"observed_universe":    map[string]any{"shape": "item", "applied_scope": subjectRef, "anchor": digest, "coverage": "complete", "observed_count": 1, "observed_refs": []string{runID}, "total": "eq(1)", "completion_evidence": "closed_structure_digest", "canonical_identity_key": "work-w/" + runID, "omissions": []string{}},
		"freshness_policy_ref": "policy/native-run-report/freshness@cd-0040", "divergence_policy_ref": "policy/native-run-report/divergence@cd-0040", "expected_version": 2, "resulting_version": 3,
	}
	if mutate != nil {
		mutate(payload)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func legacyNativeRunEvent(eventID string, payload []byte) Event {
	return Event{EventID: eventID, Kind: WorkflowNativeRunRecorded, SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "agent:legacy", OccurredAt: time.Unix(51, 0).UTC(), PayloadVersion: 1, Payload: payload}
}

func jsonMember(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
