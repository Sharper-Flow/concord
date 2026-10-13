package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// The typed failure_attribution block carried on worker terminal and
// coordinator correction records. This file owns the closed types, the byte
// bounds, the canonical observation digest the signed evidence boundary
// authenticates, and the strict decoder. It registers no event kind, folds
// no payload, and wires no workflow route.
//
// The block has two parts: the producer observation, which records what the
// observing boundary saw, and the core-derived identity, which only Concord
// derives from the dispatch window and recorded revisions. A producer never
// authors identity.
//
// Validation here is structural. It never asserts that a locator resolves,
// that a digest matches the record it cites, that a job binding equals the
// revision the dispatch recorded, or that any causal story is true: those
// joins live with the event, attempt, job, and native-run owners. A
// supported basis marks a source-backed assertion whose limits stay
// visible; a typed terminal provider error permits the observed fault as
// evidence and never by itself establishes terminal causality. The observed
// subject is a typed immutable reference to an existing observed-delivery
// event or native run — this file adds no subject-commit owner and copies
// no commit field.

// Byte bounds. A block or observation past a bound is refused whole; causal
// content is never truncated to fit.
const (
	// WorkerFailureAttributionMaxBlockBytes bounds the compact JSON
	// serialization of the whole block.
	WorkerFailureAttributionMaxBlockBytes = 8192
	// WorkerFailureAttributionMaxDecodeBytes bounds raw decoder input
	// independently of canonical content. Excess transport whitespace or
	// escaped spellings may exceed this ceiling even for small content.
	WorkerFailureAttributionMaxDecodeBytes = 16384
	// WorkerFailureAttributionMaxSources bounds the observed source list.
	WorkerFailureAttributionMaxSources = 8
	// WorkerFailureSourceRefMaxBytes bounds one immutable locator.
	WorkerFailureSourceRefMaxBytes = 512
	// WorkerFailureObservedTextMaxBytes bounds one observed message: the
	// exact error, code, or check result the boundary supplied.
	WorkerFailureObservedTextMaxBytes = 2048
	// WorkerFailureDiagnosisMaxBytes bounds the bounded diagnosis text,
	// which is distinct from the exact observation and never replaces an
	// existing correction diagnosis or strategy.
	WorkerFailureDiagnosisMaxBytes = 1024
)

// workerFailureIdentityRefMaxBytes bounds core-derived identity strings and
// workerFailureHostRefMaxBytes bounds typed host observation fields. The
// exact values are pinned by the join validations; these are structural
// guards.
const (
	workerFailureIdentityRefMaxBytes = 256
	workerFailureHostRefMaxBytes     = 256
)

// Observers are closed. The authenticated route sets the observer tag; it
// is never an authority string supplied by a worker.
const (
	WorkerFailureObserverHostAdapter  = "host_adapter"
	WorkerFailureObserverWorkerReport = "worker_report"
	WorkerFailureObserverCoordinator  = "coordinator"
)

// Phases are closed. Only the boundary that observed a phase may emit it.
const (
	WorkerFailurePhaseDispatch         = "dispatch"
	WorkerFailurePhaseHostExecution    = "host_execution"
	WorkerFailurePhaseWorkerExecution  = "worker_execution"
	WorkerFailurePhaseReadback         = "readback"
	WorkerFailurePhaseReportParse      = "report_parse"
	WorkerFailurePhaseReportValidation = "report_validation"
	WorkerFailurePhaseEvidenceWrite    = "evidence_write"
	WorkerFailurePhaseResultReview     = "result_review"
	WorkerFailurePhaseIntegration      = "integration"
)

// Causes are closed. Typed observations classify only what their boundary
// owns; failed tests and missing reports never automatically mean an
// implementation defect.
const (
	WorkerFailureCauseUnknown               = "unknown"
	WorkerFailureCauseAssignmentRequirement = "assignment_requirement"
	WorkerFailureCauseImplementation        = "implementation"
	WorkerFailureCauseDependency            = "dependency"
	WorkerFailureCauseHostProvider          = "host_provider"
	WorkerFailureCauseReportContract        = "report_contract"
	WorkerFailureCauseIdentityProvenance    = "identity_provenance"
)

// Basis states are closed. A hypothesis stays labeled even with citations;
// a supported claim names admissible immutable evidence, and the existence
// of a locator alone never establishes causality.
const (
	WorkerFailureBasisUnknown    = "unknown"
	WorkerFailureBasisHypothesis = "hypothesis"
	WorkerFailureBasisSupported  = "supported"
)

