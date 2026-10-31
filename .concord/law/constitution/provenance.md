---
{
  "id": "provenance",
  "title": "provenance",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "product_wide_rationale": "Records the origin and authority boundary of the whole public repository, including what the bootstrap excluded and where superseded records may be retained. No Domain inside the Product can own the record that establishes the Product's own provenance."
}
---
# Public provenance

**Status:** Constitutional bootstrap record.

## Purpose

This record fixes the provenance boundary of the public Concord snapshot:
what the public repository contains, what private predecessor material it
deliberately excludes, and how public identifiers relate to the private
design history they came from. The sections below carry that law — the
origin and boundary itself, the authority transition, and the date record.

## Origin and boundary

Concord's design originated in private product-planning work. This public snapshot
does not import private Git history, private issue history, private dependency
inventories, archive bundles, operator logs, credentials, or private product data.
It contains only the current constitutional documents, public-source research, and
synthetic scenarios required to explain the accepted design.

The snapshot preserves the public-facing CD, R, PM, and TS identifiers and their
decision dates. Those identifiers are document lineage, not a claim that excluded
private predecessor artifacts are present here. Superseded private pre-public
decisions remain private; CD-0002 is the first public retained authority decision.

`.concord/law/decisions/` holds the active public decisions. A superseded public record may
be retained beside them only when its public content is safe to publish and the
active decision names the supersession. No directory in this repository is a
placeholder for imported private history.

## Authority transition

The public Concord repository is:

- Remote: [`Sharper-Flow/concord`](https://github.com/Sharper-Flow/concord)
- Go module: [`github.com/sharper-flow/concord`](https://github.com/Sharper-Flow/concord)
- Default branch: `main`
- Constitutional authority start: annotated tag `constitutional-bootstrap`

The annotated `constitutional-bootstrap` tag marks public constitutional authority.
Planning belongs to Linear under CD-0213. Concord coordinates explicitly managed
development under [../specs/managed-development-authority.md](../specs/managed-development-authority.md);
outside-workflow development follows host permissions and repository rules.
Pull requests and required checks own review and merge evidence.

## Date record

- Initial Concord design capture: 2026-07-25.
- Public bootstrap contract accepted: 2026-08-06, recorded by CD-0007.
- Bootstrap execution authorized: 2026-08-07, recorded by CD-0007 and CD-0010.

No literal commit SHA is recorded here. The tag name is the stable public boundary;
the repository's tag object and commit provide the verifiable implementation proof.
