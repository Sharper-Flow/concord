package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The historical progress derivation resolves every recorded definition
// selection through the caller's authoritative registry, never through a
// builtin retry or a silent legacy fallback. A custom-registered definition
// declaring the obligation-satisfaction policy qualifies coordinator
// satisfactions on the pin and in the admission loader identically, and a
// selection pin that no longer verifies refuses instead of downgrading the
// work to worker-acceptance behavior.

const progressCustomRef = "workflow.progress_custom"

// progressCustomDefinition copies the research obligation-satisfaction head
// under a fresh reference, so the builtin registry genuinely cannot resolve
// the recorded selection and the caller registry is the only authority. The
// research kind is not Product-changing, so the journey's engineering
// contract approval carries no law boundary.
func progressCustomDefinition(t *testing.T, withPolicy bool) WorkflowDefinition {
	t.Helper()
	version := builtinWorkflowCurrentVersions["workflow.research"]
	registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.research", version)
	if !ok {
		t.Fatalf("workflow.research v%d is not registered", version)
	}
	definition := registered.Definition
	definition.Ref = progressCustomRef
	definition.Version = 1
	if !withPolicy {
		definition.ProgressPolicy = ""
		for index := range definition.ActionDefinitions {
			definition.ActionDefinitions[index].SatisfactionKind = ""
		}
	}
	return definition
}

// progressCustomRegistry registers the custom definition in a fresh
// caller-owned registry.
func progressCustomRegistry(t *testing.T, definition WorkflowDefinition) (DefinitionRegistry, RegisteredDefinition) {
	t.Helper()
	registry := NewWorkflowDefinitionRegistry()
	registered, err := registry.Register(definition)
	if err != nil {
		t.Fatalf("the custom progress definition refused registration: %v", err)
	}
	return registry, registered
}

