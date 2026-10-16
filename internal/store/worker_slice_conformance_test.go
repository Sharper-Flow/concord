package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type sliceSampleFixture struct {
	store                          *Store
	workID, attemptID, stepID      string
	claimPath, repoPath, locatorID string
	lane                           LaneDefinition
	job                            WorkerJobRecordedPayload
}

func sliceSampleLane(t *testing.T, id string) LaneDefinition {
	t.Helper()
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.ID == id {
			return lane
		}
	}
	t.Fatalf("no registered lane %q", id)
	return LaneDefinition{}
}

// Raw histories let the sampler exercise architecture-bound dispatches before
// live workflow activation. Every store and path belongs to this test only.
// Neither path exists: SQL sampling must not resolve paths or invoke Git.
func seedSliceSampleFixture(t *testing.T, laneID string) sliceSampleFixture {
	t.Helper()
	const workID = "slice-sample"
	base := seedJobBindingFixture(t, workID, jobBindingPinRef, true, true, nil)
	t.Cleanup(func() { _ = base.store.Close() })
	f := sliceSampleFixture{
		store: base.store, workID: workID, attemptID: "attempt:slice-sample",
		claimPath: filepath.Join(t.TempDir(), "unmaterialized-claim"),
		repoPath:  filepath.Join(t.TempDir(), "unmaterialized-repository"),
		locatorID: "locator:slice-sample", lane: sliceSampleLane(t, laneID),
		job: jobBindingEnvelopePayload(base, "job:slice-sample", true),
	}
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&f.stepID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT locator_id FROM project_locators WHERE project_id='project' AND kind='canonical_path'`).Scan(&f.locatorID); err != nil {
		t.Fatal(err)
	}
	f.job.AcceptanceOracle = &AcceptanceOracle{
		Owners: []OracleOwner{{
			OwnerID: "owner:fixture", DomainID: "domain:fixture",
			Mechanism:  OracleMechanism{ProjectID: "project", Path: jobBindingSliceFiles[0], EntryPoint: "sampleSliceConformance"},
			Obligation: "sample exact editing authority", PredicateIDs: []string{"predicate:return-route"},
		}},
		Cases: []OracleCase{{
			CaseID: "case:fixture", OwnerID: "owner:fixture", EntryPoint: "sampleSliceConformance",
			InputClass: "completed editing attempt", ExpectedState: "exact authority retained", ControlIDs: []string{"control:fixture"},
		}},
		Controls: []OracleControl{{
			ControlID: "control:fixture", OwnerID: "owner:fixture", PredicateIDs: []string{"predicate:return-route"}, CaseIDs: []string{"case:fixture"},
			RecipeSource: WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "checks/fixture.json", CommitOID: strings.Repeat("a", 40)},
			Argv:         []string{"go", "test", "-run", "^TestFixture$", "./internal/store"}, Cwd: ".",
			ExpectedResult: OracleExpectedResultPass, RequiredEvidenceRole: OracleEvidenceRoleReported,
			ReadinessEvidenceRefs: []string{"native-run:fixture"},
		}},
	}
	f.job.Digest = DeriveWorkerJobDigest(f.job)
	sliceSampleWrite(t, f.store, func(tx *sql.Tx) {
		sliceSampleInsertJob(t, tx, f.workID, f.job, "sample-job")
		sliceSampleExec(t, tx, `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,'completed','2026-10-01T00:00:00Z','2026-10-01T00:00:01Z')`,
			f.workID, f.attemptID, f.lane.ID, f.lane.Version, f.lane.Digest, f.lane.CapabilityClass, preferredModelForLane(f.lane), WorkerPacketSchemaVersion, WorkerReportSchemaVersion)
		sliceSampleExec(t, tx, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?, 'project','claim:sample','change/sample',?,?,'repo:sample','active','2026-10-01T00:00:00Z','{}')`, WorktreeSetID(f.workID), strings.Repeat("b", 40), f.claimPath)
		sliceSampleExec(t, tx, `UPDATE project_locators SET locator_value=?,normalized_value=? WHERE locator_id=?`, f.repoPath, f.repoPath, f.locatorID)
		sliceSampleInsertWindow(t, tx, f, "sample-window", nil)
		binding := WorkerJobBinding{JobID: f.job.JobID, Revision: f.job.Revision, Digest: f.job.Digest}
		event := implementDispatchEvidenceEvent(f.workID, f.attemptID, f.lane, binding)
		sliceSampleInsertEvent(t, tx, f.workID, "sample-dispatched", WorkerDispatched, json.RawMessage(event.Payload))
	})
	return f
}

func sliceSampleExec(t *testing.T, tx *sql.Tx, statement string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(context.Background(), statement, args...); err != nil {
		t.Fatalf("synthetic SQL %s: %v", statement, err)
	}
}

