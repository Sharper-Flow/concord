package store

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCatalogActionUsesCurrentCatalog(t *testing.T) {
	catalog := builtinWorkflowActionCatalog()
	for id := range catalog {
		want, err := catalog.action(id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := catalogAction(id)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("current action %s = %+v, %v; want %+v", id, got, err, want)
		}
	}
	if _, err := catalogAction("unknown_action"); err == nil {
		t.Fatal("unknown action resolved through the catalog")
	}
}

func TestSharedWorkerJobDeclarationKeepsResearchReadOnly(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.research").Definition
	if !workflowCallerScopeActive(definition) {
		t.Fatal("research does not reuse the shared worker-job declaration")
	}
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.CapabilityClass == "review" {
			// Review's existing corrective pass is not research production.
			continue
		}
		for _, step := range definition.StepGraph.Steps {
			if LaneStepDispatchAllowed(lane.CapabilityClass, step.Kind) && workerLaneEdits(lane.ID, lane.Digest) {
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
			pinWorkflowAtStep(t, s, workID, definition.Ref, "conclude")
			stalenessSeedWorkerDeliveryAt(t, s, workID, "attempt:research", "investigate")
			lane := reviewGateLane(t, "research")
			if slices.Contains(lane.Capabilities, "edit_scoped_files") {
				t.Fatal("research lane has editing authority")
			}
			if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE worker_attempts SET lane_id=?,lane_digest=?,capability_class='research' WHERE attempt_id='attempt:research'; DELETE FROM write_guard`, lane.ID, lane.Digest); err != nil {
				t.Fatal(err)
			}
			job := WorkerJobBinding{JobID: "job:research", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64)}
			// Even an allowance already recorded on the job grants no lane edits.
			allowance := WorkerJobCallerAllowance{ProjectID: "project", Mechanisms: []OracleMechanism{{ProjectID: "project", Path: "owner.go", EntryPoint: "Value"}}, DomainIDs: []string{"root"}, LawBindings: []OracleLawBinding{}}
			if err := validateWorkerJobAllowanceShape("project", &allowance, workerJobAllowanceAuthorFailure); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,reserved_integration,readiness,digest,state,recorded_at,recorded_payload) VALUES(?,?,?,1,'research','read-only proof','project','[]','[]','[]','[]','[]','','{}',?,'recorded','2026-10-01T00:00:00Z',?)`, workID, job.JobID, job.Revision, job.Digest, string(mustJSONValue(map[string]any{"direct_caller_allowance": allowance}))); err != nil {
				t.Fatal(err)
			}
			payload := WorkerCompletedPayload{AttemptID: "attempt:research", ObservedDelivery: &WorkerObservedDelivery{State: "clean", Base: strings.Repeat("a", 40), Commit: strings.Repeat("a", 40)}}
			if tc.changed {
				payload.ObservedDelivery.Commit = strings.Repeat("b", 40)
				payload.ObservedDelivery.ChangedPaths = []string{"value.go"}
			}
			if tc.repairs {
				payload.DirectCallerRepairs = []WorkerDirectCallerRepair{{ChangedPaths: []string{"value.go"}}}
			}
			if _, err := s.db.Exec(`UPDATE worker_attempts SET terminal_kind=?,terminal_payload=? WHERE work_id=? AND attempt_id=?`, WorkerCompleted, string(mustJSONValue(payload)), workID, payload.AttemptID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
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
