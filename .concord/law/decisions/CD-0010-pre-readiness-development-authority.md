---
{
  "id": "CD-0010",
  "title": "Pre-readiness development authority",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "product_wide_rationale": "Defines pre-readiness development authority, amended by CD-0089 for self-hosting and CD-0213 for planning and issue linkage. It governs how the Product is built rather than one runtime Domain."
}
---
# CD-0010 — Pre-readiness development authority

**Status:** Accepted.
**Date:** 2026-08-07.
**Approval:** Operator plan approval.
**Type:** Development-authority decision.

## Decision

Concord development preserves these authority boundaries:

- Linear is the authority for planned work and defects, accessed only through
  the Linear MCP server ([CD-0213](CD-0213-linear-mcp-is-the-only-planning-authority.md) D1).
- Pull requests and required checks are the authority for review and merge
  evidence for changes entering main. CD-0241 defines eligible rapid-phase local
  integration evidence without waiving existing independent review, workflow
  acceptance, admission, or branch and worktree isolation.
- Git branches and worktrees are the authority for implementation isolation.
- Concord decision records, specifications, and constitutional docs are the
  authority for Product law.
- Advance is reference-only. Concord does not dual-write predecessor state.

[How Concord's own development runs during the rebuild](../constitution/priorities.md#how-concords-own-development-runs-during-the-rebuild)
in `priorities.md` owns Concord's development route. Replacement-readiness claims
do not determine that route.

## Rationale

This is the smallest public authority model that preserves reviewability and
isolation without importing private orchestration history or creating a second
source of truth. It records the operator's 2026-08-07 execution approval while
preserving CD-0007's earlier 2026-08-06 contract-only boundary.

## Consequence

Any change to this authority model requires a new accepted decision record, public
issue/PR evidence, and an explicit replacement-readiness rationale.
