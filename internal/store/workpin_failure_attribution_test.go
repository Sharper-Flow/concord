package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The work pin's attempt read exposes the attempt's terminal failure
// attribution through the same transaction and the one event-based
// attribution reader: a recorded block on a failed terminal, the
// born-failed dispatch evidence, and no member on an attempt that never
// failed.

func TestWorkPinAttemptExposesFailureAttribution(t *testing.T) {
	t.Parallel()
	const workID = "workpin-attribution"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	job := &WorkerJobBinding{JobID: "job:" + workID}
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "workpin-attribution-dispatch", job)
	window := attributionDispatchWindow(t, s, workID, attempt)
	if err := s2WorkerFailedLive(t, s, attributionWorkerFailedEvent(workID, attempt, "failed-workpin-attribution", preferredModelForLane(lane), attributionHostObservation())); err != nil {
		t.Fatalf("the attributed terminal ingest: %v", err)
	}
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Attempt == nil || pin.Attempt.State != "failed" {
		t.Fatalf("pin attempt=%+v, want the failed terminal attempt", pin.Attempt)
	}
	failure := workPinAttemptWire(t, pin)["failure_attribution"]
	if failure == nil {
		t.Fatal("the failed attempt's pin carries no failure_attribution member")
	}
	record := failure.(map[string]any)
	if record["event_id"] != "failed-workpin-attribution" {
		t.Fatalf("failure_attribution event_id=%v, want the terminal event", record["event_id"])
	}
	if v, ok := record["source_version"].(float64); !ok || int(v) != workerFailedAttributionVersion {
		t.Fatalf("failure_attribution source_version=%v, want %d", record["source_version"], workerFailedAttributionVersion)
	}
	block, ok := record["attribution"].(map[string]any)
	if !ok || block["origin"] != WorkerFailureAttributionRecorded {
		t.Fatalf("failure_attribution=%v, want the recorded origin", record["attribution"])
	}
	observation, ok := block["observation"].(map[string]any)
	if !ok || observation["observer"] != WorkerFailureObserverHostAdapter || observation["phase"] != WorkerFailurePhaseHostExecution {
		t.Fatalf("failure_attribution observation=%v, want the host adapter's typed view", block["observation"])
	}
	identity, ok := block["identity"].(map[string]any)
	if !ok || identity["kind"] != WorkerFailureIdentityAttempt {
		t.Fatalf("failure_attribution identity=%v, want the attempt variant", block["identity"])
	}
	attemptIdentity, ok := identity["attempt"].(map[string]any)
	if !ok || attemptIdentity["attempt_id"] != attempt {
		t.Fatalf("attempt identity=%v, want attempt %s", identity["attempt"], attempt)
	}
	if attemptIdentity["epoch"].(float64) != float64(window.AttemptEpoch) || attemptIdentity["packet_digest"] != window.PacketDigest {
		t.Fatalf("attempt identity=%v, want the authorized window (epoch %d digest %s)", attemptIdentity, window.AttemptEpoch, window.PacketDigest)
	}
	binding, ok := attemptIdentity["job"].(map[string]any)
	if !ok || binding["job_id"] != job.JobID {
		t.Fatalf("attempt identity job=%v, want the dispatched revision %s", attemptIdentity["job"], job.JobID)
	}
	if _, fabricated := attemptIdentity["no_job"]; fabricated {
		t.Fatal("a job-bound attempt identity carries the no_job marker")
	}
	// The typed field carries the same record the wire serializes.
	if pin.Attempt.FailureAttribution == nil || pin.Attempt.FailureAttribution.EventID != "failed-workpin-attribution" || pin.Attempt.FailureAttribution.SourceVersion != workerFailedAttributionVersion ||
		pin.Attempt.FailureAttribution.Attribution == nil || pin.Attempt.FailureAttribution.Attribution.Origin != WorkerFailureAttributionRecorded ||
		pin.Attempt.FailureAttribution.Attribution.Identity == nil || pin.Attempt.FailureAttribution.Attribution.Identity.Attempt == nil {
		t.Fatalf("typed attempt attribution=%+v, want the recorded terminal record", pin.Attempt.FailureAttribution)
	}
}

func TestWorkPinAttemptOmitsFailureAttributionOnLiveAttempt(t *testing.T) {
	t.Parallel()
	const workID = "workpin-attribution-live"
	s, _, _, attemptID := seedWorkerAtExecution(t, workID)
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Attempt == nil || pin.Attempt.ID != attemptID || pin.Attempt.State != "dispatched" {
		t.Fatalf("pin attempt=%+v, want the live dispatched attempt", pin.Attempt)
	}
	if _, present := workPinAttemptWire(t, pin)["failure_attribution"]; present {
		t.Fatal("a live dispatched attempt carries no failure_attribution member")
	}
	if pin.Attempt.FailureAttribution != nil {
		t.Fatalf("typed live attempt attribution=%+v, want nil", pin.Attempt.FailureAttribution)
	}
}

