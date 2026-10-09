package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// CON-890 slice A, findings half: the typed report structs, the ranked
// finding identity, the eventwise open-set lineage, and the joins that bind
// receipts, findings, and closures to the dispatched immutable job's oracle.
//
// The report structs mirror contracts/agent-lane-report.schema.json exactly:
// a receipt is reported evidence, never native-run authority; a finding's
// oracle tie records classification, owner, and predicate/law/control joins;
// a resolution is an evidenced closure claim. The store validates reference
// joins — it never judges semantic truth and never manufactures native proof.
//
// Ranked finding identity is deterministic per terminal event: the event's
// context_findings keep ordinals 0..n-1 and the ranked review findings
// follow them at offset n, so the two arrays can never mint one ID and
// historical context IDs stay stable. The lineage walk below is the one
// eventwise derivation of the current open set; corrections, convergence,
// admission, and the context reader all consume it instead of re-deriving.

// Closed classification vocabulary of one typed review finding.
const (
	OracleClassificationDeliveryBlocker = "delivery_blocker"
	OracleClassificationUncoveredCase   = "uncovered_case"
	OracleClassificationFollowUp        = "follow_up"
	OracleClassificationOracleDefect    = "oracle_defect"
)

// Closed result vocabulary of one typed control-execution receipt.
const (
	OracleReceiptResultPass        = "pass"
	OracleReceiptResultFail        = "fail"
	OracleReceiptResultUnavailable = "unavailable"
	OracleReceiptResultNotRun      = "not_run"
)

// Bounds of the typed report members, mirroring
// contracts/agent-lane-report.schema.json.
const (
	oracleReceiptControlsMax        = 8
	oracleReceiptCasesMax           = 64
	oracleReceiptEvidenceRefsMax    = 8
	oracleReceiptRunRefMaxBytes     = 512
	oracleReceiptEvidenceRefBytes   = 256
	oracleFindingPredicateMax       = 8
	oracleFindingLawBindingsMax     = 8
	oracleFindingCaseMax            = 8
	oracleFindingControlMax         = 8
	oracleFindingEvidenceRefsMax    = 8
	oracleFindingFamilyMaxBytes     = 128
	oracleFindingEntryPointMaxBytes = 512
	oracleFindingEvidenceRefBytes   = 256
	oracleResolutionsMax            = 32
	oracleResolutionsArrayMaxBytes  = 8192
	oracleRetainedReceiptsMax       = 64
	// WorkflowOpenOracleFindingsLimit is the CON-885-carried bound of one
	// active open set: at most 32 open oracle findings. A work past the
	// bound refuses dispatch explicitly; blockers are never silently
	// omitted to satisfy the bound.
	WorkflowOpenOracleFindingsLimit = 32
)

// Ranked source kinds the context reader projects. A generic finding keeps
// the empty source kind; a ranked review finding carries review_finding so a
// consumer can tell a typed blocker from a worker-claimed context notebook
// entry without guessing.
const (
	WorkContextSourceKindReviewFinding = "review_finding"
)

// Receipt-closure provenance the lineage records.
const (
	oracleClosureResolution       = "resolution"
	oracleClosureOracleDefect     = "oracle_defect_resolution"
	oracleClosureSupersession     = "contract_supersession"
	oracleExitCodeMax             = 255
	workerTerminalRankedRefPrefix = "finding:"
)

var (
	oracleFindingIDRefPattern  = regexp.MustCompile(`^finding:[0-9]+:[0-9]+$`)
	oracleReceiptResultVocab   = map[string]bool{OracleReceiptResultPass: true, OracleReceiptResultFail: true, OracleReceiptResultUnavailable: true, OracleReceiptResultNotRun: true}
	oracleClassificationVocab  = map[string]bool{OracleClassificationDeliveryBlocker: true, OracleClassificationUncoveredCase: true, OracleClassificationFollowUp: true, OracleClassificationOracleDefect: true}
	oracleReviewBlockerClasses = map[string]bool{OracleClassificationDeliveryBlocker: true, OracleClassificationUncoveredCase: true, OracleClassificationOracleDefect: true}
)

// WorkerOracleRecipeSource is the exact pinned harness identity a receipt
// executed: a repository file at an exact commit. A receipt must name the
// recipe the control pinned, never the candidate's modified copy of the
// harness.
type WorkerOracleRecipeSource struct {
	Kind      string `json:"kind"`
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
	CommitOID string `json:"commit_oid"`
}

// WorkerOracleReceipt is one typed control-execution receipt (CON-890):
// optional reported evidence on one evidence entry, never native-run
// authority. SubjectCommit is the raw commit OID of the core-derived observed
// subject: dispatch-owned, stripped of any worker echo before the canonical report.
// ExitCode is a pointer so the pass/fail-required, otherwise-forbidden
// coupling stays decidable. RunRef is the immutable native-run locator:
// nonempty exactly when result is pass or fail; a not_run or unavailable
// receipt carries the empty string and explains itself in evidence_refs.
type WorkerOracleReceipt struct {
	ControlIDs    []string                 `json:"control_ids"`
	CaseIDs       []string                 `json:"case_ids"`
	SubjectCommit string                   `json:"subject_commit,omitempty"`
	RecipeSource  WorkerOracleRecipeSource `json:"recipe_source"`
	Result        string                   `json:"result"`
	ExitCode      *int                     `json:"exit_code,omitempty"`
	RunRef        string                   `json:"run_ref"`
	EvidenceRefs  []string                 `json:"evidence_refs"`
}

// WorkerOracleLawSource is the knowledge arm of one finding's law tie.
type WorkerOracleLawSource struct {
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id"`
	LawID       string `json:"law_id"`
	ContentHash string `json:"content_hash"`
}

// WorkerOracleLawBinding ties one finding to a pinned law revision.
type WorkerOracleLawBinding struct {
	Source WorkerOracleLawSource `json:"source"`
	Clause string                `json:"clause"`
}

// WorkerOracleFinding is the typed oracle tie of one review finding: a
// closed classification plus the owner, predicate/law, case, and control
// references that bind it, and the lineage references that keep identity
// stable across reports. Classification and severity are different
// dimensions; the tie records no acceptance.
type WorkerOracleFinding struct {
	Classification     string                   `json:"classification"`
	OwnerID            string                   `json:"owner_id,omitempty"`
	FailureFamily      string                   `json:"failure_family,omitempty"`
	PredicateIDs       []string                 `json:"predicate_ids,omitempty"`
	LawBindings        []WorkerOracleLawBinding `json:"law_bindings,omitempty"`
	CaseIDs            []string                 `json:"case_ids,omitempty"`
	ControlIDs         []string                 `json:"control_ids,omitempty"`
	EvidenceRefs       []string                 `json:"evidence_refs,omitempty"`
	EntryPoint         string                   `json:"entry_point,omitempty"`
	ContinuesFindingID string                   `json:"continues_finding_id,omitempty"`
	VariantOf          string                   `json:"variant_of,omitempty"`
}

