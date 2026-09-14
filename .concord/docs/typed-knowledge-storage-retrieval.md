# Typed knowledge storage and retrieval law draft

**Status:** Proposed review draft. This document is not accepted Product law.

**Basis:** Research revision 2, CD-0020, PM1, PM6, TS3, and the knowledge
formalization contract.

**Scope:** This draft defines a versioned record contract, a retrieval boundary,
and an evaluation protocol. It does not migrate `docs/`, change accepted law,
activate a runtime reader, or claim a new retrieval benchmark.

## 1. Authority and placement

Git remains the durable authority for authored Product knowledge. SQLite remains
a derived, rebuildable projection. A search provider, generated Markdown view,
or filesystem location cannot accept law or amend a record.

The default placement for a record is:

```text
.concord/knowledge/<kind>/<record-id>.json
```

The record schema binds the path, kind, and ID. One record has one canonical
source. A deterministic Markdown view may be generated from that source. A view
must carry the source record ID, schema version, source revision, and content
digest. A view cannot add a normative field or become an independent copy.

An operator may approve a scoped location override only when all of these facts
are recorded in the record provenance:

1. `location_authority` is `operator_override`.
2. `approved_by` is `operator`.
3. The override names one repository, Product, Project, or work scope.
4. The record scope is explicit and contains the ID for the override scope.
5. The override path is distinct from the default path and has no traversal.
6. The reason is bounded and remains part of the revision proof.

An override is a placement exception, not a second authority. The operator may
not use an override to hide a record from validation, create duplicate facts, or
change law status. The separate directory migration work item must prove any
future move from current roots. This draft does not perform that move.

## 2. Record envelope

Every record has one shared envelope. The executable shape is
`.concord/schemas/knowledge-record.v1.schema.json`.

| Field | Contract |
|---|---|
| `id` | Stable record identity. It does not change when a view or projection changes. |
| `kind` | One of `constitution`, `decision`, `spec`, `lesson`, `research`, `reference`, or `work_note`. |
| `scope` | Home or explicit Product, Project, domain, work, and tag scope. |
| `provenance` | Canonical location, source revision, source digest, author, time, and placement authority. |
| `status` | `current`, `historical`, or `superseded`, with effective time and a successor when superseded. |
| `version` | Record version, schema version, content digest, and migration proof when version is greater than one. |
| `body` | Exactly one closed kind body. Narrative values are Markdown strings. |
| `references` | Typed, bounded references to records and optional stable elements. |

The envelope is closed. Unknown fields fail validation. IDs are stable and are
not derived from title, rank, text similarity, or a path selected by a search
engine.

## 3. Kind bodies and addressable elements

Each kind has a closed body in the schema. Every body has a Markdown-valued
`narrative`, sections, and normative sources.

- A `constitution` body has principles and precedence.
- A `decision` body has a choice, alternatives, and consequences.
- A `spec` body has a contract and acceptance criteria.
- A `lesson` body has a lesson and evidence.
- A `research` body has findings and sources.
- A `reference` body has reference text and locators.
- A `work_note` body has an outcome, actions, and evidence.

Sections use stable IDs such as `section:authority`. Requirements use stable IDs
such as `requirement:canonical-hit`. Acceptance criteria use the same requirement
ID form. An ID remains stable across narrative edits. A removed section or
requirement remains resolvable only in a historical revision, not in the current
body.

Every requirement declares whether it is normative. A normative requirement has
one or more source IDs. Each source declares its kind, locator, revision, digest,
and the section or requirement IDs it proves. Domain validation rejects a
missing source, a dangling element, or a source that does not list the
requirement it claims to prove. This is the complete normative-source proof.

Typed references name a relation. They may target a record and an element. The
validator rejects dangling record IDs, dangling element IDs, duplicate reference
IDs, and cycles in dependency, refinement, supersession, implementation,
derivation, and conflict relations. `supports` and `cites` do not create a
decision graph.

## 4. Scope, status, and authority

`home` scope means that no explicit scope IDs are present. `explicit` scope names
the IDs that constrain the record. Scope is part of the canonical proof.

Law-bearing kinds are constitution, decision, and spec. They can be current or
superseded. Non-law kinds can be current, historical, or superseded. Status does
not describe index freshness. A current record with a stale projection remains
current law, but a retrieval result must report stale or degraded authority.

`current` is the eligible revision for current-law answers. `historical` is a
retained prior revision or non-current record that must not answer a current-law
query. `superseded` is a record replaced by the named successor. A superseded
record must have a matching typed `supersedes` reference from its successor.

