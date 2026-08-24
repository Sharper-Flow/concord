# Concord Product-Memory Membership (PM5)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-05.
> **Decision:** PM5; direct accepted authority for Concord's Product-memory membership and canonical work identity.
> **Binding inputs:** accepted PM1 query contract, PM2 global authority, PM3 typed
> projection model, and PM4 lifecycle/relation semantics.
> **Operator choices:** cross-Product work is allowed when explicit; primary Project
> is optional for both Products and work items.
> **Research basis:** public predecessor lessons plus official/public models from
> Linear, GitLab, GitHub Projects, Changesets, Jira, and Bugzilla. No benchmark or
> proof of concept (PoC) is part of this decision.
> **Does not decide:** PM6 canonical note placement, C15 managed-resource identity,
> TS5 authorization/context mechanics, exact DDL/indexes, or agent tools.

## Context

Concord coordinates work for one operator across Products and Projects. The
binding inputs are the accepted PM1 query contract, PM2 global authority, PM3
typed projection model, and PM4 lifecycle and relation semantics. This record
decides how membership itself is stored and read: which Projects make up each
Product, which Projects one work item touches, and how Product scope derives
without ever being copied onto the work item.

## Contract

The binding contract is sections 1 through 7: the membership model, the
Product-scope and cross-Product rules, stable identity and locators, atomic
membership operations, the query contract, and the structural invariants.
Section 8 records the alternatives the operator rejected; sections 9 through
11 record deferred scope, falsifiers, and sources, and carry no obligation.

## 1. Decision

Concord stores one canonical work item and typed membership edges:

- `product_projects(product_id, project_id, role)` defines which Projects make up
  each Product;
- `work_projects(work_id, project_id, role)` defines which Projects one work item
  touches;
- work lifecycle, priority, value, and terminal state live only on `work_items`;
- Product scope is a derived set reached through those joins, never copied onto the
  work item or membership edge.

A Project may belong to multiple Products. One work item may touch Projects across
multiple Products. Product-scoped queries return that canonical work item once per
applicable Product—never once per Project and never with per-Project status copies.

## 2. Membership model

### 2.1 Product↔Project membership

Each Product has one or more Project memberships. Each Project belongs to one or
more Products.

| Field | Contract |
|---|---|
| `product_id` | stable Product identity; real FK endpoint |
| `project_id` | stable Project identity; real FK endpoint |
| `role` | closed enum: `primary` or `secondary` |

Within one Product:

- zero or one Project may be `primary`;
- all remaining Projects are `secondary`;
- the pair `(product_id, project_id)` is unique;
- absence of a primary is valid and must not trigger an inferred choice.

The role is a home/navigation hint, not lifecycle, authority, completion, or release
sequencing. PM6 may use it in canonical-note placement but owns the fallback rule.

### 2.2 Work↔Project membership

Every work item has one or more Project memberships.

| Field | Contract |
|---|---|
| `work_id` | canonical work identity; real FK endpoint |
| `project_id` | stable Project identity; real FK endpoint |
| `role` | closed enum: `primary` or `secondary` |

Within one work item:

- zero or one Project may be `primary`;
- all remaining Projects are `secondary`;
- the pair `(work_id, project_id)` is unique;
- absence of a primary is valid and must remain explicit.

Membership carries no lifecycle, status, completion, blocker, required, merge-order,
priority, or evidence fields. If a Project needs independently trackable completion,
create a PM4 child work item with its own lifecycle. If sequencing is required, use
PM4 `blocks` relations or a purpose-built workflow—not membership metadata.

PM4 `external_blocker` work items follow the same at-least-one membership rule and
attach to the affected Project(s). Their opaque external reference is not a Project.

## 3. Product scope and cross-Product work

For work item `W`:

```text
Projects(W) = work_projects where work_id = W
Products(W) = distinct Products joined through product_projects for Projects(W)
cross_product(W) = count(Products(W)) > 1
```

`Products(W)` is a derived visibility/scope set, not a second authority. Concord
stores no `work_items.product_id` and no `work_products` mirror table. This is safe
because cross-Product work is allowed: changing Product↔Project membership is an
authoritative scope change, not drift from a separately stored Product owner.

- Product-scoped Q2/Q3 queries join memberships and return `DISTINCT work_item`.
- Q6 returns one canonical work record with all Project memberships.
- A work item spanning multiple Products appears once in each applicable Product's
  view, with `cross_product=true` and the complete bounded Product/Project scope.
