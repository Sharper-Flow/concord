package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"

	sqlitelib "modernc.org/sqlite/lib"
)

const (
	nonceFaultObservedAt  = "2026-01-01T00:00:00Z"
	nonceFaultExpiresAt   = "2026-01-03T00:00:00Z"
	nonceFaultPruneBefore = "2026-01-02T00:00:00Z"
)

func nonceFaultFixture(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.RegisterTrustedClient(ctx,
		TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`},
		TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"},
		nonceFaultObservedAt); err != nil {
		t.Fatal(err)
	}
	return s
}

func seedExpiredNonceFault(t *testing.T, s *Store, nonce string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO agent_nonce_replay(client_ref,nonce,observed_at,expires_at) VALUES(?,?,?,?)`, "client-1", nonce, nonceFaultObservedAt, nonceFaultObservedAt); err != nil {
		t.Fatal(err)
	}
}

// Drop the trigger's body dependency, not the nonce table, so only the
// selected statement fails.
func installNonceFaultTrigger(t *testing.T, s *Store, verb, trigger string) {
	t.Helper()
	if _, err := s.db.Exec(`CREATE TABLE nonce_fault_probe (ref TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER ` + trigger + ` BEFORE ` + verb + ` ON agent_nonce_replay BEGIN SELECT ref FROM nonce_fault_probe; END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE nonce_fault_probe`); err != nil {
		t.Fatal(err)
	}
}

func recordNonceFault(t *testing.T, s *Store, nonce string) error {
	t.Helper()
	ctx := context.Background()
	var recordErr error
	if err := s.Transact(ctx, func(tx *Transaction) error {
		recordErr = PruneAndRecordNonceTx(ctx, tx, "client-1", nonce, nonceFaultObservedAt, nonceFaultExpiresAt, nonceFaultPruneBefore)
		return recordErr
	}); err != recordErr {
		t.Fatalf("transaction returned %v, want the recorded failure identity %v", err, recordErr)
	}
	return recordErr
}

func countNonceFaultRows(t *testing.T, s *Store, nonce string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM agent_nonce_replay WHERE nonce=?`, nonce).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertNonceFaultTyped(t *testing.T, err error, kind FailureKind, detail string) *Failure {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("nonce fault = %v, want a typed store failure", err)
	}
	if failure.Kind != kind {
		t.Fatalf("nonce fault kind = %q, want %q", failure.Kind, kind)
	}
	if failure.Op != "agent_nonce" {
		t.Fatalf("nonce fault op = %q, want agent_nonce", failure.Op)
	}
	if failure.Detail != detail {
		t.Fatalf("nonce fault detail = %q, want %q", failure.Detail, detail)
	}
	return failure
}

func assertNonceFaultCause(t *testing.T, failure *Failure, code int) {
	t.Helper()
	if got := sqlitedb.ResultCode(failure.Err); got != code {
		t.Fatalf("nonce fault cause code = %d, want typed symbolic code %d", got, code)
	}
}

func TestPruneAndRecordNonceClassifiesOnlyUniqueViolationsAsReplay(t *testing.T) {
	t.Parallel()

	t.Run("an active insert records its nonce", func(t *testing.T) {
		t.Parallel()
		s := nonceFaultFixture(t)
		const nonce = "active-nonce-0000001"
		if err := recordNonceFault(t, s, nonce); err != nil {
			t.Fatalf("active nonce refused: %v", err)
		}
		if got := countNonceFaultRows(t, s, nonce); got != 1 {
			t.Fatalf("recorded nonce rows = %d, want 1", got)
		}
	})

	t.Run("a genuine duplicate keeps the exact replay refusal", func(t *testing.T) {
		t.Parallel()
		s := nonceFaultFixture(t)
		const nonce = "duplicate-nonce-00001"
		if err := recordNonceFault(t, s, nonce); err != nil {
			t.Fatalf("first nonce refused: %v", err)
		}
		err := recordNonceFault(t, s, nonce)
		failure := assertNonceFaultTyped(t, err, KindProjectionConflict, "host assertion nonce replayed")
		if failure.RetrySafe || failure.RecoveryAction != "issue a new assertion nonce" {
			t.Fatalf("replay refusal = retry %v recovery %q, want not retry-safe with the existing recovery", failure.RetrySafe, failure.RecoveryAction)
		}
	})

	t.Run("a non-constraint insert fault is unavailable, never replayed", func(t *testing.T) {
		t.Parallel()
		s := nonceFaultFixture(t)
		const expired = "expired-nonce-seed-0001"
		const nonce = "insert-fault-nonce-001"
		seedExpiredNonceFault(t, s, expired)
		installNonceFaultTrigger(t, s, "INSERT", "agent_nonce_insert_fault_probe")
		err := recordNonceFault(t, s, nonce)
		failure := assertNonceFaultTyped(t, err, KindUnavailable, "cannot record assertion nonce")
		if !failure.RetrySafe || failure.RecoveryAction != "retry the assertion" {
			t.Fatalf("insert fault = retry %v recovery %q, want retry-safe with recovery retry the assertion", failure.RetrySafe, failure.RecoveryAction)
		}
		assertNonceFaultCause(t, failure, sqlitelib.SQLITE_ERROR)
		if got := countNonceFaultRows(t, s, expired); got != 1 {
			t.Fatalf("expired seed rows = %d, want the in-transaction prune rolled back", got)
		}
		if got := countNonceFaultRows(t, s, nonce); got != 0 {
			t.Fatalf("faulted nonce rows = %d, want none recorded", got)
		}
	})

	t.Run("a CHECK constraint failure is not a replay", func(t *testing.T) {
		t.Parallel()
		s := nonceFaultFixture(t)
		err := recordNonceFault(t, s, "short")
		failure := assertNonceFaultTyped(t, err, KindUnavailable, "cannot record assertion nonce")
		if !failure.RetrySafe {
			t.Fatal("a storage fault must stay retry-safe")
		}
		assertNonceFaultCause(t, failure, sqlitelib.SQLITE_CONSTRAINT_CHECK)
	})

	t.Run("a non-constraint prune fault keeps its unavailable preservation", func(t *testing.T) {
		t.Parallel()
		s := nonceFaultFixture(t)
		const expired = "expired-nonce-seed-0002"
		const nonce = "prune-fault-nonce-0001"
		seedExpiredNonceFault(t, s, expired)
		installNonceFaultTrigger(t, s, "DELETE", "agent_nonce_prune_fault_probe")
		err := recordNonceFault(t, s, nonce)
		failure := assertNonceFaultTyped(t, err, KindUnavailable, "cannot prune assertion nonces")
		if !failure.RetrySafe || failure.RecoveryAction != "retry the assertion" {
			t.Fatalf("prune fault = retry %v recovery %q, want retry-safe with recovery retry the assertion", failure.RetrySafe, failure.RecoveryAction)
		}
		assertNonceFaultCause(t, failure, sqlitelib.SQLITE_ERROR)
		if got := countNonceFaultRows(t, s, expired); got != 1 {
			t.Fatalf("expired seed rows = %d, want the failed prune rolled back", got)
		}
		if got := countNonceFaultRows(t, s, nonce); got != 0 {
			t.Fatalf("nonce rows = %d, want none recorded past a failed prune", got)
		}
	})
}
