package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sharper-flow/concord/internal/hostlease"
)

const (
	WorkerDispatched = "worker.dispatched"
	WorkerCompleted  = "worker.completed"
	WorkerFailed     = "worker.failed"

	// WorkerPacketSchemaVersion and WorkerReportSchemaVersion are the
	// current schema identities: the card-free 2.0 revision whose
	// packet drops the retired domain_cards member. The matched pair is the
	// only one a live dispatch may record; the adapter records it on every
	// dispatch and report it builds.
	WorkerPacketSchemaVersion = "2.0"
	WorkerReportSchemaVersion = "2.0"
	// WorkerPacketSchemaVersionPrior and WorkerReportSchemaVersionPrior are
	// the released job-capable identities (CD-0205) the 2.0 cutover retired
	// from live production. Recorded dispatches that carry them keep
	// folding and replaying; no host may record a new one.
	WorkerPacketSchemaVersionPrior = "1.1"
	WorkerReportSchemaVersionPrior = "1.1"
	// WorkerPacketSchemaVersionLegacy and WorkerReportSchemaVersionLegacy
	// are the released pre-job identities. A payload that records them
	// never carries a worker-job binding: the binding at "1.0" was the
	// in-place addition the explicit boundary replaces, so the validator
	// refuses it and the historical identities keep their released shape.
	WorkerPacketSchemaVersionLegacy = "1.0"
	WorkerReportSchemaVersionLegacy = "1.0"
)

// workerMatchedSchemaVersions is the closed set of released packet/report
// revisions one recorded dispatch may carry. The set is released history, not
// configuration: 1.0 (pre-job), 1.1 (job-capable CD-0205), and the current
// card-free 2.0 are exactly the identities a store ever recorded.
var workerMatchedSchemaVersions = map[string]bool{
	WorkerPacketSchemaVersionLegacy: true,
	WorkerPacketSchemaVersionPrior:  true,
	WorkerPacketSchemaVersion:       true,
}

// workerJobCapableSchemaVersions is the closed set of released revisions
// whose packets and reports may carry the worker-job binding (CD-0205). Job
// presence binds the recorded revision, not today's current: a recorded 1.1
// history stays job-bound after the 2.0 cutover, while 1.0 never carries a
// job.
var workerJobCapableSchemaVersions = map[string]bool{
	WorkerPacketSchemaVersionPrior: true,
	WorkerPacketSchemaVersion:      true,
}

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
// worker.completed may first carry the typed oracle members: a
// receipt on one evidence entry, an oracle tie on one review finding, or a
// closure claim in resolved_findings. A stored or supplied event whose
// recorded source version sits below the boundary cannot carry oracle
// report bytes: the members did not exist when that version was released,
// so any bytes that name them are fabricated ties no store ever recorded.
const workerCompletedOracleReportVersion = 6

// workerCompletedTerminalDeliveryVersion and
// workerFailedTerminalDeliveryVersion are the payload versions at which the
// terminal events first carry the core-derived consumed_context_frontier
// (both) and the core-observed observed_delivery (completed). A
// stored event whose recorded source version sits below the boundary cannot
// carry those members: the store never derived them when that version was
// released, so any bytes that name them are fabricated identities no store
// ever recorded.
const (
	workerCompletedTerminalDeliveryVersion = 7
	workerFailedTerminalDeliveryVersion    = 3
)

// workerCompletedJobSizeVersion is the payload version at which
// worker.completed may first carry the job's measured size surface: the
// observed delivery's base and per-path counts when the dispatched job
// records a measurement basis, and the report's production-size members. A
// stored event whose recorded source version sits below the boundary cannot
// carry those members: the store never observed or folded them when that
// version was released, so any bytes that name them are fabricated size
// content no store ever recorded. Historical payloads replay with no base or
// counts, never invented ones.
const workerCompletedJobSizeVersion = 8

// workerFailedAttributionVersion and workerDispatchedAttributionVersion are
// the payload versions at which each worker event kind may first carry the
// typed failure_attribution block . A stored event whose recorded
// source version sits below the boundary cannot carry recorded attribution
// content: the store never derived the block when that version was released,
// so any content-bearing bytes that name it are forged attribution no store
// ever recorded. The one legal shape below the boundary is the pure
// legacy_unavailable marker the upcaster stamps, and the recorded source
// version is the marker that distinguishes that upcast-generated absence
// from a forged new-legacy claim: a marker on an event born at or above the
// boundary refuses.
const (
	workerFailedAttributionVersion     = 4
	workerDispatchedAttributionVersion = 6
)

// workerFailureLegacyAttribution is the explicit historical-absence marker
// the upcasters stamp onto replayed terminals that predate the attribution
// boundary. It carries no observation and no identity.
func workerFailureLegacyAttribution() *WorkerFailureAttribution {
	return &WorkerFailureAttribution{Origin: WorkerFailureAttributionLegacyUnavailable}
}

// workerFailureAuthoredAttributionKeyRefused is the upcasters' authored-member
// gate: a stored payload version below the attribution boundary that already
// carries the failure_attribution key — a marker, a null, any bytes — was
// never recorded by a store, because the field did not exist when that
// version was released. The upcaster refuses it instead of stamping around
// it, so the upcast-generated legacy marker is the only below-boundary shape
// the fold can ever see.
func workerFailureAuthoredAttributionKeyRefused(event Event, kind string) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &members); err != nil {
		return invalidWorkerPayload(kind + " payload is not a JSON object")
	}
	if _, authored := members["failure_attribution"]; authored {
		return invalidWorkerPayload(kind + " failure_attribution on a stored payload version below the attribution boundary is authored bytes no store ever recorded; the upcaster stamps the explicit legacy marker alone")
	}
	return nil
}

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
	// FailureAttribution is the typed failure attribution of a born-failed
	// terminal dispatch . Only Terminal="failed" carries one; an
	// ordinary dispatch opens no terminal outcome to attribute and refuses
	// the member. The producer supplies the observation only; the store
	// derives the core identity from the dispatch authorization at ingest,
	// so the recorded block always carries the exact authorized attempt
	// identity. Absent on every payload that predates the attribution
	// version, and on born-failed terminals recorded before the boundary.
	FailureAttribution *WorkerFailureAttribution `json:"failure_attribution,omitempty"`
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
// control-execution receipt: reported evidence only, never
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
// and never rescored. Oracle is the optional typed oracle tie:
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
// evidenced closure claims of ranked findings the lineage holds open. It
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
	// Live appends require this proof; replay trusts the recorded payload.
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
	// ConsumedContextFrontier is the core-derived frontier attribution
	// The field is the worker_context_frontier the attempt's own dispatch
	// authorization recorded. The terminal ingest owner derives it from
	// that authorization; a caller can never supply, change, or omit a
	// value the authorization did not carry. Nil means the authorization
	// predates the field and keeps explicit absence.
	ConsumedContextFrontier *int64 `json:"consumed_context_frontier,omitempty"`
	// ObservedDelivery is the closed delivery observation the core took
	// itself: one raw commit OID plus the clean or dirty state of
	// the attempt's authorized worktree at completion. Only a completed
	// implementation-lane attempt carries one, and only the store's own
	// read-only Git observation can produce it; a report, request, or
	// adapter value can neither supply nor alter it.
	ObservedDelivery *WorkerObservedDelivery `json:"observed_delivery,omitempty"`
	// ProductionSize is the report's declared production size (CD-0225 D2):
	// the counted delta between the dispatched job's recorded base and the
	// delivered tree. Required on a capped implementation job's completed
	// report and refused everywhere else; the fold binds both commits to
	// the recorded basis and the observed delivery before acceptance.
	ProductionSize *WorkerProductionSize `json:"production_size,omitempty"`
	// DirectCallerRepairs are the evidenced direct-caller repairs the
	// worker made under the dispatched job's allowance (CD-0227 D2).
	// Reserved for a capped implementation job's report; every other
	// completion refuses the member.
	DirectCallerRepairs []WorkerDirectCallerRepair `json:"direct_caller_repairs,omitempty"`
}