// Source kinds are the closed variants of the observed diagnostic: a host
// terminal/error observation, admitted failed-report evidence, a validator
// error with location, an existing result/review finding reference, or a
// native run/control result.
const (
	WorkerFailureSourceHostEvent      = "host_event"
	WorkerFailureSourceAdmittedReport = "admitted_report"
	WorkerFailureSourceValidator      = "validator"
	WorkerFailureSourceResult         = "result"
	WorkerFailureSourceNativeRun      = "native_run"
)

// Host observation tags distinguish a typed terminal error from a held
// retry and from a bare cancellation.
const (
	WorkerFailureHostTagTerminal     = "terminal"
	WorkerFailureHostTagRetry        = "retry"
	WorkerFailureHostTagCancellation = "cancellation"
)

// Subject reference kinds name an existing recorded subject: the terminal
// event that carries the core-observed observed delivery, or a retained
// native run. The joins resolve the reference; nothing here copies a
// commit.
const (
	WorkerFailureSubjectObservedDeliveryEvent = "observed_delivery_event"
	WorkerFailureSubjectNativeRun             = "native_run"
)

// Identity kinds close the attempt/integration union.
const (
	WorkerFailureIdentityAttempt     = "attempt"
	WorkerFailureIdentityIntegration = "integration"
)

// Attribution origins are closed. WorkerFailureAttributionLegacyUnavailable
// shares the legacy_unavailable token with the completion evidence origin by
// design but is a distinct field: it belongs only to this block, never feeds
// evidence coverage or result admission.
const (
	WorkerFailureAttributionRecorded          = "recorded"
	WorkerFailureAttributionLegacyUnavailable = "legacy_unavailable"
)

// workerFailureObserverPhases is the closed observer/phase coupling. The
// host adapter boundary (the adapter and its signed CLI evidence route)
// owns dispatch, host execution, readback, report parse and validation, and
// evidence write; the worker report observes only its own execution; the
// coordinator observes result review and integration.
var workerFailureObserverPhases = map[string]map[string]bool{
	WorkerFailureObserverHostAdapter: {
		WorkerFailurePhaseDispatch:         true,
		WorkerFailurePhaseHostExecution:    true,
		WorkerFailurePhaseReadback:         true,
		WorkerFailurePhaseReportParse:      true,
		WorkerFailurePhaseReportValidation: true,
		WorkerFailurePhaseEvidenceWrite:    true,
	},
	WorkerFailureObserverWorkerReport: {
		WorkerFailurePhaseWorkerExecution: true,
	},
	WorkerFailureObserverCoordinator: {
		WorkerFailurePhaseResultReview: true,
		WorkerFailurePhaseIntegration:  true,
	},
}

// workerFailureObserverSourceKinds is the closed observer/source coupling:
// an observation may cite only the source kinds its boundary owns.
var workerFailureObserverSourceKinds = map[string]map[string]bool{
	WorkerFailureObserverHostAdapter: {
		WorkerFailureSourceHostEvent: true,
		WorkerFailureSourceValidator: true,
	},
	WorkerFailureObserverWorkerReport: {
		WorkerFailureSourceAdmittedReport: true,
	},
	WorkerFailureObserverCoordinator: {
		WorkerFailureSourceResult:    true,
		WorkerFailureSourceNativeRun: true,
	},
}

var workerFailureCauseVocabulary = map[string]bool{
	WorkerFailureCauseUnknown:               true,
	WorkerFailureCauseAssignmentRequirement: true,
	WorkerFailureCauseImplementation:        true,
	WorkerFailureCauseDependency:            true,
	WorkerFailureCauseHostProvider:          true,
	WorkerFailureCauseReportContract:        true,
	WorkerFailureCauseIdentityProvenance:    true,
}

var workerFailureSourceKindVocabulary = map[string]bool{
	WorkerFailureSourceHostEvent:      true,
	WorkerFailureSourceAdmittedReport: true,
	WorkerFailureSourceValidator:      true,
	WorkerFailureSourceResult:         true,
	WorkerFailureSourceNativeRun:      true,
}

var workerFailureHostTagVocabulary = map[string]bool{
	WorkerFailureHostTagTerminal:     true,
	WorkerFailureHostTagRetry:        true,
	WorkerFailureHostTagCancellation: true,
}

