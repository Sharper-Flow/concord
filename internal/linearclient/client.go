// Package linearclient is Concord's first-party Linear GraphQL client. It
// exists to serve the Phase 1 outbox drain (issue 990): create and update
// issues with a client-supplied UUID so an ambiguous outcome is re-drained
// under the same identity instead of blind-retried.
//
// The client makes exactly one attempt per call. Retries belong to the outbox,
// which is the durable retry owner; a transport failure here means the outcome
// is unknown, and the drain re-claims the operation on its next pass.
//
// Credential custody (CD-0121 D3): the API key is read from the process
// environment at client construction and never logged, persisted, or echoed in
// an error. The endpoint default is the public Linear GraphQL URL and may be
// overridden for tests.
package linearclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// EnvAPIKey names the process variable that carries the Linear API key.
const EnvAPIKey = "CONCORD_LINEAR_API_KEY"

// EnvEndpoint names the process variable that overrides the GraphQL endpoint.
const EnvEndpoint = "CONCORD_LINEAR_ENDPOINT"

// DefaultEndpoint is the public Linear GraphQL API.
const DefaultEndpoint = "https://api.linear.app/graphql"

// FailureKind classifies one client failure so the outbox drain can choose a
// retryable or permanent outcome.
type FailureKind string

const (
	// KindMissingCredential means no API key was provisioned.
	KindMissingCredential FailureKind = "missing_credential"
	// KindAuthRefused means Linear rejected the credential.
	KindAuthRefused FailureKind = "auth_refused"
	// KindRateLimited means Linear deferred the request; RetryAfter carries the
	// server-indicated wait.
	KindRateLimited FailureKind = "rate_limited"
	// KindTransport means the request never completed; the outcome is unknown.
	KindTransport FailureKind = "transport"
	// KindMalformedResponse means the response body did not decode.
	KindMalformedResponse FailureKind = "malformed_response"
	// KindGraphqlError means Linear answered with a GraphQL-level error or a
	// mutation that reported success=false.
	KindGraphqlError FailureKind = "graphql_error"
)

// Failure is the typed client error. Detail never contains key material.
type Failure struct {
	Kind       FailureKind
	Detail     string
	RetryAfter time.Duration
}

func (f *Failure) Error() string {
	return fmt.Sprintf("linearclient: %s: %s", f.Kind, f.Detail)
}

// CreateIssueInput carries the fields the drain supplies on issue creation. ID
// is the client UUID: Linear's IssueCreateInput.id, which makes a repeated
// create converge on the existing issue instead of duplicating it.
type CreateIssueInput struct {
	ID          string `json:"id"`
	TeamID      string `json:"teamId"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// UpdateIssueInput carries the mutable fields the drain synchronizes.
type UpdateIssueInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	StatusID    string `json:"stateId,omitempty"`
}

// Issue is the remote identity a completed operation records.
type Issue struct {
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	URL        string    `json:"url"`
	UpdatedAt  time.Time `json:"updatedAt"`
	TeamID     string    `json:"teamId,omitempty"`
	ProjectID  string    `json:"projectId,omitempty"`
	StateID    string    `json:"stateId,omitempty"`
}

// Destination is the provider readback used before a native write or link.
// It contains no credential material.
type Destination struct {
	WorkspaceURL string
	TeamID       string
	ProjectID    string
	StatusIDs    map[string]string
}

// IssueRead is the remote identity and destination of an existing issue.
type IssueRead struct {
	Issue
	WorkspaceURL string
}

// Client talks to one Linear workspace over GraphQL with Bearer auth.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

type option func(*Client)

// WithEndpoint overrides the GraphQL endpoint (tests).
func WithEndpoint(endpoint string) option {
	return func(c *Client) { c.endpoint = endpoint }
}

// New constructs a client for the given key. The key is never validated
// locally; an empty key is refused because the drain must not send an
// unauthenticated request.
func New(apiKey string, options ...option) (*Client, error) {
	if apiKey == "" {
		return nil, &Failure{Kind: KindMissingCredential, Detail: "no Linear API key was provisioned"}
	}
	client := &Client{endpoint: DefaultEndpoint, apiKey: apiKey, http: &http.Client{Timeout: 30 * time.Second}}
	for _, apply := range options {
		apply(client)
	}
	return client, nil
}

// FromEnv constructs the client from CONCORD_LINEAR_API_KEY and the optional
// CONCORD_LINEAR_ENDPOINT override.
func FromEnv() (*Client, error) {
	endpoint := os.Getenv(EnvEndpoint)
	if endpoint == "" {
		return New(os.Getenv(EnvAPIKey))
	}
	return New(os.Getenv(EnvAPIKey), WithEndpoint(endpoint))
}

type graphResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// CreateIssue executes issueCreate with the client UUID and returns the remote
// identity.
func (c *Client) CreateIssue(ctx context.Context, input CreateIssueInput) (Issue, error) {
	var payload struct {
		IssueCreate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := c.call(ctx, "mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier url updatedAt } } }", map[string]any{"input": input}, &payload); err != nil {
		return Issue{}, err
	}
	if !payload.IssueCreate.Success {
		return Issue{}, &Failure{Kind: KindGraphqlError, Detail: "issueCreate reported success=false"}
	}
	return payload.IssueCreate.Issue, nil
}

// UpdateIssue executes issueUpdate against the remote issue UUID.
func (c *Client) UpdateIssue(ctx context.Context, remoteUUID string, input UpdateIssueInput) (Issue, error) {
	var payload struct {
		IssueUpdate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueUpdate"`
	}
	if err := c.call(ctx, "mutation($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success issue { id identifier url updatedAt } } }", map[string]any{"id": remoteUUID, "input": input}, &payload); err != nil {
		return Issue{}, err
	}
	if !payload.IssueUpdate.Success {
		return Issue{}, &Failure{Kind: KindGraphqlError, Detail: "issueUpdate reported success=false"}
	}
	return payload.IssueUpdate.Issue, nil
}

