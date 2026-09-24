# CD-0176: Lane workers stay native subagents, and each repository gets its own coordinator session

- **Status:** Proposed
- **Date:** 2026-09-24
- **Scope:** Where a lane worker runs, how one work item reaches a second
  repository, and what protects a worktree from removal; issues #1358, #1322,
  #1125
- **Approval:** The operator directed on 2026-09-24 that lane workers stay
  native subagents started by the host `task` tool.
- **Related:** CD-0092, CD-0098, CD-0102, CD-0103, CD-0111, CD-0151, CD-0152,
  CD-0154, CD-0155
- **Amends:** CD-0104 D1 and D2 at the stored occupant; CD-0096 D3 Destroy,
  CD-0105 D2, CD-0118 D3, and CD-0120 D3 at the session observation input
- **Preserves:** CD-0102 in full, CD-0103, CD-0104 D3 to D5, CD-0151, and the
  within-repository move of CD-0098

## Context

Concord reads one value, the host directory of the coordinator session, for
three separate purposes. It is the directory where a lane worker writes. It
is the source of worktree occupancy. It is the input that decides whether a
worktree is safe to remove.

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
and is open. So a native lane worker always runs where its coordinator runs.

A proof of concept on opencode 1.18.32 showed that the adapter can create a
child session in another repository through `POST /session` with a
`directory` query. That child renders no Task card, shows no live steps, and
does not stop when the operator presses Esc. CD-0102 records that the
operator rejected such a worker, and the operator confirmed that requirement.

Issue #1358 shows the cross-repository failure. A `worktree_claim` for a
Project in another repository created the worktree, and then the host refused
the move. The adapter returned `retry_same_request`, and every replay failed
the same way.

The store already records one occupant session on each worktree entry, the
`occupant_session_ref` field in `internal/store/worktrees.go`. The removal
gate reads it. No decision record authorizes it, and CD-0104 D1 states that
Concord stores no binding from a session to a worktree. The field holds one
session only. A second session that lands in a worktree fails its occupancy
record with `worktree_ownership_conflict`, although CD-0104 D5 lets two
sessions run in one worktree. The release rule reads the caller-supplied
session observation, which the proof of concept showed is blind to other
repositories.

## Decision

### D1. A lane worker is a native subagent in its coordinator's directory

A lane worker runs through the host `task` tool, as CD-0102 states. Its
directory is the directory of the coordinator session that dispatches it.
Concord creates no worker session through the host session API, and Concord
registers no plugin tool named `task`.

### D2. One coordinator session per repository

A work item that touches two repositories has one coordinator session in each
repository. The first session records the second Project membership with
`set_memberships`. The second session starts in its own repository and calls
`concord_work_start` with the `work_id`. It then drives that Project's part
of the work in its own worktree and dispatches lanes there.

A session never moves into another repository. The core resolves the
repository of the calling directory at each call, through the Project
resolver of `internal/agent/authority.go`. A `worktree_claim` whose target
Project lives in another repository refuses before the core creates a
worktree. The refusal kind is `cross_repository_claim`, it is not
retryable, and its remedy names the route in this rule.

The adapter reads the message of every host move refusal and returns it
unchanged. After this rule, a claim cannot reach the host cross-repository
refusal, so the remaining move refusals keep their existing classification.

Within one repository, the move route of CD-0098 and CD-0103 stays as it is.

### D3. Occupancy is one row per session, and it ends with its host process

Concord records occupancy as a fact it owns: the session that claimed or
landed in a worktree through a Concord operation. CD-0104 D1 is amended to
admit this record. The session's working directory is still read from the
host per call, and no operation resolves a directory from an occupancy row.

A worktree has one occupancy row for each session that landed in it. A
landing adds a row and never refuses because another session holds a row.
Each row records the session reference and the host process identity: the
process id and process start time of the OpenCode process whose adapter
recorded it. The host lease of CD-0111 reads the same identity through
`internal/hostlease`.

A row is released by `session_vacate`, by a verified landing of the same
session in another worktree of the same work item, or by the end of its host
process. The kernel proves whether a process is alive, for every host process
on the machine. The caller-supplied session observation is not an input to
release or to removal, and the `observed_session_directories` input is
removed from the removal operations.

