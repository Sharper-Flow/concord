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

	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

// seedDescriptionOwnershipCase seeds a linear_enabled Product whose work item
// carries a confirmed link with the given content hash and a queued
// issue_update operation. The composed payload title is the seeded work title
// and the description is composeLinearIssueBody of the seeded value statement.
func seedDescriptionOwnershipCase(t *testing.T, dbPath, productID, projectID, workID, title, confirmedHash string) {
	t.Helper()
	seedCLIProduct(t, dbPath, productID, projectID)
	enableLinearProduct(t, dbPath, productID)
	runOperatorJSON(t, dbPath, []string{"linear-connection-update"}, map[string]any{
		"event_id": "ownership-label-update", "resource_id": "drain-conn-" + productID, "product_id": productID,
		"label_ids": map[string]string{"task": "label-task", "project:" + projectID: "label-" + projectID + "-repo"}, "expected_resource_version": 1,
	})
	seedLinearCLIWork(t, dbPath, workID, projectID, title)
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, step := range []struct {
		state string
		hash  string
	}{{store.LinearLinkUnpublished, ""}, {store.LinearLinkPending, ""}, {store.LinearLinkConfirmed, confirmedHash}} {
		if err := s.RecordLinearLink(ctx, workID, "remote-ownership", "CON-7", "https://linear.app/example/issue/CON-7", "", step.hash, step.state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.EnqueueLinearIssueForWork(ctx, workID, store.LinearOpIssueUpdate); err != nil {
		t.Fatal(err)
	}
}

func drainDescriptionOwnershipProduct(t *testing.T, dbPath string, handler http.HandlerFunc) (operations []struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_ownership_test")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"ownership-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	var drained struct {
		Operations []struct {
			Outcome string `json:"outcome"`
			Detail  string `json:"detail"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &drained); err != nil {
		t.Fatalf("drain output %q: %v", out.String(), err)
	}
	return drained.Operations
}

// ownedTestBody renders the description the enqueue path composes for a work
// item seeded by seedLinearCLIWork: the seeded value statement, the task kind,
// and the documented resume line.
func ownedTestBody(workID string) string {
	return "## Value statement\n\nCLI drain value statement\n\ntask · Resume: `concord zl " + workID + " --`"
}

// revisedTestBody renders the description after an approved later revision
// changed the work item's value statement.
func revisedTestBody(workID string) string {
	return "## Value statement\n\nRevised drain value statement\n\ntask · Resume: `concord zl " + workID + " --`"
}

// answerIssueRead replies to the link-refresh sweep's issue lookup with the
// body a human wrote on Linear. The drain itself performs no content read.
func answerIssueRead(t *testing.T, body *string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		requestBody, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(requestBody), "issueUpdate") {
			t.Errorf("unexpected issueUpdate: %s", requestBody)
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"remote-ownership","identifier":"CON-7","url":"https://linear.app/example/issue/CON-7","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-ownership","identifier":"CON-7","url":"https://linear.app/example/issue/CON-7","title":"Human title","description":` + strconvQuote(*body) + `,"updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed"}}}}`))
	}
}

func strconvQuote(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func assertContentFreeUpdate(t *testing.T, input map[string]any, where string) {
	t.Helper()
	if input == nil {
		t.Fatalf("%s sent no issueUpdate", where)
	}
	if _, sent := input["title"]; sent {
		t.Fatalf("%s sent a title: %#v", where, input)
	}
	if _, sent := input["description"]; sent {
		t.Fatalf("%s sent a description: %#v", where, input)
	}
	if input["stateId"] != "state-needed" {
		t.Fatalf("%s input = %#v, want the managed stateId", where, input)
	}
}

// capturedComment is one commentCreate request the fake Linear recorded.
type capturedComment struct {
	ID      string `json:"id"`
	IssueID string `json:"issueId"`
	Body    string `json:"body"`
}

// drainOwnershipRecorder is a fake Linear that records issueUpdate inputs and
// commentCreate requests and answers the link-refresh issue lookup with a
// human-owned body.
type drainOwnershipRecorder struct {
	humanBody string
	updates   []map[string]any
	comments  []capturedComment
	// commentStatus makes every commentCreate answer with this failure;
	// empty means every commentCreate succeeds.
	commentStatus int
	t             *testing.T
}

func (rec *drainOwnershipRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestBody, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(requestBody), "issueUpdate") {
			var request struct {
				Variables struct {
					Input map[string]any `json:"input"`
				} `json:"variables"`
			}
			if err := json.Unmarshal(requestBody, &request); err != nil {
				rec.t.Errorf("decode update request: %v", err)
			}
			rec.updates = append(rec.updates, request.Variables.Input)
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"remote-ownership","identifier":"CON-7","url":"https://linear.app/example/issue/CON-7","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
			return
		}
		if strings.Contains(string(requestBody), "commentCreate") {
			var request struct {
				Variables struct {
					Input struct {
						ID      string `json:"id"`
						IssueID string `json:"issueId"`
						Body    string `json:"body"`
					} `json:"input"`
				} `json:"variables"`
			}
			if err := json.Unmarshal(requestBody, &request); err != nil {
				rec.t.Errorf("decode comment request: %v", err)
			}
			rec.comments = append(rec.comments, capturedComment{ID: request.Variables.Input.ID, IssueID: request.Variables.Input.IssueID, Body: request.Variables.Input.Body})
			if rec.commentStatus != 0 {
				w.WriteHeader(rec.commentStatus)
				_, _ = w.Write([]byte(`{"errors":[{"message":"Rate limit exceeded. Only 2500 requests are allowed per 1 hour."}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true,"comment":{"id":"` + request.Variables.Input.ID + `"}}}}`))
			return
		}
		answerIssueRead(rec.t, &rec.humanBody)(w, r)
	}
}

func runOwnershipDrain(t *testing.T, dbPath, apiKey string, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, apiKey)
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"ownership-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
}

