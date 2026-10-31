package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
