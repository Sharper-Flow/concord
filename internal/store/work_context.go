package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The store work-context surface. The record_work_context action appends
// one typed declaration event onto the existing workflow-action route, and
// one tx-scoped reader assembles the current view from that event log for
// continuity and dispatch packets. The event log owns the declaration and
// report-claim content, and the research authority owns the content behind
// selected references; this surface adds no projection table, no export
// command, and no cache, and nothing here promotes context content into
// Git or knowledge.

// Closed bounds of the work-context surface. The action declares the full
// current reading list (at most 32), selects at most 32 exact earlier
// finding references, and contributes at most 16 new findings; the current
// view holds at most 32 finding slots across its generic, ranked, and
// research entries, and its complete serialization stays inside 64 KiB. An
// over-bound action or view refuses explicitly; content is never truncated
// to fit.
const (
	WorkContextRequiredReadingMax  = 32
	WorkContextFindingRefsMax      = 32
	WorkContextActionFindingsMax   = 16
	WorkContextViewFindingsMax     = 32
	WorkContextCombinedMaxBytes    = 64 * 1024
	workContextReadingMaxBytes     = 512
	workContextRationaleMaxBytes   = 512
	workContextDomainIDMaxBytes    = 256
	workContextProjectIDMaxBytes   = 128
	workContextPathMaxBytes        = 512
	workContextLawIDMaxBytes       = 256
	workContextReportEventScanRows = 33
)

// The closed source union of one required reading. A repository reading
// names the Project and one normalized repository-contained path at a
// commit; a knowledge reading names the store's law-revision identity. No
// arm accepts the other arm's fields, and no reading accepts a tmp source.
const (
	WorkContextSourceRepositoryFile = "repository_file"
	WorkContextSourceKnowledge      = "knowledge"
)

// Finding origins the reader derives from the source event kind. A
// declaration finding was authored by the coordinator through the action; a
// worker-report finding was reported on a terminal worker report.
const (
	WorkContextOriginDeclaration  = "declaration"
	WorkContextOriginWorkerReport = "worker_report"
	// WorkContextFindingStatusReported is the finding status generic
	// findings carry: a finding is a reported claim, never acceptance.
	WorkContextFindingStatusReported = "reported"
	// WorkContextFindingStatusOpen is the status of a ranked review
	// finding the lineage currently holds open: an open blocker
	// is still a reported claim, and the status names its openness, never
	// acceptance.
	WorkContextFindingStatusOpen = "open"
)

// WorkContextReadingSource is the closed source union of one reading. The
// Kind selects the arm; the other arm's fields must stay empty.
type WorkContextReadingSource struct {
	Kind        string `json:"kind"`
	ProjectID   string `json:"project_id,omitempty"`
	Path        string `json:"path,omitempty"`
	CommitOID   string `json:"commit_oid,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
	LawID       string `json:"law_id,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

// WorkContextReading is one required reading entry of the current context.
// DomainID is validated against the current Domain registry at admission;
// ProductWideRationale is required exactly when DomainID names the
// registry's root Domain.
type WorkContextReading struct {
	DomainID             string                   `json:"domain_id"`
	Reason               string                   `json:"reason"`
	ProductWideRationale string                   `json:"product_wide_rationale,omitempty"`
	Source               WorkContextReadingSource `json:"source"`
}

// WorkContextFindingView is one assembled finding of the current view. The
// finding wire is the shared WorkerContextFinding; the identity fields are
// core-derived from the source event, never authored. SourceKind is empty
// for a generic context finding and review_finding for a ranked review
// finding, whose Oracle tie and lifecycle the lineage owns.
type WorkContextFindingView struct {
	FindingID            string   `json:"finding_id"`
	Kind                 string   `json:"kind"`
	Statement            string   `json:"statement"`
	SubjectRef           string   `json:"subject_ref"`
	EvidenceRefs         []string `json:"evidence_refs"`
	DomainID             string   `json:"domain_id"`
	ProductWideRationale string   `json:"product_wide_rationale,omitempty"`
	Origin               string   `json:"origin"`
	Status               string   `json:"status"`
	SourceEventID        string   `json:"source_event_id"`
	SourceEventSeq       int64    `json:"source_event_seq"`
	Ordinal              int      `json:"ordinal"`
	// SourceKind separates the ranked review findings from the
	// generic worker-claim notebook: a ranked entry carries
	// review_finding here, its Oracle tie below, and generic claim fields
	// projected from the finding.
	SourceKind string               `json:"source_kind,omitempty"`
	Oracle     *WorkerOracleFinding `json:"oracle,omitempty"`
}

// WorkContextDomainGroup partitions the current view's readings and findings
// by Domain, in the contract's approved affected-Domain order. Empty slots
// are legal: a group may hold only readings or only findings.
type WorkContextDomainGroup struct {
	DomainID                string   `json:"domain_id"`
	RequiredReadingOrdinals []int    `json:"required_reading_ordinals"`
	FindingIDs              []string `json:"finding_ids"`
}

// WorkContextView is the current work context the tx-scoped reader
// assembles: the latest declaration anchor's required reading, the active
// findings (anchor findings, selected earlier findings, and subsequent
// terminal-report findings), and the Domain grouping. SourceEventFrontier is
// the highest sequence among the work item's context-source events
// (declarations and terminal worker reports) at read time, so a packet built
// from this view goes stale exactly when a context source changes, and
// unrelated work events do not invalidate it.
type WorkContextView struct {
	SubjectCommit       string                    `json:"subject_commit,omitempty"`
	OraclePreparations  []NativeOraclePreparation `json:"oracle_preparations,omitempty"`
	SourceEventFrontier int64                     `json:"source_event_frontier"`
	RequiredReading     []WorkContextReading      `json:"required_reading"`
	Findings            []WorkContextFindingView  `json:"findings"`
	DomainGroups        []WorkContextDomainGroup  `json:"domain_groups"`
	// ResearchFindings carries the latest declaration anchor's selected
	// research in authored order, resolved through the canonical
	// selected-read helper. Research entries and event-owned findings share
	// the 32-slot view bound and the whole-view byte bound; the member is
	// omitted when the declaration selects none.
	ResearchFindings []WorkContextResearchFindingView `json:"research_findings,omitempty"`
	// OracleReceipts are the prior typed control-execution receipts this
	// work retained, in log order: reported evidence a later
	// lane receives as regression baselines with their exact identities,
	// never as current-subject acceptance.
	OracleReceipts []WorkerOracleReceipt `json:"oracle_receipts,omitempty"`
}

// workflowWorkContextRecordedPayload is the durable declaration event. The
// anchor's authored content is retained verbatim; finding identity is
// derived from the event sequence and ordinal at read time, so the payload
// never carries its own sequence.
type workflowWorkContextRecordedPayload struct {
	WorkflowVersionFields
	StepID          string                 `json:"step_id"`
	AttemptEpoch    int64                  `json:"attempt_epoch"`
	RequiredReading []WorkContextReading   `json:"required_reading"`
	FindingRefs     []string               `json:"finding_refs"`
	ContextFindings []WorkerContextFinding `json:"context_findings"`
	// ResearchSelections stores references only: the optional member is
	// absent and empty for the same meaning, and a new full declaration
	// explicitly replaces its research selections.
	ResearchSelections        []WorkContextResearchSelection `json:"research_finding_refs,omitempty"`
	WorkflowRef               string                         `json:"workflow_ref"`
	WorkflowDefinitionVersion int64                          `json:"workflow_definition_version"`
	WorkflowDefinitionDigest  string                         `json:"workflow_definition_digest"`
	ActorRef                  string                         `json:"actor_ref"`
	RequestID                 string                         `json:"request_id"`
}

var workContextFindingRefPattern = regexp.MustCompile(`^finding:([0-9]+):([0-9]+)$`)

// workContextOverflowRecovery is the one truthful recovery text for every
// shared work-context bound: the declaration's optional context can reduce
// through a later declaration, and a view whose mandatory context alone
// exceeds the bound names the approved contract supersession or stop route
// instead of promising that reduction always repairs.
func workContextOverflowRecovery() string {
	return "reduce the declaration's optional context (readings, findings, or research selections) through a later declaration; if mandatory context alone exceeds the bound, supersede the active contract through its approved recovery route or stop the work"
}

// workContextFailure bounds the refusal messages of this surface.
func workContextFailure(kind FailureKind, operation, detail, remedy string) error {
	return newFailure(kind, operation, detail, false, remedy)
}

// validateWorkContextDomainID bounds one domain identity reference.
func validateWorkContextDomainID(domainID string) bool {
	return len(domainID) >= 1 && len(domainID) <= workContextDomainIDMaxBytes
}

// validateWorkContextRationale bounds the optional product-wide rationale.
func validateWorkContextRationale(rationale string) bool {
	return len(rationale) <= workContextRationaleMaxBytes
}

// validateWorkContextRepoPath enforces repository containment: a normalized
// relative path whose segments stay inside the repository, never a
// repository-external tmp scratch source.
func validateWorkContextRepoPath(path string) error {
	if len(path) < 1 || len(path) > workContextPathMaxBytes {
		return workContextFailure(KindInvalidPayload, "work_context", "repository reading path must be between 1 and 512 bytes", "supply a repository-contained path")
	}
	if strings.ContainsFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return workContextFailure(KindInvalidPayload, "work_context", "repository reading path must not contain control characters", "supply a normalized relative path")
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "./") || strings.HasSuffix(path, "/") {
		return workContextFailure(KindInvalidPayload, "work_context", "repository reading path must be a normalized relative path", "supply a repository-contained path without a leading slash or trailing separator")
	}
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return workContextFailure(KindInvalidPayload, "work_context", "repository reading path must be a normalized relative path", "supply a repository-contained path without traversal or self segments")
		}
	}
	if segments[0] == "tmp" {
		return workContextFailure(KindInvalidPayload, "work_context", "repository reading path names a tmp source the context never requires", "read from the repository or the knowledge home instead of tmp scratch")
	}
	return nil
}

