# CD-0161: The ci-wait checks verdict blocks only on a dirty merge state

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The `concord ci-wait` PR selector classification; the report's
  `merge_state` field; the `ci-wait` utility registry entry and purpose
- **Approval:** The operator reviewed live dispatch evidence of green pull
  requests timing out on `mergeStateStatus UNKNOWN` and approved this
  amendment. The coordinator approved this record under the operator's delegation
  on 2026-10-18 for
  [Concord (CON) issue 1145](https://linear.app/sharper-flow/issue/CON-1145).
- **Related:** CD-0149, CD-0157, CD-0160
- **Preserves:** The CD-0160 verb ownership, one bounded slice, carried
  deadline, head-SHA supersession, narrowed command allowance, and explicit
  error reporting

## Context

CD-0160 D4 made the checks-mode PR verdict honor GitHub's merge state: a
complete green check set stayed pending whenever `mergeStateStatus` was not
`CLEAN` or `HAS_HOOKS`. The intent was to refuse success while GitHub asserts
the pull request cannot merge.

Live dispatches measured the result. GitHub computes mergeability lazily, and
its most common answer is `UNKNOWN`, which means "not computed", not "cannot
merge" (the `mergeStateStatus` values are GitHub's own:
https://docs.github.com/en/graphql/reference/pulls). Measured against
PokeEdge PR 1728 (https://github.com/Sharper-Flow/PokeEdge/pull/1728): 27
checks observed, 23 passing, 4 skipped, 0 failing, 0 pending, and the verb
returned `pending` with `merge_state UNKNOWN`. Forty-one seconds of repeated
polling did not resolve `UNKNOWN`, so a fully green pull request runs to the
30-minute timeout. Nine of fourteen sampled open pull requests read `UNKNOWN`.

The same dispatches asked a second question the selector could not answer: six
of the twenty most recent ci-wait invocations wanted to know whether the pull
request can merge, not whether its checks finished.

## Decision

### D1. In checks mode only DIRTY blocks the verdict

`DIRTY` is GitHub asserting that the merge commit cannot be created. A green
check set with `DIRTY` stays pending, and the report says why. `UNKNOWN`,
`BEHIND`, `BLOCKED`, `UNSTABLE`, `DRAFT`, and an empty value carry no verdict
about check completeness, so they never block the check verdict. A complete
check set with no failures is `success` in checks mode whatever those states
say. Under [CD-0233](CD-0233-a-checks-wait-waits-for-every-required-context.md),
a set is not complete while a required context of the base branch is absent.

### D2. Merge mode answers the merge question

The request gains `mode` with values `checks` and `merge`; `checks` stays the
default. Merge mode waits for the pull request to merge, not merely become
mergeable. Both modes apply the common classification and reporting rule in
[CD-0228 D1](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md#d1-common-classification-and-reporting-rule),
including subject qualification, newest applicable attempts, prompt failure
reporting and complete-success requirements. Under
[CD-0232](CD-0232-a-merge-wait-ends-when-the-merge-queue-removes-the-pull-request.md),
a fresh merge-queue removal that is not a merge ends an open merge wait as
`failure` before check failures, and `UNSTABLE` does not hold it open.
Without an observed failure or removal, every open merge wait stays pending,
including `CLEAN` and `HAS_HOOKS`.
In both modes the selector reads `state` and `mergedAt` on the `gh pr view`
call it already makes. In merge mode a merged pull request reports `merged`,
and a closed one reports `closed`; those outcomes take precedence over check
failures. In checks mode, merging or closing the pull request does not judge
its checks. The selector reports the watched head's check verdict and totals
through the same classification rules as an open pull request. Without a
relevant failure, no checks or an incomplete population stay pending. A complete
passing set reports `success` unless D1's `DIRTY` rule holds it pending. Head
supersession retains precedence in both modes. Non-Actions rollup identities
and attempt ordering follow CD-0228 D2.

### D3. Every report carries merge_state

The report carries the observed `merge_state` on every status. The field is
omitted when empty, so the SHA and run selectors emit no empty field.

### D4. The utility entry moves to version 3

The purpose states the input contract: the caller
passes a repository and one selector, and this utility owns all polling
through the verb. CD-0160 D6 forbids caller-side polling methods.

## Consequences

- A green pull request with `UNKNOWN`, `BEHIND`, `BLOCKED`, `UNSTABLE`, or
  `DRAFT` merge state reports `success` in checks mode. The operator accepts
  those states as check-completeness outcomes; only `DIRTY` holds a complete
  green wait open.
- A merge-mode wait can still time out while GitHub has not computed
  mergeability. No claim is made that `UNKNOWN` resolves on demand.
- The manifest digest moves, and all generated projections regenerate.

## Verification

- The Go table test in `internal/ciwait/ci_wait_test.go` proves checks-mode
  `success` for `UNKNOWN`, `BEHIND`, `BLOCKED`, `UNSTABLE`, `DRAFT`, empty,
  and `CLEAN` merge states with a complete passing check set, and `pending`
  for `DIRTY`.
- [CD-0228 Verification](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md#verification)
  names the required fail-fast, queued-workflow and newest-attempt checks in
  both modes, including failure reporting with `UNSTABLE`.
- `internal/ciwait.TestCiWaitPRMergeModeNewestAttempts` proves newest-attempt
  selection and separate check identities.
- `internal/ciwait.TestCiWaitPRMergeModeFailurePrecedence` proves merged, closed,
  and superseded outcomes take precedence over check failures in merge mode.
- `internal/ciwait.TestCiWaitPRChecksModeJudgesMergedAndClosedHeads` proves that
  merged and closed pull requests in checks mode still report the watched
  head's passing, failing, pending, and empty check populations, without
  treating the pull request's terminal state as CI proof.
- `python3 scripts/generate-agent-lanes.py --check` proves every generated
  projection matches the manifest and templates.
- `go test ./internal/ciwait/ ./cmd/concord/` proves the wait owner and the verb
  inside the full command package suite, including `TestCiWaitIngress` and
  `TestDeterministicCIWaitOwner`.
