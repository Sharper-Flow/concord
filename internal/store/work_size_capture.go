package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Both capture writers size the work they create through this core
// (CD-0229 D4): concord_work_define capture and the concord_work_start
// bootstrap. PlanWorkSize runs before the capture transaction, because the
// behavior match probes git; RecordWorkSizeTx runs inside it, after the work
// item and its memberships fold, and reads only through the transaction.

// WorkSizeCaptureInput is a capture's optional size_inputs: what the change
// will touch, plus the inputs only the agent can supply. It carries no size;
// the model decides it, and the strict decoders refuse a size field.
type WorkSizeCaptureInput struct {
	Paths                    []string `json:"paths,omitempty"`
	Behavior                 string   `json:"behavior,omitempty"`
	Security                 *bool    `json:"security"`
	UserVisible              *bool    `json:"user_visible"`
	BlastRadius              string   `json:"blast_radius"`
	EstimatedProductionLines int      `json:"estimated_production_lines"`
}

// WorkSizePlan is the validated size request and the part of its footprint
// read that must run outside the capture transaction.
type WorkSizePlan struct {
	declared  WorkSizeDeclared
	footprint preparedDomainFootprint
}

const (
	workSizeHomeRecovery  = "omit size_inputs, or designate and index the knowledge home"
	workSizeStaleRecovery = "retry the capture"
)

// PlanWorkSize validates size_inputs and resolves the Product's knowledge
// home and behavior match. Every refusal happens before any capture effect.
func (s *Store) PlanWorkSize(ctx context.Context, productID string, in WorkSizeCaptureInput) (*WorkSizePlan, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "work_size", "store is not open", false, "open the authority database")
	}
	if in.Security == nil || in.UserVisible == nil {
		return nil, newFailure(KindInvalidInput, "work_size", "size_inputs requires security and user_visible", false, "declare both booleans")
	}
	declared := WorkSizeDeclared{Security: *in.Security, UserVisible: *in.UserVisible, BlastRadius: in.BlastRadius, EstimatedProductionLines: in.EstimatedProductionLines}
	if err := validateWorkSizeDeclared(declared); err != nil {
		return nil, err
	}
	prepared, err := s.prepareDomainFootprint(ctx, DomainFootprintRequest{Product: productID, Paths: in.Paths, Behavior: in.Behavior})
	if err != nil {
		return nil, workSizeFootprintRefusal(err)
	}
	return &WorkSizePlan{declared: declared, footprint: prepared}, nil
}

// workSizeFootprintRefusal keeps the footprint read's kind and replaces its
// recovery with the one a capture can act on.
func workSizeFootprintRefusal(err error) error {
	var failure *Failure
	if !errors.As(err, &failure) {
		return err
	}
	switch failure.Kind {
	case KindUnknownScope, KindAmbiguousScope, KindIndexDegraded, KindDomainRegistryAbsent:
		refusal := *failure
		refusal.RecoveryAction = workSizeHomeRecovery
		return &refusal
	case KindStaleContext:
		refusal := *failure
		refusal.RecoveryAction = workSizeStaleRecovery
		return &refusal
	}
	return err
}

// RecordWorkSizeTx reads the footprint, the work's kind, stage, and
// memberships inside the capture transaction, decides the size, and appends
// work.sized. It does not change the work item's version.
func RecordWorkSizeTx(ctx context.Context, transaction *Transaction, plan *WorkSizePlan, workID, eventID, actor string, now time.Time) ([]string, error) {
	tx, err := transactionSQL(transaction, "work_size")
	if err != nil {
		return nil, err
	}
	payload, err := workSizedPayloadTx(ctx, tx, plan, workID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, wrapFailure(KindInvalidPayload, "work_size", "cannot encode the size record", false, "repair the size inputs", err)
	}
	if len(encoded) > workSizedPayloadMaxBytes {
		return nil, newFailure(KindLimitExceeded, "work_size", fmt.Sprintf("the complete footprint makes the size record %d bytes; the limit is %d", len(encoded), workSizedPayloadMaxBytes), false, "name fewer paths or a narrower behavior")
	}
	result, err := ApplyOperationTx(ctx, transaction, Operation{Events: []Event{
		{EventID: eventID, Kind: WorkSized, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor, OccurredAt: now, PayloadVersion: 2, Payload: encoded},
	}})
	if err != nil {
		return nil, err
	}
	return result.EventIDs, nil
}