// validateWorkContextReading enforces one reading's closed shape.
func validateWorkContextReading(reading WorkContextReading) error {
	if !validateWorkContextDomainID(reading.DomainID) {
		return workContextFailure(KindInvalidPayload, "work_context", "reading domain_id must be between 1 and 256 bytes", "name a current Domain of the Product registry")
	}
	if len(reading.Reason) < 2 || len(reading.Reason) > workContextReadingMaxBytes {
		return workContextFailure(KindInvalidPayload, "work_context", "reading reason must be between 2 and 512 bytes", "state why the reading is required")
	}
	if !validateWorkContextRationale(reading.ProductWideRationale) {
		return workContextFailure(KindInvalidPayload, "work_context", "reading product_wide_rationale exceeds 512 bytes", "bound the product-wide rationale")
	}
	switch reading.Source.Kind {
	case WorkContextSourceRepositoryFile:
		if len(reading.Source.ProjectID) < 2 || len(reading.Source.ProjectID) > workContextProjectIDMaxBytes {
			return workContextFailure(KindInvalidPayload, "work_context", "repository reading project_id must be between 2 and 128 bytes", "name the Project that owns the repository")
		}
		if err := validateWorkContextRepoPath(reading.Source.Path); err != nil {
			return err
		}
		if !workContextCommitOID(reading.Source.CommitOID) {
			return workContextFailure(KindInvalidPayload, "work_context", "repository reading commit_oid must be 40 to 64 hex bytes", "pin the commit the reading was taken at")
		}
		if reading.Source.SourceID != "" || reading.Source.LawID != "" || reading.Source.ContentHash != "" {
			return workContextFailure(KindInvalidPayload, "work_context", "repository reading carries knowledge source fields", "use one closed source arm")
		}
	case WorkContextSourceKnowledge:
		if len(reading.Source.SourceID) < 2 || len(reading.Source.SourceID) > workContextProjectIDMaxBytes {
			return workContextFailure(KindInvalidPayload, "work_context", "knowledge reading source_id must be between 2 and 128 bytes", "name the knowledge source")
		}
		if len(reading.Source.LawID) < 2 || len(reading.Source.LawID) > workContextLawIDMaxBytes {
			return workContextFailure(KindInvalidPayload, "work_context", "knowledge reading law_id must be between 2 and 256 bytes", "name the bound law")
		}
		if !workflowDigest(reading.Source.ContentHash) {
			return workContextFailure(KindInvalidPayload, "work_context", "knowledge reading content_hash must be a sha256 digest", "pin the law revision content hash")
		}
		if reading.Source.ProjectID != "" || reading.Source.Path != "" || reading.Source.CommitOID != "" {
			return workContextFailure(KindInvalidPayload, "work_context", "knowledge reading carries repository source fields", "use one closed source arm")
		}
	default:
		return workContextFailure(KindInvalidPayload, "work_context", "reading source kind must be repository_file or knowledge", "use the closed source union")
	}
	return nil
}

// workContextCommitOID accepts a 40-to-64 byte lowercase hex commit.
func workContextCommitOID(oid string) bool {
	if len(oid) < 40 || len(oid) > 64 {
		return false
	}
	return strings.Trim(oid, "0123456789abcdef") == ""
}