func readOwnershipLinkHash(t *testing.T, dbPath, workID string) string {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var linkHash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM linear_issue_links WHERE work_id=?`, workID).Scan(&linkHash); err != nil {
		t.Fatal(err)
	}
	return linkHash
}

func readOwnershipClientUUID(t *testing.T, dbPath, workID string) string {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var payload []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM linear_outbox WHERE work_id=? AND op_kind=?`, workID, store.LinearOpIssueUpdate).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ClientUUID string `json:"client_uuid"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.ClientUUID
}

func enqueueOwnershipUpdate(t *testing.T, dbPath, workID string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.EnqueueLinearIssueForWork(ctx, workID, store.LinearOpIssueUpdate); err != nil {
		t.Fatal(err)
	}
}

func reviseOwnershipValueStatement(t *testing.T, dbPath, workID string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	payload := `{"title":"Ownership title","value_statement":"Revised drain value statement","kind":"task","priority":0,"tags":[],"reason":"approved later revision","expected_version":` + strconv.FormatInt(version, 10) + `,"resulting_version":` + strconv.FormatInt(version+1, 10) + `}`
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{
		EventID: workID + "-intent-revised", Kind: "work.intent_revised", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedLinearTestTime(), PayloadVersion: 1,
		Payload: json.RawMessage(payload),
	}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
}

func TestLinearIssueUpdateDrainPublishesRevisionCommentWithoutOverwriting(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	workID := "ownership-work"
	createdHash := linearContentHash("Ownership title", "content Concord wrote at creation")
	seedDescriptionOwnershipCase(t, dbPath, "ownership-product", "ownership-project", workID, "Ownership title", createdHash)

	rec := &drainOwnershipRecorder{humanBody: "A human rewrote this body.", t: t}
	operations := drainDescriptionOwnershipProduct(t, dbPath, rec.handler())
	if len(operations) != 1 || operations[0].Outcome != "done" {
		t.Fatalf("drain result = %+v, want one done operation", operations)
	}
	if !strings.Contains(operations[0].Detail, "published managed revision comment") {
		t.Fatalf("revision drain detail = %q, want the publication named", operations[0].Detail)
	}

	if len(rec.updates) != 1 {
		t.Fatalf("update count = %d, want one managed routing update", len(rec.updates))
	}
	assertContentFreeUpdate(t, rec.updates[0], "revision drain")
	if len(rec.comments) != 1 {
		t.Fatalf("comment count = %d, want the revision published once (%+v)", len(rec.comments), rec.comments)
	}
	comment := rec.comments[0]
	if comment.IssueID != "remote-ownership" {
		t.Fatalf("comment issueId = %q, want the linked issue", comment.IssueID)
	}
	if comment.ID == "" || comment.ID != readOwnershipClientUUID(t, dbPath, workID) {
		t.Fatalf("comment id = %q, want the operation client uuid for retry convergence", comment.ID)
	}
	digest := linearContentHash("Ownership title", ownedTestBody(workID))
	if !strings.Contains(comment.Body, "**Managed title:** Ownership title") {
		t.Fatalf("comment body = %q, want the managed title", comment.Body)
	}
	if !strings.Contains(comment.Body, ownedTestBody(workID)) {
		t.Fatalf("comment body = %q, want the composed description", comment.Body)
	}
	if !strings.Contains(comment.Body, "Revision digest: `"+digest+"`") {
		t.Fatalf("comment body = %q, want the revision digest %s", comment.Body, digest)
	}
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != digest {
		t.Fatalf("link content hash = %s, want the published revision digest %s", linkHash, digest)
	}
}

func TestLinearIssueUpdateDrainPublishesEachRevisionOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	workID := "ownership-repeat-work"
	createdHash := linearContentHash("Ownership title", "content Concord wrote at creation")
	seedDescriptionOwnershipCase(t, dbPath, "ownership-product", "ownership-project", workID, "Ownership title", createdHash)
	digest := linearContentHash("Ownership title", ownedTestBody(workID))

	rec := &drainOwnershipRecorder{humanBody: "content Concord wrote at creation", t: t}
	runOwnershipDrain(t, dbPath, "lin_api_dedup_test", rec.handler())
	if len(rec.comments) != 1 {
		t.Fatalf("first drain comments = %d, want one revision comment", len(rec.comments))
	}

	// Between the drains a human edits the issue on Linear. The next drain
	// must keep the managed routing sync, must not rewrite the body, and
	// must not repost the revision it already published.
	rec2 := &drainOwnershipRecorder{humanBody: "A human rewrote this body between drains.", t: t}
	enqueueOwnershipUpdate(t, dbPath, workID)
	runOwnershipDrain(t, dbPath, "lin_api_dedup_test", rec2.handler())
	if len(rec2.updates) != 1 {
		t.Fatalf("second drain updates = %d, want the routing sync", len(rec2.updates))
	}
	assertContentFreeUpdate(t, rec2.updates[0], "dedup drain")
	if len(rec2.comments) != 0 {
		t.Fatalf("second drain comments = %+v, want no duplicate comment", rec2.comments)
	}
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != digest {
		t.Fatalf("link content hash = %s, want the published revision digest %s", linkHash, digest)
	}

	// A later approved revision composes new content and is published as a
	// second comment, carrying its own digest.
	reviseOwnershipValueStatement(t, dbPath, workID)
	rec3 := &drainOwnershipRecorder{humanBody: "A human rewrote this body between drains.", t: t}
	enqueueOwnershipUpdate(t, dbPath, workID)
	runOwnershipDrain(t, dbPath, "lin_api_dedup_test", rec3.handler())
	if len(rec3.comments) != 1 {
		t.Fatalf("revised drain comments = %+v, want the new revision published once", rec3.comments)
	}
	revisedDigest := linearContentHash("Ownership title", revisedTestBody(workID))
	if !strings.Contains(rec3.comments[0].Body, "Revision digest: `"+revisedDigest+"`") {
		t.Fatalf("revised comment body = %q, want digest %s", rec3.comments[0].Body, revisedDigest)
	}
	if rec3.comments[0].ID == rec.comments[0].ID {
		t.Fatalf("revised comment id %q equals the first revision's id; each revision needs its own identity", rec3.comments[0].ID)
	}
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != revisedDigest {
		t.Fatalf("link content hash = %s, want the revised digest %s", linkHash, revisedDigest)
	}
}

// lostResponseLinear is a fake Linear for the retry-convergence boundary.
// The first commentCreate stores the comment and then drops the connection
// before writing a response: remote persistence followed by a lost response.
// A later commentCreate with a client uuid the fake already stored answers
// the insert conflict the live API sends for a repeated id, and comment
// lookups resolve only identities the fake actually stored, with the stored
// body. A nonzero lookupStatus makes every comment lookup fail with that
// HTTP status; a nonempty lookupIDOverride makes every lookup answer that
// foreign id instead of the requested one.
type lostResponseLinear struct {
	humanBody        string
	responseLost     bool
	lookupStatus     int
	lookupIDOverride string
	comments         []capturedComment
	t                *testing.T
}

func (l *lostResponseLinear) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "issueUpdate") {
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"remote-ownership","identifier":"CON-7","url":"https://linear.app/example/issue/CON-7","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
			return
		}
		if strings.Contains(string(body), "commentCreate") {
			var request struct {
				Variables struct {
					Input struct {
						ID      string `json:"id"`
						IssueID string `json:"issueId"`
						Body    string `json:"body"`
					} `json:"input"`
				} `json:"variables"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				l.t.Errorf("decode comment request: %v", err)
				return
			}
			for _, stored := range l.comments {
				if stored.ID == request.Variables.Input.ID {
					_, _ = w.Write([]byte(`{"errors":[{"message":"conflict on insert of Comment","extensions":{"type":"invalid input","code":"INPUT_ERROR","statusCode":400,"userError":true,"userPresentableMessage":"Entity Comment with id ` + stored.ID + ` already exists."}}],"data":null}`))
					return
				}
			}
			l.comments = append(l.comments, capturedComment{ID: request.Variables.Input.ID, IssueID: request.Variables.Input.IssueID, Body: request.Variables.Input.Body})
			if l.responseLost {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					l.t.Error("response does not support hijacking")
					return
				}
				conn, _, err := hijacker.Hijack()
				if err != nil {
					l.t.Errorf("hijack: %v", err)
					return
				}
				_ = conn.Close()
				return
			}
			_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true,"comment":{"id":"` + request.Variables.Input.ID + `"}}}}`))
			return
		}
		if strings.Contains(string(body), "comment(id:") {
			if l.lookupStatus != 0 {
				w.WriteHeader(l.lookupStatus)
				_, _ = w.Write([]byte(`{"errors":[{"message":"lookup unavailable"}]}`))
				return
			}
			var request struct {
				Variables struct {
					ID string `json:"id"`
				} `json:"variables"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				l.t.Errorf("decode comment lookup: %v", err)
				return
			}
			for _, stored := range l.comments {
				if stored.ID == request.Variables.ID {
					id := stored.ID
					if l.lookupIDOverride != "" {
						id = l.lookupIDOverride
					}
					_, _ = w.Write([]byte(`{"data":{"comment":{"id":"` + id + `","issueId":"` + stored.IssueID + `","body":` + strconvQuote(stored.Body) + `}}}`))
					return
				}
			}
			_, _ = w.Write([]byte(`{"errors":[{"message":"Entity not found: Comment","extensions":{"type":"invalid input","code":"INPUT_ERROR","statusCode":400,"userError":true}}],"data":null}`))
			return
		}
		answerIssueRead(l.t, &l.humanBody)(w, r)
	}
}

