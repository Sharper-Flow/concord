package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Closed bounds of the design-record architecture value surface. Per-field
// maxima are ceilings, not permission to exceed the 8,192-byte canonical
// bound every plan must also satisfy; an over-bound plan refuses whole.
const (
	designArchitectureOwnersMax         = 16
	designArchitectureInvariantsMax     = 32
	designArchitectureDataFlowsMax      = 32
	designArchitectureFailuresMax       = 32
	designArchitectureReferencesMax     = 64
	designArchitectureFilesMax          = 128
	designArchitectureUnchangedMax      = 128
	designArchitectureSlicesMax         = 32
	designArchitectureSliceFilesMax     = 64
	designArchitectureSliceTestsMax     = 32
	designArchitectureCanonicalMaxBytes = 8 * 1024
	designArchitectureStatementMaxBytes = 1024
	designArchitectureSymbolMaxBytes    = 512
	designArchitectureCapacityTotalMax  = 31744
	designArchitectureMinChangedLines   = 1
	designArchitectureMaxChangedLines   = 500
)

// The closed file-role and slice-kind vocabularies. Generated entries name a
// pinned generator source; mixed authored/generated files are source.
const (
	DesignArchitectureFileRoleSource    = "source"
	DesignArchitectureFileRoleTest      = "test"
	DesignArchitectureFileRoleGenerated = "generated"

	DesignArchitectureSliceKindEdit        = "edit"
	DesignArchitectureSliceKindIntegration = "integration"
)

// WorkflowDesignArchitecture is the closed execution-plan object one design
// record carries: identity joins, exact file sets, and slice bounds.
// Independent review judges its meaning; it stays distinct from the
// contract's Domain WorkflowArchitectureBinding, which owns Product-law scope.
type WorkflowDesignArchitecture struct {
	Owners          []WorkflowDesignArchitectureOwner           `json:"owners"`
	Invariants      []WorkflowDesignArchitectureInvariant       `json:"invariants"`
	DataFlow        []WorkflowDesignArchitectureDataFlow        `json:"data_flow"`
	FailureBehavior []WorkflowDesignArchitectureFailureBehavior `json:"failure_behavior"`
	References      []WorkflowDesignArchitectureReference       `json:"references"`
	Files           []WorkflowDesignArchitectureFile            `json:"files"`
	UnchangedFiles  []WorkflowDesignArchitectureUnchangedFile   `json:"unchanged_files"`
	Slices          []WorkflowDesignArchitectureSlice           `json:"slices"`
	Capacity        *WorkflowDesignArchitectureCapacity         `json:"capacity"`
}

type WorkflowDesignArchitectureOwner struct {
	OwnerID   string `json:"owner_id"`
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
	Symbol    string `json:"symbol"`
}

type WorkflowDesignArchitectureInvariant struct {
	InvariantID string `json:"invariant_id"`
	OwnerID     string `json:"owner_id"`
	Statement   string `json:"statement"`
}

type WorkflowDesignArchitectureDataFlow struct {
	FlowID       string   `json:"flow_id"`
	FromOwnerID  string   `json:"from_owner_id"`
	ToOwnerID    string   `json:"to_owner_id"`
	Value        string   `json:"value"`
	InvariantIDs []string `json:"invariant_ids"`
}

type WorkflowDesignArchitectureFailureBehavior struct {
	FailureID string   `json:"failure_id"`
	OwnerID   string   `json:"owner_id"`
	Condition string   `json:"condition"`
	Result    string   `json:"result"`
	Preserves []string `json:"preserves"`
}

// WorkflowDesignArchitectureReference pins one repository-file source with
// positive ordered line bounds; the bounds resolve against that pinned blob
// at dispatch preparation, so admission fixes identity only.
type WorkflowDesignArchitectureReference struct {
	ReferenceID string                   `json:"reference_id"`
	Source      WorkContextReadingSource `json:"source"`
	StartLine   int                      `json:"start_line"`
	EndLine     int                      `json:"end_line"`
}

