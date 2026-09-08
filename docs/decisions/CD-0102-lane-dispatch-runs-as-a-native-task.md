# CD-0102: Lane dispatch runs as a native Task under a one-time authorization

- **Status:** Accepted
- **Date:** 2026-09-02
- **Scope:** Worker lane execution, the dispatch authorization window, the
  coordinator posture tool ruleset; issue #689
- **Approval:** The operator approved this execution model in-session on
  2026-09-02 and rejected a worker that renders no Task card. On 2026-09-08,
  the operator selected explicit managed-session scope for D2 in
  [issue #938](https://github.com/Sharper-Flow/concord/issues/938).
- **Related:** CD-0059, CD-0088, CD-0092, CD-0093, CD-0096, issue #689
- **Amends:** CD-0059 D1 for the worker execution route, not for its
  authorize-before-start rule
- **Preserves:** The lane packet contract, the lane report contract, and the
  rule that a lane records no workflow state

## Context

`dispatch_worker` starts a worker today by spawning `opencode run` as a child
process. The adapter authorizes the action against the core first, then spawns.
The operator sees a detached process. The terminal shows no progress, offers no
navigation into the worker, and offers no cancellation.

The terminal renders a worker card only for the tool named `task`. The name is
fixed in the terminal source. A plugin cannot supply a renderer, and a plugin
tool under any other name renders no card.

The native tool creates a child session, runs one agent in it, and returns the
last text part of that session. The model issues the call, so the tool arguments
originate outside Concord. A packet that the model composes carries no
provenance Concord can trust.

The tool is also absent from a posture that denies it outright. OpenCode drops
a tool from the model request when the last rule matching its permission has the
pattern `*` and the action `deny`.

## Decision

### D1. Typed dispatch opens one authorization window

`dispatch_worker` keeps its current validation. It resolves the lane, checks the
packet against the lane digest, records the attempt, and authorizes the action
against the core before anything starts.

The action then opens exactly one authorization window. The window carries the
work item, the attempt, the lane identity, and the session that requested it. A
window authorizes one worker start and no more.

### D2. The plugin binds the packet to the next matching call

The adapter plugin observes tool execution before the tool runs. The hook
receives the tool name, the session, the call identity, and the arguments. The
arguments are mutable.

A valid `work_start` capture or resume enrolls the calling session in managed
Task scope before the core bootstrap or resume operation. A public
`dispatch_worker` request enrolls after its packet validates and before core
dispatch authorization. Reads and ordinary host tasks do not enroll a session.

The host owns durable participation in its session metadata. The
`MANAGED_TASK_SCOPE_KEY` declaration in `adapter/opencode/move-session.ts` names
the metadata entry. Its only recorded value is `managed`; absence means no
local enrollment. A child inherits managed scope through the host's parent
identity. An agent change, plugin reload, or host restart does not clear scope.
An operator owns the host policy. The adapter exposes enrollment, not a route
for an agent to clear participation and escape its work boundary.

Participation records no worktree path, workflow state, or copy of a Concord
claim. [CD-0104](./CD-0104-a-session-worktree-is-its-actual-directory.md)
continues to derive directories from the host and claims from Concord.
Enrollment preserves unrelated metadata and must pass persisted
readback before the Concord operation proceeds. A failed enrollment refuses
that operation. A later bootstrap or dispatch refusal does not clear recorded
participation.

The hook refuses any `task` call from a managed session without an authorized
window. With a window, it overwrites the agent selection and prompt from the
recorded packet, removes a worker-resume target, and consumes the window once.
The caller cannot widen, rename, or re-aim the authorized work.

An ordinary Task from an unmanaged session retains its arguments and host
permission checks. It creates no Concord attempt or evidence. Unmanaged calls
cannot start a registered Concord lane without a window or resume a managed
session. A missing unmanaged resume target keeps the native host behavior.

The scope decision uses host session identity and recorded participation, not
agent names, prompts, repository names, or path conventions. Invalid metadata,
broken ancestry, or unavailable scope refuses the Task call rather than
guessing that the caller is unmanaged. Other host tools remain outside this
Task hook. The refusal fails one tool call, not the whole session.

An existing unmarked session enters managed scope through `work_start` before
it resumes managed work, or through an admitted public dispatch. Installing the
adapter alone does not enroll every host session.

### D3. The coordinator ruleset keeps the tool visible and lane-scoped

A blanket deny hides the tool, and a hidden tool cannot reach the hook. The
coordinator posture therefore orders its rules so that a lane pattern is last.
Visibility follows the last rule matching the permission name. The decision at
call time follows the last rule matching both the permission and the agent name.

That ordering keeps the tool visible, allows the lane agents, and refuses every
other agent type before the hook runs.

### D4. Lanes cannot nest workers

OpenCode refuses a nested worker at depth one by default. Each lane definition
also removes the tool. Both controls stay. A lane returns a report and starts
nothing.

### D5. The report is the final text part of the worker session

The worker session returns its last text part to the caller. A lane ends its
turn with the lane report and nothing after it. The adapter parses that body
with the existing report parser.

A worker that ends without a valid report fails the attempt. A worker whose own
tool call failed fails the attempt too, because OpenCode reports that failure in
place of the result.

The host wraps that body as `<task id="SESSION_ID" state="...">`, so the worker
session identifier arrives with the result. Readback evidence therefore survives
the native route unchanged: the adapter exports that session and reads the
executing model and executing agent from it, exactly as the child-process route
did. CD-0058 D1 and CD-0017 D5 keep their evidence without amendment.

Dispatch and completion are consequently two adapter entry points, not one call.
Dispatch authorizes the attempt and opens the window, and it returns before the
worker runs. Completion receives the result body, exports the worker session,
resolves the report, signs the dispatch and terminal assertions, and records the
attempt outcome. A single blocking call cannot span the two, because the host
runs the worker between them.

### D6. The child-process route is removed

`dispatch_worker` starts a worker one way. The `opencode run` spawn, its
argument construction, its standard-output parsing, and its stream recovery are
removed. No second route survives as a fallback.

### D7. The worker runs where the session runs

OpenCode creates the worker session in the directory the request carries. A
session retargeted under CD-0096 therefore dispatches its lanes into the claimed
worktree, and a session in a default checkout cannot dispatch an
implementation-bearing lane at all.

*(Amended 2026-09-06, issue #826 — the core checks this, the host does not
promise it.)* *Before `dispatch_worker` opens a window, the core resolves the
target work item's active worktree claims and compares them with the session's
worktree (CD-0104) by filesystem identity: absolute, symlink-resolved, cleaned.
No active claim, an unresolvable session path, or a mismatch refuses with
`unauthorized_dispatch`. The refusal names the sha256 identity of each path,
never the path. The same check runs in preflight and in lane registration, so a
worker cannot run in a worktree the work item does not own.*

## Consequences

- The operator sees the standard worker card, opens the worker session, and
  cancels it from the terminal. Cancellation cancels the attempt.
- The coordinator posture changes from a blanket deny to an ordered ruleset.
  That posture is host configuration outside this repository, so this record
  states the required shape and the host applies it.
- Concord no longer parses a child process. One failure class, the detached
  worker, is removed with the route that produced it.
- A lane start now depends on the coordinator issuing the call the hook expects.
  The window makes that dependency explicit and single-use.
- Unmanaged host sessions retain native Task behavior without Concord workflow
  authority. Managed participation survives changes to the selected agent.
- The host must persist session metadata and expose parent identity. Enrollment
  refuses when that supported host surface cannot record and return the policy.
- Background workers stay unused. They require an experimental flag and detach
  the result from the attempt window.

## Rejected alternatives

**Keep the child process.** Rejected because it renders no card. The operator
named that outcome unacceptable, and no adapter change adds a card to it.

**Ship a plugin tool under another name.** Rejected because the terminal binds
the card to the tool name. A renamed tool reproduces the detached experience.

**Trust the model to carry the packet.** Rejected because provenance would rest
on prose the model composes. Overwriting the arguments makes the binding
structural.

**Create the child session directly through the SDK.** Rejected because the
result is a child session with no card and no attempt, which is the outcome this
record removes.

**Leave the tool denied and authorize per call.** Rejected because a blanket
deny removes the tool from the request. The hook never runs, so the
authorization has nothing to bind.

## Verification

- `TestDispatchOpensSingleUseAuthorizationWindow` proves D1: one action opens one
  window, and a second worker start finds none.
- The adapter test `dispatch hook refuses an unauthorized task call` proves D2
  for the closed-window case.
- The adapter test `dispatch hook replaces caller arguments with the packet`
  proves D2 for the open-window case, including a caller that names another
  agent.
- `adapter/opencode/session-scope.test.ts` exercises D2 through the plugin hook:
  unmanaged Tasks, managed refusals, parent inheritance, agent changes, plugin
  recreation, resume-target admission, enrollment readback, and invalid scope.
- `adapter/opencode/concord.test.ts` verifies capture and resume enrollment and
  refusal before core effects when the host cannot persist participation.
- `adapter/opencode/lane_dispatch.test.ts` verifies that failed enrollment cannot
  authorize dispatch. `adapter/opencode/dispatch_route_end_to_end.test.ts` checks
  enrollment before real core dispatch and preserves the completion route.
- The adapter test `lane report resolves from the worker result body` proves D5,
  including the missing-report failure.
- `TestDispatchRefusesImplementationLaneFromDefaultCheckout` proves D7.
- A repository check proves D6: no `opencode run` spawn remains in the adapter.
- `internal/agent.TestAgentJobsCorpus` runs the TS1 corpus against this dispatch
  path, so the floor item `fc2-ts1-corpus-executed` keeps naming the real route.
- The scenario `WF48-lane-pipeline-typed-evidence` drives the registered lanes
  through this path, so the floor item `fc3-lane-pipeline` keeps its evidence.
- `docs/law-coverage.v1.json` records CD-0102 as proved once the tests above
  pass.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.
