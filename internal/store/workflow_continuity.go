package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const continuityMaxOffset = 1000000

// WorkflowInstance values on ContinuitySnapshot. Presence is the pinned
// default; absence is typed, never inferred from an empty step.
const (
	WorkflowInstancePresent = "present"
	WorkflowInstanceAbsent  = "absent"
)

type ContextCheckpoint struct {
	CheckpointID     string   `json:"checkpoint_id"`
	WorkVersion      int64    `json:"work_version"`
	Sequence         int64    `json:"sequence"`
	StepID           string   `json:"step_id"`
	AttemptEpoch     int64    `json:"attempt_epoch"`
	ActiveUnit       string   `json:"active_unit"`
	Hypothesis       string   `json:"hypothesis"`
	Diagnosis        string   `json:"diagnosis"`
	Strategy         string   `json:"strategy"`
	TouchedRefs      []string `json:"touched_refs"`
	EvidenceRefs     []string `json:"evidence_refs"`
	PendingQuestions []string `json:"pending_questions"`
	PendingDecisions []string `json:"pending_decisions"`
}

type WorkflowDesignDecision struct {
	ID        string   `json:"id"`
	Question  string   `json:"question"`
	Choice    string   `json:"choice"`
	Rationale string   `json:"rationale"`
	Rejected  []string `json:"rejected"`
}

type WorkflowDesignRecord struct {
	WorkVersion int64                    `json:"work_version"`
	Approach    string                   `json:"approach"`
	Decisions   []WorkflowDesignDecision `json:"decisions"`
	TouchedRefs []string                 `json:"touched_refs"`
	RecordedAt  string                   `json:"recorded_at"`
}

type WorkflowProposalRecord struct {
	WorkVersion   int64    `json:"work_version"`
	Problem       string   `json:"problem"`
	Affected      []string `json:"affected"`
	Stakes        string   `json:"stakes"`
	UserOutcomes  []string `json:"user_outcomes"`
	Constraints   []string `json:"constraints"`
	OpenQuestions []string `json:"open_questions"`
	RecordedAt    string   `json:"recorded_at"`
}

type ContextBoundary struct {
	BoundaryID         string `json:"boundary_id"`
	Sequence           int64  `json:"sequence"`
	Kind               string `json:"kind"`
	CheckpointID       string `json:"checkpoint_id"`
	CheckpointSequence int64  `json:"checkpoint_sequence"`
	Summary            string `json:"summary"`
	RecordedAt         string `json:"recorded_at"`
}

