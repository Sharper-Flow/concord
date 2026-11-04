package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// These types and helpers decode the projections used by migration 142.
type historicalWorkflowVerdict struct {
	ContractVersion          int64  `json:"contract_version"`
	PredicateID              string `json:"predicate_id"`
	VerdictKind              string `json:"verdict_kind"`
	CriterionDigest          string `json:"criterion_digest"`
	IncomparableWithApproved bool   `json:"incomparable_with_approved"`
}

type historicalWorkflowPredicate struct {
	PredicateID    string
	OutcomeKind    string
	OutcomePayload string
}

type workflowContractPredicateHistoryData map[int64]map[string]historicalWorkflowPredicate

func workflowContractPredicateHistory(ctx context.Context, q queryer, workID string, currentVersion int64) (workflowContractPredicateHistoryData, error) {
	rows, err := q.QueryContext(ctx, `SELECT contract_version,predicate_id,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version<=? ORDER BY contract_version,ordinal`, workID, currentVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := workflowContractPredicateHistoryData{}
	for rows.Next() {
		var version int64
		var predicate historicalWorkflowPredicate
		if err := rows.Scan(&version, &predicate.PredicateID, &predicate.OutcomeKind, &predicate.OutcomePayload); err != nil {
			return nil, err
		}
		if history[version] == nil {
			history[version] = map[string]historicalWorkflowPredicate{}
		}
		history[version][predicate.PredicateID] = predicate
	}
	return history, rows.Err()
}

func workflowPredicateHistoryCompatible(history workflowContractPredicateHistoryData, from, to int64, predicateID string, verdict historicalWorkflowVerdict) bool {
	if verdict.CriterionDigest != "" {
		approved, ok := history[to][predicateID]
		if !ok || verdict.PredicateID != predicateID || approved.OutcomeKind == "" {
			return false
		}
		raw, err := json.Marshal(struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}{approved.OutcomeKind, json.RawMessage(approved.OutcomePayload)})
		if err != nil {
			return false
		}
		canonical, err := canonicalJSON(raw)
		if err != nil {
			return false
		}
		sum := sha256.Sum256(canonical)
		return verdict.CriterionDigest == "sha256:"+hex.EncodeToString(sum[:])
	}
	for version := from; version <= to; version++ {
		if _, ok := history[version]; !ok || version == from && verdict.PredicateID != predicateID {
			return false
		}
		if version == to {
			break
		}
		left, leftOK := history[version][predicateID]
		right, rightOK := history[version+1][predicateID]
		if !leftOK || !rightOK || left.OutcomeKind != right.OutcomeKind {
			return false
		}
		leftPayload, leftErr := canonicalJSON(json.RawMessage(left.OutcomePayload))
		rightPayload, rightErr := canonicalJSON(json.RawMessage(right.OutcomePayload))
		if leftErr != nil || rightErr != nil || string(leftPayload) != string(rightPayload) {
			return false
		}
	}
	approved := history[to][predicateID]
	return approved.PredicateID == predicateID && approved.OutcomeKind != "" && verdict.PredicateID == predicateID
}

type workflowHealthVerdict struct {
	seq     int64
	verdict historicalWorkflowVerdict
}

type workflowHealthBasis struct {
	version int64
	history workflowContractPredicateHistoryData
}

func loadWorkflowHealthBasis(ctx context.Context, q queryer, workID string, version int64) (workflowHealthBasis, error) {
	basis := workflowHealthBasis{version: version}
	var err error
	if basis.history, err = workflowContractPredicateHistory(ctx, q, workID, version); err != nil {
		return basis, err
	}
	return basis, nil
}

func (basis workflowHealthBasis) healthySequence(candidates []workflowHealthVerdict) int64 {
	predicates := basis.history[basis.version]
	if len(predicates) == 0 {
		return 0
	}
	latest := make(map[string]workflowHealthVerdict, len(predicates))
	for _, candidate := range candidates {
		v := candidate.verdict
		if _, active := predicates[v.PredicateID]; !active || v.ContractVersion <= 0 || v.ContractVersion > basis.version {
			continue
		}
		if v.ContractVersion < basis.version && !workflowPredicateHistoryCompatible(basis.history, v.ContractVersion, basis.version, v.PredicateID, v) {
			continue
		}
		if prior, exists := latest[v.PredicateID]; !exists || candidate.seq > prior.seq {
			latest[v.PredicateID] = candidate
		}
	}
	if len(latest) != len(predicates) {
		return 0
	}
	var seq int64
	for _, candidate := range latest {
		if candidate.verdict.VerdictKind != "ok" || candidate.verdict.IncomparableWithApproved {
			return 0
		}
		seq = max(seq, candidate.seq)
	}
	return seq
}

func readWorkflowHealthVerdicts(rows *sql.Rows) ([]workflowHealthVerdict, error) {
	defer rows.Close()
	var result []workflowHealthVerdict
	for rows.Next() {
		var candidate workflowHealthVerdict
		var raw string
		if err := rows.Scan(&candidate.seq, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &candidate.verdict); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

func backfillWorkflowHealthySequences(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT c.work_id,c.contract_version,c.supersession_seq,COALESCE(s.supersession_seq,0) FROM workflow_contracts c LEFT JOIN workflow_contracts s ON s.work_id=c.work_id AND s.contract_version=c.superseded_by ORDER BY c.work_id,c.contract_version`)
	if err != nil {
		return err
	}
	type contractWindow struct {
		workID                    string
		version, opening, closing int64
	}
	var windows []contractWindow
	for rows.Next() {
		var window contractWindow
		if err := rows.Scan(&window.workID, &window.version, &window.opening, &window.closing); err != nil {
			rows.Close()
			return err
		}
		windows = append(windows, window)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO fold_guard(active) VALUES(1)`); err != nil {
		return err
	}
	for _, window := range windows {
		basis, err := loadWorkflowHealthBasis(ctx, q, window.workID, window.version)
		if err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT seq,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind='workflow.verdict_recorded' AND (?=0 OR seq<?) ORDER BY seq`, window.workID, window.closing, window.closing)
		if err != nil {
			return err
		}
		verdicts, err := readWorkflowHealthVerdicts(rows)
		if err != nil {
			return err
		}
		healthy := basis.replayHealthySequence(verdicts, window.opening)
		if _, err := q.ExecContext(ctx, `UPDATE workflow_contracts SET last_healthy_seq=? WHERE work_id=? AND contract_version=?`, healthy, window.workID, window.version); err != nil {
			return err
		}
	}
	_, err = q.ExecContext(ctx, `DELETE FROM fold_guard`)
	return err
}

func (basis workflowHealthBasis) replayHealthySequence(verdicts []workflowHealthVerdict, opening int64) int64 {
	var current []workflowHealthVerdict
	var healthy int64
	opened := false
	for _, candidate := range verdicts {
		if !opened && candidate.seq > opening {
			healthy = basis.healthySequence(current)
			opened = true
		}
		current = replaceWorkflowHealthVerdict(current, candidate)
		if opened {
			if seq := basis.healthySequence(current); seq > 0 {
				healthy = seq
			}
		}
	}
	if !opened {
		healthy = basis.healthySequence(current)
	}
	return healthy
}

func replaceWorkflowHealthVerdict(current []workflowHealthVerdict, candidate workflowHealthVerdict) []workflowHealthVerdict {
	for i, prior := range current {
		if prior.verdict.ContractVersion == candidate.verdict.ContractVersion && prior.verdict.PredicateID == candidate.verdict.PredicateID {
			current[i] = candidate
			return current
		}
	}
	return append(current, candidate)
}
