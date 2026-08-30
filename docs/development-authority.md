# Concord development authority

**Status:** Accepted under CD-0089.
**Approval date:** 2026-08-30.
**Approval:** Operator approval for issue #602.

This is the authority model for Concord development under CD-0089. Concord owns
its workflow records while GitHub retains planning, review, and merge authority.

## Context

Concord is a public Go project that coordinates its development through its own
workflow after the issue #600 bootstrap release. CD-0089 supersedes only CD-0010's
self-hosting prohibition. This record fixes which surface owns each fact type and
keeps public evidence authoritative during Concord development.
## Contract

The binding contract is the authority-by-fact-type table and the six rules:
Concord owns its workflow records, issues plan work, pull requests and checks own
review and merge evidence, branches and worktrees isolate implementation, accepted
documents are Product law, and the predecessor is reference-only.
## Authority by fact type

| Fact or action | Authority | Evidence |
|---|---|---|
| Concord development workflow | Concord | Session identity, linked issue, transitions, evidence, and completion records |
| Planned work and defects | GitHub Issues | Issue body, labels, links, and discussion history |
| Review and merge | Pull requests and required checks | Review decisions, check results, merge record, and changed files |
| Implementation isolation | Git branches and worktrees | Branch ancestry, worktree boundaries, and commits |
| Product law | Decision records, specifications, and constitutional docs | Accepted document status and internal links |
| Predecessor lessons | Public Advance issue and pull-request records | Reachable citations in [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md) |

## Rules

1. GitHub Issues plan work and record defects; they do not silently amend accepted
   product law.
2. Pull requests and checks provide review and merge evidence. A local claim is not
   merge evidence until the corresponding public record exists.
3. Branches and worktrees isolate implementation. Direct edits to the default branch
   are not the implementation path.
4. Decision records and specifications are Product law. A pull request that conflicts
   with accepted law must surface the conflict rather than silently narrowing scope.
5. Advance is reference-only. Concord does not dual-write predecessor state and does
   not treat predecessor runtime state as a second authority.
6. Replacement readiness is an evidence claim defined by the accepted floor and
   release evidence. It does not block Concord development or trigger migration.

CD-0089 supersedes only CD-0010's self-hosting prohibition. The CD-0010 file and
historical research records remain unchanged. Any later change to this model needs
an accepted decision record and public review evidence.

## Acceptance criteria

- Given a claim that planned work or a defect exists
  When the claim is checked
  Then a GitHub issue is the authority, and no agent surface may silently
  amend accepted Product law.

- Given a claim that a change merged
  When the claim is checked
  Then the public pull request with its required checks is the merge
  evidence, and a local claim is not.

- Given implementation work
  When it is written
  Then it lives on a branch and worktree, never directly on the default
  branch.

- Given a pull request that conflicts with accepted law
  When review runs
  Then the conflict is surfaced, never silently narrowed.

- Given Concord development work
  When an agent starts a session
  Then Concord records the session identity and its authoritative GitHub issue.

- Given a workflow transition, evidence verdict, or completion claim
  When the operation succeeds
  Then Concord records the operation in its durable authority.

- Given a replacement-readiness claim
  When the claim is checked
  Then the accepted readiness floor supplies the evidence without blocking
  Concord development.

- Given Concord development work
  When the workflow records its state
  Then Concord writes no Advance state.

## Verification

This record governs process authority, which is enforced by repository
validators rather than executed scenarios, so every criterion carries a
typed exemption naming the enforcing mechanism.

- Criterion 1 is enforced structurally: GitHub Issues are the planning
  surface in `docs/development-authority.md` rule 1, and issue-state
  snapshots enter the repository only through the accepted
  `scripts/release.py` evidence path guarded by
  `scripts/check-public-content.py`.
- Criterion 2 is enforced by `scripts/check-commit-title.py` and the
  required-checks configuration of `.github/workflows/ci.yml`, which block
  a release bump without public merge evidence.
- Criterion 3 is enforced by `scripts/check-doc-links.py` repository-path
  resolution over the default-branch history check in
  `test_default_branch_history_is_clean`
  (`scripts/test-commit-title.py`), which rejects direct default-branch
  implementation commits.
- Criterion 4 is enforced by `scripts/check-knowledge-closure.py --strict`,
  which requires every document under knowledge roots to carry an accepted
  record or explicit disposition before a change can pass validation.
- Criteria 5 and 6 are enforced by the Concord session and transition contracts,
  which require session identity, issue linkage, and durable operation records.
- Criterion 7 uses the accepted floor manifest and release evidence.
- Criterion 8 is enforced by `scripts/check-predecessor-independence.py`.
