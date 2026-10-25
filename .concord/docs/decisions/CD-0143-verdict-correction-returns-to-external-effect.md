# CD-0143: Verdict correction returns to the external effect

- **Status:** Accepted
- **Date:** 2026-09-13
- **Scope:** Engine-owned correction after worker delivery and an unhealthy verification verdict
- **Approval:** The operator approved this bounded workflow change.
- **Related:** CD-0013, CD-0059, CD-0067, CD-0116, CD-0124, CD-0133, CD-0137, CD-0138
- **Preserves:** Contract authority, evidence provenance, operator approval,
  fail-closed completion, and the digests each record binds
  ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D7, D14)

## Context

A completed and accepted worker result can reach a verification checkpoint with
a current unhealthy verdict. A dispatch alone is not a delivered result. The
existing result-rejection route does not model this checkpoint. A correction
route must preserve the approved contract and history, then return the pinned
workflow to the external effect that produced the result.

## Decision

### D1. Admit one typed verdict-correction action

For `approval_policy=consequence_v1`, correction requires the authenticated coordinator, diagnosis, strategy, affected predicates/findings, and bound evidence, but no operator approval or operator identity. This qualification applies to the approval prerequisite and its verification assertions below. Declared returns and fail-closed completion remain unchanged.

The engine exposes `request_correction` when a completed and accepted worker
result and a current unhealthy verdict exist. The payload requires a diagnosis,
strategy, affected predicate IDs, bound evidence references, and an exact
operator approval.
The action holds the checkpoint and records its disposition as current state
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

The engine additionally exposes `request_correction` at the
`human_checkpoint` step when the latest attempt a worker actually dispatched
there failed and a hold-mode `record_worker_failure` dispositions that exact
attempt (CD-0193 D1), with a current unhealthy verdict under the active
contract and the same payload. The failure disposition is not an accepted
success. It opens only this typed return.

### D2. Return to the declared external effect

The action returns the implementation instance to `execution` and the
break-fix instance to `repair`
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
It does not substitute `refine`, use a
generic retry edge, rewrite the contract, or bypass a lifecycle gate. The next
dispatch creates a fresh worker attempt and epoch through the normal dispatch
boundary.

### D3. Correction repeats until healthy

Correction repeats until a latest verdict set in which every approved predicate is healthy and comparable ends it ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

### D4. Keep completion fail-closed

Completion remains unavailable while any latest approved predicate is unhealthy or
incomparable with the approved result. Correction does not create a healthy
verdict and does not weaken evidence or predicate validation.

## Verification

- `request_correction` is unavailable without a current unhealthy verdict and one authorized admission ground.
- The ground is a completed and accepted worker delivery behind that verdict, or a failure record that dispositions the checkpoint's latest failed attempt.
- A verdict that predates a completed correction request cannot reopen the failed-review return, and the refusal names the stale verdict.
- Missing fields, unrelated predicates, unbound evidence, and missing operator approval are refused.
- Implementation correction returns to `execution`; break-fix correction returns to `repair`.
- The correction context exposes the affected predicates and evidence;
- Completion remains fail-closed until healthy verdicts exist. The CD-0242 D6
  anchor — no attempt count or escalation wall exists — binds when the
  implementing runtime change lands; until then,
  `python3 scripts/check-knowledge-index.py` is the executable anchor for this
  record's law registration.
- The correction route reads current state only; no released definition version
  or event history is retained
  ([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).
- A failed checkpoint review refuses `request_correction` until a failure record dispositions it, and the refusal names the missing prerequisite: absent verdict, missing accepted delivery, or missing failure disposition.
- After that record, `request_correction` returns the break-fix instance to `repair` and the implementation instance to `execution` (CD-0242 D2), and the failure record never reads as an accepted delivery.
