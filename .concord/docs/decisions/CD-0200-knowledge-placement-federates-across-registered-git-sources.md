# CD-0200: Knowledge placement federates across registered Git sources

- **Status:** Accepted
- **Date:** 2026-10-01
- **Scope:** Product knowledge placement and discovery; the
  `product_knowledge_sources` registration surface; Product-wide PM1.Q9
  iteration over the registered source set; source-qualified law identity;
  cross-source law relations; workflow law-pin and law-context resolution;
  the Domain registry's single-owner rule
- **Amends:** [CD-0020](CD-0020-retain-knowledge-index-shape.md) D1, D3, and
  Invariant 2, and records that its reopen condition 1 is met;
  [CD-0015](CD-0015-typed-law-relations.md) relation endpoints;
  [PM1](../product-memory-query-contract.md) Q9/Q10 scope and identity text;
  [PM6](../canonical-git-note-placement.md) home wording;
  [the knowledge-index reference](../concord-knowledge-index.md)
- **Preserves:** [CD-0194](CD-0194-product-knowledge-lives-under-concord.md)
  in full (the `.concord/` default stays); PM6's one-note-per-work-item rule
  stays; CD-0020 D2, D4, D5, D6, and Invariants 1, 3, 4, 5, 6 stay
- **Approval:** The operator approved this work item contract in chat on
  2026-10-01, including the finding that CD-0020's reopen condition 1 is met.

## Context

Concord resolves exactly one knowledge home per Product. The projection
partitions every derived table by home pair, and Product-wide Q9 serves that
one home. A Project whose code lives in a second repository cannot hold
canonical knowledge beside its code and stay visible to Product-wide queries.

`CON-789` (work-00e785f210cdc3c8fc0aa30d) recommends Git-backed placement by
ownership with Product-wide discovery. CD-0020's reopen condition 1 is met by
recorded evidence: `POKE-711` subcheck C needs native search and resolution of
records in a second repository, and scoped search resolves only the backend
home (observations `obs:04017bbc5627c604` and `obs:3ae8fa4b2c826b04`). The
job is inexpressible in bounded Q9/Q10 without custom fan-out or a forbidden
copy.

## Decision

### D1. A Product's source set is one shared home plus registered sources

A Product resolves Product-wide knowledge over one designated shared-law
home plus the member Project canonical-path locators the operator registers
as sources. Registration is explicit operator configuration through the
`product.knowledge_source_registered` and `product.knowledge_source_removed`
events; discovery never makes a repository law. The designated home stays
the single shared-law home and is always a source. It never appears in the
registration table, so its removal is a typed not-found refusal.

### D2. Projection stays per source; the registry has one owner

Each source rebuilds its own projection from its own Git head, exactly as
the single home did. Only the shared-law home carries the Domain registry. A
registered source that ships one refuses its rebuild, and its Domain
references validate against the registry the shared home projected.

### D3. Product-wide Q9 iterates the verified source set

Q9 over a Product with more than one source verifies each source's
watermark, merges items under the accepted ordering, returns per-source
watermarks, and binds its cursor to a digest of the source set. A source
that is unreachable or stale refuses the answer unless the caller allows
degradation; a degraded answer carries one omission per missing source and
never reads as an authoritative negative. A one-element source set takes
the identical single-home code path with unchanged output.

### D4. Law identity is source-qualified

Every returned record carries its source Project and locator, its
repository-relative path, its commit OID, and its content hash. The
qualified reference form is `project_id/law_id` in string fields and a
structured source field in manifests. A bare ID resolves Product-wide and
refuses as ambiguous when two sources hold it. A rebuild refuses a law ID
that contains `/`, which the qualified form reserves.

### D5. Cross-source relations are explicit and never infer precedence

A relation whose target lives outside the declaring manifest names the
target's source Project. The relation validates over the verified source
set at the declaring source's rebuild and again at every consequential
boundary: the target Project must be registered, the target law must be
projected, and a `conflicts_with` pair across sources blocks the rebuild
and every consequential check until an accepted relation or amendment
resolves it. The declaring source's rebuild projects the edge into
`law_cross_source_relations` with its target source, and a consequential
boundary that finds the target unregistered or unprojected refuses. A
non-home source may not declare `supersedes`, `refines`, or
`subordinate_to` toward shared-home law. No precedence is inferred.

### D6. Workflow law resolves across the source set

Workflow law pins, law context, and the mandated-law boundary check resolve
each bare law ID across the registered source set and refuse an ID that two
sources hold. Each resolved law's file locator names the repository that
holds it. The Domain registry resolves from its single owner.

## Alternatives considered

- **Copy the other Project's records into the shared home:** a forbidden
  copy. It forks authority, breaks PM6 placement-by-ownership, and leaves
  the source repository a stale shadow.
- **Auto-discover every member Project with a manifest:** discovery makes
  law of unreviewed repositories. `CON-789` requires explicit registration.
- **One merged Product projection rebuilt across all sources:** it discards
  the per-home partition every table and rebuild path already owns, and it
  couples one source's rebuild failure to every source's freshness.
- **Per-source registries merged at read time:** it federates Domain
  identity and invents merge rules no decision authorizes.
- **Serve last-known rows or skip missing sources silently:** both blur the
  authority, degradation, and reachability states CD-0020 D3 keeps distinct.

## Consequences

### Positive

- A Project keeps canonical knowledge beside its code and stays visible to
  Product-wide queries with revision proof.
- Degradation stays explicit: a missing source names its omission and never
  yields an authoritative empty answer.
- Ambiguous bare IDs refuse instead of resolving by accident.
- Local knowledge cannot override shared Product law.

### Cost

- Registration, watermarks, and ambiguity refusals add operator steps.
- Every federated read pays one watermark verification per source.
- Cross-source conflicts block until resolved; the projection carries no
  cross-source relation rows.

## Verification

- The migration adds `product_knowledge_sources` with its checksum pin
  (`TestMigrationChecksumPinsMatchDefinitions`).
- `TestProductKnowledgeSourceRegistrationRefusals` covers the registration
  and removal refusals, including the designated home's permanent source
  role (`TestSingleSourceProductQ9OutputUnchanged` asserts the same
  refusal).
- `TestFederatedQ9IteratesRegisteredSourceSet`,
  `TestFederatedQ9CursorBindsToSourceSetDigest`, and
  `TestFederatedQ9DegradedSourceRefusesAndOmits` cover the federated Q9
  iteration, the source-set cursor digest, and the degradation omissions.
- `TestSourceRebuildRefusesRegistryPrecedenceAndConflict`,
  `TestSourceRebuildAcceptsCrossSourceReferenceBetweenSources`, and
  `TestQualifiedAndAmbiguousLawIdentity` cover the rebuild refusals, the
  cross-source relation rules, and qualified versus ambiguous identity.
- `TestMandatedLawsResolveAcrossSources` covers the workflow law boundary
  across the source set.
- The CLI exposes `product-knowledge-source-register` and
  `product-knowledge-source-remove`; `TestRunHelpListsExactCommandFormsAndStdinShapes`
  pins the documented surface.
