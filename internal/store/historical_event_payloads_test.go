package store

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These recorded values seed the one-time projection migration, not a replay.
const (
	historicalResearchLaneDigest  = "sha256:3969ceda54cc6be1532877e6d5b1dc5530c280ff77835f43286c4a0ad37e861b"
	historicalRoutingPolicyToken  = "routing-v1"
	historicalRoutingPolicyDigest = "sha256:34718d4f686c90b4806533ad1cc9eb1eab7c3cce0f4e732dcdaa70d73aa9f736"
)

func histDispatchEvent(t *testing.T, eventID, attemptID string, version int, occurredAt time.Time, members map[string]any) Event {
	t.Helper()
	payload := map[string]any{
		"attempt_id": attemptID, "lane_id": "research", "lane_version": 1,
		"lane_digest": historicalResearchLaneDigest, "capability_class": "research",
		"packet_schema_version": "1.0", "report_schema_version": "1.0",
	}
	for member, value := range members {
		payload[member] = value
	}
	return Event{EventID: eventID, Kind: "worker.dispatched", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "host:fixture", OccurredAt: occurredAt, PayloadVersion: version, Payload: mustJSONValue(payload)}
}

func histDeclaredHostProvenance() map[string]any {
	return map[string]any{"digest": "sha256:" + strings.Repeat("ab", 32), "sources": []any{map[string]any{"kind": "agents_md", "path": "AGENTS.md", "sha256": "sha256:" + strings.Repeat("cd", 32)}}}
}

type histEventRow struct {
	Seq                                                      int64
	EventID, Kind, SubjectType, SubjectID, Actor, OccurredAt string
	Version                                                  int
	Payload                                                  string
}

func histEventRows(t *testing.T, s *Store) []histEventRow {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []histEventRow
	for rows.Next() {
		var row histEventRow
		var payload []byte
		if err := rows.Scan(&row.Seq, &row.EventID, &row.Kind, &row.SubjectType, &row.SubjectID, &row.Actor, &row.OccurredAt, &row.Version, &payload); err != nil {
			t.Fatal(err)
		}
		row.Payload = string(payload)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

type currentEffectRow struct {
	Seq                                          int64
	RelationSeq                                  sql.NullInt64
	EventID, Kind, SubjectType, SubjectID, Actor string
	Version, DeliveryAssertion                   int
	PayloadDigest                                string
}

func currentEffectRows(t *testing.T, s *Store) []currentEffectRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT effect_seq,event_id,kind,subject_type,subject_id,actor,payload_version,payload_digest,delivery_assertion,relation_seq FROM effect_receipts ORDER BY effect_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []currentEffectRow
	for rows.Next() {
		var row currentEffectRow
		if err := rows.Scan(&row.Seq, &row.EventID, &row.Kind, &row.SubjectType, &row.SubjectID, &row.Actor, &row.Version, &row.PayloadDigest, &row.DeliveryAssertion, &row.RelationSeq); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLiveWriteRoutesRefuseRecordedOldShapesAndCommitNothing(t *testing.T) {
	t.Parallel()
	s, _, _ := worktreeFixture(t)
	ctx := context.Background()
	before := outsideRepairDatabaseSnapshot(t, s)
	version := readWorkVersion(t, s, "work-w")
	native := legacyNativeRunWriterPayload(t, "run-live", nil)
	cases := map[string]Event{
		"native run map universe": legacyNativeRunEvent("live-old-native", native),
	}
	for name, event := range cases {
		event.EventID = "refused-" + strings.ReplaceAll(name, " ", "-")
		event.SubjectType, event.SubjectID, event.Actor = SubjectWorkItem, "work-w", "host:fixture"
		event.OccurredAt = time.Unix(40, 0).UTC()
		event.PayloadVersion = eventKindRegistry[event.Kind].CurrentVersion
		receipts := currentEffectRows(t, s)
		err := ApplyOperation(ctx, s, Operation{Events: []Event{event}})
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
			t.Fatalf("live %s = %v, want invalid_payload", name, err)
		}
		if !reflect.DeepEqual(currentEffectRows(t, s), receipts) || outsideRepairDatabaseSnapshot(t, s) != before || readWorkVersion(t, s, "work-w") != version {
			t.Fatalf("live %s refusal changed state", name)
		}
	}
	assertFoldGuardEmpty(t, s)
}
