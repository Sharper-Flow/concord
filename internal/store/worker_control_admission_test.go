package store

import (
	"context"
	"errors"
	"testing"
)

func TestNativeWorkerControlAuthorizationRefusesBeforeLeaseOrExecution(t *testing.T) {
	f := newNativeOracleFixture(t)
	request := f.request("denied-control", &f.a)
	denied := errors.New("actual child has no execution authority")
	calls := 0
	request.NativeAuthorization = func(context.Context, *Transaction) error {
		calls++
		return denied
	}
	result, err := f.s.VerifyWorktree(context.Background(), request)
	if !errors.Is(err, denied) || calls != 1 || result.OperationRef != "" {
		t.Fatalf("authorization was not enforced before execution: %+v calls=%d error=%v", result, calls, err)
	}
	var leases int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases WHERE lease_id=?`, request.LeaseID).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("denied execution acquired a lease: %d %v", leases, err)
	}
	f.assertNoLaunch(t)
}

func TestNativeWorkerControlNonceRollsBackWithAdmission(t *testing.T) {
	f := newNativeOracleFixture(t)
	request := f.request("nonce-control", &f.a)
	const client = "client/control"
	const now = "2026-10-17T00:00:00Z"
	if err := f.s.RegisterTrustedClient(context.Background(), TrustedClientRecord{ClientRef: client, PrincipalRef: "principal/operator", Status: "active", CapabilitiesJSON: `["work_transition"]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}, TrustedClientKeyRecord{ClientRef: client, KeyID: "control-key", PublicKey: make([]byte, 32), Status: "active"}, now); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("reject after nonce validation")
	request.NativeAuthorization = func(ctx context.Context, tx *Transaction) error {
		if err := PruneAndRecordNonceTx(ctx, tx, client, "control-nonce-rollback", now, "2026-10-17T01:00:00Z", now); err != nil {
			return err
		}
		return denied
	}
	if _, err := f.s.VerifyWorktree(context.Background(), request); !errors.Is(err, denied) {
		t.Fatalf("admission did not reach and roll back the signed nonce: %v", err)
	}
	var nonces int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM agent_nonce_replay WHERE client_ref=?`, client).Scan(&nonces); err != nil || nonces != 0 {
		t.Fatalf("failed admission consumed a nonce: %d %v", nonces, err)
	}
	f.assertNoLaunch(t)
	request.NativeAuthorization = func(ctx context.Context, tx *Transaction) error {
		return PruneAndRecordNonceTx(ctx, tx, client, "control-nonce-rollback", now, "2026-10-17T01:00:00Z", now)
	}
	if result, err := f.s.VerifyWorktree(context.Background(), request); err != nil || result.Oracle == nil || result.Oracle.Qualification != "pass" {
		t.Fatalf("a rolled-back nonce could not acquire its lease: %+v %v", result.Oracle, err)
	}
	var committed bool
	if err := f.s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_nonce_replay WHERE client_ref=? AND nonce='control-nonce-rollback') AND EXISTS(SELECT 1 FROM worktree_verify_leases WHERE lease_id=? AND state='released')`, client, request.LeaseID).Scan(&committed); err != nil || !committed {
		t.Fatalf("nonce and native lease did not both commit: %v %v", committed, err)
	}
}

func TestNativeWorkerControlReplayRequiresCurrentAuthorization(t *testing.T) {
	f := newNativeOracleFixture(t)
	request := f.request("replayed-control", &f.a)
	calls := 0
	request.NativeAuthorization = func(context.Context, *Transaction) error {
		calls++
		return nil
	}
	original, err := f.s.VerifyWorktree(context.Background(), request)
	if err != nil || calls != 1 || f.launches != 1 {
		t.Fatalf("authorized original execution failed: %+v calls=%d launches=%d error=%v", original, calls, f.launches, err)
	}
	denied := errors.New("current child authority revoked")
	request.NativeAuthorization = func(context.Context, *Transaction) error {
		calls++
		return denied
	}
	result, err := f.s.VerifyWorktree(context.Background(), request)
	if !errors.Is(err, denied) || calls != 2 || f.launches != 1 || result.OperationRef != "" {
		t.Fatalf("replay bypassed current authorization or relaunched: %+v calls=%d launches=%d error=%v", result, calls, f.launches, err)
	}
	request.NativeAuthorization = func(context.Context, *Transaction) error {
		calls++
		return nil
	}
	replayed, err := f.s.VerifyWorktree(context.Background(), request)
	if err != nil || calls != 3 || f.launches != 1 || replayed.OperationRef != original.OperationRef {
		t.Fatalf("authorized replay did not return the retained result: %+v calls=%d launches=%d error=%v", replayed, calls, f.launches, err)
	}
}

func TestNativeWorkerControlAttributesExecutionToAuthenticatedChild(t *testing.T) {
	f := newNativeOracleFixture(t)
	request := f.request("actual-control-child", &f.a)
	request.PrincipalRef = "principal/actual-child"
	request.Owner = SessionWorktreeOwner{ClientRef: "client/actual-child", AgentRef: "agent/actual-child", SessionRef: "session/actual-child"}
	request.NativeAuthorization = func(context.Context, *Transaction) error { return nil }
	result, err := f.s.VerifyWorktree(context.Background(), request)
	if err != nil || result.Oracle == nil || result.Oracle.Qualification != "pass" || f.launches != 1 {
		t.Fatalf("authenticated child could not use the existing in-flight native producer: %+v %v", result, err)
	}
	var principal, client, agent, session string
	if err := f.s.db.QueryRow(`SELECT principal_ref,client_ref,agent_ref,session_ref FROM worktree_verify_leases WHERE lease_id=?`, request.LeaseID).Scan(&principal, &client, &agent, &session); err != nil {
		t.Fatal(err)
	}
	if principal != request.PrincipalRef || client != request.Owner.ClientRef || agent != request.Owner.AgentRef || session != request.Owner.SessionRef {
		t.Fatalf("native execution borrowed another actor: %s %s %s %s", principal, client, agent, session)
	}
	var dispatches int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=? AND subject_id=?`, WorkerDispatched, request.WorkID).Scan(&dispatches); err != nil || dispatches != 0 {
		t.Fatalf("native execution manufactured terminal dispatch evidence: %d %v", dispatches, err)
	}
}
