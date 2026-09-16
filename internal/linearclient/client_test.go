package linearclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFromEnvRefusesMissingCredential(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	client, err := FromEnv()
	if client != nil || err == nil {
		t.Fatalf("FromEnv() = %v, %v; want nil, error", client, err)
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingCredential {
		t.Fatalf("FromEnv() error = %v, want missing_credential", err)
	}
	if strings.Contains(err.Error(), "lin_api_") {
		t.Fatalf("refusal must not echo key material: %v", err)
	}
}

func TestCreateIssueSendsBearerAndClientUUID(t *testing.T) {
	var gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","identifier":"SHA-1","url":"https://linear.app/example/issue/SHA-1","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	issue, err := client.CreateIssue(context.Background(), CreateIssueInput{
		ID:          "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0",
		TeamID:      "68d52710-76d9-4b41-ba45-778511d0e2ed",
		ProjectID:   "project-uuid-1",
		Title:       "Example issue",
		Description: "Example description",
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	if gotAuth != "lin_api_test" {
		t.Fatalf("Authorization = %q, want the raw key without a Bearer prefix", gotAuth)
	}
	for _, want := range []string{`"id":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"`, `"teamId":"68d52710-76d9-4b41-ba45-778511d0e2ed"`, `"projectId":"project-uuid-1"`, `"title":"Example issue"`, "issueCreate"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %q lacks %q", gotBody, want)
		}
	}
	if issue.ID != "68d52710-76d9-4b41-ba45-778511d0e2ed" || issue.Identifier != "SHA-1" || issue.URL != "https://linear.app/example/issue/SHA-1" {
		t.Fatalf("issue = %+v", issue)
	}
	if !issue.UpdatedAt.Equal(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("updatedAt = %v", issue.UpdatedAt)
	}
}

func TestUpdateIssueAddressesRemoteIdentity(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","identifier":"SHA-1","url":"https://linear.app/example/issue/SHA-1","updatedAt":"2026-09-09T12:01:00Z"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	issue, err := client.UpdateIssue(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed", UpdateIssueInput{Title: "Revised title", Description: "Revised description", StatusID: "state-cancelled"})
	if err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}
	if issue.Identifier != "SHA-1" {
		t.Fatalf("issue = %+v", issue)
	}
	for _, want := range []string{`"id":"68d52710-76d9-4b41-ba45-778511d0e2ed"`, `"title":"Revised title"`, `"stateId":"state-cancelled"`, "issueUpdate"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %q lacks %q", gotBody, want)
		}
	}
}

func TestGetInitiativeByIdentifier(t *testing.T) {
	var requestBody struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"initiative":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","name":"Example initiative","slugId":"example-initiative","description":"Example description","url":"https://linear.app/example/initiative/example-initiative","updatedAt":"2026-09-09T12:00:00Z"}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	initiative, err := client.GetInitiative(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed")
	if err != nil {
		t.Fatalf("GetInitiative() error = %v", err)
	}
	if !strings.Contains(requestBody.Query, "initiative(id: $id)") || requestBody.Variables["id"] != "68d52710-76d9-4b41-ba45-778511d0e2ed" {
		t.Fatalf("request = %+v", requestBody)
	}
	if initiative.ID != "68d52710-76d9-4b41-ba45-778511d0e2ed" || initiative.Name != "Example initiative" || initiative.SlugID != "example-initiative" || initiative.Description != "Example description" || initiative.URL == "" {
		t.Fatalf("initiative = %+v", initiative)
	}
	if !initiative.UpdatedAt.Equal(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("updatedAt = %v", initiative.UpdatedAt)
	}
}

