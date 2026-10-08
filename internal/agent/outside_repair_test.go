package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// Tests use an authenticated-command fake and a temporary store, never live writes.
// proves check:outside-repair-agent-boundary.

var (
	outsideFakeRepository  = "outside-fixture/concord"
	outsideFakeHeadSHA     = "1a" + strings.Repeat("ab", 19)
	outsideFakeMergeSHA    = "2b" + strings.Repeat("cd", 19)
	outsideFakeTagOID      = "00" + strings.Repeat("11", 19)
	outsideFakeReleaseSHA  = "ee" + strings.Repeat("0f", 19)
	outsideFakeAltSHA      = "77" + strings.Repeat("88", 19)
	outsideFakeTag         = "v11.63.34"
	outsideFakeIssueKey    = "CON-869"
	outsideFakeIssueNumber = int64(1339)
)

// fakeOutsideBoundary is the test double for the authenticated gh boundary.
// Each non-zero override redirects one surface so its scenario can drive the
// boundary into the typed refusal for the cause it pins.
type fakeOutsideBoundary struct {
	prBody                 string // empty sets a body naming the fixture issue key
	prURL                  string // empty sets the canonical fixture URL
	prState                string // empty sets MERGED
	headSHA                string // empty sets the fixture head SHA
	mergeSHA               string // empty sets the fixture merge SHA
	prMissing              bool
	checks                 []map[string]string
	noChecks               bool
	releaseJSON            string
	releaseCommandErr      string
	annotatedTag           bool
	releaseCommitSHA       string // empty sets the fixture release commit
	compareDifference      bool
	fetches                int
	apiOutputs             map[string]string
	apiErrors              map[string]string
	runSHA                 string
	runConclusion          string
	tagObjectType          string
	commands               []string
	canonicalNameWithOwner string // empty sets the fixture repository
	canonicalURL           string // empty sets https://github.com/<canonicalNameWithOwner>
	repoViewJSON           string // empty sets the default {nameWithOwner,url} payload
	repoViewErr            string // empty succeeds the repo view call
	effectiveRulesJSON     string // empty sets the default [[{"type":"required_status_checks",...}]] payload
}

func (f *fakeOutsideBoundary) resolvedReleaseCommit() string {
	if f.releaseCommitSHA != "" {
		return f.releaseCommitSHA
	}
	return outsideFakeReleaseSHA
}

func (f *fakeOutsideBoundary) resolvedHead() string {
	if f.headSHA != "" {
		return f.headSHA
	}
	return outsideFakeHeadSHA
}

func (f *fakeOutsideBoundary) resolvedMerge() string {
	if f.mergeSHA != "" {
		return f.mergeSHA
	}
	return outsideFakeMergeSHA
}

func (f *fakeOutsideBoundary) resolvedBody() string {
	if f.prBody != "" {
		return f.prBody
	}
	return "Fixes " + outsideFakeIssueKey + ": bounded repair."
}

func outsideFixture(t *testing.T) (*store.Store, *Service, CallEnvelope, *fakeOutsideBoundary) {
	t.Helper()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "product_read"})
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	bindOutsideRepositoryFixture(t, s)
	seedOutsideLinearLink(t, s)
	fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks()}
	installOutsideBoundary(t, service, fake)
	return s, service, env, fake
}

// bindOutsideRepositoryFixture records the fixture Project's git_remote
// locator. The outside route derives the repository from this locator; no
// invocation supplies one.
func bindOutsideRepositoryFixture(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.AddProjectLocator(context.Background(), "project-1", store.ProjectLocator{
		ID: "outside-repair-git-remote", Kind: store.LocatorGitRemote,
		Value: "https://github.com/" + outsideFakeRepository + ".git",
	}, 1); err != nil {
		t.Fatal(err)
	}
}

