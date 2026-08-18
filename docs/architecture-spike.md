# Concord Workflow Type: Architecture Spike

> **Status:** Shaped. Companion to [`workflows.md`](./workflows.md),
> [`specs-as-laws.md`](./specs-as-laws.md),
> [`product-data-model.md`](./product-data-model.md),
> [`feature-inventory.md`](./feature-inventory.md) §3.13.
> **Purpose:** Define the Initiative entry whose deliverable is a **binding
> architectural decision** — researched, planned, optionally POC-proven —
> producing no product code and hard-blocking the implementation entries after it.
> **Origin:** User direction, 2026-08-02.

## TL;DR

Some Initiative entries exist to **decide**, not to ship. Concord registers
**Architecture Spike** as a workflow type peer to the implementation change: it
frames a question, researches it, optionally proves it with a throwaway POC, and
produces a **decision record that binds downstream work until superseded**. It
has tasks, no sub-spikes, no timebox, and its POC code never merges.

---

## 1. Why this type exists

Concord's taxonomy ([`workflows.md`](./workflows.md) §3) already covers *options
research* and *evidence research* with a lightweight, gateless
research/investigation type. That type is deliberately non-committal: research
**may resolve to "no change."**

That is exactly wrong for the case where an Initiative cannot proceed until an
architectural question is answered. Such work today has two bad homes:

| Bad home | Failure |
|---|---|
| Crammed into the design gate of the first implementation change | The change becomes a monster, and the decision's blast radius is wider than the change that owns it. |
| Drifts outside the lifecycle as ad-hoc skill work | No durable record, no reviewer, no acceptance, no supersession history. |

**Concord's own planning historically demonstrated the gap.** Two spike-shaped
questions were once named and marked TBD because the primitive to hold them did not
exist. The storage/research framing is now historical: C2 was resolved by CD-0002 and
PM1–PM5, and C7 by CD-0009:

- [`clarifications.md`](./clarifications.md) §C2 — the historical storage-authority
  question, resolved by CD-0002 and accepted PM1–PM5.
- [`clarifications.md`](./clarifications.md) §C7 — research uses ordinary work plus
  retention-bounded active packs; it does not become another trackable.

C2 was the historical archetype: a blocking architectural question that needed a
POC-capable investigation and blocked downstream capabilities until CD-0002 and
PM1–PM5 settled the storage authority. CD-0009 now resolves C7 to ordinary research
work plus retention-bounded active packs; architecture spikes remain separate because
their accepted decision output binds downstream work.

## 1.1 Why it is its own type, not a research mode

The split is **completion criteria**, which [`workflows.md`](./workflows.md) §2
names as a defining aspect of a workflow type:

| | Research / investigation | Architecture spike |
|---|---|---|
| Completion | Findings recorded; may resolve to "no change" | A decision is reached and accepted |
| Output force | Advisory | Binding until superseded |
| Downstream effect | None inherent | Hard-blocks dependent Initiative entries |
| Acceptance | Not required | Reviewer, then user acceptance |

Different completion criteria and different output force mean a different
registered type. A spike is not a research workflow with a flag.

---

## 2. Shape

| Aspect | Value |
|---|---|
| **Work kind** | Architectural decision / de-risking |
| **Steps** | frame question → research → options with evidence → optional POC → decision record → reviewer → user acceptance |
| **Artifacts** | Binding decision record; supersession links; discarded POC (not an artifact) |
| **Completion criteria** | Every framed question resolved, decision recorded, reviewer validated, user accepted |
| **Value statement** | Answers *"what risk does this retire?"* — not *"what capability ships?"* |
| **Staleness rule** | Decision inputs are tracked; drift requires re-verification before the decision authorizes downstream work |
| **Active visibility** | An unaccepted spike blocking Initiative entries is surfaced as an active blocker, not passive history |
| **Structure** | Spike → tasks. Flat. No sub-spikes. |
| **Timebox** | None |

### 2.1 Initiative role

A spike is a **first-class Initiative entry**, ordered like any other. Entries
that depend on its decision declare a **hard dependency**: they cannot enter
execution until the spike's decision is accepted.