// TestLinearIssueUpdateDrainConvergesAfterPersistedCommentLostResponse runs
// the retry the durable outbox exists for: the first drain's commentCreate
// persists the revision comment remotely and loses the response, so the
// operation stays retryable while the comment is already visible on the
// issue. The retry meets Linear's insert conflict on the same client uuid,
// resolves the comment by that uuid, and converges: one comment on the
// issue, the operation done, and the digest recorded.
func TestLinearIssueUpdateDrainConvergesAfterPersistedCommentLostResponse(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	workID := "ownership-lost-response-work"
	createdHash := linearContentHash("Ownership title", "content Concord wrote at creation")
	seedDescriptionOwnershipCase(t, dbPath, "ownership-product", "ownership-project", workID, "Ownership title", createdHash)

	remote := &lostResponseLinear{humanBody: "A human rewrote this body.", responseLost: true, t: t}
	operations := drainDescriptionOwnershipProduct(t, dbPath, remote.handler())
	if len(operations) != 1 || operations[0].Outcome != "retryable" {
		t.Fatalf("first drain = %+v, want one retryable operation", operations)
	}
	if !strings.Contains(operations[0].Detail, "publish managed revision comment") {
		t.Fatalf("failure detail = %q, want the unsynchronized revision named", operations[0].Detail)
	}
	if len(remote.comments) != 1 {
		t.Fatalf("remote comments after the lost response = %+v, want the persisted comment", remote.comments)
	}
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != createdHash {
		t.Fatalf("link content hash = %s, want the created digest standing while the response is lost", linkHash)
	}

	remote.responseLost = false
	operations = drainDescriptionOwnershipProduct(t, dbPath, remote.handler())
	if len(operations) != 1 || operations[0].Outcome != "done" {
		t.Fatalf("retry drain = %+v, want one done operation", operations)
	}
	if !strings.Contains(operations[0].Detail, "published managed revision comment") {
		t.Fatalf("retry detail = %q, want the publication named", operations[0].Detail)
	}
	if len(remote.comments) != 1 {
		t.Fatalf("remote comments after the retry = %+v, want no duplicate comment", remote.comments)
	}
	if remote.comments[0].ID != readOwnershipClientUUID(t, dbPath, workID) {
		t.Fatalf("stored comment id %q, want the operation's client uuid", remote.comments[0].ID)
	}
	digest := linearContentHash("Ownership title", ownedTestBody(workID))
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != digest {
		t.Fatalf("link content hash = %s, want the published revision digest %s", linkHash, digest)
	}
}