// WorkerCallerRepairsMax and WorkerCallerRepairPathsMax are the closed
// bounds the report schema declares for the caller-repair list and each
// repair's changed paths. An over-bound report refuses whole; content is
// never truncated to fit.
const (
	WorkerCallerRepairsMax     = 8
	WorkerCallerRepairPathsMax = 64
)

// WorkerProductionSize is the report's declared production size (CD-0225
// D2): the added-plus-deleted production lines between the dispatched job's
// recorded base and the delivered tree.
type WorkerProductionSize struct {
	Base  string `json:"base"`
	Tree  string `json:"tree"`
	Count int64  `json:"count"`
}

// WorkerCallerMechanism names one repair endpoint: the source file and the
// entry point that owns the behavior (CD-0227 D2).
type WorkerCallerMechanism struct {
	Path       string `json:"path"`
	EntryPoint string `json:"entry_point"`
}

// WorkerCallerRepairCheck is one verification signal of a direct-caller
// repair: the recorded output reference, kept apart from the optional
// inferred cause so the report distinguishes observed output from an
// inferred cause (CD-0227 D2).
type WorkerCallerRepairCheck struct {
	Observed      string `json:"observed"`
	InferredCause string `json:"inferred_cause,omitempty"`
}

// WorkerCallerRepairChecks carries the three signals: the same request
// passes on the baseline, fails with the owner change alone, and passes
// with the combined change. A compile-time break names the compile check
// in all three.
type WorkerCallerRepairChecks struct {
	BaselinePass  WorkerCallerRepairCheck `json:"baseline_pass"`
	OwnerOnlyFail WorkerCallerRepairCheck `json:"owner_only_fail"`
	CombinedPass  WorkerCallerRepairCheck `json:"combined_pass"`
}

// WorkerDirectCallerRepair is one evidenced direct-caller repair (CD-0227
// D2): the changed owner mechanism, the direct caller whose request behavior
// it preserves, the direct edge between them, the request or fixture that
// exposes the break, the changed paths, and the three check signals. A
// repair that changes a generated path names its owning generator_ref input
// among its changed paths.
type WorkerDirectCallerRepair struct {
	Owner        WorkerCallerMechanism    `json:"owner"`
	Caller       WorkerCallerMechanism    `json:"caller"`
	Edge         string                   `json:"edge"`
	Exposing     string                   `json:"exposing"`
	ChangedPaths []string                 `json:"changed_paths"`
	Checks       WorkerCallerRepairChecks `json:"checks"`
	GeneratorRef []string                 `json:"generator_ref,omitempty"`
}

// WorkerObservedPath is one observed path's measured delta between the
// dispatched job's recorded base and the observed commit. Rename detection
// stays off, so a rename is one deletion entry beside one addition entry.
// Numstat's `-\t-` marks a binary path.
type WorkerObservedPath struct {
	Path    string `json:"path"`
	Added   int64  `json:"added"`
	Deleted int64  `json:"deleted"`
	Binary  bool   `json:"binary,omitempty"`
}

// WorkerObservedDelivery is the closed core-owned delivery observation
// State is clean or dirty only; dirty-but-reachable records
// dirty and cannot qualify a review subject. There is no unavailable state:
// a Git failure or deadline refuses the completion with zero effects
// instead of recording an observation. When the completed attempt's
// dispatched job records a measurement basis, Base is that recorded
// base_commit and Paths carries the observed per-path counts; a dirty
// observation records no paths and cannot establish a size, and a
// basis-free job records neither member.
type WorkerObservedDelivery struct {
	Commit string               `json:"commit"`
	State  string               `json:"state"`
	Base   string               `json:"base,omitempty"`
	Paths  []WorkerObservedPath `json:"paths,omitempty"`
}

var workerObservedDeliveryStates = map[string]bool{"clean": true, "dirty": true}

func validateWorkerObservedDelivery(delivery *WorkerObservedDelivery) error {
	if delivery == nil {
		return nil
	}
	if !worktreeSHAPattern.MatchString(delivery.Commit) {
		return invalidWorkerPayload("worker.completed observed_delivery.commit must be one raw commit OID")
	}
	if !workerObservedDeliveryStates[delivery.State] {
		return invalidWorkerPayload("worker.completed observed_delivery.state must be clean or dirty")
	}
	if delivery.Base != "" && !worktreeSHAPattern.MatchString(delivery.Base) {
		return invalidWorkerPayload("worker.completed observed_delivery.base must be one raw commit OID")
	}
	if delivery.Base == "" && delivery.Paths != nil {
		return invalidWorkerPayload("worker.completed observed_delivery paths require the dispatched job's recorded base_commit")
	}
	if delivery.State == "dirty" && delivery.Paths != nil {
		return invalidWorkerPayload("worker.completed observed_delivery of a dirty tree records no paths")
	}
	seen := map[string]bool{}
	for _, path := range delivery.Paths {
		if path.Path == "" {
			return invalidWorkerPayload("worker.completed observed_delivery carries a path entry with no path")
		}
		if path.Added < 0 || path.Deleted < 0 {
			return invalidWorkerPayload("worker.completed observed_delivery path " + path.Path + " carries a negative count")
		}
		if seen[path.Path] {
			return invalidWorkerPayload("worker.completed observed_delivery carries path " + path.Path + " twice")
		}
		seen[path.Path] = true
	}
	return nil
}

// validateWorkerProductionSize mirrors the closed shape the report schema
// gives production_size: two raw commit OIDs and a bounded count.
func validateWorkerProductionSize(size *WorkerProductionSize) error {
	if size == nil {
		return nil
	}
	if !worktreeSHAPattern.MatchString(size.Base) || !worktreeSHAPattern.MatchString(size.Tree) {
		return invalidWorkerPayload("worker.completed production_size base and tree must each be one raw commit OID")
	}
	if size.Count < 0 || size.Count > WorkerJobSizeCapMax {
		return invalidWorkerPayload("worker.completed production_size count must be between 0 and " + fmt.Sprint(WorkerJobSizeCapMax))
	}
	return nil
}