// WorkerOracleResolution is one evidenced closure claim of a previously open
// ranked finding. A claim is a claim: the fold checks each identity against
// this work's earlier ranked findings and the evidence each control
// requires. Omission, relabeling, or a confidence change never closes a
// finding.
type WorkerOracleResolution struct {
	FindingID    string   `json:"finding_id"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// oracleFindingFailure bounds this surface's refusals.
func oracleFindingFailure(kind FailureKind, detail, remedy string) error {
	return newFailure(kind, "worker_oracle_findings", detail, false, remedy)
}

// validateWorkerOracleRecipeSourceShape enforces the receipt's recipe arm:
// the repository-file source at the exact pinned commit.
func validateWorkerOracleRecipeSourceShape(source WorkerOracleRecipeSource) error {
	if source.Kind != WorkContextSourceRepositoryFile {
		return oracleFindingFailure(KindInvalidPayload, "oracle_receipt recipe_source must be a repository file source", "name the pinned harness with project_id, path, and commit_oid")
	}
	if len(source.ProjectID) < 2 || len(source.ProjectID) > oracleProjectIDMaxBytes {
		return oracleFindingFailure(KindInvalidPayload, "oracle_receipt recipe_source project_id must be between 2 and 128 bytes", "name the Project that owns the harness repository")
	}
	if len(source.Path) < 1 || len(source.Path) > workContextPathMaxBytes {
		return oracleFindingFailure(KindInvalidPayload, "oracle_receipt recipe_source path must be between 1 and 512 bytes", "name the harness path")
	}
	if !workContextCommitOID(source.CommitOID) {
		return oracleFindingFailure(KindInvalidPayload, "oracle_receipt recipe_source commit_oid must be 40 to 64 hex bytes", "pin the commit the harness was taken at")
	}
	return nil
}

// ValidateWorkerOracleReceiptShape is the closed shape of one typed receipt,
// mirroring $defs/oracle_receipt of contracts/agent-lane-report.schema.json:
// bounded unique control and case identities, the pinned recipe source, the
// closed result vocabulary, and the exact result coupling — pass or fail
// requires a nonempty run_ref and an exit code; unavailable or not_run
// requires the empty run_ref and no exit code. Exported so the terminal
// admission's transport copies validate the same shape the fold enforces.
func ValidateWorkerOracleReceiptShape(receipt *WorkerOracleReceipt) error {
	if receipt == nil {
		return nil
	}
	if len(receipt.ControlIDs) < 1 || len(receipt.ControlIDs) > oracleReceiptControlsMax {
		return oracleFindingFailure(KindInvalidPayload, "oracle_receipt must name 1 to 8 controls", "name the controls this receipt executed")
	}
	seenControls := map[string]bool{}
	for _, control := range receipt.ControlIDs {
		if !oracleControlIDPattern.MatchString(control) || seenControls[control] {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt control_ids must be unique bounded control: identities", "name each executed control once")
		}
		seenControls[control] = true
	}
	if len(receipt.CaseIDs) < 1 || len(receipt.CaseIDs) > oracleReceiptCasesMax {
		return oracleFindingFailure(KindInvalidPayload, "oracle_receipt must name 1 to 64 cases", "name the cases this receipt exercised")
	}
	seenCases := map[string]bool{}
	for _, entry := range receipt.CaseIDs {
		if !oracleCaseIDPattern.MatchString(entry) || seenCases[entry] {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt case_ids must be unique bounded case: identities", "name each exercised case once")
		}
		seenCases[entry] = true
	}
	if receipt.SubjectCommit != "" && !worktreeSHAPattern.MatchString(receipt.SubjectCommit) {
		return oracleFindingFailure(KindInvalidPayload, "oracle_receipt subject_commit must be one raw commit OID", "carry the core-derived observed subject commit")
	}
	if err := validateWorkerOracleRecipeSourceShape(receipt.RecipeSource); err != nil {
		return err
	}
	if !oracleReceiptResultVocab[receipt.Result] {
		return oracleFindingFailure(KindInvalidPayload, "oracle_receipt result must be pass, fail, unavailable, or not_run", "report the control's execution result")
	}
	switch receipt.Result {
	case OracleReceiptResultPass, OracleReceiptResultFail:
		if receipt.ExitCode == nil || *receipt.ExitCode < 0 || *receipt.ExitCode > oracleExitCodeMax {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt with result "+receipt.Result+" requires its exit code", "report the exit status the harness produced")
		}
		if len(receipt.RunRef) < 1 || len(receipt.RunRef) > oracleReceiptRunRefMaxBytes {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt with result "+receipt.Result+" requires a nonempty run_ref locator", "carry the immutable native-run locator the producing host issued")
		}
	default:
		if receipt.ExitCode != nil {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt with result "+receipt.Result+" cannot carry an exit code", "explain the unavailable or not_run execution in evidence_refs")
		}
		if receipt.RunRef != "" {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt with result "+receipt.Result+" must carry the empty run_ref", "never invent a locator: when no producing route issued one the result is unavailable")
		}
	}
	if len(receipt.EvidenceRefs) > oracleReceiptEvidenceRefsMax {
		return oracleFindingFailure(KindInvalidPayload, "oracle_receipt must carry at most 8 evidence refs", "bound the receipt's evidence references")
	}
	for _, ref := range receipt.EvidenceRefs {
		if len(ref) < 1 || len(ref) > oracleReceiptEvidenceRefBytes {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt evidence refs must be between 1 and 256 bytes", "bound the receipt's evidence references")
		}
	}
	return nil
}

// validateWorkerOracleFindingShape is the closed shape of one finding's
// oracle tie, mirroring $defs/oracle_finding: the classification vocabulary,
// bounded reference lists, and the finding-id lineage grammar. The owner,
// predicate, law, and control joins against the dispatched oracle are fold
// checks; this is the shape the transport can validate without the store.
func validateWorkerOracleFindingShape(finding *WorkerOracleFinding) error {
	if finding == nil {
		return nil
	}
	if !oracleClassificationVocab[finding.Classification] {
		return oracleFindingFailure(KindInvalidPayload, "review finding oracle classification must be delivery_blocker, uncovered_case, follow_up, or oracle_defect", "classify the finding against the dispatched oracle")
	}
	if finding.OwnerID != "" && !oracleOwnerIDPattern.MatchString(finding.OwnerID) {
		return oracleFindingFailure(KindInvalidPayload, "review finding oracle owner_id must match owner:<reference>", "name the declared owner the finding binds")
	}
	if len(finding.FailureFamily) > oracleFindingFamilyMaxBytes {
		return oracleFindingFailure(KindInvalidPayload, "review finding oracle failure_family must be at most 128 bytes", "bound the failure family")
	}
	if len(finding.PredicateIDs) > oracleFindingPredicateMax {
		return oracleFindingFailure(KindInvalidPayload, "review finding oracle must name at most 8 predicates", "bind the finding to at most 8 parent-approved predicates")
	}
	seenPredicates := map[string]bool{}
	for _, predicate := range finding.PredicateIDs {
		if !validWorkerPredicateID(predicate) || seenPredicates[predicate] {
			return oracleFindingFailure(KindInvalidPayload, "review finding oracle predicate_ids must be unique bounded predicate: identities", "name each bound predicate once")
		}
		seenPredicates[predicate] = true
	}
	if len(finding.LawBindings) > oracleFindingLawBindingsMax {
		return oracleFindingFailure(KindInvalidPayload, "review finding oracle must carry at most 8 law bindings", "bind at most 8 pinned law revisions")
	}
	seenLaws := map[string]bool{}
	for _, binding := range finding.LawBindings {
		if binding.Source.Kind != WorkContextSourceKnowledge {
			return oracleFindingFailure(KindInvalidPayload, "review finding oracle law binding source must be a knowledge source", "bind law by source_id, law_id, and the pinned content hash")
		}
		if len(binding.Source.SourceID) < 2 || len(binding.Source.SourceID) > oracleProjectIDMaxBytes || len(binding.Source.LawID) < 2 || len(binding.Source.LawID) > workContextLawIDMaxBytes || !workflowDigest(binding.Source.ContentHash) {
			return oracleFindingFailure(KindInvalidPayload, "review finding oracle law binding carries an incomplete law revision identity", "bind the pinned law revision by source_id, law_id, and content hash")
		}
		if len(binding.Clause) < 1 || len(binding.Clause) > oracleDescriptionMaxBytes {
			return oracleFindingFailure(KindInvalidPayload, "review finding oracle law binding clause must be between 1 and 512 bytes", "name the clause the obligation serves")
		}
		if seenLaws[binding.Source.LawID] {
			return oracleFindingFailure(KindInvalidPayload, "review finding oracle binds one law twice", "bind each law once")
		}
		seenLaws[binding.Source.LawID] = true
	}
	for _, entry := range []struct {
		values  []string
		pattern *regexp.Regexp
		bound   int
		label   string
	}{{finding.CaseIDs, oracleCaseIDPattern, oracleFindingCaseMax, "case"}, {finding.ControlIDs, oracleControlIDPattern, oracleFindingControlMax, "control"}} {
		if len(entry.values) > entry.bound {
			return oracleFindingFailure(KindInvalidPayload, "review finding oracle must name at most "+fmt.Sprint(entry.bound)+" "+entry.label+" ids", "bound the finding's "+entry.label+" references")
		}
		seen := map[string]bool{}
		for _, value := range entry.values {
			if !entry.pattern.MatchString(value) || seen[value] {
				return oracleFindingFailure(KindInvalidPayload, "review finding oracle "+entry.label+"_ids must be unique bounded "+entry.label+": identities", "name each "+entry.label+" once")
			}
			seen[value] = true
		}
	}
	if len(finding.EvidenceRefs) > oracleFindingEvidenceRefsMax {
		return oracleFindingFailure(KindInvalidPayload, "review finding oracle must carry at most 8 evidence refs", "bound the finding's reproduction evidence")
	}
	for _, ref := range finding.EvidenceRefs {
		if len(ref) < 1 || len(ref) > oracleFindingEvidenceRefBytes {
			return oracleFindingFailure(KindInvalidPayload, "review finding oracle evidence refs must be between 1 and 256 bytes", "bound the finding's reproduction evidence")
		}
	}
	if len(finding.EntryPoint) > oracleFindingEntryPointMaxBytes {
		return oracleFindingFailure(KindInvalidPayload, "review finding oracle entry_point must be at most 512 bytes", "bound the uncovered entry path")
	}
	for _, lineage := range []struct {
		value string
		label string
	}{{finding.ContinuesFindingID, "continues_finding_id"}, {finding.VariantOf, "variant_of"}} {
		if lineage.value != "" && !oracleFindingIDRefPattern.MatchString(lineage.value) {
			return oracleFindingFailure(KindInvalidPayload, "review finding oracle "+lineage.label+" must be a finding:<event_seq>:<ordinal> identity", "reference this work's earlier ranked finding by its canonical id")
		}
	}
	return nil
}

// validateWorkerOracleResolutionsShape is the closed shape of the review
// block's resolved_findings array: at most 32 unique closure claims, each
// naming one finding identity and 1 to 8 unique evidence references, with
// the whole array inside the 8 KiB aggregate bound.
func validateWorkerOracleResolutionsShape(resolutions []WorkerOracleResolution) error {
	if len(resolutions) > oracleResolutionsMax {
		return oracleFindingFailure(KindInvalidPayload, "review resolved_findings must carry at most 32 closure claims", "close at most 32 findings per report")
	}
	seen := map[string]bool{}
	for _, resolution := range resolutions {
		if !oracleFindingIDRefPattern.MatchString(resolution.FindingID) {
			return oracleFindingFailure(KindInvalidPayload, "review resolved_findings must name finding:<event_seq>:<ordinal> identities", "close ranked findings by their canonical ids")
		}
		if seen[resolution.FindingID] {
			return oracleFindingFailure(KindInvalidPayload, "review resolved_findings closes one finding twice", "close each finding once")
		}
		seen[resolution.FindingID] = true
		if len(resolution.EvidenceRefs) < 1 || len(resolution.EvidenceRefs) > oracleFindingEvidenceRefsMax {
			return oracleFindingFailure(KindInvalidPayload, "review resolved_findings must carry 1 to 8 evidence refs", "name the evidence the closure claims")
		}
		seenRefs := map[string]bool{}
		for _, ref := range resolution.EvidenceRefs {
			if len(ref) < 1 || len(ref) > oracleFindingEvidenceRefBytes || seenRefs[ref] {
				return oracleFindingFailure(KindInvalidPayload, "review resolved_findings evidence refs must be unique and between 1 and 256 bytes", "name each closure evidence reference once")
			}
			seenRefs[ref] = true
		}
	}
	if resolutions == nil {
		return nil
	}
	encoded, err := json.Marshal(resolutions)
	if err != nil {
		return oracleFindingFailure(KindInvalidPayload, "review resolved_findings cannot be measured", "repair the closure claims")
	}
	if len(encoded) > oracleResolutionsArrayMaxBytes {
		return oracleFindingFailure(KindLimitExceeded, "review resolved_findings serialize past the 8192-byte aggregate bound and are refused, never truncated", "split the closures across reports")
	}
	return nil
}

// ValidateWorkerOracleReportShape validates the typed oracle members of one
// terminal report without touching the store: every receipt, every finding
// tie, and the closure-claim array. The terminal admission's transport
// copies and the payload validator share it, so the shape the adapter
// accepted is the shape the fold re-proves.
func ValidateWorkerOracleReportShape(payload WorkerCompletedPayload) error {
	for _, entry := range payload.Evidence {
		if err := ValidateWorkerOracleReceiptShape(entry.OracleReceipt); err != nil {
			return err
		}
	}
	if payload.Review == nil {
		return nil
	}
	for _, finding := range payload.Review.Findings {
		if err := validateWorkerOracleFindingShape(finding.Oracle); err != nil {
			return err
		}
	}
	return validateWorkerOracleResolutionsShape(payload.Review.ResolvedFindings)
}

// hasOracleReportContent reports whether one terminal report carries any
// typed oracle member: a receipt, a finding tie, or a closure claim. The
// payload-version boundary and the fold's live join checks key on it.
func hasOracleReportContent(payload WorkerCompletedPayload) bool {
	for _, entry := range payload.Evidence {
		if entry.OracleReceipt != nil {
			return true
		}
	}
	if payload.Review == nil {
		return false
	}
	if len(payload.Review.ResolvedFindings) > 0 {
		return true
	}
	for _, finding := range payload.Review.Findings {
		if finding.Oracle != nil {
			return true
		}
	}
	return false
}

// workerRankedFindingID derives one ranked finding's canonical identity:
// the terminal event's sequence and the deterministic ordinal space — the
// event's context findings first, the ranked review findings offset by that
// count. The two arrays of one event can never mint the same ID.
func workerRankedFindingID(eventSeq int64, contextCount, reviewIndex int) string {
	return fmt.Sprintf("finding:%d:%d", eventSeq, contextCount+reviewIndex)
}

// workContextTerminalFindingsAtEvent reads both finding arrays one terminal
// or declaration event carries: the generic context findings and the ranked
// review findings. Refusing an event kind that holds neither keeps the
// resolver's ordinal bound total.
func workContextTerminalFindingsAtEvent(event Event) (context []WorkerContextFinding, ranked []WorkerReviewFinding, ok bool) {
	switch event.Kind {
	case WorkflowWorkContextRecorded:
		var payload workflowWorkContextRecordedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return nil, nil, false
		}
		return payload.ContextFindings, nil, true
	case WorkerCompleted:
		var payload WorkerCompletedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return nil, nil, false
		}
		ranked = nil
		if payload.Review != nil {
			ranked = payload.Review.Findings
		}
		return payload.ContextFindings, ranked, true
	case WorkerFailed:
		var payload WorkerFailedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return nil, nil, false
		}
		return payload.ContextFindings, nil, true
	}
	return nil, nil, false
}

// WorkerOpenOracleFinding is one ranked finding the lineage tracks: its
// canonical identity, its typed tie as reported, its minting source, and
// the closure provenance that ended it. A follow-up is tracked for context
// but never blocks: it does not enter the open set.
type WorkerOpenOracleFinding struct {
	FindingID      string
	Severity       string
	Detail         string
	Classification string
	OwnerID        string
	DomainID       string
	FailureFamily  string
	PredicateIDs   []string
	LawIDs         []string
	ControlIDs     []string
	CaseIDs        []string
	SourceEventID  string
	SourceActorRef string
	SourceEventSeq int64
	SourceOrdinal  int
	Closed         bool
	ClosureKind    string
	SourceOracle   *AcceptanceOracle
	// Tie is the oracle tie exactly as the minting report carried it,
	// projected onto the context view beside the identity fields.
	Tie *WorkerOracleFinding
}

// blocks reports whether the finding is an open delivery obligation: a
// delivery blocker, an uncovered case, or an oracle defect. A follow-up is
// never an open blocker.
func (f *WorkerOpenOracleFinding) blocks() bool {
	return f.Classification != OracleClassificationFollowUp
}

// workerOracleFindingLineage is the one eventwise derivation of a work
// item's ranked findings: every minted identity with its closure provenance,
// the open order, and whether any dispatch ever bound an oracle-bearing job
// revision — the oracle-capable history flag every correction surface keys
// on.
type workerOracleFindingLineage struct {
	findings         map[string]*WorkerOpenOracleFinding
	openOrder        []string
	oracleCapable    bool
	oracles          map[WorkerJobBinding]*AcceptanceOracle
	retainedReceipts []WorkerOracleReceipt
}

// openFindingIDs lists the currently open ranked blockers in mint order.
func (l *workerOracleFindingLineage) openFindingIDs() []string {
	ids := make([]string, 0, len(l.openOrder))
	for _, id := range l.openOrder {
		if finding := l.findings[id]; finding != nil && finding.blocks() && !finding.Closed {
			ids = append(ids, id)
		}
	}
	return ids
}

// oracleForBinding resolves one dispatched binding's recorded oracle from
// the lineage's retained oracle map, nil when the revision predates the
// oracle.
func (l *workerOracleFindingLineage) oracleForBinding(binding WorkerJobBinding) *AcceptanceOracle {
	if l.oracles == nil {
		return nil
	}
	return l.oracles[binding]
}

// sameOracleTie reports whether a continuation retains the earlier finding's
// owner, obligation binding, and failure family: same failure, same
// canonical open identity. Neither prose matching nor a hash of changeable
// detail owns identity.
func sameOracleTie(previous *WorkerOpenOracleFinding, finding *WorkerOracleFinding) bool {
	if previous.OwnerID != finding.OwnerID || previous.FailureFamily != finding.FailureFamily {
		return false
	}
	if !sameStringSet(previous.PredicateIDs, finding.PredicateIDs) || !sameStringSet(previous.LawIDs, lawIDsOf(finding)) {
		return false
	}
	return true
}

func lawIDsOf(finding *WorkerOracleFinding) []string {
	ids := make([]string, 0, len(finding.LawBindings))
	for _, binding := range finding.LawBindings {
		ids = append(ids, binding.Source.LawID)
	}
	return ids
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]bool, len(left))
	for _, value := range left {
		seen[value] = true
	}
	for _, value := range right {
		if !seen[value] {
			return false
		}
	}
	return true
}

// readWorkerOracleFindingLineageTx walks the work item's event log once, in
// log order, and derives the ranked-finding lineage: which dispatched job
// revisions carried oracles, which ranked findings each terminal report
// minted or continued, which closure claims ended, and which receipts were
// retained. The walk trusts the recorded log — the live fold validated each
// event when it landed — so replay and the live derivation agree by
// construction. The read uses the queryer already in hand.
func readWorkerOracleFindingLineageTx(ctx context.Context, q queryer, workID string) (*workerOracleFindingLineage, error) {
	return readWorkerOracleFindingLineageExcludingTx(ctx, q, workID, 0)
}

// readWorkerOracleFindingLineageExcludingTx is the walk with one sequence
// excluded: the fold appends its event before folding it, so the live
// validation of that very event must derive the prior lineage — the event's
// own mints, continuations, and closures are the claims under validation,
// not the state they are validated against.
func readWorkerOracleFindingLineageExcludingTx(ctx context.Context, q queryer, workID string, excludeSeq int64) (*workerOracleFindingLineage, error) {
	oracles, err := readWorkerJobOraclesTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	lineage := &workerOracleFindingLineage{
		findings: map[string]*WorkerOpenOracleFinding{},
		oracles:  oracles,
	}
	actors := map[string]string{}
	query := `SELECT seq,event_id,kind,COALESCE(json_extract(payload,'$.worker_job.job_id'),''),COALESCE(json_extract(payload,'$.worker_job.revision'),0),COALESCE(json_extract(payload,'$.worker_job.digest'),''),payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind IN (?,?,?)`
	args := []any{string(SubjectWorkItem), workID, WorkerCompleted, WorkerDispatched, WorkflowContractSuperseded}
	if excludeSeq > 0 {
		query += ` AND seq<>?`
		args = append(args, excludeSeq)
	}
	query += ` ORDER BY seq`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_oracle_findings", "cannot read the ranked-finding lineage", true, "retry once the event log is readable", err)
	}
	receipts := make([]WorkerOracleReceipt, 0, 8)
	for rows.Next() {
		var seq int64
		var eventID, kind, jobID, digest, payload string
		var revision int64
		if err := rows.Scan(&seq, &eventID, &kind, &jobID, &revision, &digest, &payload); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "worker_oracle_findings", "cannot scan the ranked-finding lineage", true, "retry once the event log is readable", err)
		}
		switch kind {
		case string(WorkerDispatched):
			var dispatch WorkerDispatchedPayload
			if err := json.Unmarshal([]byte(payload), &dispatch); err != nil {
				rows.Close()
				return nil, oracleFindingFailure(KindInvariantViolation, "a stored dispatch is malformed", "rebuild the dispatch projection")
			}
			actors[dispatch.AttemptID] = dispatch.LaneActorRef
			if jobID == "" {
				continue
			}
			if oracle, recorded := oracles[WorkerJobBinding{JobID: jobID, Revision: revision, Digest: digest}]; recorded && oracle != nil {
				lineage.oracleCapable = true
			}
		case string(WorkflowContractSuperseded):
			// Explicit contract supersession closes every open finding:
			// the approved acceptance that minted them is gone.
			for _, id := range lineage.openOrder {
				if finding := lineage.findings[id]; finding != nil && !finding.Closed {
					finding.Closed = true
					finding.ClosureKind = oracleClosureSupersession
				}
			}
		case string(WorkerCompleted):
			var report WorkerCompletedPayload
			if json.Unmarshal([]byte(payload), &report) != nil {
				rows.Close()
				return nil, newFailure(KindInvariantViolation, "worker_oracle_findings", "a stored terminal report is malformed", false, "rebuild the worker events from the authoritative log")
			}
			for _, entry := range report.Evidence {
				if entry.OracleReceipt != nil {
					receipts = append(receipts, *entry.OracleReceipt)
				}
			}
			if report.Review == nil {
				continue
			}
			contextCount := len(report.ContextFindings)
			for index, finding := range report.Review.Findings {
				if finding.Oracle == nil {
					continue
				}
				id := workerRankedFindingID(seq, contextCount, index)
				if finding.Oracle.ContinuesFindingID != "" {
					// Same failure, same canonical open identity: the
					// continuation maps onto the earlier finding and mints
					// nothing. An unresolvable target was refused by the
					// live fold, so the recorded log always resolves.
					if _, exists := lineage.findings[finding.Oracle.ContinuesFindingID]; exists {
						continue
					}
				}
				if _, duplicate := lineage.findings[id]; duplicate {
					continue
				}
				entry := &WorkerOpenOracleFinding{
					FindingID: id, Severity: finding.Severity, Detail: finding.Detail,
					Classification: finding.Oracle.Classification, OwnerID: finding.Oracle.OwnerID,
					FailureFamily: finding.Oracle.FailureFamily, PredicateIDs: finding.Oracle.PredicateIDs,
					LawIDs: lawIDsOf(finding.Oracle), ControlIDs: finding.Oracle.ControlIDs, CaseIDs: finding.Oracle.CaseIDs,
					SourceEventID: eventID, SourceEventSeq: seq, SourceOrdinal: contextCount + index,
					SourceActorRef: actors[report.AttemptID],
					Tie:            finding.Oracle,
					SourceOracle:   oracles[WorkerJobBinding{JobID: jobID, Revision: revision, Digest: digest}],
				}
				if entry.SourceOracle != nil {
					for _, owner := range entry.SourceOracle.Owners {
						if owner.OwnerID == entry.OwnerID {
							entry.DomainID = owner.DomainID
						}
					}
				}
				lineage.findings[id] = entry
				lineage.openOrder = append(lineage.openOrder, id)
			}
			for _, resolution := range report.Review.ResolvedFindings {
				finding, exists := lineage.findings[resolution.FindingID]
				if !exists || finding.Closed {
					continue
				}
				finding.Closed = true
				finding.ClosureKind = oracleClosureResolution
				if finding.Classification == OracleClassificationOracleDefect {
					finding.ClosureKind = oracleClosureOracleDefect
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "worker_oracle_findings", "cannot scan the ranked-finding lineage", true, "retry once the event log is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_oracle_findings", "cannot close the ranked-finding lineage read", true, "retry once the event log is readable", err)
	}
	lineage.retainedReceipts = receipts
	return lineage, nil
}

// validateWorkerOracleCompletedReportTx is the live fold's join half: every
// typed member of one terminal report joins the dispatched immutable job's
// oracle and the prior lineage. Receipts resolve controls, cases, and the
// pinned recipe, and an executed result needs a native run this work
// retained — a signed report is a claim, never proof a command ran.
// Findings bind real owners with predicate, law, or control ties, and an
// uncovered case carries its explicit new entry path. Closures name open
// findings and carry evidence each control's role admits. Replay never
// runs this: the recorded log already passed it. The payload is the report
// being folded, still outside the log: its own receipts count as retained
// current-subject evidence.
func validateWorkerOracleCompletedReportTx(ctx context.Context, tx *sql.Tx, workID string, payload WorkerCompletedPayload, oracle *AcceptanceOracle, lineage *workerOracleFindingLineage) error {
	controls := oracleControlIndex(oracle)
	owners := map[string]OracleOwner{}
	for _, owner := range oracle.Owners {
		owners[owner.OwnerID] = owner
	}
	currentSubject, err := readCurrentOracleSubject(ctx, tx, workID)
	if err != nil {
		return err
	}
	for _, entry := range payload.Evidence {
		if entry.OracleReceipt == nil {
			continue
		}
		if err := validateWorkerOracleReceiptJoins(entry.OracleReceipt, controls); err != nil {
			return err
		}
		if _, err := workerOracleReceiptProducerTx(ctx, tx, workID, entry.OracleReceipt, oracle, currentSubject); err != nil {
			return err
		}
	}
	if payload.Review != nil {
		if oracle != nil {
			for index := range payload.Review.Findings {
				finding := payload.Review.Findings[index]
				if finding.Oracle == nil {
					return oracleFindingFailure(KindInvalidPayload, "a review finding on an oracle-bound dispatch carries no oracle classification", "classify every review finding against the dispatched oracle")
				}
				if err := validateWorkerOracleFindingJoins(finding.Oracle, oracle, owners); err != nil {
					return err
				}
			}
		}
		for index := range payload.Review.Findings {
			finding := payload.Review.Findings[index]
			if finding.Oracle == nil || finding.Oracle.ContinuesFindingID == "" {
				continue
			}
			target, exists := lineage.findings[finding.Oracle.ContinuesFindingID]
			if !exists {
				return oracleFindingFailure(KindInvalidPayload, "review finding continues_finding_id "+finding.Oracle.ContinuesFindingID+" names no earlier ranked finding of this work", "continue an existing ranked finding or report a new one")
			}
			if !sameOracleTie(target, finding.Oracle) {
				return oracleFindingFailure(KindInvalidPayload, "review finding continues_finding_id "+finding.Oracle.ContinuesFindingID+" does not retain the earlier finding's owner, obligation binding, and failure family", "continue the same failure family or report a variant instead")
			}
			if !oracleControlsRetained(target.SourceOracle, oracle) {
				return oracleFindingFailure(KindInvalidPayload, "review continuation rewrites the source owner obligation or inventory", "retain the earlier obligation or report a new finding")
			}
		}
		for index := range payload.Review.Findings {
			finding := payload.Review.Findings[index]
			if finding.Oracle == nil || finding.Oracle.VariantOf == "" {
				continue
			}
			target, exists := lineage.findings[finding.Oracle.VariantOf]
			if !exists || target.OwnerID != finding.Oracle.OwnerID {
				return oracleFindingFailure(KindInvalidPayload, "review finding variant_of "+finding.Oracle.VariantOf+" names no earlier ranked finding of the same owner", "variant an existing owner-family finding")
			}
		}
		for _, resolution := range payload.Review.ResolvedFindings {
			finding, exists := lineage.findings[resolution.FindingID]
			if !exists {
				return oracleFindingFailure(KindInvalidPayload, "resolved_findings names "+resolution.FindingID+", no ranked finding of this work", "close an existing ranked finding by its canonical id")
			}
			if finding.Closed {
				return oracleFindingFailure(KindInvalidPayload, "resolved_findings closes "+resolution.FindingID+", a finding an earlier closure already ended", "close each open finding once")
			}
			if err := validateWorkerOracleResolutionEvidenceTx(ctx, tx, workID, finding, resolution, currentSubject, lineage, oracle, &payload); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateWorkerOracleReceiptJoins binds one receipt to the dispatched
// oracle: every named control and case resolves, the recipe is exactly the
// pinned harness of every named control — never the candidate's modified
// copy — and each named case is exercised by a named control.
func validateWorkerOracleReceiptJoins(receipt *WorkerOracleReceipt, controls map[string]OracleControl) error {
	for _, controlID := range receipt.ControlIDs {
		control, exists := controls[controlID]
		if !exists {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt names control "+controlID+" the dispatched oracle does not declare", "receipt only controls the dispatched job's oracle declares")
		}
		if receipt.RecipeSource != (WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: control.RecipeSource.ProjectID, Path: control.RecipeSource.Path, CommitOID: control.RecipeSource.CommitOID}) {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt recipe_source does not equal the pinned recipe of control "+controlID, "receipt the exact pinned harness identity the control declared")
		}
	}
	for _, caseID := range receipt.CaseIDs {
		controlCovers := false
		for _, controlID := range receipt.ControlIDs {
			if control, exists := controls[controlID]; exists && containsString(control.CaseIDs, caseID) {
				controlCovers = true
				break
			}
		}
		if !controlCovers {
			return oracleFindingFailure(KindInvalidPayload, "oracle_receipt names case "+caseID+" no named control exercises", "name the controls that exercise each receipted case")
		}
	}
	return nil
}

// A qualified receipt joins the native execute plan, complete retained streams,
// and observed case witnesses. It does not prove semantic acceptance. A matched
// native report must resolve that same producer; a started or signed row alone
// supplies neither execution identity nor verification authority.
func workerOracleReceiptProducerTx(ctx context.Context, q queryer, workID string, receipt *WorkerOracleReceipt, oracle *AcceptanceOracle, currentSubject string) (*WorktreeVerifyResult, error) {
	controls := oracleControlIndex(oracle)
	if receipt.SubjectCommit != "" && receipt.SubjectCommit != currentSubject {
		return nil, oracleFindingFailure(KindMissingEvidence, "oracle_receipt subject_commit differs from the current qualified producer subject", "use the current qualified candidate subject")
	}
	if receipt.Result == OracleReceiptResultUnavailable || receipt.Result == OracleReceiptResultNotRun {
		return nil, nil
	}
	if currentSubject == "" || receipt.SubjectCommit != currentSubject {
		return nil, oracleFindingFailure(KindMissingEvidence, "oracle_receipt execution has no current qualified candidate subject", "verify the clean current candidate or report unavailable")
	}
	producer, err := readOracleControlProducerTx(ctx, q, workID, receipt.RunRef, receipt.Result)
	if err != nil {
		return nil, err
	}
	if producer == nil {
		var evidenceRef string
		status := "healthy"
		if receipt.Result == OracleReceiptResultFail {
			status = "failed"
		}
		err := q.QueryRowContext(ctx, `SELECT n.evidence_ref FROM workflow_native_runs n JOIN external_observations o ON o.observation_id=n.observation_id AND o.work_id=n.work_id
			WHERE n.work_id=? AND n.run_id=? AND n.phase='health' AND n.status=? AND n.native_subject_ref=? AND n.verification_state=?
			AND o.subject_kind='native_run' AND o.subject_ref=n.native_subject_ref AND o.subject_digest=n.subject_digest
			AND o.verification_state=? AND o.verification_result=?`, workID, receipt.RunRef, status, "commit:"+currentSubject, string(VerificationVerified), string(VerificationVerified), string(VerificationMatched)).Scan(&evidenceRef)
		if err != nil && err != sql.ErrNoRows {
			return nil, wrapFailure(KindUnavailable, "worker_oracle_findings", "cannot read the receipt's verified native producer", true, "retry once the projection is readable", err)
		}
		if err == nil {
			producer, err = readOracleControlProducerTx(ctx, q, workID, evidenceRef, receipt.Result)
			if err != nil {
				return nil, err
			}
		}
	}
	if producer == nil {
		return nil, oracleFindingFailure(KindMissingEvidence, "oracle_receipt run_ref "+receipt.RunRef+" names no verified command producer for the current subject", "use an actual verify receipt or a matched native report tied to that producer, or report unavailable")
	}
	if producer.SubjectRef != "commit:"+currentSubject || receipt.ExitCode == nil || producer.ExitCode != *receipt.ExitCode || (receipt.Result == OracleReceiptResultPass) != (producer.ExitCode == 0) {
		return nil, oracleFindingFailure(KindMissingEvidence, "oracle_receipt result or subject differs from its producer", "report the producer's exact subject and exit result")
	}
	for _, id := range receipt.ControlIDs {
		control, exists := controls[id]
		if !exists || producer.ProjectID != control.RecipeSource.ProjectID || !slices.Equal(producer.Command, control.Argv) || producer.Oracle == nil || producer.Oracle.ControlID != id || producer.Oracle.LogicalCwd != control.Cwd || producer.Oracle.RecipeSource != control.RecipeSource {
			return nil, oracleFindingFailure(KindMissingEvidence, "oracle_receipt command or Project differs from control "+id, "run the exact control argument vector in its Project")
		}
		// The producer must have executed this oracle's complete
		// owner/control/case bundle, not a control with matching argv, cwd,
		// and recipe whose owner or cases differ.
		bundle, err := nativeBundleForControl(oracle, id)
		if err != nil || producer.Oracle.BundleDigest != nativeBundleDigest(bundle) {
			return nil, oracleFindingFailure(KindMissingEvidence, "oracle_receipt producer executed a bundle that differs from control "+id+" of the dispatched oracle", "execute the dispatched oracle's exact control bundle")
		}
	}
	if err := validateNativeOracleProducerTx(ctx, q, workID, producer, receipt.Result); err != nil {
		return nil, err
	}
	return producer, nil
}

// Failed executions cannot select the current candidate. They may still
// report their actual exit result against a candidate a green verify pinned.
// Failed runs retain only their released lease, never a completion authority
// operation. Their exact native result can report failure, not acceptance.
func readOracleControlProducerTx(ctx context.Context, q queryer, workID, runRef, result string) (*WorktreeVerifyResult, error) {
	if result != OracleReceiptResultFail {
		return readOracleVerifySubjectReceipt(ctx, q, workID, runRef)
	}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT result_json FROM worktree_verify_leases l WHERE work_id=? AND state='released' AND outcome='completed' AND exit_code<>0
		AND json_valid(result_json) AND json_extract(result_json,'$.work_id')=l.work_id
		AND json_extract(result_json,'$.project_id')=l.project_id AND json_extract(result_json,'$.path')=l.path
		AND json_extract(result_json,'$.lease_id')=l.lease_id AND json_extract(result_json,'$.exit_code')=l.exit_code
		AND json_extract(result_json,'$.command')=l.command_json AND json_extract(result_json,'$.tracked_files_changed')=0
		AND json_extract(result_json,'$.operation_ref')=? LIMIT 1`, workID, runRef).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var producer WorktreeVerifyResult
	if err := json.Unmarshal([]byte(raw), &producer); err != nil {
		return nil, err
	}
	if producer.WorkID != workID || producer.OperationRef != worktreeVerifyOperationRef(producer.LeaseID) || !worktreeSHAPattern.MatchString(strings.TrimPrefix(producer.SubjectRef, "commit:")) || producer.ExitCode == 0 || producer.TrackedFilesChanged {
		return nil, oracleFindingFailure(KindInvariantViolation, "failed verify receipt differs from its producer", "reconcile the retained producer")
	}
	return &producer, nil
}