- Ambient context never guesses a unique Product for cross-Product mutation. TS5
  must require explicit cross-Product intent/scope; PM5 defines the fact, not the
  authorization envelope.
- Cross-Product PM4 relations remain valid; relation reads expose each endpoint's
  derived Product scope.

### Shared-Project consequence

If one Project belongs to Products A and B, work attached only to that Project is
visible in both Products. That is the intended consequence of shared membership,
not ambiguity inside storage. Q1 may still return `ambiguous_scope` when a caller
asks a shared Project to choose one ambient Product without an explicit override.

## 4. Stable identity and locators

Product and Project identity use stable Concord-assigned IDs. Repository path,
remote URL, ADV project ID, deployment target, and similar values are typed locator
attributes of a Project; they are not membership keys.

- Moving or renaming a repository updates locators without changing Project ID.
- One Project may have multiple locators or no repository locator.
- Membership never keys on a filesystem path or remote URL.
- Accepted C15 uses first-class non-Project resource identity with owner/consumer
  links; PM5 does not silently enroll resources as Projects.

## 5. Atomic membership operations

Every operation appends accepted `domain_events` and updates all affected typed
projections in one SQLite transaction.

### Create Product

Create the Product and at least one Project membership atomically. Validate unique
pairs and at-most-one primary. A Product with no Projects is invalid under PM5.

### Create work

Create one `work_item` and all initial `work_projects` rows atomically. Validate
at-least-one membership, unique pairs, existing Project IDs, and at-most-one primary.
The operation may produce a cross-Product scope set; its result must surface that set.

### Add/remove/change role

- Adding a membership validates the Project and duplicate constraint, then returns
  the resulting derived Product scope.
- Removing a work item's last Project membership is rejected.
- Removing a Product's last Project membership is rejected.
- Removing a Project's last Product membership is rejected.
- Removing a primary is allowed and leaves zero primary unless another membership
  is explicitly promoted in the same operation.
- Setting a new primary demotes the prior primary in the same transaction or fails;
  two committed primaries are unrepresentable.
- Role change never changes work lifecycle.

### Move a Project between Products

A Product-membership edit is one atomic operation over the complete resulting set.
It may change `Products(W)` for every work item attached to that Project; the result
must return a bounded impact summary/count and a durable event reference. No copied
work rows or reconciliation job is created.

Removing the Project from its last Product is rejected. Removing it from one Product
is allowed when at least one Product membership remains; affected work immediately
leaves that Product's derived view unless another member Project still connects it.

### Remove a Project from Concord

Physical Project deletion is blocked while any `work_projects` or
`product_projects` reference remains. An explicit migration/removal operation must
reassign memberships first; no cascade silently removes work scope.

## 6. Query contract

### Q1 — Product context

- Product Projects order by `primary` before `secondary`, then display name, then
  stable Project ID.
- A shared Project queried without explicit Product context returns bounded
  `ambiguous_scope` candidates; no primary role resolves cross-Product ambiguity.

### Q2/Q3 — Product work

- Join Product→Project→work memberships and deduplicate by `work_id` before counts,
  pagination, or ordering.
- `COUNT(DISTINCT work_id)` is mandatory for Product-level counts.
- Lifecycle/filter/order values come from the one canonical work row.

### Q6 — Cross-Project work

- Return one canonical work record and its ordered memberships: primary first if
  present, then secondary, then stable Project ID.
- Both Project→work and work→Project reads return the same edge set.
- Cross-Product membership is represented by the same record, not sibling copies.
- The provisional PM1 fixture roles already match PM5 and need no normalization.

### PM1 corpus compatibility

PM1's fixture includes a provisional `product` field on each work item. PM5 does not
persist that field. The scenario adapter treats it as an expected-scope assertion:

1. derive `Products(W)` through Product→Project→work memberships;
2. require the fixture's `product` value to be a member of that set;
3. fail with `invariant_violation` if it is absent;
4. never write or mirror the fixture field onto `work_items`.

Fixture membership roles already match PM5 and require no role normalization. The
implementation-acceptance suite must add one true cross-Product case because the v1
fixture has no work item attached to a Project shared by multiple Products.

## 7. Structural invariants

1. **One canonical work identity:** work state exists only on `work_items`.
2. **No membership state copies:** membership edges contain role only; they never
   carry lifecycle, completion, blocker, priority, or evidence state.
3. **At-least-one membership:** every Product and work item has at least one Project,
   and every Project belongs to at least one Product.
