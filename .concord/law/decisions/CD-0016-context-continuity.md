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

**Status:** Accepted.
**Date:** 2026-08-11.

## Decision

Agents obtain current work state through explicit Concord reads. Concord injects
no automatic per-turn context and derives no work identity from worktree
ownership or a directory-to-work locator. CD-0252 supplies the core-derived
resume plan and addressed handoff for explicitly selected work through
store-write-free `session-prepare`. The store keeps current rows; no event log,
fold, or replay exists
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Product and workflow authority is never carried by a model summary. Summaries
are advisory and lossy. They are allowed only after a completed durable unit,
never during an effect, decision, or repair. A summary cannot carry authority,
approval, law, workflow position, or evidence claims.

The agent surface retires `concord_work_trace.continuity`, its continuity
snapshot and authenticated projection-page transport, `checkpoint_context`,
`cross_context_boundary`, their `workflow_context_checkpoints` and
`workflow_context_boundaries` tables, and typed restart availability. No
replacement or compatibility path remains for these retired surfaces.
`concord_work_trace.resume` supplies the step plan and resume fragments;
item-scoped `concord_work_browse.list` with `detail=full` supplies law context,
pending handoff and native runs. Mutation results retain the WorkPin and
derived `next_valid_intents` under CD-0113 D1–D3; refresh intents that require
current work facts use item-scoped list.

## Consequences

Session preparation and explicit reads supply current state to operator
sessions; no mutable daemon, generic host hook, `agent_definitions` table, or
second agent identity authority exists.
