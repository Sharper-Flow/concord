package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// Structural witnesses for the typed failure_attribution block. These tests
// exercise the closed types, the byte bounds, the canonical observation
// digest, and the strict decoder only: no test here records an event or
// opens a store. A passing run proves structural integrity alone; it never
// proves provenance or semantic causality — the join validations that
// resolve locators, digests, and job revisions live with the event,
// attempt, job, and native-run owners.

const (
	testDigestA = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testDigestB = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	testDigestC = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	testDigestD = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	testDigestE = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	testDigestF = "sha256:6666666666666666666666666666666666666666666666666666666666666666"
	testDigestG = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
	testDigestH = "sha256:8888888888888888888888888888888888888888888888888888888888888888"
	testDigestI = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	testDigestP = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testDigestQ = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// workerFailureJobFixture is one recorded job revision reference.
func workerFailureJobFixture() *WorkerJobBinding {
	return &WorkerJobBinding{JobID: "job-attribution", Revision: 3, Digest: testDigestE}
}

// workerFailureHostFixture is one typed terminal provider error the host
// observed for the bound child session.
func workerFailureHostFixture() *WorkerFailureHostObservation {
	return &WorkerFailureHostObservation{
		Tag:       WorkerFailureHostTagTerminal,
		EventType: "session.error",
		SessionID: "ses-attr-1",
		MessageID: "msg-attr-1",
		ErrorName: "rate_limit_exceeded",
		ErrorCode: "429",
	}
}

// workerFailureCancellationHostFixture is the typed bare-cancellation
// observation: terminal cancellation, no error identity.
func workerFailureCancellationHostFixture() *WorkerFailureHostObservation {
	return &WorkerFailureHostObservation{
		Tag:       WorkerFailureHostTagCancellation,
		EventType: "session.status",
		SessionID: "ses-attr-1",
	}
}

// workerFailureSourceFixture is one typed bare-cancellation host_event
// source: the absence case every causal rule must survive.
func workerFailureSourceFixture() WorkerFailureSource {
	return WorkerFailureSource{
		Kind:    WorkerFailureSourceHostEvent,
		Locator: "evt-cancel-0001",
		Digest:  testDigestA,
		Host:    workerFailureCancellationHostFixture(),
	}
}

// workerFailureObservationFixture is one valid bare-cancellation-shaped
// observation: host adapter, host_execution, explicit unknown cause and
// basis, one observed host event.
func workerFailureObservationFixture() *WorkerFailureObservation {
	return &WorkerFailureObservation{
		Observer: WorkerFailureObserverHostAdapter,
		Phase:    WorkerFailurePhaseHostExecution,
		Cause:    WorkerFailureCauseUnknown,
		Basis:    WorkerFailureBasisUnknown,
		Sources:  []WorkerFailureSource{workerFailureSourceFixture()},
	}
}

// workerFailureAttemptIdentityFixture is one authorized attempt identity:
// registered lane, positive epoch, pinned packet digest, explicit no_job.
func workerFailureAttemptIdentityFixture() *WorkerFailureIdentity {
	return &WorkerFailureIdentity{
		Kind: WorkerFailureIdentityAttempt,
		Attempt: &WorkerFailureAttemptIdentity{
			AttemptID:    "attempt-attribution",
			LaneID:       "implement",
			Epoch:        1,
			PacketDigest: testDigestQ,
			NoJob:        true,
		},
	}
}

// workerFailureIntegrationIdentityFixture is one combined-run identity: the
// exact native run and the exact accepted input population.
func workerFailureIntegrationIdentityFixture() *WorkerFailureIdentity {
	return &WorkerFailureIdentity{
		Kind: WorkerFailureIdentityIntegration,
		Integration: &WorkerFailureIntegrationIdentity{
			NativeRun:    WorkerFailureSubjectRef{Kind: WorkerFailureSubjectNativeRun, Locator: "run-combined-1", Digest: testDigestG},
			InputResults: []WorkerFailureInputResult{{Job: workerFailureJobFixture(), ResultRef: "result-input-1"}},
		},
	}
}

// workerFailureAttributionFixture is one valid recorded attribution block
// over a job-free authorized attempt.
func workerFailureAttributionFixture() *WorkerFailureAttribution {
	return &WorkerFailureAttribution{
		Origin:      WorkerFailureAttributionRecorded,
		Observation: workerFailureObservationFixture(),
		Identity:    workerFailureAttemptIdentityFixture(),
	}
}

// Every closed observer may emit exactly the phases its coupling table
// declares with agreeing source kinds and a matching identity variant; the
// explicit legacy marker admits; an absent block admits.
func TestWorkerFailureAttributionClosedCouplingAdmits(t *testing.T) {
	t.Parallel()
	if err := ValidateWorkerFailureAttribution(nil); err != nil {
		t.Fatalf("absent attribution block refused: %v", err)
	}
	legacy := &WorkerFailureAttribution{Origin: WorkerFailureAttributionLegacyUnavailable}
	if err := ValidateWorkerFailureAttribution(legacy); err != nil {
		t.Fatalf("explicit legacy_unavailable attribution refused: %v", err)
	}
	for observer, phases := range workerFailureObserverPhases {
		for phase := range phases {
			observation := workerFailureObservationFixture()
			observation.Observer = observer
			observation.Phase = phase
			switch observer {
			case WorkerFailureObserverWorkerReport:
				observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceAdmittedReport, Locator: "evt-report-1", Digest: testDigestB, Message: "worker reported failure"}}
			case WorkerFailureObserverCoordinator:
				if phase == WorkerFailurePhaseIntegration {
					observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceNativeRun, Locator: "run-obs-1", Digest: testDigestH}}
				} else {
					observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: "result-finding-1", Digest: testDigestC, Message: "review finding: control failed"}}
				}
			}
			block := workerFailureAttributionFixture()
			block.Observation = observation
			if phase == WorkerFailurePhaseIntegration {
				block.Identity = workerFailureIntegrationIdentityFixture()
			}
			if err := ValidateWorkerFailureAttribution(block); err != nil {
				t.Fatalf("observer %q phase %q refused: %v", observer, phase, err)
			}
		}
	}
}

