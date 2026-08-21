package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sharper-flow/concord/internal/store"
)

// laneAgentFileName names the definition file a registered lane resolves to.
// The name and the directories searched by verifyLaneAgentIdentity mirror the
// candidates adapter/opencode/dispatch.ts hashes for CD-0034 host provenance;
// the two must agree or a session can start on a definition the provenance
// record cannot cite.
func laneAgentFileName(laneID string) string {
	return "concord-" + laneID + ".md"
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
// nearest last, skipping any the caller cannot supply. An empty result is
// legal and resolves nothing, which is the fail-closed outcome.
func agentSearchDirectories(home, cwd string) []string {
	dirs := make([]string, 0, 2)
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", "opencode", "agents"))
	}
	if cwd != "" {
		dirs = append(dirs, filepath.Join(cwd, ".opencode", "agents"))
	}
	return dirs
}

// verifyLaneAgentIdentity asserts that every registered lane resolves to a
// definition file before a session starts. CD-0049 D2 places this assertion in
// Concord because `opencode run --agent` answers an unknown name by falling
// back to the operator's default agent and exiting zero, so the host cannot
// report the absence. CD-0049 D4 admits no degraded start.
func verifyLaneAgentIdentity(home, cwd string, lanes []store.LaneDefinition) error {
	dirs := agentSearchDirectories(home, cwd)
	required := make([]string, 0, len(lanes))
	missing := make([]string, 0, len(lanes))
	for _, lane := range lanes {
		name := laneAgentFileName(lane.ID)
		required = append(required, name)
		if !resolvesToDefinition(dirs, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &agentIdentityAbsentError{Required: required, Missing: missing, Searched: dirs}
}

func resolvesToDefinition(dirs []string, name string) bool {
	for _, dir := range dirs {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}
