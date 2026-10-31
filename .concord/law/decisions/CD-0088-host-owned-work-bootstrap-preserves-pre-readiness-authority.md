---
{
  "id": "CD-0088",
  "title": "Host tools own worktree isolation; capability does not establish self-host readiness",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "agent-adapter",
  "applies_to_domain_ids": []
}
---
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

Concord provides no worktree inspection operation and retains no worktree
claim, occupancy, entry, reclaim, or restore tables or metadata. Host shell and
Git tools own checkout inspection as well as lifecycle. No replacement locator
registry or compatibility bridge preserves the retired inspection service.

A mandatory verified-snapshot upgrade removes the retired tables and rows only
after their consumers are retired. It preserves active work, knowledge,
research, Project membership and handoff records, and artifacts. Ordinary
directory-resolved Project authority, session identity, no-write preparation,
continuity for verified work context, and Review retain their non-worktree
requirements. Repository-check execution and evidence follow CD-0242 D8.

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

- Ownership and worktree inspection tools and CLI entry routes are absent from
  their authored manifests.
- `internal/store.TestWorktreeRetirementFreshAbsence` and
  `internal/store.TestWorktreeRetirementVerifiedSnapshotUpgrade` assert that
  current stores contain no ownership tables. Work removal and terminal cleanup
  retain their non-ownership refusals without those tables.
- The verified-snapshot upgrade removes worktree metadata without dropping
  active work, knowledge, research, Project handoffs, or artifacts.
- Session preparation, continuity, and Review keep their non-worktree checks.
- Knowledge, law coverage, documentation links, and generated projections agree
  with this boundary.
