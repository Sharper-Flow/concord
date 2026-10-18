package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// An approval or challenge reference with no row is not found; any other read
// error is a store fault the caller may retry, never an absent record.
func TestApprovalReadsSeparateAMissingRowFromAReadFault(t *testing.T) {
	t.Parallel()
	ref := strings.Repeat("ab", 32)
	identity := ChallengeIdentity{ClientRef: "client-1", SessionRef: "session-1", AgentRef: "agent-1", Worktree: "/repo/worktree"}
	for _, read := range []struct {
		name string
		op   string
		call func(context.Context, *Transaction) error
	}{
		{"approval", "agent_approval_read", func(ctx context.Context, tx *Transaction) error {
			_, err := ReadApprovalTx(ctx, tx, ref)
			return err
		}},
		{"challenge for caller", "agent_challenge_read", func(ctx context.Context, tx *Transaction) error {
			_, err := ReadApprovalChallengeTx(ctx, tx, ref, identity)
			return err
		}},
		{"challenge by reference", "agent_challenge_read", func(ctx context.Context, tx *Transaction) error {
			_, err := ReadApprovalChallengeRefTx(ctx, tx, ref)
			return err
		}},
	} {
		t.Run(read.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			var missing, fault error
			if err := s.Transact(ctx, func(transaction *Transaction) error {
				missing = read.call(ctx, transaction)
				fault = read.call(cancelled, transaction)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var failure *Failure
			if !errors.As(missing, &failure) || failure.Kind != KindProjectionNotFound || failure.Op != read.op {
				t.Fatalf("missing row error = %#v, want %s from %s", missing, KindProjectionNotFound, read.op)
			}
			failure = nil
			if !errors.As(fault, &failure) || failure.Kind != KindUnavailable || !failure.RetrySafe || failure.Op != read.op || !errors.Is(fault, context.Canceled) {
				t.Fatalf("read fault error = %#v, want retryable %s from %s wrapping context.Canceled", fault, KindUnavailable, read.op)
			}
		})
	}
}

// The consumed-approval authority read keeps the same split for both of its
// reads: the approval row and the challenge status it names.
func TestReadApprovalAuthorityTxSeparatesAMissingRowFromAReadFault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const issuedAt = "2026-08-09T00:00:00Z"
	approvalRef, challengeRef, orphanRef := strings.Repeat("a1", 32), strings.Repeat("c1", 32), strings.Repeat("d1", 32)
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, issuedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		if err := InsertApprovalChallengeTx(ctx, transaction, ApprovalChallengeRecord{ChallengeRef: challengeRef, ClientRef: "client-1", PrincipalRef: "principal-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo/worktree", ProductScopeJSON: `[]`, OperationDigest: "operation-digest", ScopeJSON: `{}`, VersionJSON: `{}`, Consequence: "intent", HostAssertionDigest: "host-assertion-digest", IssuedAt: issuedAt, ExpiresAt: "2026-08-09T01:00:00Z", Status: "active", MaxUses: 1}); err != nil {
			return err
		}
		for ref, evidence := range map[string]string{approvalRef: "approval-challenge:" + challengeRef, orphanRef: "approval-challenge:" + strings.Repeat("e1", 32)} {
			if err := InsertApprovalTx(ctx, transaction, ApprovalInsert{ApprovalRef: ref, OperationDigest: "operation-digest", ScopeJSON: `{}`, VersionJSON: `{}`, Consequence: "intent", HumanPrincipalRef: "principal-1", ClientRef: "client-1", SessionRef: "session-1", IssuedAt: issuedAt, ExpiresAt: "2026-08-09T01:00:00Z", MaxUses: 1, ProtectedEvidenceRef: evidence, ProtectedEvidenceDigest: "sha256:evidence"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	read := func(ctx context.Context, ref string) error {
		var err error
		if txErr := s.Transact(context.Background(), func(transaction *Transaction) error {
			_, err = ReadApprovalAuthorityTx(ctx, transaction, ref)
			return nil
		}); txErr != nil {
			t.Fatal(txErr)
		}
		return err
	}
	assertKind := func(name string, err error, kind FailureKind, retry bool) {
		t.Helper()
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != kind || failure.RetrySafe != retry || failure.Op != "agent_approval_authority" {
			t.Fatalf("%s error = %#v, want %s retry_safe=%v", name, err, kind, retry)
		}
	}
	assertKind("missing approval", read(ctx, strings.Repeat("ab", 32)), KindProjectionNotFound, false)
	assertKind("missing challenge", read(ctx, orphanRef), KindProjectionNotFound, false)
	fault := read(cancelled, approvalRef)
	assertKind("approval read fault", fault, KindUnavailable, true)
	if !errors.Is(fault, context.Canceled) {
		t.Fatalf("approval read fault %v does not wrap context.Canceled", fault)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE agent_approval_challenges RENAME COLUMN status TO status_unreadable`); err != nil {
		t.Fatal(err)
	}
	assertKind("challenge status read fault", read(ctx, approvalRef), KindUnavailable, true)
}
