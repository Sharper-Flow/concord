package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

// linearGraphQLStub answers one project query with the given body and records
// the Authorization header it saw.
func linearGraphQLStub(t *testing.T, projectJSON string, sawAuth *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if sawAuth != nil {
			*sawAuth = r.Header.Get("Authorization") == "lin_api_import_test"
		}
		if !strings.Contains(string(body), "project(id:") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"project":` + projectJSON + `}}`))
	}))
}

func TestLinearInitiativeImportCLI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "import-product", "import-product-project")
	enableLinearProduct(t, dbPath, "import-product")

	var sawAuth bool
	server := linearGraphQLStub(t, `{"id":"proj-uuid-1","name":"Example initiative","description":"Imported Linear Project","content":"The imported narrative.","url":"https://linear.app/example/project/proj-uuid-1","updatedAt":"2026-09-23T00:00:00Z"}`, &sawAuth)
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)

	// A missing key refuses before any remote call.
	var out, errOut strings.Builder
	t.Setenv(dbOverrideEnv, dbPath)
	t.Setenv(linearclient.EnvAPIKey, "")
	if code := runWithInput([]string{"linear", "initiative-import"}, strings.NewReader(`{"product_id":"import-product","initiative_id":"proj-uuid-1"}`), &out, &errOut); code == 0 {
		t.Fatal("import without an API key must exit non-zero")
	}
	if !strings.Contains(errOut.String(), "missing_credential") {
		t.Fatalf("stderr=%q", errOut.String())
	}
	out.Reset()
	errOut.Reset()

	// The happy path reads the Linear Project through the first-party client
	// (CD-0171 D7) and creates the initiative.
	t.Setenv(linearclient.EnvAPIKey, "lin_api_import_test")
	if code := runWithInput([]string{"linear", "initiative-import"}, strings.NewReader(`{"product_id":"import-product","initiative_id":"proj-uuid-1"}`), &out, &errOut); code != 0 {
		t.Fatalf("import exit=%d stderr=%q", code, errOut.String())
	}
	if !sawAuth {
		t.Fatalf("the project query lacked the API key authorization header")
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
	if !imported.OK || imported.Initiative.WorkID == "" || imported.Initiative.ExternalRef != "linear:proj-uuid-1" || imported.Initiative.Title != "Example initiative" {
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
	if kind != "initiative" || externalRef != "linear:proj-uuid-1" {
		t.Fatalf("imported = %s/%s", kind, externalRef)
	}
	// The Linear Project's content lands as the Initiative narrative
	// (CD-0171 d3), so a later project_update sends it back.
	var narrative string
	if err := s.DatabaseForTesting().QueryRow(`SELECT narrative FROM work_items WHERE id=?`, imported.Initiative.WorkID).Scan(&narrative); err != nil {
		t.Fatalf("imported work item: %v", err)
	}
	if narrative != "The imported narrative." {
		t.Fatalf("imported narrative = %q, want the Linear Project content (CD-0171 d3)", narrative)
	}
	// An imported Initiative keeps the Project it already has: the capture
	// path must not queue a project_create for it.
	var queued int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox WHERE op_kind='project_create'`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("import queued %d project_create operations, want 0", queued)
	}

	// The second import of the same identity refuses typed.
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear", "initiative-import"}, strings.NewReader(`{"product_id":"import-product","initiative_id":"proj-uuid-1"}`), &out, &errOut); code == 0 {
		t.Fatal("duplicate import must exit non-zero")
	}
	if !strings.Contains(errOut.String(), "already imported") {
		t.Fatalf("stderr=%q", errOut.String())
	}
}
