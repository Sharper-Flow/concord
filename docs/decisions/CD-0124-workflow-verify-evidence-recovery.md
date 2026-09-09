# CD-0124: Workflow verify evidence recovery

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** The break-fix verify route for an outstanding contract evidence kind
- **Approval:** The operator approved this Product law amendment for the
  workflow recovery repair.
- **Related:** CD-0013, CD-0115, CD-0122, issue #837
- **Amends:** CD-0013 at the break-fix verify action set
- **Preserves:** Contract authority, event history, evidence provenance,
  operator approval, lifecycle gates, and released definition pins

## Context

An accepted break-fix delivery can reach `verify` with a contract-required
evidence kind still outstanding. The existing completion gate correctly refuses
premise confirmation, but the prior action set provides no typed route that can
repair the missing evidence without changing the step.

## Decision

### D1. Verify has one typed recovery route

Break-fix definition version 6 declares `bind_evidence` on `verify`. The action
has hold execution mode and no approval requirement. It binds only one evidence
kind that the active contract and pinned definition still require and that the
evidence projection does not satisfy.

### D2. Confirmation remains the advancing gate

`bind_evidence` never advances `verify`. `confirm_premise` remains the only
advancing action on that step. Recovery and acceptance use the same outstanding
requirement calculation. They do not create a second requirement list.

### D3. Recovery preserves authority and refuses unsafe input

The route preserves the active contract, attempts, prior evidence, verdicts,
and operator decisions. It refuses an unrelated kind, a satisfied requirement,
an unauthorized actor, a stale version, and terminal work without a durable
effect. An exact replay is idempotent and creates no duplicate binding.

Recovery does not fabricate evidence, relabel a producer's evidence, invent an
obligation, reset history, or bypass a lifecycle gate.

### D4. Released definitions remain immutable

Released definition versions keep their content and digest. Version 6 is a new
break-fix definition version. New work uses version 6; existing work continues
to use its pinned released version.

### D5. Delivery evidence stays separate

Source delivery, supported deployment, and recovery of the four stranded work
items are separate evidence obligations. Source code and tests do not claim
deployment or stranded-work recovery.

## Verification

- `workflow.break_fix` version 6 declares `bind_evidence` on `verify` with hold
  execution mode, while `confirm_premise` remains the only advancing action.
- Recovery and acceptance share the typed outstanding-requirement calculation.
- Refusal tests prove no effect for unrelated, satisfied, unauthorized, stale,
  and terminal requests.
- Replay tests prove one durable binding for one recovery identity.
- The released definition digest pins remain unchanged.