// validateWorkContextActionFinding enforces one action finding's closed
// shape: the shared report wire plus the required domain identity the
// action declaration always carries.
func validateWorkContextActionFinding(finding WorkerContextFinding) error {
	if err := validateWorkerContextFindingEntry(finding); err != nil {
		return err
	}
	if !validateWorkContextDomainID(finding.DomainID) {
		return workContextFailure(KindInvalidPayload, "work_context", "finding domain_id must be between 1 and 256 bytes", "name a current Domain of the Product registry")
	}
	if !validateWorkContextRationale(finding.ProductWideRationale) {
		return workContextFailure(KindInvalidPayload, "work_context", "finding product_wide_rationale exceeds 512 bytes", "bound the product-wide rationale")
	}
	return nil
}

// validateWorkContextDeclarationShape enforces the closed declaration shape
// the action route and the fold share: counts, per-entry bounds, and the
// combined 64 KiB bound. It reads no registry, so replay never needs
// today's registry state.
func validateWorkContextDeclarationShape(payload workflowWorkContextRecordedPayload) error {
	if payload.RequiredReading == nil || payload.FindingRefs == nil || payload.ContextFindings == nil {
		return workContextFailure(KindInvalidPayload, "work_context", "declaration arrays must be present, possibly empty", "supply required_reading, finding_refs, and context_findings")
	}
	if len(payload.RequiredReading) > WorkContextRequiredReadingMax {
		return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("declaration carries %d readings; the current list holds at most %d", len(payload.RequiredReading), WorkContextRequiredReadingMax), "declare at most 32 readings")
	}
	if len(payload.FindingRefs) > WorkContextFindingRefsMax {
		return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("declaration selects %d finding refs; at most %d are selected", len(payload.FindingRefs), WorkContextFindingRefsMax), "select at most 32 earlier findings")
	}
	if len(payload.ContextFindings) > WorkContextActionFindingsMax {
		return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("declaration carries %d findings; at most %d are declared", len(payload.ContextFindings), WorkContextActionFindingsMax), "declare at most 16 new findings")
	}
	for index, reading := range payload.RequiredReading {
		if err := validateWorkContextReading(reading); err != nil {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("reading %d: %s", index, err.Error()), "supply a closed reading entry")
		}
	}
	for index, finding := range payload.ContextFindings {
		if err := validateWorkContextActionFinding(finding); err != nil {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("finding %d: %s", index, err.Error()), "supply a closed finding entry")
		}
	}
	for index, ref := range payload.FindingRefs {
		if !workContextFindingRefPattern.MatchString(ref) {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("finding ref %d must be finding:<event_seq>:<ordinal>, got %q", index, ref), "select earlier findings by their exact ids")
		}
	}
	if len(payload.ResearchSelections) > WorkContextFindingRefsMax {
		return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("declaration selects %d research findings; at most %d are selected", len(payload.ResearchSelections), WorkContextFindingRefsMax), "select at most 32 research findings")
	}
	seenResearch := make(map[string]bool, len(payload.ResearchSelections))
	for index, selection := range payload.ResearchSelections {
		if !ValidReference(selection.PackID) {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("research selection %d carries an invalid pack reference", index), "name the research pack by its valid reference")
		}
		if selection.Revision < 1 {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("research selection %d must pin a revision of at least 1", index), "pin the exact selected revision")
		}
		if !ValidReference(selection.FindingID) {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("research selection %d carries an invalid finding reference", index), "name the selected finding by its valid reference")
		}
		if !validateWorkContextDomainID(selection.DomainID) {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("research selection %d domain_id must be between 1 and 256 bytes", index), "name a current Domain of the Product registry")
		}
		if !validateWorkContextRationale(selection.ProductWideRationale) {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("research selection %d product_wide_rationale exceeds 512 bytes", index), "bound the product-wide rationale")
		}
		identity := selection.PackID + "\x00" + strconv.FormatInt(selection.Revision, 10) + "\x00" + selection.FindingID
		if seenResearch[identity] {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("research selection %d repeats the pack, revision, and finding of an earlier selection", index), "select each research finding once")
		}
		seenResearch[identity] = true
	}
	// The combined bound measures the declaration's new context content. An
	// over-bound declaration refuses whole; content is never truncated.
	readingsJSON, readingsErr := json.Marshal(payload.RequiredReading)
	findingsJSON, findingsErr := json.Marshal(payload.ContextFindings)
	if readingsErr != nil || findingsErr != nil {
		return workContextFailure(KindInvalidPayload, "work_context", "declaration content cannot be measured", "repair the declaration content")
	}
	if len(readingsJSON)+len(findingsJSON) > WorkContextCombinedMaxBytes {
		return workContextFailure(KindLimitExceeded, "work_context", "declaration context content exceeds the 64 KiB combined bound and is refused, never truncated", "reduce the declaration's readings and findings")
	}
	return nil
}

