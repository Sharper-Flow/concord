package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestIsProjectLaunchSyntax(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"known json command", []string{"client-register"}, false},
		{"explicit launcher", []string{"launcher"}, false},
		{"absolute path", []string{"/workspace/concord", "--", "fix"}, true},
		{"relative path", []string{"./toolbox"}, true},
		{"current directory", []string{"."}, true},
		{"plain word", []string{"toolbox"}, false},
		{"path without prompt", []string{"/workspace/concord"}, true},
		{"invalid delimiter", []string{"/workspace/concord", "fix"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isProjectLaunchSyntax(tc.args); got != tc.want {
				t.Fatalf("isProjectLaunchSyntax(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestRunProjectLaunchCommandRejectsNonTTY(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runProjectLaunchCommand([]string{"/tmp", "--", "x"}, strings.NewReader(""), &out, &errOut, false)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "project launch requires an interactive TTY") {
		t.Fatalf("stderr = %q, want TTY diagnostic", errOut.String())
	}
}

func TestRunProjectLaunchCommandRejectsMissingDirectory(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runProjectLaunchCommand([]string{"/nonexistent/project/path", "--", "x"}, strings.NewReader(""), &out, &errOut, true)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "not a project directory") {
		t.Fatalf("stderr = %q, want missing-directory diagnostic", errOut.String())
	}
}

func TestRunProjectLaunchCommandResolvesRegisteredProject(t *testing.T) {
	repo := makeTempGitRepo(t)
	dbPath := freshMigratedCLIDatabase(t)
	t.Setenv(dbOverrideEnv, dbPath)

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.CreateProductWithProject(context.Background(), store.ProductCreation{
		ProductID:               "prod-launch",
		DisplayName:             "Launch Product",
		StageMaturity:           "prototype",
		StageAudienceCommitment: "operator_only",
		ProjectID:               "proj-launch",
		ProjectDisplayName:      "Launch Project",
		Role:                    "primary",
	}); err != nil {
		t.Fatalf("create product/project: %v", err)
	}
	locator := store.ProjectLocator{Kind: store.LocatorCanonicalPath, Value: repo}
	if err := s.AddProjectLocator(context.Background(), "proj-launch", locator, 1); err != nil {
		t.Fatalf("add locator: %v", err)
	}

	var capturedProduct, capturedPrompt string
	original := projectLaunchSessionStarter
	projectLaunchSessionStarter = func(productID, workID, leadPrompt string, in io.Reader, out, errOut io.Writer, terminal bool) int {
		capturedProduct = productID
		capturedPrompt = leadPrompt
		return 0
	}
	defer func() { projectLaunchSessionStarter = original }()

	var out, errOut bytes.Buffer
	code := runProjectLaunchCommand([]string{repo, "--", "fix the thing"}, strings.NewReader(""), &out, &errOut, true)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, errOut.String())
	}
	if capturedProduct != "prod-launch" {
		t.Fatalf("productID = %q, want prod-launch", capturedProduct)
	}
	if capturedPrompt != "fix the thing" {
		t.Fatalf("lead prompt = %q, want \"fix the thing\"", capturedPrompt)
	}
}

func TestRunProjectLaunchCommandRegistersUnregisteredProject(t *testing.T) {
	repo := makeTempGitRepo(t)
	dbPath := freshMigratedCLIDatabase(t)
	t.Setenv(dbOverrideEnv, dbPath)

	var capturedProduct, capturedPrompt string
	original := projectLaunchSessionStarter
	projectLaunchSessionStarter = func(productID, workID, leadPrompt string, in io.Reader, out, errOut io.Writer, terminal bool) int {
		capturedProduct = productID
		capturedPrompt = leadPrompt
		return 0
	}
	defer func() { projectLaunchSessionStarter = original }()

	// Accept all defaults during interactive registration.
	input := strings.NewReader("\n\n\n\n\n\n")
	var out bytes.Buffer
	var errOut bytes.Buffer
	code := runProjectLaunchCommand([]string{repo, "--", "explore"}, input, &out, &errOut, true)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, errOut.String())
	}
	base := filepath.Base(repo)
	if capturedProduct != base {
		t.Fatalf("productID = %q, want %q", capturedProduct, base)
	}
	if capturedPrompt != "explore" {
		t.Fatalf("lead prompt = %q, want explore", capturedPrompt)
	}
	if !strings.Contains(out.String(), "Registered Project") {
		t.Fatalf("registration confirmation missing; output=%q", out.String())
	}
}

func makeTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "sample-project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmd := exec.Command("git", "init", repo)
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return repo
}
