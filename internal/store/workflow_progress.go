package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

// workflowProgressSatisfaction preserves the derived facts behind one
// qualified coordinator satisfaction: the stable obligation identity, the
// recorded requirement, the immutable subject, the validating authority, and
// the supporting evidence identities (CD-0164 D2a/D5). A satisfaction is
// never reduced to a bare renewal flag.
type workflowProgressSatisfaction struct {
	Seq           int64
	Obligation    string
	Qualification string
	Requirement   string
	Subject       string
	Authority     string
	EvidenceIDs   []string
}

// workflowProgressExecution is one recorded execution run inside the walked
// prefix: a typed action start's executing actor with its readback model.
// Evaluator independence reads this history; the live instance executor is
// never consulted to qualify a historical prefix.
type workflowProgressExecution struct {
	seq   int64
	actor string
	model string
}

// The correction walk owns both boundaries. A qualified coordinator
// satisfaction never changes the worker recovery boundary or discharges debt.
type workflowProgressHistory struct {
	definition    WorkflowDefinition
	contract      workflowContractApprovedPayload
	seen          map[string]bool
	alias         map[string]string
	bound         map[string]bool
	executions    []workflowProgressExecution
	cutoff        int64
	satisfactions []workflowProgressSatisfaction
	qualification string
	obligation    string
	prefix        int64
}

func newWorkflowProgressHistory() workflowProgressHistory {
	return workflowProgressHistory{seen: make(map[string]bool), alias: make(map[string]string), bound: make(map[string]bool)}
}

// aliasFind returns one obligation identity's family representative. The
// graph is derived wholly from the walked prefix, so repeated walks of one
// immutable prefix rebuild it identically.
func (p *workflowProgressHistory) aliasFind(identity string) string {
	for {
		parent, ok := p.alias[identity]
		if !ok || parent == identity {
			return identity
		}
		identity = parent
	}
}

// aliasUnion joins two obligation identities into one equivalence family.
func (p *workflowProgressHistory) aliasUnion(first, second string) {
	firstRoot, secondRoot := p.aliasFind(first), p.aliasFind(second)
	if firstRoot == secondRoot {
		return
	}
	if secondRoot < firstRoot {
		firstRoot, secondRoot = secondRoot, firstRoot
	}
	p.alias[secondRoot] = firstRoot
}

// aliasFamily returns every identity in the families of the given
// identities, including the given identities themselves, in sorted order.
func (p *workflowProgressHistory) aliasFamily(identities ...string) []string {
	roots := make(map[string]bool, len(identities))
	for _, identity := range identities {
		roots[p.aliasFind(identity)] = true
	}
	family := make([]string, 0, len(identities))
	for member := range p.alias {
		if roots[p.aliasFind(member)] {
			family = append(family, member)
		}
	}
	for _, identity := range identities {
		if !slices.Contains(family, identity) {
			family = append(family, identity)
		}
	}
	slices.Sort(family)
	return family
}

// retainPredicateAliases carries obligation equivalence across a contract
// installation: each recorded predicate's stable ID and its canonical
// payload digest join one alias family, so a spent lineage stays spent
// through unsatisfied intermediate contracts and a later satisfaction of any
// member spends the whole family. An install itself grants no progress.
func (p *workflowProgressHistory) retainPredicateAliases() {
	for _, predicate := range p.contract.OutcomePredicates {
		digest := workflowPredicatePayloadDigest(predicate)
		if predicate.PredicateID == "" || digest == "" {
			continue // a malformed recorded payload forms no stable identity
		}
		p.aliasUnion("predicate-id:"+predicate.PredicateID, "predicate-payload:"+digest)
	}
}

func (p *workflowProgressHistory) declares(kind string) bool {
	if p.definition.ProgressPolicy != WorkflowProgressPolicyObligationSatisfactionV1 {
		return false
	}
	for _, action := range p.definition.ActionDefinitions {
		if action.SatisfactionKind == kind {
			return true
		}
	}
	return false
}

