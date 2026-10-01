package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

// seedResumeLinearFixture prepares the resume path for a linear_enabled
// Product: authority seeded, one captured work item bootstrapped into its
// canonical worktree while the Product is still local_only (so capture
// enqueues nothing), the lifecycle moved to in_progress, the Product
// enabled, and a confirmed link recorded with the given remote freshness.
func seedResumeLinearFixture(t *testing.T, dbPath string) (repo, workID, remoteUUID string) {
	t.Helper()
	repo = initLocatorRepo(t)
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	transitionWorkItem(t, s, origin.WorkID, "needed", "in_progress", origin.WorkVersion)
	enableLinearProduct(t, dbPath, "product-wl")
	remoteUUID = "remote-cancel-1"
	for _, state := range []string{store.LinearLinkUnpublished, store.LinearLinkPending, store.LinearLinkConfirmed} {
		updated := ""
		if state == store.LinearLinkConfirmed {
			updated = "2026-09-25T19:28:00Z"
		}
		if err := s.RecordLinearLink(context.Background(), origin.WorkID, remoteUUID, "CON-77", "https://linear.app/example/issue/CON-77", updated, "", state); err != nil {
			t.Fatal(err)
		}
	}
	return repo, origin.WorkID, remoteUUID
}

// serveLinearRemoteIssue routes one GraphQL endpoint: the issue read and the
// comments read each see their own fixture. It counts requests so the
// no-call paths can prove none happened.
type linearResumeServer struct {
	server    *httptest.Server
	requests  int
	afterSeen string
}

func serveLinearRemoteIssue(t *testing.T, issueJSON, commentsJSON string) *linearResumeServer {
	t.Helper()
	helper := &linearResumeServer{}
	helper.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		helper.requests++
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "comments(") {
			var request struct {
				Variables struct {
					After string `json:"after"`
				} `json:"variables"`
			}
			_ = json.Unmarshal(body, &request)
			helper.afterSeen = request.Variables.After
			_, _ = w.Write([]byte(`{"data":{"issue":{"comments":` + commentsJSON + `}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":` + issueJSON + `}}`))
	}))
	t.Cleanup(helper.server.Close)
	return helper
}

