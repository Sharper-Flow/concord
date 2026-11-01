package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
)

// WorkPin is the one point-in-time projection that a caller needs to prepare
// the next operation for a work item.
type WorkPin struct {
	Review                  *WorkflowReviewState `json:"review,omitempty"`
	WorkID                  string               `json:"work_id"`
	Title                   string               `json:"title"`
	LinearIssueKey          string               `json:"linear_issue_key"`
	ProjectID               string               `json:"project_id"`
	ProjectDisplayName      string               `json:"project_display_name"`
	Version                 int64                `json:"version"`
	Lifecycle               string               `json:"lifecycle"`
	CancelledInstanceCloses int64                `json:"cancelled_instance_closes"`
	WorkflowType            string               `json:"workflow_type"`
	Step                    string               `json:"step"`
	Watermark               string               `json:"watermark"`
	// WorkflowType and WorkflowDefinitionDigest carry the identity of the
	// pinned definition the registry verified for this pin.
	// They come from the same instance-row read and the same
	// verifyReadWorkflowDefinition call the pin's intents derive from, so a
	// caller can cite the exact definition the admission collector used.
	// The pointer is the typed absence CD-0210 holds open: nil means the
	// pin's reader verified no definition — the outside-repair hold reads raw
	// identity only and grants no managed pin authority — so the key stays
	// absent on the wire instead of carrying a zero-value or fabricated
	// definition identity. Only workPinReadInstanceForRegistryTx sets it.
	WorkflowDefinitionDigest *string `json:"workflow_definition_digest,omitempty"`
	// Obligations lists the sorted exact evidence obligation IDs the pinned
	// definition declares, collected from the same root, step, and rigor
	// declarations the architecture-binding admission collector reads
	// (workflowDefinitionObligations). The pin teaches what the enforcing
	// collector admits instead of a hand-copied list. The pointer keeps the
	// same typed absence as the definition identity above, while a managed
	// pin still emits the key even for a definition that declares no
	// obligations.
	Obligations *[]string `json:"obligations,omitempty"`
	// OutstandingEvidenceKinds is known kind-debt, not proof that reference
	// obligations are satisfied. Nil omits an unknown or partial view; a
	// non-nil empty slice records computed zero kind-debt.
	OutstandingEvidenceKinds *[]string       `json:"outstanding_evidence_kinds,omitempty"`
	NextValidIntents         []WorkPinIntent `json:"next_valid_intents"`
	// DrivingSessions lists the distinct agent sessions that have driven this
	// workflow, with each session's most recent action and action time. It is
	// derived from workflow actors and actions, not session identity evidence.
	DrivingSessions []WorkPinDrivingSession    `json:"driving_sessions"`
	Correction      *WorkflowCorrectionContext `json:"correction,omitempty"`
	// WorkContext carries the strict reader's exact view for dispatch.
	// WorkContextUnavailable distinguishes an over-bound view from absence.
	// The partial recovery pin does not evaluate either field.
	WorkContext            *WorkContextView        `json:"work_context,omitempty"`
	WorkContextUnavailable *WorkContextUnavailable `json:"work_context_unavailable,omitempty"`
	// VerifiedCriteria carries the approved contract predicates and their latest
	// verdict kinds only after a workflow reaches completed.
	VerifiedCriteria []WorkPinVerifiedCriterion `json:"verified_criteria,omitempty"`
	// VerdictEvidence exposes the bound immutable evidence set at steps where
	// record_verdict is declarable, so a caller cites qualifying refs without
	// a raw store read (#974). It stays nil at every other step.
	VerdictEvidence []WorkPinEvidence `json:"verdict_evidence,omitempty"`
	// OutsideRepairDisposition names the work's outside-repair disposition
	// when one is recorded. The pin exposes the same typed record the
	// continuity snapshot does, so a host process reaches the disposition
	// through either read surface.
	OutsideRepairDisposition *OutsideRepairDisposition `json:"outside_repair_disposition,omitempty"`
	// OutsideRepairRoute is the declared recovery route the boundary code
	// dispatches when the disposition holds the work. Managed intents remain
	// empty; outside-repair operations are not workflow actions.
	OutsideRepairRoute []string `json:"outside_repair_route,omitempty"`
}

