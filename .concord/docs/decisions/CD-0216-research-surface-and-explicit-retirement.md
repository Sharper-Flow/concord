# CD-0216: Research surface and explicit retirement

- **Status:** Accepted
- **Date:** 2026-10-09
- **Approval:** `CON-816` A1 under operator delegation on 2026-10-09, without calendar expiration.
- **Supersedes:** [CD-0025](CD-0025-research-surface.md)
- **Related:** [CD-0215](CD-0215-retained-research-context.md),
  [CD-0036](CD-0036-breaking-law-cutovers.md), PM1 Q11, and PM3

## Context

Agents need reachable authoring, exact research reads, and engine-proven reliance.
The same surface must permit reuse of retained terminal-owner content without allowing content authoring on a terminal owner.
Destruction needs its own explicit authorized operation rather than an archive or read side effect.

## Decision

### D1. One research capability and mutation surface

`concord_work_define` hosts pack create, revision append, finding record, source record, and freshness set under the closed `research` capability and consequence.
Finding and source records remain deterministic idempotent upserts.
Content authoring requires a nonterminal owner and respects sealed consumed revisions.
An authorized freshness update may mark retained content stale without editing that content.

The same tool hosts `research_retire`, the only pack-retirement operation.
It accepts explicit Product scope, 1–100 distinct candidate pack identities with expected versions, dry-run selection, and an idempotency key.
Authorization covers every candidate owner before any destructive effect.
Dry run writes neither pack state nor deletion effects.
Destructive execution revalidates terminal ownership and the absence of every active pin inside one transaction.
The bounded result distinguishes retired and protected candidates and version conflicts.
Invalid input and authorization failures have no partial deletion effects.
Exact committed replay returns the same result; conflicting idempotency reuse refuses.
No standalone unconditional delete, timer, sweep, or destructive read route exists.

### D2. Reliance belongs to the consuming boundary

`concord_work_transition.workflow_action` accepts optional `research_bindings`.
Inside the action transaction, the engine validates authorized scope, pack existence, and exact revision existence.
A retained terminal-owner revision is eligible for reliance while it exists.
Owner terminal status does not certify freshness.
Required non-current freshness refuses fail-closed as `research_consumer_blocked`, surfaced as `stale_requires_review`.
The boundary checks all stored required pins, not only new declarations.

The engine records each consumer pin idempotently per pack, revision, and consuming work item.
Exact replay records no duplicate pin and does not bump the pack version again.
Conflicting declarations require explicit replacement or release at the authorized consuming boundary.
Pin admission changes the pack version only when stored binding state changes.
Optional pins do not create a PM4 blocker but protect content equally from retirement.
Consumer terminalization releases its pins transactionally before projection removal.
There is no standalone bind or unbind operation whose recorded declaration substitutes for actual reliance.

### D3. One research read path

`concord_work_trace.research` exposes pack content by identifier and research owned by a selected work item.
Exact revision reads and bounded owner descriptors preserve scope checks and explicit continuation or omissions.
A bounded nested prefix cannot claim to be a complete corpus.
Terminal-owner reads remain available without destructive reconciliation.
Shared context carries pack, revision, and finding references with bounded selected provenance, never a stored corpus copy.

## Alternatives considered

- Standalone binding creates a reliance record without a boundary that consumes it.
- Operator-only research contradicts the agent-reachable research objective.
- Archive-driven deletion makes publication unsafe for active consumers.
- An unconditional delete bypasses lifecycle, scope, version, and consumer protection.

## Consequences

Direct callers use the sole pack-operation idempotency boundary; agent envelopes compose its transaction-scoped cores with envelope-owned idempotency.
Trusted-client policy and grant capability allowlists continue to admit `research`.
All terminal routes release consumer pins; terminalization does not delete owned packs.
The parent integrates schemas, surface versioning, implementation, and migration without a parallel legacy cleanup route.

Before deployment, CD-0036 requires active CD-0025 consumers to recover onto CD-0216, or cancel or supersede their work.
This offline edit provides no live consumer enumeration, re-contracting receipt, or release activation evidence.
The manifest records no fabricated Concord workflow contract.

## Verification

The full surface loop must author, read, bind, mark stale, and refuse required stale progress through real dispatch and engine boundaries.
Retirement must prove batch bounds, every-owner authorization, optional-pin protection, dry-run absence of writes, version fences, and replay.
Retained-owner reuse must preserve exact revisions and source provenance while terminal content writes refuse.
`internal/agent.TestResearchRetireDryRunAndReplay` is a candidate anchor for dry run and replay, not the complete surface obligation.

Candidate anchors include `internal/agent.TestResearchRetireAuthorizationBeforeBatchEffects`,
`internal/agent.TestResearchRetireSchemaBounds`, and
`internal/agent.TestResearchRetireDryRunAndReplay` in `internal/agent/research_surface_test.go`.
They are source references, not execution receipts or complete coverage claims.
Integrated coverage remains unmeasured until the parent supplies the missing witnesses.
