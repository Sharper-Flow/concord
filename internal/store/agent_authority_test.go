package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedClientWithKeyRoundTrip(t *testing.T) {
	t.Parallel()
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

func TestTrustedClientInspectionAndStrictReaders(t *testing.T) {
	for _, state := range []string{"active", "keyless", "revoked", "revoked-active-key", "unknown"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			client := TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`, PolicyRevision: 1}
			key := TrustedClientKeyRecord{ClientRef: client.ClientRef, KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}
			if err := s.RegisterTrustedClient(ctx, client, key, "2026-01-01T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			ref := client.ClientRef
			switch state {
			case "keyless":
				if _, err := s.db.Exec(`UPDATE agent_client_keys SET status='revoked', revoked_at='now' WHERE client_ref=?`, ref); err != nil {
					t.Fatal(err)
				}
			case "revoked", "revoked-active-key":
				if err := s.RevokeTrustedClient(ctx, ref, "2026-01-02T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
				client.Status = "revoked"
				if state == "revoked-active-key" {
					if _, err := s.db.Exec(`UPDATE agent_client_keys SET status='active', revoked_at=NULL WHERE client_ref=?`, ref); err != nil {
						t.Fatal(err)
					}
				}
			case "unknown":
				ref = "missing"
			}
			checkFailure := func(err error) {
				t.Helper()
				var failure *Failure
				if !errors.As(err, &failure) || failure.Kind != KindProjectionNotFound || failure.Op != "agent_client_read" || failure.Detail != "unknown or keyless client" {
					t.Fatalf("failure=%v, want unchanged typed keyed refusal", err)
				}
			}
			got, err := s.TrustedClient(ctx, ref)
			if state == "unknown" {
				checkFailure(err)
			} else if err != nil || got != client {
				t.Fatalf("inspection=%+v err=%v want=%+v", got, err, client)
			}
			checkKeyed := func(got TrustedClientRecord, gotKey TrustedClientKeyRecord, err error) {
				t.Helper()
				if state != "active" {
					checkFailure(err)
					return
				}
				if err != nil || got != client || gotKey.KeyID != key.KeyID || gotKey.Status != "active" {
					t.Fatalf("strict=%+v key=%+v err=%v", got, gotKey, err)
				}
			}
			checkKeyed(s.TrustedClientWithKey(ctx, ref))
			if err := s.Transact(ctx, func(tx *Transaction) error {
				checkKeyed(TrustedClientWithKeyTx(ctx, tx, ref))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTrustedClientQueryFailuresAreUnavailable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, read := range []func() error{
		func() error { _, err := s.TrustedClient(ctx, "client-1"); return err },
		func() error { _, _, err := s.TrustedClientWithKey(ctx, "client-1"); return err },
	} {
		var failure *Failure
		if err := read(); !errors.As(err, &failure) || failure.Kind != KindUnavailable {
			t.Fatalf("query error=%v, want unavailable", err)
		}
	}
}

func TestRevokeTrustedClientRequiresExactlyOneActiveRow(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestRearmApprovalChallengeTx pins the guarded deadline CAS behind the
// pending-challenge re-arm policy: only an active, unused, expired single-use
// challenge under the exact caller identity and old deadline moves, only its
// expiry changes, and the primitive never creates consent. Any guard mismatch
// or a transaction that later fails leaves the stored row unchanged.
func TestRearmApprovalChallengeTx(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const (
		issuedAt  = "2026-01-01T00:00:00Z"
		expiredAt = "2026-01-01T00:10:00Z"
		liveAt    = "2026-01-01T00:30:00Z"
		rearmNow  = "2026-01-01T00:20:00Z"
		rearmTo   = "2026-01-01T00:30:00Z"
		// RFC3339Nano stamps with optional fractional seconds are not
		// chronologically ordered as bare TEXT, so the boundary cases pin
		// nanosecond-adjacent deadlines against an integer-second now.
		rearmNowPlus1ns  = "2026-01-01T00:20:00.000000001Z"
		rearmNowMinus1ns = "2026-01-01T00:19:59.999999999Z"
	)
	ref := strings.Repeat("a", 64)
	identity := ChallengeIdentity{ClientRef: "client-1", SessionRef: "session-1", AgentRef: "agent-1", Worktree: "/repo/worktree"}
	challenge := func(status, expiresAt string, usedCount int) ApprovalChallengeRecord {
		return ApprovalChallengeRecord{
			ChallengeRef: ref, ClientRef: "client-1", PrincipalRef: "principal-1",
			SessionRef: "session-1", AgentRef: "agent-1",
			Directory: "/repo", Worktree: "/repo/worktree",
			ProductScopeJSON: `["product-1"]`, OperationDigest: "operation-digest",
			ScopeJSON: `{"scope":"one"}`, VersionJSON: `{"version":"one"}`,
			Consequence: "intent", HostAssertionDigest: "host-assertion-digest",
			IssuedAt: issuedAt, ExpiresAt: expiresAt, Status: status,
			MaxUses: 1, UsedCount: usedCount,
		}
	}
	seed := func(t *testing.T, row ApprovalChallengeRecord) *Store {
		t.Helper()
		s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, issuedAt); err != nil {
			t.Fatal(err)
		}
		if err := s.Transact(ctx, func(transaction *Transaction) error {
			return InsertApprovalChallengeTx(ctx, transaction, row)
		}); err != nil {
			t.Fatal(err)
		}
		if row.Status == "consumed" {
			if _, err := s.db.Exec(`UPDATE agent_approval_challenges SET consumed_at=? WHERE challenge_ref=?`, rearmNow, ref); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	readRow := func(t *testing.T, s *Store) ApprovalChallengeRecord {
		t.Helper()
		var got ApprovalChallengeRecord
		if err := s.Transact(ctx, func(transaction *Transaction) error {
			var err error
			got, err = ReadApprovalChallengeTx(ctx, transaction, ref, identity)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	assertNoConsent := func(t *testing.T, s *Store) {
		t.Helper()
		var approvals int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_approvals`).Scan(&approvals); err != nil {
			t.Fatal(err)
		}
		if approvals != 0 {
			t.Fatalf("re-arm left %d approval row(s); the primitive must not create consent", approvals)
		}
	}
	assertRefusal := func(t *testing.T, err error) {
		t.Helper()
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict || failure.Op != "agent_challenge_rearm" {
			t.Fatalf("re-arm error=%v, want typed projection_conflict from agent_challenge_rearm", err)
		}
	}
	assertInvalidTime := func(t *testing.T, err error) {
		t.Helper()
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Op != "agent_challenge_rearm" {
			t.Fatalf("malformed deadline error=%v, want typed invalid_operation from agent_challenge_rearm", err)
		}
	}
	assertUnchanged := func(t *testing.T, s *Store, want ApprovalChallengeRecord) {
		t.Helper()
		if got := readRow(t, s); got != want {
			t.Fatalf("stored challenge = %+v, want the untouched row %+v", got, want)
		}
		assertNoConsent(t, s)
	}
	rearm := func(s *Store, oldExpiresAt, newExpiresAt string, caller ChallengeIdentity) error {
		return s.TransactDurable(ctx, func(transaction *Transaction) error {
			return RearmApprovalChallengeTx(ctx, transaction, ref, caller, oldExpiresAt, newExpiresAt, rearmNow)
		})
	}

	t.Run("extends only the expiry of one expired unused challenge", func(t *testing.T) {
		s := seed(t, challenge("active", expiredAt, 0))
		if err := rearm(s, expiredAt, rearmTo, identity); err != nil {
			t.Fatal(err)
		}
		if got := readRow(t, s); got != challenge("active", rearmTo, 0) {
			t.Fatalf("stored challenge after re-arm = %+v, want the same row with expiry %s", got, rearmTo)
		}
		assertNoConsent(t, s)
	})
	t.Run("refuses a stale old-deadline compare-and-set", func(t *testing.T) {
		s := seed(t, challenge("active", expiredAt, 0))
		assertRefusal(t, rearm(s, "2026-01-01T00:09:59Z", rearmTo, identity))
		assertUnchanged(t, s, challenge("active", expiredAt, 0))
	})
	t.Run("refuses a wrong caller identity", func(t *testing.T) {
		s := seed(t, challenge("active", expiredAt, 0))
		other := identity
		other.SessionRef = "session-other"
		assertRefusal(t, rearm(s, expiredAt, rearmTo, other))
		assertUnchanged(t, s, challenge("active", expiredAt, 0))
	})
	t.Run("refuses a consumed challenge", func(t *testing.T) {
		s := seed(t, challenge("consumed", expiredAt, 1))
		assertRefusal(t, rearm(s, expiredAt, rearmTo, identity))
		assertUnchanged(t, s, challenge("consumed", expiredAt, 1))
	})
	t.Run("refuses a revoked challenge", func(t *testing.T) {
		s := seed(t, challenge("revoked", expiredAt, 0))
		assertRefusal(t, rearm(s, expiredAt, rearmTo, identity))
		assertUnchanged(t, s, challenge("revoked", expiredAt, 0))
	})
	t.Run("refuses a used challenge", func(t *testing.T) {
		s := seed(t, challenge("active", expiredAt, 1))
		assertRefusal(t, rearm(s, expiredAt, rearmTo, identity))
		assertUnchanged(t, s, challenge("active", expiredAt, 1))
	})
	t.Run("refuses a live deadline", func(t *testing.T) {
		s := seed(t, challenge("active", liveAt, 0))
		assertRefusal(t, rearm(s, liveAt, "2026-01-01T00:40:00Z", identity))
		assertUnchanged(t, s, challenge("active", liveAt, 0))
	})
	t.Run("refuses a replacement deadline not later than now", func(t *testing.T) {
		s := seed(t, challenge("active", expiredAt, 0))
		assertRefusal(t, rearm(s, expiredAt, rearmNow, identity))
		assertUnchanged(t, s, challenge("active", expiredAt, 0))
	})
	t.Run("accepts an old deadline exactly at now", func(t *testing.T) {
		s := seed(t, challenge("active", rearmNow, 0))
		if err := rearm(s, rearmNow, rearmTo, identity); err != nil {
			t.Fatal(err)
		}
		if got := readRow(t, s); got != challenge("active", rearmTo, 0) {
			t.Fatalf("stored challenge after re-arm = %+v, want the same row with expiry %s", got, rearmTo)
		}
		assertNoConsent(t, s)
	})
	t.Run("refuses an old deadline one nanosecond after now", func(t *testing.T) {
		s := seed(t, challenge("active", rearmNowPlus1ns, 0))
		assertRefusal(t, rearm(s, rearmNowPlus1ns, rearmTo, identity))
		assertUnchanged(t, s, challenge("active", rearmNowPlus1ns, 0))
	})
	t.Run("accepts an old deadline one nanosecond before now", func(t *testing.T) {
		s := seed(t, challenge("active", rearmNowMinus1ns, 0))
		if err := rearm(s, rearmNowMinus1ns, rearmTo, identity); err != nil {
			t.Fatal(err)
		}
		if got := readRow(t, s); got != challenge("active", rearmTo, 0) {
			t.Fatalf("stored challenge after re-arm = %+v, want the same row with expiry %s", got, rearmTo)
		}
		assertNoConsent(t, s)
	})
	t.Run("accepts a replacement deadline one nanosecond after now", func(t *testing.T) {
		s := seed(t, challenge("active", expiredAt, 0))
		if err := rearm(s, expiredAt, rearmNowPlus1ns, identity); err != nil {
			t.Fatal(err)
		}
		if got := readRow(t, s); got != challenge("active", rearmNowPlus1ns, 0) {
			t.Fatalf("stored challenge after re-arm = %+v, want the same row with expiry %s", got, rearmNowPlus1ns)
		}
		assertNoConsent(t, s)
	})
	t.Run("refuses malformed timestamps", func(t *testing.T) {
		s := seed(t, challenge("active", expiredAt, 0))
		assertInvalidTime(t, rearm(s, "not-a-timestamp", rearmTo, identity))
		assertInvalidTime(t, rearm(s, expiredAt, "not-a-timestamp", identity))
		err := s.TransactDurable(ctx, func(transaction *Transaction) error {
			return RearmApprovalChallengeTx(ctx, transaction, ref, identity, expiredAt, rearmTo, "not-a-timestamp")
		})
		assertInvalidTime(t, err)
		assertUnchanged(t, s, challenge("active", expiredAt, 0))
	})
	t.Run("rolls back the deadline when the exchange fails after re-arm", func(t *testing.T) {
		s := seed(t, challenge("active", expiredAt, 0))
		sentinel := errors.New("exchange failed after re-arm")
		err := s.TransactDurable(ctx, func(transaction *Transaction) error {
			if err := RearmApprovalChallengeTx(ctx, transaction, ref, identity, expiredAt, rearmTo, rearmNow); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("transaction error=%v, want the caller's failure", err)
		}
		assertUnchanged(t, s, challenge("active", expiredAt, 0))
	})
}