// validateWorkerOracleFindingJoins binds one finding's oracle tie to the
// dispatched oracle's authorities. An uncovered case is a legitimate
// delivery blocker naming an explicit entry path the inventory omitted — it
// is never rejected for naming no control. A follow-up sits outside the
// approved owner/contract and joins nothing.
func validateWorkerOracleFindingJoins(finding *WorkerOracleFinding, oracle *AcceptanceOracle, owners map[string]OracleOwner) error {
	controls := oracleControlIndex(oracle)
	for _, controlID := range finding.ControlIDs {
		if _, exists := controls[controlID]; !exists {
			return oracleFindingFailure(KindInvalidPayload, "review finding names control "+controlID+" the dispatched oracle does not declare", "bind findings to the dispatched job's declared controls")
		}
	}
	cases := map[string]bool{}
	for _, entry := range oracle.Cases {
		cases[entry.CaseID] = true
	}
	for _, caseID := range finding.CaseIDs {
		if !cases[caseID] {
			return oracleFindingFailure(KindInvalidPayload, "review finding names case "+caseID+" the dispatched oracle does not declare", "bind findings to the dispatched job's declared cases")
		}
	}
	owner, ownerNamed := owners[finding.OwnerID]
	switch finding.Classification {
	case OracleClassificationFollowUp:
		return nil
	case OracleClassificationUncoveredCase:
		if !ownerNamed {
			return oracleFindingFailure(KindInvalidPayload, "an uncovered_case must bind the declared owner whose inventory omitted the path", "name the declared owner the new entry path belongs to")
		}
		if finding.EntryPoint == "" {
			return oracleFindingFailure(KindInvalidPayload, "an uncovered_case must name its explicit new entry path or transition", "state the entry path the inventory omitted")
		}
		if len(finding.EvidenceRefs) < 1 {
			return oracleFindingFailure(KindInvalidPayload, "an uncovered_case must carry its reproduction evidence", "bind the evidence that reproduces the uncovered path")
		}
		return nil
	case OracleClassificationOracleDefect:
		if len(finding.ControlIDs) < 1 && len(finding.CaseIDs) < 1 {
			return oracleFindingFailure(KindInvalidPayload, "an oracle_defect must name the defective control or case", "name the harness member the defect concerns")
		}
		if len(finding.EvidenceRefs) < 1 {
			return oracleFindingFailure(KindInvalidPayload, "an oracle_defect must carry its evidence", "bind the evidence of the harness defect")
		}
		return nil
	}
	// delivery_blocker: a real owner plus a predicate, pinned-law, or
	// control tie, with reproducible evidence. A known control failure
	// uses that control.
	if !ownerNamed {
		return oracleFindingFailure(KindInvalidPayload, "a delivery_blocker must bind a declared owner of the dispatched oracle", "name the declared owner whose obligation failed")
	}
	hasPredicate, hasLaw, hasControl := false, false, false
	for _, predicate := range finding.PredicateIDs {
		if !containsString(owner.PredicateIDs, predicate) {
			return oracleFindingFailure(KindInvalidPayload, "a delivery_blocker names predicate "+predicate+" its owner does not cover", "bind the finding to predicates the declared owner covers")
		}
		hasPredicate = true
	}
	for _, binding := range finding.LawBindings {
		pinned := false
		for _, ownerBinding := range owner.LawBindings {
			if ownerBinding.Source.Kind == binding.Source.Kind && ownerBinding.Source.SourceID == binding.Source.SourceID && ownerBinding.Source.LawID == binding.Source.LawID && ownerBinding.Source.ContentHash == binding.Source.ContentHash && ownerBinding.Clause == binding.Clause {
				pinned = true
			}
		}
		if !pinned {
			return oracleFindingFailure(KindInvalidPayload, "a delivery_blocker binds law "+binding.Source.LawID+" its owner did not pin", "bind the law revision the declared owner pinned")
		}
		hasLaw = true
	}
	for _, controlID := range finding.ControlIDs {
		if control, exists := controls[controlID]; exists && control.OwnerID == finding.OwnerID {
			hasControl = true
		}
	}
	if !hasPredicate && !hasLaw && !hasControl {
		return oracleFindingFailure(KindInvalidPayload, "a delivery_blocker requires a predicate, pinned-law, or control tie to its owner", "bind the failed obligation by predicate, pinned law, or control")
	}
	if len(finding.EvidenceRefs) < 1 {
		return oracleFindingFailure(KindInvalidPayload, "a delivery_blocker must carry its reproducible evidence", "bind the evidence that reproduces the failure")
	}
	return nil
}

