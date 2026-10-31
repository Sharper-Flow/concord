# Concord Product Data Model

> **Status:** Aligned v4 under CD-0222. Companion to [`README.md`](./README.md),
> [`design-constraints.md`](./design-constraints.md),
> [`self-documentation.md`](./self-documentation.md),
> [`workflows.md`](./workflows.md).
> **Purpose:** The canonical model of *what a Product owns*, *what architecture
> governs it*, and *how that authority is recorded*.
> **Primary navigation:** Durable Product knowledge is navigated by
> **Product → Domain**, with current law and architecture-bound work together.
> CD-0213 places planning and business grouping in Linear through the Linear MCP server.

## TL;DR

A Product owns Projects and **Domains**. A Domain is the canonical
Product-internal architectural unit that owns behavior, invariants, and law.
Concord records canonical identity and membership. The model makes ownership
and architecture legible to agents.

Each Product declares a **lifecycle stage** (§8), which Projects and Domains
may override.

Product-changing work additionally binds to one home Domain, every affected
Domain, exact governing law revisions, authorized law changes, and verification
obligations. Business grouping lives in Linear through the Linear MCP server;
Concord keeps managed execution and at most one recorded issue identity per work unit.

The canonical Concord priorities are maintained in [`../law/constitution/priorities.md`](../law/constitution/priorities.md); this document
follows them without restating the ranked list.

---

## 1. Core principle: declarative ownership, not integration

- Concord records the **existence, identity, role, and metadata** of a Product's
  members.
- It does **not** build bridges/connectors/integrations to them. No Supabase
  client, no Azure SDK calls from the ownership model.
- **Live status** is a *separate, opt-in* signal layer — and
  even there, signals are opaque pulled-in facts, not authoritative ownership.
- **Why:** keeps the model simple, stable, and free of external-system coupling.
  A Product's ownership census is true regardless of whether any external system
  is reachable. Credentials/secrets are explicitly out of scope — they live
  elsewhere, never in the ownership census.

---

## 2. Member and architecture types

A Product owns durable Projects and one canonical architecture. Projects
identify repository scope. Domains identify the Product-internal architecture
through which law and work are organized.

### 2.1 Repositories
- Git repos are locators on stable Project identities (e.g. `alpha-web`,
  `alpha-api`), not direct Product-membership keys.
- **Recorded:** Project ID; path/remote and predecessor project identity as typed locators;
  optional PM6 `knowledge_locator`; Product role (`primary`/`secondary`) on accepted
  PM5 `product_projects` membership.
- These are the *code* members — the units Concord orchestrates
  changes/worktrees against.

