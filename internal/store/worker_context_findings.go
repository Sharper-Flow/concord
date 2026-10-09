package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// CON-887 typed terminal-report retention. A worker may report typed context
// findings on a terminal report; Concord retains them as worker-claimed
// content. A finding is a claim, never acceptance: it joins no obligation
// vocabulary, discharges no predicate, and records no verdict or transition.
// subject_ref is the worker's claim about what a finding concerns — a path, a
// symbol, a command, or another bounded reference — and never carries
// dispatch-owned subject identity.

const (
	// WorkerContextFindingsMaxCount is the closed entry bound of the
	// optional context_findings array.
	WorkerContextFindingsMaxCount = 16
	// WorkerContextFindingsMaxArrayBytes bounds the compact JSON
	// serialization of the whole context_findings array in UTF-8 bytes,
	// matching x-maxArrayBytes on contracts/agent-lane-report.schema.json.
	// An array past the bound is refused whole; a finding is never
	// truncated to fit.
	WorkerContextFindingsMaxArrayBytes = 16384
)

// Per-entry byte bounds, matching $defs/context_finding of
// contracts/agent-lane-report.schema.json.
const (
	workerContextFindingStatementMaxBytes   = 1024
	workerContextFindingSubjectRefMaxBytes  = 128
	workerContextFindingEvidenceRefMaxCount = 8
	workerContextFindingEvidenceRefMaxBytes = 256
)

// workerContextFindingKindVocabulary is the closed kind set a finding may
// declare, mirroring the report contract's enum.
var workerContextFindingKindVocabulary = map[string]bool{
	"observation": true, "inference": true, "hypothesis": true, "rejected_approach": true,
	"open_question": true, "contradiction": true, "direction": true,
}

// WorkerContextFinding is one typed context finding a worker reported on its
// terminal report (CON-887), and the shared wire the record_work_context
// action declaration reuses. The four report fields are required by the
// closed report shape: EvidenceRefs is present as an array that may be
// empty, which the struct keeps distinct from an absent (nil) field.
//
// DomainID is required on every finding (CON-892) and ProductWideRationale
// is legal only on the registry's root Domain. The closed entry shape here
// bounds both; registry and affected-scope validation is tx-scoped in
// ValidateWorkContextDomainTx, which the action constructor and the live
// terminal folds share.
type WorkerContextFinding struct {
	Kind                 string   `json:"kind"`
	Statement            string   `json:"statement"`
	SubjectRef           string   `json:"subject_ref"`
	EvidenceRefs         []string `json:"evidence_refs"`
	DomainID             string   `json:"domain_id"`
	ProductWideRationale string   `json:"product_wide_rationale,omitempty"`
}

// validateWorkerContextFindingEntry enforces one finding's closed entry
// shape, shared by the report aggregate bound and the action declaration.
// The Domain fields carry only byte bounds here; registry and
// affected-scope validation is tx-scoped and lives in
// ValidateWorkContextDomainTx.
func validateWorkerContextFindingEntry(finding WorkerContextFinding) error {
	if !workerContextFindingKindVocabulary[finding.Kind] {
		return invalidWorkerPayload("worker context_finding kind must be observation, inference, hypothesis, rejected_approach, open_question, contradiction, or direction")
	}
	if len(finding.Statement) < 1 || len(finding.Statement) > workerContextFindingStatementMaxBytes {
		return invalidWorkerPayload("worker context_finding statement must be between 1 and 1024 UTF-8 bytes")
	}
	if len(finding.SubjectRef) < 1 || len(finding.SubjectRef) > workerContextFindingSubjectRefMaxBytes {
		return invalidWorkerPayload("worker context_finding subject_ref must be between 1 and 128 UTF-8 bytes")
	}
	// All four report fields are required: a missing or null evidence_refs
	// array is not the report shape, an empty one is.
	if finding.EvidenceRefs == nil {
		return invalidWorkerPayload("worker context_finding must carry an evidence_refs array")
	}
	if len(finding.EvidenceRefs) > workerContextFindingEvidenceRefMaxCount {
		return invalidWorkerPayload("worker context_finding must carry at most 8 evidence refs")
	}
	for _, ref := range finding.EvidenceRefs {
		if len(ref) < 1 || len(ref) > workerContextFindingEvidenceRefMaxBytes {
			return invalidWorkerPayload("worker context_finding evidence ref must be between 1 and 256 UTF-8 bytes")
		}
	}
	if len(finding.DomainID) < 1 || len(finding.DomainID) > 256 {
		return invalidWorkerPayload("worker context_finding domain_id must be between 1 and 256 UTF-8 bytes")
	}
	if len(finding.ProductWideRationale) > 512 {
		return invalidWorkerPayload("worker context_finding product_wide_rationale must be at most 512 UTF-8 bytes")
	}
	return nil
}

// ValidateWorkerContextFindings enforces the closed entry shape and the
// aggregate bound of one terminal report's optional context_findings array.
// Nil (absent) and empty are both legal on every admitted status; an
// over-bound array is refused whole and never truncated. The aggregate
// measures the compact JSON serialization the store itself persists, so the
// durable event payload never exceeds the contract bound.
func ValidateWorkerContextFindings(findings []WorkerContextFinding) error {
	if len(findings) > WorkerContextFindingsMaxCount {
		return invalidWorkerPayload("worker context_findings must carry at most 16 entries")
	}
	for _, finding := range findings {
		if err := validateWorkerContextFindingEntry(finding); err != nil {
			return err
		}
	}
	if findings == nil {
		return nil
	}
	encoded, err := json.Marshal(findings)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "validate_worker_event", "cannot measure the worker context_findings serialization", false, "repair the worker event payload", err)
	}
	if len(encoded) > WorkerContextFindingsMaxArrayBytes {
		return invalidWorkerPayload("worker context_findings serialize past the 16384-byte aggregate bound and are refused, never truncated")
	}
	return nil
}

// validateWorkerContextFindingDomainsTx validates every reported finding's
// Domain against the current registry and the approved affected scope on the
// live terminal fold. Replay folds the retained report without consulting
// today's registry: the live fold already admitted it.
func validateWorkerContextFindingDomainsTx(ctx context.Context, tx *sql.Tx, workID string, findings []WorkerContextFinding) error {
	if isWorkflowReplay(ctx) {
		return nil
	}
	for _, finding := range findings {
		if err := ValidateWorkContextDomainTx(ctx, tx, workID, finding.DomainID, finding.ProductWideRationale); err != nil {
			return err
		}
	}
	return nil
}