var workerFailureSubjectKindVocabulary = map[string]bool{
	WorkerFailureSubjectObservedDeliveryEvent: true,
	WorkerFailureSubjectNativeRun:             true,
}

var workerFailureIdentityKindVocabulary = map[string]bool{
	WorkerFailureIdentityAttempt:     true,
	WorkerFailureIdentityIntegration: true,
}

// workerFailurePhaseVocabulary is the closed phase set, separate from the
// coupling table so an unknown phase reports as unknown rather than as a
// coupling violation.
var workerFailurePhaseVocabulary = map[string]bool{
	WorkerFailurePhaseDispatch:         true,
	WorkerFailurePhaseHostExecution:    true,
	WorkerFailurePhaseWorkerExecution:  true,
	WorkerFailurePhaseReadback:         true,
	WorkerFailurePhaseReportParse:      true,
	WorkerFailurePhaseReportValidation: true,
	WorkerFailurePhaseEvidenceWrite:    true,
	WorkerFailurePhaseResultReview:     true,
	WorkerFailurePhaseIntegration:      true,
}

func workerFailureBoundFailure(detail string) error {
	return newFailure(KindInvalidPayload, "worker_failure_attribution", detail, false, "repair the failure attribution block")
}

// workerFailureBoundedText enforces one bounded UTF-8 string field.
func workerFailureBoundedText(name, value string, minimum, maximum int) error {
	if !utf8.ValidString(value) {
		return workerFailureBoundFailure("failure_attribution " + name + " is not valid UTF-8")
	}
	if len(value) < minimum || len(value) > maximum {
		if minimum == 0 {
			return workerFailureBoundFailure("failure_attribution " + name + " must be at most " + strconv.Itoa(maximum) + " UTF-8 bytes")
		}
		return workerFailureBoundFailure("failure_attribution " + name + " must be between " + strconv.Itoa(minimum) + " and " + strconv.Itoa(maximum) + " UTF-8 bytes")
	}
	return nil
}

// WorkerFailureHostObservation is the typed host observation a host_event
// source carries: which host event was seen, for which session and message,
// and the typed terminal error fields when the host supplied them. The tag
// distinguishes a terminal error from a held retry and from a bare
// cancellation; a retry keeps its error fields but never carries terminal
// causality, and a cancellation carries none at all.
type WorkerFailureHostObservation struct {
	Tag        string `json:"tag"`
	EventType  string `json:"event_type"`
	SessionID  string `json:"session_id,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	ErrorName  string `json:"error_name,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
}

// WorkerFailureSource is one entry of the observed structured diagnostic:
// the closed variant tag, the immutable locator of the observed record, the
// content digest where one is available, and the exact observed message
// where the boundary supplied one. A source whose host event ID does not
// exist records the fields actually supplied and a content digest, never an
// invented locator.
type WorkerFailureSource struct {
	Kind    string                        `json:"kind"`
	Locator string                        `json:"locator,omitempty"`
	Digest  string                        `json:"digest,omitempty"`
	Message string                        `json:"message,omitempty"`
	Host    *WorkerFailureHostObservation `json:"host,omitempty"`
}

// WorkerFailureObservation is the producer observation: which boundary
// observed which phase, what it saw, the causal classification it claims,
// and the basis that claim rests on. The worker's report is one observation
// among these, never host or native authority, and its basis can never be
// supported. Sources serialize in slice order; the order is the order the
// boundary observed, no canonicalization sorts it, and reordering changes
// the canonical digest.
type WorkerFailureObservation struct {
	Observer  string                `json:"observer"`
	Phase     string                `json:"phase"`
	Cause     string                `json:"cause"`
	Basis     string                `json:"basis"`
	Sources   []WorkerFailureSource `json:"sources"`
	Diagnosis string                `json:"diagnosis,omitempty"`
}

// WorkerFailureSubjectRef is a typed immutable reference to an existing
// recorded subject: the observed-delivery event or the native run. It is a
// locator the joins resolve, never a copied commit field and never a
// trusted producer string.
type WorkerFailureSubjectRef struct {
	Kind    string `json:"kind"`
	Locator string `json:"locator"`
	Digest  string `json:"digest,omitempty"`
}

