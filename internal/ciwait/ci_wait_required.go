package ciwait

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// A required check that has not registered on the head is absent from every
// population source, so a complete-looking set can still lack it. Checks mode
// therefore reads what the base branch requires and counts each required
// context it has not observed as pending (CD-0233). GitHub keeps requiredness
// in two places, rulesets and classic branch protection, and the wait reads
// both.

// ghBranchRule is one active rule of the branch rules endpoint. Parameters
// stay raw until the rule type is known, because each type has its own shape.
type ghBranchRule struct {
	Type       string          `json:"type"`
	Parameters json.RawMessage `json:"parameters"`
}

type ghRequiredStatusChecksRule struct {
	RequiredStatusChecks []struct {
		Context string `json:"context"`
	} `json:"required_status_checks"`
}

// ghBranch is the branch endpoint's protection summary. GitHub returns it, with
// required_status_checks, for an unprotected branch too, so an absent summary
// or an absent required_status_checks is an unreadable answer.
type ghBranch struct {
	Protection *struct {
		RequiredStatusChecks *struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
			} `json:"checks"`
		} `json:"required_status_checks"`
	} `json:"protection"`
}

// ciWaitRequiredContexts returns the status check contexts the base branch
// requires: the union of its active rulesets and its classic protection. The
// first checks poll for a base reads both sources and caches the union in the
// state, so later slices read again only after a base change. A failed read
// is an error: completeness cannot be judged without it.
func ciWaitRequiredContexts(ctx context.Context, state *ciWaitState, base string) ([]string, error) {
	if base == "" {
		return nil, errors.New("gh pr view returned no base branch")
	}
	if state.RequiredBase == base {
		return state.RequiredContexts, nil
	}
	branch := ciWaitEscapeBranch(base)
	rulesBody, err := ciWaitGH(ctx, "api", "repos/"+state.Repo+"/rules/branches/"+branch+"?per_page=100",
		"--method", "GET", "--paginate", "--slurp")
	if err != nil {
		return nil, err
	}
	var pages [][]ghBranchRule
	if err := ciWaitDecode(rulesBody, &pages); err != nil {
		return nil, err
	}
	branchBody, err := ciWaitGH(ctx, "api", "repos/"+state.Repo+"/branches/"+branch, "--method", "GET")
	if err != nil {
		return nil, err
	}
	var protected ghBranch
	if err := ciWaitDecode(branchBody, &protected); err != nil {
		return nil, err
	}
	if protected.Protection == nil || protected.Protection.RequiredStatusChecks == nil {
		return nil, fmt.Errorf("gh api repos/%s/branches/%s returned no required status checks summary", state.Repo, branch)
	}

	var required []string
	seen := make(map[string]bool)
	add := func(name string) {
		key := ciWaitCheckKey(name)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		required = append(required, strings.TrimSpace(name))
	}
	for _, page := range pages {
		for _, rule := range page {
			if rule.Type != "required_status_checks" {
				continue
			}
			var parameters ghRequiredStatusChecksRule
			if err := ciWaitDecode(rule.Parameters, &parameters); err != nil {
				return nil, err
			}
			for _, check := range parameters.RequiredStatusChecks {
				add(check.Context)
			}
		}
	}
	classic := protected.Protection.RequiredStatusChecks
	for _, name := range classic.Contexts {
		add(name)
	}
	for _, check := range classic.Checks {
		add(check.Context)
	}
	state.RequiredBase = base
	state.RequiredContexts = required
	return required, nil
}

// ciWaitEscapeBranch escapes each segment of a branch name for an API path,
// so a branch name can never add a query, a fragment, or a gh placeholder.
func ciWaitEscapeBranch(branch string) string {
	segments := strings.Split(branch, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// ciWaitJoinRequired adds each required context the population does not name
// as a pending entry, and returns the contexts it added.
func ciWaitJoinRequired(population []ciWaitCheck, required []string) ([]ciWaitCheck, []string) {
	observed := make(map[string]bool, len(population))
	for _, check := range population {
		observed[ciWaitCheckKey(check.Name)] = true
	}
	var missing []string
	for _, name := range required {
		if observed[ciWaitCheckKey(name)] {
			continue
		}
		missing = append(missing, name)
		population = append(population, ciWaitCheck{Name: name, Bucket: "pending"})
	}
	return population, missing
}
