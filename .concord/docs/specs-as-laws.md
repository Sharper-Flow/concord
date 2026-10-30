# Concord Specs-as-Laws: Conflict Surfacing & Evolution

> **Status:** Aligned v2 under CD-0006, CD-0015, CD-0035, CD-0036,
> CD-0222, and CD-0230. Those accepted decisions are binding; this
> companion explains them.
> **Purpose:** How Concord makes pruned, architecture-bound specifications its
> primary Product deliverable while applying the six operator approval classes
> and preventing silent scope cuts or contradictory
> concurrent work.
> **Origin:** User direction, 2026-07-25.

[CD-0243 D7](decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain required.

## TL;DR

Specs are Product law, organized through one canonical Domain architecture. When
a **user's request** challenges existing law, Concord identifies and records the
conflict. An agent evolves law within its scope and records why; it asks the
operator only when an approval class applies or information needed to decide is
missing. Agents never silently cut scope. Concurrent Product-changing
work also declares its Domain footprint and exact law revisions, so two cleanly
merging changes cannot silently enact contradictory Product truth. **The
approval boundary follows the six classes, not seriousness, size, or Domain
count ([CD-0243](decisions/CD-0243-four-roles-and-six-operator-approval-classes.md) D2-D4).**

---

## 1. The problem this fixes

ADV today: specs are laws; `adv_change_validate` detects conflicts; agents are
pushed back. But when a user request implies behavior that conflicts with a spec,
agents too often **silently cut scope** during research / design / prep — *"this
goes against spec law X, so I'll drop that part"* — instead of explicitly
resolving the conflict. The user discovers the scope reduction
**late**, after the agent already complied.

The conflict needs an explicit resolution, not an unrecorded scope cut. The
approval boundary determines whether that resolution belongs to the operator
or the agent.

---

## 2. The principle: specs are laws; law-change authority follows CD-0230

- **Specs are laws:** agents respect them; drift is prevented. (ADV strength —
  unchanged.)
- **Law changes use the six approval classes:** operator approval is required
  only under CD-0243 D2. Seriousness, size, and Domain count are not independent
  triggers. Outside those classes, the agent changes law within scope and records
  why. A revision enacted without operator approval has the `derived` authority
  tier, including when the prior revision was operator-approved
  ([CD-0230](./decisions/CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md) D4).
- **A law conflict requires an explicit resolution.** Ask the operator when a
  class applies or needed information is missing; otherwise decide and record
  the reason. Never silently cut governed scope to avoid the conflict.

---

## 3. The conflict-surfacing flow

When a user request (during research / design / prep) touches behavior governed by
an existing spec law **and** the request implies conflict or stretch:

1. **DETECT** — identify the specific spec law(s) challenged.
2. **Classify** — apply CD-0243 D2-D4. A conflict alone does not require approval.
3. **Resolve** — use §4. Ask only for an applicable approval class or missing
   information; otherwise decide within scope and record why. Keep current tool
   gates and admitted recovery routes (CD-0243 D6-D7).
4. **RECORD** — whichever path is chosen, the decision is auditable (§5).

This flow is a **required step**, not optional agent behavior.

---

## 4. The three options

| Option | Meaning | When |
|---|---|---|
| **(a) Clarify intent** | The conflict may be a misunderstanding; disambiguate the request. No spec change. | The request is ambiguous or the agent misread the law's scope. |
| **(b) Evolve the spec** | The agent records an explicit law delta and its reason. Operator approval follows the six classes; without it, the revision is `derived`. | The request genuinely requires the law to change. |
| **(c) Consciously accept scope reduction** | The user agrees to cut scope to comply. Recorded explicitly as a user decision — never silent. | The user decides the law should stand and the scope cut is acceptable. |

---

## 5. Structural enforcement (not just agent policy)

Silent scope-cut can't be fixed by instruction alone — agents will still drift.
Concord enforces **structurally**:

- A check that detects when a change's scope was reduced relative to the user's
  request **and** a spec law governed the cut area → blocks unless the user
  explicitly chose (c) or the law was explicitly evolved through (b).
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
- apply the six approval classes to law changes, record the reason for agent-owned
  amendments, and retain operator approval for governed scope reductions
  ([CD-0230](./decisions/CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md) D4; CD-0243 D2-D7);
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
- a breaking supersession marks stale exactly the contracts that consumed the
  superseded revision until recovery, and each refusal names an admitted next
  action (CD-0242 D7, D12); and
- workflow contracts record the exact `(law_id, content_hash)` revisions used at
  planning, while Git remains the sole law author.

This rule removes the overwrite-or-replace guess from workflow authority. The
operator's Git delta declares the answer, and Concord enforces its consequence.

CD-0222 makes Product law architecture-bound and primary:

- every current specification and decision has exactly one home Domain;
- every Product-changing work contract names one home Domain, all affected
  Domains, exact governing law revisions, authorized additions/modifications,
  and verification obligations;
- concurrent edits to the same law or Domain reconcile through Git, without a
  shared-Domain execution gate or compatibility resolution (CD-0222 D6); and
- the checks rerun transactionally at every authoritative consequential action.

Independently introduced law can contradict inside one Domain without sharing
an ID. The later-merging item integrates main and reassesses changed dependencies
under CD-0222 D6-D7. Semantic similarity cannot author a law decision.

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
| Spec deltas modified or superseded accepted law | **Preserved as an outcome** through Git law deltas and revision identity, with approval following CD-0243 D2 and D4. |
| Agreement/design checkpoints surfaced legislative choices | **Preserved as explicit decisions** with operator consent only under the six classes, without requiring one universal workflow shape. |
| Concurrent changes may affect the same architecture | **CD-0222 D6-D7** requires Git integration and dependency-specific revalidation, not an overlap gate. |

Concord does not call, mirror, or dual-write predecessor runtime state. These rows
record public lesson evidence only.

---

## 8. Implementation state

CD-0035 capture-time governing-requirement enforcement and CD-0036 breaking-law
cutovers are implemented. CD-0222 is accepted constitutional law, but its Domain
identity, architecture-bound contract,
and read-surface mechanisms remain follow-up implementation work. Issue #192 does
not claim those runtime outcomes are complete.

---

## 9. Relationship to other docs

| Doc | Link |
|---|---|
| [`specs-as-laws.md`](./specs-as-laws.md) §2 | The guiding principle: specs are laws; law-change authority follows CD-0230 D4. |
| [`decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](./decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md) | **Accepted CD-0012** — the counterpart direction. This document governs scope **contraction** under spec-law pressure; CD-0012 governs outcome **substitution and dilution**, reusing this document's three-option flow and audit shape. Nothing in this document is altered by it. |
| [`decisions/CD-0036-breaking-law-cutovers.md`](./decisions/CD-0036-breaking-law-cutovers.md) | **Accepted CD-0036** — exact revision pins, compatible same-ID amendments, and digest-based staleness on law supersession (CD-0242 D7). |
| [`decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md`](./decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md) | **Accepted CD-0222** — Domain-owned law, architecture-bound work contracts, Git reconciliation of concurrent changes, and retained SQLite execution authority. CD-0213 places business grouping in Linear. |

---

*Specifications are Concord's Product deliverable. Laws bind agents through one
Domain architecture; law changes follow CD-0230 D4, and only the operator
accepts a governed scope cut.*
