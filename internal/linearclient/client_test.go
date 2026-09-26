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
	setProjectID := "project-uuid-1"
	if _, err := client.UpdateIssue(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed", UpdateIssueInput{Title: "Revised title", Description: "Revised description", ProjectID: &setProjectID, StatusID: "state-cancelled", AddedLabelIDs: []string{"label-task"}}); err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}
	if strings.Contains(gotBody, `"priority":`) {
		t.Fatalf("issueUpdate body %q must never resend a priority: Linear owns triage after creation", gotBody)
	}
}

// An issue_update carries the issue's full Project
// state. A nil ProjectID marshals as an explicit JSON null, which Linear reads
// as "clear the field" (the omitempty string omitted it, so a remote issue
// kept its Project after its last Initiative entry left), and RemovedLabelIDs
// carries the Concord-managed labels that no longer apply.
func TestUpdateIssueSendsProjectIdNullAndRemovedLabelIds(t *testing.T) {
	var gotBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","identifier":"SHA-1","url":"https://linear.app/example/issue/SHA-1","updatedAt":"2026-09-09T12:01:00Z"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateIssue(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed", UpdateIssueInput{Title: "T"}); err != nil {
		t.Fatalf("UpdateIssue(nil project) error = %v", err)
	}
	if !strings.Contains(gotBodies[0], `"projectId":null`) {
		t.Fatalf("request body %q lacks the explicit null projectId that clears the field", gotBodies[0])
	}
	if _, err := client.UpdateIssue(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed", UpdateIssueInput{Title: "T", ProjectID: &[]string{"remote-project-1"}[0], RemovedLabelIDs: []string{"label-stale"}}); err != nil {
		t.Fatalf("UpdateIssue(set project) error = %v", err)
	}
	if !strings.Contains(gotBodies[1], `"projectId":"remote-project-1"`) {
		t.Fatalf("request body %q lacks the owning Initiative's project uuid", gotBodies[1])
	}
	if !strings.Contains(gotBodies[1], `"removedLabelIds":["label-stale"]`) {
		t.Fatalf("request body %q lacks removedLabelIds for the label that no longer applies", gotBodies[1])
	}
}

// The drain needs the issue's current labels to compute the Concord-managed
// labels that no longer apply (CD-0171 D3, D5).
func TestGetIssueLabelIDsFetchesTheLabelConnection(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issue":{"labels":{"nodes":[{"id":"label-repo"},{"id":"label-optional"}]}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	labels, err := client.GetIssueLabelIDs(context.Background(), "68d52710-76d9-4b41-ba45-778511d0e2ed")
	if err != nil {
		t.Fatalf("GetIssueLabelIDs() error = %v", err)
	}
	if len(labels) != 2 || labels[0] != "label-repo" || labels[1] != "label-optional" {
		t.Fatalf("labels = %v, want the connection's label ids", labels)
	}
	if !strings.Contains(gotBody, "labels") || !strings.Contains(gotBody, "nodes") {
		t.Fatalf("request body %q, want the labels connection query", gotBody)
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

func TestCreateCommentSendsClientUUIDAndBody(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true,"comment":{"id":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	err = client.CreateComment(context.Background(), CommentCreateInput{
		ID:      "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0",
		IssueID: "68d52710-76d9-4b41-ba45-778511d0e2ed",
		Body:    "## Managed revision\n\nRevision digest: `sha256:abc`",
	})
	if err != nil {
		t.Fatalf("CreateComment() error = %v", err)
	}
	for _, want := range []string{"commentCreate", `"id":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"`, `"issueId":"68d52710-76d9-4b41-ba45-778511d0e2ed"`, `"body":"## Managed revision`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %q lacks %q", gotBody, want)
		}
	}
}

func TestCreateCommentRefusesDifferentReturnedID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true,"comment":{"id":"foreign-comment"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	err = client.CreateComment(context.Background(), CommentCreateInput{ID: "requested-comment", IssueID: "issue-1", Body: "body"})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindGraphqlError {
		t.Fatalf("different comment ID error = %v, want graphql_error", err)
	}
}

func TestCreateCommentRefusesMutationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":false,"comment":{"id":""}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	err = client.CreateComment(context.Background(), CommentCreateInput{ID: "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", IssueID: "issue-1", Body: "body"})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindGraphqlError {
		t.Fatalf("error = %v, want graphql_error", err)
	}
}

