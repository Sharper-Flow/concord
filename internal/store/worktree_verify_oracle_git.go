package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const nativeGitMetadataMode = "candidate_ancestry_v1"

type nativeGitPolicy string

func (p *nativeGitPolicy) UnmarshalJSON(raw []byte) error {
	var value string
	if json.Unmarshal(raw, &value) != nil || value != nativeGitMetadataMode {
		return nativeUnavailable("unsupported Git metadata policy")
	}
	*p = nativeGitPolicy(value)
	return nil
}

const (
	nativeGitMaxTags     = 4096
	nativeGitRefMaxBytes = 256
)

// NativeGitMetadata binds semantics independently of Git's pack and index encoding.
// Both views are needed: collection consumes the recipe, acceptance the candidate.
type NativeGitMetadata struct {
	Mode      string        `json:"mode"`
	Candidate NativeGitView `json:"candidate"`
	Recipe    NativeGitView `json:"recipe"`
}

type NativeGitView struct {
	ObjectFormat string         `json:"object_format"`
	Commit       string         `json:"commit"`
	Tree         string         `json:"tree"`
	Tags         []NativeGitTag `json:"tags"`
}

type NativeGitTag struct {
	Ref    string `json:"ref"`
	Object string `json:"object"`
	Peeled string `json:"peeled"`
}

// Git runs with no inherited Git variables, credentials or user configuration.
// Source configuration is used only to locate its object store and enumerate refs;
// traversal and packing use a fresh bare reader, never the source's configuration.
func nativeGitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "HOME=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0",
	}
}

type nativeGitReader struct {
	git, dir string
	env      []string
}

