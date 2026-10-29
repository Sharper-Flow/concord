# CD-0186: CD-0172 D1 limits only its correction route

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** The reading of CD-0172 D1's refusal sentence at the complete step
  of a workflow kind with no complete-step correction route
- **Preserves:** CD-0172 D1's admission gate and D2 through D6 in full;
  CD-0222 D7's ordinary stale recovery; the stale re-pin admission the store
  already performs
- **Related:** CD-0222, CD-0133, CD-0143, CD-0172,
  [Concord (CON) issue 540](https://linear.app/sharper-flow/issue/CON-540)
- **Approval:** The operator approved the work item contract that carries this
  record on 2026-09-28.

## Context

CD-0172 D1 ends with the sentence "Any other work kind or step refuses." Two
review attempts against the correction shape read that sentence as refusing a
stale-law or stale Domain registry `supersede_contract` re-pin at the complete
step of a workflow kind that declares no complete-step correction route, for
example `workflow.ops_runbook`.

The store admits that re-pin through the ordinary stale recovery branch, and
it did so before CD-0172 landed. The admission asks for no post-verdict
observation and returns the instance to no earlier step.
`TestStaleRegistryRescanHoldsLateVerdictUntilRePin` asserts the admitted
shape, and `TestCompleteStepCorrectionStaleLawStaysBehindTheGate` asserts the
gated shape. Written law and store behavior therefore read differently, and a
reader of D1 alone cannot tell which one governs.
[Concord (CON) issue 513](https://linear.app/sharper-flow/issue/CON-513)
records the two review attempts that hit this conflict.

## Decision

### D1. The refusal sentence limits the correction route it names

CD-0172 D1's sentence "Any other work kind or step refuses" limits the
complete-step correction route that D1 admits. It does not refuse the
stale-law or stale Domain registry re-pin that ordinary stale recovery admits
at the complete step of a workflow kind with no correction route (CD-0222 D7).

That re-pin is admitted at any running step of any workflow kind through
CD-0222 D7's ordinary stale recovery. CD-0172 D1's route-scoping sentence
does not apply to that recovery.

## Alternatives considered

- Refuse the stale re-pin and change the store and its tests to match.
  Rejected: the refusal makes cancellation the only exit for a stale-pinned
  item at a complete step with no complete-step correction route.

## Consequences

A stale-pinned item of a non-correction workflow kind at its complete step
keeps the ordinary stale recovery exit: the operator re-pins the law or the
Domain registry, and the item proceeds without cancellation.

The refusal sentence keeps its full force on the route it names. A work kind
or step outside break-fix and implementation still refuses the complete-step
correction, and every D1 gate condition stays.

A reader of CD-0172 D1 and this record gets one answer, and the store's
admission and the written law agree. No store, adapter, or test behavior
changes.

## Verification

- `internal/store.TestStaleRegistryRescanHoldsLateVerdictUntilRePin` proves
  the admitted shape: a stale-pinned item of a non-correction workflow kind at
  its complete step re-pins through the ordinary stale recovery branch.
- `internal/store.TestCompleteStepCorrectionStaleLawStaysBehindTheGate` proves
  the gated shape: the complete-step correction route refuses outside
  break-fix and implementation and outside the D1 gate.
- `python3 scripts/check-knowledge-index.py` proves this record registers
  with a current content hash.
