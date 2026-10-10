package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sharper-flow/concord/internal/hostlease"
)

const (
	WorkerDispatched = "worker.dispatched"
	WorkerCompleted  = "worker.completed"
	WorkerFailed     = "worker.failed"

	// WorkerPacketSchemaVersion and WorkerReportSchemaVersion are the
	// job-capable schema identities (CD-0205): the versions whose packets
	// and reports may carry the worker-job binding. The adapter records
	// them on every dispatch and report it builds.
	WorkerPacketSchemaVersion = "1.1"
	WorkerReportSchemaVersion = "1.1"
	// WorkerPacketSchemaVersionLegacy and WorkerReportSchemaVersionLegacy
	// are the released pre-job identities. A payload that records them
	// never carries a worker-job binding: the binding at "1.0" was the
	// in-place addition the explicit boundary replaces, so the validator
	// refuses it and the historical identities keep their released shape.
	WorkerPacketSchemaVersionLegacy = "1.0"
	WorkerReportSchemaVersionLegacy = "1.0"
)

// workerDispatchedWorkerJobVersion and workerCompletedWorkerJobVersion are
// the payload versions at which each worker event kind may first bind a
// worker job (CD-0205). A stored or supplied event whose recorded source
// version sits below the boundary cannot carry worker_job bytes: the field
// did not exist when that version was released, so any bytes that name it
// are a fabricated job disposition that no store ever recorded. The fold
// rejects them closed so the live boundary and log-ordered replay enforce
// one rule.
const (
	workerDispatchedWorkerJobVersion = 5
	workerCompletedWorkerJobVersion  = 4
)

// workerCompletedContextFindingsVersion and workerFailedContextFindingsVersion
// are the payload versions at which each terminal worker event kind may first
// carry typed context findings (CON-887). A stored or supplied event whose
// recorded source version sits below the boundary cannot carry
// context_findings bytes: the field did not exist when that version was
// released, so any bytes that name it are fabricated worker claims that no
// store ever recorded. The fold rejects them closed so the live boundary and
// log-ordered replay enforce one rule.
const (
	workerCompletedContextFindingsVersion = 5
	workerFailedContextFindingsVersion    = 2
)

// workerCompletedOracleReportVersion is the payload version at which
// worker.completed may first carry the CON-890 typed oracle members: a
// receipt on one evidence entry, an oracle tie on one review finding, or a
// closure claim in resolved_findings. A stored or supplied event whose
// recorded source version sits below the boundary cannot carry oracle
// report bytes: the members did not exist when that version was released,
// so any bytes that name them are fabricated ties no store ever recorded.
const workerCompletedOracleReportVersion = 6

// WorkerEvidenceEventPayloadVersion resolves the payload version the event
// registry currently owns for the named worker evidence kind (CD-0205), so
// callers at the emission boundary — the CLI command routes in
// cmd/concord, and the fixture helpers in this package — cannot hand-write a
// competing version that drifts from the registry. WorkerFailed is registered
// at version 2, the findings-capable payload (CON-887).
func WorkerEvidenceEventPayloadVersion(kind string) int {
	switch kind {
	case WorkerDispatched, WorkerCompleted, WorkerFailed:
	default:
		panic(fmt.Sprintf("worker evidence event %q is not recognized", kind))
	}
	reg, ok := registeredEventKind(kind)
	if !ok {
		panic(fmt.Sprintf("worker event %q is not registered", kind))
	}
	return reg.CurrentVersion
}

const (
	WorkerFailureFallbackBlocked        = "fallback_blocked"
	WorkerFailureWorkerError            = "worker_error"
	WorkerFailureInvalidReport          = "invalid_report"
	WorkerFailureAbandoned              = "abandoned"
	WorkerFailureModelIdentity          = "model_identity_mismatch"
	WorkerFailureModelReadbackMissing   = "model_readback_missing"
	WorkerFailureModelReadbackAmbiguous = "model_readback_ambiguous"
)

// Evidence origin is closed (CD-0056 D6). WorkerEvidenceReported means the
// payload carries the evidence a worker actually returned; a completion
// recorded today must use it and satisfy its lane's obligations.
// WorkerEvidenceLegacyUnavailable marks a completion that predates the
// evidence contract, so a legacy completion stays visibly legacy instead of
// being indistinguishable from one that reported nothing.
const (
	WorkerEvidenceReported          = "reported"
	WorkerEvidenceLegacyUnavailable = "legacy_unavailable"
)

// WorkerDispatchedPayload is the v3 dispatch identity. The event subject is
// the owning work item; AttemptID identifies the worker attempt. Concord
// records what executed (readback_model) and nothing else (CD-0058).
type WorkerDispatchedPayload struct {
	AttemptID           string `json:"attempt_id"`
	LaneID              string `json:"lane_id"`
	LaneVersion         int64  `json:"lane_version"`
	LaneDigest          string `json:"lane_digest"`
	CapabilityClass     string `json:"capability_class"`
	PacketSchemaVersion string `json:"packet_schema_version"`
	ReportSchemaVersion string `json:"report_schema_version"`
	// PacketDigest is the canonical lane-packet digest the dispatch
	// authorization recorded (CD-0067 D2). The dispatched event carries
	// it so the audit trail names the exact packet the worker ran, not
	// only the attempt identity. Required at the v3 evidence boundary:
	// the worker-dispatch gate refuses a payload whose digest was not
	// the value the dispatch_worker authorization recorded, and a
	// pre-CD-0067 window without a recorded digest refuses every
	// dispatch with a typed cutover failure (D6).
	PacketDigest string `json:"packet_digest"`
	// HostProvenance is the declared record of unversioned host prompt
	// surfaces that shape the worker's behavior (issue #103 / CD-0034):
	// the adapter enumerates what it can bind — agent definition file,
	// AGENTS.md chain at spawn cwd, declared instruction files — and hashes
	// them. Injection is permitted only when recorded. Nil is legal only
	// for payloads older than v3.
	HostProvenance *WorkerHostProvenance `json:"host_provenance,omitempty"`
	// ReadbackModel records the model the host reports as having executed
	// the attempt (CD-0058 D2). It is the only model evidence Concord
	// records; the adapter writes the same value on dispatch and on the
	// worker.completed / worker.failed terminal events.
	ReadbackModel string `json:"readback_model,omitempty"`
	// Terminal records the immediate terminal outcome forced at dispatch
	// (issue #106): "failed" for an undeclared executing model or an
	// exhausted resolution chain, empty for an ordinary dispatch. A
	// terminal dispatch exists so the prohibited outcome is durable
	// evidence, never a usable attempt.
	Terminal string `json:"terminal,omitempty"`
	// TerminalFailureKind / TerminalDetail carry the typed failure when
	// Terminal is set.
	TerminalFailureKind string `json:"terminal_failure_kind,omitempty"`
	TerminalDetail      string `json:"terminal_detail,omitempty"`
	// LaneActorRef names the workflow actor the dispatched lane executes as
	// (issue #800 / CD-0017 D4): the lane is the bounded execution attempt
	// of the external-effect step, recorded as a workflow actor whose tuple
	// is derived from the lane identity and the attempt. Empty only on
	// payloads that predate v4; the dispatch fold pins it as the workflow
	// instance's executing actor so the owner's accept_worker_result is
	// distinct from the party that executed.
	LaneActorRef string `json:"lane_actor_ref,omitempty"`
	// WorkerJob binds this dispatch to one immutable recorded worker-job
	// revision (CD-0205). The job identity is stable across revisions: a
	// later revision of the same job_id satisfies the same corrective
	// obligation, while a different job_id never does. Nil on every
	// dispatched payload that predates job-bound dispatch; those histories
	// keep the CD-0164 D2 reset unchanged.
	WorkerJob *WorkerJobBinding `json:"worker_job,omitempty"`
}

