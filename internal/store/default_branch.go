package store

import (
	"context"
	"strings"
)

// DefaultBranchRef reads the Project default branch from origin/HEAD.
func DefaultBranchRef(ctx context.Context, repo string) (string, error) {
	return bootstrapDefaultBranchRef(ctx, ExecGitRunner{}, repo)
}

func bootstrapDefaultBranchRef(ctx context.Context, runner GitRunner, repo string) (string, error) {
	defaultOut, err := runner.Run(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", newFailure(KindGitUnreachable, "work_bootstrap", "cannot resolve the Project default branch", false, "set origin/HEAD")
	}
	const remotePrefix = "refs/remotes/origin/"
	value := strings.TrimSpace(string(defaultOut))
	if !strings.HasPrefix(value, remotePrefix) || len(strings.TrimPrefix(value, remotePrefix)) == 0 {
		return "", newFailure(KindGitUnreachable, "work_bootstrap", "origin/HEAD does not name a default branch", false, "set origin/HEAD")
	}
	return "origin/" + strings.TrimPrefix(value, remotePrefix), nil
}

func worktreeAuditDefaultRef(ctx context.Context, runner GitRunner, repoRoot, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	refOut, refErr := runner.Run(ctx, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD")
	if refErr != nil || strings.TrimSpace(string(refOut)) == "" {
		return "", newFailure(KindGitUnreachable, "worktree_audit", "cannot resolve the default branch", false, "set origin/HEAD or supply the merge target ref")
	}
	return strings.TrimPrefix(strings.TrimSpace(string(refOut)), "refs/remotes/"), nil
}
