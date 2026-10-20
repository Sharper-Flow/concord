package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func splitBaselineFixture(t *testing.T, workID, foundation string) (workContextFixture, string, WorkerJobBinding, string) {
	t.Helper()
	f, dir, base := sizeJourneyFixture(t, workID)
	route := workflowReturnRouteFixture{store: f.store, owner: f.owner}
	job := WorkerJobBinding{JobID: "job:baseline-predecessor"}
	fields := workerJobSizeEditingFields(t, workID, route)
	fields["production_size_max"] = 10
	fields["measurement_basis"].(map[string]any)["base_commit"] = base
	recordEditingJobForTest(t, route, workID, fields, &job)
	attempt := "attempt:" + workID
	if err := sizeJourneyDispatch(t, f, dir, attempt, job, nil); err != nil {
		t.Fatal(err)
	}
	if err := appendJobBoundWorkerDispatch(t, f.store, workID, attempt, &job); err != nil {
		t.Fatal(err)
	}
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: dir}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "store"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "store", "worker_jobs.go"), []byte(strings.Repeat("// production line\n", 15)), 0o600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "worker@example.invalid", "overrun")
	overrun := j.head(t)
	completed := reportBindingCompletion(t, f.store, workID, attempt, reviewGateLane(t, "implementation"), &job, func(p *WorkerCompletedPayload) {
		p.ObservedDelivery = &WorkerObservedDelivery{Commit: overrun, State: "clean", Base: base, Paths: []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 15}}}
		p.ProductionSize = &WorkerProductionSize{Base: base, Tree: overrun, Count: 15}
	})
	if err := ApplyOperation(context.Background(), f.store, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}
	switch foundation {
	case "original":
		gitRunStore(t, dir, "reset", "--hard", base)
	case "descendant":
		descendantCommit(t, j, "above-overrun")
	}
	j.observeRun(t, "successor-foundation")
	subject := j.head(t)
	a := sizeAcceptance{fixture: route, s: f.store, workID: workID, attemptID: attempt, job: job, tree: overrun}
	split := replacementSplitFields(t, a, nil)
	oracle := fields["acceptance_oracle"].(map[string]any)
	seedOraclePreparationFieldsFixture(t, f.store, workID, oracle)
	for _, successor := range split["split"].(map[string]any)["successors"].([]map[string]any) {
		successor["measurement_basis"].(map[string]any)["base_commit"] = subject
		successor["acceptance_oracle"] = oracle
	}
	if err := recordWorkerJobActionForTest(t, f.store, workID, f.owner, split); err != nil {
		t.Fatal(err)
	}
	return f, dir, latestSplitBinding(t, route, workID, "job:successor-a"), overrun
}

func TestWorkerJobSplitSuccessorBaseline(t *testing.T) {
	for _, foundation := range []string{"overrun", "descendant", "original"} {
		t.Run(foundation, func(t *testing.T) {
			f, dir, job, overrun := splitBaselineFixture(t, "split-baseline-"+foundation, foundation)
			before := readWorkVersion(t, f.store, f.workID)
			err := sizeJourneyDispatch(t, f, dir, "attempt:successor", job, nil)
			if foundation == "original" {
				if err != nil {
					t.Fatalf("successor on an independent foundation: %v", err)
				}
				return
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindUnauthorizedDispatch || !strings.Contains(err.Error(), overrun) {
				t.Fatalf("successor on %s = %v; want overrun-baseline refusal naming %s", foundation, err, overrun)
			}
			if after := readWorkVersion(t, f.store, f.workID); after != before {
				t.Fatalf("refused baseline changed work version %d to %d", before, after)
			}
		})
	}
}

func TestWorkerJobSplitSuccessorRefreshesPreparation(t *testing.T) {
	f, dir, job, _ := splitBaselineFixture(t, "split-refresh-preparation", "original")
	before, err := readWorkerJobRecordedTx(context.Background(), f.store.db, f.workID, job)
	if err != nil {
		t.Fatal(err)
	}
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: dir}
	current := descendantCommit(t, j, "accepted-prerequisite")
	for _, mode := range []string{"stale", "omitted", "fresh"} {
		t.Run(mode, func(t *testing.T) {
			var oracle AcceptanceOracle
			if err := json.Unmarshal(mustJSONValue(before.AcceptanceOracle), &oracle); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "omitted":
				oracle.Controls[0].ReadinessEvidenceRefs = nil
			case "fresh":
				seedOraclePreparationLedgerFixture(t, f.store, f.workID, &oracle)
			}
			fields := workerJobSizeEditingFields(t, f.workID, workflowReturnRouteFixture{store: f.store, owner: f.owner})
			fields["job_id"] = job.JobID
			fields["production_size_max"] = 5
			fields["acceptance_oracle"] = oracle
			fields["measurement_basis"].(map[string]any)["base_commit"] = current
			fields["measurement_basis"].(map[string]any)["paths"] = before.MeasurementBasis.Paths
			err := recordWorkerJobActionForTest(t, f.store, f.workID, f.owner, fields)
			if mode != "fresh" {
				if err == nil || !strings.Contains(err.Error(), "readiness") {
					t.Fatalf("%s preparation reference = %v; want native readiness refusal", mode, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("fresh preparation with retained control obligations: %v", err)
			}
			refreshed := *before
			refreshed.AcceptanceOracle = &oracle
			if DeriveWorkerJobDigest(*before) == DeriveWorkerJobDigest(refreshed) {
				t.Fatal("fresh preparation references did not enter the revision digest")
			}
		})
	}
}

func TestWorkerJobSplitRetainsEveryOracleObligationField(t *testing.T) {
	f := workerJobSizeFixture(t, "split-exact-oracle-fields")
	job := recordSplitPredecessor(t, "split-exact-oracle-fields", f)
	predecessor, err := readWorkerJobRecordedTx(context.Background(), f.store.db, "split-exact-oracle-fields", job)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"owners", "cases", "controls"} {
		var graph map[string][]map[string]json.RawMessage
		if err := json.Unmarshal(mustJSONValue(predecessor.AcceptanceOracle), &graph); err != nil {
			t.Fatal(err)
		}
		for field, raw := range graph[group][0] {
			if field == "readiness_evidence_refs" {
				continue
			}
			t.Run(group+"/"+field, func(t *testing.T) {
				var changed map[string][]map[string]json.RawMessage
				if err := json.Unmarshal(mustJSONValue(predecessor.AcceptanceOracle), &changed); err != nil {
					t.Fatal(err)
				}
				var value any
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				switch value := value.(type) {
				case string:
					changed[group][0][field] = mustJSONValue(value + "-changed")
				case []any:
					changed[group][0][field] = mustJSONValue(append(value, "changed"))
				case map[string]any:
					if field == "recipe_source" {
						value["commit_oid"] = strings.Repeat("e", 40)
					} else {
						value["entry_point"] = "changed"
					}
					changed[group][0][field] = mustJSONValue(value)
				default:
					t.Fatalf("unsupported oracle field shape %T", value)
				}
				var oracle AcceptanceOracle
				if err := json.Unmarshal(mustJSONValue(changed), &oracle); err != nil {
					t.Fatal(err)
				}
				successor := *predecessor
				successor.AcceptanceOracle = &oracle
				if problem := workerJobSplitCoverageProblem(*predecessor, []WorkerJobRecordedPayload{successor}); !strings.Contains(problem, "no successor carries predecessor oracle") {
					t.Fatalf("changed %s/%s coverage = %q; want unchanged-oracle refusal", group, field, problem)
				}
			})
		}
	}
}

