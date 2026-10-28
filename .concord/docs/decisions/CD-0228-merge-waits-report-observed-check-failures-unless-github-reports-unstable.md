# CD-0228: CI waits report observed failures before the full population completes

- **Status:** Accepted
- **Date:** 2026-10-15
- **Scope:** Common classification and failure reporting in `concord ci-wait`
- **Preserves:** Checks-mode completeness, head supersession, merged and closed
  precedence, bounded slices, carried deadlines, and the closed report statuses
- **Related:** [CD-0160](CD-0160-the-ci-wait-utility-delegates-its-wait-to-a-deterministic-cli-verb.md)
- **Approval:** The operator approved the exact common classification and
  reporting rule.

## Context

A relevant failed check or job is negative evidence even while its peers are
pending. Waiting for the full population delays repair without changing that
observation. Positive evidence is different: passing title and issue-link checks
do not establish that a queued workflow has completed.

GitHub's [GraphQL MergeStateStatus reference](https://docs.github.com/en/graphql/reference/pulls#mergestatestatus)
defines `UNSTABLE` as "Mergeable with non-passing commit status."
Reporting a check failure does not assert that the check is required or that the
pull request cannot merge.

## Decision

### D1. Common classification and reporting rule

After subject qualification, the first successful observation of a relevant terminal failed or cancelled check or job ends a CI wait as `failure`. Pending peers, missing required contexts, incomplete registration, an unfinished workflow run, and `UNSTABLE` do not delay this negative result. A provider read error is not a CI failure.

A PR wait judges only its watched head. Expected-head fencing and head supersession precede check classification. In merge mode, observed merged or closed state and a qualifying merge-queue removal retain their existing precedence. A check failure does not assert that it is required or that it blocks merging. Without a failure, removal, merge or close, an open merge wait remains pending.

Use the newest applicable attempt, not a superseded failure. For Actions runs on the selected commit, distinct workflow IDs and events remain distinct. Select the newest run by creation time, breaking ties by run ID, and inspect that run's current attempt. A run selector judges only its named run's current attempt. Preserve the existing newest-attempt rule for non-Actions rollup entries.

Checks-mode success requires a nonempty terminal population with no relevant failure or pending check, every required context reported, and every applicable workflow run observed for the exact commit terminal. Read all pages of the selected run and job populations. A queued or active workflow keeps the wait pending even before its jobs register; title and issue-link success do not prove that workflow completed. These reads cannot prove the existence of a workflow that GitHub has not registered and that no required context names.

A failure report names the observed failed check or job, the exact subject, and its actual URL. Its reason states when the population is incomplete; its counts describe observed evidence, not a completed full-suite inventory. Do not delay failure delivery to fetch logs from an unfinished workflow. Quote available diagnostic evidence without inventing missing detail.

The existing CLI owns classification and read-only GitHub queries. The adapter keeps its existing queue, caller identity, terminal delivery, and never-drop behavior. Do not cancel, rerun or alter GitHub jobs, merge state, required checks or settings. Bounded slices, polling cadence, carried deadlines, closed statuses and explicit provider-error handling remain unchanged.

### D2. Rollup observations retain their identity and query boundary

For non-Actions rollup entries, CheckRun identity is `(name, workflowName)`
because the existing export omits event. StatusContext identity is its context,
in a separate namespace. The greatest exported `startedAt` selects the attempt;
the export maps StatusContext creation time to `startedAt`. A newer passing or
pending attempt replaces an older failure of that identity. Different identities
remain distinct. Equal timestamps retain the first entry. Zero timestamps sort
before real timestamps, as in `gh pr checks`.

Merge mode uses the rollup already returned by `gh pr view`, with no
requiredness query and no `gh pr checks` population query. The merge-queue read
of CD-0232 is not a population query. A full rollup page can establish an
observed failure without establishing population completeness. An unobserved
failure carries no verdict. Checks mode retains its paginated rollup query at
the full-page boundary. D1 additionally governs workflow-run and job reads.
The adapter gains no new call, report status, or input field.

## Alternatives considered

- Classify every raw attempt. A failed attempt would end the wait even after
  a rerun passed. Selecting the newest attempt preserves the current result.
- Infer a merge blocker from a check failure. Requiredness is a separate fact.
- Wait for every check before reporting failure. Pending peers do not negate
  an observed failure.
- Treat a green rollup as complete CI. A queued workflow may have no jobs in
  that rollup yet.

## Consequences

An agent receives observed failure evidence without waiting for pending peers
or the merge deadline. An optional failure can end a wait without proving that
merging is blocked. An early failure report describes only observed evidence;
it is not a completed full-suite inventory. Success waits for the registered
workflow population as well as checks and required contexts.

## Verification

- `internal/ciwait.TestCiWaitQueuedWorkflows` proves queued and active workflows keep
  a wait holding only green metadata checks pending.
- `internal/ciwait.TestCiWaitFailFastReports` proves the PR checks, merge, SHA and
  run selectors report an active run's failed job without waiting for pending
  peers or unfinished-workflow logs, name the actual job URL, and state that the
  population is incomplete.
- `internal/ciwait.TestCiWaitNewestWorkflowAttempts` proves an old run's failure
  cannot fail the current candidate in checks, merge, and SHA modes.
- `internal/ciwait.TestCiWaitWorkflowPaginationAndReadFailures` proves all-pages run
  and job reads bind the observed attempt, a queued run on a later page keeps
  the wait pending, and an unreadable population is an error rather than a
  failure.
- `internal/ciwait.TestCiWaitWorkflowAttemptIdentity` proves workflow IDs and events
  stay distinct and run ID breaks a creation-time tie.
- `internal/ciwait.TestCiWaitFailureDoesNotWaitForRequiredness` proves a relevant
  failure reports without requiredness discovery, including under `UNSTABLE`.
- `internal/ciwait.TestCiWaitFailFastServedReport` proves the served first-slice
  terminal failure report removes resumable state.
- `internal/ciwait.TestCiWaitWorkflowSubjectAndCurrentAttempt` proves foreign-subject
  runs and jobs are not negative evidence and a rerun judges the current attempt.
- `internal/ciwait.TestCiWaitKnownRunFailureSurvivesMissingJobDetail` proves an
  observed run failure reports when job detail is unavailable.
- `internal/ciwait.TestCiWaitPRChecksModeMixedFailureAndPendingFails` proves checks
  mode reports failure with pending peers under the common rule.
- `internal/ciwait.TestCiWaitPRMergeModeNewestAttempts`: non-Actions rollup ordering.
- `internal/ciwait.TestCiWaitPRMergeModeFailurePrecedence`: head supersession,
  merged and closed precedence.
- `internal/ciwait.TestCiWaitPRMergeModeFullPageRollupNeedsNoChecksQuery`: observed
  failure without a `gh pr checks` population query.
- `python3 scripts/test-ci-wait-contract.py` and `bun test adapter/opencode/ci-watch.test.ts`:
  served reports, terminal delivery with a pending peer, queue, caller identity
  and never-drop behavior.
