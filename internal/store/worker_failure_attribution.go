package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The block has two parts: the producer observation, which records what the
// observing boundary saw, and the core-derived identity, which only Concord
// derives from the dispatch window and recorded revisions. A producer never
// authors identity.
//
// Structural validation and SQL joins establish source identity, not the
// truth of a causal claim. A supported basis remains a producer assertion.
// Subjects reference existing observed-delivery events or native runs;
// attribution does not add another subject-commit owner.

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
// retry, from a bare cancellation, and from the coordinator's abandonment
// request the host adapter observed.
const (
	WorkerFailureHostTagTerminal     = "terminal"
	WorkerFailureHostTagRetry        = "retry"
	WorkerFailureHostTagCancellation = "cancellation"
	WorkerFailureHostTagAbandonment  = "abandonment"
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
	WorkerFailureHostTagAbandonment:  true,
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
// identity, not a fictitious single worker attempt. A lease recorded before
// the event watermark column carries no population: InputsUnavailable marks
// that population explicitly unavailable, is mutually exclusive with a
// nonempty InputResults list, and never stands for an empty accepted
// population. No copied subject-commit field exists; the consumer resolves
// the subject from the same lease result the NativeRun reference names.
type WorkerFailureIntegrationIdentity struct {
	NativeRun         WorkerFailureSubjectRef    `json:"native_run"`
	InputResults      []WorkerFailureInputResult `json:"input_results"`
	InputsUnavailable bool                       `json:"inputs_unavailable,omitempty"`
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
	if host.Tag == WorkerFailureHostTagAbandonment && (host.ErrorName != "" || host.ErrorCode != "" || host.StatusCode != 0) {
		return workerFailureBoundFailure("an abandonment observation records the coordinator's request and carries no error identity; record the fault as a terminal observation instead")
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
		if source.Host != nil && source.Host.Tag == WorkerFailureHostTagAbandonment {
			if observation.Cause != WorkerFailureCauseUnknown || observation.Basis != WorkerFailureBasisUnknown {
				return workerFailureBoundFailure("an abandonment observation records a request that proves no cause; keep cause unknown and basis unknown")
			}
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
// variant: the exact combined native run and either the exact accepted
// input population, each job revision and result reference at most once,
// or the explicit inputs_unavailable marker for a lease whose watermark is
// absent — never both.
func validateWorkerFailureIntegrationIdentity(integration *WorkerFailureIntegrationIdentity) error {
	if integration == nil {
		return workerFailureBoundFailure("failure_attribution integration identity requires the integration variant")
	}
	if err := validateWorkerFailureSubjectRef(&integration.NativeRun, true); err != nil {
		return err
	}
	if integration.InputsUnavailable {
		if len(integration.InputResults) > 0 {
			return workerFailureBoundFailure("failure_attribution inputs_unavailable excludes an input population; a lease without its event watermark names no accepted revisions")
		}
		return nil
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

// workerFailureAttributionContent reports whether one decoded block carries
// recorded content beside its origin: anything a source version below the
// attribution boundary could never have carried legally.
func workerFailureAttributionContent(block *WorkerFailureAttribution) bool {
	return block != nil && (block.Observation != nil || block.Identity != nil)
}

// validateCurrentWorkerFailureAttribution refuses historical absence markers.
func validateCurrentWorkerFailureAttribution(block *WorkerFailureAttribution) error {
	if block == nil {
		return nil
	}
	if block.Origin == WorkerFailureAttributionLegacyUnavailable {
		return workerFailureBoundFailure("failure_attribution legacy_unavailable on an attribution-capable payload is a forged new-legacy claim; a capable terminal records its observation or none")
	}
	return ValidateWorkerFailureAttribution(block)
}

// workerFailureAttemptIdentity builds the core-derived attempt identity
// from the authorization facts its callers read with SQL only: the attempt
// identity, the lane the attempt recorded, the dispatch window's exact
// epoch and packet digest, and the dispatched job revision — the explicit
// no_job marker when the authorization bound none. The builder never reads:
// every input is already resolved, so the live seam and the fold construct
// the same identity from the same owners.
func workerFailureAttemptIdentity(attemptID, laneID string, window WorkerDispatchWindow, job *WorkerJobBinding) *WorkerFailureIdentity {
	identity := &WorkerFailureAttemptIdentity{
		AttemptID:    attemptID,
		LaneID:       laneID,
		Epoch:        window.AttemptEpoch,
		PacketDigest: window.PacketDigest,
	}
	if job != nil {
		identity.Job = &WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest}
	} else {
		identity.NoJob = true
	}
	return &WorkerFailureIdentity{Kind: WorkerFailureIdentityAttempt, Attempt: identity}
}

// workerFailureTerminalObserverAdmits reports whether a worker terminal
// event (worker.failed, or a born-failed worker.dispatched) may carry the
// named observer. The coordinator observes through its own correction
// records on workflow.action_completed, never through a worker terminal.
func workerFailureTerminalObserverAdmits(observer string) bool {
	return observer == WorkerFailureObserverHostAdapter || observer == WorkerFailureObserverWorkerReport
}

// workerFailureObservedDeliverySubjectQuery resolves the one completed
// observed-delivery subject an attempt owns: the worker.completed event
// whose core-observed delivery the store itself took. The most recent one
// wins; a failed attempt has none.
const workerFailureObservedDeliverySubjectQuery = `SELECT terminal_event_id, json_extract(terminal_payload,'$.observed_delivery') FROM worker_attempts
WHERE work_id=? AND terminal_kind=? AND attempt_id=?
AND json_extract(terminal_payload,'$.observed_delivery') IS NOT NULL`

// workerFailureObservedDeliverySubject resolves the typed subject reference
// for a coordinator result_review observation: the attempt's completed
// observed-delivery event, named by its event id with the digest over the
// recorded delivery bytes. An attempt without one — a failed attempt, or a
// completion that predates the delivery observation — carries no subject.
func workerFailureObservedDeliverySubject(ctx context.Context, q queryer, workID, attemptID string) (*WorkerFailureSubjectRef, error) {
	attempt, err := workerAttemptByIDCore(ctx, q, attemptID)
	if err != nil {
		return nil, err
	}
	if attempt.WorkID != workID || attempt.LifecycleState != "completed" {
		return nil, nil
	}
	var eventID string
	var raw sql.NullString
	if err := q.QueryRowContext(ctx, workerFailureObservedDeliverySubjectQuery, workID, WorkerCompleted, attemptID).Scan(&eventID, &raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "worker_failure_attribution", "cannot read the attempt's observed delivery subject", true, "retry once the worker terminal fact is readable", err)
	}
	var delivery WorkerObservedDelivery
	if err := json.Unmarshal([]byte(raw.String), &delivery); err != nil {
		return nil, newFailure(KindInvariantViolation, "worker_failure_attribution", "the attempt's observed delivery is not a readable delivery observation", false, "reconcile_operation")
	}
	encoded, err := json.Marshal(delivery)
	if err != nil {
		return nil, wrapFailure(KindInvariantViolation, "worker_failure_attribution", "cannot serialize the observed delivery subject", false, "reconcile_operation", err)
	}
	sum := sha256.Sum256(encoded)
	return &WorkerFailureSubjectRef{Kind: WorkerFailureSubjectObservedDeliveryEvent, Locator: eventID, Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

// verifyWorkerFailureObservedDeliverySubject verifies one subject reference
// against the retained terminal: the named identity must be this attempt's completed
// observed delivery, and a supplied digest must match the recorded delivery
// bytes. A locator existence never creates the subject it names.
func verifyWorkerFailureObservedDeliverySubject(ctx context.Context, q queryer, workID string, attemptID string, subject *WorkerFailureSubjectRef) error {
	if subject == nil {
		return nil
	}
	var raw sql.NullString
	err := q.QueryRowContext(ctx, `SELECT json_extract(terminal_payload,'$.observed_delivery') FROM worker_attempts
WHERE work_id=? AND terminal_kind=? AND terminal_event_id=? AND attempt_id=? AND json_extract(terminal_payload,'$.observed_delivery') IS NOT NULL`,
		workID, WorkerCompleted, subject.Locator, attemptID).Scan(&raw)
	if err == sql.ErrNoRows {
		return workerFailureBoundFailure("failure_attribution subject reference " + subject.Locator + " names no completed observed_delivery event of attempt " + attemptID)
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "worker_failure_attribution", "cannot read the referenced observed delivery subject", true, "retry once the worker terminal fact is readable", err)
	}
	if subject.Digest == "" {
		return nil
	}
	var delivery WorkerObservedDelivery
	if err := json.Unmarshal([]byte(raw.String), &delivery); err != nil {
		return newFailure(KindInvariantViolation, "worker_failure_attribution", "the referenced observed delivery is not a readable delivery observation", false, "reconcile_operation")
	}
	encoded, err := json.Marshal(delivery)
	if err != nil {
		return wrapFailure(KindInvariantViolation, "worker_failure_attribution", "cannot serialize the referenced observed delivery", false, "reconcile_operation", err)
	}
	sum := sha256.Sum256(encoded)
	if subject.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return workerFailureBoundFailure("failure_attribution subject reference digest " + subject.Digest + " differs from the recorded observed delivery of " + subject.Locator)
	}
	return nil
}

// enrichWorkerFailureAttributionBlock applies the producer rules to one
// caller-supplied block and derives the exact core identity beside it. The
// producer records its observation only: a legacy origin, an authored
// identity, or a missing observation refuses, and the observer must be one
// of the two worker-terminal boundaries. The identity comes from the
// authorization facts the caller read with SQL — never from the caller.
func enrichWorkerFailureAttributionBlock(attemptID, laneID string, window WorkerDispatchWindow, job *WorkerJobBinding, block *WorkerFailureAttribution) (*WorkerFailureAttribution, error) {
	if block == nil {
		return nil, nil
	}
	if block.Origin != WorkerFailureAttributionRecorded {
		return nil, workerFailureBoundFailure("failure_attribution origin is core-owned: the producer records its observation and the store derives the origin and identity")
	}
	if block.Identity != nil {
		return nil, workerFailureBoundFailure("failure_attribution identity is core-derived from the dispatch authorization; no producer can author it")
	}
	if block.Observation == nil {
		return nil, workerFailureBoundFailure("a recorded failure_attribution block must carry the producer observation")
	}
	if err := validateWorkerFailureObservation(block.Observation); err != nil {
		return nil, err
	}
	if !workerFailureTerminalObserverAdmits(block.Observation.Observer) {
		return nil, workerFailureBoundFailure("failure_attribution observer " + block.Observation.Observer + " cannot record a worker terminal; the coordinator attributes through its own correction records")
	}
	identity := workerFailureAttemptIdentity(attemptID, laneID, window, job)
	return &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: block.Observation, Identity: identity}, nil
}

// enrichWorkerFailedAttributionTx is the terminal ingest's attribution
// enrichment: one worker.failed observation arrives without identity and
// the store derives the exact authorized attempt identity from the
// attempt's own dispatch window, the attempt row, and the dispatched job
// revision. A caller-authored identity or origin refuses with zero
// effects; an event without a block lands unchanged.
func enrichWorkerFailedAttributionTx(ctx context.Context, tx *sql.Tx, event Event) (Event, error) {
	if event.Kind != WorkerFailed {
		return event, nil
	}
	var payload WorkerFailedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return Event{}, err
	}
	if payload.FailureAttribution == nil {
		return event, nil
	}
	attempt, err := workerAttemptByIDCore(ctx, tx, payload.AttemptID)
	if err != nil {
		return Event{}, err
	}
	if attempt.WorkID != event.SubjectID {
		return Event{}, invalidWorkerPayload("worker.failed subject does not own the worker attempt")
	}
	window, err := findAuthorizedDispatchWindowCore(ctx, tx, event.SubjectID, payload.AttemptID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
			return Event{}, workerFailureBoundFailure("failure_attribution derives from the attempt's authorized dispatch window; attempt " + payload.AttemptID + " records none")
		}
		return Event{}, err
	}
	job, err := workflowDispatchedJobForAttempt(ctx, tx, event.SubjectID, payload.AttemptID)
	if err != nil {
		return Event{}, err
	}
	block, err := enrichWorkerFailureAttributionBlock(payload.AttemptID, attempt.LaneID, window, job, payload.FailureAttribution)
	if err != nil {
		return Event{}, err
	}
	payload.FailureAttribution = block
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, wrapFailure(KindInvariantViolation, "worker_failure_attribution", "cannot encode the enriched failure payload", false, "repair the failure attribution block", err)
	}
	enriched := event
	enriched.Payload = raw
	return enriched, nil
}