This is the distinguishing structural claim of the type. A research workflow that
happens to be linked to an Initiative informs; a spike **gates**.

### 2.2 Flat structure

A spike has **tasks**, the same way a change has tasks. A spike never spawns a
sub-spike.

This resolves half of [`workflows.md`](./workflows.md) §7.4 (composability) for
this type: spikes compose **forward** — a spike's decision leads to implementation
changes, break-fix workflows, or a later superseding spike — never **downward**
into nested spikes. Depth stays at one, matching the sub-agent depth invariant the
operating environment already enforces elsewhere.

### 2.3 No timebox

Completion is *the correct decision*, not elapsed time. Time is not a completion
criterion and a spike does not expire into failure.

The honest exit for an undecidable question is a **decision outcome, not a
timeout**: `insufficient evidence` records the options considered, precisely what
remains unknown, and what would be required to decide. It is reviewed and accepted
like any other outcome, and it **blocks downstream work the same way** — because
"we do not yet know" is a true and binding architectural state.

This gives the type a bounded exit without introducing a clock.

---

## 3. The decision record

The artifact is a **decision record**, ADR-shaped, not a findings dump. Minimum
content:

| Field | Meaning |
|---|---|
| Question(s) framed | What this spike existed to answer |
| Options considered | With source-backed evidence per option |
| Decision | The chosen direction, or `insufficient evidence` |
| Rationale | Why this option over the others |
| Consequences | What this constrains, enables, and forecloses |
| Inputs | The upstream state the decision depends on (for staleness tracking) |
| POC findings | What the throwaway POC proved or disproved, if one was built |
| `supersedes` / `superseded_by` | Position in the decision chain |

### 3.1 Binding until superseded

A decision **binds hard**. A downstream change that contradicts an accepted
decision surfaces a **conflict requiring explicit resolution** — the same
machinery as [`specs-as-laws.md`](./specs-as-laws.md), not an advisory warning.

The escape hatch is **formal supersession, not divergence**:

- You do not argue with an accepted decision from inside an implementation change.
- You open a **new spike that supersedes** the old decision.
- This keeps the architectural argument in the venue built for it — with research,
  a reviewer, and user acceptance — instead of relitigating it inside unrelated
  implementation work.

### 3.2 The supersession chain

Each decision carries `supersedes` and `superseded_by`. The resulting chain is the
**architectural history of the Product** — queryable, and the durable answer to
*"why is it like this?"*

Supersession is **not deletion**. Superseded decisions remain readable with their
era intact. This mirrors the existing rule that evidence is judged under the stage
in force when it was produced, and that promotion neither invalidates nor blesses
prior-era evidence ([`product-data-model.md`](./product-data-model.md) §8.5):
a decision that was correct given what was known is a fact about the past, and an
agent reconstructing intent needs it.

### 3.3 Staleness is not supersession

Two distinct triggers. Do not collapse them.

| | Trigger | Meaning | Effect |
|---|---|---|---|
| **Stale** | A tracked input moved (dependency version, upstream evidence, sibling decision) | The decision may still be correct, but its basis is unverified | Re-verify before it authorizes further downstream work |
| **Superseded** | A later accepted decision replaced it | The decision is no longer in force | Downstream binds to the successor |

Staleness follows the structural block rules in [`workflows.md`](./workflows.md)
§2.3: low-risk drift warns, high-risk drift blocks, and any override is durable
audit evidence.

---

## 4. POC discipline

A spike **may** build a proof of concept. When it does:

- The POC lives in an **ad-hoc worktree**, per the environment's ad-hoc worktree
  convention.
- It exists only until it has proven or disproven what it was built to test.
- **POC code never merges to a product repo.** Always. There is no per-spike
  disposition to declare, no `seed` variant, and no exception path — the rule is
  structural, not a matter of intent.

### 4.1 How value survives the POC

Two channels, both deliberate:

| Channel | Mechanism |
|---|---|
| **Knowledge** | Captured in the decision record — what worked, what failed, what surprised. |
| **Structure** | Re-created deliberately in later changes, at full rigor, under the normal workflow. |

