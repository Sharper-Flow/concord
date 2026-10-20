package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Unlike the size-acceptance unit fixture's raw authorization rows, these
// dispatch events use the registered workflow folds and can be replayed.
func seedSplitReplacementAcceptance(t *testing.T, workID string, mutate func(*WorkerCompletedPayload)) sizeAcceptance {
	t.Helper()
	return seedSplitReplacementAcceptanceAtStep(t, workID, "repair", mutate)
}

func seedSplitReplacementAcceptanceAtStep(t *testing.T, workID, step string, mutate func(*WorkerCompletedPayload)) sizeAcceptance {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, step,
		[]string{"verification"}, []string{"verification", "review", "artifact"})
	s := workerJobSizeStore(fixture)
	bootstrapOracleFixtureSubject(t, s, workID)
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["production_size_max"] = 10
	job := &WorkerJobBinding{JobID: "job:split-replacement", Revision: 1}
	recordEditingJobForTest(t, fixture, workID, fields, job)
	actor, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0)+1 FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')=?`, workID, WorkflowActionStarted, step).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt:" + workID
	var directory string
	if err := s.db.QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&directory); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	started := workflowTypedEvent(workID+":replacement-start", WorkflowActionStarted, workID, actor, time.Unix(20, 0).UTC(), version, map[string]any{
		"step_id": step, "action_id": "dispatch_worker", "attempt_epoch": epoch, "actor_ref": actor,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("d", 64), "idempotency_identity": "replacement-dispatch",
	})
	authorized := workflowTypedEvent(workID+":replacement-authorized", WorkflowActionCompleted, workID, actor, time.Unix(20, 0).UTC(), version+1, map[string]any{
		"step_id": step, "action_id": "dispatch_worker", "attempt_epoch": epoch, "actor_ref": actor,
		"worker_attempt_id": attemptID, "worker_job": job, "changed_refs": []string{workID}, "result_evidence_refs": []string{},
		"worker_worktree_identity": workerWorktreeIdentity(directory),
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{started, authorized}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record dispatch authorization: %v", err)
	}
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, job); err != nil {
		t.Fatal(err)
	}
	tree := strings.Repeat("3", 40)
	completed := reportBindingCompletion(t, s, workID, attemptID, reviewGateLane(t, "implementation"), job, func(p *WorkerCompletedPayload) {
		p.ObservedDelivery = &WorkerObservedDelivery{Commit: tree, State: "clean", Base: testJobSizeBaseCommit()}
		p.ProductionSize = &WorkerProductionSize{Base: testJobSizeBaseCommit(), Tree: tree}
		mutate(p)
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}
	return sizeAcceptance{fixture: fixture, s: s, workID: workID, attemptID: attemptID, job: *job, tree: tree}
}

func replacementSplitFields(t *testing.T, a sizeAcceptance, supplemental []WorkerJobMeasuredPath) map[string]any {
	t.Helper()
	fields := splitFields(t, a.workID, a.fixture, a.job, func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		for _, successor := range successors {
			successor["production_size_max"] = 5
		}
		return successors, allocation
	})
	var epoch int64
	if err := a.s.db.QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, a.workID, WorkflowActionCompleted, a.attemptID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	replacement := map[string]any{"attempt_id": a.attemptID, "attempt_epoch": epoch}
	if supplemental != nil {
		replacement["supplemental_classifications"] = supplemental
	}
	fields["split"].(map[string]any)["replaced_attempt"] = replacement
	return fields
}

