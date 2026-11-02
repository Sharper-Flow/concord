package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkflowLawRevision is the immutable law proof captured when a workflow
// contract is approved. The Git commit is deliberately absent: it is audit
// context, not revision identity (CD-0036 D1).
type WorkflowLawRevision struct {
	LawID       string `json:"law_id"`
	ContentHash string `json:"content_hash"`
}

// CompatibleLawAmendment reports a same-ID accepted law change that does not
// block the workflow, but needs a fresh read before consequential work.
type CompatibleLawAmendment struct {
	LawID       string `json:"law_id"`
	PinnedHash  string `json:"pinned_hash"`
	CurrentHash string `json:"current_hash"`
}

// StaleLawRevision identifies a pinned law deleted from the accepted projection.
// Same-ID content amendments remain compatible.
type StaleLawRevision struct {
	OldLawID        string   `json:"old_law_id"`
	OldContentHash  string   `json:"old_content_hash"`
	RecoveryActions []string `json:"recovery_actions"`
}

const staleLawRecoveryActions = "revise_intent,terminal_work"

func validateWorkflowLawRevisions(mandated []string, revisions []WorkflowLawRevision) error {
	if len(mandated) > 32 || len(revisions) > 32 {
		return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revision pins exceed the bounded list size", false, "supply at most 32 mandated law revisions")
	}
	mandate := make(map[string]struct{}, len(mandated))
	for _, lawID := range mandated {
		if lawID == "" {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law mandate contains an empty ID", false, "supply bounded law IDs")
		}
		if _, exists := mandate[lawID]; exists {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law mandate contains a duplicate ID", false, "supply unique law IDs")
		}
		mandate[lawID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(revisions))
	for _, revision := range revisions {
		if revision.LawID == "" || revision.LawID != strings.TrimSpace(revision.LawID) {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revision has an invalid law ID", false, "supply one pin for each mandated law ID")
		}
		if _, exists := mandate[revision.LawID]; !exists {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revision is not in spec_mandate", false, "supply pins matching spec_mandate exactly")
		}
		if _, exists := seen[revision.LawID]; exists {
			return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revisions contain a duplicate law ID", false, "supply one pin for each mandated law ID")
		}
		if err := validateContentHash(revision.ContentHash); err != nil {
			return err
		}
		seen[revision.LawID] = struct{}{}
	}
	if len(revisions) != len(mandated) {
		return newFailure(KindInvalidPayload, "validate_workflow_law_revisions", "workflow law revision pins do not correspond one-to-one with spec_mandate", false, "capture one current Git law hash for every mandated law ID")
	}
	return nil
}

// workflowLawSubject is one mandated reference's resolved projection row.
type workflowLawSubject struct {
	status string
	hash   string
}

// resolveWorkflowLawSubjectTx resolves one mandate reference — a bare law ID,
// or the qualified project_id/law_id form (CD-0200) — to exactly one current
// law in the verified law-folder snapshot of the workflow's knowledge home.
// The snapshot is the acceptance authority: a reference it does not carry is
// not current law.
func resolveWorkflowLawSubjectTx(ctx context.Context, homeProjectID, homeLocatorID, reference string) (workflowLawSubject, error) {
	projectID, lawID, qualified, err := parseQualifiedKnowledgeID("check_workflow_law_revision", reference)
	if err != nil {
		return workflowLawSubject{}, err
	}
	if qualified && projectID != homeProjectID {
		failure := newFailure(KindProjectionNotFound, "check_workflow_law_revision", "qualified mandated law names a Project outside the workflow's law home: "+reference, false, "mandate law from the workflow's knowledge home")
		failure.CandidateIDs = []string{reference}
		return workflowLawSubject{}, failure
	}
	snapshot, err := lawSnapshotForSource(ctx, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}, "check_workflow_law_revision")
	if err != nil {
		return workflowLawSubject{}, err
	}
	record, ok := snapshot.Law(lawID)
	if !ok {
		failure := newFailure(KindProjectionNotFound, "check_workflow_law_revision", "mandated law is not a file in the committed law folder at the verified snapshot commit", false, "publish the law file under the law folder through an ordinary reviewed pull request")
		failure.CandidateIDs = []string{reference}
		return workflowLawSubject{}, failure
	}
	return workflowLawSubject{status: record.Status, hash: record.SHA256}, nil
}

