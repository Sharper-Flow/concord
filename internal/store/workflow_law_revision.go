package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

type workflowContractSupersessionContextKey struct{}

func workflowContractSupersessionContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, workflowContractSupersessionContextKey{}, true)
}

func inWorkflowContractSupersessionContext(ctx context.Context) bool {
	value, _ := ctx.Value(workflowContractSupersessionContextKey{}).(bool)
	return value
}

func workflowContractRecoveryActionDefinition() WorkflowActionDefinition {
	return WorkflowActionDefinition{
		ID: "supersede_contract", Consequence: ActionInternalSQLite, ExecutionMode: ActionAdvance,
		// The successor outcome shape — the successor supplies exactly one
		// outcome group — is engine-owned typed data resolved through
		// workflowActionCrossField("supersede_contract"), not digest-covered
		// definition content: core validation and the generated published
		// supersede variant derive their oneOf branches from that one
		// declaration.
		Payload: WorkflowPayloadDefinition{Closed: true, Fields: workflowContractRecoveryPayloadFields()},
	}
}

func workflowContractRecoveryPayloadFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionIntegerField("contract_version", true),
		actionArrayField("predecessor_contract_versions", false, 32, "workflow_contract_version"),
		actionPremiseField(),
		actionArrayField("outcome_predicates", false, 8, "workflow_action_outcome_predicates"),
		actionEnumField("outcome_kind", false, "exists", "absent", "outcome", "check"),
		actionObjectField("outcome_payload", false, "workflow_action_outcome"),
		actionEnumListField("required_evidence", true, 0, 7, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"),
		actionListField("route_conventions", true, 0, 16),
		actionLawListField("spec_mandate", true),
		actionLawListField("law_modifies", true),
		actionEnumField("rigor_class", true, "prototype_internal", "prototype_trusted", "prototype_public", "prototype_safety_critical", "production_internal", "production_trusted", "production_public", "production_safety_critical", "critical_internal", "critical_trusted", "critical_public", "critical_safety_critical"),
		actionStringField("supersede_reason", true, 4096),
		actionListField("audit_evidence", true, 1, 32),
		actionObjectField("architecture_binding", false, "architecture_binding"),
		actionObjectField("design_record", false, "workflow_design_content"),
	}
}

// workflowDecisionFactRecoveryActionDefinition selects the copied decision-fact
// declaration onto the engine-owned recovery definition for the pinned
// definition: a consequence_v1 successor contract declares the required
// decision_domains fact, and a legacy pin keeps the released recovery
// declaration byte-identical.
func workflowDecisionFactRecoveryActionDefinition(definition WorkflowDefinition) WorkflowActionDefinition {
	action := workflowContractRecoveryActionDefinition()
	action.Payload.Fields = workflowDecisionFactPayloadFields(definition, action.Payload.Fields)
	return action
}

// WorkflowLawContext is the bounded, typed resolution of the approved
// contract's binding law and Domain references. The core turns every
// contract-bound law ID and home or affected Domain ID into its projection
// state at continuity read time, so a dispatched packet carries the title of
// each binding law and a file locator that opens inside the Product's
// designated knowledge home, not inside whatever repository the lane was
// dispatched into.
type WorkflowLawContext struct {
	Laws    []WorkflowLawContextLaw    `json:"laws"`
	Domains []WorkflowLawContextDomain `json:"domains"`
	// RegistryPath is the knowledge home repository's absolute locator for
	// the Domain registry shard the home checkout actually carries, set when
	// the context binds at least one Domain. A dispatched lane holds no
	// Concord tool access (CD-0017 D4), so the path is how it reads Domain
	// structure: from the file, never from a tool call.
	RegistryPath string `json:"registry_path,omitempty"`
}