// enrichWorkerBornFailedAttributionTx is the dispatch evidence route's
// attribution enrichment for a born-failed terminal record: the observation
// arrives without identity and the store derives it from the dispatch
// authorization the window still records — the attempt's own epoch and
// packet digest, the lane the payload records, and the worker-job binding
// the authorization bound. An ordinary dispatch carries no block and lands
// unchanged.
func enrichWorkerBornFailedAttributionTx(ctx context.Context, q queryer, dispatch Event, payload *WorkerDispatchedPayload) error {
	if payload.Terminal != "failed" || payload.FailureAttribution == nil {
		if payload.FailureAttribution != nil {
			return invalidWorkerPayload("worker.dispatched failure_attribution is reserved for a born-failed terminal record")
		}
		return nil
	}
	window, err := findAuthorizedDispatchWindowCore(ctx, q, dispatch.SubjectID, payload.AttemptID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
			return workerFailureBoundFailure("failure_attribution derives from the attempt's authorized dispatch window; attempt " + payload.AttemptID + " records none")
		}
		return err
	}
	block, err := enrichWorkerFailureAttributionBlock(payload.AttemptID, payload.LaneID, window, payload.WorkerJob, payload.FailureAttribution)
	if err != nil {
		return err
	}
	payload.FailureAttribution = block
	return nil
}