// rankedFindingView projects one ranked review finding onto the context
// view's finding wire: the tie and the source kind mark it as a ranked
// blocker, never a generic worker-claim notebook entry, and the generic
// claim fields carry the finding's own bounded content. The lineage
// resolves the owner's Domain and the finding's current openness.
func rankedFindingView(finding WorkerReviewFinding, eventID string, eventSeq int64, ordinal int, lineage *workerOracleFindingLineage) WorkContextFindingView {
	id := workerRankedFindingID(eventSeq, 0, ordinal)
	view := WorkContextFindingView{
		FindingID:      id,
		Statement:      finding.Detail,
		Origin:         WorkContextOriginWorkerReport,
		Status:         WorkContextFindingStatusReported,
		SourceEventID:  eventID,
		SourceEventSeq: eventSeq,
		Ordinal:        ordinal,
		SourceKind:     WorkContextSourceKindReviewFinding,
		Oracle:         finding.Oracle,
	}
	subjectRef := "review-finding"
	domain := "unresolved"
	if finding.Oracle != nil && finding.Oracle.OwnerID != "" {
		subjectRef = finding.Oracle.OwnerID
		if lineage != nil {
			if source := lineage.findings[id]; source != nil && source.DomainID != "" {
				domain = source.DomainID
			}
		}
	}
	if len(subjectRef) > workerContextFindingSubjectRefMaxBytes {
		subjectRef = subjectRef[:workerContextFindingSubjectRefMaxBytes]
	}
	view.SubjectRef = subjectRef
	view.DomainID = domain
	if finding.Oracle != nil {
		view.EvidenceRefs = finding.Oracle.EvidenceRefs
	}
	if lineage != nil {
		if entry := lineage.findings[id]; entry != nil && !entry.Closed && entry.blocks() {
			view.Status = WorkContextFindingStatusOpen
			view.DomainID = entry.DomainID
			if view.DomainID == "" {
				view.DomainID = domain
			}
		}
	}
	return view
}

