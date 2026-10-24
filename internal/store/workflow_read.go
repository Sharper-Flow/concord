package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// WorkflowReadRequest bounds the existing workflow trace/read projection. It
// is read-only: no condition is resolved and no warning is acknowledged while
// constructing the response.
type WorkflowReadRequest struct {
	WorkID string
	Limit  int
	// ContractVersion, when nonzero, selects one recorded contract version
	// instead of the active one. The contract, architecture binding, and
	// Product-truth flag then describe that version from its own pin; the
	// stale-law check and operator question belong to the active contract
	// and are omitted; the instance fields stay current. A version the work
	// never had refuses unknown_scope.
	ContractVersion int64
	// Now pins the read-time clock for await-health derivation; zero means
	// wall clock.
	Now time.Time
}

type WorkflowReadDefinition struct {
	Ref     string `json:"ref"`
	Version int64  `json:"version"`
	Digest  string `json:"digest"`
}

type WorkflowReadContract struct {
	Version             int64                        `json:"version"`
	Premise             string                       `json:"premise"`
	OutcomePredicates   []WorkflowReadPredicate      `json:"outcome_predicates"`
	RequiredEvidence    []string                     `json:"required_evidence"`
	RouteConventions    []string                     `json:"route_conventions"`
	SpecMandate         []string                     `json:"spec_mandate"`
	LawModifies         []string                     `json:"law_modifies"`
	LawRevisions        []WorkflowLawRevision        `json:"law_revisions"`
	RigorClass          string                       `json:"rigor_class"`
	ChangesProductTruth *bool                        `json:"changes_product_truth,omitempty"`
	ArchitectureBinding *WorkflowArchitectureBinding `json:"architecture_binding,omitempty"`
	SelfRepair          *WorkflowSelfRepair          `json:"self_repair,omitempty"`
	ApprovedAt          string                       `json:"approved_at,omitempty"`
	ApprovedBy          string                       `json:"approved_by,omitempty"`
	SupersededBy        *int64                       `json:"superseded_by,omitempty"`
	DefinitionPin       WorkflowContractPinRead      `json:"definition_pin,omitzero"`
	// DecisionDomains carries the contract's declared classification fact.
	// nil is historical absence (a NULL projection column), never engineering;
	// an explicit engineering contract reads as the empty set. omitzero drops
	// only the nil fact from the encoding.
	DecisionDomains []string `json:"decision_domains,omitzero"`
}

// WorkflowContractDefinitionPin is the workflow definition a contract version
// was approved under. Verified reports whether the registry still verifies it.
type WorkflowContractDefinitionPin struct {
	Ref      string `json:"ref"`
	Version  int64  `json:"version"`
	Digest   string `json:"digest"`
	Verified bool   `json:"verified"`
}

// WorkflowContractPinRead distinguishes a pin nobody read (absent from the
// JSON) from a contract recorded without one (null).
type WorkflowContractPinRead struct {
	Read bool
	Pin  *WorkflowContractDefinitionPin
}

func (p WorkflowContractPinRead) IsZero() bool { return !p.Read }

func (p WorkflowContractPinRead) MarshalJSON() ([]byte, error) { return json.Marshal(p.Pin) }