The canonical source revision and digest prove the bytes that the record
describes. A projection watermark proves which canonical revision it observed.
Neither a rank nor a semantic match proves authority.

## 5. Schema evolution and migration

The schema version is explicit in the envelope and in the version object. A
record version of one has no previous digest or migration ID. A later record
version must carry both. A migration is a typed, ordered upcaster with a stable
migration ID.

Migration rules are:

1. Read the declared schema version before reading kind fields.
2. Apply only the registered ordered migration chain.
3. Verify the previous digest before writing the next revision.
4. Validate the migrated record against the target schema and domain rules.
5. Write one new canonical revision without changing the historical revision.
6. Reject a newer unsupported schema instead of guessing or dropping fields.
7. Rebuild derived projections from the canonical target revision.

Migration tests must prove byte-stable canonical encoding, preservation of IDs,
preservation of references, rejection of unknown versions, and reconstruction of
historical revisions. No migration is implied by the presence of this draft.

## 6. Retrieval boundary

Concord separates five read propositions.

### 6.1 Exact lookup

Exact lookup uses a record ID or a record ID plus element ID. It returns one
canonical hit or a typed not-found result. It does not rank body text.

### 6.2 Structured filters

Structured filters use closed kind, scope, status, tag, revision, and time fields.
They use deterministic ordering and a bounded cursor. An authoritative empty set
is different from an unknown scope or an unreachable source.

### 6.3 Body discovery

Body discovery searches Markdown narrative and element text. It is ranked
discovery only. Each candidate must resolve to a canonical record and element
before it is returned. A zero ranked hit does not prove that no law exists.

### 6.4 Current and historical resolution

Current resolution admits only a current eligible record whose canonical path,
source revision, digest, scope, and status proof agree. Historical resolution
retains the requested revision and its proof. It never silently substitutes the
current successor.

### 6.5 Stale and superseded results

Stale results carry a stale reason, observed revision, and projection watermark.
Superseded results carry the successor ID. Neither result is eligible as current
law. A stale projection is not repaired by relabeling its record status.

TS3 remains the owning agent read boundary. This draft proposes additions to the
existing `concord_knowledge` operation shape. It does not add a replacement
index, a vector store, an FTS surface, generic graph traversal, or runtime
retrieval behavior.

## 7. Canonical hits, chunks, and rebuilds

A canonical hit is the tuple `(record_id, element_id, source_revision,
source_digest, canonical_path, scope, status)`. The resolver verifies every
member before it exposes the hit as canonical.

The proposed deterministic chunk key is:

```text
record_id + element_id + source_revision
```

The `section_then_requirement` algorithm emits one bounded chunk for a section
and its requirements. It includes identity, kind, scope, status, narrative, and
source digest. It excludes private content, unverified inference, and raw
execution exhaust. A chunk is a projection, not a law record.

Invalidation is keyed by canonical path, source digest, and schema version. A
canonical revision change invalidates all chunks for the old revision. A rebuild
constructs a replacement projection from one canonical revision and swaps it
transactionally. Rebuild and invalidation tests must prove deterministic output,
no duplicate chunks, removal of old chunks, and preservation of historical reads.

## 8. Privacy and provider limits

Private Product content is forbidden from external egress by default. An
operator permission is required before any permitted egress. Redaction occurs
before egress. Provider logs and ranked results cannot become canonical proof.

lgrep remains a non-authoritative provider. This draft does not tune lgrep, alter
its API, add an embedding provider, or infer references from search results.
The evaluation contract records provider boundaries and egress bytes so a future
measurement cannot hide private-content transmission.

## 9. Evaluation contract

The executable evaluation contract is
`.concord/schemas/knowledge-retrieval-evaluation.v1.schema.json`, with the
synthetic fixture at
`.concord/scenarios/knowledge-retrieval-evaluation.v1.json`.

The evaluation corpus identifies `pm1-metadata-queries` and preserves PM1's
existing metadata-query floor: at 10 times the measured ADV dataset, the
proposed local metadata P99 target is at most 100 ms. The workload is identified
before a result exists. No result is claimed by this draft.

The protocol measures exact lookup, structured filters, body discovery, current
resolution, historical resolution, and stale or superseded handling. It records
P50, P99, recall at K, precision at K, freshness age, rebuild time, invalidation
count, rows scanned, output bytes, and egress bytes where applicable.

Proposed targets and measured results are separate arrays. A proposed target has
a target ID, metric, comparator, threshold, and population. A measured result
must name a target, run, metric, observed value, population, time, and evidence.
An empty measured-results array is valid. The validator rejects a result that
names no proposal or changes its metric. The draft claims no benchmark result.

