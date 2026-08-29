# Concord Managed-Resource Inventory and Ownership (C15)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** C15; resource-first canonical identity with Product ownership/
> consumption links.
> **Amended by:** CD-0082 removes the unimplemented resource-replacement model.
> **Binding inputs:** PM2 global authority, PM3 typed-core/extension rule, PM5 stable
> identity/membership precedent, Product data model §§1–4, 8–9, and 11, native-authority
> placement, and accepted TS1–TS9 evolution gates.
> **Does not decide:** credentials/secrets, provider integrations/live polling,
> billing/cost allocation, resource execution tools,
> exact DDL/indexes, or C14 row fields.

## Context

Products depend on infrastructure and SaaS resources that outlive any single
Product, so shared resources need one canonical identity rather than copies.
The binding inputs are PM2 global authority, PM3's typed-core and extension
rule, PM5's stable-identity and membership precedent, the Product data model,
the native-authority placement rule, and the accepted TS1 through TS9
evolution gates. This record selects the resource-first registry: canonical
`managed_resource` entities with one owning Product, typed consumer links,
stable locators, and explicit stage.
## Contract

The binding contract is sections 1 through 5: the resource-first decision,
the canonical model covering resource fields, locators, Product links, and work
links, the stage rule, the atomic operations and
invariants, and the required bounded query directions. Sections 6 through 12
record agent-surface placement, the Product-row boundary, the candidate
comparison, evidence, implementation acceptance, rejected fields, and
falsifiers, and carry no obligation.
## 1. Decision

Managed infrastructure and SaaS resources are **first-class Concord entities** with
one canonical stable identity. Products link to them many-to-many:

- every resource has exactly one owning Product;
- zero or more other Products may consume it;
- resource stage, locators, and metadata attach once to the canonical resource—
  not copies beneath each Product; and
- work may link directly to resources it touches.

This selects the resource-first registry over Product-local `shared_with` lists.
Consistent sharing, reverse ownership, per-resource stage, and work rigor cannot be
structurally enforced when one external resource is copied into several
Product records.

Resources remain declarative inventory. Native systems own provisioning, runtime
state, health, enforcement, and deletion.

## 2. Canonical model

### 2.1 `managed_resource`

```text
resource_id                 # stable Concord ID
display_name
class                       # infrastructure|saas
kind                        # closed light-typing registry
purpose
stage { maturity, audience_commitment }
environments[]              # closed normalized values
locator_absence_reason?     # planned|not_addressable
metadata_schema_version
metadata                    # bounded per-kind validated extension
version
```

Initial kinds:

```text
service | database | queue | job | schedule | runner_pool | storage |
observability | identity | saas_account | saas_project | other
```

`other` requires a bounded `kind_detail` in the versioned extension. A repeated
filter/join kind graduates to the closed registry under PM3; arbitrary provider
objects and EAV fields do not.

Environments are normalized:

```text
development | test | preview | staging | production | other
```

`other` requires a display label but remains the normalized `other` value for
queries. Environment is declarative usage context, not live deployment state.

### 2.2 Stable locators

`resource_locators(resource_id, locator_id, authority_kind, authority_id, namespace,
kind, value, role)` records opaque non-secret native identity/address references.
`authority_kind` identifies the native authority class (provider, OS service manager,
URL authority, etc.); `authority_id` identifies the account/installation/host; and
optional `namespace` scopes non-global names. Examples: cloud resource ID, SaaS
project/account ref, service-unit name, URL, or provider slug.

- Concord ID—not locator—is the foreign key (FK) and membership key.
- Uniqueness/resolution uses `(authority_kind, authority_id, namespace, kind,
  normalized value)`. A locator may omit namespace only when that authority/kind
  guarantees uniqueness within `authority_id`; no value is assumed globally unique.
- Locator rename/move appends history without changing resource ID.
- One resource may have several locators or none only with typed
  `planned|not_addressable` reason.
- No credential, token, data source name (DSN) secret, connection string, or provider response body.

### 2.3 Product links

`resource_products(resource_id, product_id, role, purpose, environments)`:

| Field | Contract |
|---|---|
| `role` | exactly one `owner`; zero or more `consumer` links |
| `purpose` | bounded explanation of this Product's use; not global resource state |
| `environments` | subset of resource-declared environments used by this Product |

The pair is unique. One Product cannot be both owner and consumer. Ownership transfer
atomically changes the singular owner; a resource is never ownerless. If a resource
is genuinely platform-owned, the platform is represented as its Product rather than
leaving responsibility ambiguous.