func TestWorkerFailureAttributionRefusals(t *testing.T) {
	t.Parallel()
	refusals := []struct {
		name   string
		mutate func(block *WorkerFailureAttribution)
	}{
		{"evidence_origin token is not an attribution origin", func(b *WorkerFailureAttribution) { b.Origin = WorkerEvidenceReported }},
		{"unknown origin", func(b *WorkerFailureAttribution) { b.Origin = "attributed" }},
		{"legacy origin with observation", func(b *WorkerFailureAttribution) { b.Origin = WorkerFailureAttributionLegacyUnavailable }},
		{"legacy origin with identity", func(b *WorkerFailureAttribution) {
			b.Origin = WorkerFailureAttributionLegacyUnavailable
			b.Observation = nil
		}},
		{"recorded origin without observation", func(b *WorkerFailureAttribution) { b.Observation = nil }},
		{"recorded origin without identity", func(b *WorkerFailureAttribution) { b.Identity = nil }},
		{"unknown observer", func(b *WorkerFailureAttribution) { b.Observation.Observer = "vibes" }},
		{"unknown phase", func(b *WorkerFailureAttribution) { b.Observation.Phase = "deploy" }},
		{"host adapter cannot observe worker execution", func(b *WorkerFailureAttribution) { b.Observation.Phase = WorkerFailurePhaseWorkerExecution }},
		{"worker report cannot observe dispatch", func(b *WorkerFailureAttribution) { b.Observation.Observer = WorkerFailureObserverWorkerReport }},
		{"coordinator cannot observe host execution", func(b *WorkerFailureAttribution) {
			b.Observation.Observer = WorkerFailureObserverCoordinator
			b.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: "r1", Digest: testDigestD, Message: "m"}}
		}},
		{"host adapter cannot review results", func(b *WorkerFailureAttribution) { b.Observation.Phase = WorkerFailurePhaseResultReview }},
		{"unknown cause", func(b *WorkerFailureAttribution) { b.Observation.Cause = "blame" }},
		{"unknown basis", func(b *WorkerFailureAttribution) { b.Observation.Basis = "proven" }},
		{"unknown cause with hypothesis basis", func(b *WorkerFailureAttribution) { b.Observation.Basis = WorkerFailureBasisHypothesis }},
		{"unknown cause with supported basis", func(b *WorkerFailureAttribution) { b.Observation.Basis = WorkerFailureBasisSupported }},
		{"named cause without a labeled basis", func(b *WorkerFailureAttribution) { b.Observation.Cause = WorkerFailureCauseHostProvider }},
		{"worker report cannot carry a supported basis", func(b *WorkerFailureAttribution) {
			b.Observation.Observer = WorkerFailureObserverWorkerReport
			b.Observation.Phase = WorkerFailurePhaseWorkerExecution
			b.Observation.Cause = WorkerFailureCauseDependency
			b.Observation.Basis = WorkerFailureBasisSupported
			b.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceAdmittedReport, Locator: "evt-report-1", Digest: testDigestB, Message: "worker reported failure"}}
		}},
		{"absent sources array", func(b *WorkerFailureAttribution) { b.Observation.Sources = nil }},
		{"nine sources", func(b *WorkerFailureAttribution) {
			b.Observation.Sources = make([]WorkerFailureSource, 9)
			for i := range b.Observation.Sources {
				b.Observation.Sources[i] = workerFailureSourceFixture()
			}
		}},
		{"unknown source kind", func(b *WorkerFailureAttribution) { b.Observation.Sources[0].Kind = "rumor" }},
		{"source with no content", func(b *WorkerFailureAttribution) {
			b.Observation.Sources[0].Locator = ""
			b.Observation.Sources[0].Digest = ""
			b.Observation.Sources[0].Message = ""
			b.Observation.Sources[0].Host = nil
		}},
		{"host_event source without a typed host observation", func(b *WorkerFailureAttribution) { b.Observation.Sources[0].Host = nil }},
		{"host observation on a validator source", func(b *WorkerFailureAttribution) {
			b.Observation.Phase = WorkerFailurePhaseReportValidation
			b.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceValidator, Locator: "evt-validate-1", Digest: testDigestD, Host: workerFailureCancellationHostFixture()}}
		}},
		{"host observation on an admitted report source", func(b *WorkerFailureAttribution) {
			b.Observation.Observer = WorkerFailureObserverWorkerReport
			b.Observation.Phase = WorkerFailurePhaseWorkerExecution
			b.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceAdmittedReport, Locator: "evt-report-1", Digest: testDigestB, Host: workerFailureCancellationHostFixture()}}
		}},
		{"host tag outside the closed set", func(b *WorkerFailureAttribution) { b.Observation.Sources[0].Host.Tag = "queued" }},
		{"host event type is required", func(b *WorkerFailureAttribution) { b.Observation.Sources[0].Host.EventType = "" }},
		{"cancellation tag carrying an error code", func(b *WorkerFailureAttribution) { b.Observation.Sources[0].Host.ErrorCode = "429" }},
		{"status code outside the HTTP range", func(b *WorkerFailureAttribution) {
			b.Observation.Sources[0].Host.Tag = WorkerFailureHostTagTerminal
			b.Observation.Sources[0].Host.ErrorName = "bad_gateway"
			b.Observation.Sources[0].Host.StatusCode = 42
		}},
		{"source locator past 512 bytes", func(b *WorkerFailureAttribution) { b.Observation.Sources[0].Locator = strings.Repeat("e", 513) }},
		{"source message past 2048 bytes", func(b *WorkerFailureAttribution) { b.Observation.Sources[0].Message = strings.Repeat("m", 2049) }},
		{"source digest is not the provenance pattern", func(b *WorkerFailureAttribution) { b.Observation.Sources[0].Digest = "deadbeef" }},
		{"source digest past 512 bytes", func(b *WorkerFailureAttribution) {
			b.Observation.Sources[0].Digest = "sha256:" + strings.Repeat("d", 513)
		}},
		{"diagnosis past 1024 bytes", func(b *WorkerFailureAttribution) { b.Observation.Diagnosis = strings.Repeat("x", 1025) }},
		{"diagnosis is not valid UTF-8", func(b *WorkerFailureAttribution) { b.Observation.Diagnosis = string([]byte{0xff, 0xfe}) }},
		{"supported claim without addressable evidence", func(b *WorkerFailureAttribution) {
			b.Observation.Cause = WorkerFailureCauseDependency
			b.Observation.Basis = WorkerFailureBasisSupported
			b.Observation.Sources[0].Digest = ""
		}},
		{"bare cancellation with a message cannot support a provider cause", func(b *WorkerFailureAttribution) {
			b.Observation.Cause = WorkerFailureCauseHostProvider
			b.Observation.Basis = WorkerFailureBasisSupported
			b.Observation.Sources[0].Message = "Task cancelled"
		}},
		{"bare cancellation cannot support an implementation cause", func(b *WorkerFailureAttribution) {
			b.Observation.Cause = WorkerFailureCauseImplementation
			b.Observation.Basis = WorkerFailureBasisSupported
			b.Observation.Sources[0].Message = "Task cancelled"
		}},
		{"held retry with an error code cannot support a provider cause", func(b *WorkerFailureAttribution) {
			b.Observation.Cause = WorkerFailureCauseHostProvider
			b.Observation.Basis = WorkerFailureBasisSupported
			b.Observation.Sources[0].Host = &WorkerFailureHostObservation{Tag: WorkerFailureHostTagRetry, EventType: "session.error", SessionID: "ses-attr-1", ErrorName: "rate_limit_exceeded", ErrorCode: "429"}
			b.Observation.Sources[0].Message = "retrying child"
		}},
		{"terminal tag without error identity cannot support a provider cause", func(b *WorkerFailureAttribution) {
			b.Observation.Cause = WorkerFailureCauseHostProvider
			b.Observation.Basis = WorkerFailureBasisSupported
			b.Observation.Sources[0].Host = &WorkerFailureHostObservation{Tag: WorkerFailureHostTagTerminal, EventType: "session.status", SessionID: "ses-attr-1"}
			b.Observation.Sources[0].Message = "process exited"
		}},
		{"worker report cannot cite a host event", func(b *WorkerFailureAttribution) {
			b.Observation.Observer = WorkerFailureObserverWorkerReport
			b.Observation.Phase = WorkerFailurePhaseWorkerExecution
		}},
		{"host adapter cannot cite a result source", func(b *WorkerFailureAttribution) {
			b.Observation.Phase = WorkerFailurePhaseReportValidation
			b.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: "result-1", Digest: testDigestD}}
		}},
		{"coordinator cannot cite an admitted report", func(b *WorkerFailureAttribution) {
			b.Observation.Observer = WorkerFailureObserverCoordinator
			b.Observation.Phase = WorkerFailurePhaseResultReview
			b.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceAdmittedReport, Locator: "evt-report-1", Digest: testDigestB}}
		}},
		{"attempt identity on the integration phase", func(b *WorkerFailureAttribution) {
			b.Observation.Observer = WorkerFailureObserverCoordinator
			b.Observation.Phase = WorkerFailurePhaseIntegration
			b.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceNativeRun, Locator: "run-obs-1", Digest: testDigestH}}
		}},
		{"no_job absent with no job binding", func(b *WorkerFailureAttribution) {
			b.Identity.Attempt.NoJob = false
			b.Identity.Attempt.Job = nil
		}},
		{"attempt without a lane", func(b *WorkerFailureAttribution) { b.Identity.Attempt.LaneID = "" }},
		{"attempt with an unregistered lane", func(b *WorkerFailureAttribution) { b.Identity.Attempt.LaneID = "vibes" }},
		{"attempt with a zero epoch", func(b *WorkerFailureAttribution) { b.Identity.Attempt.Epoch = 0 }},
		{"attempt with a negative epoch", func(b *WorkerFailureAttribution) { b.Identity.Attempt.Epoch = -1 }},
		{"attempt packet digest is not the provenance pattern", func(b *WorkerFailureAttribution) { b.Identity.Attempt.PacketDigest = "deadbeef" }},
		{"job binding job_id outside the job pattern", func(b *WorkerFailureAttribution) {
			b.Identity.Attempt.NoJob = false
			b.Identity.Attempt.Job = &WorkerJobBinding{JobID: "$bad", Revision: 1, Digest: testDigestE}
		}},
		{"job binding digest is not the provenance pattern", func(b *WorkerFailureAttribution) {
			b.Identity.Attempt.NoJob = false
			b.Identity.Attempt.Job = &WorkerJobBinding{JobID: "job-attribution", Revision: 1, Digest: "d"}
		}},
		{"job binding past revision zero", func(b *WorkerFailureAttribution) {
			b.Identity.Attempt.NoJob = false
			b.Identity.Attempt.Job = &WorkerJobBinding{JobID: "job-attribution", Revision: 0, Digest: testDigestE}
		}},
		{"subject kind outside the closed set", func(b *WorkerFailureAttribution) {
			b.Identity.Attempt.Subject = &WorkerFailureSubjectRef{Kind: "commit_oid", Locator: "c0ffee"}
		}},
		{"subject without a locator", func(b *WorkerFailureAttribution) {
			b.Identity.Attempt.Subject = &WorkerFailureSubjectRef{Kind: WorkerFailureSubjectObservedDeliveryEvent}
		}},
		{"subject locator past 512 bytes", func(b *WorkerFailureAttribution) {
			b.Identity.Attempt.Subject = &WorkerFailureSubjectRef{Kind: WorkerFailureSubjectObservedDeliveryEvent, Locator: strings.Repeat("s", 513)}
		}},
		{"subject digest is not the provenance pattern", func(b *WorkerFailureAttribution) {
			b.Identity.Attempt.Subject = &WorkerFailureSubjectRef{Kind: WorkerFailureSubjectObservedDeliveryEvent, Locator: "evt-complete-0001", Digest: "beef"}
		}},
	}
	for _, refusal := range refusals {
		block := workerFailureAttributionFixture()
		refusal.mutate(block)
		err := ValidateWorkerFailureAttribution(block)
		if !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("%s error = %v, want %s", refusal.name, err, KindInvalidPayload)
		}
	}
	if err := ValidateWorkerFailureAttribution(func() *WorkerFailureAttribution {
		block := workerFailureAttributionFixture()
		block.Observation.Observer = WorkerFailureObserverWorkerReport
		block.Observation.Phase = WorkerFailurePhaseWorkerExecution
		block.Observation.Cause = WorkerFailureCauseDependency
		block.Observation.Basis = WorkerFailureBasisSupported
		block.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceAdmittedReport, Locator: "evt-report-1", Digest: testDigestB, Message: "worker reported failure"}}
		return block
	}()); !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker report stays a claim") {
		t.Fatalf("supported worker report error = %v, want the worker-claims-cap refusal", err)
	}
	if err := ValidateWorkerFailureAttribution(func() *WorkerFailureAttribution {
		block := workerFailureAttributionFixture()
		block.Observation.Cause = WorkerFailureCauseHostProvider
		block.Observation.Basis = WorkerFailureBasisSupported
		block.Observation.Sources[0].Message = "Task cancelled"
		return block
	}()); !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "bare cancellation") {
		t.Fatalf("bare-cancel provider cause error = %v, want the bare-cancellation refusal", err)
	}
	if err := ValidateWorkerFailureAttribution(func() *WorkerFailureAttribution {
		block := workerFailureAttributionFixture()
		block.Observation.Cause = WorkerFailureCauseHostProvider
		block.Observation.Basis = WorkerFailureBasisSupported
		block.Observation.Sources[0].Host = &WorkerFailureHostObservation{Tag: WorkerFailureHostTagRetry, EventType: "session.error", ErrorName: "rate_limit_exceeded", ErrorCode: "429"}
		return block
	}()); !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "held retry") {
		t.Fatalf("held-retry provider cause error = %v, want the held-retry refusal", err)
	}
}

