package workflowproof

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	outsideRepairSHAPattern         = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	outsideRepairLinearTokenPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]*-[0-9]+\b`)
)

func outsideRepairGitSHAValid(value string) bool {
	return outsideRepairSHAPattern.MatchString(value)
}

func outsideRepairCanonicalPullRequestURL(repository string, number int64) string {
	return "https://github.com/" + repository + "/pull/" + strconv.FormatInt(number, 10)
}

func outsideRepairCanonicalReleaseURL(repository, tag string) string {
	return "https://github.com/" + repository + "/releases/tag/" + tag
}

type collector struct {
	command func(context.Context, ...string) (string, error)
}

func missingProof(detail string) *Refusal {
	return &Refusal{detail: detail}
}

func foreignIdentity(detail string) *Refusal {
	return &Refusal{detail: detail, foreignIdentity: true}
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

// Collect fetches authenticated completion proof outside any write transaction.
// It reads the clock only after every selected merge is proven in the release.
func Collect(ctx context.Context, request Request) (Evidence, *Refusal) {
	r := collector{command: request.Command}
	zero := Evidence{}
	canonical, canonicalRefusal := r.outsideRepairCanonicalRepository(ctx, request.Repository)
	if canonicalRefusal != nil {
		return zero, canonicalRefusal
	}
	sorted := make([]int64, len(request.PullRequests))
	copy(sorted, request.PullRequests)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	evidencePullRequests := make([]PullRequestEvidence, 0, len(sorted))
	for _, number := range sorted {
		selected, selectedRefusal := r.selectedOutsideRepairPullRequest(ctx, canonical, number, request.LinearIssueKey)
		if selectedRefusal != nil {
			return zero, selectedRefusal
		}
		evidencePullRequests = append(evidencePullRequests, selected)
	}
	release, releaseRefusal := r.outsideRepairPublishedRelease(ctx, canonical, request.ReleaseTag)
	if releaseRefusal != nil {
		return zero, releaseRefusal
	}
	for _, row := range evidencePullRequests {
		ancestorRefusal := r.outsideRepairMergeAncestor(ctx, canonical, row.MergeSHA, release.ResolvedCommit, strconv.FormatInt(row.Number, 10))
		if ancestorRefusal != nil {
			return zero, ancestorRefusal
		}
		if row.MergedAt.After(release.Published) {
			return zero, missingProof("merged pull request " + strconv.FormatInt(row.Number, 10) + " joined after the published release")
		}
	}
	return Evidence{
		AuthorityRef: request.AuthorityRef,
		ObservedAt:   request.Now(),
		Repository:   canonical,
		PullRequests: evidencePullRequests,
		ReleaseTag:   request.ReleaseTag,
		ReleaseURL:   outsideRepairCanonicalReleaseURL(canonical, request.ReleaseTag),
		ReleaseSHA:   release.ResolvedCommit,
		PublishedAt:  release.Published,
	}, nil
}

// outsideRepairCanonicalRepository resolves the GitHub canonical identity for
// the normalized store-registered repository. The store records a lowercased
// owner/name from URL parsing, but GitHub returns mixed-case identifiers on
// every forge URL; the receipt must keep that casing. The command runs before
// any pull-request or release fetch, so every URL it returns downstream can be
// matched exactly against the GitHub-authenticated identifiers, and the store's
// URL exact-match validation can rely on it.
func (r collector) outsideRepairCanonicalRepository(ctx context.Context, normalized string) (string, *Refusal) {
	zero := ""
	output, err := r.command(ctx, "gh", "repo", "view", normalized, "--json", "nameWithOwner,url")
	if err != nil {
		return zero, missingProof("canonical repository " + normalized + ": " + err.Error())
	}
	var view ghOutsideRepoView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return zero, missingProof("canonical repository " + normalized + ": gh answered unparseable output")
	}
	if !strings.EqualFold(view.NameWithOwner, normalized) || !strings.EqualFold(view.URL, "https://github.com/"+normalized) {
		return zero, foreignIdentity("canonical repository " + normalized + " carries another repository's identifier or URL")
	}
	return view.NameWithOwner, nil
}

func (r collector) selectedOutsideRepairPullRequest(ctx context.Context, repository string, number int64, issueKey string) (PullRequestEvidence, *Refusal) {
	zero := PullRequestEvidence{}
	reference := strconv.FormatInt(number, 10)
	wantURL := outsideRepairCanonicalPullRequestURL(repository, number)
	output, err := r.command(ctx, "gh", "pr", "view", reference, "-R", repository, "--json", "number,state,headRefOid,mergeCommit,mergedAt,url,body")
	if err != nil {
		return zero, missingProof("pull request " + wantURL + ": " + err.Error())
	}
	var view ghPullRequestView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return zero, missingProof("pull request " + wantURL + ": gh answered unparseable output")
	}
	if view.Number != number || view.State != "MERGED" || view.MergeCommit == nil || view.MergeCommit.Oid == "" || !outsideRepairGitSHAValid(view.HeadRefOid) || !outsideRepairGitSHAValid(view.MergeCommit.Oid) {
		return zero, missingProof("pull request " + wantURL + " is not merged")
	}
	if !strings.EqualFold(view.URL, wantURL) {
		return zero, foreignIdentity("pull request " + wantURL + " carries another repository's identifier or URL")
	}
	mergedAt, parseErr := time.Parse(time.RFC3339, view.MergedAt)
	if parseErr != nil {
		return zero, missingProof("pull request " + wantURL + " carries no parseable merge time")
	}
	linked := issueKey == ""
	for _, token := range outsideRepairLinearTokenPattern.FindAllString(strings.ToUpper(view.Body), -1) {
		linked = linked || token == issueKey
	}
	if !linked {
		return zero, missingProof("pull request " + wantURL + " does not name the work's Linear ref " + issueKey)
	}
	checks, err := r.outsideRepairRequiredChecks(ctx, repository, number, view.HeadRefOid)
	if err != nil {
		return zero, missingProof("pull request " + wantURL + " required checks: " + err.Error())
	}
	return PullRequestEvidence{
		URL:            view.URL,
		Number:         number,
		HeadSHA:        view.HeadRefOid,
		MergeSHA:       view.MergeCommit.Oid,
		MergedAt:       mergedAt,
		RequiredChecks: checks,
	}, nil
}

func (r collector) outsideRepairReadJSON(ctx context.Context, target any, args ...string) error {
	output, err := r.command(ctx, append([]string{"gh", "api"}, args...)...)
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
func (r collector) outsideRepairRequiredNames(ctx context.Context, repository string, number int64, headSHA string) (map[string]int64, int64, error) {
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
	required := outsideRepairRequiredNames{}
	if err := required.addClassicProtection(pr.BaseRef.BranchProtectionRule); err != nil {
		return nil, 0, err
	}
	var pages [][]ghOutsideBranchRule
	if err := r.outsideRepairReadJSON(ctx, &pages, "repos/"+repository+"/rules/branches/"+url.PathEscape(pr.BaseRefName)+"?per_page=100", "--paginate", "--slurp"); err != nil {
		return nil, 0, err
	}
	if err := required.addEffectiveRules(pages); err != nil {
		return nil, 0, err
	}
	if len(required) == 0 || len(required) > 64 {
		return nil, 0, fmt.Errorf("outside repair requires 1..64 authenticated required names")
	}
	return required, repo.DatabaseID, nil
}

type outsideRepairRequiredNames map[string]int64

func (required outsideRepairRequiredNames) add(context string, appID int64) error {
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

func (required outsideRepairRequiredNames) addClassicProtection(protection json.RawMessage) error {
	if len(protection) == 0 {
		return fmt.Errorf("missing classic branch protection response")
	}
	if string(protection) == "null" {
		return nil
	}
	var rule ghOutsideProtection
	if err := json.Unmarshal(protection, &rule); err != nil {
		return fmt.Errorf("unparseable classic branch protection: %w", err)
	}
	if rule.RequiresStatusChecks == nil || rule.RequiredStatusCheckContexts == nil || rule.RequiredStatusChecks == nil || rule.RequiresDeployments == nil || rule.RequiredDeploymentEnvironments == nil {
		return fmt.Errorf("incomplete classic branch protection")
	}
	if *rule.RequiresDeployments || len(rule.RequiredDeploymentEnvironments) > 0 {
		// Same named follow-up and same fail-closed refusal as the
		// ruleset required_deployments rule: the collector cannot
		// certify deployment enforcement without its native receipts.
		return fmt.Errorf("unsupported classic deployment enforcement (requiresDeployments): required-deployment receipts not yet collected")
	}
	if !*rule.RequiresStatusChecks {
		return nil
	}
	for _, context := range rule.RequiredStatusCheckContexts {
		if err := required.add(context, 0); err != nil {
			return err
		}
	}
	for _, check := range rule.RequiredStatusChecks {
		appID := int64(0)
		if check.App != nil {
			appID = check.App.DatabaseID
			if appID <= 0 {
				return fmt.Errorf("invalid required app identity")
			}
		}
		if err := required.add(check.Context, appID); err != nil {
			return err
		}
	}
	if len(required) == 0 {
		return fmt.Errorf("classic protection requires checks but names none")
	}
	return nil
}

func (required outsideRepairRequiredNames) addEffectiveRules(pages [][]ghOutsideBranchRule) error {
	if len(pages) == 0 {
		return fmt.Errorf("no effective rules response")
	}
	for _, page := range pages {
		if page == nil {
			return fmt.Errorf("incomplete effective rules response")
		}
		for _, rule := range page {
			if rule.Type == "" {
				return fmt.Errorf("effective rule has no type")
			}
			// Scope: only required_status_checks is proven here; `workflows` and `required_deployments` fail closed because the collector does not yet build authenticated required-workflow receipts (named follow-up: support authenticated required-workflow receipts).
			if rule.Type != "required_status_checks" {
				if rule.Type == "workflows" || rule.Type == "required_deployments" {
					return fmt.Errorf("unsupported enforcement evidence rule %s: required-workflow receipts not yet collected", rule.Type)
				}
				continue
			}
			if rule.Parameters == nil || len(rule.Parameters.RequiredStatusChecks) == 0 {
				return fmt.Errorf("incomplete effective required status checks")
			}
			for _, check := range rule.Parameters.RequiredStatusChecks {
				if err := required.add(check.Context, check.IntegrationID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r collector) outsideRepairRequiredChecks(ctx context.Context, repository string, number int64, headSHA string) ([]RequiredCheck, error) {
	required, repositoryID, err := r.outsideRepairRequiredNames(ctx, repository, number, headSHA)
	if err != nil {
		return nil, err
	}
	var pages []ghOutsideCheckRunPage
	if err := r.outsideRepairReadJSON(ctx, &pages, "repos/"+repository+"/commits/"+headSHA+"/check-runs?filter=all&per_page=100", "--paginate", "--slurp"); err != nil {
		return nil, err
	}
	latest, err := outsideRepairLatestChecks(pages, required, headSHA)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	checks := make([]RequiredCheck, 0, len(names))
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

type ghOutsideCheckRunPage struct {
	TotalCount *int                `json:"total_count"`
	CheckRuns  []ghOutsideCheckRun `json:"check_runs"`
}

func outsideRepairLatestChecks(pages []ghOutsideCheckRunPage, required map[string]int64, headSHA string) (map[string]ghOutsideCheckRun, error) {
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
			if err := outsideRepairRetainLatestCheck(latest, check); err != nil {
				return nil, err
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
	return latest, nil
}

func outsideRepairRetainLatestCheck(latest map[string]ghOutsideCheckRun, check ghOutsideCheckRun) error {
	previous, exists := latest[check.Name]
	if exists && (previous.StartedAt.IsZero() || check.StartedAt.IsZero()) {
		return fmt.Errorf("cannot order duplicate check %s without start times", check.Name)
	}
	if !exists || check.StartedAt.After(previous.StartedAt) || (check.StartedAt.Equal(previous.StartedAt) && check.ID > previous.ID) {
		latest[check.Name] = check
	}
	return nil
}

func (r collector) outsideRepairRunProof(ctx context.Context, repository string, repositoryID int64, headSHA string, check ghOutsideCheckRun) (RequiredCheck, error) {
	zero := RequiredCheck{}
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
	var pages []ghOutsideJobPage
	if err := r.outsideRepairReadJSON(ctx, &pages, "repos/"+repository+"/actions/runs/"+parts[0]+"/jobs?filter=latest&per_page=100", "--paginate", "--slurp"); err != nil {
		return zero, err
	}
	matchedJobID, err := outsideRepairMatchedJob(pages, repository, run, check, jobID, wantURL)
	if err != nil {
		return zero, err
	}
	// The receipt keeps the exact native identities it authenticated (CD-0210
	// D2): the check-run, its workflow run, and the one current job that
	// proved it. Reruns mint new identities, so a recorded receipt cannot be
	// rewritten by a later attempt.
	return RequiredCheck{Name: check.Name, URL: run.HTMLURL, CommitSHA: run.HeadSHA, Conclusion: run.Conclusion, CheckRunID: check.ID, RunID: run.ID, JobID: matchedJobID}, nil
}

type ghOutsideJobPage struct {
	TotalCount *int           `json:"total_count"`
	Jobs       []ghOutsideJob `json:"jobs"`
}

func outsideRepairMatchedJob(pages []ghOutsideJobPage, repository string, run ghOutsideWorkflowRun, check ghOutsideCheckRun, jobID int64, wantURL string) (int64, error) {
	count, matches := 0, 0
	seen := map[int64]bool{}
	matchedJobID := int64(0)
	for _, page := range pages {
		if page.TotalCount == nil || page.Jobs == nil {
			return 0, fmt.Errorf("incomplete Actions job page")
		}
		count += len(page.Jobs)
		for _, job := range page.Jobs {
			if job.ID <= 0 || seen[job.ID] {
				return 0, fmt.Errorf("invalid or repeated job identity")
			}
			seen[job.ID] = true
			if !strings.EqualFold(job.CheckRunURL, "https://api.github.com/repos/"+repository+"/check-runs/"+strconv.FormatInt(check.ID, 10)) {
				continue
			}
			if job.RunID != run.ID || job.HeadSHA != run.HeadSHA || job.Name != check.Name || job.Status != "completed" || job.Conclusion != "success" || (jobID != 0 && job.ID != jobID) || !strings.EqualFold(job.HTMLURL, wantURL+"/job/"+strconv.FormatInt(job.ID, 10)) {
				return 0, fmt.Errorf("job does not bind the required name and check to the fetched run")
			}
			matches++
			matchedJobID = job.ID
		}
	}
	if len(pages) == 0 || matches != 1 {
		return 0, fmt.Errorf("no unique current job proves the required check")
	}
	for _, page := range pages {
		if *page.TotalCount != count {
			return 0, fmt.Errorf("job pagination is incomplete or changed during collection")
		}
	}
	return matchedJobID, nil
}

func (r collector) outsideRepairPublishedRelease(ctx context.Context, repository, tag string) (publishedRelease, *Refusal) {
	zero := publishedRelease{}
	output, err := r.command(ctx, "gh", "release", "view", tag, "-R", repository, "--json", "tagName,isDraft,isPrerelease,url,publishedAt")
	if err != nil {
		return zero, missingProof("published release " + tag + ": " + err.Error())
	}
	var view ghReleaseView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return zero, missingProof("published release " + tag + ": gh answered unparseable output")
	}
	if view.IsDraft || view.IsPrerelease || view.TagName != tag {
		return zero, missingProof("outside repair closes only a non-draft, non-prerelease published release")
	}
	published, parseErr := time.Parse(time.RFC3339, view.PublishedAt)
	if parseErr != nil {
		return zero, missingProof("published release " + tag + " carries no parseable published time")
	}
	if view.URL != outsideRepairCanonicalReleaseURL(repository, tag) {
		return zero, foreignIdentity("published release " + tag + " carries another repository's URL")
	}
	// targetCommitish may name a branch, so the immutable commit resolves
	// through the tag ref, never through that field.
	commit, commitRefusal := r.outsideRepairTagCommit(ctx, repository, tag)
	if commitRefusal != nil {
		return zero, commitRefusal
	}
	return publishedRelease{ResolvedCommit: commit, Published: published}, nil
}

func (r collector) outsideRepairTagCommit(ctx context.Context, repository, tag string) (string, *Refusal) {
	output, err := r.command(ctx, "gh", "api", "repos/"+repository+"/git/ref/tags/"+tag)
	if err != nil {
		return "", missingProof("published release " + tag + ": " + err.Error())
	}
	var ref ghRefObject
	if err := json.Unmarshal([]byte(output), &ref); err != nil {
		return "", missingProof("published release " + tag + ": gh answered unparseable output")
	}
	if !outsideRepairGitSHAValid(ref.Object.SHA) {
		return "", missingProof("published release " + tag + " carries no valid tag object SHA")
	}
	commit := ref.Object.SHA
	if ref.Object.Type == "tag" {
		// An annotated tag names its tag object, so deref it for the commit.
		output, err = r.command(ctx, "gh", "api", "repos/"+repository+"/git/tags/"+commit)
		if err != nil {
			return "", missingProof("published release " + tag + ": " + err.Error())
		}
		var object ghRefObject
		if err := json.Unmarshal([]byte(output), &object); err != nil {
			return "", missingProof("published release " + tag + ": gh answered unparseable output")
		}
		ref = object
		commit = ref.Object.SHA
	}
	if ref.Object.Type != "commit" || !outsideRepairGitSHAValid(commit) {
		return "", missingProof("published release " + tag + " resolves to no native git commit")
	}
	return commit, nil
}

// outsideRepairMergeAncestor proves the pull request merge commit is an
// ancestor of the published release commit through the authenticated compare
// endpoint: base...head reads "ahead" exactly when base is an ancestor of
// head, "identical" when both commits are the same, and a "diverged" or
// "behind" status names a merge the release does not contain.
func (r collector) outsideRepairMergeAncestor(ctx context.Context, repository, mergeSHA, releaseCommit, pullRequest string) *Refusal {
	output, err := r.command(ctx, "gh", "api", "repos/"+repository+"/compare/"+mergeSHA+"..."+releaseCommit)
	if err != nil {
		return missingProof("pull request " + pullRequest + " merge comparison: " + err.Error())
	}
	var view ghCompareView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return missingProof("pull request " + pullRequest + " merge comparison: gh answered unparseable output")
	}
	if (view.Status == "ahead" || view.Status == "identical") && view.BehindBy == 0 {
		return nil
	}
	return missingProof("published release " + releaseCommit + " does not contain pull request " + pullRequest)
}
