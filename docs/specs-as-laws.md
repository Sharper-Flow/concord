# Concord Specs-as-Laws: Conflict Surfacing & Evolution

> **Status:** Aligned v2 under CD-0006, CD-0015, CD-0035, CD-0036, and
> CD-0041. Those accepted decisions are binding; this companion explains them.
> **Purpose:** How Concord makes pruned, architecture-bound specifications its
> primary Product deliverable while preserving human legislative authority and
> preventing silent scope cuts or contradictory concurrent work.
> **Origin:** User direction, 2026-07-25.

## TL;DR

Specs are Product law, organized through one canonical Domain architecture. When
a **user's request** challenges existing law, Concord surfaces the conflict and
lets the user choose: *clarify intent*, *evolve the spec*, or *consciously accept
scope reduction*. Agents never silently cut scope. Concurrent Product-changing
work also declares its Domain footprint and exact law revisions, so two cleanly
merging changes cannot silently enact contradictory Product truth. **The user is
the legislator.**

---

## 1. The problem this fixes

ADV today: specs are laws; `adv_change_validate` detects conflicts; agents are
pushed back. But when a user request implies behavior that conflicts with a spec,
agents too often **silently cut scope** during research / design / prep — *"this
goes against spec law X, so I'll drop that part"* — instead of pausing to ask the
user whether they want to evolve the law. The user discovers the scope reduction
**late**, after the agent already complied.

**Root cause:** the conflict is treated as *"agent must comply"* rather than
*"user must decide whether to evolve the law."* The legislator (the user) is
bypassed.

---

## 2. The principle: specs are laws, the user is the legislator

- **Specs are laws:** agents respect them; drift is prevented. (ADV strength —
  unchanged.)
- **The user is the legislator:** only the user can evolve a law, via an explicit
  spec delta. Agents *propose* evolution; they do **not** unilaterally enact it,
  and they do **not** unilaterally cut scope to avoid it.
- **A conflict between user intent and a spec law is a *legislative* moment, not a
  *compliance* moment.** It surfaces to the user.

---

## 3. The conflict-surfacing flow

When a user request (during research / design / prep) touches behavior governed by
an existing spec law **and** the request implies conflict or stretch:

1. **DETECT** — identify the specific spec law(s) challenged.
2. **PAUSE** — the agent must **not** silently cut scope. It halts and surfaces.
3. **PRESENT CHOICE** — the user gets explicit options (§4).
4. **RECORD** — whichever path is chosen, the decision is auditable (§5).

This flow is a **required step**, not optional agent behavior.

---

## 4. The three options

| Option | Meaning | When |
|---|---|---|
| **(a) Clarify intent** | The conflict may be a misunderstanding; disambiguate the request. No spec change. | The request is ambiguous or the agent misread the law's scope. |
| **(b) Evolve the spec** | The user wants to change the law. The agent proposes a **spec delta** (modify / remove / rename via the existing delta tools) as part of the change. Evolution is first-class, not a workaround. | The request genuinely requires the law to change. |
| **(c) Consciously accept scope reduction** | The user agrees to cut scope to comply. Recorded explicitly as a user decision — never silent. | The user decides the law should stand and the scope cut is acceptable. |

---

## 5. Structural enforcement (not just agent policy)

Silent scope-cut can't be fixed by instruction alone — agents will still drift.
Concord enforces **structurally**:

- A check that detects when a change's scope was reduced relative to the user's
  request **and** a spec law governed the cut area → blocks unless the user
  explicitly chose (c) or evolved the spec (b).
- The conflict-surfacing step is **required**, not advisory.
- (Exact enforcement mechanism — gate hook, validator invariant, or structural
  block — is a design question; see §7.)

CD-0006 fixes the workflow policy:

- detect conflicts at every scope-changing boundary;
- gather and resolve them during planning so execution normally runs against settled
  law;
- group requirements that share one root conflict while keeping unrelated conflicts
  separate;
- present one planning checkpoint, then ask the human one decision at a time;
- let agents explain and draft, but require human approval for every law change or
  governed scope reduction;
- route newly discovered execution conflicts back to planning; and
- block completion while any law conflict remains unresolved.

CD-0006 D10 adds the **spec mandate** refinement:

- When planning approves spec deltas, the approved change contract carries a
  mandate listing exactly which specs that change may modify.
