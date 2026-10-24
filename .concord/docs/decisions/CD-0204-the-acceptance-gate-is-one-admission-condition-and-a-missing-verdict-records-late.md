# CD-0204: The acceptance gate is one admission condition and a missing verdict records late

- **Status:** Accepted
- **Date:** 2026-10-04
- **Scope:** The admission of `confirm_premise` and `record_delivery` at the
  preflight, the work pin, and execution; the late `record_verdict` route
- **Preserves:** CD-0201 D2, the liveness law; the digests derived from the
  one canonical source
  ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D14);
  every other route refusal keeps its scope
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
2. Behind a blocking Review disposition, admission refuses
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

### D1. Admission carries the acceptance gate

The admission loader runs the acceptance-deliverables check while a premise
question is open for `confirm_premise`, and `workflowAdmit` refuses
`confirm_premise` with that typed refusal
([CD-0242 D3](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d3-stage-exits-are-the-unit-of-enforcement)).
The preflight, the work pin, and execution read the same refusal. An
unreadable read fails admission.

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

## Alternatives considered

- Keep the deliverables check at execution only, and add a preflight copy.
  Rejected: a second copy is the drift that CD-0201 D1 removes.
- Defer every fresh-review refusal and give `record_delivery` a guard.
  Rejected: the refusal reads no payload, and a guard copy is a second owner.
- Let confirmation pass without a verdict. Rejected: completion
  requires the verdict, so the work strands one step later.

## Consequences

A caller that asks the preflight or reads the work pin gets the refusal that
execution applies, for the confirmation and for a delivery behind a blocking
Review disposition. At a supported premise-question or terminal step, a missing
verdict records through the late route without moving the step. The liveness checks read the recorded verdict at the
premise question
([CD-0242 D3](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d3-stage-exits-are-the-unit-of-enforcement)).

## Verification

```gherkin
Scenario: The preflight refuses a confirmation that lacks its verdict
  Given an item at acceptance with an open premise question and no recorded verdict
  When the agent asks the preflight for confirm_premise
  Then the preflight refuses with missing evidence that names the verdict
  And the work pin does not offer confirm_premise

Scenario: The preflight refuses a delivery behind a blocking Review disposition
  Given an item at a review step whose accepted Review disposition is correction required
  When the agent asks the preflight for record_delivery
  Then the preflight refuses with the fresh-review refusal that execution applies

Scenario: A premise-question step records missing criterion proof late
  Given an item at a premise-question step without a declared record_verdict action
  And an earlier step declares record_verdict and the question step declares its own return
  When Main associates applicable producer receipts with every missing criterion
  Then the step stays at the premise question
  And premise confirmation moves the instance to the complete step
```

- `go test ./internal/store/ -run 'TestPreflightRefusesConfirmationMissingMandatedDeliverables|TestWorkPinPublishesConfirmPremiseRequiredFields'` proves the first scenario.
- `go test ./internal/store/ -run TestPreflightAndMutationRequireAcceptedReadyDisposition` proves preflight, mutation, and the work pin agree that delivery requires an accepted ready disposition under [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls).
- `go test ./internal/store/ -run 'TestCriterionAssociationAtPremiseQuestionWithoutDeclaration|TestIssue1013LateVerdictRecoveryPassesReadOnlyPreflight|TestIssue933LateVerdictRecoveryHoldsStepAndRefusesHealthyReplacement'` proves the late route holds its step, agrees across preflight and mutation, and closes after the missing criteria have proof.
- `python3 scripts/check-doc-contract.py --report-only` proves this record carries the current decision outline and passes the writing rules.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0204 identifier allocates once.
