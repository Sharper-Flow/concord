package workflowproof

import (
	"context"
	"time"
)

// RequiredCheck records a successful required check on the PR's immutable
// head. Native identities distinguish checks within one run and preserve the
// exact attempt the collector authenticated.
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

// Evidence records authenticated merged PRs, complete required checks and a
// published release containing every merge. The store validates and preserves
// this receipt without contacting the forge or inferring ancestry from strings.
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