// WorkerFailureAttemptIdentity is the core-derived identity of one
// authorized worker attempt: the registered lane, the positive attempt
// epoch, and the packet digest the dispatch authorization recorded, plus
// exactly one of the explicit no_job marker or the recorded job revision. A
// missing subject is legal — a pre-launch attempt has no child session or
// subject — but a missing authorizing identity is not.
type WorkerFailureAttemptIdentity struct {
	AttemptID    string                   `json:"attempt_id"`
	LaneID       string                   `json:"lane_id"`
	Epoch        int64                    `json:"epoch"`
	PacketDigest string                   `json:"packet_digest"`
	Job          *WorkerJobBinding        `json:"job,omitempty"`
	NoJob        bool                     `json:"no_job,omitempty"`
	Subject      *WorkerFailureSubjectRef `json:"subject,omitempty"`
}

// WorkerFailureInputResult is one entry of the integration input
// population: the exact accepted job revision and its result reference, in
// event order, each at most once. No worker count, timestamp pair, or
// latest-job lookup substitutes for this population.
type WorkerFailureInputResult struct {
	Job       *WorkerJobBinding `json:"job"`
	ResultRef string            `json:"result_ref"`
}

// WorkerFailureIntegrationIdentity is the core-derived identity of a
// combined integration run: the native run that failed and the exact
// accepted input population. An integration observation carries this
// identity, not a fictitious single worker attempt.
type WorkerFailureIntegrationIdentity struct {
	NativeRun    WorkerFailureSubjectRef    `json:"native_run"`
	InputResults []WorkerFailureInputResult `json:"input_results"`
}

// WorkerFailureIdentity is the closed attempt/integration union of
// core-derived identity. Exactly one variant is present, it matches Kind,
// and its kind agrees with the observation's phase.
type WorkerFailureIdentity struct {
	Kind        string                            `json:"kind"`
	Attempt     *WorkerFailureAttemptIdentity     `json:"attempt,omitempty"`
	Integration *WorkerFailureIntegrationIdentity `json:"integration,omitempty"`
}

// WorkerFailureAttribution is the typed failure_attribution block. A
// recorded block carries the producer observation and the core-derived
// identity; the explicit legacy_unavailable marker carries neither and
// keeps a historical record visibly legacy. The block is optional at the
// payload level: whether a payload version requires it is that version's
// registration.
type WorkerFailureAttribution struct {
	Origin      string                    `json:"origin"`
	Observation *WorkerFailureObservation `json:"observation,omitempty"`
	Identity    *WorkerFailureIdentity    `json:"identity,omitempty"`
}

func workerFailureDigestPattern(name, value string) error {
	if !workerProvenancePattern.MatchString(value) {
		return workerFailureBoundFailure("failure_attribution " + name + " must be one sha256:<64 hex> content digest")
	}
	return nil
}

// validateWorkerFailureHostObservation enforces one typed host observation:
// closed tag, the event type that carried it, bounded identity fields, an
// HTTP-shaped status code when one is present, and no error identity on a
// cancellation.
func validateWorkerFailureHostObservation(host *WorkerFailureHostObservation) error {
	if host == nil {
		return workerFailureBoundFailure("failure_attribution host_event source requires the typed host observation")
	}
	if !workerFailureHostTagVocabulary[host.Tag] {
		return workerFailureBoundFailure("failure_attribution host observation tag must be terminal, retry, or cancellation")
	}
	if err := workerFailureBoundedText("host event_type", host.EventType, 1, workerFailureHostRefMaxBytes); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"host session_id", host.SessionID},
		{"host message_id", host.MessageID},
		{"host error_name", host.ErrorName},
		{"host error_code", host.ErrorCode},
	} {
		if err := workerFailureBoundedText(field.name, field.value, 0, workerFailureHostRefMaxBytes); err != nil {
			return err
		}
	}
	if host.StatusCode != 0 && (host.StatusCode < 100 || host.StatusCode > 599) {
		return workerFailureBoundFailure("failure_attribution host status_code must be absent or an HTTP-shaped status in [100,599]")
	}
	if host.Tag == WorkerFailureHostTagCancellation && (host.ErrorName != "" || host.ErrorCode != "" || host.StatusCode != 0) {
		return workerFailureBoundFailure("a cancellation observation records the absence case and carries no error identity; record the fault as a terminal observation instead")
	}
	return nil
}

