package store

// The store's own overlap-resolution and delivery-correction owners must
// enforce the same approval classes the workflow policy defines, not just the
// agent boundary. Routine overlap kinds (compatible_with, depends_on, blocks)
// are coordination answers that need no consent; merged_into and supersedes
// keep the approval requirement behind a consumed one-use approval whose
// exact kind, direction, and version binding is a Dispatch-layer fact: there
// the request digest covers the resolution kind and the from-to ordering,
// and the host assertion signs that digest. The store receives the same
// binding only as opaque fields — a digest plus recorded scope and version
// strings — and records them verbatim without re-deriving kind or direction
// from them. The delivery-correction routine path records
// the real recorded caller instead of deriving an operator from a consumed
// approval. A correction request may carry the already-authenticated caller
// tuple, which the shared actor-prefix owner records when its actor row is
// absent; a supplied tuple never substitutes a recorded identity.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// routineResolve drives one overlap resolution through the store's direct
// entry with the endpoints' current versions. Remaining callers exercise the
// routine kinds, so any supplied approval claim travels as a bare reference
// the writer refuses; the destructive classes settle only through the
// genuine host coverage in internal/agent.
func routineResolve(t *testing.T, s *Store, from, to, kind, approval, actor, eventID string) error {
	t.Helper()
	fromVersion, toVersion := readWorkVersion(t, s, from), readWorkVersion(t, s, to)
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(context.Background(), tx, WorkflowDomainOverlapResolutionRequest{
			EventID: eventID, FromWorkID: from, ToWorkID: to,
			FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
			FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind, SharedStep: sequencingSharedStep(kind),
			Reason: "routine coordination settlement", ApprovalRef: approval,
			Actor: actor,
		})
		return err
	})
	return err
}