// projectWorkerOracleOpenFindings adds every currently open ranked finding
// to the view. A coordinator's declaration selects generic context, but an
// open oracle blocker is never droppable by selection: it rides the current
// view until a supported closure, an evidence-backed oracle-defect
// correction, or an explicit contract supersession ends it.
func projectWorkerOracleOpenFindings(lineage *workerOracleFindingLineage, add func(WorkContextFindingView)) {
	if lineage == nil {
		return
	}
	for _, id := range lineage.openOrder {
		finding := lineage.findings[id]
		if finding == nil || finding.Closed || !finding.blocks() {
			continue
		}
		view := WorkContextFindingView{
			FindingID:      finding.FindingID,
			Kind:           "",
			Statement:      finding.Detail,
			SubjectRef:     finding.OwnerID,
			EvidenceRefs:   nil,
			DomainID:       finding.DomainID,
			Origin:         WorkContextOriginWorkerReport,
			Status:         WorkContextFindingStatusOpen,
			SourceEventID:  finding.SourceEventID,
			SourceEventSeq: finding.SourceEventSeq,
			Ordinal:        finding.SourceOrdinal,
			SourceKind:     WorkContextSourceKindReviewFinding,
			Oracle:         finding.Tie,
		}
		if finding.Tie != nil {
			view.EvidenceRefs = finding.Tie.EvidenceRefs
		}
		if view.SubjectRef == "" {
			view.SubjectRef = "review-finding"
		}
		if view.DomainID == "" {
			view.DomainID = "unresolved"
		}
		add(view)
	}
}

