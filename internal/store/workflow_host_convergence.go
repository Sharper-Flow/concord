package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

type workflowDispatchHostKey struct{}

type workflowDispatchHost struct {
	sessionRef string
	worktree   string
}

func withWorkflowDispatchHost(ctx context.Context, actionID, sessionRef, worktree string) context.Context {
	if actionID != "dispatch_worker" {
		return ctx
	}
	return context.WithValue(ctx, workflowDispatchHostKey{}, workflowDispatchHost{sessionRef, worktree})
}

// The caller supplies the lease set before the transaction.
func workflowHostRetryConvergence(ctx context.Context, q queryer, workID string, lastDispatch, boundary int64) (WorkflowRetryConvergence, error) {
	host, ok := ctx.Value(workflowDispatchHostKey{}).(workflowDispatchHost)
	leases, observed := hostLeaseSetFromContext(ctx)
	if !ok || host.sessionRef == "" || host.worktree == "" || !observed || leases.Err != nil {
		return WorkflowRetryConvergence{}, nil
	}
	var consumed bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.retry_convergence.basis')='host_changed')`, workID, WorkflowActionCompleted, boundary).Scan(&consumed); err != nil {
		return WorkflowRetryConvergence{}, workflowProjectionError(err, "cannot inspect consumed host convergence")
	}
	if consumed {
		return WorkflowRetryConvergence{}, nil
	}
	var seq int64
	var raw, occurredAt string
	err := q.QueryRowContext(ctx, `SELECT f.seq,f.payload,f.occurred_at FROM domain_events d JOIN worker_attempts a ON a.work_id=d.subject_id AND a.attempt_id=json_extract(d.payload,'$.worker_attempt_id') JOIN domain_events f ON f.subject_type=d.subject_type AND f.subject_id=d.subject_id AND f.kind=? AND json_extract(f.payload,'$.attempt_id')=a.attempt_id AND f.seq>d.seq WHERE d.seq=? AND d.subject_type='work_item' AND d.subject_id=? AND a.lifecycle_state='failed' ORDER BY f.seq DESC LIMIT 1`, WorkerFailed, lastDispatch, workID).Scan(&seq, &raw, &occurredAt)
	if err == sql.ErrNoRows {
		return WorkflowRetryConvergence{}, nil
	}
	if err != nil {
		return WorkflowRetryConvergence{}, workflowProjectionError(err, "cannot inspect the latest host failure")
	}
	var failure WorkerFailedPayload
	if err := json.Unmarshal([]byte(raw), &failure); err != nil {
		return WorkflowRetryConvergence{}, workflowProjectionError(err, "cannot decode the latest host failure")
	}
	if !workflowHostExecutionFailure(failure.FailureAttribution) {
		return WorkflowRetryConvergence{}, nil
	}
	failedAt, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return WorkflowRetryConvergence{}, workflowProjectionError(err, "cannot read the host failure time")
	}
	var pid int
	var start uint64
	err = q.QueryRowContext(ctx, `SELECT o.host_pid,o.host_pid_start FROM worktree_entries e JOIN worktree_occupancy o ON o.worktree_id=e.set_id||':'||e.project_id||':'||e.claim_op_id WHERE e.set_id=? AND e.path=? AND e.state='active' AND o.session_ref=? AND o.has_process_identity=1`, WorktreeSetID(workID), host.worktree, host.sessionRef).Scan(&pid, &start)
	if err == sql.ErrNoRows {
		return WorkflowRetryConvergence{}, nil
	}
	if err != nil {
		return WorkflowRetryConvergence{}, workflowProjectionError(err, "cannot read the dispatching host occupancy")
	}
	for _, lease := range leases.Leases {
		if lease.PID != pid || lease.PidStart != start || start == 0 {
			continue
		}
		liveStart, liveErr := hostlease.ProcessStart(pid)
		if liveErr != nil || liveStart != start {
			return WorkflowRetryConvergence{}, nil
		}
		startedAt, startErr := hostLeaseWallStart(pid)
		if startErr != nil || !startedAt.After(failedAt.Add(wallStartSkewBound)) {
			return WorkflowRetryConvergence{}, nil
		}
		return WorkflowRetryConvergence{Basis: "host_changed", LatestRecordSeq: seq, HostPID: pid, HostPIDStart: start}, nil
	}
	return WorkflowRetryConvergence{}, nil
}

func workflowHostExecutionFailure(attribution *WorkerFailureAttribution) bool {
	if attribution == nil || attribution.Origin != WorkerFailureAttributionRecorded || attribution.Observation == nil {
		return false
	}
	observation := attribution.Observation
	if observation.Observer != WorkerFailureObserverHostAdapter || observation.Phase != WorkerFailurePhaseHostExecution {
		return false
	}
	terminal := false
	for _, source := range observation.Sources {
		if source.Kind != WorkerFailureSourceHostEvent || source.Host == nil {
			continue
		}
		if source.Host.Tag == WorkerFailureHostTagAbandonment {
			return false
		}
		terminal = terminal || source.Host.Tag == WorkerFailureHostTagTerminal || source.Host.Tag == WorkerFailureHostTagCancellation
	}
	return terminal
}
