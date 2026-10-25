package store

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func g32ReviewFixture(t *testing.T) (*sql.DB, func(string, any) int64) {
	t.Helper()
	db, appendEvent, _ := progressHistory(t)
	if _, err := db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY); INSERT INTO work_items VALUES('work-history');
CREATE TABLE fold_guard(active INTEGER); INSERT INTO fold_guard VALUES(1);
CREATE TABLE workflow_contracts(work_id TEXT,contract_version INTEGER,superseded_by INTEGER);
CREATE TABLE workflow_contract_predicates(work_id TEXT,contract_version INTEGER,predicate_id TEXT,outcome_kind TEXT,outcome_payload TEXT);
CREATE TABLE workflow_design_records(work_id TEXT,work_version INTEGER,approach TEXT,decisions TEXT,touched_refs TEXT,recorded_at TEXT);
CREATE TABLE workflow_contract_law_revisions(work_id TEXT,contract_version INTEGER,law_id TEXT,content_hash TEXT);
CREATE TABLE worker_authorizations(work_id TEXT,attempt_id TEXT,seq INTEGER,payload TEXT);
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,terminal_kind TEXT,terminal_payload TEXT,terminal_event_id TEXT);
CREATE TABLE worker_job_revisions(work_id TEXT,job_id TEXT,revision INTEGER,correction_ref TEXT,recorded_payload TEXT,digest TEXT,recorded_seq INTEGER);
CREATE TABLE workflow_member_groups(work_id TEXT,group_id TEXT,controller_actor_ref TEXT,base_subject TEXT,permission TEXT,review_ref TEXT,recorded_seq INTEGER,PRIMARY KEY(work_id,group_id));
CREATE TABLE workflow_group_members(work_id TEXT,group_id TEXT,member_id TEXT,job_id TEXT,job_revision INTEGER,job_digest TEXT,project_id TEXT,actor_ref TEXT,confirmed INTEGER);
` + workflowReviewFactsDDL); err != nil {
		t.Fatal(err)
	}
	return db, func(kind string, payload any) int64 {
		t.Helper()
		seq := appendEvent(kind, payload)
		event := Event{SubjectID: "work-history", Seq: seq, EventID: fmt.Sprintf("progress-e%d", seq)}
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		switch kind {
		case WorkflowActionCompleted:
			action := payload.(workflowActionCompletedPayload)
			event.Actor = action.ActorRef
			if _, err := tx.Exec(`UPDATE domain_events SET actor=? WHERE seq=?`, event.Actor, seq); err != nil {
				t.Fatal(err)
			}
			if action.ActionID == "dispatch_worker" {
				_, err = tx.Exec(`INSERT INTO worker_authorizations VALUES('work-history',?,?,?)`, action.WorkerAttemptID, seq, string(mustJSONValue(action)))
			} else {
				err = advanceWorkflowReviewAcceptanceTx(t.Context(), tx, event, action)
			}
		case WorkerCompleted:
			result := payload.(WorkerCompletedPayload)
			_, err = tx.Exec(`INSERT INTO worker_attempts VALUES('work-history',?,?,?,?)`, result.AttemptID, kind, string(mustJSONValue(result)), event.EventID)
			if err == nil {
				err = advanceWorkflowReviewCompletedTx(t.Context(), tx, event, result)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return seq
	}
}

func TestG32ReviewLogBlind(t *testing.T) {
	db, appendEvent := g32ReviewFixture(t)
	report := reviewBlockFixture()
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "review:first", WorkerCapabilityClass: "review"})
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:first", Review: report})
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "review:first"})
	before, err := loadWorkflowReviewState(t.Context(), db, "work-history")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
	after, err := loadWorkflowReviewState(t.Context(), db, "work-history")
	if err != nil || string(mustJSONValue(after)) != string(mustJSONValue(before)) {
		t.Fatalf("log-blind review = %+v, %v; want %+v", after, err, before)
	}
	// Both maintaining writers also run without a log. Their input is the
	// admitted terminal and the exact authorization/attempt owner rows.
	repair := *report
	repair.OutputCandidate.Commit = strings.Repeat("e", 40)
	repair.Findings = []WorkerReviewFinding{}
	auth := workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "review:log-blind", WorkerCapabilityClass: "review"}
	result := WorkerCompletedPayload{AttemptID: auth.WorkerAttemptID, Review: &repair}
	if _, err := db.Exec(`INSERT INTO worker_authorizations VALUES('work-history',?,20,?)`, auth.WorkerAttemptID, string(mustJSONValue(auth))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worker_attempts VALUES('work-history',?,?,?,?)`, auth.WorkerAttemptID, WorkerCompleted, string(mustJSONValue(result)), "terminal:log-blind"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	event := Event{SubjectID: "work-history", Seq: 21, Actor: "main:log-blind"}
	if err := advanceWorkflowReviewCompletedTx(t.Context(), tx, event, result); err != nil {
		t.Fatal(err)
	}
	unaccepted, err := loadWorkflowReviewState(t.Context(), tx, "work-history")
	if err != nil || *unaccepted.Candidate != report.OutputCandidate || unaccepted.Accepted.ReportRef != before.Accepted.ReportRef {
		t.Fatalf("unaccepted log-blind terminal changed authority: %+v, %v", unaccepted, err)
	}
	event.Seq = 22
	if err := advanceWorkflowReviewAcceptanceTx(t.Context(), tx, event, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: auth.WorkerAttemptID}); err != nil {
		t.Fatal(err)
	}
	accepted, err := loadWorkflowReviewState(t.Context(), tx, "work-history")
	if err != nil || *accepted.Candidate != repair.OutputCandidate || accepted.Accepted.ReportRef != "terminal:log-blind" || accepted.Accepted.ActorRef != event.Actor || accepted.blocker() == "" {
		t.Fatalf("log-blind acceptance lost exact identity or omitted finding: %+v, %v", accepted, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	durable, err := loadWorkflowReviewState(t.Context(), db, "work-history")
	if err != nil || string(mustJSONValue(durable)) != string(mustJSONValue(accepted)) {
		t.Fatalf("committed Review state = %+v, %v; want %+v", durable, err, accepted)
	}
}

func TestG32ImplementationCandidateAndReviewGroupOrder(t *testing.T) {
	db, appendEvent := g32ReviewFixture(t)
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "implementation:first", WorkerCapabilityClass: "implementation"})
	result := WorkerCompletedPayload{AttemptID: "implementation:first", ObservedDelivery: &WorkerObservedDelivery{State: "clean", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}}
	appendEvent(WorkerCompleted, result)
	read := func() WorkflowReviewState {
		t.Helper()
		state, err := loadWorkflowReviewState(t.Context(), db, "work-history")
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	if read().Candidate != nil {
		t.Fatal("implementation terminal installed its candidate")
	}
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: result.AttemptID})
	if state := read(); state.Candidate == nil || state.Candidate.Commit != result.ObservedDelivery.Commit || state.Accepted != nil {
		t.Fatalf("implementation acceptance did not install only its candidate: %+v", state)
	}
	report := reviewBlockFixture()
	report.InputCandidate, report.OutputCandidate = *read().Candidate, *read().Candidate
	report.Findings, report.Disposition = []WorkerReviewFinding{}, "continue"
	refs := map[string]string{}
	for _, attempt := range []string{"review:a", "review:b"} {
		appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: attempt, WorkerCapabilityClass: "review"})
		seq := appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: attempt, Review: report})
		refs[attempt] = fmt.Sprintf("progress-e%d", seq)
	}
	accept := func(attempt string) {
		t.Helper()
		appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: attempt, ActorRef: "main:" + attempt})
		state := read()
		if state.Accepted.AttemptID != attempt || state.Accepted.ReportRef != refs[attempt] || state.Accepted.ActorRef != "main:"+attempt {
			t.Fatalf("acceptance order/identity lost: %+v", state)
		}
	}
	accept("review:a")
	if _, err := db.Exec(`INSERT INTO workflow_member_groups(work_id,group_id,permission,review_ref) VALUES('work-history','group:one','continue',?)`, refs["review:a"]); err != nil {
		t.Fatal(err)
	}
	if state := read(); !state.ExecutionProgressed || !state.ContinueConsumed || state.GroupID != "group:one" {
		t.Fatalf("accepted Review's group permission not loaded: %+v", state)
	}
	accept("review:b")
	if read().ContinueConsumed {
		t.Fatal("another Review inherited consumed continue permission")
	}
	accept("review:a")
	if !read().ContinueConsumed {
		t.Fatal("repeated acceptance lost the exact report's consumed permission")
	}
	before := read()
	if err := backfillWorkflowReviewFacts(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if after := read(); string(mustJSONValue(after)) != string(mustJSONValue(before)) {
		t.Fatalf("repeated acceptance backfill differs: %+v; want %+v", after, before)
	}
}

