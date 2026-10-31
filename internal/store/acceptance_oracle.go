package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// An acceptance oracle declares the owners, cases and controls a native
// preparation pins. Its authoring joins the contract, Domain registry, pinned
// law, Project membership and retained readiness evidence.

// Closed bounds of the oracle surface. An over-bound oracle refuses whole;
// content is never truncated to fit.
const (
	OracleOwnersMax            = 8
	OracleCasesMax             = 64
	OracleControlsMax          = 64
	OracleLawBindingsMax       = 8
	OracleOwnerPredicatesMax   = 8
	OracleControlPredicatesMax = 8
	OracleCaseControlsMax      = 8
	OracleControlCasesMax      = 64
	OracleReadinessRefsMax     = 8
	OracleArgvMax              = 32
	OracleCombinedMaxBytes     = 32 * 1024
	oracleDescriptionMaxBytes  = 512
	oracleDomainIDMaxBytes     = 256
	oracleProjectIDMaxBytes    = 128
	oracleArgMaxBytes          = 512
)

// Identity tokens use the existing bounded reference grammar with closed
// prefixes, so an owner, case, or control identity stays stable within one
// work item and can never collide with a predicate or job identity.
var (
	oracleOwnerIDPattern   = regexp.MustCompile(`^owner:[A-Za-z0-9][A-Za-z0-9._:-]{0,121}$`)
	oracleCaseIDPattern    = regexp.MustCompile(`^case:[A-Za-z0-9][A-Za-z0-9._:-]{0,126}$`)
	oracleControlIDPattern = regexp.MustCompile(`^control:[A-Za-z0-9][A-Za-z0-9._:-]{0,126}$`)
)

// The closed result and evidence-role vocabulary. The declared result
// is the exit status, not parsed natural language; a reported control
// execution stays reported evidence until an independent execution role or
// an existing verdict owner supports it.
const (
	OracleExpectedResultPass                = "pass"
	OracleJudgeReview                       = "review"
	OracleEvidenceRoleReported              = "reported"
	OracleEvidenceRoleIndependentlyExecuted = "independently_executed"
)

// OracleMechanism names the source-level owner of one obligation: the
// Project that owns the repository, one contained source path, and the
// symbol or entry point that owns the behavior. A directory name alone does
// not identify a mechanism.
type OracleMechanism struct {
	ProjectID  string `json:"project_id"`
	Path       string `json:"path"`
	EntryPoint string `json:"entry_point"`
}

// OracleLawBinding ties an owner obligation to one pinned law revision. The
// source is the work-context knowledge reading arm — source_id, law_id, and the
// pinned content hash — plus the clause or criterion the obligation serves.
type OracleLawBinding struct {
	Source WorkContextReadingSource `json:"source"`
	Clause string                   `json:"clause"`
}

// OracleOwner is the behavioral owner and its obligation: an approved
// affected Domain, the mechanism that owns the behavior, the bounded
// invariant statement, the parent-approved predicates this owner covers, and
// the pinned governing law that explains the obligation.
type OracleOwner struct {
	OwnerID      string             `json:"owner_id"`
	DomainID     string             `json:"domain_id"`
	Mechanism    OracleMechanism    `json:"mechanism"`
	Obligation   string             `json:"obligation"`
	PredicateIDs []string           `json:"predicate_ids"`
	LawBindings  []OracleLawBinding `json:"law_bindings,omitempty"`
}

// OracleCase is one finite inventory entry: a named entry path or
// transition, its input class, the expected state, and either the exact controls
// that exercise it or a Main-declared Review judge. Descriptions are bounded
// prose, never a predicate DSL.
type OracleCase struct {
	CaseID        string   `json:"case_id"`
	OwnerID       string   `json:"owner_id"`
	EntryPoint    string   `json:"entry_point"`
	InputClass    string   `json:"input_class"`
	ExpectedState string   `json:"expected_state"`
	ControlIDs    []string `json:"control_ids,omitempty"`
	Judge         string   `json:"judge,omitempty"`
}

