package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecordedHistoricalDigestlessReadbackDispatchReplays(t *testing.T) {
	requireHistoricalResearchLane(t)
	s, _, _ := worktreeFixture(t)
	event := histDispatchEvent(t, "digestless-readback-dispatch", "attempt-digestless", 3, time.Unix(30, 0).UTC(), map[string]any{
		"readback_model": "", "host_provenance": histDeclaredHostProvenance(),
	})
	insertHistoricalEvent(t, s, event)
	log := histEventRows(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("digest-less recorded readback-era dispatch must replay: %v", err)
	}
	if got := histAttemptRow(t, s.db, "attempt-digestless"); got.readback != "" || got.lifecycle != "dispatched" {
		t.Fatalf("digest-less projection = %+v, want an ordinary dispatch with unchanged empty readback", got)
	}
	if !reflect.DeepEqual(log, histEventRows(t, s)) {
		t.Fatal("replay changed the retained event bytes, headers, or order")
	}
	prepared, err := prepareRecordedEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	var payload WorkerDispatchedPayload
	if err := json.Unmarshal(prepared.current.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PacketDigest != "sha256:"+strings.Repeat("0", 64) || prepared.current.replaySourcePayloadVersion != 3 {
		t.Fatalf("normalization lost the unavailable packet marker or source version: %+v, source=%d", payload, prepared.current.replaySourcePayloadVersion)
	}
	t.Run("closed recorded schema", func(t *testing.T) {
		testDigestlessDispatchBoundaries(t, event)
	})
}

func testDigestlessDispatchBoundaries(t *testing.T, event Event) {
	t.Helper()
	refuse := func(t *testing.T, event Event) {
		t.Helper()
		_, err := prepareRecordedEvent(event)
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
			t.Fatalf("recorded dispatch must refuse with invalid_payload, got %v", err)
		}
	}
	for _, member := range []string{"attempt_id", "lane_id", "lane_version", "lane_digest", "capability_class", "packet_schema_version", "report_schema_version", "host_provenance"} {
		t.Run("missing "+member, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			delete(payload, member)
			refuse(t, histEventWithPayload(t, event, payload))
		})
	}
	for name, member := range map[string]string{
		"unknown member":      `"unexpected":true`,
		"later actor":         `"lane_actor_ref":"agent/research"`,
		"later job":           `"worker_job":null`,
		"malformed digest":    `"packet_digest":"not-a-digest"`,
		"empty digest":        `"packet_digest":""`,
		"null digest":         `"packet_digest":null`,
		"terminal wrong type": `"terminal":{}`,
	} {
		t.Run(name, func(t *testing.T) { refuse(t, withPayloadMember(t, event, member)) })
	}
	for _, version := range []int{4, 5} {
		refuse(t, withEventVersion(event, version))
	}
	refuse(t, withReplacedPayloadText(t, event, `"packet_schema_version":"1.0"`, `"packet_schema_version":"1.1"`))
	refuse(t, withReplacedPayloadText(t, event, `"readback_model":""`, `"readback_model":null`))
	refuse(t, withReplacedPayloadText(t, event, `"readback_model":""`, `"readback_model":7`))
	if _, err := prepareRegisteredEvent(event); err == nil || !strings.Contains(err.Error(), "packet_digest must be a sha256 digest") {
		t.Fatalf("live dispatch must retain its packet-digest requirement, got %v", err)
	}
}
