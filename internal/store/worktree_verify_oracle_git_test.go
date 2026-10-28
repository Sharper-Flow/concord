package store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNativeOracleSnapshotSuppliesCandidateGitMetadata(t *testing.T) {
	repo := t.TempDir()
	gitRunStore(t, repo, "init", "-b", "source")
	writeRepoFilesForTest(t, repo, map[string]string{"version.txt": "0.1.0\n", "pkg/value.txt": "candidate\n"})
	gitRunStore(t, repo, "add", ".")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "candidate")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "tag", "-a", "v0.1.0", "-m", "release")
	git := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	candidate := git(t, repo, "rev-parse", "HEAD")
	root := t.TempDir()
	if err := materializeNativeSnapshot(context.Background(), ExecGitRunner{}, repo, candidate, root); err != nil {
		t.Fatal(err)
	}
	gitPath, err := nativeTargetResolvedTool("git")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := resolveNativeGitMetadata(context.Background(), gitPath, repo, candidate, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeNativeGitMetadata(context.Background(), gitPath, repo, root, metadata.Candidate); err != nil {
		t.Fatal(err)
	}
	t.Run("tracked_inventory", func(t *testing.T) {
		if got := git(t, root, "ls-files"); got != "pkg/value.txt\nversion.txt" {
			t.Fatalf("candidate inventory = %q", got)
		}
	})
	t.Run("tag_version", func(t *testing.T) {
		if got := git(t, root, "describe", "--tags", "--exact-match", "HEAD"); got != "v0.1.0" {
			t.Fatalf("candidate version = %q", got)
		}
		if got := git(t, root, "rev-parse", "HEAD"); got != candidate {
			t.Fatalf("snapshot HEAD = %q, want %q", got, candidate)
		}
	})
	t.Run("worktree_root", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
			t.Fatal(err)
		}
		if got := git(t, filepath.Join(root, "pkg"), "rev-parse", "--show-toplevel"); got != root {
			t.Fatalf("snapshot repository root = %q, want %q", got, root)
		}
	})
}

func nativeGitOutputForTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := (ExecGitRunner{}).Run(context.Background(), repo, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestNativeOracleGitMetadataIsStandalone(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			repo, root := t.TempDir(), t.TempDir()
			gitRunStore(t, repo, "init", "-b", "source", "--object-format="+format)
			writeRepoFilesForTest(t, repo, map[string]string{"value.txt": "ancestor\n"})
			gitRunStore(t, repo, "add", ".")
			gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "ancestor")
			ancestor := nativeGitOutputForTest(t, repo, "rev-parse", "HEAD")
			gitRunStore(t, repo, "tag", "lightweight")
			gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "tag", "-a", "annotated", "-m", "release")
			gitRunStore(t, repo, "tag", "blob-tag", ancestor+":value.txt")
			writeRepoFilesForTest(t, repo, map[string]string{"value.txt": "candidate\n"})
			gitRunStore(t, repo, "add", ".")
			gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "candidate")
			candidate := nativeGitOutputForTest(t, repo, "rev-parse", "HEAD")
			if err := materializeNativeSnapshot(context.Background(), ExecGitRunner{}, repo, candidate, root); err != nil {
				t.Fatal(err)
			}
			gitPath, err := nativeTargetResolvedTool("git")
			if err != nil {
				t.Fatal(err)
			}
			// Ambient overrides cannot redirect the metadata producer.
			t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "unrelated"))
			t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "unrelated-index"))
			m, err := resolveNativeGitMetadata(context.Background(), gitPath, repo, candidate, ancestor)
			if err != nil {
				t.Fatal(err)
			}
			if m.Candidate.ObjectFormat != format || m.Candidate.Commit != candidate || m.Recipe.Commit != ancestor || len(m.Candidate.Tags) != 2 || m.Candidate.Tags[0].Peeled != ancestor {
				t.Fatalf("wrong Git view: %+v", m)
			}
			if err := materializeNativeGitMetadata(context.Background(), gitPath, repo, root, m.Candidate); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(repo); err != nil {
				t.Fatal(err)
			}
			g := nativeGitReader{git: gitPath, dir: root, env: nativeGitEnv()}
			if _, err := g.run(context.Background(), "fsck", "--full", "--strict"); err != nil {
				t.Fatalf("export still needs source objects: %v", err)
			}
			if got, err := g.run(context.Background(), "rev-parse", "HEAD"); err != nil || got != candidate {
				t.Fatalf("detached HEAD is not candidate: %s %v", got, err)
			}
			p := nativeOraclePlan{Target: &nativeTargetOraclePlan{Recipe: nativeTargetOracleManifest{Root: ".", Materializer: "none"}}}
			before, err := nativeTargetInputInventory(p, root, true)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(before, func(f NativeOracleFile) bool { return f.Path == ".git/HEAD" }) {
				t.Fatal("Git metadata did not enter the sealed inventory")
			}
			if _, err := g.run(context.Background(), "status", "--porcelain"); err != nil {
				t.Fatal(err)
			}
			if err := nativeTargetInputsUnchanged(p, root, before, "input drift"); err != nil {
				t.Fatalf("ordinary Git reads changed inputs: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte(ancestor+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := nativeTargetInputsUnchanged(p, root, before, "input drift"); err == nil || !strings.Contains(err.Error(), ".git/HEAD") {
				t.Fatalf("Git mutation escaped sealed inventory: %v", err)
			}
		})
	}
}