type ContinuitySnapshot struct {
	// ReviewInputWarning reports optional Git enrichment failure outside the
	// projection bytes so session recovery does not depend on repository access.
	ReviewInputWarning error              `json:"-"`
	ReviewInput        *WorkerReviewInput `json:"review_input,omitempty"`
	WorkID             string             `json:"work_id"`
	ProductIdentity    []string           `json:"product_identity"`
	// WorkflowInstance states workflow-instance presence as typed
	// information: WorkflowInstancePresent for the pinned default every
	// captured item carries, WorkflowInstanceAbsent for an imported or
	// otherwise instance-less work item the read still answers for.
	// WorkflowStep is empty exactly when this is WorkflowInstanceAbsent.
	WorkflowInstance         string                            `json:"workflow_instance"`
	WorkflowStep             string                            `json:"workflow_step"`
	StepActions              []string                          `json:"step_actions"`
	Contract                 *WorkflowReadContract             `json:"contract"`
	SpecMandate              []string                          `json:"spec_mandate"`
	PendingOperatorDecision  *WorkflowOperatorQuestion         `json:"pending_operator_decision"`
	WithheldOperatorDecision *WorkflowOperatorQuestionWithheld `json:"withheld_operator_decision,omitempty"`
	LatestCheckpoint         *ContextCheckpoint                `json:"latest_checkpoint"`
	DesignRecord             *WorkflowDesignRecord             `json:"design_record"`
	ProposalRecord           *WorkflowProposalRecord           `json:"proposal_record"`
	Boundaries               []ContextBoundary                 `json:"boundaries"`
	BoundaryCount            int64                             `json:"boundary_count"`
	NextCursor               *string                           `json:"next_cursor"`
	Watermark                string                            `json:"watermark"`
	SourceVersionWatermark   int64                             `json:"-"`
	// NativeRuns carries the attributed native-run reports for this work
	// item, newest phase per run, with reporter, subject, evidence, and both
	// times alongside the status (CD-0039 D1/D4).
	NativeRuns               []NativeRunReport `json:"native_runs"`
	RestartAvailable         bool              `json:"restart_available"`
	RestartUnavailableReason string            `json:"restart_unavailable_reason"`
	// PendingMessages counts sent peer messages awaiting this work's next
	// session (CD-0029). The pointer survives restarts because the snapshot
	// itself is re-derived per call.
	PendingMessages int64 `json:"pending_messages"`
	// Observations carries the newest window of the work's un-promoted
	// observations (CD-0030 D2), at most ContinuityObservationWindow.
	// ObservationsTotal counts the whole population, so a reader knows when
	// the paged concord_work_trace.observations read holds more. Read-time
	// visibility: no gate consumes this.
	Observations        []WorkObservation            `json:"observations"`
	ObservationsTotal   int64                        `json:"observations_total"`
	StaleLawRevision    *StaleLawRevision            `json:"stale_law_revision,omitempty"`
	ChangesProductTruth bool                         `json:"changes_product_truth"`
	ArchitectureBinding *WorkflowArchitectureBinding `json:"architecture_binding,omitempty"`
	// ActiveVerifyLeases re-pins the reading session's held verify leases
	// (CD-0096 D5), newest first, bounded. Empty when the session named no
	// identity or holds none.
	CompatibleLawAmendments []CompatibleLawAmendment    `json:"compatible_law_amendments"`
	ActiveVerifyLeases      []ActiveWorktreeVerifyLease `json:"active_verify_leases,omitempty"`
	WorkPin                 *WorkPin                    `json:"work_pin,omitempty"`
	// PendingProjectHandoff carries the work's newest unconsumed
	// Project-session handoff (CD-0182 amendment), so a Project-selected
	// boot renders the bounded job a receiving session must consume before
	// managed execution. Nil when no handoff stands unconsumed.
	PendingProjectHandoff *ProjectHandoff `json:"pending_project_handoff,omitempty"`
	// ReadyWorkerJobs carries the work's dispatch-ready worker-job revisions
	// in the exact inputs.worker_job packet shape (CD-0205), so the
	// dispatcher binds a selected revision verbatim and never authors job
	// content itself. Empty when no revision is ready.
	ReadyWorkerJobs []WorkerPacketJob `json:"ready_worker_jobs,omitempty"`
	// ChecksRepairReviewJobs carries satisfied repairing revisions awaiting
	// their one confirming review (CD-0205 D4), in packet shape. Only review
	// capability selects them. They are not ready_worker_jobs.
	ChecksRepairReviewJobs []WorkerPacketJob `json:"checks_repair_review_jobs,omitempty"`
	// ReviewWorkerJobs carries the delivery-bound final Review fallback, never
	// ordinary readiness. Review selects it only without another eligible job.
	ReviewWorkerJobs []WorkerPacketJob `json:"review_worker_jobs,omitempty"`
	// LawContext resolves the approved contract's binding law and Domains
	// against the law_subjects and domains projections at read time. Nil
	// when the contract binds no law and no Domain.
	LawContext *WorkflowLawContext `json:"law_context,omitempty"`
	// WorkContext and WorkContextUnavailable carry the shared projection.
	// The outside-repair early exit does not evaluate either field.
	WorkContext            *WorkContextView        `json:"work_context,omitempty"`
	WorkContextUnavailable *WorkContextUnavailable `json:"work_context_unavailable,omitempty"`
	// OutsideRepairDisposition names the work's outside-repair disposition
	// when one is recorded. The store keeps the typed evidence the boundary
	// authenticated and refuses every managed workflow move while the
	// disposition is active.
	OutsideRepairDisposition *OutsideRepairDisposition `json:"outside_repair_disposition,omitempty"`
	// OutsideRepairRoute is the declared recovery route the boundary code
	// dispatches when the disposition holds the work. The store owns the
	// typed route and never fabricates a workflow action list as a remedy.
	OutsideRepairRoute []string `json:"outside_repair_route,omitempty"`
}

type ContinuityRequest struct {
	Work   string
	Limit  int
	Cursor string
	// Owner names the reading session, so the pinned projection re-pins its
	// held verify leases (CD-0096 D5). Nil keeps the work-keyed projection
	// the session-boot path renders.
	Owner *SessionWorktreeOwner
}

func ReadWorkflowContinuity(ctx context.Context, s *Store, req ContinuityRequest) (ContinuitySnapshot, error) {
	return readWorkflowContinuityForRegistry(ctx, s, req, nil)
}