// OracleControl is one executable control: the pinned harness source, the
// exact argument vector (never shell text), a contained working directory,
// the closed expected result, the evidence role its receipt must carry, and
// the readiness evidence references that prove the harness resolves and the
// selector is nonempty. recipe_source identifies the acceptance harness,
// not the delivered subject; the harness may intentionally come from an
// earlier immutable commit.
type OracleControl struct {
	ControlID             string                   `json:"control_id"`
	OwnerID               string                   `json:"owner_id"`
	PredicateIDs          []string                 `json:"predicate_ids"`
	CaseIDs               []string                 `json:"case_ids"`
	RecipeSource          WorkContextReadingSource `json:"recipe_source"`
	Argv                  []string                 `json:"argv"`
	Cwd                   string                   `json:"cwd"`
	ExpectedResult        string                   `json:"expected_result"`
	RequiredEvidenceRole  string                   `json:"required_evidence_role"`
	ReadinessEvidenceRefs []string                 `json:"readiness_evidence_refs"`
}

// AcceptanceOracle is the closed three-part native verification graph:
// owners with obligations, a finite case inventory, and
// executable controls with exact evidence requirements.
type AcceptanceOracle struct {
	Owners   []OracleOwner   `json:"owners"`
	Cases    []OracleCase    `json:"cases"`
	Controls []OracleControl `json:"controls"`
}

// oracleFailure bounds the refusal messages of this surface.
func oracleFailure(kind FailureKind, detail, remedy string) error {
	return newFailure(kind, "acceptance_oracle", detail, false, remedy)
}

// oracleBoundedText enforces the shared description bound: present, at most
// 512 UTF-8 bytes, and free of control characters.
func oracleBoundedText(value, label string) error {
	if len(value) < 1 || len(value) > oracleDescriptionMaxBytes {
		return oracleFailure(KindInvalidPayload, "acceptance oracle "+label+" must be between 1 and 512 bytes", "bound the "+label)
	}
	if strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return oracleFailure(KindInvalidPayload, "acceptance oracle "+label+" must not contain control characters", "bound the "+label)
	}
	return nil
}

// validateOracleLawSource enforces the knowledge arm of the closed source
// union for one law binding: the store's law-revision identity only, with
// no repository fields.
func validateOracleLawSource(source WorkContextReadingSource) error {
	const surface = "acceptance oracle"
	if source.Kind != WorkContextSourceKnowledge {
		return oracleFailure(KindInvalidPayload, surface+" law binding source must be a knowledge source", "bind law by source_id, law_id, and the pinned content hash")
	}
	if len(source.SourceID) < 2 || len(source.SourceID) > oracleProjectIDMaxBytes {
		return oracleFailure(KindInvalidPayload, surface+" law binding source_id must be between 2 and 128 bytes", "name the knowledge source")
	}
	if len(source.LawID) < 2 || len(source.LawID) > workContextLawIDMaxBytes {
		return oracleFailure(KindInvalidPayload, surface+" law binding law_id must be between 2 and 256 bytes", "name the bound law")
	}
	if !workflowDigest(source.ContentHash) {
		return oracleFailure(KindInvalidPayload, surface+" law binding content_hash must be a sha256 digest", "pin the law revision content hash")
	}
	if source.ProjectID != "" || source.Path != "" || source.CommitOID != "" || source.Selector != nil {
		return oracleFailure(KindInvalidPayload, surface+" law binding carries repository source fields", "use one closed source arm")
	}
	return nil
}