func TestG32ReviewFactsGuardAndRollback(t *testing.T) {
	db, _ := g32ReviewFixture(t)
	state := WorkflowReviewState{Candidate: &WorkerReviewCandidate{ProjectID: "project:one", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}, Findings: []WorkerReviewFinding{}}
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

func TestG32OrdinaryAssignmentGroupSingleOwner(t *testing.T) {
	db, _ := g32ReviewFixture(t)
	if _, err := db.Exec(`INSERT INTO worker_job_revisions(work_id,job_id,revision,correction_ref) VALUES('work-history','job:ordinary',1,'')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	event := Event{SubjectID: "work-history", Actor: "main:ordinary", Seq: 1}
	payload := workflowActionCompletedPayload{WorkerCapabilityClass: "implementation", WorkerJob: &WorkerJobBinding{JobID: "job:ordinary", Revision: 1}}
	if err := recordWorkflowAssignmentGroup(t.Context(), tx, event, payload); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var controller string
	var base sql.NullString
	var seq int64
	if err := db.QueryRow(`SELECT controller_actor_ref,base_subject,recorded_seq FROM workflow_member_groups WHERE work_id='work-history' AND group_id='job:ordinary'`).Scan(&controller, &base, &seq); err != nil || controller != event.Actor || base.Valid || seq != event.Seq {
		t.Fatalf("ordinary admission invented a base or lost its actual author/sequence: %q, %v, %d, %v", controller, base, seq, err)
	}
	if _, err := db.Exec(`UPDATE worker_job_revisions SET correction_ref='correction:old';
INSERT INTO worker_job_revisions(work_id,job_id,revision,correction_ref) VALUES('work-history','job:ordinary',2,'correction:new');
INSERT INTO worker_job_revisions(work_id,job_id,revision,correction_ref) VALUES('work-history','job:correction-only',1,'correction:only')`); err != nil {
		t.Fatal(err)
	}
	state, err := loadWorkflowReviewState(t.Context(), db, "work-history")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := state.Groups["job:ordinary"]
	if ordinary.Permission != "first_entry" || ordinary.GroupID != "" || ordinary.CorrectionRef != "correction:new" || state.Groups["job:correction-only"].CorrectionRef != "correction:only" || !state.ExecutionProgressed {
		t.Fatalf("ordinary identity, current correction, or admission lost: %+v", state.Groups)
	}
	if err := workflowAdmitReview(state, "job:ordinary"); err != nil {
		t.Fatalf("ordinary group cannot reuse its own admission: %v", err)
	}
	if err := workflowAdmitReview(state, "job:unrelated"); err == nil {
		t.Fatal("another job reused first-entry admission")
	}
}

func TestG32ReviewBackfillUsesCallerGuard(t *testing.T) {
	db, appendEvent := g32ReviewFixture(t)
	report := reviewBlockFixture()
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "review:migrate", WorkerCapabilityClass: "review"})
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:migrate", Review: report})
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "review:migrate"})
	if _, err := db.Exec(`DELETE FROM workflow_review_state; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	// A missing current row is not permission to reconstruct from the log.
	before, err := readWorkflowReviewFacts(t.Context(), db, "work-history")
	if err != nil || before.Candidate != nil || before.Accepted != nil || len(before.Findings) != 0 {
		t.Fatalf("live read reconstructed from the log: %+v, %v", before, err)
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
		t.Fatalf("backfill consumed its caller's guard: %d, %v", count, err)
	}
	state, err := readWorkflowReviewFacts(t.Context(), tx, "work-history")
	if err != nil || state.Accepted == nil || state.Accepted.AttemptID != "review:migrate" || *state.Candidate != report.OutputCandidate {
		t.Fatalf("migration failed to install exact legacy Review: %+v, %v", state, err)
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// CD-0242 D10: Review consumes the law the active contract pins. A changed
// pin hash stales the accepted Review; an unchanged pin keeps it current.
func TestChangedPinnedLawStalesAcceptedReview(t *testing.T) {
	db, appendEvent := g32ReviewFixture(t)
	if _, err := db.Exec(`INSERT INTO workflow_contracts VALUES('work-history',1,NULL);
INSERT INTO workflow_contract_predicates VALUES('work-history',1,'predicate:primary','exists','{"kind":"exists","surface":"source","subjects":["owner"]}');
INSERT INTO workflow_contract_law_revisions VALUES('work-history',1,'spec:one','sha256:` + strings.Repeat("a", 64) + `')`); err != nil {
		t.Fatal(err)
	}
	deps, err := workflowReviewDependencies(t.Context(), db, "work-history")
	if err != nil {
		t.Fatal(err)
	}
	if deps.ArchitectureNotApplicable || deps.ArchitectureDigest == "" {
		t.Fatalf("pinned law left no architecture basis: %+v", deps)
	}
	report := reviewBlockFixture()
	report.Dependencies = deps
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "review:law", WorkerCapabilityClass: "review"})
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: "review:law", Review: report})
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "review:law"})
	current := func() bool {
		t.Helper()
		state, err := loadWorkflowReviewState(t.Context(), db, "work-history")
		if err != nil || state.Accepted == nil {
			t.Fatalf("review state = %+v, %v", state, err)
		}
		return state.DependenciesCurrent
	}
	if !current() {
		t.Fatal("unchanged law pin staled the accepted Review")
	}
	if _, err := db.Exec(`UPDATE workflow_contract_law_revisions SET content_hash='sha256:` + strings.Repeat("b", 64) + `'`); err != nil {
		t.Fatal(err)
	}
	if current() {
		t.Fatal("changed law pin left the accepted Review current")
	}
}

