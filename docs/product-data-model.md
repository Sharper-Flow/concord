# Concord Product Data Model

> **Status:** Aligned v4 under CD-0041. Companion to [`README.md`](./README.md),
> [`feature-inventory.md`](./feature-inventory.md),
> [`design-constraints.md`](./design-constraints.md),
> [`self-documentation.md`](./self-documentation.md),
> [`workflows.md`](./workflows.md).
> **Purpose:** The canonical model of *what a Product owns*, *what architecture
> governs it*, and *how that authority is recorded*.
> **Primary navigation:** Durable Product knowledge is navigated by
> **Product → Domain**, with current law and architecture-bound work together.
> **Origin:** User direction, 2026-07-25. Lifecycle stage (§8), shared resources
> (§9), and the replacement relation (§10) added by user direction, 2026-07-31;
> resource-first C15 shape accepted 2026-08-06; Domain and Initiative shape
> accepted by CD-0041 on 2026-08-18.

## TL;DR

A Product **declaratively owns or consumes** Projects, infrastructure, SaaS
solutions, and **Domains**. A Domain is the canonical Product-internal
architectural unit that owns behavior, invariants, and law. Concord records
canonical identity plus ownership/use links—it does **not** bridge to external
members or call their APIs. The model makes both ownership and architecture
legible to agents.

Ownership alone is not enough to act safely. Each owned thing also declares a
**lifecycle stage** (§8), may be **shared with other Products** (§9), and may
stand in a **replacement relation** to something it succeeds (§10). Those three
compose into the rules that decide how much rigor a piece of work must carry
(§11).

Product-changing work additionally binds to one home Domain, every affected
Domain, exact governing law revisions, authorized law changes, and verification
obligations. Initiative supplies business/outcome context only; it never owns
architecture or law.

The canonical Concord priorities are maintained in [`priorities.md`](./priorities.md); this document
follows them without restating the ranked list.

---

## 1. Core principle: declarative ownership, not integration

- Concord records the **existence, identity, role, and metadata** of a Product's
  members.
- It does **not** build bridges/connectors/integrations to them. No Supabase
  client, no Azure SDK calls from the ownership model.
- **Live status** (inventory §3.9) is a *separate, opt-in* signal layer — and
  even there, signals are opaque pulled-in facts, not authoritative ownership.
- **Why:** keeps the model simple, stable, and free of external-system coupling.
  A Product's ownership census is true regardless of whether any external system
  is reachable. Credentials/secrets are explicitly out of scope — they live
  elsewhere, never in the ownership census.

---

## 2. Member and architecture types

A Product owns durable members and one canonical architecture. Projects and
managed resources identify physical or external scope. Domains identify the
Product-internal architecture through which law and work are organized.

### 2.1 Repositories
- Git repos are locators on stable Project identities (e.g. `alpha-web`,
  `alpha-api`), not direct Product-membership keys.
- **Recorded:** Project ID; path/remote and predecessor project identity as typed locators;
  optional PM6 `knowledge_locator`; Product role (`primary`/`secondary`) on accepted
  PM5 `product_projects` membership.
- These are the *code* members — the units Concord orchestrates
  changes/worktrees against.

### 2.2 Infrastructure
- Operational resources the Product depends on: azure jobs, crons, deployed
  services, databases, queues, etc.
