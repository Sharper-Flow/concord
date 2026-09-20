package linearclient

import (
	"context"
	"io"
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
		LabelIDs:    []string{"label-task", "label-expedite"},
		StatusID:    "state-needed",
	})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	if gotAuth != "lin_api_test" {
		t.Fatalf("Authorization = %q, want the raw key without a Bearer prefix", gotAuth)
	}
	for _, want := range []string{`"id":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"`, `"teamId":"68d52710-76d9-4b41-ba45-778511d0e2ed"`, `"projectId":"project-uuid-1"`, `"title":"Example issue"`, `"labelIds":["label-task","label-expedite"]`, `"stateId":"state-needed"`, "issueCreate"} {
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

func TestCreateIssueSendsPrioritySeedOnce(t *testing.T) {
	cases := []struct {
		name       string
		priority   int
		wantInBody string
		wantAbsent bool
	}{
		{name: "expedite seeds urgent", priority: 1, wantInBody: `"priority":1`},
		{name: "standard seeds medium", priority: 3, wantInBody: `"priority":3`},
		{name: "unset omits the field", priority: 0, wantAbsent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			_, err = client.CreateIssue(context.Background(), CreateIssueInput{
				ID: "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", TeamID: "team-uuid-1", Title: "Example issue", Priority: tc.priority,
			})
			if err != nil {
				t.Fatalf("CreateIssue() error = %v", err)
			}
			if tc.wantAbsent {
				if strings.Contains(gotBody, `"priority":`) {
					t.Fatalf("request body %q must not carry a priority", gotBody)
				}
				return
			}
			if !strings.Contains(gotBody, tc.wantInBody) {
				t.Fatalf("request body %q lacks %q", gotBody, tc.wantInBody)
			}
		})
	}
}

func TestUpdateIssueNeverSendsPriority(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","identifier":"SHA-1","url":"https://linear.app/example/issue/SHA-1","updatedAt":"2026-09-09T12:01:00Z"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateIssue(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed", UpdateIssueInput{Title: "Revised title", Description: "Revised description", ProjectID: "project-uuid-1", StatusID: "state-cancelled", AddedLabelIDs: []string{"label-task"}}); err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}
	if strings.Contains(gotBody, `"priority":`) {
		t.Fatalf("issueUpdate body %q must never resend a priority: Linear owns triage after creation", gotBody)
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
	issue, err := client.UpdateIssue(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed", UpdateIssueInput{Title: "Revised title", Description: "Revised description", StatusID: "state-cancelled", AddedLabelIDs: []string{"label-task"}})
	if err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}
	if issue.Identifier != "SHA-1" {
		t.Fatalf("issue = %+v", issue)
	}
	for _, want := range []string{`"id":"68d52710-76d9-4b41-ba45-778511d0e2ed"`, `"title":"Revised title"`, `"stateId":"state-cancelled"`, `"addedLabelIds":["label-task"]`, "issueUpdate"} {
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
		{"rate limited on HTTP 200", http.StatusOK, `{"errors":[{"message":"Rate limit exceeded. Only 2500 requests are allowed per 1 hour. RATELIMITED"}]}`, "", KindRateLimited},
		{"rate limited extension code on 400", http.StatusBadRequest, `{"errors":[{"message":"Too many requests","extensions":{"code":"RATELIMITED"}}]}`, "", KindRateLimited},
		{"rate limited extension code on 200", http.StatusOK, `{"errors":[{"message":"Too many requests","extensions":{"code":"RATELIMITED"}}]}`, "", KindRateLimited},
		{"rate limit exceeded message without token", http.StatusOK, `{"errors":[{"message":"Rate limit exceeded. Only 2500 requests are allowed per 1 hour."}]}`, "", KindRateLimited},
		{"quota message without token", http.StatusBadRequest, `{"errors":[{"message":"You have exceeded your request quota."}]}`, "", KindRateLimited},
		{"graphql error", http.StatusOK, `{"errors":[{"message":"team not found"}]}`, "", KindGraphqlError},
		{"non-rate-limit extension code", http.StatusBadRequest, `{"errors":[{"message":"invalid input","extensions":{"code":"BAD_USER_INPUT"}}]}`, "", KindGraphqlError},
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

func TestListTeamStartedIssuesFollowsCursor(t *testing.T) {
	var bodies []string
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if page == 0 {
			page++
			_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[{"id":"issue-1","identifier":"CON-22","url":"https://linear.app/example/issue/CON-22","title":"First","updatedAt":"2026-09-18T00:00:00Z","state":{"id":"state-in-progress","type":"started"}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[{"id":"issue-2","identifier":"POKE-179","url":"https://linear.app/example/issue/POKE-179","title":"Second","updatedAt":"2026-09-19T00:00:00Z","state":{"id":"state-in-progress","type":"started"}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	issues, err := client.ListTeamStartedIssues(context.Background(), "team-uuid-1")
	if err != nil {
		t.Fatalf("ListTeamStartedIssues() error = %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues = %+v, want two across two pages", issues)
	}
	if issues[0].ID != "issue-1" || issues[0].Identifier != "CON-22" || issues[0].StateID != "state-in-progress" || issues[0].StateType != "started" || issues[0].TeamID != "team-uuid-1" {
		t.Fatalf("first issue = %+v", issues[0])
	}
	if issues[1].ID != "issue-2" || issues[1].Identifier != "POKE-179" {
		t.Fatalf("second issue = %+v", issues[1])
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want one per page", len(bodies))
	}
	for _, want := range []string{`"teamId":"team-uuid-1"`, `state: { type: { eq: \"started\" } }`} {
		if !strings.Contains(bodies[0], want) {
			t.Fatalf("first request %q lacks %q", bodies[0], want)
		}
	}
	if !strings.Contains(bodies[0], `"after":""`) || !strings.Contains(bodies[1], `"after":"cursor-1"`) {
		t.Fatalf("cursor flow broken: first %q then %q", bodies[0], bodies[1])
	}
}

func TestListTeamStartedIssuesRefusesCursorlessNextPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":""}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListTeamStartedIssues(context.Background(), "team-uuid-1"); err == nil {
		t.Fatal("ListTeamStartedIssues() = nil error, want malformed-response failure")
	} else {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindMalformedResponse {
			t.Fatalf("error = %v, want malformed_response", err)
		}
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
	if _, err := client.GetIssue(context.Background(), "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("GetIssue() for an unknown issue = nil error, want failure")
	} else if IsRetryable(err) {
		t.Fatalf("unknown issue error %v is retryable, want permanent", err)
	}
}