func TestWorkerJobSplitReplacesOverrunWithoutRejectingIt(t *testing.T) {
	a := seedSplitReplacementAcceptance(t, "split-replaces-overrun", func(p *WorkerCompletedPayload) {
		p.ObservedDelivery.Paths = []WorkerObservedPath{
			{Path: "internal/store/worker_jobs.go", Added: 15},
			{Path: "internal/store/new_test.go", Added: 20},
		}
		p.ProductionSize.Count = 35
	})
	supplemental := []WorkerJobMeasuredPath{{Path: "internal/store/new_test.go", Class: "test"}}
	fields := replacementSplitFields(t, a, supplemental)
	expectedEpoch := fields["split"].(map[string]any)["replaced_attempt"].(map[string]any)["attempt_epoch"].(int64)
	owner, err := WorkflowActorRef(a.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	var frontier int64
	if err := a.s.db.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM domain_events`).Scan(&frontier); err != nil {
		t.Fatal(err)
	}
	if _, live, _, err := workflowFencedWriter(context.Background(), a.s.db, a.workID, owner, frontier); err != nil || !live {
		t.Fatalf("writer before replacement: live=%v, error=%v", live, err)
	}
	if err := recordWorkerJobActionForTest(t, a.s, a.workID, a.fixture.owner, fields); err != nil {
		t.Fatalf("split with replaced overrun: %v", err)
	}
	var attempt, tree, reportRef string
	var count, sizeCap, epoch int64
	err = a.s.db.QueryRow(`SELECT json_extract(payload,'$.replaced_attempt.attempt_id'),json_extract(payload,'$.replaced_attempt.attempt_epoch'),json_extract(payload,'$.replaced_attempt.observed_commit'),json_extract(payload,'$.replaced_attempt.observed_count'),json_extract(payload,'$.replaced_attempt.cap'),json_extract(payload,'$.replaced_attempt.report_ref') FROM domain_events WHERE subject_id=? AND kind=?`, a.workID, WorkerJobSplit).Scan(&attempt, &epoch, &tree, &count, &sizeCap, &reportRef)
	if err != nil || attempt != a.attemptID || epoch != expectedEpoch || tree != a.tree || count != 15 || sizeCap != 10 || reportRef == "" {
		t.Fatalf("replacement evidence = %s/%d %s %d/%d %s, error = %v", attempt, epoch, tree, count, sizeCap, reportRef, err)
	}
	var reportCount int64
	if err := a.s.db.QueryRow(`SELECT json_extract(payload,'$.production_size.count') FROM domain_events WHERE event_id=? AND kind=?`, reportRef, WorkerCompleted).Scan(&reportCount); err != nil || reportCount != 35 {
		t.Fatalf("original report count = %d, error = %v; want immutable 35", reportCount, err)
	}
	views, err := readWorkerJobRevisions(context.Background(), a.s.db, a.workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding.JobID == a.job.JobID && view.Binding.Digest != a.job.Digest {
			t.Fatalf("replacement changed the predecessor's digest from %s to %s", a.job.Digest, view.Binding.Digest)
		}
		for _, measured := range view.MeasurementBasis.Paths {
			if measured.Path == "internal/store/new_test.go" {
				t.Fatal("overrun-only classification entered a recorded job inventory")
			}
		}
	}
	if err := a.s.db.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM domain_events`).Scan(&frontier); err != nil {
		t.Fatal(err)
	}
	if writer, live, _, err := workflowFencedWriter(context.Background(), a.s.db, a.workID, owner, frontier); err != nil || live || writer != owner {
		t.Fatalf("writer after replacement: writer=%s, live=%v, error=%v", writer, live, err)
	}
	var lifecycle string
	if err := a.s.db.QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, a.attemptID).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("replaced attempt lifecycle=%s, error=%v; want completed", lifecycle, err)
	}
	if unsettled, err := workflowUnsettledWorkerAttempt(context.Background(), a.s.db, a.workID, "test", 0); err != nil || unsettled {
		t.Fatalf("replaced attempt unsettled=%v, error=%v; want settled by its split", unsettled, err)
	}
	var dispositionSeq int64
	if err := a.s.db.QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_worker_job' AND json_extract(payload,'$.worker_attempt_id')=?`, a.workID, WorkflowActionCompleted, a.attemptID).Scan(&dispositionSeq); err != nil {
		t.Fatal(err)
	}
	for _, beforeSeq := range []int64{dispositionSeq, dispositionSeq + 1} {
		wantUnsettled := beforeSeq == dispositionSeq
		if unsettled, err := workflowUnsettledWorkerAttempt(context.Background(), a.s.db, a.workID, "test", beforeSeq); err != nil || unsettled != wantUnsettled {
			t.Fatalf("replaced attempt before seq %d: unsettled=%v, error=%v; want %v", beforeSeq, unsettled, err, wantUnsettled)
		}
	}
	var failures int
	if err := a.s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id')=? AND json_extract(payload,'$.action_id') IN ('reject_worker_result','record_worker_failure')`, a.workID, WorkflowActionCompleted, a.attemptID).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("failure/rejection records=%d, error=%v; want neither", failures, err)
	}
	for _, action := range []string{"accept_worker_result", "reject_worker_result"} {
		fields := map[string]any{"attempt_id": a.attemptID, "attempt_epoch": epoch}
		if action == "reject_worker_result" {
			fields["diagnosis"], fields["strategy"] = "the completed attempt was replaced", "work through the split successors"
			fields["predicate_ids"], fields["evidence_refs"] = []string{"predicate:return-route"}, []string{"evidence:return-route-verification"}
		}
		err := runVerdictActionAs(t, a.s, a.workID, action, mustJSONValue(fields), 0, a.fixture.owner)
		if err == nil || !strings.Contains(err.Error(), "replaced") {
			t.Fatalf("%s after replacement = %v; want replaced refusal", action, err)
		}
	}
	if err := RebuildFromLog(context.Background(), a.s); err != nil {
		t.Fatalf("rebuild replaced overrun: %v", err)
	}
}

