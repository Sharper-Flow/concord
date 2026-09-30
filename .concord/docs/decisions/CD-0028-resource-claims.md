# CD-0028: Durable resource claims for concurrent agents

- **Status:** Accepted
- **Date:** 2026-08-15
- **Scope:** Agent tool surface; work-item events; fold projection
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

**D1. A claim is a durable record of intent, not a lock.**
`concord_work_relate.resource_claim` records that one work item holds one
typed resource key for a stated reason. `resource_release` releases it. The
fold projection (`resource_claims`) is one row per key: the second claimant
receives a typed refusal (`resource_claim_held`) naming coordination, not
authority. The same holder re-claiming is an idempotent replay.

**D2. The holder is the work item, not the session.** Claims attach to work
items through ordinary versioned `work.*` events, so they survive session
restart, compaction, and context loss by construction — the same durability
the event log gives every work fact. A terminal transition of the holding
work releases everything it still holds: completion, cancellation, or
supersession cannot deadlock a resource.

**D3. Resource keys are typed, not free strings.** A key is a lowercase type
prefix and a bounded identifier (`fence:prod-pause`, `db:analytics`,
`slot:eu-deploy`), enforced by pattern in the fold, the payload schema, and
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
  release is one versioned event.
- The incident's compounding factor — foreign mutation — stays visible in
  this record rather than being silently absorbed.

## Verification

`internal/agent/resource_claims_dispatch_test.go`: uncontended claim,
contended claim (typed refusal), discovery by key and Product listing,
release, post-release reclaim, terminal-transition release (the
holder-session-gone case, since holders are work items), and the
no-authority assertion — a claim holder still requires operator approval for
a terminal lifecycle transition.
