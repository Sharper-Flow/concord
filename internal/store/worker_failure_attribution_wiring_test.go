package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Phase 2 production wiring witnesses. These tests drive the real
// seams: the signed terminal ingest (ApplyWorkerEvidenceOperationTx), the
// lane-actor dispatch route (PrepareLaneActorDispatch), the workflow action
// dispatcher, and log-ordered rebuild. They pin the version-marker rules
// (upcast-generated legacy absence versus forged new-legacy bytes), the
// exact core identity joins, the job-subset predicate ties on promoted
// pins, the coordinator correction attribution, and the derived failure
// record's attribution reference.

// attributionWorkerFailedEvent builds one worker.failed event at the current
// registry version carrying the recorded producer observation, exactly as
// the authenticated CLI assembles it: origin and observation only, never an
// identity.
func attributionWorkerFailedEvent(workID, attemptID, eventID, model string, observation *WorkerFailureObservation) Event {
	payload := WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: model, FailureKind: WorkerFailureWorkerError, Detail: "the attempt failed before a judgeable result"}
	if observation != nil {
		payload.FailureAttribution = &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: observation}
	}
	return Event{EventID: eventID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(payload)}
}

// attributionHostObservation is one host_adapter host_execution observation
// for a bare cancellation.
func attributionHostObservation() *WorkerFailureObservation {
	return workerFailureObservationFixture()
}

// attributionWorkerObservation is one worker_report worker_execution
// observation citing admitted report evidence.
func attributionWorkerObservation() *WorkerFailureObservation {
	return &WorkerFailureObservation{
		Observer: WorkerFailureObserverWorkerReport,
		Phase:    WorkerFailurePhaseWorkerExecution,
		Cause:    WorkerFailureCauseUnknown,
		Basis:    WorkerFailureBasisUnknown,
		Sources:  []WorkerFailureSource{{Kind: WorkerFailureSourceAdmittedReport, Locator: "report-1", Digest: testDigestB, Message: "worker reported the failed command"}},
	}
}

// attributionStoredFailurePayload reads one stored worker.failed payload.
func attributionStoredFailurePayload(t *testing.T, s *Store, workID, eventID string) WorkerFailedPayload {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND event_id=?`, workID, WorkerFailed, eventID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload WorkerFailedPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// attributionStoredDispatchPayload reads one stored worker.dispatched
// payload by attempt.
func attributionStoredDispatchPayload(t *testing.T, s *Store, workID, attemptID string) WorkerDispatchedPayload {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkerDispatched, attemptID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload WorkerDispatchedPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// attributionDispatchWindow reads the authorized window facts the fold
// compares the derived identity against.
func attributionDispatchWindow(t *testing.T, s *Store, workID, attemptID string) WorkerDispatchWindow {
	t.Helper()
	var window WorkerDispatchWindow
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	window, err = FindAuthorizedDispatchWindowTx(context.Background(), tx, workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	return window
}

func TestWorkerFailureAttributionRequiredOnNewPins(t *testing.T) {
	for _, version := range []int{WorkerEvidenceEventPayloadVersion(WorkerFailed), 3} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			workID := "attribution-required-" + strconv.Itoa(version)
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
			defer fixture.store.Close()
			lane := reviewGateLane(t, "implementation")
			attempt := "attempt:" + workID
			dispatchJobBoundAttempt(t, fixture.store, workID, "repair", attempt, nil, fixture.owner, 0, "dispatch-"+workID, &WorkerJobBinding{JobID: "job:" + workID})
			event := attributionWorkerFailedEvent(workID, attempt, "missing-"+workID, preferredModelForLane(lane), nil)
			event.PayloadVersion = version
			if err := s2WorkerFailedLive(t, fixture.store, event); err == nil || !strings.Contains(err.Error(), "requires recorded failure_attribution") {
				t.Fatalf("new pin admitted a missing or old-version attribution instead of refusing at its owner: %v", err)
			}
		})
	}
	// A born-failed terminal dispatch on a promoted pin owes the same
	// recorded observation: an authorization that consumes its window at the
	// attribution-capable version cannot close it without one, and the
	// upcast's legacy marker cannot stand in for it.
	func() {
		workID := "attribution-required-born-failed"
		fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
		defer fixture.store.Close()
		lane := reviewGateLane(t, "implementation")
		job := &WorkerJobBinding{JobID: "job:" + workID}
		attempt := "attempt:" + workID + ":born"
		recordWorkerJobRevisionForTest(t, fixture.store, workID, fixture.owner, job)
		packet := dispatchWorkerPacket(t, fixture.store, workID, "repair", attempt)
		packet["schema_version"] = WorkerPacketSchemaVersion
		packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, fixture.store, workID, *job, "implement")
		if err := dispatchJobPacketForTest(t, fixture.store, workID, attempt, fixture.owner, "attribution-required-born-authorization", packet); err != nil {
			t.Fatalf("the born-failed authorization refused: %v", err)
		}
		window := attributionDispatchWindow(t, fixture.store, workID, attempt)
		dispatch := Event{EventID: "born-missing-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(22, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{
			AttemptID: attempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
			PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: window.PacketDigest,
			ReadbackModel: preferredModelForLane(lane), Terminal: "failed", TerminalFailureKind: WorkerFailureModelIdentity, TerminalDetail: "undeclared executing model",
			WorkerJob: job,
		})}
		err := fixture.store.Transact(context.Background(), func(transaction *Transaction) error {
			prepared, prepErr := PrepareLaneActorDispatch(context.Background(), transaction, dispatch, "principal/operator", "client/concord-1")
			if prepErr != nil {
				return prepErr
			}
			_, appendErr := AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
			return appendErr
		})
		if err == nil || !strings.Contains(err.Error(), "requires recorded failure_attribution") {
			t.Fatalf("new pin admitted a born-failed terminal without recorded attribution instead of refusing at its owner: %v", err)
		}
	}()
}

// TestWorkerFailureAttributionSeamDerivesExactCoreIdentity pins the ingest
// enrichment: a recorded observation arrives without identity and the store
// derives the exact authorized attempt identity before the fold. The same
// worker_error failure kind records a different typed observer per route,
// and both terminals keep the released failure disposition and retry
// authority (A10).
func TestWorkerFailureAttributionSeamDerivesExactCoreIdentity(t *testing.T) {
	t.Parallel()
	const workID = "attribution-seam-identity"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	job := &WorkerJobBinding{JobID: "job:" + workID}
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "attribution-dispatch-"+workID, job)
	window := attributionDispatchWindow(t, s, workID, attempt)

	if err := s2WorkerFailedLive(t, s, attributionWorkerFailedEvent(workID, attempt, "failed-attribution-1", preferredModelForLane(lane), attributionHostObservation())); err != nil {
		t.Fatalf("the attributed host terminal ingest refused: %v", err)
	}
	payload := attributionStoredFailurePayload(t, s, workID, "failed-attribution-1")
	if payload.FailureAttribution == nil || payload.FailureAttribution.Origin != WorkerFailureAttributionRecorded {
		t.Fatalf("stored attribution = %+v, want the recorded origin", payload.FailureAttribution)
	}
	if payload.FailureAttribution.Identity == nil || payload.FailureAttribution.Identity.Kind != WorkerFailureIdentityAttempt || payload.FailureAttribution.Identity.Attempt == nil {
		t.Fatalf("stored identity = %+v, want the core-derived attempt variant", payload.FailureAttribution.Identity)
	}
	identity := payload.FailureAttribution.Identity.Attempt
	if identity.AttemptID != attempt || identity.LaneID != lane.ID || identity.Epoch != window.AttemptEpoch || identity.PacketDigest != window.PacketDigest {
		t.Fatalf("derived identity = %+v, want the exact authorized window (epoch %d digest %s)", identity, window.AttemptEpoch, window.PacketDigest)
	}
	if identity.Subject != nil {
		t.Fatalf("derived identity subject = %+v, want no subject on a failed attempt", identity.Subject)
	}
	if identity.NoJob || identity.Job == nil || *identity.Job != *job {
		t.Fatalf("derived identity job = (no_job=%v job=%+v), want the dispatched revision", identity.NoJob, identity.Job)
	}
	// The terminal keeps its released disposition: one failed attempt of the
	// same failure kind, exactly as an un-attributed terminal lands.
	if attemptFailureKind(t, s, workID, attempt) != WorkerFailureWorkerError {
		t.Fatal("the attributed terminal changed the failure disposition")
	}

	// A second attempt on a fresh work item records the same failure kind
	// through the worker report route: the observer, phase, and source stay
	// typed per boundary while the retry authority stays identical.
	const workID2 = "attribution-seam-identity-worker"
	fixture2 := seedWorkflowReturnRouteFixture(t, workID2, "workflow.break_fix", "repair")
	defer fixture2.store.Close()
	lane2 := reviewGateLane(t, "implementation")
	attempt2 := "attempt:" + workID2 + ":1"
	dispatchJobBoundAttempt(t, fixture2.store, workID2, "repair", attempt2, nil, fixture2.owner, 0, "attribution-dispatch-"+workID2, &WorkerJobBinding{JobID: "job:" + workID2})
	if err := s2WorkerFailedLive(t, fixture2.store, attributionWorkerFailedEvent(workID2, attempt2, "failed-attribution-2", preferredModelForLane(lane2), attributionWorkerObservation())); err != nil {
		t.Fatalf("the attributed worker-report terminal ingest refused: %v", err)
	}
	workerPayload := attributionStoredFailurePayload(t, fixture2.store, workID2, "failed-attribution-2")
	observation := workerPayload.FailureAttribution.Observation
	if observation.Observer != WorkerFailureObserverWorkerReport || observation.Phase != WorkerFailurePhaseWorkerExecution || observation.Sources[0].Kind != WorkerFailureSourceAdmittedReport {
		t.Fatalf("worker terminal observation = %+v, want the worker report boundary's typed view", observation)
	}
	if payload.FailureAttribution.Observation.Observer == observation.Observer {
		t.Fatal("the host and worker routes recorded the same observer for the same failure kind")
	}
	if attemptFailureKind(t, fixture2.store, workID2, attempt2) != WorkerFailureWorkerError {
		t.Fatal("the worker-report terminal changed the failure disposition")
	}
}

func attemptFailureKind(t *testing.T, s *Store, workID, attemptID string) string {
	t.Helper()
	var kind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT failure_kind FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	return kind
}

// TestWorkerFailureAttributionForgedAndSmuggledRefuse pins the source
// version marker rules at the fold: a live legacy marker is a forged
// new-legacy claim, producer-authored identity is refused at the seam, and
// attribution content on a stored source version below the boundary refuses
// on replay and rebuild (live and rebuild refuse the same way).
func TestWorkerFailureAttributionForgedAndSmuggledRefuse(t *testing.T) {
	t.Parallel()
	const workID = "attribution-forged"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "attribution-forged-dispatch", &WorkerJobBinding{JobID: "job:" + workID})

	// A producer cannot author the legacy origin at the seam.
	forgedLegacy := attributionWorkerFailedEvent(workID, attempt, "failed-attribution-legacy", preferredModelForLane(lane), nil)
	forgedLegacy.Payload = mustJSONValue(WorkerFailedPayload{AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureWorkerError, Detail: "the attempt failed", FailureAttribution: workerFailureLegacyAttribution()})
	if err := s2WorkerFailedLive(t, s, forgedLegacy); err == nil {
		t.Fatal("the seam admitted a producer-authored legacy_unavailable origin")
	}
	// A producer cannot author identity.
	authored := attributionWorkerFailedEvent(workID, attempt, "failed-attribution-authored", preferredModelForLane(lane), attributionHostObservation())
	var members map[string]json.RawMessage
	if err := json.Unmarshal(authored.Payload, &members); err != nil {
		t.Fatal(err)
	}
	members["failure_attribution"], _ = json.Marshal(&WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: attributionHostObservation(), Identity: workerFailureAttemptIdentityFixture()})
	authored.Payload, _ = json.Marshal(members)
	if err := s2WorkerFailedLive(t, s, authored); err == nil {
		t.Fatal("the seam admitted a producer-authored core identity")
	}
	var refused int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE event_id IN ('failed-attribution-legacy','failed-attribution-authored')`).Scan(&refused); err != nil {
		t.Fatal(err)
	}
	if refused != 0 {
		t.Fatalf("the refused ingests left %d events behind, want zero writes", refused)
	}

	// A live raw append of the legacy marker at the capable version is a
	// forged new-legacy claim the fold refuses.
	rawForged := Event{EventID: "failed-attribution-forged", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(31, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, FailureKind: WorkerFailureWorkerError, Detail: "the attempt failed", FailureAttribution: workerFailureLegacyAttribution()})}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{rawForged}}); err == nil {
		t.Fatal("the fold admitted a forged new-legacy marker on a capable terminal")
	}

	// Attribution content on a stored source version below the boundary is
	// smuggled bytes: the live append at the old version refuses...
	smuggled := Event{EventID: "failed-attribution-smuggled", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(32, 0).UTC(), PayloadVersion: workerFailedAttributionVersion - 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, FailureKind: WorkerFailureWorkerError, Detail: "the attempt failed", FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: attributionHostObservation()}})}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{smuggled}}); err == nil {
		t.Fatal("the fold admitted attribution content on a stored v3 worker.failed")
	}
	// ...and the same bytes on a recorded v3 log row refuse on rebuild: the
	// recorded source version pins what the store could have carried.
	if err := s2WorkerFailedLive(t, s, attributionWorkerFailedEvent(workID, attempt, "failed-attribution-rebuild", preferredModelForLane(lane), attributionHostObservation())); err != nil {
		t.Fatalf("the baseline terminal ingest: %v", err)
	}
	corruptLoggedFailurePayload(t, s, workID, "failed-attribution-rebuild", 3, `{"origin":"recorded","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"evt-1","digest":"`+testDigestA+`"}]}}`)
	if err := RebuildFromLog(context.Background(), s); err == nil {
		t.Fatal("rebuild admitted recorded attribution content on a stored v3 worker.failed")
	}
}