func TestLinearIssueUpdateDrainReportsUnresolvedRevisionOnRemoteFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	workID := "ownership-failure-work"
	createdHash := linearContentHash("Ownership title", "content Concord wrote at creation")
	seedDescriptionOwnershipCase(t, dbPath, "ownership-product", "ownership-project", workID, "Ownership title", createdHash)

	rec := &drainOwnershipRecorder{humanBody: "A human rewrote this body.", commentStatus: http.StatusTooManyRequests, t: t}
	operations := drainDescriptionOwnershipProduct(t, dbPath, rec.handler())
	if len(operations) != 1 || operations[0].Outcome != "retryable" {
		t.Fatalf("drain result = %+v, want one retryable operation", operations)
	}
	if !strings.Contains(operations[0].Detail, "publish managed revision comment") {
		t.Fatalf("failure detail = %q, want the unsynchronized revision named", operations[0].Detail)
	}
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != createdHash {
		t.Fatalf("link content hash = %s, want the created digest standing while the revision is unsynchronized", linkHash)
	}
	failedID := rec.comments[0].ID

	// The first attempt failed before Linear stored anything, so the retry's
	// commentCreate is the insert that persists the comment. The client uuid
	// stays the operation's identity across attempts: had the first attempt
	// persisted, the retry would meet the insert conflict and the drain would
	// converge instead of duplicating.
	rec2 := &drainOwnershipRecorder{humanBody: "A human rewrote this body.", t: t}
	operations = drainDescriptionOwnershipProduct(t, dbPath, rec2.handler())
	if len(operations) != 1 || operations[0].Outcome != "done" {
		t.Fatalf("retry result = %+v, want one done operation", operations)
	}
	if !strings.Contains(operations[0].Detail, "published managed revision comment") {
		t.Fatalf("retry detail = %q, want the publication named", operations[0].Detail)
	}
	digest := linearContentHash("Ownership title", ownedTestBody(workID))
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != digest {
		t.Fatalf("link content hash = %s, want the published revision digest %s", linkHash, digest)
	}
	if len(rec2.comments) != 1 || rec2.comments[0].ID != failedID {
		t.Fatalf("retry comment = %+v, want one comment converging on client uuid %q", rec2.comments, failedID)
	}
}

