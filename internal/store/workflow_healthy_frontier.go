package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

type workflowHealthVerdict struct {
	seq     int64
	verdict workflowVerdictRecordedPayload
}

type workflowHealthBasis struct {
	version int64
	cutoff  int64
	history workflowContractPredicateHistoryData
	pins    workflowContractDefinitionPinsData
}

func loadWorkflowHealthBasis(ctx context.Context, q queryer, workID string, version int64) (workflowHealthBasis, error) {
	basis := workflowHealthBasis{version: version}
	var err error
	if basis.history, err = workflowContractPredicateHistory(ctx, q, workID, version); err != nil {
		return basis, err
	}
	if basis.pins, err = workflowContractDefinitionPins(ctx, q, workID, version); err != nil {
		return basis, err
	}
	basis.cutoff, err = workflowCompleteStepCorrectionEvidenceCutoff(ctx, q, workID, version)
	return basis, err
}

func (basis workflowHealthBasis) healthySequence(candidates []workflowHealthVerdict) int64 {
	predicates := basis.history[basis.version]
	if len(predicates) == 0 {
		return 0
	}
	latest := make(map[string]workflowHealthVerdict, len(predicates))
	for _, candidate := range candidates {
		v := candidate.verdict
		if _, active := predicates[v.PredicateID]; !active || v.ContractVersion <= 0 || v.ContractVersion > basis.version || candidate.seq <= basis.cutoff {
			continue
		}
		if v.ContractVersion < basis.version && (!workflowPredicateHistoryCompatible(basis.history, v.ContractVersion, basis.version, v.PredicateID, v) || !workflowContractDefinitionPinsCompatible(basis.pins, v.ContractVersion, basis.version)) {
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

// A verdict advances health only when the whole current compatible set is
// healthy. A contract change instead recomputes that set, including its cutoff.
func refreshWorkflowHealthySequence(ctx context.Context, q migrationRunner, workID string, version int64, contractChanged bool) error {
	basis, err := loadWorkflowHealthBasis(ctx, q, workID, version)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT verdict_seq,payload FROM workflow_predicate_verdicts WHERE work_id=?`, workID)
	if err != nil {
		return workflowProjectionError(err, "cannot read the current healthy-verdict set")
	}
	candidates, err := readWorkflowHealthVerdicts(rows)
	if err != nil {
		return workflowProjectionError(err, "cannot decode the current healthy-verdict set")
	}
	seq := basis.healthySequence(candidates)
	if seq == 0 && !contractChanged {
		return nil
	}
	_, err = q.ExecContext(ctx, `UPDATE workflow_contracts SET last_healthy_seq=? WHERE work_id=? AND contract_version=?`, seq, workID, version)
	return workflowProjectionError(err, "cannot record the healthy-verdict frontier")
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
		var current []workflowHealthVerdict
		var healthy int64
		opened := false
		for _, candidate := range verdicts {
			if !opened && candidate.seq > window.opening {
				healthy = basis.healthySequence(current)
				opened = true
			}
			found := false
			for i, prior := range current {
				if prior.verdict.ContractVersion == candidate.verdict.ContractVersion && prior.verdict.PredicateID == candidate.verdict.PredicateID {
					current[i] = candidate
					found = true
					break
				}
			}
			if !found {
				current = append(current, candidate)
			}
			if opened {
				if seq := basis.healthySequence(current); seq > 0 {
					healthy = seq
				}
			}
		}
		if !opened {
			healthy = basis.healthySequence(current)
		}
		if _, err := q.ExecContext(ctx, `UPDATE workflow_contracts SET last_healthy_seq=? WHERE work_id=? AND contract_version=?`, healthy, window.workID, window.version); err != nil {
			return err
		}
	}
	_, err = q.ExecContext(ctx, `DELETE FROM fold_guard`)
	return err
}