type WorkPinVerifiedCriterion struct {
	PredicateID    string          `json:"predicate_id"`
	Ordinal        int             `json:"ordinal"`
	OutcomeKind    string          `json:"outcome_kind"`
	OutcomePayload json.RawMessage `json:"outcome_payload"`
	VerdictKind    string          `json:"verdict_kind"`
}

type WorkPinEvidence struct {
	EvidenceKind        string `json:"evidence_kind"`
	ImmutableSubjectRef string `json:"immutable_subject_ref"`
}

type WorkPinDrivingSession struct {
	SessionRef   string `json:"session_ref"`
	LastActionID string `json:"last_action_id"`
	LastActedAt  string `json:"last_acted_at"`
}

type WorkPinIntent struct {
	Tool            string   `json:"tool"`
	Operation       string   `json:"operation"`
	ReasonCode      string   `json:"reason_code"`
	ActionID        string   `json:"action_id"`
	RequiredFields  []string `json:"required_fields"`
	ExpectedVersion int64    `json:"expected_version"`
}

// ReadWorkPin opens one read transaction and derives a complete pin.
func ReadWorkPin(ctx context.Context, s *Store, workID string) (WorkPin, error) {
	var pin WorkPin
	if s == nil || s.db == nil {
		return pin, newFailure(KindUnavailable, "work_pin", "store is not open", false, "open the authority database")
	}
	// The pin is advisory: when the verification is unavailable the read
	// proceeds without a proof, and the folds report law currency and the
	// confirmation unverified instead of failing the pin.
	if verified, err := s.PrepareWorkLawProof(ctx, workID); err == nil {
		ctx = verified
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot open a consistent work pin snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	return ReadWorkPinTx(ctx, tx, workID)
}

// workStepFoldsRegistry reports whether the work's current step declares
// confirm_premise, whose fold checks the investigation artifact's Domain refs
// against the committed registry. An unresolvable definition or step folds
// nothing here; its owning path answers that refusal.
func (s *Store) workStepFoldsRegistry(ctx context.Context, workID string) bool {
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, nil, workID)
	if err != nil {
		return false
	}
	var step string
	if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		return false
	}
	return stepDeclaresAction(entry.Definition, step, "confirm_premise")
}

// ReadMutationWorkPinTxForRegistry is the registry-threaded opaque-Tx pin: a
// caller registry verifies the stored pin through itself with no builtin
// fallback, and a nil registry keeps the builtin convenience default. The
// absent/present distinction precedes the definition verification, so it
// answers identically under either registry.
func ReadMutationWorkPinTxForRegistry(ctx context.Context, transaction *Transaction, registry DefinitionRegistry, workID string, ownEventIDs []string) (WorkPin, bool, error) {
	tx, err := transactionSQL(transaction, "work_pin")
	if err != nil {
		return WorkPin{}, false, err
	}
	return readMutationWorkPinForRegistryTx(ctx, tx, registry, workID, ownEventIDs)
}

func readMutationWorkPinForRegistryTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, workID string, ownEventIDs []string) (WorkPin, bool, error) {
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "work_pin"); err != nil {
		return WorkPin{}, false, err
	}
	identity := WorkPin{WorkID: workID}
	err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&identity.Version)
	var instance bool
	switch err {
	case sql.ErrNoRows:
		removed := false
		for _, eventID := range ownEventIDs {
			var expected int64
			err := tx.QueryRowContext(ctx, `SELECT expected_version FROM work_removal_operations WHERE work_id=? AND event_id=? AND state='committed'`, workID, eventID).Scan(&expected)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return WorkPin{}, false, err
			}
			identity.Version = expected + 1
			removed = true
			break
		}
		if !removed {
			return WorkPin{}, false, newFailure(KindProjectionNotFound, "work_pin", "work item is not recorded", false, "reread_entities")
		}
	case nil:
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=?)`, workID).Scan(&instance); err != nil {
			return WorkPin{}, false, err
		}
	default:
		return WorkPin{}, false, err
	}
	if !instance {
		disposition, err := outsideRepairDispositionTx(ctx, tx, workID)
		if err != nil {
			return WorkPin{}, false, err
		}
		if disposition == nil || disposition.State == OutsideRepairStateResumed {
			return identity, false, nil
		}
	}
	pin, err := readWorkPinForRegistryTx(ctx, tx, workID, registry)
	return pin, true, err
}

// ReadWorkPinTx derives a pin from the transaction supplied by its caller.
// Callers that hold a mutation transaction must use this function. The read
// runs in named phases — identity, instance, step intents, contract,
// attempt, recovery intents — each of which owns its own refusals; this
// function owns their order and the two early exits the phases cannot
// express (the contract-ambiguity escape hatch and the terminal-state
// intent selection). It is the builtin convenience: the instance's stored
// pin verifies against the builtin registry.
func ReadWorkPinTx(ctx context.Context, tx *sql.Tx, workID string) (WorkPin, error) {
	return readWorkPinForRegistryTx(ctx, tx, workID, nil)
}

// readWorkPinForRegistryTx is the registry-threaded pin core: a caller
// registry verifies the actual stored pin through itself with no builtin
// fallback, and every phase below resolves the same verified definition.
func readWorkPinForRegistryTx(ctx context.Context, tx *sql.Tx, workID string, registry DefinitionRegistry) (WorkPin, error) {
	var pin WorkPin
	if tx == nil {
		return pin, newFailure(KindUnavailable, "work_pin", "transaction is not open", false, "supply an active read transaction")
	}
	if len(workID) < 2 || len(workID) > 128 {
		return pin, newFailure(KindInvalidOperation, "work_pin", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	if err := workPinReadIdentityTx(ctx, tx, workID, &pin); err != nil {
		return pin, err
	}
	if held, err := workPinOutsideRepairDispositionTx(ctx, tx, workID, &pin); err != nil {
		return pin, err
	} else if held {
		// A hold must remain readable even when the workflow pin is broken or
		// absent. Read raw identity only; outside repair grants no pin authority.
		err := tx.QueryRowContext(ctx, `SELECT definition_ref,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.WorkflowType, &pin.Step)
		if err != nil && err != sql.ErrNoRows {
			return pin, err
		}
		return pin, nil
	}
	registered, instanceState, err := workPinReadInstanceForRegistryTx(ctx, tx, workID, &pin, registry)
	if err != nil {
		return pin, err
	}
	// The pin reads; it does not adjudicate. A work item whose projection
	// carries duplicate active contracts must stay readable, because the
	// operator reaches the recovery that retires them through this pin. A
	// strict selection here would make the ambiguity permanent.
	//
	// Every enrichment below this point resolves the one approved contract,
	// and an ambiguous projection has none. The pin therefore reports where
	// the work item stands and the one route that repairs it, and stops.
	activeContractVersions, contractErr := activeWorkflowContractVersions(ctx, tx, workID)
	if contractErr != nil {
		return pin, contractErr
	}
	if len(activeContractVersions) > 1 {
		// Duplicate recovery belongs to earlier, running steps. At complete,
		// the one-active-contract gate refuses it, a terminal instance
		// cannot run any earlier-step recovery, and a completed instance
		// keeps the route closed on every shape but the complete-step
		// correction admission. The advertisement is the shared admission's
		// answer, not a per-site predicate: the loader folds the duplicated
		// projection, and workflowAdmitSupersede classifies the recovery, so
		// the pin, the preflight, and the fold answer identically for the
		// same state.
		state, stateErr := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, registered.Definition, pin.Step, "work_pin", registry)
		if stateErr != nil {
			return pin, stateErr
		}
		if decision := workflowAdmit(registered.Definition, state, "supersede_contract"); decision.Admitted {
			pin.NextValidIntents = []WorkPinIntent{workPinIntentForAction(workflowDecisionFactRecoveryActionDefinition(registered.Definition), pin.Version, "operator_contract_correction")}
		}
		return pin, nil
	}
	var activeContractVersion int64
	if len(activeContractVersions) == 1 {
		activeContractVersion = activeContractVersions[0]
	}
	state, err := workPinStepIntentsTx(ctx, tx, workID, &pin, registered.Definition, registry)
	if err != nil {
		return pin, err
	}
	if err := workPinReadContractTx(ctx, tx, workID, &pin, registered.Definition, instanceState, activeContractVersion, state); err != nil {
		return pin, err
	}
	if err := workPinRecoveryIntentsTx(ctx, tx, workID, &pin, registered.Definition, instanceState, state); err != nil {
		return pin, err
	}
	if err := workPinReadWorkContextTx(ctx, tx, workID, &pin); err != nil {
		return pin, err
	}
	return pin, nil
}

// workPinReadWorkContextTx uses the caller's transaction, never the pooled connection.
func workPinReadWorkContextTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) error {
	view, unavailable, err := readWorkContextProjection(ctx, tx, workID)
	pin.WorkContext, pin.WorkContextUnavailable = view, unavailable
	return err
}