Owner means declarative stewardship/contact/home in Concord. It does not grant native
provider authorization. Consumer means dependency/use, not co-ownership.

### 2.4 Work links

`work_resources(work_id, resource_id, touch_kind)` uses real FKs and a closed kind:

```text
uses | changes | migrates | retires
```

The pair/kind is unique. Links carry no lifecycle, status, priority, evidence, or
provider execution state. Every linked resource participates in the work's C16
effective-rigor calculation once that mapping is accepted. If the resource's Products
extend beyond work's derived PM5 scope, TS5 requires explicit cross-scope intent.

## 3. Stage rule

Every resource stores explicit stage. At creation, UI/core may propose the owner
Product's current stage, but acceptance materializes it on the resource and records
the event. Later Product-stage changes do not silently rewrite resource history.

This deliberately narrows the earlier generic inheritance prose for managed
resources: shared resources cannot inherit from several Products, and dynamic
inheritance would make old evidence uninterpretable. Owner transfer also does not
change stage; any stage change is a separate explicit appended action.

CD-0006 R2 owns the accepted maturity/audience obligations. C15 supplies the single
authoritative input and forbids a consumer Product from lowering it.

## 4. Atomic operations and invariants

- **Create:** resource + explicit stage + exactly one owner + environments + initial
  locators/absence reason in one SQLite transaction.
- **Share:** add one consumer after duplicate, environment-subset, and scope checks.
- **Unshare:** remove one consumer; owning link cannot use this path.
- **Transfer ownership:** replace owner atomically; prior owner may become consumer in
  the same operation or be removed explicitly.
- **Update inventory:** replace one closed metadata/stage/environment block with
  expected version; locator operations remain typed and append history.
- **Link work:** create one work-resource edge after both endpoint/version/scope
  checks.
- **Delete:** physical deletion blocked while Product/work references or retained
  authoritative history require identity.

All accepted operations append `domain_events` and update typed projections in one
transaction. No copied Product-resource rows, reconciliation jobs, or direct
projection writes.

## 5. Query contract

Required bounded directions:

1. Product → owned + consumed resources, filtered by class/kind/environment/stage;
2. Resource → owner + consumers with per-Product purpose/environment;
3. Work → linked resources and their stage/ownership;
4. Resource → linked active/terminal work;
5. locator → unique/ambiguous/unknown resource resolution.

Results return canonical resource once, stable IDs/locators, singular owner, bounded
consumers, stage/version, authority/freshness, and no live provider inference.
Owner/consumer and Product→resource/resource→Product directions must agree.

## 6. Agent-surface placement

C15 does not add a ninth tool or pre-authorize an operation. If independent TS9
unmet-intent evidence later passes its trigger, the preferred candidate to compare is
one new closed operation on existing `concord_product_view`:

```text
resources(product_id | resource_id | work_id,
          class?, kind?, environment?, detail?, cursor?, limit?)
```

It returns the bounded directions in §5. This operation fits Product ownership/
context intent and does not belong in work CRUD or knowledge search.

Database, queue, observability-account, and runner-pool examples are illustrative
requirements evidence, not independent TS9 occurrences. They do not enroll scenarios
or satisfy the expansion gate by themselves. The operation may ship only after TS9's
independent unmet-intent occurrences, failing/passing scenario, paired comparison,
TS8 major-version handling, and operator approval all pass.

Resource mutations remain operator/core CLI or future admin-detail actions in v1.
An agent mutation operation requires its own TS9 unmet-intent evidence and cannot be
smuggled into `concord_work_define`.

## 7. Product-row and live-signal boundary

C14 remains unchanged: default Product rows do not gain resource counts, health, or
provider signals merely because C15 exists. Resource inventory appears after Product
selection/drill-down.

Optional live signals are separate observations referencing `resource_id`, with
their own authority/watermark/age. They never overwrite ownership, stage, purpose,
or environment. Unreachable provider status does not erase declarative membership.

## 8. Candidate comparison

| Candidate | Decision |
|---|---|
| Product-local members + copied `shared_with` IDs | Rejected: identity/stage can diverge and reverse lookup lacks one authority. |
| Product-local member + singular owner record elsewhere | Rejected: two representations and reconciliation requirement. |
| Resource-first entity + owner/consumer links | **Selected:** one identity/stage authority and bounded both-direction queries. |
| Treat resources as Projects | Rejected: Projects are coordination/workspace identities with repository locators and PM5 semantics; SaaS/database/queue membership is different. |
| Generic external-ref blob only | Rejected: cannot enforce owner cardinality, sharing, stage, or work links. |