// Every required control must have a matched current-subject pass. Binding
// an unrelated reference cannot supply a control execution. Independent roles
// also require a binding to that producer and a different producing actor.
func validateWorkerOracleResolutionEvidenceTx(ctx context.Context, tx *sql.Tx, workID string, finding *WorkerOpenOracleFinding, resolution WorkerOracleResolution, currentSubject string, lineage *workerOracleFindingLineage, oracle *AcceptanceOracle, inFlight *WorkerCompletedPayload) error {
	controls := oracleControlIndex(oracle)
	required, err := oracleFindingClosureControls(finding, oracle)
	if err != nil {
		return err
	}
	receipts := append(append([]WorkerOracleReceipt(nil), lineage.retainedReceipts...), inFlightOracleReceipts(inFlight)...)
	covered := map[string]bool{}
	for _, ref := range resolution.EvidenceRefs {
		qualified := false
		for _, receipt := range receipts {
			if receipt.Result != OracleReceiptResultPass || currentSubject == "" || receipt.SubjectCommit != currentSubject {
				continue
			}
			if err := validateWorkerOracleReceiptJoins(&receipt, controls); err != nil {
				continue
			}
			producer, producerErr := workerOracleReceiptProducerTx(ctx, tx, workID, &receipt, oracle, currentSubject)
			if producerErr != nil {
				return producerErr
			}
			bound, bindErr := oracleClosureProducerBoundTx(ctx, tx, workID, ref, producer.OperationRef)
			if bindErr != nil {
				return bindErr
			}
			if ref != receipt.RunRef && !bound {
				continue
			}
			for _, controlID := range receipt.ControlIDs {
				if !required[controlID] || !oracleReceiptCoversControl(receipt, controls[controlID]) {
					continue
				}
				fresh, freshErr := oracleControlProducerFreshTx(ctx, tx, workID, currentSubject, controls[controlID], producer)
				if freshErr != nil {
					return freshErr
				}
				if !fresh {
					continue
				}
				if controls[controlID].RequiredEvidenceRole == OracleEvidenceRoleIndependentlyExecuted {
					independent, independentErr := oracleClosureIndependentProducerTx(ctx, tx, workID, producer, inFlight, finding.SourceActorRef)
					if independentErr != nil {
						return independentErr
					}
					if !bound || !independent {
						continue
					}
				}
				covered[controlID], qualified = true, true
			}
		}
		if !qualified {
			return oracleFindingFailure(KindMissingEvidence, "resolved_findings evidence "+ref+" for "+resolution.FindingID+" is neither durably bound evidence nor a current-subject pass receipt of the finding's controls with the required production role", "supply matched current control executions with the required independent producer bindings")
		}
	}
	for id := range required {
		if !covered[id] {
			return oracleFindingFailure(KindMissingEvidence, "resolved_findings lacks a fresh matched pass for control "+id, "prove every required control on the current candidate")
		}
	}
	return nil
}

