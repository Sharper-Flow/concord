# CD-0089: Concord development coordination

- **Status:** Accepted
- **Date:** 2026-08-30
- **Scope:** Concord's development workflow, session identity, transitions,
  evidence, and completion after the bootstrap exception; issue #602
- **Approval:** The operator approved the decision contract in issue #602 on
  2026-08-30.
- **Related:** CD-0007, CD-0010, CD-0017, CD-0082, issue #600, and pull request #605
- **Supersedes:** CD-0010's prohibition on Concord coordinating its own
  development before replacement readiness, and only that prohibition
- **Preserves:** GitHub Issues as planning authority; pull requests and required
  checks as review and merge authority; branch and worktree isolation; Product
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
and required checks remain the authority for review and merge evidence. Concord
records links to those public records but does not replace their authority.

### D3. Replacement readiness does not gate development

Replacement readiness remains the evidence claim defined by the accepted floor
and release evidence. It does not block Concord development, trigger migration,
or grant authority to a partial implementation.

### D4. Advance remains reference-only

Concord does not write Advance state or treat Advance runtime state as a second
authority. Public Advance issues and pull requests remain reachable reference
material for predecessor lessons.

### D5. The bootstrap exception ends with issue #600

Issue #600 and its release used the existing GitHub-native path because the
defect blocked Concord bootstrap. After that release, Concord development uses
Concord while retaining the external authority boundaries in D2.

## Supersession boundary

This decision supersedes only CD-0010's self-hosting prohibition. It also
retires equivalent restatements in CD-0007 D5, CD-0017 D9, CD-0019 D4,
CD-0021's consequences, CD-0047's context and consequences, CD-0082 D2, and
CD-0084's context. Those records retain their historical text. CD-0010's
planning, review, merge, isolation, Product-law, and Advance rules remain
current. The CD-0010 file and historical research records remain unchanged.

Current instructions and living law cite this decision where they describe
Concord development authority. Historical decisions retain their original text
and are superseded by this record only at the boundary named above.

## Consequences

- Concord development records session identity, issue linkage, transitions,
  evidence, and completion in Concord.
- GitHub Issues, pull requests, and required checks retain their authority.
- The readiness floor remains measurable evidence and does not block dogfooding.
- Advance remains a reference source with no Concord write path.
- Issue #600 is the final bootstrap exception.

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
- Review and merge claims link to the pull request and required checks.
- The existing readiness floor remains the source of replacement-readiness
  evidence without blocking Concord development.
- No Concord operation writes Advance state.
- The knowledge record, coverage shard, aggregate manifests, and repository
  validators identify and verify this supersession.
