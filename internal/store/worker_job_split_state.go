package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// workerJobSplitState is one recorded split's derived required-leaf state
// (CD-0225 D3): the split event beside its id, the discharge that closed it
// when one is recorded, the transitive required leaves, and the leaf keys
// still unsatisfied.
type workerJobSplitState struct {
	SplitID     string
	Split       WorkerJobSplitPayload
	DischargeID string
	Leaves      []WorkerJobRevisionView
	Unsatisfied []string
}

// workerJobSplitDischargePayload is the closed discharge content the
// derivation joins on: the exact split event the discharge closes and the
// exact predecessor binding that split recorded.
type workerJobSplitDischargePayload struct {
	SplitID     string           `json:"split_id"`
	Predecessor WorkerJobBinding `json:"predecessor"`
}

// workerJobSplitEvent is one recorded split beside its event id, so a
// discharge can join the exact split record rather than a payload match.
type workerJobSplitEvent struct {
	eventID string
	payload WorkerJobSplitPayload
}

func workerJobSplitStateRefusal(detail string) error {
	return newFailure(KindInvariantViolation, "worker_job_split_state", "worker-job split leaf state "+detail, false, "rebuild the worker-job events from the authoritative log")
}

// readWorkerJobSplitEventsTx reads every recorded split of the work in log
// order, retaining each event id beside its payload.
func readWorkerJobSplitEventsTx(ctx context.Context, q queryer, workID string) ([]workerJobSplitEvent, error) {
	rows, err := q.QueryContext(ctx, `SELECT event_id, payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? ORDER BY seq`, string(SubjectWorkItem), workID, WorkerJobSplit)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot read the work's job splits", true, "retry once the event log is readable", err)
	}
	defer rows.Close()
	events := []workerJobSplitEvent{}
	for rows.Next() {
		var event workerJobSplitEvent
		var raw string
		if err := rows.Scan(&event.eventID, &raw); err != nil {
			return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan the work's job splits", true, "retry once the event log is readable", err)
		}
		if err := json.Unmarshal([]byte(raw), &event.payload); err != nil {
			return nil, workerJobSplitStateRefusal("holds a recorded job split that is not one closed split payload")
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan the work's job splits", true, "retry once the event log is readable", err)
	}
	return events, nil
}

// workerJobSplitDischargeIDsTx joins the work's recorded discharges onto the
// splits: each discharge must name a recorded split event at most once, with
// the exact predecessor binding that split recorded. The rows are fully read
// and closed before the join checks run; neither queries again while rows
// are open.
func workerJobSplitDischargeIDsTx(ctx context.Context, q queryer, workID string, splits []workerJobSplitEvent) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT event_id, payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind='worker.job_split_discharged' ORDER BY seq`, string(SubjectWorkItem), workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot read the work's split discharges", true, "retry once the event log is readable", err)
	}
	defer rows.Close()
	dischargeBySplit := map[string]string{}
	predecessorBySplit := map[string]WorkerJobBinding{}
	for rows.Next() {
		var eventID, raw string
		if err := rows.Scan(&eventID, &raw); err != nil {
			return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan the work's split discharges", true, "retry once the event log is readable", err)
		}
		var payload workerJobSplitDischargePayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, workerJobSplitStateRefusal("holds a recorded discharge that is not one closed discharge payload")
		}
		if _, discharged := dischargeBySplit[payload.SplitID]; discharged {
			return nil, workerJobSplitStateRefusal("records a second discharge for split " + payload.SplitID)
		}
		dischargeBySplit[payload.SplitID] = eventID
		predecessorBySplit[payload.SplitID] = payload.Predecessor
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan the work's split discharges", true, "retry once the event log is readable", err)
	}
	for splitID, predecessor := range predecessorBySplit {
		index := slices.IndexFunc(splits, func(recorded workerJobSplitEvent) bool { return recorded.eventID == splitID })
		if index < 0 {
			return nil, workerJobSplitStateRefusal("discharge names split " + splitID + ", which the work holds no record of")
		}
		if splits[index].payload.Predecessor != predecessor {
			return nil, workerJobSplitStateRefusal("discharge names predecessor job " + predecessor.JobID + ", which the discharged split did not record")
		}
	}
	return dischargeBySplit, nil
}

// readWorkerJobSplitStatesTx derives every recorded split's required-leaf
// state in log order. A successor job a later split replaced contributes
// that split's successors; every other successor contributes its latest
// recorded revision under the work's active parent contract authority,
// and is satisfied only at state satisfied with a nonempty result reference
// (CD-0225 D3). This observation changes no job state.
func readWorkerJobSplitStatesTx(ctx context.Context, q queryer, workID string) ([]workerJobSplitState, error) {
	splitEvents, err := readWorkerJobSplitEventsTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if len(splitEvents) == 0 {
		return []workerJobSplitState{}, nil
	}
	splits := make([]WorkerJobSplitPayload, len(splitEvents))
	for index := range splitEvents {
		splits[index] = splitEvents[index].payload
	}
	dischargeBySplit, err := workerJobSplitDischargeIDsTx(ctx, q, workID, splitEvents)
	if err != nil {
		return nil, err
	}
	views, err := readWorkerJobRevisions(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	activeContract, err := activeWorkflowContractVersion(ctx, q, workID, "worker_job_split_state")
	if err != nil {
		return nil, err
	}
	latest := map[string]*WorkerJobRevisionView{}
	for index := range views {
		latest[views[index].Binding.JobID] = &views[index]
	}
	states := make([]workerJobSplitState, 0, len(splitEvents))
	for _, recorded := range splitEvents {
		predecessor := latest[recorded.payload.Predecessor.JobID]
		if predecessor == nil || predecessor.Binding != recorded.payload.Predecessor {
			return nil, workerJobSplitStateRefusal("split " + recorded.eventID + " names predecessor job " + recorded.payload.Predecessor.JobID + ", which is not the recorded current revision")
		}
		state := workerJobSplitState{SplitID: recorded.eventID, Split: recorded.payload, DischargeID: dischargeBySplit[recorded.eventID], Leaves: []WorkerJobRevisionView{}, Unsatisfied: []string{}}
		visited := map[string]bool{}
		pending := append([]WorkerJobBinding(nil), recorded.payload.Successors...)
		for len(pending) > 0 {
			binding := pending[0]
			pending = pending[1:]
			if visited[binding.JobID] {
				return nil, workerJobSplitStateRefusal("required leaves cycle through job " + binding.JobID)
			}
			visited[binding.JobID] = true
			if replaced := workerJobSplitReplacing(splits, binding.JobID); replaced != nil {
				pending = append(pending, replaced.Successors...)
				continue
			}
			leaf := latest[binding.JobID]
			if leaf == nil {
				return nil, workerJobSplitStateRefusal("required leaf job " + binding.JobID + " holds no recorded revision")
			}
			if leaf.ContractVersion != activeContract || leaf.State != "satisfied" || leaf.SatisfiedResultRef == "" {
				state.Unsatisfied = append(state.Unsatisfied, workerJobKey(leaf.Binding.JobID, leaf.Binding.Revision))
			}
			state.Leaves = append(state.Leaves, *leaf)
		}
		slices.SortFunc(state.Leaves, func(first, second WorkerJobRevisionView) int {
			return strings.Compare(first.Binding.JobID, second.Binding.JobID)
		})
		slices.Sort(state.Unsatisfied)
		states = append(states, state)
	}
	return states, nil
}
