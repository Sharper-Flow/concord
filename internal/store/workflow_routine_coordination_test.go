package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// routineResolve drives one overlap resolution through the store's direct
// entry with the endpoints' current versions.
func routineResolve(t *testing.T, s *Store, from, to, kind, actor, eventID string) error {
	t.Helper()
	fromVersion, toVersion := readWorkVersion(t, s, from), readWorkVersion(t, s, to)
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(context.Background(), tx, WorkflowDomainOverlapResolutionRequest{
			EventID: eventID, FromWorkID: from, ToWorkID: to,
			FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
			FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind,
			Reason: "routine coordination settlement",
			Actor:  actor,
		})
		return err
	})
	return err
}

// routineOverlapBoundaryEvent builds one raw overlap resolution event at an
// explicit payload version on the fixture's fixed pair.
func routineOverlapBoundaryEvent(id, actor string, fromExpected, toExpected int64, kind string, payloadVersion int) Event {
	fromWorkID, toWorkID := "content-left", "content-right"
	fields := map[string]any{
		"work_id": fromWorkID, "expected_version": fromExpected, "resulting_version": fromExpected + 1,
		"to_work_id": toWorkID, "to_expected_version": toExpected, "to_resulting_version": toExpected + 1,
		"from_contract_version": int64(1), "to_contract_version": int64(1),
		"resolution_kind": kind, "reason": "routine coordination settlement",
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return Event{
		EventID: id, Kind: WorkflowOverlapResolved, SubjectType: SubjectWorkItem, SubjectID: fromWorkID,
		Actor: actor, OccurredAt: time.Unix(41, 0).UTC(), PayloadVersion: payloadVersion, Payload: raw,
	}
}

func routineCorrectionEvent(id, workID, actor string, expected int64, targetEventID string, targetSeq int64, targetPayloadVersion int) Event {
	fields := map[string]any{
		"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
		"target_event_id": targetEventID, "target_seq": targetSeq, "target_payload_version": targetPayloadVersion,
		"reason":            "the asserted delivery named repository paths before the merge",
		"delivery_artifact": deliveryCorrectionMergeRef, "delivery_state": "asserted",
		"evidence_source": DeliveryEvidenceSourceCoordinatorAsserted,
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return Event{
		EventID: id, Kind: WorkflowDeliveryCorrected, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: actor, OccurredAt: time.Unix(42, 0).UTC(), PayloadVersion: 2, Payload: raw,
	}
}

func routineFailure(t *testing.T, err error) *Failure {
	t.Helper()
	if err == nil {
		t.Fatal("coordination operation was admitted, want a typed refusal")
	}
	failure := &Failure{}
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a *Failure", err)
	}
	return failure
}

func routinePairVersions(t *testing.T, s *Store) (int64, int64) {
	t.Helper()
	return readWorkVersion(t, s, "content-left"), readWorkVersion(t, s, "content-right")
}

func routineOverlapEventCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=? AND (subject_id='content-left' OR subject_id='content-right')`, WorkflowOverlapResolved).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func routineApprovalActorRows(t *testing.T, s *Store) int {
	t.Helper()
	var rows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE agent_ref LIKE 'approval:%'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// unsignedDeliveryCorrectionRequest builds the direct-entry correction the
// routine path admits: the correction carries no approval claim and
// attributes the real recorded coordinator actor instead. Actor carries the
// recorded coordinator ref the fixture's own actor event created.
func unsignedDeliveryCorrectionRequest(t *testing.T, s *Store, workID string, version int64, targetEventID string, targetSeq int64, targetPayloadVersion int) WorkflowDeliveryCorrectionRequest {
	request := deliveryCorrectionRequest(workID, version, targetEventID, targetSeq, targetPayloadVersion)
	request.Actor = readWorkflowRecordedActorRef(t, s, workID)
	return request
}

func overlapContentFixture(t *testing.T) (*Store, WorkflowArchitectureBinding, string) {
	t.Helper()
	s, _, binding, _, _, _ := architectureValidationFixture(t, "content-left")
	overlapFixtureExec(t, s, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) SELECT home_project_id,home_locator_id,product_id,'extra','Extra','Synthetic Domain',status,registry_content_hash,scanned_commit_oid FROM domains WHERE domain_id='child'`)
	seedWork(t, s, "content-right")
	overlapFixtureExec(t, s, `UPDATE work_items SET intent_json=json_set(intent_json,'$.governing_requirements',json('[]')) WHERE id IN ('content-left','content-right')`)
	actor := seedProductChangingContract(t, s, "content-left", binding)
	seedProductChangingContract(t, s, "content-right", binding)
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return s, binding, ref
}

