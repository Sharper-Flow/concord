---
{
  "id": "CD-0241",
  "title": "Rapid-development phases run the machine on a rapid branch; main and releases keep the full contract",
  "authority": {
    "tier": "derived"
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
      "target_id": "CD-0088"
    },
    {
      "kind": "refines",
      "target_id": "CD-0089"
    },
    {
      "kind": "refines",
      "target_id": "CD-0166"
    }
  ],
  "product_wide_rationale": "Rapid-development phases change the source coordination target and delivery evidence across all Domains while preserving full main-branch verification and automated releases."
}
---
# CD-0241: Rapid-development phases run the machine on a rapid branch; main and releases keep the full contract

- **Status:** Accepted
- **Date:** 2026-10-20
- **Scope:** Rapid-development coordination, completion evidence, and main-branch verification
- **Approval:** The operator approved the exact title wording and D1–D5 on 2026-10-20.

## Context

Per-change full CI cycles bottleneck rapid development. This decision records
the operator-approved staged gate for a declared rapid-development phase.
Git's current commit (HEAD) identifies its current branch tip.
The release train engineer (RTE) checks the approved text before merge.

## Decision

### D1. A declared rapid phase moves the machine's coordination target to one rapid integration branch.

The operator may declare a rapid-development phase naming one integration branch (for example rapid/<phase>) and an expiry date. During the phase, new worktrees claim from the rapid branch HEAD. The coordinating agent merges change branches into the rapid branch locally after a reduced gate passes locally: the conventional-commit title check, verify-go, and the targeted test suites owning the changed packages. Each such merge records its local gate evidence digests in the merge commit message. No merge to the rapid branch bypasses the reduced gate, and no merge occurs after phase expiry. In-flight items that began on main may land into the rapid branch under the same reduced gate.

### D2. Workflow completion evidence during a phase is the locally-evidenced rapid-branch merge.

A work item whose change branch has merged into the rapid branch with its recorded evidence digests is deliverable-complete for the phase; the phase's consolidation provides the full-contract merge evidence. Workflow admission and the store continue to run on the installed release throughout the phase, so engine behavior stays main-based while source work targets the rapid branch.

### D3. Main and releases retain the full verification contract without exception.

The rapid branch lands to main through a GitHub pull request satisfying every required check via the merge queue, at least once at phase end and optionally on a cadence during the phase. Releases continue to be cut automatically from main only. Local merges to main, hand-cut releases, and any reduction of main's required checks remain prohibited.

### D4. Law, security, and data-safety surfaces never ride the rapid route.

A change touching accepted law text, authentication, authorization, secrets, store migrations, or release tooling uses the standard pull-request route with the full contract during any phase.

### D5. Every rapid phase closes with a full-contract landing.

When a phase expires or the operator ends it, the rapid branch either lands through the full contract or is abandoned, and the machine's coordination target returns to main. No rapid branch outlives its declared phase.

## Alternatives considered

Per-change full verification retains the current CI cost. The approved decision
instead places reduced gates on rapid-branch merges and full verification on
their consolidation to main.

## Consequences

The decision separates phase delivery evidence from full-contract consolidation.
It leaves the installed engine and automated main-based releases unchanged.
This record contains no implementation changes to workflow admission or branch
selection.

## Verification

- The operator approved the title wording and D1–D5 as written. The RTE review
  compares those bytes with the approved text and checks that the surrounding
  sections add no normative text.
- Committed law-folder snapshot validation checks the record and its content
  digest.
