package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

func TestContinuityBlockPrintsDeterministicPacketForDirectory(t *testing.T) {
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "fixture.db"))
	directory := "/session/worktree"
	bootstrap := func(_ context.Context, _, dir string) ([]byte, error) {
		if dir != directory {
			t.Fatalf("continuity-block resolved %q, want the supplied directory %q", dir, directory)
		}
		return sessionboot.Build("product-1", store.ContinuitySnapshot{
			WorkID: "work-1", ProductIdentity: []string{"product-1"}, WorkflowStep: "planning",
			SpecMandate: []string{}, Boundaries: []store.ContextBoundary{}, Watermark: "seq:42",
			RestartUnavailableReason: "typed restart is deliberately excluded", PendingMessages: 3,
		})
	}
	var packets []string
	for range 2 {
		var out, errOut bytes.Buffer
		if code := runContinuityBlockCommandWithDirectory([]string{directory}, &out, &errOut, bootstrap); code != 0 {
			t.Fatalf("continuity-block exit=%d stderr=%q", code, errOut.String())
		}
		packets = append(packets, out.String())
	}
	if packets[0] == "" || packets[0] != packets[1] {
		t.Fatalf("continuity-block packets differ: %q vs %q", packets[0], packets[1])
	}
	if !strings.Contains(packets[0], `"pending_messages":3`) {
		t.Fatalf("continuity-block omitted pending messages: %q", packets[0])
	}
	if err := sessionboot.Validate([]byte(packets[0])); err != nil {
		t.Fatalf("continuity-block packet invalid: %v", err)
	}
}

func TestContinuityBlockWithoutResolvedClaimPrintsNothing(t *testing.T) {
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "fixture.db"))
	bootstrap := func(context.Context, string, string) ([]byte, error) {
		return nil, nil
	}
	var out, errOut bytes.Buffer
	if code := runContinuityBlockCommandWithDirectory([]string{"/session/main-checkout"}, &out, &errOut, bootstrap); code != 0 {
		t.Fatalf("continuity-block exit=%d stderr=%q", code, errOut.String())
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("continuity-block unresolved output=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestContinuityBlockRefusesArgumentCountsOtherThanOne(t *testing.T) {
	bootstrap := func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("bootstrap must not run")
	}
	for _, args := range [][]string{nil, {"one", "two"}} {
		var out, errOut bytes.Buffer
		if code := runContinuityBlockCommandWithDirectory(args, &out, &errOut, bootstrap); code != 2 {
			t.Fatalf("continuity-block %v exit=%d stderr=%q", args, code, errOut.String())
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), "supply exactly one session directory") {
			t.Fatalf("continuity-block %v output=%q stderr=%q", args, out.String(), errOut.String())
		}
	}
}

func TestContinuityBlockReadFailureIsSilentOnStdout(t *testing.T) {
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "fixture.db"))
	bootstrap := func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("continuity read failed")
	}
	var out, errOut bytes.Buffer
	if code := runContinuityBlockCommandWithDirectory([]string{"/session/worktree"}, &out, &errOut, bootstrap); code == 0 {
		t.Fatal("continuity-block succeeded after continuity read failure")
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "continuity read failed") {
		t.Fatalf("continuity-block failure output=%q stderr=%q", out.String(), errOut.String())
	}
}

// Continuity reads recorded state only, so a session boots with its
// worktree's Git hidden.
func TestContinuityBlockRecoversWhenWorktreeGitIsUnavailable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "session.db")
	t.Setenv(dbOverrideEnv, dbPath)
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, initLocatorRepo(t))
	started, err := s.BootstrapWorktree(t.Context(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hideGitDir(t, started.Entry.Path))
	var out, errOut bytes.Buffer
	if code := run([]string{"continuity-block", started.Entry.Path}, &out, &errOut); code != 0 {
		t.Fatalf("session recovery exit=%d stderr=%q", code, errOut.String())
	}
	if err := sessionboot.Validate(out.Bytes()); err != nil {
		t.Fatalf("invalid session boot: %v", err)
	}
	var boot sessionboot.Packet
	if err := json.Unmarshal(out.Bytes(), &boot); err != nil || boot.WorkID != started.WorkID {
		t.Fatalf("session boot lost work identity: %s, %v", out.Bytes(), err)
	}
}

// hostCommandAt returns a sessionHostCommandFunc that reports the supplied
// resolution, standing in for what hostSessionPrepareRegistry resolves in
// production.
func hostCommandAt(host hostCommandResolution) sessionHostCommandFunc {
	return func(context.Context, string) (hostCommandResolution, error) { return host, nil }
}

// defaultHostResolution is the resolution tests inject when the test is not
// about the registry: the bare probe command and no registry, which only a
// stubbed orchestrator may observe.
func defaultHostResolution() hostCommandResolution {
	return hostCommandResolution{Command: append([]string(nil), defaultHostCommand...)}
}

// registryResolutionFor builds the registry resolution whose document
// declares each supplied handle as an enabled primary agent. Tests that are
// not about registration state use it to say "the host registers what the
// definition derives", so a registration refusal never masquerades as the
// failure under test.
func registryResolutionFor(handles ...string) hostCommandResolution {
	agents := make(map[string]hostAgentEntry, len(handles))
	for _, handle := range handles {
		agents[handle] = hostAgentEntry{Mode: "primary"}
	}
	document, err := json.Marshal(hostConfigDocument{Agent: agents})
	if err != nil {
		panic(err)
	}
	return hostCommandResolution{Command: append([]string(nil), defaultHostCommand...), Registry: document}
}

func TestOrchestratorIdentityRefusalRecordsNothing(t *testing.T) {
	t.Run("definition absent", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		dbPath := filepath.Join(t.TempDir(), "concord-absent.db")
		t.Setenv(dbOverrideEnv, dbPath)
		_, err := hostOrchestratorIdentity(context.Background(), cwd, registryResolutionFor("concord-1"), "concord-1")
		if err == nil {
			t.Fatal("absent definition passed identity verification")
		}
		stderr := err.Error()
		for _, fragment := range []string{
			"required agent identity is absent",
			agentDefinitionFileName("concord-1"),
			filepath.Join(cwd, ".opencode", "agents"),
			filepath.Join(home, ".config", "opencode", "agents"),
		} {
			if !strings.Contains(stderr, fragment) {
				t.Fatalf("diagnostic missing %q: %q", fragment, stderr)
			}
		}
		if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("database was created at %q even though verification failed; the absent path must not touch the store", dbPath)
		}
	})
	t.Run("registry does not register the handle", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), agentDefinitionFileName("concord-1"))
		t.Setenv("HOME", home)
		dbPath := filepath.Join(t.TempDir(), "concord-unregistered.db")
		t.Setenv(dbOverrideEnv, dbPath)
		_, err := hostOrchestratorIdentity(context.Background(), cwd, registryResolutionFor("some-other-agent"), "concord-1")
		if err == nil {
			t.Fatal("an unregistered handle passed identity verification")
		}
		for _, fragment := range []string{"concord-1", "not registered"} {
			if !strings.Contains(err.Error(), fragment) {
				t.Fatalf("diagnostic %q omits %q", err.Error(), fragment)
			}
		}
		if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("database created at %q despite a refused registration check", dbPath)
		}
	})
}