func TestLinearIssueUpdateDrainReportsNoRevisionWithoutDivergence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	workID := "ownership-same-work"
	seedDescriptionOwnershipCase(t, dbPath, "ownership-product", "ownership-project", workID, "Ownership title", linearContentHash("Ownership title", ownedTestBody(workID)))

	rec := &drainOwnershipRecorder{humanBody: ownedTestBody(workID), t: t}
	runOwnershipDrain(t, dbPath, "lin_api_same_test", rec.handler())

	if len(rec.updates) != 1 {
		t.Fatalf("update count = %d, want one managed routing update", len(rec.updates))
	}
	assertContentFreeUpdate(t, rec.updates[0], "matching drain")
	if len(rec.comments) != 0 {
		t.Fatalf("comments = %+v, want no revision comment without divergence", rec.comments)
	}
	digest := linearContentHash("Ownership title", ownedTestBody(workID))
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != digest {
		t.Fatalf("link content hash = %s, want the matching digest standing", linkHash)
	}
}

// TestLinearIssueUpdateDrainKeepsRetryableWhenCommentLookupFails covers a
// retry that meets the insert conflict and then cannot read the stored
// comment. The conflict proves the comment exists, so the lookup failure
// decides the outcome: a transient lookup failure keeps the operation
// retryable instead of reporting the conflict as a permanent failure.
func TestLinearIssueUpdateDrainKeepsRetryableWhenCommentLookupFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	workID := "ownership-lookup-failure-work"
	createdHash := linearContentHash("Ownership title", "content Concord wrote at creation")
	seedDescriptionOwnershipCase(t, dbPath, "ownership-product", "ownership-project", workID, "Ownership title", createdHash)

	remote := &lostResponseLinear{humanBody: "A human rewrote this body.", responseLost: true, t: t}
	operations := drainDescriptionOwnershipProduct(t, dbPath, remote.handler())
	if len(operations) != 1 || operations[0].Outcome != "retryable" {
		t.Fatalf("first drain = %+v, want one retryable operation", operations)
	}

	remote.responseLost = false
	remote.lookupStatus = http.StatusTooManyRequests
	operations = drainDescriptionOwnershipProduct(t, dbPath, remote.handler())
	if len(operations) != 1 || operations[0].Outcome != "retryable" {
		t.Fatalf("lookup-failure drain = %+v, want one retryable operation", operations)
	}
	if strings.Contains(operations[0].Detail, "already exists") {
		t.Fatalf("lookup-failure detail = %q, want the lookup failure, not the insert conflict", operations[0].Detail)
	}
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != createdHash {
		t.Fatalf("link content hash = %s, want the created digest standing while the lookup fails", linkHash)
	}

	remote.lookupStatus = 0
	operations = drainDescriptionOwnershipProduct(t, dbPath, remote.handler())
	if len(operations) != 1 || operations[0].Outcome != "done" {
		t.Fatalf("recovered drain = %+v, want one done operation", operations)
	}
	if len(remote.comments) != 1 {
		t.Fatalf("remote comments = %+v, want one comment", remote.comments)
	}
}