// observeProgress consumes admitted typed semantic events, not action mode or
// caller claims. Their existing folds validate producer and actor authority.
// The only additional qualification is first satisfaction of a required
// obligation; no new event or independently persisted authority is created.
// Qualification reuses the shared semantic guards, which read the store, so
// the walk buffers its prefix before folding.
func (h *workflowCorrectionHistory) observeProgress(ctx context.Context, q queryer, workID string, row workflowCorrectionHistoryRow) error {
	p := &h.progress
	p.prefix = row.seq
	if h.boundary > h.nonprogressBoundary {
		h.nonprogressBoundary = h.boundary
		p.qualification, p.obligation = "satisfying_worker_acceptance", "worker_result"
	}
	switch row.kind {
	case WorkflowDefinitionSelected:
		var selected workflowDefinitionSelectedPayload
		if err := json.Unmarshal([]byte(row.payload), &selected); err != nil {
			return err
		}
		// The recorded selection verifies through the walk's authoritative
		// registry, fail closed: no builtin retry, no silent legacy
		// downgrade. A worker-satisfaction-only walk never resolves here.
		registry := h.registry
		if registry == nil {
			registry = BuiltinWorkflowRegistry()
		}
		entry, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: selected.Ref, Version: selected.Version, Digest: selected.Digest})
		if err != nil {
			return err
		}
		p.definition = entry.Definition
	case WorkflowContractApproved:
		if err := json.Unmarshal([]byte(row.payload), &p.contract); err != nil {
			return err
		}
		p.retainPredicateAliases()
	case WorkflowContractSuperseded:
		var successor workflowContractSupersededPayload
		if err := json.Unmarshal([]byte(row.payload), &successor); err != nil {
			return err
		}
		if successor.SuccessorContract != nil {
			p.contract = *successor.SuccessorContract
		}
		p.contract.ContractVersion = successor.NewContractVersion
		// The complete-step correction cutoff is a prefix fact: the
		// supersession that installs a route-declaring contract cuts every
		// predecessor binding off later requirement matching, exactly like
		// workflowCompleteStepCorrectionEvidenceCutoff reads it from the
		// ancestry.
		if slices.Contains(p.contract.RouteConventions, workflowCompleteStepCorrectionRoute) && row.seq > p.cutoff {
			p.cutoff = row.seq
		}
		p.retainPredicateAliases()
	case WorkflowActionStarted:
		var started workflowActionStartedPayload
		if err := json.Unmarshal([]byte(row.payload), &started); err != nil {
			return err
		}
		// The evaluation's own start records the evaluator, not the executor;
		// every other typed start records a run the verdict must be distinct
		// from. Predicate-satisfying actions are exactly the evaluation runs.
		if started.ActorRef == "" || workflowActionSatisfactionKind(started.ActionID) == WorkflowSatisfactionPredicate {
			return nil
		}
		for _, execution := range p.executions {
			if execution.actor == started.ActorRef && execution.model == started.ExecutionModel {
				return nil
			}
		}
		p.executions = append(p.executions, workflowProgressExecution{seq: row.seq, actor: started.ActorRef, model: started.ExecutionModel})
	case WorkflowEvidenceBound:
		var evidence workflowEvidenceBoundPayload
		if err := json.Unmarshal([]byte(row.payload), &evidence); err != nil {
			return err
		}
		// These identities are mandatory in the admitted evidence fold. An
		// incomplete historical record cannot become qualified by inference.
		if evidence.ImmutableSubjectRef == "" || evidence.ProducerID == "" || evidence.ProducerRunRef == "" || evidence.ProducerWatermark == "" || evidence.ObservedAt == "" {
			return nil
		}
		p.bound[evidence.ImmutableSubjectRef] = true
		return h.qualifyEvidence(ctx, q, workID, row.seq, evidence)
	case WorkflowVerdictRecorded:
		var verdict workflowVerdictRecordedPayload
		if err := json.Unmarshal([]byte(row.payload), &verdict); err != nil {
			return err
		}
		return h.qualifyPredicate(ctx, q, workID, row.seq, verdict)
	case WorkflowPremiseConfirmed:
		var confirmed workflowPremiseConfirmedPayload
		if err := json.Unmarshal([]byte(row.payload), &confirmed); err != nil {
			return err
		}
		return h.qualifyPremise(ctx, q, workID, row.seq, confirmed)
	}
	return nil
}

