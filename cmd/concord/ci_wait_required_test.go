package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The required-context fixtures follow the GitHub REST shapes: the branch
// rules endpoint read with --paginate --slurp answers an array of rule pages,
// and the branch endpoint carries a protection summary with
// required_status_checks even when the branch is unprotected.

func ciWaitRulesAnswer(contexts ...string) string {
	rules := []map[string]any{{"type": "merge_queue", "parameters": map[string]any{"merge_method": "SQUASH"}}}
	if len(contexts) > 0 {
		checks := make([]map[string]any, 0, len(contexts))
		for _, name := range contexts {
			checks = append(checks, map[string]any{"context": name, "integration_id": 1})
		}
		rules = append(rules, map[string]any{"type": "required_status_checks", "parameters": map[string]any{
			"strict_required_status_checks_policy": false, "required_status_checks": checks,
		}})
	}
	encoded, _ := json.Marshal([][]map[string]any{rules})
	return string(encoded)
}

func ciWaitBranchAnswer(contexts ...string) string {
	checks := make([]map[string]any, 0, len(contexts))
	for _, name := range contexts {
		checks = append(checks, map[string]any{"context": name, "app_id": 1})
	}
	if contexts == nil {
		contexts = []string{}
	}
	encoded, _ := json.Marshal(map[string]any{"name": "main", "protected": len(contexts) > 0, "protection": map[string]any{
		"enabled": len(contexts) > 0,
		"required_status_checks": map[string]any{
			"enforcement_level": "non_admins", "contexts": contexts, "checks": checks,
		},
	}})
	return string(encoded)
}

// ciWaitRequiredHead encodes a `gh pr view` body on the named base branch.
func ciWaitRequiredHead(t *testing.T, base string, rollup []ghPRRollup) string {
	t.Helper()
	encoded, err := json.Marshal(rollup)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"headRefOid":"aabbccdd","baseRefName":%q,"url":"https://github.com/o/r/pull/5","state":"OPEN","mergedAt":"","mergeStateStatus":"BLOCKED","statusCheckRollup":%s}`, base, encoded)
}

// ciWaitRequiredFixture stubs gh for checks-mode polls. The pr view body is
// read from a file, so a test can change the base between polls. An empty
// rules or branch answer makes that read fail. Every gh invocation is
// appended to the returned capture file.
type ciWaitRequiredFixture struct {
	head    string
	capture string
}

func newCiWaitRequiredFixture(t *testing.T, headJSON, rules, branch, checks string) ciWaitRequiredFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := ciWaitRequiredFixture{head: filepath.Join(dir, "head.json"), capture: filepath.Join(dir, "args")}
	fixture.setHead(t, headJSON)
	answer := func(body string) string {
		if body == "" {
			return "echo 'HTTP 403: Resource not accessible by integration' >&2; exit 1"
		}
		return "cat <<'EOA'\n" + body + "\nEOA"
	}
	ghStubPath(t, ghStubDir(t, `echo "$*" >> "`+fixture.capture+`"
case "$*" in
	*"pr view"*) cat "`+fixture.head+`";;
	*"pr checks"*) `+answer(checks)+`
;;
	*"api repos/o/r/rules/branches/"*) `+answer(rules)+`
;;
	*"api repos/o/r/branches/"*) `+answer(branch)+`
;;
	*"/actions/runs?"*) echo '[{"total_count":0,"workflow_runs":[]}]';;
	*) exit 97;;