- During execution, modifications to mandated specs are authorized work, not law
  violations. This prevents the Advance failure mode where a change created to
  replace a spec blocks itself during execution.
- Specs outside the mandate still enforce as laws.
- Completion verifies delivered changes match the approved mandate.

CD-0036 separates compatible amendment from breaking replacement:

- a compatible amendment keeps the same stable law ID and changes its content
  hash;
- a replacement that requires active consumers to stop uses a new law ID plus
  the existing `supersedes` relation;
- every supersession is a strict cutover: old consumers may only supersede their
  contract onto the accepted successor or make their work terminal; and
- workflow contracts record the exact `(law_id, content_hash)` revisions used at
  planning, while Git remains the sole law author.

This rule removes the overwrite-or-replace guess from workflow authority. The
operator's Git delta declares the answer, and Concord enforces its consequence.

CD-0041 makes Product law architecture-bound and primary:

- every current specification and decision has exactly one home Domain;
- every Product-changing work contract names one home Domain, all affected
  Domains, exact governing law revisions, authorized additions/modifications,
  and verification obligations;
- overlapping affected Domains require an operator-approved, version-pinned
  compatibility, sequencing, merger, supersession, or terminal resolution before
  both items hold execution authority;
- changing either work contract invalidates its prior overlap resolution; and
- the checks rerun transactionally at every authoritative consequential action.

Exact law-write overlap is not the only trigger. Independently introduced law can
contradict inside one Domain without sharing an ID, so same-Domain overlap still
requires an explicit decision. Semantic similarity may suggest that review; it
cannot author the decision.

The current-law view stays pruned structurally: one canonical Domain home per law,
explicit supersession, no dangling relations, and superseded law outside the
default browse path. Semantic deduplication remains human-owned because no
heuristic can prove two obligations equivalent.

---

## 6. Auditability

Every spec-law challenge during a change is recorded: *which law, which request,
which option chosen, by whom, when.* No silent compliance. The change's history
shows the legislative moments explicitly.

---

## 7. Relationship to predecessor evidence

| Public predecessor behavior | Concord treatment |
|---|---|
| Change validation detected spec conflicts | **Preserved as an outcome, redesigned structurally** through Git-derived law checks and typed refusals. |
| Spec deltas modified or superseded accepted law | **Preserved as an outcome** through operator-approved Git law deltas and revision identity. |
| Agreement/design checkpoints surfaced legislative choices | **Preserved as human authority** without requiring one universal workflow shape. |
| Cross-change architecture overlap was not authoritative | **Added by CD-0041** through Domain-bound contracts and version-pinned resolutions. |

Concord does not call, mirror, or dual-write predecessor runtime state. These rows
record public lesson evidence only.

---

## 8. Implementation state

CD-0035 capture-time governing-requirement enforcement and CD-0036 breaking-law
cutovers are implemented. CD-0041 is accepted constitutional law, but its Domain
identity, architecture-bound contract, overlap-resolution, Initiative migration,
and read-surface mechanisms remain follow-up implementation work. Issue #192 does
not claim those runtime outcomes are complete.

---

## 9. Relationship to other docs

| Doc | Link |
|---|---|
| `feature-inventory.md` §2.8 | The capability entry. |
| [`specs-as-laws.md`](./specs-as-laws.md) §2 | The guiding principle: specs are laws, the user is the legislator. |
| [`decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](./decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md) | **Accepted CD-0012** — the counterpart direction. This document governs scope **contraction** under spec-law pressure; CD-0012 governs outcome **substitution and dilution**, reusing this document's three-option flow and audit shape. Nothing in this document is altered by it. |
| [`decisions/CD-0036-breaking-law-cutovers.md`](./decisions/CD-0036-breaking-law-cutovers.md) | **Accepted CD-0036** — exact revision pins, compatible same-ID amendments, and strict quiescence on law supersession. |
| [`decisions/CD-0041-architecture-bound-product-law.md`](./decisions/CD-0041-architecture-bound-product-law.md) | **Accepted CD-0041** — Domain-owned law, architecture-bound work contracts, concurrent-overlap resolution, Initiative's secondary role, and retained SQLite authority. |

---

*Specifications are Concord's Product deliverable. Laws bind agents through one
Domain architecture; only the legislator can change them or accept a scope cut.*