func oracleFindingClosureControls(finding *WorkerOpenOracleFinding, oracle *AcceptanceOracle) (map[string]bool, error) {
	if finding.SourceOracle != nil && !oracleControlsRetained(finding.SourceOracle, oracle) {
		return nil, oracleFindingFailure(KindMissingEvidence, "closure rewrites the finding's source owner, cases, or controls", "retain the source obligation or supersede the contract")
	}
	required := map[string]bool{}
	for _, id := range finding.ControlIDs {
		required[id] = true
	}
	for _, entry := range oracle.Cases {
		if containsString(finding.CaseIDs, entry.CaseID) {
			for _, id := range entry.ControlIDs {
				required[id] = true
			}
		}
	}
	if finding.Classification == OracleClassificationUncoveredCase {
		if finding.Tie == nil {
			return nil, oracleFindingFailure(KindMissingEvidence, "uncovered_case has no retained reproduction path", "retain the uncovered path")
		}
		found := false
		for _, entry := range oracle.Cases {
			if entry.OwnerID == finding.OwnerID && entry.EntryPoint == finding.Tie.EntryPoint {
				found = true
				for _, id := range entry.ControlIDs {
					required[id] = true
				}
			}
		}
		if !found {
			return nil, oracleFindingFailure(KindMissingEvidence, "uncovered_case entry path is still absent from the controlled inventory", "add the reproduced entry path and prove its controls")
		}
	}
	if len(required) == 0 {
		for _, control := range oracle.Controls {
			if control.OwnerID == finding.OwnerID {
				required[control.ControlID] = true
			}
		}
	}
	if len(required) == 0 {
		return nil, oracleFindingFailure(KindMissingEvidence, "closure names no executable obligation", "prove the finding's owner controls")
	}
	return required, nil
}