// TestWorkerFailureAttributionBornFailedDispatch pins the born-failed
// terminal dispatch: the attribution identity derives from the dispatch
// authorization alone, an ordinary dispatch carries no block (and its
// upcast stays block-free), and abandonment keeps its released no-model
// outcome (A12).
func TestWorkerFailureAttributionBornFailedDispatch(t *testing.T) {
	t.Parallel()
	const workID = "attribution-born-failed"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	job := &WorkerJobBinding{JobID: "job:" + workID}
	attempt := "attempt:" + workID + ":born"
	// Authorize the window without dispatch evidence: the born-failed
	// terminal dispatch IS the attempt's own dispatch evidence.
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	packet := dispatchWorkerPacket(t, s, workID, "repair", attempt)
	packet["schema_version"] = WorkerPacketSchemaVersion
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, attempt, fixture.owner, "attribution-born-authorization", packet); err != nil {
		t.Fatalf("the born-failed authorization refused: %v", err)
	}
	window := attributionDispatchWindow(t, s, workID, attempt)

	observation := &WorkerFailureObservation{
		Observer: WorkerFailureObserverHostAdapter,
		Phase:    WorkerFailurePhaseDispatch,
		Cause:    WorkerFailureCauseIdentityProvenance,
		Basis:    WorkerFailureBasisHypothesis,
		Sources: []WorkerFailureSource{{
			Kind: WorkerFailureSourceHostEvent, Locator: "evt-readback-1", Digest: testDigestC,
			Message: "the executing model did not match the declared identity",
			Host: &WorkerFailureHostObservation{
				Tag: WorkerFailureHostTagTerminal, EventType: "session.error",
				SessionID: "ses-born-failed", MessageID: "msg-born-failed",
				ErrorName: "undeclared_model", ErrorCode: "identity_mismatch",
			},
		}},
	}
	dispatch := Event{EventID: "born-dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(22, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: attempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
		PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: window.PacketDigest,
		ReadbackModel: "provider/executing-model", Terminal: "failed", TerminalFailureKind: WorkerFailureModelIdentity, TerminalDetail: "undeclared executing model",
		WorkerJob: job, FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: observation},
	})}
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(context.Background(), transaction, dispatch, "principal/operator", "client/concord-1")
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return err
	}); err != nil {
		t.Fatalf("the born-failed attributed dispatch refused: %v", err)
	}
	payload := attributionStoredDispatchPayload(t, s, workID, attempt)
	if payload.Terminal != "failed" || payload.FailureAttribution == nil || payload.FailureAttribution.Identity == nil {
		t.Fatalf("born-failed dispatch = terminal %q attribution %+v, want the recorded block with derived identity", payload.Terminal, payload.FailureAttribution)
	}
	identity := payload.FailureAttribution.Identity
	if identity.Kind != WorkerFailureIdentityAttempt || identity.Attempt == nil || identity.Attempt.AttemptID != attempt || identity.Attempt.Epoch != window.AttemptEpoch || identity.Attempt.PacketDigest != window.PacketDigest {
		t.Fatalf("born-failed identity = %+v, want the exact authorized window (epoch %d)", identity, window.AttemptEpoch)
	}
	// The born-failed terminal keeps its durable outcome: the attempt row is
	// failed at birth with the typed kind and no second terminal exists.
	if kind := attemptFailureKind(t, s, workID, attempt); kind != WorkerFailureModelIdentity {
		t.Fatalf("born-failed attempt kind = %q, want %q", kind, WorkerFailureModelIdentity)
	}
	// The reader resolves the born-failed arm: the recorded block beside the
	// dispatched evidence's own source version.
	record, readerErr := WorkerFailureAttributionForAttemptTx(context.Background(), s.DatabaseForTesting(), workID, attempt)
	if readerErr != nil {
		t.Fatalf("read the born-failed terminal attribution: %v", readerErr)
	}
	if record.EventID != "born-dispatch-"+workID || record.SourceVersion != WorkerEvidenceEventPayloadVersion(WorkerDispatched) ||
		record.Attribution == nil || record.Attribution.Identity == nil || record.Attribution.Identity.Kind != WorkerFailureIdentityAttempt {
		t.Fatalf("born-failed reader record = %+v, want the recorded block over the dispatch evidence", record)
	}
}