type WorkflowLawContextLaw struct {
	Roles  []string `json:"roles"`
	LawID  string   `json:"law_id"`
	Kind   string   `json:"kind,omitempty"`
	Status string   `json:"status,omitempty"`
	Title  string   `json:"title,omitempty"`
	// Path is the knowledge home repository's absolute locator for the law
	// document the projection recorded, so the lane opens it inside the home
	// checkout rather than its own dispatch repository.
	Path          string   `json:"path,omitempty"`
	ObligationIDs []string `json:"obligation_ids,omitempty"`
	// Criteria lists the law's acceptance criteria bound to the reading
	// work item's own outcome predicates (CD-0180). Bindings naming another
	// work item, and scenario or exemption bindings, never surface here:
	// the law context carries the chaining this work item owes, not the
	// law's whole binding table.
	Criteria []WorkflowLawContextCriterionBinding `json:"criteria,omitempty"`
}

// WorkflowLawContextCriterionBinding is one acceptance criterion of the law
// that the reading work item's own outcome predicate discharges. The reading
// work ID is implicit: every entry the law context carries binds the
// dispatching work item, so the packet renders criterion and predicate only.
type WorkflowLawContextCriterionBinding struct {
	Criterion   int    `json:"criterion"`
	PredicateID string `json:"predicate_id"`
}

type WorkflowLawContextDomain struct {
	DomainID string `json:"domain_id"`
	Name     string `json:"name"`
	Purpose  string `json:"purpose"`
}

// Role vocabulary for the law context. The sources overlap — a modified law
// is also mandated, and a mandated or modified law may carry verification
// obligations — so each law carries every role its contract binds, sorted and
// deduplicated, not one most-specific winner.
const (
	lawContextRoleMandated   = "mandated"
	lawContextRoleModified   = "modified"
	lawContextRoleAdded      = "added"
	lawContextRoleObligation = "obligation"
)

// readWorkflowLawContext resolves the approved contract's bound law and
// Domain references against the verified committed law folder and Domain
// registry inside the caller's transaction. It returns nil when the contract
// binds no law and no Domain. Missing own-home laws retain their contract
// identity and roles with status absent, except reserved additions, which
// carry identity and roles only. Foreign-home references and missing Domains
// refuse. Every advertised file locator must open inside the knowledge home.
func readWorkflowLawContext(ctx context.Context, tx *sql.Tx, workID string, contract *WorkflowReadContract) (*WorkflowLawContext, error) {
	if contract == nil {
		return nil, nil
	}
	roleSet := map[string]map[string]bool{}
	obligationSet := map[string]map[string]bool{}
	markRole := func(lawID, role string) {
		if roleSet[lawID] == nil {
			roleSet[lawID] = map[string]bool{}
		}
		roleSet[lawID][role] = true
	}
	for _, lawID := range contract.SpecMandate {
		markRole(lawID, lawContextRoleMandated)
	}
	if binding := contract.ArchitectureBinding; binding != nil {
		for _, obligation := range binding.VerificationObligations {
			markRole(obligation.LawID, lawContextRoleObligation)
			if obligationSet[obligation.LawID] == nil {
				obligationSet[obligation.LawID] = map[string]bool{}
			}
			obligationSet[obligation.LawID][obligation.ObligationID] = true
		}
		for _, addition := range binding.LawAdditions {
			markRole(addition.LawID, lawContextRoleAdded)
		}
	}
	for _, lawID := range contract.LawModifies {
		markRole(lawID, lawContextRoleModified)
	}
	bindingDomains := 0
	if contract.ArchitectureBinding != nil {
		bindingDomains = 1 + len(contract.ArchitectureBinding.AffectedDomainIDs)
	}
	if len(roleSet) == 0 && bindingDomains == 0 {
		return nil, nil
	}
	context := &WorkflowLawContext{Laws: []WorkflowLawContextLaw{}, Domains: []WorkflowLawContextDomain{}}
	lawIDs := make([]string, 0, len(roleSet))
	for lawID := range roleSet {
		lawIDs = append(lawIDs, lawID)
	}
	sort.Strings(lawIDs)
	if len(lawIDs) > 0 {
		laws, err := resolveWorkflowLawContextLaws(ctx, tx, workID, roleSet, obligationSet, lawIDs)
		if err != nil {
			return nil, err
		}
		context.Laws = laws
	}
	if contract.ArchitectureBinding != nil {
		productID, err := workflowBindingProductIDTx(ctx, tx, workID)
		if err != nil {
			return nil, err
		}
		domains, err := resolveLawContextDomains(ctx, tx, productID, *contract.ArchitectureBinding)
		if err != nil {
			return nil, err
		}
		context.Domains = domains
		registryRepo, err := domainRegistryHomeRepo(ctx, tx, productID)
		if err != nil {
			return nil, err
		}
		context.RegistryPath, err = knowledgeRegistryLocator(registryRepo)
		if err != nil {
			return nil, err
		}
	}
	return context, nil
}