// VerifyDestination reads the declared team, project, and workflow states.
// The provider response must agree with every declared binding before a write.
func (c *Client) VerifyDestination(ctx context.Context, expected Destination) error {
	if expected.TeamID == "" || expected.ProjectID == "" {
		return &Failure{Kind: KindGraphqlError, Detail: "Linear destination requires a team and project"}
	}
	var payload struct {
		Team *struct {
			ID           string `json:"id"`
			Organization *struct {
				URLKey string `json:"urlKey"`
			} `json:"organization"`
		} `json:"team"`
		Project *struct {
			ID   string `json:"id"`
			Team *struct {
				ID string `json:"id"`
			} `json:"team"`
		} `json:"project"`
		WorkflowStates struct {
			Nodes []struct {
				ID   string `json:"id"`
				Team *struct {
					ID string `json:"id"`
				} `json:"team"`
			} `json:"nodes"`
		} `json:"workflowStates"`
	}
	if err := c.call(ctx, `query($teamId: String!, $projectId: String!) { team(id: $teamId) { id organization { urlKey } } project(id: $projectId) { id team { id } } workflowStates { nodes { id team { id } } } }`, map[string]any{"teamId": expected.TeamID, "projectId": expected.ProjectID}, &payload); err != nil {
		return err
	}
	if payload.Team == nil || payload.Team.ID != expected.TeamID {
		return &Failure{Kind: KindGraphqlError, Detail: "Linear team does not match the declared destination"}
	}
	if expected.WorkspaceURL != "" && payload.Team.Organization != nil && payload.Team.Organization.URLKey != "" {
		workspace := "https://linear.app/" + payload.Team.Organization.URLKey
		if strings.TrimRight(expected.WorkspaceURL, "/") != workspace {
			return &Failure{Kind: KindGraphqlError, Detail: "Linear team does not belong to the declared workspace"}
		}
	}
	if payload.Project == nil || payload.Project.ID != expected.ProjectID || payload.Project.Team == nil || payload.Project.Team.ID != expected.TeamID {
		return &Failure{Kind: KindGraphqlError, Detail: "Linear project does not match the declared team"}
	}
	actual := make(map[string]bool, len(payload.WorkflowStates.Nodes))
	for _, state := range payload.WorkflowStates.Nodes {
		if state.Team != nil && state.Team.ID == expected.TeamID {
			actual[state.ID] = true
		}
	}
	for lifecycle, stateID := range expected.StatusIDs {
		if stateID == "" || !actual[stateID] {
			return &Failure{Kind: KindGraphqlError, Detail: "Linear status does not match the declared destination for " + lifecycle}
		}
	}
	return nil
}

