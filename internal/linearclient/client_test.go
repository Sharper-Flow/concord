package linearclient

import (
	"context"
	"errors"
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

func TestGetIssueResolvesIdentityAndTeam(t *testing.T) {
	var gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","identifier":"SHA-7","url":"https://linear.app/example/issue/SHA-7","title":"Fetched title","description":"Fetched description","updatedAt":"2026-09-15T08:00:00Z","state":{"type":"unstarted"},"team":{"id":"team-uuid-1"}}}}`))
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
	if gotAuth != "lin_api_test" {
		t.Fatalf("Authorization = %q, want the raw key without a Bearer prefix", gotAuth)
	}
	if !resolved.UpdatedAt.Equal(time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("updatedAt = %v", resolved.UpdatedAt)
	}
	if !strings.Contains(gotBody, `"query":"query($id: String!) { issue(id: $id) { id identifier url title description updatedAt state { id type } team { id } } }"`) {
		t.Fatalf("request body %q lacks the issue query", gotBody)
	}
	if resolved.ID != "68d52710-76d9-4b41-ba45-778511d0e2ed" || resolved.Identifier != "SHA-7" || resolved.Title != "Fetched title" || resolved.Description != "Fetched description" || resolved.TeamID != "team-uuid-1" || resolved.StateType != "unstarted" {
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
	_, err = client.GetIssue(context.Background(), "00000000-0000-0000-0000-000000000000")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindGraphqlError {
		t.Fatalf("unknown issue error = %v, want graphql_error", err)
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
		{"rate limited on HTTP 200", http.StatusOK, `{"errors":[{"message":"Rate limit exceeded. Only 2500 requests are allowed per 1 hour. RATELIMITED"}]}`, "", KindRateLimited},
		{"rate limited extension code on 400", http.StatusBadRequest, `{"errors":[{"message":"Too many requests","extensions":{"code":"RATELIMITED"}}]}`, "", KindRateLimited},
		{"rate limited extension code on 200", http.StatusOK, `{"errors":[{"message":"Too many requests","extensions":{"code":"RATELIMITED"}}]}`, "", KindRateLimited},
		{"rate limit exceeded message without token", http.StatusOK, `{"errors":[{"message":"Rate limit exceeded. Only 2500 requests are allowed per 1 hour."}]}`, "", KindRateLimited},
		{"quota message without token", http.StatusBadRequest, `{"errors":[{"message":"You have exceeded your request quota."}]}`, "", KindRateLimited},
		{"graphql error", http.StatusOK, `{"errors":[{"message":"team not found"}]}`, "", KindGraphqlError},
		{"non-rate-limit extension code", http.StatusBadRequest, `{"errors":[{"message":"invalid input","extensions":{"code":"BAD_USER_INPUT"}}]}`, "", KindGraphqlError},
		{"error body surfaces", http.StatusBadRequest, `{"errors":[{"message":"an API key is not a Bearer token"}]}`, "", KindGraphqlError},
		{"duplicate comment id on 200", http.StatusOK, `{"errors":[{"message":"conflict on insert of Comment","extensions":{"code":"INPUT_ERROR"}}],"data":null}`, "", KindDuplicateEntity},
		{"duplicate issue id on 400", http.StatusBadRequest, `{"errors":[{"message":"conflict on insert of Issue","extensions":{"code":"INPUT_ERROR"}}],"data":null}`, "", KindDuplicateEntity},
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
			_, err = client.GetIssue(context.Background(), "issue-under-test")
			if err == nil {
				t.Fatal("GetIssue() must fail")
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
	_, err = client.GetIssue(context.Background(), "issue-under-test")
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