func resolveWorkflowLawContextLaws(ctx context.Context, tx *sql.Tx, workID string, roleSet, obligationSet map[string]map[string]bool, lawIDs []string) ([]WorkflowLawContextLaw, error) {
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	homeRepo, err := workflowLawHomeRepo(ctx, tx, homeProjectID, homeLocatorID)
	if err != nil {
		return nil, err
	}
	return resolveLawContextSubjects(ctx, homeProjectID, homeLocatorID, homeRepo, workID, lawContextLaws(roleSet, obligationSet, lawIDs))
}

// domainRegistryHomeRepo resolves the checkout of the knowledge home the
// Product's committed Domain registry was read from, so the registry locator
// names the file the rendered Domains came from. A Product with no designated
// law home refuses typed rather than naming any repository's file.
func domainRegistryHomeRepo(ctx context.Context, tx *sql.Tx, productID string) (string, error) {
	home, err := resolveKnowledgeQueryHome(ctx, tx, productID, "read_workflow_law_context")
	if err != nil {
		return "", err
	}
	return workflowLawHomeRepo(ctx, tx, home.HomeProjectID, home.HomeLocatorID)
}

// workflowLawHomeRepo reads the canonical repository path of one resolved Git
// law home through whichever queryer the caller already holds. A knowledge
// home locator is a canonical-path locator by designation rule, so its
// normalized value is the absolute checkout a dispatched lane can open.
func workflowLawHomeRepo(ctx context.Context, q queryer, homeProjectID, homeLocatorID string) (string, error) {
	var repo string
	err := q.QueryRowContext(ctx, `SELECT normalized_value FROM project_locators WHERE project_id=? AND locator_id=? AND kind='canonical_path'`, homeProjectID, homeLocatorID).Scan(&repo)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && repo == "") {
		return "", newFailure(KindUnknownScope, "read_workflow_law_context", "the Git law home has no canonical repository path", false, "designate the Product knowledge home's canonical-path locator")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot read the Git law home repository path", true, "retry once the Project locators are readable", err)
	}
	return repo, nil
}

// knowledgeRegistryLocator returns the independent Domain registry's
// existing repository path as a home-qualified absolute locator the lane can
// open. Domain organization resolves against that one committed file; a home
// carrying no registry, a non-regular registry file, or an unreadable path
// refuses typed, so the packet never advertises a locator no lane can read
// or silently drops the Domain binding.
func knowledgeRegistryLocator(homeRepo string) (string, error) {
	root, err := os.OpenRoot(homeRepo)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", newFailure(KindDomainRegistryAbsent, "read_workflow_law_context", "the Product knowledge home repository carries no Domain registry", false, "commit the independent domain-registry.json before reading law")
		}
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot open the knowledge home checkout", true, "restore access to the Product knowledge home and retry", err)
	}
	defer func() { _ = root.Close() }()
	registry := filepath.Join(homeRepo, filepath.FromSlash(knowledgeRegistryPath))
	info, err := root.Stat(knowledgeRegistryPath)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return "", newFailure(KindDomainRegistryAbsent, "read_workflow_law_context", "the knowledge home carries no Domain registry file: "+registry, false, "commit the independent domain-registry.json at the Product knowledge home")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot inspect the Domain registry", true, "restore access to the Product knowledge home and retry", err)
	}
	if err := checkKnowledgeLocatorReadable(homeRepo, knowledgeRegistryPath); err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot open the Domain registry", true, "restore access to the Product knowledge home and retry", err)
	}
	return registry, nil
}