// qualifyEvidence qualifies a required evidence obligation through the
// completion guards' own semantics: the requirement set is
// workflowEvidenceRequirementDefinitions over the walked contract, and the
// match, including the producer completed-operation rule, is
// unboundWorkflowEvidenceRequirements at the walked cutoff. This fold adds
// only the first-satisfaction lineage and the debt rule.
func (h *workflowCorrectionHistory) qualifyEvidence(ctx context.Context, q queryer, workID string, seq int64, evidence workflowEvidenceBoundPayload) error {
	p := &h.progress
	if !p.declares(WorkflowSatisfactionEvidence) || p.contract.ContractVersion == 0 {
		return nil
	}
	var obligations []WorkflowVerificationObligation
	if p.contract.ArchitectureBinding != nil {
		obligations = p.contract.ArchitectureBinding.VerificationObligations
	}
	for _, requirement := range workflowEvidenceRequirementDefinitions(p.contract.RequiredEvidence, p.contract.SpecMandate, p.definition, obligations) {
		identity := workflowEvidenceObligationKey(requirement)
		if p.seen[identity] {
			continue
		}
		outstanding, err := unboundWorkflowEvidenceRequirements(ctx, q, workID, []workflowEvidenceRequirement{requirement}, p.cutoff, seq+1)
		if err != nil {
			return err
		}
		if len(outstanding) != 0 {
			continue
		}
		// Attribution: only the binding that first satisfies the requirement
		// earns the record. A requirement an earlier binding already
		// satisfied — a late-added or reidentified requirement reading old
		// evidence — credits no later unrelated record, and the
		// complete-step cutoff stays in both reads.
		earlier, err := unboundWorkflowEvidenceRequirements(ctx, q, workID, []workflowEvidenceRequirement{requirement}, p.cutoff, seq)
		if err != nil {
			return err
		}
		if len(earlier) == 0 {
			continue
		}
		h.satisfyProgress(seq, "required_evidence_bound", workflowProgressSatisfaction{
			Seq: seq, Obligation: identity, Qualification: "required_evidence_bound",
			Requirement: identity, Subject: evidence.ImmutableSubjectRef,
			Authority: evidence.ProducerID, EvidenceIDs: []string{evidence.ImmutableSubjectRef},
		})
	}
	return nil
}

// workflowEvidenceObligationKey names one evidence requirement's stable
// satisfaction identity. Kind, mandate, and law-tuple requirements alias
// across contract supersessions by content, so re-binding the same required
// evidence mints no new credit.
func workflowEvidenceObligationKey(requirement workflowEvidenceRequirement) string {
	switch {
	case requirement.Kind != "" && requirement.Reference != "":
		return "obligation:" + requirement.Reference + "|" + requirement.Kind
	case requirement.Kind != "":
		return "evidence:" + requirement.Kind
	default:
		return "mandate:" + requirement.Reference
	}
}

