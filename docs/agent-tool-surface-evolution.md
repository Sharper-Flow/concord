# Concord Agent Tool Discovery, Evolution, and Deprecation (TS8)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS8; binding tool-surface evolution contract and TS9 input.
> **Binding inputs:** accepted TS1–TS7 jobs, budget, tool boundaries, call context,
> adapter transport, and strict envelope schema.
> **Does not decide:** whether a measured surface change is justified (TS9), release
> automation, package repository, Product fields (C14), or resources (C15).

## 1. Decision

Concord has one language-neutral **canonical tool-surface manifest**, validated by a
normative JSON Schema IR. The IR has closed sections for `surface`, `envelope`,
`tools`, `operations`, `schemas`, `capabilities`, `consequences`, `bounds`,
`deprecations`, and `generation`. It owns:

- surface and envelope versions;
- the v2 eight-tool identities and the v3 Epic addition, with closed operations;
- strict input/result schema references;
- capability/consequence classifications;
- context, idempotency, pagination, and output-bound metadata;
- deprecation/replacement metadata; and
- a deterministic manifest digest.

Generation/checking derives the Go contract tables/validators, `concord.ts` exports,
JSON schemas, tool descriptions, conformance fixtures, and documentation fragments
(tool/operation tables, error/recovery enums, version/deprecation matrix) from that
manifest. Every generated file embeds manifest version/digest. A hand-edited
TypeScript/Go/docs copy is never another authority. CI validates the manifest against
its IR schema and fails on missing coverage or generated/documentation drift.

The manifest is an installer/build/test artifact—not an agent tool. Concord v2 and
the Epic-era v3 surface remain historical evidence; the clean pre-launch current
surface is 4.0.0 with nine static tools. There is no `catalog`, `describe`, `invoke`,
tool-search, or progressive-discovery tool.

## 2. Contract identity and negotiation

The surface uses semantic `MAJOR.MINOR.PATCH` versioning:

- **PATCH:** implementation correction with no model-visible schema, description,
  result, permission, or semantic change.
- **MINOR:** compatible refinement inside the same established tool/operation identities,
  such as an optional field or description improvement, only when negotiated old
  clients can receive a lossless old representation.
- **MAJOR:** add/remove/rename a tool or operation; change required fields, meaning,
  authority/consequence class, outcome/error/recovery discriminants, or remove an
  accepted field/variant.

Adding an optional field is not automatically compatible: strict clients reject
unknown output fields. It is MINOR only when negotiation lets core omit/down-convert
it for an older client without losing correctness. Otherwise it is MAJOR.

At TS6 grant bootstrap, the signed assertion sends:

```text
adapter identity/version
supported surface major + minor range
supported envelope versions
generated manifest digest
```

Core selects the highest exact compatible contract and binds selected surface/
envelope versions plus manifest digest to the grant; every invocation revalidates
that binding. Core-origin responses use the negotiated `contract_version`. If no
safe common version exists, grant bootstrap fails before a domain call; adapter uses
its own TS7 `adapter_contract_version` and returns `origin=adapter`,
`kind=transport_failure`, closed `adapter_reason=incompatible_contract`, and
`contact_operator`. It never claims a core version, guesses payload, or accepts
unknown discriminants.

Manifest digest mismatch within an allegedly identical version is a build/install
integrity failure, not a negotiable difference.

## 3. Compatibility rules

### Compatible MINOR/PATCH changes

- Core may accept previous-minor input and render previous-minor output while that
  version remains supported.
- Adapter sends only fields declared by its negotiated version.
- Core defaults optional inputs structurally from the manifest, never tool prose.
- Description changes rerun TS1/TS2 selection scenarios because descriptions affect
  model behavior even when wire shape is unchanged.
- Error/warning/detail additions remain compatible only when old representation is
  semantically complete and safe. Unknown error/outcome/recovery kinds are never
  folded into generic success or prose.

### MAJOR changes

A major surface change requires:

1. named failing/passing TS1 scenario or TS9 removal evidence;
2. updated canonical manifest/schema/docs in one change;
3. migration guidance and replacement mapping;
4. old/new adapter↔core compatibility tests;
5. durable-operation/in-flight-work replay; and
6. explicit operator acceptance.

The major version may change the model-visible surface only when TS9's evidence gate
passes. SemVer labels compatibility; it does not justify the change.

## 4. Deprecation and removal

Deprecation metadata names:

```text
deprecated_since
replacement_tool/operation
removal_version
removal_not_after
migration_guidance_ref
```

Rules:

- New adapters/sessions expose only current names. They never show current and legacy
  aliases together merely for convenience.
