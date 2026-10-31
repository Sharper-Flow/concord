# CD-0110: Work start chains from a terminal worktree

- **Status:** Accepted
- **Date:** 2026-09-04
- **Scope:** The `concord_work_start` origin precondition and the default branch
  base used by a chained worktree
- **Approval:** The operator approved this amendment for issue #829.
- **Related:** CD-0098, CD-0092, CD-0093, CD-0096, CD-0105, issue #829

## Context

A session left in a terminal worktree needs a safe route to its next item.

The session can chain safely when the origin is a clean, terminal Concord
worktree. A live item or dirty tree
must refuse before capture. The vacated worktree remains for the audit.

## Decision

### D1. Work start admits a terminal origin

`concord_work_start` admits the Project default checkout or an active Concord
worktree whose item is `completed`, `cancelled`, or `superseded`.

The origin check refuses a live item or a dirty tree. It has no verification-lease gate.
The core owns this
refusal. The adapter forwards it and does not create a second origin rule.

### D2. A chained worktree starts from the default branch

When the invocation directory is a linked worktree and `ref` is empty,
`work-bootstrap` resolves [`origin/HEAD`](https://git-scm.com/docs/git-symbolic-ref) and uses that branch ref as the base.
The terminal origin branch never becomes the implicit base.

### D3. The origin remains for audit reclamation

Work start moves the session only after it claims the new worktree. It does not
remove or reclaim the origin worktree. CD-0105 remains the reclamation route.

## Acceptance Criteria

```gherkin
Scenario: work_start chains from a terminal item's worktree
  Given a session that runs in a clean Concord-claimed worktree of a terminal item
  When the session calls concord_work_start for a new item
  Then the new worktree is claimed from the default branch tip
  And the session moves to the new worktree
  And the origin remains for worktree_audit_reclaim

Scenario: work_start refuses a live or dirty origin
  Given a session that runs in a Concord-claimed worktree
  When the origin has a live lifecycle or dirty files
  Then the core refuses before capture
  And the adapter forwards the core refusal
```

## Verification

- Core tests cover terminal admission, live refusal, dirty refusal,
  default-branch base selection and origin retention.
- Adapter tests cover terminal-origin forwarding and agreement with the core's
  origin refusal.
- `python3 scripts/check-knowledge-index.py` and the repository test tier pass.