### 2.4 Domains (architecture authority)
- A **Domain** is a stable Product-internal unit that owns behavior, invariants,
  architectural contracts, and the code that implements them. It is not a
  cross-cutting concern. A rule that binds several Domains belongs to the
  enforcing Domain
  ([CD-0222 D2](../law/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md#d2-domain-is-the-canonical-product-internal-architectural-identity)).
- Every Domain belongs to exactly one Product.
- Domain identity, hierarchy, and architecture relations are declared in the
  Product's Git knowledge manifest. SQLite projects that law. Neither side
  infers identity from paths, tags, repository names, or planning groups.
- A Domain may have zero or one parent Domain. The `subdomain_of` hierarchy is
  acyclic; additional architecture relations follow CD-0222's endpoint-specific
  grammar.
- Domains are where current law, active workflows, evidence, decisions,
  and recent changes are surfaced together.

`component` is a retired authority term. It may appear in historical records or
ordinary UI prose during migration, but no second component identity or generic
grouping store survives the CD-0222 target state.

---

## 3. Locality of behavior (P04, reinforced for Concord)

- Everything a Product owns is **co-located and navigable together**. An agent
  working on a Product immediately sees its Domains and, through them, current
  law, Projects, active work, evidence, and ops.
- *"Where things belong"* must be **obvious** — no hunting across disconnected
  systems to learn what a Product comprises.
- This is a **first-class design goal**, not a nice-to-have. The data model
  exists to make ownership legible; if an agent can't trivially determine what
  a Product owns, the model has failed.

---

## 4. "What owns what" legibility

Both query directions are first-class:

- **Product → members:** *"What does Alpha own?"* → its Domains, Projects,
  current law, active work, and ops.
- **Member → Product:** *"Which Product contains this Project?"* → Alpha.

Membership is queryable by agents through CD-0005's governed surface;
new operations follow TS8/TS9.

---

## 5. Data model sketch (illustrative)

```
Product {
  id, name,
  stage: { maturity, audience_commitment }, // §8 — default for owned members
  projects: [
    { project_id, role, knowledge_locator_id?,
      locators: [{ id, kind: repo, path?, remote? }] }
  ],                                    // accepted PM5 membership
  knowledge_home?: { project_id, locator_id }, // accepted PM6 placement
  domains: [
    { id, name, purpose, parent_domain_id?, status, registry_content_hash,
      current_law_refs, active_workflows, recent_changes, stage_override? }
  ],
  domain_relations,
  ...ops scoped to this Product
}
```

`stage` is decided (§8). Product↔Project membership follows accepted PM5.
Domain replacement and work-item supersession keep their bounded owners in
CD-0222 and PM4.

Business grouping lives in Linear under CD-0213. Product embeds no grouping record, and grouping is not a second Product,
Domain, or architecture authority.
[CD-0213 D8](../law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md#d8-retired-planning-kinds-keep-no-history)
keeps planning history in Linear; the local store keeps current state and finite
snapshots, with no event log or replay
([CD-0242 D2](../law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Member records carry **identity + role + metadata** — not credentials, not live
connections.

---

## 6. Primary navigation: Product → Domain

Durable Concord knowledge is navigated primarily by **Product → Domain**, not by
a flat list of changes or workflows.

- Open a Product and reach its Domain law and unresolved work overlap.
- Exact Domain-detail reads retain canonical relation tuples for governed work.
- Drill into a Domain to see its current law, dependencies, active workflows,
  evidence, decisions, recent changes, and operational
  signals.
- A change or workflow is **architecture-bound history** from the Domain; it is not the
  top-level browse path.
- Product-scoped Initiative planning context lives only in Linear under
  [CD-0213 D1/D4](../law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md#d1-linear-is-the-only-planning-authority).
  Concord stores no Initiative or grouping projection; planning context supplies
  no architecture authority.
- Completed history, archived work, and passive context are available through
  explicit drill-down, not in the default view.

This applies to the optional admin panel and agent-tool
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

[`priorities.md` — Quality (goal E)](../law/constitution/priorities.md#quality-goal-e) names
**proportional rigor** as a quality attribute. Declared lifecycle stage is one
input to proportional rigor; see
[`goals.md` — goal B](./goals.md#b-fast-right-sized-delivery).

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

Absent an override, a member inherits its Product's stage.

### 8.3 Stage is declarative, not observed

Stage is Concord-owned declarative state, consistent with §1. It is asserted by
the operator, not inferred from traffic, uptime, or commit velocity, and it is
not a pulled signal. A pulled signal describes what a system is doing now; stage
describes what the operator has committed to keeping true.

### 8.4 What stage governs

Stage is one input to proportional rigor (the steps, proof, and testing a
change receives), not a fixed workflow shape. See
[`goals.md` — goal B](./goals.md#b-fast-right-sized-delivery) and
[`priorities.md` — Quality (goal E)](../law/constitution/priorities.md#quality-goal-e).
Mandatory stages and operator approvals cannot be waived (CD-0242 D4).

**No stage is an evidence exemption.** The lowest maturity still requires proof
that the work does what it claims. What changes is the weight of that proof — a
prototype is not obliged to carry a regression suite that will be invalidated by
next week's redesign, which is precisely the brittleness this model exists to
avoid.

### 8.5 Transitions write current rows

Each stage change writes a current row checked by its expected version
(CD-0242 D2), per
[`design-constraints.md`](./design-constraints.md) §4.

Consequently, **evidence is judged under the stage in force when it was
produced.** Promoting a project from `prototype` to `production` does not
retroactively invalidate its prototype-era evidence, and it does not retroactively
bless it either. A record about its subject stays true of that subject
(CD-0242 D7), which is what makes an old completion claim
interpretable — "this was accepted at prototype rigor in March" is a fact an agent
can act on; "this was accepted" is not.

---

## 10. Bounded replacement relations

> **Status:** Bounded existing mechanisms. CD-0222 owns Domain replacement and
> PM4 owns work-item supersession.

CD-0222 defines the Product-internal Domain `replaces` relation and its
version-pinned overlap controls. PM4 defines work-item `supersedes`, including
the atomic terminal transition. These relations have separate owners and
semantics.

The Product model does not define a generic replacement state machine or a
Product, Project, or workflow replacement home. Migration and correction are
demand-driven operations under CD-0082.

---

## 12. Open questions

C1, C16, and Domain authority are accepted by CD-0007, CD-0006, and CD-0222
respectively. The remaining model-internal open question is:

1. **Typing depth.** Are members typed further or kept as opaque tagged records?
   **Lean:** light typing and tags, enough to be legible without ossifying.

---

## 13. Relationship to other docs

| Doc | Link |
|---|---|
| [CD-0222](../law/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md) | Domain `replaces` and PM4 work-item `supersedes` remain bounded relation mechanisms. |
| [`priorities.md` — Quality (goal E)](../law/constitution/priorities.md#quality-goal-e) | Proportional rigor is the quality attribute that §8 supplies an input for. |
| `design-constraints.md` §4 | The ownership record is Concord-owned state → lock-free, current rows with finite snapshots, no history to repair (CD-0242 D2). Stage transitions write current rows (§8.5). |
| `self-documentation.md` §1 | The browse surface uses Product → Domain navigation. |
| `workflows.md` §2.5 | Workflows are reached from Product → Domain. |
| `vertical-integration.md` | Whether lgrep/vision become Product-scoped touches this model. |

---

*The Product mindset lives or dies on whether law, architecture, and ownership
are obvious. This model makes them legible, navigated first by Product and then
by Domain.*
