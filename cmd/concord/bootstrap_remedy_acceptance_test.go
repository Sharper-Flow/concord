package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func bootstrapRemedyCounts(t *testing.T, s *store.Store) map[string]int {
	t.Helper()
	counts := durableCounts(t, s)
	for _, table := range []string{"worktree_claims", "worktree_entries", "bootstrap_operations"} {
		var count int
		if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func TestBootstrapRemedyNamesCaptureCheckout(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "authority.db"))
	seedLocatorAuthority(t, s, repo)
	origin := filepath.Join(t.TempDir(), "preserved-origin")
	gitOutput(t, repo, "worktree", "add", "--detach", origin, "HEAD")
	raw, err := json.Marshal(bootstrapRequest())
	if err != nil {
		t.Fatal(err)
	}
	before := bootstrapRemedyCounts(t, s)
	t.Chdir(origin)
	var out, diagnostic bytes.Buffer
	code := runWorkBootstrap(raw, s, &out, &diagnostic)
	if code != workBootstrapRefusalExit || out.Len() != 0 {
		t.Fatalf("unclaimed origin unexpectedly admitted: code=%d out=%s diagnostic=%s", code, &out, &diagnostic)
	}
	if after := bootstrapRemedyCounts(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused capture changed authority: before=%v after=%v", before, after)
	}
	if !strings.Contains(diagnostic.String(), "capture") || !strings.Contains(diagnostic.String(), "default checkout") || !strings.Contains(diagnostic.String(), repo) {
		t.Errorf("no-active-item refusal does not name its actual capture checkout: %s", &diagnostic)
	}
	if !strings.Contains(gitOutput(t, repo, "worktree", "list", "--porcelain"), origin) {
		t.Fatal("refused capture removed the preserved origin")
	}
	t.Chdir(repo)
	out.Reset()
	diagnostic.Reset()
	if code := runWorkBootstrap(raw, s, &out, &diagnostic); code != 0 {
		t.Fatalf("capture from the actual default checkout is not callable: code=%d diagnostic=%s", code, &diagnostic)
	}
}

func TestBootstrapRemedyNamesActiveResumePath(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "authority.db"))
	seedLocatorAuthority(t, s, repo)
	active, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(t.TempDir(), "preserved-origin")
	gitOutput(t, repo, "worktree", "add", "--detach", origin, "HEAD")
	before := bootstrapRemedyCounts(t, s)
	code, _, diagnostic := resumeCLI(t, s, origin, active.WorkID)
	if code != workResumeRefusalExit {
		t.Fatalf("unclaimed origin unexpectedly admitted: code=%d diagnostic=%s", code, diagnostic)
	}
	if after := bootstrapRemedyCounts(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused resume changed authority: before=%v after=%v", before, after)
	}
	if !strings.Contains(diagnostic, active.Entry.Path) || !strings.Contains(diagnostic, active.WorkID) || !strings.Contains(diagnostic, "work_start") {
		t.Errorf("active-item refusal does not name its actual resume identity and path: %s", diagnostic)
	}
	if !strings.Contains(gitOutput(t, repo, "worktree", "list", "--porcelain"), origin) {
		t.Fatal("refused resume removed the preserved origin")
	}
	code, resumed, diagnostic := resumeCLI(t, s, active.Entry.Path, active.WorkID)
	if code != 0 || resumed.Worktree.Path != active.Entry.Path {
		t.Fatalf("resume from the actual active worktree is not callable: code=%d output=%+v diagnostic=%s", code, resumed, diagnostic)
	}
}
