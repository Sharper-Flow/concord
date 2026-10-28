// Package workflowproof collects authenticated outside-repair completion evidence
// and owns its receipt model.
package workflowproof

import (
	"context"
	"time"
)

// RequiredCheck records a successful required check on the PR's
// immutable head, not a check name or a caller-supplied success boolean. The
// native identities name the exact check-run, workflow run and job the
// boundary authenticated (CD-0210 D2), so distinct checks inside one run stay
// distinguishable and a later rerun cannot rewrite what the receipt proves.
type RequiredCheck struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	CommitSHA  string `json:"commit_sha"`
	Conclusion string `json:"conclusion"`
	CheckRunID int64  `json:"check_run_id"`
	RunID      int64  `json:"run_id"`
	JobID      int64  `json:"job_id"`
}

type PullRequestEvidence struct {
	URL            string          `json:"url"`
	Number         int64           `json:"number"`
	HeadSHA        string          `json:"head_sha"`
	MergeSHA       string          `json:"merge_sha"`
	MergedAt       time.Time       `json:"merged_at"`
	RequiredChecks []RequiredCheck `json:"required_checks"`
}

// Evidence is supplied only by the authenticated external boundary.
// That boundary must fetch the merged PRs, the complete required-check set and
// published release, and prove every merge is an ancestor of ReleaseSHA. The
// store validates and preserves the receipt; it does not contact the forge or
// infer ancestry from strings. AuthorityRef and ObservedAt identify the receipt.
type Evidence struct {
	AuthorityRef string                `json:"authority_ref"`
	ObservedAt   time.Time             `json:"observed_at"`
	Repository   string                `json:"repository"`
	PullRequests []PullRequestEvidence `json:"pull_requests"`
	ReleaseTag   string                `json:"release_tag"`
	ReleaseURL   string                `json:"release_url"`
	ReleaseSHA   string                `json:"release_sha"`
	PublishedAt  time.Time             `json:"published_at"`
}

// Request carries admitted selectors and the normalized registered repository.
// Command receives exact argv, including the fixed gh executable, without a
// shell. Now is read only after all proof has been collected successfully.
type Request struct {
	Repository     string
	PullRequests   []int64
	ReleaseTag     string
	LinearIssueKey string
	AuthorityRef   string
	Command        func(context.Context, ...string) (string, error)
	Now            func() time.Time
}

// Refusal distinguishes missing proof from a foreign repository identity.
// Its private discriminator keeps the two outcomes closed to callers.
type Refusal struct {
	detail          string
	foreignIdentity bool
}

func (r *Refusal) Error() string { return r.detail }

// ForeignIdentity reports whether authenticated evidence names another repository.
func (r *Refusal) ForeignIdentity() bool { return r.foreignIdentity }
