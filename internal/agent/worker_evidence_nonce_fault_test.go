package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func workerEvidenceNonceFixture(t *testing.T, nonce string) (*Service, WorkerEvidenceAssertion, WorkerEvidenceBinding) {
	t.Helper()
	service, control, key := workerControlProofFixture(t, []agentcontract.Capability{CapabilityWorkerEvidence})
	env := workerEvidenceV2Envelope(t, nonce)
	env.ClientRef = control.ClientRef
	var assertion WorkerEvidenceAssertion
	decodeWorkerEvidenceEnvelope(t, env, &assertion)
	assertion.Signature = ed25519.Sign(key, CanonicalWorkerEvidenceAssertion(assertion))
	return service, assertion, decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(env, 0, ""))
}

func validateWorkerEvidenceNonce(t *testing.T, service *Service, assertion WorkerEvidenceAssertion, binding WorkerEvidenceBinding) (string, error) {
	t.Helper()
	ctx := context.Background()
	var principal string
	var validateErr error
	if err := service.Store.Transact(ctx, func(tx *store.Transaction) error {
		principal, validateErr = service.ValidateWorkerEvidenceAssertionTx(ctx, tx, assertion, binding)
		return validateErr
	}); err != validateErr {
		t.Fatalf("transaction returned %v, want the validator's error identity %v", err, validateErr)
	}
	return principal, validateErr
}

func installWorkerEvidenceNonceFault(t *testing.T, service *Service, verb, trigger string) {
	t.Helper()
	db := service.Store.DatabaseForTesting()
	if _, err := db.Exec(`CREATE TABLE nonce_fault_probe (ref TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER ` + trigger + ` BEFORE ` + verb + ` ON agent_nonce_replay BEGIN SELECT ref FROM nonce_fault_probe; END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE nonce_fault_probe`); err != nil {
		t.Fatal(err)
	}
}

func seedExpiredWorkerEvidenceNonce(t *testing.T, service *Service, nonce string) {
	t.Helper()
	expiredAt := service.now().Add(-2 * service.skew()).Format(time.RFC3339Nano)
	if _, err := service.Store.DatabaseForTesting().Exec(`INSERT INTO agent_nonce_replay(client_ref,nonce,observed_at,expires_at) VALUES(?,?,?,?)`, "client-1", nonce, expiredAt, expiredAt); err != nil {
		t.Fatal(err)
	}
}