func (g nativeGitReader) command(ctx context.Context, args ...string) *exec.Cmd {
	argv := append([]string{"--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.commitGraph=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "protocol.allow=never"}, args...)
	cmd := exec.CommandContext(ctx, g.git, argv...) //nolint:gosec // resolved fingerprinted Git; producer-owned plumbing argv, no shell.
	cmd.Dir, cmd.Env, cmd.WaitDelay = g.dir, g.env, boundedGitWaitDelay
	return cmd
}

func (g nativeGitReader) run(ctx context.Context, args ...string) (string, error) {
	return g.read(ctx, nil, args...)
}

func (g nativeGitReader) read(ctx context.Context, input io.Reader, args ...string) (string, error) {
	var out, diagnostic nativeGitBuffer
	cmd := g.command(ctx, args...)
	cmd.Stdin = input
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Run(); err != nil {
		return "", nativeUnavailable(fmt.Sprintf("candidate Git metadata %s: %v: %s", args[0], err, diagnostic.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Metadata output is bounded independently of retained test-evidence streams.
type nativeGitBuffer struct{ bytes.Buffer }

func (b *nativeGitBuffer) Write(p []byte) (int, error) {
	if len(p) > 1024*1024-b.Len() {
		return 0, errors.New("git metadata output exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}

func openNativeGitReader(ctx context.Context, git, repo string) (nativeGitReader, func(), error) {
	source := nativeGitReader{git: git, dir: repo, env: nativeGitEnv()}
	common, err := source.run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nativeGitReader{}, func() {}, err
	}
	format, err := source.run(ctx, "rev-parse", "--show-object-format")
	if err != nil || (format != "sha1" && format != "sha256") {
		return nativeGitReader{}, func() {}, nativeUnavailable("unsupported Git object format")
	}
	for _, name := range []string{"shallow", "info/grafts", "objects/info/alternates"} {
		if _, err := os.Lstat(filepath.Join(common, name)); !os.IsNotExist(err) {
			return nativeGitReader{}, func() {}, nativeUnavailable("candidate Git metadata requires complete standalone history without shallow, graft or alternate inputs")
		}
	}
	replacements, err := source.run(ctx, "for-each-ref", "--format=%(refname)", "refs/replace/")
	if err != nil || replacements != "" {
		return nativeGitReader{}, func() {}, nativeUnavailable("candidate Git metadata refuses replacement refs")
	}
	dir, err := os.MkdirTemp("", "concord-native-git-")
	if err != nil {
		return nativeGitReader{}, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	g := nativeGitReader{git: git, dir: dir, env: nativeGitEnv()}
	if _, err := g.run(ctx, "init", "--bare", "--template=", "--object-format="+format); err != nil {
		cleanup()
		return nativeGitReader{}, func() {}, err
	}
	g.env = append(g.env, "GIT_OBJECT_DIRECTORY="+filepath.Join(common, "objects"))
	return g, cleanup, nil
}

func nativeGitOID(oid, format string) bool {
	want := 40
	if format == "sha256" {
		want = 64
	}
	return worktreeSHAPattern.MatchString(oid) && len(oid) == want
}

func (g nativeGitReader) view(ctx context.Context, source nativeGitReader, commit string) (NativeGitView, error) {
	format, err := g.run(ctx, "rev-parse", "--show-object-format")
	view := NativeGitView{ObjectFormat: format, Commit: commit, Tags: []NativeGitTag{}}
	if err != nil || !nativeGitOID(commit, format) {
		return view, nativeUnavailable("candidate Git metadata requires an exact commit identity")
	}
	view.Tree, err = g.run(ctx, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil || !nativeGitOID(view.Tree, format) {
		return view, nativeUnavailable("candidate Git metadata lacks its exact tree")
	}
	refs, err := source.run(ctx, "for-each-ref", "--merged="+commit, "--sort=refname", "--format=%(refname) %(objectname)", "refs/tags/")
	if err != nil || refs == "" {
		return view, err
	}
	lines := strings.Split(refs, "\n")
	if len(lines) > nativeGitMaxTags {
		return view, nativeUnavailable("candidate Git metadata exceeds 4096 ancestor tags")
	}
	var input strings.Builder
	for _, line := range lines {
		ref, oid, ok := strings.Cut(line, " ")
		if !ok || !strings.HasPrefix(ref, "refs/tags/") || len(ref) > nativeGitRefMaxBytes || !nativeGitOID(oid, format) {
			return view, nativeUnavailable("candidate Git metadata has an invalid tag identity")
		}
		view.Tags = append(view.Tags, NativeGitTag{Ref: ref, Object: oid})
		input.WriteString(oid + "^{}\n")
	}
	peeled, err := g.read(ctx, strings.NewReader(input.String()), "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return view, err
	}
	objects := strings.Split(peeled, "\n")
	if len(objects) != len(view.Tags) {
		return view, nativeUnavailable("candidate Git metadata has an incomplete peeled tag inventory")
	}
	for i, line := range objects {
		oid, kind, ok := strings.Cut(line, " ")
		if !ok || kind != "commit" || !nativeGitOID(oid, format) {
			return view, nativeUnavailable("candidate Git metadata has an invalid peeled commit identity")
		}
		view.Tags[i].Peeled = oid
	}
	return view, nil
}

func resolveNativeGitMetadata(ctx context.Context, git, repo, candidate, recipe string) (*NativeGitMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	g, cleanup, err := openNativeGitReader(ctx, git, repo)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	source := nativeGitReader{git: git, dir: repo, env: nativeGitEnv()}
	m := &NativeGitMetadata{Mode: nativeGitMetadataMode}
	m.Candidate, err = g.view(ctx, source, candidate)
	if err != nil {
		return nil, err
	}
	m.Recipe = m.Candidate
	if recipe != candidate {
		m.Recipe, err = g.view(ctx, source, recipe)
	}
	return m, err
}

// Build the metadata after the harness overlay. read-tree writes only the index:
// the pinned files on disk remain the consumed view, not a fabricated commit.
func materializeNativeGitMetadata(ctx context.Context, git, repo, snapshot string, view NativeGitView) error {
	if _, err := os.Lstat(filepath.Join(snapshot, ".git")); !os.IsNotExist(err) {
		return nativeUnavailable("pinned harness inputs must not supply Git metadata")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	g, cleanup, err := openNativeGitReader(ctx, git, repo)
	defer cleanup()
	if err != nil {
		return err
	}
	pack, err := os.CreateTemp(g.dir, "objects-*.pack")
	if err != nil {
		return err
	}
	defer func() { _ = pack.Close() }()
	tips := []string{view.Commit}
	for _, tag := range view.Tags {
		tips = append(tips, tag.Object)
	}
	cmd := g.command(ctx, "pack-objects", "--stdout", "--revs", "--threads=1")
	cmd.Stdin = strings.NewReader(strings.Join(tips, "\n") + "\n")
	cmd.Stdout = &nativeGitPackWriter{file: pack, remaining: 128 * 1024 * 1024}
	var diagnostic nativeGitBuffer
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		return nativeUnavailable(fmt.Sprintf("candidate Git pack unavailable (128 MiB limit): %v: %s", err, diagnostic.String()))
	}
	if _, err := pack.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dest := nativeGitReader{git: git, dir: snapshot, env: nativeGitEnv()}
	if _, err := dest.run(ctx, "init", "--template=", "--object-format="+view.ObjectFormat); err != nil {
		return err
	}
	cmd = dest.command(ctx, "index-pack", "--stdin", "--strict")
	cmd.Stdin = pack
	var output nativeGitBuffer
	diagnostic.Reset()
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if err := cmd.Run(); err != nil {
		return nativeUnavailable(fmt.Sprintf("candidate Git object verification: %v: %s", err, diagnostic.String()))
	}
	for _, args := range [][]string{{"config", "core.logAllRefUpdates", "false"}, {"update-ref", "--no-deref", "HEAD", view.Commit}, {"read-tree", view.Commit}} {
		if _, err := dest.run(ctx, args...); err != nil {
			return err
		}
	}
	var refs strings.Builder
	for _, tag := range view.Tags {
		refs.WriteString("create " + tag.Ref + " " + tag.Object + "\n")
	}
	if refs.Len() > 0 {
		if _, err := dest.read(ctx, strings.NewReader(refs.String()), "update-ref", "--stdin"); err != nil {
			return err
		}
	}
	tree, err := dest.run(ctx, "write-tree")
	if err != nil || tree != view.Tree {
		return nativeUnavailable("candidate Git index differs from the exact subject tree")
	}
	return nil
}

type nativeGitPackWriter struct {
	file      *os.File
	remaining int
}

func (w *nativeGitPackWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, errors.New("candidate Git pack exceeds 128 MiB")
	}
	n, err := w.file.Write(p)
	w.remaining -= n
	return n, err
}

func nativeTargetGitBinDir(root string) string { return filepath.Join(root, "git-bin") }

func nativeTargetGitExecutable(t *nativeTargetOraclePlan) ([]byte, error) {
	data, err := os.ReadFile(t.Environment.Git)
	if err != nil {
		return nil, nativeUnavailable("candidate Git executable is unavailable")
	}
	for _, tool := range t.Environment.Tools {
		if tool.Path == "git" && tool.SHA256 == nativeDigest(data) {
			return data, nil
		}
	}
	return nil, nativeUnavailable("candidate Git executable differs from the prepared fingerprint")
}

func normalizeNativeTargetGitMetadata(ctx context.Context, repo string, p *nativeOraclePlan) error {
	t := p.Target
	if t.Recipe.GitMetadata == "" {
		return nil
	}
	git, err := nativeTargetResolvedTool("git")
	if err != nil {
		return nativeUnavailable("candidate Git metadata requires an installed Git executable")
	}
	t.GitMetadata, err = resolveNativeGitMetadata(ctx, git, repo, p.SubjectCommit, p.Bundle.Control.RecipeSource.CommitOID)
	if err != nil {
		return err
	}
	if repair := nativeReviewRepairFrom(ctx); repair != nil {
		prepared, err := resolveNativeGitMetadata(ctx, git, repo, repair.Base, p.Bundle.Control.RecipeSource.CommitOID)
		if err != nil {
			return err
		}
		if repair.Commit != p.SubjectCommit || !oracleEntryIdentical(prepared.Candidate.Tags, t.GitMetadata.Candidate.Tags) || !oracleEntryIdentical(prepared.Recipe, t.GitMetadata.Recipe) {
			return oracleFailure(KindUnauthorizedDispatch, "scoped repair Git metadata differs from its preparation ancestry", "restore the prepared tag identities or request fresh explicit preparation through an admitted route")
		}
		t.PreparedGitCandidate = &prepared.Candidate
	}
	t.Environment.Git = git
	t.Environment.GitMetadataDigest = nativeDigest([]byte(workflowJSON(nativeGitPreparationMetadata(*p))))
	return nil
}

func nativeGitPreparationMetadata(p nativeOraclePlan) *NativeGitMetadata {
	if p.Target == nil || p.Target.GitMetadata == nil {
		return nil
	}
	m := *p.Target.GitMetadata
	if p.Target.PreparedGitCandidate != nil {
		m.Candidate = *p.Target.PreparedGitCandidate
	}
	return &m
}

func nativeGitMetadataMatches(p nativeOraclePlan, got *NativeGitMetadata) bool {
	if p.Target == nil {
		return got == nil
	}
	t := p.Target
	if t.Recipe.GitMetadata == "" {
		return got == nil && t.GitMetadata == nil && t.PreparedGitCandidate == nil && t.Environment.Git == "" && t.Environment.GitMetadataDigest == ""
	}
	if t.GitMetadata == nil || t.GitMetadata.Candidate.Commit != p.SubjectCommit || t.GitMetadata.Recipe.Commit != p.Bundle.Control.RecipeSource.CommitOID {
		return false
	}
	if p.ReviewRepair == nil {
		if t.PreparedGitCandidate != nil {
			return false
		}
	} else if t.PreparedGitCandidate == nil || t.PreparedGitCandidate.Commit != p.ReviewRepair.Base || t.GitMetadata.Candidate.Commit != p.ReviewRepair.Commit ||
		t.PreparedGitCandidate.ObjectFormat != t.GitMetadata.Candidate.ObjectFormat || !oracleEntryIdentical(t.PreparedGitCandidate.Tags, t.GitMetadata.Candidate.Tags) {
		return false
	}
	return t.Recipe.GitMetadata == nativeGitMetadataMode && t.GitMetadata.Mode == nativeGitMetadataMode && t.Environment.Git != "" &&
		t.Environment.GitMetadataDigest == nativeDigest([]byte(workflowJSON(nativeGitPreparationMetadata(p)))) && oracleEntryIdentical(got, t.GitMetadata)
}

func materializeNativeTargetGit(ctx context.Context, repo, root, snapshot string, p nativeOraclePlan) error {
	t := p.Target
	if t.Recipe.GitMetadata == "" {
		return nil
	}
	if err := nativeTargetGitMetadataUnchanged(ctx, repo, p); err != nil {
		return err
	}
	view := t.GitMetadata.Candidate
	if p.Request.Phase == "prepare" {
		view = t.GitMetadata.Recipe
	}
	if err := materializeNativeGitMetadata(ctx, t.Environment.Git, repo, snapshot, view); err != nil {
		return err
	}
	bin := nativeTargetGitBinDir(root)
	if err := os.Mkdir(bin, 0700); err != nil {
		return err
	}
	return os.Symlink(t.Environment.Git, filepath.Join(bin, "git"))
}

func nativeTargetGitMetadataUnchanged(ctx context.Context, repo string, p nativeOraclePlan) error {
	t := p.Target
	if t.Recipe.GitMetadata == "" {
		return nil
	}
	if !nativeGitMetadataMatches(p, t.GitMetadata) {
		return nativeUnavailable("candidate Git metadata is missing its prepared descriptor")
	}
	if _, err := nativeTargetGitExecutable(t); err != nil {
		return err
	}
	current, err := resolveNativeGitMetadata(ctx, t.Environment.Git, repo, p.SubjectCommit, p.Bundle.Control.RecipeSource.CommitOID)
	if err != nil {
		return err
	}
	if !oracleEntryIdentical(current, t.GitMetadata) {
		return nativeUnavailable("candidate Git metadata changed since preparation")
	}
	if t.PreparedGitCandidate != nil {
		prepared, err := resolveNativeGitMetadata(ctx, t.Environment.Git, repo, t.PreparedGitCandidate.Commit, p.Bundle.Control.RecipeSource.CommitOID)
		if err != nil {
			return err
		}
		if !oracleEntryIdentical(prepared, nativeGitPreparationMetadata(p)) {
			return nativeUnavailable("scoped repair Git preparation metadata changed")
		}
	}
	return nil
}