func TestRoutineCoordination(t *testing.T) {
	t.Parallel()

	t.Run("routine overlap kinds resolve unsigned", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []string{ResolutionDependsOn, ResolutionBlocks} {
			t.Run(kind, func(t *testing.T) {
				t.Parallel()
				s, _, actor := overlapContentFixture(t)
				beforeFrom, beforeTo := routinePairVersions(t, s)
				if err := routineResolve(t, s, "content-left", "content-right", kind, actor, "routine-"+kind); err != nil {
					t.Fatalf("routine %s resolution refused: %v", kind, err)
				}
				var relationCount int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE resolution_id=? AND kind=?`, "routine-"+kind, kind).Scan(&relationCount); err != nil {
					t.Fatal(err)
				}
				if relationCount != 1 {
					t.Fatalf("routine %s resolution wrote %d relations, want 1", kind, relationCount)
				}
				fromVersion, toVersion := routinePairVersions(t, s)
				if fromVersion != beforeFrom+1 || toVersion != beforeTo+1 {
					t.Fatalf("routine %s resolution moved versions %d/%d to %d/%d, want each advanced by one", kind, beforeFrom, beforeTo, fromVersion, toVersion)
				}
				var storedVersion int
				if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version FROM effect_receipts WHERE event_id=?`, "routine-"+kind).Scan(&storedVersion); err != nil {
					t.Fatal(err)
				}
				if storedVersion != eventKindRegistry[WorkflowOverlapResolved].CurrentVersion {
					t.Fatalf("entry wrote payload version %d, want the registered current version %d", storedVersion, eventKindRegistry[WorkflowOverlapResolved].CurrentVersion)
				}
			})
		}
	})

	t.Run("a v2 governing scope cut without cross scope refuses", func(t *testing.T) {
		t.Parallel()
		s, _, actor := overlapContentFixture(t)
		overlapFixtureExec(t, s, `UPDATE work_items SET intent_json=json_set(intent_json,'$.governing_requirements',json('["audit_required"]')) WHERE id='content-left'`)
		fromVersion, toVersion := routinePairVersions(t, s)
		event := routineOverlapBoundaryEvent("routine-v2-scope-cut", actor, fromVersion, toVersion, ResolutionMergedInto, 2)
		err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion("content-left", fromVersion)})
		failure := routineFailure(t, err)
		if failure.Kind != KindUnauthorized || len(failure.Violations) != 1 || failure.Violations[0] != "audit_required" {
			t.Fatalf("v2 scope cut without cross scope = %v, want the omitted requirement and unauthorized", err)
		}
	})

	t.Run("delivery correction keeps its exact target admission", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-coord-correction-target"
		s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
		unsigned := func(eventID string) WorkflowDeliveryCorrectionRequest {
			request := unsignedDeliveryCorrectionRequest(t, s, workID, version, targetEventID, targetSeq, targetPayloadVersion)
			request.EventID = eventID
			return request
		}

		// The original assertion bytes are captured before any correction
		// effect, so a live writer that rewrote the assertion could not
		// escape the comparison after the effect and the rebuild.
		var originalPayload string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, targetEventID).Scan(&originalPayload); err != nil {
			t.Fatal(err)
		}
		var nonAssertionEventID string
		if err := s.DatabaseForTesting().QueryRow(`SELECT r.event_id FROM effect_receipts r JOIN workflow_phase_exits x ON x.exit_seq=r.effect_seq WHERE x.work_id=? AND x.action_id='confirm_premise' AND r.delivery_assertion=0 ORDER BY r.effect_seq LIMIT 1`, workID).Scan(&nonAssertionEventID); err != nil {
			t.Fatal(err)
		}

		cases := []struct {
			name      string
			mutate    func(request WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest
			wantKind  FailureKind
			wantPiece string
		}{
			{name: "wrong target sequence", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
				r.TargetSeq += 7
				return r
			}, wantKind: KindInvalidOperation, wantPiece: "does not match the recorded assertion"},
			{name: "wrong target payload version", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
				r.TargetPayloadVersion++
				return r
			}, wantKind: KindInvalidOperation, wantPiece: "does not match the recorded assertion"},
			{name: "target is not an assertion", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
				r.TargetEventID = nonAssertionEventID
				return r
			}, wantKind: KindInvalidOperation, wantPiece: "not a recorded delivery assertion"},
			{name: "target event is not recorded", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
				r.TargetEventID = "absent-" + workID
				return r
			}, wantKind: KindProjectionNotFound, wantPiece: "not recorded"},
		}
		for _, item := range cases {
			requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, item.mutate(unsigned("correction-"+workID))), item.wantKind, item.wantPiece)
		}

		// A stale assertion (a later assertion replaced it) refuses: only
		// the current delivery assertion is correctable.
		staleStore := completedDeliveryFixtureWithAssertions(t, "routine-coord-correction-stale", 2)
		staleEventID, staleSeq, stalePayloadVersion := deliveryAssertionEvent(t, staleStore, "routine-coord-correction-stale", 1)
		stale := unsignedDeliveryCorrectionRequest(t, staleStore, "routine-coord-correction-stale", readWorkVersion(t, staleStore, "routine-coord-correction-stale"), staleEventID, staleSeq, stalePayloadVersion)
		stale.EventID = "routine-correction-stale"
		requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, staleStore, stale), KindInvalidOperation, "not the current delivery assertion")

		// The unsigned direct entry corrects once and attributes the real
		// recorded coordinator actor, not an approval-derived operator.
		settled := unsigned("routine-correction-first")
		if err := runDeliveryCorrection(t, s, settled); err != nil {
			t.Fatalf("unsigned direct-entry delivery correction refused: %v", err)
		}
		var eventActor string
		if err := s.DatabaseForTesting().QueryRow(`SELECT actor FROM effect_receipts WHERE event_id=?`, settled.EventID).Scan(&eventActor); err != nil {
			t.Fatal(err)
		}
		if expected := readWorkflowRecordedActorRef(t, s, workID); eventActor != expected {
			t.Fatalf("correction actor = %q, want the recorded coordinator %q", eventActor, expected)
		}
		if got := readWorkVersion(t, s, workID); got != version+1 {
			t.Fatalf("correction version = %d, want the single correction advance %d", got, version+1)
		}
		var afterEffectPayload string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, targetEventID).Scan(&afterEffectPayload); err != nil {
			t.Fatal(err)
		}
		if originalPayload != afterEffectPayload {
			t.Fatal("the correction effect rewrote the original assertion event payload")
		}
		second := unsignedDeliveryCorrectionRequest(t, s, workID, readWorkVersion(t, s, workID), targetEventID, targetSeq, targetPayloadVersion)
		second.EventID = "routine-correction-second"
		requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, second), KindInvalidOperation, "already corrected")

		// The instance stays closed after correction.
		var instanceState string
		if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
			t.Fatal(err)
		}
		if instanceState != "completed" {
			t.Fatalf("correction reopened the instance to %q", instanceState)
		}
	})

	t.Run("current delivery correction payload admission", func(t *testing.T) {
		t.Parallel()
		t.Run("a v2 correction records the real coordinator without an approval claim", func(t *testing.T) {
			t.Parallel()
			const workID = "routine-coord-correction-v2"
			s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
			correctionRef := readWorkflowRecordedActorRef(t, s, workID)
			event := routineCorrectionEvent("routine-correction-v2", workID, correctionRef, version, targetEventID, targetSeq, targetPayloadVersion)
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, version)}); err != nil {
				t.Fatalf("v2 correction without an approval claim refused: %v", err)
			}
			if got := readWorkVersion(t, s, workID); got != version+1 {
				t.Fatalf("v2 correction left version %d, want %d", got, version+1)
			}
			read, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
			if err != nil {
				t.Fatal(err)
			}
			if read.DeliveryAssertion == nil || read.DeliveryAssertion.Correction == nil || read.DeliveryAssertion.Correction.Artifact != deliveryCorrectionMergeRef {
				t.Fatalf("v2 correction overlay read = %+v, want the corrected merge evidence", read.DeliveryAssertion)
			}
			var instanceState string
			if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
				t.Fatal(err)
			}
			if instanceState != "completed" {
				t.Fatalf("v2 correction reopened the instance to %q", instanceState)
			}
		})
		t.Run("a v2 correction requires a recorded authenticated actor", func(t *testing.T) {
			t.Parallel()
			const workID = "routine-coord-correction-v2-actor"
			s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
			event := routineCorrectionEvent("routine-correction-v2-actor", workID, "principal:ghost", version, targetEventID, targetSeq, targetPayloadVersion)
			err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, version)})
			failure := routineFailure(t, err)
			if failure.Kind != KindUnauthorized {
				t.Fatalf("v2 correction with an unrecorded actor error = %v, want unauthorized", err)
			}
		})
		t.Run("a v2 correction rejects an operator substitute actor", func(t *testing.T) {
			t.Parallel()
			const workID = "routine-coord-correction-v2-operator"
			s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
			substitute := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator-substitute", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
			substituteRef, err := WorkflowActorRef(substitute)
			if err != nil {
				t.Fatal(err)
			}
			substituteEvent := workflowEvent("operator-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{
				"work_id": workID, "expected_version": version, "resulting_version": version + 1,
				"actor_ref": substituteRef, "principal_ref": substitute.PrincipalRef, "client_ref": substitute.ClientRef,
				"agent_ref": substitute.AgentRef, "session_ref": substitute.SessionRef, "actor_class": "operator",
			})
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{substituteEvent}, ExpectedVersions: workVersion(workID, version)}); err != nil {
				t.Fatal(err)
			}
			event := routineCorrectionEvent("routine-correction-v2-operator", workID, substituteRef, version+1, targetEventID, targetSeq, targetPayloadVersion)
			err = applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, version+1)})
			failure := routineFailure(t, err)
			if failure.Kind != KindUnauthorized {
				t.Fatalf("v2 correction attributed to an operator substitute error = %v, want unauthorized", err)
			}
		})
	})
}