// WorkflowDesignArchitectureFile classifies one file; this contract-approved
// map owns exclusions at conformance time, never a filename heuristic.
type WorkflowDesignArchitectureFile struct {
	ProjectID   string                    `json:"project_id"`
	Path        string                    `json:"path"`
	Role        string                    `json:"role"`
	GeneratedBy *WorkContextReadingSource `json:"generated_by,omitempty"`
}

type WorkflowDesignArchitectureUnchangedFile struct {
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
}

// WorkflowDesignArchitectureSlice is one ordered slice: a single owner,
// exact files of that owner's Project outside the unchanged set.
type WorkflowDesignArchitectureSlice struct {
	SliceID         string   `json:"slice_id"`
	Kind            string   `json:"kind"`
	Ordinal         int      `json:"ordinal"`
	OwnerID         string   `json:"owner_id"`
	Files           []string `json:"files"`
	InvariantIDs    []string `json:"invariant_ids"`
	ReferenceIDs    []string `json:"reference_ids"`
	Tests           []string `json:"tests"`
	EndState        string   `json:"end_state"`
	MaxChangedLines int      `json:"max_changed_lines"`
}

// WorkflowDesignArchitectureCapacity declares the eight variable serialized-
// byte reserves of the dispatch packet capacity profile beside the fixed
// 1,024-byte structural slot; slot use is measured at the capacity owner.
type WorkflowDesignArchitectureCapacity struct {
	DesignRecord      int `json:"design_record"`
	WorkerJob         int `json:"worker_job"`
	WorkContext       int `json:"work_context"`
	LawContext        int `json:"law_context"`
	ProposalRecord    int `json:"proposal_record"`
	WorkRecord        int `json:"work_record"`
	TaskPremise       int `json:"task_premise"`
	OutcomePredicates int `json:"outcome_predicates"`
}

type designArchitectureFileKey struct {
	projectID string
	path      string
}

func designArchitectureFailure(kind FailureKind, detail, remedy string) error {
	return newFailure(kind, "workflow_design_architecture", detail, false, remedy)
}

// decodeWorkflowDesignArchitecture decodes one closed architecture document
// with the design record's strict decoder: unknown members, duplicate keys,
// over-deep nesting, and trailing data refuse. Invalid UTF-8 refuses on the
// raw bytes before the JSON decoder can replace them with replacement
// characters; already-valid multibyte path bytes stay exact data.
func decodeWorkflowDesignArchitecture(data []byte) (WorkflowDesignArchitecture, error) {
	if !utf8.Valid(data) {
		return WorkflowDesignArchitecture{}, designArchitectureFailure(KindInvalidPayload, "architecture record is not valid UTF-8", "supply valid UTF-8 bytes instead of letting JSON replace invalid bytes")
	}
	var architecture WorkflowDesignArchitecture
	if err := decodeWorkflowActionPayloadStrict(data, &architecture); err != nil {
		return WorkflowDesignArchitecture{}, designArchitectureFailure(KindInvalidPayload, "architecture record is not a closed typed document", "supply only the closed architecture members")
	}
	return architecture, nil
}

// designArchitectureBoundedText enforces one bounded prose member: nonempty
// valid UTF-8 within its byte bound. Quotes, control bytes, and HTML stay
// data; the canonical bound measures their escaped serialized form, so no
// check here may refuse them.
func designArchitectureBoundedText(value, label string, max int) error {
	if len(value) < 1 || len(value) > max {
		return designArchitectureFailure(KindInvalidPayload, fmt.Sprintf("architecture %s must be between 1 and %d bytes", label, max), "bound the "+label)
	}
	if !utf8.ValidString(value) {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+label+" must be valid UTF-8", "repair the encoding instead of letting JSON replace bytes")
	}
	return nil
}

func designArchitectureCount(count, min, bound int, label, remedy string) error {
	if count < min || count > bound {
		return designArchitectureFailure(KindInvalidPayload, fmt.Sprintf("architecture carries %d %s; %d to %d are declared", count, label, min, bound), remedy)
	}
	return nil
}

