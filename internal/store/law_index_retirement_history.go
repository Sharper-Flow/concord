package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// A missing legacy path is not retirement evidence: its law may still need
// conversion. Only a positively identified law-folder file's deletion on the
// default branch can account for it. Historical reads deliberately do not load
// whole snapshots: a retired pre-frontmatter file need not satisfy today's
// metadata schema, but its exact recorded bytes must still be provable.
func accountLawIndexRetirements(ctx context.Context, repo, head string, laws []lawIndexEntry) (string, bool) {
	runner := lawFolderGitRunner{}
	out, err := runner.Run(ctx, repo, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", false
	}
	defaultRef := strings.TrimSpace(string(out))
	if !strings.HasPrefix(defaultRef, "refs/remotes/origin/") || !safeGitRefPath(defaultRef) || lawIndexDefaultRefCheap(repo) != defaultRef {
		return "", false
	}
	out, err = runner.Run(ctx, repo, "rev-parse", "--verify", defaultRef+"^{commit}")
	if err != nil || strings.TrimSpace(string(out)) != head {
		return "", false
	}
	for _, law := range laws {
		if validateCommitOID(law.ScannedCommit) != nil || !lawFolderRelPathClean(strings.Split(law.Path, "/")) {
			return "", false
		}
		if _, err := runner.Run(ctx, repo, "merge-base", "--is-ancestor", law.ScannedCommit, head); err != nil {
			return "", false
		}
		content, err := committedRegularBlobBytes(ctx, runner, repo, law.ScannedCommit, law.Path)
		if err != nil || lawIndexHash(content) != law.ContentHash || !lawIndexPathAbsent(ctx, runner, repo, head, law.Path) {
			return "", false
		}
		if !lawIndexFolderDeletion(ctx, runner, repo, head, law) {
			return "", false
		}
	}
	return defaultRef, true
}

// origin/HEAD is a shared symbolic ref, including for a linked worktree.
// Retargeting it invalidates retirement proof even if its old target stayed put.
func lawIndexDefaultRefCheap(repo string) string {
	dir, err := knowledgeGitDir(repo)
	if err != nil {
		return ""
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil { //nolint:gosec // fixed Git metadata name in the designated home.
		common := strings.TrimSpace(string(raw))
		if !filepath.IsAbs(common) {
			common = filepath.Join(dir, common)
		}
		dir = common
	}
	raw, err := os.ReadFile(filepath.Join(dir, "refs/remotes/origin/HEAD")) //nolint:gosec // fixed symbolic ref name in the designated home.
	if err != nil {
		return ""
	}
	ref, _ := strings.CutPrefix(strings.TrimSpace(string(raw)), "ref: ")
	return ref
}

func lawIndexHash(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func lawIndexPathAbsent(ctx context.Context, runner GitRunner, repo, commit, path string) bool {
	out, err := runner.Run(ctx, repo, "ls-tree", "-z", commit, "--", path)
	return err == nil && len(out) == 0
}

func lawIndexFolderDeletion(ctx context.Context, runner GitRunner, repo, head string, law lawIndexEntry) bool {
	out, err := runner.Run(ctx, repo, "rev-list", "--first-parent", law.ScannedCommit+".."+head, "--", strings.TrimSuffix(lawFolderRoot, "/"))
	if err != nil {
		return false
	}
	for _, commit := range strings.Fields(string(out)) {
		parent := commit + "^1"
		out, err := runner.Run(ctx, repo, "diff-tree", "-r", "--name-only", "--diff-filter=D", "--no-renames", "-z", parent, commit, "--", strings.TrimSuffix(lawFolderRoot, "/"))
		if err != nil {
			return false
		}
		for _, path := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
			segments := strings.Split(strings.TrimPrefix(path, lawFolderRoot), "/")
			if len(segments) < 2 || !lawFolderRelPathClean(segments) || !lawIndexPathAbsent(ctx, runner, repo, head, path) {
				continue
			}
			if _, ok := lawFolderTierKind(segments[0]); !ok {
				continue
			}
			content, err := committedRegularBlobBytes(ctx, runner, repo, parent, path)
			if err != nil {
				return false
			}
			if lawIndexHash(content) == law.ContentHash {
				return true
			}
			front, ok := splitLawFolderFrontMatter(content)
			if !ok || payloadschema.ValidateUniqueJSON(front) != nil {
				continue
			}
			var identity struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(front, &identity) == nil && identity.ID == law.LawID {
				return true
			}
		}
	}
	return false
}