// WorkerJobBinding is the dispatch-side binding of one worker-job revision:
// the stable job identity, the immutable revision number, and the digest of
// the recorded revision content. The dispatch records it so an accepted
// result can satisfy only the job its attempt was dispatched under, and the
// correction window can refuse closure by an unrelated accepted job
// (CD-0205 refining CD-0164 D2).
type WorkerJobBinding struct {
	JobID    string `json:"job_id"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

// workflowDispatchedJobForAttempt reads the worker-job binding recorded on
// the attempt's worker.dispatched event. The binding is dispatch-side
// authority: acceptance-side readers derive the job from this record instead
// of trusting any caller assertion. A NULL binding — every dispatch that
// predates job-bound dispatch — returns nil, so legacy histories carry no
// disposition.
func workflowDispatchedJobForAttempt(ctx context.Context, q queryer, workID, attemptID string) (*WorkerJobBinding, error) {
	if attemptID == "" {
		return nil, nil
	}
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.worker_job'),'') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkerDispatched, attemptID).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot read the dispatched worker-job binding", true, "retry once the workflow event log is readable", err)
	}
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var job WorkerJobBinding
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		return nil, wrapFailure(KindInvalidPayload, "worker_job", "the dispatched worker-job binding is not a valid binding object", false, "redispatch the attempt under a recorded worker-job revision", err)
	}
	if job.JobID == "" || job.Revision < 1 || job.Digest == "" {
		return nil, wrapFailure(KindInvalidPayload, "worker_job", "the dispatched worker-job binding is missing job_id, revision, or digest", false, "redispatch the attempt under a recorded worker-job revision", nil)
	}
	return &job, nil
}

// WorkerReportEvidence is one discharged lane evidence obligation as the
// worker reported it (CD-0056 D1). Obligation is drawn from the closed
// vocabulary in agent_lanes.go; Detail is recorded as reported and is never
// summarized, scored, or rewritten. PredicateIDs is the optional per-predicate
// tie: the predicate_id of each typed inputs.outcome_predicates entry this
// entry's evidence discharges. OracleReceipt is the optional typed
// control-execution receipt (CON-890): reported evidence only, never
// native-run authority, and bound to the dispatched job's oracle by the fold.
type WorkerReportEvidence struct {
	Obligation    string               `json:"obligation"`
	Detail        string               `json:"detail"`
	PredicateIDs  []string             `json:"predicate_ids,omitempty"`
	OracleReceipt *WorkerOracleReceipt `json:"oracle_receipt,omitempty"`
}

// WorkerBaseComparisonCheck is one verification command's result pair as the
// worker reported it: how the same command behaved on the branch and on the
// base. It is informational evidence only (CD-0043 D1): the verifier method
// is host-owned, it joins no obligation vocabulary, and no workflow guard
// reads it.
type WorkerBaseComparisonCheck struct {
	Command      string `json:"command"`
	BranchResult string `json:"branch_result"`
	BaseResult   string `json:"base_result"`
}

// WorkerBaseComparison is the optional top-level base_comparison object of
// agent-lane-report.v1. It rides the completion payload so a reported
// comparison survives the worker session, and its presence changes nothing
// about routing or obligation coverage.
type WorkerBaseComparison struct {
	Checks []WorkerBaseComparisonCheck `json:"checks"`
}

// WorkerReviewFinding is one typed review finding as the worker reported it
// (CD-0197): a severity from the closed P0-P3 scale, a confidence from the
// closed low/medium/high scale, and the bounded detail. Recorded as reported
// and never rescored. Oracle is the optional typed oracle tie (CON-890):
// required on every finding of an oracle-bound dispatch, where its
// classification is a dimension beside severity, never a replacement for it.
type WorkerReviewFinding struct {
	Severity   string               `json:"severity"`
	Confidence string               `json:"confidence"`
	Detail     string               `json:"detail"`
	Oracle     *WorkerOracleFinding `json:"oracle,omitempty"`
}

// WorkerReviewBlock is the typed review block of agent-lane-report.v1: the
// lane's explicit ship or no_ship verdict, its findings, and the optional
// evidenced closure claims of previously open ranked findings (CON-890). It
// is report content only (CD-0197): it maps to no workflow field and records
// no transition, and the coordinator records the workflow verdict through
// record_verdict.
type WorkerReviewBlock struct {
	Verdict          string                   `json:"verdict"`
	Findings         []WorkerReviewFinding    `json:"findings"`
	ResolvedFindings []WorkerOracleResolution `json:"resolved_findings,omitempty"`
}

type WorkerCompletedPayload struct {
	AttemptID           string `json:"attempt_id"`
	ReadbackModel       string `json:"readback_model"`
	ReportSchemaVersion string `json:"report_schema_version"`
	// WorkerDirectory is the directory the host session reported at completion.
	// An empty value preserves compatibility with adapters that predate this
	// boundary.
	WorkerDirectory string `json:"worker_directory,omitempty"`
	// Evidence is the reported discharge of the dispatching lane's declared
	// obligations. It is empty exactly when EvidenceOrigin is
	// legacy_unavailable.
	Evidence []WorkerReportEvidence `json:"evidence,omitempty"`
	// EvidenceOrigin is always present on a v2 payload: an absent origin
	// would let one shape mean both "reported nothing" and "predates the
	// contract".
	EvidenceOrigin string `json:"evidence_origin"`
	// BaseComparison is the worker's optional reported comparison between
	// branch and base results. Absent on payloads that predate the field;
	// present or absent never changes obligation coverage or routing.
	BaseComparison *WorkerBaseComparison `json:"base_comparison,omitempty"`
	// Review is the typed review block the report carried (CD-0197). Absent
	// on payloads that predate the field; the per-lane requirement to carry
	// it is enforced in the fold against the dispatching lane, live only, so
	// stored completions replay unchanged.
	Review *WorkerReviewBlock `json:"review,omitempty"`
	// WorkerJob is the worker-job revision the report claims to complete
	// (CD-0205). The fold requires it to equal the revision the attempt was
	// dispatched under, and requires its absence when the dispatch bound no
	// job, so a report can claim neither another job nor a later revision.
	WorkerJob *WorkerJobBinding `json:"worker_job,omitempty"`
	// ContextFindings is the optional typed context content the worker
	// reported on its terminal report (CON-887). A finding is worker-claimed
	// content only: it joins no obligation vocabulary, discharges no
	// predicate, and records no verdict. subject_ref is the worker's claim
	// about what a finding concerns, never dispatch-owned subject identity.
	// Nil (absent) and empty are both legal; an over-bound array is refused
	// whole, never truncated.
	ContextFindings []WorkerContextFinding `json:"context_findings,omitempty"`
}

type WorkerFailedPayload struct {
	AttemptID     string `json:"attempt_id"`
	ReadbackModel string `json:"readback_model"`
	FailureKind   string `json:"failure_kind"`
	Detail        string `json:"detail"`
	// ContextFindings is the optional typed context content the worker
	// reported on its terminal failure (CON-887). Only the worker-reported
	// failure kind worker_error may retain findings: the diagnostic and host
	// failure kinds — invalid_report, fallback_blocked, model identity and
	// readback failures, and abandonment — carry no worker claims. Nil
	// (absent) and empty are both legal on every kind.
	ContextFindings []WorkerContextFinding `json:"context_findings,omitempty"`
}

// WorkerHostProvenance is the typed record of host prompt-injection surfaces
// present at dispatch (CD-0034: declared). TotalDigest binds the ordered
// manifest; Sources name each enumerated surface.
type WorkerHostProvenance struct {
	Digest  string                       `json:"digest"`
	Sources []WorkerHostProvenanceSource `json:"sources"`
}

// WorkerHostProvenanceSource names one enumerated injection surface. Kind is
// closed; Path is host-relative or absolute; SHA256 is the file's content
// hash. Unenumerated surfaces may carry a descriptive path or name, but no
// content hash.
type WorkerHostProvenanceSource struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// WorkerAttempt is the fold-only current projection of one worker attempt.
// It intentionally contains no workflow step, verdict, or completion state.
// The declared-side routing columns were dropped under CD-0058.
type WorkerAttempt struct {
	WorkID              string `json:"work_id"`
	AttemptID           string `json:"attempt_id"`
	LaneID              string `json:"lane_id"`
	LaneVersion         int64  `json:"lane_version"`
	LaneDigest          string `json:"lane_digest"`
	CapabilityClass     string `json:"capability_class"`
	ReadbackModel       string `json:"readback_model"`
	PacketSchemaVersion string `json:"packet_schema_version"`
	ReportSchemaVersion string `json:"report_schema_version"`
	LifecycleState      string `json:"lifecycle_state"`
	FailureKind         string `json:"failure_kind,omitempty"`
	FailureDetail       string `json:"failure_detail,omitempty"`
	DispatchedAt        string `json:"dispatched_at"`
	CompletedAt         string `json:"completed_at,omitempty"`
	FailedAt            string `json:"failed_at,omitempty"`
}

// workerModelPattern admits a lowercase provider prefix followed by one or
// more model path segments, because hosted model identifiers arrive as
// provider/sub-family/model (for example commandcode/z-ai/glm-5.3-flash).
// Whitespace and a trailing slash stay refused.
var workerModelPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*(/[a-zA-Z0-9][a-zA-Z0-9._-]*)+$`)
var workerVersionPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

func validateWorkerDispatchedPayload(event Event, payload WorkerDispatchedPayload) error {
	return validateWorkerDispatched(event, payload)
}

func validateWorkerCompletedPayload(_ Event, payload WorkerCompletedPayload) error {
	if payload.AttemptID == "" || !workerModelPattern.MatchString(payload.ReadbackModel) {
		return invalidWorkerPayload("worker.completed payload has invalid identity or report schema")
	}
	if payload.ReportSchemaVersion != WorkerReportSchemaVersion && payload.ReportSchemaVersion != WorkerReportSchemaVersionLegacy {
		return invalidWorkerPayload("worker.completed payload has invalid identity or report schema")
	}
	if payload.WorkerJob != nil && payload.ReportSchemaVersion != WorkerReportSchemaVersion {
		return invalidWorkerPayload("worker.completed worker_job requires the job-capable report schema " + WorkerReportSchemaVersion)
	}
	if err := validateWorkerBaseComparison(payload.BaseComparison); err != nil {
		return err
	}
	if err := validateWorkerReviewBlock(payload.Review); err != nil {
		return err
	}
	if err := ValidateWorkerContextFindings(payload.ContextFindings); err != nil {
		return err
	}
	if err := ValidateWorkerOracleReportShape(payload); err != nil {
		return err
	}
	return validateWorkerReportEvidence(payload.EvidenceOrigin, payload.Evidence)
}

// workerSchemaVersionFault names the boundary violation of one dispatch's
// recorded packet/report schema identities against its worker-job binding
// (CD-0205): the released "1.0" identities never carry a job, the job-capable
// "1.1" identities may, and a dispatch records one matched pair. An empty
// return means the coupling holds.
func workerSchemaVersionFault(packetVersion, reportVersion string, jobBound bool) string {
	current := packetVersion == WorkerPacketSchemaVersion && reportVersion == WorkerReportSchemaVersion
	legacy := packetVersion == WorkerPacketSchemaVersionLegacy && reportVersion == WorkerReportSchemaVersionLegacy
	switch {
	case !current && !legacy:
		return "packet and report schema versions must be the matched pair " + WorkerPacketSchemaVersion + " or " + WorkerPacketSchemaVersionLegacy
	case jobBound && !current:
		return "worker_job requires the job-capable packet and report schema " + WorkerPacketSchemaVersion
	}
	return ""
}

// workerComparisonResultVocabulary is the closed result set one
// base_comparison check may report for either side.
var workerComparisonResultVocabulary = map[string]bool{"pass": true, "fail": true, "not_run": true}

// validateWorkerBaseComparison mirrors the closed shape the report schema
// gives the optional base_comparison object: a checks array of at most 64
// entries, each naming a command of 1 to 512 bytes and two results from the
// closed set. An empty array records that the worker compared no checks; a
// missing or null array is not the schema's shape. Like the evidence entries,
// the content is recorded as reported and never judged.
func validateWorkerBaseComparison(comparison *WorkerBaseComparison) error {
	if comparison == nil {
		return nil
	}
	if comparison.Checks == nil {
		return invalidWorkerPayload("worker.completed base_comparison must carry a checks array")
	}
	if len(comparison.Checks) > 64 {
		return invalidWorkerPayload("worker.completed base_comparison must carry at most 64 checks")
	}
	for _, check := range comparison.Checks {
		if len(check.Command) < 1 || len(check.Command) > 512 {
			return invalidWorkerPayload("worker.completed base_comparison command must be between 1 and 512 bytes")
		}
		if !workerComparisonResultVocabulary[check.BranchResult] || !workerComparisonResultVocabulary[check.BaseResult] {
			return invalidWorkerPayload("worker.completed base_comparison results must be pass, fail, or not_run")
		}
	}
	return nil
}

// workerReviewVerdictVocabulary, workerReviewSeverityVocabulary, and
// workerReviewConfidenceVocabulary are the closed sets the review block's
// fields draw from (CD-0197), mirroring the report schema's enums.
var (
	workerReviewVerdictVocabulary     = map[string]bool{"ship": true, "no_ship": true}
	workerReviewSeverityVocabulary    = map[string]bool{"P0": true, "P1": true, "P2": true, "P3": true}
	workerReviewConfidenceVocabulary  = map[string]bool{"low": true, "medium": true, "high": true}
	workerReviewShipBlockerSeverities = map[string]bool{"P0": true}
)

