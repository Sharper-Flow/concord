package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestTrustedClientWithKeyRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `["product-1"]`, ProjectScopeJSON: `["project-1"]`, AgentScopeJSON: `["agent-1"]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	client, key, err := s.TrustedClientWithKey(ctx, "client-1")
	if err != nil || client.PrincipalRef != "principal-1" || key.KeyID != "key-1" {
		t.Fatalf("trusted client round trip = %#v %#v %v", client, key, err)
	}
}

func TestRevokeTrustedClientRequiresExactlyOneActiveRow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertFailureKind := func(label string, err error, want FailureKind) {
		t.Helper()
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != want {
			t.Fatalf("%s error=%v, want %s", label, err, want)
		}
	}
	assertFailureKind("missing client", s.RevokeTrustedClient(ctx, "missing", "2026-01-01T00:00:00Z"), KindProjectionNotFound)
	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeTrustedClient(ctx, "client-1", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	assertFailureKind("already revoked client", s.RevokeTrustedClient(ctx, "client-1", "2026-01-01T00:00:00Z"), KindProjectionNotFound)
}

func TestTransactionAuthorityRejectsNilTransaction(t *testing.T) {
	_, _, err := TrustedClientWithKeyTx(context.Background(), nil, "client-1")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
		t.Fatalf("nil transaction error=%v, want %s", err, KindInvalidOperation)
	}
}

// TestAuthorityTablesCarryNoIntegrityTrigger pins CD-0071 D3: the agent_*
// tables carry no trigger, and none will be added on a tamper argument.
//
// The schema installs 200-odd guards over projection, archived-work, workflow,
// and domain_events rows. None stands over authority tables because a writer
// that can change an authority row can also drop a guard in the same session.
//
// This test exists so the next person to reach for a trigger here has to read
// that decision first. If it fails, CD-0071 D3 is being changed, and D5 governs
// what a real process boundary would require.
func TestAuthorityTablesCarryNoIntegrityTrigger(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, table := range []string{
		"agent_clients", "agent_client_keys", "agent_approvals",
		"agent_approval_challenges", "agent_nonce_replay", "agent_installation_keys",
	} {
		var tables int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&tables); err != nil {
			t.Fatalf("%s lookup: %v", table, err)
		}
		if tables != 1 {
			t.Fatalf("%s is absent; CD-0071 D3 names it and the name must stay accurate", table)
		}
		var triggers int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND tbl_name=?`, table).Scan(&triggers); err != nil {
			t.Fatalf("%s trigger lookup: %v", table, err)
		}
		if triggers != 0 {
			t.Fatalf("%s carries %d trigger(s); CD-0071 D3 accepts absent tamper-evidence on the authority tables, and D5 governs a real process boundary", table, triggers)
		}
	}
}

func TestMutateTrustedClientPolicyReadsCurrentAndWritesResultAtomically(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `["product-1"]`, ProjectScopeJSON: `["project-1"]`, AgentScopeJSON: `["agent-1"]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	sawCurrent := ""
	if err := s.MutateTrustedClientPolicy(ctx, "client-1", func(current TrustedClientRecord) (TrustedClientRecord, error) {
		sawCurrent = current.CapabilitiesJSON
		next := current
		next.CapabilitiesJSON = `["cross_scope","product_read"]`
		return next, nil
	}); err != nil {
		t.Fatal(err)
	}
	if sawCurrent != `["product_read"]` {
		t.Fatalf("mutate saw capabilities %q, want the stored array", sawCurrent)
	}
	client, _, err := s.TrustedClientWithKey(ctx, "client-1")
	if err != nil {
		t.Fatal(err)
	}
	if client.CapabilitiesJSON != `["cross_scope","product_read"]` || client.ProductScopeJSON != `["product-1"]` || client.PrincipalRef != "principal-1" {
		t.Fatalf("stored record after mutate = %#v", client)
	}
}

func TestMutateTrustedClientPolicyAbortsAndKeepsStoredPolicyOnMutateError(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `["product-1"]`, ProjectScopeJSON: `["project-1"]`, AgentScopeJSON: `["agent-1"]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	refusal := &Failure{Kind: KindInvalidOperation, Op: "agent_expand_policy", Detail: "stored capabilities policy is unreadable", RecoveryAction: "restate the full policy with client-policy-update"}
	err = s.MutateTrustedClientPolicy(ctx, "client-1", func(current TrustedClientRecord) (TrustedClientRecord, error) {
		return TrustedClientRecord{}, refusal
	})
	if err == nil {
		t.Fatal("mutate error was swallowed")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure != refusal {
		t.Fatalf("mutate error = %v, want the caller's typed failure", err)
	}
	client, _, readErr := s.TrustedClientWithKey(ctx, "client-1")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if client.CapabilitiesJSON != `["product_read"]` || client.ProductScopeJSON != `["product-1"]` {
		t.Fatalf("stored record after aborted mutate = %#v", client)
	}
}

func TestMutateTrustedClientPolicyRefusesUnknownAndRevokedClientsTyped(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertFailureKind := func(label string, err error) {
		t.Helper()
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindProjectionNotFound {
			t.Fatalf("%s error=%v, want %s", label, err, KindProjectionNotFound)
		}
	}
	assertFailureKind("missing client", s.MutateTrustedClientPolicy(ctx, "missing", func(current TrustedClientRecord) (TrustedClientRecord, error) { return current, nil }))
	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeTrustedClient(ctx, "client-1", "2026-01-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	assertFailureKind("revoked client", s.MutateTrustedClientPolicy(ctx, "client-1", func(current TrustedClientRecord) (TrustedClientRecord, error) { return current, nil }))
}
