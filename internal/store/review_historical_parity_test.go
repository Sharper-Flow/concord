package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func historicalReviewParityDB(t *testing.T) (*sql.DB, func(string, string, any) string) {
	t.Helper()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "review-parity.db"), 148)
	retirementUpgradeExec(t, db, `INSERT INTO fold_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work:parity','task','Historical review','needed',0,1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	seq := 0
	return db, func(kind, actor string, body any) string {
		t.Helper()
		seq++
		ref := fmt.Sprintf("review-parity:%d", seq)
		retirementUpgradeExec(t, db, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,'work_item','work:parity',?,?,1,?)`, seq, ref, kind, actor, retirementUpgradeTime, workflowJSON(body))
		return ref
	}
}

func historicalReviewParityRead(t *testing.T, db *sql.DB) (string, legacyReviewState) {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT state FROM workflow_review_state WHERE work_id='work:parity'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state legacyReviewState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	return raw, state
}

func TestHistoricalReviewBackfillParityInterleavings(t *testing.T) {
	db, appendEvent := historicalReviewParityDB(t)
	candidate := legacyReviewCandidate{ProjectID: "project:one", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	baseline := legacyReviewBlock{Disposition: "continue", InputCandidate: candidate, OutputCandidate: candidate, Findings: []legacyReviewFinding{{FindingID: "finding:one", Status: "open", InScope: true, Blocking: true, Oracle: &legacyReviewOracleFinding{Classification: "delivery_blocker", OwnerID: "owner:original", ControlIDs: []string{"control:original"}}}}}
	refs := map[string]string{}
	complete := func(id, capability string, report legacyReviewBlock) {
		appendEvent(WorkflowActionCompleted, "actor:dispatch", map[string]any{"action_id": "dispatch_worker", "worker_attempt_id": id, "worker_capability_class": capability})
		refs[id] = appendEvent("worker.completed", "actor:worker", map[string]any{"attempt_id": id, "review": report, "observed_delivery": map[string]any{"state": "clean", "commit": report.OutputCandidate.Commit, "tree": report.OutputCandidate.Tree}})
	}
	accept := func(id string) {
		appendEvent(WorkflowActionCompleted, "actor:"+id, map[string]any{"action_id": "accept_worker_result", "worker_attempt_id": id})
	}
	check := func(stage, accepted string, wantCandidate *legacyReviewCandidate, status string) {
		t.Helper()
		retirementUpgradeExec(t, db, `DELETE FROM workflow_review_state`)
		if err := backfillWorkflowReviewFacts(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		_, got := historicalReviewParityRead(t, db)
		if !reflect.DeepEqual(got.Candidate, wantCandidate) || (got.Accepted == nil) != (accepted == "") {
			t.Errorf("%s: candidate/acceptance=%+v, want candidate=%+v accepted=%s", stage, got, wantCandidate, accepted)
		}
		if got.Accepted != nil && (got.Accepted.ReportRef != refs[accepted] || got.Accepted.ActorRef != "actor:"+accepted) {
			t.Errorf("%s: report/actor=%+v", stage, got.Accepted)
		}
		if len(got.Findings) != 1 || got.Findings[0].Status != status || !got.Findings[0].Blocking || !got.Findings[0].InScope || got.Findings[0].Oracle == nil || got.Findings[0].Oracle.OwnerID != "owner:original" {
			t.Errorf("%s: finding=%+v, want original metadata and status=%s", stage, got.Findings, status)
		}
	}
	complete("baseline", "review", baseline)
	check("unaccepted baseline", "", nil, "open")
	accept("baseline")
	check("accepted baseline", "baseline", &candidate, "open")
	repair := baseline
	repair.Findings = append([]legacyReviewFinding(nil), baseline.Findings...)
	repair.OutputCandidate.Commit, repair.OutputCandidate.Tree = strings.Repeat("c", 40), strings.Repeat("d", 40)
	repair.Findings[0].InScope, repair.Findings[0].Blocking, repair.Findings[0].Status = false, false, "closed"
	repair.Findings[0].ClosureRefs = []string{"receipt:repair"}
	complete("repair", "review", repair)
	check("unaccepted repair", "baseline", &candidate, "open")
	accept("repair")
	check("accepted repair", "repair", &repair.OutputCandidate, "closed")
	complete("late", "review", baseline)
	check("late old-input report", "repair", &repair.OutputCandidate, "closed")
	accept("baseline")
	check("repeated old acceptance", "baseline", &candidate, "closed")
	complete("reopen", "review", baseline)
	check("current-input report", "baseline", &candidate, "open")
	omitted := baseline
	omitted.Findings = []legacyReviewFinding{}
	complete("omitted", "review", omitted)
	accept("omitted")
	check("omitted finding", "omitted", &candidate, "open")
	complete("implementation", "implementation", repair)
	accept("implementation")
	implementation := legacyReviewCandidate{Commit: repair.OutputCandidate.Commit, Tree: repair.OutputCandidate.Tree}
	check("implementation has no Review authority", "omitted", &implementation, "open")
}

func TestHistoricalReviewUnidentifiedFindingBackfill(t *testing.T) {
	db, appendEvent := historicalReviewParityDB(t)
	appendEvent(WorkflowActionCompleted, "actor:dispatch", map[string]any{"action_id": "dispatch_worker", "worker_attempt_id": "review:retained", "worker_capability_class": "review"})
	appendEvent("worker.completed", "actor:worker", json.RawMessage(`{"attempt_id":"review:retained","review":{"verdict":"no_ship","findings":[{"severity":"P2","confidence":"high","detail":"The recorded proof is incomplete."}],"resolved_findings":[]}}`))
	if err := backfillWorkflowReviewFacts(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	before, state := historicalReviewParityRead(t, db)
	if len(state.Findings) != 1 || state.Findings[0].FindingID != "" || state.Findings[0].ClosureRefs != nil || state.Findings[0].Severity != "P2" || state.Findings[0].Confidence != "high" || state.Findings[0].Detail != "The recorded proof is incomplete." {
		t.Fatalf("unidentified historical finding changed: %+v", state.Findings)
	}
	after, _ := historicalReviewParityRead(t, db)
	if before != after {
		t.Fatalf("reading changed retained facts: %s -> %s", before, after)
	}
}

func TestHistoricalReviewTreeLessCandidateBackfill(t *testing.T) {
	db, appendEvent := historicalReviewParityDB(t)
	const attempt = "implementation:without-tree"
	appendEvent(WorkflowActionCompleted, "actor:dispatch", map[string]any{"action_id": "dispatch_worker", "worker_attempt_id": attempt, "worker_capability_class": "implementation"})
	appendEvent("worker.completed", "actor:worker", map[string]any{"attempt_id": attempt, "observed_delivery": map[string]any{"state": "clean", "commit": strings.Repeat("a", 40)}})
	appendEvent(WorkflowActionCompleted, "actor:accept", map[string]any{"action_id": "accept_worker_result", "worker_attempt_id": attempt})
	if err := backfillWorkflowReviewFacts(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	before, _ := historicalReviewParityRead(t, db)
	for _, phase := range []string{"read", "rebackfill"} {
		if phase == "rebackfill" {
			if err := backfillWorkflowReviewFacts(t.Context(), db); err != nil {
				t.Fatal(err)
			}
		}
		after, state := historicalReviewParityRead(t, db)
		if state.Candidate == nil || state.Candidate.Commit != strings.Repeat("a", 40) || state.Candidate.Tree != "" || state.Candidate.ProjectID != "" || after != before {
			t.Fatalf("%s changed tree-less historical facts: %+v; %s -> %s", phase, state, before, after)
		}
	}
}
