package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func sessionWindowExplicitCorpus(t *testing.T) historicalFactCorpus {
	t.Helper()
	const work = "session-corpus"
	c := historicalFactCorpus{}
	for _, session := range []string{"session/one", "session/two"} {
		for _, kind := range []string{"work.session_vacated", "work.session_vacate_landed", "work.session_claim_landed"} {
			for n := range 3 {
				c.events = append(c.events, corpusEvent(fmt.Sprintf("%s:%s:%d", kind, session, n), kind, work, map[string]any{"session_ref": session}))
			}
		}
	}
	for n, raw := range []string{`{}`, `{"session_ref":null}`} {
		c.events = append(c.events, corpusEvent(fmt.Sprintf("missing-session:%d", n), "work.session_vacated", work, json.RawMessage(raw)))
	}
	c.events = append(c.events, corpusEvent("unrelated-session-effect", "work.intent_revised", work, map[string]any{"session_ref": "session/one"}))
	for _, branch := range []string{"work/settlement_count", "work/settlementXcount"} {
		for incarnation := range 2 {
			for n := range incarnation + 2 {
				payload := map[string]any{"set_id": WorktreeSetID(work), "project_id": "project-corpus", "claim_op_id": "claim-corpus", "branch": branch, "phase": "planned", "reason": "corpus observation"}
				if incarnation != 0 {
					payload["claim_incarnation"] = incarnation
				}
				id := fmt.Sprintf("%s:project-corpus:claim-corpus:worktree-removal-settled:%s:%d:i%d", work, branch, n, incarnation)
				c.events = append(c.events, corpusEvent(id, "work.worktree_removal_settled", work, payload))
			}
		}
	}
	c.events = append(c.events, corpusEvent("unrelated-settlement", "work.worktree_removal_settled", work, map[string]any{"set_id": WorktreeSetID(work), "project_id": "project-corpus", "claim_op_id": "claim-corpus", "claim_incarnation": 1, "branch": "work/settlement_count", "phase": "planned", "reason": "corpus observation"}))
	for n, stamp := range []string{"2028-01-01T00:00:00Z", "2026-01-01T00:00:00Z"} {
		c.events = append(c.events, corpusEvent(fmt.Sprintf("handoff-record:%d", n), "work.project_handoff_recorded", work, map[string]any{
			"work_id": work, "handoff_id": fmt.Sprintf("handoff:%d", n), "contract_version": 1,
			"source_project_id": "project-source", "target_project_id": "project-target", "source_session_ref": "session/source",
			"bounded_job": "Verify the scoped corpus", "next_action": "Run the scoped check", "recorded_at": stamp,
			"changes": []string{}, "verification": []string{}, "artifact_refs": []string{}, "blockers": []string{},
		}))
	}
	c.parents = func(t *testing.T, s *Store, guard string) {
		corpusWorkParents(t, s, guard, work)
		if guard != "fold_guard" {
			return
		}
		if _, err := s.db.Exec(`INSERT INTO fold_guard VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		// These are pre-144 data rows, not the new count/recording columns.
		for _, branch := range []string{"work/settlement_count", "work/settlementXcount"} {
			for incarnation := range 2 {
				if _, err := s.db.Exec(`INSERT INTO worktree_ref_outcomes(set_id,project_id,claim_op_id,claim_incarnation,branch,phase,reason,recorded_at) VALUES(?,?,?,?,?,'planned','corpus observation','2026-01-01T00:00:00Z')`, WorktreeSetID(work), "project-corpus", "claim-corpus", incarnation, branch); err != nil {
					t.Fatal(err)
				}
			}
		}
		for n, stamp := range []string{"2028-01-01T00:00:00Z", "2026-01-01T00:00:00Z"} {
			if _, err := s.db.Exec(`INSERT INTO project_handoffs(handoff_id,work_id,contract_version,source_project_id,target_project_id,source_session_ref,bounded_job,changes,verification,artifact_refs,blockers,next_action,state,recorded_at) VALUES(?,?,1,'project-source','project-target','session/source','Verify the scoped corpus','[]','[]','[]','[]','Run the scoped check','recorded',?)`, fmt.Sprintf("handoff:%d", n), work, stamp); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func assertSessionWindowCorpus144(t *testing.T) {
	t.Helper()
	c := sessionWindowExplicitCorpus(t)
	old := c.prefix(t, 143)
	queries := []string{
		`SELECT json_array(work_id,session_ref,vacate_count,vacate_landing_count,claim_landing_count) FROM session_relocation_counts ORDER BY work_id,session_ref`,
		`SELECT json_array(set_id,project_id,claim_op_id,claim_incarnation,branch,settlement_count) FROM worktree_ref_outcomes ORDER BY set_id,project_id,claim_op_id,claim_incarnation,branch`,
		`SELECT json_array(handoff_id,recorded_at,recorded_seq) FROM project_handoffs ORDER BY handoff_id`,
	}
	want := [][]string{
		{`["session-corpus","session/one",3,3,3]`, `["session-corpus","session/two",3,3,3]`},
		{`["wts:session-corpus","project-corpus","claim-corpus",0,"work/settlementXcount",2]`, `["wts:session-corpus","project-corpus","claim-corpus",0,"work/settlement_count",2]`, `["wts:session-corpus","project-corpus","claim-corpus",1,"work/settlementXcount",3]`, `["wts:session-corpus","project-corpus","claim-corpus",1,"work/settlement_count",3]`},
		{`["handoff:0","2028-01-01T00:00:00Z",33]`, `["handoff:1","2026-01-01T00:00:00Z",34]`},
	}
	advanceHistoricalPrefix(t, old.db, 144)
	if got := workerFactsQueryRows(t, old, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("144 migrated corpus=%v; expected recorded facts=%v", got, want)
	}
	var a, b, count int
	if err := old.db.QueryRow(`SELECT (SELECT recorded_seq FROM project_handoffs WHERE handoff_id='handoff:0'),(SELECT recorded_seq FROM project_handoffs WHERE handoff_id='handoff:1'),(SELECT settlement_count FROM worktree_ref_outcomes WHERE branch='work/settlement_count' AND claim_incarnation=1)`).Scan(&a, &b, &count); err != nil {
		t.Fatal(err)
	}
	if a <= 0 || b <= a || count != 3 {
		t.Fatalf("144 selected handoff order=%d/%d settlement=%d; want increasing/3", a, b, count)
	}
	assertCorpusSelectionRed(t, c, 144, "AND COALESCE(json_extract(e.payload,'$.claim_incarnation'),0)=o.claim_incarnation", "", queries, want)
	before := histEventRows(t, old)
	if err := Migrate(t.Context(), old.db); err != nil {
		t.Fatal(err)
	}
	assertWorktreeTablesAbsent(t, old.db)
	assertHistoricalReceipts(t, old.db, before)
	assertEventLogAbsent(t, old)
	if got := workerFactsQueryRows(t, old, queries[2:]); !reflect.DeepEqual(got, want[2:]) {
		t.Fatalf("current handoff recording order=%v, want %v", got, want[2:])
	}
}