// workerFailureAttemptIdentityMismatch names the first field where a
// recorded attempt identity differs from the identity the authorization
// owners derive, or "" when they are exactly equal.
func workerFailureAttemptIdentityMismatch(recorded, expected *WorkerFailureAttemptIdentity) string {
	switch {
	case recorded == nil || expected == nil:
		return "identity"
	case recorded.AttemptID != expected.AttemptID:
		return "attempt_id"
	case recorded.LaneID != expected.LaneID:
		return "lane_id"
	case recorded.Epoch != expected.Epoch:
		return "epoch"
	case recorded.PacketDigest != expected.PacketDigest:
		return "packet_digest"
	case (recorded.Job == nil) != (expected.Job == nil):
		return "worker_job presence"
	case recorded.Job != nil && *recorded.Job != *expected.Job:
		return "worker_job revision"
	case recorded.NoJob != expected.NoJob:
		return "no_job marker"
	}
	return ""
}

// workerFailureAbandonmentTagCoupling enforces the abandoned-terminal
// coupling: an abandonment observation records the coordinator's request,
// and only the abandoned terminal variant observed one.
func workerFailureAbandonmentTagCoupling(failureKind string, observation *WorkerFailureObservation) error {
	if observation == nil {
		return nil
	}
	for _, source := range observation.Sources {
		if source.Host != nil && source.Host.Tag == WorkerFailureHostTagAbandonment && failureKind != WorkerFailureAbandoned {
			return workerFailureBoundFailure("an abandonment observation belongs to the abandoned terminal; a " + failureKind + " terminal observed no abandonment request")
		}
	}
	return nil
}