// workPinOutsideRepairDispositionTx populates the pin's outside-repair
// disposition fields and overrides the advertised intents with the boundary
// route when the disposition is active. The active flag returns true so
// the caller returns the pin as-is and skips the managed workflow path.
func workPinOutsideRepairDispositionTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) (bool, error) {
	disposition, err := outsideRepairDispositionTx(ctx, tx, workID)
	if err != nil {
		return false, err
	}
	pin.OutsideRepairDisposition = disposition
	if disposition == nil || disposition.State == OutsideRepairStateResumed {
		return false, nil
	}
	if disposition.State == OutsideRepairStateActive {
		pin.OutsideRepairRoute = outsideRepairRouteNames()
	}
	pin.NextValidIntents = []WorkPinIntent{}
	return true, nil
}

// workPinReadIdentityTx fills the pin's item identity: version, lifecycle,
// title, Linear key, primary project, the closed-instance count, the
// watermark, and the driving sessions. The watermark belongs on every pin
// this read returns with a nil error, so it is assigned before any phase
// that can return partial data.
func workPinReadIdentityTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) error {
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "work_pin"); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT w.version,w.lifecycle,w.title,COALESCE(l.human_key,''),COALESCE(p.id,''),COALESCE(p.display_name,'') FROM work_items w LEFT JOIN linear_issue_links l ON l.work_id=w.id LEFT JOIN work_projects wp ON wp.work_id=w.id AND wp.role='primary' LEFT JOIN projects p ON p.id=wp.project_id WHERE w.id=?`, workID).Scan(&pin.Version, &pin.Lifecycle, &pin.Title, &pin.LinearIssueKey, &pin.ProjectID, &pin.ProjectDisplayName); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "work_pin", "work item is not recorded", false, "reread_entities")
		}
		return wrapFailure(KindUnavailable, "work_pin", "cannot read work item", true, "retry once the database is readable", err)
	}
	pin.WorkID = workID
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_instances wi INDEXED BY workflow_instances_state JOIN work_items w ON w.id=wi.work_id JOIN work_projects wp ON wp.work_id=w.id WHERE wi.instance_state='cancelled' AND w.lifecycle='completed' AND wp.project_id=?`, pin.ProjectID).Scan(&pin.CancelledInstanceCloses); err != nil {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read cancelled-instance lifecycle closes", true, "retry once the database is readable", err)
	}
	watermark, err := readWorkEffectSeq(ctx, tx, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read work watermark", true, "retry once the database is readable", err)
	}
	pin.Watermark = "seq:" + strconv.FormatInt(watermark, 10)
	sessions, err := workPinDrivingSessionsTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	pin.DrivingSessions = sessions
	return nil
}

// workPinReadInstanceForRegistryTx is the registry-threaded instance phase:
// a nil registry keeps the builtin convenience verification, and a caller
// registry verifies the actual stored pin through itself with no builtin
// fallback.
func workPinReadInstanceForRegistryTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, registry DefinitionRegistry) (RegisteredDefinition, string, error) {
	var definition WorkflowReadDefinition
	var instanceState string
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_digest,current_step,instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&definition.Ref, &definition.Digest, &pin.Step, &instanceState); err != nil {
		if err == sql.ErrNoRows {
			return RegisteredDefinition{}, "", newFailure(KindProjectionNotFound, "work_pin", "workflow instance is not recorded", false, "reread_entities")
		}
		return RegisteredDefinition{}, "", wrapFailure(KindUnavailable, "work_pin", "cannot read workflow instance", true, "retry once the database is readable", err)
	}
	registered, err := verifyReadWorkflowDefinitionForRegistry(definition, registry)
	if err != nil {
		return RegisteredDefinition{}, "", err
	}
	definition = WorkflowReadDefinition{Ref: registered.Definition.Ref, Digest: registered.Digest}
	pin.WorkflowType = definition.Ref
	pin.WorkflowDefinitionDigest = &definition.Digest
	// The obligation list uses the verified registered definition — the same
	// verification and the same collector the architecture-binding admission
	// reads — so the pin and admission cannot disagree on declared membership.
	// The pointer stays non-nil even for an empty membership, so a managed pin
	// always carries the obligations key.
	obligations := workflowDefinitionObligationIDs(registered.Definition)
	pin.Obligations = &obligations
	return registered, instanceState, nil
}

