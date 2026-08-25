# Development authority before replacement readiness

**Status:** Accepted under CD-0010.
**Approval date:** 2026-08-07.
**Approval:** Operator plan approval.

This is the interim authority model for public Concord development. It keeps the
project GitHub-native while Concord is not yet ready to coordinate its own
development.

## Context

Concord is a public Go project that cannot yet coordinate its own
development. The binding input is CD-0010's accepted rule: the project stays
GitHub-native until replacement readiness is proven. This record fixes the
interim authority model — which surface owns each fact type — and the seven
rules that keep public evidence authoritative while Concord is developed
outside itself.
## Contract

The binding contract is the authority-by-fact-type table and the seven
rules: issues plan work, pull requests and checks own review and merge
evidence, branches and worktrees isolate implementation, accepted documents
are Product law, the predecessor is reference-only, Concord does not
self-host its development before replacement readiness, and migration
happens only under the accepted Product-at-a-time fix-forward policy.
## Authority by fact type

| Fact or action | Authority | Evidence |
|---|---|---|
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
6. Concord does not self-host Concord development before replacement readiness. The
   public GitHub model remains authoritative until the accepted replacement-ready
   floor is proven.
7. Migration happens later under the accepted Product-at-a-time fix-forward policy;
   migration is not implied by an issue, branch, or partial implementation.

CD-0010 is the accepted pre-readiness rule. Any replacement of this model requires
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