// verifyWorkerFailureAttemptAttributionTx verifies one terminal's recorded
// attribution against its authorization owners, with the same SQL reads the
// live ingest used — identical live and on rebuild. The identity must equal
// the attempt's own window, lane, and dispatched job revision exactly, the
// observer must be one of the two worker-terminal boundaries with the
// worker report confined to the worker_error kind it actually ran, and a
// failed terminal names no observed subject: only a coordinator result_review
// on the correction record resolves one.
func verifyWorkerFailureAttemptAttributionTx(ctx context.Context, q queryer, workID, attemptID, laneID, failureKind string, block *WorkerFailureAttribution) error {
	if block.Identity == nil || block.Identity.Kind != WorkerFailureIdentityAttempt || block.Identity.Attempt == nil {
		return workerFailureBoundFailure("worker terminal failure_attribution carries recorded content without the core-derived attempt identity")
	}
	if block.Observation == nil || !workerFailureTerminalObserverAdmits(block.Observation.Observer) {
		return workerFailureBoundFailure("worker terminal failure_attribution observer must be host_adapter or worker_report; the coordinator attributes through its own correction records")
	}
	if block.Observation.Observer == WorkerFailureObserverWorkerReport && failureKind != WorkerFailureWorkerError {
		return workerFailureBoundFailure("a worker report observes only its own worker_error execution; a " + failureKind + " terminal is the host's record and cannot carry a worker report observation")
	}
	window, err := findAuthorizedDispatchWindowCore(ctx, q, workID, attemptID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
			return workerFailureBoundFailure("worker terminal failure_attribution names no authorized dispatch window for attempt " + attemptID + "; the recorded identity cannot verify")
		}
		return err
	}
	job, err := workflowDispatchedJobForAttempt(ctx, q, workID, attemptID)
	if err != nil {
		return err
	}
	expected := workerFailureAttemptIdentity(attemptID, laneID, window, job)
	if mismatch := workerFailureAttemptIdentityMismatch(block.Identity.Attempt, expected.Attempt); mismatch != "" {
		return workerFailureBoundFailure("worker terminal failure_attribution identity " + mismatch + " does not equal the attempt's authorized dispatch window; a recorded block carries the exact core identity or none")
	}
	if block.Identity.Attempt.Subject != nil {
		return workerFailureBoundFailure("worker terminal failure_attribution names an observed subject; a failed attempt records no delivery and only a coordinator result_review resolves one")
	}
	return workerFailureAbandonmentTagCoupling(failureKind, block.Observation)
}

// verifyWorkflowActionCompletedAttributionMembersTx carries the fold's two
// attribution members for one action_completed completion. The reference is
// the derived record's alone and names its terminal. The block rides
// correction actions alone and verifies its exact joins.
func verifyWorkflowActionCompletedAttributionMembersTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload) error {
	if p.FailureAttributionRef != "" {
		if err := verifyDerivedFailureAttributionReferenceTx(ctx, tx, event, p); err != nil {
			return err
		}
	} else if p.ActionID == "record_worker_failure" && strings.HasSuffix(event.EventID, workflowDerivedWorkerFailureEventIDSuffix) {
		return newFailure(KindInvalidPayload, "fold_event", "a derived failure record on the attribution-capable payload names the terminal attribution reference it derives from", false, "derive the record from the terminal worker.failed event")
	}
	if p.FailureAttribution == nil {
		return nil
	}
	if !workflowFailureAttributionCorrectionActions[p.ActionID] {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed failure_attribution is reserved for reject_worker_result and request_correction", false, "record the coordinator attribution on the correction completion that observed it")
	}
	if err := validateCurrentWorkerFailureAttribution(p.FailureAttribution); err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed failure_attribution: "+err.Error(), false, "record the coordinator attribution on the current correction completion")
	}
	if workerFailureAttributionContent(p.FailureAttribution) {
		// The fold's own sequence is the citation bound: a stored source
		// event recorded after the correction is a future citation.
		return verifyCoordinatorFailureAttributionTx(ctx, tx, event.SubjectID, p.ActionID, p.WorkerAttemptID, p.FailureAttribution, event.Seq)
	}
	return nil
}

// workerFailurePayloadDigest is the sha256 digest over one retained event's
// full payload bytes, the content identity a result source's supplied digest
// must match.
func workerFailurePayloadDigest(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// workerFailureCompletedEventSource resolves one retained worker.completed
// event of the work by its id beside its attempt identity and payload bytes,
// bounded at beforeSeq (zero leaves the bound open, the pre-append derive's
// own view). A source event recorded after the citing correction is a future
// citation no fold accepts.
func workerFailureCompletedEventSource(ctx context.Context, q queryer, workID, eventID string, beforeSeq int64) (payload string, attemptID string, found bool, err error) {
	payload, attemptID, err = readWorkerTerminalSourceBefore(ctx, q, workID, eventID, WorkerCompleted, beforeSeq)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, wrapFailure(KindUnavailable, "worker_failure_attribution", "cannot read the cited completed event", true, "retry once the worker terminal fact is readable", err)
	}
	return payload, attemptID, true, nil
}

