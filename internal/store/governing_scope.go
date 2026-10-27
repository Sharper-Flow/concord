package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
)

// GoverningRequirementScope binds the actual compared sets, not today's Project
// obligations substituted for a work item's unknown capture history.
type GoverningRequirementScope struct {
	Applicable []string `json:"applicable"`
	Declared   []string `json:"declared"`
}

func NewGoverningRequirementScope(applicable, declared []string) GoverningRequirementScope {
	canonical := func(refs []string) []string {
		out := append([]string{}, refs...)
		slices.Sort(out)
		return slices.Compact(out)
	}
	return GoverningRequirementScope{Applicable: canonical(applicable), Declared: canonical(declared)}
}

func (scope GoverningRequirementScope) Missing() []string {
	return MissingGoverningRequirements(scope.Applicable, scope.Declared)
}

func (scope GoverningRequirementScope) Equal(other GoverningRequirementScope) bool {
	return slices.Equal(scope.Applicable, other.Applicable) && slices.Equal(scope.Declared, other.Declared)
}

func (scope GoverningRequirementScope) Digest() string {
	raw, _ := json.Marshal(scope)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

func validateDeclaredGoverningRequirements(refs []string) error {
	if len(refs) > 32 {
		return newFailure(KindInvalidPayload, "capture", "governing requirements exceed the capture bound", false, "supply at most 32 governing requirements")
	}
	for _, ref := range refs {
		if !workflowString(ref, 128) {
			return newFailure(KindInvalidPayload, "capture", "governing requirement is empty or exceeds the capture bound", false, "supply bounded governing requirement references")
		}
	}
	return nil
}

// ValidateCaptureGoverningRequirementsTx revalidates the exact planned sets and
// their registry resolution before the capture writes anything.
func ValidateCaptureGoverningRequirementsTx(ctx context.Context, transaction *Transaction, projectIDs []string, planned GoverningRequirementScope) error {
	tx, err := transactionSQL(transaction, "capture")
	if err != nil {
		return err
	}
	applicable, err := governingRequirementsForProjectIDs(ctx, tx, projectIDs)
	if err != nil {
		return err
	}
	current := NewGoverningRequirementScope(applicable, planned.Declared)
	if !current.Equal(planned) {
		return newFailure(KindVersionConflict, "capture", "governing requirements changed after capture preflight", false, "reread_entities")
	}
	products, err := productsForProjectIDs(ctx, tx, projectIDs)
	if err != nil {
		return err
	}
	var productIDs []string
	for _, ids := range products {
		productIDs = append(productIDs, ids...)
	}
	return validateCaptureGoverningRequirements(ctx, tx, productIDs, applicable, planned.Declared)
}

func overlapGoverningRequirementScope(ctx context.Context, q queryer, fromID, toID, kind string) (GoverningRequirementScope, error) {
	read := func(workID string) ([]string, error) {
		var raw string
		if err := q.QueryRowContext(ctx, `SELECT intent_json FROM work_items WHERE id=?`, workID).Scan(&raw); err != nil {
			if err == sql.ErrNoRows {
				return nil, newFailure(KindProjectionNotFound, "workflow_domain_overlap", "overlap work item does not exist: "+workID, false, "reread_entities")
			}
			return nil, wrapFailure(KindUnavailable, "workflow_domain_overlap", "cannot read captured governing requirements", true, "reread_entities", err)
		}
		var intent workIntentProjection
		if err := json.Unmarshal([]byte(raw), &intent); err != nil {
			return nil, wrapFailure(KindInvariantViolation, "workflow_domain_overlap", "captured governing requirements are unreadable", false, "provide_evidence", err)
		}
		if intent.GoverningRequirements == nil || *intent.GoverningRequirements == nil {
			return nil, newFailure(KindMissingEvidence, "workflow_domain_overlap", "work item "+workID+" has no declared governing requirement set", false, "provide_evidence")
		}
		if err := validateDeclaredGoverningRequirements(*intent.GoverningRequirements); err != nil {
			return nil, err
		}
		return *intent.GoverningRequirements, nil
	}
	terminatedID, retainedID := fromID, toID
	if kind == ResolutionSupersedes {
		terminatedID, retainedID = toID, fromID
	}
	applicable, err := read(terminatedID)
	if err != nil {
		return GoverningRequirementScope{}, err
	}
	declared, err := read(retainedID)
	if err != nil {
		return GoverningRequirementScope{}, err
	}
	return NewGoverningRequirementScope(applicable, declared), nil
}

func (s *Store) OverlapGoverningRequirementScope(ctx context.Context, fromID, toID, kind string) (scope GoverningRequirementScope, err error) {
	err = s.readSnapshot(ctx, func(q queryer) error {
		return readOverlapGoverningRequirementScope(ctx, q, fromID, toID, kind, &scope)
	})
	return scope, err
}

func readOverlapGoverningRequirementScope(ctx context.Context, q queryer, fromID, toID, kind string, scope *GoverningRequirementScope) error {
	current, err := overlapGoverningRequirementScope(ctx, q, fromID, toID, kind)
	if err == nil {
		*scope = current
	}
	return err
}

// checkOverlapGoverningScope re-derives the cut inside the effect transaction,
// including for SQL-only callers. Consent binds both sets as well as endpoints
// and versions; a supplied claim on retained scope keeps its existing admission.
func checkOverlapGoverningScope(ctx context.Context, tx *sql.Tx, request WorkflowDomainOverlapResolutionRequest) error {
	if request.ResolutionKind != ResolutionMergedInto && request.ResolutionKind != ResolutionSupersedes {
		return nil
	}
	scope, err := overlapGoverningRequirementScope(ctx, tx, request.FromWorkID, request.ToWorkID, request.ResolutionKind)
	if err != nil {
		return err
	}
	var approved struct {
		GoverningRequirements string `json:"governing_requirements"`
	}
	if request.ApprovalRef != "" {
		if err := json.Unmarshal([]byte(request.ApprovalScopeJSON), &approved); err != nil {
			return workflowOverlapApprovalUnauthorized("governing_requirements")
		}
		if approved.GoverningRequirements != "" && approved.GoverningRequirements != scope.Digest() {
			return workflowOverlapApprovalUnauthorized("governing_requirements")
		}
	}
	if len(scope.Missing()) == 0 {
		return nil
	}
	if request.ApprovalRef == "" {
		failure := newFailure(KindApprovalRequired, "workflow_domain_overlap", "overlap resolution cuts governing requirements", false, "request_approval")
		failure.Violations = scope.Missing()
		return failure
	}
	if approved.GoverningRequirements != scope.Digest() {
		return workflowOverlapApprovalUnauthorized("governing_requirements")
	}
	_, err = workflowOperatorFromConsumedApprovalTx(ctx, tx, workflowApprovalBinding{ApprovalRef: request.ApprovalRef, OperationDigest: request.ApprovalOperationDigest, ScopeJSON: request.ApprovalScopeJSON, VersionsJSON: request.ApprovalVersionsJSON, Consequence: request.ApprovalConsequence}, "workflow_domain_overlap")
	return err
}
