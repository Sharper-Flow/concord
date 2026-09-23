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
	ID          string   `json:"id"`
	TeamID      string   `json:"teamId"`
	ProjectID   string   `json:"projectId,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	LabelIDs    []string `json:"labelIds,omitempty"`
	// StatusID lands the card in the workflow state its work item holds at
	// creation. Linear documents an unspecified state as the team's default
	// (or Triage); stateId is the field name issueUpdate already uses.
	StatusID string `json:"stateId,omitempty"`
	// Priority seeds Linear's triage ordering exactly once, at creation, from
	// the work item's declared urgency band: expedite sends 1 (Urgent) and
	// standard sends 3 (Medium). Linear owns the priority after creation, so
	// no update path resends it. The work item's local -100..100 priority
	// integer is separate sequencing (CD-0018) and never rides this field.
	Priority int `json:"priority,omitempty"`
}

// UpdateIssueInput carries the mutable fields the drain synchronizes. It has
// no priority field by design: creation seeded the priority once, and Linear
// owns backlog triage from then on.
type UpdateIssueInput struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// ProjectID is the issue's full Project state, not a change request: the
	// owning Initiative's Linear Project uuid, or nil to send an explicit
	// null. Linear reads the explicit null as "clear the field" (CD-0171 D4,
	// D6), so an issue whose last Initiative entry left loses its Project
	// instead of keeping a stale one.
	ProjectID *string `json:"projectId"`
	StatusID  string  `json:"stateId,omitempty"`
	// AddedLabelIDs and RemovedLabelIDs carry the label delta. RemovedLabelIDs
	// exists on Linear's IssueUpdateInput ([UUID!]); without it a label under
	// the project:* or optional keys that stopped applying would linger on the
	// remote issue forever (CD-0171 D3, D5).
	AddedLabelIDs   []string `json:"addedLabelIds,omitempty"`
	RemovedLabelIDs []string `json:"removedLabelIds,omitempty"`
}

// Issue is the remote issue identity and content returned by Linear.
type Issue struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Project is the remote Linear project identity and content. Content is the
// project's markdown document; Description is the short field (CD-0171 d3).
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Content     string    `json:"content"`
	URL         string    `json:"url"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreateProjectInput carries the fields the drain supplies on project
// creation. ID is the Concord-generated UUID v4: Linear's
// ProjectCreateInput.id, which makes a replayed create converge on the same
// remote project instead of duplicating it (CD-0171 d2).
type CreateProjectInput struct {
	ID          string   `json:"id"`
	TeamIDs     []string `json:"teamIds"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Content     string   `json:"content,omitempty"`
}

// UpdateProjectInput carries the mutable project fields the drain
// synchronizes for an Initiative.
type UpdateProjectInput struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content,omitempty"`
}

// ResolvedIssue is one fetched issue together with its owning team and state,
// so callers can verify its identity, ownership, and liveness.
type ResolvedIssue struct {
	Issue
	TeamID    string
	StateID   string
	StateType string
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

// graphError is one entry of a GraphQL errors array. Linear attaches a typed
// code in extensions for recognized failures such as rate limiting
// (https://linear.app/developers/rate-limiting).
type graphError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

type graphResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphError    `json:"errors"`
}

// isRateLimitError reports whether one GraphQL error carries Linear's
// rate-limit evidence: the RATELIMITED extension code, or a documented
// rate-limit message shape. Linear documents the extension code on an HTTP
// 400 body, and quota refusals such as "Rate limit exceeded. Only 2500
// requests are allowed per 1 hour." can arrive on either status path without
// the code.
func isRateLimitError(item graphError) bool {
	if strings.Contains(strings.ToUpper(item.Extensions.Code), "RATELIMITED") {
		return true
	}
	message := strings.ToUpper(item.Message)
	return strings.Contains(message, "RATELIMITED") ||
		strings.Contains(message, "RATE LIMIT EXCEEDED") ||
		strings.Contains(message, "EXCEEDED YOUR REQUEST QUOTA")
}

