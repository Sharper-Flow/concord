# CD-0143: Verdict correction returns to the external effect

- **Status:** Accepted
- **Date:** 2026-09-13
- **Scope:** Engine-owned correction after worker delivery and an unhealthy verification verdict
- **Approval:** The operator approved this bounded workflow change.
- **Related:** CD-0013, CD-0059, CD-0067, CD-0115, CD-0116, CD-0124, CD-0133, CD-0137, CD-0138
- **Amends:** CD-0013, CD-0059, CD-0067, and CD-0124 at their workflow correction and dispatch boundaries
- **Preserves:** Contract authority, immutable history, evidence provenance, operator approval, fail-closed completion, and historical definition pins

## Context

A completed and accepted worker result can reach a verification checkpoint with
a current unhealthy verdict. A dispatch alone is not a delivered result. The
existing result-rejection route does not model this checkpoint. A correction
route must preserve the approved contract and history, then return the pinned
workflow to the external effect that produced the result.

## Decision

### D1. Admit one typed verdict-correction action

The engine exposes `request_correction` only when a completed and accepted worker
result and a current unhealthy verdict exist. The payload requires a diagnosis,
strategy, affected predicate IDs, bound evidence references, and an exact
operator approval.
The action holds the checkpoint and records its disposition in the event log.

### D2. Return to the declared external effect

The action returns the pinned implementation instance to `execution` and the
pinned break-fix instance to `repair`. It does not substitute `refine`, use a
generic retry edge, rewrite the contract, or bypass a lifecycle gate. The next
dispatch creates a fresh worker attempt and epoch through the normal dispatch
boundary.

### D3. Bound correction sequences

The engine permits three correction attempts in one unresolved correction
sequence. The fourth request requires operator escalation and is refused. Only
a latest verdict set in which every approved predicate is healthy and
comparable ends the sequence. Prior verdicts, packets, attempts, and definition
pins remain immutable.

### D4. Keep completion fail-closed

Completion remains unavailable while any latest approved predicate is unhealthy or
incomparable with the approved result. Correction does not create a healthy
verdict and does not weaken evidence or predicate validation.

## Verification

- `request_correction` is unavailable without a completed and accepted worker delivery and a current unhealthy verdict.
- Missing fields, unrelated predicates, unbound evidence, and missing operator approval are refused.
- Implementation correction returns to `execution`; break-fix correction returns to `repair`.
- The correction context exposes the affected predicates, evidence, attempt bound, and escalation state.
- The fourth correction request is refused, and completion remains fail-closed until healthy verdicts exist.
- Historical workflow pins retain their released definition and event history.