func sliceSampleWrite(t *testing.T, s *Store, write func(*sql.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	write(tx)
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func sliceSampleInsertEvent(t *testing.T, tx *sql.Tx, workID, eventID, kind string, payload any) {
	t.Helper()
	sliceSampleExec(t, tx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,'work_item',?,'worker:test','2026-10-01T00:00:00Z',1,?)`, eventID, kind, workID, string(mustJSONValue(payload)))
}

func sliceSampleInsertWindow(t *testing.T, tx *sql.Tx, f sliceSampleFixture, label string, overrides map[string]any) {
	t.Helper()
	sliceSampleInsertEvent(t, tx, f.workID, label+"-start", WorkflowActionStarted, map[string]any{
		"work_id": f.workID, "step_id": f.stepID, "action_id": "dispatch_worker", "attempt_epoch": 1,
	})
	payload := map[string]any{
		"work_id": f.workID, "step_id": f.stepID, "action_id": "dispatch_worker", "attempt_epoch": 1,
		"worker_attempt_id": f.attemptID, "worker_subject_commit": strings.Repeat("a", 40),
		"worker_packet_digest": "sha256:" + strings.Repeat("f", 64), "worker_worktree_identity": workerWorktreeIdentity(f.claimPath),
		"worker_job": WorkerJobBinding{JobID: f.job.JobID, Revision: f.job.Revision, Digest: f.job.Digest},
	}
	for key, value := range overrides {
		payload[key] = value
	}
	sliceSampleInsertEvent(t, tx, f.workID, label+"-complete", WorkflowActionCompleted, payload)
}

func sliceSampleInsertJob(t *testing.T, tx *sql.Tx, workID string, job WorkerJobRecordedPayload, label string) {
	t.Helper()
	sliceSampleExec(t, tx, `INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'recorded','2026-10-01T00:00:00Z')`,
		workID, job.JobID, job.Revision, job.ContractVersion, job.Objective, job.StoppingCondition, job.ProjectScope,
		string(mustJSONValue(job.PathScope)), string(mustJSONValue(job.PredicateIDs)), string(mustJSONValue(job.Checks)), string(mustJSONValue(job.Prerequisites)), string(mustJSONValue(job.UnresolvedRefs)), job.Digest)
	sliceSampleInsertEvent(t, tx, workID, label, WorkerJobRecorded, job)
}

func TestSliceConformancePreparationSampleFixture(t *testing.T) {
	f := seedSliceSampleFixture(t, "implement")
	ctx := context.Background()
	q := f.store.DatabaseForTesting()
	attempt, err := workerAttemptByIDCore(ctx, q, f.attemptID)
	if err != nil || attempt.WorkID != f.workID || attempt.LifecycleState != "completed" {
		t.Fatalf("attempt = %+v, err = %v", attempt, err)
	}
	window, err := findAuthorizedDispatchWindowCore(ctx, q, f.workID, f.attemptID)
	if err != nil || window.StepID != f.stepID || window.SubjectCommit != strings.Repeat("a", 40) {
		t.Fatalf("window = %+v, err = %v", window, err)
	}
	dispatched, err := workflowDispatchedJobForAttempt(ctx, q, f.workID, f.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	found, authorized, err := dispatchCompletionJobForAttempt(ctx, q, f.workID, f.attemptID)
	if err != nil || !found || dispatched == nil || !sameWorkerJob(dispatched, authorized) {
		t.Fatalf("dispatched = %+v, authorized = %+v, found = %t, err = %v", dispatched, authorized, found, err)
	}
	views, err := readWorkerJobRevisions(ctx, q, f.workID)
	if err != nil || len(views) != 1 || views[0].Binding != *dispatched {
		t.Fatalf("job views = %+v, err = %v", views, err)
	}
	project, err := workflowJobBindingAuthorityTx(ctx, q, f.workID, views[0].ArchitectureBinding, views[0].PathScope, views[0].Checks, views[0].AcceptanceOracle)
	if err != nil || project != "project" {
		t.Fatalf("design authority Project = %q, err = %v", project, err)
	}
	locator, path, err := uniqueProjectRepositoryLocator(ctx, q, project)
	if err != nil || locator != f.locatorID || path != f.repoPath {
		t.Fatalf("locator = %q, path = %q, err = %v", locator, path, err)
	}
}

// A deadline bounds a regression that tries to acquire the Store pool while
// this caller already holds its sole connection in a transaction.
func sliceSampleTx(t *testing.T, s *Store) (context.Context, *sql.Tx) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return ctx, tx
}

type sliceSampleNoGit struct{ t *testing.T }

func (runner sliceSampleNoGit) Run(context.Context, string, ...string) ([]byte, error) {
	runner.t.Fatal("the SQL-only sampler or revalidation invoked Git")
	return nil, errors.New("Git is forbidden during SQL sampling")
}

type sliceSampleNarrowQueryer struct {
	queryer
	t *testing.T
}

func (q sliceSampleNarrowQueryer) check(statement string) {
	q.t.Helper()
	for _, unrelated := range []string{"work_context", "oracle_subject_receipts", "observed_delivery"} {
		if strings.Contains(strings.ToLower(statement), unrelated) {
			q.t.Fatalf("slice sampling read unrelated %s authority: %s", unrelated, statement)
		}
	}
}

func (q sliceSampleNarrowQueryer) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	q.check(statement)
	return q.queryer.QueryContext(ctx, statement, args...)
}

func (q sliceSampleNarrowQueryer) QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row {
	q.check(statement)
	return q.queryer.QueryRowContext(ctx, statement, args...)
}

func sliceSampleRequest(t *testing.T, f sliceSampleFixture) PrepareWorkerSliceConformanceRequest {
	t.Helper()
	return PrepareWorkerSliceConformanceRequest{
		WorkID: f.workID, AttemptID: f.attemptID,
		ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: sliceSampleNoGit{t},
	}
}

func requireSliceSampleRefusal(t *testing.T, err error) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("want a typed refusal, got %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("a blocked connection is not an authority refusal: %v", err)
	}
}

func TestSliceConformancePreparationSample(t *testing.T) {
	for _, lane := range []string{"implement", "design"} {
		t.Run("completed editing lane "+lane, func(t *testing.T) {
			f := seedSliceSampleFixture(t, lane)
			request := sliceSampleRequest(t, f)
			before := readJobBindingState(t, f.store, f.workID, f.job.JobID)
			sample, err := sampleSliceConformance(context.Background(), sliceSampleNarrowQueryer{queryer: f.store.DatabaseForTesting(), t: t}, request)
			if err != nil {
				t.Fatalf("completed %s authority must sample without reachable paths: %v", lane, err)
			}
			if after := readJobBindingState(t, f.store, f.workID, f.job.JobID); after != before {
				t.Fatalf("sampling wrote state: before %+v, after %+v", before, after)
			}
			ctx, tx := sliceSampleTx(t, f.store)
			inside, err := sampleSliceConformance(ctx, tx, request)
			if err != nil || !reflect.DeepEqual(sample, inside) {
				t.Fatalf("supplied tx must reproduce the SQL sample: equal=%t, err=%v", reflect.DeepEqual(sample, inside), err)
			}
			preparation := &WorkerSliceConformancePreparation{sample: sample}
			if err := preparation.revalidateTx(ctx, tx); err != nil {
				t.Fatalf("unchanged SQL authority must revalidate without a native proof or pool read: %v", err)
			}
		})
	}

	t.Run("request must select existing exact authority", func(t *testing.T) {
		f := seedSliceSampleFixture(t, "implement")
		for _, test := range []struct {
			name string
			edit func(*PrepareWorkerSliceConformanceRequest)
		}{
			{"missing work", func(r *PrepareWorkerSliceConformanceRequest) { r.WorkID = "" }},
			{"unknown work", func(r *PrepareWorkerSliceConformanceRequest) { r.WorkID = "work:unknown" }},
			{"missing attempt", func(r *PrepareWorkerSliceConformanceRequest) { r.AttemptID = "" }},
			{"unknown attempt", func(r *PrepareWorkerSliceConformanceRequest) { r.AttemptID = "attempt:unknown" }},
			{"stale version", func(r *PrepareWorkerSliceConformanceRequest) { r.ExpectedVersion-- }},
			{"future version", func(r *PrepareWorkerSliceConformanceRequest) { r.ExpectedVersion++ }},
		} {
			t.Run(test.name, func(t *testing.T) {
				request := sliceSampleRequest(t, f)
				test.edit(&request)
				before := readJobBindingState(t, f.store, f.workID, f.job.JobID)
				_, err := sampleSliceConformance(context.Background(), f.store.DatabaseForTesting(), request)
				requireSliceSampleRefusal(t, err)
				if after := readJobBindingState(t, f.store, f.workID, f.job.JobID); after != before {
					t.Fatalf("refusal wrote state: before %+v, after %+v", before, after)
				}
			})
		}
	})

	for _, test := range sliceSampleInvalidAuthorities() {
		t.Run(test.name, func(t *testing.T) {
			f := seedSliceSampleFixture(t, "implement")
			sliceSampleWrite(t, f.store, func(tx *sql.Tx) { test.edit(t, tx, f) })
			request := sliceSampleRequest(t, f)
			before := readJobBindingState(t, f.store, f.workID, f.job.JobID)
			_, err := sampleSliceConformance(context.Background(), f.store.DatabaseForTesting(), request)
			requireSliceSampleRefusal(t, err)
			if after := readJobBindingState(t, f.store, f.workID, f.job.JobID); after != before {
				t.Fatalf("refusal wrote state: before %+v, after %+v", before, after)
			}
		})
	}

	t.Run("select dispatched immutable revision rather than latest job revision", func(t *testing.T) {
		f := seedSliceSampleFixture(t, "implement")
		request := sliceSampleRequest(t, f)
		before, err := sampleSliceConformance(context.Background(), f.store.DatabaseForTesting(), request)
		if err != nil {
			t.Fatal(err)
		}
		sliceSampleWrite(t, f.store, func(tx *sql.Tx) {
			next := f.job
			next.Revision++
			next.Objective = "a newer revision that this attempt never dispatched"
			next.ArchitectureBinding = &architectureJobBinding{DesignRevision: f.job.ArchitectureBinding.DesignRevision, DesignDigest: f.job.ArchitectureBinding.DesignDigest, SliceID: "binding-integration"}
			next.PathScope = []string{}
			next.Digest = DeriveWorkerJobDigest(next)
			sliceSampleInsertJob(t, tx, f.workID, next, "sample-job-newer")
		})
		after, err := sampleSliceConformance(context.Background(), f.store.DatabaseForTesting(), request)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("an unrelated revision must not replace or change the dispatched sample: equal=%t err=%v", reflect.DeepEqual(before, after), err)
		}
	})
}

type sliceSampleMutation struct {
	name string
	edit func(*testing.T, *sql.Tx, sliceSampleFixture)
}

func sliceSampleSQLMutation(name, statement string) sliceSampleMutation {
	return sliceSampleMutation{name, func(t *testing.T, tx *sql.Tx, _ sliceSampleFixture) { sliceSampleExec(t, tx, statement) }}
}

func sliceSampleWindowMutation(name, key string, value any) sliceSampleMutation {
	return sliceSampleMutation{name, func(t *testing.T, tx *sql.Tx, f sliceSampleFixture) {
		sliceSampleInsertWindow(t, tx, f, "sample-replacement-window", map[string]any{key: value})
	}}
}

// sliceSampleDesignRow is one workflow_design_records row as the synthetic
// mutations read and rewrite it.
type sliceSampleDesignRow struct {
	workVersion                      int64
	approach, decisions, touchedRefs string
	architecture, digest, recordedAt string
	revision                         int64
}

// sliceSampleRewriteDesign replaces the work's design row with the caller's
// mutated copy. The projection is fold-only and its update trigger refuses
// every UPDATE, so the synthetic route deletes and re-inserts the row under
// the same fold guard the seeded write used. The architecture, revision, and
// digest triple stays complete on every rewrite.
func sliceSampleRewriteDesign(t *testing.T, tx *sql.Tx, mutate func(row sliceSampleDesignRow) sliceSampleDesignRow) {
	t.Helper()
	var row sliceSampleDesignRow
	if err := tx.QueryRowContext(context.Background(), `SELECT work_version,approach,decisions,touched_refs,COALESCE(architecture,''),COALESCE(revision,0),COALESCE(digest,''),recorded_at FROM workflow_design_records WHERE work_id='slice-sample'`).Scan(&row.workVersion, &row.approach, &row.decisions, &row.touchedRefs, &row.architecture, &row.revision, &row.digest, &row.recordedAt); err != nil {
		t.Fatal(err)
	}
	sliceSampleExec(t, tx, `DELETE FROM workflow_design_records WHERE work_id='slice-sample'`)
	row = mutate(row)
	sliceSampleExec(t, tx, `INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,architecture,revision,digest,recorded_at) VALUES('slice-sample',?,?,?,?,?,?,?,?)`, row.workVersion, row.approach, row.decisions, row.touchedRefs, row.architecture, row.revision, row.digest, row.recordedAt)
}

// sliceSampleRewriteDesignJSON applies one json_set expression to the design
// row's architecture member before the rewrite.
func sliceSampleRewriteDesignJSON(t *testing.T, tx *sql.Tx, path, value string) {
	t.Helper()
	sliceSampleRewriteDesign(t, tx, func(row sliceSampleDesignRow) sliceSampleDesignRow {
		if err := tx.QueryRowContext(context.Background(), `SELECT json_set(?, ?, ?)`, row.architecture, path, value).Scan(&row.architecture); err != nil {
			t.Fatal(err)
		}
		return row
	})
}

func sliceSampleInvalidAuthorities() []sliceSampleMutation {
	return []sliceSampleMutation{
		sliceSampleSQLMutation("attempt belongs to another work", `UPDATE worker_attempts SET work_id='work:other'`),
		sliceSampleSQLMutation("attempt is not completed", `UPDATE worker_attempts SET lifecycle_state='dispatched',completed_at=NULL`),
		sliceSampleSQLMutation("failed terminal is not completion", `UPDATE worker_attempts SET lifecycle_state='failed',completed_at=NULL,failed_at='fixture',failure_kind='worker_error'`),
		sliceSampleSQLMutation("unknown lane identity", `UPDATE worker_attempts SET lane_digest='sha256:`+strings.Repeat("0", 64)+`'`),
		{"nonediting registered lane", func(t *testing.T, tx *sql.Tx, _ sliceSampleFixture) {
			lane := sliceSampleLane(t, "review")
			sliceSampleExec(t, tx, `UPDATE worker_attempts SET lane_id=?,lane_version=?,lane_digest=?,capability_class=?`, lane.ID, lane.Version, lane.Digest, lane.CapabilityClass)
		}},
		sliceSampleSQLMutation("workflow instance missing", `DELETE FROM workflow_instances`),
		sliceSampleSQLMutation("no active contract", `UPDATE workflow_contracts SET superseded_by=contract_version`),
		sliceSampleWindowMutation("missing base never falls back to claim BaseSHA", "worker_subject_commit", ""),
		sliceSampleWindowMutation("malformed base refuses", "worker_subject_commit", "HEAD"),
		sliceSampleWindowMutation("missing worktree identity", "worker_worktree_identity", ""),
		sliceSampleWindowMutation("malformed worktree identity", "worker_worktree_identity", "claim-path"),
		sliceSampleWindowMutation("missing packet digest", "worker_packet_digest", ""),
		sliceSampleWindowMutation("malformed packet digest", "worker_packet_digest", "digest"),
		sliceSampleWindowMutation("authorization belongs to another step", "step_id", "another-step"),
		sliceSampleWindowMutation("authorization has no bound job", "worker_job", nil),
		sliceSampleWindowMutation("authorized job differs from dispatched revision", "worker_job", WorkerJobBinding{JobID: "job:slice-sample", Revision: 2, Digest: "sha256:" + strings.Repeat("e", 64)}),
		sliceSampleSQLMutation("exact job revision is absent", `DELETE FROM worker_job_revisions`),
		sliceSampleSQLMutation("job digest differs from both dispatch bindings", `UPDATE worker_job_revisions SET digest='sha256:`+strings.Repeat("e", 64)+`'`),
		sliceSampleSQLMutation("job contract differs from active contract", `UPDATE worker_job_revisions SET contract_version=2`),
		{"stale design revision", func(t *testing.T, tx *sql.Tx, _ sliceSampleFixture) {
			sliceSampleRewriteDesign(t, tx, func(row sliceSampleDesignRow) sliceSampleDesignRow { row.revision++; return row })
		}},
		{"stale design digest", func(t *testing.T, tx *sql.Tx, _ sliceSampleFixture) {
			sliceSampleRewriteDesign(t, tx, func(row sliceSampleDesignRow) sliceSampleDesignRow {
				row.digest = "sha256:" + strings.Repeat("e", 64)
				return row
			})
		}},
		sliceSampleSQLMutation("missing current design", `DELETE FROM workflow_design_records`),
		{"integration slice grants no editing authority", func(t *testing.T, tx *sql.Tx, _ sliceSampleFixture) {
			sliceSampleRewriteDesignJSON(t, tx, "$.slices[0].kind", "integration")
		}},
		sliceSampleSQLMutation("job Project differs from slice owner", `UPDATE worker_job_revisions SET project_scope='project:other'`),
		sliceSampleSQLMutation("no canonical repository locator", `UPDATE project_locators SET kind='git_remote' WHERE project_id='project' AND kind='canonical_path'`),
		{"ambiguous canonical repository locators", func(t *testing.T, tx *sql.Tx, f sliceSampleFixture) {
			sliceSampleExec(t, tx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator:other','project','canonical_path',?,?,'fixture','fixture')`, f.repoPath+"-other", f.repoPath+"-other")
		}},
	}
}