func TestWorkPinAttemptExposesBornFailedAttribution(t *testing.T) {
	t.Parallel()
	const workID = "workpin-attribution-born"
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
	if err := dispatchJobPacketForTest(t, s, workID, attempt, fixture.owner, "workpin-attribution-born-authorization", packet); err != nil {
		t.Fatalf("the born-failed authorization refused: %v", err)
	}
	window := attributionDispatchWindow(t, s, workID, attempt)
	observation := &WorkerFailureObservation{
		Observer: WorkerFailureObserverHostAdapter,
		Phase:    WorkerFailurePhaseDispatch,
		Cause:    WorkerFailureCauseIdentityProvenance,
		Basis:    WorkerFailureBasisHypothesis,
		Sources: []WorkerFailureSource{{
			Kind: WorkerFailureSourceHostEvent, Locator: "evt-workpin-born-1", Digest: testDigestC,
			Message: "the executing model did not match the declared identity",
			Host: &WorkerFailureHostObservation{
				Tag: WorkerFailureHostTagTerminal, EventType: "session.error",
				SessionID: "ses-workpin-born", MessageID: "msg-workpin-born",
				ErrorName: "undeclared_model", ErrorCode: "identity_mismatch",
			},
		}},
	}
	dispatch := Event{EventID: "born-workpin-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(22, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: attempt, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
		PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: window.PacketDigest,
		ReadbackModel: "provider/executing-model", Terminal: "failed", TerminalFailureKind: WorkerFailureModelIdentity, TerminalDetail: "undeclared executing model",
		WorkerJob: job, FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: observation},
	})}
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, prepErr := PrepareLaneActorDispatch(context.Background(), transaction, dispatch, "principal/operator", "client/concord-1")
		if prepErr != nil {
			return prepErr
		}
		_, appendErr := AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return appendErr
	}); err != nil {
		t.Fatalf("the born-failed attributed dispatch refused: %v", err)
	}
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Attempt == nil || pin.Attempt.State != "failed" {
		t.Fatalf("pin attempt=%+v, want the born-failed attempt", pin.Attempt)
	}
	failure, ok := workPinAttemptWire(t, pin)["failure_attribution"].(map[string]any)
	if !ok {
		t.Fatal("the born-failed attempt's pin carries no failure_attribution member")
	}
	if failure["event_id"] != "born-workpin-"+workID {
		t.Fatalf("failure_attribution event_id=%v, want the born-failed dispatch evidence", failure["event_id"])
	}
	if version, ok := failure["source_version"].(float64); !ok || int(version) != CurrentEventPayloadVersion(WorkerDispatched) {
		t.Fatalf("failure_attribution source_version=%v, want %d", failure["source_version"], CurrentEventPayloadVersion(WorkerDispatched))
	}
	block, ok := failure["attribution"].(map[string]any)
	if !ok || block["origin"] != WorkerFailureAttributionRecorded {
		t.Fatalf("failure_attribution=%v, want the recorded origin", failure["attribution"])
	}
	identity, ok := block["identity"].(map[string]any)
	if !ok || identity["kind"] != WorkerFailureIdentityAttempt {
		t.Fatalf("failure_attribution identity=%v, want the attempt variant", block["identity"])
	}
	attemptIdentity, ok := identity["attempt"].(map[string]any)
	if !ok || attemptIdentity["attempt_id"] != attempt || attemptIdentity["epoch"].(float64) != float64(window.AttemptEpoch) || attemptIdentity["packet_digest"] != window.PacketDigest {
		t.Fatalf("attempt identity=%v, want the born-failed authorized window (epoch %d)", identity["attempt"], window.AttemptEpoch)
	}
	if pin.Attempt.FailureAttribution == nil || pin.Attempt.FailureAttribution.Attribution == nil ||
		pin.Attempt.FailureAttribution.Attribution.Identity == nil || pin.Attempt.FailureAttribution.Attribution.Identity.Attempt == nil {
		t.Fatalf("typed born-failed attribution=%+v, want the recorded attempt identity", pin.Attempt.FailureAttribution)
	}
}

// workPinAttemptWire serializes the pin the way the agent surface does and
// returns the attempt member's wire shape.
func workPinAttemptWire(t *testing.T, pin WorkPin) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Attempt *map[string]any `json:"attempt"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Attempt == nil {
		t.Fatal("the pin wire carries no attempt member")
	}
	return *wire.Attempt
}
