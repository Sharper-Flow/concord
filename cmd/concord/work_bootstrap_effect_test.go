package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkBootstrapFailureEnvelopeRetainsCapture(t *testing.T) {
	for _, phase := range []string{"after_prepare", "after_native_create"} {
		t.Run(phase, func(t *testing.T) {
			repo := initLocatorRepo(t)
			s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			seedLocatorAuthority(t, s, repo)
			req := bootstrapRequest()
			req.Ref = "origin/main"
			stop := errors.New("injected phase interruption")
			_, err = s.BootstrapWorktree(t.Context(), req, func(current string) error {
				if current == phase {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("prepare interruption=%v", err)
			}
			var operationID, workID, path string
			if err := s.DatabaseForTesting().QueryRow(`SELECT b.operation_id,b.work_id,c.pinned_path FROM bootstrap_operations b JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.idempotency_key=?`, req.IdempotencyKey).Scan(&operationID, &workID, &path); err != nil {
				t.Fatal(err)
			}
			gitOutput(t, repo, "switch", "-q", "-c", "not-default")
			t.Chdir(repo)
			// The stored ref is resolved, but the replay's original intent is omitted.
			req.Ref = ""
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			var out, diagnostic bytes.Buffer
			code := runWorkBootstrap(raw, s, &out, &diagnostic)
			if code != workBootstrapRefusalExit || !strings.Contains(diagnostic.String(), "not on its default branch") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), diagnostic.String())
			}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("missing structured failure: %v stdout=%q", err, out.String())
			}
			want := map[string]any{"schema_version": "1.0", "outcome": "error", "effect_state": "partial", "operation_id": operationID, "product_id": req.ProductID, "project_id": req.ProjectID, "work_id": workID, "worktree_path": path}
			if len(got) != len(want) {
				t.Fatalf("failure envelope fields=%v want=%v", got, want)
			}
			for key, value := range want {
				if got[key] != value {
					t.Fatalf("%s=%v want=%v; envelope=%v", key, got[key], value, got)
				}
			}
			req.Task = "different input"
			raw, _ = json.Marshal(req)
			out.Reset()
			diagnostic.Reset()
			if code := runWorkBootstrap(raw, s, &out, &diagnostic); code != workBootstrapRefusalExit || !strings.Contains(diagnostic.String(), "bound to different input") {
				t.Fatalf("conflict exit=%d stdout=%q stderr=%q", code, out.String(), diagnostic.String())
			}
			got = nil
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 3 || got["effect_state"] != "none" || got["outcome"] != "error" || got["schema_version"] != "1.0" {
				t.Fatalf("conflicting input leaked identity: %v err=%v", got, err)
			}
			gitOutput(t, repo, "switch", "-q", "main")
			req.Task = bootstrapRequest().Task
			raw, _ = json.Marshal(req)
			out.Reset()
			diagnostic.Reset()
			if code := runWorkBootstrap(raw, s, &out, &diagnostic); code != 0 {
				t.Fatalf("exact replay exit=%d stdout=%q stderr=%q", code, out.String(), diagnostic.String())
			}
			got = nil
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 8 || got["work_id"] != workID || got["operation_id"] != operationID || got["replayed"] != true || got["outcome"] != nil || got["effect_state"] != nil {
				t.Fatalf("success shape or original replay identity changed: %v err=%v", got, err)
			}
		})
	}
}

func TestWorkBootstrapFailureEnvelopeNoEffectAndPossible(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "unreadable"}[unavailable], func(t *testing.T) {
			repo := initLocatorRepo(t)
			s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "concord.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			seedLocatorAuthority(t, s, repo)
			wantEffect, wantExit := "none", 1
			if unavailable {
				// Project resolution uses its own tables and remains admitted.
				if _, err := s.DatabaseForTesting().Exec(`ALTER TABLE bootstrap_operations RENAME TO unavailable_bootstrap_operations`); err != nil {
					t.Fatal(err)
				}
				wantEffect = "possible"
			} else if _, err := s.DatabaseForTesting().Exec(`CREATE TRIGGER refuse_bootstrap_journal BEFORE INSERT ON bootstrap_operations BEGIN SELECT RAISE(ABORT, 'injected journal refusal'); END`); err != nil {
				t.Fatal(err)
			}
			t.Chdir(repo)
			raw, _ := json.Marshal(bootstrapRequest())
			var out, diagnostic bytes.Buffer
			if code := runWorkBootstrap(raw, s, &out, &diagnostic); code != wantExit || diagnostic.Len() == 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), diagnostic.String())
			}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 3 || got["schema_version"] != "1.0" || got["outcome"] != "error" || got["effect_state"] != wantEffect {
				t.Fatalf("failure envelope=%v err=%v want=%s", got, err, wantEffect)
			}
		})
	}
}

func TestWorkBootstrapPreCallRefusalsKeepEmptyStdout(t *testing.T) {
	repo := initLocatorRepo(t)
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLocatorAuthority(t, s, repo)
	t.Chdir(repo)
	for _, raw := range [][]byte{[]byte(`[]`), []byte(`{"project_id":"different-project"}`)} {
		var out, diagnostic bytes.Buffer
		if code := runWorkBootstrap(raw, s, &out, &diagnostic); code != workBootstrapRefusalExit || out.Len() != 0 || diagnostic.Len() == 0 {
			t.Fatalf("pre-call refusal exit=%d stdout=%q stderr=%q", code, out.String(), diagnostic.String())
		}
	}
}