func TestWorkerJobSplitReplacementClassificationsCannotInventOverrun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []WorkerObservedPath
		count int64
		want  string
	}{
		{"under-cap", []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 5}, {Path: "internal/store/new_test.go", Added: 20}}, 25, "does not exceed"},
		{"unmeasurable", []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 15}, {Path: "internal/store/new_test.go", Added: 20}, {Path: "internal/store/data.bin", Binary: true}}, 35, "binary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := seedSplitReplacementAcceptance(t, "split-replacement-"+tc.name, func(p *WorkerCompletedPayload) {
				p.ObservedDelivery.Paths, p.ProductionSize.Count = tc.paths, tc.count
			})
			err := recordWorkerJobActionForTest(t, a.s, a.workID, a.fixture.owner, replacementSplitFields(t, a, []WorkerJobMeasuredPath{{Path: "internal/store/new_test.go", Class: "test"}}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("split classification refusal = %v, want %q", err, tc.want)
			}
			var splits int
			if err := a.s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, a.workID, WorkerJobSplit).Scan(&splits); err != nil || splits != 0 {
				t.Fatalf("splits after refusal = %d, error = %v", splits, err)
			}
		})
	}
}

func TestWorkerJobSplitReplacementIdentityAndEpoch(t *testing.T) {
	for _, tc := range []struct {
		name, member string
		value        any
		want         string
	}{
		{"wrong-attempt", "attempt_id", "attempt:another", "awaits disposition"},
		{"wrong-epoch", "attempt_epoch", int64(2147483647), "dispatched epoch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := seedSplitReplacementAcceptance(t, "split-identity-"+tc.name, func(p *WorkerCompletedPayload) {
				p.ObservedDelivery.Paths = []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 15}}
				p.ProductionSize.Count = 15
			})
			fields := replacementSplitFields(t, a, nil)
			fields["split"].(map[string]any)["replaced_attempt"].(map[string]any)[tc.member] = tc.value
			if err := recordWorkerJobActionForTest(t, a.s, a.workID, a.fixture.owner, fields); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("replacement identity refusal = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestWorkerJobSplitExecutorCannotSupplyOverrunClassifications(t *testing.T) {
	a := seedSplitReplacementAcceptance(t, "split-executor-classifies", func(p *WorkerCompletedPayload) {
		p.ObservedDelivery.Paths = []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 15}, {Path: "internal/store/new_test.go", Added: 20}}
		p.ProductionSize.Count = 35
	})
	executor := workflowActorForRef(t, a.s, crossAttemptDispatchedLaneActor(t, a.s, a.workID, a.attemptID))
	fields := replacementSplitFields(t, a, []WorkerJobMeasuredPath{{Path: "internal/store/new_test.go", Class: "test"}})
	err := recordWorkerJobActionForTest(t, a.s, a.workID, executor, fields)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("executor's overrun classification = %v; want unauthorized", err)
	}
	executorRef, err := WorkflowActorRef(executor)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	input := fields["split"].(map[string]any)["replaced_attempt"].(map[string]any)
	_, err = admitWorkerJobSplitReplacementTx(context.Background(), tx, a.workID, executorRef, a.job, &workerJobSplitReplacementInput{
		AttemptID: a.attemptID, AttemptEpoch: input["attempt_epoch"].(int64),
		SupplementalClassifications: []WorkerJobMeasuredPath{{Path: "internal/store/new_test.go", Class: "test"}},
	})
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(err.Error(), "executing actor cannot author its own verdict") {
		t.Fatalf("replacement admission by executor = %v; want executor-distinctness refusal", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if splits, err := readWorkerJobSplitsTx(context.Background(), a.s.db, a.workID); err != nil || len(splits) != 0 {
		t.Fatalf("executor-authored splits = %+v, error = %v; want none", splits, err)
	}
}

func TestWorkerJobSplitReplacementVersionBoundaries(t *testing.T) {
	const workID = "split-replacement-version"
	fixture := workerJobSizeFixture(t, workID)
	owner, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version int
		want    string
	}{
		{6, "requires action_completed payload version >= 7"},
		{7, "requires a replacement split from the same operation"},
	} {
		version := readWorkVersion(t, fixture.store, workID)
		event := workflowTypedEvent("completion-without-split", WorkflowActionCompleted, workID, owner, time.Unix(23, 0).UTC(), version, map[string]any{
			"step_id": "repair", "action_id": "record_worker_job", "attempt_epoch": 1, "worker_attempt_id": "attempt:unrecorded", "actor_ref": owner,
		})
		event.PayloadVersion = tc.version
		err := applyWorkflowTestOperation(context.Background(), fixture.store, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("v%d replacement completion refusal = %v, want %q", tc.version, err, tc.want)
		}
	}
	version, resulting := int64(1), int64(2)
	payload := WorkerJobSplitPayload{
		WorkflowVersionFields: WorkflowVersionFields{WorkID: workID, ExpectedVersion: &version, ResultingVersion: &resulting},
		ReplacedAttempt:       &WorkerJobSplitReplacedAttempt{},
	}
	event := Event{SubjectType: SubjectWorkItem, SubjectID: workID, replaySourcePayloadVersion: 1}
	if err := validateWorkerJobSplitPayload(event, payload); err == nil || !strings.Contains(err.Error(), "requires payload version >= 2") {
		t.Fatalf("v1 replacement split refusal = %v; want source-version refusal", err)
	}
}

