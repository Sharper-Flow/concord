# CD-0178: Each repository gets its own coordinator session

- **Status:** Accepted
- **Date:** 2026-09-24
- **Approval:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 160](https://linear.app/sharper-flow/issue/CON-160).
- **Approval:** 2026-10-17. Coordinator approval under the operator's delegation for [Concord (CON) issue 1095](https://linear.app/sharper-flow/issue/CON-1095/worker-evidence-authority-gaps-generic-applyoperation-may-append).
- **Scope:** How native delegation and one work item reach a second
  repository; issues #1358, #1322,
  #1125
- **Approval:** The operator directed on 2026-09-24 that lane workers stay
  native subagents started by the host `task` tool. The operator accepted
  this decision on 2026-09-25 in the Concord (CON) work item CON-450.
  The operator approved replacement of the numbered-example guidance with the
  current profile as exact text on 2026-10-20 for
  [Concord (CON) issue 1274](https://linear.app/sharper-flow/issue/CON-1274).
- **Related:** CD-0088, CD-0092, CD-0103, CD-0111, CD-0152,
  CD-0154, CD-0176, CD-0213
- **P12:** Serious: the named primary agent and its permission boundary change.
- **Preserves:** CD-0103 and CD-0104 D3 to D5

## Context

The coordinator's host directory determines where its native lane worker runs.

The host limits that value in three ways. A move lands on the next turn only
(CD-0103). The host refuses a move into another git repository with
`Destination directory belongs to another project`. The refusal is deliberate
and has existed since the move route was added in
[anomalyco/opencode PR 30640](https://github.com/anomalyco/opencode/pull/30640).
The session list `GET /session` returns only the sessions of the current
project, so one session cannot see the sessions of another repository.

A native `task` subagent runs in the instance of the session that calls it.
Its parameters carry no directory, and upstream
[issue 46424](https://github.com/anomalyco/opencode/issues/46424) asks for one
and is open. So a native Task subagent runs where its coordinator runs.

A proof of concept on opencode 1.18.32 showed that the adapter can create a
child session in another repository through `POST /session` with a
`directory` query. That child renders no Task card, shows no live steps, and
does not stop when the operator presses Esc. Concord creates no such subagent
session; native Task remains host-owned under CD-0242 D6.

## Decision

### D1. Native delegation belongs to the host

The coordinator uses native Task under the host's directory and permissions.
Concord creates no subagent through a session API and registers no replacement
`task` tool. It records subject-bound stage outcomes without proving the child,
directory, model or execution identity that produced them under CD-0242 D5/D6.

### D2. One coordinator session per repository

Every coordinator session with a verified landing in the receiving Project
may use the shared handoff bind and dispatch under it
([CD-0182](CD-0182-session-opener.md)). Repository-local execution and the refusal
of a move into another repository stay unchanged.

A work item that touches two repositories has one coordinator session in each
repository. The first session records the second Project membership with
`set_memberships`. Host tools place the second session in its own repository
under CD-0088 D1. It then drives that Project's part of the work in its own
worktree and delegates through native Task there.

A session never moves into another repository. The core resolves the
repository of the calling directory at each call through the Project
resolver of `internal/agent/authority.go`. Host placement does not substitute
for current Project membership, authority, or handoff evidence.

Repository-local `Concord` sessions own their item decisions and hold the
execution authority described by this clause. They need no release train
engineer (RTE) to complete their work. The user may choose to start `Concord-RTE`
for concurrent Product or Project work. That optional session sequences the
independent work-item sessions across authorized Projects. The RTE does not
claim their worktrees, run their subagents, accept their work, or publish for
them. Its tool permissions do not transfer their authority. It conveys facts
and references, not substitute workflow or handoff evidence. Session placement
and control procedures belong to the host's instructions, not the RTE profile.

### D3. Host tools own worktree lifetime

Concord records no new worktree occupancy or claim landing and provides no
vacate, reclaim, or destroy operation. Host tools own placement and removal
under CD-0088 D1. Retained worker directory checks, Project handoff placement
reads, and native verification leases do not become ownership writers.

## Alternatives considered

- Create each worker session through the host session API in its claimed
  worktree. Rejected: the worker renders no Task card, shows no live steps,
  and ignores Esc. The operator requires native subagents.
- Register a plugin tool named `task` that creates the worker in the claimed
  worktree. Rejected: Concord would replace the host `task` tool for every
  agent. Concord would then own depth limits, child permissions, resume, and
  background tasks.
- Keep one coordinator session and add more guards. Rejected: no guard can
  pass a move that the host refuses, so work in a second repository stays
  unreachable.

## Consequences

Work in two repositories needs two coordinator sessions. Both sessions act on
one work item with one contract, and `expected_version` orders their writes.
Neither session edits the other repository. Native delegation stays with its
repository-local coordinator. The workflow step stays shared: a transition by
one session changes the step for
both.

The generated `Concord` profile carries the route of D2. Under CD-0154,
numbered coordinator definitions remain host-owned; Concord does not modify
those host files.

A move within one repository still lands on the next turn. This record does
not remove that cost.

## Verification

- Project handoff tests retain D2's shared bind and placement read checks.
- The generated `Concord` profile states the cross-repository boundary.
  The profile's own boundary wording governs; literal phrase checks do not
  prove its meaning or the absence of paraphrased move instructions
  (CD-0055 D4).