func TestWorkerFailureAttributionAdmissions(t *testing.T) {
	t.Parallel()
	if err := ValidateWorkerFailureAttribution(workerFailureAttributionFixture()); err != nil {
		t.Fatalf("fixture attribution refused: %v", err)
	}
	supportedProvider := workerFailureAttributionFixture()
	supportedProvider.Observation.Cause = WorkerFailureCauseHostProvider
	supportedProvider.Observation.Basis = WorkerFailureBasisSupported
	supportedProvider.Observation.Sources[0].Host = workerFailureHostFixture()
	if err := ValidateWorkerFailureAttribution(supportedProvider); err != nil {
		t.Fatalf("supported typed terminal provider error refused: %v", err)
	}
	workerHypothesis := workerFailureAttributionFixture()
	workerHypothesis.Observation.Observer = WorkerFailureObserverWorkerReport
	workerHypothesis.Observation.Phase = WorkerFailurePhaseWorkerExecution
	workerHypothesis.Observation.Cause = WorkerFailureCauseDependency
	workerHypothesis.Observation.Basis = WorkerFailureBasisHypothesis
	workerHypothesis.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceAdmittedReport, Locator: "evt-report-1", Digest: testDigestB, Message: "upstream 503"}}
	workerHypothesis.Observation.Diagnosis = "the worker labels the dependency; the claim stays a hypothesis"
	if err := ValidateWorkerFailureAttribution(workerHypothesis); err != nil {
		t.Fatalf("worker hypothesis with citations refused: %v", err)
	}
	reportContract := workerFailureAttributionFixture()
	reportContract.Observation.Phase = WorkerFailurePhaseReportValidation
	reportContract.Observation.Cause = WorkerFailureCauseReportContract
	reportContract.Observation.Basis = WorkerFailureBasisSupported
	reportContract.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceValidator, Locator: "evt-validate-1", Digest: testDigestD, Message: "report schema 2.0: review.verdict required"}}
	if err := ValidateWorkerFailureAttribution(reportContract); err != nil {
		t.Fatalf("validator-backed report_contract cause refused: %v", err)
	}
	// A coordinator's supported implementation diagnosis rests on the typed
	// joinable control evidence it cites, never on prose inside a message.
	supportedImplementation := workerFailureAttributionFixture()
	supportedImplementation.Observation.Observer = WorkerFailureObserverCoordinator
	supportedImplementation.Observation.Phase = WorkerFailurePhaseResultReview
	supportedImplementation.Observation.Cause = WorkerFailureCauseImplementation
	supportedImplementation.Observation.Basis = WorkerFailureBasisSupported
	supportedImplementation.Observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceNativeRun, Locator: "run-control-1", Digest: testDigestH}}
	if err := ValidateWorkerFailureAttribution(supportedImplementation); err != nil {
		t.Fatalf("coordinator implementation diagnosis on native control evidence refused: %v", err)
	}
}

