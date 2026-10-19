package store

import (
	"context"
	"regexp"
	"strings"
)

// workflowCIWorkflowPath is where a Project declares its CI shard jobs.
const workflowCIWorkflowPath = ".github/workflows/ci.yml"

// workflowShardRun matches one declared shard invocation and captures the
// package dealt into shards, whatever else its job line runs.
var workflowShardRun = regexp.MustCompile(`go-test-shard\.py \./(\S+) [1-9][0-9]* [1-9][0-9]*\)`)

// workflowShardedPackages reads the packages a Project's CI tests only as
// shard jobs. Such a package has no single executing suite, so one ordinary
// run of its go_package check is not the run CI performs. The declaration is
// the Project's own CI workflow.
func workflowShardedPackages(ci []byte) map[string]bool {
	sharded := map[string]bool{}
	for _, line := range strings.Split(string(ci), "\n") {
		match := workflowShardRun.FindStringSubmatch(line)
		if match != nil && workflowValidPackagePath(match[1]) {
			sharded[match[1]] = true
		}
	}
	return sharded
}

// readWorkflowShardedPackages reads the shard declaration from the Project
// repository at one commit. A Project without the CI workflow declares no
// shards; an unreachable repository or commit refuses.
func readWorkflowShardedPackages(ctx context.Context, runner GitRunner, repoRoot, commit string) (map[string]bool, error) {
	if !worktreeSHAPattern.MatchString(commit) {
		return nil, newFailure(KindInvariantViolation, "workflow_check_shards", "the shard declaration read names no commit", false, "resolve the Project commit first")
	}
	// ls-tree separates "absent at the commit" (empty output, exit 0) from an
	// unreachable repository or commit (nonzero exit).
	listing, err := runner.Run(ctx, repoRoot, "ls-tree", commit, "--", workflowCIWorkflowPath)
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "workflow_check_shards", "cannot inspect "+workflowCIWorkflowPath+" at the Project commit", false, "retry once the Project repository is reachable", err)
	}
	if len(strings.TrimSpace(string(listing))) == 0 {
		return map[string]bool{}, nil
	}
	raw, err := runner.Run(ctx, repoRoot, "cat-file", "blob", commit+":"+workflowCIWorkflowPath)
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "workflow_check_shards", "cannot read "+workflowCIWorkflowPath+" at the Project commit", false, "retry once the Project repository is reachable", err)
	}
	return workflowShardedPackages(raw), nil
}

// workflowShardedPackageChecks returns the go_package checks whose package
// the Project's CI tests only as shards, in check order.
func workflowShardedPackageChecks(checks []FootprintCheck, sharded map[string]bool) []FootprintCheck {
	var out []FootprintCheck
	for _, check := range checks {
		if check.Kind != "go_package" {
			continue
		}
		if pkg, ok := workflowPackageCheckPackage(check.Value); ok && sharded[pkg] {
			out = append(out, check)
		}
	}
	return out
}
