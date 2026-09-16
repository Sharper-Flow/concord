// Package linearclient is Concord's first-party Linear GraphQL client. It
// exists to serve the Phase 1 outbox drain (issue 990): create and update
// issues with a client-supplied UUID so an ambiguous outcome is re-drained
// under the same identity instead of blind-retried.
//
// The client makes exactly one attempt per call. Retries belong to the outbox,
// which is the durable retry owner; a transport failure here means the outcome
// is unknown, and the drain re-claims the operation on its next pass.
//
// The API key is read from the process environment at client construction and
// never logged, persisted, or echoed in an error. The endpoint default is the
// public Linear GraphQL URL and may be overridden for tests.
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
	// KindInitiativeNotFound means no initiative matched the requested query.
	KindInitiativeNotFound FailureKind = "initiative_not_found"
	// KindInitiativeAmbiguous means multiple initiatives matched the name.
	KindInitiativeAmbiguous FailureKind = "initiative_ambiguous"
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
	ProjectID   string `json:"projectId,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// UpdateIssueInput carries the mutable fields the drain synchronizes.
type UpdateIssueInput struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	ProjectID   string `json:"projectId,omitempty"`
	StatusID    string `json:"stateId,omitempty"`
}

// Issue is the remote identity a completed operation records.
type Issue struct {
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	URL        string    `json:"url"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// ResolvedIssue is one fetched issue together with the team that owns it, so
// an adoption can verify the issue belongs to the Product's configured team.
type ResolvedIssue struct {
	Issue
	TeamID string
}

// Initiative is the remote identity and content returned by a read.
type Initiative struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	SlugID      string    `json:"slugId"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	UpdatedAt   time.Time `json:"updatedAt"`
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

// GetIssue fetches one existing issue by its remote UUID. Linear answers an
// unknown issue with a GraphQL error, which maps to the permanent
// KindGraphqlError failure.
func (c *Client) GetIssue(ctx context.Context, remoteUUID string) (ResolvedIssue, error) {
	var payload struct {
		Issue struct {
			Issue
			Team struct {
				ID string `json:"id"`
			} `json:"team"`
		} `json:"issue"`
	}
	if err := c.call(ctx, "query($id: String!) { issue(id: $id) { id identifier url updatedAt team { id } } }", map[string]any{"id": remoteUUID}, &payload); err != nil {
		return ResolvedIssue{}, err
	}
	if payload.Issue.ID == "" {
		return ResolvedIssue{}, &Failure{Kind: KindGraphqlError, Detail: "issue query returned no issue"}
	}
	return ResolvedIssue{Issue: payload.Issue.Issue, TeamID: payload.Issue.Team.ID}, nil
}

// GetInitiative reads an initiative by UUID or resolves an exact name from the
// workspace initiative list.
func (c *Client) GetInitiative(ctx context.Context, query string) (Initiative, error) {
	if isUUID(query) {
		var payload struct {
			Initiative Initiative `json:"initiative"`
		}
		if err := c.call(ctx, "query($id: String!) { initiative(id: $id) { id name slugId description url updatedAt } }", map[string]any{"id": query}, &payload); err != nil {
			return Initiative{}, err
		}
		if payload.Initiative.ID == "" {
			return Initiative{}, &Failure{Kind: KindInitiativeNotFound, Detail: fmt.Sprintf("initiative %q was not found", query)}
		}
		return payload.Initiative, nil
	}

	var payload struct {
		Initiatives struct {
			Nodes []Initiative `json:"nodes"`
		} `json:"initiatives"`
	}
	if err := c.call(ctx, "query { initiatives(first: 100) { nodes { id name slugId description url updatedAt } } }", nil, &payload); err != nil {
		return Initiative{}, err
	}
	var matches []Initiative
	for _, node := range payload.Initiatives.Nodes {
		if node.Name == query {
			matches = append(matches, node)
		}
	}
	switch len(matches) {
	case 0:
		return Initiative{}, &Failure{Kind: KindInitiativeNotFound, Detail: fmt.Sprintf("initiative %q was not found", query)}
	case 1:
		return matches[0], nil
	default:
		candidates := make([]string, 0, len(matches))
		for _, match := range matches {
			candidates = append(candidates, fmt.Sprintf("%s (%s)", match.ID, match.SlugID))
		}
		return Initiative{}, &Failure{Kind: KindInitiativeAmbiguous, Detail: fmt.Sprintf("initiative name %q matched multiple candidates: %s", query, strings.Join(candidates, ", "))}
	}
}

func isUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
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
	case KindAuthRefused, KindGraphqlError, KindInitiativeNotFound, KindInitiativeAmbiguous:
		return false
	case KindRateLimited, KindTransport, KindMalformedResponse:
		return true
	}
	return false
}