// WorkSizeRerunRequest is the agent's rerun of the size model: fresh
// declared inputs, with every other input re-derived (CD-0229 D4, D6).
type WorkSizeRerunRequest struct {
	Plan    *WorkSizePlan
	WorkID  string
	EventID string
	Actor   WorkflowActor
	Now     time.Time
}

// RerunWorkSizeTx records a new model decision on the same work item.
func RerunWorkSizeTx(ctx context.Context, transaction *Transaction, request WorkSizeRerunRequest) (WorkSizeRevisionResult, error) {
	tx, err := transactionSQL(transaction, "work_size")
	if err != nil {
		return WorkSizeRevisionResult{}, err
	}
	actorRef, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return WorkSizeRevisionResult{}, err
	}
	if request.Now.IsZero() {
		request.Now = transaction.now()
	}
	ids, err := RecordWorkSizeTx(ctx, transaction, request.Plan, request.WorkID, request.EventID, actorRef, request.Now)
	if err != nil {
		return WorkSizeRevisionResult{}, err
	}
	return workSizeRevisionResultTx(ctx, tx, request.WorkID, ids)
}

// WorkSizeRevisionResult reports the events and version of an in-place revision.
type WorkSizeRevisionResult struct {
	EventIDs    []string
	WorkVersion int64
}

func workSizeRevisionResultTx(ctx context.Context, q queryer, workID string, ids []string) (WorkSizeRevisionResult, error) {
	result := WorkSizeRevisionResult{EventIDs: ids}
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&result.WorkVersion); err != nil {
		return WorkSizeRevisionResult{}, wrapFailure(KindUnavailable, "work_size", "cannot read the revised work item's version", true, "retry once the database is readable", err)
	}
	return result, nil
}

func workSizedPayloadTx(ctx context.Context, q queryer, plan *WorkSizePlan, workID string) (workSizedPayload, error) {
	var out workSizedPayload
	footprint, err := plan.footprint.completeTx(ctx, q)
	if err != nil {
		return out, workSizeFootprintRefusal(err)
	}
	inputs := WorkSizeInputs{Declared: plan.declared, PathCount: len(footprint.Paths), DomainCount: len(footprint.Domains)}
	inputs.PackageCount, inputs.UnmappedPathCount = footprintPathCounts(footprint.Paths)
	err = q.QueryRowContext(ctx, `SELECT w.kind,
		coalesce(p.stage_maturity_override, pr.stage_maturity),
		coalesce(p.stage_audience_commitment_override, pr.stage_audience_commitment),
		(SELECT count(DISTINCT project_id) FROM work_projects WHERE work_id=w.id)
		FROM work_items w
		JOIN work_projects wp ON wp.work_id=w.id AND wp.role='primary'
		JOIN projects p ON p.id=wp.project_id
		JOIN products pr ON pr.id=?
		WHERE w.id=?`, plan.footprint.req.Product, workID).
		Scan(&inputs.Kind, &inputs.Maturity, &inputs.Audience, &inputs.RepositoryCount)
	if err == sql.ErrNoRows {
		return out, newFailure(KindProjectionNotFound, "work_size", "the sized work item has no primary Project under the sizing Product", false, "size work after its memberships fold")
	} else if err != nil {
		return out, wrapFailure(KindUnavailable, "work_size", "cannot read the sized work item's stage", true, "retry once the database is readable", err)
	}
	basis, err := workSizeReviewBasisTx(ctx, q, workID)
	if err != nil {
		return out, err
	}
	size, err := DecideWorkSize(WorkSizeModelVersion, inputs, basis)
	if err != nil {
		return out, err
	}
	domainIDs := make([]string, 0, len(footprint.Domains))
	for _, domain := range footprint.Domains {
		domainIDs = append(domainIDs, domain.DomainID)
	}
	behaviorLaws := append([]string{}, plan.footprint.behaviorLaws...)
	return workSizedPayload{ModelVersion: WorkSizeModelVersion, Size: size, Inputs: inputs, ReviewBasis: basis, Footprint: WorkSizeFootprint{
		KnowledgeCommit:       footprint.KnowledgeCommit,
		RegistryContentHash:   footprint.RegistryContentHash,
		PackageMapContentHash: footprint.PackageMapContentHash,
		Paths:                 footprint.Paths,
		DomainIDs:             domainIDs,
		Behavior:              plan.footprint.req.Behavior,
		BehaviorLawIDs:        behaviorLaws,
		Laws:                  sliceOrEmpty(footprint.Laws),
		PackageChecks:         sliceOrEmpty(footprint.PackageChecks),
		ChecksComplete:        true,
	}}, nil
}
