package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// projectToolingManifestPath is the per-project record of declared checks
// that tooling and footprint validation read.
const projectToolingManifestPath = ".concord/tooling.v1.json"

// ProjectDeclaredTool is one declared check: the tool's stable id and its
// verbatim invocation.
type ProjectDeclaredTool struct {
	ID         string
	Invocation string
}

// ProjectToolingManifest is a parsed tooling declaration.
type ProjectToolingManifest struct {
	Project     string
	Declared    []ProjectDeclaredTool
	DomainShape *DomainShapeTooling
}

// DomainShapeTooling references existing tool IDs; it grants no execution authority.
// RequiredChecks declares membership, not successful execution of those checks.
type DomainShapeTooling struct {
	BoundaryCheck       string   `json:"boundary_check,omitempty"`
	DataObjectExtractor string   `json:"data_object_extractor,omitempty"`
	ConfigExtractor     string   `json:"config_extractor,omitempty"`
	ReferenceCheck      string   `json:"reference_check,omitempty"`
	RequiredChecks      []string `json:"required_checks,omitempty"`
}

var projectToolID = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

func validateDomainShapeTooling(shape *DomainShapeTooling, tools []ProjectDeclaredTool) error {
	if shape == nil {
		return nil
	}
	declared := map[string]bool{}
	for _, tool := range tools {
		declared[tool.ID] = true
	}
	for _, id := range []string{shape.BoundaryCheck, shape.DataObjectExtractor, shape.ConfigExtractor, shape.ReferenceCheck} {
		if id != "" && !declared[id] {
			return fmt.Errorf("domain_shape references undeclared tool %q", id)
		}
	}
	if len(shape.RequiredChecks) > 64 {
		return fmt.Errorf("domain_shape required_checks exceeds 64 entries")
	}
	seen := map[string]bool{}
	for _, id := range shape.RequiredChecks {
		if !declared[id] || seen[id] {
			return fmt.Errorf("domain_shape required_checks must name distinct declared tools: %q", id)
		}
		seen[id] = true
	}
	return nil
}

// ParseProjectToolingManifest reads the declared checks out of a
// .concord/tooling.v1.json document. It admits only a current manifest whose
// tools each carry a plain invocation: one that splits to nothing declares no
// argv at all.
func ParseProjectToolingManifest(raw []byte) (*ProjectToolingManifest, error) {
	var document struct {
		SchemaVersion string          `json:"schema_version"`
		Project       string          `json:"project"`
		DomainShape   json.RawMessage `json:"domain_shape"`
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
	if document.Tools == nil || len(document.Tools) > 64 || (len(document.Tools) == 0 && document.DomainShape == nil) {
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
	shape, err := parseDomainShapeTooling(document.DomainShape, declared)
	if err != nil {
		return nil, err
	}
	return &ProjectToolingManifest{Project: document.Project, Declared: declared, DomainShape: shape}, nil
}

func parseDomainShapeTooling(raw json.RawMessage, declared []ProjectDeclaredTool) (*DomainShapeTooling, error) {
	if raw == nil {
		return nil, nil
	}
	shape := &DomainShapeTooling{}
	if err := decodeStrictKnowledgeJSON(raw, shape); err != nil {
		return nil, err
	}
	if err := domainShapeJSON(raw, shape); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, key := range []string{"boundary_check", "data_object_extractor", "config_extractor", "reference_check"} {
		if value, exists := fields[key]; exists {
			var id string
			if json.Unmarshal(value, &id) != nil || !projectToolID.MatchString(id) {
				return nil, fmt.Errorf("domain_shape %s must name a tool id", key)
			}
		}
	}
	if err := validateDomainShapeTooling(shape, declared); err != nil {
		return nil, err
	}
	return shape, nil
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
