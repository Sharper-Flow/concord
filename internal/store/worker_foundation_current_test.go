package store

import (
	"context"
	"database/sql"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func workerFoundationCurrentDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
		CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,event_id TEXT,subject_type TEXT,subject_id TEXT,kind TEXT,payload TEXT,payload_version INTEGER DEFAULT 1);
		CREATE TABLE worker_authorizations(work_id TEXT,attempt_id TEXT,seq INTEGER,contract_version INTEGER,payload TEXT);
		CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,lane_id TEXT,lane_digest TEXT,capability_class TEXT,lifecycle_state TEXT,dispatch_seq INTEGER,dispatch_payload TEXT);
		CREATE TABLE worker_job_revisions(work_id TEXT,job_id TEXT,revision INTEGER,state TEXT,satisfied_result_ref TEXT,satisfied_seq INTEGER,path_scope TEXT,digest TEXT,recorded_seq INTEGER,recorded_event_id TEXT,recorded_payload TEXT);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func foundationAuthorizationForTest(t *testing.T, db *sql.DB, seq, contract int64, attempt, laneID, subject, tree string) {
	t.Helper()
	digest := registeredLaneIdentity(t, laneID)
	payload := mustJSONValue(map[string]any{
		"action_id": "dispatch_worker", "worker_attempt_id": attempt,
		"worker_lane_id": laneID, "worker_lane_digest": digest,
		"worker_subject_commit": subject, "worker_worktree_identity": tree,
	})
	if _, err := db.Exec(`INSERT INTO worker_authorizations VALUES('work',?,?,?,?)`, attempt, seq, contract, string(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domain_events(seq,event_id,subject_type,subject_id,kind,payload) VALUES(?,?,'work_item','work',?,?)`, seq, attempt, WorkflowActionCompleted, string(payload)); err != nil {
		t.Fatal(err)
	}
}

// foundationDispatchForTest records the authorization and the attempt row the
// dispatch_worker fold binds, with the lane's capability class.
func foundationDispatchForTest(t *testing.T, db *sql.DB, seq, contract int64, attempt, laneID, subject, tree string) {
	t.Helper()
	foundationAuthorizationForTest(t, db, seq, contract, attempt, laneID, subject, tree)
	class := ""
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.ID == laneID {
			class = lane.CapabilityClass
		}
	}
	foundationAttemptForTest(t, db, attempt, laneID, class)
}

func foundationAttemptForTest(t *testing.T, db *sql.DB, attempt, laneID string, class any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO worker_attempts(work_id,attempt_id,lane_id,capability_class,lifecycle_state) VALUES('work',?,?,?,'completed')`, attempt, laneID, class); err != nil {
		t.Fatal(err)
	}
}

// staleFoundationAuthorizationForTest records an authorization whose lane
// digest no current lane content carries, as a dispatch recorded before a lane
// content change leaves it.
func staleFoundationAuthorizationForTest(t *testing.T, db *sql.DB, seq int64, attempt, laneID, subject, tree string) {
	t.Helper()
	foundationAuthorizationForTest(t, db, seq, 1, attempt, laneID, subject, tree)
	stale := "sha256:" + strings.Repeat("0", 64)
	for _, table := range []string{"worker_authorizations", "domain_events"} {
		column := "attempt_id"
		if table == "domain_events" {
			column = "event_id"
		}
		if _, err := db.Exec(`UPDATE `+table+` SET payload=json_set(payload,'$.worker_lane_digest',?) WHERE `+column+`=?`, stale, attempt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LookupLane(laneID, stale); !hasFailureKind(err, KindLaneDefinitionDigestMismatch) {
		t.Fatalf("stale lane digest still resolves: %v", err)
	}
}

func TestWorkerEditingFoundationClassifiesRecordedClassNotCurrentLane(t *testing.T) {
	subject, later := strings.Repeat("a", 40), strings.Repeat("b", 40)
	t.Run("stale research before current implement", func(t *testing.T) {
		db := workerFoundationCurrentDB(t)
		staleFoundationAuthorizationForTest(t, db, 2, "attempt:research", "research", later, "claim")
		foundationAttemptForTest(t, db, "attempt:research", "research", "research")
		foundationDispatchForTest(t, db, 3, 1, "attempt:implement", "implement", subject, "claim")
		base, err := firstWorkerEditingSubject(t.Context(), db, "work", "claim", 10)
		if err != nil || base != subject {
			t.Fatalf("stale research history blocked the Review foundation: base=%q err=%v", base, err)
		}
	})
	t.Run("stale editing row is still a foundation", func(t *testing.T) {
		db := workerFoundationCurrentDB(t)
		staleFoundationAuthorizationForTest(t, db, 2, "attempt:implement", "implement", subject, "claim")
		foundationAttemptForTest(t, db, "attempt:implement", "implement", "implementation")
		foundationDispatchForTest(t, db, 3, 1, "attempt:later", "implement", later, "claim")
		base, err := firstWorkerEditingSubject(t.Context(), db, "work", "claim", 10)
		if err != nil || base != subject {
			t.Fatalf("stale editing dispatch lost its foundation: base=%q err=%v", base, err)
		}
	})
	for _, refusal := range []struct {
		name, class, detail string
		attempt, noTree     bool
	}{
		{name: "stale editing row without worktree identity", class: "implementation", attempt: true, noTree: true, detail: "editing authorization has no recorded worktree identity"},
		{name: "missing attempt", detail: "earlier authorization has no recorded worker attempt"},
		{name: "missing class", attempt: true, detail: "earlier worker attempt records no known capability class"},
		{name: "unknown class", class: "unknown_class", attempt: true, detail: "earlier worker attempt records no known capability class"},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			db := workerFoundationCurrentDB(t)
			tree := "claim"
			if refusal.noTree {
				tree = ""
			}
			staleFoundationAuthorizationForTest(t, db, 2, "attempt:earlier", "implement", subject, tree)
			if refusal.attempt {
				var class any
				if refusal.class != "" {
					class = refusal.class
				}
				foundationAttemptForTest(t, db, "attempt:earlier", "implement", class)
			}
			foundationDispatchForTest(t, db, 3, 1, "attempt:later", "implement", later, "claim")
			base, err := firstWorkerEditingSubject(t.Context(), db, "work", "claim", 10)
			var failure *Failure
			if base != "" || !failureAs(err, &failure) || failure.Kind != KindUnauthorizedDispatch || failure.Detail != refusal.detail {
				t.Fatalf("unclassifiable earlier authorization did not refuse: base=%q err=%v", base, err)
			}
		})
	}
}

func TestLaneFoundationClassesMatchLaneCapabilities(t *testing.T) {
	classes := map[string]bool{}
	for _, lane := range BuiltinLaneDefinitions() {
		classes[lane.CapabilityClass] = slices.Contains(lane.Capabilities, "edit_scoped_files") && !slices.Contains(lane.Capabilities, "inspect_diff")
	}
	if !reflect.DeepEqual(classes, laneFoundationClasses) {
		t.Fatalf("foundation classes = %v, current lanes declare %v", laneFoundationClasses, classes)
	}
}

func TestWorkerEditingFoundationWithoutLog(t *testing.T) {
	const reviewSeq = 20
	foundation, successor := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, scenario := range []struct {
		name      string
		newEditor bool
		missing   string
		otherTree bool
	}{
		{name: "correction-only contract and repairing review keep foundation"},
		{name: "new editing contract preserves accumulated foundation", newEditor: true},
		{name: "missing first subject refuses", missing: "subject"},
		{name: "missing first worktree refuses", missing: "tree"},
		{name: "newest contract on another tree does not replace this tree's foundation", newEditor: true, otherTree: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := workerFoundationCurrentDB(t)
			for i, seq := range []int{1, 6, 12} {
				if _, err := db.Exec(`INSERT INTO domain_events(seq,event_id,subject_type,subject_id,kind,payload) VALUES(?,?,'work_item','work',?,?)`, seq, "contract", WorkflowContractApproved, string(mustJSONValue(map[string]any{"contract_version": i + 1}))); err != nil {
					t.Fatal(err)
				}
			}
			subject, tree := foundation, "claim"
			if scenario.missing == "subject" {
				subject = ""
			}
			if scenario.missing == "tree" {
				tree = ""
			}
			foundationDispatchForTest(t, db, 2, 1, "first", "implement", subject, tree)
			foundationDispatchForTest(t, db, 3, 1, "other-tree", "implement", successor, "other-claim")
			foundationDispatchForTest(t, db, 4, 1, "later", "implement", successor, "claim")
			foundationDispatchForTest(t, db, 7, 2, "repairing-review", "review", successor, "claim")
			if scenario.newEditor {
				newTree := "claim"
				if scenario.otherTree {
					newTree = "other-claim"
				}
				foundationDispatchForTest(t, db, 13, 3, "new-first", "implement", successor, newTree)
			}
			// Neither an authorization at the review boundary nor a later one
			// can select the review's foundation.
			foundationDispatchForTest(t, db, reviewSeq, 4, "at-review", "implement", strings.Repeat("c", 40), "claim")
			foundationDispatchForTest(t, db, reviewSeq+1, 4, "after-review", "implement", strings.Repeat("c", 40), "claim")
			check := func() {
				t.Helper()
				got, err := firstWorkerEditingSubject(t.Context(), db, "work", "claim", reviewSeq)
				if scenario.missing != "" {
					if got != "" || !hasFailureKind(err, KindUnauthorizedDispatch) {
						t.Fatalf("missing first %s: base=%q error=%v", scenario.missing, got, err)
					}
					return
				}
				want := foundation
				if err != nil || got != want {
					t.Fatalf("base=%q error=%v, want %q", got, err, want)
				}
			}
			check()
			if _, err := db.Exec(`ALTER TABLE domain_events RENAME TO domain_events_hidden`); err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}

func TestWorkerCallerCorrectionWindowWithoutLog(t *testing.T) {
	db := workerFoundationCurrentDB(t)
	for _, row := range []struct {
		job, attempt, state string
		seq                 int
	}{
		{"job:done", "before", "completed", 2},
		{"job:done", "before-cutoff-failure", "failed", 4},
		{"job:done", "active", "in_flight", 6},
		{"job:correction", "later-failure", "failed", 9},
		{"job:correction", "earlier-completion", "completed", 7},
		{"job:reset", "reset-failure", "failed", 3},
		{"job:done", "research", "failed", 8},
	} {
		capability := "implementation"
		if row.attempt == "research" {
			capability = "research"
		}
		payload := string(mustJSONValue(map[string]any{"attempt_id": row.attempt, "capability_class": capability, "worker_job": WorkerJobBinding{JobID: row.job}}))
		if _, err := db.Exec(`INSERT INTO worker_attempts(work_id,attempt_id,capability_class,lifecycle_state,dispatch_seq,dispatch_payload) VALUES('work',?,?,?,?,?)`, row.attempt, capability, row.state, row.seq, payload); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO domain_events(seq,event_id,subject_type,subject_id,kind,payload) VALUES(?,?,'work_item','work',?,?)`, row.seq, row.attempt, WorkerDispatched, payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO domain_events(seq,event_id,subject_type,subject_id,kind,payload) VALUES(5,'satisfied','work_item','work','workflow.action_completed','{}');
		INSERT INTO worker_job_revisions(work_id,job_id,revision,state,satisfied_result_ref,satisfied_seq,path_scope) VALUES
		('work','job:done',1,'satisfied','satisfied',5,'["done/old"]'),
		('work','job:done',2,'recorded',NULL,NULL,'["done/current"]'),
		('work','job:correction',1,'recorded',NULL,NULL,'["correction/old"]'),
		('work','job:correction',2,'recorded',NULL,NULL,'["correction/current"]'),
		('work','job:reset',1,'recorded',NULL,NULL,'["reset/current"]');
	`); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		corrections, attempts, err := workerJobCorrectionStatesTx(context.Background(), db, "work")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(corrections, map[string]bool{"job:correction": true, "job:reset": true}) || !reflect.DeepEqual(attempts, map[string][]string{
			"job:done": {"active"}, "job:correction": {"earlier-completion", "later-failure"}, "job:reset": {"reset-failure"},
		}) {
			t.Fatalf("corrections=%v attempts=%v", corrections, attempts)
		}
		paths, err := workerJobCorrectionPathsTx(t.Context(), db, "work")
		if err != nil || !reflect.DeepEqual(paths, map[string][]string{"job:correction": {"correction/current"}, "job:reset": {"reset/current"}}) {
			t.Fatalf("correction paths=%v error=%v", paths, err)
		}
		cutoff, err := workerJobSatisfactionSeqTx(t.Context(), db, "work", "job:done")
		if err != nil || cutoff != 5 {
			t.Fatalf("cutoff=%d error=%v", cutoff, err)
		}
	}
	check()
	if _, err := db.Exec(`ALTER TABLE domain_events RENAME TO domain_events_hidden`); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestWorkerDiffJobBindingWithoutLog(t *testing.T) {
	db := workerFoundationCurrentDB(t)
	expected, resulting := int64(1), int64(2)
	job := WorkerJobRecordedPayload{
		WorkflowVersionFields: WorkflowVersionFields{WorkID: "work", ExpectedVersion: &expected, ResultingVersion: &resulting},
		JobID:                 "job:diff", Revision: 1, ContractVersion: 1, Objective: "Read the scoped delta", StoppingCondition: "The diff is read",
		ProjectScope: "project", PathScope: []string{"owner/file.go"}, PredicateIDs: []string{}, Checks: []string{}, Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
	}
	job.Digest = DeriveWorkerJobDigest(job)
	if _, err := db.Exec(`INSERT INTO worker_job_revisions(work_id,job_id,revision,digest,recorded_seq,recorded_event_id,recorded_payload) VALUES('work',?,?,?,3,'recorded',?)`, job.JobID, job.Revision, job.Digest, string(mustJSONValue(job))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domain_events(seq,event_id,subject_type,subject_id,kind,payload) VALUES(3,'recorded','work_item','work',?,?)`, WorkerJobRecorded, string(mustJSONValue(job))); err != nil {
		t.Fatal(err)
	}
	binding := WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest}
	check := func() {
		t.Helper()
		got, err := readWorkerDiffJobTx(t.Context(), db, "work", binding, 1, 4)
		if err != nil || !reflect.DeepEqual(got, job) {
			t.Fatalf("recorded job=%+v error=%v", got, err)
		}
		for _, test := range []struct {
			binding  WorkerJobBinding
			contract int64
			seq      int64
		}{
			{binding, 1, 3}, {binding, 1, 2}, {binding, 2, 4},
			{WorkerJobBinding{JobID: binding.JobID, Revision: 2, Digest: binding.Digest}, 1, 4},
			{WorkerJobBinding{JobID: binding.JobID, Revision: 1, Digest: "sha256:" + strings.Repeat("0", 64)}, 1, 4},
		} {
			_, err := readWorkerDiffJobTx(t.Context(), db, "work", test.binding, test.contract, test.seq)
			if !hasFailureKind(err, KindUnauthorizedDispatch) {
				t.Fatalf("invalid binding/window %+v returned %v", test, err)
			}
		}
	}
	check()
	if _, err := db.Exec(`ALTER TABLE domain_events RENAME TO domain_events_hidden`); err != nil {
		t.Fatal(err)
	}
	check()
	for _, paths := range [][]string{{}, {"../outside"}} {
		invalid := job
		invalid.PathScope = paths
		invalid.Digest = DeriveWorkerJobDigest(invalid)
		if _, err := db.Exec(`UPDATE worker_job_revisions SET digest=?,recorded_payload=?`, invalid.Digest, string(mustJSONValue(invalid))); err != nil {
			t.Fatal(err)
		}
		invalidBinding := WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: invalid.Digest}
		_, err := readWorkerDiffJobTx(t.Context(), db, "work", invalidBinding, 1, 4)
		if !hasFailureKind(err, KindUnauthorizedDispatch) {
			t.Fatalf("invalid scope %v returned %v", paths, err)
		}
	}
}