4. **Optional singular primary:** each Product/work has zero or one primary Project.
5. **Stable endpoints:** memberships use Product/Project/work IDs, never locators.
6. **Derived Product scope:** `Products(W)` is the distinct join result and has no
   independently mutable mirror.
7. **Atomic scope change:** event append, edge changes, invariant checks, and impact
   result commit together.
8. **No silent cascade:** referenced Projects cannot disappear from membership.
9. **Duplicate-free reads:** Product query populations deduplicate by canonical
   `work_id` before counts, pagination, and ordering.

## 8. Alternatives rejected

### One copied work item per Project

Rejected. It duplicates identity/status, needs reconciliation, and violates PM1 Q6.

### Stored per-Project status or `required`

Rejected. It creates independent completion state on a membership edge. Per-Project
delivery uses child work items; readiness/sequencing uses PM4 relations.

### Stored `merge_order`

Rejected. Q6 display order is role then Project ID; delivery sequencing belongs to
workflow execution or PM4 `blocks` edges.

### Stored `work_items.product_id`

Rejected for the accepted cross-Product model. It would either forbid legitimate
multi-Product scope or require another work↔Product structure. Product scope is the
membership join by design.

### GitHub-Projects-style per-view fields

Rejected. Per-Product/view status copies are exactly the divergence PM1 forbids.

## 9. Scope deliberately deferred

PM5 does not decide:

- canonical note resolution when no primary Project exists—now settled by accepted
  PM6's Product-home→primary→typed-`ambiguous` rule;
- managed-resource ownership/sharing shape—now settled separately by accepted C15;
- TS5's ambient scope, explicit cross-Product authorization, idempotency, or
  mutation envelope;
- exact table/index/trigger syntax or impact-result pagination;
- release merge sequencing or per-Project child-work generation;
- agent tool names, schemas, or transport.

## 10. Falsifiers

Reopen PM5 if:

1. Product-scoped membership joins cannot meet PM1's bounded P99 target at 10× scale;
2. legitimate work needs per-Project lifecycle that PM4 child work cannot express;
3. most work requires stored Product ownership despite allowed cross-Product scope;
4. optional primary repeatedly prevents canonical note resolution after PM6;
5. Project membership moves make derived visibility operationally unsafe despite
   atomic events and impact reporting;
6. a repeated accepted PM1 job needs `required`, merge order, or another membership
   attribute that cannot be represented by work/relations/workflows;
7. cross-Product work requires independent per-Product status, which would reopen
   PM2/PM5 rather than add copied state.

## 11. Primary sources

- Linear conceptual model/projects: https://linear.app/docs/conceptual-model and
  https://linear.app/docs/projects
- GitLab cross-project linked issues:
  https://docs.gitlab.com/ee/user/project/issues/related_issues.html
- GitHub Projects: https://docs.github.com/en/issues/planning-and-tracking-with-projects/about-projects
- Changesets: https://github.com/changesets/changesets
- Jira issue linking: `https://confluence.atlassian.com/spaces/ADMINJIRASERVER/pages/938847862/Configuring+issue+linking`
- Bugzilla dependencies: https://bz.apache.org/bugzilla/docs/en/html/using/understanding.html

External models are comparison evidence. PM1's accepted jobs/oracles, PM2–PM4,
operator choices, and the falsifiers above remain controlling.

## Acceptance criteria

- Given a work item with memberships in two Projects
  When a caller reads that work item
  Then one canonical record carries both memberships.

- Given a Project that belongs to one Product
  When a caller resolves the Project to its Product
  Then the resolution names exactly that Product.

- Given a Project with member work items
  When a caller lists work through the Project
  Then the listing returns those work items.

- Given a work item whose memberships change in one operation
  When the operation commits
  Then the stored membership set equals the requested set completely.

## Verification

- Criterion 1 is proved by the bound `Q6-cross-project` scenario, whose
  fixture carries two Project memberships and asserts one canonical record.
- Criterion 2 is proved by the bound `Q1-project-to-product` scenario, which
  resolves a Project to its Product through the authoritative join.
- Criterion 3 is proved by the bound `Q6-project-to-work` scenario, which
  lists work through Project membership.
- Criterion 4 has no corpus scenario; it is proved by the store test
  `TestMembershipReplacementReplacesTheWholeSet`
  (internal/store/lifecycle_relations_test.go), which replaces a two-member
  set with a different two-member set and asserts the committed projection
  equals the requested set with the removed edge gone. Section 10 records
  the falsifiers for each guarantee.