// qualifyPredicate qualifies an approved predicate's verdict. Comparable
// payload, bound evidence, evaluator independence over the prefix execution
// history, and first-satisfaction lineage are all required; an unsupported or
// incomparable verdict, a self-evaluation, and a renamed or re-subjected
// obligation each qualify nothing.
func (h *workflowCorrectionHistory) qualifyPredicate(ctx context.Context, q queryer, workID string, seq int64, verdict workflowVerdictRecordedPayload) error {
	p := &h.progress
	if !p.declares(WorkflowSatisfactionPredicate) || verdict.ContractVersion != p.contract.ContractVersion || verdict.VerdictKind != "ok" || verdict.IncomparableWithApproved || verdict.VerdictActorRef == "" || len(verdict.EvaluationEvidence) == 0 {
		return nil
	}
	// Verdict evidence: every evaluation ref must equal an immutably bound
	// subject in this prefix (verifyVerdictEvidence's durable rule). The live
	// worktree-freshness check judges the current subject, not the prefix,
	// and is deliberately not applied to a historical satisfaction.
	for _, ref := range verdict.EvaluationEvidence {
		if !p.bound[ref] {
			return nil
		}
	}
	predicate, found := p.contractPredicate(verdict.PredicateID)
	if !found {
		return nil
	}
	// Evaluator authority over the prefix: an actor that dispatched a lane,
	// executed a step, or accepted a result within the walked prefix cannot
	// qualify its own delivery.
	worker, executed, err := workflowRecordedEvaluationFacts(ctx, q, workID, verdict.VerdictActorRef, p.prefix)
	if err != nil {
		return err
	}
	if worker || executed {
		return nil
	}
	verdictActor, recorded, err := progressActorTuple(ctx, q, verdict.VerdictActorRef)
	if err != nil {
		return err
	}
	if !recorded {
		return nil // an unrecorded verdict tuple cannot establish independence
	}
	verdictActor.Model = verdict.VerdictModel
	// Prefix actor/execution history owns the distinctness comparison, with
	// the model dimension the definition declares. The live instance executor
	// is never read to qualify a historical prefix.
	for _, execution := range p.executions {
		executing, recorded, err := progressActorTuple(ctx, q, execution.actor)
		if err != nil {
			return err
		}
		if !recorded {
			continue
		}
		executing.Model = execution.model
		if err := validateDistinctWorkflowActorsForReplay(executing, verdictActor, p.definition.EvaluatorIndependence.ModelDistinct); err != nil {
			return nil
		}
	}
	digest := workflowPredicatePayloadDigest(predicate)
	if digest == "" {
		return nil // a malformed recorded payload cannot qualify
	}
	// First-satisfaction lineage: the same predicate identity stays the same
	// obligation however its recorded subject changes, and a renamed identity
	// carrying a comparable payload aliases to it. Changing references,
	// subject, or identity itself buys no credit; a genuinely distinct
	// required obligation earns its own first satisfaction.
	h.satisfyProgress(seq, "comparable_predicate_satisfied", workflowProgressSatisfaction{
		Seq: seq, Obligation: "predicate-payload:" + digest, Qualification: "comparable_predicate_satisfied",
		Requirement: verdict.PredicateID, Subject: verdict.PredicateID,
		Authority: verdict.VerdictActorRef, EvidenceIDs: verdict.EvaluationEvidence,
	}, "predicate-id:"+verdict.PredicateID)
	return nil
}

func (p *workflowProgressHistory) contractPredicate(predicateID string) (workflowContractPredicatePayload, bool) {
	for _, predicate := range p.contract.OutcomePredicates {
		if predicate.PredicateID == predicateID {
			return predicate, true
		}
	}
	return workflowContractPredicatePayload{}, false
}

