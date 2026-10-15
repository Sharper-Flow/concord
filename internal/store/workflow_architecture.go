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