// ValidateWorkContextDomainTx validates one domain reference of a
// work-context entry against the current Domain registry, the work's
// primary Product scope, and the approved contract's affected-Domain scope.
// The root Domain is product-wide by nature: it is admissible only under
// the explicitly approved root affected scope with a nonempty product-wide
// rationale, and a child Domain never carries one. No heuristic content
// analysis runs here: identity, registry state, and scope decide.
func ValidateWorkContextDomainTx(ctx context.Context, tx *sql.Tx, workID, domainID, productWideRationale string) error {
	if !validateWorkContextDomainID(domainID) {
		return workContextFailure(KindInvalidPayload, "work_context_domain", "domain_id must be between 1 and 256 bytes", "name a current Domain of the Product registry")
	}
	if !validateWorkContextRationale(productWideRationale) {
		return workContextFailure(KindInvalidPayload, "work_context_domain", "product_wide_rationale exceeds 512 bytes", "bound the product-wide rationale")
	}
	productID, err := architectureBindingProductIDTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	var rootDomainID, registryHash string
	if err := tx.QueryRowContext(ctx, `SELECT root_domain_id,content_hash FROM domain_registries WHERE product_id=?`, productID).Scan(&rootDomainID, &registryHash); err != nil {
		if err == sql.ErrNoRows {
			return workContextFailure(KindUnknownScope, "work_context_domain", "Product has no current Domain registry", "publish and rebuild the Product Domain registry")
		}
		return wrapFailure(KindUnavailable, "work_context_domain", "cannot read the Product Domain registry", true, "retry once the registry projection is readable", err)
	}
	var status, domainHash string
	if err := tx.QueryRowContext(ctx, `SELECT status,registry_content_hash FROM domains WHERE product_id=? AND domain_id=?`, productID, domainID).Scan(&status, &domainHash); err != nil {
		if err == sql.ErrNoRows {
			var elsewhere int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domains WHERE domain_id=?`, domainID).Scan(&elsewhere); err != nil {
				return wrapFailure(KindUnavailable, "work_context_domain", "cannot resolve the named Domain", true, "retry once the Domain projection is readable", err)
			}
			if elsewhere > 0 {
				return workContextFailure(KindUnknownScope, "work_context_domain", "Domain "+domainID+" belongs to another Product", "name a current Domain of the work's Product")
			}
			return workContextFailure(KindUnknownScope, "work_context_domain", "unknown Domain "+domainID, "name a current Domain of the Product registry")
		}
		return wrapFailure(KindUnavailable, "work_context_domain", "cannot read the named Domain", true, "retry once the Domain projection is readable", err)
	}
	if status != "current" || domainHash != registryHash {
		return workContextFailure(KindStaleRequiresReview, "work_context_domain", "non-current or stale Domain "+domainID, "rebuild the current Product Domain registry")
	}
	// The approved contract's affected scope binds only when the work holds
	// a contract with an architecture binding.
	contractVersion, contractErr := activeWorkflowContractVersion(ctx, tx, workID, "work_context_domain")
	if contractErr != nil && contractErr != sql.ErrNoRows {
		return contractErr
	}
	if contractErr == nil {
		var binding int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&binding); err == nil {
			var affected int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contract_affected_domains WHERE work_id=? AND contract_version=? AND domain_id=?`, workID, contractVersion, domainID).Scan(&affected); err != nil {
				return wrapFailure(KindUnavailable, "work_context_domain", "cannot read the approved affected Domain scope", true, "retry once the contract projection is readable", err)
			}
			if affected == 0 {
				return workContextFailure(KindUnknownScope, "work_context_domain", "Domain "+domainID+" is outside the approved affected Domain scope of the active contract", "name a Domain the approved contract affects")
			}
		} else if err != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, "work_context_domain", "cannot read the contract architecture binding", true, "retry once the contract projection is readable", err)
		}
	}
	if domainID == rootDomainID {
		if strings.TrimSpace(productWideRationale) == "" {
			return workContextFailure(KindInvalidPayload, "work_context_domain", "the root Domain "+domainID+" requires a nonempty product-wide rationale", "state why the claim spans every Domain of the Product")
		}
		return nil
	}
	if productWideRationale != "" {
		return workContextFailure(KindInvalidPayload, "work_context_domain", "the child Domain "+domainID+" must not carry a product-wide rationale", "drop the rationale or declare the root Domain")
	}
	return nil
}

// parseWorkContextFindingRef splits one exact finding reference.
func parseWorkContextFindingRef(ref string) (seq int64, ordinal int, ok bool) {
	match := workContextFindingRefPattern.FindStringSubmatch(ref)
	if match == nil {
		return 0, 0, false
	}
	parsedSeq, seqErr := strconv.ParseInt(match[1], 10, 64)
	parsedOrdinal, ordinalErr := strconv.Atoi(match[2])
	if seqErr != nil || ordinalErr != nil || parsedSeq < 1 || parsedOrdinal < 0 {
		return 0, 0, false
	}
	return parsedSeq, parsedOrdinal, true
}

// workContextFindingsAtEvent reads the finding entries one referenced event
// carries, refusing an event kind that never holds findings.
func workContextFindingsAtEvent(event Event) ([]WorkerContextFinding, bool) {
	switch event.Kind {
	case WorkflowWorkContextRecorded:
		var payload workflowWorkContextRecordedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return nil, false
		}
		return payload.ContextFindings, true
	case WorkerCompleted:
		var payload WorkerCompletedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return nil, false
		}
		return payload.ContextFindings, true
	case WorkerFailed:
		var payload WorkerFailedPayload
		if json.Unmarshal(event.Payload, &payload) != nil {
			return nil, false
		}
		return payload.ContextFindings, true
	}
	return nil, false
}

// validateWorkContextFindingRefs resolves every selected reference against
// the events this work item already holds, inside the caller's transaction.
// A reference must name a real earlier finding: a recorded sequence of a
// findings-bearing kind with an in-range ordinal.
func validateWorkContextFindingRefs(ctx context.Context, tx *sql.Tx, workID string, refs []string) error {
	var frontier int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&frontier); err != nil {
		return wrapFailure(KindUnavailable, "work_context", "cannot read the work event frontier", true, "retry once the event log is readable", err)
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if seen[ref] {
			return workContextFailure(KindInvalidPayload, "work_context", "finding ref "+ref+" is selected twice", "select each earlier finding once")
		}
		seen[ref] = true
		seq, ordinal, ok := parseWorkContextFindingRef(ref)
		if !ok {
			return workContextFailure(KindInvalidPayload, "work_context", "finding ref "+ref+" must be finding:<event_seq>:<ordinal>", "select earlier findings by their exact ids")
		}
		if seq > frontier {
			return workContextFailure(KindInvalidPayload, "work_context", "finding ref "+ref+" names a finding no event has recorded", "select earlier findings by their exact ids")
		}
		var event Event
		if err := tx.QueryRowContext(ctx, `SELECT event_id,kind,subject_type,subject_id,actor,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND seq=?`, workID, seq).Scan(&event.EventID, &event.Kind, &event.SubjectType, &event.SubjectID, &event.Actor, &event.Payload); err != nil {
			if err == sql.ErrNoRows {
				return workContextFailure(KindInvalidPayload, "work_context", "finding ref "+ref+" names no recorded event", "select earlier findings by their exact ids")
			}
			return wrapFailure(KindUnavailable, "work_context", "cannot resolve a finding reference", true, "retry once the event log is readable", err)
		}
		findings, ranked, decodes := workContextTerminalFindingsAtEvent(event)
		if !decodes {
			return workContextFailure(KindInvalidPayload, "work_context", "finding ref "+ref+" names an event kind that holds no findings", "select findings from declarations or terminal worker reports")
		}
		if ordinal >= len(findings)+len(ranked) {
			return workContextFailure(KindInvalidPayload, "work_context", "finding ref "+ref+" names an ordinal the event does not hold", "select earlier findings by their exact ids")
		}
	}
	return nil
}

