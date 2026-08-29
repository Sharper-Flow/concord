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
> **Origin:** User direction, 2026-07-25. Lifecycle stage (§8) and shared resources
> (§9) added by user direction, 2026-07-31; resource-first C15 shape accepted
> 2026-08-06; Domain and Initiative shape
> accepted by CD-0041 on 2026-08-18.

## TL;DR

A Product **declaratively owns or consumes** Projects, infrastructure, SaaS
solutions, and **Domains**. A Domain is the canonical Product-internal
architectural unit that owns behavior, invariants, and law. Concord records
canonical identity plus ownership/use links—it does **not** bridge to external
members or call their APIs. The model makes both ownership and architecture
legible to agents.

Ownership alone is not enough to act safely. Each owned thing also declares a
**lifecycle stage** (§8) and may be **shared with other Products** (§9). Those
properties compose into the rules that decide how much rigor a piece of work must
carry (§10).

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
}
```

`stage` is decided (§8). Product↔Project membership follows accepted PM5. Non-Project
resource identity and attachments follow accepted C15. Domain replacement and
work-item supersession keep their bounded owners in CD-0041 and PM4.

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
| **Extend membership** | Keep Product-owns-members. Add a primary owner plus an optional `shared_with` list. | **Rejected:** copies can diverge in identity, stage, and ownership. |
| **Resource-first registry** | Resources become first-class entities with their own identity and stage. Products link as one owner plus zero-or-more consumers. | **Selected:** canonical both-direction ownership, sharing, stage, and work links. |

Per-resource stage attaches once to canonical resource identity. Managed resources
store explicit stage; they do not dynamically inherit
from several linked Products. Exactly one Product owns; others consume. Native
systems retain runtime authority.

What is **not** open: credentials and secrets stay out of the census (§1), and
cost or billing attribution is not part of this model.

---

## 10. Bounded replacement relations

> **Status:** Bounded existing mechanisms. CD-0041 owns Domain replacement and
> PM4 owns work-item supersession.

CD-0041 defines the Product-internal Domain `replaces` relation and its
version-pinned overlap controls. PM4 defines work-item `supersedes`, including
the atomic terminal transition. These relations have separate owners and
semantics.

The Product model does not define a generic replacement state machine or a
Product, Project, resource, or workflow replacement home. C15 still defines
canonical managed-resource identity, sharing, stage, locators, and work links.
The released managed-resource and Domain-attachment operator surfaces remain in
scope. Migration and correction are demand-driven operations under CD-0082.

---

## 11. How stage and sharing compose

Stage (§8.5) and sharing (§9) compose into one cross-field rule: **effective
rigor is the greatest across the touch set**. No field can lower the rigor owed
to another touched resource or Product.

---

## 12. Open questions

C1, C15, C16, and Domain authority are accepted by CD-0007, the managed-resource
contract, CD-0006, and CD-0041 respectively. The remaining model-internal open
question is:

1. **Typing depth.** Are members typed further (e.g. infra sub-kinds:
   job/cron/service/db), or kept as opaque tagged records? **Lean:** light typing
   + tags — enough to be legible, not so much it ossifies.

---

## 13. Relationship to other docs

| Doc | Link |
|---|---|
| `feature-inventory.md` §3.1 | The Product entity feature — this doc details its data model. |
| `feature-inventory.md` §3.3 | Signal ingestion is the *live* layer; ownership is the *declarative* layer. Distinct. |
| `feature-inventory.md` §3.17 | Lifecycle stage + proportional-rigor governance — §8 and §11 detail its model. |
| `feature-inventory.md` §3.18 | Managed-resource inventory with cross-Product linking — §9 and accepted C15 define the resource-first shape. |
| `decisions/CD-0041-architecture-bound-product-law.md` | Domain `replaces` and PM4 work-item `supersedes` remain bounded relation mechanisms. |
| `priorities.md` §3 | Proportional rigor is the quality attribute that §8 supplies the input for. |
| `design-constraints.md` §4 | The ownership record is Concord-owned state → lock-free, append-only, no repair. Stage transitions are appends (§8.5). |
| `self-documentation.md` §1 | The browse surface uses Product → Domain navigation. |
| `workflows.md` §2.5 | Workflows are reached from Product → Domain. |
| `vertical-integration.md` | Whether lgrep/vision/episode become Product-scoped touches this model. |

---

*The Product mindset lives or dies on whether law, architecture, and ownership
are obvious. This model makes them legible, navigated first by Product and then
by Domain.*
