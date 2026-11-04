package store

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

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
	s := worktreeFixture(t)
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
