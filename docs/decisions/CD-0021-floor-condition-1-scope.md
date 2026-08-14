# CD-0021: Floor Condition 1 Means Reach, Not an Operator Authoring Surface

**Status:** Accepted under operator approval.
**Approval date:** 2026-08-14.
**Approval:** Operator-approved direction for GitHub issue #76.
**Type:** Product-law reconciliation (floor definition against accepted contract).
**Issue:** [#76](https://github.com/Sharper-Flow/concord/issues/76).
**Refines:** [`priorities.md`](../priorities.md) first-usable floor condition 1.
**Preserves:** the accepted terminal-launcher contract (C18), including §12
anti-requirements 1 and 11, and CD-0010.

## Context

Floor condition 1 read "the operator can see, plan, and act across the full
Product scope from the launcher." The accepted launcher contract is read-only
(C18 §12 anti-requirement 1) and Product-scoped (anti-requirement 11). Issue #76
recorded the two documents as disagreeing, which left condition 1 unclaimable
and the floor unable to be honestly assessed.

Review split the apparent conflict into two independent questions with different
answers. Only one was a real gap.

### The authoring gap is real

The launcher performs exactly one action with an external effect: handing work
identity to a fresh session. `cmd/concord` registers no command that creates
work. The whole authoring surface — `concord_work_define`,
`concord_work_transition`, `concord_work_relate` — is reachable only through a
grant envelope, and the operator holds no grant. The operator genuinely cannot
author work outside an agent session.

### The cross-Product conflict is not

S1 already lists every Product in the portfolio, so every Product is reachable
from the launcher without leaving it. Anti-requirement 11 does not contradict
that. It forbids a *result set* of work or knowledge drawn from several Products
at once, which is a different claim from whether all Products are reachable.
Issue #76 conflated reach with cross-Product query.

## Decision

**D1. Planning happens through an agent session, and that is the intended path.**
Condition 1's "plan" is satisfied when the operator can reach the work and open a
session that authors it. It does not require an operator-side authoring surface.
The launcher stays read-only and work creation keeps a single write authority.

**D2. "Across the full Product scope" means reach, not cross-Product result
sets.** Condition 1 is satisfied when every Product in the portfolio is reachable
from the launcher. A query returning work or knowledge spanning several Products
remains excluded by C18 §12 anti-requirement 11 and CD-0014, and is not part of
the floor.

Both documents are amended to state these distinctions explicitly rather than
leaving them inferable.

## Rejected alternatives

**An operator CLI capture path.** Work creation authorized as operator rather
than agent avoids touching C18, but introduces a second authorization path into
work creation and would need TS5 reconciliation for a caller with no envelope.
The floor does not require it, so it would buy a second authority for no floor
progress. Available later on evidence of need.

**A launcher planning affordance.** Amending C18 to permit a bounded capture
action reopens the read-only boundary. That boundary exists because a second
write authority was the predecessor's core failure, and the launcher is the
surface most likely to grow one. Rejected on cost against a gap D1 shows is not
a gap.

**Treating the cross-Product clause as a genuine conflict.** Amending
anti-requirement 11 and adding a cross-Product query contract would satisfy a
reading of condition 1 that the portfolio screen already satisfies under D2.
CD-0014 excluded cross-Product query until a separate contract demonstrates the
need; no such need is demonstrated.

## Consequences

- Condition 1 becomes claimable. `fc1-operator-work-capture` and
  `fc1-full-product-scope` move to satisfied in the readiness manifest, evidenced
  by this decision and the launcher contract rather than by new code.
- C18 is unchanged in substance. Anti-requirement 11 gains a sentence
  distinguishing reach from result sets; no anti-requirement is added or removed.
- No new authorization path, no launcher write path, and no change to CD-0010.
- If an operator authoring surface is later wanted, it arrives as its own
  decision with its own evidence, not as floor pressure.

## Verification

- [`priorities.md`](../priorities.md) condition 1 and
  [`terminal-launcher-contract.md`](../terminal-launcher-contract.md) §12 agree
  in wording after this change.
- [`floor-readiness.v1.json`](../floor-readiness.v1.json) records both items as
  satisfied with this decision among their evidence, and
  `scripts/check-floor-readiness.py` passes.