// verifyWorkerFailureReviewResultSourceTx verifies one result_review source
// against the reviewed attempt's own retained evidence. The locator resolves either a
// worker.completed event of the exact attempt — a supplied digest must match
// its retained payload bytes — or an existing ranked finding whose minting
// completion belongs to that attempt. A native_run source is the
// integration phase's anchor and never a review citation.
func verifyWorkerFailureReviewResultSourceTx(ctx context.Context, q queryer, workID, attemptID string, source WorkerFailureSource, beforeSeq int64) error {
	if source.Kind == WorkerFailureSourceNativeRun {
		return workerFailureBoundFailure("result_review cites its own completed evidence; a native_run source is the integration phase's anchor, not a review citation")
	}
	if source.Kind != WorkerFailureSourceResult {
		return workerFailureBoundFailure("result_review cites result sources; " + source.Kind + " belongs to another boundary")
	}
	payload, sourceAttempt, found, err := workerFailureCompletedEventSource(ctx, q, workID, source.Locator, beforeSeq)
	if err != nil {
		return err
	}
	if !found {
		return verifyWorkerFailureReviewFindingSourceTx(ctx, q, workID, attemptID, source, beforeSeq)
	}
	if sourceAttempt != attemptID {
		return workerFailureBoundFailure("result source " + source.Locator + " is the completion of attempt " + sourceAttempt + "; result_review cites the exact attempt it reviews")
	}
	if source.Digest != "" && source.Digest != workerFailurePayloadDigest(payload) {
		return workerFailureBoundFailure("result source " + source.Locator + " digest " + source.Digest + " differs from the retained payload bytes of the completed event")
	}
	return nil
}

// verifyWorkerFailureReviewFindingSourceTx resolves the finding-id arm of a
// result_review source through the existing ranked-finding lineage owner:
// the cited identity must be an existing ranked finding of this work whose
// minting completion belongs to the reviewed attempt, at or before the
// citation bound, with a supplied digest matching the minting event's
// retained payload bytes.
func verifyWorkerFailureReviewFindingSourceTx(ctx context.Context, q queryer, workID, attemptID string, source WorkerFailureSource, beforeSeq int64) error {
	if !oracleFindingIDRefPattern.MatchString(source.Locator) {
		return workerFailureBoundFailure("result source " + source.Locator + " names no completed event of " + workID + " and no ranked finding id; cite the reviewed attempt's completion or an existing ranked finding")
	}
	lineage, err := readWorkerOracleFindingLineageTx(ctx, q, workID)
	if err != nil {
		return err
	}
	finding, exists := lineage.findings[source.Locator]
	if !exists {
		return workerFailureBoundFailure("result source " + source.Locator + " names no existing ranked finding of this work")
	}
	if beforeSeq != 0 && finding.SourceEventSeq >= beforeSeq {
		return workerFailureBoundFailure("result source " + source.Locator + " mints at a sequence after the correction it cites")
	}
	payload, sourceAttempt, found, err := workerFailureCompletedEventSource(ctx, q, workID, finding.SourceEventID, 0)
	if err != nil {
		return err
	}
	if !found {
		return newFailure(KindInvariantViolation, "worker_failure_attribution", "ranked finding "+source.Locator+" names the retained completion "+finding.SourceEventID+" that no longer resolves", false, "reconcile_operation")
	}
	if sourceAttempt != attemptID {
		return workerFailureBoundFailure("result source " + source.Locator + " mints from the completion of attempt " + sourceAttempt + "; result_review cites the exact attempt it reviews")
	}
	if source.Digest != "" && source.Digest != workerFailurePayloadDigest(payload) {
		return workerFailureBoundFailure("result source " + source.Locator + " digest " + source.Digest + " differs from the retained payload bytes of its minting completion")
	}
	return nil
}

// verifyWorkerFailureIntegrationResultSourceTx verifies one integration
// observation's result source: its locator must be an exact accepted input
// reference of the derived population, and a supplied digest must match the
// acceptance event's retained payload bytes, bounded at beforeSeq.
func verifyWorkerFailureIntegrationResultSourceTx(ctx context.Context, q queryer, workID string, source WorkerFailureSource, inputRefs map[string]bool, beforeSeq int64) error {
	if source.Kind != WorkerFailureSourceResult {
		return nil
	}
	if !inputRefs[source.Locator] {
		return workerFailureBoundFailure("integration result source " + source.Locator + " is not an accepted input reference of the cited run's population")
	}
	var payload string
	err := q.QueryRowContext(ctx, `SELECT d.first_acceptance_payload FROM worker_attempt_dispositions d JOIN effect_receipts r ON r.effect_seq=d.first_acceptance_seq WHERE d.work_id=? AND d.action_id='accept_worker_result' AND r.event_id=? AND (?=0 OR d.first_acceptance_seq<?)`,
		workID, source.Locator, beforeSeq, beforeSeq).Scan(&payload)
	if err == sql.ErrNoRows {
		return workerFailureBoundFailure("integration result source " + source.Locator + " names no acceptance event at or before the correction")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "worker_failure_attribution", "cannot read the cited acceptance event", true, "retry once the current rows are readable", err)
	}
	if source.Digest != "" && source.Digest != workerFailurePayloadDigest(payload) {
		return workerFailureBoundFailure("integration result source " + source.Locator + " digest " + source.Digest + " differs from the retained payload bytes of the acceptance event")
	}
	return nil
}