// TestCreateCommentLostResponseThenConflictIsClassified models remote
// persistence followed by a lost response: the first commentCreate is
// received and executed, then the connection drops before any response is
// written. The durable retry re-sends the same client UUID, and the live API
// answers the insert conflict instead of upserting.
// Both outcomes must keep their classification so the outbox retries the
// first and the caller converges the second.
func TestCreateCommentLostResponseThenConflictIsClassified(t *testing.T) {
	const conflictBody = `{"errors":[{"message":"conflict on insert of Comment","extensions":{"type":"invalid input","code":"INPUT_ERROR","statusCode":400,"userError":true,"userPresentableMessage":"Entity Comment with id 0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0 already exists."}}],"data":null}`
	stored := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "commentCreate") {
			t.Errorf("unexpected call: %s", body)
			return
		}
		if stored {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(conflictBody))
			return
		}
		stored = true
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	input := CommentCreateInput{ID: "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", IssueID: "issue-1", Body: "body"}
	err = client.CreateComment(context.Background(), input)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindTransport {
		t.Fatalf("lost-response error = %v, want transport", err)
	}
	if !IsRetryable(err) {
		t.Fatalf("a lost response must stay retryable: %v", err)
	}
	err = client.CreateComment(context.Background(), input)
	if !failureAs(err, &failure) || failure.Kind != KindDuplicateEntity {
		t.Fatalf("retry error = %v, want duplicate_entity", err)
	}
	if IsRetryable(err) {
		t.Fatalf("a duplicate-entity conflict must not requeue a blind resend: %v", err)
	}
	if !IsDuplicateEntity(err) {
		t.Fatalf("IsDuplicateEntity(%v) = false, want true", err)
	}
	if !strings.Contains(failure.Detail, "conflict on insert of Comment") || !strings.Contains(failure.Detail, "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0") {
		t.Fatalf("detail = %q, want the conflict message and the conflicted id surfaced", failure.Detail)
	}
}

