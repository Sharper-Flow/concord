package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("run() exit code = %d, want 0", code)
	}
	if got := out.String(); got != "dev\n" {
		t.Fatalf("version output = %q, want %q", got, "dev\n")
	}
	if errOut.Len() != 0 {
		t.Fatalf("version error output = %q, want empty", errOut.String())
	}
}

func TestDatabaseOverrideRefusesRepositoryLocalPath(t *testing.T) {
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet", repo)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, filepath.Join(repo, "nested", "concord.db"))
	if _, err := databasePath(); err == nil {
		t.Fatal("repository-local database override accepted")
	}
	if _, err := os.Stat(filepath.Join(repo, "nested")); !os.IsNotExist(err) {
		t.Fatal("database override created a repository-local directory")
	}
}

func TestInvokeNeverEchoesGrantToken(t *testing.T) {
	grantRef := strings.Repeat("a", 63) + "b"
	raw := []byte(`{"call_envelope":{"schema_version":"1.0","request_id":"r","grant_ref":"` + grantRef + `","client_ref":"c","scope_version":""},"tool":"concord_product_view","operation":"resolve","input":{}}`)
	var out, errOut bytes.Buffer
	if code := runInvoke(raw, nil, nil, &out, &errOut); code != 0 {
		t.Fatalf("runInvoke exit=%d stderr=%q", code, errOut.String())
	}
	if strings.Contains(out.String(), grantRef) || strings.Contains(errOut.String(), grantRef) {
		t.Fatal("grant token leaked through invoke output")
	}
}

func TestRunWithoutArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 0 {
		t.Fatalf("run() exit code = %d, want 0", code)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("run() output = %q / %q, want empty", out.String(), errOut.String())
	}
}

func TestRunRejectsUnsupportedArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"status"}, &out, &errOut); code != 2 {
		t.Fatalf("run() exit code = %d, want 2", code)
	}
	if got := errOut.String(); got != "concord: unsupported arguments: status\n" {
		t.Fatalf("error output = %q", got)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
}