// knowledgeLawLocator qualifies one committed law document path with its
// knowledge home repository and refuses typed unless the qualified locator is
// a regular file there: a bound law is a required source, so the packet must
// never advertise a document the dispatched lane cannot open. The verified
// snapshot commit owns whether the recorded content hash is current; this
// check owns only the locator's readability.
func knowledgeLawLocator(homeRepo, lawID, subjectPath string) (string, error) {
	if subjectPath == "" {
		return "", nil
	}
	qualified := filepath.Join(homeRepo, filepath.FromSlash(subjectPath))
	root, err := os.OpenRoot(homeRepo)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot open the knowledge home checkout", true, "restore access to the Product knowledge home and retry", err)
	}
	if root != nil {
		defer func() { _ = root.Close() }()
	}
	var info os.FileInfo
	if root != nil {
		info, err = root.Stat(subjectPath)
	} else {
		// A removed checkout: the confined stat cannot run, and the bound law
		// keeps its missing-document refusal.
		err = os.ErrNotExist
	}
	if errors.Is(err, os.ErrNotExist) {
		failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound law document is missing from the knowledge home checkout: "+qualified, false, "restore the bound law file in the knowledge home checkout from its default-branch commit")
		failure.CandidateIDs = []string{lawID}
		return "", failure
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot inspect the bound law document in the knowledge home checkout", true, "restore access to the Product knowledge home and retry", err)
	}
	if !info.Mode().IsRegular() {
		failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound law locator is not a regular file in the knowledge home checkout: "+qualified, false, "restore the bound law file in the knowledge home checkout from its default-branch commit")
		failure.CandidateIDs = []string{lawID}
		return "", failure
	}
	if err := checkKnowledgeLocatorReadable(homeRepo, subjectPath); err != nil {
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot open the bound law document in the knowledge home checkout", true, "restore access to the Product knowledge home and retry", err)
	}
	return qualified, nil
}

// checkKnowledgeLocatorReadable opens one repository-relative knowledge path
// confined to the knowledge home checkout, so a projected path that escapes
// the home through ".." or a symlink cannot be advertised as readable.
func checkKnowledgeLocatorReadable(homeRepo, relative string) error {
	file, err := os.OpenInRoot(homeRepo, filepath.FromSlash(relative))
	if err != nil {
		return err
	}
	return file.Close()
}

// lawContextLaws builds each bound law's identity with its sorted, deduplicated
// roles and obligation IDs, ahead of the subject lookup.
func lawContextLaws(roleSet, obligationSet map[string]map[string]bool, lawIDs []string) []WorkflowLawContextLaw {
	laws := make([]WorkflowLawContextLaw, 0, len(lawIDs))
	for _, lawID := range lawIDs {
		law := WorkflowLawContextLaw{LawID: lawID}
		for role := range roleSet[lawID] {
			law.Roles = append(law.Roles, role)
		}
		sort.Strings(law.Roles)
		for obligationID := range obligationSet[lawID] {
			law.ObligationIDs = append(law.ObligationIDs, obligationID)
		}
		sort.Strings(law.ObligationIDs)
		laws = append(laws, law)
	}
	return laws
}