func TestNativeOracleGitMetadataRefusesIncompleteHistory(t *testing.T) {
	for _, marker := range []string{"shallow", "info/grafts", "objects/info/alternates", "replace"} {
		t.Run(marker, func(t *testing.T) {
			repo := t.TempDir()
			gitRunStore(t, repo, "init", "-b", "source")
			writeRepoFilesForTest(t, repo, map[string]string{"value.txt": "candidate\n"})
			gitRunStore(t, repo, "add", ".")
			gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "candidate")
			candidate := nativeGitOutputForTest(t, repo, "rev-parse", "HEAD")
			if marker == "replace" {
				gitRunStore(t, repo, "update-ref", "refs/replace/"+candidate, candidate)
			} else {
				writeRepoFilesForTest(t, filepath.Join(repo, ".git"), map[string]string{marker: candidate + "\n"})
			}
			gitPath, err := nativeTargetResolvedTool("git")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := resolveNativeGitMetadata(context.Background(), gitPath, repo, candidate, candidate); failureKind(err) != KindUnavailable {
				t.Fatalf("unsupported history qualified: %v", err)
			}
		})
	}
}

func TestNativeOracleGitMetadataRefusesTagDriftBeforeExport(t *testing.T) {
	repo := t.TempDir()
	gitRunStore(t, repo, "init", "-b", "source")
	writeRepoFilesForTest(t, repo, map[string]string{"value.txt": "candidate\n"})
	gitRunStore(t, repo, "add", ".")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "candidate")
	candidate := nativeGitOutputForTest(t, repo, "rev-parse", "HEAD")
	gitPath, err := nativeTargetResolvedTool("git")
	if err != nil {
		t.Fatal(err)
	}
	m, err := resolveNativeGitMetadata(context.Background(), gitPath, repo, candidate, candidate)
	if err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "tag", "changed-after-plan")
	root := t.TempDir()
	p := nativeGitPlanForTest(t, gitPath, m)
	if err := materializeNativeTargetGit(context.Background(), repo, root, root, p); failureKind(err) != KindUnavailable || !strings.Contains(err.Error(), "changed since preparation") {
		t.Fatalf("changed source tags exported: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("metadata drift refusal wrote a partial export")
	}
}

func nativeGitPlanForTest(t *testing.T, gitPath string, m *NativeGitMetadata) nativeOraclePlan {
	t.Helper()
	e := nativeTargetEnvironment{Git: gitPath, GitMetadataDigest: nativeDigest([]byte(workflowJSON(m)))}
	var err error
	e.Tools, err = nativeTargetToolDigests(e)
	if err != nil {
		t.Fatal(err)
	}
	finalizeNativeTargetEnvironment(&e)
	return nativeOraclePlan{SubjectCommit: m.Candidate.Commit, Request: NativeOracleRequest{Phase: "execute"},
		Bundle: NativeOracleControlBundle{Control: OracleControl{RecipeSource: WorkContextReadingSource{CommitOID: m.Recipe.Commit}}},
		Target: &nativeTargetOraclePlan{Recipe: nativeTargetOracleManifest{GitMetadata: nativeGitMetadataMode}, GitMetadata: m, Environment: e}}
}

