# CD-0233: Checks-mode success waits for every required context

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** Checks-mode completeness in `concord ci-wait` for the PR selector
- **Preserves:** The `DIRTY` rule, the rollup page boundary, merge mode, head
  supersession, merged and closed precedence, bounded slices, carried
  deadlines, and the closed report statuses
- **Related:** [CD-0228](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md),
  [CD-0232](CD-0232-a-merge-wait-ends-when-the-merge-queue-removes-the-pull-request.md)
- **Approval:** The requesting coordinator accepted the design on 2026-10-17,
  including the fail-closed read and its stated evidence limits. It ruled the
  record not serious under P12, because it holds a partial green set open
  inside one Domain with no new status, input, or report field.

## Context

Checks mode reports `success` when every observed check is terminal and none
failed. A check that has not registered on the head yet is absent from every
population source. A partial green set can therefore look complete, and the
wait reports `success` before a required check starts.

GitHub keeps requiredness in two places, and a repository can use either. The
[branch rules endpoint](https://docs.github.com/en/rest/repos/rules) returns
the active ruleset rules for a branch. A `required_status_checks` rule lists
its contexts. The [branch endpoint](https://docs.github.com/en/rest/branches/branches)
returns a `protection` summary whose `required_status_checks` lists the
classic protection contexts. The branch endpoint returns that summary, with
empty lists, for an unprotected branch too: `gh api
repos/octocat/Hello-World/branches/master` answers `"protected": false` with
`protection.required_status_checks` present.

## Decision

### D1. Required contexts join the checks population as pending

Checks mode applies the subject qualification and classification rule in
[CD-0228 D1](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md#d1-common-classification-and-reporting-rule).
Missing contexts do not delay an observed relevant failure. To judge
completeness, checks mode reads the required contexts of the pull request's
base branch. The set is the union of both sources:

- `gh api repos/<repo>/rules/branches/<base>?per_page=100 --method GET --paginate --slurp`,
  every `required_status_checks` rule;
- `gh api repos/<repo>/branches/<base> --method GET`, the `contexts` and
  `checks` of `protection.required_status_checks`.

The base name is escaped per path segment. Each required context that no
population entry names, under the existing case-insensitive name identity,
joins the population as a `pending` entry. The join runs on the merged
population, so at the full rollup page the paginated snapshot counts too.
The report reason names the missing contexts, and `checks.entries` can name a
context nobody observed. CD-0228 D1 governs failure and success; a branch with
no required context still needs its complete applicable workflow population
before success. Requiredness discovery, caching and failed-read handling remain
as defined here.

### D2. The read is cached per base

The state file caches the union with the base it was read for. A later slice
reads both sources again only when the base changes. The cost is two REST
reads per base branch, plus any further ruleset pages, not per poll.

### D3. A failed read fails closed

A `gh` error on either read, an unreadable answer, a branch answer without a
`protection` summary or without its `required_status_checks`, or a pull
request without a base is an `error` report under CD-0160 D7. Completeness
cannot be judged without the read, so the wait never guesses. A stalled read
is `pending` under CD-0168 D3. A failed read caches nothing.

### D4. Merge mode reads no requiredness

Merge mode keeps
[CD-0228](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md)
D2: it adds no requiredness read.

## Alternatives considered

- Read only rulesets, or only classic protection. A repository that uses the
  other source would keep reporting partial success.
- Read requiredness only when the observed set looks complete. The read
  would move between polls, and a cache keyed by base already bounds the cost.
- Treat a failed read as no required context. A token without access would
  silently restore the defect this record repairs.
- Match the integration or app identity of each required check. The rollup
  export carries no app identity, so the wait matches the context name only.

## Consequences

When completeness requires the requiredness read, a token that cannot read
the rules or branch endpoints ends the checks wait as `error`. A branch answer
without `protection.required_status_checks` also fails closed instead of reporting a
partial success. Whether every token sees the classic contexts was not
verified: a token that received emptied lists would pass silently.

A required context that another app reports under the same name satisfies the
requirement here. A ruleset rule can name the integration the status must
originate from, and then GitHub does not accept that report. A required
context that never reports leaves a wait with no relevant failure pending
until the deadline.

## Verification

- `cmd/concord.TestCiWaitPRChecksModeWaitsForRequiredContexts` proves that a
  context required only by a ruleset or only by classic protection holds the
  wait. It also proves required pass, required failure, and no required
  context. [CD-0228 Verification](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md#verification)
  also requires fail-fast and queued-workflow checks: missing contexts cannot
  delay failure, and their presence alone cannot establish success.
- `cmd/concord.TestCiWaitPRChecksModeRequiredContextAtTheFullPage` proves the
  paginated snapshot counts as observed.
- `cmd/concord.TestCiWaitPRChecksModeReadsRequiredContextsOncePerBase` proves
  D2 and the per-segment escape.
- `cmd/concord.TestCiWaitPRChecksModeRequiredReadFailureIsError` and
  `cmd/concord.TestCiWaitPRChecksModeStalledRequiredReadIsPending` prove D3.
- `cmd/concord.TestCiWaitPRMergeModeQueueRemovalEndsTheWait` proves merge mode
  reads no requiredness source.