// TestWorkerFailureAttributionAbandonKeepsReleasedOutcome pins the
// abandonment semantics beside the block: the abandoned attempt keeps the
// dispatched readback model and no extra outcome, an ordinary dispatch carries
// no block, and the upcast stamps the explicit legacy marker on a
// pre-boundary source version.
func TestWorkerFailureAttributionAbandonKeepsReleasedOutcome(t *testing.T) {
	t.Parallel()
	const workID = "attribution-abandon"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + workID + ":abandon"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "attribution-abandon-dispatch", &WorkerJobBinding{JobID: "job:" + workID})
	// The fixture dispatch evidence is a v2-source event: its upcast to the
	// attribution version must stay block-free (an ordinary dispatch).
	normal := attributionStoredDispatchPayload(t, s, workID, attempt)
	if normal.FailureAttribution != nil {
		t.Fatalf("ordinary dispatch upcast carries %+v, want no attribution block", normal.FailureAttribution)
	}
	// A pre-boundary abandonment source carries no block; the upcast chain
	// stamps the explicit legacy marker at the attribution version without
	// inventing content.
	legacy := Event{EventID: "failed-attribution-abandon-legacy", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(33, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, FailureKind: WorkerFailureAbandoned, Detail: "the host closed the attempt"})}
	upcast, err := upcastEvent(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if upcast.PayloadVersion != workerFailedAttributionVersion {
		t.Fatalf("upcast version = %d, want %d", upcast.PayloadVersion, workerFailedAttributionVersion)
	}
	var upcastPayload WorkerFailedPayload
	if err := json.Unmarshal(upcast.Payload, &upcastPayload); err != nil {
		t.Fatal(err)
	}
	if upcastPayload.FailureAttribution == nil || upcastPayload.FailureAttribution.Origin != WorkerFailureAttributionLegacyUnavailable || upcastPayload.FailureAttribution.Observation != nil {
		t.Fatalf("abandoned upcast attribution = %+v, want the explicit legacy marker", upcastPayload.FailureAttribution)
	}
	// The current definition requires the host's recorded abandonment
	// observation live; the abandonment keeps its outcome and model.
	abandoned := Event{EventID: "failed-attribution-abandon", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(33, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, FailureKind: WorkerFailureAbandoned, Detail: "the host closed the attempt", FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: &WorkerFailureObservation{
		Observer: WorkerFailureObserverHostAdapter, Phase: WorkerFailurePhaseHostExecution, Cause: WorkerFailureCauseUnknown, Basis: WorkerFailureBasisUnknown,
		Sources: []WorkerFailureSource{workerFailureAbandonmentSourceFixture()},
	}}})}
	if err := s2WorkerFailedLive(t, s, abandoned); err != nil {
		t.Fatalf("the abandonment ingest refused: %v", err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the abandoned terminal: %v", err)
	}
	if attemptFailureKind(t, s, workID, attempt) != WorkerFailureAbandoned {
		t.Fatal("the abandonment changed its released outcome")
	}
	var model string
	if err := s.DatabaseForTesting().QueryRow(`SELECT readback_model FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attempt).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != preferredModelForLane(lane) {
		t.Fatalf("abandoned attempt model = %q, want the dispatched model preserved", model)
	}
}

// attributionTiesPredicateEntry builds one well-shaped check-kind outcome
// predicate a dispatch packet may declare.
func attributionTiesPredicateEntry(predicateID string, ordinal int) map[string]any {
	checkRef := "check:" + strings.ToLower(strings.TrimPrefix(predicateID, "predicate:"))
	return map[string]any{
		"predicate_id": predicateID, "ordinal": ordinal, "outcome_kind": "check",
		"outcome_payload": map[string]any{"kind": "check", "check_ref": checkRef, "immutable_subject_ref": "commit:ties", "expected_result": "pass"},
	}
}

// dispatchJobBoundAttemptWithPredicates is dispatchJobBoundAttempt with the
// dispatch packet's own outcome predicate list overridden, so a test can offer
// a parent packet predicate the dispatched job revision does not own.
func dispatchJobBoundAttemptWithPredicates(t *testing.T, s *Store, workID, stepID, attemptID string, actor WorkflowActor, key string, job *WorkerJobBinding, predicateIDs []string) {
	t.Helper()
	if job.Digest == "" {
		recordWorkerJobRevisionForTest(t, s, workID, actor, job)
	}
	packet := dispatchWorkerPacket(t, s, workID, stepID, attemptID)
	packet["schema_version"] = WorkerPacketSchemaVersion
	entries := make([]map[string]any, 0, len(predicateIDs))
	for index, id := range predicateIDs {
		entries = append(entries, attributionTiesPredicateEntry(id, index))
	}
	packet["inputs"].(map[string]any)["outcome_predicates"] = entries
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, attemptID, actor, key, packet); err != nil {
		t.Fatalf("dispatch job-bound attempt %s: %v", attemptID, err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attemptID, job)
}

// TestWorkerPredicateTiesResolveFromTheJobRevisionOnAttributionPins pins
// A6: the permitted tie list is the dispatched job revision's own predicate
// list — tying pB under a job that owns only pA refuses by name even though
// the parent packet lists pB.
func TestWorkerPredicateTiesResolveFromTheJobRevisionOnAttributionPins(t *testing.T) {
	t.Parallel()
	const workID = "attribution-job-ties"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	const jobPredicate = "predicate:return-route"
	jobFields := map[string]any{
		"job_id": "job:ties", "objective": "bounded tie job", "stopping_condition": "the recorded checks pass",
		"path_scope": []string{"internal/store"}, "checks": []string{"go test"}, "predicate_ids": []string{jobPredicate},
		"reserved_integration": "integration evidence binds at the parent effect step",
		"ready":                true, "readiness_evidence": []string{"evidence:coordinator-ready"},
	}
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, jobFields); err != nil {
		t.Fatalf("record the tie job: %v", err)
	}
	job := &WorkerJobBinding{JobID: "job:ties", Revision: 1}
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding.JobID == job.JobID {
			*job = view.Binding
		}
	}
	attempt := "attempt:" + workID + ":ties"
	dispatchJobBoundAttemptWithPredicates(t, s, workID, "repair", attempt, fixture.owner, "attribution-ties-dispatch", job, []string{jobPredicate, "predicate:pB-tie"})

	// A report tying pB — a predicate the job revision does not own —
	// refuses and names pB.
	tiedEvidence := laneCoveringEvidence(lane)
	tiedEvidence[0].PredicateIDs = []string{"predicate:pB-tie"}
	completion := withReportedSizeForTest(t, s, jobBoundCompletionEvent(workID, "ties-completed-pb", attempt, lane, time.Unix(16, 0).UTC(), job, workerFixtureDirectory(t, s, workID)), unobservedTreeForTest)
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(completion.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Evidence = tiedEvidence
	completion.Payload = mustJSONValue(payload)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completion}}); err == nil {
		t.Fatal("a report tied a predicate outside the dispatched job revision's own list")
	} else if !strings.Contains(err.Error(), "predicate:pB-tie") {
		t.Fatalf("tie refusal = %v, want the named undeclared predicate", err)
	}
	// The attempt is still dispatched: the refusal left the terminal out.
	if lifecycle := attemptLifecycle(t, s, workID, attempt); lifecycle != "dispatched" {
		t.Fatalf("attempt lifecycle after the refused tie = %q, want dispatched", lifecycle)
	}
	// Tying the job's own predicate succeeds.
	valid := jobBoundCompletionEvent(workID, "ties-completed-pa", attempt, lane, time.Unix(16, 0).UTC(), job, workerFixtureDirectory(t, s, workID))
	if err := json.Unmarshal(valid.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Evidence = laneCoveringEvidence(lane)
	payload.Evidence[0].PredicateIDs = []string{jobPredicate}
	valid.Payload = mustJSONValue(payload)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{valid}}); err != nil {
		t.Fatalf("a report tied to the job's own predicate refused: %v", err)
	}
}

func attemptLifecycle(t *testing.T, s *Store, workID, attemptID string) string {
	t.Helper()
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	return lifecycle
}

// corruptLoggedFailurePayload rewrites one logged worker.failed payload and
// its recorded source version for a rebuild witness: the log is append-only
// in production, so the test drops the update guard for one transaction and
// restores it exactly as the legacy-replay witnesses do. An empty attribution
// omits the member, the shape a pre-attribution source version recorded.
func corruptLoggedFailurePayload(t *testing.T, s *Store, workID, eventID string, version int, attribution string) {
	t.Helper()
	payload := attributionStoredFailurePayload(t, s, workID, eventID)
	members := map[string]any{
		"attempt_id": payload.AttemptID, "readback_model": payload.ReadbackModel,
		"failure_kind": payload.FailureKind, "detail": payload.Detail,
	}
	if attribution != "" {
		members["failure_attribution"] = json.RawMessage(attribution)
	}
	raw, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var trigger string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='domain_events_no_update'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DROP TRIGGER domain_events_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE domain_events SET payload_version=?, payload=? WHERE event_id=?`, version, string(raw), eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// attributionIntegrationObservation builds one coordinator integration
// observation citing the combined run by its lease id with the retained
// result digest, as a supported implementation claim.
func attributionIntegrationObservation(t *testing.T, s *Store, leaseID string) *WorkerFailureObservation {
	t.Helper()
	digest := integrationRetainedResultDigest(t, s, leaseID)
	return &WorkerFailureObservation{
		Observer:  WorkerFailureObserverCoordinator,
		Phase:     WorkerFailurePhaseIntegration,
		Cause:     WorkerFailureCauseImplementation,
		Basis:     WorkerFailureBasisSupported,
		Sources:   []WorkerFailureSource{{Kind: WorkerFailureSourceNativeRun, Locator: leaseID, Digest: digest}},
		Diagnosis: "the combined run failed its compatibility check",
	}
}

// TestWorkerFailureAttributionCoordinatorIntegrationCorrection composes the
// A14 fence end to end: the correction attribution derives from the cited
// failed run alone through the correction boundary's own derivation, the
// fold verifies the recorded block against the same retained lease on the
// live fold and on rebuild, the input population is exactly the acceptances
// at or below the acquire watermark, and a corrupted population refuses the
// rebuild. The action-preflight admission of the observation field lands
// with the generated schema the parent slice owns; this witness drives the
// derivation and the fold through the workflow authority route.
func TestWorkerFailureAttributionCoordinatorIntegrationCorrection(t *testing.T) {
	t.Parallel()
	const workID = "attribution-integration-correction"
	fixture := tSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	acceptor := reviewGateAcceptor(workID)

	jobA := &WorkerJobBinding{JobID: "job:integration-correction-a", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, jobA)
	attemptA := "attempt:" + workID + ":input-a"
	dispatchJobBoundAttempt(t, s, workID, "repair", attemptA, nil, fixture.owner, 0, "integration-correction-a", jobA)
	completeAndAcceptAttempt(t, s, workID, attemptA, lane, acceptor)

	git := integrationWorkflowWorktree(t, s, workID)
	failed := integrationFailedVerifyFor(t, s, git, workID, "project", "lease-attribution-a14")

	jobB := &WorkerJobBinding{JobID: "job:integration-correction-b", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, jobB)
	attemptB := "attempt:" + workID + ":input-b"
	tDispatchJobBoundAttempt(t, s, workID, "repair", attemptB, nil, fixture.owner, "integration-correction-b", jobB)
	// Attempt B completes but stays unaccepted: it is the completed result
	// the rejection completes, recorded after the run started.
	if err := completeJobBoundAttemptForTest(s, workID, attemptB, lane, jobB); err != nil {
		t.Fatalf("complete the post-run attempt: %v", err)
	}

	fields := map[string]json.RawMessage{
		"attempt_id": json.RawMessage(`"` + attemptB + `"`), "attempt_epoch": json.RawMessage(`1`),
		"diagnosis":           json.RawMessage(`"the combined run failed its compatibility check"`),
		"strategy":            json.RawMessage(`"repair the contributing job and rerun the combined verification"`),
		"predicate_ids":       json.RawMessage(`["predicate:return-route"]`),
		"evidence_refs":       json.RawMessage(`["evidence:return-route-verification"]`),
		"failure_attribution": mustJSONValue(attributionIntegrationObservation(t, s, failed.LeaseID)),
	}
	block, err := deriveCoordinatorCorrectionAttributionTx(context.Background(), s.DatabaseForTesting(), workID, "reject_worker_result", fields)
	if err != nil {
		t.Fatalf("the correction attribution derivation refused: %v", err)
	}
	if block.Identity.Kind != WorkerFailureIdentityIntegration || block.Identity.Integration.NativeRun.Locator != failed.OperationRef {
		t.Fatalf("derived block = %+v, want the integration identity over %q", block.Identity, failed.OperationRef)
	}
	if want := integrationRetainedResultDigest(t, s, failed.LeaseID); block.Identity.Integration.NativeRun.Digest != want {
		t.Fatalf("native run digest = %q, want the retained result digest", block.Identity.Integration.NativeRun.Digest)
	}
	inputs := block.Identity.Integration.InputResults
	if len(inputs) != 1 || inputs[0].Job == nil || *inputs[0].Job != *jobA {
		t.Fatalf("input population = %+v, want exactly the pre-watermark acceptance of %s", inputs, jobA.JobID)
	}
	window := attributionDispatchWindow(t, s, workID, attemptB)
	completionValues := map[string]any{
		"step_id": "repair", "action_id": "reject_worker_result", "attempt_epoch": window.AttemptEpoch,
		"worker_attempt_id":        attemptB,
		"correction_diagnosis":     "the combined run failed its compatibility check",
		"correction_strategy":      "repair the contributing job and rerun the combined verification",
		"correction_predicate_ids": []string{"predicate:return-route"},
		"correction_evidence_refs": []string{"evidence:return-route-verification"},
		"failure_attribution":      block,
		"result_evidence_refs":     []string{"evidence:return-route-verification"},
		"changed_refs":             []string{workID}, "actor_ref": mustActorRef(t, fixture.owner),
	}
	event := workflowTypedEvent("attribution-rejection-correction", WorkflowActionCompleted, workID, mustActorRef(t, fixture.owner), time.Unix(40, 0).UTC(), readWorkVersion(t, s, workID), completionValues)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): readWorkVersion(t, s, workID)}}); err != nil {
		t.Fatalf("the rejection completion with the derived block refused: %v", err)
	}
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var completion workflowActionCompletedPayload
	if err := json.Unmarshal([]byte(raw), &completion); err != nil {
		t.Fatal(err)
	}
	if completion.FailureAttributionRef != "" {
		t.Fatalf("the correction completion carries the terminal reference %q; the reference is the derived record's alone", completion.FailureAttributionRef)
	}
	// Replay folds the recorded block against the same retained lease and
	// log: the rebuild reaches the identical attribution.
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the recorded integration attribution: %v", err)
	}
	var rebuilt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&rebuilt); err != nil {
		t.Fatal(err)
	}
	if rebuilt != raw {
		t.Fatal("rebuild changed the recorded integration attribution payload")
	}
	completion.FailureAttribution.Identity.Integration.InputResults[0].Job.Digest = testDigestC
	var corrupted map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &corrupted); err != nil {
		t.Fatal(err)
	}
	corrupted["failure_attribution"] = mustJSONValue(completion.FailureAttribution)
	corruptLoggedWorkflowPayload(t, s, workID, "reject_worker_result", corrupted)
	if err := RebuildFromLog(context.Background(), s); err == nil || !strings.Contains(err.Error(), "integration identity does not equal") {
		t.Fatalf("rebuild did not refuse the corrupted integration population at its owning join: %v", err)
	}
}