- An old negotiated client may use its old identity during the bounded compatibility
  window; core returns typed `deprecated_contract` warning with replacement/removal
  metadata.
- Compatibility lasts at least 30 days. Normal removal occurs in the first stable
  release on or after day 30; a compatibility-only release removes it no later than
  day 90. Security revocation may be faster only with explicit operator approval and
  migration/recovery guidance.
- Removal requires TS9 scenario/usage evidence, migration guidance, and proof no
  supported durable operation needs the old external representation.
- At window end, old bootstrap fails `incompatible_contract`; no fallback alias.

### Workflow engine 2.0.0 amendment

The workflow engine ships the first TS8 MAJOR amendment as surface `2.0.0`.
It adds the closed core error classification `outcome_mismatch`, whose only
recovery action is `contact_operator`; clients must not downgrade it to retry or
success. Surface `1.x` adapters are not silently dual-accepted: they fail grant
negotiation with `incompatible_contract` and must upgrade their generated
manifest, envelope validator, and signed surface range to `2.0.0`.

This amendment does not rename a tool or operation. Durable workflow operations
retain their existing operation and idempotency identities and are projected into
the `2.0.0` envelope on replay; no event history is rewritten. A client that
supports an unknown major, an unknown error classification, or a mismatched
manifest digest fails closed before any domain call.

### Epic 3.0.0 amendment

**Operator approval:** 2026-08-14, GitHub issue #128; CD-0024. **TS9 evidence:** the accepted
CD-0009 Epic model had event folds, invariant checks, and bounded reads but no
reachable agent or operator surface; `docs/predecessor-operational-coverage.md`
therefore recorded initiative framing and promotion provenance as not covered. The
new `concord_work_epic` surface is the minimal bounded route to those accepted
outcomes. A v2 adapter cannot express that ninth tool or its closed operations, so
an additive minor would be dishonest.

Surface `3.0.0` adds `concord_work_epic` with create, entry, narrative, and bounded
entries-read operations. It is a MAJOR change. The core supports only `3.0.0` for
new grants; a `2.x` adapter fails bootstrap before a domain call and must upgrade its
generated adapter, manifest digest, and signed surface range to `3.0.0-3.0.0` before
requesting a fresh grant. No alias, tool hiding, digest override, or version-dependent
meaning is offered.

Existing work/domain events and durable workflow operations accepted under `1.x` or
`2.x` remain readable and recoverable under the current envelope. The upgrade changes
only the model-visible invocation contract; it does not rewrite durable history.
- Permanent aliases, silent redirects, and one name with version-dependent meaning
  are forbidden.

### Workflow architecture-binding 4.0.0 MAJOR cutover