// routineOverlapBoundaryEvent builds one raw overlap resolution event at an
// explicit payload version on the fixture's fixed pair. Approval claims are omitted entirely
// unless supplied.
func routineOverlapBoundaryEvent(id, actor string, fromExpected, toExpected int64, kind string, payloadVersion int, approval string) Event {
	fromWorkID, toWorkID := "content-left", "content-right"
	fields := map[string]any{
		"work_id": fromWorkID, "expected_version": fromExpected, "resulting_version": fromExpected + 1,
		"to_work_id": toWorkID, "to_expected_version": toExpected, "to_resulting_version": toExpected + 1,
		"from_contract_version": int64(1), "to_contract_version": int64(1),
		"resolution_kind": kind, "reason": "routine coordination settlement",
	}
	if approval != "" {
		fields["approval_ref"] = approval
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

// routineCorrectionEvent builds one raw delivery correction event at the
// routine payload version 2. Approval binding fields are omitted entirely
// unless supplied, which is the shape the routine path refuses.
func routineCorrectionEvent(id, workID, actor string, expected int64, targetEventID string, targetSeq int64, targetPayloadVersion int, approval map[string]string) Event {
	return routineCorrectionEventAt(id, workID, actor, expected, targetEventID, targetSeq, targetPayloadVersion, 2, approval)
}

func routineCorrectionEventAt(id, workID, actor string, expected int64, targetEventID string, targetSeq int64, targetPayloadVersion, payloadVersion int, approval map[string]string) Event {
	fields := map[string]any{
		"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
		"target_event_id": targetEventID, "target_seq": targetSeq, "target_payload_version": targetPayloadVersion,
		"reason":            "the asserted delivery named repository paths before the merge",
		"delivery_artifact": deliveryCorrectionMergeRef, "delivery_state": "asserted",
		"evidence_source": DeliveryEvidenceSourceCoordinatorAsserted,
	}
	for key, value := range approval {
		fields[key] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return Event{
		EventID: id, Kind: WorkflowDeliveryCorrected, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: actor, OccurredAt: time.Unix(42, 0).UTC(), PayloadVersion: payloadVersion, Payload: raw,
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
// routine path admits: the five approval fields are cleared entirely, because
// the correction carries no approval claim and attributes the real recorded
// coordinator actor instead. Actor carries the recorded coordinator ref the
// fixture's own actor event created.
func unsignedDeliveryCorrectionRequest(t *testing.T, s *Store, workID string, version int64, targetEventID string, targetSeq int64, targetPayloadVersion int) WorkflowDeliveryCorrectionRequest {
	request := deliveryCorrectionRequest(workID, version, targetEventID, targetSeq, targetPayloadVersion)
	request.ApprovalRef = ""
	request.ApprovalOperationDigest = ""
	request.ApprovalScopeJSON = ""
	request.ApprovalVersionsJSON = ""
	request.ApprovalConsequence = ""
	request.Actor = readWorkflowRecordedActorRef(t, s, workID)
	return request
}

// seedRecordedWorkerLaneActor records one lane workflow actor and its
// event-owned dispatch on the completed work, so the correction actor check
// can reject the recorded worker-lane identity the dispatch pinned. It
// returns the lane actor ref and the work version after the actor event.
func seedRecordedWorkerLaneActor(t *testing.T, s *Store, workID string, version int64) (string, int64) {
	t.Helper()
	lane := BuiltinLaneDefinitions()[0]
	laneActor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/lane:" + lane.ID, SessionRef: "session/" + workID + "-lane", ActorClass: ActorAgent}
	laneRef, err := WorkflowActorRef(laneActor)
	if err != nil {
		t.Fatal(err)
	}
	actorEvent := workflowEvent("lane-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": laneRef, "principal_ref": laneActor.PrincipalRef, "client_ref": laneActor.ClientRef,
		"agent_ref": laneActor.AgentRef, "session_ref": laneActor.SessionRef, "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{actorEvent}, ExpectedVersions: workVersion(workID, version)}); err != nil {
		t.Fatal(err)
	}
	dispatch := Event{
		EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(map[string]any{
			"attempt_id": "attempt:" + workID + "-lane", "lane_id": lane.ID, "lane_digest": lane.Digest,
			"capability_class": lane.CapabilityClass, "packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
			"packet_digest": "sha256:" + strings.Repeat("b", 64), "readback_model": preferredModelForLane(lane), "lane_actor_ref": laneRef,
		}),
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatalf("record the dispatched lane: %v", err)
	}
	return laneRef, version + 1
}

func TestRoutineCoordination(t *testing.T) {
	t.Parallel()

	t.Run("routine overlap kinds resolve without an approval claim", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []string{ResolutionCompatibleWith, ResolutionDependsOn, ResolutionBlocks} {
			t.Run(kind, func(t *testing.T) {
				t.Parallel()
				s, _, actor := overlapContentFixture(t)
				beforeFrom, beforeTo := routinePairVersions(t, s)
				if err := routineResolve(t, s, "content-left", "content-right", kind, "", actor, "routine-"+kind); err != nil {
					t.Fatalf("routine %s resolution without an approval claim refused: %v", kind, err)
				}
				var relationCount int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE resolution_id=? AND kind=?`, "routine-"+kind, kind).Scan(&relationCount); err != nil {
					t.Fatal(err)
				}
				if relationCount != 1 {
					t.Fatalf("routine %s resolution wrote %d relations, want 1", kind, relationCount)
				}
				var recordedRef string
				if err := s.DatabaseForTesting().QueryRow(`SELECT approval_ref FROM workflow_overlap_resolutions WHERE resolution_id=?`, "routine-"+kind).Scan(&recordedRef); err != nil {
					t.Fatal(err)
				}
				if recordedRef != "" {
					t.Fatalf("routine %s resolution recorded an approval claim %q", kind, recordedRef)
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

	t.Run("supplied approval claims refuse on routine kinds", func(t *testing.T) {
		t.Parallel()
		// A supplied approval on a routine path is never consumed or
		// transformed into human attribution: an unexpected authority claim
		// is rejected, not accepted as an inert record.
		for _, kind := range []string{ResolutionCompatibleWith, ResolutionDependsOn, ResolutionBlocks} {
			t.Run(kind, func(t *testing.T) {
				t.Parallel()
				s, _, actor := overlapContentFixture(t)
				beforeFrom, beforeTo := routinePairVersions(t, s)
				err := routineResolve(t, s, "content-left", "content-right", kind, "approval:routine-supplied", actor, "routine-supplied-claim-"+kind)
				failure := routineFailure(t, err)
				if failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "approval") {
					t.Fatalf("routine %s supplied claim error = %v, want an invalid_payload refusal naming the approval", kind, err)
				}
				afterFrom, afterTo := routinePairVersions(t, s)
				if afterFrom != beforeFrom || afterTo != beforeTo {
					t.Fatalf("refused supplied claim moved versions %d/%d to %d/%d", beforeFrom, beforeTo, afterFrom, afterTo)
				}
				var resolutions int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_overlap_resolutions`).Scan(&resolutions); err != nil {
					t.Fatal(err)
				}
				if resolutions != 0 {
					t.Fatalf("refused supplied claim left %d resolution rows", resolutions)
				}
				if count := routineOverlapEventCount(t, s); count != 0 {
					t.Fatalf("refused supplied claim left %d resolution events", count)
				}
				if rows := routineApprovalActorRows(t, s); rows != 0 {
					t.Fatalf("the refused claim left %d approval-derived workflow actor rows", rows)
				}
			})
		}
	})

	t.Run("endpoint and content freshness races refuse without effect", func(t *testing.T) {
		t.Parallel()
		t.Run("stale from endpoint version", func(t *testing.T) {
			t.Parallel()
			s, _, actor := overlapContentFixture(t)
			fromVersion, toVersion := routinePairVersions(t, s)
			err := s.Transact(context.Background(), func(tx *Transaction) error {
				_, err := ResolveWorkflowDomainOverlapTx(context.Background(), tx, WorkflowDomainOverlapResolutionRequest{
					EventID: "routine-race-from", FromWorkID: "content-left", ToWorkID: "content-right",
					FromExpectedVersion: fromVersion - 1, ToExpectedVersion: toVersion,
					FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: ResolutionCompatibleWith, SharedStep: sequencingSharedStep(ResolutionCompatibleWith),
					Reason: "stale declarer pin", Actor: actor,
				})
				return err
			})
			assertFailureKind(t, err, KindVersionConflict)
			if nowFrom, nowTo := routinePairVersions(t, s); nowFrom != fromVersion || nowTo != toVersion {
				t.Fatalf("refused stale race moved versions to %d/%d", nowFrom, nowTo)
			}
		})
		t.Run("stale to endpoint version", func(t *testing.T) {
			t.Parallel()
			s, _, actor := overlapContentFixture(t)
			fromVersion, toVersion := routinePairVersions(t, s)
			err := s.Transact(context.Background(), func(tx *Transaction) error {
				_, err := ResolveWorkflowDomainOverlapTx(context.Background(), tx, WorkflowDomainOverlapResolutionRequest{
					EventID: "routine-race-to", FromWorkID: "content-left", ToWorkID: "content-right",
					FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion - 1,
					FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: ResolutionCompatibleWith, SharedStep: sequencingSharedStep(ResolutionCompatibleWith),
					Reason: "stale peer pin", Actor: actor,
				})
				return err
			})
			assertFailureKind(t, err, KindVersionConflict)
			if nowFrom, nowTo := routinePairVersions(t, s); nowFrom != fromVersion || nowTo != toVersion {
				t.Fatalf("refused stale race moved versions to %d/%d", nowFrom, nowTo)
			}
		})
		t.Run("stale judged contract content", func(t *testing.T) {
			t.Parallel()
			s, binding, actor := overlapContentFixture(t)
			applyContentSuccessor(t, s, contentSuccessor(t, s, actor, 2, binding, []string{}))
			fromVersion, toVersion := routinePairVersions(t, s)
			err := s.Transact(context.Background(), func(tx *Transaction) error {
				_, err := ResolveWorkflowDomainOverlapTx(context.Background(), tx, WorkflowDomainOverlapResolutionRequest{
					EventID: "routine-race-content", FromWorkID: "content-left", ToWorkID: "content-right",
					FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
					FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: ResolutionCompatibleWith, SharedStep: sequencingSharedStep(ResolutionCompatibleWith),
					Reason: "resolution pinned to the superseded contract", Actor: actor,
				})
				return err
			})
			failure := routineFailure(t, err)
			if failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "not pinned to both current Product contract versions") {
				t.Fatalf("stale content race error = %v, want the contract-freshness refusal", err)
			}
		})
	})

	t.Run("overlap resolution cycles refuse with rollback", func(t *testing.T) {
		t.Parallel()
		// A direct reversal of one pair's resolution is a re-resolution, not
		// a cycle: the fold invalidates the old pair resolution (and its
		// relation) before the cycle check runs. The cycle guard fires on an
		// indirect loop across three pairs, so the witness chains a third
		// pairwise-overlapping work: A->B, B->C, then the closing C->A.
		for _, kind := range []string{ResolutionDependsOn, ResolutionBlocks} {
			t.Run(kind, func(t *testing.T) {
				t.Parallel()
				s, binding, actor := overlapContentFixture(t)
				seedWork(t, s, "content-third")
				seedProductChangingContract(t, s, "content-third", binding)
				if err := routineResolve(t, s, "content-left", "content-right", kind, "", actor, "routine-cycle-first-"+kind); err != nil {
					t.Fatalf("first %s leg refused: %v", kind, err)
				}
				if err := routineResolve(t, s, "content-right", "content-third", kind, "", actor, "routine-cycle-second-"+kind); err != nil {
					t.Fatalf("second %s leg refused: %v", kind, err)
				}
				versionsBefore := map[string]int64{}
				for _, workID := range []string{"content-left", "content-right", "content-third"} {
					versionsBefore[workID] = readWorkVersion(t, s, workID)
				}
				err := routineResolve(t, s, "content-third", "content-left", kind, "", actor, "routine-cycle-third-"+kind)
				failure := routineFailure(t, err)
				if failure.Kind != KindCycleDetected {
					t.Fatalf("closing %s leg error = %v, want cycle_detected", kind, err)
				}
				for workID, before := range versionsBefore {
					if after := readWorkVersion(t, s, workID); after != before {
						t.Fatalf("refused cycle moved %s from %d to %d", workID, before, after)
					}
				}
				if count := routineOverlapEventCount(t, s); count != 2 {
					t.Fatalf("cycle rollback left %d resolution events, want only the two settled legs", count)
				}
				var relations int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE kind=?`, kind).Scan(&relations); err != nil {
					t.Fatal(err)
				}
				if relations != 2 {
					t.Fatalf("cycle rollback left %d %s relations, want the two settled legs only", relations, kind)
				}
			})
		}
	})

	t.Run("current overlap payload admission", func(t *testing.T) {
		t.Parallel()
		t.Run("a v2 routine event needs no claim", func(t *testing.T) {
			t.Parallel()
			s, _, actor := overlapContentFixture(t)
			fromVersion, toVersion := routinePairVersions(t, s)
			event := routineOverlapBoundaryEvent("routine-v2-routine-claimless", actor, fromVersion, toVersion, ResolutionCompatibleWith, 2, "")
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion("content-left", fromVersion)}); err != nil {
				t.Fatalf("v2 routine event without a claim refused: %v", err)
			}
			var storedVersion int
			if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&storedVersion); err != nil {
				t.Fatal(err)
			}
			if storedVersion != 2 {
				t.Fatalf("stored v2 payload version = %d, want the source version kept", storedVersion)
			}
		})
		t.Run("a v2 governing scope cut without consent refuses", func(t *testing.T) {
			t.Parallel()
			s, _, actor := overlapContentFixture(t)
			// CD-0243 D7 retains scope-cut consent, not kind-only consent.
			overlapFixtureExec(t, s, `UPDATE work_items SET intent_json=json_set(intent_json,'$.governing_requirements',json('["audit_required"]')) WHERE id='content-left'`)
			fromVersion, toVersion := routinePairVersions(t, s)
			event := routineOverlapBoundaryEvent("routine-v2-destructive-claimless", actor, fromVersion, toVersion, ResolutionMergedInto, 2, "")
			err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion("content-left", fromVersion)})
			failure := routineFailure(t, err)
			if failure.Kind != KindApprovalRequired || len(failure.Violations) != 1 || failure.Violations[0] != "audit_required" {
				t.Fatalf("v2 scope cut without consent = %v, want the omitted requirement and approval_required", err)
			}
		})
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
			event := routineCorrectionEvent("routine-correction-v2", workID, correctionRef, version, targetEventID, targetSeq, targetPayloadVersion, nil)
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
			if correction := read.DeliveryAssertion.Correction; correction.ApprovalRef != "" {
				t.Fatalf("v2 correction recorded an approval claim %q", correction.ApprovalRef)
			}
			var instanceState string
			if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
				t.Fatal(err)
			}
			if instanceState != "completed" {
				t.Fatalf("v2 correction reopened the instance to %q", instanceState)
			}
		})
		t.Run("a v2 correction rejects a supplied approval claim by name", func(t *testing.T) {
			t.Parallel()
			const workID = "routine-coord-correction-v2-claim"
			s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
			correctionRef := readWorkflowRecordedActorRef(t, s, workID)
			event := routineCorrectionEvent("routine-correction-v2-claim", workID, correctionRef, version, targetEventID, targetSeq, targetPayloadVersion, map[string]string{
				"approval_ref": deliveryCorrectionApprovalRef, "approval_operation_digest": deliveryCorrectionDigest,
				"approval_scope_json": deliveryCorrectionScopeJSON(workID), "approval_versions_json": deliveryCorrectionVersionsJSON(version),
				"approval_consequence": deliveryCorrectionConsequence,
			})
			err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, version)})
			failure := routineFailure(t, err)
			if failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "approval") {
				t.Fatalf("v2 correction with a supplied claim error = %v, want an invalid_payload refusal naming the approval", err)
			}
			if got := readWorkVersion(t, s, workID); got != version {
				t.Fatalf("refused v2 claim moved version to %d", got)
			}
		})
		t.Run("a v2 correction requires a recorded authenticated actor", func(t *testing.T) {
			t.Parallel()
			const workID = "routine-coord-correction-v2-actor"
			s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
			event := routineCorrectionEvent("routine-correction-v2-actor", workID, "principal:ghost", version, targetEventID, targetSeq, targetPayloadVersion, nil)
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
			// The recorded operator actor the old approval-derived writer
			// would attribute is a substitute, never the coordinator of record.
			substitute := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "approval:" + deliveryCorrectionApprovalRef, SessionRef: "session/" + workID + "-approval", ActorClass: ActorOperator}
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
			event := routineCorrectionEvent("routine-correction-v2-operator", workID, substituteRef, version+1, targetEventID, targetSeq, targetPayloadVersion, nil)
			err = applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, version+1)})
			failure := routineFailure(t, err)
			if failure.Kind != KindUnauthorized {
				t.Fatalf("v2 correction attributed to an operator substitute error = %v, want unauthorized", err)
			}
		})
		t.Run("a v2 correction rejects a recorded worker-lane actor", func(t *testing.T) {
			t.Parallel()
			const workID = "routine-coord-correction-v2-lane"
			s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
			laneRef, currentVersion := seedRecordedWorkerLaneActor(t, s, workID, version)
			event := routineCorrectionEvent("routine-correction-v2-lane", workID, laneRef, currentVersion, targetEventID, targetSeq, targetPayloadVersion, nil)
			err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, currentVersion)})
			failure := routineFailure(t, err)
			if failure.Kind != KindUnauthorized {
				t.Fatalf("v2 correction attributed to a recorded worker-lane actor error = %v, want unauthorized", err)
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