// verifyReadWorkflowDefinitionForRegistry verifies one read definition
// through the existing pin verifier: a nil registry keeps the builtin
// convenience and its historical refusal shapes, and an explicit registry
// gets VerifyWorkflowDefinitionPin's complete digest verification with no
// builtin fallback. There is no second resolver.
func verifyReadWorkflowDefinitionForRegistry(definition WorkflowReadDefinition, registry DefinitionRegistry) (RegisteredDefinition, error) {
	if registry == nil {
		return verifyReadWorkflowDefinition(definition)
	}
	return resolveCurrentWorkflowDefinition(registry, definition.Ref)
}

// workPinStepIntentsTx fills the pin's step intents and returns the folded
// admission state the later intent phases consume. The whole admission is
// the shared derivation — the tx-scoped loader folds the instance history
// once, and workflowAdmit decides each advance — so the pin and the delivery
// guard answer identically for the same state instead of re-deriving the
// conditions per site. The pin never offers an advance the shared admission
// refuses.
func workPinStepIntentsTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, definition WorkflowDefinition, registry DefinitionRegistry) (WorkflowAdmissionState, error) {
	state, stateErr := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, definition, pin.Step, "work_pin", registry)
	if stateErr != nil {
		return WorkflowAdmissionState{}, stateErr
	}
	pin.Review = state.Review
	pin.NextValidIntents = workPinIntents(definition, pin.Step, pin.Version)
	kept := pin.NextValidIntents[:0:0]
	for _, intent := range pin.NextValidIntents {
		decision := workflowAdmit(definition, state, intent.ActionID)
		if !decision.Admitted {
			continue
		}
		kept = append(kept, intent)
	}
	pin.NextValidIntents = kept
	if state.LateCriterionAssociation && !workPinContainsAction(pin.NextValidIntents, "record_verdict") {
		if decision := workflowAdmit(definition, state, "record_verdict"); decision.Admitted {
			pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowActionDefinitionByID(definition, "record_verdict"), pin.Version, "late_criterion_association"))
		}
	}
	// The evidence-binding recovery advertises only when the pure decision
	// admits the binding: a staleness or terminal refusal keeps the route
	// hidden instead of advertising a dead one.
	if state.EvidenceRecoveryRoute && !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		if decision := workflowAdmit(definition, state, "bind_evidence"); decision.Admitted {
			pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowActionDefinitionByID(definition, "bind_evidence"), pin.Version, "evidence_binding_recovery"))
		}
	}
	return state, nil
}

// workPinReadContractTx reads the one active contract and every enrichment
// that hangs off it: predicates, verified criteria on a completed instance,
// self-repair, the late-verdict recovery, and
// the verdict evidence. A work item with no contract row skips the block.
// The late-verdict recovery's availability is the folded admission state's
// answer, not a per-site re-derivation.
func workPinReadContractTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, definition WorkflowDefinition, instanceState string, activeContractVersion int64, state WorkflowAdmissionState) error {
	var contract WorkflowReadContract
	var required, routes, mandate, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT contract_version,premise,required_evidence,route_conventions,spec_mandate,law_modifies,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, activeContractVersion).Scan(&contract.Version, &contract.Premise, &required, &routes, &mandate, &modifies, &contract.RigorClass); err == nil {
		if jsonErr := decodeWorkflowPinContract(&contract, required, routes, mandate, modifies); jsonErr != nil {
			return jsonErr
		}
		kinds, evidenceErr := workPinOutstandingEvidenceKindsTx(ctx, tx, workID, definition, state.EvidenceRecovery)
		if evidenceErr != nil {
			return evidenceErr
		}
		pin.OutstandingEvidenceKinds = kinds
		predicates, predicateErr := readWorkflowContractPredicates(ctx, tx, workID, contract.Version)
		if predicateErr != nil {
			return predicateErr
		}
		contract.OutcomePredicates = predicates
		if pin.Lifecycle == "completed" && instanceState == "completed" {
			criteria, criteriaErr := workPinVerifiedCriteriaTx(ctx, tx, workID, contract)
			if criteriaErr != nil {
				return criteriaErr
			}
			pin.VerifiedCriteria = criteria
		}
		if stepDeclaresAction(definition, pin.Step, "record_verdict") || state.LateCriterionAssociation {
			verdictEvidence, evidenceErr := workPinVerdictEvidenceTx(ctx, tx, workID)
			if evidenceErr != nil {
				return evidenceErr
			}
			pin.VerdictEvidence = verdictEvidence
		}
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read workflow contract", true, "retry once the database is readable", err)
	}
	return nil
}

