package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func agentBindVerificationReport(t *testing.T, f *spineFixture, commit, result, runRef string) string {
	t.Helper()
	ref := "commit:" + commit
	spineOK(t, f.action(t, "bind_evidence", map[string]any{
		"evidence_kind": "verification", "immutable_subject_ref": ref,
		"verification_report": map[string]any{"source": "local", "commit": commit, "result": result, "run_ref": runRef},
	}))
	return ref
}

// fixtureRepoWithOrigin creates one real git repository whose origin HEAD is
// set, the shape the tooling resolution reads the default ref from.
func fixtureRepoWithOrigin(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "Fixture")
	run("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "fixture")
	run("remote", "add", "origin", "https://example.invalid/fixture.git")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo
}
