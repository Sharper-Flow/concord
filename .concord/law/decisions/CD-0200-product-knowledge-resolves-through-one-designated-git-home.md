# CD-0200: Product knowledge resolves through one designated Git home

- **Status:** Accepted
- **Date:** 2026-10-01
- **Scope:** Product knowledge placement, discovery, law identity, and workflow law resolution
- **Preserves:** [CD-0194](CD-0194-product-knowledge-lives-under-concord.md)
  and PM6's one-note-per-work-item rule

## Context

A Product needs one authoritative place to publish its law. A Project's
membership does not make its repository a knowledge authority.

## Decision

### D1. A Product has one designated knowledge home

Product-wide knowledge reads resolve through the Product's designated
member Project and canonical-path locator. The designation is explicit
operator configuration. The store keeps current rows in one transaction
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
Discovery never makes a repository law.

### D2. The home owns the Product registry

The home's repository carries the Product's Domain registry at its existing
path. Law Domain homes and applicability must resolve against that registry.

### D4. Records carry revision proof

Every returned record carries its home Project and locator, its
repository-relative path, its commit OID, and its content hash. A
`<project_id>/<law_id>` reference resolves within that Project's canonical
knowledge home. A Product-scoped law lookup resolves within its designated
home. Historical work notes retain their recorded home and revision.

### D5. Law relations stay within the law folder

Typed law relations name law files in the same law folder and obey
[CD-0015](CD-0015-typed-law-relations.md). Repository discovery grants no
authority; rank and precedence follow
[law-folder-authority](../constitution/law-folder-authority.md).

### D6. Workflow law resolves through the home

Workflow law pins, law context, and mandated-law checks resolve through the
Product's designated home. File locators name that repository. Consequential
transactions consume freshness proof established before they open; they
never probe Git while holding the store connection.

## Alternatives considered

- Resolve historical work notes through the current Product home. Rejected:
  a later designation cannot change a note's recorded identity and revision.
- Use search rank to select workflow law. Rejected: ranking establishes
  discovery, not law authority or an exact revision.

## Consequences

Product knowledge has one explicit authority. Home changes preserve
historical work-note locators. Consequential law boundaries require
verified home proof.

## Verification

- `TestSingleSourceProductQ9OutputUnchanged` covers Product search,
  continuation, Project search, and authority.
- `TestHomeProofSeparatesVerifiedHeadFromScannedRevision` distinguishes a
  verified Git head from the projection's scanned revision.
- `TestKnowledgeHomeReplacementDuringRebuildRefusesStaleContext`,
  `TestKnowledgeHomeReplacementMissingManifestKeepsPriorRegistry`, and
  `TestKnowledgeHomeReplacementKeepsOtherProductRows` cover designation
  races, refusal atomicity, and isolation between Products.