// validateWorkerFailureSource enforces one observed source entry: closed
// kind, the typed host observation on host_event sources and nowhere else,
// byte bounds, digest shape, and at least one recorded field.
func validateWorkerFailureSource(source WorkerFailureSource) error {
	if !workerFailureSourceKindVocabulary[source.Kind] {
		return workerFailureBoundFailure("failure_attribution source kind must be host_event, admitted_report, validator, result, or native_run")
	}
	if source.Kind == WorkerFailureSourceHostEvent {
		if err := validateWorkerFailureHostObservation(source.Host); err != nil {
			return err
		}
	} else if source.Host != nil {
		return workerFailureBoundFailure("the typed host observation belongs to host_event sources only")
	}
	if err := workerFailureBoundedText("source locator", source.Locator, 0, WorkerFailureSourceRefMaxBytes); err != nil {
		return err
	}
	if source.Digest != "" {
		if err := workerFailureDigestPattern("source digest", source.Digest); err != nil {
			return err
		}
	}
	if err := workerFailureBoundedText("source message", source.Message, 0, WorkerFailureObservedTextMaxBytes); err != nil {
		return err
	}
	if source.Locator == "" && source.Digest == "" && source.Message == "" && source.Host == nil {
		return workerFailureBoundFailure("failure_attribution source records no content: store the fields actually supplied and a content digest, never an invented locator and never an empty entry")
	}
	return nil
}

// hostErrorIdentity reports whether the typed host observation carries an
// actual error name, code, or status — the observed fault itself.
func hostErrorIdentity(host *WorkerFailureHostObservation) bool {
	return host.ErrorName != "" || host.ErrorCode != "" || host.StatusCode != 0
}

// validateWorkerFailureObservation enforces the producer observation: the
// closed observer/phase and observer/source couplings, the closed cause and
// basis vocabularies, the basis rules, and the source bounds. The supported
// rules name which cited sources may carry a cause; they never judge
// prose and never establish that a cited record exists or caused anything.
func validateWorkerFailureObservation(observation *WorkerFailureObservation) error {
	if observation == nil {
		return workerFailureBoundFailure("a recorded failure_attribution block must carry the producer observation")
	}
	if err := workerFailureBoundedText("diagnosis", observation.Diagnosis, 0, WorkerFailureDiagnosisMaxBytes); err != nil {
		return err
	}
	phases, knownObserver := workerFailureObserverPhases[observation.Observer]
	if !knownObserver {
		return workerFailureBoundFailure("failure_attribution observer must be host_adapter, worker_report, or coordinator")
	}
	if !workerFailurePhaseVocabulary[observation.Phase] {
		return workerFailureBoundFailure("failure_attribution phase is not one of the nine closed phases")
	}
	if !phases[observation.Phase] {
		return workerFailureBoundFailure("failure_attribution observer " + observation.Observer + " may not emit phase " + observation.Phase + "; only the boundary that observed a phase may emit it")
	}
	if !workerFailureCauseVocabulary[observation.Cause] {
		return workerFailureBoundFailure("failure_attribution cause must be unknown, assignment_requirement, implementation, dependency, host_provider, report_contract, or identity_provenance")
	}
	if observation.Basis != WorkerFailureBasisUnknown && observation.Basis != WorkerFailureBasisHypothesis && observation.Basis != WorkerFailureBasisSupported {
		return workerFailureBoundFailure("failure_attribution basis must be unknown, hypothesis, or supported")
	}
	if observation.Cause == WorkerFailureCauseUnknown && observation.Basis != WorkerFailureBasisUnknown {
		return workerFailureBoundFailure("an unknown cause cannot carry a hypothesis or supported basis; keep the causal diagnosis explicit beside the observed content")
	}
	if observation.Cause != WorkerFailureCauseUnknown && observation.Basis == WorkerFailureBasisUnknown {
		return workerFailureBoundFailure("failure_attribution cause " + observation.Cause + " with an unknown basis is a bare assertion; label it hypothesis or name admissible evidence")
	}
	if observation.Observer == WorkerFailureObserverWorkerReport && observation.Basis == WorkerFailureBasisSupported {
		return workerFailureBoundFailure("a worker report stays a claim; its basis can be unknown or hypothesis, never supported")
	}
	if observation.Sources == nil {
		return workerFailureBoundFailure("failure_attribution observation must carry a sources array; an absent array is not the observation shape")
	}
	if len(observation.Sources) == 0 {
		return workerFailureBoundFailure("failure_attribution observation must name what it saw; record the observed variant as a source entry")
	}
	if len(observation.Sources) > WorkerFailureAttributionMaxSources {
		return workerFailureBoundFailure("failure_attribution observation must carry at most 8 sources")
	}
	for _, source := range observation.Sources {
		if err := validateWorkerFailureSource(source); err != nil {
			return err
		}
		if !workerFailureObserverSourceKinds[observation.Observer][source.Kind] {
			return workerFailureBoundFailure("failure_attribution observer " + observation.Observer + " may cite only the source kinds it owns; source kind " + source.Kind + " belongs to another boundary")
		}
	}
	return validateWorkerFailureBasis(observation)
}