// validateOracleRecipeSource enforces the repository arm of the closed
// source union for one control's pinned harness: a member Project, one
// normalized repository-contained path, and the pinned commit, with no
// knowledge fields. Git object resolution happens at dispatch preparation,
// not at store admission; here the identity is shape-checked and pinned.
func validateOracleRecipeSource(source WorkContextReadingSource) error {
	if source.Kind != WorkContextSourceRepositoryFile {
		return oracleFailure(KindInvalidPayload, "acceptance oracle recipe_source must be a repository file source", "pin the harness with project_id, path, and commit_oid")
	}
	if len(source.ProjectID) < 2 || len(source.ProjectID) > oracleProjectIDMaxBytes {
		return oracleFailure(KindInvalidPayload, "acceptance oracle recipe_source project_id must be between 2 and 128 bytes", "name the Project that owns the harness repository")
	}
	if err := validateWorkContextRepoPath(source.Path); err != nil {
		return oracleFailure(KindInvalidPayload, "acceptance oracle recipe_source path must be a contained repository path", "supply a normalized relative path")
	}
	if !workContextCommitOID(source.CommitOID) {
		return oracleFailure(KindInvalidPayload, "acceptance oracle recipe_source commit_oid must be 40 to 64 hex bytes", "pin the commit the harness was taken at")
	}
	if source.SourceID != "" || source.LawID != "" || source.ContentHash != "" || source.Selector != nil {
		return oracleFailure(KindInvalidPayload, "acceptance oracle recipe_source carries knowledge source fields", "use one closed source arm")
	}
	return nil
}

// validateOracleOwnerEntry enforces one owner's closed shape: the Domain,
// the mechanism's contained repository identity, the bounded obligation,
// the covered predicates, and the pinned law bindings.
func validateOracleOwnerEntry(owner OracleOwner) error {
	if len(owner.DomainID) < 1 || len(owner.DomainID) > oracleDomainIDMaxBytes {
		return oracleFailure(KindInvalidPayload, "acceptance oracle owner "+owner.OwnerID+" domain_id must be between 1 and 256 bytes", "name an approved affected Domain")
	}
	if len(owner.Mechanism.ProjectID) < 2 || len(owner.Mechanism.ProjectID) > oracleProjectIDMaxBytes {
		return oracleFailure(KindInvalidPayload, "acceptance oracle owner "+owner.OwnerID+" mechanism project_id must be between 2 and 128 bytes", "name the Project that owns the mechanism's repository")
	}
	if err := validateWorkContextRepoPath(owner.Mechanism.Path); err != nil {
		return oracleFailure(KindInvalidPayload, "acceptance oracle owner "+owner.OwnerID+" mechanism path must be a contained repository path", "supply a normalized relative source path")
	}
	if err := oracleBoundedText(owner.Mechanism.EntryPoint, "owner "+owner.OwnerID+" mechanism entry_point"); err != nil {
		return err
	}
	if err := oracleBoundedText(owner.Obligation, "owner "+owner.OwnerID+" obligation"); err != nil {
		return err
	}
	if len(owner.PredicateIDs) < 1 || len(owner.PredicateIDs) > OracleOwnerPredicatesMax {
		return oracleFailure(KindInvalidPayload, "acceptance oracle owner "+owner.OwnerID+" must cover 1 to 8 predicates", "name the parent-approved predicates this owner covers")
	}
	if err := validateUniqueOracleReferences(owner.PredicateIDs, func(value string) bool { return reviewPredicatePattern.MatchString(value) },
		"acceptance oracle owner "+owner.OwnerID+" names predicate ", " outside the declared predicate grammar", "acceptance oracle owner "+owner.OwnerID+" covers predicate ", "cover each predicate once per owner"); err != nil {
		return err
	}
	return validateOracleLawBindings(owner.OwnerID, owner.LawBindings)
}

