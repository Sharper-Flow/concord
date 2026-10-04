# CD-0204: The acceptance gate is one admission condition and a missing verdict records late

- **Status:** Accepted
- **Date:** 2026-10-04
- **Scope:** The admission of `confirm_premise` and `record_delivery` at the
  preflight, the work pin, and execution; the late `record_verdict` route
- **Extends:** CD-0201 D1, the single admission owner, to the acceptance
  deliverables and to the fresh-review refusal over `record_delivery`
- **Amends:** CD-0203 D2 at its consequence: a frozen ops runbook that
  reaches cleanup without a verdict records the verdict there before the
  premise question can be confirmed
- **Preserves:** CD-0201 D2, the liveness law; CD-0115 D1, released
  definition content and digests; every other route refusal keeps its scope
  (CD-0186)
- **Related:** CD-0116, CD-0197, CD-0203
- **Approval:** The operator directed in session chat on 2026-10-02 that the
  checks stay strict, with no named exception, and that the engine routes
  change until the law holds. This record serves the approved contract that
  derives admission once, whose fresh review found the two gaps below.

## Context

CD-0201 D1 puts workflow action admission in one function over one folded
state. A review of that work found two conditions that the execution boundary
enforced and the folded state did not carry:

1. The confirmation of the premise refuses while a deliverable is missing: a
   recorded verdict, a verdict for each approved predicate, or a bound
   required evidence kind. The preflight and the work pin did not read that
   gate. They offered a confirmation that execution then refused.
2. Behind outstanding post-rejection review debt, admission refuses
   `record_delivery` at a review step with the fresh-review refusal. The
   preflight deferred that refusal to the guards, as it does for the accept
   that can name the ready review. No guard owns the refusal over
   `record_delivery`, so the preflight admitted a delivery that execution
   refused.

When the checks folded the acceptance gate, they found one more strand.
Version-1 to version-4 ops runbooks leave health through `record_health`
without a verdict. Cleanup does not declare `record_verdict`, and the late
verdict route served only the terminal step. The confirmation at cleanup
refused the missing verdict, and no action could record it.

## Decision

### D1. The folded state carries the acceptance gate

The admission loader runs the acceptance-deliverables check while a premise
question is open for `confirm_premise`. A typed refusal of that check becomes
part of the folded state, and `workflowAdmit` refuses `confirm_premise` with
it. The preflight, the work pin, and execution read the same refusal. An
unreadable projection fails the fold.

### D2. Only the accept defers the fresh-review refusal

The fresh-review refusal defers to the guards only for
`accept_worker_result`. Its attempt identity can name the ready review, and
that check reads the payload. Over `record_delivery` the refusal depends on
the state alone, so every admission site applies it.

### D3. The late verdict route serves the premise-question step

The late `record_verdict` route serves the terminal step and every step that
hosts the premise question for `confirm_premise` without declaring
`record_verdict`. The route keeps its other conditions: an active contract, an
earlier step that declares `record_verdict`, and a predicate without an `ok`
verdict. A late verdict does not move the step.

No definition version changes. Admission code is not definition content
(CD-0115 D1).

## Alternatives considered

- Keep the deliverables check at execution only, and add a preflight copy.
  Rejected: a second copy is the drift that CD-0201 D1 removes.
- Defer every fresh-review refusal and give `record_delivery` a guard.
  Rejected: the refusal reads no payload, and a guard copy is a second owner.
- Publish a new ops-runbook version that declares `record_verdict` on cleanup.
  Rejected: items pinned to version 1 to version 4 stay stranded
  (CD-0115 D2).
- Let the cleanup confirmation pass without a verdict. Rejected: completion
  requires the verdict, so the work strands one step later.

## Consequences

A caller that asks the preflight or reads the work pin gets the refusal that
execution applies, for the confirmation and for a delivery behind review
debt. A frozen ops runbook at cleanup without a verdict records the verdict
there, then confirms. The liveness checks fold the recorded verdict at the
premise question.

## Verification

```gherkin
Scenario: The preflight refuses a confirmation that lacks its verdict
  Given an item at acceptance with an open premise question and no recorded verdict
  When the agent asks the preflight for confirm_premise
  Then the preflight refuses with missing evidence that names the verdict
  And the work pin does not offer confirm_premise

Scenario: The preflight refuses a delivery behind review debt
  Given an item at a review step with outstanding post-rejection review debt
  When the agent asks the preflight for record_delivery
  Then the preflight refuses with the fresh-review refusal that execution applies

Scenario: A frozen ops runbook records a missing verdict at cleanup
  Given a version-4 ops runbook item at cleanup with no verdict and an investigation artifact
  When the agent records the verdict
  Then the step stays at cleanup
  And the operator confirmation moves the instance to the complete step
```

- `go test ./internal/store/ -run 'TestPreflightRefusesConfirmationMissingMandatedDeliverables|TestWorkPinPublishesConfirmPremiseRequiredFields'` proves the first scenario.
- `go test ./internal/store/ -run TestPreflightRefusesReviewDebtDeliveryTheExecutionRefuses` proves the second scenario.
- `go test ./internal/store/ -run 'TestFrozenOpsRunbookCleanupRecordsAMissingVerdict|TestReachableAdmissionStateReachesTerminal'` proves the third scenario.
- `python3 scripts/check-doc-contract.py --report-only` proves this record carries the current decision outline and passes the writing rules.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0204 identifier allocates once.