// workPinRecoveryIntentsTx completes the intent set with every recovery and
// correction route the folded admission state admits: worker-failure and
// contract correction, the delivery-gate correction request,
// the rejected-worker-result correction, and the terminal-state
// selections that close the set. Every append consumes the pure decision,
// so a route the folded state owns but another folded condition refuses —
// staleness, terminal immutability, the hold — stays hidden.
func workPinRecoveryIntentsTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, definition WorkflowDefinition, instanceState string, state WorkflowAdmissionState) error {
	admits := func(actionID string) bool {
		decision := workflowAdmit(definition, state, actionID)
		return decision.Admitted
	}
	// The contract recovery advertises exactly the pure decision's answer:
	// the stale-law and duplicate recoveries the fold admits at a running
	// step, and the ordinary correction admission, but never a terminal,
	// off-shape, or gate-refused state. The correction checkpoint's own
	// answer is not the advertisement's gate — the stale-law recovery stays
	// offered at a step whose correction checkpoint refuses.
	if !workPinContainsAction(pin.NextValidIntents, "supersede_contract") && admits("supersede_contract") {
		pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowDecisionFactRecoveryActionDefinition(definition), pin.Version, "operator_contract_correction"))
	}
	if err := workPinCorrectionRequestIntentsTx(ctx, tx, workID, pin, definition, state); err != nil {
		return err
	}
	// A completed instance with nonterminal work retains only the admitted
	// complete-step contract correction. Every other action remains immutable.
	if !isTerminalLifecycle(instanceState) {
		return nil
	}
	if instanceState == "completed" && admits("supersede_contract") {
		pin.NextValidIntents = []WorkPinIntent{workPinIntentForAction(workflowDecisionFactRecoveryActionDefinition(definition), pin.Version, "operator_contract_correction")}
	} else {
		pin.NextValidIntents = []WorkPinIntent{}
	}
	return nil
}

// workPinCorrectionRequestIntentsTx applies the correction-request routes
// over the folded admission state: the delivery gate replaces
// request_correction with its own bounded intent, and a
// verification correction is advertised once. The correction context read below
// carries the pin's correction display; the admission decisions read the
// folded state.
func workPinCorrectionRequestIntentsTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, definition WorkflowDefinition, state WorkflowAdmissionState) error {
	decision := workflowAdmit(definition, state, "request_correction")
	admitted := decision.Admitted
	if workflowStepIsDeliveryGate(workflowStep(definition, pin.Step)) {
		// The gate's corrective return carries one reason code on every
		// pinned shape, and it is advertised only when the folded admission
		// proves the route and the pure
		// decision admits the return.
		pin.NextValidIntents = workPinWithoutAction(pin.NextValidIntents, "request_correction")
		if state.CorrectionRequestRecovery && admitted {
			pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowCorrectionRequestActionDefinition(), pin.Version, "delivery_gate_correction"))
		}
	} else if state.CorrectionRequestRecovery && admitted && !workPinContainsAction(pin.NextValidIntents, "request_correction") {
		pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowCorrectionRequestActionDefinition(), pin.Version, "verification_correction"))
	}
	correction, correctionErr := readWorkflowCorrectionContext(ctx, tx, workID)
	if correctionErr != nil {
		return correctionErr
	}
	pin.Correction = correction
	return nil
}

func workPinVerifiedCriteriaTx(ctx context.Context, tx *sql.Tx, workID string, contract WorkflowReadContract) ([]WorkPinVerifiedCriterion, error) {
	verdicts, err := latestWorkflowVerdicts(ctx, tx, workID, contract.Version)
	if err != nil {
		return nil, err
	}
	verdictByPredicate := make(map[string]workflowVerdictRecordedPayload, len(verdicts))
	for _, verdict := range verdicts {
		verdictByPredicate[verdict.PredicateID] = verdict
	}
	criteria := make([]WorkPinVerifiedCriterion, 0, len(contract.OutcomePredicates))
	for _, predicate := range contract.OutcomePredicates {
		verdict, ok := verdictByPredicate[predicate.PredicateID]
		if !ok || !json.Valid([]byte(predicate.OutcomePayload)) {
			return nil, nil
		}
		criteria = append(criteria, WorkPinVerifiedCriterion{
			PredicateID:    predicate.PredicateID,
			Ordinal:        predicate.Ordinal,
			OutcomeKind:    predicate.OutcomeKind,
			OutcomePayload: json.RawMessage(predicate.OutcomePayload),
			VerdictKind:    verdict.VerdictKind,
		})
	}
	return criteria, nil
}

