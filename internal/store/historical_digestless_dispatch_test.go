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
}