// The core-derived identity is a closed union: exactly one of the no_job
// marker or a job binding, an authorized attempt carrying its lane, epoch,
// and packet digest, and a typed immutable subject reference instead of a
// copied commit field.
func TestWorkerFailureAttributionIdentityUnion(t *testing.T) {
	t.Parallel()
	withJob := workerFailureAttributionFixture()
	withJob.Identity.Attempt.AttemptID = "attempt-job-bound"
	withJob.Identity.Attempt.NoJob = false
	withJob.Identity.Attempt.Job = workerFailureJobFixture()
	if err := ValidateWorkerFailureAttribution(withJob); err != nil {
		t.Fatalf("job-bound attempt identity refused: %v", err)
	}
	noJobWithJob := workerFailureAttributionFixture()
	noJobWithJob.Identity.Attempt.Job = workerFailureJobFixture()
	if err := ValidateWorkerFailureAttribution(noJobWithJob); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("no_job with a job binding error = %v, want %s", err, KindInvalidPayload)
	}
	refusals := []struct {
		name   string
		mutate func(block *WorkerFailureAttribution)
	}{
		{"attempt kind without the attempt variant", func(b *WorkerFailureAttribution) { b.Identity.Attempt = nil }},
		{"attempt kind with both variants", func(b *WorkerFailureAttribution) {
			b.Identity.Integration = workerFailureIntegrationIdentityFixture().Integration
		}},
		{"identity kind outside the closed union", func(b *WorkerFailureAttribution) { b.Identity.Kind = "guess" }},
		{"empty attempt id", func(b *WorkerFailureAttribution) { b.Identity.Attempt.AttemptID = "" }},
		{"attempt id past 256 bytes", func(b *WorkerFailureAttribution) { b.Identity.Attempt.AttemptID = strings.Repeat("a", 257) }},
		{"negative epoch", func(b *WorkerFailureAttribution) { b.Identity.Attempt.Epoch = -7 }},
	}
	for _, refusal := range refusals {
		block := workerFailureAttributionFixture()
		refusal.mutate(block)
		if err := ValidateWorkerFailureAttribution(block); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("%s error = %v, want %s", refusal.name, err, KindInvalidPayload)
		}
	}
	subjects := workerFailureAttributionFixture()
	subjects.Identity.Attempt.Subject = &WorkerFailureSubjectRef{Kind: WorkerFailureSubjectObservedDeliveryEvent, Locator: "evt-complete-0001", Digest: testDigestF}
	if err := ValidateWorkerFailureAttribution(subjects); err != nil {
		t.Fatalf("observed-delivery event subject reference refused: %v", err)
	}
	nativeSubject := workerFailureAttributionFixture()
	nativeSubject.Identity.Attempt.Subject = &WorkerFailureSubjectRef{Kind: WorkerFailureSubjectNativeRun, Locator: "run-ref-1"}
	if err := ValidateWorkerFailureAttribution(nativeSubject); err != nil {
		t.Fatalf("native-run subject reference refused: %v", err)
	}

	integration := func() *WorkerFailureAttribution {
		return &WorkerFailureAttribution{
			Origin: WorkerFailureAttributionRecorded,
			Observation: func() *WorkerFailureObservation {
				observation := workerFailureObservationFixture()
				observation.Observer = WorkerFailureObserverCoordinator
				observation.Phase = WorkerFailurePhaseIntegration
				observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceNativeRun, Locator: "run-obs-1", Digest: testDigestH}}
				return observation
			}(),
			Identity: workerFailureIntegrationIdentityFixture(),
		}
	}
	if err := ValidateWorkerFailureAttribution(integration()); err != nil {
		t.Fatalf("integration identity refused: %v", err)
	}
	secondJob := &WorkerJobBinding{JobID: "job-attribution-b", Revision: 1, Digest: testDigestF}
	integrationRefusals := []struct {
		name   string
		mutate func(block *WorkerFailureAttribution)
	}{
		{"integration identity on a result review phase", func(b *WorkerFailureAttribution) { b.Observation.Phase = WorkerFailurePhaseResultReview }},
		{"integration identity under the host adapter", func(b *WorkerFailureAttribution) { b.Observation.Observer = WorkerFailureObserverHostAdapter }},
		{"integration kind without the integration variant", func(b *WorkerFailureAttribution) { b.Identity = workerFailureAttemptIdentityFixture() }},
		{"integration without the combined native run", func(b *WorkerFailureAttribution) { b.Identity.Integration.NativeRun = WorkerFailureSubjectRef{} }},
		{"integration run reference is not a native run", func(b *WorkerFailureAttribution) {
			b.Identity.Integration.NativeRun.Kind = WorkerFailureSubjectObservedDeliveryEvent
		}},
		{"integration without input results", func(b *WorkerFailureAttribution) { b.Identity.Integration.InputResults = nil }},
		{"integration input without a job binding", func(b *WorkerFailureAttribution) { b.Identity.Integration.InputResults[0].Job = nil }},
		{"integration input without a result reference", func(b *WorkerFailureAttribution) { b.Identity.Integration.InputResults[0].ResultRef = "" }},
		{"integration input result reference past 512 bytes", func(b *WorkerFailureAttribution) {
			b.Identity.Integration.InputResults[0].ResultRef = strings.Repeat("r", 513)
		}},
		{"duplicate input job revision and result", func(b *WorkerFailureAttribution) {
			b.Identity.Integration.InputResults = append(b.Identity.Integration.InputResults, b.Identity.Integration.InputResults[0])
		}},
		{"duplicate input job revision with another result", func(b *WorkerFailureAttribution) {
			b.Identity.Integration.InputResults = append(b.Identity.Integration.InputResults, WorkerFailureInputResult{Job: b.Identity.Integration.InputResults[0].Job, ResultRef: "result-input-2"})
		}},
		{"duplicate input result reference across jobs", func(b *WorkerFailureAttribution) {
			b.Identity.Integration.InputResults = append(b.Identity.Integration.InputResults, WorkerFailureInputResult{Job: secondJob, ResultRef: "result-input-1"})
		}},
	}
	for _, refusal := range integrationRefusals {
		block := integration()
		refusal.mutate(block)
		if err := ValidateWorkerFailureAttribution(block); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("%s error = %v, want %s", refusal.name, err, KindInvalidPayload)
		}
	}
}

