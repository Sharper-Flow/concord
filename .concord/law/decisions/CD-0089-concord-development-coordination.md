---
{
  "id": "CD-0089",
  "title": "Concord development coordination",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0010"
    },
    {
      "kind": "refines",
      "target_id": "CD-0007"
    },
    {
      "kind": "refines",
      "target_id": "CD-0082"
    },
    {
      "kind": "refines",
      "target_id": "CD-0088"
    }
  ],
  "product_wide_rationale": "Owns Concord development coordination and its separation from planning and repository review and merge evidence. CD-0213 amends planning and issue linkage. CD-0241 distinguishes eligible phase-local integration from main-branch review and merge evidence."
}
---
# CD-0089: Concord development coordination

- **Status:** Accepted
- **Date:** 2026-08-30
- **Scope:** Concord's development workflow, session identity, transitions,
  evidence, and completion after the bootstrap exception; issue #602
- **Approval:** The operator approved the decision contract in issue #602 on
  2026-08-30.
- **Related:** CD-0007, CD-0010, CD-0082, CD-0088, issue #600, and pull request #605
- **Preserves:** Linear as planning authority; pull requests and required
  checks as main-branch review and merge authority; branch and worktree isolation; and Product
  law

## Context

CD-0010 kept Concord's development on a GitHub-native path during bootstrap.
That boundary prevented Concord from using its own workflow, hid
bootstrap defects from normal use, and kept development outside its own
coordination system after Concord became the active coordination system.

Issue #600 blocked client registration and typed Concord dispatch. Its release
shipped through the GitHub path in pull request #605. That bootstrap exception
has ended, so Concord can coordinate its own development without changing which
surface owns planning, review, or merge evidence.

## Decision

### D1. priorities.md owns Concord's development route

[How Concord's own development runs during the rebuild](../constitution/priorities.md#how-concords-own-development-runs-during-the-rebuild)
in `priorities.md` owns Concord's development route, including new assignments,
work already underway, and authorized workflow-defect repair.

### D2. Linear owns planning; GitHub owns review and merge

Linear MCP owns planned work and defects ([CD-0213](CD-0213-linear-mcp-is-the-only-planning-authority.md) D1). Pull requests
and required checks remain the authority for review and merge evidence for
changes entering main. For eligible rapid-phase changes, CD-0241 defines local
integration and phase-delivery evidence. Existing independent-review obligations
remain unchanged; the reduced merge gate does not waive them. Phase-local
integration is not evidence of main-branch delivery, release publication, or
installation. Concord records links to the applicable evidence but does not
replace its authority. Branch and worktree isolation remain required.

### D3. The readiness floor does not gate development

The first-usable floor and release-evidence bar remain evidence claims with
their owning records. They do not block Concord development, trigger migration,
or grant authority to a partial implementation.

## Supersession boundary

Concord development requires branch and worktree isolation and Product-law
compliance. Main-branch review and merge
evidence is GitHub-native; CD-0241 defines eligible phase-local integration
evidence. The default-checkout implementation firewall and CD-0088's
bootstrap authority boundaries apply to self-development.

## Consequences

- Concord development follows `priorities.md` and links planning and delivery
  facts to their owning systems.
- Pull requests and required checks retain their authority for main-branch
  integration. CD-0241 defines eligible rapid-phase local integration evidence
  without waiving independent review.
- Readiness-floor claims remain separate from Concord's development route.

## Rejected alternatives

**Replace Linear planning or GitHub merge authority with Concord.** Rejected
because each fact retains its owning system under CD-0213.

**Treat partial use as readiness proof.** Rejected because readiness
remains an evidence claim governed by its owning records.

## Verification

- New Concord assignments, work already underway, and authorized defect repair
  follow the development route in `priorities.md`.
- Planned work and defect claims link to authoritative Linear issues.
- Main-branch review and merge claims link to the pull request and required
  checks. Eligible phase-local integration claims link to the local merge and
  recorded gate evidence required by CD-0241, with existing independent review
  preserved.
- The readiness floor does not block Concord development.
