# Development authority before replacement readiness

**Status:** Accepted under CD-0010.
**Approval date:** 2026-08-07.
**Approval:** Operator plan approval.

This is the interim authority model for public Concord development. It keeps the
project GitHub-native while Concord is not yet ready to coordinate its own
development.

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