// resolveLawContextSubjects resolves each bound law from the verified
// law-folder snapshot of the workflow home. The 128-entry ceiling is the
// write-side sum of the mandate (32), additions (32), and obligations (64)
// bounds. Each law's authored criterion bindings ride the record; the ones
// naming the reading work item's predicates become that law's Criteria
// (CD-0180). Each record's committed path is qualified with the knowledge home
// repository and verified to open there before it reaches the packet. A bound
// law may carry the qualified project_id/law_id reference form (CD-0200); in
// this single-home path it resolves by its bare ID when the reference names
// this home's own Project.
func resolveLawContextSubjects(ctx context.Context, homeProjectID, homeLocatorID, homeRepo, workID string, laws []WorkflowLawContextLaw) ([]WorkflowLawContextLaw, error) {
	if len(laws) > 128 {
		return nil, newFailure(KindLimitExceeded, "read_workflow_law_context", "workflow contract binds more laws than the law context carries", false, "reduce_limit")
	}
	snapshot, err := lawSnapshotForSource(ctx, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}, "read_workflow_law_context")
	if err != nil {
		return nil, err
	}
	for index := range laws {
		projectID, bareID, qualified, parseErr := parseQualifiedKnowledgeID("read_workflow_law_context", laws[index].LawID)
		if parseErr != nil {
			return nil, parseErr
		}
		if qualified && projectID != homeProjectID {
			failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound law reference names a different knowledge home", false, "resolve the law through its designated Product knowledge home")
			failure.CandidateIDs = []string{laws[index].LawID}
			return nil, failure
		}
		record, exists := snapshot.Law(bareID)
		if !exists {
			law := WorkflowLawContextLaw{LawID: laws[index].LawID, Roles: laws[index].Roles, ObligationIDs: laws[index].ObligationIDs}
			if !slices.Contains(law.Roles, lawContextRoleAdded) {
				law.Status = "absent"
			}
			laws[index] = law
			continue
		}
		laws[index].Kind, laws[index].Status, laws[index].Title = record.Kind, record.Status, record.Title
		path, err := knowledgeLawLocator(homeRepo, laws[index].LawID, record.Path)
		if err != nil {
			return nil, err
		}
		laws[index].Path = path
		laws[index].Criteria = lawContextCriteria(record.CriterionBindings, workID)
	}
	return laws, nil
}

// lawContextCriteria keeps the law's predicate bindings that name the reading
// work item, in criterion order. A binding naming another work item, and a
// scenario or exemption binding, never surface in the law context.
func lawContextCriteria(bindings []KnowledgeCriterionBinding, workID string) []WorkflowLawContextCriterionBinding {
	var criteria []WorkflowLawContextCriterionBinding
	for _, binding := range bindings {
		if binding.WorkID != workID || binding.PredicateID == "" {
			continue
		}
		criteria = append(criteria, WorkflowLawContextCriterionBinding{Criterion: binding.Criterion, PredicateID: binding.PredicateID})
	}
	sort.Slice(criteria, func(left, right int) bool { return criteria[left].Criterion < criteria[right].Criterion })
	return criteria
}

// resolveLawContextDomains resolves the binding's home Domain first, then its
// affected Domains in ID order, from the verified committed Domain registry.
func resolveLawContextDomains(ctx context.Context, tx *sql.Tx, productID string, binding WorkflowArchitectureBinding) ([]WorkflowLawContextDomain, error) {
	home, err := resolveKnowledgeQueryHome(ctx, tx, productID, "read_workflow_law_context")
	if err != nil {
		return nil, err
	}
	snapshot, err := lawSnapshotForSource(ctx, home, "read_workflow_law_context")
	if err != nil {
		return nil, err
	}
	registry, err := snapshot.Registry()
	if err != nil {
		return nil, err
	}
	ordered := make([]string, 0, 1+len(binding.AffectedDomainIDs))
	seen := map[string]bool{}
	for _, domainID := range append([]string{binding.HomeDomainID}, binding.AffectedDomainIDs...) {
		if !seen[domainID] {
			seen[domainID] = true
			ordered = append(ordered, domainID)
		}
	}
	sort.Strings(ordered[1:])
	domains := make([]WorkflowLawContextDomain, 0, len(ordered))
	for _, domainID := range ordered {
		var domain WorkflowLawContextDomain
		for _, entry := range registry.Domains {
			if entry.DomainID == domainID {
				domain.Name, domain.Purpose = entry.Name, entry.Purpose
				break
			}
		}
		if domain.Name == "" && domain.Purpose == "" {
			failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound Domain is missing from the committed Domain registry at the verified snapshot commit", false, "commit the Domain under the registry at the Product knowledge home through an ordinary reviewed pull request")
			failure.CandidateIDs = []string{domainID}
			return nil, failure
		}
		domain.DomainID = domainID
		domains = append(domains, domain)
	}
	return domains, nil
}

