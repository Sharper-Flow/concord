package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A depends_on resolution records its own authority: the declarer waits and
// the peer proceeds, so the fold accepts it with no approval reference.
// Every other kind changes another item's admission or identity and the
// fold refuses an unapproved resolution.
func TestWorkflowDomainOverlapDependsOnCarriesOwnAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, actor := seedOverlapProjection(t, "seq-self-left", "seq-self-right", false)
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	resolve := func(eventID, kind, approvalRef string, fromVersion, toVersion int64) error {
		return s.Transact(ctx, func(tx *Transaction) error {
			_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
				EventID: eventID, FromWorkID: "seq-self-left", ToWorkID: "seq-self-right",
				FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
				FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind,
				Reason: "agent sequencing", ApprovalRef: approvalRef, Actor: actor, OccurredAt: now,
			})
			return err
		})
	}
	if err := resolve("seq-self-depends", ResolutionDependsOn, "", 2, 2); err != nil {
		t.Fatalf("unapproved depends_on resolution: %v", err)
	}
	if err := InspectWorkflowDomainOverlap(ctx, s, "seq-self-right"); err != nil {
		t.Fatalf("peer must proceed: %v", err)
	}
	err := InspectWorkflowDomainOverlap(ctx, s, "seq-self-left")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap == nil {
		t.Fatalf("declarer must be sequenced behind the peer: %v", err)
	}

	s2, actor2 := seedOverlapProjection(t, "seq-self-blocked", "seq-self-blocker", false)
	blocked := func(eventID, kind string, fromVersion, toVersion int64) error {
		return s2.Transact(ctx, func(tx *Transaction) error {
			_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
				EventID: eventID, FromWorkID: "seq-self-blocked", ToWorkID: "seq-self-blocker",
				FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
				FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind,
				Reason: "unapproved authority claim", ApprovalRef: "", Actor: actor2, OccurredAt: now,
			})
			return err
		})
	}
	for _, kind := range []string{ResolutionBlocks, ResolutionCompatibleWith, ResolutionMergedInto, ResolutionSupersedes} {
		if err := blocked("seq-self-"+kind, kind, 2, 2); err == nil {
			t.Fatalf("unapproved %s resolution must refuse: no operator approved another item's admission", kind)
		} else {
			var refusal *Failure
			if !errors.As(err, &refusal) || refusal.Kind != KindInvalidPayload {
				t.Fatalf("unapproved %s refusal kind=%v, want invalid_payload: %v", kind, refusal, err)
			}
		}
	}
}
