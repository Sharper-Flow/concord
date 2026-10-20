package store

import (
	"context"
	"encoding/json"
	"slices"
)

// workflowFootprintProofBasis is what a footprint receipt must match: the
// work, the Project whose worktree ran the check, the accepted candidate
// commit, and the event-order frontier at or after which the run must start.
// The frontier is the work item's domain-event sequence of its active
// contract approval or latest repair; a run whose lease watermark is below it
// started before that event and proves nothing.
type workflowFootprintProofBasis struct {
	WorkID    string
	ProjectID string
	Commit    string
	Frontier  int64
	// BeforeSeq is the exclusive event-prefix boundary for a fold. Zero
	// reads live state; a later binding cannot qualify an earlier boundary.
	BeforeSeq int64
}

// workflowFootprintProof is one footprint check's proof state. Supported is
// false when the check has no derivable verify argv. OperationRef names the
// proving verify run and is empty when no qualified receipt proves the check.
// PrincipalRef is that run's authenticated producer principal; which
// principal the proof requires is the admission owner's decision.
type workflowFootprintProof struct {
	Check        FootprintCheck
	Supported    bool
	OperationRef string
	PrincipalRef string
}

// workflowFootprintReceiptSQL narrows the qualified verify receipts to one
// exact command on one Project and commit, started at or after the frontier.
// A lease predating the watermark column carries NULL, which compares to no
// frontier and never qualifies.
const workflowFootprintReceiptSQL = `SELECT l.result_json, l.principal_ref` + oracleVerifyReceiptJoinSQL + `
	AND l.project_id=? AND l.command_json=? AND json_extract(l.result_json,'$.subject_ref')=?
	AND l.event_watermark>=?
	AND EXISTS (SELECT 1 FROM domain_events bound
		WHERE bound.subject_type='work_item' AND bound.subject_id=l.work_id AND bound.kind=?
		AND json_extract(bound.payload,'$.evidence_kind')='verification'
		AND json_extract(bound.payload,'$.producer_run_ref')=d.op_id
		AND json_extract(bound.payload,'$.producer_id')=d.principal_ref
		AND json_extract(bound.payload,'$.producer_watermark')=d.request_id
		AND EXISTS (SELECT 1 FROM json_each(d.evidence_refs)
			WHERE value=json_extract(bound.payload,'$.immutable_subject_ref'))
		AND bound.seq>? AND (?=0 OR bound.seq<?))
	ORDER BY d.rowid DESC`

// readWorkflowFootprintProofs resolves each check against the work's
// qualified verify receipts. It performs SQL only, so the caller's queryer
// may be its transaction.
func readWorkflowFootprintProofs(ctx context.Context, q queryer, basis workflowFootprintProofBasis, checks []FootprintCheck) ([]workflowFootprintProof, error) {
	if !worktreeSHAPattern.MatchString(basis.Commit) || basis.Frontier <= 0 || basis.BeforeSeq < 0 {
		return nil, newFailure(KindInvariantViolation, "footprint_receipt_read", "the footprint proof basis has no valid commit, frontier or event prefix", false, "resolve the accepted candidate commit and its contract frontier first")
	}
	proofs := make([]workflowFootprintProof, 0, len(checks))
	for _, check := range checks {
		proof := workflowFootprintProof{Check: check}
		if command, ok := workflowFootprintCheckCommand(check); ok {
			proof.Supported = true
			var err error
			if proof.OperationRef, proof.PrincipalRef, err = readWorkflowFootprintReceipt(ctx, q, basis, check, command); err != nil {
				return nil, err
			}
		}
		proofs = append(proofs, proof)
	}
	return proofs, nil
}

func readWorkflowFootprintReceipt(ctx context.Context, q queryer, basis workflowFootprintProofBasis, check FootprintCheck, command []string) (string, string, error) {
	// The lease stores json.Marshal of its argv; the same encoding matches it byte for byte.
	commandJSON, err := json.Marshal(command)
	if err != nil {
		return "", "", wrapFailure(KindInvariantViolation, "footprint_receipt_read", "cannot encode the derived check command", false, "repair the footprint check", err)
	}
	rows, err := q.QueryContext(ctx, workflowFootprintReceiptSQL, basis.WorkID, basis.ProjectID, string(commandJSON), "commit:"+basis.Commit, basis.Frontier, WorkflowEvidenceBound, basis.Frontier, basis.BeforeSeq, basis.BeforeSeq)
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "footprint_receipt_read", "cannot read the footprint verify receipts", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw, principal string
		if err := rows.Scan(&raw, &principal); err != nil {
			return "", "", wrapFailure(KindUnavailable, "footprint_receipt_read", "cannot read a footprint verify receipt", true, "retry once the database is readable", err)
		}
		result, err := decodeOracleVerifyReceipt(raw, basis.WorkID)
		if err != nil {
			return "", "", err
		}
		if workflowReceiptProvesCheck(check, command, result) {
			return result.OperationRef, principal, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", wrapFailure(KindUnavailable, "footprint_receipt_read", "cannot read the footprint verify receipts", true, "retry once the database is readable", err)
	}
	return "", "", nil
}

// workflowReceiptProvesCheck decides one green receipt of the exact derived
// command. A validator or generated check is proved by that run's success. A
// Go check is never proved by exit status alone: it needs the complete native
// observation, bound to the receipt's own commit by an unchanged source
// closure, of exactly the one selected package. A named test must be among
// that package's observed top-level passes. A package proves by a terminal
// pass with at least one passed test, or by go test's no-test-files skip,
// which still builds the package; a pass where every test skipped is
// zero-match and proves nothing.
func workflowReceiptProvesCheck(check FootprintCheck, command []string, result *WorktreeVerifyResult) bool {
	if !slices.Equal(result.Command, command) {
		return false
	}
	if check.Kind != "go_test" && check.Kind != "go_package" {
		return true
	}
	suite := result.GoSuite
	if suite == nil || !suite.Complete || suite.Protocol != "native_go_suite_v1" || !slices.Equal(suite.Command, command) ||
		suite.Closure == nil || !suite.Closure.Complete || suite.Closure.SubjectRef != result.SubjectRef || len(suite.Packages) != 1 {
		return false
	}
	observed := suite.Packages[0]
	if check.Kind == "go_package" {
		pkg, _ := workflowPackageCheckPackage(check.Value)
		return observed.Package == suite.Closure.ModulePath+"/"+pkg &&
			(observed.Passed && observed.PassedCount > 0 || !observed.Passed && observed.PassedCount == 0 && observed.SkippedCount == 0)
	}
	pkg, name, _ := workflowTestCheckParts(check.Value)
	return observed.Package == suite.Closure.ModulePath+"/"+pkg && observed.Passed && slices.Contains(observed.ObservedTestNames, name)
}