// workPinDrivingSessionsTx returns the bounded set of agent sessions that have
// driven the work item, most recent first. The execution actor preserves the
// session that initialized the workflow, while current action facts identify later
// coordinator sessions that took over the item.
func workPinDrivingSessionsTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorkPinDrivingSession, error) {
	rows, err := tx.QueryContext(ctx, workPinDrivingSessionsSQL, workPinDrivingSessionsArgs(workID)...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot read driving coordinator sessions", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	return scanWorkPinDrivingSessions(rows)
}

func workPinDrivingSessionsArgs(workID string) []any {
	return []any{workID, string(ActorAgent), workID, workID}
}

const workPinDrivingSessionsSQL = `
		SELECT a.session_ref,
		       COALESCE(f.last_action_id,'workflow.actor_recorded'),
		       COALESCE(f.last_acted_at,a.first_seen_at) AS last_acted_at
		FROM workflow_actors a
		LEFT JOIN workflow_coordinator_actions f ON f.work_id=? AND f.actor_ref=a.actor_ref
		WHERE a.actor_class=?
		  AND (
			 a.actor_ref=(SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?)
			 OR a.actor_ref IN (
				SELECT actor_ref FROM workflow_coordinator_actions WHERE work_id=?
			 )
		  )
		ORDER BY last_acted_at DESC,a.session_ref
		LIMIT 17`

func scanWorkPinDrivingSessions(rows *sql.Rows) ([]WorkPinDrivingSession, error) {
	sessions := make([]WorkPinDrivingSession, 0, 4)
	for rows.Next() {
		var session WorkPinDrivingSession
		if err := rows.Scan(&session.SessionRef, &session.LastActionID, &session.LastActedAt); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_pin", "cannot scan driving coordinator session", true, "retry once the database is readable", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot enumerate driving coordinator sessions", true, "retry once the database is readable", err)
	}
	if len(sessions) > 16 {
		return nil, newFailure(KindLimitExceeded, "work_pin", "driving coordinator sessions exceed the pin bound", false, "reduce the number of active coordinator sessions")
	}
	return sessions, nil
}

// workPinVerdictEvidenceTx reads the distinct bound evidence pairs for a
// work item, ordered deterministically. The set is read-only; binding stays
// owned by the workflow actions that mint evidence events.
func workPinVerdictEvidenceTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorkPinEvidence, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT json_extract(payload,'$.evidence_kind') AS evidence_kind, json_extract(payload,'$.immutable_subject_ref') AS immutable_subject_ref FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref') IS NOT NULL ORDER BY immutable_subject_ref, evidence_kind LIMIT 100`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot read bound verdict evidence", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []WorkPinEvidence{}
	for rows.Next() {
		var entry WorkPinEvidence
		if scanErr := rows.Scan(&entry.EvidenceKind, &entry.ImmutableSubjectRef); scanErr != nil {
			return nil, wrapFailure(KindUnavailable, "work_pin", "cannot scan bound verdict evidence", true, "retry once the database is readable", scanErr)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot iterate bound verdict evidence", true, "retry once the database is readable", err)
	}
	return out, nil
}

func decodeWorkflowPinContract(contract *WorkflowReadContract, required, routes, mandate, modifies string) error {
	if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandate), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
		return newFailure(KindInvariantViolation, "work_pin", "workflow contract projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	contract.RequiredEvidence = nonNilStrings(contract.RequiredEvidence)
	contract.RouteConventions = nonNilStrings(contract.RouteConventions)
	contract.SpecMandate = nonNilStrings(contract.SpecMandate)
	contract.LawModifies = nonNilStrings(contract.LawModifies)
	return nil
}

