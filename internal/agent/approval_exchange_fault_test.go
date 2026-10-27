package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// approvalExchangeFixture issues one challenge for a fixed check.
func approvalExchangeFixture(t *testing.T) (*store.Store, *Service, Invocation, ApprovalCheck) {
	t.Helper()
	db := openAgentDB(t)
	seedSimpleAuthorityScope(t, db)
	service, inv, _ := newAuthorizedService(t, db, "client-1", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	inv.HostAssertionDigest = "sha256:host-resolution"
	ctx := context.Background()
	check := ApprovalCheck{OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", ClientRef: inv.ClientRef, SessionRef: inv.SessionRef}
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		var err error
		check.ApprovalRef, err = service.CreateApprovalChallengeTx(ctx, tx, probedHost(), inv, ApprovalChallengeSpec{OperationDigest: check.OperationDigest, Scope: check.Scope, Versions: check.Versions, Consequence: check.Consequence, HostAssertionDigest: inv.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return db, service, inv, check
}

func execFixtureSQL(t *testing.T, db *store.Store, statement string) {
	t.Helper()
	if _, err := db.DatabaseForTesting().Exec(statement); err != nil {
		t.Fatal(err)
	}
}

// assertApprovalInvalidRefusal requires the owner's typed approval_invalid
// refusal, not a plain error the mapper must guess at.
func assertApprovalInvalidRefusal(t *testing.T, err error, message string) {
	t.Helper()
	var refusal *runtimeFailure
	if !errors.As(err, &refusal) || refusal.kind != "approval_invalid" || refusal.recovery != "request_approval" || refusal.retry || refusal.message != message {
		t.Fatalf("refusal = %#v, want typed approval_invalid %q", err, message)
	}
}

// assertStoreFault requires the store's unavailable fault from op to reach
// the caller unchanged.
func assertStoreFault(t *testing.T, err error, op string) {
	t.Helper()
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.Kind != store.KindUnavailable || failure.Op != op {
		t.Fatalf("error = %#v, want store %s from %s", err, store.KindUnavailable, op)
	}
}

// Deriving an approval from a challenge refuses approval-semantic outcomes
// as typed approval_invalid and passes every store fault through.
func TestCreateApprovalFromChallengeSeparatesRefusalsFromStoreFaults(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		inject func(*testing.T, *store.Store, *Invocation, *ApprovalCheck)
		assert func(*testing.T, error)
	}{
		{"unknown challenge", func(_ *testing.T, _ *store.Store, _ *Invocation, check *ApprovalCheck) {
			check.ApprovalRef = strings.Repeat("ab", 32)
		}, func(t *testing.T, err error) { assertApprovalInvalidRefusal(t, err, "approval challenge not found") }},
		{"challenge no longer usable", func(_ *testing.T, _ *store.Store, inv *Invocation, _ *ApprovalCheck) {
			inv.HostAssertionDigest = "sha256:other-host"
		}, func(t *testing.T, err error) { assertApprovalInvalidRefusal(t, err, "approval challenge invalid") }},
		{"challenge read fault", func(t *testing.T, db *store.Store, _ *Invocation, _ *ApprovalCheck) {
			execFixtureSQL(t, db, `ALTER TABLE agent_approval_challenges RENAME COLUMN host_assertion_digest TO host_assertion_digest_unreadable`)
		}, func(t *testing.T, err error) { assertStoreFault(t, err, "agent_challenge_read") }},
		{"challenge consumption lost race", func(t *testing.T, db *store.Store, _ *Invocation, _ *ApprovalCheck) {
			execFixtureSQL(t, db, `CREATE TRIGGER challenge_consume_lost_race BEFORE UPDATE ON agent_approval_challenges BEGIN SELECT RAISE(IGNORE); END`)
		}, func(t *testing.T, err error) {
			assertApprovalInvalidRefusal(t, err, "approval challenge consumption lost race")
		}},
		{"challenge consumption fault", func(t *testing.T, db *store.Store, _ *Invocation, _ *ApprovalCheck) {
			execFixtureSQL(t, db, `CREATE TRIGGER challenge_consume_fault BEFORE UPDATE ON agent_approval_challenges BEGIN SELECT RAISE(ABORT, 'challenge consume fault'); END`)
		}, func(t *testing.T, err error) {
			assertStoreFault(t, err, "agent_challenge_consume")
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			db, service, inv, check := approvalExchangeFixture(t)
			testCase.inject(t, db, &inv, &check)
			ctx := context.Background()
			err := db.Transact(ctx, func(tx *store.Transaction) error {
				_, err := service.CreateApprovalFromChallengeTx(ctx, tx, probedHost(), inv, check.ApprovalRef)
				return err
			})
			testCase.assert(t, err)
			if countRows(t, db.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 {
				t.Fatal("refused derivation recorded an approval")
			}
		})
	}
}

// Reading the operator behind a consumed approval refuses approval-semantic
// outcomes as typed approval_invalid and passes every store fault through.
func TestApprovalAuthorityActorSeparatesRefusalsFromStoreFaults(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		consume bool
		inject  func(*testing.T, *store.Store, *Invocation, *string)
		assert  func(*testing.T, error)
	}{
		{"unknown approval", true, func(_ *testing.T, _ *store.Store, _ *Invocation, ref *string) {
			*ref = strings.Repeat("ab", 32)
		}, func(t *testing.T, err error) {
			assertApprovalInvalidRefusal(t, err, "consumed approval authority is unavailable")
		}},
		{"approval read fault", true, func(t *testing.T, db *store.Store, _ *Invocation, _ *string) {
			execFixtureSQL(t, db, `ALTER TABLE agent_approvals RENAME COLUMN protected_evidence_ref TO protected_evidence_ref_unreadable`)
		}, func(t *testing.T, err error) {
			assertStoreFault(t, err, "agent_approval_authority")
		}},
		{"approval not consumed", false, func(*testing.T, *store.Store, *Invocation, *string) {}, func(t *testing.T, err error) {
			assertApprovalInvalidRefusal(t, err, "approval authority was not exactly consumed")
		}},
		{"challenge not consumed", true, func(t *testing.T, db *store.Store, _ *Invocation, _ *string) {
			execFixtureSQL(t, db, `UPDATE agent_approval_challenges SET status='active', consumed_at=NULL`)
		}, func(t *testing.T, err error) {
			assertApprovalInvalidRefusal(t, err, "approval challenge was not exactly consumed")
		}},
		{"challenge bound to another session", true, func(_ *testing.T, _ *store.Store, inv *Invocation, _ *string) {
			inv.SessionRef = "session-other"
		}, func(t *testing.T, err error) {
			assertApprovalInvalidRefusal(t, err, "approval challenge is not bound to the invoking session")
		}},
		{"challenge read fault", true, func(t *testing.T, db *store.Store, _ *Invocation, _ *string) {
			execFixtureSQL(t, db, `ALTER TABLE agent_approval_challenges RENAME COLUMN host_assertion_digest TO host_assertion_digest_unreadable`)
		}, func(t *testing.T, err error) { assertStoreFault(t, err, "agent_challenge_read") }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			db, service, inv, check := approvalExchangeFixture(t)
			ctx := context.Background()
			var approvalRef string
			if err := db.Transact(ctx, func(tx *store.Transaction) error {
				var err error
				if approvalRef, err = service.CreateApprovalFromChallengeTx(ctx, tx, probedHost(), inv, check.ApprovalRef); err != nil || !testCase.consume {
					return err
				}
				return service.ValidateAndConsumeApprovalTx(ctx, tx, approvalRef, check)
			}); err != nil {
				t.Fatal(err)
			}
			testCase.inject(t, db, &inv, &approvalRef)
			err := db.Transact(ctx, func(tx *store.Transaction) error {
				_, err := service.ApprovalAuthorityActorTx(ctx, tx, inv, approvalRef)
				return err
			})
			testCase.assert(t, err)
		})
	}
}
