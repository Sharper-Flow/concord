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

CD-0031 supplies core-derived continuity to operator sessions through work-start
preparation. CD-0090 requires a bounded per-turn host plugin hook that re-asserts
the pinned projection after context boundaries. This surface holds no authority.
The store keeps current rows; no event log, fold, or replay exists
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Concord names this capability **context continuity**, not compaction. Product
and workflow authority is never carried by a model summary. On every call,
Concord derives a pinned projection from durable state containing Product/work
identity, the current workflow step, the approved contract, the complete
explicit `spec_mandate` law set, the latest
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

## Work context

The next version of each built-in definition adds the closed, unapproved
`record_work_context` action to every step except the delivery gates. The
action records the work-context state with ordered required reading,
references to earlier findings, selected research references, and new typed
findings. A reading source is a
repository file pinned by Project, path, and commit OID, or a law revision
pinned by source, law ID, and content hash. Each reading and finding names a
registry Domain.

One transaction-scoped reader assembles the current work-context view from
these records, recorded Review findings, and exact research
selections resolved from the research authority. The reader
adds no table and no migration. The work pin and the continuity snapshot carry
the view. The view holds at most 32 readings and 32 findings within 64 KiB. A
view past these bounds refuses the read and leaves the records unchanged. Each
recorded finding keeps its source record and ordinal and stays a reported
claim. A research entry keeps its selection record and ordinal, exact pack,
revision, finding identity, native finding fields, and complete linked source
descriptors. Research entries and event-owned findings share the 32-finding
bound. A research selection stores references, never corpus content, in the store.
Completion, checkpoints, and context boundaries copy no work-context content
into another record.

A declaration refuses without effects if its resulting shared view exceeds the bounds. The
check runs once in the live action transaction that records the declaration. A
declaration that restores a bounded view remains admissible.
Recorded Review outcomes, blockers, acceptance and bound verification reports remain retained even
when the resulting view exceeds its read bound.

When the shared reader refuses with `limit_exceeded`, the work pin and continuity
snapshot carry the closed `work_context_unavailable: {"reason":"limit_exceeded"}`
member instead of `work_context`. The two members are mutually exclusive. A
successfully evaluated absent context emits neither member. Other reader errors
propagate. The outside-repair hold and ambiguous-contract partial-pin exits
remain unchanged. Strict readers still refuse the over-bound view.

The shared reader reports missing, stale, rebound, or inactive research
selections as typed entries without content. A required unresolved selection
refuses required reliance. Optional unresolved selections stay
visible without content and do not create a required reliance blocker.
A missing consumer binding cannot establish optional reliance and refuses
reliance. Later revisions never replace selected content. Research body or
selection-state changes participate in the existing full-view equality,
not a second context watermark or digest.

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

Context continuity is held in current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)) and does not add a
mutable daemon or a second agent identity authority. Work-start
preparation and the bounded per-turn hook supply continuity to operator sessions;
no generic host hook or `agent_definitions` table exists.
