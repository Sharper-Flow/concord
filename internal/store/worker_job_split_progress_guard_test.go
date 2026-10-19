package store

import (
	"context"
	"strings"
	"testing"
)

// The split_leaf_progress dispatch target guard binds the packet's job to the
// funding the log still supplies: the funded sibling admits, and a foreign
// job, a jobless packet, or a stale acceptance reference refuses.
func TestWorkerJobSplitProgressDispatchGuard(t *testing.T) {
	const workID = "split-progress-guard"
	fixture := splitProgressRawFixture(t, workID)
	s := workerJobSizeStore(fixture)
	unrelated := WorkerJobBinding{JobID: "job:unrelated", Revision: 1}
	unrelatedFields := workerJobSizeEditingFields(t, workID, fixture)
	unrelatedFields["job_id"] = unrelated.JobID
	unrelatedFields["path_scope"] = []string{"adapter/opencode"}
	unrelatedFields["measurement_basis"] = map[string]any{
		"base_commit": testJobSizeBaseCommit(),
		"paths":       []map[string]any{{"path": "adapter/opencode/packet.ts", "class": "production"}},
	}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, unrelatedFields); err != nil {
		t.Fatalf("unrelated job refused: %v", err)
	}
	acceptanceSeq := splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)
	convergence := WorkflowRetryConvergence{Basis: "split_leaf_progress", LatestRecordSeq: acceptanceSeq}
	bindingB := latestSplitBinding(t, fixture, workID, "job:successor-b")
	bindingUnrelated := latestSplitBinding(t, fixture, workID, unrelated.JobID)

	for _, tc := range []struct {
		name        string
		convergence WorkflowRetryConvergence
		target      *WorkerJobBinding
		wantKind    FailureKind
		wantText    string
	}{
		{name: "the funded sibling admits", convergence: convergence, target: &bindingB},
		{name: "a foreign job refuses", convergence: convergence, target: &bindingUnrelated, wantKind: KindUnauthorizedDispatch, wantText: "funds only the never-dispatched required sibling(s) job:successor-b"},
		{name: "a jobless packet refuses", convergence: convergence, target: nil, wantKind: KindInvalidOperation, wantText: "job-bound dispatch"},
		{name: "a stale acceptance reference refuses", convergence: WorkflowRetryConvergence{Basis: "split_leaf_progress", LatestRecordSeq: acceptanceSeq + 1000}, target: &bindingB, wantKind: KindMissingEvidence, wantText: "no longer funds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			guardErr := admitWorkerJobSplitProgressDispatchTx(context.Background(), tx, workID, tc.convergence, tc.target)
			if tc.wantKind == "" {
				if guardErr != nil {
					t.Fatalf("funded sibling dispatch = %v, want admission", guardErr)
				}
				return
			}
			if guardErr == nil || !hasFailureKind(guardErr, tc.wantKind) || !strings.Contains(guardErr.Error(), tc.wantText) {
				t.Fatalf("guard = %v, want %s naming %q", guardErr, tc.wantKind, tc.wantText)
			}
		})
	}
}