// workflowRecordWorkContextEvents is the record_work_context semantic
// constructor: it validates the closed declaration shape, validates every
// domain reference against the current registry and approved affected scope
// in the caller's transaction, resolves every finding reference, and
// assembles the typed declaration event.
func workflowRecordWorkContextEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, stepID, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	var payload workflowWorkContextRecordedPayload
	if raw := fields["required_reading"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &payload.RequiredReading); err != nil {
			return nil, workContextFailure(KindInvalidPayload, "work_context", "required_reading must be an array of closed reading entries", "supply the required_reading array")
		}
	} else {
		payload.RequiredReading = []WorkContextReading{}
	}
	if raw := fields["finding_refs"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &payload.FindingRefs); err != nil {
			return nil, workContextFailure(KindInvalidPayload, "work_context", "finding_refs must be an array of exact finding ids", "supply the finding_refs array")
		}
	} else {
		payload.FindingRefs = []string{}
	}
	if raw := fields["context_findings"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &payload.ContextFindings); err != nil {
			return nil, workContextFailure(KindInvalidPayload, "work_context", "context_findings must be an array of closed finding entries", "supply the context_findings array")
		}
	} else {
		payload.ContextFindings = []WorkerContextFinding{}
	}
	if raw := fields["research_finding_refs"]; len(raw) != 0 {
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload.ResearchSelections); err != nil {
			return nil, workContextFailure(KindInvalidPayload, "work_context", "research_finding_refs must be an array of closed selection entries", "supply exact pack, revision, finding, and Domain references")
		}
	}
	if err := validateWorkContextDeclarationShape(payload); err != nil {
		return nil, err
	}
	for _, reading := range payload.RequiredReading {
		if err := ValidateWorkContextDomainTx(ctx, tx, request.WorkID, reading.DomainID, reading.ProductWideRationale); err != nil {
			return nil, err
		}
	}
	for _, finding := range payload.ContextFindings {
		if err := ValidateWorkContextDomainTx(ctx, tx, request.WorkID, finding.DomainID, finding.ProductWideRationale); err != nil {
			return nil, err
		}
	}
	if err := validateWorkContextFindingRefs(ctx, tx, request.WorkID, payload.FindingRefs); err != nil {
		return nil, err
	}
	if len(payload.ResearchSelections) > 0 {
		if err := validateWorkContextResearchSelectionsTx(ctx, tx, request.WorkID, payload.ResearchSelections); err != nil {
			return nil, err
		}
	}
	var workflowRef, workflowDigestValue string
	var workflowDefinitionVersion, attemptEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&workflowRef, &workflowDefinitionVersion, &workflowDigestValue); err != nil {
		return nil, workflowProjectionError(err, "cannot read workflow identity for the work context declaration")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=?`, request.WorkID).Scan(&attemptEpoch); err != nil {
		return nil, workflowProjectionError(err, "cannot read workflow attempt epoch")
	}
	if attemptEpoch <= 0 {
		attemptEpoch = 1
	}
	values := map[string]any{
		"step_id": stepID, "attempt_epoch": attemptEpoch,
		"required_reading": payload.RequiredReading, "finding_refs": payload.FindingRefs, "context_findings": payload.ContextFindings,
		"workflow_ref": workflowRef, "workflow_definition_version": workflowDefinitionVersion, "workflow_definition_digest": workflowDigestValue,
		"actor_ref": actor, "request_id": request.RequestID,
	}
	// The selection member stays absent for declarations without research,
	// so those declaration events keep their exact published shape.
	if len(payload.ResearchSelections) > 0 {
		values["research_finding_refs"] = payload.ResearchSelections
	}
	return []Event{workflowTypedEvent(eventID, WorkflowWorkContextRecorded, request.WorkID, actor, request.Now, expected, values)}, nil
}

// foldWorkflowWorkContextRecorded is the declaration fold. It enforces the
// closed shape and the workflow bindings the replayed log carries, and
// writes no projection row: the event log is the only context authority.
// Registry state is never read here, so replaying a historical declaration
// never requires today's registry.
func foldWorkflowWorkContextRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowWorkContextRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	if err := validateWorkContextDeclarationShape(p); err != nil {
		return err
	}
	if !workflowString(p.StepID, 128) || p.AttemptEpoch <= 0 || !workflowString(p.WorkflowRef, 128) || p.WorkflowDefinitionVersion <= 0 || !workflowDigest(p.WorkflowDefinitionDigest) || !workflowString(p.ActorRef, 70) || !workflowString(p.RequestID, 128) {
		return workContextFailure(KindInvalidPayload, "fold_event", "work context declaration is incomplete or outside its bounds", "supply the complete typed declaration")
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	var currentStep, workflowRef, workflowDigestValue string
	var workflowVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT current_step,definition_ref,definition_digest,definition_version FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentStep, &workflowRef, &workflowDigestValue, &workflowVersion); err != nil {
		return workflowProjectionError(err, "cannot read workflow identity for the work context declaration")
	}
	if currentStep != p.StepID || workflowRef != p.WorkflowRef || workflowDigestValue != p.WorkflowDefinitionDigest || workflowVersion != p.WorkflowDefinitionVersion {
		return newFailure(KindStaleAttempt, "fold_event", "work context declaration does not bind the current workflow step and definition", false, "reread the current workflow context")
	}
	var activeAttempt int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=?`, event.SubjectID).Scan(&activeAttempt); err != nil {
		return workflowProjectionError(err, "cannot read workflow attempt epoch")
	}
	if activeAttempt != p.AttemptEpoch {
		return newFailure(KindStaleAttempt, "fold_event", "work context declaration does not bind the current attempt epoch", false, "reread the current workflow attempt")
	}
	return advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields)
}

// workContextFindingView derives one view finding from a stored finding and
// its source event identity.
func workContextFindingView(finding WorkerContextFinding, origin string, eventID string, eventSeq int64, ordinal int) WorkContextFindingView {
	return WorkContextFindingView{
		FindingID:            fmt.Sprintf("finding:%d:%d", eventSeq, ordinal),
		Kind:                 finding.Kind,
		Statement:            finding.Statement,
		SubjectRef:           finding.SubjectRef,
		EvidenceRefs:         finding.EvidenceRefs,
		DomainID:             finding.DomainID,
		ProductWideRationale: finding.ProductWideRationale,
		Origin:               origin,
		Status:               WorkContextFindingStatusReported,
		SourceEventID:        eventID,
		SourceEventSeq:       eventSeq,
		Ordinal:              ordinal,
	}
}

// workContextOriginForKind maps a findings-bearing event kind to the view's
// origin vocabulary.
func workContextOriginForKind(kind string) string {
	if kind == WorkflowWorkContextRecorded {
		return WorkContextOriginDeclaration
	}
	return WorkContextOriginWorkerReport
}