// verifyWorkerFailureReviewResultSources runs the result_review source joins
// for one observation beside its derived attempt identity.
func verifyWorkerFailureReviewResultSources(ctx context.Context, q queryer, workID, attemptID string, observation *WorkerFailureObservation, beforeSeq int64) error {
	for _, source := range observation.Sources {
		if err := verifyWorkerFailureReviewResultSourceTx(ctx, q, workID, attemptID, source, beforeSeq); err != nil {
			return err
		}
	}
	return nil
}

// verifyDerivedFailureAttributionReferenceTx verifies one derived failure
// record's attribution reference: the record is the derivation pair's
// alone, the reference names the same retained terminal worker.failed event
// the event id suffix names, and that terminal's attempt is the record's
// own. A reference that names another terminal, or a caller-authored event
// carrying the reference without its derivation pair, refuses.
func verifyDerivedFailureAttributionReferenceTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload) error {
	if !strings.HasSuffix(event.EventID, workflowDerivedWorkerFailureEventIDSuffix) {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed failure_attribution_ref rides only the derived failure record the terminal event's derivation authored", false, "derive the record from the terminal worker.failed event")
	}
	terminalEventID := strings.TrimSuffix(event.EventID, workflowDerivedWorkerFailureEventIDSuffix)
	if p.FailureAttributionRef != terminalEventID {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed failure_attribution_ref "+p.FailureAttributionRef+" does not name the terminal event "+terminalEventID+" the derivation pair derives from", false, "derive the record from the terminal worker.failed event")
	}
	if event.Seq <= 0 {
		return newFailure(KindMissingEvidence, "fold_event", "action_completed failure_attribution_ref "+terminalEventID+" names no retained terminal worker.failed event", false, "derive the record from the terminal worker.failed event")
	}
	_, attemptID, err := readWorkerTerminalSourceBefore(ctx, tx, event.SubjectID, terminalEventID, WorkerFailed, event.Seq)
	if err == sql.ErrNoRows {
		return newFailure(KindMissingEvidence, "fold_event", "action_completed failure_attribution_ref "+terminalEventID+" names no retained terminal worker.failed event", false, "derive the record from the terminal worker.failed event")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the referenced terminal failure event", true, "retry once the worker terminal fact is readable", err)
	}
	if attemptID == "" || attemptID != p.WorkerAttemptID {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed failure_attribution_ref names a terminal whose attempt is not the record's attempt", false, "derive the record from the terminal worker.failed event")
	}
	// The reference resolves the terminal's attribution record through the
	// current-fact reader: a referenced terminal whose attribution bytes no
	// longer decode refuses the derived record too, so the reference always
	// names a readable attribution or an explicit absence.
	if _, err := WorkerFailureAttributionForAttemptTx(ctx, tx, event.SubjectID, attemptID); err != nil {
		return err
	}
	return nil
}

// verifyCoordinatorFailureAttributionTx verifies one coordinator correction
// completion's recorded attribution against its owners. The attempt joins
// and the subject reference read current rows. The integration identity
// re-derives from the cited run's verify lease without current-host reads.
// beforeSeq is the citing correction's own effect sequence: a source recorded
// after it is a future citation the writer refuses. Pre-write derivation
// passes zero because the correction has not been recorded yet.
func verifyCoordinatorFailureAttributionTx(ctx context.Context, q queryer, workID, actionID, workerAttemptID string, block *WorkerFailureAttribution, beforeSeq int64) error {
	if block.Observation == nil || block.Observation.Observer != WorkerFailureObserverCoordinator {
		return workerFailureBoundFailure("failure_attribution on a correction completion observes as the coordinator; host and worker observations ride their own terminal records")
	}
	if block.Observation.Phase != WorkerFailurePhaseResultReview && block.Observation.Phase != WorkerFailurePhaseIntegration {
		return workerFailureBoundFailure("failure_attribution on a correction completion observes the result_review or integration phase; " + block.Observation.Phase + " belongs to another boundary")
	}
	if block.Identity == nil {
		return workerFailureBoundFailure("a correction completion's recorded failure_attribution carries the core-derived identity")
	}
	if block.Identity.Kind == WorkerFailureIdentityIntegration {
		return verifyCoordinatorIntegrationAttributionTx(ctx, q, workID, block, beforeSeq)
	}
	if block.Observation.Phase != WorkerFailurePhaseResultReview {
		return workerFailureBoundFailure("failure_attribution attempt identity rides the result_review phase; the integration phase names the combined run")
	}
	if workerAttemptID == "" || block.Identity.Attempt == nil || block.Identity.Attempt.AttemptID != workerAttemptID {
		return workerFailureBoundFailure("failure_attribution result_review names the exact action attempt; " + block.Identity.Attempt.AttemptID + " is not the attempt this " + actionID + " completes")
	}
	if block.Identity.Attempt.Subject != nil && block.Identity.Attempt.Subject.Kind != WorkerFailureSubjectObservedDeliveryEvent {
		return workerFailureBoundFailure("failure_attribution result_review subject reference names a completed observed_delivery event")
	}
	if err := verifyWorkerFailureReviewResultSources(ctx, q, workID, workerAttemptID, block.Observation, beforeSeq); err != nil {
		return err
	}
	window, err := findAuthorizedDispatchWindowCore(ctx, q, workID, workerAttemptID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
			return workerFailureBoundFailure("failure_attribution result_review names no authorized dispatch window for attempt " + workerAttemptID + "; the recorded identity cannot verify")
		}
		return err
	}
	attempt, err := workerAttemptByIDCore(ctx, q, workerAttemptID)
	if err != nil {
		return err
	}
	job, err := workflowDispatchedJobForAttempt(ctx, q, workID, workerAttemptID)
	if err != nil {
		return err
	}
	expected := workerFailureAttemptIdentity(workerAttemptID, attempt.LaneID, window, job)
	if mismatch := workerFailureAttemptIdentityMismatch(block.Identity.Attempt, expected.Attempt); mismatch != "" {
		return workerFailureBoundFailure("failure_attribution result_review identity " + mismatch + " does not equal the exact action attempt's authorized dispatch window")
	}
	// The subject is availability-gated, not optional prose: when the
	// attempt owns a completed observed-delivery event, a result_review
	// identity cannot silently omit it.
	available, err := workerFailureObservedDeliverySubject(ctx, q, workID, workerAttemptID)
	if err != nil {
		return err
	}
	if available != nil && block.Identity.Attempt.Subject == nil {
		return workerFailureBoundFailure("result_review omits the observed delivery subject " + available.Locator + " the attempt's completed event records; name the subject the review resolved")
	}
	return verifyWorkerFailureObservedDeliverySubject(ctx, q, workID, workerAttemptID, block.Identity.Attempt.Subject)
}