// workPinIntents lists the actions the current step offers, the pure shape
// read. The per-intent workflowAdmit decision in workPinStepIntentsTx drops
// what the folded state refuses — a dispatched worker's hold hides every
// advancing exit except accept_worker_result (CD-0133 D4) — so the hold is
// the shared admission's answer, never a per-site predicate here.
func workPinIntents(definition WorkflowDefinition, stepID string, version int64) []WorkPinIntent {
	step := workflowStep(definition, stepID)
	if step == nil {
		return []WorkPinIntent{}
	}
	actions := make(map[string]WorkflowActionDefinition, len(definition.ActionDefinitions))
	for _, action := range definition.ActionDefinitions {
		actions[action.ID] = action
	}
	intents := make([]WorkPinIntent, 0, len(step.Actions))
	for _, actionID := range step.Actions {
		action, ok := actions[actionID]
		if !ok {
			continue
		}
		// The approve intent advertises the pinned declaration's selected
		// shape; an unusable pinned initial declaration is never advertised.
		if actionID == "approve_contract" && workflowDeclaresDecisionDomains(definition) {
			selected, err := workflowDecisionFactInitialActionDefinition(definition)
			if err != nil {
				continue
			}
			action = selected
		}
		payload := publicWorkflowActionPayload(action)
		fields := make([]string, 0, len(payload.Fields))
		for _, field := range payload.Fields {
			if field.Required {
				fields = append(fields, field.Name)
			}
		}
		intents = append(intents, WorkPinIntent{Tool: "concord_work_transition", Operation: "workflow_action", ReasonCode: "declared_step_action", ActionID: action.ID, RequiredFields: fields, ExpectedVersion: version})
	}
	if stepDeclaresAction(definition, stepID, "confirm_premise") && workflowContractCorrectionCheckpoint(definition, stepID) {
		intents = append(intents, workPinIntentForAction(workflowDecisionFactRecoveryActionDefinition(definition), version, "operator_contract_correction"))
	}
	return intents
}

func workPinContainsAction(intents []WorkPinIntent, actionID string) bool {
	for _, intent := range intents {
		if intent.ActionID == actionID {
			return true
		}
	}
	return false
}

func workPinWithoutAction(intents []WorkPinIntent, actionID string) []WorkPinIntent {
	filtered := make([]WorkPinIntent, 0, len(intents))
	for _, intent := range intents {
		if intent.ActionID != actionID {
			filtered = append(filtered, intent)
		}
	}
	return filtered
}

func workPinIntentForAction(action WorkflowActionDefinition, version int64, reason string) WorkPinIntent {
	payload := publicWorkflowActionPayload(action)
	fields := make([]string, 0, len(payload.Fields))
	for _, field := range payload.Fields {
		if field.Required {
			fields = append(fields, field.Name)
		}
	}
	return WorkPinIntent{Tool: "concord_work_transition", Operation: "workflow_action", ReasonCode: reason, ActionID: action.ID, RequiredFields: fields, ExpectedVersion: version}
}

func publicWorkflowActionPayload(action WorkflowActionDefinition) WorkflowPayloadDefinition {
	if action.PublicPayload != nil {
		return *action.PublicPayload
	}
	if policy, ok := builtinActionPolicies[action.ID]; ok && policy.PublicPayload != nil {
		return *policy.PublicPayload
	}
	return action.Payload
}

// workflowActionDefinitionByID returns the pinned definition's action, or a bare
// action carrying the ID when the definition omits it.
func workflowActionDefinitionByID(definition WorkflowDefinition, actionID string) WorkflowActionDefinition {
	for _, action := range definition.ActionDefinitions {
		if action.ID == actionID {
			return action
		}
	}
	return WorkflowActionDefinition{ID: actionID}
}

func workPinOutstandingEvidenceKindsTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, recovery *workflowEvidenceBindingRecoveryState) (*[]string, error) {
	var requirements []workflowEvidenceRequirement
	if recovery != nil {
		if recovery.Refusal != nil {
			return nil, nil
		}
		requirements = recovery.Requirements
	}
	if requirements == nil {
		required, mandates, obligations, cutoff, err := workflowEvidenceRequirementInputs(ctx, q, workID)
		if err != nil {
			return nil, err
		}
		requirements, err = outstandingWorkflowEvidenceRequirements(ctx, q, workID, required, mandates, definition, obligations, cutoff)
		if err != nil {
			return nil, err
		}
	}
	var kinds []string
	for _, requirement := range requirements {
		kinds = append(kinds, requirement.Kind)
	}
	kinds = uniqueSorted(kinds)
	return &kinds, nil
}