## 9. Evidence basis

- Concord already requires Product→member and member→Product legibility, shared
  resources, and per-resource stage (`product-data-model.md`).
- PM3 classifies managed-resource identity as first-class when sharing and stage
  attach; PM5 provides stable-ID and typed-membership precedent without
  enrolling resources as Projects.
- Backstage models infrastructure as first-class Resource entities with required
  singular ultimate owner and directional ownership relations. It is comparison
  evidence, not Concord runtime authority:
  <https://backstage.io/docs/features/software-catalog/descriptor-format> and
  <https://backstage.io/docs/features/software-catalog/well-known-relations/>.
- Kubernetes distinguishes ownership references from labels/selectors, supporting
  the separation between identity/ownership and query metadata—not Concord DDL:
  <https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/>.

## 10. Implementation acceptance

Prove:

- duplicate native locator cannot create two canonical resources;
- owner cardinality is unrepresentable as zero/two committed owners;
- shared resource stage changes once and reads identically from every Product;
- Product↔resource reverse queries agree and stay bounded;
- work-resource scope/rigor inputs are complete without copied Product state;
- owner transfer never changes stage or drops consumer purpose silently;
- provider outage leaves declarative inventory authoritative;
- 10× measured inventory queries meet PM1 P99 ≤100 ms target; and
- TS8/TS9 operation evolution passes without adding a tool or weakening current
  scenarios.

## 11. Rejected fields and behavior

- Credentials, secrets, private connection strings, tokens, provider payloads.
- Live health/status mixed into inventory authority.
- Cost/billing attribution, team assignments, on-call, service level agreement (SLA), or incident ownership.
- Product-specific copies of stage or lifecycle.
- Multiple owners, ownerless resources, inferred owner from consumer order.
- Generic tags as identity or authorization.
- Cascading Product/work deletion of resources.
- Connector/API execution implied by membership.
- Resource fields added to C14 default rows without separate glance evidence.

## 12. Falsifiers

Reopen C15 when:

- real resources repeatedly require legitimate joint/no-Product ownership;
- one resource identity cannot survive provider moves/splits/merges;
- owner/consumer purpose/environment fields prove insufficient or become edge-state
  dumping grounds;
- coarse kind/environment registries churn on most additions;
- explicit resource stage causes harmful duplication without preserving history;
- work-resource links cannot support C16 rigor jobs without richer typed semantics;
- the `concord_product_view.resources` candidate fails TS9 selection/context evidence.

Any amendment preserves canonical resource identity, one ownership authority, typed
sharing, explicit stage, real FK relations, native execution ownership, and no
secret storage.

## Acceptance criteria

- Given a resource creation
  When it commits
  Then resource, explicit stage, exactly one owner Product, environments,
  and initial locators or typed absence reason land in one event-backed
  SQLite transaction.

- Given a duplicate consumer link or an environment outside the resource's
  declared set
  When the share is attempted
  Then the core refuses it with a typed error and no event.

- Given locator authority
  When a locator is stored
  Then the Concord ID remains the membership key and no credential, token,
  DSN secret, or provider response body is stored.

- Given bounded metadata on an `other`-kind resource
  When it validates
  Then the per-kind extension enforces its bounds and requires the bounded
  `kind_detail`.

## Verification

No corpus scenario exercises the managed-resource model, so every criterion
carries a typed exemption in the record naming the store test that proves
the guarantee.

- Criterion 1 is proved by
  `TestCreateManagedResourceAndAddConsumerAreEventBacked`
  (`internal/store/managed_resources_test.go`).
- Criterion 2 is proved by
  `TestManagedResourceRejectsDuplicateConsumerAndEnvironmentOutsideResource`
  (`internal/store/managed_resources_test.go`).
- Criterion 3 is a schema-boundary law: the locator table carries only the
  typed columns of section 2.2, enforced by the schema manifest applied by
  `TestOpenAppliesSchemaManifest` (`internal/store/schema_test.go`) and the
  exhaustive binary large object (BLOB) column allow-list of
  `TestPM8AndPM9DeclareNoEvidenceOrReceiptStore`
  (`internal/store/pm8_pm9_absence_test.go`), which admits only fixed-size
  authority material.
- Criterion 4 is proved by `TestManagedResourceMetadataBoundsAndOtherKindDetail`
  (`internal/store/managed_resources_test.go`). Section 12 records the
  falsifiers for each guarantee.