// GetIssue reads an existing issue without changing it. It is the only client
// route used by native adoption.
func (c *Client) GetIssue(ctx context.Context, remoteUUID string) (IssueRead, error) {
	if remoteUUID == "" {
		return IssueRead{}, &Failure{Kind: KindGraphqlError, Detail: "remote issue uuid is required"}
	}
	var payload struct {
		Issue *struct {
			ID         string    `json:"id"`
			Identifier string    `json:"identifier"`
			URL        string    `json:"url"`
			UpdatedAt  time.Time `json:"updatedAt"`
			Team       *struct {
				ID           string `json:"id"`
				Organization *struct {
					URLKey string `json:"urlKey"`
				} `json:"organization"`
			} `json:"team"`
			Project *struct {
				ID string `json:"id"`
			} `json:"project"`
			State *struct {
				ID string `json:"id"`
			} `json:"state"`
		} `json:"issue"`
	}
	if err := c.call(ctx, `query($id: String!) { issue(id: $id) { id identifier url updatedAt team { id organization { urlKey } } project { id } state { id } } }`, map[string]any{"id": remoteUUID}, &payload); err != nil {
		return IssueRead{}, err
	}
	if payload.Issue == nil || payload.Issue.ID == "" {
		return IssueRead{}, &Failure{Kind: KindGraphqlError, Detail: "Linear issue was not found"}
	}
	read := IssueRead{Issue: Issue{ID: payload.Issue.ID, Identifier: payload.Issue.Identifier, URL: payload.Issue.URL, UpdatedAt: payload.Issue.UpdatedAt}}
	if payload.Issue.Team != nil {
		read.TeamID = payload.Issue.Team.ID
		if payload.Issue.Team.Organization != nil && payload.Issue.Team.Organization.URLKey != "" {
			read.WorkspaceURL = "https://linear.app/" + payload.Issue.Team.Organization.URLKey
		}
	}
	if payload.Issue.Project != nil {
		read.ProjectID = payload.Issue.Project.ID
	}
	if payload.Issue.State != nil {
		read.StateID = payload.Issue.State.ID
	}
	return read, nil
}

func (c *Client) call(ctx context.Context, query string, variables map[string]any, into any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return &Failure{Kind: KindMalformedResponse, Detail: "cannot encode request"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return &Failure{Kind: KindMalformedResponse, Detail: "cannot build request"}
	}
	request.Header.Set("Content-Type", "application/json")
	// Linear API keys ride the Authorization header without the Bearer prefix;
	// OAuth tokens use Bearer. Sending Bearer with an API key answers HTTP 400
	// with an explicit message (verified against the live API, 2026-09-09).
	request.Header.Set("Authorization", c.apiKey)
	response, err := c.http.Do(request)
	if err != nil {
		return &Failure{Kind: KindTransport, Detail: "request did not complete"}
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return &Failure{Kind: KindAuthRefused, Detail: "linear rejected the credential"}
	case response.StatusCode == http.StatusTooManyRequests:
		retryAfter, parseErr := time.ParseDuration(response.Header.Get("Retry-After") + "s")
		if parseErr != nil {
			retryAfter = 0
		}
		return &Failure{Kind: KindRateLimited, Detail: "linear deferred the request", RetryAfter: retryAfter}
	case response.StatusCode >= 400:
		// Linear reports rate limiting as HTTP 400 with a RATELIMITED extension
		// code, and every other 4xx carries a JSON error body that names the
		// cause. The body is evidence: surface it instead of the bare status.
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		if readErr != nil {
			return &Failure{Kind: KindGraphqlError, Detail: fmt.Sprintf("linear answered HTTP %d", response.StatusCode)}
		}
		var envelope graphResponse
		if json.Unmarshal(raw, &envelope) == nil && len(envelope.Errors) > 0 {
			messages := make([]string, 0, len(envelope.Errors))
			for _, item := range envelope.Errors {
				messages = append(messages, item.Message)
			}
			detail := strings.Join(messages, "; ")
			for _, item := range envelope.Errors {
				if strings.Contains(strings.ToUpper(item.Message), "RATELIMITED") {
					return &Failure{Kind: KindRateLimited, Detail: detail}
				}
			}
			return &Failure{Kind: KindGraphqlError, Detail: detail}
		}
		return &Failure{Kind: KindGraphqlError, Detail: fmt.Sprintf("linear answered HTTP %d", response.StatusCode)}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return &Failure{Kind: KindMalformedResponse, Detail: "cannot read response body"}
	}
	var envelope graphResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return &Failure{Kind: KindMalformedResponse, Detail: "response is not JSON"}
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		for _, item := range envelope.Errors {
			messages = append(messages, item.Message)
		}
		return &Failure{Kind: KindGraphqlError, Detail: strings.Join(messages, "; ")}
	}
	if err := json.Unmarshal(envelope.Data, into); err != nil {
		return &Failure{Kind: KindMalformedResponse, Detail: "response data does not match the expected shape"}
	}
	return nil
}

// IsRetryable reports whether the outbox should requeue after this failure.
// Auth refusal is permanent; rate limiting and unknown outcomes are retryable;
// a malformed body is retryable because the request may not have executed.
func IsRetryable(err error) bool {
	var failure *Failure
	if !errors.As(err, &failure) {
		return false
	}
	switch failure.Kind {
	case KindAuthRefused, KindGraphqlError:
		return false
	case KindRateLimited, KindTransport, KindMalformedResponse:
		return true
	}
	return false
}
