# CD-0025: Research surface and engine-bound reliance

- **Status:** Accepted
- **Date:** 2026-08-14
- **Scope:** Agent tool surface; active research packs; workflow action boundary
- **Related:** CD-0009 (active research context), CD-0002/PM3 boundary exception
- **Issue:** #108 (fc6 group 2)

## Context

CD-0009 accepted the active-research model and the store implemented all of
it: packs, revisions, findings, sources, consumer bindings, and a
`RequiredResearchFreshness` query that is exactly the fail-closed
consequential-boundary check D6 describes. None of it was reachable. No agent
operation created a pack, no read path exposed one, and no workflow action
could declare research context — so the two enumerated outcomes ("persist a
reusable research pack", "prove a consumer read a sufficiently fresh revision")
were both not covered, by the same absence.

## Decision

**D1. Authoring lands on `concord_work_define` under a new `research`
capability.** Five mutation operations — pack create, revision append, finding
record, source record, freshness set — host on the define tool because pack
content is working context captured for owned work. Finding and source record
are upserts: deterministic on stored state and idempotent under the caller's
replay. A new closed capability (`research`) and consequence (`research`)
enter the surface vocabulary additively (surface 2.5.0). Pack deletion stays
archive-driven (CD-0009 D7); there is deliberately no delete operation.

**D2. Reliance is declared at the workflow boundary and proven by the engine.**
`concord_work_transition.workflow_action` accepts an optional
`research_bindings` list. Inside the action's own transaction the engine
validates each declaration (pack exists, owner nonterminal, revision exists),
refuses a required binding on non-current freshness fail-closed
(`research_consumer_blocked`, surfaced as `stale_requires_review`), and
records the consumer pin — idempotently per (pack, revision, consumer), so a
replayed action records nothing twice. There is deliberately no standalone
bind operation: reliance declared anywhere other than the boundary that
consumes it is unproven reliance. This is CD-0009 D6's consequential-boundary
query, structurally placed.

**D3. One read path.** `concord_work_trace.research` (PM1.Q11) returns the
pack by identifier or owning work item, so consumers and the authoring session
can read what a revision contains.

## Rejected alternatives

**Standalone bind/unbind operations.** More flexible, more contract surface,
and it re-creates the gap: a binding recorded without a boundary consuming it
carries no reliance proof.

**Restating CD-0009 as operator-internal.** The decision's own conformance
scenario 2 is "two agents editing one pack"; the model was always meant to be
agent-reachable. Recording an exclusion would contradict accepted law rather
than implement it.

## Consequences

- The pack-operation boundary (CD-0009 D4) remains the sole idempotent route
  for direct callers; the agent surface composes `WithinTx` cores that skip the
  research idempotency table because the agent envelope already owns
  idempotency for those operations.
- Binding bumps the pack's `expected_version` only when a row lands.
- Trusted-client policy and grant-request capability allowlists admit
  `research`.
- Consumer-terminal cleanup (CD-0009 D6) was already wired in
  `foldWorkTransitioned`; no change was needed.

## Verification

`internal/agent/research_surface_test.go` drives the full loop through the
real dispatch and engine: author a pack on an owned work item, record a
finding and a source, read the pack back through the trace surface, declare
required reliance at a workflow action on a fresh pack (binding lands,
`RequiredResearchFreshness` reports current), stale the pack, and observe the
next required reliance refused fail-closed.
