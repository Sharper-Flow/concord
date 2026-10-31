package store

import (
	"encoding/json"
	"regexp"
)

// An acceptance oracle is the owners, cases and controls a recorded Review
// finding source may retain. Concord prepares and runs nothing from it.

// Closed bounds of the oracle surface.
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

// AcceptanceOracle is the closed three-part graph: owners with obligations,
// a finite case inventory, and controls with exact evidence requirements.
type AcceptanceOracle struct {
	Owners   []OracleOwner   `json:"owners"`
	Cases    []OracleCase    `json:"cases"`
	Controls []OracleControl `json:"controls"`
}

// oracleEntryIdentical compares two entries by canonical JSON.
func oracleEntryIdentical[T any](previous, latest T) bool {
	left, leftErr := json.Marshal(previous)
	right, rightErr := json.Marshal(latest)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}
