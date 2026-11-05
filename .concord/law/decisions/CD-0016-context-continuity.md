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

Agents obtain current work context through explicit Concord reads. Concord
injects no automatic per-turn context and derives no work identity from worktree
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
pending handoff and native runs. Mutation results retain the WorkPin, which
carries the work-context view below, and derived `next_valid_intents` under
CD-0113 D1–D3; refresh intents that require current work facts use item-scoped
list.

## Work context

The next version of each built-in definition adds the closed, unapproved
`record_work_context` action to every step except the delivery gates. The
action records the work-context state with ordered required reading,
references to earlier findings, and new typed findings. A reading source is a
repository file pinned by Project, path, and commit OID, or a law revision
pinned by source, law ID, and content hash. Each reading and finding names a
registry Domain.

One transaction-scoped reader assembles the current work-context view from
these records and retained worker findings. The reader
adds no table and no migration. The work pin carries
the view. The view holds at most 32 readings and 32 findings within 64 KiB. A
view past these bounds refuses the read and leaves the records unchanged. Each
recorded finding keeps its source record and ordinal and stays a reported
claim. Completion copies no work-context content into another record.

A declaration refuses without effects if its resulting shared view exceeds the bounds. The
check runs once in the live action transaction that records the declaration. A
declaration that restores a bounded view remains admissible.

When the shared reader refuses with `limit_exceeded`, the work pin carries the
closed `work_context_unavailable: {"reason":"limit_exceeded"}` member instead
of `work_context`. The two members are mutually exclusive. A
successfully evaluated absent context emits neither member. Other reader errors
propagate. The ambiguous-contract partial-pin exit remains unchanged.
Strict readers still refuse the over-bound view.

Declared readings retain exact source identity. A repository-file
reading resolves in the declared Project's own repository: the pinned commit
must resolve as one commit object, and the pinned path must resolve as one
ordinary blob at that commit. The current checkout is never the proof, so a
changed or deleted checkout file cannot invalidate a reachable pinned
commit, and a missing object refuses. A law reading resolves through the
Project knowledge home its `source_id` names. The law ID and content
hash must join that source's exact projected law revision, and the
revision's canonical locator must resolve in the git-derived projection. A
moved law revision whose canonical locator no longer exists refuses as
unavailable; an old recorded locator that still resolves stays valid. The
store never rescans Git history to guess a home, never repins the declared
hash, and keeps no replacement revision registry. Resolution never registers
a source, fetches from the network, or mutates the declared readings. The
resolution does not authorize or start a subagent. A failure names the
reading's ordinal and source identity.
No replay or rebuild re-resolves a reading.

The view bounds constrain the derived view, not retained evidence. A bounded
read never trims a finding into a different claim.

## Consequences

Session preparation and explicit work-context reads supply current state to
operator sessions; no mutable daemon, generic host hook, `agent_definitions`
table, or second agent identity authority exists.