// readWorkflowContinuityForRegistry is the registry-threaded continuity
// core: a caller registry verifies the stored pin and projects the same
// folded confirmation answer the admission state stores, with no builtin
// fallback. A nil registry keeps the builtin convenience.
func readWorkflowContinuityForRegistry(ctx context.Context, s *Store, req ContinuityRequest, registry DefinitionRegistry) (ContinuitySnapshot, error) {
	var out ContinuitySnapshot
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C19.Continuity", "store is not open", false, "open the authority database")
	}
	if len(req.Work) < 2 || len(req.Work) > 128 {
		return out, newFailure(KindInvalidPayload, "C19.Continuity", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 20 {
		return out, newFailure(KindInvalidPayload, "C19.Continuity", "continuity limit/page.limit exceeds maximum 20", false, "supply a limit of at most 20")
	}
	offset, err := continuityOffset(req.Cursor, req.Work)
	if err != nil {
		return out, err
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot open a consistent continuity snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	out.SourceVersionWatermark, err = storeRevision(ctx, tx)
	if err != nil {
		return out, err
	}
	if req.Owner != nil {
		if err := validateSessionWorktreeOwner(*req.Owner); err != nil {
			return out, err
		}
		leases, err := heldWorktreeVerifyLeasesByOwnerTx(ctx, tx, *req.Owner)
		if err != nil {
			return out, err
		}
		out.ActiveVerifyLeases = leases
	}
	out, err = continuityReadIdentityTx(ctx, tx, req.Work, out)
	if err != nil {
		return out, err
	}
	// Outside repair is readable without trusting the workflow it must repair.
	// Establish work and Product identity first, then stop before definition,
	// contract, admission, or managed history enrichment.
	if err := continuityReadOutsideRepairTx(ctx, tx, req.Work, &out); err != nil {
		return out, err
	}
	if disposition := out.OutsideRepairDisposition; disposition != nil && disposition.State != OutsideRepairStateResumed {
		return continuityReadOutsideRepairIdentityTx(ctx, tx, req.Work, registry, out)
	}
	if err := continuityReadManagedHistoryTx(ctx, tx, req, registry, offset, &out); err != nil {
		return out, err
	}
	// Candidate identity is sampled with the dependencies, then its immutable
	// tree resolves after the read snapshot closes. Dispatch revalidates it
	// against the sealed native observation; this read grants no authority.
	reviewSubject, err := sampleContinuityReviewSubjectTx(ctx, tx, req.Work, &out)
	if err != nil {
		return out, err
	}
	if err := tx.Rollback(); err != nil {
		return out, err
	}
	observeContinuityReviewSubject(ctx, &out, reviewSubject)
	return out, nil
}

func continuityReadIdentityTx(ctx context.Context, tx *sql.Tx, workID string, out ContinuitySnapshot) (ContinuitySnapshot, error) {
	out.WorkID = workID
	out.Boundaries = []ContextBoundary{}
	out.ProductIdentity = []string{}
	out.SpecMandate = []string{}
	out.StepActions = []string{}
	out.CompatibleLawAmendments = []CompatibleLawAmendment{}
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "C19.Continuity"); err != nil {
		return out, err
	}
	exists, err := workExistsCore(ctx, tx, workID)
	if err != nil {
		return out, err
	}
	if !exists {
		return out, newFailure(KindProjectionNotFound, "C19.Continuity", "work item is not recorded", false, "reread_entities")
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? ORDER BY pp.product_id LIMIT 65`, workID)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read Product identity", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		out.ProductIdentity = append(out.ProductIdentity, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if len(out.ProductIdentity) > 64 {
		return out, newFailure(KindLimitExceeded, "C19.Continuity", "Product identity exceeds the continuity snapshot bound", false, "reduce_limit")
	}
	return out, nil
}

func continuityReadOutsideRepairIdentityTx(ctx context.Context, tx *sql.Tx, workID string, registry DefinitionRegistry, out ContinuitySnapshot) (ContinuitySnapshot, error) {
	pin, err := readWorkPinForRegistryTx(ctx, tx, workID, registry)
	if err != nil {
		return out, err
	}
	out.WorkPin = &pin
	out.Watermark = pin.Watermark
	out.WorkflowInstance = WorkflowInstanceAbsent
	err = tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&out.WorkflowStep)
	if err == nil {
		out.WorkflowInstance = WorkflowInstancePresent
	} else if err != sql.ErrNoRows {
		return out, workflowProjectionError(err, "cannot read outside-repair workflow identity")
	}
	out.NativeRuns = []NativeRunReport{}
	out.Observations = []WorkObservation{}
	return out, nil
}

func continuityReadManagedHistoryTx(ctx context.Context, tx *sql.Tx, req ContinuityRequest, registry DefinitionRegistry, offset int, out *ContinuitySnapshot) error {
	instance, err := readContinuityInstanceForRegistryTx(ctx, tx, req.Work, registry, out)
	if err != nil {
		return err
	}
	currentStep, definition, workVersion := instance.step, instance.definition, instance.workVersion
	activeContractVersion, contractErr := activeWorkflowContractVersion(ctx, tx, req.Work, "C19.Continuity")
	if contractErr != nil && contractErr != sql.ErrNoRows {
		return contractErr
	}
	if err := continuityReadContractTx(ctx, tx, req.Work, out, activeContractVersion, currentStep, workVersion, definition, registry); err != nil {
		return err
	}
	if err := continuityReadCheckpointTx(ctx, tx, req.Work, out); err != nil {
		return err
	}
	// The work's newest unconsumed Project-session handoff rides the
	// snapshot when present; the absent field keeps a handoff-free work's
	// bytes unchanged.
	out.PendingProjectHandoff, err = PendingProjectHandoffTx(ctx, tx, req.Work)
	if err != nil {
		return err
	}
	out.DesignRecord, err = readCurrentWorkflowDesign(ctx, tx, req.Work)
	if err != nil {
		return err
	}
	if err := continuityReadReadyWorkerJobsTx(ctx, tx, req.Work, instance.callerCapable, out); err != nil {
		return err
	}
	if out.ProposalRecord, err = readLatestWorkflowProposal(ctx, tx, req.Work, "C19.Continuity"); err != nil {
		return err
	}
	if err := continuityReadBoundariesTx(ctx, tx, req.Work, out, req.Limit, offset); err != nil {
		return err
	}
	if err := continuityReadTrailingTx(ctx, tx, req.Work, out); err != nil {
		return err
	}
	view, unavailable, viewErr := readWorkContextProjection(ctx, tx, req.Work)
	if viewErr != nil {
		return viewErr
	}
	out.WorkContext, out.WorkContextUnavailable = view, unavailable
	return nil
}

// continuityReadOutsideRepairTx exposes the work's outside-repair disposition
// and the declared recovery route the boundary code dispatches. The route is
// present while a disposition is active, regardless of whether the work item
// also carries a workflow instance, so a host process can drive the reconcile
// without rereading the workflow projection.
func continuityReadOutsideRepairTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot) error {
	disposition, err := outsideRepairDispositionTx(ctx, tx, work)
	if err != nil {
		return err
	}
	out.OutsideRepairDisposition = disposition
	if disposition != nil && disposition.State == OutsideRepairStateActive {
		out.OutsideRepairRoute = outsideRepairRouteNames()
	}
	if disposition != nil && disposition.State != OutsideRepairStateResumed {
		out.StepActions = []string{}
		out.PendingOperatorDecision = nil
		out.WithheldOperatorDecision = nil
		out.RestartAvailable = false
		out.RestartUnavailableReason = "outside-repair disposition owns the work"
	}
	return nil
}

// continuityReadContractTx reads the one active contract and every enrichment
// that hangs off it — predicates, architecture binding, self-repair, law
// revisions and context, stale-law and compatible-amendment scans, and the
// open operator question. A work item with no contract row leaves the
// snapshot without one.
func continuityReadContractTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot, activeContractVersion int64, currentStep string, workVersion int64, definition WorkflowReadDefinition, registry DefinitionRegistry) error {
	var contract WorkflowReadContract
	var required, routes, mandates, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT contract_version,premise,required_evidence,route_conventions,spec_mandate,law_modifies,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, work, activeContractVersion).Scan(&contract.Version, &contract.Premise, &required, &routes, &mandates, &modifies, &contract.RigorClass); err == nil {
		if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
			return newFailure(KindInvariantViolation, "C19.Continuity", "workflow contract projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		contract.RequiredEvidence = nonNilStrings(contract.RequiredEvidence)
		contract.RouteConventions = nonNilStrings(contract.RouteConventions)
		contract.SpecMandate = nonNilStrings(contract.SpecMandate)
		contract.LawModifies = nonNilStrings(contract.LawModifies)
		predicates, predicateErr := readWorkflowContractPredicates(ctx, tx, work, contract.Version)
		if predicateErr != nil {
			return predicateErr
		}
		contract.OutcomePredicates = predicates
		changes := out.ChangesProductTruth
		contract.ChangesProductTruth = &changes
		binding, bindingErr := readWorkflowArchitectureBinding(ctx, tx, work, contract.Version)
		if bindingErr != nil {
			return bindingErr
		}
		contract.ArchitectureBinding = binding
		out.ArchitectureBinding = contract.ArchitectureBinding
		out.Contract = &contract
		revisions, revisionErr := readWorkflowLawRevisions(ctx, tx, work, contract.Version)
		if revisionErr != nil {
			return revisionErr
		}
		contract.LawRevisions = revisions
		if err := continuityResolveLawMandateTx(ctx, tx, work, out, &contract); err != nil {
			return err
		}
		// A stale contract remains inspectable, but cannot supply readable
		// current-law context for a law that no longer exists (CD-0036 D3/D6).
		if out.StaleLawRevision == nil {
			lawContext, lawErr := readWorkflowLawContext(ctx, tx, work, &contract)
			if lawErr != nil {
				return lawErr
			}
			out.LawContext = lawContext
		}
		out.SpecMandate = nonNilStrings(append([]string(nil), contract.SpecMandate...))
		registered, verifyErr := verifyReadWorkflowDefinitionForRegistry(definition, registry)
		if verifyErr != nil {
			return verifyErr
		}
		confirmation, foldErr := foldWorkflowConfirmationTx(ctx, tx, work)
		if foldErr != nil {
			return foldErr
		}
		pending, withheld, questionErr := workflowOperatorQuestionForDefinitionTx(ctx, tx, work, currentStep, workVersion, registered.Definition, definition, contract, confirmation)
		if questionErr != nil {
			return questionErr
		}
		out.PendingOperatorDecision, out.WithheldOperatorDecision = pending, withheld
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot read workflow contract", true, "retry once the database is readable", err)
	}
	return nil
}

// continuityResolveLawMandateTx scans the contract's spec mandate for stale
// law revisions and, when none is stale, for compatible amendments.
func continuityResolveLawMandateTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot, contract *WorkflowReadContract) error {
	if len(contract.SpecMandate) == 0 {
		return nil
	}
	homeProjectID, homeLocatorID, homeErr := workflowLawHome(ctx, tx, work)
	if homeErr != nil {
		return homeErr
	}
	currentMandate, mandateErr := currentWorkflowLawMandate(contract.SpecMandate, contract.ArchitectureBinding)
	if mandateErr != nil {
		return mandateErr
	}
	stale, staleErr := findStaleWorkflowLawRevision(ctx, tx, homeProjectID, homeLocatorID, work, contract.Version, currentMandate)
	if staleErr != nil {
		return staleErr
	}
	out.StaleLawRevision = stale
	if out.StaleLawRevision != nil {
		return nil
	}
	amendments, amendErr := findCompatibleWorkflowLawAmendments(ctx, tx, homeProjectID, homeLocatorID, work, contract.Version, currentMandate)
	if amendErr != nil {
		return amendErr
	}
	out.CompatibleLawAmendments = amendments
	return nil
}

// readLatestContextCheckpointTx is the one latest-checkpoint derivation the
// continuity snapshot and the dispatch packet admission share. It reads the
// newest row of the checkpoint projection inside the caller's transaction;
// the projection carries no source event identity, so the checkpoint's own
// durable identity (checkpoint_id, work_version, sequence) is the identity
// every consumer compares. A checkpoint row with malformed arrays is an
// invariant violation.
func readLatestContextCheckpointTx(ctx context.Context, q queryer, workID, operation string) (*ContextCheckpoint, error) {
	var checkpoint ContextCheckpoint
	var touched, evidence, questions, decisions string
	if err := q.QueryRowContext(ctx, `SELECT checkpoint_id,work_version,checkpoint_sequence,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions FROM workflow_context_checkpoints WHERE work_id=? ORDER BY checkpoint_sequence DESC LIMIT 1`, workID).Scan(&checkpoint.CheckpointID, &checkpoint.WorkVersion, &checkpoint.Sequence, &checkpoint.StepID, &checkpoint.AttemptEpoch, &checkpoint.ActiveUnit, &checkpoint.Hypothesis, &checkpoint.Diagnosis, &checkpoint.Strategy, &touched, &evidence, &questions, &decisions); err == nil {
		if json.Unmarshal([]byte(touched), &checkpoint.TouchedRefs) != nil || json.Unmarshal([]byte(evidence), &checkpoint.EvidenceRefs) != nil || json.Unmarshal([]byte(questions), &checkpoint.PendingQuestions) != nil || json.Unmarshal([]byte(decisions), &checkpoint.PendingDecisions) != nil {
			return nil, newFailure(KindInvariantViolation, operation, "context checkpoint projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		return &checkpoint, nil
	} else if err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, operation, "cannot read latest context checkpoint", true, "retry once the database is readable", err)
	}
	return nil, nil
}

// continuityReadCheckpointTx reads the latest context checkpoint onto the
// snapshot through the shared tx-scoped reader.
func continuityReadCheckpointTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot) error {
	checkpoint, err := readLatestContextCheckpointTx(ctx, tx, work, "C19.Continuity")
	if err != nil {
		return err
	}
	out.LatestCheckpoint = checkpoint
	return nil
}

// validateWorkerPacketCheckpoint refuses a dispatch whose packet does not
// carry the latest context checkpoint (CON-883), mirroring the correction
// and work-context admissions above it. The latest checkpoint is re-read
// inside the dispatch transaction through the same tx-scoped derivation the
// continuity snapshot projects, so the comparison is against the state the
// spawn will land on. A present checkpoint requires the member, decoded
// closed and equal byte-for-byte to the reader's canonical serialization; a
// packet carrying a checkpoint no record backs refuses.
func validateWorkerPacketCheckpoint(ctx context.Context, q queryer, workID string, packetRaw json.RawMessage) error {
	var packet struct {
		Inputs struct {
			Checkpoint json.RawMessage `json:"checkpoint"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is malformed", false, "supply the lane packet bound to this work item and attempt")
	}
	current, err := readLatestContextCheckpointTx(ctx, q, workID, "workflow_action")
	if err != nil {
		return err
	}
	present := len(packet.Inputs.Checkpoint) != 0 && string(packet.Inputs.Checkpoint) != "null"
	if current == nil {
		if present {
			return newFailure(KindInvalidPayload, "workflow_action", "worker packet carries a context checkpoint without a latest context checkpoint", false, "build the packet from the current work pin")
		}
		return nil
	}
	if !present {
		return newFailure(KindInvalidPayload, "workflow_action", "worker packet does not carry the latest context checkpoint", false, "build a fresh packet from the current work pin")
	}
	decoder := json.NewDecoder(strings.NewReader(string(packet.Inputs.Checkpoint)))
	decoder.DisallowUnknownFields()
	var claimed ContextCheckpoint
	if err := decoder.Decode(&claimed); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action", "worker packet inputs.checkpoint is not one closed context checkpoint", false, "build a fresh packet from the current work pin")
	}
	claimedJSON, claimedErr := json.Marshal(claimed)
	currentJSON, currentErr := json.Marshal(current)
	if claimedErr != nil || currentErr != nil || !bytes.Equal(claimedJSON, currentJSON) {
		return newFailure(KindInvalidPayload, "workflow_action", "worker packet does not carry the latest context checkpoint", false, "build a fresh packet from the current work pin")
	}
	return nil
}