// validateWorkerReviewBlock mirrors the closed shape the report schema gives
// the optional review object, including the two verdict couplings: a P0 is by
// definition a ship blocker, and a no_ship with no finding is an unexplained
// verdict. An absent block is legal here because whether the dispatching lane
// requires the block is per-lane, and the validator cannot reach the lane;
// that requirement is enforced in the fold.
func validateWorkerReviewBlock(review *WorkerReviewBlock) error {
	if review == nil {
		return nil
	}
	if !workerReviewVerdictVocabulary[review.Verdict] {
		return invalidWorkerPayload("worker.completed review verdict must be ship or no_ship")
	}
	if review.Findings == nil {
		return invalidWorkerPayload("worker.completed review must carry a findings array")
	}
	if len(review.Findings) > 64 {
		return invalidWorkerPayload("worker.completed review must carry at most 64 findings")
	}
	if review.Verdict == "no_ship" && len(review.Findings) == 0 {
		return invalidWorkerPayload("worker.completed review with a no_ship verdict must carry at least one finding")
	}
	for _, finding := range review.Findings {
		if !workerReviewSeverityVocabulary[finding.Severity] {
			return invalidWorkerPayload("worker.completed review finding severity must be P0, P1, P2, or P3")
		}
		if !workerReviewConfidenceVocabulary[finding.Confidence] {
			return invalidWorkerPayload("worker.completed review finding confidence must be low, medium, or high")
		}
		if len(finding.Detail) < 1 || len(finding.Detail) > 512 {
			return invalidWorkerPayload("worker.completed review finding detail must be between 1 and 512 UTF-8 bytes")
		}
	}
	if review.Verdict == "ship" {
		for _, finding := range review.Findings {
			if workerReviewShipBlockerSeverities[finding.Severity] {
				return invalidWorkerPayload("worker.completed review with a ship verdict cannot carry a P0 finding")
			}
		}
	}
	// CON-890: severity and classification are different dimensions, and
	// both couplings hold on any report whose findings carry oracle ties —
	// exactly the oracle-bound dispatches, where every finding must. A ship
	// never carries a classified blocker; a no_ship justified only by
	// sub-P0 follow-ups is an inconsistent verdict, while an out-of-scope
	// P0 follow-up stays a valid retained no_ship for the decision owner.
	oracleFindings := false
	for _, finding := range review.Findings {
		if finding.Oracle != nil {
			oracleFindings = true
		}
	}
	if oracleFindings {
		if review.Verdict == "ship" {
			for _, finding := range review.Findings {
				if finding.Oracle != nil && oracleReviewBlockerClasses[finding.Oracle.Classification] {
					return invalidWorkerPayload("worker.completed review with a ship verdict cannot carry a classified delivery blocker, uncovered case, or oracle defect")
				}
			}
		}
		if review.Verdict == "no_ship" {
			onlySubP0FollowUps := true
			for _, finding := range review.Findings {
				if finding.Oracle == nil || finding.Oracle.Classification != OracleClassificationFollowUp || finding.Severity == "P0" {
					onlySubP0FollowUps = false
				}
			}
			if onlySubP0FollowUps {
				return invalidWorkerPayload("worker.completed review with a no_ship verdict cannot be justified only by follow-ups below P0; an out-of-scope P0 follow-up stays a valid retained no_ship")
			}
		}
	}
	return nil
}