// validateWorkflowContractRecoveryPayload enforces the successor contract
// payload against its one registry declaration: the field list, the required
// set, and the exactly-one outcome group all come from the definition's
// selected recovery declaration (workflowDecisionFactRecoveryActionDefinition),
// so no handwritten field-name list or outcome-shape branch can drift from
// the declaration the published variant generates from, and a consequence_v1
// successor's required decision fact is declared beside the released fields.
// On a canonical authoring head the outcome group is the explicit predicate
// array alone: the legacy outcome pair refuses and the array is required, so
// the successor's end state is always the one the caller supplied.
func validateWorkflowContractRecoveryPayload(definition WorkflowDefinition, raw json.RawMessage) error {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return err
	}
	declaration := workflowDecisionFactRecoveryActionDefinition(definition).Payload
	if workflowCanonicalAuthoringActive(definition) {
		declaration.Fields = workflowCanonicalContractRecoveryPayloadFields(declaration.Fields)
	}
	crossField := workflowActionCrossField("supersede_contract")
	allowed := make(map[string]WorkflowPayloadField)
	for _, field := range declaration.Fields {
		allowed[field.Name] = field
	}
	for name, value := range fields {
		field, ok := allowed[name]
		if !ok || !validateWorkflowPayloadValue(field, value) {
			return newFailure(KindInvalidPayload, "workflow_action", "successor contract contains an undeclared or invalid field", false, "supply the typed successor contract")
		}
		// The declared schema refs bind here exactly as the action payload
		// preflight binds them: the per-kind outcome_predicates items and
		// the closed object fields the published successor variant projects
		// refuse at this boundary, not first at the fold. The successor
		// contract is a current engine declaration, so it resolves through
		// the public authoring document.
		if err := validateWorkflowPayloadSchema(payloadschema.Document(), field, value); err != nil {
			return err
		}
	}
	for _, field := range declaration.Fields {
		if !field.Required {
			continue
		}
		if _, ok := fields[field.Name]; !ok {
			return newFailure(KindInvalidPayload, "workflow_action", "successor contract requires a fully supplied contract", false, "supply every successor contract field")
		}
	}
	// The submitted fact keeps its semantic contract beside the declared
	// field: workflowSubmittedDecisionDomains refuses absence, null, and any
	// noncanonical array the per-field value check cannot see. A legacy
	// declaration carries no fact, so the call stays a no-op there.
	if _, err := workflowSubmittedDecisionDomains(definition, "successor", fields); err != nil {
		return err
	}
	switch complete, partial := workflowPayloadAlternativeGroups(crossField.Alternatives, fields); {
	case complete == 1 && !partial:
	case complete == 0 && !partial:
		return newFailure(KindInvalidPayload, "workflow_action", "successor contract requires outcome_predicates or the outcome pair", false, "supply the complete successor outcome")
	case complete >= 2:
		return newFailure(KindInvalidPayload, "workflow_action", "successor contract must supply one outcome shape", false, "supply outcome_predicates or the outcome pair")
	default:
		// A partially supplied second outcome group: the published closed
		// branches refuse it, so the engine guard refuses it too
		// (CON-412).
		return newFailure(KindInvalidPayload, "workflow_action", "successor contract must supply one outcome shape: a field of the other outcome group is present without its complete group", false, "complete the started outcome group or remove its fields")
	}
	if workflowFieldInt(fields, "contract_version", 0) <= 0 || workflowFieldStringDefault(fields, "premise", "") == "" || workflowFieldStringDefault(fields, "rigor_class", "") == "" || workflowFieldStringDefault(fields, "supersede_reason", "") == "" {
		return newFailure(KindInvalidPayload, "workflow_action", "successor contract has an empty required field", false, "supply the complete successor contract")
	}
	// The nested discriminator equality the declaration carries: a successor
	// outcome pair whose outcome_kind and outcome_payload.kind disagree is
	// refused here, at the same declaration the published pair branches
	// close per kind (CON-412). The fold keeps its deeper decode guard.
	if match, fieldKind, objectKind, violated := workflowPayloadKindMatchViolation(crossField, fields); violated {
		return workflowPayloadKindMatchRefusal("supersede_contract", match, fieldKind, objectKind)
	}
	return nil
}