// continuityReadReadyWorkerJobsTx reads the dispatch-ready worker-job
// revisions through the one readiness predicate dispatch admission applies.
// On a caller-capable pin each job carries the caller_exclusions its
// implementation dispatch binds (CD-0227 D1); a dispatcher drops them for
// every other lane. A set above the packet's declared bounds — in count or
// in per-path characters — is not published, so the read stays available
// and the dispatch refuses with its recovery.
func continuityReadReadyWorkerJobsTx(ctx context.Context, tx *sql.Tx, work string, callerCapable bool, out *ContinuitySnapshot) error {
	views, err := readWorkerJobRevisions(ctx, tx, work)
	if err != nil {
		return err
	}
	var corrections map[string][]string
	if callerCapable {
		if corrections, err = workerJobCorrectionPathsTx(ctx, tx, work); err != nil {
			return err
		}
	}
	for _, view := range views {
		if !view.Ready {
			continue
		}
		job := packetJobFromView(view)
		if callerCapable {
			exclusions := workerJobCorrectionExclusions(corrections, view.Binding.JobID)
			if len(exclusions) > 0 && validateWorkerJobPacketExclusions(view.Binding.JobID, exclusions) == nil {
				job.CallerExclusions = exclusions
			}
		}
		out.ReadyWorkerJobs = append(out.ReadyWorkerJobs, job)
	}
	// The awaiting confirming-review jobs (CD-0205 D4):
	// every open accepted exact repairing revision whose one confirming
	// review is still unconsumed.
	// They are never ready jobs — the one review dispatch each admits reads
	// them here; ordinary dispatch admission still applies.
	// Candidates keep deterministic binding order, so a reader resolves the
	// same job first on every read.
	candidates, err := workerChecksRepairAwaitingReviewTx(ctx, tx, work)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		for _, view := range views {
			if view.Binding == candidate {
				out.ChecksRepairReviewJobs = append(out.ChecksRepairReviewJobs, packetJobFromView(view))
				break
			}
		}
	}
	final, err := finalReviewWorkerJobTx(ctx, tx, work)
	if err != nil || final == nil {
		return err
	}
	if i := slices.IndexFunc(views, func(view WorkerJobRevisionView) bool { return view.Binding == *final }); i >= 0 {
		out.ReviewWorkerJobs = append(out.ReviewWorkerJobs, packetJobFromView(views[i]))
	}
	return nil
}