func countWorkerEvidenceNonceRows(t *testing.T, service *Service, nonce string) int {
	t.Helper()
	var n int
	if err := service.Store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_nonce_replay WHERE nonce=?`, nonce).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertWorkerEvidenceNonceFault(t *testing.T, err error, detail string) {
	t.Helper()
	var failure *store.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("nonce fault = %v, want a typed store failure", err)
	}
	if failure.Kind != store.KindUnavailable || failure.Op != "agent_nonce" || failure.Detail != detail || !failure.RetrySafe || failure.RecoveryAction != "retry the assertion" {
		t.Fatalf("nonce fault = %+v, want retryable unavailable agent_nonce %q with recovery retry the assertion", failure, detail)
	}
}

func assertWorkerEvidenceNonceCause(t *testing.T, err error, code int) {
	t.Helper()
	var cause *sqlite.Error
	if !errors.As(err, &cause) {
		t.Fatalf("nonce fault = %v, want a typed SQLite driver cause", err)
	}
	if got := cause.Code(); got != code {
		t.Fatalf("nonce fault cause code = %d, want typed symbolic code %d", got, code)
	}
}

func assertWorkerEvidenceNonceEnvelope(t *testing.T, err error, detail string) {
	t.Helper()
	out := failureEnvelope(agentcontract.NewBase("worker-evidence-nonce-fault", "concord_product_view", "resolve"), err)
	if out.Error == nil || out.Error.Kind != "unreachable" {
		t.Fatalf("public kind = %+v for fault %v, want unreachable", out.Error, err)
	}
	if !out.Error.RetrySafe {
		t.Fatal("the public envelope must keep the fault retry-safe")
	}
	if out.Authority != agentcontract.AuthorityUnreachable {
		t.Fatalf("authority = %q, want %q", out.Authority, agentcontract.AuthorityUnreachable)
	}
	if !strings.HasPrefix(out.Error.Message, detail+": ") {
		t.Fatalf("public message = %q, want the detail followed by its driver cause", out.Error.Message)
	}
	if _, err := out.Encode(); err != nil {
		t.Fatalf("the fault refusal must be deliverable, got %v", err)
	}
}

func TestWorkerEvidenceNonceFaultClassification(t *testing.T) {
	t.Parallel()

	t.Run("an active signature authenticates", func(t *testing.T) {
		t.Parallel()
		service, assertion, binding := workerEvidenceNonceFixture(t, "worker-evidence-nonce-active-1")
		principal, err := validateWorkerEvidenceNonce(t, service, assertion, binding)
		if err != nil || principal != "human-1" {
			t.Fatalf("active principal = %q, error = %v", principal, err)
		}
		if got := countWorkerEvidenceNonceRows(t, service, "worker-evidence-nonce-active-1"); got != 1 {
			t.Fatalf("authenticated nonce rows = %d, want 1", got)
		}
	})

	t.Run("a genuine replay keeps the exact existing plain error", func(t *testing.T) {
		t.Parallel()
		const nonce = "worker-evidence-replay-nonce-1"
		service, assertion, binding := workerEvidenceNonceFixture(t, nonce)
		if principal, err := validateWorkerEvidenceNonce(t, service, assertion, binding); err != nil || principal != "human-1" {
			t.Fatalf("first assertion refused: principal %q error %v", principal, err)
		}
		_, err := validateWorkerEvidenceNonce(t, service, assertion, binding)
		const replay = "worker evidence assertion nonce replayed"
		if err == nil || err.Error() != replay {
			t.Fatalf("replay = %v, want the exact existing refusal %q", err, replay)
		}
		var failure *store.Failure
		if errors.As(err, &failure) {
			t.Fatalf("replay = %+v, want the existing plain refusal, not a store failure", failure)
		}
		if got := countWorkerEvidenceNonceRows(t, service, nonce); got != 1 {
			t.Fatalf("nonce rows = %d, want only the first write", got)
		}
	})

	t.Run("a non-constraint insert fault stays a typed unavailable fault", func(t *testing.T) {
		t.Parallel()
		const nonce = "worker-evidence-fault-nonce-001"
		const expired = "worker-evidence-expired-seed-1"
		service, assertion, binding := workerEvidenceNonceFixture(t, nonce)
		seedExpiredWorkerEvidenceNonce(t, service, expired)
		installWorkerEvidenceNonceFault(t, service, "INSERT", "agent_nonce_insert_fault_probe")
		_, err := validateWorkerEvidenceNonce(t, service, assertion, binding)
		assertWorkerEvidenceNonceFault(t, err, "cannot record assertion nonce")
		assertWorkerEvidenceNonceCause(t, err, sqlitelib.SQLITE_ERROR)
		assertWorkerEvidenceNonceEnvelope(t, err, "cannot record assertion nonce")
		if got := countWorkerEvidenceNonceRows(t, service, expired); got != 1 {
			t.Fatalf("expired seed rows = %d, want the in-transaction prune rolled back", got)
		}
		if got := countWorkerEvidenceNonceRows(t, service, nonce); got != 0 {
			t.Fatalf("faulted nonce rows = %d, want none recorded", got)
		}
	})

	t.Run("a prune fault stays a typed unavailable fault", func(t *testing.T) {
		t.Parallel()
		const nonce = "worker-evidence-prune-fault-01"
		const expired = "worker-evidence-expired-seed-2"
		service, assertion, binding := workerEvidenceNonceFixture(t, nonce)
		seedExpiredWorkerEvidenceNonce(t, service, expired)
		installWorkerEvidenceNonceFault(t, service, "DELETE", "agent_nonce_prune_fault_probe")
		_, err := validateWorkerEvidenceNonce(t, service, assertion, binding)
		assertWorkerEvidenceNonceFault(t, err, "cannot prune assertion nonces")
		assertWorkerEvidenceNonceCause(t, err, sqlitelib.SQLITE_ERROR)
		assertWorkerEvidenceNonceEnvelope(t, err, "cannot prune assertion nonces")
		if got := countWorkerEvidenceNonceRows(t, service, expired); got != 1 {
			t.Fatalf("expired seed rows = %d, want the failed prune rolled back", got)
		}
		if got := countWorkerEvidenceNonceRows(t, service, nonce); got != 0 {
			t.Fatalf("nonce rows = %d, want none recorded past a failed prune", got)
		}
	})
}