func TestWorkerJobSplitBaselineRequiresExactPreparedLineage(t *testing.T) {
	f, dir, job, _ := splitBaselineFixture(t, "split-baseline-proof", "original")
	for _, mode := range []string{"missing", "wrong-lineage", "dirty", "outage"} {
		t.Run(mode, func(t *testing.T) {
			request := PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: dir, WorkerJob: &job}
			if mode == "outage" {
				request.Runner = reviewAncestryOutageRunner{}
			}
			prepared, err := PrepareWorkerDispatchFreshness(context.Background(), f.store, request)
			if err != nil {
				t.Fatal(err)
			}
			subject := prepared.proof.observation.Commit
			switch mode {
			case "missing":
				prepared.proof.splitOverruns = nil
			case "wrong-lineage":
				prepared.proof.splitOverruns[0].commit = strings.Repeat("e", 40)
			case "dirty":
				prepared.proof.observation.State = "dirty"
			}
			tx, err := f.store.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = admitWorkerJobSplitBaselineTx(WithWorkerDispatchPreparation(context.Background(), prepared), tx, f.workID, job.JobID, subject, subject)
			want := KindProjectionConflict
			if mode == "outage" {
				want = KindGitUnreachable
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != want {
				t.Fatalf("%s proof = %v; want %s", mode, err, want)
			}
		})
	}
}

func TestWorkerJobSplitBaselineRetainsAncestorOverruns(t *testing.T) {
	f, dir, job, overrun := splitBaselineFixture(t, "split-baseline-nested", "original")
	route := workflowReturnRouteFixture{store: f.store, owner: f.owner}
	split := splitFields(t, f.workID, route, job, func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		for i, successor := range successors {
			name := successor["job_id"].(string) + "-leaf"
			successor["job_id"] = name
			successor["production_size_max"] = 5
			allocation[i]["job_id"] = name
		}
		return successors, allocation
	})
	if err := recordWorkerJobActionForTest(t, f.store, f.workID, f.owner, split); err != nil {
		t.Fatal(err)
	}
	leaf := latestSplitBinding(t, route, f.workID, "job:successor-a-leaf")
	commits, err := workerJobSplitOverrunCommitsTx(context.Background(), f.store.db, f.workID, leaf.JobID)
	if err != nil || len(commits) != 1 || commits[0] != overrun {
		t.Fatalf("nested split overruns=%v, error=%v; want ancestor %s", commits, err, overrun)
	}
	gitRunStore(t, dir, "reset", "--hard", overrun)
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: dir}
	j.observeRun(t, "retained-overrun")
	recorded, err := readWorkerJobRecordedTx(context.Background(), f.store.db, f.workID, leaf)
	if err != nil {
		t.Fatal(err)
	}
	seedOraclePreparationLedgerFixture(t, f.store, f.workID, recorded.AcceptanceOracle)
	fields := workerJobSizeEditingFields(t, f.workID, route)
	fields["job_id"], fields["production_size_max"] = leaf.JobID, 5
	fields["acceptance_oracle"] = recorded.AcceptanceOracle
	fields["measurement_basis"].(map[string]any)["base_commit"] = overrun
	fields["measurement_basis"].(map[string]any)["paths"] = recorded.MeasurementBasis.Paths
	recordEditingJobForTest(t, route, f.workID, fields, &leaf)
	if err := sizeJourneyDispatch(t, f, dir, "attempt:nested-successor", leaf, nil); err == nil || !strings.Contains(err.Error(), "cannot measure from overrun commit "+overrun) {
		t.Fatalf("nested successor baseline = %v; want retained ancestor-overrun refusal", err)
	}
}