// readWorkflowContractVersion reads one recorded contract version and every
// enrichment keyed by it. It answers sql.ErrNoRows when the work never had
// that version. A verified pin answers changes_product_truth; the active
// read overwrites it with the instance's flag.
func readWorkflowContractVersion(ctx context.Context, q queryer, workID string, version int64) (WorkflowReadContract, error) {
	var contract WorkflowReadContract
	var required, routes, mandates, modifies string
	var supersededBy sql.NullInt64
	var decisionDomains sql.NullString
	var pin WorkflowContractDefinitionPin
	err := q.QueryRowContext(ctx, `SELECT contract_version,premise,required_evidence,route_conventions,spec_mandate,law_modifies,rigor_class,decision_domains,approved_at,approved_by,superseded_by,definition_ref,definition_version,definition_digest FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, version).Scan(&contract.Version, &contract.Premise, &required, &routes, &mandates, &modifies, &contract.RigorClass, &decisionDomains, &contract.ApprovedAt, &contract.ApprovedBy, &supersededBy, &pin.Ref, &pin.Version, &pin.Digest)
	if err == sql.ErrNoRows {
		return contract, err
	}
	if err != nil {
		return contract, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow contract", true, "retry once the database is readable", err)
	}
	if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
		return contract, newFailure(KindInvariantViolation, "workflow_read", "workflow contract projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	contract.RequiredEvidence = nonNilStrings(contract.RequiredEvidence)
	contract.RouteConventions = nonNilStrings(contract.RouteConventions)
	contract.SpecMandate = nonNilStrings(contract.SpecMandate)
	contract.LawModifies = nonNilStrings(contract.LawModifies)
	// NULL is historical absence and stays nil; a recorded array decodes
	// as the declared set, where [] reads as engineering.
	if decisionDomains.Valid {
		if json.Unmarshal([]byte(decisionDomains.String), &contract.DecisionDomains) != nil {
			return contract, newFailure(KindInvariantViolation, "workflow_read", "workflow contract projection contains a malformed decision domain set", false, "restore a verified backup into a new path with concord restore, then swap")
		}
	}
	if supersededBy.Valid {
		contract.SupersededBy = &supersededBy.Int64
	}
	contract.DefinitionPin.Read = true
	if pin.Ref != "" {
		registered, verifyErr := verifyReadWorkflowDefinition(WorkflowReadDefinition{Ref: pin.Ref, Version: pin.Version, Digest: pin.Digest})
		pin.Verified = verifyErr == nil
		contract.DefinitionPin.Pin = &pin
		if pin.Verified {
			changes := registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth
			contract.ChangesProductTruth = &changes
		}
	}
	if contract.OutcomePredicates, err = readWorkflowContractPredicates(ctx, q, workID, contract.Version); err != nil {
		return contract, err
	}
	if contract.LawRevisions, err = readWorkflowLawRevisions(ctx, q, workID, contract.Version); err != nil {
		return contract, err
	}
	if contract.ArchitectureBinding, err = readWorkflowArchitectureBinding(ctx, q, workID, contract.Version); err != nil {
		return contract, err
	}
	if contract.SelfRepair, err = readWorkflowSelfRepair(ctx, q, workID, contract.Version); err != nil {
		return contract, err
	}
	return contract, nil
}

type WorkflowReadPredicate struct {
	PredicateID    string `json:"predicate_id"`
	Ordinal        int    `json:"ordinal"`
	OutcomeKind    string `json:"outcome_kind"`
	OutcomePayload string `json:"outcome_payload"`
}

type workflowPredicateQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readWorkflowContractPredicates(ctx context.Context, queryer workflowPredicateQueryer, workID string, contractVersion int64) ([]WorkflowReadPredicate, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? ORDER BY ordinal`, workID, contractVersion)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow contract predicates", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	result := []WorkflowReadPredicate{}
	for rows.Next() {
		var predicate WorkflowReadPredicate
		if err := rows.Scan(&predicate.PredicateID, &predicate.Ordinal, &predicate.OutcomeKind, &predicate.OutcomePayload); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow contract predicate", true, "retry once the database is readable", err)
		}
		result = append(result, predicate)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow contract predicates", true, "retry once the database is readable", err)
	}
	return result, nil
}

func verifyReadWorkflowDefinition(definition WorkflowReadDefinition) (RegisteredDefinition, error) {
	return resolveCurrentWorkflowDefinition(BuiltinWorkflowRegistry(), definition.Ref)
}

