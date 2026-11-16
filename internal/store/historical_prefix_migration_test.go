package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func advanceHistoricalPrefix(t *testing.T, _ *sql.DB, through int) {
	t.Helper()
	t.Fatalf("historical migration target %d is unsupported", through)
}

type historicalFactCorpus struct {
	events  []Event
	parents func(*testing.T, *Store, string)
	write   func(context.Context, *sql.Tx, Event) error
}

func (c historicalFactCorpus) prefix(t *testing.T, n int) *Store {
	t.Helper()
	t.Fatalf("historical migration target %d is unsupported", n)
	return nil
}

func (c historicalFactCorpus) current(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	c.parents(t, s, "write_guard")
	for _, e := range c.events {
		err := s.Transact(t.Context(), func(tx *Transaction) error {
			scope, err := beginWrite(t.Context(), tx.tx)
			if err != nil {
				return err
			}
			if e.Seq > 0 {
				var last int64
				if err := tx.tx.QueryRowContext(t.Context(), `SELECT last FROM store_sequences WHERE name='effect'`).Scan(&last); err != nil {
					return err
				}
				if last >= e.Seq {
					return fmt.Errorf("corpus effect coordinate %d is not after %d", e.Seq, last)
				}
				for last < e.Seq-1 {
					if _, err := allocateStoreSequence(t.Context(), tx.tx, "effect"); err != nil {
						return err
					}
					last++
				}
			}
			seq, err := appendPreparedEvent(t.Context(), tx.tx, e)
			if err != nil {
				return err
			}
			e.Seq = seq
			if err := c.write(t.Context(), tx.tx, e); err != nil {
				return err
			}
			if err := advanceWorkEffectSeqTx(t.Context(), tx.tx, e); err != nil {
				return err
			}
			return scope.close(t.Context())
		})
		if err != nil {
			t.Fatalf("current corpus %s (%s): %v", e.EventID, e.Kind, err)
		}
	}
	want := make([]string, len(c.events))
	for i, e := range c.events {
		want[i] = e.EventID
	}
	slices.Sort(want)
	rows, err := s.db.Query(`SELECT event_id FROM effect_receipts ORDER BY event_id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("corpus/receipt IDs differ in either direction: got %v want %v", got, want)
	}
	return s
}

func corpusWorkParents(t *testing.T, s *Store, guard string, works ...string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO ` + guard + ` VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, work := range works {
		query := `INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES(?,'task',?,'needed',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`
		if _, err := s.db.Exec(query, work, work); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM ` + guard); err != nil {
		t.Fatal(err)
	}
}

func corpusEvent(id, kind, work string, payload any) Event {
	return operationEvent(id, kind, SubjectWorkItem, work, payload)
}
