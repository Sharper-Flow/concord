package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestOverlapResolutionVersionConflictNamesTheStaleEndpoint holds the identity
// of a two-sided optimistic-concurrency refusal. resolve_overlap pins a version
// for each endpoint, and the refusal must name the endpoint whose pin is stale
// together with that endpoint's live version. A refusal that pairs one work
// item's id with the other's versions sends the caller to re-read an item that
// is already current, and the caller cannot correct the request.
//
// The fixture puts the two endpoints at different live versions on purpose.
// The fold reads the event subject for one side and the payload's to_work_id
// for the other, so endpoints that share a live version hide a mispairing: the
// wrong id still reports a number that matches. The distinctness assertion
// below keeps a later fixture change from flattening the proof.
func TestOverlapResolutionVersionConflictNamesTheStaleEndpoint(t *testing.T) {
	const left, right = "conflict-left", "conflict-right"
	for _, stale := range []string{"to", "from"} {
		t.Run(stale, func(t *testing.T) {
			ctx := context.Background()
			s, actor := seedOverlapProjection(t, left, right, true)

			// Advance the right endpoint alone so the two live versions differ.
			// An intent revision carries no lifecycle change, so the unresolved
			// overlap this fixture exists to create does not refuse it.
			revision := []byte(`{"title":"Revised","value_statement":"Raise this endpoint's version alone","kind":"task","priority":4,"tags":[],"reason":"separate the endpoint versions","expected_version":2,"resulting_version":3}`)
			if err := ApplyOperation(ctx, s, Operation{
				Events:           []Event{{EventID: "conflict-right-revised", Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: right, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: revision}},
				ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, right): 2},
			}); err != nil {
				t.Fatalf("cannot advance %s: %v", right, err)
			}
			leftLive, rightLive := liveWorkVersion(t, s, left), liveWorkVersion(t, s, right)
			if leftLive == rightLive {
				t.Fatalf("both endpoints are at version %d; the fixture cannot expose a mispaired id", leftLive)
			}

			fromExpected, toExpected := leftLive, rightLive
			wantID, wantLive, otherID := right, rightLive, left
			if stale == "to" {
				toExpected = rightLive + 5
			} else {
				fromExpected = leftLive + 5
				wantID, wantLive, otherID = left, leftLive, right
			}

			err := s.Transact(ctx, func(tx *Transaction) error {
				_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
					EventID: "overlap-conflict-" + stale, FromWorkID: left, ToWorkID: right,
					FromExpectedVersion: fromExpected, ToExpectedVersion: toExpected,
					FromContractVersion: 1, ToContractVersion: 1,
					ResolutionKind: ResolutionCompatibleWith, Reason: "identity of the refusal",
					ApprovalRef: "approval:overlap-conflict", Actor: actor,
					OccurredAt: time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC),
				})
				return err
			})

			var failure *Failure
			if !errors.As(err, &failure) {
				t.Fatalf("a stale %s pin must refuse, got %v", stale, err)
			}
			if failure.Kind != KindVersionConflict {
				t.Fatalf("kind %q, want %q: %s", failure.Kind, KindVersionConflict, failure.Detail)
			}
			if !strings.Contains(failure.Detail, wantID) {
				t.Errorf("detail %q does not name %s, the endpoint whose pin is stale", failure.Detail, wantID)
			}
			if strings.Contains(failure.Detail, otherID) {
				t.Errorf("detail %q names %s, which is not the stale endpoint", failure.Detail, otherID)
			}
			if !strings.Contains(failure.Detail, "has version "+strconv.FormatInt(wantLive, 10)) {
				t.Errorf("detail %q does not carry %s's live version %d", failure.Detail, wantID, wantLive)
			}
			if len(failure.CurrentVersions) != 1 {
				t.Fatalf("current_versions len=%d, want 1", len(failure.CurrentVersions))
			}
			if current := failure.CurrentVersions[0]; current.SubjectID != wantID || current.Version != wantLive {
				t.Errorf("current_versions[0]=%+v, want %s at version %d", current, wantID, wantLive)
			}
		})
	}
}

func liveWorkVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id = ?`, workID).Scan(&version); err != nil {
		t.Fatalf("cannot read %s version: %v", workID, err)
	}
	return version
}
