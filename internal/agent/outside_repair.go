package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// Completion evidence is collected outside write transactions through
// Service.ExternalEvidenceCommand and bound to the Project's git_remote.

const (
	OutsideRepairOperationDisposition = "outside_repair"
	OutsideRepairOperationReconcile   = "outside_repair_reconcile"

	OutsideRepairModeCompleted = "completed"
	OutsideRepairModeResume    = "resume"

	outsideRepairHoldIntent      = "close_outside_repair"
	outsideRepairRefreshedIntent = "refresh_outside_repair"
	outsideRepairVerifiedIntent  = "verify_outside_repair"
)

// boundedExternalEvidenceWait caps one external command run while the runtime
// budget still bounds the whole operation on its dispatch context.
const boundedExternalEvidenceWait = 300 * time.Second

var (
	outsideRepairRepositoryPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}/[a-z0-9][a-z0-9._-]{0,99}$`)
	outsideRepairReleaseTagPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	outsideRepairSelectorPattern    = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	outsideRepairSHAPattern         = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	outsideRepairLinearTokenPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]*-[0-9]+\b`)
)

func outsideRepairReleaseTagBound(tag string) bool {
	return outsideRepairReleaseTagPattern.MatchString(tag)
}

func outsideRepairPullRequestsBound(numbers []int64) bool {
	if len(numbers) == 0 || len(numbers) > 32 {
		return false
	}
	seen := map[int64]bool{}
	for _, number := range numbers {
		if number < 1 || seen[number] {
			return false
		}
		seen[number] = true
	}
	return true
}

// outsideRepairPullRequestSelectorBound bounds the decimal strings the
// reconcile challenge binds; validChallengeScope consumes it.
func outsideRepairPullRequestSelectorBound(selector string) bool {
	return outsideRepairSelectorPattern.MatchString(selector)
}

func outsideRepairGitSHAValid(value string) bool {
	return outsideRepairSHAPattern.MatchString(value)
}

func outsideRepairCanonicalPullRequestURL(repository string, number int64) string {
	return "https://github.com/" + repository + "/pull/" + strconv.FormatInt(number, 10)
}

func outsideRepairCanonicalReleaseURL(repository, tag string) string {
	return "https://github.com/" + repository + "/releases/tag/" + tag
}

