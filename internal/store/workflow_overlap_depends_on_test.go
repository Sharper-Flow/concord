package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A depends_on resolution records its own authority: the declarer waits and
// the peer proceeds, so the fold accepts it with no approval reference.
// compatible_with and blocks are routine coordination answers that settle
// without consent too. merged_into and supersedes change another item's
// admission or identity, and the live writer refuses an unsigned resolution.
func TestWorkflowDomainOverlapDependsOnCarriesOwnAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	resolve := func(s *Store, actor, eventID, kind, fromWorkID, toWorkID, reason string, fromVersion, toVersion int64) error {
		return s.Transact(ctx, func(tx *Transaction) error {
			_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
				EventID: eventID, FromWorkID: fromWorkID, ToWorkID: toWorkID,
				FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
				FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind,
				Reason: reason, Actor: actor, OccurredAt: now,
			})
			return err
		})
	}

	s, actor := seedOverlapProjection(t, "seq-self-left", "seq-self-right", false)
	if err := resolve(s, actor, "seq-self-depends", ResolutionDependsOn, "seq-self-left", "seq-self-right", "agent sequencing", 2, 2); err != nil {
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

	// blocks is a routine coordination answer now: the declarer holds the
	// pair's admission without consent, in its own fixture so no consent
	// control shares its durability.
	sBlocks, blocksActor := seedOverlapProjection(t, "seq-self-blocked", "seq-self-blocker", false)
	if err := resolve(sBlocks, blocksActor, "seq-self-blocks", ResolutionBlocks, "seq-self-blocked", "seq-self-blocker", "agent sequencing", 2, 2); err != nil {
		t.Fatalf("routine blocks resolution: %v", err)
	}
	if err := InspectWorkflowDomainOverlap(ctx, sBlocks, "seq-self-blocked"); err != nil {
		t.Fatalf("blocks declarer must proceed: %v", err)
	}
	err = InspectWorkflowDomainOverlap(ctx, sBlocks, "seq-self-blocker")
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap == nil || failure.DomainOverlap.Overlaps[0].ResolutionKind != ResolutionBlocks || failure.DomainOverlap.Overlaps[0].ResolutionState != "sequenced" {
		t.Fatalf("blocks peer must wait behind the declarer: %v", err)
	}

	// compatible_with is routine too: both sides proceed without consent.
	sCompatible, compatibleActor := seedOverlapProjection(t, "seq-self-compatible-left", "seq-self-compatible-right", false)
	if err := resolve(sCompatible, compatibleActor, "seq-self-compatible", ResolutionCompatibleWith, "seq-self-compatible-left", "seq-self-compatible-right", "agent sequencing", 2, 2); err != nil {
		t.Fatalf("routine compatible_with resolution: %v", err)
	}
	for _, workID := range []string{"seq-self-compatible-left", "seq-self-compatible-right"} {
		if err := InspectWorkflowDomainOverlap(ctx, sCompatible, workID); err != nil {
			t.Fatalf("compatible_with must admit %s: %v", workID, err)
		}
	}

	// merged_into and supersedes keep their live unsigned refusals: no
	// operator approved another item's admission.
	sRefusal, refusalActor := seedOverlapProjection(t, "seq-self-terminal-source", "seq-self-terminal-target", false)
	for _, kind := range []string{ResolutionMergedInto, ResolutionSupersedes} {
		if err := resolve(sRefusal, refusalActor, "seq-self-"+kind, kind, "seq-self-terminal-source", "seq-self-terminal-target", "unapproved authority claim", 2, 2); err == nil {
			t.Fatalf("unapproved %s resolution must refuse: no operator approved another item's admission", kind)
		} else {
			var refusal *Failure
			if !errors.As(err, &refusal) || refusal.Kind != KindInvalidPayload {
				t.Fatalf("unapproved %s refusal kind=%v, want invalid_payload: %v", kind, refusal, err)
			}
		}
	}
}