func TestG32ReviewBackfillParityInterleavings(t *testing.T) {
	db, appendEvent := g32ReviewFixture(t)
	if _, err := db.Exec(`INSERT INTO workflow_contracts VALUES('work-history',1,NULL);
INSERT INTO workflow_contract_predicates VALUES('work-history',1,'predicate:primary','exists','{"kind":"exists","surface":"source","subjects":["owner"]}')`); err != nil {
		t.Fatal(err)
	}
	deps, err := workflowReviewDependencies(t.Context(), db, "work-history")
	if err != nil {
		t.Fatal(err)
	}
	read := func() WorkflowReviewState {
		t.Helper()
		state, err := loadWorkflowReviewState(t.Context(), db, "work-history")
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	parity := func() {
		t.Helper()
		before := read()
		legacy, err := g32LegacyReviewFactsForTest(t.Context(), db, "work-history")
		if err != nil {
			t.Fatal(err)
		}
		currentFacts := workflowReviewFacts{Candidate: before.Candidate, Accepted: before.Accepted, Findings: before.Findings}
		legacyFacts := workflowReviewFacts{Candidate: legacy.Candidate, Accepted: legacy.Accepted, Findings: legacy.Findings}
		if string(mustJSONValue(currentFacts)) != string(mustJSONValue(legacyFacts)) {
			t.Fatalf("current facts differ from legacy selection: got %+v; want %+v", currentFacts, legacyFacts)
		}
		if _, err := db.Exec(`DELETE FROM workflow_review_state`); err != nil {
			t.Fatal(err)
		}
		if err := backfillWorkflowReviewFacts(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		if got := read(); string(mustJSONValue(got)) != string(mustJSONValue(before)) {
			t.Fatalf("backfill differs: got %+v; want %+v", got, before)
		}
	}
	complete := func(id, capability string, report *WorkerReviewBlock) {
		t.Helper()
		appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: id, WorkerCapabilityClass: capability})
		appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: id, Review: report})
		parity()
	}
	accept := func(id string) {
		t.Helper()
		appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: id})
		parity()
	}
	baseline := reviewBlockFixture()
	baseline.Dependencies, baseline.Disposition = deps, "continue"
	baseline.Findings[0].Oracle = &WorkerOracleFinding{OwnerID: "owner:original", ControlIDs: []string{"control:original"}}
	complete("review:baseline", "review", baseline)
	if state := read(); state.Candidate != nil || state.Accepted != nil || state.blocker() == "" {
		t.Fatalf("terminal acquired acceptance authority: %+v", state)
	}
	accept("review:baseline")
	repair := reviewBlockFixture()
	repair.Dependencies = deps
	repair.OutputCandidate.Commit, repair.OutputCandidate.Tree = strings.Repeat("c", 40), strings.Repeat("d", 40)
	repair.Findings[0].InScope, repair.Findings[0].Blocking, repair.Findings[0].Status = false, false, "closed"
	repair.Findings[0].ClosureRefs = []string{"receipt:repair"}
	complete("review:repair", "review", repair)
	if state := read(); *state.Candidate != baseline.OutputCandidate || state.Findings[0].Oracle.OwnerID != "owner:original" || state.blocker() == "" {
		t.Fatalf("unaccepted repair replaced candidate, obligation or blocker: %+v", state)
	}
	accept("review:repair")
	if state := read(); *state.Candidate != repair.OutputCandidate || state.Findings[0].Status != "closed" || state.Accepted.AttemptID != "review:repair" || !state.DependenciesCurrent {
		t.Fatalf("exact repair not current: %+v", state)
	}
	// A late report on the old input cannot reopen a closed current finding.
	complete("review:late", "review", baseline)
	if state := read(); state.blocker() != "" || state.Accepted.AttemptID != "review:repair" {
		t.Fatalf("late terminal changed current Review: %+v", state)
	}
	// Reaccepting an unchanged older result is ordered after all intervening
	// effects. It must not re-observe its terminal and reopen closed findings.
	accept("review:baseline")
	if state := read(); *state.Candidate != baseline.OutputCandidate || state.Accepted.AttemptID != "review:baseline" || state.Findings[0].Status != "closed" {
		t.Fatalf("repeated acceptance order was lost: %+v", state)
	}
	complete("review:reopen", "review", baseline)
	omitted := *baseline
	omitted.Findings = []WorkerReviewFinding{}
	complete("review:omit", "review", &omitted)
	accept("review:omit")
	if state := read(); state.blocker() == "" {
		t.Fatal("omission closed an accumulated blocker")
	}
	before := read()
	complete("implementation:claim", "implementation", repair)
	accept("implementation:claim")
	if got := read(); string(mustJSONValue(got)) != string(mustJSONValue(before)) {
		t.Fatalf("implementation report claimed Review authority: %+v", got)
	}
	if _, err := db.Exec(`UPDATE workflow_contracts SET superseded_by=2;
INSERT INTO workflow_contracts VALUES('work-history',2,NULL);
INSERT INTO workflow_contract_predicates VALUES('work-history',2,'predicate:primary','exists','{"kind":"exists","surface":"changed","subjects":["owner"]}')`); err != nil {
		t.Fatal(err)
	}
	if state := read(); state.DependenciesCurrent || state.Accepted.AttemptID != before.Accepted.AttemptID || state.blocker() == "" {
		t.Fatalf("supersession cached readiness or reset Review: %+v", state)
	}
	parity()
	prior := read()
	if _, err := db.Exec(`INSERT INTO workflow_design_records VALUES('work-history',2,'Use the revised architecture.','[]','["owner.go"]','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if state := read(); state.Dependencies.ArchitectureDigest == prior.Dependencies.ArchitectureDigest || state.Dependencies.ArchitectureNotApplicable || state.Accepted.AttemptID != prior.Accepted.AttemptID || state.DependenciesCurrent {
		t.Fatalf("current design was not recomputed independently of accepted Review: %+v", state)
	}
	parity()
}

func g32CorrectionFixture(t *testing.T) (*Store, *workflowCorrectionHistory, *WorkflowReviewState) {
	t.Helper()
	s := effectReceiptReadFixture(t)
	if _, err := s.db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY); INSERT INTO work_items VALUES('work');
CREATE TABLE workflow_correction_state(work_id TEXT PRIMARY KEY,state TEXT);
CREATE TABLE worker_authorizations(work_id TEXT,attempt_id TEXT,seq INTEGER,payload TEXT);
ALTER TABLE worker_attempts ADD COLUMN failure_kind TEXT;
ALTER TABLE worker_attempts ADD COLUMN failure_detail TEXT`); err != nil {
		t.Fatal(err)
	}
	h := newWorkflowCorrectionState()
	h.records = []workflowCorrectionRecordState{{seq: 7, sourceID: "correction:explicit", fields: workflowCorrectionCompletionFields{ActionID: "request_correction"}}}
	if err := writeWorkflowCorrectionFacts(t.Context(), s.db, "work", h); err != nil {
		t.Fatal(err)
	}
	report := reviewBlockFixture()
	report.Disposition = "correction_required"
	state := &WorkflowReviewState{Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{Report: *report}, DependenciesCurrent: true}
	hideEffectLog(t, s)
	return s, h, state
}

