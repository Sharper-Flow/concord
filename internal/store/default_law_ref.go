package store

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
)

const defaultLawRefRecovery = "run git fetch origin and git remote set-head origin --auto in the knowledge home, then verify its default-branch law again"

// resolveDefaultLawRefCheap binds current law to the local origin default's
// concrete ref, never the checkout or the retargetable origin/HEAD alias.
// It also runs inside transactions, so it only reads local Git metadata.
func resolveDefaultLawRefCheap(repo string) (string, error) {
	fail := func(detail string) (string, error) {
		return "", newFailure(KindGitUnreachable, "knowledge_home", "cannot establish default-branch law authority: "+detail, true, defaultLawRefRecovery)
	}
	dir, err := knowledgeGitDir(repo)
	if err != nil {
		return fail("the knowledge home's Git metadata is unreachable")
	}
	dirs := cheapGitObjectDirs(dir)
	common := dirs[len(dirs)-1]
	raw, err := os.ReadFile(path.Join(common, "refs/remotes/origin/HEAD")) //nolint:gosec // fixed Git metadata name in the designated home.
	if err != nil {
		return fail("local refs/remotes/origin/HEAD is missing or unreadable")
	}
	ref, symbolic := strings.CutPrefix(strings.TrimSpace(string(raw)), "ref: ")
	if !symbolic || !safeDefaultLawRef(ref) {
		return fail("local refs/remotes/origin/HEAD must designate a safe refs/remotes/origin/<branch> other than origin/HEAD")
	}
	if _, err := resolveKnowledgeHeadCheap(repo, ref); err != nil {
		return fail("the designated default ref " + ref + " cannot be resolved locally")
	}
	return ref, nil
}

func safeDefaultLawRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/remotes/origin/") || ref == "refs/remotes/origin/HEAD" || !safeGitRefPath(ref) || strings.HasSuffix(ref, ".") || strings.ContainsAny(ref, " ~^:?*[\\\t\r\n\x00\x7f") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
		for _, ch := range part {
			if ch < 32 {
				return false
			}
		}
	}
	return true
}

func currentLawHome(home KnowledgeHome) (KnowledgeHome, error) {
	ref, err := resolveDefaultLawRefCheap(home.RepoPath)
	if err != nil {
		return KnowledgeHome{}, err
	}
	home.HeadRef = ref
	return home, nil
}

func resolveCurrentLawHead(ctx context.Context, home KnowledgeHome) (string, error) {
	head, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		return "", wrapFailure(KindGitUnreachable, "knowledge_home", "cannot resolve default-branch law authority at "+home.HeadRef+" to a local commit", true, defaultLawRefRecovery, err)
	}
	return head, nil
}

func requireDefaultLawRef(home KnowledgeHome) error {
	ref, err := resolveDefaultLawRefCheap(home.RepoPath)
	if err != nil {
		return err
	}
	if ref != home.HeadRef {
		return newFailure(KindInvalidOperation, "check_mandated_laws", fmt.Sprintf("the default law ref changed from %s to %s after verification", home.HeadRef, ref), true, defaultLawRefRecovery)
	}
	return nil
}
