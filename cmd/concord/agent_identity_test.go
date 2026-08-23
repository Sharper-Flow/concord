package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
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

func TestLaneAgentIdentityResolvesFromEitherSearchedDirectory(t *testing.T) {
	lanes := store.BuiltinLaneDefinitions()
	if len(lanes) == 0 {
		t.Fatal("no registered lanes")
	}
	for _, location := range []string{"global", "project"} {
		t.Run(location, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			dir := filepath.Join(cwd, ".opencode", "agents")
			if location == "global" {
				dir = filepath.Join(home, ".config", "opencode", "agents")
			}
			for _, lane := range lanes {
				writeAgentDefinition(t, dir, laneAgentFileName(lane.ID))
			}
			if err := verifyLaneAgentIdentity(home, cwd, lanes); err != nil {
				t.Fatalf("identity = %v, want nil", err)
			}
		})
	}
}

func TestLaneAgentIdentityNamesEveryMissingDefinitionAndSearchedDirectory(t *testing.T) {
	lanes := store.BuiltinLaneDefinitions()
	home, cwd := t.TempDir(), t.TempDir()
	present, absent := lanes[0], lanes[1:]
	writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(present.ID))

	err := verifyLaneAgentIdentity(home, cwd, lanes)
	if err == nil {
		t.Fatal("identity = nil, want absent-identity failure")
	}
	message := err.Error()
	if strings.Contains(message, laneAgentFileName(present.ID)+";") {
		t.Fatalf("present lane reported missing: %q", message)
	}
	for _, lane := range absent {
		if !strings.Contains(message, laneAgentFileName(lane.ID)) {
			t.Fatalf("missing lane %q absent from failure: %q", lane.ID, message)
		}
	}
	for _, dir := range []string{
		filepath.Join(home, ".config", "opencode", "agents"),
		filepath.Join(cwd, ".opencode", "agents"),
	} {
		if !strings.Contains(message, dir) {
			t.Fatalf("searched directory %q absent from failure: %q", dir, message)
		}
	}
}

func TestLaneAgentIdentityFailsClosedWithoutSearchableDirectories(t *testing.T) {
	lanes := store.BuiltinLaneDefinitions()
	err := verifyLaneAgentIdentity("", "", lanes)
	if err == nil {
		t.Fatal("identity = nil, want absent-identity failure with no searchable directory")
	}
	for _, lane := range lanes {
		if !strings.Contains(err.Error(), laneAgentFileName(lane.ID)) {
			t.Fatalf("lane %q absent from failure: %q", lane.ID, err.Error())
		}
	}
}

func TestLaneAgentIdentityRejectsADirectoryNamedLikeADefinition(t *testing.T) {
	lanes := store.BuiltinLaneDefinitions()
	home, cwd := t.TempDir(), t.TempDir()
	for _, lane := range lanes {
		path := filepath.Join(cwd, ".opencode", "agents", laneAgentFileName(lane.ID))
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyLaneAgentIdentity(home, cwd, lanes); err == nil {
		t.Fatal("identity = nil, want failure when the definition path is not a regular file")
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
			writeAgentDefinition(t, dir, orchestratorAgentFileName)
			assertion, err := verifyOrchestratorIdentity(home, cwd)
			if err != nil {
				t.Fatalf("identity = %v, want nil", err)
			}
			if assertion.Type != OrchestratorIdentityType {
				t.Errorf("type = %q, want %q", assertion.Type, OrchestratorIdentityType)
			}
			if assertion.Version != OrchestratorIdentityVersion {
				t.Errorf("version = %q, want %q", assertion.Version, OrchestratorIdentityVersion)
			}
			if len(assertion.Sources) == 0 {
				t.Fatalf("assertion sources empty; expected at least the orchestrator definition")
			}
			if assertion.Sources[0].Kind != "orchestrator_definition" {
				t.Errorf("first source kind = %q, want orchestrator_definition", assertion.Sources[0].Kind)
			}
		})
	}
}

func TestOrchestratorIdentityNamesAbsentDefinitionAndSearchedDirectories(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	_, err := verifyOrchestratorIdentity(home, cwd)
	if err == nil {
		t.Fatal("identity = nil, want absent-identity failure")
	}
	message := err.Error()
	for _, fragment := range []string{
		orchestratorAgentFileName,
		filepath.Join(home, ".config", "opencode", "agents"),
		filepath.Join(cwd, ".opencode", "agents"),
	} {
		if !strings.Contains(message, fragment) {
			t.Fatalf("diagnostic missing %q: %q", fragment, message)
		}
	}
}

func TestOrchestratorIdentityDigestIsStableAcrossInvocations(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	dir := filepath.Join(cwd, ".opencode", "agents")
	writeAgentDefinition(t, dir, orchestratorAgentFileName)
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# project instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := verifyOrchestratorIdentity(home, cwd)
	if err != nil {
		t.Fatalf("first verify: %v", err)
	}
	second, err := verifyOrchestratorIdentity(home, cwd)
	if err != nil {
		t.Fatalf("second verify: %v", err)
	}
	if first.RulesetDigest != second.RulesetDigest {
		t.Fatalf("digest changed across invocations on the same files: %q vs %q", first.RulesetDigest, second.RulesetDigest)
	}
	if !strings.HasPrefix(first.RulesetDigest, "sha256:") {
		t.Fatalf("digest = %q, want sha256:<hex>", first.RulesetDigest)
	}
}

func TestOrchestratorIdentityDigestChangesWhenArtifactChanges(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	dir := filepath.Join(cwd, ".opencode", "agents")
	writeAgentDefinition(t, dir, orchestratorAgentFileName)
	agentsPath := filepath.Join(cwd, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# instructions v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := verifyOrchestratorIdentity(home, cwd)
	if err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if err := os.WriteFile(agentsPath, []byte("# instructions v2 — silently changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := verifyOrchestratorIdentity(home, cwd)
	if err != nil {
		t.Fatalf("second verify: %v", err)
	}
	if first.RulesetDigest == second.RulesetDigest {
		t.Fatalf("digest unchanged after AGENTS.md mutation: %q", second.RulesetDigest)
	}
}