func verifyCoordinatorIntegrationAttributionTx(ctx context.Context, q queryer, workID string, block *WorkerFailureAttribution, beforeSeq int64) error {
	derived, err := deriveWorkerFailureIntegration(ctx, q, workID, block.Observation)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(derived, block.Identity) {
		return workerFailureBoundFailure("failure_attribution integration identity does not equal the identity the cited combined run derives; the recorded population, run reference, and digest are the reader's answer")
	}
	inputRefs := make(map[string]bool, len(block.Identity.Integration.InputResults))
	for _, input := range block.Identity.Integration.InputResults {
		inputRefs[input.ResultRef] = true
	}
	for _, source := range block.Observation.Sources {
		if err := verifyWorkerFailureIntegrationResultSourceTx(ctx, q, workID, source, inputRefs, beforeSeq); err != nil {
			return err
		}
	}
	return nil
}

// deriveCoordinatorCorrectionAttributionTx derives one coordinator
// correction completion's recorded attribution block inside the
// authenticated action transaction. The request carries the direct producer
// observation alone — decoded strictly, so an origin or identity member
// refuses at the boundary — and the store derives the core identity: the
// integration phase through the bounded integration reader over the cited
// combined run, and the result_review phase through the exact action
// attempt's authorized window, lane, dispatched job revision, and completed
// observed-delivery subject. A nil return means the request carried no
// attribution and the completion records none.
func deriveCoordinatorCorrectionAttributionTx(ctx context.Context, q queryer, workID, actionID string, fields map[string]json.RawMessage) (*WorkerFailureAttribution, error) {
	raw, present := fields["failure_attribution"]
	if !present || len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	if !utf8.Valid(raw) {
		return nil, workerFailureBoundFailure("failure_attribution is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var observation WorkerFailureObservation
	if err := decoder.Decode(&observation); err != nil {
		return nil, workerFailureBoundFailure("failure_attribution is not the closed producer observation shape; the request carries the observation alone and never an origin or identity: " + err.Error())
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, workerFailureBoundFailure("failure_attribution carries trailing content after the producer observation")
	}
	if err := validateWorkerFailureObservation(&observation); err != nil {
		return nil, err
	}
	if observation.Observer != WorkerFailureObserverCoordinator {
		return nil, workerFailureBoundFailure("failure_attribution on " + actionID + " observes as the coordinator; the authenticated workflow boundary sets the observer and no other boundary records here")
	}
	if observation.Phase != WorkerFailurePhaseResultReview && observation.Phase != WorkerFailurePhaseIntegration {
		return nil, workerFailureBoundFailure("failure_attribution on " + actionID + " observes the result_review or integration phase; " + observation.Phase + " belongs to another boundary")
	}
	var identity *WorkerFailureIdentity
	switch observation.Phase {
	case WorkerFailurePhaseIntegration:
		derived, err := deriveWorkerFailureIntegration(ctx, q, workID, &observation)
		if err != nil {
			return nil, err
		}
		identity = derived
	case WorkerFailurePhaseResultReview:
		derived, err := deriveCoordinatorReviewIdentityTx(ctx, q, workID, actionID, fields)
		if err != nil {
			return nil, err
		}
		identity = derived
	}
	block := &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: &observation, Identity: identity}
	if err := ValidateWorkerFailureAttribution(block); err != nil {
		return nil, err
	}
	// The derive runs before the correction exists in the log, so its
	// citation bound is open; the fold re-verifies the same joins bounded at
	// the correction's own sequence.
	switch observation.Phase {
	case WorkerFailurePhaseIntegration:
		inputRefs := make(map[string]bool, len(identity.Integration.InputResults))
		for _, input := range identity.Integration.InputResults {
			inputRefs[input.ResultRef] = true
		}
		for _, source := range observation.Sources {
			if err := verifyWorkerFailureIntegrationResultSourceTx(ctx, q, workID, source, inputRefs, 0); err != nil {
				return nil, err
			}
		}
	case WorkerFailurePhaseResultReview:
		if err := verifyWorkerFailureReviewResultSources(ctx, q, workID, identity.Attempt.AttemptID, &observation, 0); err != nil {
			return nil, err
		}
	}
	return block, nil
}

func deriveCoordinatorReviewIdentityTx(ctx context.Context, q queryer, workID, actionID string, fields map[string]json.RawMessage) (*WorkerFailureIdentity, error) {
	attemptID := workflowFieldStringDefault(fields, "attempt_id", "")
	if attemptID == "" {
		return nil, workerFailureBoundFailure("failure_attribution result_review names the exact action attempt; " + actionID + " carries no attempt to review")
	}
	window, err := findAuthorizedDispatchWindowCore(ctx, q, workID, attemptID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
			return nil, workerFailureBoundFailure("failure_attribution result_review names no authorized dispatch window for attempt " + attemptID)
		}
		return nil, err
	}
	attempt, err := workerAttemptByIDCore(ctx, q, attemptID)
	if err != nil {
		return nil, err
	}
	if attempt.WorkID != workID {
		return nil, workerFailureBoundFailure("failure_attribution result_review names an attempt of another work item")
	}
	job, err := workflowDispatchedJobForAttempt(ctx, q, workID, attemptID)
	if err != nil {
		return nil, err
	}
	identity := workerFailureAttemptIdentity(attemptID, attempt.LaneID, window, job)
	subject, err := workerFailureObservedDeliverySubject(ctx, q, workID, attemptID)
	if err != nil {
		return nil, err
	}
	identity.Attempt.Subject = subject
	return identity, nil
}

