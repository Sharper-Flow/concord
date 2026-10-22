package store

import (
	"context"
	"database/sql"
)

// A matched native report refers to the same retained command producer as a
// direct run locator. Subject derivation and report validation share that join.
func readMatchedOracleControlProducerTx(ctx context.Context, q queryer, work string, receipt *WorkerOracleReceipt, currentSubject string) (*WorktreeVerifyResult, error) {
	var evidenceRef string
	status := "healthy"
	if receipt.Result == OracleReceiptResultFail {
		status = "failed"
	}
	err := q.QueryRowContext(ctx, `SELECT n.evidence_ref FROM workflow_native_runs n JOIN external_observations o ON o.observation_id=n.observation_id AND o.work_id=n.work_id
		WHERE n.work_id=? AND n.run_id=? AND n.phase='health' AND n.status=? AND n.native_subject_ref=? AND n.verification_state=?
		AND o.subject_kind='native_run' AND o.subject_ref=n.native_subject_ref AND o.subject_digest=n.subject_digest
		AND o.verification_state=? AND o.verification_result=?`, work, receipt.RunRef, status, "commit:"+currentSubject, string(VerificationVerified), string(VerificationVerified), string(VerificationMatched)).Scan(&evidenceRef)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_oracle_findings", "cannot read the receipt's verified native producer", true, "retry once the projection is readable", err)
	}
	return readOracleControlProducerTx(ctx, q, work, evidenceRef, receipt.Result)
}