func oracleReceiptCoversControl(receipt WorkerOracleReceipt, control OracleControl) bool {
	for _, id := range control.CaseIDs {
		if !containsString(receipt.CaseIDs, id) {
			return false
		}
	}
	return true
}

// Freshness comes from native execution order, not the order in which a model
// repeats receipts. A later failure of this exact command on the same subject
// prevents an earlier success from closing a finding, even if re-reported.
func oracleControlProducerFreshTx(ctx context.Context, q queryer, workID, subject string, control OracleControl, producer *WorktreeVerifyResult) (bool, error) {
	if producer.Oracle == nil {
		return false, nil
	}
	var lease string
	err := q.QueryRowContext(ctx, `SELECT lease_id FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND command_json=? AND state='released' AND json_valid(result_json) AND json_extract(result_json,'$.subject_ref')='commit:'||? AND json_extract(result_json,'$.oracle.phase')='execute' AND json_extract(result_json,'$.oracle.bundle_digest')=? AND json_extract(result_json,'$.oracle.logical_cwd')=? AND json_extract(result_json,'$.oracle.build_environment_digest')=? ORDER BY rowid DESC LIMIT 1`, workID, control.RecipeSource.ProjectID, workflowJSON(control.Argv), subject, producer.Oracle.BundleDigest, control.Cwd, producer.Oracle.BuildEnvironmentDigest).Scan(&lease)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return lease == producer.LeaseID, nil
}

func oracleClosureProducerBoundTx(ctx context.Context, q queryer, workID, ref, producer string) (bool, error) {
	var bound int
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=? AND json_extract(payload,'$.producer_run_ref')=? AND json_extract(payload,'$.evidence_kind') IN ('verification','native_run'))`, workID, WorkflowEvidenceBound, ref, producer).Scan(&bound)
	return bound != 0, err
}

func oracleClosureIndependentProducerTx(ctx context.Context, q queryer, workID string, producer *WorktreeVerifyResult, payload *WorkerCompletedPayload, sourceActor string) (bool, error) {
	if payload == nil || payload.AttemptID == "" {
		return false, nil
	}
	var client, agent, session, principal, worker string
	err := q.QueryRowContext(ctx, `SELECT l.client_ref,l.agent_ref,l.session_ref,l.principal_ref,COALESCE(json_extract(d.payload,'$.lane_actor_ref'),'') FROM worktree_verify_leases l JOIN domain_events d ON d.subject_id=l.work_id AND d.subject_type='work_item' AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=? WHERE l.work_id=? AND l.lease_id=?`, WorkerDispatched, payload.AttemptID, workID, producer.LeaseID).Scan(&client, &agent, &session, &principal, &worker)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	actor := DeriveWorkflowActorRef(principal, client, agent, session)
	return worker != "" && sourceActor != "" && actor != worker && actor != sourceActor, nil
}

// inFlightOracleReceipts is a seam the fold uses to add the report being
// validated to the retained-receipt set: the event is not yet in the log,
// so its own receipts qualify as current-subject evidence.
func inFlightOracleReceipts(payload *WorkerCompletedPayload) []WorkerOracleReceipt {
	if payload == nil {
		return nil
	}
	receipts := make([]WorkerOracleReceipt, 0, 4)
	for _, entry := range payload.Evidence {
		if entry.OracleReceipt != nil {
			receipts = append(receipts, *entry.OracleReceipt)
		}
	}
	return receipts
}

// validateCorrectionOpenFindingsTx enforces the derived-set equality on
// oracle-capable histories: the open_finding_ids a rejection or correction
// request carries must exactly equal the lineage's derived open set. A
// shorter list is not resolution — omission never closes a finding. On an
// oracle-free history the check is absent and the legacy behavior stands.
func validateCorrectionOpenFindingsTx(ctx context.Context, q queryer, workID string, supplied []string, present bool) error {
	lineage, err := readWorkerOracleFindingLineageTx(ctx, q, workID)
	if err != nil {
		return err
	}
	if !lineage.oracleCapable {
		return nil
	}
	derived := lineage.openFindingIDs()
	if len(derived) > WorkflowOpenOracleFindingsLimit {
		return newFailure(KindLimitExceeded, "worker_oracle_findings", fmt.Sprintf("the work holds %d open oracle blockers above the %d-ID bound; the correction surface refuses rather than truncate the set", len(derived), WorkflowOpenOracleFindingsLimit), false, "close findings through supported closures or an approved scope/contract revision, or stop the work")
	}
	if len(derived) == 0 {
		// The payload grammar cannot carry zero ids, so an empty derived
		// set is recorded through the field's absence — exactly.
		if present {
			return oracleFindingFailure(KindInvalidPayload, "open_finding_ids names ids while the derived open set holds 0; the empty set is recorded by omitting the field", "omit open_finding_ids when every finding is closed")
		}
		return nil
	}
	if !present {
		return oracleFindingFailure(KindInvalidPayload, "an oracle-capable history requires open_finding_ids equal to the derived open finding set", "record the complete derived open finding set")
	}
	if len(supplied) != len(derived) {
		return oracleFindingFailure(KindInvalidPayload, fmt.Sprintf("open_finding_ids carries %d ids while the derived open set holds %d; omission and addition are both refusals", len(supplied), len(derived)), "record exactly the derived open finding set")
	}
	derivedSet := make(map[string]bool, len(derived))
	for _, id := range derived {
		derivedSet[id] = true
	}
	for _, id := range supplied {
		if !derivedSet[id] {
			return oracleFindingFailure(KindInvalidPayload, "open_finding_ids names "+id+", which is not in the derived open finding set", "record exactly the derived open finding set")
		}
	}
	return nil
}

func loadWorkflowOracleAdmissionTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, state *WorkflowAdmissionState) error {
	if !workflowOwnerOracleActive(definition) {
		return nil
	}
	lineage, err := readWorkerOracleFindingLineageTx(ctx, q, workID)
	if err != nil {
		return err
	}
	state.OracleOpenFindings = len(lineage.openFindingIDs())
	return nil
}