func deriveWorkflowLawRevisionsTx(ctx context.Context, tx *sql.Tx, workID string, mandated, modified []string) ([]WorkflowLawRevision, error) {
	if err := validateLawModificationSubset(mandated, modified); err != nil {
		return nil, err
	}
	if len(mandated) == 0 {
		return []WorkflowLawRevision{}, nil
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	if err := checkMandatedLawsTxAtHome(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, true); err != nil {
		return nil, err
	}
	revisions := make([]WorkflowLawRevision, 0, len(mandated))
	for _, lawID := range mandated {
		subject, err := resolveWorkflowLawSubjectTx(ctx, homeProjectID, homeLocatorID, lawID)
		if err != nil {
			var failure *Failure
			if failureAs(err, &failure) && failure.Kind == KindProjectionNotFound {
				return nil, newFailure(KindProjectionNotFound, "derive_workflow_law_revisions", "mandated law is not currently committed law", false, "publish the law file under the law folder through an ordinary reviewed pull request")
			}
			return nil, err
		}
		if subject.status != "accepted" {
			return nil, newFailure(KindProjectionNotFound, "derive_workflow_law_revisions", "mandated law is not currently committed law", false, "publish the law file under the law folder through an ordinary reviewed pull request")
		}
		revisions = append(revisions, WorkflowLawRevision{LawID: lawID, ContentHash: subject.hash})
	}
	return revisions, nil
}

// findStaleWorkflowLawRevision is read-only and accepts either *sql.DB or
// *sql.Tx. It consults only the current Git-derived law projection and the
// contract pins; it never changes either authority. Each mandated reference
// resolves within the workflow's knowledge home. Only a deleted
// pinned law is stale; an accepted same-ID amendment stays compatible.
func findStaleWorkflowLawRevision(ctx context.Context, q queryer, homeProjectID, homeLocatorID, workID string, contractVersion int64, mandated []string) (*StaleLawRevision, error) {
	if len(mandated) == 0 {
		return nil, nil
	}
	if err := validateWorkflowLawMandate(mandated); err != nil {
		return nil, err
	}
	for _, reference := range mandated {
		var pinnedHash string
		err := q.QueryRowContext(ctx, `SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=? AND law_id=?`, workID, contractVersion, reference).Scan(&pinnedHash)
		pinned := err == nil
		if err != nil && err != sql.ErrNoRows {
			return nil, wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot read workflow law revision pins", true, "retry once the workflow projection is readable", err)
		}
		subject, err := resolveWorkflowLawSubjectTx(ctx, homeProjectID, homeLocatorID, reference)
		if err != nil {
			var failure *Failure
			if pinned && failureAs(err, &failure) && failure.Kind == KindProjectionNotFound {
				return &StaleLawRevision{OldLawID: reference, OldContentHash: pinnedHash, RecoveryActions: strings.Split(staleLawRecoveryActions, ",")}, nil
			}
			return nil, err
		}
		if subject.status != "accepted" {
			return nil, newFailure(KindProjectionNotFound, "check_workflow_law_revision", "mandated law is not currently committed law", false, "publish the law file under the law folder through an ordinary reviewed pull request")
		}
	}
	return nil, nil
}

func findCompatibleWorkflowLawAmendments(ctx context.Context, q queryer, homeProjectID, homeLocatorID, workID string, contractVersion int64, mandated []string) ([]CompatibleLawAmendment, error) {
	amendments := []CompatibleLawAmendment{}
	for _, lawID := range mandated {
		var pinnedHash string
		err := q.QueryRowContext(ctx, `SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=? AND law_id=?`, workID, contractVersion, lawID).Scan(&pinnedHash)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot read workflow law revision pins", true, "retry once the workflow projection is readable", err)
		}
		subject, err := resolveWorkflowLawSubjectTx(ctx, homeProjectID, homeLocatorID, lawID)
		if err != nil {
			return nil, err
		}
		if subject.status == "accepted" && pinnedHash != subject.hash {
			amendments = append(amendments, CompatibleLawAmendment{LawID: lawID, PinnedHash: pinnedHash, CurrentHash: subject.hash})
		}
	}
	return amendments, nil
}

func validateWorkflowLawMandate(mandated []string) error {
	seen := map[string]struct{}{}
	for _, lawID := range mandated {
		if lawID == "" {
			return newFailure(KindInvalidPayload, "check_workflow_law_revision", "workflow law mandate contains an empty ID", false, "supply bounded law IDs")
		}
		if _, exists := seen[lawID]; exists {
			return newFailure(KindInvalidPayload, "check_workflow_law_revision", "workflow law mandate contains a duplicate ID", false, "supply unique law IDs")
		}
		seen[lawID] = struct{}{}
	}
	return nil
}

