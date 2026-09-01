package main

import (
	"crypto/sha256"
	"encoding/hex"
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

// orchestratorAgentFileName is the host-side definition Concord requires to
// start an orchestrator session. CD-0061 D5 fixes the name and the searched
// directories so the provenance record and the assertion agree.
const orchestratorAgentFileName = "concord-orchestrator.md"

// orchestratorAgentName is the fallback name the session selects when the
// definition carries no `name:` frontmatter. The host takes an agent's
// invocation handle from its frontmatter `name:` when present and from the
// definition file stem otherwise: a session that selects any other string
// resolves to the operator's default agent and starts anyway, which is the
// substitution CD-0049 D2 names.
var orchestratorAgentName = strings.TrimSuffix(orchestratorAgentFileName, ".md")

// orchestratorInvocationHandle returns the name the host registers the
// resolved definition under: the frontmatter `name:` value when the
// definition carries one, else the file stem. Renaming via `name:` is not an
// alias — the stem stops resolving — so selection by stem alone silently
// falls back to the operator's default agent on a renamed definition.
// The scan is a conservative subset of the host's YAML: a `name:` line at
// column 0 inside the leading frontmatter block, value trimmed, symmetric
// quotes stripped. Anything else reads as the stem.
func orchestratorInvocationHandle(resolved string) string {
	data, err := os.ReadFile(resolved) //nolint:gosec // resolved is an operator-owned host agent definition selected from the fixed search roots.
	if err != nil {
		return orchestratorAgentName
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return orchestratorAgentName
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
	return orchestratorAgentName
}

// OrchestratorIdentityType is the Concord-owned role constant the assertion
// records. Naming a role is not authoring a persona; CD-0049 Invariant 4
// keeps persona authorship out of Concord.
const OrchestratorIdentityType = "orchestrator"

// OrchestratorIdentityVersion is the Concord-owned contract version this
// build requires. The operator has decided Concord picks this rather than
// reading it from the file or deriving it; CD-0061 D5 fixes that choice
// explicitly. The "1.0" shape matches the schema-version strings the rest
// of Concord uses (knowledge manifest schema, lane packet schema).
const OrchestratorIdentityVersion = "1.0"

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

// verifyLaneAgentIdentity asserts that every registered lane resolves to a
// definition file before a session starts. CD-0049 D2 places this assertion in
// Concord because `opencode run --agent` answers an unknown name by falling
// back to the operator's default agent and exiting zero, so the host cannot
// report the absence. CD-0049 D4 admits no degraded start.
func verifyLaneAgentIdentity(home, dir string, lanes []store.LaneDefinition) error {
	dirs := agentSearchDirectories(home, dir)
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

// resolvesToDefinition reports whether any directory in dirs contains a
// regular file named name.
func resolvesToDefinition(dirs []string, name string) bool {
	for _, dir := range dirs {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// manifestSeparator separates per-source lines in the ruleset digest
// manifest. The literal matches adapter/opencode/dispatch.ts
// computeHostPromptProvenance so the two sides agree on concatenation.
const manifestSeparator = "\n---\n"

// verifyOrchestratorIdentity asserts the orchestrator definition file
// resolves in the searched directories and returns the assertion it would
// record. The ruleset digest derives ONLY from artifacts actually resolved —
// the orchestrator definition file and the instruction chain the host loads —
// never from a declared or expected value (CD-0061 Invariant 4).
//
// An absent or unresolvable definition is a typed failure naming the
// required identity, the observed absence, and the paths searched, because
// CD-0049 D4 admits no degraded start. The returned handle is the name the
// host registers the resolved definition under; the session must select
// exactly it (CD-0049 D2).
func verifyOrchestratorIdentity(home, dir string) (store.OrchestratorIdentityAssertion, string, error) {
	dirs := agentSearchDirectories(home, dir)
	resolved, err := firstOrchestratorDefinition(dirs)
	if err != nil {
		return store.OrchestratorIdentityAssertion{}, "", err
	}
	handle := orchestratorInvocationHandle(resolved)
	sources := collectOrchestratorArtifactSources(resolved, dir)
	digest := computeOrchestratorRulesetDigest(sources)
	return store.OrchestratorIdentityAssertion{
		Type:          OrchestratorIdentityType,
		Version:       OrchestratorIdentityVersion,
		RulesetDigest: digest,
		Sources:       sources,
	}, handle, nil
}

// firstOrchestratorDefinition returns the first searched directory that
// contains a regular file named orchestratorAgentFileName. When no directory
// supplies the file, the returned error is the typed agentIdentityAbsentError
// the session command surfaces unchanged.
func firstOrchestratorDefinition(dirs []string) (string, error) {
	resolved := ""
	for _, dir := range dirs {
		candidate := filepath.Join(dir, orchestratorAgentFileName)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			resolved = candidate
			break
		}
	}
	if resolved == "" {
		return "", &agentIdentityAbsentError{
			Required: []string{orchestratorAgentFileName},
			Missing:  []string{orchestratorAgentFileName},
			Searched: dirs,
		}
	}
	return resolved, nil
}

// collectOrchestratorArtifactSources enumerates the host artifacts the
// orchestrator assertion actually resolved: the orchestrator definition
// file first, then the AGENTS.md chain at the session directory dir
// (up to 4 deep), then any paths
// declared via the CONCORD_HOST_INSTRUCTIONS environment variable (up to
// 16). Every entry contributes its filesystem path and content hash to the
// ruleset digest manifest.
//
// This mirrors adapter/opencode/dispatch.ts computeHostPromptProvenance so
// the two sides agree on the manifest construction, with one structural
// difference required by CD-0061 Invariant 4: dispatch records unenumerated
// surfaces by name, the orchestrator does not — its digest derives only from
// artifacts it actually opened and hashed.
func collectOrchestratorArtifactSources(definition, dir string) []store.OrchestratorArtifactSource {
	sources := make([]store.OrchestratorArtifactSource, 0, 4)
	if src, ok := hashOrchestratorSource("orchestrator_definition", definition); ok {
		sources = append(sources, src)
	}
	walk := dir
	for depth := 0; depth < 8 && countKind(sources, "agents_md") < 4; depth++ {
		candidate := filepath.Join(walk, "AGENTS.md")
		if src, ok := hashOrchestratorSource("agents_md", candidate); ok {
			sources = append(sources, src)
		}
		parent := filepath.Dir(walk)
		if parent == "" || parent == walk {
			break
		}
		walk = parent
	}
	declared := os.Getenv("CONCORD_HOST_INSTRUCTIONS")
	for _, p := range strings.Split(declared, ":") {
		if p == "" {
			continue
		}
		if src, ok := hashOrchestratorSource("instruction_file", p); ok {
			sources = append(sources, src)
		}
		if countKind(sources, "instruction_file") >= 16 {
			break
		}
	}
	return sources
}

// hashOrchestratorSource reads path and returns its kind+path+sha256 triple
// when the file is a regular file the host actually supplies. A missing or
// non-regular file returns ok=false so the caller skips it rather than
// recording a declared-but-absent artifact (CD-0061 Invariant 4).
func hashOrchestratorSource(kind, path string) (store.OrchestratorArtifactSource, bool) {
	info, err := os.Stat(path) //nolint:gosec // path is a fixed host artifact candidate or an operator-declared instruction file.
	if err != nil || !info.Mode().IsRegular() {
		return store.OrchestratorArtifactSource{}, false
	}
	data, err := os.ReadFile(path) //nolint:gosec // the regular-file check above guards the operator-owned host artifact path.
	if err != nil {
		return store.OrchestratorArtifactSource{}, false
	}
	sum := sha256.Sum256(data)
	return store.OrchestratorArtifactSource{
		Kind:   kind,
		Path:   path,
		SHA256: hex.EncodeToString(sum[:]),
	}, true
}

func countKind(sources []store.OrchestratorArtifactSource, kind string) int {
	n := 0
	for _, src := range sources {
		if src.Kind == kind {
			n++
		}
	}
	return n
}

// computeOrchestratorRulesetDigest returns the SHA-256 over the manifest
// Concord records for this assertion. Each source contributes one line of
// the form "<kind>\n<path>\n<sha256>"; sources are joined with manifestSeparator
// to match adapter/opencode/dispatch.ts. The digest format is sha256:<hex>,
// the same shape the lane provenance record and the worker report envelope
// use elsewhere in Concord.
func computeOrchestratorRulesetDigest(sources []store.OrchestratorArtifactSource) string {
	if len(sources) == 0 {
		return ""
	}
	parts := make([]string, 0, len(sources))
	for _, src := range sources {
		parts = append(parts, strings.Join([]string{src.Kind, src.Path, src.SHA256}, "\n"))
	}
	manifest := strings.Join(parts, manifestSeparator)
	sum := sha256.Sum256([]byte(manifest))
	return "sha256:" + hex.EncodeToString(sum[:])
}
