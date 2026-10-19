package store

import (
	"context"
	"database/sql"
	"slices"
)

type workerSplitOverrunAncestry struct {
	commit   string
	ancestry reviewSubjectAncestry
}

func workerJobSplitOverrunCommitsTx(ctx context.Context, q queryer, workID, jobID string) ([]string, error) {
	splits, err := readWorkerJobSplitsTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	var commits []string
	visited := map[string]bool{}
	for jobID != "" && !visited[jobID] {
		visited[jobID] = true
		parent := workerJobSplitCreating(splits, jobID)
		if parent == nil {
			break
		}
		if replacement := parent.ReplacedAttempt; replacement != nil && !slices.Contains(commits, replacement.ObservedCommit) {
			commits = append(commits, replacement.ObservedCommit)
		}
		jobID = parent.Predecessor.JobID
	}
	return commits, nil
}

// The first successor dispatch cannot hide its allocated work behind an
// overrun tree. Git runs only in dispatch preparation; this reader revalidates
// the exact ancestor set and consumes those sealed observations with SQL only.
func admitWorkerJobSplitBaselineTx(ctx context.Context, tx *sql.Tx, workID, jobID, base, subject string) error {
	commits, err := workerJobSplitOverrunCommitsTx(ctx, tx, workID, jobID)
	if err != nil || len(commits) == 0 {
		return err
	}
	proof := workerDispatchPreparationFrom(ctx)
	if proof == nil || proof.proof.workID != workID || len(proof.proof.splitOverruns) != len(commits) || proof.proof.observation == nil || proof.proof.observation.State != "clean" || proof.proof.observation.Commit != subject {
		return newFailure(KindProjectionConflict, "workflow_action", "split successor "+jobID+" has no matching sealed baseline ancestry preparation", true, "prepare the dispatch again for the exact successor revision and clean subject")
	}
	for i, commit := range commits {
		witness := proof.proof.splitOverruns[i]
		if witness.commit != commit {
			return newFailure(KindProjectionConflict, "workflow_action", "split successor "+jobID+" was prepared against another overrun lineage", true, "prepare the dispatch again for the exact successor revision")
		}
		if base == commit || subject == commit || witness.ancestry.strictDescendant {
			return newFailure(KindUnauthorizedDispatch, "workflow_action", "split successor "+jobID+" cannot measure from overrun commit "+commit+" or a subject descended from it", false, "retain the predecessor artifacts, isolate the assigned delta on a clean baseline without undelivered predecessor changes, then record a fresh successor revision through record_worker_job")
		}
		if witness.ancestry.failure != nil {
			return witness.ancestry.failure
		}
	}
	return nil
}
