package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func openResolveStore(t *testing.T) *store.Store {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The reason project-resolve exists: a host cannot join on a directory name.
// CD-0008 D1 makes a path replaceable evidence for a stable Project ID, so the
// mapping is authority data and only the core can read it.
func TestProjectResolveAnswersDirectoryToProjectAndProductHop(t *testing.T) {
	repo := initLocatorRepo(t)
	s := openResolveStore(t)
	seedLocatorAuthority(t, s, repo)

	var out, errOut bytes.Buffer
	if code := runProjectResolve([]byte(`{"directory":"`+repo+`"}`), s, &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	var got struct {
		ProjectID    string   `json:"project_id"`
		ProductIDs   []string `json:"product_ids"`
		ScopeVersion string   `json:"scope_version"`
		MainWorktree bool     `json:"main_worktree"`
		Repository   struct {
			CanonicalPath string `json:"canonical_path"`
			GitRemote     string `json:"git_remote"`
		} `json:"repository"`
		Locators []struct {
			Kind            string `json:"kind"`
			NormalizedValue string `json:"normalized_value"`
		} `json:"locators"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if got.ProjectID != "project-wl" {
		t.Errorf("project_id=%q want project-wl", got.ProjectID)
	}
	if !reflect.DeepEqual(got.ProductIDs, []string{"product-wl"}) {
		t.Errorf("product_ids=%v want [product-wl]", got.ProductIDs)
	}
	if got.ScopeVersion == "" {
		t.Error("scope_version is absent; a host cache has no membership watermark")
	}
	if got.Repository.CanonicalPath == "" {
		t.Error("repository.canonical_path is absent")
	}
	if len(got.Locators) == 0 {
		t.Fatal("locators are absent; the identity evidence is unreadable")
	}
	if got.Locators[0].Kind != string(store.LocatorCanonicalPath) {
		t.Errorf("locator kind=%q want %q", got.Locators[0].Kind, store.LocatorCanonicalPath)
	}
}

// An unregistered repository is a typed refusal, never a guess. A verb that
// invented a Project ID would manufacture the identity CD-0008 D1 denies.
func TestProjectResolveRefusesUnregisteredRepository(t *testing.T) {
	repo := initLocatorRepo(t)
	s := openResolveStore(t)

	var out, errOut bytes.Buffer
	if code := runProjectResolve([]byte(`{"directory":"`+repo+`"}`), s, &out, &errOut); code == 0 {
		t.Fatalf("unregistered repository resolved: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "no known Project locator") {
		t.Errorf("diagnostic=%q want the typed unknown-scope refusal", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("refusal wrote a payload: %q", out.String())
	}
}

// The verb reads. It must never become a second write authority (CD-0021).
func TestProjectResolveWritesNothing(t *testing.T) {
	repo := initLocatorRepo(t)
	s := openResolveStore(t)
	seedLocatorAuthority(t, s, repo)
	before := launcherDurableCounts(t, s)

	var out, errOut bytes.Buffer
	if code := runProjectResolve([]byte(`{"directory":"`+repo+`"}`), s, &out, &errOut); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	if after := launcherDurableCounts(t, s); !reflect.DeepEqual(before, after) {
		t.Errorf("project-resolve changed durable state: before=%v after=%v", before, after)
	}
}

func TestProjectResolveRequiresDirectory(t *testing.T) {
	s := openResolveStore(t)
	for _, raw := range []string{`{}`, `{"directory":""}`} {
		var out, errOut bytes.Buffer
		if code := runProjectResolve([]byte(raw), s, &out, &errOut); code == 0 {
			t.Errorf("%s resolved without a directory: %q", raw, out.String())
		}
	}
}