// The canonical observation digest carries the repository sha256 prefix, is
// deterministic for the same content, changes when any observed member
// changes, treats the source order as explicit, and refuses observations
// that fail structural validation or serialize past the block bound.
func TestWorkerFailureObservationDigestDeterministicAndSensitive(t *testing.T) {
	t.Parallel()
	base := workerFailureObservationFixture()
	base.Sources = append(base.Sources, WorkerFailureSource{Kind: WorkerFailureSourceValidator, Locator: "validator-2", Digest: testDigestH, Message: "second observation"})
	digest, err := WorkerFailureObservationDigest(base)
	if err != nil {
		t.Fatalf("fixture observation digest refused: %v", err)
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		t.Fatalf("observation digest = %q, want the sha256-prefixed repository digest shape", digest)
	}
	again, err := WorkerFailureObservationDigest(base)
	if err != nil || again != digest {
		t.Fatalf("digest not deterministic: %q then %q (%v)", digest, again, err)
	}
	reordered := workerFailureObservationFixture()
	reordered.Sources = append(reordered.Sources, base.Sources[1])
	reordered.Sources[0], reordered.Sources[1] = reordered.Sources[1], reordered.Sources[0]
	reorderedDigest, err := WorkerFailureObservationDigest(reordered)
	if err != nil {
		t.Fatalf("reordered observation digest refused: %v", err)
	}
	if reorderedDigest == digest {
		t.Fatal("reordering sources kept the canonical observation digest; source order must be explicit")
	}
	mutations := []struct {
		name   string
		mutate func(observation *WorkerFailureObservation)
	}{
		{"cause relabel", func(o *WorkerFailureObservation) {
			o.Cause = WorkerFailureCauseDependency
			o.Basis = WorkerFailureBasisHypothesis
		}},
		{"diagnosis text", func(o *WorkerFailureObservation) { o.Diagnosis = "the boundary saw the terminal cancel" }},
		{"source message", func(o *WorkerFailureObservation) { o.Sources[0].Message = "session.status: cancelled" }},
		{"source digest", func(o *WorkerFailureObservation) { o.Sources[0].Digest = testDigestI }},
		{"host tag", func(o *WorkerFailureObservation) { o.Sources[0].Host.Tag = WorkerFailureHostTagRetry }},
		{"host terminal error identity", func(o *WorkerFailureObservation) {
			o.Sources[0].Host.Tag = WorkerFailureHostTagTerminal
			o.Sources[0].Host.EventType = "session.error"
			o.Sources[0].Host.ErrorCode = "500"
		}},
	}
	for _, mutation := range mutations {
		changed := workerFailureObservationFixture()
		changed.Sources = append(changed.Sources, base.Sources[1])
		mutation.mutate(changed)
		changedDigest, err := WorkerFailureObservationDigest(changed)
		if err != nil {
			t.Fatalf("%s: mutated observation refused: %v", mutation.name, err)
		}
		if changedDigest == digest {
			t.Fatalf("%s: mutation kept the canonical observation digest", mutation.name)
		}
	}
	invalid := workerFailureObservationFixture()
	invalid.Observer = "vibes"
	if _, err := WorkerFailureObservationDigest(invalid); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("invalid observation digest error = %v, want %s", err, KindInvalidPayload)
	}
	if _, err := WorkerFailureObservationDigest(nil); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("nil observation digest error = %v, want %s", err, KindInvalidPayload)
	}
	oversized := workerFailureObservationFixture()
	oversized.Sources = make([]WorkerFailureSource, 0, WorkerFailureAttributionMaxSources)
	for i := 0; i < WorkerFailureAttributionMaxSources; i++ {
		oversized.Sources = append(oversized.Sources, WorkerFailureSource{
			Kind:    WorkerFailureSourceHostEvent,
			Locator: "evt-" + string(rune('a'+i)),
			Digest:  testDigestP,
			Message: strings.Repeat("m", 1100),
			Host:    workerFailureCancellationHostFixture(),
		})
	}
	encoded, err := json.Marshal(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= WorkerFailureAttributionMaxBlockBytes {
		t.Fatalf("fixture serialized to %d bytes; the oversized observation must exceed %d", len(encoded), WorkerFailureAttributionMaxBlockBytes)
	}
	if _, err := WorkerFailureObservationDigest(oversized); !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "never truncated") {
		t.Fatalf("oversized observation digest error = %v, want the bound refusal", err)
	}
}

