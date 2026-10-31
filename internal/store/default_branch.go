package store

import (
	"context"
	"strings"
)

// originDefaultRef resolves the merge target ref: the override when one is
// supplied, otherwise the branch origin/HEAD names.
func originDefaultRef(ctx context.Context, runner GitRunner, repoRoot, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	refOut, refErr := runner.Run(ctx, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD")
	if refErr != nil || strings.TrimSpace(string(refOut)) == "" {
		return "", newFailure(KindGitUnreachable, "default_branch", "cannot resolve the default branch", false, "set origin/HEAD or supply the merge target ref")
	}
	return strings.TrimPrefix(strings.TrimSpace(string(refOut)), "refs/remotes/"), nil
}
