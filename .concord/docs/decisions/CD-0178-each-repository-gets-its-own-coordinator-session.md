# CD-0178: Lane workers stay native subagents, and each repository gets its own coordinator session

- **Status:** Accepted
- **Date:** 2026-09-24
- **Amended:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 160](https://linear.app/sharper-flow/issue/CON-160). A new worker completion must prove its directory.
- **Amended:** 2026-10-17. Coordinator approval under the operator's delegation for [Concord (CON) issue 1095](https://linear.app/sharper-flow/issue/CON-1095/worker-evidence-authority-gaps-generic-applyoperation-may-append). A new completion proves its original Project's claim.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D5
- **Scope:** Where a lane worker runs, how one work item reaches a second
  repository, and what protects a worktree from removal; issues #1358, #1322,
  #1125
- **Approval:** The operator directed on 2026-09-24 that lane workers stay
  native subagents started by the host `task` tool. The operator accepted
  this decision on 2026-09-25 in the Concord (CON) work item CON-450.
  The operator approved replacement of the numbered-example guidance with the
  current profile as exact text on 2026-10-20 for
  [Concord (CON) issue 1274](https://linear.app/sharper-flow/issue/CON-1274).
- **Related:** CD-0092, CD-0098, CD-0102, CD-0103, CD-0111, CD-0151, CD-0152,
  CD-0154, CD-0176, CD-0213
- **Amendment reason:** Concord needs no release train engineer; a user may
  choose one to sequence independent work-item sessions.
- **Revision choice:** In-place amendment under CD-0036. Repository-local
  execution, native dispatch, handoffs, and worktree occupancy stay unchanged.
- **P12:** Serious: the named primary agent and its permission boundary change.
- **Amends:** CD-0104 D1 and D2 at the stored occupant; CD-0096 D3 Destroy,
  CD-0105 D2, CD-0118 D3, and CD-0120 D3 at the session observation input
- **Preserves:** CD-0102 in full, CD-0103, CD-0104 D3 to D5, CD-0151, and the
  within-repository move of CD-0098
- **Amended by:** [CD-0182](CD-0182-session-opener.md) at D2's exclusive
  session count, through CON-850. Concord (CON) issue CON-910 settles this
  record's Verification citation of the removed launch surface; the decision
  sections are unchanged. [CD-0239](CD-0239-a-later-process-claim-permits-one-bounded-host-recovery-attempt.md)
  D3 amends the replacement-host landing rule of D3.

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

A lane worker runs through the host `task` tool, as CD-0102 states. Its directory is the claimed worktree its assignment names: the dispatching
coordinator's worktree, or a member worktree of a concurrent group
([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)).
Concord creates no worker session through the host session API, and Concord
registers no plugin tool named `task`.

Every new authenticated worker completion carries the nonempty worker directory the host observed. The live worker-evidence ingest checks that directory against the claimed worktree its assignment names before appending completion. An omitted or empty directory provides no proof and refuses without recording completion. Rebuilding recorded events never requires a live filesystem check. An exact acknowledgment of recorded completion appends nothing and does not repeat the directory check.

A new worker completion proves the active claimed worktree of the Project that the attempt's original dispatch authorization identifies. An active claim for another Project of the same work item is not proof. If the original Project or its active claim cannot be established, the completion refuses without recording a terminal outcome. Rebuilding recorded events does not check the live filesystem. An exact acknowledgment appends no new completion and does not repeat this check.

### D2. One coordinator session per repository

**Amended by [CD-0182](CD-0182-session-opener.md) (CON-850):** The exclusive
session count widens to every coordinator session with a verified landing
in the receiving Project. Each such session may use the shared handoff
bind and dispatch under it. Repository-local execution and the refusal
of a move into another repository stay unchanged.

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
retryable, and its remedy names the route in this rule. The refusal carries
a typed `session_placement` detail with the work identifier, target Project
identifier, and target Project's registered canonical checkout directory.
The directory never comes from the caller or the Git common directory.
The receiving session resumes the work through `concord_work_start` in its own
repository. Concord does not treat a placement answer as a verified landing.

The adapter reads the message of every host move refusal and returns it
unchanged. After this rule, a claim cannot reach the host cross-repository
refusal, so the remaining move refusals keep their existing classification.

Within one repository, the move route of CD-0098 and CD-0103 stays as it is.

Repository-local `Concord` sessions own their item decisions and hold the
execution authority described by this clause. They need no release train
engineer (RTE) to complete their work. The user may choose to start `Concord-RTE`
for concurrent Product or Project work. That optional session sequences the
independent work-item sessions across authorized Projects. The RTE does not
claim their worktrees, dispatch their lanes, accept their work, or publish for
them. Its tool permissions do not transfer their authority. It conveys facts
and references, not substitute workflow or handoff evidence. Session placement
and control procedures belong to the host's instructions, not the RTE profile.

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

A same-session landing with a different core-checked process identity, or
with a legacy row lacking process identity, appends a new landing event and
refreshes that row (CD-0239 D3). The session-to-process association remains
caller-supplied. A replay from the same recorded process remains idempotent.

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

The generated `Concord` profile carries the route of D2. Under CD-0154,
numbered coordinator definitions remain host-owned; Concord does not modify
those host files.

A move within one repository still lands on the next turn. This record does
not remove that cost.

Process liveness is coarser than session liveness. One OpenCode process can
hold many sessions, for example under `opencode serve`. A row whose session
ended in a live process stays until `session_vacate`, a landing elsewhere, or
the end of that process. That cost keeps a worktree longer, and it never
removes a live one.

## Verification

- A `worktree_claim` for a Project in another repository refuses with
  `cross_repository_claim`, creates no worktree, and names D2 as the remedy.
  `internal/agent.TestWorktreeClaimRefusesCrossRepositoryBeforeCreation`
  proves the refusal kind, the absent worktree, and the named remedy.
- One work item holds a worktree in each of two repositories, a
  cross-repository claim refuses before creating one, and a
  second-repository resume runs as a host-started session that resumes
  with the `work_id` alone while this session captures nothing.
  `internal/store.TestStoreTwoProjectClaimsConcurrent` proves the two
  worktrees on one item;
  `internal/agent.TestWorktreeClaimRefusesCrossRepositoryBeforeCreation`
  proves the refusal;
  `bun test adapter/opencode/concord.test.ts -t "work start resume re-reads
  the handoff after the landing for a cold second session"` proves the cold
  second session: the resume re-reads the addressed handoff after its
  verified landing, and the asserted child sequence contains no capture and
  no opener run. `bun test adapter/opencode/concord.test.ts -t
  "cross-repository placement passes the generated adapter boundary"` proves
  the refusal carries the typed `session_placement` envelope. The
  two-session dispatch route itself is D2 decision text carried by this
  record's acceptance, not a claim of this section.
- A second session that lands in an occupied worktree records its own row
  and receives no `worktree_ownership_conflict`.
  `internal/store.TestClaimLandingTransfersOccupancyInOneTransaction` and
  `internal/store.TestClaimLandingRecordsResumedSessionInUnoccupiedWorktree`
  prove the added row and the absent refusal.
- A row whose host process ended is released. A row whose host process is
  alive is not released, also when that process runs in another repository,
  and its worktree is not removed by a reclaim or an audit pass.
  `internal/store.TestLegacyRowReleasesOnEndedRecordingProcess` proves the
  ended-process release. `internal/store.TestReclaimWorktreeKeepsLiveOccupantRefusal`,
  `internal/agent.TestWorktreeReclaimRefusesOccupiedWorktreeThroughToolSurface`,
  and `internal/store.TestWorktreeAuditReclaimRefusesResumedOccupiedUnstartedWorktree`
  prove a live row keeps the worktree from a reclaim and an audit pass.
- A row with no process identity is not released by liveness.
  `internal/store.TestReclaimWorktreeKeepsLiveOccupantRefusal` and
  `internal/store.TestLegacyRowStaysOnUnreadableLeaseSet` prove the row
  stays while a live lease predates it and when the release proof cannot
  run.
- The removal operations accept no `observed_session_directories` input.
  `bun test adapter/opencode/concord.test.ts` proves no removal input
  carries the field and no removal makes a host session-list round-trip.
- The generated `Concord` profile states the cross-repository boundary.
  `python3 scripts/check-primary-prompts.py` proves the boundary statement is
  present. A repository
  check cannot prove the absence of every paraphrased move instruction
  (CD-0055 D4); the profile's own boundary wording governs that.
