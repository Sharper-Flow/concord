# CD-0088: Host tools own worktree isolation; capability does not establish self-host readiness

- **Status:** Accepted
- **Date:** 2026-08-30
- **Scope:** Host-owned Git worktree isolation and Concord's self-host readiness restriction
- **Related:** CD-0010, CD-0031, CD-0049, CD-0072, CD-0079, CD-0104, CD-0243
- **Preserves:** CD-0010 and the default-checkout implementation firewall

## Decision

### D1. Host tools own worktree isolation

The host owns branch and worktree creation, placement, recovery, and removal.
Implementation uses an isolated branch and worktree. The default checkout stays
on its default branch and remains the read and deploy source.

Concord does not capture or resume work through a worktree bootstrap, claim a
worktree, derive host branch or path policy, move a session into or out of a
worktree, record claim or vacate landings, restore a missing checkout, or audit,
reclaim, destroy, or compensate native worktree ownership effects. These duties
belong to host tools, not a second Concord entry route.

### D2. Ownership does not gate Product-state cleanup

Concord work removal and terminal cleanup do not require worktree claims,
occupancy, native retirement, restoration debt, or Git deletion proof. Cleanup
does not remove a host worktree or branch. Existing non-ownership eligibility,
scope, version, evidence, handoff, and receipt requirements remain.

Retained worktree metadata supports the existing native verification,
inspection, session preparation, continuity, and Review reads. This decision
does not drop tables or change live data. Native verification leases and
read-only inspection retain CD-0096's obligations. Project handoff and other
non-ownership rules are not retired by this clause.

### D3. Host placement grants no Concord authority

A host-created worktree grants no Product authority. Concord resolves the
calling Project and applies its existing authentication, capability, scope,
workflow, identity, and default-checkout checks. Host tools do not waive them.

### D4. Session preparation keeps its identity guarantees

`session-prepare` verifies the active work directory, installed agent definitions,
and orchestrator identity, and obtains the core-derived session packet. It
records no placement or ownership. Its directory and registry refusals remain
required under CD-0093.

### D5. Capability does not activate Concord self-hosting

This decision does not claim replacement readiness and does not amend CD-0010.
Main-branch review and merge evidence remain GitHub-native. CD-0241 defines
eligible rapid-phase local integration evidence without changing default-checkout
isolation or installed workflow admission. Using Concord to coordinate Concord
development requires both a proven replacement-readiness claim and a later
accepted decision that changes development authority.

## Reason

Concord's worktree ownership duplicates the host's Git lifecycle and makes
Product-state work depend on native checkout bookkeeping. Host isolation keeps
code separate without that ownership service. Removing the service does not
establish workflow readiness or remove current verification and review duties.

## Verification

- Ownership tools and CLI entry routes are absent from their authored manifests.
- Work removal and terminal cleanup retain non-ownership refusals while ignoring
  retained ownership rows.
- Session preparation, native verification, inspection, continuity, and Review
  keep their existing checks.
- Knowledge, law coverage, documentation links, and generated projections agree
  with this boundary.