func TestGetCommentResolvesPlacement(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"comment":{"id":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0","issueId":"issue-9","body":"stored body"}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	comment, err := client.GetComment(context.Background(), "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0")
	if err != nil {
		t.Fatalf("GetComment() error = %v", err)
	}
	if comment.ID != "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0" || comment.IssueID != "issue-9" || comment.Body != "stored body" {
		t.Fatalf("comment = %+v, want the stored identity, its issue, and its body", comment)
	}
	if !strings.Contains(gotBody, "comment(id:") || !strings.Contains(gotBody, "body") {
		t.Fatalf("request body %q must resolve the comment by id with its body", gotBody)
	}
}

func TestGetCommentReportsUnknownComment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"Entity not found: Comment","extensions":{"type":"invalid input","code":"INPUT_ERROR","statusCode":400,"userError":true}}],"data":null}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetComment(context.Background(), "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0")
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindGraphqlError {
		t.Fatalf("error = %v, want graphql_error for an unknown comment", err)
	}
	if IsDuplicateEntity(err) {
		t.Fatalf("an unknown-comment refusal must not classify as a duplicate entity: %v", err)
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
	if strings.Contains(bodies[0], `"after":`) || !strings.Contains(bodies[1], `"after":"cursor-1"`) {
		t.Fatalf("first page must omit the after cursor; cursor flow: first %q then %q", bodies[0], bodies[1])
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

func TestCreateProjectSendsClientUUIDAndContentFields(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"projectCreate":{"success":true,"project":{"id":"proj-uuid-1","name":"Initiative title","description":"Initiative value statement","content":"The narrative.","url":"https://linear.app/example/project/proj-uuid-1","updatedAt":"2026-09-23T00:00:00Z"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.CreateProject(context.Background(), CreateProjectInput{
		ID:          "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0",
		TeamIDs:     []string{"team-uuid-1"},
		Name:        "Initiative title",
		Description: "Initiative value statement",
		Content:     "The narrative.",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if project.ID != "proj-uuid-1" || project.Name != "Initiative title" || project.Content != "The narrative." || project.URL == "" {
		t.Fatalf("project = %+v", project)
	}
	// CD-0171 d2: the Concord-generated UUID rides ProjectCreateInput.id, so
	// a replayed create converges on the same remote Project. CD-0171 d3:
	// description is the short field, content the markdown narrative.
	for _, want := range []string{`"id":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"`, `"teamIds":["team-uuid-1"]`, `"name":"Initiative title"`, `"description":"Initiative value statement"`, `"content":"The narrative."`, "projectCreate"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %q lacks %q", gotBody, want)
		}
	}
}

func TestUpdateProjectAddressesTheRemoteUUID(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"projectUpdate":{"success":true,"project":{"id":"proj-uuid-1","name":"Initiative title","updatedAt":"2026-09-23T01:00:00Z"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateProject(context.Background(), "proj-uuid-1", UpdateProjectInput{Name: "Initiative title", Description: "Revised value", Content: "Revised narrative."}); err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	for _, want := range []string{"projectUpdate", `"id":"proj-uuid-1"`, `"description":"Revised value"`, `"content":"Revised narrative."`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %q lacks %q", gotBody, want)
		}
	}
}

// Content rides only when the caller holds a narrative: an empty content is
// omitted, so Linear keeps its current markdown and the drain never clears
// the Project doc. Description always rides as the full-state statement.
func TestUpdateProjectOmitsAnEmptyContent(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"projectUpdate":{"success":true,"project":{"id":"proj-uuid-1","name":"Initiative title","updatedAt":"2026-09-23T01:00:00Z"}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateProject(context.Background(), "proj-uuid-1", UpdateProjectInput{Name: "Initiative title", Description: "Value statement", Content: ""}); err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if !strings.Contains(gotBody, `"description":"Value statement"`) {
		t.Fatalf("request body %q lacks %q", gotBody, `"description":"Value statement"`)
	}
	if strings.Contains(gotBody, `"content":`) {
		t.Fatalf("request body %q carries a content field; an empty narrative must keep the remote markdown", gotBody)
	}
}

// The initiative import maps the Linear Project's summary to the Concord
// value statement, so GetProject requests and parses the summary field next
// to the legacy description and the markdown content.
func TestGetProjectReadsSummary(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"project":{"id":"proj-uuid-1","name":"Initiative title","summary":"Initiative value","description":"A long legacy document.","content":"# The doc","url":"https://linear.app/example/project/proj-uuid-1","updatedAt":"2026-09-23T01:00:00Z","teams":{"nodes":[{"id":"team-uuid-1"}]}}}}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.GetProject(context.Background(), "proj-uuid-1")
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if project.Summary != "Initiative value" || project.Description != "A long legacy document." || project.Content != "# The doc" {
		t.Fatalf("project = %+v, want the summary, description, and content parsed", project)
	}
	if len(project.TeamIDs) != 1 || project.TeamIDs[0] != "team-uuid-1" {
		t.Fatalf("team ids = %v, want team-uuid-1", project.TeamIDs)
	}
	if !strings.Contains(gotBody, "summary description content") {
		t.Fatalf("request body %q lacks the summary field in the project selection", gotBody)
	}
}

func TestGetProjectRefusesAnUnknownProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"Entity not found: Project","extensions":{"code":"NOT_FOUND"}}]}`))
	}))
	defer server.Close()
	client, err := New("lin_api_test", WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetProject(context.Background(), "ghost-project-uuid"); err == nil {
		t.Fatal("GetProject() on an unknown project must fail")
	} else {
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindGraphqlError {
			t.Fatalf("GetProject() error = %v, want graphql_error", err)
		}
	}
}