func TestSliceConformancePreparationSampleRevalidation(t *testing.T) {
	drifts := []sliceSampleMutation{
		sliceSampleSQLMutation("work version", `UPDATE work_items SET version=version+1 WHERE id='slice-sample'`),
		sliceSampleSQLMutation("step without work version", `UPDATE workflow_instances SET current_step='another-step'`),
		sliceSampleSQLMutation("active contract without work version", `UPDATE workflow_contracts SET superseded_by=contract_version`),
		sliceSampleSQLMutation("claim generation", `UPDATE worktree_entries SET claim_op_id='claim:replacement'`),
		sliceSampleSQLMutation("claim branch", `UPDATE worktree_entries SET branch='change/replacement'`),
		sliceSampleSQLMutation("raw claim path", `UPDATE worktree_entries SET path=path||'/replacement'`),
		sliceSampleSQLMutation("claim repository identity", `UPDATE worktree_entries SET repository_id='repo:replacement'`),
		sliceSampleSQLMutation("claim state", `UPDATE worktree_entries SET state='reclaimed',reclaimed_at='fixture'`),
		{"design content under same digest", func(t *testing.T, tx *sql.Tx, _ sliceSampleFixture) {
			sliceSampleRewriteDesign(t, tx, func(row sliceSampleDesignRow) sliceSampleDesignRow { row.approach += " changed"; return row })
		}},
		{"design classification under same digest", func(t *testing.T, tx *sql.Tx, _ sliceSampleFixture) {
			sliceSampleRewriteDesignJSON(t, tx, "$.files[0].role", "test")
		}},
		{"design revision", func(t *testing.T, tx *sql.Tx, _ sliceSampleFixture) {
			sliceSampleRewriteDesign(t, tx, func(row sliceSampleDesignRow) sliceSampleDesignRow { row.revision++; return row })
		}},
		sliceSampleSQLMutation("job content under same digest", `UPDATE worker_job_revisions SET stopping_condition=stopping_condition||' changed'`),
		sliceSampleSQLMutation("attempt source identity", `UPDATE worker_attempts SET completed_at='2026-10-01T00:00:02Z'`),
		sliceSampleWindowMutation("authorization source identity", "worker_subject_commit", strings.Repeat("a", 40)),
		sliceSampleWindowMutation("authorization base", "worker_subject_commit", strings.Repeat("c", 40)),
		sliceSampleWindowMutation("authorization packet", "worker_packet_digest", "sha256:"+strings.Repeat("c", 64)),
		sliceSampleSQLMutation("locator path", `UPDATE project_locators SET locator_value=locator_value||'/replacement',normalized_value=normalized_value||'/replacement' WHERE project_id='project' AND kind='canonical_path'`),
		{"locator identity at same path", func(t *testing.T, tx *sql.Tx, f sliceSampleFixture) {
			sliceSampleExec(t, tx, `UPDATE project_locators SET kind='git_remote' WHERE locator_id=?`, f.locatorID)
			sliceSampleExec(t, tx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator:replacement','project','canonical_path',?,?,'fixture','fixture')`, f.repoPath, f.repoPath)
		}},
	}
	for _, test := range drifts {
		t.Run(test.name, func(t *testing.T) {
			f := seedSliceSampleFixture(t, "implement")
			request := sliceSampleRequest(t, f)
			sample, err := sampleSliceConformance(context.Background(), f.store.DatabaseForTesting(), request)
			if err != nil {
				t.Fatal(err)
			}
			preparation := &WorkerSliceConformancePreparation{sample: sample}
			ctx, tx := sliceSampleTx(t, f.store)
			if err := enterFold(ctx, tx); err != nil {
				t.Fatal(err)
			}
			test.edit(t, tx, f)
			if err := leaveFold(ctx, tx); err != nil {
				t.Fatal(err)
			}
			var version int64
			if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, f.workID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if test.name != "work version" && version != request.ExpectedVersion {
				t.Fatal("source-identity fixture also changed the work version")
			}
			requireSliceSampleRefusal(t, preparation.revalidateTx(ctx, tx))
		})
	}
}

// sliceGitSeamRunner is a trusted test seam whose successful outputs are
// scripted per call, so the shared-allowance accounting is observable.
type sliceGitSeamRunner struct {
	outputs [][]byte
	err     error
	calls   int
}

func (r *sliceGitSeamRunner) Run(context.Context, string, ...string) ([]byte, error) {
	if r.calls >= len(r.outputs) {
		return nil, fmt.Errorf("unexpected seam call %d", r.calls)
	}
	out := r.outputs[r.calls]
	r.calls++
	// A scripted error belongs to the call whose output slot is nil, so later
	// scripted outputs still exercise the allowance.
	if r.err != nil && out == nil {
		return nil, r.err
	}
	return out, nil
}

// sliceGitFakePath installs a fake git executable on a test-only PATH, so the
// confined native path runs real subprocesses without trusting host git.
func sliceGitFakePath(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// sliceGitOldShim installs a git shim that drops the named variables from the
// child environment before execing the host git, modeling an older supported
// Git that ignores a variable the sanitized environment pins.
func sliceGitOldShim(t *testing.T, dropped ...string) {
	t.Helper()
	host, lookErr := exec.LookPath("git")
	if lookErr != nil {
		t.Fatal(lookErr)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n"
	for _, name := range dropped {
		script += "unset " + name + "\n"
	}
	script += "exec '" + host + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestSliceConformancePreparationGit pins the sanitized bounded Git runner of
// one slice conformance probe: one aggregate stdout+stderr allowance across
// its sequential calls, typed refusals that never truncate-pass, a confined
// environment, and no index, ref, or fsmonitor side effects.
func TestSliceConformancePreparationGit(t *testing.T) {
	t.Run("seam shares one allowance across sequential calls", func(t *testing.T) {
		seam := &sliceGitSeamRunner{outputs: [][]byte{
			bytes.Repeat([]byte("a"), maxGitOutput/2),
			bytes.Repeat([]byte("b"), maxGitOutput/2),
		}}
		git := &sliceConformanceGit{seam: seam}
		for i := 0; i < 2; i++ {
			out, err := git.run(context.Background(), "/repo", "status")
			if err != nil || len(out) != maxGitOutput/2 {
				t.Fatalf("call %d returned %d bytes, %v; want %d bytes and no error", i+1, len(out), err, maxGitOutput/2)
			}
		}
	})

	t.Run("seam refuses output beyond the aggregate cap", func(t *testing.T) {
		seam := &sliceGitSeamRunner{outputs: [][]byte{
			bytes.Repeat([]byte("a"), maxGitOutput/2),
			bytes.Repeat([]byte("b"), maxGitOutput/2+1),
			[]byte("tail"),
		}}
		git := &sliceConformanceGit{seam: seam}
		if out, err := git.run(context.Background(), "/repo", "status"); err != nil || len(out) != maxGitOutput/2 {
			t.Fatalf("first call returned %d bytes, %v; want success", len(out), err)
		}
		out, err := git.run(context.Background(), "/repo", "status")
		assertFailureKind(t, err, KindUnavailable)
		if len(out) != 0 {
			t.Fatalf("cap refusal returned %d output bytes, want none", len(out))
		}
		// The first refusal poisons the probe: later calls refuse even when
		// their own output would fit the remainder.
		if _, err := git.run(context.Background(), "/repo", "status"); !hasFailureKind(err, KindUnavailable) {
			t.Fatalf("post-poison error = %v, want %s", err, KindUnavailable)
		}
	})

	t.Run("seam errors pass through without spending the allowance", func(t *testing.T) {
		sentinel := errors.New("seam outage")
		seam := &sliceGitSeamRunner{err: sentinel, outputs: [][]byte{nil, bytes.Repeat([]byte("c"), maxGitOutput)}}
		git := &sliceConformanceGit{seam: seam}
		if _, err := git.run(context.Background(), "/repo", "status"); !errors.Is(err, sentinel) {
			t.Fatalf("seam error = %v, want the trusted passthrough sentinel", err)
		}
		if out, err := git.run(context.Background(), "/repo", "status"); err != nil || len(out) != maxGitOutput {
			t.Fatalf("post-error call returned %d bytes, %v; want the full allowance available", len(out), err)
		}
	})

	t.Run("native commands carry the sanitized argv and environment", func(t *testing.T) {
		t.Setenv("GIT_DIR", "/hostile/objects")
		t.Setenv("GIT_INDEX_FILE", "/hostile/index")
		t.Setenv("GIT_OBJECT_DIRECTORY", "/hostile/objects")
		t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", "/hostile/alternates")
		t.Setenv("GIT_CONFIG", "/hostile/config")
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
		t.Setenv("GIT_CONFIG_VALUE_0", "/hostile/fsmonitor")
		t.Setenv("HOME", "/hostile/home")
		sliceGitFakePath(t, `printf '%s\n' "$@"; echo ---env---; env`)
		repo := t.TempDir()
		git := &sliceConformanceGit{}
		out, err := git.run(context.Background(), repo, "status", "--porcelain")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"-C", repo, "--no-replace-objects", "-c", "core.fsmonitor=false", "-c", "core.attributesFile=/dev/null", "status", "--porcelain", "---env---"}
		lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(lines) < len(want) || !reflect.DeepEqual(lines[:len(want)], want) {
			t.Fatalf("argv = %q, want sanitized prefix %q", lines, want)
		}
		allowed := map[string]bool{
			"GIT_CONFIG_NOSYSTEM=1": true, "GIT_CONFIG_GLOBAL=/dev/null": true, "GIT_ATTR_NOSYSTEM=1": true,
			"GIT_NO_LAZY_FETCH=1": true, "GIT_ALLOW_PROTOCOL=": true, "GIT_NO_REPLACE_OBJECTS=1": true, "GIT_OPTIONAL_LOCKS=0": true,
		}
		hasLocale := false
		for _, line := range lines[len(want):] {
			if line == "LC_ALL=C" {
				hasLocale = true
			}
			if strings.HasPrefix(line, "GIT_") && !allowed[line] {
				t.Fatalf("confined environment carries unpinned %q", line)
			}
			if strings.HasPrefix(line, "HOME=") || strings.Contains(line, "/hostile") {
				t.Fatalf("confined environment carries inherited %q", line)
			}
		}
		if !hasLocale {
			t.Fatal("confined environment carries no LC_ALL=C")
		}
	})

	t.Run("empty protocol allowlist refuses promisor fetch without retrieval", func(t *testing.T) {
		remote := filepath.Join(t.TempDir(), "remote")
		gitRunStore(t, filepath.Dir(remote), "init", "-q", "-b", "main", remote)
		gitRunStore(t, remote, "config", "user.email", "fixture@invalid")
		gitRunStore(t, remote, "config", "user.name", "fixture")
		gitRunStore(t, remote, "config", "uploadpack.allowFilter", "true")
		gitRunStore(t, remote, "config", "uploadpack.allowAnySHA1InWant", "true")
		sliceNativeWrite(t, remote, "promised.txt", "promised\n")
		gitRunStore(t, remote, "add", "-A")
		gitRunStore(t, remote, "commit", "-q", "-m", "promised")
		promised := sliceNativeHead(t, remote, "HEAD:promised.txt")
		clone := filepath.Join(t.TempDir(), "clone")
		gitRunStore(t, filepath.Dir(clone), "clone", "-q", "--filter=blob:none", "--no-checkout", "--no-local", remote, clone)
		// The clone itself admits the local file protocol, so the runner's
		// empty allowlist is the only control left that can deny the fetch.
		gitRunStore(t, clone, "config", "protocol.file.allow", "always")
		// cat-file -e reports missing promisor objects optimistically, so
		// absence is read from the local object inventory instead.
		listed := func() bool {
			out, err := (ExecGitRunner{}).Run(context.Background(), clone, "cat-file", "--batch-all-objects", "--batch-check")
			if err != nil {
				t.Fatal(err)
			}
			return strings.Contains(string(out), promised)
		}
		if listed() {
			t.Fatal("fixture must start without the promised blob; the partial clone did not filter")
		}
		// Old supported Git ignores GIT_NO_LAZY_FETCH; the shim models that.
		sliceGitOldShim(t, "GIT_NO_LAZY_FETCH")
		git := &sliceConformanceGit{}
		out, err := git.run(context.Background(), clone, "cat-file", "blob", promised)
		assertFailureKind(t, err, KindGitUnreachable)
		if len(out) != 0 {
			t.Fatalf("refused read returned %d bytes, want none", len(out))
		}
		if listed() {
			t.Fatal("the refused native read retrieved the promised object; the transport denial did not hold")
		}
	})

	t.Run("native combined stdout and stderr refuse at the shared cap", func(t *testing.T) {
		sliceGitFakePath(t, `head -c 3145728 /dev/zero; head -c 2097152 /dev/zero >&2`)
		git := &sliceConformanceGit{}
		out, err := git.run(context.Background(), t.TempDir(), "status")
		if !hasFailureKind(err, KindUnavailable) {
			t.Fatalf("cap refusal error = %v, want %s", err, KindUnavailable)
		}
		if len(out) != 0 {
			t.Fatalf("cap refusal returned %d output bytes, want none", len(out))
		}
	})

	t.Run("native combined output within the cap passes", func(t *testing.T) {
		sliceGitFakePath(t, `head -c 4096 /dev/zero; head -c 2048 /dev/zero >&2`)
		git := &sliceConformanceGit{}
		out, err := git.run(context.Background(), t.TempDir(), "status")
		if err != nil || len(out) != 4096 {
			t.Fatalf("small output returned %d bytes, %v; want 4096 bytes and no error", len(out), err)
		}
	})

	t.Run("native nonzero exit diagnoses from bounded stderr", func(t *testing.T) {
		sliceGitFakePath(t, `echo boom-diagnosis >&2; exit 3`)
		git := &sliceConformanceGit{}
		out, err := git.run(context.Background(), t.TempDir(), "status")
		assertFailureKind(t, err, KindGitUnreachable)
		if !strings.Contains(err.Error(), "boom-diagnosis") {
			t.Fatalf("native exit error = %v, want the bounded stderr diagnosis", err)
		}
		if len(out) != 0 {
			t.Fatalf("failed run returned %d output bytes, want none", len(out))
		}
	})

	t.Run("caller deadline and cancel classify as timeout without extension", func(t *testing.T) {
		sliceGitFakePath(t, `sleep 30`)
		git := &sliceConformanceGit{}
		deadlineCtx, stopDeadline := context.WithDeadline(context.Background(), time.Now().Add(250*time.Millisecond))
		defer stopDeadline()
		start := time.Now()
		if _, err := git.run(deadlineCtx, t.TempDir(), "status"); !hasFailureKind(err, KindTimeout) {
			t.Fatalf("deadline error = %v, want %s", err, KindTimeout)
		}
		if elapsed := time.Since(start); elapsed > 4*time.Second {
			t.Fatalf("deadline run took %s; the caller's 250ms deadline was extended", elapsed)
		}
		if deadlineCtx.Err() != context.DeadlineExceeded {
			t.Fatalf("caller ctx error = %v, want DeadlineExceeded with the deadline intact", deadlineCtx.Err())
		}
		cancelCtx, cancelRun := context.WithCancel(context.Background())
		timer := time.AfterFunc(150*time.Millisecond, cancelRun)
		defer timer.Stop()
		if _, err := git.run(cancelCtx, t.TempDir(), "status"); !hasFailureKind(err, KindTimeout) {
			t.Fatalf("cancel error = %v, want %s", err, KindTimeout)
		}
		if cancelCtx.Err() != context.Canceled {
			t.Fatalf("caller ctx error = %v, want Canceled with the caller's cancel intact", cancelCtx.Err())
		}
	})

	t.Run("native probe leaves the repository index and refs unchanged", func(t *testing.T) {
		repo := t.TempDir()
		gitRunStore(t, repo, "init", "-b", "main")
		gitRunStore(t, repo, "config", "user.email", "t@e.invalid")
		gitRunStore(t, repo, "config", "user.name", "t")
		if err := os.WriteFile(filepath.Join(repo, "slice.txt"), []byte("slice\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRunStore(t, repo, "add", "-A")
		gitRunStore(t, repo, "commit", "-m", "slice")
		// A configured fsmonitor hook must never run: the runner pins
		// core.fsmonitor=false above any repository configuration.
		marker := filepath.Join(t.TempDir(), "fsmonitor-marker")
		hook := filepath.Join(t.TempDir(), "fsmonitor-hook")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		gitRunStore(t, repo, "config", "core.fsmonitor", hook)
		// A stat-only change is what an unconfined status would flush into
		// the index; GIT_OPTIONAL_LOCKS=0 must keep the index bytes intact.
		future := time.Now().Add(time.Second)
		if err := os.Chtimes(filepath.Join(repo, "slice.txt"), future, future); err != nil {
			t.Fatal(err)
		}
		// The state-recording reads carry the same fsmonitor pin as the
		// runner: an unconfined ls-files spawns the configured hook itself
		// and would pollute the marker assertion with fixture noise.
		read := func(args ...string) string {
			t.Helper()
			out, err := ExecGitRunner{}.Run(context.Background(), repo, append([]string{"-c", "core.fsmonitor=false"}, args...)...)
			if err != nil {
				t.Fatalf("git %v: %v", args, err)
			}
			return string(out)
		}
		indexBefore, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		head, files, refs := read("rev-parse", "HEAD"), read("ls-files", "--stage"), read("for-each-ref")
		git := &sliceConformanceGit{}
		if _, err := git.run(context.Background(), repo, "status", "--porcelain=v1", "-z", "--untracked-files=all"); err != nil {
			t.Fatal(err)
		}
		if _, err := git.run(context.Background(), repo, "diff-index", "--numstat", "HEAD"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("the pinned core.fsmonitor=false did not stop the configured fsmonitor hook")
		}
		indexAfter, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(indexBefore, indexAfter) {
			t.Fatal("the probe wrote to the index; GIT_OPTIONAL_LOCKS=0 was lost")
		}
		if read("rev-parse", "HEAD") != head || read("ls-files", "--stage") != files || read("for-each-ref") != refs {
			t.Fatal("the probe changed HEAD, staged entries, or refs")
		}
	})
}

// sliceNativeFiles is the native fixture's edit-slice set inside the
// synthetic repository; "unïcode.go" pins exact non-ASCII byte-path fidelity.
var sliceNativeFiles = []string{"slice.go", "slice_test.go", "slice_gen.go", "unïcode.go"}

// sliceNativePlan reshapes the seeded design to the synthetic repository's
// exact approved file map, unchanged set, edit-slice files, and line limit.
func sliceNativePlan(plan *WorkflowDesignArchitecture, limit int) {
	plan.Files = []WorkflowDesignArchitectureFile{
		{ProjectID: "project", Path: "slice.go", Role: DesignArchitectureFileRoleSource},
		{ProjectID: "project", Path: "slice_test.go", Role: DesignArchitectureFileRoleTest},
		{ProjectID: "project", Path: "slice_gen.go", Role: DesignArchitectureFileRoleGenerated, GeneratedBy: &WorkContextReadingSource{
			Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "checks/fixture.json", CommitOID: strings.Repeat("a", 40),
		}},
		{ProjectID: "project", Path: "unïcode.go", Role: DesignArchitectureFileRoleSource},
	}
	plan.UnchangedFiles = []WorkflowDesignArchitectureUnchangedFile{{ProjectID: "project", Path: "unchanged.go"}}
	plan.Slices[0].Files = sliceNativeFiles
	plan.Slices[0].MaxChangedLines = limit
}

func sliceNativeWrite(t *testing.T, dir, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sliceNativeHead(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := ExecGitRunner{}.Run(context.Background(), dir, "rev-parse", rev)
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", rev, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// sliceNativeOracle mirrors the sample fixture's acceptance oracle so the
// bound slice's declared control resolves.
func sliceNativeOracle() *AcceptanceOracle {
	return &AcceptanceOracle{
		Owners: []OracleOwner{{
			OwnerID: "owner:fixture", DomainID: "domain:fixture",
			Mechanism:  OracleMechanism{ProjectID: "project", Path: "slice.go", EntryPoint: "PrepareWorkerSliceConformance"},
			Obligation: "measure the exact committed slice", PredicateIDs: []string{"predicate:return-route"},
		}},
		Cases: []OracleCase{{
			CaseID: "case:fixture", OwnerID: "owner:fixture", EntryPoint: "PrepareWorkerSliceConformance",
			InputClass: "completed editing attempt", ExpectedState: "exact authority retained", ControlIDs: []string{"control:fixture"},
		}},
		Controls: []OracleControl{{
			ControlID: "control:fixture", OwnerID: "owner:fixture", PredicateIDs: []string{"predicate:return-route"}, CaseIDs: []string{"case:fixture"},
			RecipeSource: WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "checks/fixture.json", CommitOID: strings.Repeat("a", 40)},
			Argv:         []string{"go", "test", "-run", "^TestFixture$", "./internal/store"}, Cwd: ".",
			ExpectedResult: OracleExpectedResultPass, RequiredEvidenceRole: OracleEvidenceRoleReported,
			ReadinessEvidenceRefs: []string{"native-run:fixture"},
		}},
	}
}

type sliceNativeFixture struct {
	sliceSampleFixture
	base, end, tree string
}

// seedSliceNativeFixture seeds the completed-authority SQL history against a
// real synthetic repository and one linked claimed worktree, commits the
// authorized base there and one slice end commit on top of it. The claim's
// recorded BaseSHA stays synthetic: the native base comes from the
// authorization alone, never from the claim or any terminal observation.
func seedSliceNativeFixture(t *testing.T, mutatePlan func(*WorkflowDesignArchitecture), edit func(*testing.T, string)) sliceNativeFixture {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	claim := filepath.Join(t.TempDir(), "claim")
	gitRunStore(t, filepath.Dir(repo), "init", "-q", "-b", "main", repo)
	gitRunStore(t, repo, "config", "user.email", "fixture@invalid")
	gitRunStore(t, repo, "config", "user.name", "fixture")
	for path, content := range map[string]string{
		"slice.go": "one\ntwo\nthree\n", "slice_test.go": "package t\n", "slice_gen.go": "generated\n",
		"unïcode.go": "unicode\n", "unchanged.go": "do-not-touch\n",
	} {
		sliceNativeWrite(t, repo, path, content)
	}
	gitRunStore(t, repo, "add", "-A")
	gitRunStore(t, repo, "commit", "-q", "-m", "base")
	base := sliceNativeHead(t, repo, "HEAD")
	gitRunStore(t, repo, "worktree", "add", "-q", "-b", "change/sample", claim, "HEAD")
	if edit == nil {
		edit = func(t *testing.T, dir string) {
			sliceNativeWrite(t, dir, "slice.go", "one\ntwo\nthree\nfour\nfive\n")
			sliceNativeWrite(t, dir, "unïcode.go", "unicode\nplus\n")
			sliceNativeWrite(t, dir, "slice_test.go", "package t changed\n")
			sliceNativeWrite(t, dir, "slice_gen.go", "generated changed\n")
		}
	}
	edit(t, claim)
	gitRunStore(t, claim, "add", "-A")
	gitRunStore(t, claim, "commit", "-q", "-m", "end")
	var sliceFiles []string
	plan := func(design *WorkflowDesignArchitecture) {
		sliceNativePlan(design, designArchitectureMaxChangedLines)
		if mutatePlan != nil {
			mutatePlan(design)
		}
		sliceFiles = append([]string{}, design.Slices[0].Files...)
	}
	binding := seedJobBindingFixture(t, "slice-native", jobBindingPinRef, true, true, plan)
	t.Cleanup(func() { _ = binding.store.Close() })
	f := sliceNativeFixture{base: base}
	f.sliceSampleFixture = sliceSampleFixture{
		store: binding.store, workID: "slice-native", attemptID: "attempt:slice-native",
		claimPath: claim, repoPath: repo, lane: sliceSampleLane(t, "implement"),
		job: jobBindingEnvelopePayload(binding, "job:slice-native", true),
	}
	f.job.PathScope = sliceFiles
	f.job.AcceptanceOracle = sliceNativeOracle()
	f.job.Digest = DeriveWorkerJobDigest(f.job)
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, f.workID).Scan(&f.stepID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT locator_id FROM project_locators WHERE project_id='project' AND kind='canonical_path'`).Scan(&f.locatorID); err != nil {
		t.Fatal(err)
	}
	sliceSampleWrite(t, f.store, func(tx *sql.Tx) {
		sliceSampleInsertJob(t, tx, f.workID, f.job, "native-job")
		sliceSampleExec(t, tx, `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,'completed','2026-10-01T00:00:00Z','2026-10-01T00:00:01Z')`,
			f.workID, f.attemptID, f.lane.ID, f.lane.Version, f.lane.Digest, f.lane.CapabilityClass, preferredModelForLane(f.lane), WorkerPacketSchemaVersion, WorkerReportSchemaVersion)
		sliceSampleExec(t, tx, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?, 'project','claim:native','change/sample',?,?,'repo:native','active','2026-10-01T00:00:00Z','{}')`, WorktreeSetID(f.workID), strings.Repeat("b", 40), f.claimPath)
		sliceSampleExec(t, tx, `UPDATE project_locators SET locator_value=?,normalized_value=? WHERE locator_id=?`, f.repoPath, f.repoPath, f.locatorID)
		sliceSampleInsertWindow(t, tx, f.sliceSampleFixture, "sample-window", map[string]any{"worker_subject_commit": base})
		jobBinding := WorkerJobBinding{JobID: f.job.JobID, Revision: f.job.Revision, Digest: f.job.Digest}
		event := implementDispatchEvidenceEvent(f.workID, f.attemptID, f.lane, jobBinding)
		sliceSampleInsertEvent(t, tx, f.workID, "native-dispatched", WorkerDispatched, json.RawMessage(event.Payload))
	})
	f.end = sliceNativeHead(t, claim, "HEAD")
	f.tree = sliceNativeHead(t, claim, "HEAD^{tree}")
	return f
}

func sliceNativeRequest(t *testing.T, f sliceNativeFixture) PrepareWorkerSliceConformanceRequest {
	t.Helper()
	return PrepareWorkerSliceConformanceRequest{
		WorkID: f.workID, AttemptID: f.attemptID,
		ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: nil,
	}
}

// sliceNativeSideCommit creates one detached worktree commit on top of the
// fixture's base and returns its OID, so wrong-base and merge fixtures use a
// real commit object without touching shared branches.
func sliceNativeSideCommit(t *testing.T, repo, name string) string {
	t.Helper()
	side := filepath.Join(t.TempDir(), name)
	gitRunStore(t, repo, "worktree", "add", "-q", "--detach", side, "HEAD")
	sliceNativeWrite(t, side, name+".txt", name+"\n")
	gitRunStore(t, side, "add", "-A")
	gitRunStore(t, side, "commit", "-q", "-m", name)
	return sliceNativeHead(t, side, "HEAD")
}

// sliceNativeCommitTree forges one real commit object with the caller's exact
// tree and parent list, so graft fixtures can forge raw lineage that no branch
// history produces.
func sliceNativeCommitTree(t *testing.T, dir, tree, message string, parents ...string) string {
	t.Helper()
	args := []string{"commit-tree", tree, "-m", message}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	out, err := ExecGitRunner{}.Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("commit-tree %s in %s: %v", tree, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// sliceNativeProbeSeam is a trusted seam that proves the read snapshot closed
// before the probe: every Run queries the same store under a short deadline
// before returning its scripted output.
type sliceNativeProbeSeam struct {
	store    *Store
	outputs  [][]byte
	deadline time.Duration
	calls    int
}

func (r *sliceNativeProbeSeam) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	r.calls++
	queryCtx, cancel := context.WithTimeout(context.Background(), r.deadline)
	defer cancel()
	var one int
	if err := r.store.DatabaseForTesting().QueryRowContext(queryCtx, `SELECT 1`).Scan(&one); err != nil {
		return nil, fmt.Errorf("the read snapshot still held the store pool: %w", err)
	}
	if r.calls > len(r.outputs) {
		return nil, fmt.Errorf("unexpected seam call %d", r.calls)
	}
	return r.outputs[r.calls-1], nil
}

// sliceCommitObject builds one raw commit object's bytes with the given tree
// and sole parent, so seam probes exercise the commit-header lineage read
// without real commit objects.
func sliceCommitObject(tree, parent string) []byte {
	return []byte("tree " + tree + "\nparent " + parent + "\nauthor fixture <fixture@invalid> 0 +0000\ncommitter fixture <fixture@invalid> 0 +0000\n\nsealed slice\n")
}

// sliceSeamCommonDir materializes the synthetic fixture's claim and repository
// directories and returns the common Git dir, so the probe's post-snapshot
// canonicalization resolves real paths under a seam.
func sliceSeamCommonDir(t *testing.T, f sliceSampleFixture) string {
	t.Helper()
	if err := os.MkdirAll(f.claimPath, 0o755); err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(f.repoPath, ".git")
	if err := os.MkdirAll(common, 0o755); err != nil {
		t.Fatal(err)
	}
	return common
}

// TestSliceConformancePreparationNative proves the native observation end to
// end: sealed base/end/tree facts from real repositories, every refusal path,
// byte-exact paths, the shared deadline and output allowance, and that the
// read snapshot is closed while the probe runs.
func TestSliceConformancePreparationNative(t *testing.T) {
	t.Run("clean single commit seals base end tree and classified counts", func(t *testing.T) {
		f := seedSliceNativeFixture(t, nil, nil)
		preparation, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
		if err != nil {
			t.Fatalf("clean one-commit slice must prepare: %v", err)
		}
		m := preparation.measurement
		if m.base != f.base || m.end != f.end || m.tree != f.tree || m.algorithm != "git-diff-myers-v1" {
			t.Fatalf("sealed identities = base %s end %s tree %s %s; want %s %s %s", m.base, m.end, m.tree, m.algorithm, f.base, f.end, f.tree)
		}
		if m.claim.ClaimOpID != "claim:native" || m.claim.ProjectID != "project" || m.claim.Path != f.claimPath {
			t.Fatalf("sealed claim = %s %s %s; want the selected raw claim:native entry at the stored path %s", m.claim.ClaimOpID, m.claim.ProjectID, m.claim.Path, f.claimPath)
		}
		want := []slicePathCounts{
			{Path: "slice.go", Role: DesignArchitectureFileRoleSource, Additions: 2},
			{Path: "slice_gen.go", Role: DesignArchitectureFileRoleGenerated, Additions: 1, Deletions: 1},
			{Path: "slice_test.go", Role: DesignArchitectureFileRoleTest, Additions: 1, Deletions: 1},
			{Path: "unïcode.go", Role: DesignArchitectureFileRoleSource, Additions: 1},
		}
		if !reflect.DeepEqual(m.result.Paths, want) || m.result.TotalLines != 3 {
			t.Fatalf("sealed counts = %+v total %d; want %+v total 3", m.result.Paths, m.result.TotalLines, want)
		}
		ctx, tx := sliceSampleTx(t, f.store)
		if err := preparation.revalidateTx(ctx, tx); err != nil {
			t.Fatalf("revalidation after the native probe must stay SQL-only: %v", err)
		}
	})

	t.Run("dirty tracked and untracked claimed worktrees refuse", func(t *testing.T) {
		for _, test := range []struct {
			name string
			dirt func(*testing.T, string)
		}{
			{"tracked modification", func(t *testing.T, dir string) { sliceNativeWrite(t, dir, "slice.go", "dirty\n") }},
			{"untracked file", func(t *testing.T, dir string) { sliceNativeWrite(t, dir, "stray.go", "untracked\n") }},
		} {
			t.Run(test.name, func(t *testing.T) {
				f := seedSliceNativeFixture(t, nil, nil)
				test.dirt(t, f.claimPath)
				before := readJobBindingState(t, f.store, f.workID, f.job.JobID)
				_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
				requireSliceSampleRefusal(t, err)
				if after := readJobBindingState(t, f.store, f.workID, f.job.JobID); after != before {
					t.Fatal("refusal wrote state")
				}
			})
		}
	})

	t.Run("missing and wrong authorization base refuse the lineage", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			subject func(*testing.T, sliceNativeFixture) string
		}{
			{"missing base object", func(t *testing.T, f sliceNativeFixture) string { return strings.Repeat("d", 40) }},
			{"real wrong base commit", func(t *testing.T, f sliceNativeFixture) string {
				return sliceNativeSideCommit(t, f.repoPath, "wrong-base")
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				f := seedSliceNativeFixture(t, nil, nil)
				sliceSampleWrite(t, f.store, func(tx *sql.Tx) {
					sliceSampleInsertWindow(t, tx, f.sliceSampleFixture, "replacement-window", map[string]any{"worker_subject_commit": test.subject(t, f)})
				})
				_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
				requireSliceSampleRefusal(t, err)
			})
		}
	})

	t.Run("merge head and extra commits refuse", func(t *testing.T) {
		t.Run("merge head with two parents", func(t *testing.T) {
			f := seedSliceNativeFixture(t, nil, nil)
			side := sliceNativeSideCommit(t, f.repoPath, "merge-side")
			gitRunStore(t, f.claimPath, "merge", "--no-ff", "-q", "-m", "merge", side)
			_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
			requireSliceSampleRefusal(t, err)
		})
		t.Run("two commits after the base", func(t *testing.T) {
			f := seedSliceNativeFixture(t, nil, nil)
			sliceNativeWrite(t, f.claimPath, "extra.txt", "extra\n")
			gitRunStore(t, f.claimPath, "add", "-A")
			gitRunStore(t, f.claimPath, "commit", "-q", "-m", "extra")
			_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
			requireSliceSampleRefusal(t, err)
		})
	})

	t.Run("grafted traversal cannot bless forged raw lineage", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			kind    FailureKind
			parents func(*testing.T, sliceNativeFixture) []string
		}{
			{"merge raw parents", KindGitUnreachable, func(t *testing.T, f sliceNativeFixture) []string {
				return []string{
					sliceNativeSideCommit(t, f.repoPath, "graft-side"),
					sliceNativeSideCommit(t, f.repoPath, "graft-second"),
				}
			}},
			{"wrong sole raw parent", KindProjectionConflict, func(t *testing.T, f sliceNativeFixture) []string {
				return []string{sliceNativeSideCommit(t, f.repoPath, "graft-side")}
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				f := seedSliceNativeFixture(t, nil, nil)
				forged := sliceNativeCommitTree(t, f.repoPath, f.tree, "forged end", test.parents(t, f)...)
				grafts := filepath.Join(f.repoPath, ".git", "info", "grafts")
				if err := os.MkdirAll(filepath.Dir(grafts), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(grafts, []byte(forged+" "+f.base+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitRunStore(t, f.claimPath, "reset", "--hard", forged)
				_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
				assertFailureKind(t, err, test.kind)
			})
		}
	})

	t.Run("foreign registered repository refuses", func(t *testing.T) {
		f := seedSliceNativeFixture(t, nil, nil)
		foreign := filepath.Join(t.TempDir(), "foreign")
		gitRunStore(t, filepath.Dir(foreign), "init", "-q", "-b", "main", foreign)
		sliceSampleWrite(t, f.store, func(tx *sql.Tx) {
			sliceSampleExec(t, tx, `UPDATE project_locators SET locator_value=?,normalized_value=? WHERE locator_id=?`, foreign, foreign, f.locatorID)
		})
		_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
		requireSliceSampleRefusal(t, err)
	})

	t.Run("rename endpoints count both sides", func(t *testing.T) {
		f := seedSliceNativeFixture(t, func(plan *WorkflowDesignArchitecture) {
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "project", Path: "moved.go", Role: DesignArchitectureFileRoleSource})
			plan.Slices[0].Files = append(append([]string{}, sliceNativeFiles...), "moved.go")
		}, func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "slice.go")); err != nil {
				t.Fatal(err)
			}
			sliceNativeWrite(t, dir, "moved.go", "one\ntwo\nthree\nfour\nfive\n")
			sliceNativeWrite(t, dir, "unïcode.go", "unicode\nplus\n")
			sliceNativeWrite(t, dir, "slice_test.go", "package t changed\n")
			sliceNativeWrite(t, dir, "slice_gen.go", "generated changed\n")
		})
		preparation, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
		if err != nil {
			t.Fatalf("a rename inside the declared slice files must prepare: %v", err)
		}
		counts := map[string]slicePathCounts{}
		for _, path := range preparation.measurement.result.Paths {
			counts[path.Path] = path
		}
		if counts["slice.go"].Deletions != 3 || counts["slice.go"].Additions != 0 || counts["moved.go"].Additions != 5 || counts["moved.go"].Deletions != 0 {
			t.Fatalf("rename endpoints = %+v", preparation.measurement.result.Paths)
		}
	})

	t.Run("binary source, symlink mode, outside and unchanged paths refuse", func(t *testing.T) {
		for _, test := range []struct {
			name string
			edit func(*testing.T, string)
		}{
			{"binary source blob", func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "slice.go"), []byte{'b', 0x00, 'i', 'n'}, 0o644); err != nil {
					t.Fatal(err)
				}
			}},
			{"symlink mode", func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "slice.go")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("unchanged.go", filepath.Join(dir, "slice.go")); err != nil {
					t.Fatal(err)
				}
			}},
			{"path outside the slice", func(t *testing.T, dir string) { sliceNativeWrite(t, dir, "outside.go", "outside\n") }},
			{"declared unchanged path", func(t *testing.T, dir string) { sliceNativeWrite(t, dir, "unchanged.go", "touched\n") }},
		} {
			t.Run(test.name, func(t *testing.T) {
				f := seedSliceNativeFixture(t, nil, test.edit)
				_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
				requireSliceSampleRefusal(t, err)
			})
		}
	})

	t.Run("declared cap bounds pass exactly and refuse above", func(t *testing.T) {
		t.Run("exactly the declared limit passes", func(t *testing.T) {
			f := seedSliceNativeFixture(t, func(plan *WorkflowDesignArchitecture) { plan.Slices[0].MaxChangedLines = 3 }, nil)
			if _, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f)); err != nil {
				t.Fatalf("exactly the declared limit must pass: %v", err)
			}
		})
		t.Run("one line above refuses", func(t *testing.T) {
			f := seedSliceNativeFixture(t, func(plan *WorkflowDesignArchitecture) { plan.Slices[0].MaxChangedLines = 2 }, nil)
			_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
			requireSliceSampleRefusal(t, err)
		})
	})

	t.Run("tab byte path refuses with the exact bytes", func(t *testing.T) {
		f := seedSliceNativeFixture(t, nil, func(t *testing.T, dir string) {
			sliceNativeWrite(t, dir, "odd\tname.go", "tabbed\n")
		})
		_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
		requireSliceSampleRefusal(t, err)
		if !strings.Contains(err.Error(), fmt.Sprintf("%q", "odd\tname.go")) {
			t.Fatalf("refusal %v does not carry the exact tab path bytes", err)
		}
	})

	t.Run("caller deadline bounds the whole probe as a timeout", func(t *testing.T) {
		f := seedSliceNativeFixture(t, nil, nil)
		sliceGitFakePath(t, `sleep 30`)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(1200*time.Millisecond))
		defer cancel()
		start := time.Now()
		_, err := PrepareWorkerSliceConformance(ctx, f.store, sliceNativeRequest(t, f))
		if !hasFailureKind(err, KindTimeout) {
			t.Fatalf("deadline error = %v, want %s", err, KindTimeout)
		}
		if elapsed := time.Since(start); elapsed > 4*time.Second {
			t.Fatalf("probe took %s; the caller's deadline was extended", elapsed)
		}
	})

	t.Run("native combined output bound refuses without truncation pass", func(t *testing.T) {
		f := seedSliceNativeFixture(t, nil, nil)
		sliceGitFakePath(t, `head -c 3145728 /dev/zero; head -c 2097152 /dev/zero >&2`)
		_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
		if !hasFailureKind(err, KindUnavailable) {
			t.Fatalf("output bound error = %v, want %s", err, KindUnavailable)
		}
	})

	t.Run("malformed commit header objects refuse the lineage", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			object []byte
		}{
			{"missing body separator", []byte("tree " + strings.Repeat("c", 40) + "\nparent " + strings.Repeat("a", 40) + "\nauthor x <x@invalid> 0 +0000\n")},
			{"no parent header", []byte("tree " + strings.Repeat("c", 40) + "\nauthor x <x@invalid> 0 +0000\n\nforged\n")},
			{"two parent headers", []byte("tree " + strings.Repeat("c", 40) + "\nparent " + strings.Repeat("a", 40) + "\nparent " + strings.Repeat("9", 40) + "\nauthor x <x@invalid> 0 +0000\n\nforged\n")},
			{"parent header after author", []byte("tree " + strings.Repeat("c", 40) + "\nauthor x <x@invalid> 0 +0000\nparent " + strings.Repeat("a", 40) + "\ncommitter x <x@invalid> 0 +0000\n\nforged\n")},
			{"malformed tree oid", []byte("tree HEAD\nparent " + strings.Repeat("a", 40) + "\nauthor x <x@invalid> 0 +0000\n\nforged\n")},
			{"malformed parent oid", []byte("tree " + strings.Repeat("c", 40) + "\nparent " + strings.Repeat("A", 40) + "\nauthor x <x@invalid> 0 +0000\n\nforged\n")},
			{"empty object", nil},
		} {
			t.Run(test.name, func(t *testing.T) {
				f := seedSliceSampleFixture(t, "implement")
				common := sliceSeamCommonDir(t, f)
				seam := &sliceGitSeamRunner{outputs: [][]byte{
					[]byte(common + "\n"), []byte(common + "\n"), {},
					[]byte(strings.Repeat("b", 40) + "\n"),
					test.object,
				}}
				request := PrepareWorkerSliceConformanceRequest{
					WorkID: f.workID, AttemptID: f.attemptID,
					ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: seam,
				}
				_, err := PrepareWorkerSliceConformance(context.Background(), f.store, request)
				assertFailureKind(t, err, KindGitUnreachable)
			})
		}
	})

	t.Run("unreachable Git common dir refuses as a retry-safe typed failure", func(t *testing.T) {
		f := seedSliceSampleFixture(t, "implement")
		sliceSeamCommonDir(t, f)
		absent := filepath.Join(f.repoPath, ".git", "absent-store")
		seam := &sliceGitSeamRunner{outputs: [][]byte{[]byte(absent + "\n")}}
		request := PrepareWorkerSliceConformanceRequest{
			WorkID: f.workID, AttemptID: f.attemptID,
			ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: seam,
		}
		_, err := PrepareWorkerSliceConformance(context.Background(), f.store, request)
		var failure *Failure
		if !errors.As(err, &failure) {
			t.Fatalf("unreachable common dir error %v must be a typed *Failure, not a raw filesystem error", err)
		}
		if failure.Kind != KindGitUnreachable || !failure.RetrySafe {
			t.Fatalf("unreachable common dir failure kind %s retry-safe %v; want %s retry-safe", failure.Kind, failure.RetrySafe, KindGitUnreachable)
		}
	})

	t.Run("empty or relative common-dir output refuses before canonicalization", func(t *testing.T) {
		for _, out := range []string{"", ".", ".git"} {
			t.Run("output "+fmt.Sprintf("%q", out), func(t *testing.T) {
				f := seedSliceSampleFixture(t, "implement")
				sliceSeamCommonDir(t, f)
				seam := &sliceGitSeamRunner{outputs: [][]byte{[]byte(out)}}
				request := PrepareWorkerSliceConformanceRequest{
					WorkID: f.workID, AttemptID: f.attemptID,
					ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: seam,
				}
				_, err := PrepareWorkerSliceConformance(context.Background(), f.store, request)
				assertFailureKind(t, err, KindGitUnreachable)
			})
		}
	})

	t.Run("registered canonical locator through a symlink to the same repository prepares", func(t *testing.T) {
		f := seedSliceNativeFixture(t, nil, nil)
		link := filepath.Join(filepath.Dir(f.repoPath), "registered-link")
		if err := os.Symlink(f.repoPath, link); err != nil {
			t.Fatal(err)
		}
		sliceSampleWrite(t, f.store, func(tx *sql.Tx) {
			sliceSampleExec(t, tx, `UPDATE project_locators SET locator_value=?,normalized_value=? WHERE locator_id=?`, link, link, f.locatorID)
		})
		preparation, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
		if err != nil {
			t.Fatalf("a registered symlink to the same repository must prepare: %v", err)
		}
		if preparation.measurement.end != f.end || preparation.measurement.tree != f.tree {
			t.Fatalf("sealed = %s %s; want %s %s", preparation.measurement.end, preparation.measurement.tree, f.end, f.tree)
		}
	})

	t.Run("the read snapshot stays closed during the probe", func(t *testing.T) {
		f := seedSliceSampleFixture(t, "implement")
		common := sliceSeamCommonDir(t, f)
		base, end, tree := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
		seam := &sliceNativeProbeSeam{store: f.store, deadline: 2 * time.Second, outputs: [][]byte{
			[]byte(common + "\n"), []byte(common + "\n"), {},
			[]byte(end + "\n"), sliceCommitObject(tree, base),
			{}, {},
			{}, []byte(end + "\n"), sliceCommitObject(tree, base),
		}}
		request := PrepareWorkerSliceConformanceRequest{
			WorkID: f.workID, AttemptID: f.attemptID,
			ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: seam,
		}
		preparation, err := PrepareWorkerSliceConformance(context.Background(), f.store, request)
		if err != nil {
			t.Fatalf("seamed probe must prepare: %v", err)
		}
		m := preparation.measurement
		if m.base != base || m.end != end || m.tree != tree || m.algorithm != "git-diff-myers-v1" || len(m.result.Paths) != 0 || seam.calls != 10 {
			t.Fatalf("sealed = %+v calls %d; want the base/end/tree seal with empty counts and 10 seam calls", m, seam.calls)
		}
	})

	t.Run("clean HEAD or tree drift under the measurement refuses", func(t *testing.T) {
		f := seedSliceSampleFixture(t, "implement")
		common := sliceSeamCommonDir(t, f)
		base, end, tree := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
		moved := strings.Repeat("9", 40)
		seam := &sliceNativeProbeSeam{store: f.store, deadline: 2 * time.Second, outputs: [][]byte{
			[]byte(common + "\n"), []byte(common + "\n"), {},
			[]byte(end + "\n"), sliceCommitObject(tree, base),
			{}, {},
			{}, []byte(moved + "\n"), sliceCommitObject(tree, base),
		}}
		request := PrepareWorkerSliceConformanceRequest{
			WorkID: f.workID, AttemptID: f.attemptID,
			ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: seam,
		}
		_, err := PrepareWorkerSliceConformance(context.Background(), f.store, request)
		requireSliceSampleRefusal(t, err)
	})

	t.Run("seam aggregate output bound refuses", func(t *testing.T) {
		f := seedSliceSampleFixture(t, "implement")
		common := sliceSeamCommonDir(t, f)
		header := []byte("tree " + strings.Repeat("c", 40) + "\nparent " + strings.Repeat("a", 40) + "\nauthor fixture <fixture@invalid> 0 +0000\ncommitter fixture <fixture@invalid> 0 +0000\n\n")
		padded := make([]byte, 0, maxGitOutput/2)
		padded = append(padded, header...)
		padded = append(padded, bytes.Repeat([]byte("m"), maxGitOutput/2-len(header))...)
		seam := &sliceNativeProbeSeam{store: f.store, deadline: 2 * time.Second, outputs: [][]byte{
			[]byte(common + "\n"), []byte(common + "\n"), {},
			[]byte(strings.Repeat("b", 40) + "\n"),
			padded,
			bytes.Repeat([]byte("d"), maxGitOutput/2+1),
		}}
		request := PrepareWorkerSliceConformanceRequest{
			WorkID: f.workID, AttemptID: f.attemptID,
			ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: seam,
		}
		_, err := PrepareWorkerSliceConformance(context.Background(), f.store, request)
		if !hasFailureKind(err, KindUnavailable) {
			t.Fatalf("aggregate bound error = %v, want %s", err, KindUnavailable)
		}
	})

	t.Run("revalidation after the native seal stays SQL-only through drift", func(t *testing.T) {
		f := seedSliceNativeFixture(t, nil, nil)
		preparation, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
		if err != nil {
			t.Fatal(err)
		}
		sliceSampleWrite(t, f.store, func(tx *sql.Tx) {
			sliceSampleExec(t, tx, `UPDATE worktree_entries SET branch='change/replacement'`)
		})
		ctx, tx := sliceSampleTx(t, f.store)
		requireSliceSampleRefusal(t, preparation.revalidateTx(ctx, tx))
	})

	t.Run("claim recorded beneath a symlinked root prepares under its canonical identity", func(t *testing.T) {
		f := seedSliceNativeFixture(t, nil, nil)
		link := filepath.Join(t.TempDir(), "claim-root-link")
		if err := os.Symlink(filepath.Dir(f.claimPath), link); err != nil {
			t.Fatal(err)
		}
		stored := filepath.Join(link, filepath.Base(f.claimPath))
		sliceSampleWrite(t, f.store, func(tx *sql.Tx) {
			sliceSampleExec(t, tx, `UPDATE worktree_entries SET path=?`, stored)
		})
		preparation, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
		if err != nil {
			t.Fatalf("a claim recorded beneath a symlinked root must prepare: %v", err)
		}
		if preparation.measurement.end != f.end {
			t.Fatalf("sealed end %s; want %s", preparation.measurement.end, f.end)
		}
		if preparation.measurement.claim.Path != stored || preparation.measurement.claim.ClaimOpID != "claim:native" {
			t.Fatalf("sealed claim = %s %s; want the stored raw path %s and the claim:native generation", preparation.measurement.claim.Path, preparation.measurement.claim.ClaimOpID, stored)
		}
	})

	t.Run("inactive or foreign-Project claim refuses after the snapshot", func(t *testing.T) {
		for _, test := range []struct {
			name            string
			statement       string
			kind            FailureKind
			selectorAdapted bool
		}{
			{"inactive claim", `UPDATE worktree_entries SET state='reclaimed',reclaimed_at='fixture'`, KindUnauthorizedDispatch, true},
			{"foreign-Project claim", `UPDATE worktree_entries SET project_id='project:other'`, KindProjectionConflict, false},
		} {
			t.Run(test.name, func(t *testing.T) {
				f := seedSliceNativeFixture(t, nil, nil)
				sliceSampleWrite(t, f.store, func(tx *sql.Tx) { sliceSampleExec(t, tx, test.statement) })
				_, err := PrepareWorkerSliceConformance(context.Background(), f.store, sliceNativeRequest(t, f))
				assertFailureKind(t, err, test.kind)
				var failure *Failure
				if !errors.As(err, &failure) || !test.selectorAdapted {
					return
				}
				// The shared selector refuses with the terminal observation's
				// op and an abandon recovery a completed attempt cannot take.
				if failure.Op != workerSliceConformanceOp || failure.RecoveryAction != "restore the attempt's recorded worktree claim" {
					t.Fatalf("claim refusal = op %q recovery %q; want the conformance op and the recorded-claim restore", failure.Op, failure.RecoveryAction)
				}
			})
		}
	})
}
