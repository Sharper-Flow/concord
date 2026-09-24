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

// seedAuditCommentDrainCase seeds a linear_enabled Product whose work item
// holds a confirmed link with a recorded creation digest, then queues one
// issue_audit_comment operation through the store route. It returns the
// operation's client UUID, the identity the comment will carry.
func seedAuditCommentDrainCase(t *testing.T, dbPath, workID, auditBody string) string {
	t.Helper()
	seedCLIProduct(t, dbPath, "audit-drain-product", "audit-drain-project")
	enableLinearProduct(t, dbPath, "audit-drain-product")
	seedLinearCLIWork(t, dbPath, workID, "audit-drain-project", "Audit drain title")
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, step := range []struct {
		state string
		hash  string
	}{{store.LinearLinkUnpublished, ""}, {store.LinearLinkPending, ""}, {store.LinearLinkConfirmed, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}} {
		if err := s.RecordLinearLink(ctx, workID, "remote-audit-drain", "CON-400", "https://linear.app/example/issue/CON-400", "2026-09-20T08:00:00Z", step.hash, step.state); err != nil {
			t.Fatal(err)
		}
	}
	op, err := s.EnqueueLinearIssueAuditComment(ctx, "audit-drain-product", workID, auditBody)
	if err != nil {
		t.Fatal(err)
	}
	return op.IdempotencyKey
}

// drainAuditCommentCase runs the operator drain against the fake Linear and
// returns the reported operation outcomes.
func drainAuditCommentCase(t *testing.T, dbPath string, handler http.HandlerFunc) []struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
} {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_audit_test")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"audit-drain-product"}`), &out, &errOut); code != 0 {
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

// readAuditCommentDrainLink reads the link row the drain completed against.
func readAuditCommentDrainLink(t *testing.T, dbPath, workID string) (linkState, remoteUpdatedAt, contentHash string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT link_state, remote_updated_at, content_hash FROM linear_issue_links WHERE work_id=?`, workID).Scan(&linkState, &remoteUpdatedAt, &contentHash); err != nil {
		t.Fatal(err)
	}
	return linkState, remoteUpdatedAt, contentHash
}

// auditCommentStored is one comment the fake Linear reports as already
// stored; nil means the lookup answers with the unknown-comment refusal.
type auditCommentStored struct {
	ID      string `json:"id"`
	IssueID string `json:"issueId"`
	Body    string `json:"body"`
}

// auditCommentRecorder is a fake Linear that records commentCreate requests,
// fails any issueUpdate as a test failure, answers the comment lookup from
// its stored comment, and serves the link-refresh issue read.
type auditCommentRecorder struct {
	t *testing.T
	// commentConflict makes every commentCreate answer the duplicate-entity
	// refusal a lost response produces; convergence then performs the lookup.
	commentConflict bool
	stored          *auditCommentStored
	comments        []capturedComment
}

func (rec *auditCommentRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestBody, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(requestBody), "issueUpdate") {
			rec.t.Errorf("audit comment drain sent an issueUpdate: %s", requestBody)
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"remote-audit-drain","identifier":"CON-400","url":"https://linear.app/example/issue/CON-400","updatedAt":"2026-09-21T08:00:00Z"}}}}`))
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
			if rec.commentConflict {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":[{"message":"conflict on insert of Comment","extensions":{"type":"invalid input","code":"INPUT_ERROR","statusCode":400,"userError":true,"userPresentableMessage":"Entity Comment with id ` + request.Variables.Input.ID + ` already exists."}}],"data":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true,"comment":{"id":"` + request.Variables.Input.ID + `"}}}}`))
			return
		}
		if strings.Contains(string(requestBody), "comment(id:") {
			if rec.stored == nil {
				_, _ = w.Write([]byte(`{"errors":[{"message":"Entity not found: Comment","extensions":{"type":"invalid input","code":"INPUT_ERROR","statusCode":400,"userError":true}}],"data":null}`))
				return
			}
			encoded, _ := json.Marshal(rec.stored)
			_, _ = w.Write([]byte(`{"data":{"comment":` + string(encoded) + `}}`))
			return
		}
		answerIssueRead(rec.t, &humanAuditIssueBody)(w, r)
	}
}

