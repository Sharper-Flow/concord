package linearclient

import (
	"context"
)

// WorkspaceInventory is the remote read used by setup preflight and readback.
// It carries IDs with display metadata so callers never identify objects by
// name alone.
type WorkspaceInventory struct {
	WorkspaceID string          `json:"workspace_id"`
	Teams       []RemoteTeam    `json:"teams"`
	Projects    []RemoteProject `json:"projects"`
}

type RemoteTeam struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Key    string         `json:"key"`
	States []RemoteStatus `json:"states"`
}

type RemoteProject struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	Team RemoteTeamRef `json:"team"`
}

type RemoteTeamRef struct {
	ID string `json:"id"`
}

type RemoteStatus struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Inventory reads all teams, workflow states, and projects in the workspace.
// Setup code must compare the returned IDs with its explicit manifest before
// it performs any remote write.
func (c *Client) Inventory(ctx context.Context) (WorkspaceInventory, error) {
	var payload struct {
		Organization struct {
			ID string `json:"id"`
		} `json:"organization"`
		Teams struct {
			Nodes []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Key    string `json:"key"`
				States struct {
					Nodes []RemoteStatus `json:"nodes"`
				} `json:"states"`
			} `json:"nodes"`
		} `json:"teams"`
		Projects struct {
			Nodes []RemoteProject `json:"nodes"`
		} `json:"projects"`
	}
	if err := c.call(ctx, `query LinearSetupInventory { organization { id } teams { nodes { id name key states { nodes { id name type } } } } projects { nodes { id name team { id } } } }`, nil, &payload); err != nil {
		return WorkspaceInventory{}, err
	}
	inventory := WorkspaceInventory{WorkspaceID: payload.Organization.ID, Teams: make([]RemoteTeam, 0, len(payload.Teams.Nodes)), Projects: payload.Projects.Nodes}
	for _, team := range payload.Teams.Nodes {
		inventory.Teams = append(inventory.Teams, RemoteTeam{ID: team.ID, Name: team.Name, Key: team.Key, States: team.States.Nodes})
	}
	return inventory, nil
}
