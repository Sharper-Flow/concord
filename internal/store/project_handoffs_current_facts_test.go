package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// recordOrderedHandoffPair records two handoffs whose recorded_at text order
// disagrees with their recording order: the older whole-second stamp sorts
// after the later fractional stamp.
func recordOrderedHandoffPair(t *testing.T, f projectHandoffFixture) (older, newer string) {
	t.Helper()
	first := f.recordRequest(f.sourceTree)
	first.Now = time.Unix(40, 0).UTC()
	first.ArtifactRefs = []string{"sha256:" + strings.Repeat("a", 64) + ":/first-artifact.txt"}
	firstRec, err := runRecordProjectHandoffTx(f, first)
	if err != nil {
		t.Fatal(err)
	}
	second := f.recordRequest(f.sourceTree)
	second.BoundedJob = "the later recorded repository job"
	second.ArtifactRefs = []string{"sha256:" + strings.Repeat("b", 64) + ":/second-artifact.txt"}
	second.Now = time.Unix(40, 1).UTC()
	secondRec, err := runRecordProjectHandoffTx(f, second)
	if err != nil {
		t.Fatal(err)
	}
	return firstRec.HandoffID, secondRec.HandoffID
}

func TestProjectHandoffNewestSelectionsReadCurrentRows(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	_, newer := recordOrderedHandoffPair(t, f)
	assertEventLogAbsent(t, f.store)
	if err := f.store.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "project_handoff_test")
		if err != nil {
			return err
		}
		t.Run("continuity", func(t *testing.T) {
			pending, err := PendingProjectHandoffTx(ctx, tx, f.workID)
			if err != nil {
				t.Fatal(err)
			}
			if pending == nil || pending.HandoffID != newer {
				t.Fatalf("continuity handoff=%+v, want the later-recorded %s", pending, newer)
			}
		})
		t.Run("boot", func(t *testing.T) {
			frontier, err := PendingProjectHandoffForProjectTx(ctx, tx, f.workID, f.targetProject, "")
			if err != nil {
				t.Fatal(err)
			}
			if frontier == nil || frontier.HandoffID != newer {
				t.Fatalf("boot frontier=%+v, want the later-recorded %s", frontier, newer)
			}
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectHandoffAdmissionAndConsumeReadCurrentRows(t *testing.T) {
	for _, state := range []string{"pending-newer", "consumed-newer", "stale-newer"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := setupProjectHandoffFixture(t)
			older, newer := recordOrderedHandoffPair(t, f)
			consumed := newer
			if state != "consumed-newer" {
				consumed = older
			}
			consumeHandoff(t, f, consumed)
			if state == "stale-newer" {
				seedReplacementProjectHandoffContract(t, f.store, f.workID, readWorkVersion(t, f.store, f.workID))
			}
			assertEventLogAbsent(t, f.store)
			if err := inRelocationTx(t, f.store, func(_ *Transaction, tx *sql.Tx) error {
				err := RefuseUnconsumedProjectHandoffTx(t.Context(), tx, f.workID, f.targetSession)
				if state == "consumed-newer" {
					if err != nil {
						t.Errorf("current bind refused admission: %v", err)
					}
				} else if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), newer) {
					t.Errorf("admission err=%v, want the newest handoff %s refusal", err, newer)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if state == "pending-newer" {
				// A fresh consume still writes through the event seam. Its
				// addressed selection is tested without that seam by the
				// current-bind replay and stale-contract refusal below.
				return
			}
			out, err := runConsumeProjectHandoffTx(f, ConsumeProjectHandoffRequest{
				WorkID: f.workID, ConsumerProjectID: f.targetProject, ConsumerSessionRef: f.targetSession,
			})
			if state == "consumed-newer" {
				if err != nil || !out.AlreadyConsumed || out.HandoffID != newer {
					t.Fatalf("addressed consume=%+v err=%v, want the newest standing bind %s", out, err, newer)
				}
			} else if failureKind(err) != KindInvalidOperation || !strings.Contains(fmt.Sprint(err), "active contract is version 2") {
				t.Fatalf("addressed consume err=%v, want the newest stale-contract refusal", err)
			}
		})
	}
}

func TestProjectHandoffRecordedSeqMigrationBackfillsRecordingOrder(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	older, newer := recordOrderedHandoffPair(t, f)
	const query = `SELECT handoff_id||':'||recorded_seq FROM project_handoffs ORDER BY handoff_id`
	want := workerFactsQueryRows(t, f.store, []string{query})
	var olderSeq, newerSeq int64
	if err := f.store.db.QueryRow(`SELECT (SELECT recorded_seq FROM project_handoffs WHERE handoff_id=?),(SELECT recorded_seq FROM project_handoffs WHERE handoff_id=?)`, older, newer).Scan(&olderSeq, &newerSeq); err != nil {
		t.Fatal(err)
	}
	if olderSeq <= 0 || newerSeq <= olderSeq {
		t.Fatalf("recorded_seq older=%d newer=%d, want the recording event order", olderSeq, newerSeq)
	}
	assertSessionWindowCorpus144(t)
	if got := workerFactsQueryRows(t, f.store, []string{query}); !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill recorded_seq=%v, want the folded %v", got, want)
	}
}

func TestProjectHandoffRetirementSelectionsReadCurrentRows(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "active-contract"
		if stale {
			name = "stale-contract"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := setupProjectHandoffFixture(t)
			_, newer := recordOrderedHandoffPair(t, f)
			if stale {
				seedReplacementProjectHandoffContract(t, f.store, f.workID, readWorkVersion(t, f.store, f.workID))
			}
			read := func() (projectRetirementHandoffFacts, error) {
				var facts projectRetirementHandoffFacts
				err := readRetirementHandoffFactsTx(t.Context(), f.store.db, f.workID, f.sourceProject, f.sourceSession, &facts)
				return facts, err
			}
			want, err := read()
			if err != nil {
				t.Fatal(err)
			}
			if stale {
				if want.recorded || !strings.Contains(want.contractBlocker, "contract version 1, but the active contract is version 2") {
					t.Fatalf("stale facts=%+v, want the stale-contract blocker", want)
				}
			} else if !want.recorded || want.handoffID != newer {
				t.Fatalf("active facts=%+v, want the later-recorded %s", want, newer)
			}
			assertEventLogAbsent(t, f.store)
			got, err := read()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("log-blind facts=%+v, want %+v", got, want)
			}
		})
	}
}
