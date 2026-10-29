package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Historical bytes are inputs to migration, never a live execution route.
func g32ReviewFixture(t *testing.T) (*sql.DB, func(string, any) int64) {
	t.Helper()
	db, appendEvent, _ := progressHistory(t)
	if _, err := db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY); INSERT INTO work_items VALUES('work-history');
CREATE TABLE fold_guard(active INTEGER); INSERT INTO fold_guard VALUES(1);
CREATE TABLE workflow_contracts(work_id TEXT,contract_version INTEGER,superseded_by INTEGER);
CREATE TABLE workflow_contract_predicates(work_id TEXT,contract_version INTEGER,predicate_id TEXT,outcome_kind TEXT,outcome_payload TEXT);
CREATE TABLE workflow_design_records(work_id TEXT,work_version INTEGER,approach TEXT,decisions TEXT,touched_refs TEXT,recorded_at TEXT);
CREATE TABLE workflow_contract_law_revisions(work_id TEXT,contract_version INTEGER,law_id TEXT,content_hash TEXT);
CREATE TABLE work_size_assessments(event_id TEXT,work_id TEXT,sequence INTEGER,model_version INTEGER,size_class TEXT,inputs_json TEXT,footprint_json TEXT,recorded_at TEXT,effect_seq INTEGER,review_basis TEXT);
CREATE TABLE work_size_overrides(work_id TEXT PRIMARY KEY,size_class TEXT);
` + workflowReviewFactsDDL); err != nil {
		t.Fatal(err)
	}
	return db, func(kind string, payload any) int64 {
		t.Helper()
		seq := appendEvent(kind, payload)
		var fields struct {
			ActorRef string `json:"actor_ref"`
		}
		if err := json.Unmarshal(mustJSONValue(payload), &fields); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE domain_events SET actor=? WHERE seq=?`, fields.ActorRef, seq); err != nil {
			t.Fatal(err)
		}
		return seq
	}
}

func TestG32ReviewLogBlind(t *testing.T) {
	j, _ := retainedReviewFixture(t, "g32-current-log-blind")
	finding := reviewBlockFixture().Findings[0]
	completeCurrentReviewForTest(t, j.fixture, j.repo, "correction_required", func(report *ReviewBlock) { report.Findings = []ReviewFinding{finding} })
	assertWorkerFactsLogBlind(t, j.s, map[string]func() (any, error){
		"current-review": func() (any, error) { return loadWorkflowReviewState(t.Context(), j.s.db, j.workID) },
	})
	assertEventLogAbsent(t, j.s)
	head := descendantCommit(t, j, "log-blind-correction")
	ref, _ := completeCurrentReviewForTest(t, j.fixture, j.repo, "continue")
	state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
	actor, actorErr := WorkflowActorRef(j.fixture.owner)
	if err != nil || actorErr != nil || state.Candidate == nil || state.Candidate.Commit != head || state.Accepted == nil || state.Accepted.ReportRef != ref || state.Accepted.ActorRef != actor || state.blocker() != finding.FindingID {
		t.Fatalf("log-blind writer lost subject/actor/report/omitted finding: %+v, %v, %v", state, err, actorErr)
	}
}

func TestG32RecordedReviewOrder(t *testing.T) {
	j, _ := retainedReviewFixture(t, "g32-report-order")
	for _, label := range []string{"a", "b", "a-again"} {
		head := descendantCommit(t, j, label)
		ref, _ := completeCurrentReviewForTest(t, j.fixture, j.repo, "continue")
		state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
		if err != nil || state.Accepted == nil || state.Accepted.ReportRef != ref || state.Candidate == nil || state.Candidate.Commit != head {
			t.Fatalf("%s: latest recorded Review identity=%+v, %v", label, state, err)
		}
	}
}

