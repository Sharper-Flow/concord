# Advance predecessor postmortem

**Status:** Public, non-authorizing design evidence.

> **Primary public source:** [Advance #348](https://github.com/Sharper-Flow/Advance/issues/348)

## Purpose

This record distills public predecessor issue evidence into mechanisms Concord must
prevent. It is not a dependency inventory, a runtime contract, or a claim about
current Concord state. Private snapshots, local logs, archive bundles, commit
identifiers, and private product examples are excluded.

## Root cause

The recurring failure pattern is **split state authority**: durable workflow state
and a writable projection can disagree, while callers have no deterministic
convergence owner. A view can therefore report a different lifecycle, active set,
or evidence state than the durable record. Concord prevents this structurally with
one SQLite authority, typed disposable projections, version checks, and explicit
degraded outcomes.

This diagnosis is narrower than “the predecessor's storage technology is wrong.”
The lesson is about competing writers and unverified reads, not a blanket judgment
against any particular database or workflow engine.

## Publicly supported failure lessons

| Public source | Observed mechanism | Concord lesson |
|---|---|---|
| [Advance #348](https://github.com/Sharper-Flow/Advance/issues/348) | Lifecycle, terminal, projection, and gate views can diverge. | One subject has one writable authority; all other views derive from it. |
| [Advance #205](https://github.com/Sharper-Flow/Advance/issues/205) and [#192](https://github.com/Sharper-Flow/Advance/issues/192) | Cross-project serviceability and caller context can be confused. | Resolve Product/Project context structurally and expose serviceability as typed evidence. |
| [Advance #325](https://github.com/Sharper-Flow/Advance/issues/325) | Evidence production and evidence consumption can lose attribution. | Bind proof to an immutable subject and attributable producer record. |
| [Advance #349](https://github.com/Sharper-Flow/Advance/issues/349) and [#354](https://github.com/Sharper-Flow/Advance/issues/354) | Acknowledgement can outrun durable persistence. | Success requires durable proof; partial outcomes remain partial and recoverable. |

## Guardrails against over-generalization

- This evidence does not establish a failure rate.
- It does not establish that every predecessor gate, contract, or orchestration
  mechanism was defective.
- It does not authorize copying predecessor commands, transports, or tool names.
- It does not make the predecessor a Concord runtime dependency.

## Consequences for Concord

1. SQLite is the sole durable authority under CD-0002.
2. Projections are one-way, typed, disposable, and rebuildable.
3. Evidence is immutable-subject-bound and producer-attributable under CD-0008.
4. Unreadable or stale records produce typed degraded or unreachable outcomes;
   they are never silently replaced by a convenient snapshot.
5. Advance remains reference-only. The concise public lesson index is
   [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md).