// designArchitectureSection proves one section's count and unique bounded
// identities, returning the identity index.
func designArchitectureSection[T any](entries []T, min, bound int, label string, ident func(T) string) (map[string]bool, error) {
	if err := designArchitectureCount(len(entries), min, bound, label, "declare the section within its bounds"); err != nil {
		return nil, err
	}
	index := make(map[string]bool, len(entries))
	for _, entry := range entries {
		id := ident(entry)
		if !workflowSubjectPattern.MatchString(id) {
			return nil, designArchitectureFailure(KindInvalidPayload, "architecture "+label+" identity "+id+" is not a bounded stable identifier", "use stable bounded identifiers")
		}
		if index[id] {
			return nil, designArchitectureFailure(KindInvalidPayload, "architecture "+label+" identity "+id+" is declared twice", "declare each entry once")
		}
		index[id] = true
	}
	return index, nil
}

// designArchitectureContainedPath enforces one exact repository path: valid
// UTF-8 bytes, the shared containment shape, and refusal of glob spellings,
// which the shared reading helper admits and the exact-path contract forbids.
func designArchitectureContainedPath(label, path string) error {
	if !utf8.ValidString(path) {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+label+" must be valid UTF-8", "declare the path in valid UTF-8 instead of letting JSON replace invalid bytes")
	}
	if err := validateWorkContextRepoPath(path); err != nil {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+label+" must be a normalized repository-contained path", "declare exact contained paths without globs or traversal")
	}
	if strings.ContainsAny(path, "*?[]") {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+label+" must be an exact path, not a glob pattern", "name the exact file without *, ?, or bracket spellings")
	}
	return nil
}

// designArchitectureRepoEntry proves one {project_id, path} pair: bounded
// Project, exact contained path, member membership.
func designArchitectureRepoEntry(label, project, path string, memberProject func(string) bool) error {
	if len(project) < 2 || len(project) > workContextProjectIDMaxBytes {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+label+" project_id must be between 2 and 128 bytes", "name the Project that owns the repository")
	}
	if err := designArchitectureContainedPath(label+" path", path); err != nil {
		return err
	}
	if !memberProject(project) {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+label+" names Project "+project+" which is not a member of the work", "name it inside a member Project")
	}
	return nil
}

// designArchitectureRepositorySource enforces the closed repository-file
// source arm on a pinned reference or generator binding: the pinned commit,
// no knowledge fields, and the shared Project/path/membership entry proof.
func designArchitectureRepositorySource(source WorkContextReadingSource, label string, memberProject func(string) bool) error {
	if source.Kind != WorkContextSourceRepositoryFile {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+label+" source must be a repository file source", "pin the source with project_id, path, and commit_oid")
	}
	if !workContextCommitOID(source.CommitOID) {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+label+" source commit_oid must be 40 to 64 hex bytes", "pin the commit the source was taken at")
	}
	if source.SourceID != "" || source.LawID != "" || source.ContentHash != "" {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+label+" source carries knowledge source fields", "use one closed source arm")
	}
	return designArchitectureRepoEntry(label, source.ProjectID, source.Path, memberProject)
}

// validateDesignArchitectureCapacity enforces the declared eight-slot
// allocation: non-negative reserves whose running sum stays within the
// remaining permitted total, so no caller-supplied slot values can overflow
// the sum into acceptance.
func validateDesignArchitectureCapacity(capacity *WorkflowDesignArchitectureCapacity) error {
	if capacity == nil {
		return designArchitectureFailure(KindInvalidPayload, "architecture carries no capacity allocation", "declare the eight variable slot reserves")
	}
	slots := []struct {
		name    string
		reserve int
	}{
		{"design_record", capacity.DesignRecord}, {"worker_job", capacity.WorkerJob},
		{"work_context", capacity.WorkContext}, {"law_context", capacity.LawContext},
		{"proposal_record", capacity.ProposalRecord}, {"work_record", capacity.WorkRecord},
		{"task_premise", capacity.TaskPremise}, {"outcome_predicates", capacity.OutcomePredicates},
	}
	total := 0
	for _, slot := range slots {
		if slot.reserve < 0 {
			return designArchitectureFailure(KindInvalidPayload, "architecture capacity slot "+slot.name+" is negative", "declare each reserve as a non-negative byte count")
		}
		if slot.reserve > designArchitectureCapacityTotalMax-total {
			return designArchitectureFailure(KindLimitExceeded, "architecture capacity reserves exceed the declared allocation at slot "+slot.name+"; the plan is refused, never truncated", "reduce one or more slot reserves within the same total")
		}
		total += slot.reserve
	}
	return nil
}