// DefaultExternalEvidenceCommand runs the GitHub CLI by exact argv with
// a bounded wait; never a shell command string. Production installs it as the
// Service.ExternalEvidenceCommand; tests install a deterministic fake.
func DefaultExternalEvidenceCommand(ctx context.Context, command string, args ...string) (string, error) {
	if command != "gh" {
		return "", fmt.Errorf("external evidence requires the GitHub CLI")
	}
	run, cancel := context.WithTimeout(ctx, boundedExternalEvidenceWait)
	defer cancel()
	process := exec.CommandContext(run, "gh", args...) //nolint:gosec // gh is fixed; the evidence collector supplies read-only argv, and values stay separate without a shell.
	var stdout, stderr bytes.Buffer
	process.Stdout, process.Stderr = &stdout, &stderr
	runErr := process.Run()
	if runErr == nil && run.Err() == context.DeadlineExceeded {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("external command %s %s exceeded the bounded wait", command, strings.Join(args, " "))
	}
	if runErr != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("external command %s %s failed: %v: %s", command, strings.Join(args, " "), runErr, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// outsideRepairCommand runs one external command through the injected seam.
func (r runtime) outsideRepairCommand(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("external command carries no argv")
	}
	if r.Authority == nil {
		return "", fmt.Errorf("the external evidence boundary is not installed")
	}
	if r.Authority.ExternalEvidenceCommand == nil {
		return "", fmt.Errorf("external evidence command is not installed")
	}
	return r.Authority.ExternalEvidenceCommand(ctx, args[0], args[1:]...)
}

type outsideRepairDispositionInput struct {
	WorkID          string         `json:"work_id"`
	ExpectedVersion int64          `json:"expected_version"`
	Reason          string         `json:"reason"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Approval        *approvalInput `json:"approval"`
}

type outsideRepairReconcileInput struct {
	WorkID          string         `json:"work_id"`
	ExpectedVersion int64          `json:"expected_version"`
	Reason          string         `json:"reason"`
	Mode            string         `json:"mode"`
	ReleaseTag      string         `json:"release_tag"`
	PullRequests    []int64        `json:"pull_requests"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Approval        *approvalInput `json:"approval"`
}

type ghPullRequestView struct {
	Number      int64  `json:"number"`
	State       string `json:"state"`
	HeadRefOid  string `json:"headRefOid"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	MergedAt string `json:"mergedAt"`
	URL      string `json:"url"`
	Body     string `json:"body"`
}

type ghOutsideProtection struct {
	RequiresStatusChecks        *bool    `json:"requiresStatusChecks"`
	RequiredStatusCheckContexts []string `json:"requiredStatusCheckContexts"`
	RequiredStatusChecks        []struct {
		Context string `json:"context"`
		App     *struct {
			DatabaseID int64 `json:"databaseId"`
		} `json:"app"`
	} `json:"requiredStatusChecks"`
	// Classic protection expresses deployment enforcement as a boolean plus
	// its environment list (the GraphQL BranchProtectionRule fields), not as
	// a ruleset required_deployments rule. CD-0210 D2 counts it as effective
	// enforcement, so the collector must read it and fail closed.
	RequiresDeployments            *bool    `json:"requiresDeployments"`
	RequiredDeploymentEnvironments []string `json:"requiredDeploymentEnvironments"`
}

type ghOutsideRulesResponse struct {
	Errors []json.RawMessage `json:"errors"`
	Data   struct {
		Repository *struct {
			DatabaseID    int64  `json:"databaseId"`
			NameWithOwner string `json:"nameWithOwner"`
			URL           string `json:"url"`
			PullRequest   *struct {
				Number      int64  `json:"number"`
				HeadRefOid  string `json:"headRefOid"`
				BaseRefName string `json:"baseRefName"`
				BaseRef     *struct {
					Name                 string          `json:"name"`
					BranchProtectionRule json.RawMessage `json:"branchProtectionRule"`
				} `json:"baseRef"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

type ghOutsideRepoView struct {
	NameWithOwner string `json:"nameWithOwner"`
	URL           string `json:"url"`
}

type ghOutsideBranchRule struct {
	Type       string `json:"type"`
	Parameters *struct {
		RequiredStatusChecks []struct {
			Context       string `json:"context"`
			IntegrationID int64  `json:"integration_id"`
		} `json:"required_status_checks"`
	} `json:"parameters"`
}

type ghOutsideCheckRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	DetailsURL string    `json:"details_url"`
	HeadSHA    string    `json:"head_sha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	StartedAt  time.Time `json:"started_at"`
	App        struct {
		ID int64 `json:"id"`
	} `json:"app"`
}

type ghOutsideWorkflowRun struct {
	ID         int64  `json:"id"`
	HTMLURL    string `json:"html_url"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

type ghOutsideJob struct {
	ID          int64  `json:"id"`
	RunID       int64  `json:"run_id"`
	Name        string `json:"name"`
	HeadSHA     string `json:"head_sha"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	HTMLURL     string `json:"html_url"`
	CheckRunURL string `json:"check_run_url"`
}

type ghReleaseView struct {
	TagName      string `json:"tagName"`
	IsDraft      bool   `json:"isDraft"`
	IsPrerelease bool   `json:"isPrerelease"`
	URL          string `json:"url"`
	PublishedAt  string `json:"publishedAt"`
}

type ghRefObject struct {
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

type ghCompareView struct {
	Status   string `json:"status"`
	BehindBy int64  `json:"behind_by"`
}

type publishedRelease struct {
	ResolvedCommit string
	Published      time.Time
}

// outsideRepairLiveWork reads the work item's lifecycle for the hold through
// the store's own lightweight read. It deliberately avoids WorkPin: a work
// item with no workflow instance (imported work) carries no pin row, yet the
// hold must stay recordable on imported work exactly as the store fold admits
// it (needed or in_progress).
func (r runtime) outsideRepairLiveWork(ctx context.Context, base Envelope, workID string) Envelope {
	lifecycle, err := r.Store.WorkLifecycle(ctx, workID)
	if err != nil {
		var failure *store.Failure
		if errors.As(err, &failure) && failure.Kind == store.KindProjectionNotFound {
			return coreError(base, "invalid_input", "outside repair selects a live work item", "reread_entities", false)
		}
		return failureEnvelope(base, err)
	}
	if lifecycle != "needed" && lifecycle != "in_progress" {
		// The store fold repeats this bound; the challenge is never offered
		// on closed work.
		return coreError(base, "invalid_transition", "outside repair requires needed or in_progress work", "reread_entities", false)
	}
	return Envelope{}
}

// outsideRepairHoldWorkPin reads the work pin the reconcile binds against. A
// hold makes the pin readable even on instance-less work (the store's pin
// short-circuit), so a projection-not-found refusal here names the missing
// hold, and every other failure is a genuine read refusal.
func (r runtime) outsideRepairHoldWorkPin(ctx context.Context, base Envelope, workID string) (store.WorkPin, Envelope) {
	pin, err := store.ReadWorkPin(ctx, r.Store, workID)
	if err != nil {
		var failure *store.Failure
		if errors.As(err, &failure) && failure.Kind == store.KindProjectionNotFound {
			return store.WorkPin{}, coreError(base, "invalid_input", "outside repair reconcile requires an active outside-repair hold", "reread_entities", false)
		}
		return store.WorkPin{}, failureEnvelope(base, err)
	}
	if pin.Lifecycle != "needed" && pin.Lifecycle != "in_progress" {
		return store.WorkPin{}, coreError(base, "invalid_transition", "outside repair requires needed or in_progress work", "reread_entities", false)
	}
	return pin, Envelope{}
}

// outsideRepairRepositoryForProject resolves the Project's recorded GitHub
// git_remote locator. The caller never supplies the repository, and every
// forge URL the receipt names is constructed from the resolved owner/repo.
func (r runtime) outsideRepairRepositoryForProject(ctx context.Context, base Envelope, projectID string) (string, Envelope) {
	locators, err := r.Store.ProjectLocators(ctx, projectID)
	if err != nil {
		return "", failureEnvelope(base, err)
	}
	for _, locator := range locators {
		if locator.Kind != store.LocatorGitRemote {
			continue
		}
		repository, ok := outsideRepairGitHubRepository(locator.NormalizedValue)
		if ok {
			return repository, Envelope{}
		}
	}
	return "", coreError(base, "invalid_input", "outside repair closes a Project whose registered git origin is not a GitHub repository", "reread_entities", false)
}

func outsideRepairGitHubRepository(normalizedRemote string) (string, bool) {
	parsed, err := url.Parse(normalizedRemote)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(parsed.Host)
	path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	if host == "github.com" {
		if !outsideRepairRepositoryPattern.MatchString(path) {
			return "", false
		}
		return path, true
	}
	// The recorded locator is normalized by the store, which turns any
	// scheme-carrying remote into an ssh form whose host carries the original
	// scheme: an https://github.com origin reads `ssh://https/github.com/...`
	// and an ssh://git@github.com origin reads `ssh://git@github.com/...` for
	// a greater user prefix. Read the scheme-carrying host shape
	// deterministically: the GitHub host follows the scheme marker, so the
	// remaining path is the owner/repo.
	if host == "https" || host == "http" {
		trimmed, trimmedOK := strings.CutPrefix(path, "github.com/")
		if trimmedOK && outsideRepairRepositoryPattern.MatchString(trimmed) {
			return trimmed, true
		}
	}
	return "", false
}

// outsideRepairEvidenceForPlan collects the completion proof outside any
// write transaction: it fetches the merged pull requests and their complete
// successful required-check sets, the published release and its tag-resolved
// immutable commit, and proves every merge is an ancestor of that release
// commit. A zero Envelope return names an ok collection.
func (r runtime) outsideRepairEvidenceForPlan(ctx context.Context, base Envelope, pin store.WorkPin, in outsideRepairReconcileInput) (store.OutsideRepairEvidence, Envelope) {
	zero := store.OutsideRepairEvidence{}
	authorityRef := "approval:" + in.Approval.ApprovalRef
	normalized, repositoryRefusal := r.outsideRepairRepositoryForProject(ctx, base, pin.ProjectID)
	if repositoryRefusal.Error != nil {
		return zero, repositoryRefusal
	}
	canonical, canonicalRefusal := r.outsideRepairCanonicalRepository(ctx, base, normalized)
	if canonicalRefusal.Error != nil {
		return zero, canonicalRefusal
	}
	sorted := make([]int64, len(in.PullRequests))
	copy(sorted, in.PullRequests)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	evidencePullRequests := make([]store.OutsideRepairPullRequestEvidence, 0, len(sorted))
	for _, number := range sorted {
		selected, selectedRefusal := r.selectedOutsideRepairPullRequest(ctx, base, canonical, number, pin.LinearIssueKey)
		if selectedRefusal.Error != nil {
			return zero, selectedRefusal
		}
		evidencePullRequests = append(evidencePullRequests, selected)
	}
	release, releaseRefusal := r.outsideRepairPublishedRelease(ctx, base, canonical, in.ReleaseTag)
	if releaseRefusal.Error != nil {
		return zero, releaseRefusal
	}
	for _, row := range evidencePullRequests {
		ancestorRefusal := r.outsideRepairMergeAncestor(ctx, base, canonical, row.MergeSHA, release.ResolvedCommit, strconv.FormatInt(row.Number, 10))
		if ancestorRefusal.Error != nil {
			return zero, ancestorRefusal
		}
		if row.MergedAt.After(release.Published) {
			return zero, coreError(base, "missing_evidence", "merged pull request "+strconv.FormatInt(row.Number, 10)+" joined after the published release", "provide_evidence", false)
		}
	}
	return store.OutsideRepairEvidence{
		AuthorityRef: authorityRef,
		ObservedAt:   r.Authority.now(),
		Repository:   canonical,
		PullRequests: evidencePullRequests,
		ReleaseTag:   in.ReleaseTag,
		ReleaseURL:   outsideRepairCanonicalReleaseURL(canonical, in.ReleaseTag),
		ReleaseSHA:   release.ResolvedCommit,
		PublishedAt:  release.Published,
	}, Envelope{}
}

// outsideRepairCanonicalRepository resolves the GitHub canonical identity for
// the normalized store-registered repository. The store records a lowercased
// owner/name from URL parsing, but GitHub returns mixed-case identifiers on
// every forge URL; the receipt must keep that casing. The command runs before
// any pull-request or release fetch, so every URL it returns downstream can be
// matched exactly against the GitHub-authenticated identifiers, and the store's
// URL exact-match validation can rely on it.
func (r runtime) outsideRepairCanonicalRepository(ctx context.Context, base Envelope, normalized string) (string, Envelope) {
	zero := ""
	output, err := r.outsideRepairCommand(ctx, "gh", "repo", "view", normalized, "--json", "nameWithOwner,url")
	if err != nil {
		return zero, coreError(base, "missing_evidence", "canonical repository "+normalized+": "+err.Error(), "provide_evidence", false)
	}
	var view ghOutsideRepoView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return zero, coreError(base, "missing_evidence", "canonical repository "+normalized+": gh answered unparseable output", "provide_evidence", false)
	}
	if !strings.EqualFold(view.NameWithOwner, normalized) || !strings.EqualFold(view.URL, "https://github.com/"+normalized) {
		return zero, coreError(base, "unauthorized", "canonical repository "+normalized+" carries another repository's identifier or URL", "contact_operator", false)
	}
	return view.NameWithOwner, Envelope{}
}

func (r runtime) selectedOutsideRepairPullRequest(ctx context.Context, base Envelope, repository string, number int64, issueKey string) (store.OutsideRepairPullRequestEvidence, Envelope) {
	zero := store.OutsideRepairPullRequestEvidence{}
	reference := strconv.FormatInt(number, 10)
	wantURL := outsideRepairCanonicalPullRequestURL(repository, number)
	output, err := r.outsideRepairCommand(ctx, "gh", "pr", "view", reference, "-R", repository, "--json", "number,state,headRefOid,mergeCommit,mergedAt,url,body")
	if err != nil {
		return zero, coreError(base, "missing_evidence", "pull request "+wantURL+": "+err.Error(), "provide_evidence", false)
	}
	var view ghPullRequestView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return zero, coreError(base, "missing_evidence", "pull request "+wantURL+": gh answered unparseable output", "provide_evidence", false)
	}
	if view.Number != number || view.State != "MERGED" || view.MergeCommit == nil || view.MergeCommit.Oid == "" || !outsideRepairGitSHAValid(view.HeadRefOid) || !outsideRepairGitSHAValid(view.MergeCommit.Oid) {
		return zero, coreError(base, "missing_evidence", "pull request "+wantURL+" is not merged", "provide_evidence", false)
	}
	if !strings.EqualFold(view.URL, wantURL) {
		return zero, coreError(base, "unauthorized", "pull request "+wantURL+" carries another repository's identifier or URL", "contact_operator", false)
	}
	mergedAt, parseErr := time.Parse(time.RFC3339, view.MergedAt)
	if parseErr != nil {
		return zero, coreError(base, "missing_evidence", "pull request "+wantURL+" carries no parseable merge time", "provide_evidence", false)
	}
	linked := issueKey == ""
	for _, token := range outsideRepairLinearTokenPattern.FindAllString(strings.ToUpper(view.Body), -1) {
		linked = linked || token == issueKey
	}
	if !linked {
		return zero, coreError(base, "missing_evidence", "pull request "+wantURL+" does not name the work's Linear ref "+issueKey, "provide_evidence", false)
	}
	checks, err := r.outsideRepairRequiredChecks(ctx, repository, number, view.HeadRefOid)
	if err != nil {
		return zero, coreError(base, "missing_evidence", "pull request "+wantURL+" required checks: "+err.Error(), "provide_evidence", false)
	}
	return store.OutsideRepairPullRequestEvidence{
		URL:            view.URL,
		Number:         number,
		HeadSHA:        view.HeadRefOid,
		MergeSHA:       view.MergeCommit.Oid,
		MergedAt:       mergedAt,
		RequiredChecks: checks,
	}, Envelope{}
}