// TestLinearIssueUpdateDrainRefusesAnAlteredStoredCommentBody covers the
// retry whose insert conflict resolves to a stored comment a human altered:
// the digest marker still matches, but the body differs from the published
// revision. The drain must refuse convergence and leave the recorded digest.
func TestLinearIssueUpdateDrainRefusesAnAlteredStoredCommentBody(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	workID := "ownership-altered-body-work"
	createdHash := linearContentHash("Ownership title", "content Concord wrote at creation")
	seedDescriptionOwnershipCase(t, dbPath, "ownership-product", "ownership-project", workID, "Ownership title", createdHash)

	remote := &lostResponseLinear{humanBody: "A human rewrote this body.", responseLost: true, t: t}
	operations := drainDescriptionOwnershipProduct(t, dbPath, remote.handler())
	if len(operations) != 1 || operations[0].Outcome != "retryable" {
		t.Fatalf("first drain = %+v, want one retryable operation", operations)
	}

	// Between the attempts a human changes the stored content but leaves
	// the revision digest marker in place.
	remote.comments[0].Body = strings.Replace(remote.comments[0].Body, "**Managed title:** Ownership title", "**Managed title:** Human edit", 1)
	remote.responseLost = false
	operations = drainDescriptionOwnershipProduct(t, dbPath, remote.handler())
	if len(operations) != 1 || operations[0].Outcome != "permanent" {
		t.Fatalf("altered-body drain = %+v, want one permanently failed operation", operations)
	}
	if !strings.Contains(operations[0].Detail, "stored body differs") {
		t.Fatalf("failure detail = %q, want the altered stored body named", operations[0].Detail)
	}
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != createdHash {
		t.Fatalf("link content hash = %s, want the created digest standing while the altered comment stands", linkHash)
	}
}

// TestLinearIssueUpdateDrainRefusesAMismatchedCommentLookupID covers a
// lookup that answers a different comment identity than the requested uuid:
// the drain must refuse the convergence, fail the operation permanently, and
// record no digest.
func TestLinearIssueUpdateDrainRefusesAMismatchedCommentLookupID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	workID := "ownership-wrong-id-work"
	createdHash := linearContentHash("Ownership title", "content Concord wrote at creation")
	seedDescriptionOwnershipCase(t, dbPath, "ownership-product", "ownership-project", workID, "Ownership title", createdHash)

	remote := &lostResponseLinear{humanBody: "A human rewrote this body.", responseLost: true, t: t}
	operations := drainDescriptionOwnershipProduct(t, dbPath, remote.handler())
	if len(operations) != 1 || operations[0].Outcome != "retryable" {
		t.Fatalf("first drain = %+v, want one retryable operation", operations)
	}

	remote.responseLost = false
	remote.lookupIDOverride = "comment-elsewhere"
	operations = drainDescriptionOwnershipProduct(t, dbPath, remote.handler())
	if len(operations) != 1 || operations[0].Outcome != "permanent" {
		t.Fatalf("mismatched-id drain = %+v, want one permanently failed operation", operations)
	}
	if !strings.Contains(operations[0].Detail, "returned comment id") {
		t.Fatalf("failure detail = %q, want the mismatched lookup id named", operations[0].Detail)
	}
	if linkHash := readOwnershipLinkHash(t, dbPath, workID); linkHash != createdHash {
		t.Fatalf("link content hash = %s, want the created digest standing while the lookup diverges", linkHash)
	}
}
