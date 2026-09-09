package linearmcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetInitiativeRefusesUnconfiguredEndpoint(t *testing.T) {
	_, err := GetInitiative(context.Background(), "", "some-id")
	if err == nil {
		t.Fatal("unconfigured endpoint must refuse")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnconfigured {
		t.Fatalf("error = %v, want mcp_unconfigured", err)
	}
}

func TestGetInitiativeReadsThroughHandshake(t *testing.T) {
	var sawSession, sawInitiativeID bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "" {
			sawSession = true
		}
		switch {
		case strings.Contains(r.RequestURI, "") && r.Method == "POST":
			// Distinguish by body: initialize vs notification vs call.
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			body := string(buf)
			if strings.Contains(body, `"method":"initialize"`) {
				w.Header().Set("Mcp-Session-Id", "sess-1")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"stub","version":"1"}}}`))
				return
			}
			if strings.Contains(body, "notifications/initialized") {
				w.WriteHeader(202)
				return
			}
			if strings.Contains(body, "get_initiative") {
				if strings.Contains(body, `"query":"ini-1"`) {
					sawInitiativeID = true
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"{\\\"id\\\":\\\"ini-1\\\",\\\"name\\\":\\\"Example initiative\\\",\\\"summary\\\":\\\"Example description\\\"}\"}],\"isError\":false}}\n\n"))
				return
			}
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	initiative, err := GetInitiative(context.Background(), server.URL+"/mcp", "ini-1")
	if err != nil {
		t.Fatalf("GetInitiative() error = %v", err)
	}
	if initiative.ID != "ini-1" || initiative.Name != "Example initiative" || initiative.Description != "Example description" {
		t.Fatalf("initiative = %+v", initiative)
	}
	if !sawSession || !sawInitiativeID {
		t.Fatalf("handshake session=%v initiative-id=%v", sawSession, sawInitiativeID)
	}
}

func TestGetInitiativeClassifiesTypedFailures(t *testing.T) {
	cases := []struct {
		name     string
		handler  http.HandlerFunc
		wantKind FailureKind
	}{
		{"session refused", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}, KindSessionRefused},
		{"tool error", func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			if strings.Contains(string(buf), "initialize") {
				w.Header().Set("Mcp-Session-Id", "sess-1")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"Initiative not found"}],"isError":true}}`))
		}, KindNotFound},
		{"malformed payload", func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			if strings.Contains(string(buf), "initialize") {
				w.Header().Set("Mcp-Session-Id", "sess-1")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{}"}],"isError":false}}`))
		}, KindNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			_, err := GetInitiative(context.Background(), server.URL, "ini-1")
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != tc.wantKind {
				t.Fatalf("error = %v, want %s", err, tc.wantKind)
			}
		})
	}
}

func TestGetInitiativeUnreachableIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	_, err := GetInitiative(context.Background(), server.URL, "ini-1")
	if err == nil {
		t.Fatal("closed endpoint must refuse")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnreachable {
		t.Fatalf("error = %v, want mcp_unreachable", err)
	}
}

func failureAs(err error, target **Failure) bool {
	if failure, ok := err.(*Failure); ok {
		*target = failure
		return true
	}
	return false
}
