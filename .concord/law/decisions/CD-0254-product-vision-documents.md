---
{
  "id": "CD-0254",
  "title": "Product vision documents",
  "authority": {"tier": "derived"},
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "product_wide_rationale": "The vision document and its change rule bind every Domain of every Concord Product, so no child Domain owns them."
}
---
# CD-0254: Product vision documents

- **Status:** Accepted
- **Date:** 2026-11-05
- **Scope:** Product vision guidance, its use in direction decisions, and its
  operator approval boundary
- **Related:** [CD-0243 D2](CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision),
  [law-folder-authority](../constitution/law-folder-authority.md)
- **Direction:** The operator directed every Concord project to have a product
  vision document on 2026-11-03 and chose guidance rather than moving law on
  2026-11-05. Both decisions are recorded on
  [CON-1676](https://linear.app/sharper-flow/issue/CON-1676/concord-products-have-no-product-vision-document-for-agents-to-check).

## Context

Agents need the operator's vision for a Product to check direction and settle
questions it answers. Concord's goals guidance and constitutional law have
separate authority; moving the law into guidance would remove that authority.

## Decision

### D1. Each Product keeps the operator's vision

Every Concord Product keeps `.concord/docs/product-vision.md` in its designated
knowledge home. It reflects the operator's own vision: purpose, goals with
measures, and non-goals. It is guidance, not Product law. Product law remains
in `.concord/law/` under law-folder authority.

Changing the vision is product direction and requires operator approval under
CD-0243 D2.

### D2. Agents use the vision for direction

Agents read the Product vision before direction calls. When an agent settles a
direction question without asking the operator, it cites the vision.

### D3. Concord retains its constitutional law

Concord's [product vision](../../docs/product-vision.md) replaces its former
goals document. [Priorities](../constitution/priorities.md) keeps the operating
envelope, constraints, standing law and Appendix A conflict rules as law.

## Verification

Repository law and documentation validators check metadata and links. Independent
review checks the vision and these obligations against the operator's direction;
passing structural checks does not prove their meaning.
