package store

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestLatestCatalogActionUsesLatestContainingCatalog(t *testing.T) {
	catalogs := builtinWorkflowActionCatalogs()
	for _, catalog := range catalogs {
		for id := range catalog.revisions {
			want, err := catalog.action(id)
			if err != nil {
				t.Fatal(err)
			}
			for _, later := range catalogs {
				if later.Version <= catalog.Version {
					continue
				}
				if _, declared := later.revisions[id]; declared {
					want, err = later.action(id)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := latestCatalogAction(id)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("latest action %s = %+v, %v; want %+v", id, got, err, want)
			}
		}
	}
	if _, err := latestCatalogAction("unknown_action"); err == nil {
		t.Fatal("unknown action resolved through the catalog")
	}
}

func TestSharedWorkerJobDeclarationKeepsResearchReadOnly(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.research").Definition
	if !workflowChecksRepairActive(definition) || !workflowCallerScopeActive(definition) {
		t.Fatal("research does not reuse the shared worker-job declaration")
	}
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.CapabilityClass == "review" {
			// Review's existing corrective pass is not research production.
			continue
		}
		for _, step := range definition.StepGraph.Steps {
			if LaneStepDispatchAllowed(lane.CapabilityClass, step.Kind) && workerLaneEdits(lane.ID, lane.Version, lane.Digest) {
				t.Fatalf("research step %s admits editing producer %s", step.ID, lane.ID)
			}
		}
	}
	for _, tc := range []struct {
		name             string
		changed, repairs bool
	}{
		{"unchanged", false, false}, {"changed subject", true, false}, {"caller repair", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			const workID = "shared-job-research"
			pinWorkflowAtStep(t, s, workID, definition.Ref, definition.Version, "conclude")
			stalenessSeedWorkerDeliveryAt(t, s, workID, "attempt:research", "investigate")
			lane := reviewGateLane(t, "research")
			if slices.Contains(lane.Capabilities, "edit_scoped_files") {
				t.Fatal("research lane has editing authority")
			}
			if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worker_attempts SET lane_id=?,lane_version=?,lane_digest=?,capability_class='research' WHERE attempt_id='attempt:research'; DELETE FROM fold_guard`, lane.ID, lane.Version, lane.Digest); err != nil {
				t.Fatal(err)
			}
			job := WorkerJobBinding{JobID: "job:research", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64)}
			// Even an allowance already recorded on the job grants no lane edits.
			allowance := WorkerJobCallerAllowance{ProjectID: "project", Mechanisms: []OracleMechanism{{ProjectID: "project", Path: "owner.go", EntryPoint: "Value"}}, DomainIDs: []string{"root"}, LawBindings: []OracleLawBinding{}}
			if err := validateWorkerJobAllowanceShape("project", &allowance, workerJobAllowanceAuthorFailure); err != nil {
				t.Fatal(err)
			}
			stalenessEvent(t, s, workID, WorkerJobRecorded, string(mustJSONValue(map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest, "direct_caller_allowance": allowance})))
			payload := WorkerCompletedPayload{AttemptID: "attempt:research", ObservedDelivery: &WorkerObservedDelivery{State: "clean", Base: strings.Repeat("a", 40), Commit: strings.Repeat("a", 40)}}
			if tc.changed {
				payload.ObservedDelivery.Commit = strings.Repeat("b", 40)
				payload.ObservedDelivery.ChangedPaths = []string{"value.go"}
			}
			if tc.repairs {
				payload.DirectCallerRepairs = []WorkerDirectCallerRepair{{ChangedPaths: []string{"value.go"}}}
			}
			stalenessEvent(t, s, workID, WorkerCompleted, string(mustJSONValue(payload)))
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = admitWorkerDeliveredPathsTx(t.Context(), tx, workID, definition, &job, payload.AttemptID)
			if tc.changed || tc.repairs {
				if !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(err.Error(), "without editing authority") {
					t.Fatalf("research editing refusal = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