esac`))
	return fixture
}

func (f ciWaitRequiredFixture) setHead(t *testing.T, headJSON string) {
	t.Helper()
	if err := os.WriteFile(f.head, []byte(headJSON), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f ciWaitRequiredFixture) calls(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(f.capture)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ciWaitChecksState() *ciWaitState {
	return &ciWaitState{
		SchemaVersion: 1,
		Selector:      ciWaitSelector{Kind: "pr", Value: "5"},
		Repo:          "o/r",
		BudgetSeconds: ciWaitDefaultBudget,
		StartedAt:     time.Now().UTC(),
		DeadlineAt:    time.Now().UTC().Add(time.Hour),
		Mode:          "checks",
	}
}

func TestCiWaitPRChecksModeWaitsForRequiredContexts(t *testing.T) {
	failJob := "https://github.com/o/r/actions/runs/10/job/2"
	for _, tc := range []struct {
		name        string
		rollup      []ghPRRollup
		rules       string
		branch      string
		wantStatus  string
		wantMissing []string
	}{
		{
			name:        "a context only a ruleset requires",
			rollup:      []ghPRRollup{ciWaitRollupPass("verify-go")},
			rules:       ciWaitRulesAnswer("verify-go", "ruleset-gate"),
			branch:      ciWaitBranchAnswer(),
			wantStatus:  "pending",
			wantMissing: []string{"ruleset-gate"},
		},
		{
			name:        "a context only classic protection requires",
			rollup:      []ghPRRollup{ciWaitRollupPass("verify-go")},
			rules:       ciWaitRulesAnswer(),
			branch:      ciWaitBranchAnswer("verify-go", "classic-gate"),
			wantStatus:  "pending",
			wantMissing: []string{"classic-gate"},
		},
		{
			name:        "both sources missing contexts",
			rollup:      []ghPRRollup{ciWaitRollupPass("verify-go")},
			rules:       ciWaitRulesAnswer("ruleset-gate"),
			branch:      ciWaitBranchAnswer("classic-gate", "Ruleset-Gate"),
			wantStatus:  "pending",
			wantMissing: []string{"ruleset-gate", "classic-gate"},
		},
		{
			name:       "required contexts that passed",
			rollup:     []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupPass("ruleset-gate")},
			rules:      ciWaitRulesAnswer("ruleset-gate"),
			branch:     ciWaitBranchAnswer("Verify-Go"),
			wantStatus: "success",
		},
		{
			name:       "a required context that failed",
			rollup:     []ghPRRollup{ciWaitRollupPass("verify-go"), ciWaitRollupFail("ruleset-gate", failJob)},
			rules:      ciWaitRulesAnswer("ruleset-gate"),
			branch:     ciWaitBranchAnswer("verify-go"),
			wantStatus: "failure",
		},
		{
			name:       "no required context",
			rollup:     []ghPRRollup{ciWaitRollupPass("verify-go")},
			rules:      ciWaitRulesAnswer(),
			branch:     ciWaitBranchAnswer(),
			wantStatus: "success",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newCiWaitRequiredFixture(t, ciWaitRequiredHead(t, "main", tc.rollup), tc.rules, tc.branch, "")
			report, terminal, err := ciWaitPollPR(context.Background(), ciWaitChecksState())
			if err != nil {
				t.Fatalf("ciWaitPollPR errored: %v", err)
			}
			if report.Status != tc.wantStatus || terminal != (tc.wantStatus != "pending") {
				t.Fatalf("status=%s terminal=%t want %s (report=%+v)", report.Status, terminal, tc.wantStatus, report)
			}
			if len(tc.wantMissing) == 0 {
				return
			}
			if report.Checks == nil || report.Checks.Total != len(tc.rollup)+len(tc.wantMissing) || report.Checks.Pending != len(tc.wantMissing) {
				t.Fatalf("each missing required context must count as pending, got %+v", report.Checks)
			}
			buckets := make(map[string]string, len(report.Checks.Entries))
			for _, entry := range report.Checks.Entries {
				buckets[entry.Name] = entry.Bucket
			}
			for _, name := range tc.wantMissing {
				if buckets[name] != "pending" || !strings.Contains(report.Reason, name) {
					t.Fatalf("missing context %q must be a pending entry named in the reason, got reason=%q entries=%+v", name, report.Reason, report.Checks.Entries)
				}
			}
		})
	}
}

func TestCiWaitPRChecksModeRequiredContextAtTheFullPage(t *testing.T) {
	// At the full rollup page the population joins the paginated snapshot, so
	// a required context reported only there is observed, not missing.
	rollup := make([]ghPRRollup, ciWaitPRRollupPageSize)
	for i := range rollup {
		rollup[i] = ciWaitRollupPass(fmt.Sprintf("check-%d", i))
	}
	snapshot := `[{"name":"late-gate","state":"SUCCESS","link":"https://github.com/o/r/actions/runs/11/job/1","bucket":"pass"}]`
	newCiWaitRequiredFixture(t, ciWaitRequiredHead(t, "main", rollup), ciWaitRulesAnswer("late-gate"), ciWaitBranchAnswer(), snapshot)
	report, terminal, err := ciWaitPollPR(context.Background(), ciWaitChecksState())
	if err != nil || !terminal || report.Status != "success" {
		t.Fatalf("a required context in the paginated snapshot must count as observed, got status=%s terminal=%t err=%v reason=%q", report.Status, terminal, err, report.Reason)
	}
	if report.Checks == nil || report.Checks.Total != ciWaitPRRollupPageSize+1 {
		t.Fatalf("the population must hold the page and the snapshot entry, got %+v", report.Checks)
	}
}

func TestCiWaitPRChecksModeReadsRequiredContextsOncePerBase(t *testing.T) {
	pending := []ghPRRollup{ciWaitRollupPending("verify-go")}
	fixture := newCiWaitRequiredFixture(t, ciWaitRequiredHead(t, "main", pending), ciWaitRulesAnswer("verify-go"), ciWaitBranchAnswer(), "")
	state := ciWaitChecksState()
	if _, _, err := ciWaitPollPR(context.Background(), state); err != nil {
		t.Fatalf("first poll errored: %v", err)
	}
	// The next slice resumes from the state file.
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var resumed ciWaitState
	if err := json.Unmarshal(encoded, &resumed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ciWaitPollPR(context.Background(), &resumed); err != nil {
		t.Fatalf("second poll errored: %v", err)
	}
	calls := fixture.calls(t)
	for _, endpoint := range []string{"api repos/o/r/rules/branches/main?per_page=100 --method GET --paginate --slurp", "api repos/o/r/branches/main --method GET"} {
		if got := strings.Count(calls, endpoint); got != 1 {
			t.Fatalf("%q must be read once across two slices, got %d, calls=%q", endpoint, got, calls)
		}
	}

	// A changed base re-reads both sources, escaping each path segment.
	fixture.setHead(t, ciWaitRequiredHead(t, "release/1#2", pending))
	if _, _, err := ciWaitPollPR(context.Background(), &resumed); err != nil {
		t.Fatalf("third poll errored: %v", err)
	}
	calls = fixture.calls(t)
	for _, endpoint := range []string{"api repos/o/r/rules/branches/release/1%232?per_page=100", "api repos/o/r/branches/release/1%232 --method GET"} {
		if !strings.Contains(calls, endpoint) {
			t.Fatalf("a base change must re-read %q, calls=%q", endpoint, calls)
		}
	}
	if resumed.RequiredBase != "release/1#2" {
		t.Fatalf("the cache must name the base it was read for, got %q", resumed.RequiredBase)
	}
}

func TestCiWaitPRChecksModeRequiredReadFailureIsError(t *testing.T) {
	passing := []ghPRRollup{ciWaitRollupPass("verify-go")}
	for _, tc := range []struct {
		name   string
		base   string
		rules  string
		branch string
	}{
		{"no base branch", "", ciWaitRulesAnswer(), ciWaitBranchAnswer()},
		{"a failed ruleset read", "main", "", ciWaitBranchAnswer()},
		{"a failed protection read", "main", ciWaitRulesAnswer(), ""},
		{"an unreadable ruleset answer", "main", `{"message":"Not Found"}`, ciWaitBranchAnswer()},
		{"a ruleset with an unreadable check list", "main", `[[{"type":"required_status_checks","parameters":{"required_status_checks":"verify-go"}}]]`, ciWaitBranchAnswer()},
		{"a branch without a protection summary", "main", ciWaitRulesAnswer(), `{"name":"main","protected":true}`},
		{"a protection summary without required status checks", "main", ciWaitRulesAnswer(), `{"name":"main","protected":true,"protection":{"enabled":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newCiWaitRequiredFixture(t, ciWaitRequiredHead(t, tc.base, passing), tc.rules, tc.branch, "")
			state := ciWaitChecksState()
			report, terminal, err := ciWaitPollPR(context.Background(), state)
			if err == nil || terminal {
				t.Fatalf("a failed required-context read must be an error, got status=%s terminal=%t err=%v", report.Status, terminal, err)
			}
			if state.RequiredBase != "" {
				t.Fatalf("a failed read must not cache, got base %q", state.RequiredBase)
			}
		})
	}
}

func TestCiWaitPRChecksModeStalledRequiredReadIsPending(t *testing.T) {
	head := ciWaitRequiredHead(t, "main", []ghPRRollup{ciWaitRollupPass("verify-go")})
	ghStubPath(t, ghStubDir(t, `case "$*" in
	*"pr view"*) cat <<'EOC'
`+head+`
EOC
;;
	*"rules/branches/"*) sleep 500;;
	*"/actions/runs?"*) echo '[{"total_count":0,"workflow_runs":[]}]';;
	*) exit 97;;
esac`))
	saved := ciWaitCommandTimeout
	ciWaitCommandTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ciWaitCommandTimeout = saved })
	code, report := runCiWaitStdin(t, ciWaitJSON(t, ciWaitRequest{
		Selector: &ciWaitSelector{Kind: "pr", Value: "5"}, Repo: "o/r", TimeSecondsMax: ciWaitBudget(8),
	}))
	if code != 0 || report.Status != "pending" || !strings.Contains(report.Reason, "stalled") || !strings.Contains(report.Reason, "rules/branches/main") {
		t.Fatalf("a stalled required-context read must be pending and name the endpoint, got code=%d report=%+v", code, report)
	}
}
