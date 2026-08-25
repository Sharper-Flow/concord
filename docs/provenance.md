# Public provenance

**Status:** Constitutional bootstrap record.

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

`docs/decisions/` holds the active public decisions. A superseded public record may
be retained beside them only when its public content is safe to publish and the
active decision names the supersession. No directory in this repository is a
placeholder for imported private history.

## Authority transition

The public Concord repository is:

- Remote: [`Sharper-Flow/concord`](https://github.com/Sharper-Flow/concord)
- Go module: `github.com/sharper-flow/concord`
- Default branch: `main`
- Constitutional authority start: annotated tag `constitutional-bootstrap`

Before that tag, this candidate is a preparation artifact. At and after that tag,
the public repository is the authority for the constitutional documents. Later
changes require the accepted GitHub-native development authority described in
[`development-authority.md`](./development-authority.md).

## Date record

- Initial Concord design capture: 2026-07-25.
- Public bootstrap contract accepted: 2026-08-06, recorded by CD-0007.
- Bootstrap execution authorized: 2026-08-07, recorded by CD-0007 and CD-0010.

No literal commit SHA is recorded here. The tag name is the stable public boundary;
the repository's tag object and commit provide the verifiable implementation proof.