// TestWorkResumeCommentsStayInsideTheEnvelopeBudget proves the encoded
// budget: a full page of multibyte bodies reports fewer comments than the
// page held and marks the page truncated instead of overflowing the adapter
// envelope.
func TestWorkResumeCommentsStayInsideTheEnvelopeBudget(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	repo, workID, remoteUUID := seedResumeLinearFixture(t, dbPath)
	nodes := make([]string, 0, linearResumeCommentPage)
	for index := range linearResumeCommentPage {
		nodes = append(nodes, `{"id":"comment-`+strconv.Itoa(index)+`","body":"`+strings.Repeat("\U0001F600", linearResumeCommentBodyLimit)+`","createdAt":"2026-09-27T00:0`+strconv.Itoa(index%10)+`:00Z","user":{"name":"Dana","displayName":"Dana D"}}`)
	}
	helper := serveLinearRemoteIssue(t,
		`{"id":"`+remoteUUID+`","identifier":"CON-77","url":"https://linear.app/example/issue/CON-77","title":"Bootstrap work","description":"","updatedAt":"2026-09-25T19:28:00Z","state":{"id":"state-in-progress","type":"started"},"team":{"id":"team-uuid-1"}}`,
		`{"nodes":[`+strings.Join(nodes, ",")+`],"pageInfo":{"hasNextPage":false}}`)
	t.Setenv(linearclient.EnvEndpoint, helper.server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_resume_test")

	code, output, stderr := resumeCLI(t, mustOpenStore(t, dbPath), repo, workID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	remote := output.LinearRemote
	if remote == nil || remote.Comments == nil {
		t.Fatalf("linear_remote=%+v want comments", remote)
	}
	if !remote.Comments.Truncated {
		t.Fatal("the budget-stopped page must report truncated")
	}
	if len(remote.Comments.Items) == 0 || len(remote.Comments.Items) >= linearResumeCommentPage {
		t.Fatalf("comments items=%d want a partial page", len(remote.Comments.Items))
	}
	encoded, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 48*1024 {
		t.Fatalf("encoded section=%d bytes approaches the adapter envelope", len(encoded))
	}
}

// TestWorkResumeCommentsFailureDegradesOnlyTheComments proves the partial
// degradation: a comments read that fails after a successful issue read
// keeps the comparison and states the typed reason on the comments alone.
func TestWorkResumeCommentsFailureDegradesOnlyTheComments(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	repo, workID, remoteUUID := seedResumeLinearFixture(t, dbPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "comments(") {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"` + remoteUUID + `","identifier":"CON-77","title":"Bootstrap work","description":"","updatedAt":"2026-09-25T19:28:00Z","state":{"id":"state-in-progress","type":"started"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_resume_test")

	code, output, stderr := resumeCLI(t, mustOpenStore(t, dbPath), repo, workID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	remote := output.LinearRemote
	if remote == nil || remote.Authority != "ok" {
		t.Fatalf("linear_remote=%+v want authority ok with a failed comments read", remote)
	}
	if remote.Comments == nil || remote.Comments.Reason != "rate_limited" || len(remote.Comments.Items) != 0 || remote.Comments.Truncated {
		t.Fatalf("linear_remote comments=%+v want degraded rate_limited", remote.Comments)
	}
	if remote.Status == nil || remote.Status.Mismatch {
		t.Fatalf("linear_remote status=%+v want the comparison kept", remote.Status)
	}
}

// TestWorkResumeSurfacesCancelledRemoteWhileInProgress is the deterministic
// replacement for the POKE-295 live case: the issue reads canceled remotely
// while the item is in_progress, with a newer updatedAt, a changed title, and
// one new comment. The section reports the mismatch, the drift, and the
// comment, and the resume records nothing: the link row and the outbox are
// untouched afterwards.
func TestWorkResumeSurfacesCancelledRemoteWhileInProgress(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	const recorded = "2026-09-25T19:28:00Z"
	repo, workID, remoteUUID := seedResumeLinearFixture(t, dbPath)
	helper := serveLinearRemoteIssue(t,
		`{"id":"`+remoteUUID+`","identifier":"CON-77","url":"https://linear.app/example/issue/CON-77","title":"Renamed by the coordinator","description":"New remote body","updatedAt":"2026-09-28T12:00:00Z","state":{"id":"state-cancelled-remote","type":"canceled"},"team":{"id":"team-uuid-1"}}`,
		`{"nodes":[{"id":"comment-1","body":"Heads up: this moved on","createdAt":"2026-09-27T00:00:00Z","user":{"name":"Dana","displayName":"Dana D"}}],"pageInfo":{"hasNextPage":false}}`)
	t.Setenv(linearclient.EnvEndpoint, helper.server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_resume_test")

	code, output, stderr := resumeCLI(t, mustOpenStore(t, dbPath), repo, workID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	if helper.requests != 2 {
		t.Fatalf("linear requests=%d want one issue read and one comments read", helper.requests)
	}
	remote := output.LinearRemote
	if remote == nil || remote.Authority != "ok" {
		t.Fatalf("linear_remote=%+v want authority ok", remote)
	}
	if remote.Reason != "" || remote.ChangedSinceRecorded == nil || !*remote.ChangedSinceRecorded || remote.UpdatedAt == nil || *remote.UpdatedAt != "2026-09-28T12:00:00Z" {
		t.Fatalf("linear_remote freshness=%+v", remote)
	}
	if remote.Status == nil || remote.Status.Expected != "state-in-progress" || remote.Status.Actual != "state-cancelled-remote" || remote.Status.RemoteStateType != "canceled" || !remote.Status.Mismatch {
		t.Fatalf("linear_remote status=%+v", remote.Status)
	}
	if remote.Title == nil || remote.Title.Remote != "Renamed by the coordinator" || !remote.Title.Differs {
		t.Fatalf("linear_remote title=%+v", remote.Title)
	}
	if remote.Description == nil || *remote.Description != "New remote body" || remote.DescriptionTruncated == nil || *remote.DescriptionTruncated {
		t.Fatalf("linear_remote description=%+v truncated=%v", remote.Description, remote.DescriptionTruncated)
	}
	if remote.Comments == nil || len(remote.Comments.Items) != 1 || remote.Comments.Truncated || remote.Comments.Reason != "" {
		t.Fatalf("linear_remote comments=%+v", remote.Comments)
	}
	if remote.Comments.Items[0].Author != "Dana" || remote.Comments.Items[0].CreatedAt != "2026-09-27T00:00:00Z" || remote.Comments.Items[0].Body != "Heads up: this moved on" {
		t.Fatalf("linear_remote comment item=%+v", remote.Comments.Items[0])
	}
	// The comment window is the recorded freshness, not the content hash.
	if helper.afterSeen != recorded {
		t.Fatalf("comments after=%q want recorded %q", helper.afterSeen, recorded)
	}

	// Resume records nothing: the link row keeps its recorded freshness and
	// the outbox stays empty.
	s := mustOpenStore(t, dbPath)
	var linkState, linkUpdated string
	if err := s.DatabaseForTesting().QueryRow(`SELECT link_state, remote_updated_at FROM linear_issue_links WHERE work_id=?`, workID).Scan(&linkState, &linkUpdated); err != nil {
		t.Fatal(err)
	}
	if linkState != store.LinearLinkConfirmed || linkUpdated != recorded {
		t.Fatalf("link row after resume: state=%s updated_at=%q", linkState, linkUpdated)
	}
	var outbox int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if outbox != 0 {
		t.Fatalf("outbox rows=%d want 0", outbox)
	}
}

// TestWorkResumeMatchesUnchangedRemote proves the quiet path: an issue that
// still matches its recorded freshness, mapped status, and local title
// reports no drift, no mismatch, and no comments.
func TestWorkResumeMatchesUnchangedRemote(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	const recorded = "2026-09-25T19:28:00Z"
	repo, workID, remoteUUID := seedResumeLinearFixture(t, dbPath)
	helper := serveLinearRemoteIssue(t,
		`{"id":"`+remoteUUID+`","identifier":"CON-77","url":"https://linear.app/example/issue/CON-77","title":"Bootstrap work","description":"Same body","updatedAt":"`+recorded+`","state":{"id":"state-in-progress","type":"started"},"team":{"id":"team-uuid-1"}}`,
		`{"nodes":[],"pageInfo":{"hasNextPage":false}}`)
	t.Setenv(linearclient.EnvEndpoint, helper.server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_resume_test")

	code, output, stderr := resumeCLI(t, mustOpenStore(t, dbPath), repo, workID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	remote := output.LinearRemote
	if remote == nil || remote.Authority != "ok" {
		t.Fatalf("linear_remote=%+v want authority ok", remote)
	}
	if remote.ChangedSinceRecorded == nil || *remote.ChangedSinceRecorded || remote.Status == nil || remote.Status.Mismatch || remote.Status.Expected != "state-in-progress" || remote.Status.Actual != "state-in-progress" {
		t.Fatalf("linear_remote freshness/status=%+v", remote)
	}
	if remote.Title == nil || remote.Title.Differs || remote.Title.Remote != "Bootstrap work" {
		t.Fatalf("linear_remote title=%+v", remote.Title)
	}
	if remote.Comments == nil || len(remote.Comments.Items) != 0 || remote.Comments.Truncated {
		t.Fatalf("linear_remote comments=%+v", remote.Comments)
	}
}

// TestWorkResumeRemoteDegradedStillSucceeds covers the degraded vocabulary:
// every Linear read failure states its typed reason and the resume itself
// still succeeds with its worktree intact.
func TestWorkResumeRemoteDegradedStillSucceeds(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		handler func(t *testing.T, w http.ResponseWriter)
		reason  string
	}{
		{
			name: "unauthorized",
			key:  "lin_api_resume_test",
			handler: func(_ *testing.T, w http.ResponseWriter) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			reason: "unauthorized",
		},
		{
			name: "rate_limited",
			key:  "lin_api_resume_test",
			handler: func(_ *testing.T, w http.ResponseWriter) {
				w.WriteHeader(http.StatusTooManyRequests)
			},
			reason: "rate_limited",
		},
		{
			name: "missing_credentials",
			key:  "",
			handler: func(t *testing.T, _ http.ResponseWriter) {
				t.Fatal("a resume without credentials must make no Linear call")
			},
			reason: "missing_credentials",
		},
		{
			name: "not_found",
			key:  "lin_api_resume_test",
			handler: func(_ *testing.T, w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"data":{"issue":null}}`))
			},
			reason: "not_found",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "concord.db")
			repo, workID, _ := seedResumeLinearFixture(t, dbPath)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				testCase.handler(t, w)
			}))
			defer server.Close()
			t.Setenv(linearclient.EnvEndpoint, server.URL)
			t.Setenv(linearclient.EnvAPIKey, testCase.key)

			code, output, stderr := resumeCLI(t, mustOpenStore(t, dbPath), repo, workID)
			if code != 0 {
				t.Fatalf("resume code=%d stderr=%q", code, stderr)
			}
			remote := output.LinearRemote
			if remote == nil || remote.Authority != "degraded" || remote.Reason != testCase.reason {
				t.Fatalf("linear_remote=%+v want degraded %s", remote, testCase.reason)
			}
			if remote.Status != nil || remote.Title != nil || remote.Comments != nil || remote.UpdatedAt != nil {
				t.Fatalf("degraded linear_remote carries comparison fields: %+v", remote)
			}
			if output.Worktree.State != "active" {
				t.Fatalf("degraded resume lost the worktree: %+v", output.Worktree)
			}
		})
	}
}

// TestWorkResumeRemoteTimeoutStillSucceeds proves the timeout vocabulary and
// that a hung Linear endpoint cannot hang the worktree move.
func TestWorkResumeRemoteTimeoutStillSucceeds(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	repo, workID, _ := seedResumeLinearFixture(t, dbPath)
	previousTimeout := linearResumeTimeout
	linearResumeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { linearResumeTimeout = previousTimeout })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_resume_test")

	code, output, stderr := resumeCLI(t, mustOpenStore(t, dbPath), repo, workID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	remote := output.LinearRemote
	if remote == nil || remote.Authority != "degraded" || remote.Reason != "timeout" {
		t.Fatalf("linear_remote=%+v want degraded timeout", remote)
	}
}

// TestWorkResumeWithoutApplicableLinkMakesNoLinearCall proves the unchanged
// outputs: a local_only Product and a linear_enabled item without a
// confirmed link both resume with no linear_remote field and no Linear call.
func TestWorkResumeWithoutApplicableLinkMakesNoLinearCall(t *testing.T) {
	t.Run("local_only_product", func(t *testing.T) {
		repo := initLocatorRepo(t)
		s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
		seedLocatorAuthority(t, s, repo)
		origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Error("a local_only resume must make no Linear call")
		}))
		defer server.Close()
		t.Setenv(linearclient.EnvEndpoint, server.URL)
		t.Setenv(linearclient.EnvAPIKey, "lin_api_resume_test")

		code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
		if code != 0 {
			t.Fatalf("resume code=%d stderr=%q", code, stderr)
		}
		if output.LinearRemote != nil {
			t.Fatalf("local_only resume carried linear_remote: %+v", output.LinearRemote)
		}
	})
	t.Run("no_confirmed_link", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "concord.db")
		repo := initLocatorRepo(t)
		s := mustOpenStore(t, dbPath)
		seedLocatorAuthority(t, s, repo)
		origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
		if err != nil {
			t.Fatal(err)
		}
		enableLinearProduct(t, dbPath, "product-wl")
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Error("an unlinked resume must make no Linear call")
		}))
		defer server.Close()
		t.Setenv(linearclient.EnvEndpoint, server.URL)
		t.Setenv(linearclient.EnvAPIKey, "lin_api_resume_test")

		code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
		if code != 0 {
			t.Fatalf("resume code=%d stderr=%q", code, stderr)
		}
		if output.LinearRemote != nil {
			t.Fatalf("unlinked resume carried linear_remote: %+v", output.LinearRemote)
		}
	})
}

// TestWorkResumeCutCommentBodyReportsTruncated proves a comment body cut to
// its rune limit marks the page truncated even when every comment is kept.
func TestWorkResumeCutCommentBodyReportsTruncated(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	repo, workID, remoteUUID := seedResumeLinearFixture(t, dbPath)
	helper := serveLinearRemoteIssue(t,
		`{"id":"`+remoteUUID+`","identifier":"CON-77","url":"https://linear.app/example/issue/CON-77","title":"Bootstrap work","description":"","updatedAt":"2026-09-26T00:00:00Z","state":{"id":"state-in-progress","type":"started"},"team":{"id":"team-uuid-1"}}`,
		`{"nodes":[{"id":"comment-long","body":"`+strings.Repeat("x", linearResumeCommentBodyLimit+1)+`","createdAt":"2026-09-27T00:00:00Z","user":{"name":"Dana","displayName":"Dana D"}}],"pageInfo":{"hasNextPage":false}}`)
	t.Setenv(linearclient.EnvEndpoint, helper.server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_resume_test")

	code, output, stderr := resumeCLI(t, mustOpenStore(t, dbPath), repo, workID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	comments := output.LinearRemote.Comments
	if comments == nil || len(comments.Items) != 1 || !comments.Truncated {
		t.Fatalf("comments=%+v want one kept comment marked truncated", comments)
	}
	if got := len([]rune(comments.Items[0].Body)); got != linearResumeCommentBodyLimit {
		t.Fatalf("kept body runes=%d want %d", got, linearResumeCommentBodyLimit)
	}
}

// TestWorkResumeVanishedIssueDegradesTheComments proves an issue that
// disappears between the issue read and the comments read reports degraded
// comments, never a complete empty page.
func TestWorkResumeVanishedIssueDegradesTheComments(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	repo, workID, remoteUUID := seedResumeLinearFixture(t, dbPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "comments(") {
			_, _ = w.Write([]byte(`{"data":{"issue":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"` + remoteUUID + `","identifier":"CON-77","title":"Bootstrap work","description":"","updatedAt":"2026-09-25T19:28:00Z","state":{"id":"state-in-progress","type":"started"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_resume_test")

	code, output, stderr := resumeCLI(t, mustOpenStore(t, dbPath), repo, workID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	comments := output.LinearRemote.Comments
	if comments == nil || comments.Reason != "not_found" || len(comments.Items) != 0 {
		t.Fatalf("comments=%+v want degraded not_found", comments)
	}
}

// TestLinearRemoteLocalReadFailureDegrades proves a failed local read is
// reported as local_unavailable instead of silently omitting the section.
func TestLinearRemoteLocalReadFailureDegrades(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	_, workID, _ := seedResumeLinearFixture(t, dbPath)
	s := mustOpenStore(t, dbPath)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	remote := checkLinearRemote(context.Background(), s, workID)
	if remote == nil || remote.Authority != "degraded" || remote.Reason != "local_unavailable" {
		t.Fatalf("linear_remote=%+v want degraded local_unavailable", remote)
	}
}