// validateWorkerFailureBasis enforces what a supported claim may rest on:
// addressable immutable evidence for every supported claim, a typed
// terminal host error for a supported provider cause — the observed fault,
// never a bare cancellation or a held retry, and never a claim of terminal
// causality — and typed control or review evidence for a supported
// implementation cause.
func validateWorkerFailureBasis(observation *WorkerFailureObservation) error {
	if observation.Basis != WorkerFailureBasisSupported {
		return nil
	}
	addressable := false
	for _, source := range observation.Sources {
		if source.Locator != "" && source.Digest != "" {
			addressable = true
		}
	}
	if !addressable {
		return workerFailureBoundFailure("a supported claim names admissible immutable evidence: at least one source must carry a locator and its content digest")
	}
	switch observation.Cause {
	case WorkerFailureCauseHostProvider:
		for _, source := range observation.Sources {
			if source.Kind == WorkerFailureSourceHostEvent && source.Host.Tag == WorkerFailureHostTagTerminal &&
				hostErrorIdentity(source.Host) && source.Locator != "" && source.Digest != "" {
				return nil
			}
		}
		return workerFailureBoundFailure("a supported host_provider cause requires a typed terminal host error with its actual error name, code, or status; a bare cancellation or a held retry cannot support it")
	case WorkerFailureCauseImplementation:
		for _, source := range observation.Sources {
			if (source.Kind == WorkerFailureSourceNativeRun || source.Kind == WorkerFailureSourceResult) &&
				source.Locator != "" && source.Digest != "" {
				return nil
			}
		}
		return workerFailureBoundFailure("a supported implementation cause requires cited native run or result evidence; a host cancellation or report absence cannot support it")
	}
	return nil
}

// validateWorkerFailureSubjectRef enforces one typed immutable subject
// reference: closed kind, bounded locator, digest shape when present.
func validateWorkerFailureSubjectRef(ref *WorkerFailureSubjectRef, requireNativeRun bool) error {
	if ref == nil {
		return workerFailureBoundFailure("failure_attribution requires the subject reference")
	}
	if !workerFailureSubjectKindVocabulary[ref.Kind] {
		return workerFailureBoundFailure("failure_attribution subject reference kind must be observed_delivery_event or native_run")
	}
	if requireNativeRun && ref.Kind != WorkerFailureSubjectNativeRun {
		return workerFailureBoundFailure("failure_attribution integration names the combined native run; the subject reference kind must be native_run")
	}
	if err := workerFailureBoundedText("subject reference locator", ref.Locator, 1, WorkerFailureSourceRefMaxBytes); err != nil {
		return err
	}
	if ref.Digest != "" {
		return workerFailureDigestPattern("subject reference digest", ref.Digest)
	}
	return nil
}

// validateWorkerFailureJobBinding enforces the structural shape of one
// recorded job revision reference: job identity, content digest, and a
// recorded revision. That the revision is exactly the one the dispatch
// recorded is a join against the recorded job event, never a check here.
func validateWorkerFailureJobBinding(binding *WorkerJobBinding) error {
	if binding == nil {
		return workerFailureBoundFailure("failure_attribution job binding is required")
	}
	if !workerJobIDPattern.MatchString(binding.JobID) {
		return workerFailureBoundFailure("failure_attribution job binding job_id must match the recorded job identity pattern")
	}
	if err := workerFailureDigestPattern("job binding digest", binding.Digest); err != nil {
		return err
	}
	if binding.Revision < 1 {
		return workerFailureBoundFailure("failure_attribution job binding revision must be a recorded revision of at least 1")
	}
	return nil
}