// progressRegistryJourney records the genuine historical journey: the custom
// selection pinned through the caller registry's own fold, two failed
// attempts, the approved contract, the bound evidence, the independent
// qualified verdict, and one further failed attempt. With the policy
// declared, the verdict renews the nonprogress window at its own sequence
// and only the last failure counts.
func progressRegistryJourney(t *testing.T, s *Store, registry DefinitionRegistry, registered RegisteredDefinition, workID string) (verdictSeq int64) {
	t.Helper()
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	selection := Operation{Events: []Event{
		workflowEventWithActor("progress-owner-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef,
			"agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent",
		}),
		workflowTypedEvent("progress-selection-"+workID, WorkflowDefinitionSelected, workID, ownerRef, time.Unix(9, 0).UTC(), version+1, map[string]any{
			"ref": registered.Definition.Ref, "version": registered.Definition.Version, "digest": registered.Digest,
			"work_kind": string(registered.Definition.WorkKind),
			"work_id":   workID, "expected_version": version, "resulting_version": version + 1,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}
	appendWorkflowEventsWithRegistry(t, s, registry, selection)
	// Two failed attempts stand behind the qualification.
	progressFailedAttempt(t, s, workID, 1)
	progressFailedAttempt(t, s, workID, 2)
	// The contract, the durably bound evidence, and the independent verdict.
	version = readWorkVersion(t, s, workID)
	contract := workflowTypedEvent("progress-contract-"+workID, WorkflowContractApproved, workID, ownerRef, time.Unix(9, 0).UTC(), version, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": int64(1), "premise": "the custom policy journey premise",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:progress", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{
			"kind": "outcome", "allowed": []string{"resolved"}}}},
		"required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{},
		"law_modifies": []string{}, "rigor_class": "prototype_internal", "decision_domains": []string{},
		"consequence_class": "internal_sqlite",
	})
	appendWorkflowEventsWithRegistry(t, s, registry, Operation{Events: []Event{contract}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-review", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version = readWorkVersion(t, s, workID)
	runRef := workID + "-evidence-run"
	seedWorkflowAuthority(t, s, runRef, workID, reviewer.PrincipalRef, runRef, []string{"commit:progress"})
	qualified := Operation{Events: []Event{
		workflowEventWithActor("progress-reviewer-"+workID, WorkflowActorRecorded, workID, reviewerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef,
			"agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent",
		}),
		workflowTypedEvent("progress-evidence-"+workID, WorkflowEvidenceBound, workID, ownerRef, time.Unix(9, 0).UTC(), version+1, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"evidence_kind": "artifact", "immutable_subject_ref": "commit:progress",
			"producer_id": reviewer.PrincipalRef, "producer_run_ref": runRef, "producer_watermark": runRef, "observed_at": "2026-10-19T00:00:00Z",
		}),
		workflowTypedEvent("progress-verdict-"+workID, WorkflowVerdictRecorded, workID, reviewerRef, time.Unix(9, 0).UTC(), version+2, map[string]any{
			"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2,
			"contract_version": int64(1), "predicate_id": "predicate:progress", "verdict_kind": "ok",
			"verdict_actor_ref": reviewerRef, "evaluation_evidence": []string{"commit:progress"}, "incomparable_with_approved": false,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}
	appendWorkflowEventsWithRegistry(t, s, registry, qualified)
	if err := s.DatabaseForTesting().QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? AND event_id=?`, workID, WorkflowVerdictRecorded, "progress-verdict-"+workID).Scan(&verdictSeq); err != nil {
		t.Fatal(err)
	}
	// One further failure counts; the two behind the renewed window do not.
	progressFailedAttempt(t, s, workID, 3)
	return verdictSeq
}

// progressFailedAttempt records one dispatched and failed worker attempt
// through the real worker folds, with the fenced attempt authorization the
// workflow epoch read consumes.
func progressFailedAttempt(t *testing.T, s *Store, workID string, n int) {
	t.Helper()
	attemptID := fmt.Sprintf("attempt:%s:%d", workID, n)
	lane := BuiltinLaneDefinitions()[0]
	var currentStep string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	started := map[string]any{"work_id": workID, "action_id": "dispatch_worker", "step_id": currentStep, "attempt_epoch": n}
	if _, err := tx.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_started', 'work_item', ?, 'worker:test', '2026-10-19T00:00:00Z', 2, ?)`,
		fmt.Sprintf("progress-started-%s-%d", workID, n), workID, string(mustJSONValue(started))); err != nil {
		t.Fatalf("record the fenced attempt authorization %d: %v", n, err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dispatch := Event{EventID: fmt.Sprintf("progress-dispatch-%s-%d", workID, n), Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
		ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion,
	})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatalf("record dispatch %d: %v", n, err)
	}
	fail := Event{EventID: fmt.Sprintf("progress-failed-%s-%d", workID, n), Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(21, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureWorkerError, Detail: "the bounded attempt failed"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{fail}}); err != nil {
		t.Fatalf("record failure %d: %v", n, err)
	}
}

// readRegistryThreadedPin reads the pin through the caller registry, the one
// surface where a custom registry is selected.
func readRegistryThreadedPin(t *testing.T, s *Store, registry DefinitionRegistry, workID string) (WorkPin, error) {
	t.Helper()
	var pin WorkPin
	var pinErr error
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		pin, _, pinErr = ReadMutationWorkPinTxForRegistry(context.Background(), tx, registry, workID, nil)
		return pinErr
	})
	if err != nil {
		return WorkPin{}, err
	}
	return pin, pinErr
}

// TestCustomRegistryProgressQualifiesThroughTheCallerRegistry proves the
// recorded custom selection resolves through the caller registry: the
// qualified verdict renews the nonprogress window at its own sequence, the
// pin's window and the admission loader answer the same count, and the
// published window validates.
func TestCustomRegistryProgressQualifiesThroughTheCallerRegistry(t *testing.T) {
	t.Parallel()
	const workID = "progress-custom-qualifies"
	s := openTemp(t)
	defer s.Close()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	registry, registered := progressCustomRegistry(t, progressCustomDefinition(t, true))
	verdictSeq := progressRegistryJourney(t, s, registry, registered, workID)
	pin, err := readRegistryThreadedPin(t, s, registry, workID)
	if err != nil {
		t.Fatalf("registry-threaded pin: %v", err)
	}
	window := pin.RetryWindow
	if window == nil {
		t.Fatal("the registry-threaded pin carries no retry window")
	}
	if window.Policy != WorkflowProgressPolicyObligationSatisfactionV1 {
		t.Fatalf("window policy = %q, want the custom definition's declared policy", window.Policy)
	}
	if window.Count != 1 {
		t.Fatalf("nonprogress count = %d, want only the failure after the qualified verdict", window.Count)
	}
	if window.NonprogressAnchor == nil || window.NonprogressAnchor.Seq != verdictSeq || window.NonprogressAnchor.Qualification != "comparable_predicate_satisfied" {
		t.Fatalf("nonprogress anchor = %+v, want the qualified verdict at seq %d", window.NonprogressAnchor, verdictSeq)
	}
	if window.NonprogressAnchor.EventID != "progress-verdict-"+workID || window.NonprogressAnchor.Kind != WorkflowVerdictRecorded {
		t.Fatalf("nonprogress anchor identity = %s/%s, want the verdict event's own row", window.NonprogressAnchor.EventID, window.NonprogressAnchor.Kind)
	}
	// The admission loader folds the same history through the same registry:
	// pin and loader agree on the renewed window.
	var state WorkflowAdmissionState
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		q, qErr := transactionSQL(tx, "progress_registry_test")
		if qErr != nil {
			return qErr
		}
		state, _, _, qErr = loadWorkflowAdmissionStateTxForRegistry(context.Background(), q, workID, registered.Definition, pin.Step, "progress_registry_test", registry)
		return qErr
	}); err != nil {
		t.Fatalf("registry-threaded admission fold: %v", err)
	}
	if state.NonProgressAttempts != window.Count {
		t.Fatalf("loader nonprogress count = %d, want the pin's %d", state.NonProgressAttempts, window.Count)
	}
	if marshaled, err := json.Marshal(window); err != nil {
		t.Fatal(err)
	} else if err := payloadschema.Validate("workflow_retry_window", marshaled); err != nil {
		t.Fatalf("the qualified window violates the published contract: %v", err)
	}
}

