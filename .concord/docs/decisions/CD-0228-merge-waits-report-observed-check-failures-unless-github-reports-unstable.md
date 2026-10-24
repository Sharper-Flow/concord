# CD-0228: Merge waits report observed check failures unless GitHub reports UNSTABLE

- **Status:** Accepted
- **Date:** 2026-10-15
- **Scope:** Merge-mode classification in `concord ci-wait`
- **Preserves:** Checks-mode completeness, head supersession, merged and closed
  precedence, bounded slices, carried deadlines, and the closed report statuses
- **Related:** [CD-0160](CD-0160-the-ci-wait-utility-delegates-its-wait-to-a-deterministic-cli-verb.md)
- **Approval:** The Snowball coordinator approved this law change on 2026-10-15
  under the operator's delegation (coordinator, operator-delegated).

## Context

An open merge wait must report a failed check when that observation ends the
wait's purpose. Waiting until the deadline delays the agent's repair without
adding evidence. However, a non-passing check does not always prevent a merge.

GitHub's [GraphQL MergeStateStatus reference](https://docs.github.com/en/graphql/reference/pulls#mergestatestatus)
defines `UNSTABLE` as "Mergeable with non-passing commit status."
The wait must preserve that possibility instead of treating every failure as
evidence that the merge cannot happen. This state describes the pull request,
not the requiredness of an individual check.

## Decision

### D1. Observed failures end merge waits except in UNSTABLE

After head supersession, merged, and closed classification, select the newest
attempt per check identity by the greatest exported `startedAt`.
CheckRun identity is `(name, workflowName)` because the existing rollup export
omits event. StatusContext identity is its context, in a separate namespace.
The export maps StatusContext creation time to `startedAt`.
A newer passing or pending attempt replaces an older failure of that identity.
Different identities remain distinct. Equal timestamps retain the first entry.
Zero timestamps sort before real timestamps, as in `gh pr checks`.

A failed or cancelled selected attempt ends an open merge-mode wait as
`failure`, unless the
observed `merge_state` is `UNSTABLE`. Failure takes precedence over pending
checks in the same rollup. The report carries the observed failure names,
URLs, classifications, and available failed-log excerpts under CD-0160 D4.

With `UNSTABLE`, the wait stays pending despite observed failures, because
GitHub reports that the pull request can merge. Without an observed failure
or a merge-queue removal under CD-0232, every open merge wait stays pending.
`CLEAN` and `HAS_HOOKS` are not a merge.
Only an observed merge produces `merged`.

### D2. The existing rollup supplies the observation

Merge mode classifies the rollup already returned by `gh pr view`. It adds
no requiredness query and no `gh pr checks` population query. The
merge-queue read of CD-0232 is not a population query. A full rollup
page can establish the existence of an observed failure without establishing
the complete population. An unobserved failure carries no verdict.

Checks mode keeps its complete-population requirements, its paginated query
at the full-page boundary, and its terminal rules. The adapter gains no new
call, report status, or input field.

## Alternatives considered

- Classify every raw attempt. A failed attempt would end the wait even after
  a rerun passed. Selecting the newest attempt preserves the current result.
- Fail on every selected failure. This rule would end a wait even when GitHub
  reports that the pull request can merge with non-passing commit status.
- Query requiredness separately. The approved merge-state exception needs no
  new query and makes no unsupported claim about an individual check.
- Wait for every check before reporting failure. Pending peers do not negate
  an observed failure outside `UNSTABLE`.

## Consequences

An agent receives failure evidence without the merge deadline. Merge waits
can still finish after `UNSTABLE` when the pull request merges. A rollup page
that contains no failure can leave the wait pending even when an unseen check
failed. The wait makes no completeness claim for that observation.
The exception applies only to `UNSTABLE`. A failed optional check in another
merge state can therefore end a wait. The state is not a requiredness proof.

The export omits the event field, so attempts with the same name and workflow
share an identity even across events. An unstarted attempt with zero time
does not replace an older attempt with real time. This ordering matches
`gh pr checks` but does not infer recency from unavailable timestamps.

## Verification

- `cmd/concord.TestCiWaitPRMergeModeCheckFailures` proves failure, cancellation,
  pending peers, the `UNSTABLE` exception, and observed failure details.
- `cmd/concord.TestCiWaitPRMergeModeNewestAttempts` proves newer passing and
  pending reruns replace older failures, while different identities stay distinct.
- `cmd/concord.TestCiWaitPRMergeModeFailurePrecedence` proves head supersession,
  merged, and closed outcomes retain precedence.
- `cmd/concord.TestCiWaitPRMergeModeFullPageRollupNeedsNoChecksQuery` proves a
  full rollup page reports an observed failure without a population query.
- `cmd/concord.TestCiWaitPRChecksModeMixedFailureAndPendingStaysPending` proves
  checks mode retains its completeness requirement.
