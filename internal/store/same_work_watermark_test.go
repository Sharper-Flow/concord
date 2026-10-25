package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"
)

func TestSameWorkWatermarksWithoutLog(t *testing.T) {
	for _, reader := range []string{"work_pin", "continuity"} {
		t.Run(reader, func(t *testing.T) {
			s := openTemp(t)
			seedWork(t, s, "watermark-work")
			seedWork(t, s, "watermark-other")
			ctx := context.Background()
			var want int64
			if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id='watermark-work'`).Scan(&want); err != nil {
				t.Fatal(err)
			}
			read := func() string {
				t.Helper()
				tx, err := s.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if reader == "work_pin" {
					var pin WorkPin
					if err := workPinReadIdentityTx(ctx, tx, "watermark-work", &pin); err != nil {
						t.Fatal(err)
					}
					return pin.Watermark
				}
				var snapshot ContinuitySnapshot
				if err := continuityReadTrailingTx(ctx, tx, "watermark-work", &snapshot); err != nil {
					t.Fatal(err)
				}
				return snapshot.Watermark
			}
			if got := read(); got != fmt.Sprintf("seq:%d", want) || want == 0 {
				t.Fatalf("watermark=%s, want seq:%d", got, want)
			}
			if _, err := s.db.Exec(`ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
				t.Fatal(err)
			}
			if got := read(); got != fmt.Sprintf("seq:%d", want) {
				t.Fatalf("log-blind watermark=%s, want seq:%d", got, want)
			}
		})
	}
}

// The old watermark selects by subject, not kind. Exercise the frontier writer
// over the whole registry, including kinds normally written for other subjects.
func TestSameWorkEffectFrontierEveryRegisteredKind(t *testing.T) {
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY,effect_seq INTEGER NOT NULL DEFAULT 0,last_progress_at TEXT NOT NULL DEFAULT ''); INSERT INTO work_items(id) VALUES('work'); CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,kind TEXT,subject_type TEXT,subject_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(eventKindRegistry))
	for kind := range eventKindRegistry {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i, kind := range kinds {
		seq := int64(i + 1)
		if _, err := tx.Exec(`INSERT INTO domain_events VALUES(?,?,?,?)`, seq, kind, SubjectWorkItem, "work"); err != nil {
			t.Fatal(err)
		}
		if err := advanceWorkEffectSeqTx(ctx, tx, Event{Seq: seq, EventID: kind, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: "work", OccurredAt: time.Unix(seq, 0).UTC()}); err != nil {
			t.Fatal(err)
		}
		got, err := readWorkEffectSeq(ctx, tx, "work")
		var want int64
		if queryErr := tx.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id='work'`).Scan(&want); queryErr != nil {
			t.Fatal(queryErr)
		}
		if err != nil || got != want {
			t.Fatalf("%s: frontier=%d want=%d err=%v", kind, got, want, err)
		}
	}
}