A worktree with a live occupancy row is not removed by a reclaim or by an
audit pass. A destructive removal with operator approval under CD-0096 D3
stays available, and its approval consequence names each live occupant.

A row written before this rule has no process identity. It stays until
`session_vacate` or an operator-approved removal releases it. Liveness never
releases it.

### D4. Content gates stay as they are

The content gates for removal do not change. A merged terminal worktree and
a clean unstarted worktree stay eligible, and uncommitted content stays
report-only. D3 changes only the occupancy input.

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
- Keep the session observation as the release input. Rejected: the proof of
  concept showed that the observation omits the sessions of other
  repositories.
- Remove stored occupancy and rest removal on content alone. Rejected: a
  clean unstarted worktree passes every content gate while a session works
  in it.

## Consequences

Work in two repositories needs two coordinator sessions. Both sessions act on
one work item with one contract, and `expected_version` orders their writes.
Neither session edits the other repository. The dispatch window is scoped to
one session, so each session can dispatch a lane in its own repository. The
workflow step stays shared: a transition by one session changes the step for
both.

The coordinator instructions change. Under CD-0154 the numbered coordinator
definitions belong to the host, so no generator writes them. The examples in
`examples/opencode/agents/` gain the route of D2. Each host edits its own
`concord-1.md` and `concord-2.md` by hand and removes any text that tells a
coordinator to claim a second worktree and move into it.

A move within one repository still lands on the next turn. This record does
not remove that cost.

Process liveness is coarser than session liveness. One OpenCode process can
hold many sessions, for example under `opencode serve`. A row whose session
ended in a live process stays until `session_vacate`, a landing elsewhere, or
the end of that process. That cost keeps a worktree longer, and it never
removes a live one.

The linked open work takes these dispositions:

| Item | Disposition |
|---|---|
| [#1358](https://github.com/Sharper-Flow/concord/issues/1358) cross-repository claim move | Fixed by D2 |
| [#1322](https://github.com/Sharper-Flow/concord/issues/1322) reported move that did not land | Independent; CD-0103 governs |
| [#1125](https://github.com/Sharper-Flow/concord/issues/1125) lane writes outside the worktree | Independent; D1 keeps the existing guards |
| work-e2c0dffe24416cf6d106a626 next-turn move cost | Independent; D2 does not add moves |
| work-02e898db3f441e2007af334a reclaim under live sessions | Fixed by D3 |
| work-30598c83e9f676093ad3ac1c audit blind to other processes | Fixed by D3 |
| work-61736f7c24cde1c94844c167 audit removes a live worktree | Fixed by D3 |
| work-b8f69d65034a1207dd2b40cf vacate clears occupancy early | Independent |
| work-0228c078b9fa01a5e485727e resume records no occupant | Fixed by D3 |
| work-1d0676d0ea6fa1e14b2b7592 worktree has no enforced owner | Fixed by D3 |
| work-a164c308215037315f6ade79 recreated worktree keeps occupancy | Superseded by D3 |
| work-d1627ad522600bea9c87c417 content-reachability policy | Independent; D4 |
| work-57165c6c0285632642ed14f5 handoff matches the session directory | Independent; D1 |
| work-5604d254ec27df800c0d055a removal strands non-session processes | Independent; D3 covers host sessions only |
| work-751acb4faaaeeaa76758dd15 route to another Product's work item | Independent; CD-0155 governs Products |

## Verification

- A `worktree_claim` for a Project in another repository refuses with
  `cross_repository_claim`, creates no worktree, and names D2 as the remedy.
- Two coordinator sessions in two repositories each dispatch a lane on one
  work item, and each lane writes only in its own repository's worktree.
- A second session that lands in an occupied worktree records its own row
  and receives no `worktree_ownership_conflict`.
- A row whose host process ended is released. A row whose host process is
  alive is not released, also when that process runs in another repository,
  and its worktree is not removed by a reclaim or an audit pass.
- A row with no process identity is not released by liveness.
- The removal operations accept no `observed_session_directories` input.
- The coordinator examples contain no text that tells a coordinator to move
  into another repository.
