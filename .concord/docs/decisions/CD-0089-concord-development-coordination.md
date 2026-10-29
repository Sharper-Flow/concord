# CD-0089: Concord development coordination

- **Status:** Accepted
- **Date:** 2026-08-30
- **Scope:** Concord's development workflow, session identity, transitions,
  evidence, and completion after the bootstrap exception; issue #602
- **Approval:** The operator approved the decision contract in issue #602 on
  2026-08-30.
- **Related:** CD-0007, CD-0010, CD-0017, CD-0082, CD-0088, issue #600, and pull request #605
- **Preserves:** Linear as planning authority; pull requests and required
  checks as main-branch review and merge authority; branch and worktree isolation; Product
  law; replacement readiness as an evidence claim; and Advance as reference-only

## Context

CD-0010 kept Concord's development on a GitHub-native path until replacement
readiness. That boundary prevented Concord from using its own workflow, hid
bootstrap defects from normal use, and kept development on a predecessor-style
path after Concord became the active coordination system.

Issue #600 blocked client registration and typed Concord dispatch. Its release
shipped through the GitHub path in pull request #605. That bootstrap exception
has ended, so Concord can coordinate its own development without changing which
surface owns planning, review, merge, or predecessor evidence.

## Decision

### D1. priorities.md owns Concord's development route

[How Concord's own development runs during the rebuild](../priorities.md#how-concords-own-development-runs-during-the-rebuild)
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

### D3. Replacement readiness does not gate development

Replacement readiness remains the evidence claim defined by the accepted floor
and release evidence. It does not block Concord development, trigger migration,
or grant authority to a partial implementation.

### D4. Advance remains reference-only

Concord does not write Advance state or treat Advance runtime state as a second
authority. Public Advance issues and pull requests remain reachable reference
material for predecessor lessons.

## Supersession boundary

Concord development requires branch and worktree isolation, Product-law
compliance, and reference-only use of Advance. Main-branch review and merge
evidence is GitHub-native; CD-0241 defines eligible phase-local integration
evidence. The default-checkout implementation firewall and CD-0088's
bootstrap authority boundaries apply to self-development.

## Consequences

- Concord development follows `priorities.md` and links planning and delivery
  facts to their owning systems.
- Pull requests and required checks retain their authority for main-branch
  integration. CD-0241 defines eligible rapid-phase local integration evidence
  without waiving independent review.
- Replacement-readiness claims remain separate from Concord's development route.
- Advance remains a reference source with no Concord write path.

## Rejected alternatives

**Replace Linear planning or GitHub merge authority with Concord.** Rejected
because each fact retains its owning system under CD-0213.

**Treat partial use as replacement readiness.** Rejected because readiness
remains an evidence claim governed by the accepted floor and release evidence.

## Verification

- New Concord assignments, work already underway, and authorized defect repair
  follow the development route in `priorities.md`.
- Planned work and defect claims link to authoritative Linear issues.
- Main-branch review and merge claims link to the pull request and required
  checks. Eligible phase-local integration claims link to the local merge and
  recorded gate evidence required by CD-0241, with existing independent review
  preserved.
- The existing readiness floor remains the source of replacement-readiness
  evidence without blocking Concord development.
- No Concord operation writes Advance state.