// corruptLoggedWorkflowPayload rewrites the newest logged action_completed
// completion of one action for a rebuild witness, restoring the append-only
// guard exactly as it was.
func corruptLoggedWorkflowPayload(t *testing.T, s *Store, workID, actionID string, members map[string]json.RawMessage) {
	t.Helper()
	var eventID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, actionID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	corruptLoggedWorkflowPayloadByEventID(t, s, eventID, members)
}

// TestWorkerFailureAttributionCoordinatorIntegrationUnavailablePopulation
// pins the NULL-watermark lease shape: a run acquired before the watermark
// column records the explicit unavailable population, never an empty
// accepted list, and the fold admits the marker on rebuild.
func TestWorkerFailureAttributionCoordinatorIntegrationUnavailablePopulation(t *testing.T) {
	t.Parallel()
	const workID = "attribution-integration-unavailable"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	job := &WorkerJobBinding{JobID: "job:integration-unavailable", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	attempt := "attempt:" + workID + ":unavailable"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "integration-unavailable-dispatch", job)
	if err := completeJobBoundAttemptForTest(s, workID, attempt, lane, job); err != nil {
		t.Fatalf("complete the reviewed attempt: %v", err)
	}
	git := integrationWorkflowWorktree(t, s, workID)
	failed := integrationFailedVerifyFor(t, s, git, workID, "project", "lease-attribution-unavailable")
	if _, err := s.DatabaseForTesting().Exec(`UPDATE worktree_verify_leases SET event_watermark=NULL WHERE lease_id=?`, failed.LeaseID); err != nil {
		t.Fatal(err)
	}
	fields := map[string]json.RawMessage{
		"attempt_id": json.RawMessage(`"` + attempt + `"`), "attempt_epoch": json.RawMessage(`1`),
		"diagnosis":           json.RawMessage(`"the combined run failed its compatibility check"`),
		"strategy":            json.RawMessage(`"repair the contributing job and rerun the combined verification"`),
		"predicate_ids":       json.RawMessage(`["predicate:return-route"]`),
		"evidence_refs":       json.RawMessage(`["evidence:return-route-verification"]`),
		"failure_attribution": mustJSONValue(attributionIntegrationObservation(t, s, failed.LeaseID)),
	}
	block, err := deriveCoordinatorCorrectionAttributionTx(context.Background(), s.DatabaseForTesting(), workID, "reject_worker_result", fields)
	if err != nil {
		t.Fatalf("the unavailable-population derivation refused: %v", err)
	}
	integration := block.Identity.Integration
	if !integration.InputsUnavailable || len(integration.InputResults) != 0 {
		t.Fatalf("input population = (unavailable=%v results=%+v), want the explicit unavailable marker", integration.InputsUnavailable, integration.InputResults)
	}
	window := attributionDispatchWindow(t, s, workID, attempt)
	completionValues := map[string]any{
		"step_id": "repair", "action_id": "reject_worker_result", "attempt_epoch": window.AttemptEpoch,
		"worker_attempt_id":        attempt,
		"correction_diagnosis":     "the combined run failed its compatibility check",
		"correction_strategy":      "repair the contributing job and rerun the combined verification",
		"correction_predicate_ids": []string{"predicate:return-route"},
		"correction_evidence_refs": []string{"evidence:return-route-verification"},
		"failure_attribution":      block,
		"result_evidence_refs":     []string{"evidence:return-route-verification"},
		"changed_refs":             []string{workID}, "actor_ref": mustActorRef(t, fixture.owner),
	}
	event := workflowTypedEvent("attribution-unavailable-rejection", WorkflowActionCompleted, workID, mustActorRef(t, fixture.owner), time.Unix(40, 0).UTC(), readWorkVersion(t, s, workID), completionValues)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): readWorkVersion(t, s, workID)}}); err != nil {
		t.Fatalf("the rejection completion with the unavailable population refused: %v", err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the unavailable population: %v", err)
	}
}