func TestG32ReviewFactsGuardAndRollback(t *testing.T) {
	db, _ := g32ReviewFixture(t)
	state := WorkflowReviewState{Candidate: &reviewBlockFixture().OutputCandidate, Findings: []ReviewFinding{}}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkflowReviewFacts(t.Context(), db, "work-history", state); err == nil {
		t.Fatal("unguarded Review write succeeded")
	}
	if _, err := db.Exec(`INSERT INTO fold_guard VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := writeWorkflowReviewFacts(t.Context(), tx, "work-history", state); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, err := readWorkflowReviewFacts(t.Context(), db, "work-history")
	if err != nil || got.Candidate != nil || got.Accepted != nil || len(got.Findings) != 0 {
		t.Fatalf("rolled back Review escaped its transaction: %+v, %v", got, err)
	}
}

func TestG32ReviewBackfillUsesCallerGuard(t *testing.T) {
	db, appendEvent := g32ReviewFixture(t)
	report := reviewBlockFixture()
	appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "dispatch_worker", "worker_attempt_id": "review:migrate", "worker_capability_class": "review", "worker_subject_commit": report.InputCandidate.Commit})
	seq := appendEvent("worker.completed", map[string]any{"attempt_id": "review:migrate", "review": report, "observed_delivery": map[string]any{"state": "clean", "commit": report.OutputCandidate.Commit, "tree": report.OutputCandidate.Tree}})
	appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_result", "worker_attempt_id": "review:migrate", "actor_ref": "actor:migrating"})
	before, err := readWorkflowReviewFacts(t.Context(), db, "work-history")
	if err != nil || before.Candidate != nil || before.Accepted != nil || len(before.Findings) != 0 {
		t.Fatalf("live read reconstructed historical bytes: %+v, %v", before, err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := backfillWorkflowReviewFacts(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM fold_guard WHERE active=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("backfill consumed caller guard: %d, %v", count, err)
	}
	state, err := readWorkflowReviewFacts(t.Context(), tx, "work-history")
	if err != nil || state.Accepted == nil || state.Accepted.ReportRef != fmt.Sprintf("progress-e%d", seq) || state.Accepted.ActorRef != "actor:migrating" || state.Candidate == nil || *state.Candidate != report.OutputCandidate {
		t.Fatalf("migration lost exact accepted legacy Review: %+v, %v", state, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	state, err = readWorkflowReviewFacts(t.Context(), db, "work-history")
	if err != nil || state.Accepted != nil || state.Candidate != nil || len(state.Findings) != 0 {
		t.Fatalf("rolled-back backfill escaped its transaction: %+v, %v", state, err)
	}
}

func TestChangedPinnedLawStalesAcceptedReview(t *testing.T) {
	j, _ := retainedReviewFixture(t, "g32-pinned-law")
	if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,1,'spec:one',?); DELETE FROM write_guard`, j.workID, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	completeCurrentReviewForTest(t, j.fixture, j.repo, "continue")
	read := func() WorkflowReviewState {
		t.Helper()
		state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
		if err != nil || state.Accepted == nil || state.Dependencies == nil {
			t.Fatalf("Review state=%+v, %v", state, err)
		}
		return state
	}
	before := read()
	if !before.DependenciesCurrent || before.Dependencies.ArchitectureNotApplicable || before.Dependencies.ArchitectureDigest == "" {
		t.Fatalf("pinned law is not an applicable architecture basis: %+v", before)
	}
	if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contract_law_revisions SET content_hash=? WHERE work_id=?; DELETE FROM write_guard`, "sha256:"+strings.Repeat("b", 64), j.workID); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after.DependenciesCurrent || after.Dependencies.ArchitectureDigest == before.Dependencies.ArchitectureDigest || !reflect.DeepEqual(after.Accepted, before.Accepted) {
		t.Fatalf("changed law cached readiness or rewrote accepted Review: %+v", after)
	}
}

func TestG32ChangedDependenciesRetainRecordedReview(t *testing.T) {
	for _, change := range []string{"criterion", "design"} {
		t.Run(change, func(t *testing.T) {
			j, _ := retainedReviewFixture(t, "g32-dependency-"+change)
			before, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
			if err != nil || before.Accepted == nil || before.Dependencies == nil || !before.DependenciesCurrent {
				t.Fatalf("applicable Review fixture=%+v, %v", before, err)
			}
			if change == "criterion" {
				payload := pContractSuccessor(t, j.s, j.workID, "The same outcome needs a revised criterion.")
				payload["outcome_predicates"].([]any)[0].(map[string]any)["outcome_payload"].(map[string]any)["expected_result"] = "corrected-result"
				if err := runVerdictActionAs(t, j.s, j.workID, "supersede_contract", mustJSONValue(payload), 0, j.fixture.owner); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES(?,2,'Use the revised architecture.','[]','["owner.go"]','2026-01-01T00:00:00Z'); DELETE FROM write_guard`, j.workID); err != nil {
					t.Fatal(err)
				}
			}
			after, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
			if err != nil || after.DependenciesCurrent || after.Dependencies == nil || sameReviewDependencies(*after.Dependencies, *before.Dependencies) || !reflect.DeepEqual(after.Accepted, before.Accepted) || !reflect.DeepEqual(after.Findings, before.Findings) {
				t.Fatalf("changed %s cached readiness or rewrote recorded Review: %+v, %v", change, after, err)
			}
		})
	}
}

