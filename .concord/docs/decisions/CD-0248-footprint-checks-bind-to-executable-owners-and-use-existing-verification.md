# CD-0248: Footprint checks bind to executable owners and use existing verification

- **Status:** Accepted
- **Date:** 2026-10-27
- **Approval:** The operator approved the exact replacement policy on 2026-10-27 in
  [Concord issue 1219](https://linear.app/sharper-flow/issue/CON-1219/declare-and-validate-tooling-coverage-for-footprint-checks-and),
  policy comment `489ae093-a7b4-4358-b7e9-fdfa4ab4399e`,
  approval comment `24c4c120-8f17-4398-a38d-0a47bca05807`.

## Context

Scope: Declared-tool and scenario bindings; applicable-check evidence in existing verification and Review.

This decision refines CD-0074, CD-0090 and CD-0229 D5. Native verification
remains under the workflow engine contract and CD-0242 D8; CD-0192 is unchanged.

## Decision

1. Each applicable typed footprint check names a declared command and an existing executable owner. Existing repository validators check that relation. A missing, conflicting, deferred or nonexistent owner fails validation. Source structure establishes a binding, not successful execution or semantic adequacy.
2. A scenario binding names its exact corpus entry and the test or harness that consumes that id. Review checks that its assertions uphold the scenario. Required scenario witnesses must be observed executing and passing; a missing, skipped, failed or incomplete witness remains unproved.
3. Applicable checks are selected from approved scope and the final change. Native producer receipts, local verification and required CI supply execution evidence. Every execution claim names its actual candidate and results. An unrelated green run, preparation or declaration does not prove execution. A selected test result is not a whole-package result.
4. Accepted Review records this rule, the applicable checks, the evidence inspected and any remaining obligation. Review must not declare the change ready while required checks lack adequate evidence. A Project without required CI supplies the applicable local execution evidence; the absence of CI waives no check.
5. Outcome-contract, criterion-proof, native-provenance, current-candidate, exact-declared-command refinement and accepted-Review gates retain their owners. This adds no automatic footprint refusal at delivery or completion, no new receipt system and no package-complete native recipe kind. No worker-job binding supplies proof under CD-0242 D8.
6. All eight continuity obligations remain required. Coverage becomes satisfied only after checked bindings, observed passing execution and independent Review establish their assertions. A local delivery record is not evidence of passing required CI, merge or release.

## Alternatives considered

This choice relies on Review to catch omitted applicable checks and weakened assertions.

## Consequences

It does not automatically prove every footprint obligation for an arbitrary local candidate. No coverage claim may imply that stronger guarantee.

## Enforcement and limits

Existing tooling and evidence-anchor validators uphold structural bindings. Focused tests exercise invalid bindings and the consuming assertions. Existing result readers check the expected witness population. Required CI and recorded Review uphold applicable-check execution and adequacy. Existing workflow refusals uphold their current narrower guarantees.

### Required continuity population

The required continuity population in
[the corpus](../../scenarios/adapter-continuity.v1.json) is:

- `adapter-continuity-byte-stable-across-turns`
- `adapter-continuity-state-change-replaces-block`
- `adapter-continuity-failure-is-absence`
- `adapter-continuity-ttl-gates-spawn`
- `adapter-continuity-work-start-retarget`
- `adapter-continuity-lane-leak-absent`
- `adapter-continuity-post-boundary-re-pin`
- `adapter-continuity-session-vacate-then-reclaim-passes-occupancy-gate`

## Verification

- [`python3 scripts/check-project-tooling.py`](../../../scripts/check-project-tooling.py)
- [`python3 scripts/test-project-tooling.py`](../../../scripts/test-project-tooling.py)
- [`python3 scripts/test-adapter-acceptance.py`](../../../scripts/test-adapter-acceptance.py)
- [`python3 scripts/check-law-coverage.py`](../../../scripts/check-law-coverage.py)
- [`python3 scripts/check-doc-contract.py`](../../../scripts/check-doc-contract.py)
- [`python3 scripts/check-knowledge-index.py`](../../../scripts/check-knowledge-index.py)

## Related law

[CD-0074](CD-0074-project-tooling-declaration.md) ·
[CD-0090](CD-0090-per-turn-continuity-re-pin-hook.md) ·
[CD-0229](CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md) ·
[CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) ·
[CD-0192](CD-0192-the-refine-pass-proves-a-declared-analysis-ran.md)
