package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestSessionPrepareCallbackFailureClassification(t *testing.T) {
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

	for _, stage := range []string{"identity", "bootstrap"} {
		t.Run(stage, func(t *testing.T) {
			untypedExit := 1
			if stage == "identity" {
				untypedExit = sessionPrepareRefusalExit
			}
			for _, tc := range []struct {
				name string
				err  error
				want int
			}{
				{"retry-safe", &store.Failure{Kind: store.KindUnavailable, Op: "sync_durable", Detail: "checkpoint busy", RetrySafe: true, EffectPossible: true}, 1},
				{"wrapped retry-safe", fmt.Errorf("assert identity: %w", &store.Failure{Kind: store.KindUnavailable, Detail: "checkpoint busy", RetrySafe: true}), 1},
				{"unsafe", &store.Failure{Kind: store.KindUnavailable, Detail: "checkpoint busy", RetrySafe: false}, sessionPrepareRefusalExit},
				{"wrapped unsafe", fmt.Errorf("assert identity: %w", &store.Failure{Kind: store.KindUnavailable, Detail: "checkpoint busy", RetrySafe: false}), sessionPrepareRefusalExit},
				{"untyped", errors.New("checkpoint busy"), untypedExit},
			} {
				t.Run(tc.name, func(t *testing.T) {
					identityCalls, bootCalls := 0, 0
					var out, errOut bytes.Buffer
					code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
						func(string) error { return nil },
						hostCommandAt(defaultHostResolution()),
						func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
							identityCalls++
							if stage == "identity" {
								return "", tc.err
							}
							return "concord-1", nil
						},
						func(context.Context, string, string, string) ([]byte, error) {
							bootCalls++
							return nil, tc.err
						})
					if code != tc.want {
						t.Errorf("exit=%d want %d; stderr=%q", code, tc.want, errOut.String())
					}
					if out.Len() != 0 || !strings.Contains(errOut.String(), tc.err.Error()) {
						t.Errorf("stdout=%q stderr=%q; want no success output and the original failure", out.String(), errOut.String())
					}
					wantBootCalls := 0
					if stage == "bootstrap" {
						wantBootCalls = 1
					}
					if identityCalls != 1 || bootCalls != wantBootCalls {
						t.Errorf("identity calls=%d boot calls=%d; want 1 and %d", identityCalls, bootCalls, wantBootCalls)
					}
				})
			}
		})
	}
}

func TestSessionPrepareBusyIdentityCheckpointAndRecovery(t *testing.T) {
	ctx := context.Background()
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(ctx, bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, dbPath)
	t.Chdir(result.Entry.Path)
	if _, err := s.DatabaseForTesting().Exec("PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readTx.Rollback() }()
	var eventCount int
	if err := readTx.QueryRowContext(ctx, "SELECT count(*) FROM domain_events").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	identityCalls, bootCalls := 0, 0
	var identityErr error
	run := func() (int, string, string) {
		var out, errOut bytes.Buffer
		code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
			func(string) error { return nil },
			hostCommandAt(defaultHostResolution()),
			func(ctx context.Context, _ string, _ hostCommandResolution, productID, workID, _ string) (string, error) {
				identityCalls++
				_, identityErr = s.RecordOrchestratorIdentityAssertion(ctx, fmt.Sprintf("prepare-identity-%d", identityCalls), s.Now(), store.OrchestratorIdentityAssertion{
					Type: "orchestrator", Version: "1", RulesetDigest: "sha256:" + strings.Repeat("a", 64),
					Sources:   []store.OrchestratorArtifactSource{{Kind: "orchestrator_definition", Path: filepath.Join(repo, "orchestrator.md"), SHA256: strings.Repeat("b", 64)}},
					ProductID: productID, WorkID: workID, PrincipalRef: "principal/orchestrator", ClientRef: "client/session", AgentRef: "agent/concord-1", SessionRef: "session/prepare",
				})
				return "concord-1", identityErr
			},
			func(context.Context, string, string, string) ([]byte, error) {
				bootCalls++
				return []byte(`{"watermark":"test"}`), nil
			})
		return code, out.String(), errOut.String()
	}
	code, out, diagnostic := run()
	var failure *store.Failure
	if !errors.As(identityErr, &failure) || !failure.RetrySafe || !failure.EffectPossible || failure.Op != "sync_durable" {
		t.Fatalf("identity error=%v; want a retry-safe post-commit checkpoint failure", identityErr)
	}
	if code != 1 || out != "" || bootCalls != 0 || !strings.Contains(diagnostic, "busy=1") {
		t.Fatalf("busy checkpoint exit=%d boot calls=%d stdout=%q stderr=%q", code, bootCalls, out, diagnostic)
	}
	var committedEvents int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM domain_events").Scan(&committedEvents); err != nil {
		t.Fatal(err)
	}
	if committedEvents != eventCount+1 {
		t.Fatalf("events=%d want %d; the failure must not erase the committed assertion", committedEvents, eventCount+1)
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	code, out, diagnostic = run()
	if code != 0 || identityErr != nil || bootCalls != 1 || out == "" || diagnostic != "" {
		t.Fatalf("released reader exit=%d boot calls=%d identity error=%v stdout=%q stderr=%q", code, bootCalls, identityErr, out, diagnostic)
	}
}