// seedOutsideLinearLink attaches the confirmed external Linear ref the PR
// body linkage check binds.
func seedOutsideLinearLink(t *testing.T, s *store.Store) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES('work-1', 'outside-fixture-issue-uuid-1', ?, 'https://linear.app/sharper-flow/issue/con-869', '', '', 'confirmed', '2026-09-28T00:00:00Z', '2026-09-28T00:00:00Z'); DELETE FROM fold_guard`, outsideFakeIssueKey); err != nil {
		t.Fatal(err)
	}
}

func installOutsideBoundary(t *testing.T, service *Service, fake *fakeOutsideBoundary) {
	t.Helper()
	service.ExternalEvidenceCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command != "gh" {
			t.Fatalf("external boundary received non-gh command %q", command)
		}
		argv := append([]string{"gh"}, args...)
		joined := strings.Join(argv, " ")
		if !strings.Contains(strings.ToLower(joined), "-r "+outsideFakeRepository+" ") && !strings.Contains(strings.ToLower(joined), "repos/"+outsideFakeRepository+"/") && !(len(args) > 1 && args[1] == "graphql" && strings.Contains(strings.ToLower(joined), "owner=outside-fixture") && strings.Contains(joined, "name=concord")) && !(len(args) >= 2 && args[0] == "repo" && args[1] == "view" && strings.EqualFold(args[2], outsideFakeRepository)) {
			t.Fatalf("external boundary fetched %q; want the recorded Project origin", joined)
		}
		fake.fetches++
		fake.commands = append(fake.commands, joined)
		if len(args) > 1 && args[0] == "api" {
			if strings.Contains(args[1], "?") && (len(args) != 4 || args[2] != "--paginate" || args[3] != "--slurp") {
				t.Fatalf("collection command lacks pagination: %s", joined)
			}
			if message, ok := fake.apiErrors[args[1]]; ok {
				return "", fmt.Errorf("%s", message)
			}
			if output, ok := fake.apiOutputs[args[1]]; ok {
				return output, nil
			}
		}
		switch {
		case len(args) >= 2 && args[0] == "repo" && args[1] == "view":
			canonical := fake.canonicalNameWithOwner
			if canonical == "" {
				canonical = outsideFakeRepository
			}
			canonicalURL := fake.canonicalURL
			if canonicalURL == "" {
				canonicalURL = "https://github.com/" + canonical
			}
			if fake.repoViewErr != "" {
				return "", fmt.Errorf("%s", fake.repoViewErr)
			}
			if fake.repoViewJSON != "" {
				return fake.repoViewJSON, nil
			}
			return fmt.Sprintf(`{"nameWithOwner":"%s","url":"%s"}`, canonical, canonicalURL), nil
		case len(args) > 1 && args[1] == "graphql":
			return `{"data":{"repository":{"databaseId":123,"nameWithOwner":"outside-fixture/concord","url":"https://github.com/outside-fixture/concord","pullRequest":{"number":1339,"headRefOid":"` + fake.resolvedHead() + `","baseRefName":"main","baseRef":{"name":"main","branchProtectionRule":{"requiresStatusChecks":true,"requiredStatusCheckContexts":["ci/build","ci/lint"],"requiredStatusChecks":[{"context":"ci/build","app":{"databaseId":15368}},{"context":"ci/lint","app":{"databaseId":15368}}]}}}}}}`, nil
		case strings.Contains(joined, "/rules/branches/"):
			if fake.effectiveRulesJSON != "" {
				return fake.effectiveRulesJSON, nil
			}
			return `[[{"type":"merge_queue"}]]`, nil
		case strings.Contains(joined, "/check-runs?"):
			if fake.noChecks {
				return "", fmt.Errorf("could not read check runs")
			}
			runs := []map[string]any{}
			for index, row := range fake.checks {
				id := int64(index + 1)
				if row["id"] != "" {
					id, _ = strconv.ParseInt(row["id"], 10, 64)
				}
				sha := row["head_sha"]
				if sha == "" {
					sha = fake.resolvedHead()
				}
				conclusion := "success"
				if row["bucket"] != "pass" {
					conclusion = "failure"
				}
				started := row["started_at"]
				if started == "" {
					started = fmt.Sprintf("2026-08-07T09:00:%02dZ", id)
				}
				runs = append(runs, map[string]any{"id": id, "name": row["name"], "details_url": row["link"], "head_sha": sha, "status": "completed", "conclusion": conclusion, "started_at": started, "app": map[string]any{"id": 15368}})
			}
			encoded, _ := json.Marshal([]any{map[string]any{"total_count": len(runs), "check_runs": runs}})
			return string(encoded), nil
		case strings.Contains(joined, "/jobs?"):
			jobs := []map[string]any{}
			for index, row := range fake.checks {
				id := int64(index + 1)
				if row["id"] != "" {
					id, _ = strconv.ParseInt(row["id"], 10, 64)
				}
				runPath, _, _ := strings.Cut(strings.TrimPrefix(row["link"], "https://github.com/"), "/job/")
				expected := "repos/" + runPath + "/jobs?filter=latest&per_page=100"
				if !strings.EqualFold(args[1], expected) {
					continue
				}
				runID, _ := strconv.ParseInt(runPath[strings.LastIndex(runPath, "/")+1:], 10, 64)
				jobs = append(jobs, map[string]any{"id": 9100 + id, "run_id": runID, "head_sha": fake.resolvedHead(), "name": row["name"], "status": "completed", "conclusion": "success", "html_url": "https://github.com/" + runPath + "/job/" + strconv.FormatInt(9100+id, 10), "check_run_url": "https://api.github.com/repos/" + outsideFakeRepository + "/check-runs/" + strconv.FormatInt(id, 10)})
			}
			encoded, _ := json.Marshal([]any{map[string]any{"total_count": len(jobs), "jobs": jobs}})
			return string(encoded), nil
		case strings.Contains(joined, "/actions/runs/") && !strings.Contains(joined, "/jobs?"):
			canonical := fake.canonicalNameWithOwner
			if canonical == "" {
				canonical = outsideFakeRepository
			}
			prefix := "repos/" + canonical + "/actions/runs/"
			idText := args[1]
			if !strings.HasPrefix(strings.ToLower(idText), strings.ToLower(prefix)) {
				t.Fatalf("unexpected run endpoint: %s", joined)
			}
			idText = idText[len(prefix):]
			// Strip /jobs/<n> suffix when present.
			if slash := strings.Index(idText, "/"); slash >= 0 {
				idText = idText[:slash]
			}
			id, err := strconv.ParseInt(idText, 10, 64)
			if err != nil {
				t.Fatalf("unexpected run endpoint: %s", joined)
			}
			sha, conclusion := fake.runSHA, fake.runConclusion
			if sha == "" {
				sha = fake.resolvedHead()
			}
			if conclusion == "" {
				conclusion = "success"
			}
			return fmt.Sprintf(`{"id":%d,"html_url":"https://github.com/%s/actions/runs/%d","head_sha":"%s","status":"completed","conclusion":"%s","repository":{"id":123,"full_name":"%s","html_url":"https://github.com/%s"}}`, id, canonical, id, sha, conclusion, canonical, canonical), nil
		case strings.Contains(joined, "pr view"):
			if fake.prMissing {
				return "", fmt.Errorf("could not resolve to a PullRequest")
			}
			body, _ := json.Marshal(fake.resolvedBody())
			state := fake.prState
			if state == "" {
				state = "MERGED"
			}
			URL := fake.prURL
			if URL == "" {
				URL = "https://github.com/" + outsideFakeRepository + "/pull/1339"
			}
			return fmt.Sprintf(`{"number":%d,"state":"%s","headRefOid":"%s","mergeCommit":{"oid":"%s"},"mergedAt":"2026-08-07T10:00:00Z","url":"%s","body":%s}`, outsideFakeIssueNumber, state, fake.resolvedHead(), fake.resolvedMerge(), URL, body), nil
		case strings.Contains(joined, "release view"):
			if fake.releaseCommandErr != "" {
				return "", fmt.Errorf("%s", fake.releaseCommandErr)
			}
			if fake.releaseJSON != "" {
				return fake.releaseJSON, nil
			}
			return fmt.Sprintf(`{"tagName":"%s","isDraft":false,"isPrerelease":false,"url":"https://github.com/%s/releases/tag/%s","publishedAt":"2026-08-07T11:00:00Z","targetCommitish":"main"}`, outsideFakeTag, outsideFakeRepository, outsideFakeTag), nil
		case strings.Contains(joined, "git/ref/tags/"):
			if fake.annotatedTag {
				return fmt.Sprintf(`{"ref":"refs/tags/%s","object":{"sha":"%s","type":"tag"}}`, outsideFakeTag, outsideFakeTagOID), nil
			}
			kind := fake.tagObjectType
			if kind == "" {
				kind = "commit"
			}
			return fmt.Sprintf(`{"ref":"refs/tags/%s","object":{"sha":"%s","type":"%s"}}`, outsideFakeTag, fake.resolvedReleaseCommit(), kind), nil
		case strings.Contains(joined, "git/tags/"):
			kind := fake.tagObjectType
			if kind == "" {
				kind = "commit"
			}
			return fmt.Sprintf(`{"object":{"sha":"%s","type":"%s"}}`, fake.resolvedReleaseCommit(), kind), nil
		case strings.Contains(joined, "/compare/"):
			// The compare endpoint (left...right). The default fixture
			// proves the left merge commit is an ancestor of the right
			// published release commit: status "ahead", behind_by 0.
			status, behind := "ahead", 0
			if fake.compareDifference {
				status, behind = "diverged", 2
			}
			return fmt.Sprintf(`{"status":"%s","ahead_by":0,"behind_by":%d}`, status, behind), nil
		default:
			t.Fatalf("unexpected external command: %s", joined)
			return "", fmt.Errorf("unexpected external command")
		}
	}
}

func outsideSuccessfulChecks() []map[string]string {
	return []map[string]string{
		{"name": "ci/build", "link": "https://github.com/" + outsideFakeRepository + "/actions/runs/9000000001", "state": "SUCCESS", "bucket": "pass"},
		{"name": "ci/lint", "link": "https://github.com/" + outsideFakeRepository + "/actions/runs/9000000002", "state": "SUCCESS", "bucket": "pass"},
	}
}

func TestOutsideRepairCollectorRejectsIncompleteProof(t *testing.T) {
	rulesEndpoint := "repos/" + outsideFakeRepository + "/rules/branches/main?per_page=100"
	runEndpoint := "repos/" + outsideFakeRepository + "/actions/runs/9000000001"
	for _, test := range []struct {
		name   string
		change func(*fakeOutsideBoundary)
	}{
		{"missing required context omitted from rows", func(f *fakeOutsideBoundary) { f.checks = f.checks[:1] }},
		{"wrong run SHA despite pass", func(f *fakeOutsideBoundary) { f.runSHA = outsideFakeAltSHA }},
		{"wrong run conclusion despite pass", func(f *fakeOutsideBoundary) { f.runConclusion = "failure" }},
		{"unreadable classic rules", func(f *fakeOutsideBoundary) { f.apiErrors["graphql"] = "HTTP 403" }},
		{"unreadable effective rules", func(f *fakeOutsideBoundary) { f.apiErrors[rulesEndpoint] = "HTTP 404" }},
		{"malformed effective rules", func(f *fakeOutsideBoundary) { f.apiOutputs[rulesEndpoint] = `[[{"type":"required_status_checks"}]]` }},
		{"ruleset required context omitted", func(f *fakeOutsideBoundary) {
			f.apiOutputs[rulesEndpoint] = `[[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"security","integration_id":15368}]}}]]`
		}},
		{"wrong run repository", func(f *fakeOutsideBoundary) {
			f.apiOutputs[runEndpoint] = fmt.Sprintf(`{"id":9000000001,"html_url":"https://github.com/%s/actions/runs/9000000001","head_sha":"%s","status":"completed","conclusion":"success","repository":{"id":999,"full_name":"%s","html_url":"https://github.com/%s"}}`, outsideFakeRepository, outsideFakeHeadSHA, outsideFakeRepository, outsideFakeRepository)
		}},
		{"wrong check SHA", func(f *fakeOutsideBoundary) { f.checks[0]["head_sha"] = outsideFakeAltSHA }},
		{"newest duplicate fails", func(f *fakeOutsideBoundary) {
			f.checks = append(f.checks, map[string]string{"id": "3", "name": "ci/build", "link": f.checks[0]["link"], "bucket": "fail"})
		}},
		{"insecure run URL", func(f *fakeOutsideBoundary) {
			f.checks[0]["link"] = strings.Replace(f.checks[0]["link"], "https:", "http:", 1)
		}},
		{"run URL suffix", func(f *fakeOutsideBoundary) { f.checks[0]["link"] += "/garbage" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks(), apiOutputs: map[string]string{}, apiErrors: map[string]string{}}
			test.change(fake)
			service := &Service{}
			installOutsideBoundary(t, service, fake)
			evidence, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, outsideFakeIssueKey)
			if refusal.Error == nil || refusal.Error.Kind != "missing_evidence" || len(evidence.RequiredChecks) != 0 {
				t.Fatalf("collector accepted incomplete proof: %+v / %+v", evidence, refusal.Error)
			}
		})
	}
}

func TestOutsideRepairCollectorReceiptUsesFetchedRun(t *testing.T) {
	fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks()}
	service := &Service{}
	installOutsideBoundary(t, service, fake)
	evidence, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, outsideFakeIssueKey)
	if refusal.Error != nil || len(evidence.RequiredChecks) != 2 {
		t.Fatalf("collector: %+v / %+v", evidence, refusal.Error)
	}
	for index, check := range evidence.RequiredChecks {
		endpoint := "gh api repos/" + outsideFakeRepository + "/actions/runs/" + strconv.FormatInt(9000000001+int64(index), 10)
		found := false
		for _, command := range fake.commands {
			found = found || command == endpoint
		}
		if !found || check.CommitSHA != fake.resolvedHead() || check.Conclusion != "success" || check.URL != fake.checks[index]["link"] {
			t.Fatalf("receipt lacks exact fetched run proof: %+v; commands=%v", check, fake.commands)
		}
	}
}

func overrideOutsideAPI(t *testing.T, fake *fakeOutsideBoundary, endpoint string, change func(any) any) {
	t.Helper()
	service := &Service{}
	installOutsideBoundary(t, service, fake)
	args := []string{"api", endpoint}
	if endpoint == "graphql" {
		args = append(args, "-f", "owner=outside-fixture", "-f", "name=concord")
	} else if strings.Contains(endpoint, "?") {
		args = append(args, "--paginate", "--slurp")
	}
	output, err := service.ExternalEvidenceCommand(context.Background(), "gh", args...)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(change(value))
	if err != nil {
		t.Fatal(err)
	}
	if fake.apiOutputs == nil {
		fake.apiOutputs = map[string]string{}
	}
	fake.apiOutputs[endpoint] = string(encoded)
}

func TestOutsideRepairCollectorAuthenticatesEverySurface(t *testing.T) {
	checksEndpoint := "repos/" + outsideFakeRepository + "/commits/" + outsideFakeHeadSHA + "/check-runs?filter=all&per_page=100"
	runEndpoint := "repos/" + outsideFakeRepository + "/actions/runs/9000000001"
	jobsEndpoint := runEndpoint + "/jobs?filter=latest&per_page=100"
	for _, test := range []struct {
		name, endpoint string
		change         func(any) any
	}{
		{"partial GraphQL errors", "graphql", func(v any) any {
			v.(map[string]any)["errors"] = []any{map[string]any{"message": "permission denied"}}
			return v
		}},
		{"missing base ref", "graphql", func(v any) any {
			v.(map[string]any)["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)["baseRef"] = nil
			return v
		}},
		{"missing protection field", "graphql", func(v any) any {
			delete(v.(map[string]any)["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)["baseRef"].(map[string]any), "branchProtectionRule")
			return v
		}},
		{"changed head", "graphql", func(v any) any {
			v.(map[string]any)["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)["headRefOid"] = outsideFakeAltSHA
			return v
		}},
		{"incomplete check pages", checksEndpoint, func(v any) any { v.([]any)[0].(map[string]any)["total_count"] = 3; return v }},
		{"repeated check identity", checksEndpoint, func(v any) any {
			page := v.([]any)[0].(map[string]any)
			runs := page["check_runs"].([]any)
			page["check_runs"] = append(runs, runs[0])
			page["total_count"] = 3
			return v
		}},
		{"wrong check app", checksEndpoint, func(v any) any {
			v.([]any)[0].(map[string]any)["check_runs"].([]any)[0].(map[string]any)["app"] = map[string]any{"id": 999}
			return v
		}},
		{"pending current check", checksEndpoint, func(v any) any {
			v.([]any)[0].(map[string]any)["check_runs"].([]any)[0].(map[string]any)["status"] = "in_progress"
			return v
		}},
		{"wrong run identity", runEndpoint, func(v any) any { v.(map[string]any)["id"] = 9000000999; return v }},
		{"wrong run URL", runEndpoint, func(v any) any {
			v.(map[string]any)["html_url"] = "https://github.com/other/repository/actions/runs/9000000001"
			return v
		}},
		{"wrong repository name", runEndpoint, func(v any) any {
			v.(map[string]any)["repository"].(map[string]any)["full_name"] = "other/repository"
			return v
		}},
		{"wrong repository URL", runEndpoint, func(v any) any {
			v.(map[string]any)["repository"].(map[string]any)["html_url"] = "https://github.com/other/repository"
			return v
		}},
		{"run still pending", runEndpoint, func(v any) any { v.(map[string]any)["status"] = "in_progress"; return v }},
		{"incomplete job pages", jobsEndpoint, func(v any) any { v.([]any)[0].(map[string]any)["total_count"] = 2; return v }},
		{"wrong job name", jobsEndpoint, func(v any) any {
			v.([]any)[0].(map[string]any)["jobs"].([]any)[0].(map[string]any)["name"] = "unrelated"
			return v
		}},
		{"wrong job run", jobsEndpoint, func(v any) any {
			v.([]any)[0].(map[string]any)["jobs"].([]any)[0].(map[string]any)["run_id"] = 9000000999
			return v
		}},
		{"wrong job SHA", jobsEndpoint, func(v any) any {
			v.([]any)[0].(map[string]any)["jobs"].([]any)[0].(map[string]any)["head_sha"] = outsideFakeAltSHA
			return v
		}},
		{"wrong job conclusion", jobsEndpoint, func(v any) any {
			v.([]any)[0].(map[string]any)["jobs"].([]any)[0].(map[string]any)["conclusion"] = "failure"
			return v
		}},
		{"wrong job check mapping", jobsEndpoint, func(v any) any {
			v.([]any)[0].(map[string]any)["jobs"].([]any)[0].(map[string]any)["check_run_url"] = "https://api.github.com/repos/" + outsideFakeRepository + "/check-runs/999"
			return v
		}},
		{"wrong job URL", jobsEndpoint, func(v any) any {
			v.([]any)[0].(map[string]any)["jobs"].([]any)[0].(map[string]any)["html_url"] = "https://github.com/other/repository/actions/runs/9000000001/job/9101"
			return v
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks()}
			if test.name == "missing protection field" {
				fake.apiOutputs = map[string]string{"repos/" + outsideFakeRepository + "/rules/branches/main?per_page=100": `[[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"ci/build","integration_id":15368},{"context":"ci/lint","integration_id":15368}]}}]]`}
			}
			overrideOutsideAPI(t, fake, test.endpoint, test.change)
			service := &Service{}
			installOutsideBoundary(t, service, fake)
			_, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, outsideFakeIssueKey)
			assertOutsideRefusedWith(t, refusal, test.name, "missing_evidence")
		})
	}
}

func TestOutsideRepairCollectorCompletePaginationAndRulesUnion(t *testing.T) {
	fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks()}
	fake.checks = append(fake.checks, map[string]string{"name": "security", "link": "https://github.com/" + outsideFakeRepository + "/actions/runs/9000000003", "bucket": "pass"})
	rulesEndpoint := "repos/" + outsideFakeRepository + "/rules/branches/main?per_page=100"
	fake.apiOutputs = map[string]string{rulesEndpoint: `[[{"type":"merge_queue"}],[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"security","integration_id":15368},{"context":"ci/build","integration_id":15368}]}}]]`}
	checksEndpoint := "repos/" + outsideFakeRepository + "/commits/" + outsideFakeHeadSHA + "/check-runs?filter=all&per_page=100"
	overrideOutsideAPI(t, fake, checksEndpoint, func(v any) any {
		page := v.([]any)[0].(map[string]any)
		runs := page["check_runs"].([]any)
		return []any{map[string]any{"total_count": 3, "check_runs": runs[:1]}, map[string]any{"total_count": 3, "check_runs": runs[1:]}}
	})
	jobsEndpoint := "repos/" + outsideFakeRepository + "/actions/runs/9000000001/jobs?filter=latest&per_page=100"
	overrideOutsideAPI(t, fake, jobsEndpoint, func(v any) any {
		page := v.([]any)[0].(map[string]any)
		return []any{map[string]any{"total_count": 1, "jobs": []any{}}, page}
	})
	service := &Service{}
	installOutsideBoundary(t, service, fake)
	evidence, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, outsideFakeIssueKey)
	if refusal.Error != nil || len(evidence.RequiredChecks) != 3 {
		t.Fatalf("pagination/union: %+v / %+v", evidence, refusal.Error)
	}
	for index, name := range []string{"ci/build", "ci/lint", "security"} {
		if evidence.RequiredChecks[index].Name != name {
			t.Fatalf("required union: %+v", evidence.RequiredChecks)
		}
	}
}

func TestOutsideRepairCollectorRulesetOnlyWithExplicitClassicAbsence(t *testing.T) {
	fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks()}
	overrideOutsideAPI(t, fake, "graphql", func(v any) any {
		v.(map[string]any)["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)["baseRef"].(map[string]any)["branchProtectionRule"] = nil
		return v
	})
	fake.apiOutputs["repos/"+outsideFakeRepository+"/rules/branches/main?per_page=100"] = `[[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"ci/build","integration_id":15368},{"context":"ci/lint","integration_id":15368}]}}]]`
	service := &Service{}
	installOutsideBoundary(t, service, fake)
	evidence, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, outsideFakeIssueKey)
	if refusal.Error != nil || len(evidence.RequiredChecks) != 2 {
		t.Fatalf("ruleset-only: %+v / %+v", evidence, refusal.Error)
	}
}

func TestOutsideRepairCollectorNewestDuplicateAndJobURL(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks()}
			fake.checks[0]["bucket"] = "fail"
			fake.checks = append(fake.checks, map[string]string{"id": "3", "name": "ci/build", "link": fake.checks[0]["link"] + "/job/9103", "bucket": "pass"})
			for index, row := range fake.checks {
				if row["id"] == "" {
					row["id"] = strconv.Itoa(index + 1)
				}
			}
			if reverse {
				fake.checks[0], fake.checks[2] = fake.checks[2], fake.checks[0]
			}
			service := &Service{}
			installOutsideBoundary(t, service, fake)
			evidence, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, outsideFakeIssueKey)
			if refusal.Error != nil || len(evidence.RequiredChecks) != 2 || evidence.RequiredChecks[0].URL != "https://github.com/"+outsideFakeRepository+"/actions/runs/9000000001" {
				t.Fatalf("newest check/job binding: %+v / %+v", evidence, refusal.Error)
			}
		})
	}
}

func TestOutsideRepairCollectorDuplicateOrderingRefusesNewestFailure(t *testing.T) {
	for _, test := range []struct{ name, oldStart, newStart string }{
		{"later start outranks larger ID", "2026-08-07T09:02:00Z", "2026-08-07T09:01:00Z"},
		{"equal start uses native ID", "2026-08-07T09:01:00Z", "2026-08-07T09:01:00Z"},
		{"unknown start refuses ambiguity", "null", "2026-08-07T09:01:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks()}
			fake.checks[0]["started_at"] = test.oldStart
			fake.checks = append(fake.checks, map[string]string{"id": "3", "name": "ci/build", "link": fake.checks[0]["link"], "bucket": "fail", "started_at": test.newStart})
			if test.name == "later start outranks larger ID" {
				fake.checks[0]["bucket"], fake.checks[2]["bucket"] = "fail", "pass"
			}
			if test.oldStart == "null" {
				fake.checks[0]["started_at"] = "2026-08-07T09:00:00Z"
				overrideOutsideAPI(t, fake, "repos/"+outsideFakeRepository+"/commits/"+outsideFakeHeadSHA+"/check-runs?filter=all&per_page=100", func(v any) any {
					v.([]any)[0].(map[string]any)["check_runs"].([]any)[0].(map[string]any)["started_at"] = nil
					return v
				})
			}
			service := &Service{}
			installOutsideBoundary(t, service, fake)
			_, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, outsideFakeIssueKey)
			assertOutsideRefusedWith(t, refusal, test.name, "missing_evidence")
		})
	}
}

func TestOutsideRepairCollectorCanonicalRepositoryReceipt(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	canonical := "Outside-Fixture/concord"
	fake.canonicalNameWithOwner = canonical
	fake.canonicalURL = "https://github.com/" + canonical
	fake.prURL = "https://github.com/" + canonical + "/pull/1339"
	for _, endpoint := range []string{
		"graphql",
		"repos/" + outsideFakeRepository + "/commits/" + outsideFakeHeadSHA + "/check-runs?filter=all&per_page=100",
		"repos/" + outsideFakeRepository + "/actions/runs/9000000001",
		"repos/" + outsideFakeRepository + "/actions/runs/9000000002",
		"repos/" + outsideFakeRepository + "/actions/runs/9000000001/jobs?filter=latest&per_page=100",
		"repos/" + outsideFakeRepository + "/actions/runs/9000000002/jobs?filter=latest&per_page=100",
	} {
		overrideOutsideAPI(t, fake, endpoint, func(v any) any {
			encoded, _ := json.Marshal(v)
			var result any
			if err := json.Unmarshal([]byte(strings.ReplaceAll(string(encoded), outsideFakeRepository, canonical)), &result); err != nil {
				t.Fatal(err)
			}
			return result
		})
	}
	fake.releaseJSON = fmt.Sprintf(`{"tagName":"%s","isDraft":false,"isPrerelease":false,"url":"https://github.com/%s/releases/tag/%s","publishedAt":"2026-08-07T11:00:00Z"}`, outsideFakeTag, canonical, outsideFakeTag)
	input := outsideReconcileInput(4, nil)
	ref := dispatchOutsideMints(t, s, service, env, "outside_repair_reconcile", input)
	assertOutsideOK(t, dispatchOutsideApproved(t, s, service, env, "outside_repair_reconcile", ref, input), "canonical repository receipt")
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	evidence := pin.OutsideRepairDisposition.Evidence
	if evidence.Repository != canonical || evidence.PullRequests[0].RequiredChecks[0].URL != "https://github.com/"+canonical+"/actions/runs/9000000001" {
		t.Fatalf("receipt lost authenticated canonical casing: %+v", evidence)
	}
}

// TestOutsideRepairCanonicalRepositoryMismatchRefusesFailBefore covers the
// fail-before path that CD-0210 + the second reviewer require: when the
// canonical identity returned by `gh repo view` differs from the registered
// Project's normalized repository, or when the canonical call itself cannot
// be authenticated, the collector refuses before any pull-request or release
// fetch.
func TestOutsideRepairCanonicalRepositoryMismatchRefusesFailBefore(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeOutsideBoundary)
		kind   string
	}{
		{"different owner", func(f *fakeOutsideBoundary) { f.canonicalNameWithOwner = "Other-Owner/concord" }, "unauthorized"},
		{"different name", func(f *fakeOutsideBoundary) { f.canonicalNameWithOwner = "outside-fixture/other" }, "unauthorized"},
		{"different url", func(f *fakeOutsideBoundary) { f.canonicalURL = "https://github.com/Other-Owner/concord" }, "unauthorized"},
		{"repo view unparseable", func(f *fakeOutsideBoundary) { f.repoViewJSON = `{"nameWithOwner":"unparseable"` }, "missing_evidence"},
		{"repo view errors", func(f *fakeOutsideBoundary) { f.repoViewErr = "HTTP 404" }, "missing_evidence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, service, env, fake := outsideFixture(t)
			outsideHoldCompletes(t, s, service, env, fake)
			test.change(fake)
			input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-canonical-mismatch" })
			ref := dispatchOutsideMints(t, s, service, env, "outside_repair_reconcile", input)
			fetches := fake.fetches
			refused := dispatchOutsideApproved(t, s, service, env, "outside_repair_reconcile", ref, input)
			assertOutsideRefusedWith(t, refused, "canonical repository mismatch", test.kind)
			if got := outsideWorkVersion(t, s); got != 4 {
				t.Fatalf("fail-before advanced work version to %d, want 4", got)
			}
			if state := outsideDispositionState(t, s); state != store.OutsideRepairStateActive {
				t.Fatalf("fail-before moved disposition to %q, want active", state)
			}
			if fake.fetches <= fetches {
				t.Fatalf("fail-before made no further boundary calls (fetches=%d, before=%d)", fake.fetches, fetches)
			}
		})
	}
}

// TestOutsideRepairCanonicalRepositoryAcceptsMixedCase proves the canonical
// resolver accepts the live-mixed-case identity GitHub returns when the store
// registered the lowercased variant. This is the real PR 1575 path.
func TestOutsideRepairCanonicalRepositoryAcceptsMixedCase(t *testing.T) {
	fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks()}
	fake.canonicalNameWithOwner = "Outside-Fixture/concord"
	fake.canonicalURL = "https://github.com/Outside-Fixture/concord"
	service := &Service{}
	installOutsideBoundary(t, service, fake)
	_, refusal := (runtime{Authority: service}).outsideRepairCanonicalRepository(context.Background(), Envelope{}, outsideFakeRepository)
	if refusal.Error != nil {
		t.Fatalf("mixed-case canonical refused: %+v", refusal.Error)
	}
}

// TestOutsideRepairRequiredWorkflowRuleRefusesMissingEvidence covers the
// required-workflow rule path: when the effective rules include a `workflows`
// rule, the collector refuses with missing_evidence even when every status
// check passes. The route currently does not collect authenticated
// required-workflow receipts (see named follow-up). Required_deployments
// behaves identically.
func TestOutsideRepairRequiredWorkflowRuleRefusesMissingEvidence(t *testing.T) {
	for _, ruleType := range []string{"workflows", "required_deployments"} {
		t.Run(ruleType, func(t *testing.T) {
			s, service, env, fake := outsideFixture(t)
			outsideHoldCompletes(t, s, service, env, fake)
			rulesEndpoint := "repos/" + outsideFakeRepository + "/rules/branches/main?per_page=100"
			if fake.apiOutputs == nil {
				fake.apiOutputs = map[string]string{}
			}
			if ruleType == "workflows" {
				fake.apiOutputs[rulesEndpoint] = `[[{"type":"workflows","parameters":{"workflows":[{"path":".github/workflows/ci.yml","repository_id":1326298317}]}}]]`
			} else {
				fake.apiOutputs[rulesEndpoint] = `[[{"type":"required_deployments","parameters":{"required_deployment_environments":["production"]}}]]`
			}
			input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-rule-" + ruleType })
			ref := dispatchOutsideMints(t, s, service, env, "outside_repair_reconcile", input)
			refused := dispatchOutsideApproved(t, s, service, env, "outside_repair_reconcile", ref, input)
			assertOutsideRefusedWith(t, refused, ruleType+"/required rule", "missing_evidence")
			if got := outsideWorkVersion(t, s); got != 4 {
				t.Fatalf("%s rule advanced work version to %d, want 4", ruleType, got)
			}
			if state := outsideDispositionState(t, s); state != store.OutsideRepairStateActive {
				t.Fatalf("%s rule moved disposition to %q, want active", ruleType, state)
			}
		})
	}
}

// TestOutsideRepairBenignRuleTypesStillPass keeps the benign rule types
// (merge_queue, non_fast_forward, the pattern rules, and so on) on the silent
// path: only `workflows` and `required_deployments` are evidence-bearing and
// fail closed.
func TestOutsideRepairBenignRuleTypesStillPass(t *testing.T) {
	benign := []string{"merge_queue", "non_fast_forward", "creation", "update", "deletion", "required_linear_history", "required_signatures", "pull_request", "commit_message_pattern", "commit_author_email_pattern", "committer_email_pattern", "branch_name_pattern", "tag_name_pattern", "file_path_restriction", "max_file_path_length", "file_extension_restriction", "max_file_size"}
	for _, ruleType := range benign {
		t.Run(ruleType, func(t *testing.T) {
			fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks()}
			rulesEndpoint := "repos/" + outsideFakeRepository + "/rules/branches/main?per_page=100"
			fake.apiOutputs = map[string]string{rulesEndpoint: fmt.Sprintf(`[[{"type":%q}]]`, ruleType)}
			service := &Service{}
			installOutsideBoundary(t, service, fake)
			evidence, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, outsideFakeIssueKey)
			if refusal.Error != nil || len(evidence.RequiredChecks) != 2 {
				t.Fatalf("benign rule %s: %+v / %+v", ruleType, evidence, refusal.Error)
			}
		})
	}
}

func TestOutsideRepairReconcileRejectsPrimaryProofViolationsWithoutEffect(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeOutsideBoundary)
	}{
		{"omitted required context", func(f *fakeOutsideBoundary) { f.checks = f.checks[:1] }},
		{"wrong run SHA", func(f *fakeOutsideBoundary) { f.runSHA = outsideFakeMergeSHA }},
		{"wrong run conclusion", func(f *fakeOutsideBoundary) { f.runConclusion = "failure" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, service, env, fake := outsideFixture(t)
			outsideHoldCompletes(t, s, service, env, fake)
			test.change(fake)
			outsideCompletionRefused(t, s, service, env, fake, outsideReconcileInput(4, nil), "missing_evidence", test.name)
		})
	}
}

func TestOutsideRepairCollectorExactLinearToken(t *testing.T) {
	for _, body := range []string{"Fixes CON-869", "Fixes XCON-86", "Fixes CON-860", "Fixes _CON-86"} {
		t.Run(body, func(t *testing.T) {
			fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks(), prBody: body}
			service := &Service{}
			installOutsideBoundary(t, service, fake)
			_, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, "CON-86")
			if refusal.Error == nil {
				t.Fatalf("body %q satisfied CON-86", body)
			}
		})
	}
}

func TestOutsideRepairCollectorAcceptsExactLinearToken(t *testing.T) {
	for _, body := range []string{"Fixes CON-86.", "[CON-86](https://linear.app/fixture/issue/CON-86)", "https://linear.app/fixture/issue/con-86/repair"} {
		t.Run(body, func(t *testing.T) {
			fake := &fakeOutsideBoundary{checks: outsideSuccessfulChecks(), prBody: body}
			service := &Service{}
			installOutsideBoundary(t, service, fake)
			_, refusal := (runtime{Authority: service}).selectedOutsideRepairPullRequest(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeIssueNumber, "CON-86")
			if refusal.Error != nil {
				t.Fatalf("exact token refused: %+v", refusal.Error)
			}
		})
	}
}

func TestOutsideRepairTagRejectsNonCommitObject(t *testing.T) {
	for _, annotated := range []bool{false, true} {
		for _, kind := range []string{"tree", "blob", "tag"} {
			if !annotated && kind == "tag" {
				continue
			}
			t.Run(fmt.Sprintf("annotated=%t/%s", annotated, kind), func(t *testing.T) {
				fake := &fakeOutsideBoundary{annotatedTag: annotated, tagObjectType: kind}
				service := &Service{}
				installOutsideBoundary(t, service, fake)
				sha, refusal := (runtime{Authority: service}).outsideRepairTagCommit(context.Background(), Envelope{}, outsideFakeRepository, outsideFakeTag)
				if refusal.Error == nil || sha != "" {
					t.Fatalf("accepted %s as commit: %s", kind, sha)
				}
			})
		}
	}
}

func outsideHoldInput(expectedVersion int64, mutate func(map[string]any)) map[string]any {
	input := map[string]any{
		"work_id": "work-1", "expected_version": expectedVersion,
		"reason":          "bounded defect repair outside the blocked workflow route",
		"idempotency_key": "outside-hold-1",
	}
	if mutate != nil {
		mutate(input)
	}
	return input
}

func outsideReconcileInput(expectedVersion int64, mutate func(map[string]any)) map[string]any {
	input := map[string]any{
		"work_id": "work-1", "expected_version": expectedVersion,
		"reason":          "bounded defect repair closed from the completed release",
		"mode":            "completed",
		"release_tag":     outsideFakeTag,
		"pull_requests":   []int64{outsideFakeIssueNumber},
		"idempotency_key": "outside-reconcile-1",
	}
	if mutate != nil {
		mutate(input)
	}
	return input
}

func outsideResumeInput(expectedVersion int64, mutate func(map[string]any)) map[string]any {
	input := map[string]any{
		"work_id": "work-1", "expected_version": expectedVersion,
		"reason":          "the operator returns the repair to the managed route",
		"mode":            "resume",
		"idempotency_key": "outside-resume-1",
	}
	if mutate != nil {
		mutate(input)
	}
	return input
}

// outsideApprovalScope mirrors the exact bounded scope the challenge binds:
// the plan defaults (selected Product, ambient Project, scope version) plus
// the derived Product scope for the bound work ids, optionally with the
// completed reconcile's bounded release selectors.
func outsideApprovalScope(env CallEnvelope, selectors func(map[string]any)) map[string]any {
	scope := map[string]any{
		"product_id":    env.SelectedProductID,
		"product_ids":   []string{env.SelectedProductID},
		"project_ids":   []string{env.AmbientProjectID},
		"scope_version": env.ScopeVersion,
		"work_ids":      []string{"work-1"},
	}
	if selectors != nil {
		selectors(scope)
	}
	return scope
}

func dispatchOutsideMints(t *testing.T, s *store.Store, service *Service, env CallEnvelope, operation string, input map[string]any) string {
	t.Helper()
	unapprovedRaw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: operation, Input: unapprovedRaw}, env)
	if response.Error == nil || response.Error.Kind != "approval_required" {
		t.Fatalf("%s without approval = %+v, want the operator challenge", operation, response.Error)
	}
	ref, ok := response.Error.Details["approval_ref"].(string)
	if !ok || ref == "" {
		t.Fatalf("%s challenge carries no approval reference: %+v", operation, response.Error.Details)
	}
	return ref
}

// dispatchOutsideApproved sends the approved submission; the signed host
// assertion binds the challenge to this exact approved input, whose digest
// excludes the approval reference and the idempotency key.
func dispatchOutsideApproved(t *testing.T, s *store.Store, service *Service, env CallEnvelope, operation string, ref string, input map[string]any) Envelope {
	t.Helper()
	withApproval := map[string]any{}
	for key, value := range input {
		withApproval[key] = value
	}
	withApproval["approval"] = map[string]any{"approval_ref": ref}
	approvedRaw, err := json.Marshal(withApproval)
	if err != nil {
		t.Fatal(err)
	}
	scope := outsideApprovalScope(env, outsideReleaseSelectorsFor(operation, input))
	hostEnv := env
	hostEnv.HostApproval = signedHostApproval(mustKey(t), ref, mutationDigest("concord_work_transition", operation, env, approvedRaw), scope, map[string]any{"work": input["expected_version"]}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(ref))
	return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: operation, Input: approvedRaw}, hostEnv)
}

// outsideReleaseSelectorsFor adds the reconcile challenge's bound release
// selectors to the host assertion scope for a completed mode; the hold binds
// none.
func outsideReleaseSelectorsFor(operation string, input map[string]any) func(map[string]any) {
	return func(scope map[string]any) {
		if operation == "outside_repair_reconcile" && input["mode"] == "completed" {
			if tag, ok := input["release_tag"].(string); ok && tag != "" {
				scope["release_tag"] = tag
			}
			if prs, ok := input["pull_requests"].([]int64); ok && len(prs) > 0 {
				strs := make([]string, 0, len(prs))
				for _, number := range prs {
					strs = append(strs, fmt.Sprintf("%d", number))
				}
				scope["pull_requests"] = strs
			}
		}
	}
}

func assertOutsideOK(t *testing.T, response Envelope, what string) Envelope {
	t.Helper()
	if response.Outcome != OutcomeOK || response.Error != nil {
		t.Fatalf("%s = %s / %+v, want ok", what, response.Outcome, response.Error)
	}
	return response
}

func assertOutsideRefusedWith(t *testing.T, response Envelope, what, kind string) {
	t.Helper()
	if response.Error == nil || response.Error.Kind != kind {
		t.Fatalf("%s = %s / %+v, want %s", what, response.Outcome, response.Error, kind)
	}
}

func outsideWorkVersion(t *testing.T, s *store.Store) int64 {
	t.Helper()
	return workVersion(t, s, "work-1")
}

func outsideDispositionState(t *testing.T, s *store.Store) string {
	t.Helper()
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.OutsideRepairDisposition == nil {
		return ""
	}
	return pin.OutsideRepairDisposition.State
}

// outsideHoldCompletes runs the approved hold to version 4 and asserts the
// zero-fetch invariant: a hold never contacts the forge.
func outsideHoldCompletes(t *testing.T, s *store.Store, service *Service, env CallEnvelope, fake *fakeOutsideBoundary) {
	t.Helper()
	input := outsideHoldInput(2, nil)
	ref := dispatchOutsideMints(t, s, service, env, "outside_repair", input)
	assertOutsideOK(t, dispatchOutsideApproved(t, s, service, env, "outside_repair", ref, input), "hold")
	if got := outsideWorkVersion(t, s); got != 4 {
		t.Fatalf("held work version=%d, want 4", got)
	}
	if fake.fetches != 0 {
		t.Fatalf("a hold must not fetch external evidence; the boundary fetched %d times", fake.fetches)
	}
}

// outsideCompletionRefused drives one completed reconcile that must refuse
// with the named typed kind, leaving the hold and version exactly as before.
func outsideCompletionRefused(t *testing.T, s *store.Store, service *Service, env CallEnvelope, fake *fakeOutsideBoundary, input map[string]any, kind, what string) {
	t.Helper()
	ref := dispatchOutsideMints(t, s, service, env, "outside_repair_reconcile", input)
	fetches := fake.fetches
	refused := dispatchOutsideApproved(t, s, service, env, "outside_repair_reconcile", ref, input)
	assertOutsideRefusedWith(t, refused, what, kind)
	if got := outsideWorkVersion(t, s); got != 4 {
		t.Fatalf("%s advanced work version to %d, want 4", what, got)
	}
	if state := outsideDispositionState(t, s); state != store.OutsideRepairStateActive {
		t.Fatalf("%s left disposition state=%q, want active", what, state)
	}
	if fake.fetches == fetches {
		t.Fatalf("%s fetched no completion proof before refusing", what)
	}
}

func resultPin(t *testing.T, response Envelope) map[string]any {
	t.Helper()
	var result struct {
		WorkPins []map[string]any `json:"work_pins"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("mutation result is not readable: %v", err)
	}
	if len(result.WorkPins) != 1 {
		t.Fatalf("mutation result carries %d work pins, want one", len(result.WorkPins))
	}
	return result.WorkPins[0]
}

