# CD-0181: A squash-merged branch is durable where the default ref holds its net diff

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** The durable gate for terminal worktree reclaim and the unpushed
  content class, for branches whose squash merge deleted the remote head
  branch
- **Amends:** CD-0144 D1 at the derivation of the unpushed class
- **Preserves:** CD-0144 D4 in full, the remote-ref count as the first way to
  pass, the clean-tree and observed-session gates, and operator approval for
  destructive removal
- **Related:** CD-0096, CD-0104, CD-0105, CD-0118, CD-0144
- **Approval:** The operator approved this decision in contract version 1 of
  work item `work-77b5c1691911819c7d94abcd`.

## Context

A squash merge rewrites the branch's commits into one new commit on the
default ref. The remote head branch is gone after the merge. Local
remote-tracking refs then hold no branch commit.

CD-0144 D1 derives the unpushed class from the count of branch commits
unreachable from those refs. The reclaim gate counts the same refs. Both
surfaces therefore call the branch unpushed forever. The worktree of a
terminal work item reads as content risk and never reclaims, and no local
action can lift that block.

A dry run on the live repository measured the block: 35 of 36 terminal
worktrees held branches whose whole net diff the default ref already holds as
one commit.

## Decision

### D1. Squash containment is the second way to pass the durable gate

A terminal branch counts as merged and durable when the default ref holds a
commit whose `git patch-id --stable` equals the branch's net diff from its
merge base. The remote-ref count stays the first way to pass. The audit
classifier and the reclaim gate use this one check.

The comparison reads two fixed commits: the branch net diff, and the
non-merge commits the default ref holds since that merge base. The answer
therefore cannot decay. Every probe reads the worktree, local Git refs, and
patch text, so CD-0144 D4 holds.

### D2. The refused edges stay refused

An empty net diff establishes no containment. Commits beyond the merged
patch change the net diff and establish no containment either. A dirty tree
keeps every gate it runs now. A probe that cannot run, or a runner without
the stdin plumbing, keeps the count alone authoritative.

## Alternatives considered

- Map `refs/pull/*` into the local tracking refs. Rejected: the mapping needs
  per-repository git config and fetches every pull-request ref.
- Ask the remote with `git ls-remote`. Rejected: the audit gains network
  authority, which breaks CD-0144 D4.
- Keep the count alone and let the operator destroy each worktree by hand.
  Rejected: every affected worktree then costs operator attention for content
  the default ref already holds.

## Consequences

- A terminal worktree whose squash merge deleted the remote head branch
  reclaims without a push that can never happen.
- Branch content the default ref does not hold still refuses, and its
  worktree stays report-only.
- The audit runs local Git probes only. It touches no network service and no
  git config.

## Verification

- `internal/store.TestReclaimWorktreeAcceptsSquashMergedBranchWithDeletedRemote`
  asserts the gate accepts a contained branch whose remote head branch is
  deleted.
- `internal/store.TestReclaimWorktreeResolvesDefaultRefForSquashContainment`
  asserts the gate resolves the default ref from the repository's
  `origin/HEAD` when the caller names none.
- `internal/store.TestReclaimWorktreeRefusesEmptyNetDiff` and
  `internal/store.TestReclaimWorktreeRefusesCommitsBeyondTheMergedPatch`
  assert the refused edges of D2.
- `internal/store.TestWorktreeAuditReclaimsSquashContainedTerminalWork`
  asserts the classifier keeps no unpushed row for a contained branch, and
  the audit reclaim pass reclaims the worktree through the same check.
