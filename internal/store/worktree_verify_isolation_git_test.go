package store

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Export planning reads ELF metadata and synthetic scratch only. No Git,
// launcher, loader, package manager or candidate process runs here.
func TestNativeIsolationGitExport(t *testing.T) {
	t.Setenv("LANG", "C")
	t.Setenv("LC_ALL", "C")
	git, err := nativeTargetResolvedTool("git")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(git)
	if err != nil {
		t.Fatal(err)
	}
	git = filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(git, data, 0700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	bin := nativeTargetGitBinDir(root)
	for _, dir := range []string{snapshot, bin} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(bin, "git")
	if err := os.Symlink(git, alias); err != nil {
		t.Fatal(err)
	}
	target := &nativeTargetOraclePlan{
		Recipe: nativeTargetOracleManifest{Materializer: "none", Runner: "check", Isolation: nativeIsolationLoopback, GitMetadata: nativeGitMetadataMode},
		Environment: nativeTargetEnvironment{Git: git, Isolation: &nativeIsolationEnvironment{
			Policy: nativeIsolationLoopback, Profile: nativeIsolationProfile,
			Files: []NativeOracleFile{{Path: "synthetic-launcher", SHA256: nativeDigest([]byte("fixed"))}},
		}},
	}
	target.Environment.Tools, err = nativeTargetToolDigests(target.Environment)
	if err != nil {
		t.Fatal(err)
	}
	withNode := target.Environment
	withNode.Node = git
	tools, err := nativeTargetToolDigests(withNode)
	if err != nil || !slices.ContainsFunc(tools, func(f NativeOracleFile) bool { return f.Path == "git" }) || !slices.ContainsFunc(tools, func(f NativeOracleFile) bool { return f.Path == "node" }) {
		t.Fatalf("Git and Node tool slots did not survive together: %v %v", tools, err)
	}
	if err := nativeIsolationBindTargetExports(target, snapshot); err != nil {
		t.Fatal(err)
	}
	closure, err := nativeIsolationELFClosure([]string{git})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range closure {
		if !slices.ContainsFunc(target.Environment.Isolation.Exports, func(f NativeOracleFile) bool { return f.Path == name }) {
			t.Fatalf("Git executable/library missing from prepared exports: %s", name)
		}
	}
	build := func() (*nativeStageIsolation, error) {
		return nativeIsolationBuildExport(target, root, snapshot, snapshot, []string{alias, "ls-files"})
	}
	plan, err := build()
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan.Argv, "\x00")
	for _, file := range target.Environment.Isolation.Exports {
		resolved, err := filepath.EvalSymlinks(file.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "--ro-bind\x00"+resolved+"\x00"+file.Path+"\x00") {
			t.Fatalf("runtime export is not an exact read-only file: %s", file.Path)
		}
	}
	if !strings.Contains(text, "--ro-bind\x00"+git+"\x00"+alias+"\x00") {
		t.Fatal("pinned Git PATH alias not exported read-only")
	}
	if strings.Contains(text, "--ro-bind\x00"+bin+"\x00") {
		t.Fatal("whole Git bin directory exported instead of its one pinned alias")
	}
	for _, path := range []string{"/usr", "/lib", "/lib64"} {
		if strings.Contains(text, "--ro-bind\x00"+path+"\x00") {
			t.Fatalf("broad host export: %s", path)
		}
	}
	t.Run("opt-in-only", func(t *testing.T) {
		legacy := *target
		legacy.Recipe.GitMetadata = ""
		paths, err := nativeIsolationExportPaths(&legacy, snapshot)
		if err != nil || slices.Contains(paths, git) {
			t.Fatalf("nonopted recipe exported Git: %v %v", paths, err)
		}
	})
	t.Run("missing-runtime", func(t *testing.T) {
		missing := *target
		missing.Environment.Git = ""
		if _, err := nativeIsolationExportPaths(&missing, snapshot); err == nil {
			t.Fatal("opted recipe without Git runtime qualified")
		}
	})
	t.Run("alias-drift", func(t *testing.T) {
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		if _, err := build(); err == nil {
			t.Fatal("missing Git alias qualified")
		}
		if err := os.Symlink("/unprepared/git", alias); err != nil {
			t.Fatal(err)
		}
		if _, err := build(); err == nil {
			t.Fatal("retargeted Git alias qualified")
		}
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(git, alias); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("export-drift", func(t *testing.T) {
		exports := target.Environment.Isolation.Exports
		old := exports[0]
		exports[0].SHA256 = nativeDigest([]byte("changed library identity"))
		if _, err := build(); err == nil {
			t.Fatal("changed prepared export identity qualified")
		}
		exports[0] = old
	})
	t.Run("executable-mode-drift", func(t *testing.T) {
		if err := os.Chmod(git, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := build(); err == nil {
			t.Fatal("changed runtime mode qualified")
		}
		if err := os.Chmod(git, 0700); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("executable-byte-drift", func(t *testing.T) {
		if err := os.WriteFile(git, append(slices.Clone(data), '\n'), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := build(); err == nil {
			t.Fatal("changed runtime bytes qualified")
		}
		if err := os.WriteFile(git, data, 0700); err != nil {
			t.Fatal(err)
		}
	})
	restored, err := build()
	if err != nil || restored.Fingerprint != plan.Fingerprint {
		t.Fatalf("unchanged exports are not stable: %v", err)
	}
}

func TestNativeIsolationGitReceiptBindings(t *testing.T) {
	subject, recipe := strings.Repeat("a", 40), strings.Repeat("b", 40)
	metadata := &NativeGitMetadata{Mode: nativeGitMetadataMode, Candidate: NativeGitView{Commit: subject}, Recipe: NativeGitView{Commit: recipe}}
	target := &nativeTargetOraclePlan{
		Recipe:      nativeTargetOracleManifest{Materializer: "none", Runner: "check", Isolation: nativeIsolationLoopback, GitMetadata: nativeGitMetadataMode},
		GitMetadata: metadata,
		Environment: nativeTargetEnvironment{Materializer: "none", Runner: "check", RuntimeIdentity: "pinned runtime", Git: "/synthetic/git",
			GitMetadataDigest: nativeDigest([]byte(workflowJSON(metadata))),
			Isolation: &nativeIsolationEnvironment{Policy: nativeIsolationLoopback, Profile: nativeIsolationProfile,
				Files:   []NativeOracleFile{{Path: "synthetic-launcher", SHA256: nativeDigest([]byte("launcher"))}},
				Exports: []NativeOracleFile{{Path: "/synthetic/git", SHA256: nativeDigest([]byte("git"))}},
			},
		},
	}
	finalizeNativeTargetEnvironment(&target.Environment)
	bound := target.Environment.Digest
	for _, omit := range []string{"isolation", "git"} {
		e := target.Environment
		if omit == "isolation" {
			e.Isolation = nil
		} else {
			e.GitMetadataDigest = ""
		}
		finalizeNativeTargetEnvironment(&e)
		if e.Digest == bound {
			t.Fatalf("%s not bound to combined environment identity", omit)
		}
	}
	p := nativeOraclePlan{WorkID: "work", ProjectID: "project", ContractVersion: 1, SubjectCommit: subject, Target: target,
		Request:      NativeOracleRequest{Phase: "prepare", WorkerJobBinding: &WorkerJobBinding{}},
		Bundle:       NativeOracleControlBundle{Control: OracleControl{Cwd: ".", Argv: []string{"check", "witness"}, RecipeSource: WorkContextReadingSource{CommitOID: recipe}}},
		ManifestFile: NativeOracleFile{BlobOID: recipe, SHA256: nativeDigest([]byte("manifest"))},
		Files:        []NativeOracleFile{{Path: "check.sh", SHA256: nativeDigest([]byte("harness"))}},
		Stages:       []NativeOracleStage{{Name: "test", Argv: []string{"bash", "check.sh"}}},
	}
	p.BundleDigest = nativeBundleDigest(p.Bundle)
	initial := nativeOracleInitialResult(WorktreeVerifyRequest{WorkID: p.WorkID, ProjectID: p.ProjectID, Oracle: &p.Request}, WorktreeEntry{}, p)
	o := initial.Oracle
	if o.GitMetadata == nil || o.IsolationPolicy != nativeIsolationLoopback || o.IsolationEnforcement != "unavailable" {
		t.Fatal("initial result lost Git metadata or fabricated isolation enforcement")
	}
	o.Qualification = "ready"
	o.IsolationEnforcement = "established"
	o.Stages = slices.Clone(p.Stages)
	o.Stages[0].IsolationEnforced = true
	o.Stages[0].IsolationFingerprint = nativeDigest([]byte("export"))
	if !nativePreparationJoinsValid(o, p) {
		t.Fatal("combined preparation refused")
	}
	p.Request.Phase = "execute"
	o.InputManifestDigest = nativeDigest([]byte("inputs"))
	digest, identity := nativePlanEnvironment(p)
	check := func(candidate *NativeOracleResult) bool {
		if !nativeOraclePlanMatchesReceipt(p, p.WorkID, p.ProjectID, candidate, digest, identity) {
			return false
		}
		prepare := p
		prepare.Request.Phase = "prepare"
		return nativePreparationJoinsValid(candidate, prepare)
	}
	if !check(o) {
		t.Fatal("combined execute receipt refused")
	}
	for name, change := range map[string]func(*NativeOracleResult){
		"missing-git": func(o *NativeOracleResult) { o.GitMetadata = nil },
		"changed-git": func(o *NativeOracleResult) {
			m := *o.GitMetadata
			m.Candidate.Commit = recipe
			o.GitMetadata = &m
		},
		"missing-isolation":  func(o *NativeOracleResult) { o.IsolationFingerprint = "" },
		"unproved-isolation": func(o *NativeOracleResult) { o.IsolationEnforcement = "unavailable" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *o
			change(&candidate)
			prepare := p
			prepare.Request.Phase = "prepare"
			if nativePreparationJoinsValid(&candidate, prepare) || nativeOraclePlanMatchesReceipt(p, p.WorkID, p.ProjectID, &candidate, digest, identity) {
				t.Fatal("one binding bypassed the other at a receipt/preparation join")
			}
		})
	}
}