Nothing valuable is lost; nothing unvetted is inherited. The classic failure —
a POC becoming production by inertia — is closed by construction rather than by
review vigilance.

### 4.2 Interaction with proportional rigor

Because POC code never enters a product repo, the lifecycle-stage evidence rules
([`product-data-model.md`](./product-data-model.md) §11.1,
[`priorities.md`](./priorities.md) §3) **never have to reason about POC code at
all**. There is no "it was only a POC" discount to defend against at acceptance,
and no floor-raiser edge case where throwaway code touches a production-stage
resource.

---

## 5. Review and acceptance

Two stages, matching a change's acceptance shape:

1. **Workflow reviewer** — validates the decision record against its cited
   evidence: are the options real, is the evidence source-backed, does the
   rationale follow, are the consequences stated, is the POC finding supported by
   what was actually built?
2. **User acceptance** — the operator accepts the decision. Acceptance is what
   makes it **binding**.

An unaccepted decision does not bind and does not unblock dependent Initiative entries.
Treating the spike as a peer of a change — reviewed and accepted, not merely
filed — is what keeps it from decaying into an ignorable report.

---

## 6. Composition

| Direction | Allowed | Example |
|---|---|---|
| Spike → implementation change | Yes | Storage spike decides the engine; implementation changes build against it. |
| Spike → break-fix / ops runbook | Yes | Spike reveals a live defect or an operational precondition. |
| Spike → superseding spike | Yes | New evidence invalidates the prior decision. |
| Spike → sub-spike | **No** | Use tasks. |
| Change → spike | Yes | A change hits an architectural question larger than itself; it surfaces a conflict and a spike is opened. |

The last row matters: it is the pressure-release valve that prevents implementation
changes from silently absorbing architectural decisions again.

---

## 7. Open questions

1. **Relation to external ADR conventions** — is a Concord decision record the
   same artifact, a superset, or a separate track? **Deferred to research** — this
   is the first question the first spike frames, not a design assumption made now.
2. **Registration mechanics** — inherits [`workflows.md`](./workflows.md) §7.1;
   the spike type is defined however workflow types generally are.
3. **Staleness thresholds** — what input drift is high-risk enough to block a
   decision from authorizing downstream work? Couples to
   [`workflows.md`](./workflows.md) §2.3.
4. **Conflict surfacing mechanics** — does decision-vs-change conflict reuse the
   spec-conflict HITL flow verbatim ([`feature-inventory.md`](./feature-inventory.md)
   §2.8), or need a variant?

---

## 8. Relationship to other docs

| Doc | Relationship |
|---|---|
| [`workflows.md`](./workflows.md) §3, §4 | Registers Architecture Spike in the work-kind taxonomy and example-type table. |
| [`workflows.md`](./workflows.md) §2.3 | Staleness policy this type declares. |
| [`workflows.md`](./workflows.md) §7.4 | This type answers composability for itself: forward only, no nesting. |
| [`specs-as-laws.md`](./specs-as-laws.md) | Conflict-surfacing machinery reused for decision-vs-change contradiction. |
| [`product-data-model.md`](./product-data-model.md) §8.5 | Evidence judged under the stage in force when produced — mirrored by supersession-not-deletion. |
| [`product-data-model.md`](./product-data-model.md) §11.1 | Floor-raiser rigor rule, which POC-never-merges keeps out of scope entirely. |
| [`feature-inventory.md`](./feature-inventory.md) §3.13 | The workflow-type-system capability entry this type is registered under. |
| [`feature-inventory.md`](./feature-inventory.md) §2.5 / §3.6 | Research trackable — the *adjacent* type this one is deliberately distinct from. |
| [`clarifications.md`](./clarifications.md) §C2 | Historical storage-spike framing, resolved by CD-0002 and PM1–PM5; the type remains available for genuinely unresolved architecture questions. |
| [`clarifications.md`](./clarifications.md) §C7 | Accepted adjacent model: ordinary research work plus active packs; still distinct because spikes produce binding decisions. |

---

*Some Initiative entries ship capability. Some retire risk. Both deserve a shape.*