// humanAuditIssueBody is the human-owned body the fake Linear serves; the
// drain must never read or write it.
var humanAuditIssueBody = "Human-written body that stays untouched"

func TestDrainAuditCommentPublishesTypedCommentAndKeepsIssueText(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	const auditBody = "- CON-400 names dependency CON-397 as blocking; the tracking issue shows no such dependency."
	clientUUID := seedAuditCommentDrainCase(t, dbPath, "audit-drain-work", auditBody)
	rec := &auditCommentRecorder{t: t}
	results := drainAuditCommentCase(t, dbPath, rec.handler())
	if len(results) != 1 || results[0].Outcome != "done" {
		t.Fatalf("results = %+v, want one done operation", results)
	}
	if len(rec.comments) != 1 {
		t.Fatalf("commentCreate calls = %d, want exactly one", len(rec.comments))
	}
	comment := rec.comments[0]
	if comment.ID != clientUUID {
		t.Fatalf("comment id %s does not match the operation's client UUID %s", comment.ID, clientUUID)
	}
	if comment.IssueID != "remote-audit-drain" {
		t.Fatalf("comment sits on issue %q, want the linked remote issue", comment.IssueID)
	}
	if !strings.HasPrefix(comment.Body, "## Independent audit\n\n") || !strings.Contains(comment.Body, auditBody) {
		t.Fatalf("comment body %q lacks the typed header or the verbatim audit", comment.Body)
	}
	linkState, remoteUpdatedAt, contentHash := readAuditCommentDrainLink(t, dbPath, "audit-drain-work")
	if linkState != store.LinearLinkConfirmed {
		t.Fatalf("link state = %s, want confirmed", linkState)
	}
	if remoteUpdatedAt != "2026-09-20T08:00:00Z" {
		t.Fatalf("remote_updated_at = %q, want the recorded freshness kept by the comment-only drain", remoteUpdatedAt)
	}
	if contentHash != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("content_hash = %q, want the recorded creation digest standing", contentHash)
	}
}

func TestDrainAuditCommentConvergesOnStoredComment(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	const auditBody = "- CON-400 completion cites a check that never ran; cite the rerun instead."
	clientUUID := seedAuditCommentDrainCase(t, dbPath, "audit-converge-work", auditBody)
	// The stored comment the lookup returns is the one a previous attempt of
	// this operation minted: same identity, same issue, same rendered body.
	stored := &auditCommentStored{ID: clientUUID, IssueID: "remote-audit-drain", Body: linearAuditCommentBody(auditBody)}
	rec := &auditCommentRecorder{t: t, commentConflict: true, stored: stored}
	results := drainAuditCommentCase(t, dbPath, rec.handler())
	if len(results) != 1 {
		t.Fatalf("results = %+v, want exactly one drained operation", results)
	}
	if results[0].Outcome != "done" {
		t.Fatalf("outcome = %+v, want the duplicate-entity conflict to converge as done", results[0])
	}
	if len(rec.comments) != 1 {
		t.Fatalf("commentCreate calls = %d, want one refused insert", len(rec.comments))
	}
}

func TestDrainAuditCommentRefusesADivergentStoredBody(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	const auditBody = "- CON-400 completion cites a check that never ran; cite the rerun instead."
	clientUUID := seedAuditCommentDrainCase(t, dbPath, "audit-divergent-work", auditBody)
	stored := &auditCommentStored{ID: clientUUID, IssueID: "remote-audit-drain", Body: "a body this operation never rendered"}
	rec := &auditCommentRecorder{t: t, commentConflict: true, stored: stored}
	results := drainAuditCommentCase(t, dbPath, rec.handler())
	if len(results) != 1 || results[0].Outcome != "permanent" || !strings.Contains(results[0].Detail, "stored body differs") {
		t.Fatalf("results = %+v, want a permanent refusal naming the divergent stored body", results)
	}
}