// TestCustomRegistryLegacySelectionStaysWorkerAcceptance proves the
// resolution fix alone qualifies nothing: the same genuine custom journey on
// a definition without the progress policy keeps worker-acceptance behavior.
func TestCustomRegistryLegacySelectionStaysWorkerAcceptance(t *testing.T) {
	t.Parallel()
	const workID = "progress-custom-legacy"
	s := openTemp(t)
	defer s.Close()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	registry, registered := progressCustomRegistry(t, progressCustomDefinition(t, false))
	progressRegistryJourney(t, s, registry, registered, workID)
	pin, err := readRegistryThreadedPin(t, s, registry, workID)
	if err != nil {
		t.Fatalf("registry-threaded pin: %v", err)
	}
	window := pin.RetryWindow
	if window == nil {
		t.Fatal("the registry-threaded pin carries no retry window")
	}
	if window.Policy != "worker_acceptance" {
		t.Fatalf("window policy = %q, want worker_acceptance on a pin without the policy", window.Policy)
	}
	if window.NonprogressAnchor != nil {
		t.Fatalf("nonprogress anchor = %+v, want an explicit null: the legacy policy qualifies no coordinator satisfaction", window.NonprogressAnchor)
	}
	if window.Count != 3 {
		t.Fatalf("nonprogress count = %d, want all three failed attempts behind the worker-acceptance window", window.Count)
	}
}

// TestCustomRegistryWrongSelectionPinRefuses proves a recorded selection that
// no longer verifies through the caller registry fails closed on the
// progress-qualified reads instead of downgrading to worker-acceptance
// behavior.
func TestCustomRegistryWrongSelectionPinRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		field string
		value string
	}{
		{name: "wrong digest", field: "digest", value: "sha256:" + strings.Repeat("a", 64)},
		{name: "unregistered reference", field: "ref", value: "workflow.progress_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const workID = "progress-custom-wrong-pin"
			s := openTemp(t)
			defer s.Close()
			seedWork(t, s, workID)
			seedWorkflowLaw(t, s)
			registry, registered := progressCustomRegistry(t, progressCustomDefinition(t, true))
			progressRegistryJourney(t, s, registry, registered, workID)
			// The historical drift: a later recorded selection no longer
			// names a pin the registry verifies. The log row is authority;
			// the walk must refuse rather than resolve anything else.
			var payload string
			if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND event_id=?`, workID, WorkflowDefinitionSelected, "progress-selection-"+workID).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(payload), &fields); err != nil {
				t.Fatal(err)
			}
			fields[tc.field] = tc.value
			version := readWorkVersion(t, s, workID)
			fields["expected_version"] = version
			fields["resulting_version"] = version + 1
			tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := enterFold(context.Background(), tx); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, ?, 'work_item', ?, 'principal/operator', '2026-10-19T01:00:00Z', 2, ?)`,
				"progress-selection-drift-"+workID, WorkflowDefinitionSelected, workID, string(mustJSONValue(fields))); err != nil {
				t.Fatalf("record the drifted selection: %v", err)
			}
			if err := leaveFold(context.Background(), tx); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := readRegistryThreadedPin(t, s, registry, workID); err == nil {
				t.Fatal("the registry-threaded pin read a drifted selection; want the typed refusal")
			} else {
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation {
					t.Fatalf("drifted selection pin = %v, want the typed pin refusal", err)
				}
			}
		})
	}
}
