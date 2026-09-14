# CD-0144: Reconciliation records repository reality before reclaim

- **Status:** Accepted
- **Date:** 2026-09-13
- **Scope:** Worktree content drift classes, reclaim recommendations, and the
  execution boundary for Domain exclusivity
- **Approval:** The operator approved this bounded reconciliation duty and its
  execution claim boundary.
- **Related:** CD-0041, CD-0088, CD-0095, CD-0096, CD-0118
- **Amends:** CD-0041 D6 where it binds the active architecture footprint to
  contract approval
- **Preserves:** Local Git and SQLite as audit authorities; the clean-tree,
  merged-branch, and observed-session reclaim gates; operator approval for
  destructive removal

## Context

The work record and the repository can disagree without a query naming the
content at risk. A present worktree with uncommitted changes or local-only
commits can appear as terminal drift, so the audit can recommend a reclaim that
would discard the only copy of work.

CD-0041 D6 also derives a work item's architecture footprint from its approved
contract. Approval records intent, but execution is the boundary that starts
the Product-changing action. A contract that has not started execution must not
hold a Domain against a peer.

## Decision

### D1. Content is a first-class drift class

The worktree audit adds `uncommitted_content` and `unpushed_content` classes.
The first class comes from local `git status --porcelain`. The second class
comes from the count of branch commits unreachable from every local
remote-tracking ref. Each row carries its lifecycle, names the content risk,
and uses `worktree_inspect` rather than `worktree_reclaim` as its recovery
action.

### D2. Content risk suppresses reclaim recommendation

A worktree with either content class does not also emit `terminal_present` or
`unstarted_present`. The audit reclaim pass therefore returns the content rows
as report-only and does not invoke a reclaim. The direct reclaim keeps its
clean-tree and merged-branch gates unchanged, and the observed-session gate
continues to refuse removal.

### D3. Domain exclusivity starts with execution

An approved contract with no execution start holds no active architecture
footprint and cannot block a peer. The execution-start transition moves the
work item into `in_progress`; the overlap guard then checks its approved
footprint against other executing items. The subject keeps its prospective
footprint during that check so the claim cannot be skipped at its boundary.

### D4. The audit has no network authority

The audit reads the worktree, local Git refs, and the store. It does not query a
pull request or any other external service. Delivery reconciliation against an
external service remains a separate decision.

## Consequences

- The audit identifies uncommitted changes and unpushed commits before any
  reclaim recommendation can discard them.
- Content-bearing worktrees remain visible and require inspection or an
  operator-selected recovery instead of an automatic reclaim path.
- Dormant approved contracts no longer create Domain contention before work
  starts.

## Verification

- `internal/store.TestWorktreeAuditProtectsUncommittedAndUnpushedContent`
  asserts both content classes, their risk statements, and report-only reclaim
  behavior.
- `internal/store.TestWorktreeAuditReclaimsMergedTerminalWork` asserts clean
  terminal reclaim and content-risk protection.
- `internal/store.TestUnstartedContractDoesNotBlockAPeer` and
  `internal/store.TestExecutionStartMovesLifecycleIntoTheClaim` assert the
  execution claim boundary.
- `python3 scripts/generate-agent-contracts.py --check` verifies the generated
  payload schema projection.