// TestWorkerFailureAttributionCoordinatorIntegrationCorruptPopulationRefuses
// pins the live fold's integration join: a recorded block whose population
// names a revision the cited run's watermark excludes refuses the fold with
// zero effects — the recorded population is the reader's exact answer.
func TestWorkerFailureAttributionCoordinatorIntegrationCorruptPopulationRefuses(t *testing.T) {
	t.Parallel()
	const workID = "attribution-integration-corrupt"
	fixture := tSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	acceptor := reviewGateAcceptor(workID)
	jobA := &WorkerJobBinding{JobID: "job:integration-corrupt-input", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, jobA)
	attemptA := "attempt:" + workID + ":input"
	dispatchJobBoundAttempt(t, s, workID, "repair", attemptA, nil, fixture.owner, 0, "integration-corrupt-input-dispatch", jobA)
	completeAndAcceptAttempt(t, s, workID, attemptA, lane, acceptor)
	job := &WorkerJobBinding{JobID: "job:integration-corrupt", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	attempt := "attempt:" + workID + ":corrupt"
	tDispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, "integration-corrupt-dispatch", job)
	if err := completeJobBoundAttemptForTest(s, workID, attempt, lane, job); err != nil {
		t.Fatalf("complete the reviewed attempt: %v", err)
	}
	git := integrationWorkflowWorktree(t, s, workID)
	failed := integrationFailedVerifyFor(t, s, git, workID, "project", "lease-attribution-corrupt")
	fields := map[string]json.RawMessage{
		"attempt_id":          json.RawMessage(`"` + attempt + `"`),
		"attempt_epoch":       json.RawMessage(`1`),
		"diagnosis":           json.RawMessage(`"the combined run failed"`),
		"strategy":            json.RawMessage(`"rerun the combined verification"`),
		"predicate_ids":       json.RawMessage(`["predicate:return-route"]`),
		"evidence_refs":       json.RawMessage(`["evidence:return-route-verification"]`),
		"failure_attribution": mustJSONValue(attributionIntegrationObservation(t, s, failed.LeaseID)),
	}
	block, err := deriveCoordinatorCorrectionAttributionTx(context.Background(), s.DatabaseForTesting(), workID, "reject_worker_result", fields)
	if err != nil {
		t.Fatalf("the correction attribution derivation refused: %v", err)
	}
	// Smuggle the post-run revision into the population: the run's
	// watermark excludes it, so the derived answer differs.
	block.Identity.Integration.InputResults = append(block.Identity.Integration.InputResults, WorkerFailureInputResult{
		Job:       job,
		ResultRef: "result:smuggled",
	})
	window := attributionDispatchWindow(t, s, workID, attempt)
	completionValues := map[string]any{
		"step_id": "repair", "action_id": "reject_worker_result", "attempt_epoch": window.AttemptEpoch,
		"worker_attempt_id":        attempt,
		"correction_diagnosis":     "the combined run failed",
		"correction_strategy":      "rerun the combined verification",
		"correction_predicate_ids": []string{"predicate:return-route"},
		"correction_evidence_refs": []string{"evidence:return-route-verification"},
		"failure_attribution":      block,
		"result_evidence_refs":     []string{"evidence:return-route-verification"},
		"changed_refs":             []string{workID}, "actor_ref": mustActorRef(t, fixture.owner),
	}
	event := workflowTypedEvent("attribution-corrupt-rejection", WorkflowActionCompleted, workID, mustActorRef(t, fixture.owner), time.Unix(42, 0).UTC(), readWorkVersion(t, s, workID), completionValues)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): readWorkVersion(t, s, workID)}}); err == nil {
		t.Fatal("the live fold admitted an integration population the cited run's watermark excludes")
	}
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE event_id='attribution-corrupt-rejection'`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Fatal("the refused corruption left its event in the log")
	}
}

// TestWorkerFailureAttributionCoordinatorResultReview pins the result_review
// route: the derivation resolves the exact action attempt's authorized
// window and dispatched job with its completed observed-delivery subject,
// the fold verifies the recorded block live and on rebuild, and a corrupted
// subject reference refuses the rebuild.
func TestWorkerFailureAttributionCoordinatorResultReview(t *testing.T) {
	t.Parallel()
	const workID = "attribution-result-review"
	fixture := tSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	job := &WorkerJobBinding{JobID: "job:" + workID}
	attempt := "attempt:" + workID + ":review"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "attribution-review-dispatch", job)
	window := attributionDispatchWindow(t, s, workID, attempt)

	// Complete through the authenticated terminal ingest with the sealed
	// delivery preparation, so the attempt owns a completed observed
	// delivery event the review can name. The claimed worktree is the
	// fixture's synthetic session entry, so the probe runs through the fake
	// git bound to its recorded path.
	var sessionPath string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' LIMIT 1`, WorktreeSetID(workID)).Scan(&sessionPath); err != nil {
		t.Fatal(err)
	}
	eventID := "completed-" + workID + "-review"
	completion := jobBoundCompletionEvent(workID, eventID, attempt, lane, time.Unix(25, 0).UTC(), job, sessionPath)
	prepared, prepErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attempt, EventID: eventID})
	if prepErr != nil {
		t.Fatalf("the terminal delivery preparation refused: %v", prepErr)
	}
	completion = workerCompletionWithReportedSize(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, completion)
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		_, err := ApplyWorkerEvidenceOperationTx(WithWorkerTerminalPreparation(context.Background(), prepared), transaction, completion)
		return err
	}); err != nil {
		t.Fatalf("the completed ingest refused: %v", err)
	}
	var completedEvent string
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? AND json_extract(payload,'$.observed_delivery') IS NOT NULL`, workID, WorkerCompleted, attempt).Scan(&completedEvent); err != nil {
		t.Fatalf("the completed attempt recorded no observed delivery subject: %v", err)
	}

	var completedPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, completedEvent).Scan(&completedPayload); err != nil {
		t.Fatal(err)
	}
	observation := &WorkerFailureObservation{
		Observer:  WorkerFailureObserverCoordinator,
		Phase:     WorkerFailurePhaseResultReview,
		Cause:     WorkerFailureCauseAssignmentRequirement,
		Basis:     WorkerFailureBasisHypothesis,
		Sources:   []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: completedEvent, Digest: sha256PayloadDigestForTest(completedPayload), Message: "review finding: the delivered subject misses the stopping condition"}},
		Diagnosis: "the report's evidence does not cover the recorded stopping condition",
	}
	fields := map[string]json.RawMessage{
		"attempt_id":          json.RawMessage(`"` + attempt + `"`),
		"attempt_epoch":       json.RawMessage(strconv.FormatInt(window.AttemptEpoch, 10)),
		"diagnosis":           json.RawMessage(`"the report misses the stopping condition"`),
		"strategy":            json.RawMessage(`"re-dispatch with the recorded condition"`),
		"predicate_ids":       json.RawMessage(`["predicate:return-route"]`),
		"evidence_refs":       json.RawMessage(`["evidence:return-route-verification"]`),
		"failure_attribution": mustJSONValue(observation),
	}
	block, err := deriveCoordinatorCorrectionAttributionTx(context.Background(), s.DatabaseForTesting(), workID, "reject_worker_result", fields)
	if err != nil {
		t.Fatalf("the result-review derivation refused: %v", err)
	}
	identity := block.Identity.Attempt
	if identity == nil || identity.AttemptID != attempt || identity.Epoch != window.AttemptEpoch || identity.PacketDigest != window.PacketDigest {
		t.Fatalf("review identity = %+v, want the exact action attempt's window (epoch %d)", identity, window.AttemptEpoch)
	}
	if identity.Subject == nil || identity.Subject.Locator != completedEvent || identity.Subject.Kind != WorkerFailureSubjectObservedDeliveryEvent {
		t.Fatalf("review subject = %+v, want the completed observed delivery event %q", identity.Subject, completedEvent)
	}
	completionValues := map[string]any{
		"step_id": "repair", "action_id": "reject_worker_result", "attempt_epoch": window.AttemptEpoch,
		"worker_attempt_id":        attempt,
		"correction_diagnosis":     "the report misses the stopping condition",
		"correction_strategy":      "re-dispatch with the recorded condition",
		"correction_predicate_ids": []string{"predicate:return-route"},
		"correction_evidence_refs": []string{"evidence:return-route-verification"},
		"failure_attribution":      block,
		"result_evidence_refs":     []string{"evidence:return-route-verification"},
		"changed_refs":             []string{workID}, "actor_ref": mustActorRef(t, fixture.owner),
	}
	event := workflowTypedEvent("attribution-review-rejection", WorkflowActionCompleted, workID, mustActorRef(t, fixture.owner), time.Unix(41, 0).UTC(), readWorkVersion(t, s, workID), completionValues)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): readWorkVersion(t, s, workID)}}); err != nil {
		t.Fatalf("the rejection completion with the review block refused: %v", err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the recorded review attribution: %v", err)
	}
	// A corrupted subject reference — naming an event that never existed —
	// refuses the rebuild.
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		t.Fatal(err)
	}
	var blockMembers map[string]json.RawMessage
	if err := json.Unmarshal(members["failure_attribution"], &blockMembers); err != nil {
		t.Fatal(err)
	}
	var identityMembers map[string]json.RawMessage
	if err := json.Unmarshal(blockMembers["identity"], &identityMembers); err != nil {
		t.Fatal(err)
	}
	var attemptMembers map[string]json.RawMessage
	if err := json.Unmarshal(identityMembers["attempt"], &attemptMembers); err != nil {
		t.Fatal(err)
	}
	attemptMembers["subject"], _ = json.Marshal(map[string]any{"kind": "observed_delivery_event", "locator": "completed-never-recorded", "digest": testDigestE})
	identityMembers["attempt"], _ = json.Marshal(attemptMembers)
	blockMembers["identity"], _ = json.Marshal(identityMembers)
	members["failure_attribution"], _ = json.Marshal(blockMembers)
	corruptLoggedWorkflowPayload(t, s, workID, "reject_worker_result", members)
	if err := RebuildFromLog(context.Background(), s); err == nil {
		t.Fatal("rebuild admitted a review subject reference no completed observed delivery event answers")
	}

	// A recorded block that omits the subject while the attempt's completed
	// observed-delivery event exists is an incomplete identity: the fold
	// refuses the omission instead of folding it silently.
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(members["failure_attribution"], &blockMembers); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(blockMembers["identity"], &identityMembers); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(identityMembers["attempt"], &attemptMembers); err != nil {
		t.Fatal(err)
	}
	delete(attemptMembers, "subject")
	identityMembers["attempt"], _ = json.Marshal(attemptMembers)
	blockMembers["identity"], _ = json.Marshal(identityMembers)
	members["failure_attribution"], _ = json.Marshal(blockMembers)
	corruptLoggedWorkflowPayload(t, s, workID, "reject_worker_result", members)
	if err := RebuildFromLog(context.Background(), s); err == nil {
		t.Fatal("rebuild admitted a result_review identity that omits the subject the attempt's completed observed delivery records")
	}
}

// TestWorkerFailureAttributionCoordinatorBoundaryRefuses pins the
// authenticated boundary rules of the correction derivation: a
// non-coordinator observer, a phase outside the correction pair, an
// authored origin member, a forged core identity member, and a result_review
// on an attemptless correction request each refuse typed.
func TestWorkerFailureAttributionCoordinatorBoundaryRefuses(t *testing.T) {
	t.Parallel()
	const workID = "attribution-boundary"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	git := integrationWorkflowWorktree(t, s, workID)
	failed := integrationFailedVerifyFor(t, s, git, workID, "project", "lease-attribution-boundary")

	requests := []struct {
		name   string
		action string
		extra  any
	}{
		{name: "host observer", action: "reject_worker_result", extra: attributionHostObservation()},
		{name: "coordinator host phase", action: "reject_worker_result", extra: &WorkerFailureObservation{Observer: WorkerFailureObserverCoordinator, Phase: WorkerFailurePhaseHostExecution, Cause: WorkerFailureCauseUnknown, Basis: WorkerFailureBasisUnknown, Sources: []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: "r1", Digest: testDigestC}}}},
		{name: "authored origin member", action: "reject_worker_result", extra: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: attributionIntegrationObservation(t, s, failed.LeaseID)}},
		{name: "authored identity member", action: "reject_worker_result", extra: map[string]any{"observer": "coordinator", "phase": "integration", "cause": "implementation", "basis": "hypothesis", "sources": []map[string]any{{"kind": "native_run", "locator": failed.LeaseID}}, "identity": map[string]any{"kind": "integration"}}},
		{name: "result_review without an action attempt", action: "request_correction", extra: &WorkerFailureObservation{Observer: WorkerFailureObserverCoordinator, Phase: WorkerFailurePhaseResultReview, Cause: WorkerFailureCauseUnknown, Basis: WorkerFailureBasisUnknown, Sources: []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: "result-finding-1", Digest: testDigestC}}}},
	}
	for _, tc := range requests {
		fields := map[string]json.RawMessage{
			"diagnosis":     json.RawMessage(`"the combined run failed"`),
			"strategy":      json.RawMessage(`"rerun the combined verification"`),
			"predicate_ids": json.RawMessage(`["predicate:return-route"]`),
			"evidence_refs": json.RawMessage(`["evidence:return-route-verification"]`),
		}
		if tc.action == "reject_worker_result" {
			fields["attempt_id"] = json.RawMessage(`"attempt:attribution-boundary:1"`)
			fields["attempt_epoch"] = json.RawMessage(`1`)
		}
		if tc.extra != nil {
			fields["failure_attribution"] = mustJSONValue(tc.extra)
		}
		if _, err := deriveCoordinatorCorrectionAttributionTx(context.Background(), s.DatabaseForTesting(), workID, tc.action, fields); err == nil {
			t.Fatalf("the %s attribution request was admitted", tc.name)
		}
	}
}

// TestDerivedWorkerFailureAttributionReference pins the derived pair's
// reference: the derived record names the terminal attribution by the
// failed event's id at every derivation, the reference survives rebuild,
// and a corrupted reference refuses it.
func TestDerivedWorkerFailureAttributionReference(t *testing.T) {
	t.Parallel()
	const workID = "attribution-derived-ref"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "attribution-ref-dispatch", &WorkerJobBinding{JobID: "job:" + workID})
	if err := s2WorkerFailedLive(t, s, attributionWorkerFailedEvent(workID, attempt, "failed-attribution-ref", preferredModelForLane(lane), attributionHostObservation())); err != nil {
		t.Fatalf("the attributed terminal ingest: %v", err)
	}
	assertDerivedReference(t, s, workID, attempt, "failed-attribution-ref")
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the derived reference: %v", err)
	}
	assertDerivedReference(t, s, workID, attempt, "failed-attribution-ref")

	// A corrupted reference — naming another terminal event — refuses the
	// rebuild.
	var eventID, raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id,payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attempt).Scan(&eventID, &raw); err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		t.Fatal(err)
	}
	members["failure_attribution_ref"] = json.RawMessage(`"failed-attribution-somebody-else"`)
	corruptLoggedWorkflowPayloadByEventID(t, s, eventID, members)
	if err := RebuildFromLog(context.Background(), s); err == nil {
		t.Fatal("rebuild admitted a derived reference that names another terminal event")
	}
}

func assertDerivedReference(t *testing.T, s *Store, workID, attempt, terminalEventID string) {
	t.Helper()
	var ref string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.failure_attribution_ref') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attempt).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if ref != terminalEventID {
		t.Fatalf("derived attribution reference = %q, want the terminal event id %q", ref, terminalEventID)
	}
}

func corruptLoggedWorkflowPayloadByEventID(t *testing.T, s *Store, eventID string, members map[string]json.RawMessage) {
	t.Helper()
	encoded, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var trigger string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='domain_events_no_update'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DROP TRIGGER domain_events_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE domain_events SET payload=? WHERE event_id=?`, string(encoded), eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkerFailureAttributionReaderResolvesSourceVersion pins the