func readWorkflowLawRevisions(ctx context.Context, q queryer, workID string, contractVersion int64) ([]WorkflowLawRevision, error) {
	rows, err := q.QueryContext(ctx, `SELECT law_id,content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=? ORDER BY law_id`, workID, contractVersion)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "read_workflow_law_revision", "cannot read workflow law revision pins", true, "retry once the workflow projection is readable", err)
	}
	defer rows.Close()
	revisions := []WorkflowLawRevision{}
	for rows.Next() {
		var revision WorkflowLawRevision
		if err := rows.Scan(&revision.LawID, &revision.ContentHash); err != nil {
			return nil, wrapFailure(KindUnavailable, "read_workflow_law_revision", "cannot decode workflow law revision pin", true, "retry once the workflow projection is readable", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "read_workflow_law_revision", "cannot enumerate workflow law revision pins", true, "retry once the workflow projection is readable", err)
	}
	return revisions, nil
}

// workflowContractRecoveryStaleness identifies a deleted-law pin refusal.
func workflowContractRecoveryStaleness(err error) bool {
	var failure *Failure
	return failureAs(err, &failure) && failure.Kind == KindStaleLawRevision
}

// checkWorkflowLawRevisionStalenessTx refuses an action whose active contract
// pins a law that no longer exists (CD-0036). It is the only post-approval
// comparison of a contract with current law (CD-0222 D7).
func checkWorkflowLawRevisionStalenessTx(ctx context.Context, tx queryer, workID string) error {
	var mandateJSON string
	// This boundary reads the law revisions that one approved contract pins.
	// An absent projection pins nothing, and an ambiguous one names no single
	// contract to read, so neither can carry a stale pin this check could
	// evaluate. An ambiguous item also reaches no implementation-bearing
	// action while it stays ambiguous, so admitting it here surrenders no
	// guard that another one is not already holding.
	activeVersions, err := activeWorkflowContractVersions(ctx, tx, workID)
	if err != nil {
		return err
	}
	if len(activeVersions) != 1 {
		return nil
	}
	contractVersion := activeVersions[0]
	if err := tx.QueryRowContext(ctx, `SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&mandateJSON); err != nil {
		return wrapFailure(KindUnavailable, "check_workflow_law_revision", "cannot read active workflow contract", true, "retry once the workflow projection is readable", err)
	}
	var mandated []string
	if err := json.Unmarshal([]byte(mandateJSON), &mandated); err != nil {
		return newFailure(KindInvariantViolation, "check_workflow_law_revision", "workflow contract law mandate is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	var mandateErr error
	mandated, mandateErr = currentWorkflowLawMandateFromProjection(ctx, tx, workID, contractVersion, mandated)
	if mandateErr != nil {
		return mandateErr
	}
	if len(mandated) == 0 {
		return nil
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	stale, err := findStaleWorkflowLawRevision(ctx, tx, homeProjectID, homeLocatorID, workID, contractVersion, mandated)
	if err != nil {
		return err
	}
	if stale == nil {
		return nil
	}
	failure := newFailure(KindStaleLawRevision, "check_workflow_law_revision", "workflow contract pins a deleted law", false, "reconcile_operation")
	failure.StaleLawRevision = stale
	return failure
}

// CheckWorkflowConsequentialBoundaryTx is the CD-0222 D7 preflight for a
// caller-owned mutation transaction. It validates the contract's law revision
// pins in the transaction that owns the write, so they cannot change between
// the check and the effect. It is read-only and returns a typed refusal.
func CheckWorkflowConsequentialBoundaryTx(ctx context.Context, transaction *Transaction, workID string) error {
	tx, err := transactionSQL(transaction, "check_workflow_law_revision")
	if err != nil {
		return err
	}
	return checkWorkflowLawRevisionStalenessTx(ctx, tx, workID)
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
// binds no law and no Domain, so a contract with no bound law still
// dispatches. A law listed in law_additions — a reserved addition before its
// subject is published — carries its role and identity only. Any other bound
// law without a committed file, and any home or affected Domain missing from
// the committed registry, refuses with KindProjectionNotFound: the packet
// would otherwise bind a document no lane can read. Every file locator the
// context carries is qualified with the Product knowledge home's repository
// and verified to open there, so a lane dispatched into any member Project
// reads the home's checkout.
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
		record, exists := snapshot.Law(bareID)
		if !exists || qualified && projectID != homeProjectID {
			if listedAsAddition(laws[index].Roles) {
				continue
			}
			failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound law is not a file in the committed law folder at the verified snapshot commit", false, "publish the law file under the law folder through an ordinary reviewed pull request")
			failure.CandidateIDs = []string{laws[index].LawID}
			return nil, failure
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

// listedAsAddition reports whether the contract lists the law in
// law_additions, the one listing the approved design allows to precede its
// committed law file: an addition is barred from modifying or pinning an
// existing law ID, so its absent subject at continuity read time is the
// designed pre-publication state. Every other bound role promises a document
// that already exists.
func listedAsAddition(roles []string) bool {
	for _, role := range roles {
		if role == lawContextRoleAdded {
			return true
		}
	}
	return false
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
