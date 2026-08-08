# Concord Specs-as-Laws: Conflict Surfacing & Evolution

> **Status:** Draft v1. Companion to [`README.md`](./README.md),
> [`feature-inventory.md`](./feature-inventory.md).
> **Purpose:** How Concord evolves ADV's "specs as laws" model — preserving the
> pushback strength while fixing the **silent-scope-cut** failure with proper
> HITL conflict surfacing.
> **Origin:** User direction, 2026-07-25.

## TL;DR

Specs remain laws — agents still get pushed back when they'd violate one. But
when a **user's request** challenges an existing spec law, Concord **surfaces the
conflict via HITL** and lets the user choose: *clarify intent*, *evolve the spec*,
or *consciously accept scope reduction*. Agents **never silently cut scope** to
comply with a law — **the user is the legislator.**

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

---

## 6. Auditability

Every spec-law challenge during a change is recorded: *which law, which request,
which option chosen, by whom, when.* No silent compliance. The change's history
shows the legislative moments explicitly.

---

## 7. Relationship to ADV today

| ADV today | Concord treatment |
|---|---|
| `adv_change_validate` (Transfer §1.3) | **Extended** — detection mechanism now surfaces conflicts HITL instead of only flagging. |
| `adv_delta_modify` / `remove` / `rename` (Transfer §1.3) | **Reused** — the evolution mechanism; already exists. Option (b) uses them directly. |
| Agreement / design gates (Transfer §1.1) | **Enhanced** — where conflicts surface; the choice (a/b/c) is recorded. |
| Conflict-surfacing flow + structural enforcement + audit | **New** — layered on the transferred foundation. |

---

## 8. Remaining implementation question

The root policy is accepted. Exact structural enforcement remains implementation
design; instruction-only is insufficient and cannot weaken the policy above.

---

## 9. Relationship to other docs

| Doc | Link |
|---|---|
| `feature-inventory.md` §2.8 | The capability entry. |
| [`specs-as-laws.md`](./specs-as-laws.md) §2 | The guiding principle: specs are laws, the user is the legislator. |
| [`decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](./decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md) | The counterpart direction. This document governs scope **contraction** under spec-law pressure; CD-0012 governs outcome **substitution and dilution**, and reuses this document's three-option flow and audit shape. |

---

*The strength of specs-as-laws is preserved; the failure of silent compliance is
removed. Laws still bind agents — but only the legislator can change them, and
only the legislator can choose to cut scope to fit them.*
