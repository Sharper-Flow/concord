package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkRemovalReconcilesUnknownCommitOutcome(t *testing.T) {
	ctx := t.Context()
	s := openTemp(t)
	seedWork(t, s, "remove-work-1")
	req := removalTestRequest()
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	connector := &restoreFaultConnector{dsn: dataSourceName(s.Path())}
	s.db = sql.OpenDB(connector)
	s.db.SetMaxOpenConns(1)
	key := MutationIdempotencyKey{PrincipalRef: req.Actor, Tool: "concord_work_transition", OperationKind: "remove", IdempotencyKey: req.IdempotencyKey}
	digest := "sha256:" + strings.Repeat("a", 64)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	connector.failCommit.Store(true)
	err := s.TransactDurable(ctx, func(tx *Transaction) error {
		prepared, err := PrepareWorkRemovalTx(ctx, tx, req)
		if err != nil {
			return err
		}
		payload, err := MarshalWorkRemovedPayload(req, prepared.HandoffDigest)
		if err != nil {
			return err
		}
		result, err := ApplyOperationTx(ctx, tx, Operation{
			Events:           []Event{{EventID: "work.removed:" + req.OperationID, Kind: WorkRemoved, SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.Actor, OccurredAt: now, PayloadVersion: 1, Payload: payload}},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion},
		})
		if err != nil {
			return err
		}
		eventIDs, err := json.Marshal(result.EventIDs)
		if err != nil {
			return err
		}
		return InsertMutationIdempotencyTx(ctx, tx, MutationIdempotencyInsert{
			Key: key, CanonicalDigest: digest, OperationID: req.OperationID, ResultEventIDs: string(eventIDs),
			ResultPayload:           `{"changed_refs":[{"entity_kind":"work_item","id":"remove-work-1","version":"3"}]}`,
			ChangedRefs:             `[{"entity_kind":"work_item","id":"remove-work-1","version":"3"}]`,
			AuthorizedScopeSnapshot: `{"work_ids":["remove-work-1"]}`, ObservedAt: now,
		})
	})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.EffectPossible || failure.Op != "durable_transaction" || !strings.Contains(err.Error(), "injected failure after SQLite commit") {
		t.Fatalf("lost acknowledgement did not report possible committed effect: %v", err)
	}
	var remaining int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE id=?`, req.WorkID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("removal did not commit: remaining=%d err=%v", remaining, err)
	}
	retained, found, err := s.LookupMutationIdempotency(ctx, key)
	if err != nil || !found || retained.OperationID != req.OperationID || retained.CanonicalDigest != digest || retained.ResultPayload == "" || retained.AuthorizedScopeSnapshot == "" {
		t.Fatalf("unknown commit lost retained replay binding: record=%+v found=%v err=%v", retained, found, err)
	}
	var receipt WorkRemovalReceipt
	if err := s.TransactDurable(ctx, func(tx *Transaction) error {
		var err error
		receipt, err = PrepareWorkRemovalTx(ctx, tx, req)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !receipt.Replayed || receipt.State != "committed" || receipt.EventID != "work.removed:"+req.OperationID || receipt.HandoffDigest != removalHandoffDigest(req.Handoff) {
		t.Fatalf("same-operation reconciliation did not recover committed receipt: %+v", receipt)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM work_removal_operations WHERE work_id=?`, req.WorkID).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("reconciliation duplicated removal: receipts=%d err=%v", remaining, err)
	}
}
