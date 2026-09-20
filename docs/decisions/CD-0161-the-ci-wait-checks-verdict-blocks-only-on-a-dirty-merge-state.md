# CD-0161: The ci-wait checks verdict blocks only on a dirty merge state

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The `concord ci-wait` PR selector classification; the report's
  `merge_state` field; the `ci-wait` utility registry entry and purpose
- **Approval:** The operator reviewed live dispatch evidence of green pull
  requests timing out on `mergeStateStatus UNKNOWN` and approved this
  amendment.
- **Related:** CD-0149, CD-0157, CD-0160
- **Preserves:** The CD-0160 verb ownership, one bounded slice, carried
  deadline, head-SHA supersession, narrowed command allowance, and explicit
  error reporting
- **Amends:** CD-0160 D4

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
say.

### D2. Merge mode answers the merge question

The request gains `mode` with values `checks` and `merge`; `checks` stays the
default, so every CD-0160 caller is unchanged. Merge mode gates `success` on
`CLEAN` or `HAS_HOOKS` only and keeps every other value, including `UNKNOWN`,
pending. In both modes the selector reads `state` and `mergedAt` on the
`gh pr view` call it already makes, so a merged pull request reports `merged`
and a closed one reports `closed` as terminal statuses.

### D3. Every report carries merge_state

The report carries the observed `merge_state` on every status. The field is
omitted when empty, so the SHA and run selectors emit no empty field.

### D4. The utility entry moves to version 3

The registry entry moves to version 3: the request gained a mode field and the
report gained statuses. The purpose states the input contract: the caller
passes a repository and one selector, and this utility owns all polling
through the verb. The CD-0160 D6 denial of caller-side polling methods stands
unchanged.

## Consequences

- A green pull request with `UNKNOWN`, `BEHIND`, `BLOCKED`, `UNSTABLE`, or
  `DRAFT` merge state reports `success` in checks mode. The operator accepts
  those states as check-completeness outcomes; only `DIRTY` holds a green
  wait open.
- A merge-mode wait can still time out while GitHub has not computed
  mergeability. No claim is made that `UNKNOWN` resolves on demand.
- The manifest digest moves, and all generated projections regenerate.
- CD-0160 D4's merge-state clause is amended as recorded here; its other
  clauses stand.

## Verification

- The Go table test in `cmd/concord/ci_wait_test.go` proves checks-mode
  `success` for `UNKNOWN`, `BEHIND`, `BLOCKED`, `UNSTABLE`, `DRAFT`, empty,
  and `CLEAN` merge states with a complete passing check set, and `pending`
  for `DIRTY`.
- Merge mode proves `CLEAN` success and `UNKNOWN` pending; merged and closed
  pull requests prove terminal in both modes.
- `python3 scripts/generate-agent-lanes.py --check` proves every generated
  projection matches the manifest and templates.
- `go test ./cmd/concord/` proves the verb inside the full command package
  suite.
