package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// linearMCPStub serves the MCP handshake and one get_initiative answer.
func linearMCPStub(t *testing.T, initiativeJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		body := string(buf)
		switch {
		case strings.Contains(body, `"method":"initialize"`):
			w.Header().Set("Mcp-Session-Id", "sess-import")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"stub","version":"1"}}}`))
		case strings.Contains(body, "notifications/initialized"):
			w.WriteHeader(202)
		case strings.Contains(body, "get_initiative"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"" + initiativeJSON + "\"}],\"isError\":false}}\n\n"))
		default:
			w.WriteHeader(400)
		}
	}))
}

func TestLinearInitiativeImportCLI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "import-product", "import-product-project")
	enableLinearProduct(t, dbPath, "import-product")

	initiativeJSON := `{\"id\":\"ini-uuid-1\",\"name\":\"Example initiative\",\"summary\":\"Imported drafting pad\"}`
	server := linearMCPStub(t, initiativeJSON)
	defer server.Close()
	t.Setenv("CONCORD_LINEAR_MCP_URL", server.URL)

	// A missing endpoint refuses before any remote call.
	var out, errOut strings.Builder
	t.Setenv(dbOverrideEnv, dbPath)
	t.Setenv("CONCORD_LINEAR_MCP_URL", "")
	if code := runWithInput([]string{"linear", "initiative-import"}, strings.NewReader(`{"product_id":"import-product","initiative_id":"ini-uuid-1"}`), &out, &errOut); code == 0 {
		t.Fatal("import without an MCP endpoint must exit non-zero")
	}
	if !strings.Contains(errOut.String(), "CONCORD_LINEAR_MCP_URL") {
		t.Fatalf("stderr=%q", errOut.String())
	}
	out.Reset()
	errOut.Reset()

	// The happy path reads through the MCP server and creates the initiative.
	t.Setenv("CONCORD_LINEAR_MCP_URL", server.URL)
	if code := runWithInput([]string{"linear", "initiative-import"}, strings.NewReader(`{"product_id":"import-product","initiative_id":"ini-uuid-1"}`), &out, &errOut); code != 0 {
		t.Fatalf("import exit=%d stderr=%q", code, errOut.String())
	}
	var imported struct {
		OK         bool `json:"ok"`
		Initiative struct {
			WorkID      string `json:"work_id"`
			ExternalRef string `json:"external_ref"`
			Title       string `json:"title"`
		} `json:"initiative"`
	}
	if err := json.Unmarshal([]byte(out.String()), &imported); err != nil {
		t.Fatalf("import output %q: %v", out.String(), err)
	}
	if !imported.OK || imported.Initiative.WorkID == "" || imported.Initiative.ExternalRef != "linear:ini-uuid-1" || imported.Initiative.Title != "Example initiative" {
		t.Fatalf("import result = %+v", imported)
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var kind, externalRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT kind, json_extract(intent_json,'$.external_ref') FROM work_items WHERE id=?`, imported.Initiative.WorkID).Scan(&kind, &externalRef); err != nil {
		t.Fatalf("imported work item: %v", err)
	}
	if kind != "initiative" || externalRef != "linear:ini-uuid-1" {
		t.Fatalf("imported = %s/%s", kind, externalRef)
	}

	// The second import of the same identity refuses typed.
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear", "initiative-import"}, strings.NewReader(`{"product_id":"import-product","initiative_id":"ini-uuid-1"}`), &out, &errOut); code == 0 {
		t.Fatal("duplicate import must exit non-zero")
	}
	if !strings.Contains(errOut.String(), "already imported") {
		t.Fatalf("stderr=%q", errOut.String())
	}
}
