package store

import "testing"

func TestWorkLivenessReadsCurrentProgress(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const work = "liveness-current-progress"
	seedTestWorkflow(t, s, work)
	read := func(id string) (Q3Result, error) {
		return s.QueryQ3(t.Context(), Q3Request{Product: "product", WorkIDs: []string{id}})
	}
	result, err := read(work)
	if err != nil || len(result.Items) != 1 || result.Items[0].Liveness == nil || result.Items[0].Liveness.LastProgress == "" {
		t.Fatalf("items=%+v err=%v, want recorded progress", result.Items, err)
	}
	var stamp string
	if err := s.db.QueryRow(`SELECT last_progress_at FROM work_items WHERE id=?`, work).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Liveness.LastProgress != stamp {
		t.Fatalf("last_progress=%q, want the work row's retained progress timestamp %q", result.Items[0].Liveness.LastProgress, stamp)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"existing-work": func() (any, error) { r, err := read(work); return r.Items, err },
		"unknown-work":  func() (any, error) { r, err := read("unknown-work"); return r.Items, err },
	})
}
