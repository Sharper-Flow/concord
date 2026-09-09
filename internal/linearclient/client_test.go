package linearclient

import (
	"context"
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
		Title:       "Example issue",
		Description: "Example description",
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	if gotAuth != "lin_api_test" {
		t.Fatalf("Authorization = %q, want the raw key without a Bearer prefix", gotAuth)
	}
	for _, want := range []string{`"id":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"`, `"teamId":"68d52710-76d9-4b41-ba45-778511d0e2ed"`, `"title":"Example issue"`, "issueCreate"} {
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
	issue, err := client.UpdateIssue(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed", UpdateIssueInput{Title: "Revised title", Description: "Revised description"})
	if err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}
	if issue.Identifier != "SHA-1" {
		t.Fatalf("issue = %+v", issue)
	}
	for _, want := range []string{`"id":"68d52710-76d9-4b41-ba45-778511d0e2ed"`, `"title":"Revised title"`, "issueUpdate"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %q lacks %q", gotBody, want)
		}
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