// event-based attribution reader: the attributed terminal resolves its
// recorded block beside the source payload version the log recorded, and
// the legacy terminal resolves the explicit absence with its own source
// version.
func TestWorkerFailureAttributionReaderResolvesSourceVersion(t *testing.T) {
	t.Parallel()
	const workID = "attribution-reader"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	attributed := "attempt:" + workID + ":attributed"
	dispatchJobBoundAttempt(t, s, workID, "repair", attributed, nil, fixture.owner, 0, "attribution-reader-dispatch-1", &WorkerJobBinding{JobID: "job:" + workID})
	if err := s2WorkerFailedLive(t, s, attributionWorkerFailedEvent(workID, attributed, "failed-attribution-reader-1", preferredModelForLane(lane), attributionHostObservation())); err != nil {
		t.Fatalf("the attributed terminal ingest: %v", err)
	}
	record, err := WorkerFailureAttributionForAttemptTx(context.Background(), s.DatabaseForTesting(), workID, attributed)
	if err != nil {
		t.Fatalf("read the attributed terminal attribution: %v", err)
	}
	if record.SourceVersion != workerFailedAttributionVersion || record.Attribution == nil || record.Attribution.Origin != WorkerFailureAttributionRecorded || record.Attribution.Identity == nil {
		t.Fatalf("attributed record = %+v, want the recorded block at source version %d", record, workerFailedAttributionVersion)
	}
	legacy := "attempt:" + workID + ":legacy"
	correction, correctionErr := workflowCorrectionContext(context.Background(), s.DatabaseForTesting(), workID, "repair")
	if correctionErr != nil {
		t.Fatal(correctionErr)
	}
	dispatchJobBoundAttempt(t, s, workID, "repair", legacy, correction, fixture.owner, 0, "attribution-reader-dispatch-2", &WorkerJobBinding{JobID: "job:" + workID})
	if err := s2WorkerFailedLive(t, s, attributionWorkerFailedEvent(workID, legacy, "failed-attribution-reader-2", preferredModelForLane(lane), attributionHostObservation())); err != nil {
		t.Fatalf("the second terminal ingest: %v", err)
	}
	// The current definition requires the recorded block live, so the
	// retained pre-attribution row is the logged source version 1 bytes.
	corruptLoggedFailurePayload(t, s, workID, "failed-attribution-reader-2", 1, "")
	legacyRecord, err := WorkerFailureAttributionForAttemptTx(context.Background(), s.DatabaseForTesting(), workID, legacy)
	if err != nil {
		t.Fatalf("read the legacy terminal attribution: %v", err)
	}
	// A below-boundary source without raw bytes resolves the explicit
	// legacy marker, so a history that predates the block reads as visibly
	// legacy beside its recorded source version.
	if legacyRecord.Attribution == nil || legacyRecord.Attribution.Origin != WorkerFailureAttributionLegacyUnavailable || legacyRecord.SourceVersion != 1 {
		t.Fatalf("legacy record = %+v, want the explicit legacy marker at source version 1", legacyRecord)
	}
	if _, err := WorkerFailureAttributionForAttemptTx(context.Background(), s.DatabaseForTesting(), workID, "attempt:absent"); err == nil {
		t.Fatal("the reader resolved attribution for an attempt with no terminal event")
	}
}

