package store

import (
	"context"
	"fmt"
	"testing"
)

func TestWorkerJobSplitReplacementCountsOnceAndRetainsDebt(t *testing.T) {
	for _, noShip := range []bool{false, true} {
		t.Run(fmt.Sprint(noShip), func(t *testing.T) {
			a := seedSplitReplacementAcceptance(t, fmt.Sprintf("split-debt-%v", noShip), func(p *WorkerCompletedPayload) {
				p.ObservedDelivery.Paths = []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 15}}
				p.ProductionSize.Count = 15
				if noShip {
					p.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{Severity: "P0", Confidence: "high", Detail: "an out-of-scope safety issue prevents shipment", Oracle: &WorkerOracleFinding{Classification: OracleClassificationFollowUp}}}}
				}
			})
			if err := recordWorkerJobActionForTest(t, a.s, a.workID, a.fixture.owner, replacementSplitFields(t, a, nil)); err != nil {
				t.Fatal(err)
			}
			if count, err := workflowNonProgressAttemptCountForRegistry(context.Background(), a.s.db, a.workID, "test", BuiltinWorkflowRegistry()); err != nil || count != 1 {
				t.Fatalf("split-replaced nonprogress population=%d, error=%v; want exactly one", count, err)
			}
			var splitSeq int64
			if err := a.s.db.QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=?`, a.workID, WorkerJobSplit).Scan(&splitSeq); err != nil {
				t.Fatal(err)
			}
			history, err := workflowCorrectionWalkForRegistry(context.Background(), a.s.db, a.workID, splitSeq, "test", BuiltinWorkflowRegistry())
			if err != nil {
				t.Fatal(err)
			}
			key := workerJobKey(a.job.JobID, a.job.Revision)
			prior, err := workflowCorrectionWalkForRegistry(context.Background(), a.s.db, a.workID, splitSeq-1, "test", BuiltinWorkflowRegistry())
			if err != nil || prior.unresolved[key] != noShip {
				t.Fatalf("pre-split prefix debt=%v, error=%v; want no_ship debt only (%v)", prior, err, noShip)
			}
			if !history.unresolved[key] {
				t.Fatal("replacement did not open predecessor debt")
			}
			for _, successor := range []string{"job:successor-a", "job:successor-b"} {
				if history.discharge(successor, 1) {
					t.Fatal("successor acceptance reset the window before integration discharged the split")
				}
			}
			if !history.unresolved[key] {
				t.Fatal("successor acceptance consumed predecessor debt")
			}
			if err := RebuildFromLog(context.Background(), a.s); err != nil {
				t.Fatal(err)
			}
			if count, err := workflowNonProgressAttemptCountForRegistry(context.Background(), a.s.db, a.workID, "test", BuiltinWorkflowRegistry()); err != nil || count != 1 {
				t.Fatalf("replayed nonprogress population=%d, error=%v; want exactly one", count, err)
			}
		})
	}
}