func TestG32DeclaredCorrectionLogBlind(t *testing.T) {
	s, h, state := g32CorrectionFixture(t)
	got, err := workflowDeclaredReviewCorrection(t.Context(), s.db, "work", "", state)
	if err != nil || got != "correction:explicit" {
		t.Fatalf("declared correction = %q, %v", got, err)
	}
	for _, action := range []string{"reject_worker_result", "record_worker_failure"} {
		h.records[0].fields.ActionID = action
		h.records[0].fields.AttemptID = "attempt:failed"
		if err := writeWorkflowCorrectionFacts(t.Context(), s.db, "work", h); err != nil {
			t.Fatal(err)
		}
		got, err := workflowDeclaredReviewCorrection(t.Context(), s.db, "work", "", state)
		if err != nil || got != "" {
			t.Fatalf("%s acquired explicit correction permission: %q, %v", action, got, err)
		}
	}
	h.records[0].fields.ActionID = "request_correction"
	if err := writeWorkflowCorrectionFacts(t.Context(), s.db, "work", h); err != nil {
		t.Fatal(err)
	}
	state.DependenciesCurrent = false
	got, err = workflowDeclaredReviewCorrection(t.Context(), s.db, "work", "", state)
	if err != nil || got != "" {
		t.Fatalf("stale Review acquired explicit correction permission: %q, %v", got, err)
	}
}