// validateWorkerFailureAttemptIdentity enforces the attempt variant: the
// authorizing identity is complete (registered lane, positive epoch,
// pinned packet digest), exactly one of the no_job marker or a job binding
// is present, and the subject stays a typed immutable reference.
func validateWorkerFailureAttemptIdentity(attempt *WorkerFailureAttemptIdentity) error {
	if attempt == nil {
		return workerFailureBoundFailure("failure_attribution attempt identity requires the attempt variant")
	}
	if err := workerFailureBoundedText("attempt_id", attempt.AttemptID, 1, workerFailureIdentityRefMaxBytes); err != nil {
		return err
	}
	if !validLaneID(attempt.LaneID) {
		return workerFailureBoundFailure("failure_attribution attempt lane_id must be one registered lane id")
	}
	if attempt.Epoch < 1 {
		return workerFailureBoundFailure("failure_attribution attempt epoch must be positive")
	}
	if err := workerFailureDigestPattern("attempt packet_digest", attempt.PacketDigest); err != nil {
		return err
	}
	if (attempt.NoJob && attempt.Job != nil) || (!attempt.NoJob && attempt.Job == nil) {
		return workerFailureBoundFailure("failure_attribution attempt carries exactly one of the no_job marker or a job binding; a jobless attempt never fabricates one and a job-bound attempt never hides it")
	}
	if attempt.Job != nil {
		if err := validateWorkerFailureJobBinding(attempt.Job); err != nil {
			return err
		}
	}
	if attempt.Subject != nil {
		return validateWorkerFailureSubjectRef(attempt.Subject, false)
	}
	return nil
}

// validateWorkerFailureIntegrationIdentity enforces the integration
// variant: the exact combined native run and the exact accepted input
// population, each job revision and result reference at most once.
func validateWorkerFailureIntegrationIdentity(integration *WorkerFailureIntegrationIdentity) error {
	if integration == nil {
		return workerFailureBoundFailure("failure_attribution integration identity requires the integration variant")
	}
	if err := validateWorkerFailureSubjectRef(&integration.NativeRun, true); err != nil {
		return err
	}
	if len(integration.InputResults) == 0 {
		return workerFailureBoundFailure("failure_attribution integration must name the exact accepted input population; an empty input_results array is not the integration shape")
	}
	seenJobs := map[string]bool{}
	seenResults := map[string]bool{}
	for _, input := range integration.InputResults {
		if err := validateWorkerFailureJobBinding(input.Job); err != nil {
			return err
		}
		if err := workerFailureBoundedText("input result_ref", input.ResultRef, 1, WorkerFailureSourceRefMaxBytes); err != nil {
			return err
		}
		jobKey := input.Job.JobID + ":" + strconv.FormatInt(input.Job.Revision, 10)
		if seenJobs[jobKey] {
			return workerFailureBoundFailure("failure_attribution input population repeats job " + jobKey + "; the accepted population names each job revision at most once")
		}
		if seenResults[input.ResultRef] {
			return workerFailureBoundFailure("failure_attribution input population repeats result reference " + input.ResultRef + "; each accepted result is named at most once")
		}
		seenJobs[jobKey] = true
		seenResults[input.ResultRef] = true
	}
	return nil
}

// validateWorkerFailureIdentity enforces the closed union: the kind is
// closed, exactly one variant is present, and it matches the kind.
func validateWorkerFailureIdentity(identity *WorkerFailureIdentity) error {
	if identity == nil {
		return nil
	}
	if !workerFailureIdentityKindVocabulary[identity.Kind] {
		return workerFailureBoundFailure("failure_attribution identity kind must be attempt or integration")
	}
	if identity.Kind == WorkerFailureIdentityAttempt {
		if identity.Attempt == nil || identity.Integration != nil {
			return workerFailureBoundFailure("failure_attribution identity kind attempt carries exactly the attempt variant")
		}
		return validateWorkerFailureAttemptIdentity(identity.Attempt)
	}
	if identity.Integration == nil || identity.Attempt != nil {
		return workerFailureBoundFailure("failure_attribution identity kind integration carries exactly the integration variant")
	}
	return validateWorkerFailureIntegrationIdentity(identity.Integration)
}

// validateWorkerFailureIdentityObservationCoupling ties the identity
// variant to the observation: integration identity rides only a coordinator
// integration observation, and attempt identity rides every other phase.
func validateWorkerFailureIdentityObservationCoupling(identity *WorkerFailureIdentity, observation *WorkerFailureObservation) error {
	if identity.Kind == WorkerFailureIdentityAttempt {
		if observation.Phase == WorkerFailurePhaseIntegration {
			return workerFailureBoundFailure("an attempt identity cannot ride the integration phase; a combined run is not a single worker attempt")
		}
		return nil
	}
	if observation.Phase != WorkerFailurePhaseIntegration || observation.Observer != WorkerFailureObserverCoordinator {
		return workerFailureBoundFailure("integration identity rides only a coordinator integration observation")
	}
	return nil
}

