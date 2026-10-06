package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestSessionPrepareIdentityContentionKeepsRetryRoute(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, dbPath)
	t.Chdir(result.Entry.Path)
	holder := mustOpenStore(t, dbPath)
	// Bound the synthetic contention without changing the production timeout.
	if _, err := s.DatabaseForTesting().ExecContext(t.Context(), "PRAGMA busy_timeout = 1"); err != nil {
		t.Fatal(err)
	}
	const eventID = "prepare-contended-identity"
	identityCalls, bootCalls := 0, 0
	var identityErr error
	identity := func(ctx context.Context, _ string, _ hostCommandResolution, productID, workID, _ string) (string, error) {
		identityCalls++
		_, identityErr = s.RecordOrchestratorIdentityAssertion(ctx, eventID, s.Now(), store.OrchestratorIdentityAssertion{
			Type: "orchestrator", Version: "1", RulesetDigest: "sha256:" + strings.Repeat("a", 64),
			Sources:   []store.OrchestratorArtifactSource{{Kind: "orchestrator_definition", Path: "/tmp/orchestrator.md", SHA256: strings.Repeat("b", 64)}},
			ProductID: productID, WorkID: workID, PrincipalRef: "principal/orchestrator", ClientRef: "client/session", AgentRef: "agent/concord-1", SessionRef: "session/prepare",
		})
		return "concord-1", identityErr
	}
	var out, errOut bytes.Buffer
	prepare := func() int {
		out.Reset()
		errOut.Reset()
		return runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, ""), s, &out, &errOut,
			func(string) error { return nil }, hostCommandAt(defaultHostResolution()), identity,
			func(context.Context, string, string, string) ([]byte, error) {
				bootCalls++
				return []byte(`{"watermark":"test"}`), nil
			})
	}
	if err := holder.Transact(t.Context(), func(*store.Transaction) error {
		code := prepare()
		var failure *store.Failure
		if !errors.As(identityErr, &failure) || !failure.RetrySafe || !strings.Contains(identityErr.Error(), "SQLITE_BUSY") {
			t.Fatalf("expected retry-safe SQLite contention, got %v", identityErr)
		}
		if code != 1 || identityCalls != 1 || bootCalls != 0 || out.Len() != 0 {
			t.Fatalf("code=%d identity=%d boot=%d stdout=%q stderr=%q", code, identityCalls, bootCalls, out.String(), errOut.String())
		}
		var count int
		if err := s.DatabaseForTesting().QueryRowContext(t.Context(), "SELECT count(*) FROM domain_events WHERE event_id = ?", eventID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("contended assertion count=%d err=%v", count, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code := prepare(); code != 0 || identityErr != nil || identityCalls != 2 || bootCalls != 1 {
		t.Fatalf("prepare after lock release: code=%d identity=%d boot=%d err=%v stderr=%q", code, identityCalls, bootCalls, identityErr, errOut.String())
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(t.Context(), "SELECT count(*) FROM domain_events WHERE event_id = ?", eventID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("successful assertion count=%d err=%v", count, err)
	}
}
