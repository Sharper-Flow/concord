package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

func TestLinearCompletionFailureClass(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "typed retry-safe store failure",
			err:  &store.Failure{Kind: store.KindUnavailable, Op: "linear_outbox_complete", Detail: "database is locked", RetrySafe: true},
			want: "retryable",
		},
		{
			name: "typed non-retry-safe store failure",
			err:  &store.Failure{Kind: store.KindInvalidTransition, Op: "linear_outbox_complete", Detail: "a create operation cannot complete an already confirmed link", RetrySafe: false},
			want: "permanent",
		},
		{
			name: "wrapped store failure keeps its flag",
			err:  fmt.Errorf("drain: %w", &store.Failure{Kind: store.KindUnavailable, Op: "linear_outbox_complete", Detail: "database is locked", RetrySafe: true}),
			want: "retryable",
		},
		{
			name: "untyped error",
			err:  errors.New("sqlite: disk I/O error"),
			want: "permanent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := linearCompletionFailureClass(tc.err); got != tc.want {
				t.Fatalf("linearCompletionFailureClass(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestLinearDrainReportsPermanentCompletionConflict(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "perm-product", "perm-product-project")
	enableLinearProduct(t, dbPath, "perm-product")
	seedLinearCLIWork(t, dbPath, "perm-work", "perm-product-project", "Permanent conflict")

	// Enqueue first: capture creates the placeholder link row the operation
	// owns. Confirming that link out from under the queued create is the
	// completion conflict: the provider call succeeds, but the store returns
	// an invalid_transition failure whose RetrySafe is false.
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "perm-product", "work_id": "perm-work", "op_kind": "issue_create"})

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	placeholder, err := s.ReadLinearLink(context.Background(), "perm-work")
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	for _, state := range []string{store.LinearLinkPending, store.LinearLinkConfirmed} {
		if err := s.RecordLinearLink(context.Background(), "perm-work", placeholder.RemoteIssueUUID, "OLD-9", "https://linear.app/example/issue/OLD-9", "", "", state); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	s.Close()

	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "issueCreate") {
			createCalls++
			_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"bbbbbbbb-0000-0000-0000-000000000002","identifier":"NEW-9","url":"https://linear.app/example/issue/NEW-9","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"` + placeholder.RemoteIssueUUID + `","identifier":"OLD-9","url":"https://linear.app/example/issue/OLD-9","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"perm-team"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_perm_test")
	t.Setenv(dbOverrideEnv, dbPath)

	drain := func(t *testing.T) struct {
		OK         bool `json:"ok"`
		Drained    int  `json:"drained"`
		Operations []struct {
			OperationID string `json:"operation_id"`
			Outcome     string `json:"outcome"`
			Detail      string `json:"detail"`
		} `json:"operations"`
	} {
		t.Helper()
		var out, errOut strings.Builder
		if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"perm-product"}`), &out, &errOut); code != 0 {
			t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
		}
		var result struct {
			OK         bool `json:"ok"`
			Drained    int  `json:"drained"`
			Operations []struct {
				OperationID string `json:"operation_id"`
				Outcome     string `json:"outcome"`
				Detail      string `json:"detail"`
			} `json:"operations"`
		}
		if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
			t.Fatalf("drain output %q: %v", out.String(), err)
		}
		return result
	}

	first := drain(t)
	if !first.OK || len(first.Operations) != 1 {
		t.Fatalf("first drain = %+v", first)
	}
	op := first.Operations[0]
	if op.Outcome != "permanent" || !strings.Contains(op.Detail, "already confirmed link") {
		t.Fatalf("first drain operation = %+v, want permanent completion conflict", op)
	}
	if createCalls != 1 {
		t.Fatalf("provider create calls = %d, want 1", createCalls)
	}

	s, err = store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var outboxState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&outboxState); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()
	if outboxState != store.LinearOutboxFailed {
		t.Fatalf("outbox state = %s, want %s", outboxState, store.LinearOutboxFailed)
	}

	// The failed operation is never re-claimed, so the provider sees no second
	// create call.
	second := drain(t)
	if !second.OK || second.Drained != 0 || len(second.Operations) != 0 {
		t.Fatalf("second drain = %+v, want no claimed operations", second)
	}
	if createCalls != 1 {
		t.Fatalf("provider create calls after second drain = %d, want 1", createCalls)
	}
}
