package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func commandSessionPrepareInput(t *testing.T, workID, task string) []byte {
	t.Helper()
	raw, err := json.Marshal(sessionPrepareInput{ProductID: "product-wl", WorkID: workID, Task: task, Agent: "concord-1"})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustOpenStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSessionPrepareRefusesWrongDirectoryBeforeIdentity(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result := seedCLISessionWorktree(t, s, repo)
	t.Chdir(repo)
	identityCalls := 0
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run the task"), s, &out, &errOut,
		hostCommandAt(defaultHostResolution()),
		func(context.Context, string, hostCommandResolution, string) (string, error) {
			identityCalls++
			return "agent", nil
		},
		func(context.Context, string, string, string, string) ([]byte, error) {
			return []byte(`{"watermark":"test"}`), nil
		})
	if code == 0 || identityCalls != 0 || !strings.Contains(errOut.String(), "main checkout") {
		t.Fatalf("wrong-directory code=%d identity_calls=%d stderr=%q", code, identityCalls, errOut.String())
	}
}

func modelCLIHistoricalInitiative(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET kind='initiative' WHERE id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPrepareRefusesForeignScopeBeforeCallbacks(t *testing.T) {
	for _, foreignProject := range []bool{false, true} {
		name := "wrong Product"
		if foreignProject {
			name = "Project outside work memberships"
		}
		t.Run(name, func(t *testing.T) {
			repo := initLocatorRepo(t)
			dbPath := filepath.Join(t.TempDir(), "concord.db")
			s := mustOpenStore(t, dbPath)
			seedLocatorAuthority(t, s, repo)
			work := seedCLISessionWorktree(t, s, repo)
			input := sessionPrepareInput{ProductID: "product-other", WorkID: work.WorkID, Agent: "concord-1"}
			directory := work.Entry.Path
			want := "requested Product scope"
			if foreignProject {
				foreignRepo := initLocatorRepo(t)
				seedProductProject(t, s, "product-other", "project-other", "other")
				if err := s.AddProjectLocator(context.Background(), "project-other", store.ProjectLocator{ID: "path-other", Kind: store.LocatorCanonicalPath, Value: foreignRepo}, 1); err != nil {
					t.Fatal(err)
				}
				directory = seedCLIHostWorktree(t, foreignRepo, work.WorkID, "project-other").Path
				input.ProductID = "product-wl"
				want = "not a work membership"
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(directory)
			calls := 0
			var out, errOut bytes.Buffer
			code := runSessionPrepare(raw, s, &out, &errOut,
				func(context.Context, string) (hostCommandResolution, error) {
					calls++
					return defaultHostResolution(), nil
				},
				func(context.Context, string, hostCommandResolution, string) (string, error) {
					calls++
					return "agent", nil
				},
				func(context.Context, string, string, string, string) ([]byte, error) {
					calls++
					return []byte(`{}`), nil
				})
			if code != sessionPrepareRefusalExit || calls != 0 || out.Len() != 0 || !strings.Contains(errOut.String(), want) {
				t.Fatalf("code=%d callbacks=%d stdout=%q stderr=%q want=%q", code, calls, out.String(), errOut.String(), want)
			}
		})
	}
}

func TestSessionPrepareRefusesRetiredInitiativeBeforeCallbacks(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result := seedCLISessionWorktree(t, s, repo)
	t.Setenv(dbOverrideEnv, dbPath)
	t.Chdir(result.Entry.Path)
	modelCLIHistoricalInitiative(t, s, result.WorkID)
	var before, after int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
		func(context.Context, string) (hostCommandResolution, error) {
			calls++
			return defaultHostResolution(), nil
		},
		func(context.Context, string, hostCommandResolution, string) (string, error) {
			calls++
			return "orchestrator", nil
		},
		func(context.Context, string, string, string, string) ([]byte, error) {
			calls++
			return []byte(`{}`), nil
		})
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if code != sessionPrepareRefusalExit || calls != 0 || after != before || out.Len() != 0 || !strings.Contains(errOut.String(), "CD-0213") {
		t.Fatalf("code=%d callbacks=%d events=%d->%d stdout=%q stderr=%q", code, calls, before, after, out.String(), errOut.String())
	}
}

func TestSessionPrepareRunsIdentityBeforeBoot(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result := seedCLISessionWorktree(t, s, repo)
	t.Setenv(dbOverrideEnv, dbPath)
	t.Chdir(result.Entry.Path)
	var before, after int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM effect_receipts").Scan(&before); err != nil {
		t.Fatal(err)
	}
	identityCalls, bootCalls := 0, 0
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "use UTF-8 ✓"), s, &out, &errOut,
		hostCommandAt(defaultHostResolution()),
		func(_ context.Context, _ string, _ hostCommandResolution, agent string) (string, error) {
			identityCalls++
			if agent != "concord-1" {
				return "", errors.New("session-prepare must pass the active agent through to identity verification")
			}
			return "orchestrator", nil
		},
		func(context.Context, string, string, string, string) ([]byte, error) {
			if identityCalls != 1 {
				t.Error("boot ran before identity")
			}
			bootCalls++
			return []byte(`{"watermark":"test"}`), nil
		})
	if code != 0 || identityCalls != 1 || bootCalls != 1 || !strings.Contains(out.String(), "use UTF-8 ✓") {
		t.Fatalf("prepare code=%d identity=%d boot=%d stdout=%q stderr=%q", code, identityCalls, bootCalls, out.String(), errOut.String())
	}
	var prepared struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.Title != "Locator Work" {
		t.Fatalf("prepare title=%q", prepared.Title)
	}
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM effect_receipts").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("success event count before=%d after=%d", before, after)
	}
	before = after
	identityCalls, bootCalls = 0, 0
	out, errOut = bytes.Buffer{}, bytes.Buffer{}
	code = runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
		func(context.Context, string) (hostCommandResolution, error) {
			return hostCommandResolution{}, errors.New("registry unavailable")
		},
		func(context.Context, string, hostCommandResolution, string) (string, error) {
			identityCalls++
			return "orchestrator", nil
		},
		func(context.Context, string, string, string, string) ([]byte, error) {
			bootCalls++
			return []byte(`{"watermark":"test"}`), nil
		})
	if code == 0 || identityCalls != 0 || bootCalls != 0 {
		t.Fatalf("registry failure code=%d identity=%d boot=%d stderr=%q", code, identityCalls, bootCalls, errOut.String())
	}
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM effect_receipts").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("registry failure wrote event before=%d after=%d", before, after)
	}
}

