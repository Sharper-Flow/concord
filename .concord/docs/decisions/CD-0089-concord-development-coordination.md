# CD-0089: Concord development coordination

- **Status:** Accepted
- **Date:** 2026-08-30
- **Scope:** Concord's development workflow, session identity, transitions,
  evidence, and completion after the bootstrap exception; issue #602
- **Approval:** The operator approved the decision contract in issue #602 on
  2026-08-30.
- **Related:** CD-0007, CD-0010, CD-0017, CD-0082, CD-0088, issue #600, and pull request #605
- **Preserves:** GitHub Issues as planning authority; pull requests and required
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

### D1. Concord owns its development workflow

After the issue #600 release, Concord coordinates Concord development. Each
development session has a Concord session identity and links to its authoritative
GitHub issue. Concord records workflow transitions, evidence verdicts, and
completion claims for that session.

### D2. GitHub remains authoritative for planning and merge

GitHub Issues remain the authority for planned work and defects. Pull requests
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

### D5. The bootstrap exception ends with issue #600

Concord development uses Concord while retaining the external authority
boundaries in D2.

## Supersession boundary

Concord development requires branch and worktree isolation, Product-law
compliance, and reference-only use of Advance. Main-branch review and merge
evidence is GitHub-native; CD-0241 defines eligible phase-local integration
evidence. The default-checkout implementation firewall and CD-0088's
bootstrap authority boundaries apply to self-development.

## Consequences

- Concord development records session identity, issue linkage, transitions,
  evidence, and completion in Concord.
- Pull requests and required checks retain their authority for main-branch
  integration. CD-0241 defines eligible rapid-phase local integration evidence
  without waiving independent review.
- The readiness floor remains measurable evidence and does not block dogfooding.
- Advance remains a reference source with no Concord write path.

## Rejected alternatives

**Keep the self-hosting prohibition until replacement readiness.** Rejected
because it blocks real Concord use and hides defects in the coordination path.

**Replace GitHub planning or merge authority with Concord.** Rejected because
the approved contract preserves public issue, pull request, and check authority.

**Treat partial use as replacement readiness.** Rejected because readiness
remains an evidence claim governed by the accepted floor and release evidence.

## Verification

- A Concord development session records a session identity and an authoritative
  GitHub issue link.
- Workflow transitions, evidence verdicts, and completion claims have Concord
  operation records.
- Planned work and defect claims link to authoritative GitHub Issues.
- Main-branch review and merge claims link to the pull request and required
  checks. Eligible phase-local integration claims link to the local merge and
  recorded gate evidence required by CD-0241, with existing independent review
  preserved.
- The existing readiness floor remains the source of replacement-readiness
  evidence without blocking Concord development.
- No Concord operation writes Advance state.