// validateWorkerReportEvidence is the shape half of the CD-0056 evidence
// contract. The validator receives the event but not the attempt, so it cannot
// reach the dispatching lane: obligation coverage is enforced in the fold.
func validateWorkerReportEvidence(origin string, evidence []WorkerReportEvidence) error {
	switch origin {
	case WorkerEvidenceLegacyUnavailable:
		if len(evidence) != 0 {
			return invalidWorkerPayload("worker.completed evidence_origin legacy_unavailable cannot carry reported evidence")
		}
		return nil
	case WorkerEvidenceReported:
	default:
		return invalidWorkerPayload("worker.completed evidence_origin must be reported or legacy_unavailable")
	}
	if len(evidence) < 1 || len(evidence) > 64 {
		return invalidWorkerPayload("worker.completed evidence_origin reported requires between 1 and 64 evidence entries")
	}
	// A lane may discharge one obligation with several distinct facts, so
	// only an identical (obligation, detail) pair is a duplicate. The
	// predicate_ids tie is not part of the duplicate key: the same fact may
	// legitimately name its predicates twice across differently-bounded
	// entries.
	type evidenceKey struct {
		obligation string
		detail     string
	}
	seen := make(map[evidenceKey]struct{}, len(evidence))
	for _, entry := range evidence {
		if !ValidLaneEvidenceObligation(entry.Obligation) {
			return invalidWorkerPayload("worker.completed evidence names an obligation outside the closed lane evidence vocabulary")
		}
		if len(entry.Detail) < 1 || len(entry.Detail) > 512 {
			return invalidWorkerPayload("worker.completed evidence detail must be between 1 and 512 UTF-8 bytes")
		}
		if len(entry.PredicateIDs) > 8 {
			return invalidWorkerPayload("worker.completed evidence entry carries more than 8 predicate ids")
		}
		for _, id := range entry.PredicateIDs {
			if !validWorkerPredicateID(id) {
				return invalidWorkerPayload("worker.completed evidence names a predicate id outside the bounded predicate: prefixed shape")
			}
		}
		key := evidenceKey{obligation: entry.Obligation, detail: entry.Detail}
		if _, exists := seen[key]; exists {
			return invalidWorkerPayload("worker.completed evidence repeats an identical obligation and detail pair")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// validWorkerPredicateID mirrors the predicate id rule the approved contract
// predicates follow (workflow.go): a bounded 11-128 character id with the
// "predicate:" prefix.
func validWorkerPredicateID(id string) bool {
	return len(id) >= 11 && len(id) <= 128 && strings.HasPrefix(id, "predicate:")
}

// workerPacketPredicateIDs extracts the typed outcome predicate ids from the
// dispatch packet the dispatch_worker action authorizes. The payload schema
// preflight has already admitted the packet shape; this read re-checks the
// identity rules the fold's discharge requirement rests on — bounded
// predicate-prefixed ids, position ordinals, and a decodable strict payload —
// so a malformed set refuses the action instead of recording obligations the
// fold cannot bind. A packet without the typed field is a legacy shape and
// records no predicate ids.
func workerPacketPredicateIDs(packetRaw json.RawMessage) ([]string, error) {
	var packet struct {
		Inputs struct {
			OutcomePredicates []struct {
				PredicateID    string          `json:"predicate_id"`
				Ordinal        int             `json:"ordinal"`
				OutcomeKind    string          `json:"outcome_kind"`
				OutcomePayload json.RawMessage `json:"outcome_payload"`
			} `json:"outcome_predicates"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is malformed", false, "supply the lane packet bound to this work item and attempt")
	}
	predicates := packet.Inputs.OutcomePredicates
	if len(predicates) == 0 {
		return nil, nil
	}
	if len(predicates) > 8 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet carries more than 8 outcome predicates", false, "supply at most 8 outcome predicates")
	}
	ids := make([]string, 0, len(predicates))
	seen := make(map[string]struct{}, len(predicates))
	for index, predicate := range predicates {
		id := predicate.PredicateID
		switch {
		case !validWorkerPredicateID(id):
			return nil, newFailure(KindInvalidPayload, "workflow_action",
				fmt.Sprintf("dispatch_worker worker_packet predicate_id %q is not a bounded predicate: prefixed id", id), false,
				"prefix every predicate_id with \"predicate:\" at 11-128 characters")
		case predicate.Ordinal != index:
			return nil, newFailure(KindInvalidPayload, "workflow_action",
				fmt.Sprintf("outcome predicate %q declares ordinal %d at position %d", id, predicate.Ordinal, index), false,
				"set each predicate ordinal to its own position in the set")
		case len(predicate.OutcomePayload) == 0:
			return nil, newFailure(KindInvalidPayload, "workflow_action",
				fmt.Sprintf("outcome predicate %q has no outcome_payload", id), false,
				"supply the outcome_payload matching the declared outcome_kind")
		}
		if _, exists := seen[id]; exists {
			return nil, newFailure(KindInvalidPayload, "workflow_action",
				fmt.Sprintf("outcome predicate id %q appears more than once", id), false,
				"give every outcome predicate a distinct predicate_id")
		}
		seen[id] = struct{}{}
		if _, err := DecodeWorkflowPredicate(predicate.OutcomePayload); err != nil {
			return nil, newFailure(KindInvalidPayload, "workflow_action",
				fmt.Sprintf("dispatch_worker worker_packet outcome predicate %q carries a malformed outcome_payload: %s", id, err), false,
				"supply one strict closed predicate payload per outcome_kind")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func validateWorkerFailedPayload(_ Event, payload WorkerFailedPayload) error {
	readbackValid := workerModelPattern.MatchString(payload.ReadbackModel) || payload.ReadbackModel == "" && (modelReadbackFailureKind(payload.FailureKind) || payload.FailureKind == WorkerFailureAbandoned)
	if payload.AttemptID == "" || !readbackValid || !validWorkerFailureKind(payload.FailureKind) || len(payload.Detail) < 1 || len(payload.Detail) > 4096 {
		return invalidWorkerPayload("worker.failed payload has invalid identity or failure")
	}
	// CON-887: findings are worker-reported claims, so only the
	// worker-reported failure kind retains them. The diagnostic and host
	// failure kinds close attempts the worker's report never validly
	// shaped, and their payloads carry no worker claims. An absent or empty
	// array stays legal on every kind.
	if len(payload.ContextFindings) > 0 && payload.FailureKind != WorkerFailureWorkerError {
		return invalidWorkerPayload("worker.failed context_findings are reserved for the worker_error failure kind")
	}
	return ValidateWorkerContextFindings(payload.ContextFindings)
}

func decodeClosedWorkerPayload(event Event, target any) error {
	return decodeRegisteredPayload(event, target)
}

func validateWorkerDispatched(event Event, payload WorkerDispatchedPayload) error {
	if event.SubjectType != SubjectWorkItem || event.SubjectID == "" || payload.AttemptID == "" || !laneIDPattern.MatchString(payload.LaneID) || payload.LaneVersion < 1 || !laneDigestPattern.MatchString(payload.LaneDigest) || !workerVersionPattern.MatchString(payload.CapabilityClass) {
		return invalidWorkerPayload("worker.dispatched payload has invalid identity")
	}
	if fault := workerSchemaVersionFault(payload.PacketSchemaVersion, payload.ReportSchemaVersion, payload.WorkerJob != nil); fault != "" {
		return invalidWorkerPayload("worker.dispatched payload has invalid identity: " + fault)
	}
	// CD-0067 D6: the dispatched event carries the packet digest the
	// authorization recorded, so a forged dispatch that swaps the
	// digest cannot land as evidence. v3 is the cutover; payloads
	// carried over by v1/v2 upcasters are not legal here because the
	// register boundary runs at append time after upcasting.
	if !workerProvenancePattern.MatchString(payload.PacketDigest) {
		return invalidWorkerPayload("worker.dispatched packet_digest must be a sha256 digest")
	}
	if payload.ReadbackModel != "" && !workerModelPattern.MatchString(payload.ReadbackModel) {
		return invalidWorkerPayload("worker.dispatched readback_model has invalid shape")
	}
	// Issue #800 / CD-0017 D4: when the dispatch names the lane's executing
	// actor it must name a well-formed actor ref. Empty is the pre-v4
	// legacy shape and stays legal so replayed history folds unchanged.
	if payload.LaneActorRef != "" && !workflowActorRefPattern.MatchString(payload.LaneActorRef) {
		return invalidWorkerPayload("worker.dispatched lane_actor_ref must be an actor ref")
	}
	lane, err := LookupLane(payload.LaneID, payload.LaneVersion, payload.LaneDigest)
	if err != nil {
		return err
	}
	if payload.CapabilityClass != lane.CapabilityClass {
		return invalidWorkerPayload("worker.dispatched capability class does not match lane")
	}
	if payload.Terminal != "" && payload.Terminal != "failed" {
		return invalidWorkerPayload("worker.dispatched terminal value must be empty or 'failed'")
	}
	if payload.Terminal == "failed" && !validWorkerFailureKind(payload.TerminalFailureKind) {
		return invalidWorkerPayload("worker.dispatched terminal failure requires a typed kind")
	}
	if payload.Terminal == "failed" && modelReadbackFailureKind(payload.TerminalFailureKind) && payload.ReadbackModel != "" {
		return invalidWorkerPayload("worker.dispatched readback failure cannot carry a readback_model")
	}
	return ValidateWorkerHostProvenance(payload.HostProvenance)
}

// WorkerDispatchWindow is the durable authorization a registered dispatch_worker
// action opens for a single worker attempt. CD-0059 D5 makes the worker-dispatch
// evidence boundary refuse if no such window exists or the same window has
// already been consumed. CD-0067 D2 carries the canonical lane-packet digest
// alongside the bound attempt_id, and CD-0067 D6 makes ValidateWorkerDispatchWindow
// compare that digest against the value the dispatch evidence claims; a window
// whose recorded digest is empty predates the boundary and refuses with a typed
// cutover failure so the operator opens a fresh authorization.
type WorkerDispatchWindow struct {
	WorkID           string
	StepID           string
	AttemptID        string
	AttemptEpoch     int64
	StartSeq         int64
	PacketDigest     string
	SubjectCommit    string
	WorktreeIdentity string
}

// FindAuthorizedDispatchWindowTx reads the dispatch_worker authorization
// bound to attemptID inside the caller's transaction. The window is the
// attempt's own, not the latest window at a step: concurrent coordinators
// may hold concurrent authorizations at one shared step, and a worker's
// evidence must validate against the authorization opened for it, whichever
// authorization a later coordinator opened since. Refusal modes return
// KindUnauthorizedDispatch so the caller can surface a typed failure without
// re-classifying.
//
// CD-0067 D6: the read surfaces PacketDigest as an empty string for any
// completion that did not record worker_packet_digest; the gate owns the
// empty-versus-non-empty refusal so this read does not double-classify.
func FindAuthorizedDispatchWindowTx(ctx context.Context, tx *sql.Tx, workID, attemptID string) (WorkerDispatchWindow, error) {
	var window WorkerDispatchWindow
	window.WorkID = workID
	window.AttemptID = attemptID
	if err := tx.QueryRowContext(ctx, `SELECT seq,json_extract(payload,'$.step_id'),COALESCE(json_extract(payload,'$.attempt_epoch'),0),COALESCE(json_extract(payload,'$.worker_packet_digest'),''),COALESCE(json_extract(payload,'$.worker_worktree_identity'),''),COALESCE(json_extract(payload,'$.worker_subject_commit'),'') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, "dispatch_worker", attemptID).Scan(&window.StartSeq, &window.StepID, &window.AttemptEpoch, &window.PacketDigest, &window.WorktreeIdentity, &window.SubjectCommit); err != nil {
		if err == sql.ErrNoRows {
			return window, newFailure(KindUnauthorizedDispatch, "worker_dispatch_window", "no authorized dispatch window exists for this work item bound to this attempt", false, "open a dispatch_worker authorization for this attempt before recording worker evidence")
		}
		return window, wrapFailure(KindUnavailable, "worker_dispatch_window", "cannot read the dispatch authorization window", true, "retry once the database is readable", err)
	}
	// The start anchors the single-use consumption read and carries the step
	// epoch the completion authorized against.
	if err := tx.QueryRowContext(ctx, `SELECT seq,COALESCE(json_extract(payload,'$.attempt_epoch'),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? AND json_extract(payload,'$.step_id')=? AND seq<? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionStarted, "dispatch_worker", window.StepID, window.StartSeq).Scan(&window.StartSeq, &window.AttemptEpoch); err != nil {
		if err == sql.ErrNoRows {
			return window, newFailure(KindInvariantViolation, "worker_dispatch_window", "dispatch_worker completed without a starting authorization event", false, "reopen the workflow action against a fresh step epoch")
		}
		return window, wrapFailure(KindUnavailable, "worker_dispatch_window", "cannot read the dispatch authorization start", true, "retry once the database is readable", err)
	}
	if window.SubjectCommit != "" && !worktreeSHAPattern.MatchString(window.SubjectCommit) {
		return window, newFailure(KindInvalidPayload, "worker_dispatch_window", "worker_subject_commit must be one raw commit OID", false, "reconcile the recorded authorization")
	}
	return window, nil
}

// WorkerDispatchWindowIsOpenTx reports whether the window has already been
// consumed by a recorded worker.dispatched event for the bound attempt_id.
// The check is single-use: one authorization admits exactly one attempt.
func WorkerDispatchWindowIsOpenTx(ctx context.Context, tx *sql.Tx, window WorkerDispatchWindow) (bool, error) {
	var consumed int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? AND seq > ?`, string(SubjectWorkItem), window.WorkID, WorkerDispatched, window.AttemptID, window.StartSeq).Scan(&consumed); err != nil {
		return false, wrapFailure(KindUnavailable, "worker_dispatch_window", "cannot inspect the dispatch window consumption", true, "retry once the database is readable", err)
	}
	return consumed == 0, nil
}

// ValidateWorkerDispatchWindow is the gate the worker-dispatch CLI runs
// inside its authenticating transaction. It refuses if the work item has no
// workflow instance, if no authorized window is bound to the claimed
// attempt, if the window belongs to a step other than the one the caller
// named, if the binding packet digest disagrees with the recorded digest (or
// the window predates the digest), or if the window has already been
// consumed. The lookup is the attempt match: the window is the one the
// dispatch_worker completion bound to this exact attempt, so a concurrent
// coordinator's later window at the same step can never shadow it.
//
// CD-0059 D5 narrows the gate to work items that have already entered a
// workflow: a worker attempt belongs to a work item a workflow is executing,
// and a work item without a workflow instance is not in a state where
// dispatch is legal. The refusal names the missing instance so an operator
// can tell it apart from a missing authorization at an existing step.
//
// CD-0067 D6: packetDigest is the value the dispatch assertion quotes; the
// gate compares it against the digest the dispatch_worker completion recorded.
// An empty recorded digest means the authorization predates the boundary and
// the operator must open a fresh authorization; a non-empty mismatch means
// the worker is trying to dispatch against a packet the core did not
// authorize. Both refuse closed.
//
// Pass stepID="" to fence the validation to the attempt's own window alone;
// the CLI uses that path because it has no independent step knowledge, and a
// live attempt's late evidence must land even after the shared step moved.
// A non-empty stepID additionally pins the validation to that step: evidence
// validated against step X refuses when the attempt's window was opened at a
// different step, so one window can never be spent across steps. Empty
// workID or attemptID is rejected as malformed.
func ValidateWorkerDispatchWindow(ctx context.Context, transaction *Transaction, workID, stepID, attemptID, packetDigest string) error {
	if workID == "" {
		return newFailure(KindInvalidPayload, "worker_dispatch_window", "work_id is required for dispatch window validation", false, "supply the work item that the worker attempt belongs to")
	}
	if attemptID == "" {
		return newFailure(KindInvalidPayload, "worker_dispatch_window", "attempt_id is required for dispatch window validation", false, "supply the worker attempt identity the dispatch authorizes")
	}
	if packetDigest == "" {
		return newFailure(KindInvalidPayload, "worker_dispatch_window", "packet_digest is required for dispatch window validation", false, "supply the packet digest the dispatch authorization recorded")
	}
	tx, err := transactionSQL(transaction, "worker_dispatch_window")
	if err != nil {
		return err
	}
	if err := requireNoOutsideRepairTx(ctx, tx, workID, "worker_dispatch_window"); err != nil {
		return err
	}
	window, err := FindAuthorizedDispatchWindowTx(ctx, tx, workID, attemptID)
	if err != nil {
		// CD-0059 D5: on the CLI path (no explicit step) a work item with
		// no workflow instance keeps its distinct refusal, so an operator
		// can tell the missing surface apart from a missing authorization.
		if stepID == "" {
			var failure *Failure
			if errors.As(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
				var instances int
				if lookErr := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=?)`, workID).Scan(&instances); lookErr == nil && instances == 0 {
					return newFailure(KindUnauthorizedDispatch, "worker_dispatch_window", "no workflow instance exists for this work item, so dispatch is not in an authorized surface", false, "drive the work item into a registered workflow before dispatching a worker")
				}
			}
		}
		return err
	}
	if stepID != "" && window.StepID != stepID {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch_window", "the attempt's authorized dispatch window belongs to a different step", false, "validate the worker evidence against the step that authorized it")
	}
	if window.PacketDigest == "" {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch_window", "dispatch window predates packet binding (CD-0067)", false, "open a fresh dispatch_worker authorization for a new attempt")
	}
	if window.PacketDigest != packetDigest {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch_window", "worker packet digest does not match the authorized dispatch window", false, "open a dispatch_worker authorization for this packet or dispatch the bound packet")
	}
	if window.WorktreeIdentity == "" {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch_window", "dispatch window predates worktree binding", false, "open a fresh dispatch_worker authorization from the claimed worktree")
	}
	if err := validateWorkerDispatchWorktreeIdentity(ctx, tx, workID, window.WorktreeIdentity); err != nil {
		return err
	}
	open, err := WorkerDispatchWindowIsOpenTx(ctx, tx, window)
	if err != nil {
		return err
	}
	if !open {
		return newFailure(KindUnauthorizedDispatch, "worker_dispatch_window", "dispatch window has already been consumed by a recorded attempt", false, "open a fresh dispatch_worker authorization for a new attempt")
	}
	return nil
}

// workerProvenancePattern binds the total digest and per-source hashes.
var workerProvenancePattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var workerProvenanceKinds = map[string]bool{
	"agent_definition": true, "agents_md": true, "instruction_file": true, "unenumerated": true,
}

// ValidateWorkerHostProvenance enforces the CD-0034 declared rule at the
// evidence boundary: when present the provenance must be complete and
// closed. Payload version 3 requires it; that gate is the emitter's
// contract, pinned by the adapter's own tests.
func ValidateWorkerHostProvenance(p *WorkerHostProvenance) error {
	if p == nil {
		return nil
	}
	if !workerProvenancePattern.MatchString(p.Digest) || len(p.Sources) < 1 || len(p.Sources) > 32 {
		return invalidWorkerPayload("worker host provenance has invalid digest or source bound")
	}
	seen := map[string]bool{}
	for _, source := range p.Sources {
		if !workerProvenanceKinds[source.Kind] || len(source.Path) > 512 {
			return invalidWorkerPayload("worker host provenance source has an unknown kind or oversized path")
		}
		if source.SHA256 != "" && !workerProvenancePattern.MatchString(source.SHA256) {
			return invalidWorkerPayload("worker host provenance source hash is not a sha256 digest")
		}
		if source.Kind == "unenumerated" && source.SHA256 != "" {
			return invalidWorkerPayload("unenumerated provenance sources carry no content hash")
		}
		if source.Kind != "unenumerated" && source.SHA256 == "" {
			return invalidWorkerPayload("enumerated provenance sources must carry their content hash")
		}
		key := source.Kind + ":" + source.Path
		if seen[key] {
			return invalidWorkerPayload("worker host provenance names a source twice")
		}
		seen[key] = true
	}
	return nil
}

func upcastWorkerDispatchedV1(event Event) (Event, error) {
	var payload WorkerDispatchedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return Event{}, err
	}
	if _, err := LookupLane(payload.LaneID, payload.LaneVersion, payload.LaneDigest); err != nil {
		return Event{}, err
	}
	// CD-0058: declared-side routing fields were dropped. v1 payloads that
	// carried them are folded as-is — the columns simply do not survive the
	// migration to v3 and the fold reads only the surviving identity.
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "worker_event_upcast", "cannot encode the worker dispatch payload", false, "repair the worker event payload", err)
	}
	event.PayloadVersion = 2
	event.Payload = encoded
	return event, nil
}

// upcastWorkerCompletedV1 records a stored completion as legacy rather than as
// an empty report (CD-0056 D6). No upcaster can invent evidence the worker
// never returned, so the origin says which it is.
func upcastWorkerCompletedV1(event Event) (Event, error) {
	var payload WorkerCompletedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return Event{}, err
	}
	payload.Evidence = nil
	payload.EvidenceOrigin = WorkerEvidenceLegacyUnavailable
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "worker_event_upcast", "cannot encode the worker completion payload", false, "repair the worker event payload", err)
	}
	event.PayloadVersion = 2
	event.Payload = encoded
	return event, nil
}

func invalidWorkerPayload(detail string) error {
	return newFailure(KindInvalidPayload, "validate_worker_event", detail, false, "repair the worker event payload")
}

// upcastWorkerCompletedV2 carries a v2 completion into the v3 payload that
// may carry the typed review block (CD-0197). v2 payloads never carried one,
// so the upcast is the bytes unchanged at the new version: a replayed
// completion stays one that reported no review block, which the fold forgives
// on replay.
func upcastWorkerCompletedV2(event Event) (Event, error) {
	event.PayloadVersion = 3
	return event, nil
}

func validWorkerFailureKind(value string) bool {
	switch value {
	case WorkerFailureFallbackBlocked, WorkerFailureWorkerError, WorkerFailureInvalidReport, WorkerFailureAbandoned, WorkerFailureModelIdentity, WorkerFailureModelReadbackMissing, WorkerFailureModelReadbackAmbiguous:
		return true
	default:
		return false
	}
}

func modelReadbackFailureKind(value string) bool {
	return value == WorkerFailureModelReadbackMissing || value == WorkerFailureModelReadbackAmbiguous
}

func foldWorkerDispatched(ctx context.Context, tx *sql.Tx, event Event) error {
	if !isWorkflowReplay(ctx) {
		if err := requireNoOutsideRepairTx(ctx, tx, event.SubjectID, "worker_dispatch"); err != nil {
			return err
		}
	}
	var payload WorkerDispatchedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	// CD-0205: worker_job was introduced at the worker.dispatched job-capable
	// payload version (workerDispatchedWorkerJobVersion). A replayed event
	// whose recorded source version sits below the boundary cannot carry
	// worker_job bytes: the field did not exist when that version was
	// released, so any bytes that name it are a fabricated binding that no
	// store ever recorded. Refuse it closed so the live boundary and
	// log-ordered replay enforce one rule.
	if payload.WorkerJob != nil && event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < workerDispatchedWorkerJobVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched worker_job is reserved for payload version >= 5", false, "record the worker_job on the current dispatch payload")
	}
	// CD-0205: the dispatch evidence carries exactly the worker-job binding
	// its dispatch_worker authorization recorded — the same revision, or none
	// when the authorization bound none. A binding with no authorizing
	// completion refuses, so evidence cannot attach a job the core did not
	// authorize. The bound revision must still be recorded under its digest
	// and unsatisfied. The check lives in the fold, so the live boundary and
	// log-ordered replay enforce one rule.
	authorized, authorizedJob, err := dispatchCompletionJobForAttempt(ctx, tx, event.SubjectID, payload.AttemptID)
	if err != nil {
		return err
	}
	if payload.WorkerJob != nil && !authorized || authorized && !sameWorkerJob(authorizedJob, payload.WorkerJob) {
		return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched worker_job does not match the worker job its dispatch_worker authorization bound", false, "record the dispatch with the worker job the authorization returned")
	}
	if payload.WorkerJob != nil {
		if err := verifyWorkerDispatchedJobBindingTx(ctx, tx, event.SubjectID, payload.WorkerJob); err != nil {
			return err
		}
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	lifecycleState := "dispatched"
	failureKind, failureDetail := "", ""
	readbackModel := payload.ReadbackModel
	if payload.Terminal == "failed" {
		// Terminal-at-birth dispatch (issue #106): the evidence row records
		// a prohibited outcome as durable evidence, never a usable attempt.
		lifecycleState = "failed"
		failureKind = payload.TerminalFailureKind
		failureDetail = payload.TerminalDetail
	}
	failedAt := any(nil)
	if lifecycleState == "failed" {
		failedAt = now
	}
	// The dispatch_worker completion binds its authorized attempt in flight
	// before any worker evidence exists; the worker's own dispatch evidence
	// promotes that binding instead of re-inserting the row. Rows no
	// completion bound take the insert path unchanged.
	// CD-0205: the promote path records the dispatch payload's packet and
	// report schema identities on the row so the completion fold can read
	// the matched pair the attempt was bound under and refuse a completion
	// whose claim disagrees with it.
	promoted, err := tx.ExecContext(ctx, `UPDATE worker_attempts
		SET readback_model=?,lifecycle_state=?,failure_kind=?,failure_detail=?,dispatched_at=?,failed_at=?,packet_schema_version=?,report_schema_version=?
		WHERE attempt_id=? AND work_id=? AND lifecycle_state='in_flight'`,
		readbackModel, lifecycleState, failureKind, failureDetail, now, failedAt, payload.PacketSchemaVersion, payload.ReportSchemaVersion, payload.AttemptID, event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot promote the in-flight worker attempt binding", true, "retry once the database is writable", err)
	}
	if rows, rowsErr := promoted.RowsAffected(); rowsErr != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the in-flight promotion result", true, "retry once the database is writable", rowsErr)
	} else if rows > 0 {
		return pinWorkerDispatchedLaneActor(ctx, tx, event, payload)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO worker_attempts
		(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,failed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, payload.AttemptID, payload.LaneID, payload.LaneVersion, payload.LaneDigest, payload.CapabilityClass, readbackModel, payload.PacketSchemaVersion, payload.ReportSchemaVersion, lifecycleState, failureKind, failureDetail, now, failedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "worker attempt is already dispatched", false, "use a new attempt identity")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot create worker attempt projection", true, "retry once the database is writable", err)
	}
	return pinWorkerDispatchedLaneActor(ctx, tx, event, payload)
}

// pinWorkerDispatchedLaneActor carries the issue #800 / CD-0017 D4 actor
// pinning every dispatch evidence fold owes: a dispatched lane is the
// executing actor of the external-effect step its window opened on. The
// actor row itself is recorded by the workflow.actor_recorded event the
// dispatch operation prepends; this fold only refuses an unrecorded ref and
// pins the instance's executing actor, so accept_worker_result compares the
// owner against the party that actually executed. A work item without a
// running workflow instance keeps its projection untouched.
func pinWorkerDispatchedLaneActor(ctx context.Context, tx *sql.Tx, event Event, payload WorkerDispatchedPayload) error {
	if payload.LaneActorRef == "" {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_actors WHERE actor_ref=?`, payload.LaneActorRef).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched lane_actor_ref is not a recorded workflow actor", false, "prepend the lane actor event to the dispatch operation")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot read lane workflow actor", true, "retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET execution_actor_ref=? WHERE work_id=?`, payload.LaneActorRef, event.SubjectID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot pin lane executing actor", true, "retry once the database is writable", err)
	}
	return nil
}

// PrepareLaneActorDispatch turns one worker.dispatched event into the pair the
// v4 evidence boundary requires (issue #800 / CD-0017 D4): a
// workflow.actor_recorded event for the lane's executing identity, followed by
// the dispatch event carrying the recorded actor ref. The lane actor's tuple is
// derived, not supplied: the agent is the lane, the session is the attempt,
// and the principal and client come from the host identity that authenticated
// the dispatch. Callers run this inside the dispatch transaction so the actor
// row and the attempt projection commit atomically. An already-enriched event
// is returned unchanged, which makes the helper idempotent.
func PrepareLaneActorDispatch(ctx context.Context, transaction *Transaction, dispatch Event, principalRef, clientRef string) ([]Event, error) {
	tx, err := transactionSQL(transaction, "worker_lane_actor_prepare")
	if err != nil {
		return nil, err
	}
	var payload WorkerDispatchedPayload
	if err := decodeClosedWorkerPayload(dispatch, &payload); err != nil {
		return nil, err
	}
	if payload.LaneActorRef != "" {
		return []Event{dispatch}, nil
	}
	if payload.AttemptID == "" || payload.LaneID == "" {
		return nil, invalidWorkerPayload("worker.dispatched payload has invalid identity")
	}
	laneActor := WorkflowActor{
		PrincipalRef: principalRef,
		ClientRef:    clientRef,
		AgentRef:     "agent/lane:" + payload.LaneID,
		SessionRef:   "session/" + payload.AttemptID,
		ActorClass:   ActorAgent,
	}
	laneRef, err := WorkflowActorRef(laneActor)
	if err != nil {
		return nil, err
	}
	version, exists, err := projectionVersion(ctx, tx, SubjectWorkItem, dispatch.SubjectID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, newFailure(KindProjectionNotFound, "worker_lane_actor_prepare", "work item does not exist", false, "dispatch against an existing work item")
	}
	resulting := version + 1
	actorPayload := workflowActorRecordedPayload{
		WorkflowVersionFields: WorkflowVersionFields{WorkID: dispatch.SubjectID, ExpectedVersion: &version, ResultingVersion: &resulting},
		ActorRef:              laneRef,
		PrincipalRef:          laneActor.PrincipalRef,
		ClientRef:             laneActor.ClientRef,
		AgentRef:              laneActor.AgentRef,
		SessionRef:            laneActor.SessionRef,
		ActorClass:            string(ActorAgent),
	}
	actorRaw, err := json.Marshal(actorPayload)
	if err != nil {
		return nil, wrapFailure(KindInvalidPayload, "worker_lane_actor_prepare", "cannot encode lane actor payload", false, "report the encoding failure", err)
	}
	payload.LaneActorRef = laneRef
	dispatchRaw, err := json.Marshal(payload)
	if err != nil {
		return nil, wrapFailure(KindInvalidPayload, "worker_lane_actor_prepare", "cannot encode enriched dispatch payload", false, "report the encoding failure", err)
	}
	actorEvent := Event{
		EventID: dispatch.EventID + ":lane-actor", Kind: WorkflowActorRecorded,
		SubjectType: dispatch.SubjectType, SubjectID: dispatch.SubjectID, Actor: dispatch.Actor,
		OccurredAt: dispatch.OccurredAt, PayloadVersion: 1, Payload: actorRaw,
	}
	dispatch.Payload = dispatchRaw
	dispatch.PayloadVersion = WorkerEvidenceEventPayloadVersion(WorkerDispatched)
	return []Event{actorEvent, dispatch}, nil
}

// AppendLaneActorDispatchTx appends the enriched dispatch pair through the
// authoritative workflow route (issue #800 / CD-0017 D4). The worker-dispatch
// evidence boundary is an authoritative route — it validated a signed
// assertion before reaching here — but only for this closed shape: one lane
// actor event whose identity is derived from the dispatch it precedes, and
// the dispatch event itself. The grant does not extend to any other workflow
// event and cannot be reached through the generic append APIs.
func AppendLaneActorDispatchTx(ctx context.Context, transaction *Transaction, events []Event) (ApplyOperationResult, error) {
	if len(events) != 2 || events[0].Kind != WorkflowActorRecorded || events[1].Kind != WorkerDispatched {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "worker_lane_actor_append", "lane actor dispatch pair has an unexpected shape", false, "build the pair with PrepareLaneActorDispatch")
	}
	if events[0].SubjectType != events[1].SubjectType || events[0].SubjectID != events[1].SubjectID || events[0].EventID != events[1].EventID+":lane-actor" {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "worker_lane_actor_append", "lane actor event does not derive from its dispatch", false, "build the pair with PrepareLaneActorDispatch")
	}
	tx, err := transactionSQL(transaction, "worker_lane_actor_append")
	if err != nil {
		return ApplyOperationResult{}, err
	}
	scope := transaction.fold
	if scope == nil {
		scope = newFoldScope(tx)
	}
	return applyWorkflowOperationTx(ctx, tx, Operation{Events: events}, scope)
}

func foldWorkerCompleted(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerCompletedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	// CD-0205: worker_job was introduced at the worker.completed job-capable
	// payload version (workerCompletedWorkerJobVersion). A replayed event
	// whose recorded source version sits below the boundary cannot carry
	// worker_job bytes: the field did not exist when that version was
	// released, so any bytes that name it are a fabricated claim that no
	// store ever recorded. Refuse it closed so the live boundary and
	// log-ordered replay enforce one rule.
	if payload.WorkerJob != nil && event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < workerCompletedWorkerJobVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.completed worker_job is reserved for payload version >= 4", false, "record the worker_job on the current completion payload")
	}
	// CON-887: context findings were introduced at the completed
	// findings-capable payload version. A replayed event whose recorded
	// source version sits below the boundary cannot carry context_findings
	// bytes: any bytes that name them are fabricated worker claims that no
	// store ever recorded.
	if payload.ContextFindings != nil && event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < workerCompletedContextFindingsVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.completed context_findings are reserved for payload version >= 5", false, "record the context_findings on the current completion payload")
	}
	// CON-890: the typed oracle report members were introduced at the
	// oracle-capable payload version. A replayed event whose recorded
	// source version sits below the boundary cannot carry oracle report
	// bytes: any bytes that name them are fabricated ties no store ever
	// recorded. A report that carries them must also claim the worker-job
	// revision the attempt was dispatched under; the live fold joins every
	// tie against that revision's recorded oracle and the prior
	// ranked-finding lineage, while replay trusts the recorded log.
	if hasOracleReportContent(payload) {
		if event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < workerCompletedOracleReportVersion {
			return newFailure(KindInvalidPayload, "fold_event", "worker.completed oracle report content is reserved for payload version >= 6", false, "record the oracle receipts, finding ties, and closures on the current completion payload")
		}
		if payload.WorkerJob == nil {
			return newFailure(KindInvalidPayload, "fold_event", "worker.completed oracle report content requires the worker-job revision the attempt was dispatched under", false, "report oracle content only for a job-bound dispatch")
		}
	}
	if err := validateWorkerContextFindingDomainsTx(ctx, tx, event.SubjectID, payload.ContextFindings); err != nil {
		return err
	}
	attempt, err := readWorkerTerminalAttempt(ctx, tx, event, payload.AttemptID, map[string]bool{"dispatched": true})
	if err != nil {
		return err
	}
	// CD-0205: the completion's report schema identity must match the one the
	// dispatch persisted on the attempt row. A dispatch recorded the matched
	// packet/report pair (workerSchemaVersionFault refuses the rest), and the
	// completion carries only the report side; an unmatched claim — legacy on
	// a job-capable attempt, or job-capable on a legacy attempt — refuses
	// closed so a mixed legacy/current shape never reaches the projection.
	if payload.ReportSchemaVersion != attempt.ReportSchemaVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.completed report_schema_version does not match the schema identity the attempt was dispatched with", false, "report the report_schema_version the dispatch packet carried")
	}
	// The host session can retain its old process directory after the host moves
	// the session record. Refuse a reported directory that is not an active claim
	// before the completion event can make the attempt terminal. The boundary
	// reads the live host filesystem, which replay neither owns nor can reach:
	// reclaimed worktrees no longer resolve, so replay trusts the recorded
	// dispatch evidence instead.
	if payload.WorkerDirectory != "" && !isWorkflowReplay(ctx) {
		if err := validateWorkerDispatchWorktree(ctx, tx, event.SubjectID, payload.WorkerDirectory); err != nil {
			return err
		}
	}
	dispatchedJob, err := workflowDispatchedJobForAttempt(ctx, tx, attempt.WorkID, payload.AttemptID)
	if err != nil {
		return err
	}
	if !sameWorkerJob(dispatchedJob, payload.WorkerJob) {
		return newFailure(KindInvalidPayload, "fold_event", "worker.completed worker_job does not name the worker-job revision the attempt was dispatched under", false, "report the worker_job the dispatch packet carried, or none when it carried none")
	}
	// CON-890: the live oracle join. The dispatched immutable job is the
	// one authority: a report carrying oracle content must have been
	// dispatched under an oracle-bearing revision, every review finding of
	// an oracle-bound dispatch carries a classification, and every
	// receipt, finding tie, and closure claim joins that revision's
	// recorded oracle and the prior ranked-finding lineage. Replay trusts
	// the recorded log — the live fold already passed these checks when it
	// landed — and oracle-free dispatches keep their legacy reviews.
	if !isWorkflowReplay(ctx) && (hasOracleReportContent(payload) || payload.Review != nil) {
		var oracle *AcceptanceOracle
		if dispatchedJob != nil {
			read, oracleErr := readWorkerJobOracle(ctx, tx, attempt.WorkID, *dispatchedJob)
			if oracleErr != nil {
				return oracleErr
			}
			oracle = read
		}
		if hasOracleReportContent(payload) && oracle == nil {
			return oracleFindingFailure(KindInvalidPayload, "a report carrying oracle content was dispatched under a job revision that records no oracle", "report oracle content only under an oracle-bearing revision")
		}
		if oracle != nil {
			lineage, lineageErr := readWorkerOracleFindingLineageExcludingTx(ctx, tx, attempt.WorkID, event.Seq)
			if lineageErr != nil {
				return lineageErr
			}
			if err := validateWorkerOracleCompletedReportTx(ctx, tx, attempt.WorkID, payload, oracle, lineage); err != nil {
				return err
			}
		}
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	// CD-0056 D4: the fold is the only point where the attempt's lane
	// identity and the reported evidence are both in hand, so coverage is
	// enforced inside the transaction that would make the attempt terminal.
	live := !isWorkflowReplay(ctx)
	if payload.EvidenceOrigin == WorkerEvidenceReported || live {
		lane, err := LookupLane(attempt.LaneID, attempt.LaneVersion, attempt.LaneDigest)
		if err != nil {
			return err
		}
		if payload.EvidenceOrigin == WorkerEvidenceReported {
			if err := verifyWorkerEvidenceCoverage(lane, payload.Evidence, payload.Review); err != nil {
				return err
			}
		}
		// CD-0197: a lane that requires the typed review block refuses every
		// live completion without it, whatever evidence origin the completion
		// claims. Stored completions from before the requirement replay
		// unchanged. The per-lane requirement sits here rather than in the
		// payload validator, which cannot reach the dispatching lane.
		if live {
			if err := verifyWorkerReportBlockRequirement(lane, payload.Review); err != nil {
				return err
			}
		}
		// The recorded dispatch owns the predicate vocabulary: the typed
		// outcome predicate ids it carried are read from the dispatch
		// authorization event, never re-derived from the report, so a report
		// can tie only predicates the dispatch declared. Replay trusts the
		// recorded completion: history folded before this check may carry ties
		// the live fold now refuses, or a dispatch recorded before the
		// predicate list existed.
		if live && payload.EvidenceOrigin == WorkerEvidenceReported {
			declared, err := dispatchedPacketPredicateIDsTx(ctx, tx, attempt.WorkID, payload.AttemptID)
			if err != nil {
				return err
			}
			if err := verifyWorkerPredicateTies(declared, payload.Evidence); err != nil {
				return err
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET readback_model=?, lifecycle_state='completed', completed_at=? WHERE attempt_id=? AND work_id=? AND lifecycle_state='dispatched'`, payload.ReadbackModel, now, payload.AttemptID, attempt.WorkID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot complete worker attempt projection", true, "retry once the database is writable", err)
	}
	if err := verifyWorkerTerminalUpdate(result, "cannot verify worker completion projection", "record worker.dispatched before worker.completed"); err != nil {
		return err
	}
	return nil
}

func foldWorkerFailed(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerFailedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	// CON-887: context findings were introduced at the failed
	// findings-capable payload version (workerFailedContextFindingsVersion).
	// A replayed event whose recorded source version sits below the boundary
	// cannot carry context_findings bytes: any bytes that name them are
	// fabricated worker claims that no store ever recorded.
	if payload.ContextFindings != nil && event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < workerFailedContextFindingsVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.failed context_findings are reserved for payload version >= 2", false, "record the context_findings on the current failure payload")
	}
	if err := validateWorkerContextFindingDomainsTx(ctx, tx, event.SubjectID, payload.ContextFindings); err != nil {
		return err
	}
	// A dispatched attempt is closable by every failure kind. An in_flight
	// binding — authorized by a dispatch_worker completion whose window was
	// lost before the native task call — is closable by abandonment alone:
	// no worker evidence exists for it, so no evidence-demanding failure
	// kind can honestly describe it (CON-791).
	admittedTerminal := map[string]bool{"dispatched": true}
	if payload.FailureKind == WorkerFailureAbandoned {
		admittedTerminal["in_flight"] = true
	}
	attempt, err := readWorkerTerminalAttempt(ctx, tx, event, payload.AttemptID, admittedTerminal)
	if err != nil {
		return err
	}
	if payload.FailureKind == WorkerFailureAbandoned && !isWorkflowReplay(ctx) {
		if err := validateNoLiveWorkerSession(ctx, tx, event.SubjectID, payload.AttemptID); err != nil {
			return err
		}
	}
	readbackModel := payload.ReadbackModel
	if payload.FailureKind == WorkerFailureAbandoned {
		// Abandonment has no worker readback. Preserve the model recorded when
		// the attempt was dispatched instead of accepting caller-supplied data.
		// An in_flight binding carries none, so the abandoned row keeps the
		// empty model the binding recorded.
		readbackModel = attempt.ReadbackModel
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	result, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET readback_model=?, lifecycle_state='failed', failure_kind=?, failure_detail=?, failed_at=? WHERE attempt_id=? AND work_id=? AND lifecycle_state=?`, readbackModel, payload.FailureKind, payload.Detail, now, payload.AttemptID, attempt.WorkID, attempt.Lifecycle)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot fail worker attempt projection", true, "retry once the database is writable", err)
	}
	if err := verifyWorkerTerminalUpdate(result, "cannot verify worker failure projection", "record worker.dispatched before worker.failed"); err != nil {
		return err
	}
	return nil
}

// validateNoLiveWorkerSession is the live-admission host-observation gate for
// an abandoned attempt (CD-0178 D3). Replay trusts the recorded worker.failed
// event instead of consulting today's filesystem, occupancy, or host liveness.
// Its dispatch window owns the Project, and the durable projection owns
// occupancy. Recorded rows carry the host process identity, and the kernel
// proves whether a process is still alive. A live row blocks abandonment; a dead
// row or no row at all admits the close. The store never reaches for the
// host session list, so a session running in another repository cannot
// strand this attempt through observation alone.
//
// A row without process identity follows the CD-0179 D3 release rule the
// reclaim path applies: it admits the close when the caller's pre-transaction
// host lease set proves every live host lease started after the row's
// recorded_at, which proves the recording process ended. A missing or
// unreadable lease set releases nothing, and so does any live lease that
// started at or before the row.
func validateNoLiveWorkerSession(ctx context.Context, tx *sql.Tx, workID, attemptID string) error {
	projectID, err := workerAttemptProjectTx(ctx, tx, workID, attemptID)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT e.set_id, e.project_id, e.claim_op_id, o.session_ref, o.has_process_identity, o.host_pid, o.host_pid_start, o.recorded_at
		  FROM worktree_entries e
		  JOIN worktree_occupancy o ON o.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id
		 WHERE e.set_id=? AND e.state='active' AND (?='' OR e.project_id=?)`, WorktreeSetID(workID), projectID, projectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "worker_fail", "cannot read worktree occupancy", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	type pending struct {
		setID, projectID, claimOpID, sessionRef string
		hasIdentity                             bool
		hostPID                                 *int64
		hostPIDStart                            *uint64
		recordedAt                              string
	}
	var actives []pending
	for rows.Next() {
		var p pending
		var hostPID sql.NullInt64
		var hostPIDStart sql.NullInt64
		var hasIdentity int
		if err := rows.Scan(&p.setID, &p.projectID, &p.claimOpID, &p.sessionRef, &hasIdentity, &hostPID, &hostPIDStart, &p.recordedAt); err != nil {
			return err
		}
		p.hasIdentity = hasIdentity == 1
		if hostPID.Valid {
			v := hostPID.Int64
			p.hostPID = &v
		}
		if hostPIDStart.Valid {
			v, err := occupancyPIDStart(hostPIDStart.Int64)
			if err != nil {
				return err
			}
			p.hostPIDStart = &v
		}
		actives = append(actives, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range actives {
		if !p.hasIdentity {
			// Legacy row without process identity: the kernel cannot prove
			// liveness. The close is admitted only on the CD-0179 D3
			// lease-set proof the caller carried on the context; a missing
			// or unreadable observation releases nothing.
			leases, carried := hostLeaseSetFromContext(ctx)
			if !carried || !legacyOccupancyRowEnded(leases, p.recordedAt) {
				return newFailure(KindWorktreeOwnershipConflict, "worker_fail",
					fmt.Sprintf("session %s holds a legacy occupancy row on the worker attempt worktree; process liveness cannot prove it ended", p.sessionRef),
					false, "session_vacate the legacy occupant before retrying abandonment")
			}
			continue
		}
		start, err := hostlease.ProcessStart(int(*p.hostPID))
		if err != nil || start != *p.hostPIDStart {
			continue
		}
		return newFailure(KindWorktreeOwnershipConflict, "worker_fail",
			fmt.Sprintf("session %s still holds the worker attempt worktree; its host process %d is still live", p.sessionRef, *p.hostPID),
			false, "end or move the live session, then retry the abandonment")
	}
	return nil
}

// workerTerminalAttempt is the dispatched-attempt identity a terminal worker
// fold needs. It carries the lane identity and dispatch-time model so the fold
// can resolve the lane and preserve that model for an abandonment event, and
// the report schema identity the dispatch fold persisted so the completion
// fold can refuse a completion whose claim disagrees with the matched pair
// the dispatch bound (CD-0205).
type workerTerminalAttempt struct {
	WorkID              string
	Lifecycle           string
	LaneID              string
	LaneVersion         int64
	LaneDigest          string
	ReadbackModel       string
	ReportSchemaVersion string
}

// readWorkerTerminalAttempt reads through the passed transaction only. The
// store pools one connection, so any *Store method call here would park on the
// pool forever while this transaction holds it. admittedTerminal names the
// lifecycle states the calling fold may take terminal: a completion admits a
// dispatched attempt only, while a failure additionally admits an in_flight
// binding for the abandoned kind (CON-791).
func readWorkerTerminalAttempt(ctx context.Context, tx *sql.Tx, event Event, attemptID string, admittedTerminal map[string]bool) (workerTerminalAttempt, error) {
	var attempt workerTerminalAttempt
	if event.SubjectType != SubjectWorkItem {
		return attempt, newFailure(KindInvalidPayload, "fold_event", "worker terminal event must target a work item", false, "use subject_type=work_item")
	}
	if err := tx.QueryRowContext(ctx, `SELECT work_id,lifecycle_state,lane_id,lane_version,lane_digest,readback_model,report_schema_version FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&attempt.WorkID, &attempt.Lifecycle, &attempt.LaneID, &attempt.LaneVersion, &attempt.LaneDigest, &attempt.ReadbackModel, &attempt.ReportSchemaVersion); err != nil {
		if err == sql.ErrNoRows {
			return attempt, newFailure(KindProjectionNotFound, "fold_event", "worker dispatch row does not exist", false, "record worker.dispatched before the terminal worker event")
		}
		return attempt, wrapFailure(KindUnavailable, "fold_event", "cannot read worker attempt projection", true, "retry once the database is readable", err)
	}
	if attempt.WorkID != event.SubjectID {
		return attempt, newFailure(KindInvalidOperation, "fold_event", "worker terminal event subject does not own the worker attempt", false, "use the attempt's owning work item as the event subject")
	}
	if !admittedTerminal[attempt.Lifecycle] {
		return attempt, newFailure(KindProjectionConflict, "fold_event", "worker attempt lifecycle is not admissible for this terminal event", false, "use a new attempt identity")
	}
	return attempt, nil
}

