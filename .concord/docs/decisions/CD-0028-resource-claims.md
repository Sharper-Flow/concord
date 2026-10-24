# CD-0028: Durable resource claims for concurrent agents

- **Status:** Accepted
- **Amended:** 2026-10-18. [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
  D2 and D14 restate the claim store as current rows with no historical pins.
- **Date:** 2026-08-15
- **Scope:** Agent tool surface; work-item claim state
- **Related:** CD-0016 (continuity), CD-0006 D8 (single operator), issue #88
- **Issue:** #88 (fc6, final row)

## Context

Two legitimate workstreams can hold plausible reasons to mutate the same
external thing — a fence over scheduled jobs, a test database, a deployment
slot, a migration lock. The observed incident was resolved by a human
relaying one sentence of prose between sessions; nothing structural made the
first holder's intent visible to the second, and nothing would have recorded
a collision. A third actor outside the coordination system entirely (a
deployment pipeline) could and did partially undo the held state, so no claim
model may assume it is the only writer.

## Decision

A resource claim grants no approval or workflow authority. Terminal-transition consent follows CD-0013 D11: routine acceptance-backed completion needs no operator approval, and cancellation and work removal require it only when a CD-0243 D2 class applies. All lifecycle, evidence, dependency, and claim-release invariants remain required. Identities active work needs derive at the integration boundary ([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

**D1. A claim is a durable record of intent, not a lock.**
`concord_work_relate.resource_claim` records that one work item holds one
typed resource key for a stated reason. `resource_release` releases it. The
`resource_claims` table is one row per key (each mutation writes current rows in one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history))): the second claimant
receives a typed refusal (`resource_claim_held`) naming coordination, not
authority. The same holder re-claiming is an idempotent replay.

**D2. The holder is the work item, not the session.** Claims attach to work
items as versioned current rows, so they survive session
restart, compaction, and context loss by construction — the same durability
every work fact has. A terminal transition of the holding
work releases everything it still holds: completion, cancellation, or
supersession cannot deadlock a resource.

**D3. Resource keys are typed, not free strings.** A key is a lowercase type
prefix and a bounded identifier (`fence:prod-pause`, `db:analytics`,
`slot:eu-deploy`), enforced by pattern in the claim write, the payload schema, and
the read. Untyped claims would become an informal lock namespace.

**D4. Advisory and honest about it.** A claim grants no authority: holding
one does not bypass any approval, capability, or scope check (asserted by
test). Concord cannot prevent an agent from running an arbitrary command
against a cloud API, and the claim record does not imply exclusivity it never
had — a foreign actor may still mutate a claimed resource, and detection of
that divergence is explicitly out of scope. What the claim provides is
legibility: `concord_work_browse.resource_claims` (PM1.Q13) resolves a key to
holder, reason, and state before another agent acts, and lists held claims
for the ambient Product. A point lookup by resource key is deliberately not
Product-scoped: a claim is a statement about the external resource, not a
Product entity, and cross-Product result sets (C18 §12 anti-requirement 11)
govern Product data, not external-resource intent.

**D5. No polling, timers, or background reconciliation.** Claims never
expire on a clock; they end by release or by their holder's terminal
transition. Expiry would reintroduce exactly the timer-heuristics the
accepted law elsewhere refuses.

## Rejected alternatives

**Enforcement via tool-surface consultation.** Requiring every mutating
operation to check claims would be advisory anyway (arbitrary commands remain
possible) while adding a hot-path join and a false sense of guarantee.

**A lease with expiry.** Deadlock-avoidance by timeout trades determinism for
guessing: too short breaks legitimate long holds, too long blocks after a
crash. Terminal-transition release is the structural answer.

**Free-form resource names.** Rejected by D3's typing rule.

## Consequences

- The `claim` consequence class enters the closed surface vocabulary
  (surface 3.4.0, 43 operations).
- Claims ride expected-version fencing on the holder work item; a claim or
  release is one checked current-row write.
- The incident's compounding factor — foreign mutation — stays visible in
  this record rather than being silently absorbed.

## Verification

`internal/agent/resource_claims_dispatch_test.go`: uncontended claim,
contended claim (typed refusal), discovery by key and Product listing,
release, post-release reclaim, terminal-transition release (the
holder-session-gone case, since holders are work items), and the
no-authority assertion — a claim holder still requires operator approval for
a terminal lifecycle transition.