// validateDesignArchitectureCanonicalBound measures the plan's serialized
// form. Go's canonical encoding is the authority; an over-bound plan refuses
// whole and is never truncated.
func validateDesignArchitectureCanonicalBound(architecture WorkflowDesignArchitecture) error {
	raw, err := json.Marshal(architecture)
	if err != nil {
		return designArchitectureFailure(KindInvalidPayload, "architecture content cannot be measured", "repair the architecture content")
	}
	if len(raw) > designArchitectureCanonicalMaxBytes {
		return designArchitectureFailure(KindLimitExceeded, fmt.Sprintf("canonical architecture is %d bytes and exceeds the %d byte bound; it is refused, never truncated", len(raw), designArchitectureCanonicalMaxBytes), "reduce repeated prose and declare only necessary sections")
	}
	return nil
}

// designArchitectureFirstError returns the first refusal in a list of pure
// checks: every check only reads its arguments, so evaluating the whole list
// is safe and the first listed refusal keeps precedence.
func designArchitectureFirstError(checks ...error) error {
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

func designArchitectureOwnerRef(section, ownerID string, owners map[string]WorkflowDesignArchitectureOwner) error {
	if _, exists := owners[ownerID]; !exists {
		return designArchitectureFailure(KindInvalidPayload, "architecture "+section+" names undeclared owner "+ownerID, "map every entry to a declared owner")
	}
	return nil
}

func designArchitectureResolves(section, label string, ids []string, index map[string]bool) error {
	for _, id := range ids {
		if !index[id] {
			return designArchitectureFailure(KindInvalidPayload, "architecture "+section+" names undeclared "+label+" "+id, "name only declared entries")
		}
	}
	return nil
}

// validateWorkflowDesignArchitecture enforces the closed architecture value
// surface and reads no store state: membership arrives from the caller's
// predicate, and pinned-blob resolution of reference ranges happens later at
// dispatch preparation.
func validateWorkflowDesignArchitecture(architecture WorkflowDesignArchitecture, memberProject func(string) bool) error {
	owners, err := validateDesignArchitectureOwners(architecture.Owners, memberProject)
	if err != nil {
		return err
	}
	invariants, err := validateDesignArchitectureInvariants(architecture.Invariants, owners)
	if err != nil {
		return err
	}
	references, err := validateDesignArchitectureReferences(architecture.References, memberProject)
	if err != nil {
		return err
	}
	files, err := validateDesignArchitectureFiles(architecture.Files, memberProject)
	if err != nil {
		return err
	}
	unchanged, err := validateDesignArchitectureUnchangedFiles(architecture.UnchangedFiles, memberProject)
	if err != nil {
		return err
	}
	return designArchitectureFirstError(
		validateDesignArchitectureDataFlows(architecture.DataFlow, owners, invariants),
		validateDesignArchitectureFailureBehavior(architecture.FailureBehavior, owners, invariants),
		validateDesignArchitectureSlices(architecture.Slices, owners, invariants, references, unchanged, files),
		validateDesignArchitectureCapacity(architecture.Capacity),
		validateDesignArchitectureCanonicalBound(architecture),
	)
}

func validateDesignArchitectureOwners(entries []WorkflowDesignArchitectureOwner, memberProject func(string) bool) (map[string]WorkflowDesignArchitectureOwner, error) {
	if _, err := designArchitectureSection(entries, 1, designArchitectureOwnersMax, "owners", func(owner WorkflowDesignArchitectureOwner) string { return owner.OwnerID }); err != nil {
		return nil, err
	}
	owners := make(map[string]WorkflowDesignArchitectureOwner, len(entries))
	for _, owner := range entries {
		if err := designArchitectureFirstError(
			designArchitectureRepoEntry("owner "+owner.OwnerID, owner.ProjectID, owner.Path, memberProject),
			designArchitectureBoundedText(owner.Symbol, "owner "+owner.OwnerID+" symbol", designArchitectureSymbolMaxBytes),
		); err != nil {
			return nil, err
		}
		owners[owner.OwnerID] = owner
	}
	return owners, nil
}

func validateDesignArchitectureInvariants(entries []WorkflowDesignArchitectureInvariant, owners map[string]WorkflowDesignArchitectureOwner) (map[string]bool, error) {
	index, err := designArchitectureSection(entries, 1, designArchitectureInvariantsMax, "invariants", func(invariant WorkflowDesignArchitectureInvariant) string { return invariant.InvariantID })
	if err != nil {
		return nil, err
	}
	for _, invariant := range entries {
		if err := designArchitectureFirstError(
			designArchitectureOwnerRef("invariant "+invariant.InvariantID, invariant.OwnerID, owners),
			designArchitectureBoundedText(invariant.Statement, "invariant "+invariant.InvariantID+" statement", designArchitectureStatementMaxBytes),
		); err != nil {
			return nil, err
		}
	}
	return index, nil
}

func validateDesignArchitectureDataFlows(entries []WorkflowDesignArchitectureDataFlow, owners map[string]WorkflowDesignArchitectureOwner, invariants map[string]bool) error {
	if _, err := designArchitectureSection(entries, 1, designArchitectureDataFlowsMax, "data flows", func(flow WorkflowDesignArchitectureDataFlow) string { return flow.FlowID }); err != nil {
		return err
	}
	for _, flow := range entries {
		if err := designArchitectureFirstError(
			designArchitectureOwnerRef("data flow "+flow.FlowID, flow.FromOwnerID, owners),
			designArchitectureOwnerRef("data flow "+flow.FlowID, flow.ToOwnerID, owners),
			designArchitectureBoundedText(flow.Value, "data flow "+flow.FlowID+" value", designArchitectureStatementMaxBytes),
			designArchitectureResolves("data flow "+flow.FlowID, "invariant", flow.InvariantIDs, invariants),
		); err != nil {
			return err
		}
		if !uniqueStrings(flow.InvariantIDs) {
			return designArchitectureFailure(KindInvalidPayload, "architecture data flow "+flow.FlowID+" names an invariant twice", "name each invariant once per flow")
		}
	}
	return nil
}

func validateDesignArchitectureFailureBehavior(entries []WorkflowDesignArchitectureFailureBehavior, owners map[string]WorkflowDesignArchitectureOwner, invariants map[string]bool) error {
	if _, err := designArchitectureSection(entries, 1, designArchitectureFailuresMax, "failure behaviors", func(behavior WorkflowDesignArchitectureFailureBehavior) string { return behavior.FailureID }); err != nil {
		return err
	}
	for _, behavior := range entries {
		if err := designArchitectureFirstError(
			designArchitectureOwnerRef("failure behavior "+behavior.FailureID, behavior.OwnerID, owners),
			designArchitectureBoundedText(behavior.Condition, "failure behavior "+behavior.FailureID+" condition", designArchitectureStatementMaxBytes),
			designArchitectureBoundedText(behavior.Result, "failure behavior "+behavior.FailureID+" result", designArchitectureStatementMaxBytes),
			designArchitectureResolves("failure behavior "+behavior.FailureID, "invariant", behavior.Preserves, invariants),
		); err != nil {
			return err
		}
		if !uniqueStrings(behavior.Preserves) {
			return designArchitectureFailure(KindInvalidPayload, "architecture failure behavior "+behavior.FailureID+" preserves an invariant twice", "preserve each invariant once")
		}
	}
	return nil
}

func validateDesignArchitectureReferences(entries []WorkflowDesignArchitectureReference, memberProject func(string) bool) (map[string]bool, error) {
	index, err := designArchitectureSection(entries, 1, designArchitectureReferencesMax, "references", func(reference WorkflowDesignArchitectureReference) string { return reference.ReferenceID })
	if err != nil {
		return nil, err
	}
	for _, reference := range entries {
		if reference.StartLine < 1 || reference.EndLine < reference.StartLine {
			return nil, designArchitectureFailure(KindInvalidPayload, "architecture reference "+reference.ReferenceID+" line bounds must be positive with end not before start", "pin ordered line bounds")
		}
		if err := designArchitectureRepositorySource(reference.Source, "reference "+reference.ReferenceID, memberProject); err != nil {
			return nil, err
		}
	}
	return index, nil
}

func validateDesignArchitectureFiles(entries []WorkflowDesignArchitectureFile, memberProject func(string) bool) (map[designArchitectureFileKey]bool, error) {
	if err := designArchitectureCount(len(entries), 0, designArchitectureFilesMax, "files", "declare at most 128 files"); err != nil {
		return nil, err
	}
	roles := map[string]bool{DesignArchitectureFileRoleSource: true, DesignArchitectureFileRoleTest: true, DesignArchitectureFileRoleGenerated: true}
	files := make(map[designArchitectureFileKey]bool, len(entries))
	for _, file := range entries {
		key := designArchitectureFileKey{file.ProjectID, file.Path}
		if files[key] {
			return nil, designArchitectureFailure(KindInvalidPayload, "architecture file "+file.Path+" of Project "+file.ProjectID+" is declared twice", "declare each file once")
		}
		if !roles[file.Role] {
			return nil, designArchitectureFailure(KindInvalidPayload, "architecture file "+file.Path+" role must be source, test, or generated", "use one closed file role")
		}
		switch file.Role {
		case DesignArchitectureFileRoleGenerated:
			if file.GeneratedBy == nil {
				return nil, designArchitectureFailure(KindInvalidPayload, "architecture generated file "+file.Path+" names no pinned generator source", "pin the generator with project_id, path, and commit_oid")
			}
			if err := designArchitectureRepositorySource(*file.GeneratedBy, "generated file "+file.Path+" generator", memberProject); err != nil {
				return nil, err
			}
		default:
			if file.GeneratedBy != nil {
				return nil, designArchitectureFailure(KindInvalidPayload, "architecture "+file.Role+" file "+file.Path+" carries a generator source", "only generated entries name a generator")
			}
		}
		if err := designArchitectureRepoEntry("file "+file.Path, file.ProjectID, file.Path, memberProject); err != nil {
			return nil, err
		}
		files[key] = true
	}
	return files, nil
}

func validateDesignArchitectureUnchangedFiles(entries []WorkflowDesignArchitectureUnchangedFile, memberProject func(string) bool) (map[designArchitectureFileKey]bool, error) {
	if err := designArchitectureCount(len(entries), 0, designArchitectureUnchangedMax, "unchanged files", "declare at most 128 unchanged files"); err != nil {
		return nil, err
	}
	unchanged := make(map[designArchitectureFileKey]bool, len(entries))
	for _, file := range entries {
		key := designArchitectureFileKey{file.ProjectID, file.Path}
		if unchanged[key] {
			return nil, designArchitectureFailure(KindInvalidPayload, "architecture unchanged file "+file.Path+" of Project "+file.ProjectID+" is declared twice", "declare each unchanged file once")
		}
		if err := designArchitectureRepoEntry("unchanged file "+file.Path, file.ProjectID, file.Path, memberProject); err != nil {
			return nil, err
		}
		unchanged[key] = true
	}
	return unchanged, nil
}

func validateDesignArchitectureSlices(entries []WorkflowDesignArchitectureSlice, owners map[string]WorkflowDesignArchitectureOwner, invariants, references map[string]bool, unchanged, files map[designArchitectureFileKey]bool) error {
	if _, err := designArchitectureSection(entries, 1, designArchitectureSlicesMax, "slices", func(slice WorkflowDesignArchitectureSlice) string { return slice.SliceID }); err != nil {
		return err
	}
	kinds := map[string]bool{DesignArchitectureSliceKindEdit: true, DesignArchitectureSliceKindIntegration: true}
	for position, slice := range entries {
		if slice.Ordinal != position {
			return designArchitectureFailure(KindInvalidPayload, fmt.Sprintf("architecture slice %s ordinal %d must equal its declared position %d", slice.SliceID, slice.Ordinal, position), "order slices by their declared position")
		}
		if !kinds[slice.Kind] {
			return designArchitectureFailure(KindInvalidPayload, "architecture slice "+slice.SliceID+" kind must be edit or integration", "use one closed slice kind")
		}
		owner, exists := owners[slice.OwnerID]
		if !exists {
			return designArchitectureFailure(KindInvalidPayload, "architecture slice "+slice.SliceID+" names undeclared owner "+slice.OwnerID, "map every slice to its one declared owner")
		}
		if slice.Kind != DesignArchitectureSliceKindIntegration || len(slice.Files) > 0 {
			if err := designArchitectureCount(len(slice.Files), 1, designArchitectureSliceFilesMax, "slice "+slice.SliceID+" files", "give each editing slice one to sixty-four exact files"); err != nil {
				return err
			}
		}
		if !uniqueStrings(slice.Files) || !uniqueStrings(slice.InvariantIDs) || !uniqueStrings(slice.ReferenceIDs) || !uniqueStrings(slice.Tests) {
			return designArchitectureFailure(KindInvalidPayload, "architecture slice "+slice.SliceID+" names a file, invariant, reference, or test control twice", "name each file, invariant, reference, and test control once per slice")
		}
		for _, path := range slice.Files {
			key := designArchitectureFileKey{owner.ProjectID, path}
			if unchanged[key] {
				return designArchitectureFailure(KindInvalidPayload, "architecture slice "+slice.SliceID+" file "+path+" overlaps an unchanged file of Project "+owner.ProjectID, "remove the overlap; unchanged files accept no worker or resolution edits")
			}
			if !files[key] {
				return designArchitectureFailure(KindInvalidPayload, "architecture slice "+slice.SliceID+" file "+path+" is not a declared file of the owner's Project "+owner.ProjectID, "draw each slice file set from the approved file map")
			}
		}
		if slice.MaxChangedLines < designArchitectureMinChangedLines || slice.MaxChangedLines > designArchitectureMaxChangedLines {
			return designArchitectureFailure(KindInvalidPayload, fmt.Sprintf("architecture slice %s max_changed_lines %d must be between %d and %d", slice.SliceID, slice.MaxChangedLines, designArchitectureMinChangedLines, designArchitectureMaxChangedLines), "declare each slice budget between one and five hundred lines")
		}
		if err := designArchitectureFirstError(
			designArchitectureBoundedText(slice.EndState, "slice "+slice.SliceID+" end_state", designArchitectureStatementMaxBytes),
			designArchitectureCount(len(slice.Tests), 1, designArchitectureSliceTestsMax, "slice "+slice.SliceID+" tests", "give each slice one to thirty-two stable control IDs"),
			designArchitectureResolves("slice "+slice.SliceID, "invariant", slice.InvariantIDs, invariants),
			designArchitectureResolves("slice "+slice.SliceID, "reference", slice.ReferenceIDs, references),
		); err != nil {
			return err
		}
		for _, test := range slice.Tests {
			if !oracleControlIDPattern.MatchString(test) {
				return designArchitectureFailure(KindInvalidPayload, "architecture slice "+slice.SliceID+" test "+test+" must match the existing control identity grammar", "name existing acceptance-oracle controls, not a second command language")
			}
		}
	}
	return nil
}