**Operator approval:** 2026-08-19, [GitHub issue #194 comment](https://github.com/Sharper-Flow/concord/issues/194#issuecomment-5344595697).

Surface `4.0.0` is the clean pre-launch MAJOR cutover. It adds the strict
`architecture_binding`, `spec_mandate`, and `law_modifies` fields to the workflow-action
payload so Product-changing workflow contracts can bind their current Domain registry,
Domain footprint, and law boundary. Dedicated Domain, law, and obligation ID schemas
preserve the authority boundary's 256-character limits and trimmed-value semantics.

Only the current 4.0.0 manifest digest is grantable, negotiable, session-bootable, or
invocable. V3 identities are not compatibility grants or fallback paths. Migration is
explicit: restart the session, update the adapter, regenerate its manifest/digest, and
request a new 4.0.0 grant. Concord provides no aliases, compatibility grant, or silent
fallback. Immutable event/read upcasters and historical workflow definitions remain
solely for deterministic history and rebuild; they are not agent-surface paths.

An OpenCode session loaded before an adapter update may keep its old tool definitions
only while its negotiated version remains supported. After removal, restart/update is
required. Concord does not mutate a running session's tool list behind the model.

## 5. In-flight work and durable operations

Work/domain events and TS4 durable operations record the contract version that
accepted their intent. Surface removal never deletes or rewrites that history.

- Core keeps internal readers/migrations needed to interpret authoritative history.
- A current adapter projects a durable operation into the current negotiated result
  contract without changing its authority or idempotency identity.
- A deprecated external operation cannot be removed while the only safe recovery
  path requires that old call shape.
- Workflow-definition pinning remains separate from tool-surface negotiation; neither
  silently upgrades the other.

This preserves in-flight recoverability without keeping every old tool model-visible.

## 6. Discovery

Discovery is OpenCode's static registration of the generated exports.
Agents do not search for Concord tools at runtime.

The canonical manifest may be read by installers, generators, conformance tests, and
future client adapters. It is not Product data and not an agent-facing catalog.

Progressive discovery may be proposed only when TS9 shows the static accepted surface
causes a named selection/context failure and a discovery candidate materially
improves it after including its own calls, schema tokens, latency, errors, and
compatibility cost. Any adopted discovery view is generated from the same manifest;
it cannot own hidden tools or divergent schemas.

## 7. Change workflow

Every surface change follows:

1. add/update a named TS1/TS9 scenario or bug reproduction;
2. classify PATCH/MINOR/MAJOR;
3. edit the canonical manifest and version;
4. generate Go/TypeScript/schema/docs artifacts;
5. run drift/conformance validation;
6. replay PM1 + TS1 scenarios and TS9 scorecard;
7. run old/new client-core version matrix and durable-operation replay;
8. add deprecation/migration metadata when applicable; and
9. obtain required operator acceptance for major/surface changes.

No implementation-only change can add a tool, operation, alias, error kind, or
permission class outside this workflow.

## 8. Compatibility matrix

At minimum test:

| Adapter | Core | Expected |
|---|---|---|
| current | current, matching digest | Highest compatible version selected. |
| previous supported minor | current | Old schema rendered losslessly; deprecation warning if applicable. |
| current | previous supported core | Negotiate supported version or fail before call. |
| unsupported major | current | Bootstrap fails; no tools execute. |
| same version, different digest | any | Integrity failure; no tools execute. |
| old adapter after removal window | current | Bootstrap fails with upgrade guidance; no alias. |
| current adapter, old durable operation | current | Operation remains readable/recoverable under current envelope. |
| `1.x` adapter | `2.0.0` core | Bootstrap fails `incompatible_contract`; no tool executes. |
| `2.0.0` adapter, durable workflow action accepted by `1.x` | `2.0.0` core | Replay uses the original idempotency identity and returns the typed `2.0.0` envelope. |
| `2.3.0` adapter | `3.0.0` core | Bootstrap fails before a domain call; adapter upgrades generated manifest/digest and requests a fresh `3.0.0` grant. |
| `3.0.0` adapter | `3.0.0` core | Matching digest negotiates `3.0.0`; Epic operations are available only when `work_epic` is granted. |
| `3.0.0` adapter, durable operation accepted under `2.x` | `3.0.0` core | Historic operation remains readable/recoverable with its original contract version; no event is rewritten. |
| V3 adapter | `4.0.0` core | Bootstrap fails `incompatible_contract` before a domain call; no compatibility grant or alias is issued. |
| `4.0.0` adapter | `4.0.0` core | Matching current digest negotiates `4.0.0`; workflow actions may use the strict architecture-binding fields. |

Compatibility tests validate strict schema rejection of unknown fields/variants and
ensure fail-closed responses never become `ok`.

## 9. Evidence basis

- TS2 accepted a static single-digit v1 surface and requires evidence before
  progressive discovery.
- TS6 selected one generated multi-export module and pins current OpenCode APIs.
- TS7 rejects unknown outcome/error/recovery discriminants and requires contract
  version on every result.
- Semantic Versioning defines major/minor/patch compatibility intent; Concord adds
  stricter model-visible and negotiated-output rules:
  <https://semver.org/>.
- OpenCode custom tools load named exports as static tool identities; changes require
  a new adapter load/session rather than silently mutating the active list:
  <https://opencode.ai/docs/custom-tools/>.

## 10. Rejected alternatives

- Hand-maintained Go, TypeScript, docs, and schema registries.
- Permanent aliases or old/new names exposed together.
- Same tool name with version-dependent meaning and no negotiation.
- Accepting unknown fields/errors as generic maps/prose.
- Tool discovery as a ninth always-visible v1 tool.
- Runtime-generated hidden tools outside canonical manifest.
- Calendar-only removal without scenario/usage/migration proof.
- Keeping old client support indefinitely because removal is inconvenient.
- Rewriting in-flight history to current schema.

## 11. Falsifiers

Reopen TS8 when:

- one manifest cannot generate/verify Go, TypeScript, schemas, and docs without
  lossy language-specific exceptions;
- negotiated down-conversion hides authority, recovery, or consequence semantics;
- the 90-day/one-release window repeatedly strands legitimate supported clients;
- static OpenCode sessions cannot handle required security-critical removals safely;
- durable operations cannot recover after old external shapes are removed;
- a concrete second client requires a compatible discovery/negotiation mechanism; or
- measured static-surface failures justify progressive discovery through TS9.

Any amendment preserves one canonical registry, strict negotiation, bounded
deprecation, no permanent aliases, and fail-closed unknown variants.
