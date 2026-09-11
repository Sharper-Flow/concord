package store

import (
	"fmt"
)

// LinearSetupConfig is the versioned local declaration consumed by setup and
// migration tooling. Remote IDs are evidence, not display-name lookups.
type LinearSetupConfig struct {
	SchemaVersion string                 `json:"schema_version"`
	WorkspaceID   string                 `json:"workspace_id"`
	Teams         []LinearTeamBinding    `json:"teams"`
	Projects      []LinearProjectBinding `json:"projects"`
	StatusPolicy  LinearStatusPolicy     `json:"status_policy"`
}

type LinearTeamBinding struct {
	ProductID string `json:"product_id"`
	TeamID    string `json:"team_id"`
}

type LinearProjectBinding struct {
	ProjectID       string `json:"project_id"`
	LinearProjectID string `json:"linear_project_id"`
	TeamID          string `json:"team_id"`
}

type LinearStatusPolicy struct {
	CompletedID  string `json:"completed_id"`
	CancelledID  string `json:"cancelled_id"`
	SupersededID string `json:"superseded_id"`
	DuplicateID  string `json:"duplicate_id"`
}

const LinearSetupSchemaVersion = "linear-setup.v1"

// ValidateLinearSetupConfig checks identity and status invariants before a
// setup operation can read or write a remote workspace.
func ValidateLinearSetupConfig(config LinearSetupConfig) error {
	if config.SchemaVersion != LinearSetupSchemaVersion {
		return newFailure(KindInvalidPayload, "linear_setup_validate", "setup schema version is not recognized", false, "use linear-setup.v1")
	}
	if config.WorkspaceID == "" {
		return newFailure(KindInvalidPayload, "linear_setup_validate", "workspace id is required", false, "supply the verified workspace id")
	}
	if len(config.Teams) == 0 || len(config.Teams) > 32 {
		return newFailure(KindInvalidPayload, "linear_setup_validate", "teams must contain 1 to 32 bindings", false, "supply one binding for each Product")
	}
	seenProducts := make(map[string]bool, len(config.Teams))
	seenTeams := make(map[string]bool, len(config.Teams))
	for _, team := range config.Teams {
		if team.ProductID == "" || team.TeamID == "" {
			return newFailure(KindInvalidPayload, "linear_setup_validate", "team bindings require Product and remote team ids", false, "supply both ids")
		}
		if seenProducts[team.ProductID] || seenTeams[team.TeamID] {
			return newFailure(KindAmbiguousScope, "linear_setup_validate", "team bindings must be unique", false, "declare one remote team for each Product")
		}
		seenProducts[team.ProductID] = true
		seenTeams[team.TeamID] = true
	}
	seenProjects := make(map[string]bool, len(config.Projects))
	for _, project := range config.Projects {
		if project.ProjectID == "" || project.LinearProjectID == "" || project.TeamID == "" {
			return newFailure(KindInvalidPayload, "linear_setup_validate", "project bindings require local project, remote project, and team ids", false, "supply all binding ids")
		}
		if !seenTeams[project.TeamID] {
			return newFailure(KindAmbiguousScope, "linear_setup_validate", fmt.Sprintf("project %s names an undeclared team", project.ProjectID), false, "bind the project to one declared team")
		}
		if seenProjects[project.ProjectID] {
			return newFailure(KindAmbiguousScope, "linear_setup_validate", "project bindings must be unique", false, "declare one remote project for each local Project")
		}
		seenProjects[project.ProjectID] = true
	}
	if config.StatusPolicy.CompletedID == "" || config.StatusPolicy.CancelledID == "" || config.StatusPolicy.SupersededID == "" || config.StatusPolicy.DuplicateID == "" {
		return newFailure(KindInvalidPayload, "linear_setup_validate", "all four status ids are required", false, "declare completed, cancelled, superseded, and duplicate ids")
	}
	statusIDs := map[string]bool{}
	for _, statusID := range []string{config.StatusPolicy.CompletedID, config.StatusPolicy.CancelledID, config.StatusPolicy.SupersededID, config.StatusPolicy.DuplicateID} {
		if statusIDs[statusID] {
			return newFailure(KindInvalidPayload, "linear_setup_validate", "terminal status ids must be distinct", false, "keep Done, Canceled, Superseded, and Duplicate separate")
		}
		statusIDs[statusID] = true
	}
	return nil
}
