package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// projectToolingManifestPath is the per-project record of declared checks.
const projectToolingManifestPath = ".concord/tooling.v1.json"

// ProjectDeclaredTool is one declared check: the tool's stable id and its
// verbatim invocation.
type ProjectDeclaredTool struct {
	ID         string
	Invocation string
}

// ProjectToolingManifest is a parsed tooling declaration.
type ProjectToolingManifest struct {
	Project  string
	Declared []ProjectDeclaredTool
}

var projectToolID = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

// ParseProjectToolingManifest reads the declared checks out of a
// .concord/tooling.v1.json document. It admits only a current manifest whose
// tools each carry a plain invocation: one that splits to nothing declares no
// argv at all.
func ParseProjectToolingManifest(raw []byte) (*ProjectToolingManifest, error) {
	var document struct {
		SchemaVersion string `json:"schema_version"`
		Project       string `json:"project"`
		Tools         []struct {
			ID         string `json:"id"`
			Invocation string `json:"invocation"`
		} `json:"tools"`
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("tooling manifest exceeds 65536 bytes")
	}
	if err := payloadschema.ValidateUniqueJSON(raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest is not valid JSON: "+err.Error(), false, "repair "+projectToolingManifestPath+" on the default ref")
	}
	if document.SchemaVersion != "1.0" {
		return nil, newFailure(KindSchemaUnsupported, "project_tooling", fmt.Sprintf("tooling manifest schema_version must be \"1.0\", got %q", document.SchemaVersion), false, "declare schema_version \"1.0\" in "+projectToolingManifestPath)
	}
	if document.Project == "" {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest names no project", false, "declare the project identifier in "+projectToolingManifestPath)
	}
	if len(document.Tools) == 0 || len(document.Tools) > 64 {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest declares no tools", false, "declare at least one tool in "+projectToolingManifestPath)
	}
	declared := make([]ProjectDeclaredTool, 0, len(document.Tools))
	seen := map[string]bool{}
	for _, tool := range document.Tools {
		if !projectToolID.MatchString(tool.ID) || seen[tool.ID] {
			return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest declares a tool without an id", false, "declare every tool's stable id in "+projectToolingManifestPath)
		}
		seen[tool.ID] = true
		if !projectToolingPlainInvocation(tool.Invocation) {
			return nil, newFailure(KindInvalidPayload, "project_tooling", fmt.Sprintf("tooling manifest tool %q declares no argv", tool.ID), false, "declare a splittable invocation in "+projectToolingManifestPath)
		}
		declared = append(declared, ProjectDeclaredTool{ID: tool.ID, Invocation: tool.Invocation})
	}
	return &ProjectToolingManifest{Project: document.Project, Declared: declared}, nil
}

func projectToolingPlainInvocation(value string) bool {
	if len(value) == 0 || len(value) > 512 || len(strings.Fields(value)) == 0 || strings.ContainsAny(value, "'\"`\\$;|&<>()") {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
