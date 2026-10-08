package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestStaleRegistryRescanStrandsCurrentPinDispatchBesideStalePeer reproduces
// the CON-714 shape: a subject whose own approved contract pins the current
// registry hash holds an unresolved Domain overlap with a peer whose pin a
// rescan stranded. The peer's stale pin must not become the subject's
// refusal: the subject's own pin check passes, so the boundary's answer is
// the unresolved-overlap refusal with its four recovery routes, exactly as it
// is for an unresolved overlap beside a current-pinned peer. The stranded
// peer's marker keeps opening the peer's own re-pin route (CD-0041 D7), and a
// recorded resolution admits the subject's dispatch.
func TestStaleRegistryRescanStrandsCurrentPinDispatchBesideStalePeer(t *testing.T) {
	const workID = "stale-registry-current-pin-dispatch"
	const peerID = "stale-registry-current-pin-dispatch-peer"
	ctx := context.Background()
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 19, "repair")
	s, owner, operator := fixture.store, fixture.owner, fixture.operator
	defer s.Close()
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	// The subject's fixture contract writes no Domain. One shared Domain
	// modification on both sides makes the pair a real overlap under
	// CD-0145 D1: the subject's modification rides its active contract v1,
	// the peer's rides the contract seedStaleRegistryRescanPeer inserts.
	execStaleRegistryInFold(t, s, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('`+workID+`',1,'root')`)
	// The rescan drifts the registry before the peer seeds, so both contracts
	// start on the stale hash. The subject then re-pins to the current hash,
	// leaving the strand one-sided: the subject's own pin check passes and
	// only the peer stays stranded.
	driftStaleRegistryFixture(t, s)
	seedStaleRegistryRescanPeer(t, s, peerID, ownerRef)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", registryRepinSuccessorPayload(2, registryRescannedHash(), workID, 1, "root"), owner, operator); err != nil {
		t.Fatalf("subject re-pin to the current hash: %v", err)
	}

	// Each dispatch builds its packet from the state it is admitted at, as
	// the adapter does, so the binding names the current work version.
	dispatch := func(operationID, idempotencyKey string) error {
		packetPayload, err := json.Marshal(dispatchWorkerPacket(t, s, workID, "repair", "attempt-"+workID))
		if err != nil {
			t.Fatalf("marshal dispatch packet: %v", err)
		}
		fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": "attempt-" + workID, "worker_packet": json.RawMessage(packetPayload)})
		if err != nil {
			t.Fatalf("marshal dispatch fields: %v", err)
		}
		_, invokeErr := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: verdictItemVersion(t, s, workID), ActionID: "dispatch_worker",
			Payload: fieldsPayload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
			Actor: owner, AcceptedInputsDigest: cd0059TestDigest(t, "peer-stale-inputs"),
			IdempotencyIdentity: operationID, OperationID: "op-" + operationID, PrincipalRef: owner.PrincipalRef,
			Tool: "concord_work_transition", IdempotencyKey: idempotencyKey, RequestID: "req-" + operationID,
			AcceptedScope: `{}`, ContractDigest: testManifestDigest,
		})
		return invokeErr
	}

	// The subject's dispatch_worker refuses on the unresolved overlap, never
	// on the peer's stale pin: the refusal names the pair and carries the
	// four closed recovery routes, the same answer an unresolved overlap
	// beside a current-pinned peer produces.
	err = dispatch("peer-stale", "peer-stale-key")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap == nil {
		t.Fatalf("dispatch_worker beside a stale peer pin error=%v, want the unresolved-overlap refusal", err)
	}
	if len(failure.DomainOverlap.Overlaps) != 1 || failure.DomainOverlap.Overlaps[0].ToWorkID != peerID {
		t.Fatalf("overlap refusal = %#v, want one overlap naming the stranded peer", failure.DomainOverlap)
	}
	var recovery []string
	for _, route := range failure.DomainOverlap.Overlaps {
		recovery = route.RecoveryActions
	}
	if len(recovery) == 0 || recovery[0] != "wait" {
		t.Fatalf("overlap recovery actions = %v, want the closed route list", recovery)
	}

	// The subject's own pin is current, so the stale-pin marker never names
	// the subject: the recorded marker belongs to the peer's own boundary.
	if failure.StaleDomainRegistryPin != nil && failure.StaleDomainRegistryPin.WorkID == workID {
		t.Fatalf("overlap refusal carries a stale-pin marker naming the subject: %#v", failure.StaleDomainRegistryPin)
	}

	// resolve_overlap is the D7-exempt recovery route and the declarer's own
	// pin is current, so the operator clears the pair by recording a
	// resolution that names the stranded peer.
	subjectVersion := verdictItemVersion(t, s, workID)
	peerVersion := readWorkVersion(t, s, peerID)
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		_, txErr := ResolveWorkflowDomainOverlapTx(ctx, transaction, WorkflowDomainOverlapResolutionRequest{
			EventID: "stale-peer-resolution-" + workID, FromWorkID: workID, ToWorkID: peerID,
			FromExpectedVersion: subjectVersion, ToExpectedVersion: peerVersion,
			FromContractVersion: 2, ToContractVersion: 1, ResolutionKind: ResolutionCompatibleWith,
			Reason: "the delivered repair is independent of the peer's stranded claim", ApprovalRef: "approval:stale-registry-current-pin-dispatch",
			Actor: owner.PrincipalRef, OccurredAt: time.Unix(40, 0).UTC(),
		})
		return txErr
	}); err != nil {
		t.Fatalf("resolve_overlap beside a peer whose pin is stale: %v", err)
	}

	// The recorded resolution is evaluated before the peer pin check, so the
	// freshly approved current-hash contract reaches execution.
	if err := dispatch("peer-stale-2", "peer-stale-key-2"); err != nil {
		t.Fatalf("dispatch_worker under the recorded resolution: %v", err)
	}
	var windows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker'`, workID, WorkflowActionStarted).Scan(&windows); err != nil {
		t.Fatal(err)
	}
	if windows < 1 {
		t.Fatalf("dispatch windows after admission = %d, want >= 1", windows)
	}
}
