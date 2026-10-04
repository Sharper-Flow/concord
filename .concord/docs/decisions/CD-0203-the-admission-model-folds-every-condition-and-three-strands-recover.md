# CD-0203: The admission model folds every condition and three strands recover

- **Status:** Accepted
- **Date:** 2026-10-04
- **Scope:** The workflow liveness, well-formed-exit, and conformance checks;
  three store admission routes those checks proved stranded
- **Extends:** CD-0201 at D1, which names the widening of the checks to the
  full folded state as later work in a separate record
- **Amends:** CD-0172 D1 at its contract count: the complete-step correction
  admits one or more active contracts, so a duplicated projection at that
  step recovers instead of refusing
- **Preserves:** CD-0201 D2, the liveness law; CD-0115 D1, released
  definition content and digests; CD-0112 D1, continuity actions never move
  the step; every other route refusal keeps its scope (CD-0186)
- **Related:** CD-0090, CD-0133, CD-0166, CD-0173, CD-0186
- **Approval:** The operator directed in session chat on 2026-10-02 that the
  checks stay strict, with no named exception, and that the engine routes
  change until the law holds. The operator approved the fix of all three
  strands in session chat.

## Context

CD-0201 D2 states the liveness law. Each well-formed nonterminal admission
state admits an exit, and each reachable nonterminal state reaches a terminal
step. The first checks folded only a subset of the admission conditions. An
exit that the subset could not prove was not counted, and a state that the
subset could not reach was not checked.

The checks now fold every condition `workflowAdmit` reads, over all 108
registered definition versions. The widened checks found three strands in
the engine:

1. A duplicated contract projection at the pinned complete step refused every
   recovery. CD-0172 D1 admitted the complete-step correction only with
   exactly one active contract, and the supersede admission refused duplicate
   recovery at that step.
2. Version-1 to version-4 ops runbooks declare `confirm_premise` on an
   internal-SQLite cleanup step. The operator question serves only a human
   checkpoint, so the approval that `confirm_premise` consumes never opened,
   and the only edge out of cleanup was never admitted.
3. Version-1 research declares `frame_research` in advance mode beside
   `approve_contract` on its frame step. A contractless advance moved the
   work past the only step that binds a contract, and completion refuses
   without one.

## Decision

### D1. The contract step and the complete step keep their contract routes

An advancing action with no active contract refuses at the contract step,
except `approve_contract`. The refusal kind is an illegal lifecycle
transition. A work item leaves the contract step only through the approval
it hosts.

The complete-step correction admits one or more active contracts. A
duplicated projection at the complete step takes the same shared gate as a
single stale contract. The successor names every active version, so one
supersession repairs the projection and returns the instance to its
external-effect step.

### D2. The premise question opens wherever confirm_premise is declared

`workflowOperatorQuestionAction` owns which approval-required action the
operator question at a step serves. A human checkpoint serves its first
approval-required action. A step of any kind that declares `confirm_premise`
serves that action, because the operator question is the only route that
answers it. Other approval-required actions off a human checkpoint open no
question. `request_correction` at a delivery gate keeps its own approval
route.

The work pin, the operator question read, the product row, and admission
call this one function. The investigation artifact and the recorded verdict
stay required, so the question opens only when its evidence exists.

### D3. The checks fold every admission condition

The liveness and well-formed-exit checks enumerate the abstract states over
every condition `workflowAdmit` reads. The conformance replay compares the
loaded state with the abstract successor on the fields `workflowAdmit`
reads. The step-kind checks cover every registered version, frozen ones
included, and use the same question function as the engine.

No definition version changes. Admission code is not definition content
(CD-0115 D1), so pinned instances receive the corrected routes on release.

## Alternatives considered

- Name the three strands as exceptions in the checks. Rejected: the
  operator directed that the law holds without exceptions, and an excepted
  strand still strands real work.
- Publish new definition versions for ops runbook and research. Rejected:
  items already pinned to the old versions stay stranded (CD-0115 D2).
- Make the ops-runbook cleanup step a human checkpoint at read time.
  Rejected: that rewrites released definition content in place.
- Keep the duplicate refusal and require recovery on an earlier step.
  Rejected: the pinned complete step declares no earlier route, so the
  refusal leaves no exit.

## Consequences

A duplicated projection at the complete step recovers through one operator
approval. Frozen ops runbooks at cleanup complete through the premise
question. Version-1 research cannot skip its contract. A future change that
strands any registered version fails the widened checks with a witness.

## Verification

```gherkin
Scenario: A contractless advance at the contract step refuses
  Given a version-1 research item at its frame step with no active contract
  When the agent requests frame_research
  Then admission refuses with an illegal lifecycle transition
  And approve_contract stays admitted

Scenario: A duplicated projection at the complete step recovers
  Given a completed break-fix item with two active contracts and a later contradiction observation
  When the operator approves a supersession that names every active version
  Then the projection holds one active contract and the instance returns to repair

Scenario: A frozen ops runbook leaves its cleanup step
  Given a version-4 ops runbook item at cleanup with a verdict and an investigation artifact
  When the work pin is read
  Then the pin carries the confirm_premise question
  And the operator confirmation moves the instance to the complete step

Scenario: Every registered version stays live under the full model
  Given every registered definition version
  When the checks enumerate the full abstract admission state
  Then each well-formed nonterminal state has an exit and each reachable state reaches a terminal step
```

- `go test ./internal/store/ -run TestContractStepAdvancesOnlyThroughItsApproval` proves the first scenario.
- `go test ./internal/store/ -run TestCompleteStepCorrectionRecoversDuplicateProjection` proves the second scenario.
- `go test ./internal/store/ -run 'TestFrozenOpsRunbookCleanupOpensThePremiseQuestion|TestRegisteredStepsAdmitTheirApprovalRequiredActions|TestRegisteredNonTerminalStepsCanAdvance'` proves the third scenario.
- `go test ./internal/store/ -run 'TestWellFormedAdmissionStateHasNonContinuityExit|TestReachableAdmissionStateReachesTerminal|TestAdmissionConformanceReplayRejectionReviewAndSettlingAccept'` proves the fourth scenario.
- `python3 scripts/check-doc-contract.py --report-only` proves this record carries the current decision outline and passes the writing rules.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0203 identifier allocates once.