// validateOracleLawBindings enforces one owner's pinned law bindings: the
// closed knowledge source arm, the bounded clause, and one binding per law.
func validateOracleLawBindings(ownerID string, bindings []OracleLawBinding) error {
	if len(bindings) > OracleLawBindingsMax {
		return oracleFailure(KindInvalidPayload, "acceptance oracle owner "+ownerID+" carries more than 8 law bindings", "bind at most 8 pinned law revisions per owner")
	}
	seenLaws := map[string]bool{}
	for _, binding := range bindings {
		if err := validateOracleLawSource(binding.Source); err != nil {
			return err
		}
		if seenLaws[binding.Source.LawID] {
			return oracleFailure(KindInvalidPayload, "acceptance oracle owner "+ownerID+" binds law "+binding.Source.LawID+" twice", "bind each law once per owner")
		}
		seenLaws[binding.Source.LawID] = true
		if err := oracleBoundedText(binding.Clause, "law binding clause for law "+binding.Source.LawID); err != nil {
			return err
		}
	}
	return nil
}

// validateOracleCases proves every case entry's closed shape and declared owner.
func validateOracleCases(entries []OracleCase, owners map[string]OracleOwner) error {
	if !oracleJudgedHistorySafe(&AcceptanceOracle{Cases: entries}, &AcceptanceOracle{Cases: entries}) {
		return oracleFailure(KindInvalidPayload, "acceptance oracle declares executable and Review-judged cases on the same owner and entry point", "keep executable obligations executable; judgment cannot replace them")
	}
	cases := map[string]OracleCase{}
	for _, entry := range entries {
		if !oracleCaseIDPattern.MatchString(entry.CaseID) {
			return oracleFailure(KindInvalidPayload, "acceptance oracle case_id "+entry.CaseID+" must match case:<reference>", "name each case with a stable case: identity")
		}
		if _, exists := cases[entry.CaseID]; exists {
			return oracleFailure(KindInvalidPayload, "acceptance oracle case "+entry.CaseID+" is declared twice", "declare each case once")
		}
		if _, exists := owners[entry.OwnerID]; !exists {
			return oracleFailure(KindInvalidPayload, "acceptance oracle case "+entry.CaseID+" names undeclared owner "+entry.OwnerID, "map every case to a declared owner")
		}
		for _, text := range []struct {
			value string
			label string
		}{{entry.EntryPoint, "entry_point"}, {entry.InputClass, "input_class"}, {entry.ExpectedState, "expected_state"}} {
			if err := oracleBoundedText(text.value, "case "+entry.CaseID+" "+text.label); err != nil {
				return err
			}
		}
		if entry.Judge != "" {
			if entry.Judge != OracleJudgeReview || entry.ControlIDs != nil {
				return oracleFailure(KindInvalidPayload, "acceptance oracle judged case "+entry.CaseID+" must name judge review and no controls", "declare either executable controls or Review judgment")
			}
		} else if len(entry.ControlIDs) < 1 || len(entry.ControlIDs) > OracleCaseControlsMax {
			return oracleFailure(KindInvalidPayload, "acceptance oracle case "+entry.CaseID+" must name 1 to 8 controls", "map every case to at least one executable control")
		}
		if err := validateUniqueOracleReferences(entry.ControlIDs, func(value string) bool { return oracleControlIDPattern.MatchString(value) },
			"acceptance oracle case "+entry.CaseID+" names control ", " outside the control grammar", "acceptance oracle case "+entry.CaseID+" names control ", "name each control once per case"); err != nil {
			return err
		}
		cases[entry.CaseID] = entry
	}
	return nil
}

func oracleJudgedHistorySafe(previous, latest *AcceptanceOracle) bool {
	if previous == nil {
		return true
	}
	for _, judged := range latest.Cases {
		if judged.Judge != OracleJudgeReview {
			continue
		}
		for _, entry := range previous.Cases {
			if entry.Judge == "" && (entry.CaseID == judged.CaseID || entry.OwnerID == judged.OwnerID && entry.EntryPoint == judged.EntryPoint) {
				return false
			}
		}
	}
	return true
}

