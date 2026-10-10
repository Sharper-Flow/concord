package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// An acceptance oracle is immutable worker-job content retained by the event
// log and covered by the job digest. Replay validates its closed graph from
// retained content alone. Authoring also joins the live contract, Domain
// registry, pinned law, Project membership, and retained readiness evidence.

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
	oracleOwnerIDPattern   = regexp.MustCompile(`^owner:[A-Za-z0-9][A-Za-z0-9._:-]{0,126}$`)
	oracleCaseIDPattern    = regexp.MustCompile(`^case:[A-Za-z0-9][A-Za-z0-9._:-]{0,126}$`)
	oracleControlIDPattern = regexp.MustCompile(`^control:[A-Za-z0-9][A-Za-z0-9._:-]{0,126}$`)
)

// The closed result and evidence-role vocabulary. The declared result
// is the exit status, not parsed natural language; a reported control
// execution stays reported evidence until an independent execution role or
// an existing verdict owner supports it.
const (
	OracleExpectedResultPass                = "pass"
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
// transition, its input class, the expected state, and the exact controls
// that exercise it. Descriptions are bounded prose, never a predicate DSL.
type OracleCase struct {
	CaseID        string   `json:"case_id"`
	OwnerID       string   `json:"owner_id"`
	EntryPoint    string   `json:"entry_point"`
	InputClass    string   `json:"input_class"`
	ExpectedState string   `json:"expected_state"`
	ControlIDs    []string `json:"control_ids"`
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

// AcceptanceOracle is the closed three-part graph one immutable worker-job
// revision carries: owners with obligations, a finite case inventory, and
// executable controls with exact evidence requirements.
type AcceptanceOracle struct {
	Owners   []OracleOwner   `json:"owners"`
	Cases    []OracleCase    `json:"cases"`
	Controls []OracleControl `json:"controls"`
}

// oracleFailure bounds the refusal messages of this surface.
func oracleFailure(kind FailureKind, detail, remedy string) error {
	return newFailure(kind, "worker_oracle", detail, false, remedy)
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
	if source.Kind != WorkContextSourceKnowledge {
		return oracleFailure(KindInvalidPayload, "acceptance oracle law binding source must be a knowledge source", "bind law by source_id, law_id, and the pinned content hash")
	}
	if len(source.SourceID) < 2 || len(source.SourceID) > oracleProjectIDMaxBytes {
		return oracleFailure(KindInvalidPayload, "acceptance oracle law binding source_id must be between 2 and 128 bytes", "name the knowledge source")
	}
	if len(source.LawID) < 2 || len(source.LawID) > workContextLawIDMaxBytes {
		return oracleFailure(KindInvalidPayload, "acceptance oracle law binding law_id must be between 2 and 256 bytes", "name the bound law")
	}
	if !workflowDigest(source.ContentHash) {
		return oracleFailure(KindInvalidPayload, "acceptance oracle law binding content_hash must be a sha256 digest", "pin the law revision content hash")
	}
	if source.ProjectID != "" || source.Path != "" || source.CommitOID != "" {
		return oracleFailure(KindInvalidPayload, "acceptance oracle law binding carries repository source fields", "use one closed source arm")
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
	if source.SourceID != "" || source.LawID != "" || source.ContentHash != "" {
		return oracleFailure(KindInvalidPayload, "acceptance oracle recipe_source carries knowledge source fields", "use one closed source arm")
	}
	return nil
}

// validateAcceptanceOracle enforces the closed oracle graph against the
// job's declared predicates: counts and bounds, unique identities, the
// closed source arms, reciprocal case/control mapping, owner resolution for
// every case and control, and coverage of every job predicate by both an
// owner and a control. It reads no registry, so replay re-proves a recorded
// oracle from the retained historical input alone. The per-part validators
// below own one cohesive surface each; this function only composes them.
func validateAcceptanceOracle(oracle *AcceptanceOracle, jobPredicates []string) error {
	if oracle == nil {
		return oracleFailure(KindInvalidPayload, "acceptance oracle is absent", "author the closed owner/case/control oracle for the job")
	}
	for _, part := range []struct {
		count int
		bound int
		label string
	}{{len(oracle.Owners), OracleOwnersMax, "owners"}, {len(oracle.Cases), OracleCasesMax, "cases"}, {len(oracle.Controls), OracleControlsMax, "controls"}} {
		if part.count < 1 || part.count > part.bound {
			return oracleFailure(KindInvalidPayload, fmt.Sprintf("acceptance oracle carries %d %s; 1 to %d are declared", part.count, part.label, part.bound), "declare at most "+fmt.Sprint(part.bound)+" "+part.label)
		}
	}
	owners, ownerPredicates, err := validateOracleOwners(oracle.Owners)
	if err != nil {
		return err
	}
	cases, err := validateOracleCases(oracle.Cases, owners)
	if err != nil {
		return err
	}
	controlIndex, controlPredicates, err := validateOracleControls(oracle.Controls, owners)
	if err != nil {
		return err
	}
	if err := validateOracleReciprocity(oracle, cases, controlIndex); err != nil {
		return err
	}
	if err := validateOraclePredicateCoverage(jobPredicates, ownerPredicates, controlPredicates); err != nil {
		return err
	}
	return validateOracleCombinedBound(oracle)
}

// validateOracleOwners proves every owner entry's closed shape and returns
// the identity index plus the union of covered predicates. A duplicate
// identity refuses before any index use, so the index is exact.
func validateOracleOwners(entries []OracleOwner) (map[string]OracleOwner, map[string]bool, error) {
	owners := map[string]OracleOwner{}
	ownerPredicates := map[string]bool{}
	for _, owner := range entries {
		if !oracleOwnerIDPattern.MatchString(owner.OwnerID) {
			return nil, nil, oracleFailure(KindInvalidPayload, "acceptance oracle owner_id "+owner.OwnerID+" must match owner:<reference>", "name each owner with a stable owner: identity")
		}
		if _, exists := owners[owner.OwnerID]; exists {
			return nil, nil, oracleFailure(KindInvalidPayload, "acceptance oracle owner "+owner.OwnerID+" is declared twice", "declare each owner once")
		}
		if err := validateOracleOwnerEntry(owner); err != nil {
			return nil, nil, err
		}
		for _, predicate := range owner.PredicateIDs {
			ownerPredicates[predicate] = true
		}
		owners[owner.OwnerID] = owner
	}
	return owners, ownerPredicates, nil
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
	if err := validateUniqueOracleReferences(owner.PredicateIDs, func(value string) bool { return workerJobPredicatePattern.MatchString(value) },
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

// validateOracleCases proves every case entry's closed shape and returns the
// identity index, with each case mapped to a declared owner.
func validateOracleCases(entries []OracleCase, owners map[string]OracleOwner) (map[string]OracleCase, error) {
	cases := map[string]OracleCase{}
	for _, entry := range entries {
		if !oracleCaseIDPattern.MatchString(entry.CaseID) {
			return nil, oracleFailure(KindInvalidPayload, "acceptance oracle case_id "+entry.CaseID+" must match case:<reference>", "name each case with a stable case: identity")
		}
		if _, exists := cases[entry.CaseID]; exists {
			return nil, oracleFailure(KindInvalidPayload, "acceptance oracle case "+entry.CaseID+" is declared twice", "declare each case once")
		}
		if _, exists := owners[entry.OwnerID]; !exists {
			return nil, oracleFailure(KindInvalidPayload, "acceptance oracle case "+entry.CaseID+" names undeclared owner "+entry.OwnerID, "map every case to a declared owner")
		}
		for _, text := range []struct {
			value string
			label string
		}{{entry.EntryPoint, "entry_point"}, {entry.InputClass, "input_class"}, {entry.ExpectedState, "expected_state"}} {
			if err := oracleBoundedText(text.value, "case "+entry.CaseID+" "+text.label); err != nil {
				return nil, err
			}
		}
		if len(entry.ControlIDs) < 1 || len(entry.ControlIDs) > OracleCaseControlsMax {
			return nil, oracleFailure(KindInvalidPayload, "acceptance oracle case "+entry.CaseID+" must name 1 to 8 controls", "map every case to at least one executable control")
		}
		if err := validateUniqueOracleReferences(entry.ControlIDs, func(value string) bool { return oracleControlIDPattern.MatchString(value) },
			"acceptance oracle case "+entry.CaseID+" names control ", " outside the control grammar", "acceptance oracle case "+entry.CaseID+" names control ", "name each control once per case"); err != nil {
			return nil, err
		}
		cases[entry.CaseID] = entry
	}
	return cases, nil
}

// validateOracleControls proves every control entry's closed shape and
// returns the identity index plus the union of exercised predicates, with
// each control mapped to a declared owner.
func validateOracleControls(entries []OracleControl, owners map[string]OracleOwner) (map[string]OracleControl, map[string]bool, error) {
	controlIndex := map[string]OracleControl{}
	controlPredicates := map[string]bool{}
	for _, control := range entries {
		if !oracleControlIDPattern.MatchString(control.ControlID) {
			return nil, nil, oracleFailure(KindInvalidPayload, "acceptance oracle control_id "+control.ControlID+" must match control:<reference>", "name each control with a stable control: identity")
		}
		if _, exists := controlIndex[control.ControlID]; exists {
			return nil, nil, oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" is declared twice", "declare each control once")
		}
		if _, exists := owners[control.OwnerID]; !exists {
			return nil, nil, oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" names undeclared owner "+control.OwnerID, "map every control to a declared owner")
		}
		if err := validateOracleControlEntry(control); err != nil {
			return nil, nil, err
		}
		for _, predicate := range control.PredicateIDs {
			controlPredicates[predicate] = true
		}
		controlIndex[control.ControlID] = control
	}
	return controlIndex, controlPredicates, nil
}

// validateOracleControlEntry enforces one control's closed shape: the
// exercised predicates, the named cases, the pinned recipe source, the exact
// argument vector, the contained working directory, the closed result and
// evidence-role vocabulary, and the readiness evidence references.
func validateOracleControlEntry(control OracleControl) error {
	if err := validateOracleControlDefinition(control); err != nil {
		return err
	}
	if len(control.ReadinessEvidenceRefs) < 1 || len(control.ReadinessEvidenceRefs) > OracleReadinessRefsMax {
		return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" must carry 1 to 8 readiness evidence references", "bind the evidence that proves the harness resolves and the selector is nonempty")
	}
	return validateUniqueOracleReferences(control.ReadinessEvidenceRefs, workflowEvidenceRef,
		"acceptance oracle control "+control.ControlID+" readiness evidence reference ", " is not a bounded evidence reference", "acceptance oracle control "+control.ControlID+" readiness evidence reference ", "bind each retained readiness evidence reference once")
}

// Preparation validates executable content before readiness exists. Recorded
// job controls also require the separate readiness-reference validator.
func validateOracleControlDefinition(control OracleControl) error {
	if len(control.PredicateIDs) < 1 || len(control.PredicateIDs) > OracleControlPredicatesMax {
		return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" must exercise 1 to 8 predicates", "name the predicates this control exercises")
	}
	if err := validateUniqueOracleReferences(control.PredicateIDs, func(value string) bool { return workerJobPredicatePattern.MatchString(value) },
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
	if err := validateWorkContextRepoPath(control.Cwd); err != nil {
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

// validateOracleReciprocity proves the case/control mapping total: a case
// names exactly the controls that name it, and a control names exactly the
// cases that name it. A one-sided reference is an unresolved mapping.
func validateOracleReciprocity(oracle *AcceptanceOracle, cases map[string]OracleCase, controlIndex map[string]OracleControl) error {
	for _, entry := range oracle.Cases {
		for _, controlID := range entry.ControlIDs {
			named, exists := controlIndex[controlID]
			if !exists {
				return oracleFailure(KindInvalidPayload, "acceptance oracle case "+entry.CaseID+" names undeclared control "+controlID, "map every case to a declared control")
			}
			if !containsString(named.CaseIDs, entry.CaseID) {
				return oracleFailure(KindInvalidPayload, "acceptance oracle control "+controlID+" does not name back case "+entry.CaseID, "keep the case/control mapping reciprocal")
			}
		}
	}
	for _, control := range oracle.Controls {
		for _, caseID := range control.CaseIDs {
			named, exists := cases[caseID]
			if !exists {
				return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" names undeclared case "+caseID, "map every control to a declared case")
			}
			if !containsString(named.ControlIDs, control.ControlID) {
				return oracleFailure(KindInvalidPayload, "acceptance oracle case "+caseID+" does not name back control "+control.ControlID, "keep the case/control mapping reciprocal")
			}
		}
	}
	return nil
}

// validateOraclePredicateCoverage proves the oracle covers the job's
// declared predicates: every job predicate is covered by an owner and
// exercised by a control.
func validateOraclePredicateCoverage(jobPredicates []string, ownerPredicates, controlPredicates map[string]bool) error {
	for _, predicate := range jobPredicates {
		if !ownerPredicates[predicate] {
			return oracleFailure(KindInvalidPayload, "acceptance oracle leaves job predicate "+predicate+" without an owning owner", "map every job predicate to an owner")
		}
		if !controlPredicates[predicate] {
			return oracleFailure(KindInvalidPayload, "acceptance oracle leaves job predicate "+predicate+" without an exercising control", "map every job predicate to a control")
		}
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

// oracleControlIndex indexes one oracle's controls by identity for the
// retention walk. Callers admit the oracle through
// validateAcceptanceOracle first, so duplicate identities cannot survive
// into an index.
func oracleControlIndex(oracle *AcceptanceOracle) map[string]OracleControl {
	index := make(map[string]OracleControl, len(oracle.Controls))
	for _, control := range oracle.Controls {
		index[control.ControlID] = control
	}
	return index
}

// oracleControlsRetained reports whether the latest oracle retains every
// previous owner, case, and control's obligation: the pinned recipe identity,
// the argument vector, the expected result, and the evidence role. Readiness
// evidence references are normalized out: they are candidate-specific
// pointers to that revision's native preparations, excluded from the protocol
// bundle digest and re-proven against the current candidate by the exact
// native-readiness gate at record time, so refreshing them is not a rewrite
// of the obligation. A newer revision may add cases and controls — a
// strengthened inventory — but a deleted or changed required control does not
// discharge the earlier obligation, and a re-pinned recipe never compares as
// retained.
func oracleControlsRetained(previous, latest *AcceptanceOracle) bool {
	if previous == nil {
		return true
	}
	if latest == nil {
		return false
	}
	owners := make(map[string]OracleOwner, len(latest.Owners))
	for _, owner := range latest.Owners {
		owners[owner.OwnerID] = owner
	}
	for _, owner := range previous.Owners {
		retained, exists := owners[owner.OwnerID]
		if !exists || !oracleEntryIdentical(owner, retained) {
			return false
		}
	}
	cases := make(map[string]OracleCase, len(latest.Cases))
	for _, entry := range latest.Cases {
		cases[entry.CaseID] = entry
	}
	for _, entry := range previous.Cases {
		retained, exists := cases[entry.CaseID]
		if !exists || !oracleEntryIdentical(entry, retained) {
			return false
		}
	}
	latestControls := oracleControlIndex(latest)
	for _, control := range previous.Controls {
		retained, exists := latestControls[control.ControlID]
		if !exists || !oracleControlObligationEqual(control, retained) {
			return false
		}
	}
	return true
}

// oracleControlObligationEqual compares two controls on every semantic
// obligation field, with the candidate-specific readiness references
// normalized out of both sides.
func oracleControlObligationEqual(previous, latest OracleControl) bool {
	previous.ReadinessEvidenceRefs = nil
	latest.ReadinessEvidenceRefs = nil
	return oracleEntryIdentical(previous, latest)
}

func oracleEntryIdentical[T any](previous, latest T) bool {
	left, leftErr := json.Marshal(previous)
	right, rightErr := json.Marshal(latest)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

// decodeAcceptanceOracle decodes one authored oracle strictly: an unknown
// member is a payload no pinned contract declared, so it refuses rather
// than silently dropping content out of the digest.
func decodeAcceptanceOracle(raw json.RawMessage) (*AcceptanceOracle, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var oracle AcceptanceOracle
	if err := decoder.Decode(&oracle); err != nil {
		return nil, oracleFailure(KindInvalidPayload, "record_worker_job acceptance_oracle is not one closed acceptance-oracle object", "supply the closed owners/cases/controls object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, oracleFailure(KindInvalidPayload, "record_worker_job acceptance_oracle is not one closed acceptance-oracle object", "supply exactly one acceptance-oracle object")
	}
	return &oracle, nil
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
			return wrapFailure(KindUnavailable, "worker_oracle", "cannot verify the acceptance-oracle predicate authority", true, "retry once the contract projection is readable", err)
		}
	}
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
		if err := validateOracleDomainTx(ctx, tx, workID, contractVersion, domainID); err != nil {
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
			return wrapFailure(KindUnavailable, "worker_oracle", "cannot verify the acceptance-oracle Project scope", true, "retry once the membership projection is readable", err)
		}
	}
	for binding := range laws {
		var pinnedHash string
		err := tx.QueryRowContext(ctx, `SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=? AND law_id=?`, workID, contractVersion, binding.Source.LawID).Scan(&pinnedHash)
		if err == sql.ErrNoRows {
			return oracleFailure(KindInvalidPayload, "acceptance oracle binds law "+binding.Source.LawID+" that the active parent contract did not pin", "bind a law revision the active contract pinned")
		}
		if err != nil {
			return wrapFailure(KindUnavailable, "worker_oracle", "cannot verify the acceptance-oracle law pin", true, "retry once the contract projection is readable", err)
		}
		if pinnedHash != binding.Source.ContentHash {
			return oracleFailure(KindInvalidPayload, "acceptance oracle binds law "+binding.Source.LawID+" at a content hash the active parent contract did not pin", "bind the pinned law revision content hash")
		}
	}
	for _, control := range oracle.Controls {
		bundle, bundleErr := nativeBundleForControl(oracle, control.ControlID)
		if bundleErr != nil {
			return bundleErr
		}
		var project string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
			return err
		}
		current, subjectErr := readCurrentOracleSubject(ctx, tx, workID)
		if subjectErr != nil {
			return subjectErr
		}
		for _, ref := range control.ReadinessEvidenceRefs {
			preparation, err := readOraclePreparationReceiptTx(ctx, tx, workID, project, ref)
			if err != nil {
				return err
			}
			if preparation == nil || preparation.ContractVersion != contractVersion || preparation.SubjectCommit != current || preparation.BundleDigest != nativeBundleDigest(bundle) {
				return oracleFailure(KindInvalidPayload, "acceptance oracle control "+control.ControlID+" readiness evidence "+ref+" is not its exact qualified native preparation", "prepare the exact owner/case/control bundle before recording the ready job")
			}
		}
	}
	return nil
}

// validateOracleDomainTx joins one owner Domain against the current Product
// registry and the active contract's approved affected scope. It mirrors the
// registry and scope joins of ValidateWorkContextDomainTx without that
// surface's product-wide rationale rule: an owner names a mechanism inside
// one Domain, and the oracle shape carries no rationale field to import
// that rule's semantics.
func validateOracleDomainTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, domainID string) error {
	productID, err := architectureBindingProductIDTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	var rootDomainID, registryHash string
	if err := tx.QueryRowContext(ctx, `SELECT root_domain_id,content_hash FROM domain_registries WHERE product_id=?`, productID).Scan(&rootDomainID, &registryHash); err != nil {
		if err == sql.ErrNoRows {
			return oracleFailure(KindUnknownScope, "Product has no current Domain registry", "publish and rebuild the Product Domain registry")
		}
		return wrapFailure(KindUnavailable, "worker_oracle", "cannot read the Product Domain registry", true, "retry once the registry projection is readable", err)
	}
	var status, domainHash string
	if err := tx.QueryRowContext(ctx, `SELECT status,registry_content_hash FROM domains WHERE product_id=? AND domain_id=?`, productID, domainID).Scan(&status, &domainHash); err != nil {
		if err == sql.ErrNoRows {
			var elsewhere int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domains WHERE domain_id=?`, domainID).Scan(&elsewhere); err != nil {
				return wrapFailure(KindUnavailable, "worker_oracle", "cannot resolve the named Domain", true, "retry once the Domain projection is readable", err)
			}
			if elsewhere > 0 {
				return oracleFailure(KindUnknownScope, "acceptance oracle Domain "+domainID+" belongs to another Product", "name a current Domain of the work's Product")
			}
			return oracleFailure(KindUnknownScope, "acceptance oracle names unknown Domain "+domainID, "name a current Domain of the Product registry")
		}
		return wrapFailure(KindUnavailable, "worker_oracle", "cannot read the named Domain", true, "retry once the Domain projection is readable", err)
	}
	if status != "current" || domainHash != registryHash {
		return oracleFailure(KindStaleRequiresReview, "acceptance oracle names non-current or stale Domain "+domainID, "rebuild the current Product Domain registry")
	}
	var binding int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&binding)
	if err == nil {
		var affected int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contract_affected_domains WHERE work_id=? AND contract_version=? AND domain_id=?`, workID, contractVersion, domainID).Scan(&affected); err != nil {
			return wrapFailure(KindUnavailable, "worker_oracle", "cannot read the approved affected Domain scope", true, "retry once the contract projection is readable", err)
		}
		if affected == 0 {
			return oracleFailure(KindUnknownScope, "acceptance oracle Domain "+domainID+" is outside the approved affected Domain scope of the active contract", "name a Domain the approved contract affects")
		}
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "worker_oracle", "cannot read the contract architecture binding", true, "retry once the contract projection is readable", err)
	}
	return nil
}

// readWorkerJobOraclesTx is the one tx-scoped reader of recorded oracles:
// it scans the work item's worker.job_recorded events once, in log order,
// and maps each recorded revision binding onto the oracle its event
// retained (nil when the revision predates the oracle). The recorded digest
// travels with the event, so the map's keys carry the digest the recording
// derived. The read uses the queryer already in hand — never a *Store reach
// through the pool inside a transaction — and issues no nested query while
// rows are open.
func readWorkerJobOraclesTx(ctx context.Context, q queryer, workID string) (map[WorkerJobBinding]*AcceptanceOracle, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.job_id'), json_extract(payload,'$.revision'), json_extract(payload,'$.digest'), json_extract(payload,'$.acceptance_oracle') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=?`, string(SubjectWorkItem), workID, WorkerJobRecorded)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_oracle", "cannot read recorded acceptance oracles", true, "retry once the event log is readable", err)
	}
	oracles := map[WorkerJobBinding]*AcceptanceOracle{}
	for rows.Next() {
		var jobID, digest string
		var revision int64
		var rawOracle sql.NullString
		if err := rows.Scan(&jobID, &revision, &digest, &rawOracle); err != nil {
			_ = rows.Close()
			return nil, wrapFailure(KindUnavailable, "worker_oracle", "cannot scan recorded acceptance oracles", true, "retry once the event log is readable", err)
		}
		binding := WorkerJobBinding{JobID: jobID, Revision: revision, Digest: digest}
		if !rawOracle.Valid || rawOracle.String == "null" || strings.TrimSpace(rawOracle.String) == "" {
			oracles[binding] = nil
			continue
		}
		oracle, decodeErr := decodeAcceptanceOracle(json.RawMessage(rawOracle.String))
		if decodeErr != nil {
			_ = rows.Close()
			return nil, newFailure(KindInvariantViolation, "worker_oracle", "a recorded worker-job revision carries an acceptance oracle that is not one closed object", false, "rebuild the worker-job events from the authoritative log")
		}
		oracles[binding] = oracle
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapFailure(KindUnavailable, "worker_oracle", "cannot scan recorded acceptance oracles", true, "retry once the event log is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_oracle", "cannot close the recorded acceptance oracle read", true, "retry once the event log is readable", err)
	}
	return oracles, nil
}