// ValidateWorkerFailureAttribution enforces the closed structural shape of
// one failure_attribution block. An absent (nil) block is legal; whether a
// payload version requires a block is that version's registration. The
// explicit legacy_unavailable marker carries no content. A recorded block
// carries the producer observation and the core-derived identity. A block
// past the bound is refused whole and never truncated. Structural only: no
// rule here resolves a locator, matches a digest to its record, compares a
// job binding to the dispatch window, or judges a causal claim.
func ValidateWorkerFailureAttribution(block *WorkerFailureAttribution) error {
	if block == nil {
		return nil
	}
	if block.Origin != WorkerFailureAttributionRecorded && block.Origin != WorkerFailureAttributionLegacyUnavailable {
		return workerFailureBoundFailure("failure_attribution origin must be recorded or legacy_unavailable; evidence_origin tokens are a different field and never an attribution origin")
	}
	if block.Origin == WorkerFailureAttributionLegacyUnavailable {
		if block.Observation != nil || block.Identity != nil {
			return workerFailureBoundFailure("failure_attribution legacy_unavailable marks explicit historical absence and carries no observation or identity content")
		}
		return nil
	}
	if err := validateWorkerFailureObservation(block.Observation); err != nil {
		return err
	}
	if block.Identity == nil {
		return workerFailureBoundFailure("a recorded failure_attribution block must carry the core-derived identity")
	}
	if err := validateWorkerFailureIdentity(block.Identity); err != nil {
		return err
	}
	if err := validateWorkerFailureIdentityObservationCoupling(block.Identity, block.Observation); err != nil {
		return err
	}
	encoded, err := json.Marshal(block)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "worker_failure_attribution", "cannot measure the failure_attribution serialization", false, "repair the failure attribution block", err)
	}
	if len(encoded) > WorkerFailureAttributionMaxBlockBytes {
		return workerFailureBoundFailure("failure_attribution serializes past the 8192-byte block bound and is refused whole, never truncated")
	}
	return nil
}

// WorkerFailureObservationDigest computes the canonical observation-content
// digest: the repository sha256-prefixed digest over the compact JSON of
// the validated producer observation. It is deterministic for the same
// content, treats the source order as explicit, and changes when any
// observed member changes. The signed evidence boundary authenticates this
// digest under its own domain prefix; no domain separation happens here.
// An observation that fails structural validation or serializes past the
// bound is never digested.
func WorkerFailureObservationDigest(observation *WorkerFailureObservation) (string, error) {
	if observation == nil {
		return "", workerFailureBoundFailure("the observation digest received no observation")
	}
	if err := validateWorkerFailureObservation(observation); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return "", wrapFailure(KindInvalidPayload, "worker_failure_attribution", "cannot serialize the producer observation", false, "repair the failure attribution block", err)
	}
	if len(encoded) > WorkerFailureAttributionMaxBlockBytes {
		return "", workerFailureBoundFailure("the producer observation serializes past the 8192-byte bound and is refused whole, never truncated")
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// DecodeWorkerFailureAttribution decodes one present failure_attribution
// block strictly: raw input is bounded before any decode work, must be
// valid UTF-8, one closed JSON object with unique members and no trailing
// content, and no member outside the closed shapes at any depth. The
// decoded block then passes the structural validator. Payload owners check
// member presence before calling; this function refuses empty input rather
// than guessing absence into a value.
func DecodeWorkerFailureAttribution(raw json.RawMessage) (*WorkerFailureAttribution, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, workerFailureBoundFailure("failure_attribution decode received no content; check member presence at the payload owner")
	}
	if len(raw) > WorkerFailureAttributionMaxDecodeBytes {
		return nil, workerFailureBoundFailure("failure_attribution raw input exceeds the " + strconv.Itoa(WorkerFailureAttributionMaxDecodeBytes) + "-byte decode ceiling")
	}
	if !utf8.Valid(raw) {
		return nil, workerFailureBoundFailure("failure_attribution bytes are not valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, workerFailureBoundFailure("failure_attribution is not one closed JSON object with unique members: " + err.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var block WorkerFailureAttribution
	if err := decoder.Decode(&block); err != nil {
		return nil, workerFailureBoundFailure("failure_attribution is not the closed attribution shape: " + err.Error())
	}
	if err := ValidateWorkerFailureAttribution(&block); err != nil {
		return nil, err
	}
	return &block, nil
}