// readWorkContextView assembles the current work context view from the
// event log inside the caller's transaction: the latest declaration
// anchor's readings, the anchor's findings, the exact earlier findings its
// references select, and the terminal-report findings recorded after the
// anchor. The queries are bounded: the report scan reads at most 33
// findings-bearing events, and any work-wide current view past 32 finding
// slots or 64 KiB of complete view content refuses the read explicitly
// instead of truncating. A work item without any required context reads as
// nil.
func readWorkContextView(ctx context.Context, q queryer, workID string) (*WorkContextView, error) {
	view := WorkContextView{RequiredReading: []WorkContextReading{}, Findings: []WorkContextFindingView{}, DomainGroups: []WorkContextDomainGroup{}}
	subject, subjectErr := readCurrentOracleSubject(ctx, q, workID)
	if subjectErr != nil {
		return nil, subjectErr
	}
	view.SubjectCommit = subject
	preparations, prepareErr := readSelectedOraclePreparations(ctx, q, workID)
	if prepareErr != nil {
		return nil, prepareErr
	}
	view.OraclePreparations = preparations
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind IN (?,?,?)`, workID, WorkflowWorkContextRecorded, WorkerCompleted, WorkerFailed).Scan(&view.SourceEventFrontier); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot read the work context source frontier", true, "retry once the event log is readable", err)
	}
	var anchorSeq int64 = 0
	var anchor Event
	var anchorResearch []WorkContextResearchSelection
	anchorErr := q.QueryRowContext(ctx, `SELECT seq,event_id,kind,subject_type,subject_id,actor,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowWorkContextRecorded).Scan(&anchorSeq, &anchor.EventID, &anchor.Kind, &anchor.SubjectType, &anchor.SubjectID, &anchor.Actor, &anchor.Payload)
	if anchorErr != nil && anchorErr != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot read the latest work context declaration", true, "retry once the event log is readable", anchorErr)
	}
	// One lineage read feeds the whole assembly — the ranked
	// ordinals a declaration may select, the open findings the view can
	// never drop, and the retained receipts a later lane receives.
	lineage, lineageErr := readWorkerOracleFindingLineageTx(ctx, q, workID)
	if lineageErr != nil {
		return nil, lineageErr
	}
	byID := make(map[string]WorkContextFindingView)
	order := make([]string, 0, WorkContextViewFindingsMax+1)
	addFinding := func(finding WorkContextFindingView) {
		if _, exists := byID[finding.FindingID]; exists {
			return
		}
		byID[finding.FindingID] = finding
		order = append(order, finding.FindingID)
	}
	if anchorErr == nil {
		var payload workflowWorkContextRecordedPayload
		if err := json.Unmarshal(anchor.Payload, &payload); err != nil {
			return nil, workContextFailure(KindInvariantViolation, "work_context_read", "the latest work context declaration payload is malformed", "rebuild the projection from the event log")
		}
		view.RequiredReading = append(view.RequiredReading, payload.RequiredReading...)
		anchorResearch = payload.ResearchSelections
		for ordinal, finding := range payload.ContextFindings {
			addFinding(workContextFindingView(finding, WorkContextOriginDeclaration, anchor.EventID, anchorSeq, ordinal))
		}
		// Resolve the anchor's selected earlier findings through one query
		// per distinct referenced sequence, bounded by the selection bound.
		for _, ref := range payload.FindingRefs {
			seq, ordinal, ok := parseWorkContextFindingRef(ref)
			if !ok {
				return nil, workContextFailure(KindInvariantViolation, "work_context_read", "the stored declaration carries a malformed finding ref "+ref, "rebuild the projection from the event log")
			}
			var event Event
			if err := q.QueryRowContext(ctx, `SELECT event_id,kind,subject_type,subject_id,actor,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND seq=?`, workID, seq).Scan(&event.EventID, &event.Kind, &event.SubjectType, &event.SubjectID, &event.Actor, &event.Payload); err != nil {
				if err == sql.ErrNoRows {
					return nil, workContextFailure(KindInvariantViolation, "work_context_read", "the stored declaration selects finding "+ref+" whose event is absent", "rebuild the projection from the event log")
				}
				return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot resolve a selected finding", true, "retry once the event log is readable", err)
			}
			generic, ranked, decodes := workContextTerminalFindingsAtEvent(event)
			if !decodes {
				return nil, workContextFailure(KindInvariantViolation, "work_context_read", "the stored declaration selects finding "+ref+" whose source event holds no findings", "rebuild the projection from the event log")
			}
			switch {
			case ordinal < len(generic):
				addFinding(workContextFindingView(generic[ordinal], workContextOriginForKind(event.Kind), event.EventID, seq, ordinal))
			case ordinal < len(generic)+len(ranked):
				// A selected ranked finding projects with its tie and
				// source kind, never into the generic claim notebook.
				addFinding(rankedFindingView(ranked[ordinal-len(generic)], event.EventID, seq, ordinal, lineage))
			default:
				return nil, workContextFailure(KindInvariantViolation, "work_context_read", "the stored declaration selects finding "+ref+" the source event does not hold", "rebuild the projection from the event log")
			}
		}
	}
	// Terminal-report findings recorded after the anchor ride the current
	// view until a later declaration selects or drops them. The scan is
	// bounded: every matched event carries at least one finding, so a full
	// page of 33 proves the work-wide view past the 32-finding bound.
	rows, err := q.QueryContext(ctx, `SELECT seq,event_id,kind,payload FROM domain_events
		WHERE subject_type='work_item' AND subject_id=? AND kind IN (?,?) AND seq>? AND json_array_length(payload,'$.context_findings')>0
		ORDER BY seq DESC LIMIT ?`, workID, WorkerCompleted, WorkerFailed, anchorSeq, workContextReportEventScanRows)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot read terminal-report findings", true, "retry once the event log is readable", err)
	}
	type reportRow struct {
		event Event
		seq   int64
	}
	reportRows := make([]reportRow, 0, workContextReportEventScanRows)
	for rows.Next() {
		var row reportRow
		if err := rows.Scan(&row.seq, &row.event.EventID, &row.event.Kind, &row.event.Payload); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot decode a terminal-report finding event", true, "retry once the event log is readable", err)
		}
		reportRows = append(reportRows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot finish reading terminal-report findings", true, "retry once the event log is readable", err)
	}
	rows.Close()
	for i := len(reportRows) - 1; i >= 0; i-- {
		row := reportRows[i]
		findings, decodes := workContextFindingsAtEvent(row.event)
		if !decodes {
			return nil, workContextFailure(KindInvariantViolation, "work_context_read", "a stored terminal report's findings are malformed", "rebuild the projection from the event log")
		}
		for ordinal, finding := range findings {
			addFinding(workContextFindingView(finding, WorkContextOriginWorkerReport, row.event.EventID, row.seq, ordinal))
		}
	}
	// The ranked oracle findings and the retained receipts project
	// from the same one lineage the correction and convergence surfaces
	// read. Open ranked findings ride the view unconditionally: a
	// coordinator's context selection can drop generic claims, never an
	// open oracle blocker. Closed findings ride only through an explicit
	// selection above.
	projectWorkerOracleOpenFindings(lineage, addFinding)
	if len(lineage.retainedReceipts) > oracleRetainedReceiptsMax {
		return nil, oracleFindingFailure(KindLimitExceeded, fmt.Sprintf("the work retains %d oracle receipts above the %d view bound; the read refuses instead of truncating", len(lineage.retainedReceipts), oracleRetainedReceiptsMax), "supersede the active contract through its approved recovery route or stop the work; predecessor receipts remain in the event log")
	}
	if len(lineage.retainedReceipts) > 0 {
		view.OracleReceipts = lineage.retainedReceipts
	}
	// The anchor's research selections resolve in authored order through
	// the canonical selected-read helper. Unresolved entries stay visible
	// without content and still consume their shared slot, so omissions
	// cannot evade the bounds by omitting their body.
	if anchorErr == nil && len(anchorResearch) > 0 {
		research, researchErr := readWorkContextResearchSelections(ctx, q, workID, anchor.EventID, anchorSeq, anchorResearch)
		if researchErr != nil {
			return nil, researchErr
		}
		view.ResearchFindings = research
	}
	if len(order)+len(view.ResearchFindings) > WorkContextViewFindingsMax {
		return nil, workContextFailure(KindLimitExceeded, "work_context_read", fmt.Sprintf("the current work context holds %d finding slots across its generic, ranked, and research entries; the view bound is %d and the read refuses instead of truncating", len(order)+len(view.ResearchFindings), WorkContextViewFindingsMax), workContextOverflowRecovery())
	}
	for _, id := range order {
		view.Findings = append(view.Findings, byID[id])
	}
	if err := assembleWorkContextDomainGroups(ctx, q, workID, &view); err != nil {
		return nil, err
	}
	// The whole serialized view measures once, after grouping, with the
	// same Go encoding the dispatch equality owner compares: readings,
	// findings, research entries, groups, and oracle metadata share the
	// 64 KiB budget, and no second budget exists.
	viewJSON, err := json.Marshal(view)
	if err != nil {
		return nil, workContextFailure(KindInvariantViolation, "work_context_read", "the current work context cannot be measured", "rebuild the projection from the event log")
	}
	if len(viewJSON) > WorkContextCombinedMaxBytes {
		return nil, workContextFailure(KindLimitExceeded, "work_context_read",
			fmt.Sprintf("the current work context measures %d bytes of canonical JSON, above the %d-byte view bound and the read refuses instead of truncating", len(viewJSON), WorkContextCombinedMaxBytes),
			workContextOverflowRecovery())
	}
	if anchorErr == sql.ErrNoRows && len(order) == 0 && len(view.ResearchFindings) == 0 && len(view.RequiredReading) == 0 && len(view.OracleReceipts) == 0 && len(view.OraclePreparations) == 0 && view.SubjectCommit == "" {
		return nil, nil
	}
	return &view, nil
}