// WorkerFailureAttributionRecord is one attempt's resolved terminal
// attribution: the decoded block — nil when the terminal predates the
// boundary and recorded none at a capable version — beside the source
// payload version the log recorded and the terminal event identity that
// owns it. A stored source version below the boundary resolves the explicit
// legacy_unavailable marker, so a history that predates the block reads as
// visibly legacy instead of as an empty claim.
type WorkerFailureAttributionRecord struct {
	EventID       string                    `json:"event_id"`
	SourceVersion int                       `json:"source_version"`
	Attribution   *WorkerFailureAttribution `json:"attribution"`
}

// workerFailureAttributionTerminalRecord resolves one terminal arm: the
// event identity, the recorded source version, and the decoded attribution —
// the explicit legacy marker when a below-boundary source recorded none.
func workerFailureAttributionTerminalRecord(ctx context.Context, q queryer, workID, attemptID string, kind string, terminalFilter string, capableVersion int) (*WorkerFailureAttributionRecord, bool, error) {
	var eventID string
	var sourceVersion int
	var raw sql.NullString
	query := `SELECT terminal_event_id,terminal_payload_version,json_extract(terminal_payload,'$.failure_attribution') FROM worker_attempts WHERE work_id=? AND attempt_id=? AND terminal_kind=?`
	args := []any{workID, attemptID, kind}
	if terminalFilter != "" {
		query = `SELECT dispatch_event_id,dispatch_payload_version,json_extract(dispatch_payload,'$.failure_attribution') FROM worker_attempts WHERE work_id=? AND attempt_id=? AND json_extract(dispatch_payload,'$.terminal')='failed'`
		args = []any{workID, attemptID}
	}
	err := q.QueryRowContext(ctx, query, args...).Scan(&eventID, &sourceVersion, &raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, wrapFailure(KindUnavailable, "worker_failure_attribution", "cannot read the attempt's terminal attribution", true, "retry once the worker attempt facts are readable", err)
	}
	record := &WorkerFailureAttributionRecord{EventID: eventID, SourceVersion: sourceVersion}
	if !raw.Valid || raw.String == "" || raw.String == "null" {
		if sourceVersion < capableVersion {
			record.Attribution = &WorkerFailureAttribution{Origin: WorkerFailureAttributionLegacyUnavailable}
		}
		return record, true, nil
	}
	block, err := DecodeWorkerFailureAttribution(json.RawMessage(raw.String))
	if err != nil {
		return nil, false, err
	}
	record.Attribution = block
	return record, true, nil
}

// WorkerFailureAttributionForAttemptTx resolves the attempt's retained terminal —
// the worker.failed event, or the born-failed worker.dispatched evidence
// that closed the attempt at dispatch — through the caller's queryer,
// decodes the attribution member strictly, and resolves a below-boundary
// source without raw bytes as the explicit legacy_unavailable marker. A
// terminal born at or above the boundary without raw bytes returns a nil
// block: whether that absence is legal is the fold's definition-gated
// refusal, never the reader's guess.
func WorkerFailureAttributionForAttemptTx(ctx context.Context, q queryer, workID, attemptID string) (*WorkerFailureAttributionRecord, error) {
	record, found, err := workerFailureAttributionTerminalRecord(ctx, q, workID, attemptID, WorkerFailed, "", workerFailedAttributionVersion)
	if err != nil || found {
		return record, err
	}
	record, found, err = workerFailureAttributionTerminalRecord(ctx, q, workID, attemptID, WorkerDispatched, "$.terminal", workerDispatchedAttributionVersion)
	if err != nil || found {
		return record, err
	}
	return nil, newFailure(KindProjectionNotFound, "worker_failure_attribution", "attempt "+attemptID+" records no terminal worker.failed or born-failed dispatch evidence", false, "read the attribution of a failed attempt")
}