// The strict decoder refuses unknown members at every level, duplicate JSON
// keys, trailing content, and bytes that are not valid UTF-8, bounds raw
// input before any decode work, and hands the decoded block to the
// structural validator.
func TestWorkerFailureAttributionDecodeStrict(t *testing.T) {
	t.Parallel()
	valid := mustJSONValue(workerFailureAttributionFixture())
	decoded, err := DecodeWorkerFailureAttribution(valid)
	if err != nil {
		t.Fatalf("fixture attribution decode refused: %v", err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) != string(valid) {
		t.Fatalf("decode changed the attribution bytes:\n%s\nwant\n%s", reencoded, valid)
	}
	legacy, err := DecodeWorkerFailureAttribution([]byte(`{"origin":"legacy_unavailable"}`))
	if err != nil {
		t.Fatalf("legacy_unavailable decode refused: %v", err)
	}
	if legacy.Observation != nil || legacy.Identity != nil {
		t.Fatal("legacy_unavailable decode produced attribution content; the marker must stay empty")
	}
	refusals := []struct {
		name     string
		raw      string
		contains string
	}{
		{"unknown top member", `{"origin":"legacy_unavailable","extra":1}`, "unknown field"},
		{"unknown member inside a source", `{"origin":"recorded","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e1","message":"m","surprise":1}]}}`, "unknown field"},
		{"unknown member inside a host observation", `{"origin":"recorded","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e1","host":{"tag":"terminal","event_type":"session.error","phantom":true}}]}}`, "unknown field"},
		{"unknown member inside the attempt identity", `{"origin":"recorded","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e1"}]},"identity":{"kind":"attempt","attempt":{"attempt_id":"a1","lane_id":"implement","epoch":1,"packet_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","no_job":true,"phantom":true}}}`, "unknown field"},
		{"duplicate JSON member", `{"origin":"recorded","origin":"legacy_unavailable"}`, "duplicate"},
		{"trailing JSON content", `{"origin":"legacy_unavailable"}{}`, "closed JSON object"},
		{"observation outside the closed vocabulary", `{"origin":"recorded","observation":{"observer":"vibes","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e1","message":"m"}]}}`, "observer"},
	}
	for _, refusal := range refusals {
		_, err := DecodeWorkerFailureAttribution([]byte(refusal.raw))
		if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), refusal.contains) {
			t.Fatalf("%s error = %v, want %s naming %q", refusal.name, err, KindInvalidPayload, refusal.contains)
		}
	}
	notUTF8 := append([]byte(`{"origin":"re`), 0xff)
	notUTF8 = append(notUTF8, []byte(`corded"}`)...)
	if _, err := DecodeWorkerFailureAttribution(notUTF8); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("non-UTF-8 attribution error = %v, want %s", err, KindInvalidPayload)
	}
	if _, err := DecodeWorkerFailureAttribution(nil); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("empty attribution decode error = %v, want %s", err, KindInvalidPayload)
	}
	oversized := []byte(`{"origin":"recorded","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e1","message":"` + strings.Repeat("x", 4*WorkerFailureAttributionMaxDecodeBytes) + `"}]}}`)
	if _, err := DecodeWorkerFailureAttribution(oversized); !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "decode ceiling") {
		t.Fatalf("oversized raw attribution error = %v, want the raw decode ceiling refusal", err)
	}
}

