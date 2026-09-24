# CD-0176: Lane workers stay native subagents, and each repository gets its own coordinator session

- **Status:** Proposed
- **Date:** 2026-09-24
- **Scope:** Where a lane worker runs, how one work item reaches a second
  repository, and what makes a worktree safe to remove; issues #1358, #1322,
  #1125
- **Approval:** The operator directed on 2026-09-24 that lane workers stay
  native subagents started by the host `task` tool.
- **Related:** CD-0092, CD-0096, CD-0098, CD-0102, CD-0103, CD-0104, CD-0111
- **Amends:** CD-0104 at the occupancy release rule; the coordinator
  instruction to claim a second Project's worktree and move into it
- **Preserves:** CD-0102 in full, CD-0103, and CD-0104 D1

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
does not stop when the operator presses Esc. The operator requires native
subagents.

## Decision

### D1. A lane worker is a native subagent in its coordinator's directory

A lane worker runs through the host `task` tool, as CD-0102 states. Its
directory is the directory of the coordinator session that dispatches it, as
CD-0104 D1 states. Concord creates no worker session through the host session
API, and Concord registers no plugin tool named `task`.

### D2. One coordinator session per repository

A work item that touches two repositories has one coordinator session in each
repository. Each session starts in its own repository and calls
`concord_work_start` with the `work_id`. The session then drives the work in
its own Project's worktree and dispatches lanes there.

A session never moves into another repository. A `worktree_claim` for a
Project in another repository refuses before it creates a worktree. The
refusal is typed, it is not retryable, and its remedy names the route in this
rule. The adapter reads the host refusal message and returns it unchanged.

Within one repository, the move route of CD-0098 and CD-0103 stays as it is.

### D3. Occupancy ends when its host process ends

An occupancy row records the host process that holds the session, by process
id and process start time, as the host lease of CD-0111 does. The kernel
proves whether that process is alive, in every host process on the machine.

An occupancy row is released by `session_vacate`, by a verified landing in
another worktree of the same work item, or by the end of its host process.
The adapter session list is not an input to release or to removal. A worktree
with a live occupant is never removed.

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
- Keep the session list as the release input. Rejected: the proof of concept
  showed that the list omits the sessions of other repositories.

## Consequences

Work in two repositories needs two coordinator sessions. Both sessions act on
one work item with one contract, and `expected_version` orders their writes.
Neither session edits the other repository.

The generated coordinator instructions change. They must tell a coordinator to
start a session in the second repository. They must not tell it to claim the
second worktree and move into it.

A move within one repository still lands on the next turn. This record does
not remove that cost.

An occupancy row needs the host process identity. Rows written before the
change have no identity. The implementation must state how such a row is
released, and it must not release a row that a live session holds.

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

## Verification

- A `worktree_claim` for a Project in another repository refuses with no
  worktree created and a remedy that names D2.
- Two coordinator sessions in two repositories each dispatch a lane on one
  work item, and each lane writes only in its own repository's worktree.
- An occupancy row whose host process ended is released. A row whose host
  process runs in another host process is not released, and its worktree is
  not removed.
- The coordinator instruction check finds no text that tells a coordinator to
  move into another repository.
