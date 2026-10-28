# CD-0168: The ci-wait PR rollup query follows the page boundary

- **Status:** Accepted
- **Date:** 2026-09-21
- **Scope:** The `ci-wait` CLI verb: its PR check population, its poll interval, and its classification of a stalled `gh` command
- **Approval:** The operator approved dropping the second query, staging the interval, and reclassifying a stalled command as pending, on 2026-09-21.
- **Related:** CD-0160, CD-0161
- **Preserves:** The bounded slice, the state-carried deadline, and the closed report status set

## Context

CD-0160 D4 gave the PR selector one query: `gh pr checks`. A later repair added
the pull request status-check rollup to the same poll, because a `gh pr checks`
snapshot taken in the first minutes of a pull request holds a nonzero,
zero-pending subset that the gate read as the whole set. The PR path then made
two GitHub GraphQL round trips for every checks-mode poll.

The second round trip buys nothing below one boundary. The `gh` implementation
builds `gh pr checks` from the same `statusCheckRollup` the rollup query
returns, and the union helper overwrites each same-named snapshot entry with
its rollup entry. One boundary is real: the rollup query caps its first page at
100 contexts and sends no cursor, while `gh pr checks` pages until the rollup
is exhausted. At exactly 100 contexts the rollup may be a truncated page.

Two other costs sat beside it. The poll interval was a fixed 15 seconds against
a 1800-second budget, so one wait could issue about 120 iterations at a cadence
tuned for a six-minute check run. And a `gh` command that reached its 45-second
timeout returned a terminal `error`, which discarded a wait the state file
could have resumed, and pushed the caller into a fresh wait that spent the
requests again.

## Decision

### D1. The rollup supplies checks, and the page cap requires pagination

Checks mode builds its rollup check population from the
`gh pr view` status-check rollup. It queries `gh pr checks` only when that
rollup holds exactly the full 100-context page, where pagination still decides
the rollup set. Merge mode uses the rollup already read without a second
`gh pr checks` population query. Both modes apply the common classification
and reporting rule in
[CD-0228 D1](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md#d1-common-classification-and-reporting-rule),
including its workflow-run and job reads. Non-Actions rollup identities and
attempt ordering follow CD-0228 D2. A full rollup page can establish an
observed failure without establishing population completeness. Without a
relevant failure, an empty population stays pending.
Under [CD-0233](CD-0233-a-checks-wait-waits-for-every-required-context.md),
checks mode then adds each required context the population lacks as a
pending entry. That read is not a population query.

### D2. The poll interval stages on the wait's elapsed time

The interval is 15 seconds for the first minute of the wait, 30 seconds until
five minutes, and 60 seconds after that. It derives from the elapsed time the
CD-0160 D3 state file already carries, not from the slice, so a resumed wait
keeps the cadence its age earns instead of restarting at the tightest one. The
CD-0160 D2 bounded slice and its shutdown margin are unchanged.

### D3. A stalled command is pending, not an error

A `gh` command that reaches its command timeout is retried once after two
seconds. If the retry also times out, the poll returns `pending` with a reason
naming the stalled command, and the wait resumes through the existing state
file. A transport, authentication, or decode failure that is not a timeout
still returns `error`. A stall is the absence of an observation, so it carries
no verdict about the checks.

## Verification

- `TestCiWaitChecksModePollsOnceBelowThePageCap` proves a short rollup decides
  the poll without the `gh pr checks` query.
- `TestCiWaitFullRollupPageStillReadsThePaginatedSource` proves a full
  100-context page still queries the paginated source.
- `internal/ciwait.TestCiWaitPRMergeModeNewestAttempts` proves merge mode selects
  the newest non-Actions rollup attempt without a `gh pr checks` query.
- [CD-0228 Verification](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md#verification)
  names the required fail-fast, queued-workflow and newest-workflow-attempt
  checks beyond the rollup population.
- `TestCiWaitPollIntervalStagesOnElapsedWaitTime` proves the interval stages at
  the one-minute and five-minute boundaries.
- `TestCiWaitTimedOutCommandRetriesOnceThenReportsPending` proves a timed-out
  command retries once and then reports pending rather than error.
- `TestCiWaitPRPartialPopulationNeverSucceeds` and
  `TestCiWaitPRMergeModeCleanCanMergeIsNotSuccess` prove the terminal rules
  this record preserves.