// readWorkerJobOracle reads one recorded revision's retained oracle from
// the job-recorded event, under the exact binding a dispatch or correction
// owner already holds. A revision that predates the oracle reads as nil; a
// binding that names no recorded event, or names one under a different
// digest, refuses — the reader never synthesizes an oracle a recording did
// not retain.
func readWorkerJobOracle(ctx context.Context, q queryer, workID string, binding WorkerJobBinding) (*AcceptanceOracle, error) {
	oracles, err := readWorkerJobOraclesTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	for recorded, oracle := range oracles {
		if recorded.JobID != binding.JobID || recorded.Revision != binding.Revision {
			continue
		}
		if recorded.Digest != binding.Digest {
			return nil, oracleFailure(KindInvalidPayload, "the worker-job oracle binding names a digest the recorded revision does not hold", "read the oracle under the recorded revision digest")
		}
		return oracle, nil
	}
	return nil, oracleFailure(KindProjectionNotFound, "the worker-job oracle binding names no recorded revision", "record the job revision before reading its oracle")
}

// workflowOwnerOracleActive reports whether a pinned definition carries the
// acceptance oracle: the record_worker_job action's declared
// payload names the acceptance_oracle member. Capability is derived from
// the declared action member — historical definitions keep the payload they
// were pinned under, so no version switch or mutable behavior flag exists
// here.
func workflowOwnerOracleActive(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID != "record_worker_job" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "acceptance_oracle" {
				return true
			}
		}
		return false
	}
	return false
}