// Preparation validates executable content before readiness exists.
func validateOracleControlDefinition(control OracleControl) error {
	if len(control.PredicateIDs) < 1 || len(control.PredicateIDs) > OracleControlPredicatesMax {
		return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" must exercise 1 to 8 predicates", "name the predicates this control exercises")
	}
	if err := validateUniqueOracleReferences(control.PredicateIDs, func(value string) bool { return reviewPredicatePattern.MatchString(value) },
		"acceptance oracle control "+control.ControlID+" names predicate ", " outside the declared predicate grammar", "acceptance oracle control "+control.ControlID+" exercises predicate ", "exercise each predicate once per control"); err != nil {
		return err
	}
	if len(control.CaseIDs) < 1 || len(control.CaseIDs) > OracleControlCasesMax {
		return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" must name 1 to 64 cases", "name the cases this control exercises")
	}
	if err := validateUniqueOracleReferences(control.CaseIDs, func(value string) bool { return oracleCaseIDPattern.MatchString(value) },
		"acceptance oracle control "+control.ControlID+" names case ", " outside the case grammar", "acceptance oracle control "+control.ControlID+" names case ", "name each case once per control"); err != nil {
		return err
	}
	if err := validateOracleRecipeSource(control.RecipeSource); err != nil {
		return err
	}
	if err := validateOracleArgv(control); err != nil {
		return err
	}
	if err := validateOracleControlDirectory(control.Cwd); err != nil {
		return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" cwd must be a contained relative directory", "supply a contained working directory")
	}
	if control.ExpectedResult != OracleExpectedResultPass {
		return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" expected_result must be pass in the first slice", "declare the exit-status result the harness must produce")
	}
	if control.RequiredEvidenceRole != OracleEvidenceRoleReported && control.RequiredEvidenceRole != OracleEvidenceRoleIndependentlyExecuted {
		return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" required_evidence_role must be reported or independently_executed", "declare the evidence role the control's receipt must carry")
	}
	return nil
}

// A control directory can name the repository root. Every other directory
// retains the normalized containment and byte bounds of repository paths.
func validateOracleControlDirectory(directory string) error {
	if directory == "." {
		return nil
	}
	return validateWorkContextRepoPath(directory)
}

// validateOracleArgv enforces one control's exact argument vector: 1 to 32
// present arguments, each bounded and free of control characters. The
// arguments are an exact vector, never shell text.
func validateOracleArgv(control OracleControl) error {
	if len(control.Argv) < 1 || len(control.Argv) > OracleArgvMax {
		return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" must carry 1 to 32 arguments", "supply the exact argument vector")
	}
	for _, argument := range control.Argv {
		if len(argument) < 1 || len(argument) > oracleArgMaxBytes {
			return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" arguments must be between 1 and 512 bytes", "supply bounded arguments, not shell text")
		}
		if strings.ContainsFunc(argument, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" arguments must not contain control characters", "supply the argument vector, not shell text")
		}
	}
	return nil
}

// validateUniqueOracleReferences proves one reference list's entries unique
// and grammar-valid, with the caller's own refusal phrasing for the two
// failure shapes.
func validateUniqueOracleReferences(values []string, grammar func(string) bool, malformedPrefix, malformedSuffix, duplicatePrefix, duplicateRemedy string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !grammar(value) {
			return oracleFailure(KindInvalidPayload, malformedPrefix+value+malformedSuffix, "name bounded declared identities")
		}
		if seen[value] {
			return oracleFailure(KindInvalidPayload, duplicatePrefix+value+" twice", duplicateRemedy)
		}
		seen[value] = true
	}
	return nil
}

// validateOracleCombinedBound measures the oracle's serialized content. An
// over-bound oracle refuses whole; content is never truncated.
func validateOracleCombinedBound(oracle *AcceptanceOracle) error {
	raw, err := json.Marshal(oracle)
	if err != nil {
		return oracleFailure(KindInvalidPayload, "acceptance oracle content cannot be measured", "repair the oracle content")
	}
	if len(raw) > OracleCombinedMaxBytes {
		return oracleFailure(KindLimitExceeded, fmt.Sprintf("acceptance oracle content is %d bytes and exceeds the %d byte bound; it is refused, never truncated", len(raw), OracleCombinedMaxBytes), "reduce the oracle's repeated descriptions and select only necessary controls")
	}
	return nil
}