func TestWorkerJobSplitReplacementRefusesAnotherUnsettledAttempt(t *testing.T) {
	for _, lifecycle := range []string{"dispatched", "completed"} {
		t.Run(lifecycle, func(t *testing.T) {
			a := seedSplitReplacementAcceptance(t, "split-replacement-other-"+lifecycle, func(p *WorkerCompletedPayload) {
				p.ObservedDelivery.Paths = []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 15}}
				p.ProductionSize.Count = 15
			})
			fields := replacementSplitFields(t, a, nil)
			db := a.s.DatabaseForTesting()
			var completedAt any
			if lifecycle == "completed" {
				completedAt = "2026-10-19T00:01:00Z"
			}
			if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at) VALUES(?, 'attempt:second', 'lane:review', 1, ?, 'review', 'provider/model', '1.0', '1.0', ?, '2026-10-19T00:00:00Z', ?); DELETE FROM fold_guard`, a.workID, "sha256:"+strings.Repeat("a", 64), lifecycle, completedAt); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, "split-replacement-second-dispatch", WorkflowActionCompleted, SubjectWorkItem, a.workID, a.fixture.owner.PrincipalRef, "2026-10-19T00:00:00Z", 6, `{"action_id":"dispatch_worker","worker_attempt_id":"attempt:second","worker_job":`+string(mustJSONValue(a.job))+`}`); err != nil {
				t.Fatal(err)
			}
			want := "attempt attempt:second is active"
			if lifecycle == "completed" {
				want = "attempt attempt:second awaits disposition"
			}
			if err := recordWorkerJobActionForTest(t, a.s, a.workID, a.fixture.owner, fields); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("replacement beside another %s attempt = %v; want %q", lifecycle, err, want)
			}
		})
	}
}

func TestWorkerJobSplitReplacementSettlesRefineCorrection(t *testing.T) {
	a := seedSplitReplacementAcceptanceAtStep(t, "split-replacement-refine", "refine", func(p *WorkerCompletedPayload) {
		p.ObservedDelivery.Paths = []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 15}}
		p.ProductionSize.Count = 15
	})
	if err := recordWorkerJobActionForTest(t, a.s, a.workID, a.fixture.owner, replacementSplitFields(t, a, nil)); err != nil {
		t.Fatal(err)
	}
	owner, err := WorkflowActorRef(a.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewID := "attempt:split-replacement-refine-review"
	epoch := reviewGateRunAttemptWithVerdict(t, a.s, a.workID, reviewID, reviewGateLane(t, "review"), owner, "ship", 100)
	if err := runVerdictActionAs(t, a.s, a.workID, "accept_worker_evidence", mustJSONValue(map[string]any{"attempt_id": reviewID, "attempt_epoch": epoch}), 0, reviewGateAcceptor(a.workID)); err != nil {
		t.Fatal(err)
	}
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	for _, replay := range []bool{false, true} {
		if replay {
			if err := RebuildFromLog(context.Background(), a.s); err != nil {
				t.Fatal(err)
			}
		}
		if decision, err := workflowRefineReviewCorrectionDecision(context.Background(), a.s.db, a.workID, definition, "refine", "test", 0); err != nil || decision != workflowRefineReviewCorrectionOpen {
			t.Fatalf("refine correction after split replacement (replay=%v) = %v, error = %v; want open", replay, decision, err)
		}
	}
}

func TestWorkerJobSplitReplacementFoldRefusesUnobservedForeignPath(t *testing.T) {
	a := seedSplitReplacementAcceptance(t, "split-replacement-foreign-path", func(p *WorkerCompletedPayload) {
		p.ObservedDelivery.Paths = []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 15}}
		p.ProductionSize.Count = 15
	})
	if err := recordWorkerJobActionForTest(t, a.s, a.workID, a.fixture.owner, replacementSplitFields(t, a, nil)); err != nil {
		t.Fatal(err)
	}
	splits, err := readWorkerJobSplitsTx(context.Background(), a.s.db, a.workID)
	if err != nil || len(splits) != 1 {
		t.Fatalf("splits=%v, error=%v; want one replacement", splits, err)
	}
	event := Event{SubjectID: a.workID}
	if err := a.s.db.QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=?`, a.workID, WorkerJobSplit).Scan(&event.Seq); err != nil {
		t.Fatal(err)
	}
	tx, err := a.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	predecessor, err := readWorkerJobRecordedTx(context.Background(), tx, a.workID, a.job)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWorkerJobSplitReplacementTx(context.Background(), tx, event, splits[0], predecessor); err != nil {
		t.Fatalf("original replacement evidence: %v", err)
	}
	splits[0].ReplacedAttempt.ForeignAcceptedPaths = []string{"internal/store/unobserved.go"}
	if err := verifyWorkerJobSplitReplacementTx(context.Background(), tx, event, splits[0], predecessor); err == nil || !strings.Contains(err.Error(), "foreign path is not in its sealed observation") {
		t.Fatalf("unobserved foreign path = %v; want sealed-observation refusal", err)
	}
}
