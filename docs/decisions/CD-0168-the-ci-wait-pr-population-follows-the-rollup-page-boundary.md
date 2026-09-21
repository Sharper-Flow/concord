# CD-0168: The ci-wait PR population follows the rollup page boundary

- **Status:** Accepted
- **Date:** 2026-09-21
- **Scope:** The `ci-wait` CLI verb: its PR check population, its poll interval, and its classification of a stalled `gh` command
- **Approval:** The operator approved dropping the second query, staging the interval, and reclassifying a stalled command as pending, on 2026-09-21.
- **Related:** CD-0160, CD-0161
- **Preserves:** The bounded slice, the state-carried deadline, the closed report status set, and the checks-mode and merge-mode terminal rules

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

### D1. The rollup is the population, and the page cap is the exception

This record amends CD-0160 D4. Checks mode builds its check population from the
`gh pr view` status-check rollup. It queries `gh pr checks` only when that
rollup holds exactly the full 100-context page, where pagination still decides
the set. Merge mode is unchanged, because it answers before the check query.
An empty population stays pending, and the terminal rules for checks mode and
merge mode are untouched.

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
- `TestCiWaitPollIntervalStagesOnElapsedWaitTime` proves the interval stages at
  the one-minute and five-minute boundaries.
- `TestCiWaitTimedOutCommandRetriesOnceThenReportsPending` proves a timed-out
  command retries once and then reports pending rather than error.
- `TestCiWaitPRPartialPopulationNeverSucceeds` and
  `TestCiWaitPRMergeModeCleanCanMergeIsNotSuccess` prove the terminal rules
  this record preserves.