// WorkContextUnavailable is the typed marker a compound read serves instead
// of a view when the strict reader refuses the work-wide context on a bound.
type WorkContextUnavailable struct {
	Reason string `json:"reason"`
}

// readWorkContextProjection reads the strict work-context view exactly once
// through the caller's queryer and classifies its one closed refusal: a
// compound read must not strand on a work-wide bound, while the dedicated
// context read and dispatch admission keep the strict refusal.
func readWorkContextProjection(ctx context.Context, q queryer, workID string) (*WorkContextView, *WorkContextUnavailable, error) {
	view, err := readWorkContextView(ctx, q, workID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindLimitExceeded {
			return nil, &WorkContextUnavailable{Reason: string(KindLimitExceeded)}, nil
		}
		return nil, nil, err
	}
	return view, nil, nil
}

// checkWorkContextDeclarationTx is the one live post-append declaration
// check: it runs in the action transaction just after the action's events
// append and fold, and reads the resulting shared view through the caller's
// transaction. Any appended work-context declaration carries the read
// obligation, and any reader refusal rolls the whole action back. Replay
// folds and terminal reports never run it.
func checkWorkContextDeclarationTx(ctx context.Context, tx *sql.Tx, workID string, events []Event) error {
	for _, event := range events {
		if event.Kind == WorkflowWorkContextRecorded {
			_, err := readWorkContextView(ctx, tx, workID)
			return err
		}
	}
	return nil
}

