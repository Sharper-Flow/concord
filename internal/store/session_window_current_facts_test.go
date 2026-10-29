package store

import "testing"

func TestWorkLivenessReadsCurrentProgress(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const work = "liveness-current-progress"
	continuityTestWorkflow(t, s, work)
	want, err := deriveWorkLivenessQ(t.Context(), s.db, work)
	if err != nil || want.LastProgress == "" {
		t.Fatalf("liveness=%+v err=%v, want recorded progress", want, err)
	}
	var stamp string
	if err := s.db.QueryRow(`SELECT last_progress_at FROM work_items WHERE id=?`, work).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if want.LastProgress != stamp {
		t.Fatalf("last_progress=%q, want the work row's retained progress timestamp %q", want.LastProgress, stamp)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"existing-work": func() (any, error) { return deriveWorkLivenessQ(t.Context(), s.db, work) },
		"unknown-work":  func() (any, error) { return deriveWorkLivenessQ(t.Context(), s.db, "unknown-work") },
	})
}