// The 8192-byte whole-block bound refuses an over-bound block whole and
// never truncates or rewrites it.
func TestWorkerFailureAttributionBlockBoundRefusesWholeNeverTruncates(t *testing.T) {
	t.Parallel()
	build := func(messageBytes int) *WorkerFailureAttribution {
		block := workerFailureAttributionFixture()
		block.Observation.Sources = make([]WorkerFailureSource, 0, WorkerFailureAttributionMaxSources)
		for i := 0; i < WorkerFailureAttributionMaxSources; i++ {
			block.Observation.Sources = append(block.Observation.Sources, WorkerFailureSource{
				Kind:    WorkerFailureSourceHostEvent,
				Locator: "evt-" + string(rune('a'+i)),
				Digest:  testDigestP,
				Message: strings.Repeat("m", messageBytes),
				Host:    workerFailureCancellationHostFixture(),
			})
		}
		return block
	}
	atBound := build(700)
	atBoundBytes, err := json.Marshal(atBound)
	if err != nil {
		t.Fatal(err)
	}
	if len(atBoundBytes) > WorkerFailureAttributionMaxBlockBytes {
		t.Fatalf("fixture serialized to %d bytes; the at-bound case must fit %d", len(atBoundBytes), WorkerFailureAttributionMaxBlockBytes)
	}
	if err := ValidateWorkerFailureAttribution(atBound); err != nil {
		t.Fatalf("at-bound attribution refused: %v", err)
	}
	over := build(1100)
	before, err := json.Marshal(over)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) <= WorkerFailureAttributionMaxBlockBytes {
		t.Fatalf("fixture serialized to %d bytes; the over-bound case must exceed %d", len(before), WorkerFailureAttributionMaxBlockBytes)
	}
	if err := ValidateWorkerFailureAttribution(over); !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "never truncated") {
		t.Fatalf("over-bound attribution error = %v, want the whole-block bound refusal", err)
	}
	after, err := json.Marshal(over)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("the block-bound refusal rewrote or truncated the attribution block")
	}
}
