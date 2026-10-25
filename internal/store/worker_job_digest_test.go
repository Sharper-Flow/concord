package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkerJobV1MeasurementBasisDigestSurvivesStoredRoundTrip(t *testing.T) {
	t.Parallel()
	// Digest-covered content uses the recording structs' field order, not
	// the key-sorted map order of the retained event.
	const content = `{"job_id":"job:retired-basis","revision":1,"contract_version":1,"objective":"Verify the recorded outcome","stopping_condition":"Stop after verification","project_scope":"project","path_scope":["src"],"predicate_ids":[],"checks":[],"prerequisites":[],"unresolved_refs":[],"production_size_max":100,"measurement_basis":{"base_commit":"0123456789012345678901234567890123456789","paths":[{"path":"src/example.go","class":"production"},{"path":"src/generated.go","class":"generated","generator_ref":"tools/generate.go"}]}}`
	sum := sha256.Sum256([]byte(content))
	want := "sha256:" + hex.EncodeToString(sum[:])
	var original WorkerJobRecordedPayload
	if err := json.Unmarshal([]byte(content), &original); err != nil {
		t.Fatal(err)
	}
	original.Digest = want
	if got := DeriveWorkerJobDigest(original); got != want {
		t.Fatalf("record-time digest = %s, want %s", got, want)
	}
	event, err := workerJobRecordedEvent("recorded-job", WorkflowActionExecutionRequest{
		WorkID: "work:recorded-job", Now: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
	}, "actor:coordinator", 1, &original)
	if err != nil {
		t.Fatal(err)
	}
	if event.PayloadVersion != 1 {
		t.Fatalf("recorded payload version = %d, want 1", event.PayloadVersion)
	}
	if !bytes.Contains(event.Payload, []byte(`"paths":[{"class":"production","path":"src/example.go"}`)) {
		t.Fatal("stored event did not key-sort its measurement paths")
	}
	var stored WorkerJobRecordedPayload
	if err := json.Unmarshal(event.Payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Digest != want {
		t.Fatalf("stored digest = %s, want unchanged %s", stored.Digest, want)
	}
	if got := DeriveWorkerJobDigest(stored); got != want {
		t.Fatalf("stored round-trip digest = %s, want recorded %s", got, want)
	}
	if err := validateWorkerJobRecordedPayload(event, stored); err == nil || !strings.Contains(err.Error(), "measurement_basis are retired") {
		t.Fatalf("live size-bearing recording = %v, want retired-member refusal", err)
	}
	stored.Objective = "A different outcome"
	if got := DeriveWorkerJobDigest(stored); got == want {
		t.Fatal("changed content retained the recorded digest")
	}
}