func TestGetInitiativeByExactName(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		gotQuery = request.Query
		_, _ = w.Write([]byte(`{"data":{"initiatives":{"nodes":[{"id":"ini-1","name":"Other initiative","slugId":"other","description":"Other description","url":"https://linear.app/example/initiative/other","updatedAt":"2026-09-09T11:00:00Z"},{"id":"ini-2","name":"Example initiative","slugId":"example-initiative","description":"Example description","url":"https://linear.app/example/initiative/example-initiative","updatedAt":"2026-09-09T12:00:00Z"}]}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	initiative, err := client.GetInitiative(context.Background(), "Example initiative")
	if err != nil {
		t.Fatalf("GetInitiative() error = %v", err)
	}
	for _, field := range []string{"initiatives(first: 100)", "id", "name", "slugId", "description", "url", "updatedAt"} {
		if !strings.Contains(gotQuery, field) {
			t.Fatalf("query = %q, want %q", gotQuery, field)
		}
	}
	if initiative.ID != "ini-2" || initiative.Name != "Example initiative" || initiative.SlugID != "example-initiative" || initiative.Description != "Example description" || initiative.URL != "https://linear.app/example/initiative/example-initiative" {
		t.Fatalf("initiative = %+v", initiative)
	}
	if !initiative.UpdatedAt.Equal(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("updatedAt = %v", initiative.UpdatedAt)
	}
}

func TestGetInitiativeByNameReturnsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"initiatives":{"nodes":[{"id":"ini-1","name":"Other initiative","slugId":"other"}]}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetInitiative(context.Background(), "Missing initiative")
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInitiativeNotFound || IsRetryable(err) {
		t.Fatalf("error = %v, want permanent initiative_not_found", err)
	}
}

func TestGetInitiativeByNameReturnsAmbiguity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"initiatives":{"nodes":[{"id":"ini-1","name":"Example initiative","slugId":"example-one"},{"id":"ini-2","name":"Example initiative","slugId":"example-two"}]}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetInitiative(context.Background(), "Example initiative")
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInitiativeAmbiguous || IsRetryable(err) {
		t.Fatalf("error = %v, want permanent initiative_ambiguous", err)
	}
	if !strings.Contains(failure.Detail, "ini-1") || !strings.Contains(failure.Detail, "ini-2") {
		t.Fatalf("detail = %q, want both candidates", failure.Detail)
	}
}

func TestGetInitiativeGraphQLErrorIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"initiative access denied"}]}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetInitiative(context.Background(), "Example initiative")
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindGraphqlError || IsRetryable(err) {
		t.Fatalf("error = %v, want permanent graphql_error", err)
	}
}

func TestGetInitiativeTransportFailureIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetInitiative(context.Background(), "Example initiative")
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindTransport || !IsRetryable(err) {
		t.Fatalf("error = %v, want retryable transport", err)
	}
}

func TestFailureClassificationIsTyped(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		wantKind   FailureKind
	}{
		{"auth refused", http.StatusUnauthorized, `{"errors":[{"message":"unauthorized"}]}`, "", KindAuthRefused},
		{"rate limited", http.StatusTooManyRequests, ``, "7", KindRateLimited},
		{"rate limited as 400", http.StatusBadRequest, `{"errors":[{"message":"You have exceeded your request quota. RATELIMITED"}]}`, "", KindRateLimited},
		{"graphql error", http.StatusOK, `{"errors":[{"message":"team not found"}]}`, "", KindGraphqlError},
		{"mutation reported failure", http.StatusOK, `{"data":{"issueCreate":{"success":false}}}`, "", KindGraphqlError},
		{"error body surfaces", http.StatusBadRequest, `{"errors":[{"message":"an API key is not a Bearer token"}]}`, "", KindGraphqlError},
		{"malformed body", http.StatusOK, `not-json`, "", KindMalformedResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := New("lin_api_test", WithEndpoint(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateIssue(context.Background(), CreateIssueInput{ID: "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", TeamID: "t", Title: "x"})
			if err == nil {
				t.Fatal("CreateIssue() must fail")
			}
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != tc.wantKind {
				t.Fatalf("error = %v, want kind %s", err, tc.wantKind)
			}
			if tc.name == "error body surfaces" && !strings.Contains(failure.Detail, "not a Bearer token") {
				t.Fatalf("detail = %q, want the server message surfaced", failure.Detail)
			}
			if tc.wantKind == KindRateLimited && tc.retryAfter != "" && failure.RetryAfter != 7*time.Second {
				t.Fatalf("retry-after = %v, want 7s", failure.RetryAfter)
			}
		})
	}
}

func TestTransportFailureIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // port is now closed; every dial fails
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateIssue(context.Background(), CreateIssueInput{ID: "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", TeamID: "t", Title: "x"})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindTransport {
		t.Fatalf("error = %v, want transport", err)
	}
}

func failureAs(err error, target **Failure) bool {
	if failure, ok := err.(*Failure); ok {
		*target = failure
		return true
	}
	return false
}

func TestGetIssueResolvesIdentityAndTeam(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","identifier":"SHA-7","url":"https://linear.app/example/issue/SHA-7","updatedAt":"2026-09-15T08:00:00Z","team":{"id":"team-uuid-1"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := client.GetIssue(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed")
	if err != nil {
		t.Fatalf("GetIssue() error = %v", err)
	}
	if !strings.Contains(gotBody, `"query":"query($id: String!) { issue(id: $id) { id identifier url updatedAt team { id } } }"`) {
		t.Fatalf("request body %q lacks the issue query", gotBody)
	}
	if resolved.ID != "68d52710-76d9-4b41-ba45-778511d0e2ed" || resolved.Identifier != "SHA-7" || resolved.TeamID != "team-uuid-1" {
		t.Fatalf("resolved issue = %+v", resolved.Issue)
	}
}

func TestGetIssueMapsUnknownIssueToPermanentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"Issue not found"}]}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetIssue(context.Background(), "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("GetIssue() for an unknown issue = nil error, want failure")
	} else if IsRetryable(err) {
		t.Fatalf("unknown issue error %v is retryable, want permanent", err)
	}
}
