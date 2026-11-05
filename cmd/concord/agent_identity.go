package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// agentDefinitionFileName names the definition file an agent resolves to:
// the agent's invocation handle plus the host definition extension.
func agentDefinitionFileName(agent string) string {
	return agent + ".md"
}

// orchestratorInvocationHandle uses the frontmatter name or the file stem.
// A renamed definition no longer resolves by stem; selection by stem makes the
// host substitute its default. The scan accepts a column-zero name in leading frontmatter,
// trims its value and strips symmetric quotes; other forms use the stem.
func orchestratorInvocationHandle(resolved string) string {
	stem := strings.TrimSuffix(filepath.Base(resolved), ".md")
	data, err := os.ReadFile(resolved) //nolint:gosec // resolved is an operator-owned host agent definition selected from the fixed search roots.
	if err != nil {
		return stem
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return stem
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "---" {
			break
		}
		value, ok := strings.CutPrefix(trimmed, "name:")
		if !ok {
			continue
		}
		name := strings.TrimSpace(value)
		if len(name) >= 2 && (name[0] == '"' && name[len(name)-1] == '"' || name[0] == '\'' && name[len(name)-1] == '\'') {
			name = name[1 : len(name)-1]
		}
		if name != "" {
			return name
		}
	}
	return stem
}

// agentIdentityMismatchError reports a definition that resolves for the
// requested agent but registers under a different invocation handle. The
// session runs as the requested agent, so asserting the derived handle would
// record evidence for an agent the session is not.
type agentIdentityMismatchError struct {
	Requested  string
	Handle     string
	Definition string
}

func (e *agentIdentityMismatchError) Error() string {
	return fmt.Sprintf(
		"agent definition registers a different handle: definition %s registers %q, but the session runs as %q",
		e.Definition, e.Handle, e.Requested,
	)
}

// agentIdentityAbsentError reports required agent identity that no searched
// directory supplies. It names what was required, what was missing, and where
// Concord looked, because the host resolves an unknown agent by silently
// substituting its default rather than failing.
type agentIdentityAbsentError struct {
	Required []string
	Missing  []string
	Searched []string
}

func (e *agentIdentityAbsentError) Error() string {
	searched := "no searchable directory"
	if len(e.Searched) > 0 {
		searched = strings.Join(e.Searched, ", ")
	}
	return fmt.Sprintf(
		"required agent identity is absent: %s; required: %s; searched: %s",
		strings.Join(e.Missing, ", "),
		strings.Join(e.Required, ", "),
		searched,
	)
}

// agentSearchDirectories searches nearest last. dir is the resolved Project
// directory or verified worktree; an empty result resolves nothing.
func agentSearchDirectories(home, dir string) []string {
	dirs := make([]string, 0, 2)
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", "opencode", "agents"))
	}
	if dir != "" {
		dirs = append(dirs, filepath.Join(dir, ".opencode", "agents"))
	}
	return dirs
}

// verifyOrchestratorIdentity requires the requested agent's definition and
// matching invocation handle. Absence or mismatch is a typed failure; there is
// no degraded start and the host otherwise substitutes its default agent.
func verifyOrchestratorIdentity(home, dir, agent string) (string, error) {
	dirs := agentSearchDirectories(home, dir)
	fileName := agentDefinitionFileName(agent)
	resolved, err := firstAgentDefinition(dirs, fileName)
	if err != nil {
		return "", err
	}
	handle := orchestratorInvocationHandle(resolved)
	if handle != agent {
		return "", &agentIdentityMismatchError{
			Requested:  agent,
			Handle:     handle,
			Definition: resolved,
		}
	}
	return handle, nil
}

// firstAgentDefinition returns the first matching regular file or a typed absence.
// The caller validates fileName's agent name, excluding traversal and separators.
func firstAgentDefinition(dirs []string, fileName string) (string, error) {
	resolved := ""
	for _, dir := range dirs {
		candidate := filepath.Join(dir, fileName)
		info, err := os.Stat(candidate) //nolint:gosec // candidate joins a fixed search root with a caller-validated agent name (see above); no traversal is reachable.
		if err == nil && info.Mode().IsRegular() {
			resolved = candidate
			break
		}
	}
	if resolved == "" {
		return "", &agentIdentityAbsentError{
			Required: []string{fileName},
			Missing:  []string{fileName},
			Searched: dirs,
		}
	}
	return resolved, nil
}
