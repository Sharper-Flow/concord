# CD-0137: Iterative correction preserves result disposition

- **Status:** Accepted
- **Date:** 2026-09-12
- **Scope:** Worker result disposition and fresh-attempt correction
- **Related:** CD-0008, CD-0017, CD-0027, CD-0112, CD-0113, CD-0130, CD-0133, CD-0148, CD-0193, CD-0205
- **Preserves:** Approved contracts, immutable attempt history, and fail-closed completion
- **Approval:** The Snowball coordinator approved this law change on 2026-10-16 under the operator's delegation.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2

## Context

Worker failure recovery records transport failure, but a completed worker result
has no durable rejection route. A rejected result therefore cannot teach the next
attempt what failed or prevent reuse of the same attempt identity.

## Decision

### D1. Record a result disposition

The workflow accepts or rejects each completed worker result exactly once. A
rejection records its attempt, diagnosis, strategy, predicate IDs, and evidence.

At a worker-dispatch step other than a `human_checkpoint`, rejection names
the completed result's own attempt and epoch. Its materialized dispatch and
completion, and its recorded authorization when one exists, must identify
that work and step.
The rejecting actor must differ from that attempt's executor, not merely
from the latest executor on the work item. A prior acceptance, evidence
acceptance, or rejection of that target refuses another rejection, even
when another result remains open.

A completed result's rejection interval opens at its own dispatch start.
The interval remains open across later dispatch starts at the same step
under the same contract. A later review does not prevent
rejection of the original result. A capability class establishes neither
replacement nor evaluation of another result. A non-dispatch fenced start,
an actual step transition, a corrective return, or contract supersession
closes the interval. Returning to the same step does not revive an earlier
result. Historical attempts without recorded dispatch authorization keep
their existing single-window eligibility and gain no cross-epoch authority.

Rejection holds the step and preserves every attempt's identity and report.
It does not grant an earlier result authority to advance the step.
Acceptance and failure retain their existing epoch fences. Human checkpoints
retain their existing timing and hold rules. A checkpoint disposition names
its exact target, and a disposed target refuses another disposition.

An unconsumed rejection of a result without a worker-job binding blocks
an advancing acceptance at that step. A review completed before the rejection
cannot bypass the correction. A fresh materialized dispatch must consume
the correction context under D2 before an acceptance can advance. Hold-mode
evidence acceptance and the operator's checkpoint gate keep their existing
rules. Job-bound correction retains its obligation-discharge and local
acceptance rules.

The CD-0133 D4 exception for `accept_worker_result` describes the dispatch
hold only. While an unconsumed rejection holds the step, the pin also omits
an advancing `accept_worker_result` because the fold refuses the advance.
The pin and fold must agree.

The work pin, action admission, and event fold use the same eligible-result
population. Admission and fold validate the exact requested attempt and epoch,
not the existence of another eligible result. For disposition validation,
each fold reads only the event prefix before its own event.
Future events cannot authorize or invalidate that disposition.

For oracle-capable histories, `reject_worker_result` and `request_correction`
also bind `open_finding_ids` to the exact derived ranked-finding set.
The field is required for a nonempty set and absent exactly for an empty
set. Predicate IDs are not finding IDs. Supported `resolved_findings`
closures name canonical IDs and qualifying evidence (CD-0056, CD-0197).
A shorter list, omitted finding, relabeling, or changed confidence closes
nothing. Explicit contract supersession retains its existing authority.

### D2. Require a fresh correction attempt

A failed or rejected result exposes bounded correction context through the work
pin and lane packet. A fresh packet must consume that context before dispatch.
The context names the source event of the record that opened the correction,
and the failed or rejected attempt when one exists. Dispatch compares these
identities, so an equal diagnosis and strategy from another record do not
consume the correction.

The same reader carries every open oracle blocker and retained receipt
reference into correction context. Earlier-subject receipts remain baselines,
not proof for the new subject. New uncovered cases remain blockers until
supported closure or contract supersession. A report's closure claim neither
changes the once-only result disposition nor supplies workflow acceptance.

### D3. Bound retries

The workflow permits at most three worker attempts for one correction sequence.
The next dispatch requires a diagnosis and strategy. The fourth dispatch requires
operator escalation and is refused by the engine.

### D4. Preserve historical pins

The rejection route is a recovery action outside the pinned definition action
list. It preserves historical definition digests and all prior attempt records.

## Verification

- Completed worker results expose accept and reject dispositions.
- Failed and rejected results expose bounded correction context.
- Fresh dispatch rejects missing or stale correction context.
- The fourth attempt is refused and prior attempts remain unchanged.
- Work-pin and packet journeys use the same correction projection.
- Original-result rejection after a later same-step review preserves both attempt identities and the review judgment.
- Forged pairs, prior dispositions, and closed rejection intervals refuse without a durable effect.
- A stale result cannot advance, and an earlier review cannot bypass an unconsumed rejection correction.
- Work-pin admission, single-result rejection, and human-checkpoint protections
  retain the same disposition boundaries.
- `TestOwnerOracleConvergence`, `TestOwnerOracleRequestOpenFindingsComparison`, and `TestOwnerOracleEmptyDerivedSetEquality` cover exact correction sets and supported shrinkage.
