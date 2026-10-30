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

// orchestratorInvocationHandle returns the name the host registers the
// resolved definition under: the frontmatter `name:` value when the
// definition carries one, else the file stem. Renaming via `name:` is not
// an alias — the stem stops resolving — so selection by stem alone silently
// falls back to the operator's default agent on a renamed definition.
// The scan is a conservative subset of the host's YAML: a `name:` line at
// column 0 inside the leading frontmatter block, value trimmed, symmetric
// quotes stripped. Anything else reads as the stem.
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
// record evidence for an agent the session is not (CD-0049 D2).
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

// agentSearchDirectories lists the directories a definition may resolve from,
// nearest last, skipping any the caller cannot supply. dir is the session
// directory (CD-0093 D2): the resolved Project directory for `concord
// session`, the verified worktree for session-prepare. An empty result is
// legal and resolves nothing, which is the fail-closed outcome.
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

// verifyOrchestratorIdentity asserts that the active host agent's definition
// resolves in the searched directories, that the host registers it under the
// same name the session runs as, and returns that handle.
//
// An absent or unresolvable definition is a typed failure naming the
// required identity, the observed absence, and the paths searched, because
// CD-0049 D4 admits no degraded start. A definition whose frontmatter
// `name:` differs from the requested agent is a typed mismatch for the same
// reason: the host would substitute the operator's default agent
// (CD-0049 D2). The returned handle equals agent on success.
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

// firstAgentDefinition returns the first searched directory that contains a
// regular file named fileName. When no directory supplies the file, the
// returned error is the typed agentIdentityAbsentError session preparation
// surfaces unchanged.
//
// fileName derives from the caller-validated agent name: session-prepare
// matches it against ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$ before this runs, so
// it cannot carry a path separator or traversal.
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