func TestOutsideRepairHoldRequiresApprovalAndRecordsHold(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	input := outsideHoldInput(2, nil)
	ref := dispatchOutsideMints(t, s, service, env, "outside_repair", input)
	response := assertOutsideOK(t, dispatchOutsideApproved(t, s, service, env, "outside_repair", ref, input), "hold")
	if got := outsideWorkVersion(t, s); got != 4 {
		t.Fatalf("held work version=%d, want 4 (an approval-consuming event advances by 2)", got)
	}
	if fake.fetches != 0 {
		t.Fatalf("a hold must not fetch external evidence; the boundary fetched %d times", fake.fetches)
	}
	disposition, ok := resultPin(t, response)["outside_repair_disposition"].(map[string]any)
	if !ok || disposition["state"] != "active" {
		t.Fatalf("result work pin carries no active outside-repair disposition: %v", resultPin(t, response)["outside_repair_disposition"])
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.OutsideRepairDisposition == nil || pin.OutsideRepairDisposition.State != store.OutsideRepairStateActive {
		t.Fatalf("pin carries no active disposition: %+v", pin.OutsideRepairDisposition)
	}
	if len(pin.OutsideRepairRoute) != 1 || pin.OutsideRepairRoute[0] != "outside_repair_reconcile" {
		t.Fatalf("pin must name the declared reconcile route for completed and resume: %v", pin.OutsideRepairRoute)
	}
	if len(pin.NextValidIntents) != 0 {
		t.Fatalf("held work advertises managed intents: %+v", pin.NextValidIntents)
	}
}

func TestOutsideRepairHoldStaleVersionRefuses(t *testing.T) {
	s, service, env, _ := outsideFixture(t)
	input := outsideHoldInput(5, nil)
	ref := dispatchOutsideMints(t, s, service, env, "outside_repair", input)
	refused := dispatchOutsideApproved(t, s, service, env, "outside_repair", ref, input)
	assertOutsideRefusedWith(t, refused, "stale-version hold", "version_conflict")
	if got := outsideWorkVersion(t, s); got != 2 {
		t.Fatalf("a refused stale hold advanced work version to %d, want 2", got)
	}
}

func TestOutsideRepairReconcileCompletedClosesFromBoundaryEvidence(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	input := outsideReconcileInput(4, nil)
	ref := dispatchOutsideMints(t, s, service, env, "outside_repair_reconcile", input)
	fake.fetches = 0
	response := assertOutsideOK(t, dispatchOutsideApproved(t, s, service, env, "outside_repair_reconcile", ref, input), "completed reconcile")
	if fake.fetches == 0 {
		t.Fatalf("the boundary never fetched the completion proof")
	}
	if got := outsideWorkVersion(t, s); got != 6 {
		t.Fatalf("reconciled work version=%d, want 6", got)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Lifecycle != "completed" {
		t.Fatalf("reconciled lifecycle=%q, want completed", pin.Lifecycle)
	}
	if pin.OutsideRepairDisposition == nil || pin.OutsideRepairDisposition.State != store.OutsideRepairStateCompleted {
		t.Fatalf("reconciled disposition=%+v, want completed", pin.OutsideRepairDisposition)
	}
	evidence := pin.OutsideRepairDisposition.Evidence
	if evidence == nil {
		t.Fatalf("reconciled disposition carries no evidence")
	}
	if evidence.Repository != outsideFakeRepository || evidence.ReleaseSHA != outsideFakeReleaseSHA || evidence.ReleaseTag != outsideFakeTag {
		t.Fatalf("recorded evidence=%+v, want the boundary-authenticated receipt", evidence)
	}
	if len(evidence.PullRequests) != 1 || evidence.PullRequests[0].MergeSHA != outsideFakeMergeSHA || len(evidence.PullRequests[0].RequiredChecks) != 2 {
		t.Fatalf("recorded pull-request evidence=%+v", evidence.PullRequests)
	}
	encoded, err := json.Marshal(resultPin(t, response)["outside_repair_disposition"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"release_sha"`) {
		t.Fatalf("result work pin carries no release evidence: %s", encoded)
	}
}

func TestOutsideRepairReconcileAnnotatedTagResolvesToCommit(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	fake.annotatedTag = true
	fake.releaseCommitSHA = outsideFakeAltSHA
	input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-annotated" })
	ref := dispatchOutsideMints(t, s, service, env, "outside_repair_reconcile", input)
	assertOutsideOK(t, dispatchOutsideApproved(t, s, service, env, "outside_repair_reconcile", ref, input), "annotated-tag reconcile")
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.OutsideRepairDisposition == nil || pin.OutsideRepairDisposition.Evidence == nil || pin.OutsideRepairDisposition.Evidence.ReleaseSHA != outsideFakeAltSHA {
		t.Fatalf("evidence=%+v, want the annotated tag's commit", pin.OutsideRepairDisposition)
	}
}

func TestOutsideRepairReconcileResumeClearsHoldWithoutFetch(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	input := outsideResumeInput(4, nil)
	ref := dispatchOutsideMints(t, s, service, env, "outside_repair_reconcile", input)
	assertOutsideOK(t, dispatchOutsideApproved(t, s, service, env, "outside_repair_reconcile", ref, input), "resume")
	if fake.fetches != 0 {
		t.Fatalf("resume fetched external evidence %d times; resume must not fetch", fake.fetches)
	}
	if got := outsideWorkVersion(t, s); got != 6 {
		t.Fatalf("resumed work version=%d, want 6", got)
	}
	// After the resume the hold is inactive and the fixture work carries no
	// workflow instance, so the work pin is unreadable again; the durable
	// disposition row carries the resumed state, and the lifecycle never
	// closed.
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id='work-1'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle == "completed" {
		t.Fatalf("resume closed the lifecycle")
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM outside_repair_dispositions WHERE work_id='work-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != store.OutsideRepairStateResumed {
		t.Fatalf("resumed disposition state=%s, want resumed", state)
	}
}

func TestOutsideRepairReconcileWithoutHoldRefuses(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	// No active disposition exists, and the reconcile plan reads the held
	// work pin first: both modes refuse before any challenge or effect.
	input := outsideReconcileInput(2, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-no-hold" })
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "outside_repair_reconcile", Input: raw}, env)
	assertOutsideRefusedWith(t, refused, "completed reconcile without hold", "invalid_input")
	resume := outsideResumeInput(2, func(value map[string]any) { value["idempotency_key"] = "outside-resume-no-hold" })
	resumeRaw, err := json.Marshal(resume)
	if err != nil {
		t.Fatal(err)
	}
	refusedResume := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "outside_repair_reconcile", Input: resumeRaw}, env)
	assertOutsideRefusedWith(t, refusedResume, "resume without hold", "invalid_input")
	if fake.fetches != 0 {
		t.Fatalf("a refused reconcile fetched external evidence %d times", fake.fetches)
	}
	if got := outsideWorkVersion(t, s); got != 2 {
		t.Fatalf("refused reconcile advanced work version to %d, want 2", got)
	}
}

func TestOutsideRepairReconcileMissingChecksRefuses(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	fake.noChecks = true
	input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-missing-checks" })
	outsideCompletionRefused(t, s, service, env, fake, input, "missing_evidence", "absent required checks")
}

func TestOutsideRepairReconcileFailingRequiredCheckRefuses(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	fake.checks = []map[string]string{
		{"name": "ci/build", "link": "https://github.com/" + outsideFakeRepository + "/actions/runs/9000000001", "state": "FAILURE", "bucket": "fail"},
	}
	input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-failing-check" })
	outsideCompletionRefused(t, s, service, env, fake, input, "missing_evidence", "a failing required check")
}

func TestOutsideRepairReconcileWrongRepositoryRefuses(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	fake.prURL = "https://github.com/other-owner/other-repo/pull/1339"
	input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-wrong-repo" })
	outsideCompletionRefused(t, s, service, env, fake, input, "unauthorized", "pull request carrying another repository's state")
}

func TestOutsideRepairReconcileUnmergedPullRequestRefuses(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	fake.prState = "OPEN"
	input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-open-state" })
	outsideCompletionRefused(t, s, service, env, fake, input, "missing_evidence", "an unmerged pull request")
}

func TestOutsideRepairReconcileNonAncestorMergeRefuses(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	fake.compareDifference = true
	input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-not-ancestor" })
	outsideCompletionRefused(t, s, service, env, fake, input, "missing_evidence", "a merge the published release does not contain")
}

func TestOutsideRepairReconcileDraftReleaseRefuses(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	fake.releaseJSON = fmt.Sprintf(`{"tagName":"%s","isDraft":true,"isPrerelease":false,"url":"https://github.com/%s/releases/tag/%s","publishedAt":"2026-08-07T11:00:00Z","targetCommitish":"main"}`, outsideFakeTag, outsideFakeRepository, outsideFakeTag)
	input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-draft" })
	outsideCompletionRefused(t, s, service, env, fake, input, "missing_evidence", "a draft release")
}

func TestOutsideRepairReconcileUnpublishedTagRefuses(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	input := outsideReconcileInput(4, func(value map[string]any) {
		value["release_tag"] = "v11.63.35"
		value["idempotency_key"] = "outside-reconcile-unpublished"
	})
	outsideCompletionRefused(t, s, service, env, fake, input, "missing_evidence", "an unpublished release tag")
}

func TestOutsideRepairReconcilePRBodyWithoutLinearRefRefuses(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	fake.prBody = "A pull request body with no issue linkage."
	input := outsideReconcileInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-reconcile-no-linkage" })
	outsideCompletionRefused(t, s, service, env, fake, input, "missing_evidence", "a pull request body without the Linear ref")
}

func TestOutsideRepairReconcileTooManyPullRequestsRefuses(t *testing.T) {
	s, service, env, _ := outsideFixture(t)
	prs := make([]int64, 33)
	for index := range prs {
		prs[index] = int64(index + 1)
	}
	input := outsideReconcileInput(2, func(value map[string]any) { value["pull_requests"] = prs })
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	_, refusalErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "outside_repair_reconcile", Input: raw}, env)
	if refusalErr == nil || !strings.Contains(refusalErr.Error(), "pull_requests") {
		t.Fatalf("33 pull requests = (%v), want an input refusal naming pull_requests", refusalErr)
	}
}

func TestOutsideRepairIdempotentReplayReplaysRecordedResult(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	input := outsideReconcileInput(4, nil)
	ref := dispatchOutsideMints(t, s, service, env, "outside_repair_reconcile", input)
	assertOutsideOK(t, dispatchOutsideApproved(t, s, service, env, "outside_repair_reconcile", ref, input), "first reconcile")
	replayed := dispatchOutsideApproved(t, s, service, env, "outside_repair_reconcile", ref, input)
	if replayed.Error != nil {
		t.Fatalf("replayed reconcile refused: %+v", replayed.Error)
	}
	if !replayed.Replayed {
		t.Fatalf("replayed reconcile is not marked replayed: %+v", replayed)
	}
	if got := outsideWorkVersion(t, s); got != 6 {
		t.Fatalf("replayed result advanced work version to %d, want 6", got)
	}
}

func TestOutsideRepairApprovalBindsExactOperation(t *testing.T) {
	s, service, env, fake := outsideFixture(t)
	outsideHoldCompletes(t, s, service, env, fake)
	var consumedRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT approval_ref FROM outside_repair_dispositions WHERE work_id='work-1'`).Scan(&consumedRef); err != nil {
		t.Fatal(err)
	}
	// The hold's one-use approval is consumed; a second hold under the same
	// ref carries a different operation digest, and its approval binding
	// refuses before any effect.
	input := outsideHoldInput(4, func(value map[string]any) { value["idempotency_key"] = "outside-hold-second" })
	unapprovedRaw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	challenged := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "outside_repair", Input: unapprovedRaw}, env)
	ref := challenged.Error.Details["approval_ref"].(string)
	if ref == "" {
		t.Fatalf("second hold minted no challenge")
	}
	refused := dispatchOutsideConsumedApproval(t, s, service, env, "outside_repair", consumedRef, input)
	assertOutsideRefusedWith(t, refused, "approval reused for another operation", "approval_invalid")
	if got := outsideWorkVersion(t, s); got != 4 {
		t.Fatalf("reused approval advanced work version to %d, want 4", got)
	}
	if state := outsideDispositionState(t, s); state != store.OutsideRepairStateActive {
		t.Fatalf("reused approval moved disposition to %q, want active", state)
	}
	if pin, err := store.ReadWorkPin(context.Background(), s, "work-1"); err != nil {
		t.Fatal(err)
	} else if pin.OutsideRepairDisposition == nil || pin.OutsideRepairDisposition.State != store.OutsideRepairStateActive {
		t.Fatalf("disposition=%+v after refused reuse", pin.OutsideRepairDisposition)
	}
}

// dispatchOutsideConsumedApproval dispatches the approved submission with a
// consumed approval ref so its one-use binding refuses the second consequence.
func dispatchOutsideConsumedApproval(t *testing.T, s *store.Store, service *Service, env CallEnvelope, operation string, consumedRef string, input map[string]any) Envelope {
	t.Helper()
	return dispatchOutsideApproved(t, s, service, env, operation, consumedRef, input)
}