func oracleEntryIdentical[T any](previous, latest T) bool {
	left, leftErr := json.Marshal(previous)
	right, rightErr := json.Marshal(latest)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

// validateAcceptanceOracleAuthorityTx joins one authored oracle against the
// authorities the parent contract and the work already hold: every covered
// predicate is approved by the active contract, every owner Domain is
// current in the Product registry and inside the contract's approved
// affected scope, every law binding matches the contract's pinned law
// revision, every named Project is a member Project of the work, and every
// readiness evidence reference names evidence this work retained. These are
// author-time joins: the fold re-proves only the closed graph, so replay
// never needs today's registry.
func validateAcceptanceOracleAuthorityTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, oracle *AcceptanceOracle) error {
	if err := validateOraclePredicateAuthorityTx(ctx, tx, workID, contractVersion, oracle); err != nil {
		return err
	}
	if err := validateOracleOwnerAuthorityTx(ctx, tx, workID, contractVersion, oracle); err != nil {
		return err
	}
	return nil
}

func validateOraclePredicateAuthorityTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, oracle *AcceptanceOracle) error {
	predicates := map[string]bool{}
	for _, owner := range oracle.Owners {
		for _, predicate := range owner.PredicateIDs {
			predicates[predicate] = true
		}
	}
	for _, control := range oracle.Controls {
		for _, predicate := range control.PredicateIDs {
			predicates[predicate] = true
		}
	}
	for predicate := range predicates {
		var approved int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? AND predicate_id=?`, workID, contractVersion, predicate).Scan(&approved)
		if err == sql.ErrNoRows {
			return oracleFailure(KindInvalidPayload, "acceptance oracle predicate "+predicate+" is not approved by the active parent contract", "cover only predicates the active parent contract approved")
		}
		if err != nil {
			return wrapFailure(KindUnavailable, "acceptance_oracle", "cannot verify the acceptance-oracle predicate authority", true, "retry once the contract projection is readable", err)
		}
	}
	return nil
}

func validateOracleOwnerAuthorityTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, oracle *AcceptanceOracle) error {
	domains := map[string]bool{}
	projects := map[string]bool{}
	laws := map[OracleLawBinding]bool{}
	for _, owner := range oracle.Owners {
		domains[owner.DomainID] = true
		projects[owner.Mechanism.ProjectID] = true
		for _, binding := range owner.LawBindings {
			laws[binding] = true
		}
	}
	for _, control := range oracle.Controls {
		projects[control.RecipeSource.ProjectID] = true
	}
	for domainID := range domains {
		if err := validateOracleDomainTx(ctx, tx, workID, contractVersion, "acceptance oracle", domainID); err != nil {
			return err
		}
	}
	for projectID := range projects {
		var member int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM work_projects WHERE work_id=? AND project_id=?`, workID, projectID).Scan(&member)
		if err == sql.ErrNoRows {
			return oracleFailure(KindInvalidPayload, "acceptance oracle names Project "+projectID+" that is not a member Project of the work", "name a Project the work holds")
		}
		if err != nil {
			return wrapFailure(KindUnavailable, "acceptance_oracle", "cannot verify the acceptance-oracle Project scope", true, "retry once the membership projection is readable", err)
		}
	}
	for binding := range laws {
		if err := validateOracleLawPinTx(ctx, tx, workID, contractVersion, "acceptance oracle", binding); err != nil {
			return err
		}
	}
	return nil
}

