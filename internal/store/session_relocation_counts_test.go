package store

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestSessionRelocationOrdinalsReadCurrentCounts(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	s := f.store
	var claim string
	if err := s.db.QueryRow(`SELECT claim_op_id FROM worktree_entries WHERE path=?`, f.targetTree).Scan(&claim); err != nil {
		t.Fatal(err)
	}
	var events []Event
	for _, session := range []string{f.targetSession, "session/other"} {
		for _, kind := range []string{"work.session_vacated", "work.session_vacate_landed", "work.session_claim_landed"} {
			for n := 0; n < 3; n++ {
				p := map[string]any{
					"work_id": f.workID, "project_id": f.targetProject, "session_ref": session,
					"source_directory": f.targetTree, "destination_directory": "/data/main",
				}
				if kind != "work.session_claim_landed" && n == 2 {
					p["project_id"] = f.sourceProject
					p["source_directory"] = f.sourceTree
				}
				if kind != "work.session_vacated" {
					delete(p, "source_directory")
					p["landed_directory"] = "/data/main"
					p["host_pid"] = 1
				}
				if kind == "work.session_claim_landed" {
					delete(p, "destination_directory")
					p["landed_directory"] = f.targetTree
					// An unbound landing still consumes an ordinal, even
					// though it proves no claim placement.
					if n != 1 {
						p["claim_op_id"] = claim
					}
				}
				e := operationEvent(fmt.Sprintf("%s:%s:%d", kind, session, n), kind, SubjectWorkItem, f.workID, p)
				if kind == "work.session_vacated" {
					e.PayloadVersion = 2
				}
				events = append(events, e)
			}
		}
	}
	if err := ApplyOperation(t.Context(), s, Operation{Events: events}); err != nil {
		t.Fatal(err)
	}
	// An exact effect replay must not consume another ordinal.
	if err := ApplyOperation(t.Context(), s, Operation{Events: events[:1]}); failureKind(err) != KindDuplicateEvent {
		t.Fatalf("duplicate effect err=%v, want duplicate_event", err)
	}
	rollback := events[0]
	rollback.EventID = "rollback:request"
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{rollback, events[0]}}); failureKind(err) != KindDuplicateEvent {
		t.Fatalf("rolled-back effects err=%v, want duplicate_event", err)
	}
	read := func(work, session string) ([3]int, error) {
		var counts [3]int
		err := inRelocationTx(t, s, func(transaction *Transaction, tx *sql.Tx) error {
			var errs [3]error
			counts[0], errs[0] = CountWorkSessionVacatesTx(t.Context(), transaction, work, session)
			counts[1], errs[1] = countSessionVacateLandingsTx(t.Context(), tx, work, session)
			counts[2], errs[2] = countSessionClaimLandingsTx(t.Context(), tx, work, session)
			return errors.Join(errs[:]...)
		})
		return counts, err
	}
	for _, session := range []string{f.targetSession, "session/other"} {
		if got, err := read(f.workID, session); err != nil || got != [3]int{3, 3, 3} {
			t.Fatalf("%s counts=%v err=%v, want [3 3 3]", session, got, err)
		}
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"ordinals":        func() (any, error) { return read(f.workID, f.targetSession) },
		"other-session":   func() (any, error) { return read(f.workID, "session/other") },
		"missing-session": func() (any, error) { return read(f.workID, "session/missing") },
		"missing-work":    func() (any, error) { return read("work-missing", f.targetSession) },
	})
	assertEventLogAbsent(t, s)
	const query = `SELECT work_id||':'||session_ref||':'||vacate_count||':'||vacate_landing_count||':'||claim_landing_count FROM session_relocation_counts ORDER BY work_id,session_ref`
	want := workerFactsQueryRows(t, s, []string{query})
	assertSessionWindowCorpus144(t)
	if got := workerFactsQueryRows(t, s, []string{query}); !reflect.DeepEqual(got, want) {
		t.Fatalf("backfilled ordinal counts=%v, want live-written %v", got, want)
	}
}
