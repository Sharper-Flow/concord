# CD-0016: Context continuity

**Status:** Accepted under operator approval.
**Approval date:** 2026-08-11.
**Approval:** Operator-approved GitHub issue #42.

> **Subsequent decision:** CD-0031 supplies automatic core-derived continuity to launcher-started operator sessions. The absence statement in §Consequences records the state when CD-0016 was accepted.

## Decision

Concord names this capability **context continuity**, not compaction. Product
and workflow authority is never carried by a model summary. On every call,
Concord derives a pinned projection from durable state containing Product/work
identity, the current workflow step, the approved contract, the complete
explicit `spec_mandate` law set, any pending operator decision, the latest
durable checkpoint, and any unresolved typed failure.

The preferred boundary is a clean restart, but restart dispatch remains
structurally unavailable until the registered/versioned Concord-owned typed
agent registry required by issue #57 exists. Unknown agent type, version, or
ruleset digest must fail closed. Concord does not provide a generic-agent
fallback.

Summaries are advisory and lossy. They are allowed only after a completed
durable unit, never during an effect, decision, or repair. A summary cannot
carry authority, approval, law, workflow position, or evidence claims.

## Durable mechanism

Workflow events own two fold-only projections:

- `workflow_context_checkpoints`: one immutable bounded working-state record per
  work/version/sequence;
- `workflow_context_boundaries`: monotonic summary-boundary history with its
  checkpoint reference, source workflow/attempt identity, and reserved typed
  agent identity fields for the future restart dispatcher.

The closed event kinds are `workflow.context_checkpointed` and
`workflow.context_boundary_crossed`. Generic workflow actions expose
`checkpoint_context` and `cross_context_boundary` through the existing
`concord_work_transition.workflow_action` operation. The latter currently
accepts `summary` only; `restart` produces a typed unavailable failure and no
event or row.
Both actions are closed, unapproved actions owned by every built-in definition
and every step through one definition builder. They use the normal definition
lookup, step allowance, payload validation, and operator-selection paths.

Boundary eligibility is structural and transaction-bound: the boundary must
reference the latest checkpoint and current workflow identity, no nonterminal
effect/attempt may be active, pending decisions reject summaries, and the
required working state must already be durable. Bounds are active unit 256;
hypothesis, diagnosis, and strategy 4096 each; unique touched/evidence refs 64
each; pending questions/decisions 16 each; summary 16 KiB; and history pages
20. Token counts and model judgement never authorize a boundary.

The canonical read is `concord_work_trace.continuity`, added in surface 2.3.0
while retaining 2.1/2.2 negotiation and replay. It returns a closed continuity
snapshot with the derived pinned projection, latest checkpoint, authenticated
cursor history, one source watermark, monotonic boundary count, and explicit
typed restart availability. The launcher identity handoff remains unchanged.

## Consequences

Context continuity is rebuildable from SQLite event authority and does not add a
mutable daemon or a second agent identity authority. Future issue #57 dispatch
must inject this canonical projection on every typed-agent call; this decision
does not claim that current OpenCode calls receive automatic pinned-context
injection, and does not add a generic host hook or an `agent_definitions` table.
