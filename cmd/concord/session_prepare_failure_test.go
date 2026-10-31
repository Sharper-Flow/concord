package main

import (
	"bytes"
	"context"
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
	result := seedCLISessionWorktree(t, s, repo)
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
						hostCommandAt(defaultHostResolution()),
						func(context.Context, string, hostCommandResolution, string) (string, error) {
							identityCalls++
							if stage == "identity" {
								return "", tc.err
							}
							return "concord-1", nil
						},
						func(context.Context, string, string, string, string) ([]byte, error) {
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
