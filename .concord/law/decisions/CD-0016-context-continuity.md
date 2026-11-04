---
{
  "id": "CD-0016",
  "title": "Context continuity",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [
    "agent-adapter",
    "agent-call-boundary",
    "agent-contract",
    "agent-surface"
  ]
}
---
# CD-0016: Context continuity

**Status:** Accepted under operator approval.
**Approval date:** 2026-08-11.
**Approval:** Operator-approved GitHub issue #42.

## Decision

Agents obtain current work state through explicit Concord reads, including
`concord_work_trace.continuity` with a work ID. Concord injects no automatic
per-turn context and derives no work identity from worktree ownership or a
directory-to-work locator. CD-0252 supplies the core-derived resume plan and
addressed handoff for explicitly selected work through store-write-free
`session-prepare`.
The store keeps current rows; no event log, fold, or replay exists
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Concord names this capability **context continuity**, not compaction. Product
and workflow authority is never carried by a model summary. On every call,
Concord derives a pinned projection from durable state containing Product/work
identity, the current workflow step, the approved contract, the complete
explicit `spec_mandate` law set, the latest
durable checkpoint, and any unresolved typed failure.

The continuity projection carries the current workflow step's `step_actions`
from its verified definition as state, not methodology. Existing holds still
suppress managed step actions; no workflow in flight yields an empty set.
`ManifestDigest` identifies the generated agent-tool contract for skew checks.

The preferred boundary is a clean restart, but restart dispatch remains
structurally unavailable until the registered/versioned Concord-owned typed
agent registry required by issue #57 exists. Unknown agent type, version, or
ruleset digest must fail closed. Concord does not provide a generic-agent
fallback.

Summaries are advisory and lossy. They are allowed only after a completed
durable unit, never during an effect, decision, or repair. A summary cannot
carry authority, approval, law, workflow position, or evidence claims.

## Durable mechanism

Two current-row tables hold the continuity state (each mutation writes current rows in one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history))):

- `workflow_context_checkpoints`: one immutable bounded working-state record per
  work/version/sequence;
- `workflow_context_boundaries`: monotonic summary-boundary history with its
  checkpoint reference, source workflow/attempt identity, and reserved typed
  agent identity fields for the future restart dispatcher.

The `checkpoint_context` and `cross_context_boundary` actions record the
checkpoint and boundary rows through the existing
`concord_work_transition.workflow_action` operation. The latter currently
accepts `summary` only; `restart` produces a typed unavailable failure and no
row.
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

The canonical read is `concord_work_trace.continuity`. It returns a closed continuity
snapshot with the derived pinned projection, latest checkpoint, authenticated
cursor history, one source watermark, monotonic boundary count, and explicit
typed restart availability. An oversized projection travels as lossless,
authenticated JSON-fragment pages, never as an authority-bearing summary.
Each page binds one complete projection digest and states its remaining bytes
and continuation. The read refuses a continuation whose projection changed, so
a completed page sequence is one projection. A consumer joins every page before
it uses the projection's authority. The required content and pre-write reading
obligations remain unchanged.

The read returns the inline payload whenever the complete result fits the
51,200-byte envelope limit. Otherwise each page carries the largest whole UTF-8
fragment that fits that limit with its metadata and signed envelope. Every page
carries at least one code point, so reconstruction always advances.

## Work context

The work-context declaration surface this section governed is retired
([CON-1571](https://linear.app/sharper-flow/issue/CON-1571) end state 2, CD-0243 D3):
the `record_work_context` action, the `workflow.work_context_recorded` event,
the `work_context*` projections and their reads are gone. Migration 186 drops
the tables; frozen migration replay code keeps historical stores upgradeable.
No current action, fold, or read produces or consumes a work-context
declaration. The context-checkpoint and context-boundary actions above are
unaffected.

## Consequences

Context continuity is held in current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)) and does not add a
mutable daemon or a second agent identity authority. Session
preparation and explicit continuity reads supply continuity to operator
sessions; no generic host hook or `agent_definitions` table exists.