// validateWorkerDirectCallerRepairs mirrors the closed shape the report
// schema gives direct_caller_repairs (CD-0227 D2): at most 8 bounded
// repairs, each naming its owner and caller mechanisms, the direct edge,
// the exposing request or fixture, at most 64 changed paths, and the three
// check signals each carrying its observed output reference apart from the
// optional inferred cause. Every generator_ref input appears among the
// repair's changed paths, because a generated output is never hand-edited.
func validateWorkerDirectCallerRepairs(repairs []WorkerDirectCallerRepair) error {
	if repairs == nil {
		return nil
	}
	if len(repairs) < 1 || len(repairs) > WorkerCallerRepairsMax {
		return invalidWorkerPayload("worker.completed direct_caller_repairs must carry between 1 and " + fmt.Sprint(WorkerCallerRepairsMax) + " repairs")
	}
	for _, repair := range repairs {
		for _, mechanism := range []struct {
			role      string
			mechanism WorkerCallerMechanism
		}{
			{"owner", repair.Owner}, {"caller", repair.Caller},
		} {
			if problem := normalizedRelativeRepoPathProblem("worker.completed direct_caller_repairs "+mechanism.role+" path", mechanism.mechanism.Path); problem != "" {
				return invalidWorkerPayload(problem)
			}
			if err := oracleBoundedText(mechanism.mechanism.EntryPoint, "worker.completed direct_caller_repairs "+mechanism.role+" entry_point"); err != nil {
				return invalidWorkerPayload("worker.completed direct_caller_repairs " + mechanism.role + " entry_point must be between 1 and 512 bytes without control characters")
			}
		}
		if len(repair.Edge) < 1 || len(repair.Edge) > 256 {
			return invalidWorkerPayload("worker.completed direct_caller_repairs edge must be between 1 and 256 bytes")
		}
		if len(repair.Exposing) < 1 || len(repair.Exposing) > 512 {
			return invalidWorkerPayload("worker.completed direct_caller_repairs exposing request must be between 1 and 512 bytes")
		}
		if len(repair.ChangedPaths) < 1 || len(repair.ChangedPaths) > WorkerCallerRepairPathsMax {
			return invalidWorkerPayload("worker.completed direct_caller_repairs must carry between 1 and " + fmt.Sprint(WorkerCallerRepairPathsMax) + " changed paths")
		}
		changed := map[string]bool{}
		for _, path := range repair.ChangedPaths {
			if problem := normalizedRelativeRepoPathProblem("worker.completed direct_caller_repairs changed paths", path); problem != "" {
				return invalidWorkerPayload(problem)
			}
			if changed[path] {
				return invalidWorkerPayload("worker.completed direct_caller_repairs names changed path " + path + " twice")
			}
			changed[path] = true
		}
		for _, check := range []struct {
			role  string
			check WorkerCallerRepairCheck
		}{
			{"baseline_pass", repair.Checks.BaselinePass},
			{"owner_only_fail", repair.Checks.OwnerOnlyFail},
			{"combined_pass", repair.Checks.CombinedPass},
		} {
			if len(check.check.Observed) < 1 || len(check.check.Observed) > 512 {
				return invalidWorkerPayload("worker.completed direct_caller_repairs " + check.role + " must carry its observed output reference of 1 to 512 bytes")
			}
			if len(check.check.InferredCause) > 512 {
				return invalidWorkerPayload("worker.completed direct_caller_repairs " + check.role + " inferred cause must be at most 512 bytes")
			}
		}
		if repair.GeneratorRef != nil && (len(repair.GeneratorRef) < 1 || len(repair.GeneratorRef) > 8) {
			return invalidWorkerPayload("worker.completed direct_caller_repairs generator_ref must name between 1 and 8 inputs when present")
		}
		for _, generator := range repair.GeneratorRef {
			if !changed[generator] {
				return invalidWorkerPayload("worker.completed direct_caller_repairs names generator_ref input " + generator + " outside its changed paths; a generated output changes only through its owning input among the changed paths")
			}
		}
	}
	return nil
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
	// ConsumedContextFrontier is the core-derived frontier attribution
	// The field is the frontier the attempt's own dispatch authorization
	// recorded, derived by the terminal ingest owner even when the failure
	// carries no report at all. A failed terminal records no delivery
	// observation and never probes Git. Nil means the authorization
	// predates the field and keeps explicit absence.
	ConsumedContextFrontier *int64 `json:"consumed_context_frontier,omitempty"`
	// FailureAttribution is the typed failure attribution of the terminal
	// failure : what the observing boundary saw, beside the
	// core-derived identity the store derives from the attempt's own
	// dispatch authorization at ingest. The producer supplies the
	// observation only; origin and identity are core-owned. Absent on every
	// payload that predates the attribution version, and on terminals
	// recorded without an observation — a replayed history reads those
	// through the upcaster's explicit legacy_unavailable marker.
	FailureAttribution *WorkerFailureAttribution `json:"failure_attribution,omitempty"`
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
	if !workerMatchedSchemaVersions[payload.ReportSchemaVersion] {
		return invalidWorkerPayload("worker.completed payload has invalid identity or report schema")
	}
	if payload.WorkerJob != nil && !workerJobCapableSchemaVersions[payload.ReportSchemaVersion] {
		return invalidWorkerPayload("worker.completed worker_job requires the job-capable report schema " + WorkerReportSchemaVersionPrior + " or " + WorkerReportSchemaVersion)
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
	if err := validateWorkerObservedDelivery(payload.ObservedDelivery); err != nil {
		return err
	}
	if err := validateWorkerProductionSize(payload.ProductionSize); err != nil {
		return err
	}
	if err := validateWorkerDirectCallerRepairs(payload.DirectCallerRepairs); err != nil {
		return err
	}
	if payload.ConsumedContextFrontier != nil && *payload.ConsumedContextFrontier < 0 {
		return invalidWorkerPayload("worker.completed consumed_context_frontier must be a nonnegative frontier")
	}
	if err := ValidateWorkerOracleReportShape(payload); err != nil {
		return err
	}
	return validateWorkerReportEvidence(payload.EvidenceOrigin, payload.Evidence)
}

// workerSchemaVersionFault names the boundary violation of one dispatch's
// recorded packet/report schema identities against its worker-job binding
// (CD-0205): every dispatch records one matched released pair — 1.0/1.0
// (pre-job), 1.1/1.1 (job-capable), or the current card-free 2.0/2.0 — and a
// job binding is reserved for the job-capable revisions, so the released 1.0
// pair never carries a job. An empty return means the coupling holds.
func workerSchemaVersionFault(packetVersion, reportVersion string, jobBound bool) string {
	matched := packetVersion == reportVersion && workerMatchedSchemaVersions[packetVersion]
	switch {
	case !matched:
		return "packet and report schema versions must be the matched pair " + WorkerPacketSchemaVersion + " (current), " + WorkerPacketSchemaVersionPrior + ", or " + WorkerPacketSchemaVersionLegacy
	case jobBound && !workerJobCapableSchemaVersions[packetVersion]:
		return "worker_job requires the job-capable packet and report schema " + WorkerPacketSchemaVersionPrior + " or " + WorkerPacketSchemaVersion
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
		if err := validateWorkerReviewFinding(finding); err != nil {
			return err
		}
	}
	for _, finding := range review.Findings {
		if review.Verdict == "ship" && workerReviewShipBlockerSeverities[finding.Severity] {
			return invalidWorkerPayload("worker.completed review with a ship verdict cannot carry a P0 finding")
		}
	}
	return validateWorkerReviewOracleVerdict(review)
}

func validateWorkerReviewFinding(finding WorkerReviewFinding) error {
	if !workerReviewSeverityVocabulary[finding.Severity] {
		return invalidWorkerPayload("worker.completed review finding severity must be P0, P1, P2, or P3")
	}
	if !workerReviewConfidenceVocabulary[finding.Confidence] {
		return invalidWorkerPayload("worker.completed review finding confidence must be low, medium, or high")
	}
	if len(finding.Detail) < 1 || len(finding.Detail) > 512 {
		return invalidWorkerPayload("worker.completed review finding detail must be between 1 and 512 UTF-8 bytes")
	}
	return nil
}

// validateWorkerReviewOracleVerdict couples the verdict to oracle
// classifications. Severity and classification are different dimensions, and
// both couplings hold on any report whose findings carry oracle ties —
// exactly the oracle-bound dispatches, where every finding must. A ship
// never carries a classified blocker; a no_ship justified only by
// sub-P0 follow-ups is an inconsistent verdict, while an out-of-scope
// P0 follow-up stays a valid retained no_ship for the decision owner.
func validateWorkerReviewOracleVerdict(review *WorkerReviewBlock) error {
	oracleFindings := false
	shipBlocker := false
	onlySubP0FollowUps := true
	for _, finding := range review.Findings {
		if finding.Oracle != nil {
			oracleFindings = true
			shipBlocker = shipBlocker || oracleReviewBlockerClasses[finding.Oracle.Classification]
		}
		if finding.Oracle == nil || finding.Oracle.Classification != OracleClassificationFollowUp || finding.Severity == "P0" {
			onlySubP0FollowUps = false
		}
	}
	if !oracleFindings {
		return nil
	}
	if review.Verdict == "ship" && shipBlocker {
		return invalidWorkerPayload("worker.completed review with a ship verdict cannot carry a classified delivery blocker, uncovered case, or oracle defect")
	}
	if review.Verdict == "no_ship" && onlySubP0FollowUps {
		return invalidWorkerPayload("worker.completed review with a no_ship verdict cannot be justified only by follow-ups below P0; an out-of-scope P0 follow-up stays a valid retained no_ship")
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
	if err := ValidateWorkerContextFindings(payload.ContextFindings); err != nil {
		return err
	}
	// the attribution block carries its closed structural shape on
	// every version. The source-version rules — upcast-generated legacy
	// absence versus forged or smuggled bytes — are the fold's, which reads
	// the recorded source version; this validator runs after the upcast,
	// where that distinction no longer exists.
	return ValidateWorkerFailureAttribution(payload.FailureAttribution)
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
	// the born-failed attribution block carries its closed
	// structural shape; an ordinary dispatch opens no terminal outcome to
	// attribute and refuses the member at every version.
	if payload.Terminal != "failed" && payload.FailureAttribution != nil {
		return invalidWorkerPayload("worker.dispatched failure_attribution is reserved for a born-failed terminal record; an ordinary dispatch records no attribution")
	}
	if err := ValidateWorkerFailureAttribution(payload.FailureAttribution); err != nil {
		return err
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
	WorkID       string
	StepID       string
	AttemptID    string
	AttemptEpoch int64
	StartSeq     int64
	PacketDigest string
	// SubjectCommit is the admitted core subject the authorization
	// recorded, empty when the authorization predates the field.
	SubjectCommit    string
	WorktreeIdentity string
	// Original native claim provenance; empty below its payload boundary.
	ProjectID string
	ClaimOpID string
	// ContextFrontier is the recorded worker_context_frontier of the
	// dispatch completion: the source-event frontier of the
	// work-context view the authorization admitted. ContextFrontierPresent
	// distinguishes a recorded zero (a new authorization over an absent
	// view) from explicit absence (a historical authorization recorded
	// before the field existed); the reader never substitutes one for the
	// other.
	ContextFrontier        int64
	ContextFrontierPresent bool
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
	return findAuthorizedDispatchWindowCore(ctx, tx, workID, attemptID)
}

// readAuthorizedDispatchWindowBefore is the raw SQL read both window readers
// share: exclusive at beforeSeq (0 reads the current prefix), the nullable
// frontier keeps explicit absence, and the completion epoch returns beside the window.
func readAuthorizedDispatchWindowBefore(ctx context.Context, q queryer, workID, attemptID string, beforeSeq int64) (WorkerDispatchWindow, int64, error) {
	var window WorkerDispatchWindow
	var frontier sql.NullInt64
	var completionEpoch, startEpoch, completionVersion int64
	var recordedProjectID, recordedClaimOpID string
	window.WorkID = workID
	window.AttemptID = attemptID
	if err := q.QueryRowContext(ctx, `SELECT seq,payload_version,json_extract(payload,'$.step_id'),COALESCE(json_extract(payload,'$.attempt_epoch'),0),COALESCE(json_extract(payload,'$.worker_packet_digest'),''),COALESCE(json_extract(payload,'$.worker_worktree_identity'),''),COALESCE(json_extract(payload,'$.worker_subject_commit'),''),COALESCE(json_extract(payload,'$.worker_project_id'),''),COALESCE(json_extract(payload,'$.worker_claim_op_id'),''),json_extract(payload,'$.worker_context_frontier') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? AND json_extract(payload,'$.worker_attempt_id')=? AND (?=0 OR seq<?) ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, "dispatch_worker", attemptID, beforeSeq, beforeSeq).Scan(&window.StartSeq, &completionVersion, &window.StepID, &completionEpoch, &window.PacketDigest, &window.WorktreeIdentity, &window.SubjectCommit, &recordedProjectID, &recordedClaimOpID, &frontier); err != nil {
		if err == sql.ErrNoRows {
			return window, 0, newFailure(KindUnauthorizedDispatch, "worker_dispatch_window", "no authorized dispatch window exists for this work item bound to this attempt", false, "open a dispatch_worker authorization for this attempt before recording worker evidence")
		}
		return window, 0, wrapFailure(KindUnavailable, "worker_dispatch_window", "cannot read the dispatch authorization window", true, "retry once the database is readable", err)
	}
	window.ContextFrontier, window.ContextFrontierPresent = frontier.Int64, frontier.Valid
	if completionVersion >= workflowActionCompletedDispatchIdentityVersion {
		window.ProjectID, window.ClaimOpID = recordedProjectID, recordedClaimOpID
	}
	// The start anchors the single-use consumption read and carries the step
	// epoch the completion authorized against.
	if err := q.QueryRowContext(ctx, `SELECT seq,COALESCE(json_extract(payload,'$.attempt_epoch'),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? AND json_extract(payload,'$.step_id')=? AND seq<? AND (?=0 OR seq<?) ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionStarted, "dispatch_worker", window.StepID, window.StartSeq, beforeSeq, beforeSeq).Scan(&window.StartSeq, &startEpoch); err != nil {
		if err == sql.ErrNoRows {
			return window, 0, newFailure(KindInvariantViolation, "worker_dispatch_window", "dispatch_worker completed without a starting authorization event", false, "reopen the workflow action against a fresh step epoch")
		}
		return window, 0, wrapFailure(KindUnavailable, "worker_dispatch_window", "cannot read the dispatch authorization start", true, "retry once the database is readable", err)
	}
	window.AttemptEpoch = startEpoch
	if window.SubjectCommit != "" && !worktreeSHAPattern.MatchString(window.SubjectCommit) {
		return window, 0, newFailure(KindInvalidPayload, "worker_dispatch_window", "worker_subject_commit must be one raw commit OID", false, "reconcile the recorded authorization")
	}
	return window, completionEpoch, nil
}

// findAuthorizedDispatchWindowBefore is the strict read: the completion epoch must match the recorded start.
func findAuthorizedDispatchWindowBefore(ctx context.Context, q queryer, workID, attemptID string, beforeSeq int64) (WorkerDispatchWindow, error) {
	window, completionEpoch, err := readAuthorizedDispatchWindowBefore(ctx, q, workID, attemptID, beforeSeq)
	if err != nil {
		return window, err
	}
	if completionEpoch != window.AttemptEpoch {
		return window, newFailure(KindInvariantViolation, "worker_dispatch_window", "dispatch_worker completion attempt_epoch does not match the starting authorization epoch", false, "reopen the workflow action against a fresh step epoch")
	}
	return window, nil
}

// findAuthorizedDispatchWindowCore is the current-prefix read without the strict epoch equality.
func findAuthorizedDispatchWindowCore(ctx context.Context, q queryer, workID, attemptID string) (WorkerDispatchWindow, error) {
	window, _, err := readAuthorizedDispatchWindowBefore(ctx, q, workID, attemptID, 0)
	return window, err
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
	// The live dispatch boundary records only the current card-free
	// pair. An old host that stamps a released 1.0 or 1.1 identity is refused
	// typed before the dispatch can land, so the cutover leaves exactly one
	// live producer; recorded 1.0/1.1 history folds unchanged on replay.
	if !isWorkflowReplay(ctx) && (payload.PacketSchemaVersion != WorkerPacketSchemaVersion || payload.ReportSchemaVersion != WorkerReportSchemaVersion) {
		return newFailure(KindInvalidPayload, "fold_event",
			"worker.dispatched must record the current matched packet and report schema "+WorkerPacketSchemaVersion+", not the retired pair "+payload.PacketSchemaVersion+"/"+payload.ReportSchemaVersion,
			false, "dispatch from a host that records the current lane packet revision")
	}
	if err := validateWorkerDispatchedJob(ctx, tx, event, payload); err != nil {
		return err
	}
	if err := validateWorkerDispatchedAttribution(ctx, tx, event, payload); err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	lifecycleState := "dispatched"
	failureKind, failureDetail := "", ""
	readbackModel := payload.ReadbackModel
	if payload.Terminal == "failed" {
		// Terminal-at-birth dispatch: the evidence row records
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

// validateWorkerDispatchedJob refuses a worker-job binding the payload version
// predates or the dispatch_worker authorization did not bind.
func validateWorkerDispatchedJob(ctx context.Context, tx *sql.Tx, event Event, payload WorkerDispatchedPayload) error {
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
		if err := verifyWorkerDispatchedJobBindingTx(ctx, tx, event, payload.AttemptID, payload.WorkerJob); err != nil {
			return err
		}
	}
	return nil
}

// validateWorkerDispatchedAttribution applies the born-failed attribution
// block rules: its source-version marker, the promoted-definition requirement,
// and the identity join for recorded content. An ordinary dispatch refuses the
// member entirely.
func validateWorkerDispatchedAttribution(ctx context.Context, tx *sql.Tx, event Event, payload WorkerDispatchedPayload) error {
	if err := validateWorkerFailureAttributionSource(payload.FailureAttribution, event.replaySourcePayloadVersion, workerDispatchedAttributionVersion); err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched failure_attribution: "+err.Error(), false, "record the attribution on the current born-failed dispatch payload")
	}
	if payload.Terminal == "failed" {
		// A promoted definition requires the recorded block on a born-failed
		// terminal too: the authorization is being consumed at the
		// attribution-capable version, so a missing block and the upcast's
		// legacy marker are the same refusal and an old source version
		// cannot bypass the requirement. Released pins keep the optional
		// absence they shipped with.
		required, gateErr := workflowFailureAttributionActiveForWorkTx(ctx, tx, event.SubjectID)
		if gateErr != nil {
			return gateErr
		}
		if required && !workerFailureAttributionContent(payload.FailureAttribution) {
			return newFailure(KindInvalidPayload, "fold_event", "the pinned definition requires recorded failure_attribution on this born-failed terminal; record the observation through the authenticated dispatch evidence route", false, "record the born-failed dispatch with its producer observation")
		}
	}
	if workerFailureAttributionContent(payload.FailureAttribution) {
		return validateWorkerDispatchedAttributionContent(ctx, tx, event, payload)
	}
	return nil
}

// validateWorkerDispatchedAttributionContent joins recorded attribution content
// to the exact authorized window, the payload's own lane, and the binding the
// authorization bound.
func validateWorkerDispatchedAttributionContent(ctx context.Context, tx *sql.Tx, event Event, payload WorkerDispatchedPayload) error {
	if payload.Terminal != "failed" {
		return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched failure_attribution is reserved for a born-failed terminal record; an ordinary dispatch records no attribution", false, "record the observation on the terminal outcome that was observed")
	}
	window, windowErr := findAuthorizedDispatchWindowCore(ctx, tx, event.SubjectID, payload.AttemptID)
	if windowErr != nil {
		var failure *Failure
		if failureAs(windowErr, &failure) && failure.Kind == KindUnauthorizedDispatch {
			return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched failure_attribution names no authorized dispatch window for attempt "+payload.AttemptID+"; the recorded identity cannot verify", false, "record the attribution through the authenticated dispatch evidence route")
		}
		return windowErr
	}
	block := workerFailureAttemptIdentity(payload.AttemptID, payload.LaneID, window, payload.WorkerJob)
	if payload.FailureAttribution.Identity == nil || payload.FailureAttribution.Identity.Kind != WorkerFailureIdentityAttempt || payload.FailureAttribution.Identity.Attempt == nil {
		return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched failure_attribution carries recorded content without the core-derived attempt identity", false, "record the attribution through the authenticated dispatch evidence route")
	}
	if mismatch := workerFailureAttemptIdentityMismatch(payload.FailureAttribution.Identity.Attempt, block.Attempt); mismatch != "" {
		return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched failure_attribution identity "+mismatch+" does not equal the attempt's authorized dispatch window", false, "record the attribution through the authenticated dispatch evidence route")
	}
	// A born-failed terminal is the host adapter's record: the worker
	// cannot describe the readback terminal it never reported.
	if payload.FailureAttribution.Observation == nil || payload.FailureAttribution.Observation.Observer != WorkerFailureObserverHostAdapter {
		return newFailure(KindInvalidPayload, "fold_event", "a born-failed terminal records the host adapter's observation; the worker cannot describe the readback terminal it never reported", false, "record the attribution through the authenticated dispatch evidence route")
	}
	if err := workerFailureAbandonmentTagCoupling(payload.TerminalFailureKind, payload.FailureAttribution.Observation); err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched failure_attribution: "+err.Error(), false, "record the attribution through the authenticated dispatch evidence route")
	}
	if payload.FailureAttribution.Identity.Attempt.Subject != nil {
		return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched failure_attribution names an observed subject; a born-failed attempt records no delivery", false, "record the attribution through the authenticated dispatch evidence route")
	}
	return nil
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
	// a born-failed terminal record's attribution observation
	// derives its core identity here, inside the authenticated dispatch
	// transaction. The window is still open at this boundary, so the
	// identity derives from the authorization the dispatch consumes. An
	// ordinary dispatch carries no block and keeps its recorded bytes.
	if payload.Terminal == "failed" && payload.FailureAttribution != nil {
		if err := enrichWorkerBornFailedAttributionTx(ctx, tx, dispatch, &payload); err != nil {
			return nil, err
		}
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, wrapFailure(KindInvalidPayload, "worker_lane_actor_prepare", "cannot encode the born-failed dispatch payload", false, "report the encoding failure", marshalErr)
		}
		dispatch.Payload = raw
	} else if payload.FailureAttribution != nil {
		// An ordinary dispatch records no attribution, at the boundary too.
		return nil, invalidWorkerPayload("worker.dispatched failure_attribution is reserved for a born-failed terminal record")
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
	if err := validateWorkerCompletedSourceVersion(event, payload); err != nil {
		return err
	}
	if err := validateWorkerContextFindingDomainsTx(ctx, tx, event.SubjectID, payload.ContextFindings); err != nil {
		return err
	}
	// The completion's report identity is owned by the attempt row below:
	// the dispatch recorded one matched released pair, and a completion whose
	// claim disagrees with the pair the attempt was dispatched under refuses
	// closed. There is deliberately no global current-revision check here —
	// a recorded pre-cutover attempt finishes under the pair it recorded
	// (retained-report recovery), while no new dispatch can create such a
	// row, because the live dispatch boundary admits only the current pair.
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
	// the session record. Completion requires its reported directory to match an
	// active claim in the dispatch Project, which abandonment also resolves from
	// the recorded authorization. The boundary reads the live host filesystem,
	// which replay neither owns nor can reach:
	// reclaimed worktrees no longer resolve, so replay trusts the recorded
	// dispatch evidence instead.
	if !isWorkflowReplay(ctx) {
		if err := validateWorkerCompletionWorktree(ctx, tx, event.SubjectID, payload.AttemptID, payload.WorkerDirectory); err != nil {
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
	if err := validateWorkerCompletedOracleJoin(ctx, tx, event, attempt.WorkID, payload, dispatchedJob); err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if err := validateWorkerCompletedLaneEvidence(ctx, tx, event, attempt, payload, dispatchedJob); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET readback_model=?, lifecycle_state='completed', completed_at=? WHERE attempt_id=? AND work_id=? AND lifecycle_state='dispatched'`, payload.ReadbackModel, now, payload.AttemptID, attempt.WorkID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot complete worker attempt projection", true, "retry once the database is writable", err)
	}
	if err := verifyWorkerTerminalUpdate(result, "cannot verify worker completion projection", "record worker.dispatched before worker.completed"); err != nil {
		return err
	}
	// A completed no_ship verdict voids its confirming review's repair
	// (CD-0205 D4): the named revisions lose their provisional readiness at
	// the strict prefix of this completion. A completed ship report keeps
	// waiting for its acceptance and reverts nothing.
	if payload.Review != nil && payload.Review.Verdict == "no_ship" {
		if err := voidWorkerJobRepairByAttemptTx(ctx, tx, event.SubjectID, payload.AttemptID, event.Seq-1); err != nil {
			return err
		}
	}
	return nil
}

// validateWorkerCompletedSourceVersion refuses completion content a replayed
// event's recorded source payload version predates: worker_job, context
// findings, oracle report content, the terminal delivery identity, and the
// job's size members. Oracle report content also requires the dispatched
// worker-job revision.
func validateWorkerCompletedSourceVersion(event Event, payload WorkerCompletedPayload) error {
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
	// The typed oracle report members are reserved for the
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
	// The core-derived delivery identity is reserved for the terminal
	// delivery payload version. A replayed event whose recorded
	// source version sits below the boundary cannot carry a consumed
	// frontier or an observed delivery: the store never derived either when
	// that version was released, and recorded history is never re-observed.
	if (payload.ConsumedContextFrontier != nil || payload.ObservedDelivery != nil) && event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < workerCompletedTerminalDeliveryVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.completed consumed_context_frontier and observed_delivery are reserved for payload version >= 7", false, "record the terminal delivery identity on the current completion payload")
	}
	// The job's measured size surface is reserved for the size payload
	// version: the observed delivery's base and per-path counts, and the
	// report's production-size members beside them. A replayed event whose
	// recorded source version sits below the boundary cannot carry those
	// members — the store never observed a measured delta or folded a
	// production-size report when that version was released, so any bytes
	// that name them are fabricated size content no store ever recorded.
	if payload.ObservedDelivery != nil && (payload.ObservedDelivery.Base != "" || payload.ObservedDelivery.Paths != nil) && event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < workerCompletedJobSizeVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.completed observed_delivery base and paths are reserved for payload version >= 8", false, "record the measured delta on the current completion payload")
	}
	if (payload.ProductionSize != nil || payload.DirectCallerRepairs != nil) && event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < workerCompletedJobSizeVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.completed production_size and direct_caller_repairs are reserved for payload version >= 8", false, "record the size report on the current completion payload")
	}
	return nil
}

// validateWorkerCompletedOracleJoin is the live oracle join. The dispatched
// immutable job is the one authority: a report carrying oracle content must
// have been dispatched under an oracle-bearing revision, every review finding
// of an oracle-bound dispatch carries a classification, and every receipt,
// finding tie, and closure claim joins that revision's recorded oracle and the
// prior ranked-finding lineage. Replay trusts the recorded log — the live fold
// already passed these checks when it landed — and oracle-free dispatches keep
// their legacy reviews.
func validateWorkerCompletedOracleJoin(ctx context.Context, tx *sql.Tx, event Event, workID string, payload WorkerCompletedPayload, dispatchedJob *WorkerJobBinding) error {
	if !isWorkflowReplay(ctx) && (hasOracleReportContent(payload) || payload.Review != nil) {
		var oracle *AcceptanceOracle
		if dispatchedJob != nil {
			read, oracleErr := readWorkerJobOracle(ctx, tx, workID, *dispatchedJob)
			if oracleErr != nil {
				return oracleErr
			}
			oracle = read
		}
		if hasOracleReportContent(payload) && oracle == nil {
			return oracleFindingFailure(KindInvalidPayload, "a report carrying oracle content was dispatched under a job revision that records no oracle", "report oracle content only under an oracle-bearing revision")
		}
		if oracle != nil {
			lineage, lineageErr := readWorkerOracleFindingLineageExcludingTx(ctx, tx, workID, event.Seq)
			if lineageErr != nil {
				return lineageErr
			}
			if err := validateWorkerOracleCompletedReportTx(ctx, tx, workID, payload, oracle, lineage); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateWorkerCompletedLaneEvidence enforces the dispatching lane's
// evidence coverage, report-block requirement, and predicate ties. CD-0056 D4:
// the fold is the only point where the attempt's lane identity and the
// reported evidence are both in hand, so coverage is enforced inside the
// transaction that would make the attempt terminal.
func validateWorkerCompletedLaneEvidence(ctx context.Context, tx *sql.Tx, event Event, attempt workerTerminalAttempt, payload WorkerCompletedPayload, dispatchedJob *WorkerJobBinding) error {
	live := !isWorkflowReplay(ctx)
	if payload.EvidenceOrigin == WorkerEvidenceReported || live {
		lane, err := LookupLane(attempt.LaneID, attempt.LaneVersion, attempt.LaneDigest)
		if err != nil {
			return err
		}
		// The report's size members bind the dispatched job's recorded
		// measurement (CD-0225 D2, CD-0227 D2). The live fold holds every
		// completion to it: a capped implementation job's report carries
		// production_size bound to the job's base and the observed tree —
		// a missing member is malformed output — and every other report
		// carries neither member. Replay trusts the recorded log: the live
		// fold already held it when it landed.
		if live {
			if err := bindWorkerCompletedReportSizeTx(ctx, tx, attempt, dispatchedJob, lane, event.replaySourcePayloadVersion, payload); err != nil {
				return err
			}
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
		if payload.EvidenceOrigin == WorkerEvidenceReported {
			if err := verifyWorkerCompletedPredicateTies(ctx, tx, attempt.WorkID, payload, dispatchedJob, live); err != nil {
				return err
			}
		}
	}
	return nil
}

// verifyWorkerCompletedPredicateTies checks reported predicate ties. The
// recorded dispatch owns the predicate vocabulary: the typed
// outcome predicate ids it carried are read from the dispatch
// authorization event, never re-derived from the report, so a report
// can tie only predicates the dispatch declared. Replay trusts the
// recorded completion: history folded before this check may carry ties
// the live fold now refuses, or a dispatch recorded before the
// predicate list existed.
//
// The job-subset tie rule: on a promoted pin whose attempt was dispatched under
// a job revision, the permitted tie list is that exact recorded job
// revision's own predicate list, and the membership check runs with
// the same SQL on the live fold and on rebuild. The parent packet
// list stays the context and keeps its released live-only behavior
// on every older pin and on every job-free attempt.
func verifyWorkerCompletedPredicateTies(ctx context.Context, tx *sql.Tx, workID string, payload WorkerCompletedPayload, dispatchedJob *WorkerJobBinding, live bool) error {
	tiesActive, tiesErr := workflowFailureAttributionActiveForWorkTx(ctx, tx, workID)
	if tiesErr != nil {
		return tiesErr
	}
	if tiesActive && dispatchedJob != nil {
		declared, declaredErr := workerJobRevisionPredicateIDsTx(ctx, tx, workID, *dispatchedJob)
		if declaredErr != nil {
			return declaredErr
		}
		if err := verifyWorkerPredicateTies(declared, payload.Evidence); err != nil {
			return err
		}
	} else if live {
		declared, err := dispatchedPacketPredicateIDsTx(ctx, tx, workID, payload.AttemptID)
		if err != nil {
			return err
		}
		if err := verifyWorkerPredicateTies(declared, payload.Evidence); err != nil {
			return err
		}
	}
	return nil
}

// bindWorkerCompletedReportSizeTx is the live fold's size binding (CD-0225
// D2, CD-0227 D2): a completed implementation attempt whose dispatched job
// records a measurement basis must report production_size with that
// recorded base — a missing member is malformed output — and with the
// observed delivered tree whenever the core sealed an observation. A
// payload version below the size boundary cannot carry the member, so it
// owes none. Every other completion carries neither size member. A capped
// completion without a sealed observation has no measured delta, which
// acceptance refuses as unavailable evidence.
func bindWorkerCompletedReportSizeTx(ctx context.Context, tx *sql.Tx, attempt workerTerminalAttempt, dispatchedJob *WorkerJobBinding, lane LaneDefinition, sourceVersion int, payload WorkerCompletedPayload) error {
	basisBase, err := workerJobBasisBase(ctx, tx, attempt.WorkID, dispatchedJob)
	if err != nil {
		return err
	}
	if basisBase != "" && lane.CapabilityClass == "implementation" {
		if sourceVersion != 0 && sourceVersion < workerCompletedJobSizeVersion {
			return nil
		}
		if payload.ProductionSize == nil {
			return invalidWorkerPayload("worker.completed production_size is required for a completed report of a capped implementation job: missing production_size is malformed output")
		}
		if payload.ProductionSize.Base != basisBase {
			return invalidWorkerPayload("worker.completed production_size base " + payload.ProductionSize.Base + " does not equal the dispatched job's recorded base_commit " + basisBase)
		}
		if payload.ObservedDelivery != nil && payload.ProductionSize.Tree != payload.ObservedDelivery.Commit {
			return invalidWorkerPayload("worker.completed production_size tree must equal the observed delivery commit")
		}
		return nil
	}
	if payload.ProductionSize != nil || payload.DirectCallerRepairs != nil {
		return invalidWorkerPayload("worker.completed production_size and direct_caller_repairs are reserved for a capped implementation job's report; an uncapped job or another lane carries neither member")
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
	// The core-derived frontier attribution is reserved for the failed
	// terminal delivery payload version. A replayed event whose
	// recorded source version sits below the boundary cannot carry a consumed
	// frontier: the store never derived one when that version was released,
	// and recorded history is never re-attributed.
	if payload.ConsumedContextFrontier != nil && event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < workerFailedTerminalDeliveryVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.failed consumed_context_frontier is reserved for payload version >= 3", false, "record the frontier attribution on the current failure payload")
	}
	// the source-version marker rules apply before anything else
	// about the block. Below the attribution version only the upcast's
	// legacy marker may ride the event; recorded content there is smuggled
	// bytes no store ever recorded. At or above the version the marker
	// itself is a forged new-legacy claim.
	if err := validateWorkerFailureAttributionSource(payload.FailureAttribution, event.replaySourcePayloadVersion, workerFailedAttributionVersion); err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "worker.failed failure_attribution: "+err.Error(), false, "record the attribution block on the current failure payload")
	}
	// A promoted definition requires the recorded block on every failed
	// terminal: a missing block and the upcast's legacy marker are the same
	// refusal, so an old source version cannot bypass the requirement by
	// arriving without attribution bytes. The pin is the one the active
	// workflow instance recorded — it cannot have changed retroactively —
	// and every released pin keeps the optional absence it shipped with.
	required, gateErr := workflowFailureAttributionActiveForWorkTx(ctx, tx, event.SubjectID)
	if gateErr != nil {
		return gateErr
	}
	if required && !workerFailureAttributionContent(payload.FailureAttribution) {
		return newFailure(KindInvalidPayload, "fold_event", "the pinned definition requires recorded failure_attribution on this failed terminal; record the observation through the authenticated ingest", false, "record the failure with its producer observation")
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
	// a terminal carrying recorded attribution content verifies its
	// identity against the same authorization owners the ingest derived it
	// from — identical SQL on the live fold and on rebuild.
	if workerFailureAttributionContent(payload.FailureAttribution) {
		if err := verifyWorkerFailureAttemptAttributionTx(ctx, tx, event.SubjectID, payload.AttemptID, attempt.LaneID, payload.FailureKind, payload.FailureAttribution); err != nil {
			return newFailure(KindInvalidPayload, "fold_event", "worker.failed failure_attribution: "+err.Error(), false, "record the attribution through the authenticated terminal ingest")
		}
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
	// A failed or abandoned confirming review voids its repair (CD-0205
	// D4): the named revisions lose their provisional readiness at the
	// strict prefix of this terminal.
	if err := voidWorkerJobRepairByAttemptTx(ctx, tx, event.SubjectID, payload.AttemptID, event.Seq-1); err != nil {
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
	projectID, err := workerAttemptProjectTx(ctx, tx, workID, attemptID, "worker_fail", "restore the attempt's recorded worktree claim before abandonment")
	if err != nil {
		return err
	}
	// Retained dispatch identity cannot prove the live occupancy projection
	// exists: an absent entry would disappear from the occupancy join below.
	if projectID != "" {
		var present bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_entries WHERE set_id=? AND project_id=?)`, WorktreeSetID(workID), projectID).Scan(&present); err != nil {
			return wrapFailure(KindUnavailable, "worker_fail", "cannot read worker Project ownership", true, "retry once the database is readable", err)
		}
		if !present {
			return newFailure(KindWorktreeOwnershipConflict, "worker_fail", "worker attempt Project has no recorded worktree entry", false, "restore the attempt's recorded worktree claim before abandonment")
		}
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

// workerJobRevisionPredicateIDsTx reads the permitted report evidence tie
// list of one exact recorded job revision: the predicate_ids the retained
// worker.job_recorded event carries under the binding's job, revision, and
// digest. A binding that names no retained recording with that digest is a
// corrupt join and refuses fail-closed, so a missing projection can never
// silently widen the predicates a report may tie.
func workerJobRevisionPredicateIDsTx(ctx context.Context, tx *sql.Tx, workID string, job WorkerJobBinding) ([]string, error) {
	var raw *string
	if err := tx.QueryRowContext(ctx, `SELECT json_extract(payload,'$.predicate_ids') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? AND json_extract(payload,'$.digest')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkerJobRecorded, job.JobID, job.Revision, job.Digest).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, newFailure(KindInvariantViolation, "worker_predicate_discharge", "the dispatched job revision "+job.JobID+":"+strconv.FormatInt(job.Revision, 10)+" names no retained worker.job_recorded recording under its digest", false, "reconcile_operation")
		}
		return nil, wrapFailure(KindUnavailable, "worker_predicate_discharge", "cannot read the recorded job revision predicates", true, "retry once the database is readable", err)
	}
	if raw == nil {
		return nil, newFailure(KindInvariantViolation, "worker_predicate_discharge", "worker.job_recorded recorded no predicate_ids for "+job.JobID+":"+strconv.FormatInt(job.Revision, 10), false, "reconcile_operation")
	}
	var ids []string
	if err := json.Unmarshal([]byte(*raw), &ids); err != nil {
		return nil, newFailure(KindInvariantViolation, "worker_predicate_discharge", "worker.job_recorded recorded malformed predicate_ids for "+job.JobID+":"+strconv.FormatInt(job.Revision, 10), false, "reconcile_operation")
	}
	return ids, nil
}

// workflowFailureAttributionActiveForWorkTx reports whether the work item's
// pinned definition carries the failure-attribution semantics. A work item
// with no workflow instance keeps every released behavior, so the absence
// reads as inactive rather than refusing. The gate reads the pin's ref and
// version alone and never adds a digest authority of its own: the pin's
// digest is the owning preflight and fold checks' strict boundary, and a
// retained pin resolves the structure it names.
func workflowFailureAttributionActiveForWorkTx(ctx context.Context, tx *sql.Tx, workID string) (bool, error) {
	var ref string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, wrapFailure(KindUnavailable, "worker_predicate_discharge", "cannot read the workflow instance", true, "retry once the database is readable", err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		return false, nil
	}
	return workflowFailureAttributionActive(registered.Definition), nil
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
// may carry the typed oracle report members. v5 payloads never
// carried any, so the upcast is the bytes unchanged at the new version: a
// replayed completion stays a report without oracle ties, exactly as the
// worker returned it, and no upcaster fabricates a receipt, a tie, or a
// closure.
func upcastWorkerCompletedV5(event Event) (Event, error) {
	event.PayloadVersion = 6
	return event, nil
}

// upcastWorkerCompletedV6 carries a v6 completion into the v7 payload that
// may carry the core-derived consumed_context_frontier and observed_delivery
// v6 payloads never carried either, so the upcast is the bytes
// unchanged at the new version: a replayed completion stays the report the
// worker returned, with no fabricated frontier and no fabricated delivery
// observation. Recorded history is never re-observed.
func upcastWorkerCompletedV6(event Event) (Event, error) {
	event.PayloadVersion = 7
	return event, nil
}

// upcastWorkerCompletedV7 carries a v7 completion into the v8 payload that
// may carry the job's measured size surface: the observed delivery's base
// and per-path counts, and the report's production-size members beside
// them. v7 payloads never carried either, so the upcast is the bytes
// unchanged at the new version: a replayed completion stays the report the
// store observed, with no fabricated base, no invented counts, and no
// invented size report. Recorded history is never re-observed.
func upcastWorkerCompletedV7(event Event) (Event, error) {
	event.PayloadVersion = 8
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

// upcastWorkerFailedV2 carries a v2 failure into the v3 payload that may
// carry the core-derived consumed_context_frontier. v2 payloads
// never carried one, so the upcast is the bytes unchanged at the new
// version: a replayed failure stays the diagnostic the host recorded, with
// no fabricated frontier attribution.
func upcastWorkerFailedV2(event Event) (Event, error) {
	event.PayloadVersion = 3
	return event, nil
}

// upcastWorkerFailedV3 carries a v3 failure into the v4 payload that may
// carry the typed failure_attribution block . v3 payloads never
// carried one, so the upcast stamps the explicit legacy_unavailable marker:
// a replayed failure stays visibly without attribution instead of reading
// as a shape the store could have recorded. The recorded source version
// keeps the marker distinguishable from a forged new-legacy claim, which
// the fold refuses. Bytes that already carry an attribution member are left
// untouched so the fold's source-version gate — not the upcaster — refuses
// the smuggled content.
func upcastWorkerFailedV3(event Event) (Event, error) {
	if err := workerFailureAuthoredAttributionKeyRefused(event, "worker.failed"); err != nil {
		return Event{}, err
	}
	var payload WorkerFailedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return Event{}, err
	}
	if payload.FailureAttribution == nil {
		payload.FailureAttribution = workerFailureLegacyAttribution()
		raw, err := json.Marshal(payload)
		if err != nil {
			return Event{}, wrapFailure(KindInvalidPayload, "worker_event_upcast", "cannot encode the worker failure payload", false, "repair the worker event payload", err)
		}
		event.Payload = raw
	}
	event.PayloadVersion = 4
	return event, nil
}

// upcastWorkerDispatchedV5 carries a v5 dispatch into the v6 payload that may
// carry the typed failure attribution of a born-failed terminal .
// Only a born-failed terminal records one, so the upcast stamps the explicit
// legacy_unavailable marker there alone: an ordinary dispatch opens no
// terminal outcome to attribute and upcasts unchanged without a block. The
// recorded source version keeps the marker distinguishable from a forged
// new-legacy claim, and bytes that already carry the member stay untouched
// for the fold's source-version gate to refuse.
func upcastWorkerDispatchedV5(event Event) (Event, error) {
	if err := workerFailureAuthoredAttributionKeyRefused(event, "worker.dispatched"); err != nil {
		return Event{}, err
	}
	var payload WorkerDispatchedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return Event{}, err
	}
	if payload.Terminal == "failed" && payload.FailureAttribution == nil {
		payload.FailureAttribution = workerFailureLegacyAttribution()
		raw, err := json.Marshal(payload)
		if err != nil {
			return Event{}, wrapFailure(KindInvalidPayload, "worker_event_upcast", "cannot encode the worker dispatch payload", false, "repair the worker event payload", err)
		}
		event.Payload = raw
	}
	event.PayloadVersion = 6
	return event, nil
}
