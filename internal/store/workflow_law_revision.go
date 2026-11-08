package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// WorkflowLawContext is the bounded, typed resolution of the approved
// contract's binding law and Domain references. The core turns every
// contract-bound law ID and home or affected Domain ID into its projection
// state in the full work list, so the context carries the title of
// each binding law and a file locator that opens inside the Product's
// designated knowledge home, not inside whatever repository the work
// runs in.
type WorkflowLawContext struct {
	Laws    []WorkflowLawContextLaw    `json:"laws"`
	Domains []WorkflowLawContextDomain `json:"domains"`
	// RegistryPath is the knowledge home repository's absolute locator for
	// the Domain registry shard the home checkout actually carries, set when
	// the context binds at least one Domain.
	RegistryPath string `json:"registry_path,omitempty"`
}

type WorkflowLawContextLaw struct {
	Roles  []string `json:"roles"`
	LawID  string   `json:"law_id"`
	Kind   string   `json:"kind,omitempty"`
	Status string   `json:"status,omitempty"`
	Title  string   `json:"title,omitempty"`
	// Path is the knowledge home repository's absolute locator for the law
	// document the projection recorded, so the reading agent opens it inside
	// the home checkout rather than the repository the work runs in.
	Path          string   `json:"path,omitempty"`
	ObligationIDs []string `json:"obligation_ids,omitempty"`
}

