package store

import (
	"errors"
	"testing"
)

// Removing an edge leaves one retained edge at position 2, not position 1.
// A projection-row count cannot allocate the next relation identity safely.
func retainedCaptureFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	for _, id := range []string{"capture-retired", "capture-target"} {
		seedWork(t, s, id)
	}
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{
		externalRefWorkCreatedEvent("capture-owner-created", "capture-owner", "tracker:retained", ""),
		externalRefMembershipEvent("capture-owner-member", "capture-owner"),
	}, ExpectedVersions: workVersion("capture-owner", 0)}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{
		relationAddedEvent("capture-edge-removed", "implements", "capture-retired", "capture-owner", 2, 3),
		relationRemovedEvent("capture-remove-edge", "implements", "capture-retired", "capture-owner", 3, 4),
		relationAddedEvent("capture-edge-retained", "implements", "capture-target", "capture-owner", 2, 3),
	} {
		if err := applyWorkEvent(t, s, event, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := relationProjectionIDs(t, s); got != "[2]" {
		t.Fatalf("retained relation IDs = %s, want [2]", got)
	}
	hideEffectLog(t, s)
	return s
}

func retainedCaptureOperation() Operation {
	return Operation{Events: []Event{
		externalRefWorkCreatedEvent("capture-new-created", "capture-new", "tracker:retained", "capture-owner"),
		externalRefMembershipEvent("capture-new-member", "capture-new"),
	}, ExpectedVersions: workVersion("capture-new", 0)}
}

func retainedCaptureSnapshot(t *testing.T, s *Store) [8]int64 {
	t.Helper()
	assertWorktreeTablesAbsent(t, s.db)
	var snapshot [8]int64
	for i, table := range []string{"work_items", "relations", "work_projects", "effect_receipts", "workflow_instances", "idempotency_records"} {
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&snapshot[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i, name := range []string{"effect", "relation"} {
		if err := s.db.QueryRow(`SELECT last FROM store_sequences WHERE name=?`, name).Scan(&snapshot[6+i]); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func assertRetainedCapture(t *testing.T, s *Store) {
	t.Helper()
	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT count(*) FROM work_items WHERE id IN ('capture-owner','capture-new')
 AND json_extract(intent_json,'$.external_ref')='tracker:retained' AND terminal_time IS NULL`, 2},
		{`SELECT count(*) FROM relations WHERE id=2 AND work_id_from='capture-target'
 AND work_id_to='capture-owner' AND kind='implements'`, 1},
		{`SELECT count(*) FROM relations r JOIN effect_receipts e ON e.relation_seq=r.id
 WHERE r.id=3 AND r.work_id_from='capture-new' AND r.work_id_to='capture-owner'
 AND r.kind='raised_from' AND r.record_version=1 AND e.event_id='capture-new-created' AND e.kind='work.created'`, 1},
		{`SELECT last FROM store_sequences WHERE name='relation'`, 3},
		{`SELECT version FROM work_items WHERE id='capture-owner'`, 2},
	} {
		var got int
		if err := s.db.QueryRow(check.query).Scan(&got); err != nil || got != check.want {
			t.Fatalf("capture assertion %s = %d, want %d; err=%v", check.query, got, check.want, err)
		}
	}
	assertFoldGuardEmpty(t, s)
}

func TestAcknowledgedCaptureAllocatesAfterRetainedRelationGap(t *testing.T) {
	s := retainedCaptureFixture(t)
	if err := ApplyOperation(t.Context(), s, retainedCaptureOperation()); err != nil {
		t.Fatalf("valid acknowledged capture with retained relations: %v", err)
	}
	assertRetainedCapture(t, s)
}

func TestUnacknowledgedCaptureWithRetainedRelationsNamesOwnerWithoutMutation(t *testing.T) {
	s := retainedCaptureFixture(t)
	before := retainedCaptureSnapshot(t, s)
	op := retainedCaptureOperation()
	op.Events[0] = externalRefWorkCreatedEvent("capture-new-created", "capture-new", "tracker:retained", "")
	err := ApplyOperation(t.Context(), s, op)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict || failure.ExternalRefConflict == nil || failure.ExternalRefConflict.ExistingWorkID != "capture-owner" || failure.ExternalRefConflict.ExternalRef != "tracker:retained" || failure.EffectPossible || len(failure.CommittedRefs) != 0 {
		t.Fatalf("unacknowledged capture failure = %+v, err=%v", failure, err)
	}
	if after := retainedCaptureSnapshot(t, s); after != before {
		t.Fatalf("refusal changed retained state or allocation: %v -> %v", before, after)
	}
	assertFoldGuardEmpty(t, s)
	if err := ApplyOperation(t.Context(), s, retainedCaptureOperation()); err != nil {
		t.Fatalf("acknowledging the owner with the same event identities: %v", err)
	}
	assertRetainedCapture(t, s)
}

func TestAcknowledgedCaptureWithRetainedRelationsRollsBackBeforeCommit(t *testing.T) {
	s := retainedCaptureFixture(t)
	before := retainedCaptureSnapshot(t, s)
	injected := errors.New("injected acknowledged capture commit failure")
	reachedCommit := false
	_, err := applyOperationWithResult(t.Context(), s, retainedCaptureOperation(), func() error {
		reachedCommit = true
		return injected
	})
	if !reachedCommit || !errors.Is(err, injected) {
		t.Fatalf("did not reach the post-fold rollback boundary: reached=%t err=%v", reachedCommit, err)
	}
	if after := retainedCaptureSnapshot(t, s); after != before {
		t.Fatalf("failed capture left partial state or allocation: %v -> %v", before, after)
	}
	assertFoldGuardEmpty(t, s)
	if err := ApplyOperation(t.Context(), s, retainedCaptureOperation()); err != nil {
		t.Fatalf("retry after rolled-back work, relation and receipts: %v", err)
	}
	assertRetainedCapture(t, s)
}