// TestWorkerFailureAttributionResultReviewSourceJoins pins the result_review
// source joins: every result source resolves this work's own retained
// evidence — a completed event of the reviewed attempt under its payload
// digest, or an existing ranked finding whose minting completion belongs to
// that attempt — a native_run source is the integration phase's anchor and
// never a review citation, and a source recorded after the correction is a
// future citation the replay's sequence bound refuses.
func TestWorkerFailureAttributionResultReviewSourceJoins(t *testing.T) {
	t.Parallel()
	const workID = "attribution-review-sources"
	fixture := tSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	job := &WorkerJobBinding{JobID: "job:" + workID}

	// The reviewed attempt completes last among its peers at derive time and
	// stays the step's latest authorized attempt, so the rejection's epoch
	// fence passes; the earlier attempt's completion is the wrong-attempt
	// citation.
	earlier := "attempt:" + workID + ":earlier"
	dispatchJobBoundAttempt(t, s, workID, "repair", earlier, nil, fixture.owner, 0, "attribution-sources-dispatch-earlier", job)
	earlierCompleted := "completed-" + workID + "-earlier"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{withReportedSizeForTest(t, s, jobBoundCompletionEvent(workID, earlierCompleted, earlier, lane, time.Unix(24, 0).UTC(), job, workerFixtureDirectory(t, s, workID)), unobservedTreeForTest)}}); err != nil {
		t.Fatalf("the earlier completion refused: %v", err)
	}
	var earlierPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, earlierCompleted).Scan(&earlierPayload); err != nil {
		t.Fatal(err)
	}

	attempt := "attempt:" + workID + ":review"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "attribution-sources-dispatch", job)
	window := attributionDispatchWindow(t, s, workID, attempt)
	var sessionPath string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' LIMIT 1`, WorktreeSetID(workID)).Scan(&sessionPath); err != nil {
		t.Fatal(err)
	}
	completedEvent := "completed-" + workID + "-review"
	completion := jobBoundCompletionEvent(workID, completedEvent, attempt, lane, time.Unix(25, 0).UTC(), job, workerFixtureDirectory(t, s, workID))
	completion = workerCompletionAtClaim(t, s, completion)
	prepared, prepErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attempt, EventID: completedEvent})
	if prepErr != nil {
		t.Fatalf("the terminal delivery preparation refused: %v", prepErr)
	}
	completion = workerCompletionWithReportedSize(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, completion)
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		_, err := ApplyWorkerEvidenceOperationTx(WithWorkerTerminalPreparation(context.Background(), prepared), transaction, completion)
		return err
	}); err != nil {
		t.Fatalf("the completed ingest refused: %v", err)
	}
	var completedPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, completedEvent).Scan(&completedPayload); err != nil {
		t.Fatal(err)
	}

	review := func(citedAttempt string, sources []WorkerFailureSource) error {
		observation := &WorkerFailureObservation{
			Observer: WorkerFailureObserverCoordinator,
			Phase:    WorkerFailurePhaseResultReview,
			Cause:    WorkerFailureCauseAssignmentRequirement,
			Basis:    WorkerFailureBasisHypothesis,
			Sources:  sources,
		}
		fields := map[string]json.RawMessage{
			"attempt_id":          json.RawMessage(`"` + citedAttempt + `"`),
			"attempt_epoch":       json.RawMessage(strconv.FormatInt(window.AttemptEpoch, 10)),
			"diagnosis":           json.RawMessage(`"the report misses the stopping condition"`),
			"strategy":            json.RawMessage(`"re-dispatch with the recorded condition"`),
			"predicate_ids":       json.RawMessage(`["predicate:return-route"]`),
			"evidence_refs":       json.RawMessage(`["evidence:return-route-verification"]`),
			"failure_attribution": mustJSONValue(observation),
		}
		_, err := deriveCoordinatorCorrectionAttributionTx(context.Background(), s.DatabaseForTesting(), workID, "reject_worker_result", fields)
		return err
	}
	cases := []struct {
		name    string
		sources []WorkerFailureSource
		want    string
	}{
		{"bogus locator", []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: "result:no-such-event", Digest: testDigestD}}, "result:no-such-event"},
		{"bogus digest", []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: completedEvent, Digest: testDigestD}}, "differs from the retained payload"},
		{"other attempt's completion", []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: earlierCompleted, Digest: sha256PayloadDigestForTest(earlierPayload)}}, "is the completion of attempt " + earlier},
		{"native run anchor", []WorkerFailureSource{{Kind: WorkerFailureSourceNativeRun, Locator: "lease-integration-native", Digest: testDigestH}}, "native_run"},
	}
	for _, tc := range cases {
		if err := review(attempt, tc.sources); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v, want a refusal naming %q", tc.name, err, tc.want)
		}
	}
	// The real citation derives: the reviewed attempt's own completion under
	// its payload digest.
	valid := []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: completedEvent, Digest: sha256PayloadDigestForTest(completedPayload), Message: "review finding: the delivered subject misses the stopping condition"}}
	block, deriveErr := func() (*WorkerFailureAttribution, error) {
		observation := &WorkerFailureObservation{Observer: WorkerFailureObserverCoordinator, Phase: WorkerFailurePhaseResultReview, Cause: WorkerFailureCauseAssignmentRequirement, Basis: WorkerFailureBasisHypothesis, Sources: valid}
		fields := map[string]json.RawMessage{
			"attempt_id": json.RawMessage(`"` + attempt + `"`), "attempt_epoch": json.RawMessage(strconv.FormatInt(window.AttemptEpoch, 10)),
			"diagnosis": json.RawMessage(`"the report misses the stopping condition"`), "strategy": json.RawMessage(`"re-dispatch with the recorded condition"`),
			"predicate_ids": json.RawMessage(`["predicate:return-route"]`), "evidence_refs": json.RawMessage(`["evidence:return-route-verification"]`),
			"failure_attribution": mustJSONValue(observation),
		}
		return deriveCoordinatorCorrectionAttributionTx(context.Background(), s.DatabaseForTesting(), workID, "reject_worker_result", fields)
	}()
	if deriveErr != nil {
		t.Fatalf("the real result citation refused: %v", deriveErr)
	}
	rejectionValues := map[string]any{
		"step_id": "repair", "action_id": "reject_worker_result", "attempt_epoch": window.AttemptEpoch,
		"worker_attempt_id":        attempt,
		"correction_diagnosis":     "the report misses the stopping condition",
		"correction_strategy":      "re-dispatch with the recorded condition",
		"correction_predicate_ids": []string{"predicate:return-route"},
		"correction_evidence_refs": []string{"evidence:return-route-verification"},
		"failure_attribution":      block,
		"result_evidence_refs":     []string{"evidence:return-route-verification"},
		"changed_refs":             []string{workID}, "actor_ref": mustActorRef(t, fixture.owner),
	}
	rejection := workflowTypedEvent("attribution-source-joins-rejection", WorkflowActionCompleted, workID, mustActorRef(t, fixture.owner), time.Unix(41, 0).UTC(), readWorkVersion(t, s, workID), rejectionValues)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{rejection}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): readWorkVersion(t, s, workID)}}); err != nil {
		t.Fatalf("the rejection with the real citation refused: %v", err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the real citation: %v", err)
	}
	// A completion recorded after the rejection is a future citation: corrupt
	// the stored correction's source to the later attempt's completion and
	// the replay's sequence bound refuses it.
	later := "attempt:" + workID + ":later"
	correction, correctionErr := workflowCorrectionContext(context.Background(), s.DatabaseForTesting(), workID, "repair")
	if correctionErr != nil {
		t.Fatal(correctionErr)
	}
	tDispatchJobBoundAttempt(t, s, workID, "repair", later, correction, fixture.owner, "attribution-sources-dispatch-later", job)
	laterCompleted := "completed-" + workID + "-later"
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{withReportedSizeForTest(t, s, jobBoundCompletionEvent(workID, laterCompleted, later, lane, time.Unix(45, 0).UTC(), job, workerFixtureDirectory(t, s, workID)), unobservedTreeForTest)}}); err != nil {
		t.Fatalf("the later completion refused: %v", err)
	}
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		t.Fatal(err)
	}
	var blockMembers map[string]json.RawMessage
	if err := json.Unmarshal(members["failure_attribution"], &blockMembers); err != nil {
		t.Fatal(err)
	}
	var observationMembers map[string]json.RawMessage
	if err := json.Unmarshal(blockMembers["observation"], &observationMembers); err != nil {
		t.Fatal(err)
	}
	observationMembers["sources"], _ = json.Marshal([]map[string]any{{"kind": "result", "locator": laterCompleted, "digest": testDigestD}})
	blockMembers["observation"], _ = json.Marshal(observationMembers)
	members["failure_attribution"], _ = json.Marshal(blockMembers)
	corruptLoggedWorkflowPayload(t, s, workID, "reject_worker_result", members)
	if err := RebuildFromLog(context.Background(), s); err == nil {
		t.Fatal("rebuild admitted a result source recorded after the correction it cites")
	}
}