// continuityReadBoundariesTx reads one history page of context boundaries and
// derives the next cursor from the page's last retained boundary.
func continuityReadBoundariesTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot, limit, offset int) error {
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_context_boundaries WHERE work_id=?`, work).Scan(&total); err != nil {
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot count context boundaries", true, "retry once the database is readable", err)
	}
	out.BoundaryCount = total
	rows, err := tx.QueryContext(ctx, `SELECT boundary_id,boundary_sequence,boundary_kind,checkpoint_id,checkpoint_sequence,summary,recorded_at FROM workflow_context_boundaries WHERE work_id=? ORDER BY boundary_sequence DESC LIMIT ? OFFSET ?`, work, limit+1, offset)
	if err != nil {
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot read context boundary history", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var item ContextBoundary
		if err := rows.Scan(&item.BoundaryID, &item.Sequence, &item.Kind, &item.CheckpointID, &item.CheckpointSequence, &item.Summary, &item.RecordedAt); err != nil {
			rows.Close()
			return err
		}
		out.Boundaries = append(out.Boundaries, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot finish reading context boundary history", true, "retry once the database is readable", err)
	}
	rows.Close()
	if len(out.Boundaries) > limit {
		out.limitBoundaries(work, offset, limit)
	}
	return nil
}

// limitBoundaries keeps the first limit boundaries of the page read at
// offset and derives the continuation cursor from the last retained one, so
// no boundary drops without a cursor that resumes at it.
func (snapshot *ContinuitySnapshot) limitBoundaries(work string, offset, limit int) {
	last := snapshot.Boundaries[limit-1]
	snapshot.Boundaries = snapshot.Boundaries[:limit]
	raw, _ := json.Marshal(map[string]any{"v": 1, "work": work, "offset": offset + limit, "last": last.Sequence})
	next := base64.RawURLEncoding.EncodeToString(raw)
	snapshot.NextCursor = &next
}

// LimitWindow returns a copy of one captured snapshot that keeps complete
// prefixes: at most observations of the newest observation window and at
// most boundaries of the boundary page. A caller fitting a byte budget uses
// it, so every fitted variant derives from the same read transaction. The
// observation total stays the population count; a truncated boundary page
// carries a cursor that resumes at its first dropped boundary.
func (snapshot ContinuitySnapshot) LimitWindow(req ContinuityRequest, observations, boundaries int) (ContinuitySnapshot, error) {
	// A boundary page that keeps no boundary cannot advance its cursor, so a
	// non-empty page keeps at least one.
	if observations < 0 || boundaries < 1 {
		return snapshot, newFailure(KindInvalidPayload, "C19.Continuity", "continuity window bounds are out of range", false, "keep at least one boundary and no negative observation window")
	}
	if observations < len(snapshot.Observations) {
		snapshot.Observations = append([]WorkObservation{}, snapshot.Observations[:observations]...)
	}
	if boundaries < len(snapshot.Boundaries) {
		offset, err := continuityOffset(req.Cursor, req.Work)
		if err != nil {
			return snapshot, err
		}
		snapshot.Boundaries = append([]ContextBoundary{}, snapshot.Boundaries...)
		snapshot.limitBoundaries(req.Work, offset, boundaries)
	}
	return snapshot, nil
}

// continuityReadTrailingTx fills the snapshot's trailing scalar and list
// fields: watermark, restart availability, pending messages, observations,
// and native runs.
func continuityReadTrailingTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot) error {
	watermark, err := readWorkEffectSeq(ctx, tx, work)
	if err != nil {
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot read continuity watermark", true, "retry once the database is readable", err)
	}
	out.Watermark = "seq:" + strconv.FormatInt(watermark, 10)
	out.RestartAvailable = false
	out.RestartUnavailableReason = "typed restart is deliberately excluded (CD-0027); pinned continuity is re-derived per call"
	// Tx-scoped: this function holds a read transaction, and a second
	// connection would deadlock on SQLite's single writer.
	if countErr := tx.QueryRowContext(ctx, `SELECT count(*) FROM work_messages WHERE recipient_work_id=? AND state='sent'`, work).Scan(&out.PendingMessages); countErr != nil {
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot count pending messages", true, "retry once the database is readable", countErr)
	}
	nativeRuns, nativeErr := readWorkflowNativeRunsTx(ctx, tx, work)
	if nativeErr != nil {
		return nativeErr
	}
	out.NativeRuns = nativeRuns
	page, err := observationsPageForWork(ctx, tx, WorkObservationsRequest{WorkID: work, Limit: ContinuityObservationWindow})
	if err != nil {
		return err
	}
	out.Observations, out.ObservationsTotal = page.Observations, page.Total
	return nil
}

// ContinuityObservationWindow bounds the newest observations continuity
// carries. The whole population pages through concord_work_trace.observations.
const ContinuityObservationWindow = 16

// ContinuityWorkResolution names the active work item a session directory
// resolves to. ProductID is the first Product identity the work's Project
// carries, so the session boot render always names an identity the
// continuity snapshot binds.
type ContinuityWorkResolution struct {
	WorkID    string
	ProductID string
	ProjectID string
}

// ResolveContinuityWorkByDirectory resolves a session directory through the
// claimed worktree to the active work item. The read is pure projection over
// worktree_entries and worktree_claims (CD-0104 D1): it records nothing and
// binds no session to work. A directory that holds no active claimed
// worktree — the registered main checkout, an unknown path, a reclaimed
// claim — resolves an empty WorkID with no error, which the caller renders
// as no block rather than the launch item's.
func (s *Store) ResolveContinuityWorkByDirectory(ctx context.Context, directory string) (ContinuityWorkResolution, error) {
	var out ContinuityWorkResolution
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "continuity_directory", "store is not open", false, "open the authority database")
	}
	if directory == "" {
		return out, nil
	}
	// A claimed worktree path is stored clean and absolute. The session
	// directory the host reports may traverse symlinks, so the symlink-
	// resolved form is a candidate beside the clean one. A path that cannot
	// be resolved on this machine still resolves on its clean form: the read
	// stays a projection over the claims, not a filesystem probe.
	normalized, err := normalizePath(directory)
	if err != nil {
		normalized = ""
	}
	err = s.db.QueryRowContext(ctx, `SELECT c.work_id,c.project_id FROM worktree_entries e
		JOIN worktree_claims c ON c.op_id=e.claim_op_id
		JOIN work_items w ON w.id=c.work_id
		WHERE e.state='active' AND c.state IN ('pending','verified') AND e.path IN (?,?)
		ORDER BY c.work_id LIMIT 1`, filepath.Clean(directory), normalized).Scan(&out.WorkID, &out.ProjectID)
	if err == sql.ErrNoRows {
		return ContinuityWorkResolution{}, nil
	}
	if err != nil {
		return out, wrapFailure(KindUnavailable, "continuity_directory", "cannot read the claimed worktree", true, "retry once the database is readable", err)
	}
	err = s.db.QueryRowContext(ctx, `SELECT product_id FROM product_projects WHERE project_id=? ORDER BY product_id LIMIT 1`, out.ProjectID).Scan(&out.ProductID)
	if err == sql.ErrNoRows {
		return ContinuityWorkResolution{}, nil
	}
	if err != nil {
		return out, wrapFailure(KindUnavailable, "continuity_directory", "cannot read the work's Product identity", true, "retry once the database is readable", err)
	}
	return out, nil
}

// continuityInstance is the workflow-instance part of one continuity read.
// present is false for a recorded work item that holds no instance.
type continuityInstance struct {
	step string
	// definition is the read-model reference; the full registered
	// definition loads beside it, so the caller-scope capability derives
	// from the declared record_worker_job members exactly as dispatch
	// admission derives it.
	definition    WorkflowReadDefinition
	registered    *RegisteredDefinition
	workVersion   int64
	present       bool
	callerCapable bool
}

// readContinuityInstanceForRegistryTx is the registry-threaded continuity
// instance phase: a caller registry verifies the stored pin and seats the
// work pin through itself with no builtin fallback, so the continuity
// snapshot projects the same folded confirmation answer the work pin does.
func readContinuityInstanceForRegistryTx(ctx context.Context, tx *sql.Tx, work string, registry DefinitionRegistry, out *ContinuitySnapshot) (continuityInstance, error) {
	var instance continuityInstance
	err := tx.QueryRowContext(ctx, `SELECT current_step,definition_ref,definition_digest,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, work).Scan(&instance.step, &instance.definition.Ref, &instance.definition.Digest, &instance.workVersion)
	if err == sql.ErrNoRows {
		exists, existsErr := workExistsCore(ctx, tx, work)
		if existsErr != nil {
			return instance, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read work item", true, "retry once the database is readable", existsErr)
		}
		if !exists {
			return instance, newFailure(KindProjectionNotFound, "C19.Continuity", "work item is not recorded", false, "reread_entities")
		}
		out.WorkflowInstance = WorkflowInstanceAbsent
		return instance, nil
	}
	if err != nil {
		return instance, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read workflow step", true, "retry once the database is readable", err)
	}
	instance.present = true
	out.WorkflowInstance = WorkflowInstancePresent
	pin, err := readWorkPinForRegistryTx(ctx, tx, work, registry)
	if err != nil {
		return instance, err
	}
	out.WorkPin = &pin
	out.WorkflowStep = instance.step
	registered, err := verifyReadWorkflowDefinitionForRegistry(instance.definition, registry)
	if err != nil {
		return instance, err
	}
	out.ChangesProductTruth = registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth
	instance.callerCapable = workflowCallerScopeActive(registered.Definition)
	if step := workflowStep(registered.Definition, instance.step); step != nil {
		out.StepActions = append(out.StepActions, step.Actions...)
	}
	return instance, nil
}

func continuityOffset(cursor, work string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, newFailure(KindInvalidCursor, "C19.Continuity", "continuity cursor is malformed", false, "use the cursor returned for this continuity query")
	}
	var value struct {
		V      int    `json:"v"`
		Work   string `json:"work"`
		Offset int    `json:"offset"`
	}
	if json.Unmarshal(b, &value) != nil || value.V != 1 || value.Work != work || value.Offset < 0 {
		return 0, newFailure(KindInvalidCursor, "C19.Continuity", "continuity cursor does not match the work", false, "use the cursor returned for this continuity query")
	}
	if value.Offset > continuityMaxOffset {
		return 0, newFailure(KindLimitExceeded, "C19.Continuity", "continuity cursor offset exceeds the bounded history", false, "reduce_limit")
	}
	return value.Offset, nil
}