func TestG32DeclaredCorrectionRetainsInvalidDataRefusals(t *testing.T) {
	for _, action := range []string{"reject_worker_result", "record_worker_failure"} {
		for _, fault := range []string{"no attempt", "attempt read error"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				s, h, state := g32CorrectionFixture(t)
				h.records[0].fields.ActionID = action
				want := KindInvariantViolation
				detail := "correction completion has no worker attempt"
				if fault == "attempt read error" {
					h.records[0].fields.AttemptID = "attempt:failed"
					if _, err := s.db.Exec(`ALTER TABLE worker_attempts DROP COLUMN failure_kind`); err != nil {
						t.Fatal(err)
					}
					want, detail = KindUnavailable, "cannot read worker attempt detail"
				}
				if err := writeWorkflowCorrectionFacts(t.Context(), s.db, "work", h); err != nil {
					t.Fatal(err)
				}
				_, previous := workflowCorrectionContextForDispatch(t.Context(), s.db, "work", "execution", "")
				if !hasFailureKind(previous, want) {
					t.Fatalf("existing correction validation = %v, want %s", previous, want)
				}
				got, err := workflowDeclaredReviewCorrection(t.Context(), s.db, "work", "", state)
				if !hasFailureKind(err, want) || !strings.Contains(err.Error(), detail) || got != "" {
					t.Fatalf("declared correction = %q, %v; want %s naming %q", got, err, want, detail)
				}
			})
		}
	}
}
