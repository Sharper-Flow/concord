// Package linearmcp reads Linear through an MCP server over Streamable HTTP.
// It serves the drafting-pad import (one-way Linear initiative import): the
// host exposes the read-only Linear MCP, so the one-way mandate is structural
// — no write tool exists to call. The caller holds no Linear credential; the
// MCP server owns custody.
package linearmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FailureKind classifies one MCP caller failure.
type FailureKind string

const (
	// KindUnconfigured means no MCP endpoint was supplied.
	KindUnconfigured FailureKind = "mcp_unconfigured"
	// KindUnreachable means the endpoint did not answer.
	KindUnreachable FailureKind = "mcp_unreachable"
	// KindSessionRefused means initialize did not yield a session.
	KindSessionRefused FailureKind = "mcp_session_refused"
	// KindToolError means the tool answered isError with a message.
	KindToolError FailureKind = "mcp_tool_error"
	// KindNotFound means the requested initiative does not exist.
	KindNotFound FailureKind = "mcp_initiative_not_found"
	// KindMalformedResponse means the tool text did not decode.
	KindMalformedResponse FailureKind = "mcp_malformed_response"
)

// Failure is the typed caller error.
type Failure struct {
	Kind   FailureKind
	Detail string
}

func (f *Failure) Error() string {
	return fmt.Sprintf("linearmcp: %s: %s", f.Kind, f.Detail)
}

// Initiative is the imported identity: exactly the fields the drafting pad
// carries into Concord.
type Initiative struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type client struct {
	endpoint string
	http     *http.Client
	session  string
}

func newClient(endpoint string) *client {
	return &client{endpoint: endpoint, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *client) post(ctx context.Context, body []byte, session bool) (int, http.Header, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if session {
		request.Header.Set("Mcp-Session-Id", c.session)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, response.Header, nil, err
	}
	return response.StatusCode, response.Header, raw, nil
}

// dataLine extracts the JSON payload from a JSON-RPC response that may arrive
// as text/event-stream.
func dataLine(raw []byte) []byte {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "data: ") {
			return []byte(strings.TrimPrefix(line, "data: "))
		}
	}
	return raw
}

func (c *client) initialize(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "concord-drafting-pad", "version": "1.0"}},
	})
	status, header, _, err := c.post(ctx, body, false)
	if err != nil {
		return &Failure{Kind: KindUnreachable, Detail: "the MCP endpoint did not answer"}
	}
	if status >= 400 {
		return &Failure{Kind: KindSessionRefused, Detail: fmt.Sprintf("initialize answered HTTP %d", status)}
	}
	session := header.Get("Mcp-Session-Id")
	if session == "" {
		return &Failure{Kind: KindSessionRefused, Detail: "initialize carried no session id"}
	}
	c.session = session
	notice, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if _, _, _, err := c.post(ctx, notice, true); err != nil {
		return &Failure{Kind: KindUnreachable, Detail: "the initialized notification did not complete"}
	}
	return nil
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// GetInitiative reads one Linear initiative through the MCP server's
// get_initiative tool.
func GetInitiative(ctx context.Context, endpoint, initiativeID string) (Initiative, error) {
	if endpoint == "" {
		return Initiative{}, &Failure{Kind: KindUnconfigured, Detail: "no MCP endpoint was supplied"}
	}
	if initiativeID == "" {
		return Initiative{}, &Failure{Kind: KindToolError, Detail: "initiative id is required"}
	}
	caller := newClient(endpoint)
	if err := caller.initialize(ctx); err != nil {
		return Initiative{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "get_initiative", "arguments": map[string]any{"initiativeId": initiativeID}},
	})
	status, _, raw, err := caller.post(ctx, body, true)
	if err != nil {
		return Initiative{}, &Failure{Kind: KindUnreachable, Detail: "the tool call did not complete"}
	}
	if status >= 400 {
		return Initiative{}, &Failure{Kind: KindSessionRefused, Detail: fmt.Sprintf("the tool call answered HTTP %d", status)}
	}
	var envelope struct {
		Result toolResult `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(dataLine(raw), &envelope); err != nil {
		return Initiative{}, &Failure{Kind: KindMalformedResponse, Detail: "the tool response did not decode"}
	}
	if envelope.Error != nil {
		return Initiative{}, &Failure{Kind: KindToolError, Detail: envelope.Error.Message}
	}
	text := ""
	for _, item := range envelope.Result.Content {
		if item.Type == "text" {
			text = item.Text
		}
	}
	if envelope.Result.IsError {
		detail := text
		if detail == "" {
			detail = "the tool reported an error without a message"
		}
		if strings.Contains(strings.ToLower(detail), "not found") || strings.Contains(strings.ToLower(detail), "no initiative") {
			return Initiative{}, &Failure{Kind: KindNotFound, Detail: detail}
		}
		return Initiative{}, &Failure{Kind: KindToolError, Detail: detail}
	}
	var payload struct {
		Initiative Initiative `json:"initiative"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil || payload.Initiative.ID == "" {
		return Initiative{}, &Failure{Kind: KindNotFound, Detail: "the initiative does not exist"}
	}
	return payload.Initiative, nil
}
