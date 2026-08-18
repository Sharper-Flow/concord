# CD-0022: Active Research Findings Carry Applies-To Scope

**Status:** Accepted under operator approval.
**Approval date:** 2026-08-14.
**Approval:** Operator approved the finding-level scope direction for GitHub issue #118.
**Type:** Storage-shape amendment to CD-0009 D3/D4.
**Issue:** [#118](https://github.com/Sharper-Flow/concord/issues/118).
**Depends on:** [#117](https://github.com/Sharper-Flow/concord/issues/117).
**Preserves:** CD-0009 D7/D8, PM6, CD-0020, and C18.
**Amended by:** CD-0041 replaces `component_ids` with `domain_ids` and makes
Domain references validate against the Git-derived canonical Domain projection.

## Context

Durable knowledge distinguishes where a record is stored from what it applies to.
PM6 selects one deterministic Git home; the durable knowledge manifest separately
carries a closed scope tuple:

```text
Scopes { mode: home | explicit,
         product_ids, project_ids, domain_ids, tag_ids }
```

`home` means no explicit IDs; `explicit` means exactly the declared scope. That
shape is validated and projected into typed lookup tables.

Active research packs previously carried only `owner_work_id`. It recorded where a
pack was found, but not what an individual finding applied to. A finding discovered
while working in one Domain could not say that it applied to another Domain,
a sibling Project, or a cross-cutting tag. Archive promotion therefore depended on
memory at the exact point the pack was deleted.

Research remains intentionally writerless at this time: no agent tool, launcher
write path, or CLI command creates a pack. This decision settles storage shape ahead
of a writer. It does not make the inactive subsystem appear reachable.

## Decision

**D1. Finding scope uses the durable knowledge vocabulary verbatim.** Every active
research finding carries `mode`, `product_ids`, `project_ids`, `domain_ids`, and
`tag_ids`. `home` carries no IDs. `explicit` carries one or more IDs. The finding,
not the pack, owns applicability: one pack can yield conclusions for different
contexts while the pack's owner remains its provenance.

**D2. Findings remain normalized.** The scope relation gives findings a real query
and integrity role. They remain rows, rather than collapsing into a revision JSON
document, with a closed-kind scope relation and cascade deletion. This matches the
durable vocabulary without mechanically copying its four-table physical layout.

**D3. Validation is structural where Concord has an authority.** Product and Project
IDs are verified against their canonical projections in the mutation transaction.
Domain IDs resolve through CD-0041's Git-derived Domain projection. Tag IDs remain
bounded, clean, unique declared identifiers because Concord has no canonical tag
registry; treating tags as known by an empty lookup would be an unvalidated join.
The `home`-implies-no-IDs invariant is also enforced by a database trigger, not
just mutation code.

**D4. Scope copies with revision content.** A successor revision retains its
findings' applies-to scope under #117's copy-forward rule. Later revision changes do
not rewrite a consumer's pinned revision.

## Rejected alternatives

**Pack-level scope.** One pack often contains findings with different applicability.
Pack scope would either over-broaden every finding or force one pack per context.

**A revision JSON document.** Whole-pack reads alone argued for it, but finding-level
scope needs typed, independently queryable relations and direct cascade guarantees.
A document would turn those into write-time conventions.

**A tag lookup now.** No canonical tag registry exists. Adding one merely to
validate this relation would create another authority before there is a
demonstrated owner or writer. CD-0041 later creates the Git-authoritative Domain
registry, so Domain lookup is now required rather than rejected.

**A research tool surface.** Rejected by operator direction. The schema does not
imply a tool, writer, durable `research` knowledge kind, or retention change.

## Consequences

- Active and durable knowledge now share one applies-to vocabulary while retaining
  their distinct lifecycles: active packs delete at archive; durable knowledge stays
  Git-authoritative.
- Archive promotion has typed applicability available for selection. It remains a
  human-reviewed decision, not automatic conversion to durable knowledge.
- #119 independently repairs CD-0009 D8's wording; this decision does not change it.
- A future reader or writer must use this shape rather than invent another context
  vocabulary.

## Verification

- Explicit scope round-trips through a finding and a copied successor revision.
- `home` with IDs, empty `explicit`, unknown Product IDs, and duplicate scope IDs are
  refused before state changes.
- Updating an explicit finding to `home` removes its scope rows before the database
  guard permits the mode change.
- `bin/oc-test full` passes.