func TestSessionPrepareReportsAKilledRegistryProbeAsRetryable(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result := seedCLISessionWorktree(t, s, repo)
	t.Setenv(dbOverrideEnv, dbPath)
	installFakeCommand(t, t.TempDir(), "#!/bin/sh\nkill -9 $$\n")
	t.Chdir(result.Entry.Path)
	identityCalls := 0
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
		hostSessionPrepareRegistry,
		func(context.Context, string, hostCommandResolution, string) (string, error) {
			identityCalls++
			return "concord-1", nil
		},
		func(context.Context, string, string, string, string) ([]byte, error) { return []byte(`{}`), nil })
	if code != 1 || identityCalls != 0 || !strings.Contains(errOut.String(), "killed") {
		t.Fatalf("killed probe code=%d identity_calls=%d stderr=%q", code, identityCalls, errOut.String())
	}
}

func TestTimedOutRegistryProbeTypedAndExit1(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	result := seedCLISessionWorktree(t, s, repo)
	t.Setenv(dbOverrideEnv, dbPath)
	installFakeCommand(t, t.TempDir(), "#!/bin/sh\nwhile true; do :; done\n")
	t.Chdir(result.Entry.Path)
	probeCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, probeErr := probeHostConfig(probeCtx, hostProbeArgv(defaultHostCommand), result.Entry.Path)
	var interrupted *hostProbeInterruptedError
	if !errors.As(probeErr, &interrupted) || !interrupted.DeadlinePassed {
		t.Fatalf("timed-out probe error=%v", probeErr)
	}
	identityCalls := 0
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
		func(ctx context.Context, dir string) (hostCommandResolution, error) {
			timedCtx, timedCancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer timedCancel()
			return hostSessionPrepareRegistry(timedCtx, dir)
		},
		func(context.Context, string, hostCommandResolution, string) (string, error) {
			identityCalls++
			return "concord-1", nil
		},
		func(context.Context, string, string, string, string) ([]byte, error) { return []byte(`{}`), nil })
	if code != 1 || identityCalls != 0 || !strings.Contains(errOut.String(), "deadline") {
		t.Fatalf("timed-out probe code=%d identity_calls=%d stderr=%q", code, identityCalls, errOut.String())
	}
}