// assembleWorkContextDomainGroups partitions the view's readings and
// findings by Domain, in the approved contract's affected-Domain order.
// Domains the affected order does not name sort after it by identity. The
// store keeps the affected list in its canonical sorted form, so that sorted
// list is the contract's affected order.
func assembleWorkContextDomainGroups(ctx context.Context, q queryer, workID string, view *WorkContextView) error {
	orderedDomains := make([]string, 0, 8)
	contractVersion, contractErr := activeWorkflowContractVersion(ctx, q, workID, "work_context_read")
	if contractErr != nil && contractErr != sql.ErrNoRows {
		return contractErr
	}
	if contractErr == nil {
		var binding int
		bindingErr := q.QueryRowContext(ctx, `SELECT 1 FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&binding)
		if bindingErr == nil {
			rows, err := q.QueryContext(ctx, `SELECT domain_id FROM workflow_contract_affected_domains WHERE work_id=? AND contract_version=? ORDER BY domain_id`, workID, contractVersion)
			if err != nil {
				return wrapFailure(KindUnavailable, "work_context_read", "cannot read the approved affected Domain order", true, "retry once the contract projection is readable", err)
			}
			for rows.Next() {
				var domainID string
				if err := rows.Scan(&domainID); err != nil {
					rows.Close()
					return wrapFailure(KindUnavailable, "work_context_read", "cannot decode the approved affected Domain order", true, "retry once the contract projection is readable", err)
				}
				orderedDomains = append(orderedDomains, domainID)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return wrapFailure(KindUnavailable, "work_context_read", "cannot finish reading the approved affected Domain order", true, "retry once the contract projection is readable", err)
			}
			rows.Close()
		} else if bindingErr != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, "work_context_read", "cannot read the contract architecture binding", true, "retry once the contract projection is readable", bindingErr)
		}
	}
	grouped := make(map[string]*WorkContextDomainGroup)
	ordered := make(map[string]bool, len(orderedDomains))
	for _, domainID := range orderedDomains {
		ordered[domainID] = true
	}
	groupFor := func(domainID string) *WorkContextDomainGroup {
		if group, exists := grouped[domainID]; exists {
			return group
		}
		group := &WorkContextDomainGroup{DomainID: domainID, RequiredReadingOrdinals: []int{}, FindingIDs: []string{}}
		grouped[domainID] = group
		if !ordered[domainID] {
			orderedDomains = append(orderedDomains, domainID)
			ordered[domainID] = true
		}
		return group
	}
	for ordinal, reading := range view.RequiredReading {
		group := groupFor(reading.DomainID)
		group.RequiredReadingOrdinals = append(group.RequiredReadingOrdinals, ordinal)
	}
	for _, finding := range view.Findings {
		groupFor(finding.DomainID).FindingIDs = append(groupFor(finding.DomainID).FindingIDs, finding.FindingID)
	}
	// Research entries join the existing Domain group's finding_ids under
	// their research view_id namespace; there is no second grouping
	// structure.
	for _, research := range view.ResearchFindings {
		groupFor(research.Selection.DomainID).FindingIDs = append(groupFor(research.Selection.DomainID).FindingIDs, research.ViewID)
	}
	for _, domainID := range orderedDomains {
		if group := grouped[domainID]; group != nil && (len(group.RequiredReadingOrdinals) > 0 || len(group.FindingIDs) > 0) {
			view.DomainGroups = append(view.DomainGroups, *group)
		}
	}
	return nil
}

func admittedWorkerPacketWorkContext(ctx context.Context, q queryer, workID string, packetRaw json.RawMessage) (*WorkContextView, error) {
	var packet struct {
		Inputs struct {
			WorkContext json.RawMessage `json:"work_context"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is malformed", false, "supply the lane packet bound to this work item and attempt")
	}
	current, err := readWorkContextView(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	present := len(packet.Inputs.WorkContext) != 0 && string(packet.Inputs.WorkContext) != "null"
	if current == nil {
		if present {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "worker packet carries work context without a current work context view", false, "build the packet from the current work pin")
		}
		return nil, nil
	}
	if !present {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "worker packet does not consume the current work context", false, "build a fresh packet from the current work pin")
	}
	decoder := json.NewDecoder(strings.NewReader(string(packet.Inputs.WorkContext)))
	decoder.DisallowUnknownFields()
	var claimed WorkContextView
	if err := decoder.Decode(&claimed); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "worker packet inputs.work_context is not one closed work-context view", false, "build a fresh packet from the current work pin")
	}
	claimedJSON, claimedErr := json.Marshal(claimed)
	currentJSON, currentErr := json.Marshal(current)
	if claimedErr != nil || currentErr != nil || !bytes.Equal(claimedJSON, currentJSON) {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "worker packet does not consume the current work context", false, "build a fresh packet from the current work pin")
	}
	// Byte equality stays necessary and becomes insufficient: an equal
	// view carrying a required unresolved research selection refuses before
	// a worker launches. The normal dispatch and the core-derived
	// reject/retry dispatch share this one admission owner.
	if err := requireResolvedResearchSelections(current); err != nil {
		return nil, err
	}
	return current, nil
}

// DispatchContextAggregateMaxBytes is the whole-dispatch-context bound: the
// canonical JSON object of exactly the packet's present work_context,
// checkpoint, and correction members may not exceed 64 KiB of UTF-8 bytes,
// member names and delimiters included. It is a dispatch capacity and owns
// its own population: the declaration combined bound, the view findings
// bound, the per-entry bounds, and the terminal findings bound protect
// different populations and stay untouched. The whole-action decode budget
// composes the packet's other declared maxima around this aggregate —
// TestWorkflowActionPayloadBudgetDerivesFromDeclaredMaxima derives that
// composition from the generated contract, so no copied packet-parts
// nominal exists.
const DispatchContextAggregateMaxBytes = 64 * 1024

// measureDispatchContextAggregateBytes measures one pure population: the
// canonical JSON object formed from exactly the packet input members whose
// keys are present — inputs.work_context, inputs.checkpoint, and
// inputs.correction — with member names and JSON delimiters included. Each
// present member is canonicalized through the shared encoder, so the count
// is UTF-8 canonical bytes, never the raw packet's whitespace and never a
// rune count. An absent key contributes no member bytes, so a packet with
// none of the three members measures the two bytes of "{}". A present key
// carrying JSON null is a present member and measures its canonical name
// and value; the closed packet schema refuses such a member, so the shape
// owner rejects it before this measurement ever runs in live admission.
func measureDispatchContextAggregateBytes(packetRaw json.RawMessage) (int, error) {
	var packet struct {
		Inputs struct {
			WorkContext json.RawMessage `json:"work_context"`
			Checkpoint  json.RawMessage `json:"checkpoint"`
			Correction  json.RawMessage `json:"correction"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return 0, workContextFailure(KindInvalidPayload, "work_context", "the worker packet does not decode as one JSON object", "supply the lane packet bound to this work item and attempt")
	}
	members := map[string]json.RawMessage{}
	for name, raw := range map[string]json.RawMessage{
		"work_context": packet.Inputs.WorkContext,
		"checkpoint":   packet.Inputs.Checkpoint,
		"correction":   packet.Inputs.Correction,
	} {
		if len(raw) == 0 {
			continue
		}
		canonical, err := canonicalJSON(raw)
		if err != nil {
			return 0, workContextFailure(KindInvalidPayload, "work_context", "the packet's "+name+" member cannot be canonicalized for measurement", "supply the closed typed member the packet schema declares")
		}
		members[name] = canonical
	}
	object, err := json.Marshal(members)
	if err != nil {
		return 0, wrapFailure(KindInvariantViolation, "work_context", "the dispatch context aggregate cannot be measured", false, "repair the packet members", err)
	}
	return len(object), nil
}

// refuseOverBoundDispatchContext refuses a dispatch whose present context
// members measure above the aggregate bound. The refusal is whole and keeps
// the whole request: no findings, preparations, checkpoint directions, or
// correction state is omitted to fit, and the error names the measured bytes
// and the bound.
func refuseOverBoundDispatchContext(packetRaw json.RawMessage) error {
	measured, err := measureDispatchContextAggregateBytes(packetRaw)
	if err != nil {
		return err
	}
	if measured <= DispatchContextAggregateMaxBytes {
		return nil
	}
	return workContextFailure(KindLimitExceeded, "work_context",
		fmt.Sprintf("the dispatch context members measure %d bytes of canonical JSON, above the %d-byte aggregate dispatch context bound; the request is refused whole and never trimmed", measured, DispatchContextAggregateMaxBytes),
		"reduce the retained work context, checkpoint, or correction through their own owners, then rebuild and dispatch a fresh packet")
}