type WorkflowReadCondition struct {
	ID                  string `json:"id"`
	AwaitType           string `json:"await_type"`
	AwaitRef            string `json:"await_ref"`
	ResolutionAuthority string `json:"resolution_authority"`
	State               string `json:"state"`
	// ExpectedWithinSeconds is the declared wait bound (0 = none declared).
	// Overdue/Health are derived at read time against that bound:
	// an await beyond its bound reads overdue — unverified, never resolved.
	ExpectedWithinSeconds int64  `json:"expected_within_seconds"`
	AgeSeconds            int64  `json:"age_seconds"`
	Overdue               bool   `json:"overdue"`
	Health                string `json:"health"`
}

type WorkflowReadNotice struct {
	NoticeID              string  `json:"notice_id"`
	SourceWorkID          string  `json:"source_work_id"`
	SourceContractVersion int64   `json:"source_contract_version"`
	EntityKind            string  `json:"entity_kind"`
	EntityRef             string  `json:"entity_ref"`
	TargetWorkID          string  `json:"target_work_id"`
	EdgeOwnerWorkID       string  `json:"edge_owner_work_id"`
	EdgeID                string  `json:"edge_id"`
	OldHash               *string `json:"old_hash,omitempty"`
	NewHash               *string `json:"new_hash,omitempty"`
	Severity              string  `json:"severity"`
}

type WorkflowReadParkedDelivery struct {
	WorkID       string `json:"work_id"`
	StepID       string `json:"step_id"`
	ResumeAction string `json:"resume_action"`
	// ParkedSeconds is a read-time display age derived from the work item's
	// last update. Elapsed time holds no authority here, matching the
	// await-health boundary: an old park is labeled, never resolved by clock.
	ParkedSeconds int64 `json:"parked_seconds"`
	// Unreconciled marks a terminal instance that left the delivery gate
	// uncrossed: cancelled or superseded while a delivery was still owed.
	Unreconciled bool `json:"unreconciled"`
}

// WorkflowReadProjection is the bounded observation exposed through existing
// read surfaces. All values are derived from event-folded projections and the
// pinned definition; it is intentionally not an authority for mutation.
type WorkflowReadProjection struct {
	WorkID                   string                            `json:"work_id"`
	State                    string                            `json:"state"`
	CurrentStep              string                            `json:"current_step"`
	Definition               WorkflowReadDefinition            `json:"definition"`
	Contract                 *WorkflowReadContract             `json:"contract,omitempty"`
	OperatorQuestion         *WorkflowOperatorQuestion         `json:"operator_question,omitempty"`
	WithheldOperatorQuestion *WorkflowOperatorQuestionWithheld `json:"withheld_operator_question,omitempty"`
	CandidateIDs             []string                          `json:"candidate_ids"`
	Conditions               []WorkflowReadCondition           `json:"conditions"`
	UnresolvedConditions     []string                          `json:"unresolved_conditions"`
	// OverdueAwaits lists condition ids whose wait exceeded the declared
	// bound, derived at read time — the waiting/never-completable split.
	OverdueAwaits        []string                     `json:"overdue_awaits"`
	AwaitHealth          []WorkflowReadCondition      `json:"await_health"`
	UnreadableConditions []string                     `json:"unreadable_conditions"`
	Ready                bool                         `json:"ready"`
	BlockingConditions   []string                     `json:"blocking_conditions"`
	ImpactNotices        []WorkflowReadNotice         `json:"impact_notices"`
	CompletionWarnings   []string                     `json:"completion_warnings"`
	StaleLawRevision     *StaleLawRevision            `json:"stale_law_revision,omitempty"`
	ChangesProductTruth  bool                         `json:"changes_product_truth"`
	ArchitectureBinding  *WorkflowArchitectureBinding `json:"architecture_binding,omitempty"`
	ProposalRecord       *WorkflowProposalRecord      `json:"proposal_record,omitempty"`
	ParkedDelivery       *WorkflowReadParkedDelivery  `json:"parked_delivery,omitempty"`
	// DeliveryAssertion carries the current completed delivery assertion with
	// its typed correction. The asserted artifact is the unchanged original
	// event; the correction, when present, holds the effective merge evidence
	// and its coordinator provenance, and the assertion carries that overlay's
	// selection as effective_artifact: the correction's merge evidence when a
	// correction exists, the asserted artifact otherwise.
	DeliveryAssertion *WorkflowReadDeliveryAssertion `json:"delivery_assertion,omitempty"`
}