func TestDeterministicHostRegistryRefusalsExit2(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonzero exit", true: "missing executable"}[missing], func(t *testing.T) {
			repo := initLocatorRepo(t)
			dbPath := filepath.Join(t.TempDir(), "concord.db")
			s := mustOpenStore(t, dbPath)
			seedLocatorAuthority(t, s, repo)
			result := seedCLISessionWorktree(t, s, repo)
			t.Setenv(dbOverrideEnv, dbPath)
			want := "exit status 3"
			if missing {
				gitPath, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				binDir := t.TempDir()
				if err := os.Symlink(gitPath, filepath.Join(binDir, "git")); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", binDir)
				want = "executable file not found"
			} else {
				installFakeCommand(t, t.TempDir(), "#!/bin/sh\nexit 3\n")
			}
			t.Chdir(result.Entry.Path)
			var out, errOut bytes.Buffer
			code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
				hostSessionPrepareRegistry,
				func(context.Context, string, hostCommandResolution, string) (string, error) {
					t.Fatal("identity ran after refused probe")
					return "", nil
				},
				func(context.Context, string, string, string, string) ([]byte, error) { return []byte(`{}`), nil })
			if code != sessionPrepareRefusalExit || !strings.Contains(errOut.String(), want) {
				t.Fatalf("probe code=%d stderr=%q want %q", code, errOut.String(), want)
			}
		})
	}
}

func TestSessionPrepareProbesTheCallingHostOnce(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unregistered", true: "registered"}[registered], func(t *testing.T) {
			repo := initLocatorRepo(t)
			dbPath := filepath.Join(t.TempDir(), "concord.db")
			s := mustOpenStore(t, dbPath)
			seedLocatorAuthority(t, s, repo)
			result := seedCLISessionWorktree(t, s, repo)
			t.Setenv(dbOverrideEnv, dbPath)
			bare := `{"agent":{"someone-else":{"mode":"primary"}}}`
			if registered {
				bare = `{"agent":{"concord-1":{"mode":"primary"}}}`
			}
			if err := os.WriteFile(filepath.Join(result.Entry.Path, "opencode.registry.json"), []byte(bare), 0o644); err != nil {
				t.Fatal(err)
			}
			recordDir := t.TempDir()
			installFakeHost(t, recordDir)
			t.Chdir(result.Entry.Path)
			var out, errOut bytes.Buffer
			code := runSessionPrepare(commandSessionPrepareInput(t, result.WorkID, "run"), s, &out, &errOut,
				hostSessionPrepareRegistry,
				func(_ context.Context, _ string, host hostCommandResolution, agent string) (string, error) {
					if err := verifyHostRegistersHandle(host, agent); err != nil {
						return "", err
					}
					return agent, nil
				},
				func(context.Context, string, string, string, string) ([]byte, error) { return []byte(`{}`), nil })
			if count := hostRecord(t, recordDir, "probe-count"); count != "1" {
				t.Fatalf("bare probes=%s", count)
			}
			if registered {
				if code != 0 {
					t.Fatalf("prepare code=%d stderr=%q", code, errOut.String())
				}
			} else if code != sessionPrepareRefusalExit || !strings.Contains(errOut.String(), "concord-1") || !strings.Contains(errOut.String(), "not registered") {
				t.Fatalf("prepare code=%d stderr=%q", code, errOut.String())
			}
		})
	}
}

func TestSessionPrepareAcceptsEmptyTask(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	origin := seedCLISessionWorktree(t, s, repo)
	t.Setenv(dbOverrideEnv, dbPath)
	t.Chdir(origin.Entry.Path)
	var out, errOut bytes.Buffer
	code := runSessionPrepare(commandSessionPrepareInput(t, origin.WorkID, ""), s, &out, &errOut,
		hostCommandAt(defaultHostResolution()),
		func(context.Context, string, hostCommandResolution, string) (string, error) {
			return "agent", nil
		},
		func(context.Context, string, string, string, string) ([]byte, error) {
			return []byte(`{"watermark":"test"}`), nil
		})
	if code != 0 {
		t.Fatalf("empty task session-prepare code=%d stderr=%q", code, errOut.String())
	}
	var prepared sessionPrepareOutput
	if err := json.Unmarshal(out.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prepared.Prompt, "Task:") {
		t.Fatalf("empty task prompt carries a task line: %q", prepared.Prompt)
	}
}