// readWorkflowRecordedActorRef returns the delivery fixture owner's recorded actor.
func readWorkflowRecordedActorRef(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var actorRef string
	if err := s.db.QueryRow(`SELECT actor_ref FROM workflow_actors WHERE principal_ref='principal/operator' AND client_ref='client/concord-1' AND agent_ref='agent/owner' AND session_ref=?`, "session/"+workID).Scan(&actorRef); err != nil {
		t.Fatal(err)
	}
	return actorRef
}

func routineWorkEventCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func routineActorRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// requireRoutineCorrectionNoEffect holds the no-effect surface every refused
// correction must leave untouched: the work's domain events, the durable
// actor rows, and the work version.
func requireRoutineCorrectionNoEffect(t *testing.T, s *Store, workID string, events, actors int, version int64) {
	t.Helper()
	if got := routineWorkEventCount(t, s, workID); got != events {
		t.Fatalf("refused correction left %d events on %s, want %d", got, workID, events)
	}
	if got := routineActorRowCount(t, s); got != actors {
		t.Fatalf("refused correction left %d actor rows, want %d", got, actors)
	}
	if got := readWorkVersion(t, s, workID); got != version {
		t.Fatalf("refused correction moved the work version to %d, want %d", got, version)
	}
}

// TestRoutineCorrectionActorPrefix pins the fresh-session correction route:
// the request's AuthenticatedActor carries the already-authenticated caller
// tuple from the agent boundary, so an absent actor row records through the
// shared prefix owner before the correction, while a supplied tuple never
// substitutes for or overwrites a recorded identity.
func TestRoutineCorrectionActorPrefix(t *testing.T) {
	t.Parallel()

	t.Run("a fresh authenticated coordinator tuple records its prefix and corrects", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-coord-correction-prefix"
		s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
		fresh := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/fresh-coordinator", SessionRef: "session/" + workID + "-fresh", ActorClass: ActorAgent}
		freshRef, err := WorkflowActorRef(fresh)
		if err != nil {
			t.Fatal(err)
		}
		var actorRows int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, freshRef).Scan(&actorRows); err != nil {
			t.Fatal(err)
		}
		if actorRows != 0 {
			t.Fatalf("fresh coordinator actor already recorded (%d rows)", actorRows)
		}
		var originalPayload string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, targetEventID).Scan(&originalPayload); err != nil {
			t.Fatal(err)
		}
		var watermark int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(effect_seq),0) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&watermark); err != nil {
			t.Fatal(err)
		}
		actorsBefore := routineActorRowCount(t, s)
		request := unsignedDeliveryCorrectionRequest(t, s, workID, version, targetEventID, targetSeq, targetPayloadVersion)
		request.Actor = freshRef
		request.AuthenticatedActor = &fresh
		request.EventID = "routine-correction-prefix"
		if err := runDeliveryCorrection(t, s, request); err != nil {
			t.Fatalf("fresh authenticated tuple correction refused: %v", err)
		}
		// Exactly the actor prefix and the correction landed on this work,
		// the prefix first, both attributed to the fresh coordinator.
		var newEvents []struct {
			kind    string
			actor   string
			payload string
		}
		rows, err := s.DatabaseForTesting().Query(`SELECT kind,actor,payload_digest FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND effect_seq>? ORDER BY effect_seq`, workID, watermark)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var item struct {
				kind    string
				actor   string
				payload string
			}
			if err := rows.Scan(&item.kind, &item.actor, &item.payload); err != nil {
				t.Fatal(err)
			}
			newEvents = append(newEvents, item)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if len(newEvents) != 2 {
			t.Fatalf("fresh-tuple correction wrote %d events, want exactly the actor prefix and the correction", len(newEvents))
		}
		if newEvents[0].kind != WorkflowActorRecorded || newEvents[1].kind != WorkflowDeliveryCorrected {
			t.Fatalf("new events = %s then %s, want the actor prefix recorded before the correction", newEvents[0].kind, newEvents[1].kind)
		}
		if newEvents[0].actor != freshRef || newEvents[1].actor != freshRef {
			t.Fatalf("new event actors = %q,%q, want both attributed to %q", newEvents[0].actor, newEvents[1].actor, freshRef)
		}
		prefixDigest, err := effectPayloadDigest(mustJSONValue(map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"actor_ref": freshRef, "principal_ref": fresh.PrincipalRef, "client_ref": fresh.ClientRef,
			"agent_ref": fresh.AgentRef, "session_ref": fresh.SessionRef, "actor_class": string(ActorAgent),
		}))
		if err != nil || newEvents[0].payload != prefixDigest {
			t.Fatalf("prefix digest=%q, want the exact authenticated tuple/version digest %q: %v", newEvents[0].payload, prefixDigest, err)
		}
		var principal, client, agent, session, class string
		if err := s.DatabaseForTesting().QueryRow(`SELECT principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, freshRef).Scan(&principal, &client, &agent, &session, &class); err != nil {
			t.Fatalf("no recorded actor row after the correction: %v", err)
		}
		if principal != fresh.PrincipalRef || client != fresh.ClientRef || agent != fresh.AgentRef || session != fresh.SessionRef || class != string(ActorAgent) {
			t.Fatalf("recorded actor tuple = %q,%q,%q,%q,%q, want the exact canonical tuple", principal, client, agent, session, class)
		}
		correctionDigest, err := effectPayloadDigest(mustJSONValue(map[string]any{
			"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2,
			"target_event_id": request.TargetEventID, "target_seq": request.TargetSeq, "target_payload_version": request.TargetPayloadVersion,
			"reason": request.Reason, "delivery_artifact": request.DeliveryArtifact, "delivery_state": request.DeliveryState,
			"evidence_source": DeliveryEvidenceSourceCoordinatorAsserted,
		}))
		if err != nil || newEvents[1].payload != correctionDigest {
			t.Fatalf("correction digest=%q, want the exact unsigned request/version digest %q: %v", newEvents[1].payload, correctionDigest, err)
		}
		if got := routineActorRowCount(t, s); got != actorsBefore+1 {
			t.Fatalf("actor rows after the effect = %d, want only the fresh coordinator row added to %d", got, actorsBefore)
		}
		if got := readWorkVersion(t, s, workID); got != version+2 {
			t.Fatalf("correction version = %d, want the prefix plus correction advance %d", got, version+2)
		}
		var afterPayload string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, targetEventID).Scan(&afterPayload); err != nil {
			t.Fatal(err)
		}
		if originalPayload != afterPayload {
			t.Fatal("the correction effect rewrote the original assertion event payload")
		}
		var instanceState string
		if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
			t.Fatal(err)
		}
		if instanceState != "completed" {
			t.Fatalf("correction reopened the instance to %q", instanceState)
		}
	})

	t.Run("a recorded ref with a different authenticated tuple refuses", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-coord-correction-mismatch"
		s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
		other := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/other-genuine", SessionRef: "session/" + workID + "-other", ActorClass: ActorAgent}
		otherRef, err := WorkflowActorRef(other)
		if err != nil {
			t.Fatal(err)
		}
		// Actor names the real recorded owner ref while the authenticated
		// tuple is a different genuine coordinator: the recorded identity,
		// not the requested ref, decides.
		request := unsignedDeliveryCorrectionRequest(t, s, workID, version, targetEventID, targetSeq, targetPayloadVersion)
		if otherRef == request.Actor {
			t.Fatal("mismatch fixture derived the recorded ref")
		}
		request.AuthenticatedActor = &other
		request.EventID = "routine-correction-mismatch"
		eventsBefore := routineWorkEventCount(t, s, workID)
		actorsBefore := routineActorRowCount(t, s)
		requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, request), KindInvariantViolation, "does not match the authenticated actor")
		requireRoutineCorrectionNoEffect(t, s, workID, eventsBefore, actorsBefore, version)
	})

	t.Run("a malformed fresh tuple refuses without effects", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-coord-correction-malformed"
		s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
		malformed := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/fresh-malformed", ActorClass: ActorAgent}
		// The requested ref names a fully recorded coordinator, so only the
		// tuple validation can refuse: a valid attribution ref never
		// substitutes for the authenticated tuple.
		request := unsignedDeliveryCorrectionRequest(t, s, workID, version, targetEventID, targetSeq, targetPayloadVersion)
		request.AuthenticatedActor = &malformed
		request.EventID = "routine-correction-malformed"
		eventsBefore := routineWorkEventCount(t, s, workID)
		actorsBefore := routineActorRowCount(t, s)
		requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, request), KindUnauthorized, "actor tuple is incomplete")
		requireRoutineCorrectionNoEffect(t, s, workID, eventsBefore, actorsBefore, version)
	})

	t.Run("a fresh operator tuple refuses without effects", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-coord-correction-operator-tuple"
		s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
		operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/fresh-operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
		operatorRef, err := WorkflowActorRef(operator)
		if err != nil {
			t.Fatal(err)
		}
		request := unsignedDeliveryCorrectionRequest(t, s, workID, version, targetEventID, targetSeq, targetPayloadVersion)
		request.Actor = operatorRef
		request.AuthenticatedActor = &operator
		request.EventID = "routine-correction-operator-tuple"
		eventsBefore := routineWorkEventCount(t, s, workID)
		actorsBefore := routineActorRowCount(t, s)
		failure := routineFailure(t, runDeliveryCorrection(t, s, request))
		if failure.Kind != KindUnauthorized {
			t.Fatalf("fresh operator tuple error = %v, want unauthorized", failure)
		}
		requireRoutineCorrectionNoEffect(t, s, workID, eventsBefore, actorsBefore, version)
	})
}
