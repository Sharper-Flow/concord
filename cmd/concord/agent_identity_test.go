package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgentDefinition(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("---\nmode: subagent\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeAgentDefinitionBody writes a definition file whose exact bytes the
// caller supplies, for handle-derivation cases the default helper cannot
// express.
func writeAgentDefinitionBody(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOrchestratorIdentityResolvesFromEitherSearchedDirectory(t *testing.T) {
	for _, location := range []string{"global", "project"} {
		t.Run(location, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			dir := filepath.Join(cwd, ".opencode", "agents")
			if location == "global" {
				dir = filepath.Join(home, ".config", "opencode", "agents")
			}
			writeAgentDefinition(t, dir, agentDefinitionFileName("concord-1"))
			handle, err := verifyOrchestratorIdentity(home, cwd, "concord-1")
			if err != nil {
				t.Fatalf("identity = %v, want nil", err)
			}
			if handle != "concord-1" {
				t.Errorf("handle = %q, want concord-1", handle)
			}
		})
	}
}

func TestOrchestratorIdentityNamesAbsentDefinitionAndSearchedDirectories(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	_, err := verifyOrchestratorIdentity(home, cwd, "concord-1")
	if err == nil {
		t.Fatal("identity = nil, want absent-identity failure")
	}
	message := err.Error()
	for _, fragment := range []string{
		agentDefinitionFileName("concord-1"),
		filepath.Join(home, ".config", "opencode", "agents"),
		filepath.Join(cwd, ".opencode", "agents"),
	} {
		if !strings.Contains(message, fragment) {
			t.Fatalf("diagnostic missing %q: %q", fragment, message)
		}
	}
}

// A definition whose frontmatter `name:` differs from the requested agent
// registers under a different handle. The session runs as the requested
// agent, so the verification must refuse instead of asserting an agent the
// session is not (CD-0049 D2).
func TestOrchestratorIdentityRefusesADefinitionThatRegistersADifferentHandle(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeAgentDefinitionBody(t, filepath.Join(cwd, ".opencode", "agents"), agentDefinitionFileName("concord-1"),
		[]byte("---\nname: op-renamed\nmode: all\n---\nbody\n"))
	_, err := verifyOrchestratorIdentity(home, cwd, "concord-1")
	if err == nil {
		t.Fatal("identity = nil, want handle-mismatch failure")
	}
	var mismatch *agentIdentityMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %T (%v), want *agentIdentityMismatchError", err, err)
	}
	if mismatch.Handle != "op-renamed" || mismatch.Requested != "concord-1" {
		t.Fatalf("mismatch = %+v, want handle op-renamed for requested concord-1", mismatch)
	}
}

// The invocation handle is the name the host registers the definition under.
// Frontmatter `name:` renames the handle rather than adding an alias, so the
// handle must come from the resolved file, not from the file stem: selecting
// the stem of a renamed definition silently starts the operator's default
// agent.
func TestOrchestratorInvocationHandleFollowsFrontmatterName(t *testing.T) {
	const stem = "concord-1"
	cases := []struct {
		name string
		body string
		want string
	}{
		{"no frontmatter name uses the stem", "---\nmode: all\n---\nbody\n", stem},
		{"frontmatter name overrides the stem", "---\nname: op-renamed\nmode: all\n---\nbody\n", "op-renamed"},
		{"quoted frontmatter name is unquoted", "---\nname: \"quoted name\"\n---\nbody\n", "quoted name"},
		{"name after the frontmatter block is body text", "---\nmode: all\n---\nname: body-name\n", stem},
		{"no frontmatter block at all", "name: body-name\n", stem},
		{"empty name value falls back to the stem", "---\nname:\n---\nbody\n", stem},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			dir := filepath.Join(cwd, ".opencode", "agents")
			writeAgentDefinitionBody(t, dir, agentDefinitionFileName(stem), []byte(tc.body))
			resolved := filepath.Join(dir, agentDefinitionFileName(stem))
			if handle := orchestratorInvocationHandle(resolved); handle != tc.want {
				t.Fatalf("handle = %q, want %q", handle, tc.want)
			}
		})
	}
}