func (r runtime) outsideRepairReadJSON(ctx context.Context, target any, args ...string) error {
	output, err := r.outsideRepairCommand(ctx, append([]string{"gh", "api"}, args...)...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(output), target); err != nil {
		return fmt.Errorf("gh api %s answered unparseable output: %w", args[0], err)
	}
	return nil
}

// The PR's effective classic protection and active repository/organization
// rulesets own the required names. A null classic rule is an explicit absence;
// a failed read of either source is not evidence of absence.
func (r runtime) outsideRepairRequiredNames(ctx context.Context, repository string, number int64, headSHA string) (map[string]int64, int64, error) {
	owner, name, _ := strings.Cut(repository, "/")
	const query = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){databaseId nameWithOwner url pullRequest(number:$number){number headRefOid baseRefName baseRef{name branchProtectionRule{requiresStatusChecks requiredStatusCheckContexts requiredStatusChecks{context app{databaseId}} requiresDeployments requiredDeploymentEnvironments}}}}}`
	var response ghOutsideRulesResponse
	if err := r.outsideRepairReadJSON(ctx, &response, "graphql", "-f", "query="+query, "-f", "owner="+owner, "-f", "name="+name, "-F", "number="+strconv.FormatInt(number, 10)); err != nil {
		return nil, 0, err
	}
	repo := response.Data.Repository
	if len(response.Errors) != 0 || repo == nil || repo.DatabaseID <= 0 || !strings.EqualFold(repo.NameWithOwner, repository) || !strings.EqualFold(repo.URL, "https://github.com/"+repository) || repo.PullRequest == nil {
		return nil, 0, fmt.Errorf("cannot authenticate repository branch protection")
	}
	pr := repo.PullRequest
	if pr.Number != number || pr.HeadRefOid != headSHA || pr.BaseRef == nil || pr.BaseRefName == "" || pr.BaseRef.Name != pr.BaseRefName {
		return nil, 0, fmt.Errorf("cannot authenticate pull request head and base branch")
	}
	required := map[string]int64{}
	add := func(context string, appID int64) error {
		if context == "" || len([]rune(context)) > 128 || appID < 0 {
			return fmt.Errorf("required status check has invalid context or app identity")
		}
		old, exists := required[context]
		if exists && old != 0 && appID != 0 && old != appID {
			return fmt.Errorf("required status check %s has conflicting app identities", context)
		}
		if !exists || appID != 0 {
			required[context] = appID
		}
		return nil
	}
	protection := pr.BaseRef.BranchProtectionRule
	if len(protection) == 0 {
		return nil, 0, fmt.Errorf("missing classic branch protection response")
	}
	if string(protection) != "null" {
		var rule ghOutsideProtection
		if err := json.Unmarshal(protection, &rule); err != nil {
			return nil, 0, fmt.Errorf("unparseable classic branch protection: %w", err)
		}
		if rule.RequiresStatusChecks == nil || rule.RequiredStatusCheckContexts == nil || rule.RequiredStatusChecks == nil || rule.RequiresDeployments == nil || rule.RequiredDeploymentEnvironments == nil {
			return nil, 0, fmt.Errorf("incomplete classic branch protection")
		}
		if *rule.RequiresDeployments || len(rule.RequiredDeploymentEnvironments) > 0 {
			// Same named follow-up and same fail-closed refusal as the
			// ruleset required_deployments rule: the collector cannot
			// certify deployment enforcement without its native receipts.
			return nil, 0, fmt.Errorf("unsupported classic deployment enforcement (requiresDeployments): required-deployment receipts not yet collected")
		}
		if *rule.RequiresStatusChecks {
			for _, context := range rule.RequiredStatusCheckContexts {
				if err := add(context, 0); err != nil {
					return nil, 0, err
				}
			}
			for _, check := range rule.RequiredStatusChecks {
				appID := int64(0)
				if check.App != nil {
					appID = check.App.DatabaseID
					if appID <= 0 {
						return nil, 0, fmt.Errorf("invalid required app identity")
					}
				}
				if err := add(check.Context, appID); err != nil {
					return nil, 0, err
				}
			}
			if len(required) == 0 {
				return nil, 0, fmt.Errorf("classic protection requires checks but names none")
			}
		}
	}
	var pages [][]ghOutsideBranchRule
	if err := r.outsideRepairReadJSON(ctx, &pages, "repos/"+repository+"/rules/branches/"+url.PathEscape(pr.BaseRefName)+"?per_page=100", "--paginate", "--slurp"); err != nil {
		return nil, 0, err
	}
	if len(pages) == 0 {
		return nil, 0, fmt.Errorf("no effective rules response")
	}
	for _, page := range pages {
		if page == nil {
			return nil, 0, fmt.Errorf("incomplete effective rules response")
		}
		for _, rule := range page {
			if rule.Type == "" {
				return nil, 0, fmt.Errorf("effective rule has no type")
			}
			// Scope: only required_status_checks is proven here; `workflows` and `required_deployments` fail closed because the collector does not yet build authenticated required-workflow receipts (named follow-up: support authenticated required-workflow receipts).
			if rule.Type != "required_status_checks" {
				if rule.Type == "workflows" || rule.Type == "required_deployments" {
					return nil, 0, fmt.Errorf("unsupported enforcement evidence rule %s: required-workflow receipts not yet collected", rule.Type)
				}
				continue
			}
			if rule.Parameters == nil || len(rule.Parameters.RequiredStatusChecks) == 0 {
				return nil, 0, fmt.Errorf("incomplete effective required status checks")
			}
			for _, check := range rule.Parameters.RequiredStatusChecks {
				if err := add(check.Context, check.IntegrationID); err != nil {
					return nil, 0, err
				}
			}
		}
	}
	if len(required) == 0 || len(required) > 64 {
		return nil, 0, fmt.Errorf("outside repair requires 1..64 authenticated required names")
	}
	return required, repo.DatabaseID, nil
}

func (r runtime) outsideRepairRequiredChecks(ctx context.Context, repository string, number int64, headSHA string) ([]store.OutsideRepairRequiredCheck, error) {
	required, repositoryID, err := r.outsideRepairRequiredNames(ctx, repository, number, headSHA)
	if err != nil {
		return nil, err
	}
	var pages []struct {
		TotalCount *int                `json:"total_count"`
		CheckRuns  []ghOutsideCheckRun `json:"check_runs"`
	}
	if err := r.outsideRepairReadJSON(ctx, &pages, "repos/"+repository+"/commits/"+headSHA+"/check-runs?filter=all&per_page=100", "--paginate", "--slurp"); err != nil {
		return nil, err
	}
	latest := map[string]ghOutsideCheckRun{}
	seen := map[int64]bool{}
	count := 0
	for _, page := range pages {
		if page.TotalCount == nil || page.CheckRuns == nil {
			return nil, fmt.Errorf("incomplete check run page")
		}
		count += len(page.CheckRuns)
		for _, check := range page.CheckRuns {
			if check.ID <= 0 || seen[check.ID] {
				return nil, fmt.Errorf("invalid or repeated check run identity")
			}
			seen[check.ID] = true
			appID, needed := required[check.Name]
			if !needed || (appID != 0 && check.App.ID != appID) {
				continue
			}
			if check.HeadSHA != headSHA {
				return nil, fmt.Errorf("check %s ran on another SHA", check.Name)
			}
			previous, exists := latest[check.Name]
			if exists && (previous.StartedAt.IsZero() || check.StartedAt.IsZero()) {
				return nil, fmt.Errorf("cannot order duplicate check %s without start times", check.Name)
			}
			if !exists || check.StartedAt.After(previous.StartedAt) || (check.StartedAt.Equal(previous.StartedAt) && check.ID > previous.ID) {
				latest[check.Name] = check
			}
		}
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no check run pages")
	}
	for _, page := range pages {
		if *page.TotalCount != count {
			return nil, fmt.Errorf("check run pagination is incomplete or changed during collection")
		}
	}
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	checks := make([]store.OutsideRepairRequiredCheck, 0, len(names))
	for _, name := range names {
		check, exists := latest[name]
		if !exists || check.Status != "completed" || check.Conclusion != "success" {
			return nil, fmt.Errorf("required check %s is absent or did not pass", name)
		}
		proof, err := r.outsideRepairRunProof(ctx, repository, repositoryID, headSHA, check)
		if err != nil {
			return nil, fmt.Errorf("required check %s: %w", name, err)
		}
		checks = append(checks, proof)
	}
	return checks, nil
}

func (r runtime) outsideRepairRunProof(ctx context.Context, repository string, repositoryID int64, headSHA string, check ghOutsideCheckRun) (store.OutsideRepairRequiredCheck, error) {
	zero := store.OutsideRepairRequiredCheck{}
	u, err := url.Parse(check.DetailsURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return zero, fmt.Errorf("invalid Actions URL")
	}
	prefix := "/" + repository + "/actions/runs/"
	if len(u.Path) < len(prefix) || !strings.EqualFold(u.Path[:len(prefix)], prefix) {
		return zero, fmt.Errorf("actions URL names another repository")
	}
	path := u.Path[len(prefix):]
	parts := strings.Split(path, "/")
	if len(parts) != 1 && (len(parts) != 3 || parts[1] != "job") {
		return zero, fmt.Errorf("unsupported Actions URL")
	}
	parseID := func(value string) (int64, error) {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
			return 0, fmt.Errorf("invalid Actions identity")
		}
		return id, nil
	}
	runID, err := parseID(parts[0])
	if err != nil {
		return zero, err
	}
	jobID := int64(0)
	if len(parts) == 3 {
		jobID, err = parseID(parts[2])
		if err != nil {
			return zero, err
		}
	}
	var run ghOutsideWorkflowRun
	if err := r.outsideRepairReadJSON(ctx, &run, "repos/"+repository+"/actions/runs/"+parts[0]); err != nil {
		return zero, err
	}
	wantURL := "https://github.com/" + repository + "/actions/runs/" + parts[0]
	if run.ID != runID || !strings.EqualFold(run.HTMLURL, wantURL) || run.HeadSHA != headSHA || run.Status != "completed" || run.Conclusion != "success" || run.Repository.ID != repositoryID || !strings.EqualFold(run.Repository.FullName, repository) || !strings.EqualFold(run.Repository.HTMLURL, "https://github.com/"+repository) {
		return zero, fmt.Errorf("run does not prove success on the immutable PR head in the recorded repository")
	}
	var pages []struct {
		TotalCount *int           `json:"total_count"`
		Jobs       []ghOutsideJob `json:"jobs"`
	}
	if err := r.outsideRepairReadJSON(ctx, &pages, "repos/"+repository+"/actions/runs/"+parts[0]+"/jobs?filter=latest&per_page=100", "--paginate", "--slurp"); err != nil {
		return zero, err
	}
	count, matches := 0, 0
	seen := map[int64]bool{}
	matchedJobID := int64(0)
	for _, page := range pages {
		if page.TotalCount == nil || page.Jobs == nil {
			return zero, fmt.Errorf("incomplete Actions job page")
		}
		count += len(page.Jobs)
		for _, job := range page.Jobs {
			if job.ID <= 0 || seen[job.ID] {
				return zero, fmt.Errorf("invalid or repeated job identity")
			}
			seen[job.ID] = true
			if !strings.EqualFold(job.CheckRunURL, "https://api.github.com/repos/"+repository+"/check-runs/"+strconv.FormatInt(check.ID, 10)) {
				continue
			}
			if job.RunID != run.ID || job.HeadSHA != run.HeadSHA || job.Name != check.Name || job.Status != "completed" || job.Conclusion != "success" || (jobID != 0 && job.ID != jobID) || !strings.EqualFold(job.HTMLURL, wantURL+"/job/"+strconv.FormatInt(job.ID, 10)) {
				return zero, fmt.Errorf("job does not bind the required name and check to the fetched run")
			}
			matches++
			matchedJobID = job.ID
		}
	}
	if len(pages) == 0 || matches != 1 {
		return zero, fmt.Errorf("no unique current job proves the required check")
	}
	for _, page := range pages {
		if *page.TotalCount != count {
			return zero, fmt.Errorf("job pagination is incomplete or changed during collection")
		}
	}
	// The receipt keeps the exact native identities it authenticated (CD-0210
	// D2): the check-run, its workflow run, and the one current job that
	// proved it. Reruns mint new identities, so a recorded receipt cannot be
	// rewritten by a later attempt.
	return store.OutsideRepairRequiredCheck{Name: check.Name, URL: run.HTMLURL, CommitSHA: run.HeadSHA, Conclusion: run.Conclusion, CheckRunID: check.ID, RunID: run.ID, JobID: matchedJobID}, nil
}

func (r runtime) outsideRepairPublishedRelease(ctx context.Context, base Envelope, repository, tag string) (publishedRelease, Envelope) {
	zero := publishedRelease{}
	output, err := r.outsideRepairCommand(ctx, "gh", "release", "view", tag, "-R", repository, "--json", "tagName,isDraft,isPrerelease,url,publishedAt")
	if err != nil {
		return zero, coreError(base, "missing_evidence", "published release "+tag+": "+err.Error(), "provide_evidence", false)
	}
	var view ghReleaseView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return zero, coreError(base, "missing_evidence", "published release "+tag+": gh answered unparseable output", "provide_evidence", false)
	}
	if view.IsDraft || view.IsPrerelease || view.TagName != tag {
		return zero, coreError(base, "missing_evidence", "outside repair closes only a non-draft, non-prerelease published release", "provide_evidence", false)
	}
	published, parseErr := time.Parse(time.RFC3339, view.PublishedAt)
	if parseErr != nil {
		return zero, coreError(base, "missing_evidence", "published release "+tag+" carries no parseable published time", "provide_evidence", false)
	}
	if view.URL != outsideRepairCanonicalReleaseURL(repository, tag) {
		return zero, coreError(base, "unauthorized", "published release "+tag+" carries another repository's URL", "contact_operator", false)
	}
	// targetCommitish may name a branch, so the immutable commit resolves
	// through the tag ref, never through that field.
	commit, commitRefusal := r.outsideRepairTagCommit(ctx, base, repository, tag)
	if commitRefusal.Error != nil {
		return zero, commitRefusal
	}
	return publishedRelease{ResolvedCommit: commit, Published: published}, Envelope{}
}

func (r runtime) outsideRepairTagCommit(ctx context.Context, base Envelope, repository, tag string) (string, Envelope) {
	output, err := r.outsideRepairCommand(ctx, "gh", "api", "repos/"+repository+"/git/ref/tags/"+tag)
	if err != nil {
		return "", coreError(base, "missing_evidence", "published release "+tag+": "+err.Error(), "provide_evidence", false)
	}
	var ref ghRefObject
	if err := json.Unmarshal([]byte(output), &ref); err != nil {
		return "", coreError(base, "missing_evidence", "published release "+tag+": gh answered unparseable output", "provide_evidence", false)
	}
	if !outsideRepairGitSHAValid(ref.Object.SHA) {
		return "", coreError(base, "missing_evidence", "published release "+tag+" carries no valid tag object SHA", "provide_evidence", false)
	}
	commit := ref.Object.SHA
	if ref.Object.Type == "tag" {
		// An annotated tag names its tag object, so deref it for the commit.
		output, err = r.outsideRepairCommand(ctx, "gh", "api", "repos/"+repository+"/git/tags/"+commit)
		if err != nil {
			return "", coreError(base, "missing_evidence", "published release "+tag+": "+err.Error(), "provide_evidence", false)
		}
		var object ghRefObject
		if err := json.Unmarshal([]byte(output), &object); err != nil {
			return "", coreError(base, "missing_evidence", "published release "+tag+": gh answered unparseable output", "provide_evidence", false)
		}
		ref = object
		commit = ref.Object.SHA
	}
	if ref.Object.Type != "commit" || !outsideRepairGitSHAValid(commit) {
		return "", coreError(base, "missing_evidence", "published release "+tag+" resolves to no native git commit", "provide_evidence", false)
	}
	return commit, Envelope{}
}

// outsideRepairMergeAncestor proves the pull request merge commit is an
// ancestor of the published release commit through the authenticated compare
// endpoint: base...head reads "ahead" exactly when base is an ancestor of
// head, "identical" when both commits are the same, and a "diverged" or
// "behind" status names a merge the release does not contain.
func (r runtime) outsideRepairMergeAncestor(ctx context.Context, base Envelope, repository, mergeSHA, releaseCommit, pullRequest string) Envelope {
	output, err := r.outsideRepairCommand(ctx, "gh", "api", "repos/"+repository+"/compare/"+mergeSHA+"..."+releaseCommit)
	if err != nil {
		return coreError(base, "missing_evidence", "pull request "+pullRequest+" merge comparison: "+err.Error(), "provide_evidence", false)
	}
	var view ghCompareView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return coreError(base, "missing_evidence", "pull request "+pullRequest+" merge comparison: gh answered unparseable output", "provide_evidence", false)
	}
	if (view.Status == "ahead" || view.Status == "identical") && view.BehindBy == 0 {
		return Envelope{}
	}
	return coreError(base, "missing_evidence", "published release "+releaseCommit+" does not contain pull request "+pullRequest, "provide_evidence", false)
}

// planOutsideRepairDisposition plans the hold: durably record the
// operator-approved outside-repair disposition on live work. No workflow pin
// and no managed step is required to enter the route.
func (r runtime) planOutsideRepairDisposition(ctx context.Context, base Envelope, raw []byte, digest string, _ Authority, _ ContractOperation, plan *mutationPlan) (Envelope, error, bool) {
	var in outsideRepairDispositionInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	if liveWorkRefusal := r.outsideRepairLiveWork(ctx, base, in.WorkID); liveWorkRefusal.Error != nil {
		return liveWorkRefusal, nil, true
	}
	if refusal, handled := r.prepareOutsideRepairPlan(base, plan, in.WorkID, in.ExpectedVersion, in.Reason, in.Approval, "", "", nil); handled {
		return refusal, nil, true
	}
	plan.effect = r.outsideRepairPlanEffect(digest, plan, in.WorkID, in.ExpectedVersion, in.Reason, "", nil)
	return Envelope{}, nil, false
}

// planOutsideRepairReconcile plans the one reconcile action: completed mode
// closes the held work from collected evidence, resume mode clears the hold.
func (r runtime) planOutsideRepairReconcile(ctx context.Context, base Envelope, raw []byte, digest string, _ Authority, _ ContractOperation, plan *mutationPlan) (Envelope, error, bool) {
	var in outsideRepairReconcileInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	switch in.Mode {
	case OutsideRepairModeCompleted:
		if in.ReleaseTag == "" || len(in.PullRequests) == 0 {
			return coreError(base, "invalid_input", "a completed reconcile names its published release tag and merged pull requests", "reread_entities", false), nil, true
		}
		if !outsideRepairReleaseTagBound(in.ReleaseTag) || !outsideRepairPullRequestsBound(in.PullRequests) {
			return coreError(base, "invalid_input", "release_tag accepts 1..128 characters of [A-Za-z0-9._-] and pull_requests accepts 1..32 unique positive numbers", "reread_entities", false), nil, true
		}
	case OutsideRepairModeResume:
	default:
		return coreError(base, "invalid_input", "outside repair reconcile accepts mode completed or resume", "reread_entities", false), nil, true
	}
	pin, pinRefusal := r.outsideRepairHoldWorkPin(ctx, base, in.WorkID)
	if pinRefusal.Error != nil {
		return pinRefusal, nil, true
	}
	// The completion proof is fetched once, before any transaction opens,
	// and only when an approval reference is present: the challenge path
	// never contacts the forge.
	var evidence *store.OutsideRepairEvidence
	if in.Mode == OutsideRepairModeCompleted && in.Approval != nil {
		collected, collectRefusal := r.outsideRepairEvidenceForPlan(ctx, base, pin, in)
		if collectRefusal.Error != nil {
			return collectRefusal, nil, true
		}
		evidence = &collected
	}
	if refusal, handled := r.prepareOutsideRepairPlan(base, plan, in.WorkID, in.ExpectedVersion, in.Reason, in.Approval, in.Mode, in.ReleaseTag, in.PullRequests); handled {
		return refusal, nil, true
	}
	plan.outsideEvidence = evidence
	plan.effect = r.outsideRepairPlanEffect(digest, plan, in.WorkID, in.ExpectedVersion, in.Reason, in.Mode, evidence)
	return Envelope{}, nil, false
}

// prepareOutsideRepairPlan carries the shared outside-repair plan shape. The
// operator approval binds the exact work, expected version, and — on a
// completed reconcile — the bounded release selectors, so approval consumption
// authorizes one consequence and never another result.
func (r runtime) prepareOutsideRepairPlan(base Envelope, plan *mutationPlan, workID string, expectedVersion int64, reason string, approval *approvalInput, mode, releaseTag string, pullRequests []int64) (Envelope, bool) {
	if len([]rune(reason)) < 2 || len([]rune(reason)) > 4096 {
		return coreError(base, "invalid_input", "outside repair requires a bounded repair reason", "reread_entities", false), true
	}
	if approval != nil {
		plan.approval = approval.ApprovalRef
	}
	plan.requiresApproval = true
	plan.versions["work"] = expectedVersion
	plan.scope["work_ids"] = []string{workID}
	if mode == OutsideRepairModeCompleted {
		plan.scope["release_tag"] = releaseTag
		selectors := make([]string, 0, len(pullRequests))
		for _, number := range pullRequests {
			selectors = append(selectors, strconv.FormatInt(number, 10))
		}
		plan.scope["pull_requests"] = selectors
	}
	return Envelope{}, false
}

// outsideRepairPlanEffect composes the typed effect: executeMutation consumed
// the one-use operator approval within the same transaction, and the store's
// approval-consumption route re-verifies the binding while replaying.
func (r runtime) outsideRepairPlanEffect(digest string, plan *mutationPlan, workID string, expectedVersion int64, reason, mode string, evidence *store.OutsideRepairEvidence) mutationEffect {
	return func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []ChangedRef, error) {
		_ = grant
		consumedApprovalRef, _ := plan.scope["approval_ref"].(string)
		approvalScopeJSON, _ := json.Marshal(boundedApprovalScope(plan.scope))
		approvalVersionsJSON, _ := json.Marshal(plan.versions)
		request := store.OutsideRepairRequest{
			OutsideRepairApproval: store.OutsideRepairApproval{
				ApprovalRef:             consumedApprovalRef,
				ApprovalOperationDigest: digest,
				ApprovalScopeJSON:       string(approvalScopeJSON),
				ApprovalVersionsJSON:    string(approvalVersionsJSON),
				ApprovalConsequence:     plan.consequence,
			},
			WorkID:          workID,
			Reason:          reason,
			EventID:         digest + ":outside-repair",
			ExpectedVersion: expectedVersion,
			OccurredAt:      r.Authority.now(),
		}
		var result store.ApplyOperationResult
		var err error
		switch mode {
		case OutsideRepairModeCompleted:
			if evidence == nil {
				return nil, nil, nil, fmt.Errorf("completed reconcile collected no evidence")
			}
			result, err = store.ReconcileOutsideRepairTx(ctx, tx, store.OutsideRepairReconcileRequest{OutsideRepairRequest: request, Evidence: *evidence})
		case OutsideRepairModeResume:
			result, err = store.ResumeOutsideRepairTx(ctx, tx, request)
		default:
			result, err = store.SetOutsideRepairDispositionTx(ctx, tx, request)
		}
		if err != nil {
			return nil, nil, nil, err
		}
		changed := []ChangedRef{{EntityKind: "work_item", ID: workID, Version: strconv.FormatInt(expectedVersion+2, 10)}}
		var intents []NextIntent
		switch mode {
		case OutsideRepairModeCompleted:
			intents = []NextIntent{{Tool: "concord_work_trace", Operation: "history", ReasonCode: outsideRepairVerifiedIntent, RequiredFields: []string{"work_id"}}}
		case OutsideRepairModeResume:
			intents = []NextIntent{{Tool: "concord_work_browse", Operation: "scope", ReasonCode: outsideRepairRefreshedIntent, RequiredFields: []string{"work_id"}}}
		default:
			intents = []NextIntent{{Tool: "concord_work_transition", Operation: OutsideRepairOperationReconcile, ReasonCode: outsideRepairHoldIntent, RequiredFields: []string{"mode"}}}
		}
		return mutationPayload(changed, intents), result.EventIDs, changed, nil
	}
}
