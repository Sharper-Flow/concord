package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// newTrustedClientReadFixture fails the test unless client-1 authorizes for
// real, so every later refusal is a status or fault, not a broken fixture.
func newTrustedClientReadFixture(t *testing.T) (*store.Store, *Service, Invocation) {
	t.Helper()
	db := openAgentDB(t)
	seedSimpleAuthorityScope(t, db)
	service, invocation, authority := newAuthorizedService(t, db, "client-1", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	if authority.ClientRef != "client-1" || authority.PrincipalRef != "human-1" {
		t.Fatalf("active control authority = %+v, want client client-1 for human-1", authority)
	}
	return db, service, invocation
}

func revokeSoleClientKey(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.DatabaseForTesting().Exec(`UPDATE agent_client_keys SET status='revoked', revoked_at='now' WHERE client_ref='client-1'`); err != nil {
		t.Fatal(err)
	}
}

func revokeTrustedClient(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.DatabaseForTesting().Exec(`UPDATE agent_clients SET status='revoked', revoked_at='now' WHERE client_ref='client-1'`); err != nil {
		t.Fatal(err)
	}
}

func assertClientRefusalEnvelope(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("an absent client or key was authorized")
	}
	out := failureEnvelope(agentcontract.NewBase("trusted-client-refusal", "concord_product_view", "resolve"), err)
	if out.Error == nil || out.Error.Kind != "unauthorized" {
		t.Fatalf("refusal = %v, want public kind unauthorized", err)
	}
	if out.Error.Message != "client or key is not active" {
		t.Fatalf("refusal message = %q, want %q", out.Error.Message, "client or key is not active")
	}
	if out.Error.RecoveryAction.Kind != "contact_operator" {
		t.Fatalf("refusal recovery = %q, want contact_operator", out.Error.RecoveryAction.Kind)
	}
	if out.Error.RetrySafe {
		t.Fatal("a status refusal must not be marked retry-safe")
	}
	if _, err := out.Encode(); err != nil {
		t.Fatalf("the refusal must be deliverable, got %v", err)
	}
}

func assertUnavailableClientReadFault(t *testing.T, err error) {
	t.Helper()
	var failure *store.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("read fault = %v, want a typed store failure", err)
	}
	if failure.Kind != store.KindUnavailable {
		t.Fatalf("fault kind = %q, want %q", failure.Kind, store.KindUnavailable)
	}
	if failure.Op != "agent_client_read" {
		t.Fatalf("fault op = %q, want agent_client_read", failure.Op)
	}
	if !failure.RetrySafe {
		t.Fatal("a cancelled read must stay retry-safe")
	}
	if failure.Detail != "cannot read trusted client" {
		t.Fatalf("fault detail = %q, want %q", failure.Detail, "cannot read trusted client")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fault = %v, want it to wrap context.Canceled", err)
	}
	out := failureEnvelope(agentcontract.NewBase("trusted-client-read-fault", "concord_product_view", "resolve"), err)
	if out.Error == nil || out.Error.Kind != "unreachable" {
		t.Fatalf("public kind = %+v for fault %v, want unreachable", out.Error, err)
	}
	if !out.Error.RetrySafe {
		t.Fatal("the public envelope must keep the fault retry-safe")
	}
	if out.Authority != agentcontract.AuthorityUnreachable {
		t.Fatalf("authority = %q, want %q", out.Authority, agentcontract.AuthorityUnreachable)
	}
	if want := "cannot read trusted client: context canceled"; out.Error.Message != want {
		t.Fatalf("public message = %q, want %q", out.Error.Message, want)
	}
	if _, err := out.Encode(); err != nil {
		t.Fatalf("the fault refusal must be deliverable, got %v", err)
	}
}

// cancelledContext is the fault injection: only the trusted-client query
// observes it.
func cancelledContext() context.Context {
	faultCtx, cancel := context.WithCancel(context.Background())
	cancel()
	return faultCtx
}