// verifyWorkerEvidenceCoverage enforces CD-0056 D4 coverage: every obligation
// the dispatching lane declares appears at least once, and the report names no
// obligation the lane does not declare. Concord does not count entries, rank
// them, or judge their content.
//
// CD-0197 D2: a completion that carries the typed review block discharges the
// severity obligation through the block, so severity has one source. The
// block covers a declared severity obligation, and a free-text severity entry
// beside the block is refused. The gate is the carried block, not the live
// boundary: stored completions replay exactly as they were accepted — a
// pre-CD-0197 completion carries a severity entry and no block, a current one
// carries the block and no severity entry — so a rebuild reaches the same
// projection.
func verifyWorkerEvidenceCoverage(lane LaneDefinition, evidence []WorkerReportEvidence, review *WorkerReviewBlock) error {
	declared := make(map[string]struct{}, len(lane.EvidenceObligations))
	for _, obligation := range lane.EvidenceObligations {
		declared[obligation] = struct{}{}
	}
	discharged := make(map[string]struct{}, len(evidence))
	undeclared := make(map[string]struct{})
	for _, entry := range evidence {
		if _, ok := declared[entry.Obligation]; !ok {
			undeclared[entry.Obligation] = struct{}{}
			continue
		}
		discharged[entry.Obligation] = struct{}{}
	}
	if len(undeclared) > 0 {
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("worker report names evidence obligations the dispatching lane does not declare: %s", sortedObligationList(undeclared)),
			false, "record worker.failed with the invalid_report failure kind")
	}
	if review != nil {
		if _, isDeclared := declared["severity"]; isDeclared {
			if _, reported := discharged["severity"]; reported {
				return newFailure(KindInvalidPayload, "fold_event",
					"worker report carries a free-text severity entry beside the typed review block that discharges severity",
					false, "record worker.failed with the invalid_report failure kind")
			}
			discharged["severity"] = struct{}{}
		}
	}
	missing := make(map[string]struct{})
	for _, obligation := range lane.EvidenceObligations {
		if _, ok := discharged[obligation]; !ok {
			missing[obligation] = struct{}{}
		}
	}
	if len(missing) > 0 {
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("worker report leaves lane evidence obligations undischarged: %s", sortedObligationList(missing)),
			false, "record worker.failed with the invalid_report failure kind")
	}
	return nil
}

