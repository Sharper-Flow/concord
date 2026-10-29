package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
)

// One terminal proof belongs to the current baseline row, not replay history.
// Before is the verified comparison cursor after earlier completed peers; After
// is the enacted inventory admitted by this item's ordered completion gate.
type workflowLawEnactment struct {
	Before workflowLawBaseline `json:"before"`
	After  workflowLawBaseline `json:"after"`
}

func sameWorkflowLawSources(a, b *workflowLawBaseline) bool {
	return a.NoHome == b.NoHome && a.Home == b.Home && reflect.DeepEqual(a.Sources, b.Sources)
}

// The proof holds only the item's own law: an externally made delta is not
// this item's enactment, so a peer cannot consume it as attribution.
func recordWorkflowLawEnactmentTx(ctx context.Context, tx *sql.Tx, completion Event, change *WorkflowLawChange) error {
	before, after, _, err := readWorkflowLawAttributedBaselineTx(ctx, tx, completion.SubjectID, change)
	if err != nil {
		return err
	}
	if len(workflowLawInventoryDifferences(before, after)) == 0 && reflect.DeepEqual(before.Registry, after.Registry) {
		return nil
	}
	encoded, err := json.Marshal(workflowLawEnactment{Before: *before, After: *after})
	if err != nil || len(encoded) > 8*1024*1024 {
		return newFailure(KindInvalidPayload, "complete_workflow", "terminal law enactment exceeds its bounded storage", false, "reduce the law inventory before completing work")
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_law_baselines SET enactment_json=?,enactment_effect_seq=(SELECT effect_seq FROM effect_receipts WHERE event_id=? AND kind='workflow.completed' AND subject_type='work_item' AND subject_id=?) WHERE work_id=? AND enactment_json IS NULL`, string(encoded), completion.EventID, completion.SubjectID, completion.SubjectID)
	if err != nil {
		return workflowProjectionError(err, "cannot record terminal law enactment")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return newFailure(KindInvariantViolation, "complete_workflow", "terminal law enactment has no unique unrecorded baseline", false, "retain the existing proof and reconcile the completion")
	}
	return nil
}

const completedWorkflowLawEnactmentsSQL = ` FROM workflow_law_baselines b JOIN effect_receipts r ON r.effect_seq=b.enactment_effect_seq JOIN workflow_instances i ON i.work_id=b.work_id JOIN work_items w ON w.id=b.work_id WHERE r.kind='workflow.completed' AND r.subject_type='work_item' AND r.subject_id=b.work_id AND i.instance_state='completed' AND w.lifecycle='completed'`

// The attribution reader needs the entire completion join, not only the proof
// bytes. CD-0217 retention therefore fences deletion of any of its owners while
// an older, source-matching execution remains an unfinished consumer.
func requireReleasedWorkflowLawEnactmentQ(ctx context.Context, q queryer, workID, operation string) error {
	var encoded string
	var completionSeq int64
	err := q.QueryRowContext(ctx, `SELECT b.enactment_json,b.enactment_effect_seq`+completedWorkflowLawEnactmentsSQL+` AND b.work_id=?`, workID).Scan(&encoded, &completionSeq)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return workflowProjectionError(err, "cannot read retained law enactment")
	}
	var proof workflowLawEnactment
	if err := json.Unmarshal([]byte(encoded), &proof); err != nil {
		return workflowProjectionError(err, "cannot decode retained law enactment")
	}
	rows, err := q.QueryContext(ctx, `SELECT baseline_json FROM workflow_law_baselines WHERE work_id<>? AND start_effect_seq<? AND work_id IN `+terminalCleanupConsumers, workID, completionSeq)
	if err != nil {
		return workflowProjectionError(err, "cannot read law enactment consumers")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return workflowProjectionError(err, "cannot read law enactment consumer baseline")
		}
		var baseline workflowLawBaseline
		if err := json.Unmarshal([]byte(raw), &baseline); err != nil {
			return workflowProjectionError(err, "cannot decode law enactment consumer baseline")
		}
		if sameWorkflowLawSources(&baseline, &proof.Before) && sameWorkflowLawSources(&baseline, &proof.After) {
			return newFailure(KindOperationConflict, operation, "another execution consumes the completed law enactment", false, "retain the completion proof until its consumers are terminal, published and settled or removed")
		}
	}
	if err := rows.Err(); err != nil {
		return workflowProjectionError(err, "cannot finish reading law enactment consumers")
	}
	return nil
}

func applyCompletedWorkflowLawEnactmentsTx(ctx context.Context, q queryer, workID string, startSeq int64, cursor *workflowLawBaseline) error {
	rows, err := q.QueryContext(ctx, `SELECT b.enactment_json`+completedWorkflowLawEnactmentsSQL+` AND b.work_id<>? AND b.enactment_effect_seq>? ORDER BY b.enactment_effect_seq`, workID, startSeq)
	if err != nil {
		return workflowProjectionError(err, "cannot read completed law enactments")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return workflowProjectionError(err, "cannot read terminal law enactment")
		}
		var proof workflowLawEnactment
		if err := json.Unmarshal([]byte(encoded), &proof); err != nil {
			return workflowProjectionError(err, "cannot decode terminal law enactment")
		}
		applyWorkflowLawEnactment(cursor, proof)
	}
	if err := rows.Err(); err != nil {
		return workflowProjectionError(err, "cannot finish reading completed law enactments")
	}
	return nil
}

// Each identity advances only over an exact before→after link. A gap cannot
// launder a later transition; an unrelated identity need not block a valid link.
func applyWorkflowLawEnactment(cursor *workflowLawBaseline, proof workflowLawEnactment) {
	if !sameWorkflowLawSources(cursor, &proof.Before) || !sameWorkflowLawSources(cursor, &proof.After) {
		return
	}
	states := workflowLawInventoryMap(cursor)
	old, next := workflowLawInventoryMap(&proof.Before), workflowLawInventoryMap(&proof.After)
	for _, id := range workflowLawInventoryDifferences(&proof.Before, &proof.After) {
		current, present := states[id]
		before, existed := old[id]
		if present != existed || present && current.differs(before) {
			continue
		}
		if after, exists := next[id]; exists {
			states[id] = after
		} else {
			delete(states, id)
		}
	}
	cursor.Laws = make([]workflowLawState, 0, len(states))
	for _, state := range states {
		cursor.Laws = append(cursor.Laws, state)
	}
	slices.SortFunc(cursor.Laws, func(a, b workflowLawState) int { return strings.Compare(a.LawID, b.LawID) })
	cursor.Registry = advanceWorkflowLawRegistry(cursor.Registry, proof.Before.Registry, proof.After.Registry)
}

func advanceWorkflowLawRegistry(cursor, before, after *workflowLawRegistrySnapshot) *workflowLawRegistrySnapshot {
	normalize := func(snapshot *workflowLawRegistrySnapshot) workflowLawRegistrySnapshot {
		if snapshot == nil {
			return workflowLawRegistrySnapshot{}
		}
		return *snapshot
	}
	current, old, next := normalize(cursor), normalize(before), normalize(after)
	if current.RootDomainID == old.RootDomainID {
		current.RootDomainID = next.RootDomainID
	}
	pins := func(snapshot workflowLawRegistrySnapshot) map[string]workflowLawRegistryPin {
		result := make(map[string]workflowLawRegistryPin, len(snapshot.Entries))
		for _, pin := range snapshot.Entries {
			result[pin.key()] = pin
		}
		return result
	}
	states, was, now := pins(current), pins(old), pins(next)
	keys := map[string]bool{}
	for key := range was {
		keys[key] = true
	}
	for key := range now {
		keys[key] = true
	}
	for key := range keys {
		prior, existed := was[key]
		future, exists := now[key]
		present, held := states[key]
		if existed == exists && prior == future || held != existed || held && present != prior {
			continue
		}
		if exists {
			states[key] = future
		} else {
			delete(states, key)
		}
	}
	if current.RootDomainID == "" && len(states) == 0 {
		return nil
	}
	current.Entries = make([]workflowLawRegistryPin, 0, len(states))
	for _, pin := range states {
		current.Entries = append(current.Entries, pin)
	}
	slices.SortFunc(current.Entries, func(a, b workflowLawRegistryPin) int { return strings.Compare(a.key(), b.key()) })
	return &current
}