// classifyGraphErrors returns a rate-limited failure when any error carries
// rate-limit evidence, otherwise a GraphQL failure carrying every message.
// Both classification paths (a 4xx body and an HTTP 200 envelope) share it so
// they cannot drift.
func classifyGraphErrors(envelope graphResponse) *Failure {
	messages := make([]string, 0, len(envelope.Errors))
	for _, item := range envelope.Errors {
		messages = append(messages, item.Message)
	}
	detail := strings.Join(messages, "; ")
	for _, item := range envelope.Errors {
		if isRateLimitError(item) {
			return &Failure{Kind: KindRateLimited, Detail: detail}
		}
	}
	return &Failure{Kind: KindGraphqlError, Detail: detail}
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

// GetIssue fetches one existing issue by its UUID or human identifier. Linear
// answers an unknown issue with a GraphQL error, which maps to the permanent
// KindGraphqlError failure.
func (c *Client) GetIssue(ctx context.Context, remoteUUID string) (ResolvedIssue, error) {
	var payload struct {
		Issue struct {
			Issue
			State struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"state"`
			Team struct {
				ID string `json:"id"`
			} `json:"team"`
		} `json:"issue"`
	}
	if err := c.call(ctx, "query($id: String!) { issue(id: $id) { id identifier url title description updatedAt state { id type } team { id } } }", map[string]any{"id": remoteUUID}, &payload); err != nil {
		return ResolvedIssue{}, err
	}
	if payload.Issue.ID == "" {
		return ResolvedIssue{}, &Failure{Kind: KindGraphqlError, Detail: "issue query returned no issue"}
	}
	return ResolvedIssue{Issue: payload.Issue.Issue, TeamID: payload.Issue.Team.ID, StateID: payload.Issue.State.ID, StateType: payload.Issue.State.Type}, nil
}

// GetIssueLabelIDs fetches only the issue's current label ids, so the drain
// can compute the Concord-managed labels that no longer apply without a full
// issue read.
func (c *Client) GetIssueLabelIDs(ctx context.Context, remoteUUID string) ([]string, error) {
	var payload struct {
		Issue struct {
			Labels struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"issue"`
	}
	if err := c.call(ctx, "query($id: String!) { issue(id: $id) { labels { nodes { id } } } }", map[string]any{"id": remoteUUID}, &payload); err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(payload.Issue.Labels.Nodes))
	for _, node := range payload.Issue.Labels.Nodes {
		labels = append(labels, node.ID)
	}
	return labels, nil
}

// CreateProject executes projectCreate with the Concord-generated UUID and
// returns the remote project identity.
func (c *Client) CreateProject(ctx context.Context, input CreateProjectInput) (Project, error) {
	var payload struct {
		ProjectCreate struct {
			Success bool    `json:"success"`
			Project Project `json:"project"`
		} `json:"projectCreate"`
	}
	query := "mutation($input: ProjectCreateInput!) { projectCreate(input: $input) { success project { id name description content url updatedAt } } }"
	if err := c.call(ctx, query, map[string]any{"input": input}, &payload); err != nil {
		return Project{}, err
	}
	if !payload.ProjectCreate.Success {
		return Project{}, &Failure{Kind: KindGraphqlError, Detail: "projectCreate reported success=false"}
	}
	return payload.ProjectCreate.Project, nil
}

// UpdateProject executes projectUpdate against the remote project UUID.
func (c *Client) UpdateProject(ctx context.Context, projectUUID string, input UpdateProjectInput) (Project, error) {
	var payload struct {
		ProjectUpdate struct {
			Success bool    `json:"success"`
			Project Project `json:"project"`
		} `json:"projectUpdate"`
	}
	query := "mutation($id: String!, $input: ProjectUpdateInput!) { projectUpdate(id: $id, input: $input) { success project { id name description content url updatedAt } } }"
	if err := c.call(ctx, query, map[string]any{"id": projectUUID, "input": input}, &payload); err != nil {
		return Project{}, err
	}
	if !payload.ProjectUpdate.Success {
		return Project{}, &Failure{Kind: KindGraphqlError, Detail: "projectUpdate reported success=false"}
	}
	return payload.ProjectUpdate.Project, nil
}

// GetProject fetches one project by its UUID. Linear answers an unknown
// project with a GraphQL error, which maps to the permanent KindGraphqlError
// failure.
func (c *Client) GetProject(ctx context.Context, projectUUID string) (Project, error) {
	var payload struct {
		Project Project `json:"project"`
	}
	query := "query($id: String!) { project(id: $id) { id name description content url updatedAt } }"
	if err := c.call(ctx, query, map[string]any{"id": projectUUID}, &payload); err != nil {
		return Project{}, err
	}
	if payload.Project.ID == "" {
		return Project{}, &Failure{Kind: KindGraphqlError, Detail: "project query returned no project"}
	}
	return payload.Project, nil
}

// startedIssuesPageSize is the page size the started-issue sweep requests.
// Linear caps a connection page at 250; a smaller page keeps one team's In
// Progress set streaming predictably.
const startedIssuesPageSize = 100

// maxStartedIssuePages bounds the pagination loop so a server that keeps
// reporting a next page cannot spin forever. It holds 10,000 issues.
const maxStartedIssuePages = 100

// ListTeamStartedIssues returns every issue of one team whose workflow state
// type is started, following the connection cursor until a page reports no
// next page. Each issue carries its state identity so a caller can reason
// about liveness without a second fetch per issue.
func (c *Client) ListTeamStartedIssues(ctx context.Context, teamID string) ([]ResolvedIssue, error) {
	var payload struct {
		Issues struct {
			Nodes []struct {
				Issue
				State struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				} `json:"state"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `json:"issues"`
	}
	query := fmt.Sprintf(`query($teamId: ID!, $after: String) { issues(first: %d, after: $after, filter: { team: { id: { eq: $teamId } }, state: { type: { eq: "started" } } }) { nodes { id identifier url title updatedAt state { id type } } pageInfo { hasNextPage endCursor } } }`, startedIssuesPageSize)
	issues := make([]ResolvedIssue, 0)
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxStartedIssuePages {
			return nil, &Failure{Kind: KindMalformedResponse, Detail: fmt.Sprintf("issues pagination exceeded %d pages", maxStartedIssuePages)}
		}
		variables := map[string]any{"teamId": teamID}
		// Linear rejects an empty `after` as an invalid pagination argument,
		// so the first page must omit the cursor rather than send "".
		if cursor != "" {
			variables["after"] = cursor
		}
		if err := c.call(ctx, query, variables, &payload); err != nil {
			return nil, err
		}
		for _, node := range payload.Issues.Nodes {
			if node.ID == "" {
				return nil, &Failure{Kind: KindMalformedResponse, Detail: "issues page returned a node without an id"}
			}
			issues = append(issues, ResolvedIssue{Issue: node.Issue, TeamID: teamID, StateID: node.State.ID, StateType: node.State.Type})
		}
		if !payload.Issues.PageInfo.HasNextPage {
			return issues, nil
		}
		if payload.Issues.PageInfo.EndCursor == "" {
			return nil, &Failure{Kind: KindMalformedResponse, Detail: "issues page reported a next page without an end cursor"}
		}
		cursor = payload.Issues.PageInfo.EndCursor
	}
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
			return classifyGraphErrors(envelope)
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
		// Linear reports rate limiting as HTTP 400 with a RATELIMITED
		// extension code and also as a GraphQL error on an otherwise
		// successful HTTP 200. Both shapes answer the same refusal.
		return classifyGraphErrors(envelope)
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