// workflowPredicatePayloadDigest hashes the canonical outcome kind and
// payload, so key reordering and whitespace do not move the identity.
func workflowPredicatePayloadDigest(predicate workflowContractPredicatePayload) string {
	var payload any
	if err := json.Unmarshal(predicate.OutcomePayload, &payload); err != nil {
		return ""
	}
	canonical, err := json.Marshal(struct {
		Kind    string
		Payload any
	}{predicate.OutcomeKind, payload})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// qualifyPremise qualifies a required premise confirmation with
// workflowPremiseConfirmedAuthorityTx semantics over recorded prefix facts:
// the walked contract's own decision fact classifies the confirmation, the
// claimed authority must be the one the fact licenses, and the confirming
// actor must hold the matching class without worker evaluation authority.
func (h *workflowCorrectionHistory) qualifyPremise(ctx context.Context, q queryer, workID string, seq int64, confirmed workflowPremiseConfirmedPayload) error {
	p := &h.progress
	if !p.declares(WorkflowSatisfactionPremise) || confirmed.ContractVersion != p.contract.ContractVersion || confirmed.ConfirmingActorRef == "" {
		return nil
	}
	authority := confirmed.ConfirmationAuthority
	if authority != "operator" && authority != "coordinator" {
		return nil
	}
	if p.definition.ApprovalPolicy != WorkflowApprovalPolicyConsequenceV1 {
		if authority != "operator" {
			return nil
		}
	} else {
		if p.contract.DecisionDomains == nil {
			return nil // absence is the legacy shape; nothing qualifies it
		}
		decision, err := ClassifyWorkflowApproval(p.definition, "confirm_premise", WorkflowApprovalFacts{ActiveDecisionDomains: p.contract.DecisionDomains})
		if err != nil {
			return nil // a malformed recorded fact qualifies nothing
		}
		if decision.Consent != WorkflowConsentNone && authority != "operator" {
			return nil
		}
		if decision.Consent == WorkflowConsentNone && authority != "coordinator" {
			return nil
		}
	}
	class := string(ActorOperator)
	if authority == "coordinator" {
		worker, _, err := workflowRecordedEvaluationFacts(ctx, q, workID, confirmed.ConfirmingActorRef, p.prefix)
		if err != nil {
			return err
		}
		if worker {
			return nil
		}
		class = string(ActorAgent)
	}
	recorded, err := progressActorClass(ctx, q, confirmed.ConfirmingActorRef, class)
	if err != nil {
		return err
	}
	if !recorded {
		return nil
	}
	// Premise identity is the semantic content of the required confirmation:
	// the approved premise text, the licensed authority, and the walked
	// contract's declared decision domains. A supersession that changes only
	// the contract version or references re-records the same premise and
	// mints no new credit; a genuinely changed premise is a distinct
	// obligation with its own first satisfaction. Version validity stays in
	// the gate above.
	domains := make([]string, 0, len(p.contract.DecisionDomains))
	domains = append(domains, p.contract.DecisionDomains...)
	slices.Sort(domains)
	canonical, err := json.Marshal(struct {
		Premise   string
		Authority string
		Domains   []string
	}{p.contract.Premise, authority, domains})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	h.satisfyProgress(seq, "required_premise_confirmed", workflowProgressSatisfaction{
		Seq: seq, Obligation: "premise-content:" + hex.EncodeToString(sum[:]), Qualification: "required_premise_confirmed",
		Requirement: "premise_confirmation", Subject: fmt.Sprintf("contract:v%d", confirmed.ContractVersion),
		Authority: authority + " by " + confirmed.ConfirmingActorRef,
	})
	return nil
}

func progressActorTuple(ctx context.Context, q queryer, actorRef string) (WorkflowActor, bool, error) {
	var actor WorkflowActor
	err := q.QueryRowContext(ctx, `SELECT actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&actor.ActorRef, &actor.PrincipalRef, &actor.ClientRef, &actor.AgentRef, &actor.SessionRef, &actor.ActorClass)
	if err == sql.ErrNoRows {
		return WorkflowActor{}, false, nil
	}
	if err != nil {
		return WorkflowActor{}, false, wrapFailure(KindUnavailable, "workflow_progress", "cannot read the workflow actor tuple", true, "retry once the workflow actor projection is readable", err)
	}
	return actor, true, nil
}

func progressActorClass(ctx context.Context, q queryer, actorRef, want string) (bool, error) {
	var class string
	err := q.QueryRowContext(ctx, `SELECT actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&class)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_progress", "cannot read the workflow actor class", true, "retry once the workflow actor projection is readable", err)
	}
	return class == want, nil
}

// satisfyProgress records the first qualified satisfaction of one obligation
// family, whether or not the budget can renew, and spends the whole family:
// the alias graph carries every equivalence the walked prefix holds, so a
// member satisfied under any recorded identity or payload spends them all,
// and a family holding a spent member can never renew again. A satisfaction
// recorded while failed-job debt or an open unconfirmed checks repair exists
// is spent: it cannot rebind after the debt clears to mint credit. Only the
// nonprogress window renews; the worker recovery boundary and the unresolved
// set never move here.
func (h *workflowCorrectionHistory) satisfyProgress(seq int64, qualification string, satisfaction workflowProgressSatisfaction, aliases ...string) {
	p := &h.progress
	for _, alias := range aliases {
		p.aliasUnion(satisfaction.Obligation, alias)
	}
	identities := append([]string{satisfaction.Obligation}, aliases...)
	family := p.aliasFamily(identities...)
	spent := false
	for _, member := range family {
		if p.seen[member] {
			spent = true
			break
		}
	}
	for _, member := range family {
		p.seen[member] = true
	}
	if spent {
		return
	}
	if len(h.unresolved) != 0 {
		return
	}
	for _, repair := range h.checksRepairs {
		if repair.openUnconfirmed() {
			return
		}
	}
	h.nonprogressBoundary = seq
	p.qualification, p.obligation = qualification, satisfaction.Obligation
	satisfaction.Qualification = qualification
	p.satisfactions = append(p.satisfactions, satisfaction)
}
