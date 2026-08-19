package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func testClaim(opID, key string) ClaimRequest {
	return ClaimRequest{OpID: opID, WorkID: "work-1", WorkflowTypeRef: "implementation", WorkflowTypeVersion: 1, StepID: "step-1", StepKind: StepExternalEffect, AcceptedInputsDigest: testDigest("inputs"), AcceptedScopeSnapshot: `{"work_id":"work-1"}`, PrincipalRef: "agent-1", Tool: "execute", IdempotencyKey: key, RequestID: "request-" + key, ObservedAt: time.Unix(1, 0).UTC()}
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestFenceClaimsReplayConflictsAndCompletesIdempotently(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	claim := testClaim("op-1", "claim-1")
	first, err := ClaimStep(ctx, s, claim)
	if err != nil || first.AttemptEpoch != 1 {
		t.Fatalf("first ClaimStep() = %+v, %v", first, err)
	}
	replay, err := ClaimStep(ctx, s, claim)
	if err != nil || !replay.Replayed || replay.AttemptEpoch != first.AttemptEpoch {
		t.Fatalf("claim replay = %+v, %v", replay, err)
	}
	conflict := claim
	conflict.AcceptedInputsDigest = testDigest("different")
	_, err = ClaimStep(ctx, s, conflict)
	assertFailureKind(t, err, KindIdempotencyConflict)

	complete := CompleteRequest{OpID: "op-1", AttemptEpoch: 1, ResultKind: ResultCompleted, ResultPayload: `{"ok":true}`, EvidenceRefs: []string{"e1"}, ChangedRefs: []string{"c1"}, PrincipalRef: "agent-1", Tool: "execute", IdempotencyKey: "complete-1", RequestID: "complete-request", ObservedAt: time.Unix(2, 0).UTC(), ResultEventIDs: []string{"event-1"}}
	done, err := CompleteStep(ctx, s, complete)
	if err != nil || done.ResultKind != ResultCompleted {
		t.Fatalf("CompleteStep() = %+v, %v", done, err)
	}
	doneReplay, err := CompleteStep(ctx, s, complete)
	if err != nil || !doneReplay.Replayed || len(doneReplay.ResultEventIDs) != 1 {
		t.Fatalf("completion replay = %+v, %v", doneReplay, err)
	}
	if got := countRows(t, s, "durable_operations"); got != 1 {
		t.Fatalf("durable operation rows = %d, want 1", got)
	}
}

func TestFenceKeepsShippedSurfaceAndAcceptsCurrentMajor(t *testing.T) {
	for _, version := range []string{"3.8.0", "4.0.0"} {
		t.Run(version, func(t *testing.T) {
			s := openTemp(t)
			claim := testClaim("surface-"+version, "surface-"+version)
			claim.ContractVersion = version
			if _, err := ClaimStep(context.Background(), s, claim); err != nil {
				t.Fatalf("ClaimStep(%s): %v", version, err)
			}
			got, err := Step(context.Background(), s, claim.OpID)
			if err != nil || got.ContractVersion != version {
				t.Fatalf("Step(%s) = %+v, %v", version, got, err)
			}
		})
	}
	claim := testClaim("surface-unshipped-3.9", "surface-unshipped-3.9")
	claim.ContractVersion = "3.9.0"
	if _, err := ClaimStep(context.Background(), openTemp(t), claim); err == nil {
		t.Fatal("unshipped 3.9.0 durable contract was accepted")
	}
}

func TestDurableOperationReplayVectorsMigrateLegacyResultsAndRejectFutureValues(t *testing.T) {
	for _, vector := range []struct {
		name string
		kind ResultKind
	}{
		{name: "legacy success", kind: ResultCompleted},
		{name: "legacy pending", kind: ResultPending},
		{name: "legacy non-success", kind: ResultFailed},
	} {
		t.Run(vector.name, func(t *testing.T) {
			s := openTemp(t)
			claim := testClaim("replay-"+vector.name, "replay-"+vector.name)
			claim.ContractVersion = "1.0.0"
			if _, err := ClaimStep(context.Background(), s, claim); err != nil {
				t.Fatal(err)
			}
			if _, err := CompleteStep(context.Background(), s, CompleteRequest{
				OpID: claim.OpID, AttemptEpoch: 1, ResultKind: vector.kind, ResultPayload: `{"legacy":true},`,
				PrincipalRef: claim.PrincipalRef, Tool: claim.Tool, IdempotencyKey: "complete-" + vector.name,
				RequestID: "complete-" + vector.name, ObservedAt: time.Unix(2, 0).UTC(),
			}); err == nil {
				t.Fatal("malformed legacy result payload unexpectedly completed")
			}
			// The durable result is written through the production completion path;
			// retry with a valid object to exercise the actual replay projection.
			if _, err := CompleteStep(context.Background(), s, CompleteRequest{
				OpID: claim.OpID, AttemptEpoch: 1, ResultKind: vector.kind, ResultPayload: `{"legacy":true}`,
				PrincipalRef: claim.PrincipalRef, Tool: claim.Tool, IdempotencyKey: "complete-valid-" + vector.name,
				RequestID: "complete-valid-" + vector.name, ObservedAt: time.Unix(3, 0).UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			got, err := Step(context.Background(), s, claim.OpID)
			if err != nil || got.ContractVersion != "1.0.0" || got.ResultKind != vector.kind || got.ResultPayload != `{"legacy":true}` {
				t.Fatalf("legacy replay = %+v, %v", got, err)
			}
		})
	}

	t.Run("future contract version", func(t *testing.T) {
		s := openTemp(t)
		claim := testClaim("future-contract", "future-contract")
		if _, err := ClaimStep(context.Background(), s, claim); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`UPDATE durable_operations SET contract_version='9.0.0' WHERE op_id=?`, claim.OpID); err != nil {
			t.Fatal(err)
		}
		_, err := Step(context.Background(), s, claim.OpID)
		assertFailureKind(t, err, KindSchemaUnsupported)
	})

	t.Run("future result classification", func(t *testing.T) {
		s := openTemp(t)
		claim := testClaim("future-result", "future-result")
		if _, err := ClaimStep(context.Background(), s, claim); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`PRAGMA ignore_check_constraints=ON; UPDATE durable_operations SET result_kind='succeeded_with_unknown_semantics' WHERE op_id=?`, claim.OpID); err != nil {
			t.Fatal(err)
		}
		_, err := Step(context.Background(), s, claim.OpID)
		assertFailureKind(t, err, KindSchemaUnsupported)
	})
}

func TestFenceStaleAttemptAndExplicitTakeover(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	claim := testClaim("op-2", "claim-2")
	first, err := ClaimStep(ctx, s, claim)
	if err != nil {
		t.Fatal(err)
	}
	takeoverClaim := testClaim("op-2", "claim-3")
	takeoverClaim.PrincipalRef = "operator"
	takeover, err := OperatorTakeover(ctx, s, takeoverClaim, "approval-1")
	if err != nil || takeover.AttemptEpoch != 2 || takeover.ApprovalRef != "approval-1" {
		t.Fatalf("takeover = %+v, %v", takeover, err)
	}
	stale := CompleteRequest{OpID: "op-2", AttemptEpoch: first.AttemptEpoch, ResultKind: ResultCompleted, PrincipalRef: "agent-1", Tool: "execute", IdempotencyKey: "stale-complete", RequestID: "stale-request", ObservedAt: time.Unix(3, 0).UTC()}
	_, err = CompleteStep(ctx, s, stale)
	assertFailureKind(t, err, KindStaleAttempt)
	current := CompleteRequest{OpID: "op-2", AttemptEpoch: 2, ResultKind: ResultCompleted, PrincipalRef: "operator", Tool: "execute", IdempotencyKey: "current-complete", RequestID: "current-request", ObservedAt: time.Unix(4, 0).UTC()}
	if _, err := CompleteStep(ctx, s, current); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT MAX(attempt_epoch) FROM durable_operations WHERE op_id='op-2'`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != 2 {
		t.Fatalf("Step reads advanced epoch to %d", epoch)
	}
}

func TestFenceCommitHookRollsBackAllWrites(t *testing.T) {
	s := openTemp(t)
	operation := Operation{Events: []Event{
		productCreatedEvent("hook-product", "hook-product-created"), projectCreatedEvent("hook-project", "hook-project-created"),
		membershipEvent("hook-membership", "product_project.added", SubjectProduct, "hook-product", map[string]any{"product_id": "hook-product", "project_id": "hook-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "hook-product"): 0, VersionRef(SubjectProject, "hook-project"): 0}}
	_, err := applyOperationWithResult(context.Background(), s, operation, func() error { return errors.New("commit gate") })
	if err == nil {
		t.Fatal("commit hook unexpectedly succeeded")
	}
	if got := countRows(t, s, "domain_events"); got != 0 {
		t.Fatalf("events after hook rollback = %d", got)
	}
	if got := countRows(t, s, "products"); got != 0 {
		t.Fatalf("products after hook rollback = %d", got)
	}
	if _, err := ApplyOperationWithResult(context.Background(), s, operation); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestBackupVerifyTamperAndOlderSchemaRejection(t *testing.T) {
	s := openTemp(t)
	operation := Operation{Events: []Event{
		productCreatedEvent("backup-product", "backup-product-created"), projectCreatedEvent("backup-project", "backup-project-created"),
		membershipEvent("backup-membership", "product_project.added", SubjectProduct, "backup-product", map[string]any{"product_id": "backup-product", "project_id": "backup-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "backup-product"): 0, VersionRef(SubjectProject, "backup-project"): 0}}
	if _, err := ApplyOperationWithResult(context.Background(), s, operation); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := Backup(context.Background(), s, destination)
	if err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	verified, err := VerifyBackup(context.Background(), destination)
	if err != nil || verified.FileSHA256 != manifest.FileSHA256 || verified.SourceEventMaxSeq != manifest.SourceEventMaxSeq {
		t.Fatalf("VerifyBackup() = %+v, %v", verified, err)
	}
	if _, err := VerifyBackup(context.Background(), destination, CurrentSchemaVersion()-1); err == nil {
		t.Fatal("older supported max accepted v7 snapshot")
	} else {
		assertFailureKind(t, err, KindSchemaUnsupported)
	}
	bytes, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, append(bytes, byte('x')), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(context.Background(), destination); err == nil {
		t.Fatal("tampered backup verified")
	}
}
