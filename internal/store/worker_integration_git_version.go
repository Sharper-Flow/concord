package store

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const integrationGitVersionOp = "integration_git_version"

var integrationVersionProbeTimeout = 5 * time.Second

// The >= 2.38.0 Git floor for architecture-capable native integration.
const (
	integrationGitFloorMajor = 2
	integrationGitFloorMinor = 38
)

var errNotAGitVersionReport = errors.New("git --version output is not a Git version report")

// integrationGitVersionRe accepts exactly one whole `git --version` report:
// the literal prefix, major.minor, an optional numeric patch, and one optional
// build or vendor suffix that only a numeric patch can precede. The Python installer
// preflight (scripts/install.py) keeps the same shape in intent.
var integrationGitVersionRe = regexp.MustCompile(`^git version ([0-9]+)\.([0-9]+)(?:\.([0-9]+)(?:\.[A-Za-z0-9][A-Za-z0-9.-]*| \([A-Za-z0-9 .-]+\))?)?$`)

// requireIntegrationGitVersion probes the host Git version through the caller's
// GitRunner under the fixed five-second bound and refuses any Git below the
// integration floor before an integration proof derives. The full reported
// version is retained on success; refusals are typed; a nil runner uses the
// bounded default.
func requireIntegrationGitVersion(ctx context.Context, runner GitRunner, repo string) (string, error) {
	if runner == nil {
		runner = runGitRunner{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, integrationVersionProbeTimeout)
	defer cancel()
	out, err := runner.Run(probeCtx, repo, "--version")
	if err != nil {
		if probeCtx.Err() != nil {
			return "", wrapFailure(KindTimeout, integrationGitVersionOp, "the Git version probe did not complete before its context ended", true, "retry once Git answers", err)
		}
		return "", wrapFailure(KindGitUnreachable, integrationGitVersionOp, "cannot probe the host Git version", true, "retry once Git is reachable", err)
	}
	version, major, minor, err := parseIntegrationGitVersion(string(out))
	if err != nil {
		return "", newFailure(KindMalformedResponse, integrationGitVersionOp, "git --version output is not a Git version report", false, "install a supported Git release and retry")
	}
	if major < integrationGitFloorMajor || (major == integrationGitFloorMajor && minor < integrationGitFloorMinor) {
		return "", newFailure(KindInvalidOperation, integrationGitVersionOp, "host Git "+version+" does not meet the >= 2.38.0 integration floor", false, "install Git 2.38.0 or newer and retry")
	}
	return version, nil
}

// parseIntegrationGitVersion parses one `git --version` report and returns the
// full reported version text (the tool identity, without the `git version `
// prefix and a single optional final newline) with its major/minor numbers.
func parseIntegrationGitVersion(report string) (string, int, int, error) {
	report = strings.TrimSuffix(report, "\n")
	m := integrationGitVersionRe.FindStringSubmatch(report)
	if m == nil {
		return "", 0, 0, errNotAGitVersionReport
	}
	var nums [3]int
	for i, part := range m[1:4] {
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return "", 0, 0, errNotAGitVersionReport
		}
		nums[i] = value
	}
	return report[len("git version "):], nums[0], nums[1], nil
}