// readWorkflowProjectionTx returns the bounded workflow observation from the
// caller's snapshot. An absent instance returns an empty projection.
func readWorkflowProjectionTx(ctx context.Context, q queryer, request WorkflowReadRequest) (WorkflowReadProjection, error) {
	var out WorkflowReadProjection
	if request.Limit <= 0 {
		request.Limit = 32
	}
	if request.Limit > 100 {
		return out, newFailure(KindInvalidOperation, "workflow_read", "workflow read limit exceeds the bounded maximum", false, "reduce the read limit")
	}
	var activeActor sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT instance_state,current_step,definition_ref,definition_version,definition_digest,execution_actor_ref FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&out.State, &out.CurrentStep, &out.Definition.Ref, &out.Definition.Version, &out.Definition.Digest, &activeActor); err != nil {
		if err == sql.ErrNoRows {
			return out, nil
		}
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow instance", true, "retry once the database is readable", err)
	}
	out.WorkID = request.WorkID
	registered, err := verifyReadWorkflowDefinition(out.Definition)
	if err != nil {
		return out, err
	}
	out.Definition = WorkflowReadDefinition{Ref: registered.Definition.Ref, Version: registered.Definition.Version, Digest: registered.Digest}
	activeContractVersion, contractErr := activeWorkflowContractVersion(ctx, q, request.WorkID, "workflow_read")
	if contractErr != nil && contractErr != sql.ErrNoRows {
		return out, contractErr
	}
	out.ChangesProductTruth = registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth
	out.CandidateIDs = []string{}
	out.Conditions = []WorkflowReadCondition{}
	out.UnresolvedConditions = []string{}
	out.OverdueAwaits = []string{}
	out.AwaitHealth = []WorkflowReadCondition{}
	out.UnreadableConditions = []string{}
	out.BlockingConditions = []string{}
	out.ImpactNotices = []WorkflowReadNotice{}
	out.CompletionWarnings = []string{}
	out.ParkedDelivery = parkedDeliveryRead(ctx, q, registered.Definition, request.WorkID, out.CurrentStep, out.State)
	deliveryAssertion, assertionErr := workflowDeliveryAssertionRead(ctx, q, request.WorkID)
	if assertionErr != nil {
		return out, assertionErr
	}
	out.DeliveryAssertion = deliveryAssertion
	var proposal WorkflowProposalRecord
	var proposalAffected, proposalOutcomes, proposalConstraints, proposalQuestions string
	if err := q.QueryRowContext(ctx, `SELECT work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,recorded_at FROM workflow_proposal_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, request.WorkID).Scan(&proposal.WorkVersion, &proposal.Problem, &proposalAffected, &proposal.Stakes, &proposalOutcomes, &proposalConstraints, &proposalQuestions, &proposal.RecordedAt); err == nil {
		if json.Unmarshal([]byte(proposalAffected), &proposal.Affected) != nil || json.Unmarshal([]byte(proposalOutcomes), &proposal.UserOutcomes) != nil || json.Unmarshal([]byte(proposalConstraints), &proposal.Constraints) != nil || json.Unmarshal([]byte(proposalQuestions), &proposal.OpenQuestions) != nil {
			return out, newFailure(KindInvariantViolation, "workflow_read", "proposal record projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		out.ProposalRecord = &proposal
	} else if err != sql.ErrNoRows {
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read latest workflow proposal record", true, "retry once the database is readable", err)
	}

	version := activeContractVersion
	if request.ContractVersion != 0 {
		version = request.ContractVersion
	}
	contract, err := readWorkflowContractVersion(ctx, q, request.WorkID, version)
	switch {
	case err == sql.ErrNoRows && request.ContractVersion != 0:
		return out, newFailure(KindUnknownScope, "workflow_read", "the work item has no such contract version", false, "choose a contract version the work item recorded")
	case err == sql.ErrNoRows:
	case err != nil:
		return out, err
	case request.ContractVersion != 0:
		out.ChangesProductTruth = contract.ChangesProductTruth != nil && *contract.ChangesProductTruth
		out.ArchitectureBinding = contract.ArchitectureBinding
		out.Contract = &contract
	default:
		changes := out.ChangesProductTruth
		contract.ChangesProductTruth = &changes
		out.ArchitectureBinding = contract.ArchitectureBinding
		out.Contract = &contract
		if len(contract.SpecMandate) != 0 {
			homeProjectID, homeLocatorID, homeErr := workflowLawHome(ctx, q, request.WorkID)
			if homeErr != nil {
				return out, homeErr
			}
			currentMandate, mandateErr := currentWorkflowLawMandate(contract.SpecMandate, contract.ArchitectureBinding)
			if mandateErr != nil {
				return out, mandateErr
			}
			out.StaleLawRevision, err = findStaleWorkflowLawRevision(ctx, q, homeProjectID, homeLocatorID, request.WorkID, contract.Version, currentMandate)
			if err != nil {
				return out, err
			}
		}
		var workVersion int64
		if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, request.WorkID).Scan(&workVersion); err != nil {
			return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow history version", true, "retry once the database is readable", err)
		}
		out.OperatorQuestion, out.WithheldOperatorQuestion, err = workflowOperatorQuestionTx(ctx, q, request.WorkID, out.CurrentStep, workVersion, out.Definition, contract)
		if err != nil {
			return out, err
		}
	}

	rows, err := q.QueryContext(ctx, `SELECT candidate_ref FROM workflow_candidate_sets WHERE work_id=? AND contract_version=COALESCE(?,0) ORDER BY candidate_ref LIMIT ?`, request.WorkID, activeContractVersion, request.Limit)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow candidates", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow candidate", true, "retry once the database is readable", err)
		}
		out.CandidateIDs = append(out.CandidateIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow candidates", true, "retry once the database is readable", err)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT condition_id,await_type,await_ref,resolution_authority,condition_state,coalesce(expected_within_seconds,0),recorded_at FROM workflow_external_conditions WHERE work_id=? ORDER BY condition_id LIMIT ?`, request.WorkID, request.Limit)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow conditions", true, "retry once the database is readable", err)
	}
	health := []WorkflowReadCondition{}
	for rows.Next() {
		var condition WorkflowReadCondition
		var waitingSince string
		if err := rows.Scan(&condition.ID, &condition.AwaitType, &condition.AwaitRef, &condition.ResolutionAuthority, &condition.State, &condition.ExpectedWithinSeconds, &waitingSince); err != nil {
			rows.Close()
			return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow condition", true, "retry once the database is readable", err)
		}
		out.Conditions = append(out.Conditions, condition)
		if condition.State == "open" {
			out.UnresolvedConditions = append(out.UnresolvedConditions, condition.ID)
			// Read-time health derivation: compare the wait's
			// age to the declared bound. No state change, no timer.
			if recorded, parseErr := time.Parse(time.RFC3339Nano, waitingSince); parseErr == nil {
				clock := request.Now
				if clock.IsZero() {
					clock = time.Now().UTC()
				}
				condition.AgeSeconds = int64(clock.Sub(recorded).Seconds())
				if condition.AgeSeconds < 0 {
					condition.AgeSeconds = 0
				}
			}
			condition.Health = "waiting"
			if condition.ExpectedWithinSeconds > 0 && condition.AgeSeconds > condition.ExpectedWithinSeconds {
				condition.Overdue = true
				condition.Health = "overdue"
				out.OverdueAwaits = append(out.OverdueAwaits, condition.ID)
			}
			health = append(health, condition)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow conditions", true, "retry once the database is readable", err)
	}
	rows.Close()
	out.AwaitHealth = health
	ready, err := deriveWorkflowReady(ctx, q, request.WorkID)
	if err != nil {
		return out, err
	}
	out.Ready = ready.Ready
	out.BlockingConditions = append(out.BlockingConditions, ready.BlockingConditions...)
	out.UnreadableConditions = append(out.UnreadableConditions, ready.UnknownConditions...)

	rows, err = q.QueryContext(ctx, `SELECT notice_id,source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,edge_owner_work_id,edge_id,old_hash,new_hash,severity FROM workflow_impact_notices WHERE target_work_id=? OR source_work_id=? ORDER BY recorded_at,notice_id LIMIT ?`, request.WorkID, request.WorkID, request.Limit)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow impact notices", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var notice WorkflowReadNotice
		if err := rows.Scan(&notice.NoticeID, &notice.SourceWorkID, &notice.SourceContractVersion, &notice.EntityKind, &notice.EntityRef, &notice.TargetWorkID, &notice.EdgeOwnerWorkID, &notice.EdgeID, &notice.OldHash, &notice.NewHash, &notice.Severity); err != nil {
			rows.Close()
			return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow impact notice", true, "retry once the database is readable", err)
		}
		out.ImpactNotices = append(out.ImpactNotices, notice)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow impact notices", true, "retry once the database is readable", err)
	}
	rows.Close()

	var warnings string
	if err := q.QueryRowContext(ctx, `SELECT json_extract(completion_payload,'$.warnings') FROM workflow_instances WHERE work_id=? AND completion_payload IS NOT NULL`, request.WorkID).Scan(&warnings); err == nil && warnings != "" && warnings != "null" {
		if json.Unmarshal([]byte(warnings), &out.CompletionWarnings) != nil {
			return out, newFailure(KindInvariantViolation, "workflow_read", "completion warnings are malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
	}
	persisted, warningErr := workflowRecordedStalenessWarnings(ctx, q, request.WorkID)
	if warningErr != nil {
		return out, warningErr
	}
	for _, warning := range persisted {
		found := false
		for _, existing := range out.CompletionWarnings {
			if existing == warning {
				found = true
				break
			}
		}
		if !found {
			out.CompletionWarnings = append(out.CompletionWarnings, warning)
		}
	}
	return out, nil
}

// parkedDeliveryRead derives the CD-0166 parked-delivery observation for a
// work item whose current step is the delivery gate. ParkedSeconds is a
// read-time display age taken from the work item's last update; elapsed time
// holds no authority here, matching the await-health boundary, so an old park
// is labeled and never resolved by clock. A terminal instance that left the
// gate uncrossed reads as unreconciled.
func parkedDeliveryRead(ctx context.Context, q queryer, definition WorkflowDefinition, workID, currentStep, state string) *WorkflowReadParkedDelivery {
	if !workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		return nil
	}
	parked := &WorkflowReadParkedDelivery{WorkID: workID, StepID: currentStep, ResumeAction: "record_delivery"}
	var updatedAt string
	if err := q.QueryRowContext(ctx, `SELECT updated_at FROM work_items WHERE id=?`, workID).Scan(&updatedAt); err == nil {
		if touched, parseErr := time.Parse(time.RFC3339, updatedAt); parseErr == nil {
			if age := int64(time.Now().UTC().Sub(touched).Seconds()); age > 0 {
				parked.ParkedSeconds = age
			}
		}
	}
	parked.Unreconciled = state == "cancelled" || state == "superseded"
	return parked
}