type WorkflowLawContextDomain struct {
	DomainID string `json:"domain_id"`
	Name     string `json:"name"`
	Purpose  string `json:"purpose"`
	Status   string `json:"status,omitempty"`
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
// binds no law and no Domain. Every own-home reference folds to its resolved
// law identity, so one law appears once under its canonical spelling with
// every role it carries; missing own-home laws retain their identity and
// roles with status absent, except reserved additions, which carry identity
// and roles only, including foreign-qualified additions. Other foreign-home
// references refuse. Declared Domain additions missing from the registry carry
// status added; missing Domains not declared as additions refuse. Every
// advertised file locator must open inside the knowledge home.
func readWorkflowLawContext(ctx context.Context, tx *sql.Tx, workID string, contract *WorkflowReadContract) (*WorkflowLawContext, error) {
	if contract == nil {
		return nil, nil
	}
	binding := contract.ArchitectureBinding
	if !workflowContractBindsLaw(contract) {
		return workflowLawContextDomainsOnly(ctx, tx, workID, binding)
	}
	// The home is resolved only for a contract that binds law: law-less
	// contracts are legitimate on works without a designated home, and the
	// Domain-only path resolves its own home.
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	roleSet, obligationSet := foldWorkflowLawContextRoles(homeProjectID, contract)
	lawIDs := make([]string, 0, len(roleSet))
	for lawID := range roleSet {
		lawIDs = append(lawIDs, lawID)
	}
	sort.Strings(lawIDs)
	laws, err := resolveWorkflowLawContextLaws(ctx, tx, homeProjectID, homeLocatorID, roleSet, obligationSet, lawIDs)
	if err != nil {
		return nil, err
	}
	context := &WorkflowLawContext{Laws: laws, Domains: []WorkflowLawContextDomain{}}
	if binding == nil {
		return context, nil
	}
	productID, err := workflowBindingProductIDTx(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	if context.Domains, err = resolveLawContextDomains(ctx, tx, productID, *binding); err != nil {
		return nil, err
	}
	registryRepo, err := domainRegistryHomeRepo(ctx, tx, productID)
	if err != nil {
		return nil, err
	}
	if context.RegistryPath, err = knowledgeRegistryLocator(registryRepo); err != nil {
		return nil, err
	}
	return context, nil
}

// workflowContractBindsLaw reports whether any composed field names a law
// reference the law context must resolve.
func workflowContractBindsLaw(contract *WorkflowReadContract) bool {
	if len(contract.SpecMandate) > 0 || len(contract.LawModifies) > 0 {
		return true
	}
	binding := contract.ArchitectureBinding
	return binding != nil && (len(binding.VerificationObligations) > 0 || len(binding.LawAdditions) > 0)
}

// foldWorkflowLawContextRoles collects the role and obligation sets keyed by
// each reference's resolved law identity, so one law identity appears once
// under its canonical spelling with every role it carries.
func foldWorkflowLawContextRoles(homeProjectID string, contract *WorkflowReadContract) (map[string]map[string]bool, map[string]map[string]bool) {
	roleSet := map[string]map[string]bool{}
	obligationSet := map[string]map[string]bool{}
	markRole := func(lawID, role string) {
		lawID = ownHomeLawAdditionIdentityID(homeProjectID, lawID)
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
			obligationID := ownHomeLawAdditionIdentityID(homeProjectID, obligation.LawID)
			if obligationSet[obligationID] == nil {
				obligationSet[obligationID] = map[string]bool{}
			}
			obligationSet[obligationID][obligation.ObligationID] = true
		}
		for _, addition := range binding.LawAdditions {
			markRole(addition.LawID, lawContextRoleAdded)
		}
	}
	for _, lawID := range contract.LawModifies {
		markRole(lawID, lawContextRoleModified)
	}
	return roleSet, obligationSet
}

// workflowLawContextDomainsOnly renders the Domain half of a law context for
// a contract that binds no law. A contract with neither law nor a binding
// carries no context at all.
func workflowLawContextDomainsOnly(ctx context.Context, tx *sql.Tx, workID string, binding *WorkflowArchitectureBinding) (*WorkflowLawContext, error) {
	if binding == nil {
		return nil, nil
	}
	productID, err := workflowBindingProductIDTx(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	domains, err := resolveLawContextDomains(ctx, tx, productID, *binding)
	if err != nil {
		return nil, err
	}
	registryRepo, err := domainRegistryHomeRepo(ctx, tx, productID)
	if err != nil {
		return nil, err
	}
	registryPath, err := knowledgeRegistryLocator(registryRepo)
	if err != nil {
		return nil, err
	}
	return &WorkflowLawContext{Laws: []WorkflowLawContextLaw{}, Domains: domains, RegistryPath: registryPath}, nil
}

func resolveWorkflowLawContextLaws(ctx context.Context, tx *sql.Tx, homeProjectID, homeLocatorID string, roleSet, obligationSet map[string]map[string]bool, lawIDs []string) ([]WorkflowLawContextLaw, error) {
	homeRepo, err := workflowLawHomeRepo(ctx, tx, homeProjectID, homeLocatorID)
	if err != nil {
		return nil, err
	}
	return resolveLawContextSubjects(ctx, homeProjectID, homeLocatorID, homeRepo, lawContextLaws(roleSet, obligationSet, lawIDs))
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
// normalized value is the absolute checkout the reading agent can open.
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
// existing repository path as a home-qualified absolute locator the reading
// agent can open. Domain organization resolves against that one committed
// file; a home carrying no registry, a non-regular registry file, or an
// unreadable path refuses typed, so the context never advertises a locator
// the agent cannot read or silently drops the Domain binding. The locator
// runs after the committed registry was read, so both absent-file shapes
// mean the checkout disagrees with a commit that carries the registry:
// their recoveries restore the checkout, they do not ask for a commit that
// already exists.
func knowledgeRegistryLocator(homeRepo string) (string, error) {
	root, err := os.OpenRoot(homeRepo)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", newFailure(KindDomainRegistryAbsent, "read_workflow_law_context", "the Product knowledge home checkout is missing: "+homeRepo, false, "restore the Product knowledge home checkout from its default branch and retry; the verified snapshot commit already carries the Domain registry")
		}
		return "", wrapFailure(KindUnavailable, "read_workflow_law_context", "cannot open the knowledge home checkout", true, "restore access to the Product knowledge home and retry", err)
	}
	defer func() { _ = root.Close() }()
	registry := filepath.Join(homeRepo, filepath.FromSlash(knowledgeRegistryPath))
	info, err := root.Stat(knowledgeRegistryPath)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return "", newFailure(KindDomainRegistryAbsent, "read_workflow_law_context", "the knowledge home checkout carries no Domain registry file: "+registry, false, "restore the Domain registry in the knowledge home checkout from its default-branch commit")
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
// a regular file there: a bound law is a required source, so the context must
// never advertise a document the reading agent cannot open. The verified
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
// bounds. Each record's committed path is qualified with the knowledge home
// repository and verified to open there before it reaches the context. A bound
// law may carry the qualified project_id/law_id reference form; in
// this single-home path it resolves by its bare ID when the reference names
// this home's own Project.
func resolveLawContextSubjects(ctx context.Context, homeProjectID, homeLocatorID, homeRepo string, laws []WorkflowLawContextLaw) ([]WorkflowLawContextLaw, error) {
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
			if slices.Contains(laws[index].Roles, lawContextRoleAdded) {
				laws[index] = WorkflowLawContextLaw{LawID: laws[index].LawID, Roles: laws[index].Roles}
				continue
			}
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
	}
	return laws, nil
}

// resolveLawContextDomains resolves the binding's home Domain first, then its
// affected Domains in ID order, from the verified committed Domain registry.
// Declared additions missing from the registry carry status added; missing
// Domains not declared as additions refuse.
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
		found := false
		for _, entry := range registry.Domains {
			if entry.DomainID == domainID {
				found = true
				domain.Name, domain.Purpose = entry.Name, entry.Purpose
				break
			}
		}
		if !found && slices.Contains(binding.DomainAdditions, domainID) {
			domain.Status = "added"
		} else if !found {
			failure := newFailure(KindProjectionNotFound, "read_workflow_law_context", "bound Domain is missing from the committed Domain registry at the verified snapshot commit", false, "commit the Domain under the registry at the Product knowledge home through an ordinary reviewed pull request")
			failure.CandidateIDs = []string{domainID}
			return nil, failure
		}
		domain.DomainID = domainID
		domains = append(domains, domain)
	}
	return domains, nil
}