// validateOracleLawPinTx joins one law binding against the active contract's
// pinned law revisions: the law must be pinned at the binding's content
// hash. Surface names the carrying member in every refusal.
func validateOracleLawPinTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, surface string, binding OracleLawBinding) error {
	var pinnedHash string
	err := tx.QueryRowContext(ctx, `SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=? AND law_id=?`, workID, contractVersion, binding.Source.LawID).Scan(&pinnedHash)
	if err == sql.ErrNoRows {
		return oracleFailure(KindInvalidPayload, surface+" binds law "+binding.Source.LawID+" that the active parent contract did not pin", "bind a law revision the active contract pinned")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "acceptance_oracle", "cannot verify the "+surface+" law pin", true, "retry once the contract projection is readable", err)
	}
	if pinnedHash != binding.Source.ContentHash {
		return oracleFailure(KindInvalidPayload, surface+" binds law "+binding.Source.LawID+" at a content hash the active parent contract did not pin", "bind the pinned law revision content hash")
	}
	return nil
}

// validateOracleDomainTx joins one owner Domain against the current Product
// registry and the active contract's approved affected scope. It mirrors the
// registry and scope joins of ValidateWorkContextDomainTx without that
// surface's product-wide rationale rule: an owner names a mechanism inside
// one Domain, and the oracle shape carries no rationale field to import
// that rule's semantics. Surface names the carrying member in every refusal.
func validateOracleDomainTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, surface, domainID string) error {
	productID, err := architectureBindingProductIDTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	var rootDomainID, registryHash string
	if err := tx.QueryRowContext(ctx, `SELECT root_domain_id,content_hash FROM domain_registries WHERE product_id=?`, productID).Scan(&rootDomainID, &registryHash); err != nil {
		if err == sql.ErrNoRows {
			return oracleFailure(KindUnknownScope, "Product has no current Domain registry", "publish and rebuild the Product Domain registry")
		}
		return wrapFailure(KindUnavailable, "acceptance_oracle", "cannot read the Product Domain registry", true, "retry once the registry projection is readable", err)
	}
	var status, domainHash string
	if err := tx.QueryRowContext(ctx, `SELECT status,registry_content_hash FROM domains WHERE product_id=? AND domain_id=?`, productID, domainID).Scan(&status, &domainHash); err != nil {
		if err == sql.ErrNoRows {
			var elsewhere int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domains WHERE domain_id=?`, domainID).Scan(&elsewhere); err != nil {
				return wrapFailure(KindUnavailable, "acceptance_oracle", "cannot resolve the named Domain", true, "retry once the Domain projection is readable", err)
			}
			if elsewhere > 0 {
				return oracleFailure(KindUnknownScope, surface+" Domain "+domainID+" belongs to another Product", "name a current Domain of the work's Product")
			}
			return oracleFailure(KindUnknownScope, surface+" names unknown Domain "+domainID, "name a current Domain of the Product registry")
		}
		return wrapFailure(KindUnavailable, "acceptance_oracle", "cannot read the named Domain", true, "retry once the Domain projection is readable", err)
	}
	if status != "current" || domainHash != registryHash {
		return oracleFailure(KindStaleRequiresReview, surface+" names non-current or stale Domain "+domainID, "rebuild the current Product Domain registry")
	}
	var binding int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&binding)
	if err == nil {
		var affected int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contract_affected_domains WHERE work_id=? AND contract_version=? AND domain_id=?`, workID, contractVersion, domainID).Scan(&affected); err != nil {
			return wrapFailure(KindUnavailable, "acceptance_oracle", "cannot read the approved affected Domain scope", true, "retry once the contract projection is readable", err)
		}
		if affected == 0 {
			return oracleFailure(KindUnknownScope, surface+" Domain "+domainID+" is outside the approved affected Domain scope of the active contract", "name a Domain the approved contract affects")
		}
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "acceptance_oracle", "cannot read the contract architecture binding", true, "retry once the contract projection is readable", err)
	}
	return nil
}