func TestNativeTargetGitMetadataPolicyAndEnv(t *testing.T) {
	for _, raw := range []string{`null`, `""`, `"ambient"`, `true`, `{}`} {
		if _, err := decodeNativeTargetManifest([]byte(`{"git_metadata":` + raw + `}`)); err == nil {
			t.Fatalf("invalid policy was admitted: %s", raw)
		}
	}
	if m, err := decodeNativeTargetManifest([]byte(`{"git_metadata":"candidate_ancestry_v1"}`)); err != nil || m.GitMetadata != nativeGitMetadataMode {
		t.Fatalf("supported policy refused: %+v, %v", m, err)
	}
	t.Setenv("PATH", "")
	root := t.TempDir()
	env := nativeTargetControlledEnv(&nativeTargetOraclePlan{Recipe: nativeTargetOracleManifest{GitMetadata: nativeGitMetadataMode}}, root)
	for _, value := range env {
		if strings.HasPrefix(value, "PATH=") && value != "PATH="+nativeTargetGitBinDir(root) {
			t.Fatalf("empty inherited PATH introduced candidate discovery: %q", value)
		}
	}
}

func TestNativeTargetGitMetadataExecutableDrift(t *testing.T) {
	repo := t.TempDir()
	gitRunStore(t, repo, "init", "-b", "source")
	writeRepoFilesForTest(t, repo, map[string]string{"value.txt": "candidate\n"})
	gitRunStore(t, repo, "add", ".")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "candidate")
	candidate := nativeGitOutputForTest(t, repo, "rev-parse", "HEAD")
	gitPath, err := nativeTargetResolvedTool("git")
	if err != nil {
		t.Fatal(err)
	}
	m, err := resolveNativeGitMetadata(context.Background(), gitPath, repo, candidate, candidate)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(copyPath, data, 0700); err != nil {
		t.Fatal(err)
	}
	p := nativeGitPlanForTest(t, copyPath, m)
	before := p.Target.Environment.Digest
	if err := os.WriteFile(copyPath, append(data, '\n'), 0700); err != nil {
		t.Fatal(err)
	}
	if err := nativeTargetGitMetadataUnchanged(context.Background(), repo, p); failureKind(err) != KindUnavailable || !strings.Contains(err.Error(), "prepared fingerprint") {
		t.Fatalf("runtime drift did not refuse before Git launch: %v", err)
	}
	p.Target.Environment.Tools, err = nativeTargetToolDigests(p.Target.Environment)
	if err != nil {
		t.Fatal(err)
	}
	finalizeNativeTargetEnvironment(&p.Target.Environment)
	if p.Target.Environment.Digest == before {
		t.Fatal("fresh runtime observation did not change the preparation identity")
	}
	if err := nativeTargetGitMetadataUnchanged(context.Background(), repo, p); err != nil {
		t.Fatalf("fresh observed runtime remained unavailable: %v", err)
	}
}

func TestNativeOracleGitMetadataRefusesPreexistingSnapshotGit(t *testing.T) {
	repo := t.TempDir()
	gitRunStore(t, repo, "init", "-b", "source")
	writeRepoFilesForTest(t, repo, map[string]string{"value.txt": "candidate\n"})
	gitRunStore(t, repo, "add", ".")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "candidate")
	candidate := nativeGitOutputForTest(t, repo, "rev-parse", "HEAD")
	gitPath, err := nativeTargetResolvedTool("git")
	if err != nil {
		t.Fatal(err)
	}
	m, err := resolveNativeGitMetadata(context.Background(), gitPath, repo, candidate, candidate)
	if err != nil {
		t.Fatal(err)
	}
	for name, seed := range map[string]map[string]string{
		"gitfile":   {".git": "gitdir: " + filepath.Join(repo, ".git") + "\n"},
		"directory": {".git/config": "[core]\n\tworktree = " + repo + "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRepoFilesForTest(t, root, seed)
			err := materializeNativeGitMetadata(context.Background(), gitPath, repo, root, m.Candidate)
			if failureKind(err) != KindUnavailable || !strings.Contains(err.Error(), "must not supply Git metadata") {
				t.Fatalf("pre-existing snapshot Git metadata was reused: %v", err)
			}
			if got := nativeGitOutputForTest(t, repo, "symbolic-ref", "HEAD"); got != "refs/heads/source" {
				t.Fatalf("source HEAD changed: %q", got)
			}
		})
	}
}