- **Recorded:** kind, identifier, role/purpose, owning Product.
- **Not** live-polled by the ownership model (that's the optional signal layer).

### 2.3 SaaS solutions
- Third-party services: Supabase, PostHog, Stripe, etc.
- **Recorded:** provider, what it's used for, environment(s), role.
- Concord knows *"Alpha uses Supabase for its DB and PostHog for analytics"*
  as a **fact** — not by querying them.

### 2.4 Domains (architecture authority)
- A **Domain** is a stable Product-internal architectural identity: a capability,
  subsystem, service, or cross-cutting concern that owns behavior, invariants,
  and architectural contracts.
- Every Domain belongs to exactly one Product. It may refer to Projects and
  managed resources without becoming either one.
- Domain identity, hierarchy, and architecture relations are declared in the
  Product's Git knowledge manifest. SQLite projects that law and owns only local
  stage/Project/resource attachments. Neither side infers identity from paths,
  tags, repository names, or Initiative membership.
- A Domain may have zero or one parent Domain. The `subdomain_of` hierarchy is
  acyclic; additional architecture relations follow CD-0041's endpoint-specific
  grammar.
- Example: "Alpha → Sync Domain → accepted sync law, active changes, cron
  resource, sync API dependency, and `alpha-sync` Project."
- Domains are where current law, active workflows, evidence, decisions,
  resources, and recent changes are surfaced together.

`component` is a retired authority term. It may appear in historical records or
ordinary UI prose during migration, but no second component identity or generic
grouping store survives the CD-0041 target state.

---

## 3. Locality of behavior (P04, reinforced for Concord)

- Everything a Product owns is **co-located and navigable together**. An agent
  working on a Product immediately sees its Domains and, through them, current
  law, Projects, managed resources, active work, evidence, initiatives, and ops.
- *"Where things belong"* must be **obvious** — no hunting across disconnected
  systems to learn what a Product comprises.
- This is a **first-class design goal**, not a nice-to-have. The data model
  exists to make ownership legible; if an agent can't trivially determine what
  a Product owns, the model has failed.

---

## 4. "What owns what" legibility

Both query directions are first-class:

- **Product → members:** *"What does Alpha own?"* → its Domains, Projects,
  resources, current law, active work, initiatives, and ops.
- **Member → Product:** *"Which Product owns this azure job / repo / SaaS?"* →
  Alpha.

The census/registry must be queryable by agents through CD-0005's governed surface;
accepted C15 defines the inventory while any new operation still follows TS8/TS9.

---

## 5. Data model sketch (illustrative)

```
Product {
  id, name,
  stage: { maturity, audience_commitment }, // §8 — default for owned members
  projects: [
    { project_id, role, knowledge_locator_id?,
      locators: [{ id, kind: repo, path?, remote?, adv_project_id? }] }
  ],                                    // accepted PM5 membership
  knowledge_home?: { project_id, locator_id }, // accepted PM6 placement
  resource_links: [
    { resource_id, role: owner|consumer, purpose, environments },
  ],                                    // accepted C15 membership
  domains: [
    { id, name, purpose, parent_domain_id?, status, registry_content_hash,
      project_links: [{ project_id, role: primary|supporting }],
      resource_links: [{ resource_id, purpose, environments }],
      current_law_refs, active_workflows, recent_changes, stage_override? }
  ],
  domain_relations,
  ...wishlist, ops, initiatives scoped to this Product
}

ManagedResource {
  id, class, kind, display_name, purpose, stage, environments,
  locators: [{ authority_kind, authority_id, namespace?, kind, value, role }],
  replacement_relations,
}
```

`stage` is decided (§8). Product↔Project membership follows accepted PM5. How
non-Project resources attach and resource replacement lives follows accepted C15;
other entity replacement homes remain separate decisions (§10/§12).

CD-0041 amends CD-0009's predecessor shape. Initiative is a derived Product view over
canonical `work_items.kind = initiative`, PM5 scope, and the bounded
Initiative-entry projection. Product does not embed mutable Initiative records,
and Initiative is not a second Product, Domain, or architecture authority.
CD-0042 makes this a direct pre-go-live replacement: #196 deletes the predecessor runtime
forms instead of preserving aliases, upcasters, or a compatibility window.

Member records carry **identity + role + metadata** — not credentials, not live
connections.

---

## 6. Primary navigation: Product → Domain

Durable Concord knowledge is navigated primarily by **Product → Domain**, not by
a flat list of changes, workflows, or initiatives.

- Open a Product and see its Domain hierarchy and typed architecture relations.
- Drill into a Domain to see its current law, dependencies, active workflows,
  evidence, decisions, resources, wishlist, recent changes, and operational
  signals.
- A change or workflow is **architecture-bound history** from the Domain; it is not the
  top-level browse path.
- An Initiative may group work across Domains for business/outcome context, but
  it never replaces Domain navigation or supplies architecture truth.
- Completed history, archived work, and passive context are available through
  explicit drill-down, not in the default view.

This applies to the terminal launcher, the optional admin panel, and agent-tool
read surfaces.

---

## 7. Active-work visibility

The default Product/Domain view is intentionally minimal:

- **Active gates** and **active problems** are shown first.
- **What blocks execution right now** is surfaced as a problem indicator.
- Stale state that exceeds a workflow's declared risk threshold is surfaced as a
  block.
- Completed history, archived changes, and historical projections are available
  through explicit drill-down, not cluttering the default view.

This is a property of the read surfaces, not an optional filter.

---

## 8. Lifecycle stage

> **Status:** Captured need, 2026-07-31. The vocabulary and attachment scope
> below are decided; the mapping from stage to concrete evidence requirements is
> not (§12).

[`priorities.md`](./priorities.md) §3 names **proportional rigor** — "the depth
of process matches the risk of the work" — as a quality attribute. Nothing in the
model declared that risk. Lifecycle stage is the declaration.

### 8.1 Two axes, deliberately separate

| Axis | Values | Answers |
|---|---|---|
| `maturity` | `prototype` \| `alpha` \| `beta` \| `production` \| `deprecated` | How stable is it? |
| `audience_commitment` | `operator_only` \| `limited` \| `public` | Who has been promised enough reliability to reasonably depend on it? |

They are separate because collapsing them produces the wrong answer in the common
case. An **operator-only production** service may be load-bearing and must earn
production rigor. Conversely a **public-commitment prototype** carries responsibility
to users that its maturity does not imply.

Audience commitment is declared by the operator. Concord never infers it from public
source code, deployment reachability, traffic, or repository visibility. A public
GitHub repository may remain `operator_only` or `limited` when no general stability
promise exists.

### 8.2 Where stage attaches

- **Product** declares the default.
- **Project or Domain** may override it. A Product can hold a production web app
  and a prototype sibling repo; one stage for the whole Product would be a lie
  about one of them.
- **Infrastructure and SaaS resources** carry their own stage. A shared
  production database does not become less production because a prototype repo
  happens to link to it.

Absent an override, a member inherits its Product's stage.

### 8.3 Stage is declarative, not observed

Stage is Concord-owned declarative state, consistent with §1. It is asserted by
the operator, not inferred from traffic, uptime, or commit velocity, and it is
not a pulled signal. A pulled signal describes what a system is doing now; stage
describes what the operator has committed to keeping true.

### 8.4 What stage governs

Stage governs the **evidence bar**: what depth of testing is expected, what proof
of testing is required before work is called done, and how strictly review and
gates apply.

Stage does **not** change the workflow shape. A prototype does not skip gates; it
satisfies them with proportionate evidence.

**No stage is an evidence exemption.** The lowest maturity still requires proof
that the work does what it claims. What changes is the weight of that proof — a
prototype is not obliged to carry a regression suite that will be invalidated by
next week's redesign, which is precisely the brittleness this model exists to
avoid.

### 8.5 Transitions are appends

Stage changes are **appended**, never edited in place, per
[`design-constraints.md`](./design-constraints.md) §4 (append-only, no history
repair).

Consequently, **evidence is judged under the stage in force when it was
produced.** Promoting a project from `prototype` to `production` does not
retroactively invalidate its prototype-era evidence, and it does not retroactively
bless it either. The stage history is what makes an old completion claim
interpretable — "this was accepted at prototype rigor in March" is a fact an agent
can act on; "this was accepted" is not.

---

## 9. Shared and linked resources

> **Status:** **Accepted by C15, 2026-08-06.** Binding detail:
> [`managed-resource-inventory.md`](./managed-resource-inventory.md).

§2.2 and §2.3 record infrastructure and SaaS solutions as members beneath one
owning Product. That is insufficient in practice: **some managed resources are
shared across Products.** A single Supabase project, a shared queue, an
observability account, or a CI runner pool may serve several Products at once.

Two candidate shapes were evaluated. C15 selects the resource-first registry.

| Shape | How it works | Cost |
|---|---|---|
| **Extend membership** | Keep Product-owns-members. Add a primary owner plus an optional `shared_with` list. | **Rejected:** copies can diverge in identity, stage, ownership, and replacement. |
| **Resource-first registry** | Resources become first-class entities with their own identity and stage. Products link as one owner plus zero-or-more consumers. | **Selected:** canonical both-direction ownership, sharing, stage, work links, and typed replacement. |

Per-resource stage and cross-Product replacement attach once to canonical resource
identity. Managed resources store explicit stage; they do not dynamically inherit
from several linked Products. Exactly one Product owns; others consume. Native
systems retain runtime authority.

What is **not** open: credentials and secrets stay out of the census (§1), and
cost or billing attribution is not part of this model.

---

## 10. Replacement relation

> **Status:** Partially accepted. C15 owns managed-resource replacement;
> CD-0041 owns Domain replacement; other endpoint homes remain open.

Building a replacement for something is a normal event, and the fact that
something *is* a replacement is load-bearing information. Domain and managed-
resource replacement now have typed owners; other endpoint families remain
prose until their owning decisions land (§10.4).

### 10.1 Shape

A replacement is a **typed, directional relation between two things of the same
kind** — product↔product, Domain↔Domain, Project↔Project, resource↔resource, or
workflow-type↔workflow-type.
It is not a boolean flag on either side, and not a sentence in a document.

Both query directions are first-class, matching the legibility principle in §4:

- *What replaces this?*
- *What does this replace?*

### 10.2 States

A replacement is not an instant. It has a state:

| State | Meaning |
|---|---|
| `declared` | A replacement is intended. Nothing is built. |
| `building` | The successor exists but does not yet cover the incumbent's scope. |
| `coexisting` | Both exist; scope/authority says which still governs each migration unit. |
| `cutover` | The successor carries the migrated scope; correction is fix-forward. |
| `retired` | The incumbent is decommissioned. |

For Concord→Advance migration, full replacement readiness is proven before any
Product moves. During migration, Advance governs unmigrated Products and Concord
governs migrated Products. One Product is never split. Cutover is one-way/fix-forward;
Advance retires after the final Product moves (CD-0006 D1–D2).

### 10.3 Two rules

1. **`maturity: deprecated` is not self-sufficient.** A deprecated thing must
   either name its successor or explicitly declare *retired, no successor*.
   Deprecation without a destination is a dead end an agent cannot act on — it
   says "stop" without saying "go where".
2. **Cutover maturity floor.** A replacement cannot be declared cutover-ready at
   lower maturity than the incumbent it replaces. See §11.2.

### 10.4 This already exists as prose

Replacement relationships are already load-bearing across this document set and
are maintained entirely by hand:

| Location | Relation |
|---|---|
| [`priorities.md`](./priorities.md) § First-usable floor | Concord becomes usable only at full replacement readiness; migration then proceeds Product by Product. |
| [`rollout-plan.md`](./rollout-plan.md) § Staged crossover | Selected active work migrates with each Product; migrated Products fix forward. |
| [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md) | Public issue-linked lessons define predecessor mechanisms Concord must replace or make unnecessary later. |
| [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md) | Public lessons inform replacement design without importing predecessor implementation identities. |
| [`workflows.md`](./workflows.md) § Workflow types | Static-analysis workflow types "replace" the ad-hoc analysis skills. |
| [`vertical-integration.md`](./vertical-integration.md) § Options | "Swallowing" — Concord absorbs and replaces the tools entirely. |
| [`clarifications.md`](./clarifications.md) C11 | Accepted: Concord is Advance's full successor under CD-0006. |

Prose supersession decays silently: the labels have to be re-verified by hand on
every refresh, and a stale one is indistinguishable from a current one.

CD-0041 assigns Domain replacement to a stateful, same-Product Domain relation.
Product, Project, and workflow-type replacement still require their own typed
homes; no polymorphic weak foreign key (FK) relation is implied.

### 10.5 Scope boundary

The relation records **that** a replacement exists and **what state it is in**.
It does not execute migrations, move data, or perform cutovers. Migration tooling
is a separate question and is not implied by this model.

The nearest existing precedent is Advance's `supersededBy` field on change close:
terminal-only, change-scoped, with no coexistence period and no applicability to
products, repos, or resources.

---

## 11. How the three compose

Stage (§8.5), sharing (§9), and replacement (§10.3) compose into two cross-field rules. First, **effective rigor is the greatest across the touch set**: no field can lower the rigor owed to anything else. Second, **a replacement cannot be declared cutover-ready at lower maturity than the incumbent**. Each rule is binding, and together they make the three fields one model.

---

## 12. Open questions

C1, C15, C16, and Domain authority are accepted by CD-0007, the managed-resource
contract, CD-0006, and CD-0041 respectively. The remaining model-internal open
questions are:

1. **Typing depth.** Are members typed further (e.g. infra sub-kinds:
   job/cron/service/db), or kept as opaque tagged records? **Lean:** light typing
   + tags — enough to be legible, not so much it ossifies.
2. **Non-Domain replacement relation home.** C15 fixes a typed resource relation
   table with real FKs, and CD-0041 fixes Domain replacement. Product, Project,
   and workflow-type replacement homes remain their owning decisions; no
   polymorphic weak-FK relation is implied.

---

## 13. Relationship to other docs

| Doc | Link |
|---|---|
| `feature-inventory.md` §3.1 | The Product entity feature — this doc details its data model. |
| `feature-inventory.md` §3.3 | Signal ingestion is the *live* layer; ownership is the *declarative* layer. Distinct. |
| `feature-inventory.md` §3.17 | Lifecycle stage + proportional-rigor governance — §8 and §11 detail its model. |
| `feature-inventory.md` §3.18 | Managed-resource inventory with cross-Product linking — §9 and accepted C15 define the resource-first shape. |
| `feature-inventory.md` §3.19 | Replacement relation — §10 details its states and rules. |
| `priorities.md` §3 | Proportional rigor is the quality attribute that §8 supplies the input for. |
| `design-constraints.md` §4 | The ownership record is Concord-owned state → lock-free, append-only, no repair. Stage transitions are appends (§8.5). |
| `self-documentation.md` §1 | The browse surface uses Product → Domain navigation. |
| `workflows.md` §2.5 | Workflows are reached from Product → Domain. |
| `vertical-integration.md` | Whether lgrep/vision/episode become Product-scoped touches this model. |

---

*The Product mindset lives or dies on whether law, architecture, and ownership
are obvious. This model makes them legible, navigated first by Product and then
by Domain.*
