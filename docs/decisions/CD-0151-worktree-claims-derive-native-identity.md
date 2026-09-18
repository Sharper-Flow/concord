# CD-0151: Worktree claims derive native identity

- **Status:** Accepted
- **Date:** 2026-09-14
- **Scope:** Worktree claim identity and active claim uniqueness
- **Approval:** The operator approved this law in the existing-work bootstrap plan.
- **Related:** CD-0008, CD-0119
- **Amends:** CD-0119 D2 at the existing-item bootstrap claim route

## Context

The worktree claim operation accepted branch and path values from its caller.
Those values could diverge from the registered Project locator. The claim then
validated caller input instead of making divergent identity unrepresentable.

## Decision

### D1. The core derives claim identity

`worktree_claim` derives the branch and path from the Project locator and the
work identity. The branch is `work/<work_id>`. The path is
`<data-root>/worktrees/<project_id>/<work_id>`. The claim request carries no
caller-supplied branch or path.

### D2. Active native identity is unique

The pinned path is globally unique: the store permits at most one pending or
verified claim for each pinned path, so one repository cannot stand behind two
Projects. The pinned branch is unique per repository: a branch is native state
of one repository, and one work item claiming worktrees in two Projects derives
the same branch name in each repository, so the store permits at most one
pending or verified claim for each repository and branch pair. Reclaimed
historical claims occupy neither slot.

## Acceptance Criteria

```gherkin
Scenario: A claim derives its native identity
  Given a registered Project locator and a work item
  When Concord creates a worktree claim
  Then the claim branch is work/<work_id>
  And the claim path is the canonical data-root worktree path
  And caller input cannot replace either value

Scenario: Active native identity is unique
  Given an active claim for a pinned path
  When another claim names the same derived path
  Then the store refuses the second active claim

Scenario: Active branch identity is unique within one repository
  Given an active claim for a branch in a repository
  When another claim names the same derived branch in the same repository
  Then the store refuses the second active claim

Scenario: Two repositories may hold the same derived branch
  Given an active claim for a branch in one repository
  When another claim names the same derived branch in another repository
  Then the store admits the second claim
```

## Verification

- `internal/store` claim tests prove derivation, replay, and refusal behavior.
- Schema migration tests prove the two partial unique indexes.
- Generated agent contract checks pass after the claim surface regeneration.