func TestAuthorizeSeparatesClientRefusalsFromStoreFaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("an active client and key authorize", func(t *testing.T) {
		t.Parallel()
		_, service, invocation := newTrustedClientReadFixture(t)
		authority, err := service.Authorize(ctx, invocation)
		if err != nil {
			t.Fatalf("active client refused: %v", err)
		}
		if authority.ClientRef != "client-1" || authority.PrincipalRef != "human-1" {
			t.Fatalf("authority = %+v, want client client-1 for human-1", authority)
		}
	})
	t.Run("an unknown client is refused unauthorized", func(t *testing.T) {
		t.Parallel()
		_, service, invocation := newTrustedClientReadFixture(t)
		invocation.ClientRef = "client-unknown"
		_, err := service.Authorize(ctx, invocation)
		assertClientRefusalEnvelope(t, err)
	})
	t.Run("a sole revoked key is refused unauthorized", func(t *testing.T) {
		t.Parallel()
		db, service, invocation := newTrustedClientReadFixture(t)
		revokeSoleClientKey(t, db)
		_, err := service.Authorize(ctx, invocation)
		assertClientRefusalEnvelope(t, err)
	})
	t.Run("a revoked client with an active key is refused unauthorized", func(t *testing.T) {
		t.Parallel()
		db, service, invocation := newTrustedClientReadFixture(t)
		revokeTrustedClient(t, db)
		_, err := service.Authorize(ctx, invocation)
		assertClientRefusalEnvelope(t, err)
	})
	t.Run("a cancelled trusted-client read keeps its unavailable fault", func(t *testing.T) {
		t.Parallel()
		_, service, invocation := newTrustedClientReadFixture(t)
		_, err := service.Authorize(cancelledContext(), invocation)
		assertUnavailableClientReadFault(t, err)
	})
}

func TestAuthorizeTxSeparatesClientRefusalsFromStoreFaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("an active client and key authorize", func(t *testing.T) {
		t.Parallel()
		db, service, invocation := newTrustedClientReadFixture(t)
		var authority Authority
		if err := db.Transact(ctx, func(tx *store.Transaction) error {
			var err error
			authority, err = service.AuthorizeTx(ctx, tx, probedHost(), invocation)
			return err
		}); err != nil {
			t.Fatalf("active client refused: %v", err)
		}
		if authority.ClientRef != "client-1" || authority.PrincipalRef != "human-1" {
			t.Fatalf("authority = %+v, want client client-1 for human-1", authority)
		}
	})
	t.Run("an unknown client is refused unauthorized", func(t *testing.T) {
		t.Parallel()
		db, service, invocation := newTrustedClientReadFixture(t)
		invocation.ClientRef = "client-unknown"
		var err error
		if txErr := db.Transact(ctx, func(tx *store.Transaction) error {
			_, err = service.AuthorizeTx(ctx, tx, probedHost(), invocation)
			return err
		}); txErr != nil {
			err = txErr
		}
		assertClientRefusalEnvelope(t, err)
	})
	t.Run("a sole revoked key is refused unauthorized", func(t *testing.T) {
		t.Parallel()
		db, service, invocation := newTrustedClientReadFixture(t)
		revokeSoleClientKey(t, db)
		var err error
		if txErr := db.Transact(ctx, func(tx *store.Transaction) error {
			_, err = service.AuthorizeTx(ctx, tx, probedHost(), invocation)
			return err
		}); txErr != nil {
			err = txErr
		}
		assertClientRefusalEnvelope(t, err)
	})
	t.Run("a revoked client with an active key is refused unauthorized", func(t *testing.T) {
		t.Parallel()
		db, service, invocation := newTrustedClientReadFixture(t)
		revokeTrustedClient(t, db)
		var err error
		if txErr := db.Transact(ctx, func(tx *store.Transaction) error {
			_, err = service.AuthorizeTx(ctx, tx, probedHost(), invocation)
			return err
		}); txErr != nil {
			err = txErr
		}
		assertClientRefusalEnvelope(t, err)
	})
	t.Run("a cancelled trusted-client read keeps its unavailable fault", func(t *testing.T) {
		t.Parallel()
		db, service, invocation := newTrustedClientReadFixture(t)
		var authErr error
		// The transaction opens on the live context so creation succeeds; only
		// AuthorizeTx sees the cancelled context, so the fault lands in the
		// trusted-client query and not in BEGIN.
		transactErr := db.Transact(ctx, func(tx *store.Transaction) error {
			_, authErr = service.AuthorizeTx(cancelledContext(), tx, probedHost(), invocation)
			return authErr
		})
		assertUnavailableClientReadFault(t, authErr)
		if transactErr != authErr {
			t.Fatalf("transaction returned %v, want the entry point's own fault %v", transactErr, authErr)
		}
	})
}
