// Package linearclient is Concord's first-party Linear GraphQL client. It
// reads issue state from Linear: one request per call, with a typed Failure
// for every refusal shape the HTTP transport and the GraphQL layer can
// produce, so a caller can tell a permanent refusal from an unknown outcome.
//
// The client makes exactly one attempt per call and never retries. A
// transport failure means the outcome is unknown; the caller owns what
// happens next.
//
// Credential custody: the API key is read from the process environment at
// client construction and never logged, persisted, or echoed in an error.
// The endpoint default is the public Linear GraphQL URL and may be
// overridden for tests.
package linearclient

import (
	"bytes"
	"context"
	"encoding/json"
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

// FailureKind classifies one client failure so a caller can tell a permanent
// refusal from an unknown outcome.
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
	// KindGraphqlError means Linear answered with a GraphQL-level error.
	KindGraphqlError FailureKind = "graphql_error"
	// KindDuplicateEntity means an error reports Linear's insert conflict on an
	// id that already exists remotely. The failure detail carries the conflict
	// message and the colliding id.
	KindDuplicateEntity FailureKind = "duplicate_entity"
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

// Issue is the remote issue identity and content returned by Linear.
type Issue struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ResolvedIssue is one fetched issue together with its owning team and state,
// so callers can verify its identity, ownership, and liveness.
type ResolvedIssue struct {
	Issue
	TeamID    string
	StateID   string
	StateType string
}

// Client talks to one Linear workspace over GraphQL, authenticating with the
// workspace API key.
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
// locally; an empty key is refused because the client must not send an
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
		// UserPresentableMessage is Linear's operator-facing restatement of
		// the refusal. For an insert conflict it is the field that names the
		// entity and the id that already exists.
		UserPresentableMessage string `json:"userPresentableMessage"`
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
// rate-limit evidence, a duplicate-entity failure when any error reports
// Linear's insert conflict on a supplied id, otherwise a GraphQL failure
// carrying every message. All classification paths (a 4xx body and an HTTP
// 200 envelope) share it so they cannot drift.
func classifyGraphErrors(envelope graphResponse) *Failure {
	messages := make([]string, 0, len(envelope.Errors))
	conflict := false
	for _, item := range envelope.Errors {
		messages = append(messages, item.Message)
		if item.Extensions.UserPresentableMessage != "" {
			messages = append(messages, item.Extensions.UserPresentableMessage)
		}
		if strings.Contains(item.Message, "conflict on insert of") {
			conflict = true
		}
	}
	detail := strings.Join(messages, "; ")
	if conflict {
		return &Failure{Kind: KindDuplicateEntity, Detail: detail}
	}
	for _, item := range envelope.Errors {
		if isRateLimitError(item) {
			return &Failure{Kind: KindRateLimited, Detail: detail}
		}
	}
	return &Failure{Kind: KindGraphqlError, Detail: detail}
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