## 10. Verification matrix

| ID | Obligation | Executable evidence |
|---|---|---|
| V1 | Every kind has a closed body. | `check-typed-knowledge.py` and seven valid fixtures. |
| V2 | Missing fields and unknown fields fail. | Four negative cases for every kind. |
| V3 | Wrong kind/status combinations fail. | Status negatives for every kind. |
| V4 | Record and element references resolve. | Dangling-reference negatives and domain checks. |
| V5 | Normative source proof is complete. | Every normative requirement names a proving source and element. |
| V6 | IDs and canonical locations are unique. | Duplicate and override domain checks. |
| V7 | Relevant relation cycles fail. | Dependency-family cycle check. |
| V8 | Retrieval targets remain proposed until measured. | Evaluation schema and empty measured-results fixture. |
| V9 | PM1's metadata floor remains visible. | Identified `pm1-metadata-queries` workload and proposed 100 ms P99. |
| V10 | Existing guards remain active. | `check-json.py`, current knowledge checks, and this checker. |

The checker reports zero unexpected outcomes only when every positive fixture
passes, every negative fixture fails, every required category is present, and the
evaluation contract passes. Schema validity alone does not prove runtime
retrieval, performance, authority, privacy, or law acceptance.

## 11. Exact proposed amendment text

The following text is the proposed amendment text. It is quoted for review and
does not amend the accepted records until separately approved and committed.

### CD-0020 amendment

> Add to D2: “The typed knowledge record contract may add exact record and
> element lookup, closed structured filters, and ranked body discovery as
> explicitly distinct propositions. Ranked discovery is non-authoritative. Every
> returned hit must resolve to a canonical record or element with scope, status,
> source revision, source digest, and canonical-location proof. This paragraph
> does not authorize a replacement index, vector store, FTS surface, arbitrary
> query language, or generic graph traversal.”

### PM6 amendment

> Add to the canonical placement rule: “The default canonical knowledge record
> location is `.concord/knowledge/<kind>/<record-id>.json`. An operator may
> approve a repository-, Product-, Project-, or work-scoped override only when
> the record carries explicit scope, the override authority is recorded as
> operator approval, the path is distinct and safe, and the reason is included
> in the revision proof. An override changes placement only. It does not create
> a second authoring authority, duplicate fact, or independent Markdown copy.”

### PM1 amendment

> Add to Q9 and Q10: “Q9 separates exact ID lookup, closed structured filtering,
> and ranked body discovery. Exact lookup and structured filtering use
> deterministic typed fields. Body discovery is ranked advisory output and
> cannot establish law, absence, current status, or canonical proof. Q10 and any
> current-resolution result must verify record or element identity, Product
> scope, law status, canonical path, source revision, source digest, and the
> projection watermark. Historical, stale, and superseded results remain
> explicitly labeled. The existing metadata-query acceptance floor remains P99
> at most 100 ms locally at 10 times the measured workload, with the workload
> named before measurement.”

### TS3 amendment

> Add to `concord_knowledge`: “The knowledge read surface may expose exact
> record/element lookup, closed structured filters, ranked body discovery, and
> current or historical canonical resolution only as distinct typed operations.
> A ranked hit must resolve through the canonical resolver before it is returned.
> The result must identify authority, scope, status, source revision, source
> digest, canonical location, freshness, and omissions. The operation is
> read-only, bounded, and fail-closed. No search rank, zero result, stale
> projection, or inferred relation can confer Product-law authority.”

### Formalization contract amendment

> Add to the formalization procedure: “A typed knowledge record is valid only
> when its closed kind body, common envelope, stable section and requirement
> IDs, typed references, canonical location authority, status semantics, and
> normative-source proof all validate. The default record location is under
> `.concord/`. A scoped operator override must be explicit in provenance and
> scope. A document that is proposed, unprocessed, out of date, or out of spec
> is not accepted law. Schema validation and retrieval evaluation do not replace
> operator acceptance or Git revision proof.”

## 12. Non-enactment and follow-up proof

This draft is not a record in the accepted knowledge manifest. It does not make
the `.concord/` tree the live Product knowledge home. It does not rewrite root
`docs/`, `instructions/`, or `scenarios/`. It does not claim that runtime
retrieval supports these operations.

Final law adoption requires separate operator approval, amendment commits for
each owning contract, manifest and source proof, migration evidence for any
directory move, and implementation tests. The separate blocked workflow remains
open until its own authority and readiness evidence is complete.
