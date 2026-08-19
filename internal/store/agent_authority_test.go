package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentAuthorityPersistenceRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `["product-1"]`, ProjectScopeJSON: `["project-1"]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	client, key, err := s.TrustedClientForGrant(ctx, "client-1")
	if err != nil || client.PrincipalRef != "principal-1" || key.KeyID != "key-1" {
		t.Fatalf("trusted client round trip = %#v %#v %v", client, key, err)
	}
	token := []byte("grant-token")
	hash := sha256.Sum256(token)
	if err := s.PersistGrant(ctx, GrantInsert{RecordID: strings.Repeat("a", 64), TokenHash: hash[:], PrincipalRef: client.PrincipalRef, ClientRef: client.ClientRef, SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ClientKeyID: key.KeyID, ManifestDigest: "sha256:" + strings.Repeat("0", 64), CapabilitiesJSON: client.CapabilitiesJSON, ProductScopeJSON: client.ProductScopeJSON, ProjectScopeJSON: client.ProjectScopeJSON, IssuedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2027-01-01T00:00:00Z", ScopeSnapshotJSON: `{}`, CandidateProductsJSON: `["product-1"]`, Nonce: "nonce-000000000001", NonceObservedAt: "2026-01-01T00:00:00Z", NonceExpiresAt: "2026-01-02T00:00:00Z", NoncePruneBefore: "2025-12-31T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	grant, err := s.Grant(ctx, hash[:])
	if err != nil || grant.RecordID != strings.Repeat("a", 64) || grant.UsedCount != 0 {
		t.Fatalf("grant round trip = %#v %v", grant, err)
	}
	if err := s.ConsumeGrant(ctx, hash[:], "client-1", "2026-01-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	grant, err = s.Grant(ctx, hash[:])
	if err != nil || grant.UsedCount != 1 {
		t.Fatalf("consumed grant = %#v %v", grant, err)
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
	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeTrustedClient(ctx, "client-1", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	assertFailureKind("already revoked client", s.RevokeTrustedClient(ctx, "client-1", "2026-01-01T00:00:00Z"), KindProjectionNotFound)
}

func TestTransactionAuthorityRejectsNilTransaction(t *testing.T) {
	_, err := GrantTx(context.Background(), nil, []byte("hash"))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
		t.Fatalf("nil transaction error=%v, want %s", err, KindInvalidOperation)
	}
}
