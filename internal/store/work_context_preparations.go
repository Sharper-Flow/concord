package store

import (
	"context"
	"encoding/json"
	"strings"
)

// Context advertises the latest qualified native preparation for each control
// on the current subject. A superseded preparation remains explicitly readable
// by its immutable run reference but does not replace the current selection.
func readSelectedOraclePreparations(ctx context.Context, q queryer, work string) ([]NativeOraclePreparation, error) {
	selected := []NativeOraclePreparation{}
	subject, err := readCurrentOracleSubjectReceipt(ctx, q, work)
	if err != nil || subject == nil {
		return selected, err
	}
	commit := strings.TrimPrefix(subject.SubjectRef, "commit:")
	version, err := activeWorkflowContractVersion(ctx, q, work, "work_context_read")
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT json_extract(l.result_json,'$.operation_ref')
 FROM worktree_verify_leases l
 WHERE l.work_id=? AND l.project_id=? AND l.state='released' AND l.outcome='completed'
 AND json_extract(l.result_json,'$.oracle.phase')='prepare' AND json_extract(l.result_json,'$.oracle.qualification')='ready'
 AND json_extract(l.result_json,'$.oracle.subject_commit')=? AND json_extract(l.result_json,'$.oracle.contract_version')=?
 AND l.rowid=(SELECT MAX(latest.rowid) FROM worktree_verify_leases latest
 WHERE latest.work_id=l.work_id AND latest.project_id=l.project_id AND latest.state='released' AND latest.outcome='completed'
 AND json_extract(latest.result_json,'$.oracle.phase')='prepare' AND json_extract(latest.result_json,'$.oracle.qualification')='ready'
 AND json_extract(latest.result_json,'$.oracle.subject_commit')=? AND json_extract(latest.result_json,'$.oracle.contract_version')=?
 AND json_extract(latest.native_plan_json,'$.bundle.owner.owner_id')=json_extract(l.native_plan_json,'$.bundle.owner.owner_id')
 AND json_extract(latest.native_plan_json,'$.bundle.control.control_id')=json_extract(l.native_plan_json,'$.bundle.control.control_id'))
 ORDER BY json_extract(l.native_plan_json,'$.bundle.owner.owner_id'),json_extract(l.native_plan_json,'$.bundle.control.control_id')`, work, subject.ProjectID, commit, version, commit, version)
	if err != nil {
		return nil, err
	}
	refs := []string{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, ref := range refs {
		preparation, err := readOraclePreparationReceiptTx(ctx, q, work, subject.ProjectID, ref)
		if err != nil {
			return nil, err
		}
		if preparation == nil {
			return nil, workContextFailure(KindInvariantViolation, "work_context_read", "selected native preparation is unavailable", "restore the retained preparation and producer receipt")
		}
		selected = append(selected, *preparation)
		raw, err := json.Marshal(selected)
		if err != nil {
			return nil, err
		}
		if len(raw) > 32*1024 {
			return nil, workContextFailure(KindLimitExceeded, "work_context_read", "selected preparation metadata exceeds 32768 bytes", workContextOverflowRecovery())
		}
	}
	return selected, nil
}