func sha256PayloadDigestForTest(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestWorkerFailureAttributionObserverFailureKindCoupling pins the terminal
// observer couplings: a worker report describes only its own worker_error
// execution, and a born-failed terminal is the host adapter's observation.
func TestWorkerFailureAttributionObserverFailureKindCoupling(t *testing.T) {
	t.Parallel()
	const workID = "attribution-observer-coupling"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + workID + ":coupling"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "attribution-coupling-dispatch", &WorkerJobBinding{JobID: "job:" + workID})

	// A worker_report observation cannot ride a diagnostic failure kind: the
	// worker describes only its own execution.
	workerObservation := &WorkerFailureObservation{
		Observer: WorkerFailureObserverWorkerReport,
		Phase:    WorkerFailurePhaseWorkerExecution,
		Cause:    WorkerFailureCauseUnknown,
		Basis:    WorkerFailureBasisUnknown,
		Sources:  []WorkerFailureSource{{Kind: WorkerFailureSourceAdmittedReport, Locator: "report-coupling", Digest: testDigestB}},
	}
	payload := WorkerFailedPayload{AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureInvalidReport, Detail: "the report never validly shaped", FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: workerObservation}}
	event := Event{EventID: "failed-coupling-invalid-report", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(payload)}
	if err := s2WorkerFailedLive(t, s, event); err == nil || !strings.Contains(err.Error(), "worker report") {
		t.Fatalf("worker_report on invalid_report err=%v, want the worker report coupling refusal", err)
	}

	// A born-failed terminal dispatch is the host adapter's record; the
	// worker cannot describe the readback terminal it never reported.
	window := attributionDispatchWindow(t, s, workID, attempt)
	bornObservation := &WorkerFailureObservation{
		Observer: WorkerFailureObserverWorkerReport,
		Phase:    WorkerFailurePhaseWorkerExecution,
		Cause:    WorkerFailureCauseUnknown,
		Basis:    WorkerFailureBasisUnknown,
		Sources:  []WorkerFailureSource{{Kind: WorkerFailureSourceAdmittedReport, Locator: "report-born", Digest: testDigestB}},
	}
	bornAttempt := "attempt:" + workID + ":born"
	job := &WorkerJobBinding{JobID: "job:" + workID}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	packet := dispatchWorkerPacket(t, s, workID, "repair", bornAttempt)
	packet["schema_version"] = WorkerPacketSchemaVersion
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, bornAttempt, fixture.owner, "attribution-coupling-born-authorization", packet); err != nil {
		t.Fatalf("the born authorization refused: %v", err)
	}
	bornWindow := attributionDispatchWindow(t, s, workID, bornAttempt)
	bornDispatch := Event{EventID: "born-coupling-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(31, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: bornAttempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
		PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: bornWindow.PacketDigest,
		ReadbackModel: preferredModelForLane(lane), Terminal: "failed", TerminalFailureKind: WorkerFailureModelIdentity, TerminalDetail: "undeclared executing model",
		WorkerJob: job, FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: bornObservation},
	})}
	err := s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, prepErr := PrepareLaneActorDispatch(context.Background(), transaction, bornDispatch, "principal/operator", "client/concord-1")
		if prepErr != nil {
			return prepErr
		}
		_, appendErr := AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return appendErr
	})
	if err == nil || !strings.Contains(err.Error(), "host adapter") {
		t.Fatalf("worker_report on a born-failed terminal err=%v, want the host adapter coupling refusal", err)
	}
	_ = window
}

// TestWorkerFailureAttributionAbandonmentTagCoupling pins the abandoned-
// terminal coupling of the abandonment tag on both terminal folds: the
// request the host observed belongs to the abandoned variant alone, and the
// abandoned terminal admits it live and on rebuild.
func TestWorkerFailureAttributionAbandonmentTagCoupling(t *testing.T) {
	t.Parallel()
	const workID = "attribution-abandonment-coupling"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	abandonmentObservation := func() *WorkerFailureObservation {
		return &WorkerFailureObservation{
			Observer: WorkerFailureObserverHostAdapter,
			Phase:    WorkerFailurePhaseHostExecution,
			Cause:    WorkerFailureCauseUnknown,
			Basis:    WorkerFailureBasisUnknown,
			Sources:  []WorkerFailureSource{workerFailureAbandonmentSourceFixture()},
		}
	}
	// A worker_error terminal observed no abandonment request: the worker
	// reported a failure, the coordinator did not close the attempt.
	attempt := "attempt:" + workID + ":worker-error"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "attribution-abandonment-dispatch", &WorkerJobBinding{JobID: "job:" + workID})
	workerError := Event{EventID: "failed-abandonment-worker-error", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureWorkerError, Detail: "the report failed validation", FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: abandonmentObservation()}})}
	if err := s2WorkerFailedLive(t, s, workerError); err == nil || !strings.Contains(err.Error(), "abandonment observation belongs to the abandoned terminal") {
		t.Fatalf("abandonment observation on a worker_error terminal err=%v, want the abandoned-terminal coupling refusal", err)
	}
	// A born-failed dispatch terminal names its own observed fault; the
	// abandonment tag is not it.
	bornAttempt := "attempt:" + workID + ":born"
	job := &WorkerJobBinding{JobID: "job:" + workID}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	packet := dispatchWorkerPacket(t, s, workID, "repair", bornAttempt)
	packet["schema_version"] = WorkerPacketSchemaVersion
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, bornAttempt, fixture.owner, "attribution-abandonment-born-authorization", packet); err != nil {
		t.Fatalf("the born authorization refused: %v", err)
	}
	bornWindow := attributionDispatchWindow(t, s, workID, bornAttempt)
	bornDispatch := Event{EventID: "born-abandonment-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(31, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: bornAttempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
		PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: bornWindow.PacketDigest,
		ReadbackModel: preferredModelForLane(lane), Terminal: "failed", TerminalFailureKind: WorkerFailureModelIdentity, TerminalDetail: "undeclared executing model",
		WorkerJob: job, FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: abandonmentObservation()},
	})}
	bornErr := s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, prepErr := PrepareLaneActorDispatch(context.Background(), transaction, bornDispatch, "principal/operator", "client/concord-1")
		if prepErr != nil {
			return prepErr
		}
		_, appendErr := AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return appendErr
	})
	if bornErr == nil || !strings.Contains(bornErr.Error(), "abandonment observation belongs to the abandoned terminal") {
		t.Fatalf("abandonment observation on a born-failed terminal err=%v, want the abandoned-terminal coupling refusal", bornErr)
	}
	// The refused born-failed evidence leaves that authorization's launch
	// outcome unknown, so it is reconciled before any other launch (CD-0242
	// D6): its abandoned terminal admits the abandonment observation live,
	// and the rebuild folds the same bytes to the same admission.
	abandonedAttempt := bornAttempt
	abandoned := Event{EventID: "failed-abandonment-admitted", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(32, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(WorkerFailedPayload{AttemptID: abandonedAttempt, FailureKind: WorkerFailureAbandoned, Detail: "the host closed the attempt", FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: abandonmentObservation()}})}
	if err := s2WorkerFailedLive(t, s, abandoned); err != nil {
		t.Fatalf("the abandoned terminal refused its abandonment observation: %v", err)
	}
	if attemptFailureKind(t, s, workID, abandonedAttempt) != WorkerFailureAbandoned {
		t.Fatal("the abandoned terminal did not record the abandoned kind")
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with the admitted abandonment observation: %v", err)
	}
}

// TestWorkerFailureAttributionAuthoredMarkerSmuggles pins the upcaster's
// authored-member gate under a released pin, where no required-block refusal
// could stand in for it: a stored source version below the boundary that
// already carries the legacy marker — or the key at all, null included — is
// authored bytes no store ever recorded, and the upcaster refuses it instead
// of stamping around it.
func TestWorkerFailureAttributionAuthoredMarkerSmuggles(t *testing.T) {
	t.Parallel()
	const workID = "attribution-authored-marker"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + workID + ":marker"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "attribution-marker-dispatch", &WorkerJobBinding{JobID: "job:" + workID})
	for name, marker := range map[string]string{
		"legacy marker": `{"origin":"legacy_unavailable"}`,
		"null member":   `null`,
	} {
		smuggled := Event{EventID: "failed-authored-marker-" + strconv.FormatInt(int64(len(name)), 10), Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(33, 0).UTC(), PayloadVersion: workerFailedAttributionVersion - 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureWorkerError, Detail: "the attempt failed", FailureAttribution: func() *WorkerFailureAttribution {
			if marker == "null" {
				return nil
			}
			return &WorkerFailureAttribution{Origin: WorkerFailureAttributionLegacyUnavailable}
		}()})}
		// The null member needs raw bytes, not the omitempty struct.
		if marker == "null" {
			smuggled.Payload = []byte(`{"attempt_id":` + string(mustJSONValue(attempt)) + `,"readback_model":` + string(mustJSONValue(preferredModelForLane(lane))) + `,"failure_kind":"worker_error","detail":"the attempt failed","failure_attribution":null}`)
		}
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{smuggled}}); err == nil {
			t.Fatalf("the %s authored marker was admitted at v3", name)
		}
	}
}