// verifyWorkerReportBlockRequirement enforces the per-lane report block
// requirement (CD-0197): a live completion for a lane whose contract requires
// the typed review block is refused without it. The refusal names the block so
// the caller records worker.failed with the invalid_report kind. Replay
// forgives: a stored completion predating the requirement cannot invent the
// block, and replay must reach the same projection it always reached.
func verifyWorkerReportBlockRequirement(lane LaneDefinition, review *WorkerReviewBlock) error {
	required := false
	for _, block := range lane.RequiredReportBlocks {
		if block == "review" {
			required = true
		}
	}
	if !required || review != nil {
		return nil
	}
	return newFailure(KindInvalidPayload, "fold_event",
		fmt.Sprintf("worker report completes without the typed review block the %s lane requires", lane.ID),
		false, "record worker.failed with the invalid_report failure kind")
}

func sortedObligationList(values map[string]struct{}) string {
	names := make([]string, 0, len(values))
	for value := range values {
		names = append(names, value)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// dispatchedPacketPredicateIDsTx reads the typed outcome predicate ids the
// dispatch authorization recorded for one worker attempt, in contract
// ordinal order. The dispatch_worker completion event is the immutable
// source: the core extracted the ids from the same packet bytes it digested
// at authorization and recorded them unconditionally, empty when the packet
// declared no typed predicates. A recorded dispatch without the list is
// malformed and refuses fail-closed, so a missing projection can never
// silently widen the predicates a report may tie. An attempt with no
// dispatch authorization at all carries no recorded packet, so the fold
// admits no predicate tie for it.
func dispatchedPacketPredicateIDsTx(ctx context.Context, tx *sql.Tx, workID, attemptID string) ([]string, error) {
	var raw *string
	if err := tx.QueryRowContext(ctx, `SELECT json_extract(payload,'$.worker_packet_predicate_ids') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attemptID).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "worker_predicate_discharge", "cannot read the dispatched packet predicates", true, "retry once the database is readable", err)
	}
	if raw == nil {
		return nil, newFailure(KindInvariantViolation, "worker_predicate_discharge", "dispatch_worker completion recorded no worker_packet_predicate_ids", false, "verify the store that recorded the dispatch authorization")
	}
	var ids []string
	if err := json.Unmarshal([]byte(*raw), &ids); err != nil {
		return nil, newFailure(KindInvariantViolation, "worker_predicate_discharge", "dispatch_worker completion recorded malformed worker_packet_predicate_ids", false, "verify the store that recorded the dispatch authorization")
	}
	return ids, nil
}

// verifyWorkerPredicateTies admits a completed report's per-predicate ties
// only against the typed outcome predicates the recorded dispatch carried. A
// report ties the predicates its own evidence discharges, which may be none
// of them: discharge of each predicate is owned by its verdict, which
// completion requires for every contract predicate (CD-0180). A tie to a
// predicate id the dispatch did not declare is refused, and the refusal names
// the undeclared ids.
func verifyWorkerPredicateTies(declared []string, evidence []WorkerReportEvidence) error {
	known := make(map[string]struct{}, len(declared))
	for _, id := range declared {
		known[id] = struct{}{}
	}
	undeclared := make(map[string]struct{})
	for _, entry := range evidence {
		for _, id := range entry.PredicateIDs {
			if _, ok := known[id]; !ok {
				undeclared[id] = struct{}{}
			}
		}
	}
	if len(undeclared) > 0 {
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("worker report ties outcome predicates the dispatch did not declare: %s", sortedObligationList(undeclared)),
			false, "record worker.failed with the invalid_report failure kind")
	}
	return nil
}

func verifyWorkerTerminalUpdate(result sql.Result, unavailableDetail, missingDetail string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", unavailableDetail, true, "retry once the worker attempt projection is readable", err)
	}
	if affected != 1 {
		return newFailure(KindProjectionConflict, "fold_event", "worker attempt terminal transition was not uniquely applied", false, missingDetail)
	}
	return nil
}

// WorkerAttemptByID returns the durable dispatch identity needed to validate a
// completion or failure callback. The lookup is read-only; worker projections
// remain fold-only and can only be changed by appending an event.
func (s *Store) WorkerAttemptByID(ctx context.Context, attemptID string) (WorkerAttempt, error) {
	if s == nil || s.db == nil {
		return WorkerAttempt{}, newFailure(KindUnavailable, "worker_attempt_read", "database is not open", true, "open the authority database")
	}
	return workerAttemptByIDCore(ctx, s.db, attemptID)
}

// WorkerAttemptByIDTx is the transaction-scoped lookup. Authenticating a worker
// evidence write, checking that the attempt has not already reached a recorded
// outcome, and appending the evidence must observe one snapshot, so the caller
// reads the attempt through its own transaction rather than through the pooled
// connection.
func WorkerAttemptByIDTx(ctx context.Context, transaction *Transaction, attemptID string) (WorkerAttempt, error) {
	tx, err := transactionSQL(transaction, "worker_attempt_read")
	if err != nil {
		return WorkerAttempt{}, err
	}
	return workerAttemptByIDCore(ctx, tx, attemptID)
}

// WorkerAttemptIsTerminal reports whether an attempt already reached a recorded
// outcome. A terminal attempt refuses further evidence, so a valid signature
// cannot overwrite a completion with a failure or a failure with a completion.
func WorkerAttemptIsTerminal(attempt WorkerAttempt) bool {
	return attempt.LifecycleState == "completed" || attempt.LifecycleState == "failed"
}

func workerAttemptByIDCore(ctx context.Context, q queryer, attemptID string) (WorkerAttempt, error) {
	var attempt WorkerAttempt
	err := q.QueryRowContext(ctx, `SELECT work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,COALESCE(failure_kind,''),COALESCE(failure_detail,''),dispatched_at,COALESCE(completed_at,''),COALESCE(failed_at,'') FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(
		&attempt.WorkID, &attempt.AttemptID, &attempt.LaneID, &attempt.LaneVersion, &attempt.LaneDigest, &attempt.CapabilityClass, &attempt.ReadbackModel, &attempt.PacketSchemaVersion, &attempt.ReportSchemaVersion, &attempt.LifecycleState, &attempt.FailureKind, &attempt.FailureDetail, &attempt.DispatchedAt, &attempt.CompletedAt, &attempt.FailedAt,
	)
	if err == sql.ErrNoRows {
		return WorkerAttempt{}, newFailure(KindProjectionNotFound, "worker_attempt_read", "worker dispatch row does not exist", false, "record worker.dispatched before the worker result")
	}
	if err != nil {
		return WorkerAttempt{}, wrapFailure(KindUnavailable, "worker_attempt_read", "cannot read worker attempt projection", true, "retry once the database is readable", err)
	}
	return attempt, nil
}

// upcastWorkerDispatchedV2 stamps legacy v2 dispatch evidence with an honest
// provenance marker: recorded before host prompt provenance was declared
// (CD-0034), so the injection surfaces are unknown by construction. CD-0067
// D6: the v3 payload also needs packet_digest, and the legacy v2 record
// never carried one. Pre-cutover data is rewritten to a sentinel digest
// that satisfies the v3 validator; the dispatch-window gate separately
// refuses empty digests on windows that did not record one, so the placeholder
// never reaches the worker-evidence boundary.
func upcastWorkerDispatchedV2(event Event) (Event, error) {
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return Event{}, invalidWorkerPayload("worker event payload does not match its closed schema")
	}
	if _, exists := payload["host_provenance"]; !exists {
		payload["host_provenance"] = map[string]any{
			"digest":  "sha256:" + strings.Repeat("0", 64),
			"sources": []map[string]any{{"kind": "unenumerated"}},
		}
	}
	if _, exists := payload["packet_digest"]; !exists {
		payload["packet_digest"] = "sha256:" + strings.Repeat("0", 64)
	} else if v, ok := payload["packet_digest"].(string); !ok || v == "" {
		payload["packet_digest"] = "sha256:" + strings.Repeat("0", 64)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	event.PayloadVersion = 3
	event.Payload = raw
	return event, nil
}

// upcastWorkerDispatchedV3 carries a v3 dispatch into v4. The only v4
// addition is lane_actor_ref, which v3 payloads never carried: a replayed
// dispatch stays a legacy dispatch and pins no executing actor, matching the
// behavior of the store that originally recorded it.
func upcastWorkerDispatchedV3(event Event) (Event, error) {
	event.PayloadVersion = 4
	return event, nil
}

// upcastWorkerDispatchedV4 carries a v4 dispatch into the v5 payload that may
// bind a worker job (CD-0205). v4 payloads never carried one and recorded the
// released 1.0 packet/report identities, so the upcast is the bytes unchanged
// at the new version: a replayed dispatch stays an unbound dispatch under the
// identities the store originally recorded, and no upcaster fabricates a job.
func upcastWorkerDispatchedV4(event Event) (Event, error) {
	event.PayloadVersion = 5
	return event, nil
}

// upcastWorkerCompletedV3 carries a v3 completion into the v4 payload that
// may claim a worker-job revision (CD-0205). v3 payloads never claimed one
// and recorded the released 1.0 report identity, so the upcast is the bytes
// unchanged at the new version: a replayed completion stays a report without
// a job claim, exactly as the worker returned it.
func upcastWorkerCompletedV3(event Event) (Event, error) {
	event.PayloadVersion = 4
	return event, nil
}

// upcastWorkerCompletedV4 carries a v4 completion into the v5 payload that
// may carry typed context findings (CON-887). v4 payloads never carried any,
// so the upcast is the bytes unchanged at the new version: a replayed
// completion stays a report without findings, exactly as the worker returned
// it, and no upcaster fabricates worker claims.
func upcastWorkerCompletedV4(event Event) (Event, error) {
	event.PayloadVersion = 5
	return event, nil
}

// upcastWorkerCompletedV5 carries a v5 completion into the v6 payload that
// may carry the typed oracle report members (CON-890). v5 payloads never
// carried any, so the upcast is the bytes unchanged at the new version: a
// replayed completion stays a report without oracle ties, exactly as the
// worker returned it, and no upcaster fabricates a receipt, a tie, or a
// closure.
func upcastWorkerCompletedV5(event Event) (Event, error) {
	event.PayloadVersion = 6
	return event, nil
}

// upcastWorkerFailedV1 carries a v1 failure into the v2 payload that may
// carry typed context findings (CON-887). v1 payloads never carried any, so
// the upcast is the bytes unchanged at the new version: a replayed failure
// stays the diagnostic the host recorded, with no fabricated worker claims.
func upcastWorkerFailedV1(event Event) (Event, error) {
	event.PayloadVersion = 2
	return event, nil
}