func TestG32ReviewBackfillParityInterleavings(t *testing.T) {
	db, appendEvent := g32ReviewFixture(t)
	baseline := reviewBlockFixture()
	baseline.Disposition = "continue"
	baseline.Findings[0].Oracle = &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:original", ControlIDs: []string{"control:original"}}
	refs := map[string]string{}
	complete := func(id, capability string, report *ReviewBlock) {
		t.Helper()
		appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "dispatch_worker", "worker_attempt_id": id, "worker_capability_class": capability, "worker_subject_commit": report.InputCandidate.Commit})
		seq := appendEvent("worker.completed", map[string]any{"attempt_id": id, "review": report, "observed_delivery": map[string]any{"state": "clean", "commit": report.OutputCandidate.Commit, "tree": report.OutputCandidate.Tree}})
		refs[id] = fmt.Sprintf("progress-e%d", seq)
	}
	accept := func(id string) {
		appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_result", "worker_attempt_id": id, "actor_ref": "actor:" + id})
	}
	check := func(stage, accepted string, candidate *ReviewCandidate, status string) {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM workflow_review_state`); err != nil {
			t.Fatal(err)
		}
		if err := backfillWorkflowReviewFacts(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		got, err := readWorkflowReviewFacts(t.Context(), db, "work-history")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Candidate, candidate) || (got.Accepted == nil) != (accepted == "") {
			t.Errorf("%s: candidate/acceptance=%+v, want candidate=%+v accepted=%s", stage, got, candidate, accepted)
		}
		if got.Accepted != nil && (got.Accepted.ReportRef != refs[accepted] || got.Accepted.ActorRef != "actor:"+accepted) {
			t.Errorf("%s: exact report/actor lost: %+v", stage, got.Accepted)
		}
		if len(got.Findings) != 1 || got.Findings[0].Status != status || !got.Findings[0].Blocking || !got.Findings[0].InScope || got.Findings[0].Oracle == nil || got.Findings[0].Oracle.OwnerID != "owner:original" {
			t.Errorf("%s: retained finding=%+v, want original metadata and status=%s", stage, got.Findings, status)
		}
	}
	complete("baseline", "review", baseline)
	check("unaccepted baseline", "", nil, "open")
	accept("baseline")
	check("accepted baseline", "baseline", &baseline.OutputCandidate, "open")
	repair := reviewBlockFixture()
	repair.OutputCandidate.Commit, repair.OutputCandidate.Tree = strings.Repeat("c", 40), strings.Repeat("d", 40)
	repair.Findings[0].InScope, repair.Findings[0].Blocking, repair.Findings[0].Status = false, false, "closed"
	repair.Findings[0].ClosureRefs = []string{"receipt:repair"}
	complete("repair", "review", repair)
	check("unaccepted repair", "baseline", &baseline.OutputCandidate, "open")
	accept("repair")
	check("accepted repair", "repair", &repair.OutputCandidate, "closed")
	complete("late", "review", baseline)
	check("late old-input report", "repair", &repair.OutputCandidate, "closed")
	accept("baseline")
	check("repeated old acceptance", "baseline", &baseline.OutputCandidate, "closed")
	complete("reopen", "review", baseline)
	check("current-input report", "baseline", &baseline.OutputCandidate, "open")
	omitted := *baseline
	omitted.Findings = []ReviewFinding{}
	complete("omitted", "review", &omitted)
	accept("omitted")
	check("omitted finding", "omitted", &baseline.OutputCandidate, "open")
	complete("implementation", "implementation", repair)
	accept("implementation")
	check("implementation has no Review authority", "omitted", &baseline.OutputCandidate, "open")
}

func TestG32DeclaredCorrectionLogBlind(t *testing.T) {
	db, appendEvent := correctionHistoryFixture(t)
	appendEvent(WorkflowActionCompleted, correctionHistoryRequest("explicit"))
	if _, err := db.Exec(`ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
	got, err := readWorkflowCorrectionContext(t.Context(), db, "work-history")
	if err != nil || got == nil || got.SourceEventID != "history-e1" || got.SourceEventSeq != 1 || got.Diagnosis != "diagnosis:explicit" {
		t.Fatalf("log-blind declared correction=%+v, %v", got, err)
	}
}

func TestG32DeclaredCorrectionRetainsInvalidDataRefusals(t *testing.T) {
	for _, fault := range []string{"malformed state", "unreadable state"} {
		t.Run(fault, func(t *testing.T) {
			db, appendEvent := correctionHistoryFixture(t)
			appendEvent(WorkflowActionCompleted, correctionHistoryRequest("explicit"))
			want := KindInvariantViolation
			if fault == "malformed state" {
				if _, err := db.Exec(`UPDATE workflow_correction_state SET state='{'`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.Exec(`ALTER TABLE workflow_correction_state RENAME TO hidden_correction_state`); err != nil {
					t.Fatal(err)
				}
				want = KindUnavailable
			}
			got, err := readWorkflowCorrectionContext(t.Context(), db, "work-history")
			if got != nil || !hasFailureKind(err, want) {
				t.Fatalf("%s: correction=%+v, %v, want %s", fault, got, err, want)
			}
		})
	}
}
