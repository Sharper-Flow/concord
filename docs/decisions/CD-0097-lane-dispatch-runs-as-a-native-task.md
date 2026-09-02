# CD-0097: Lane dispatch runs as a native Task under a one-time authorization

- **Status:** Accepted
- **Date:** 2026-09-02
- **Scope:** Worker lane execution, the dispatch authorization window, the
  coordinator posture tool ruleset; issue #689
- **Approval:** The operator approved this execution model in-session on
  2026-09-02 and rejected a worker that renders no Task card.
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

The hook refuses a `task` call when no window is open for that session. When a
window is open, the hook overwrites the agent selection and the prompt from the
recorded packet, then closes the window. The model cannot widen, rename, or
re-aim the work, because the call it issues is replaced by the recorded packet.

A refused call fails the tool. The attempt stays recorded and unstarted, and the
coordinator reports the refusal.

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
- The adapter test `lane report resolves from the worker result body` proves D5,
  including the missing-report failure.
- `TestDispatchRefusesImplementationLaneFromDefaultCheckout` proves D7.
- A repository check proves D6: no `opencode run` spawn remains in the adapter.
- `internal/agent.TestAgentJobsCorpus` runs the TS1 corpus against this dispatch
  path, so the floor item `fc2-ts1-corpus-executed` keeps naming the real route.
- The scenario `WF48-lane-pipeline-typed-evidence` drives the registered lanes
  through this path, so the floor item `fc3-lane-pipeline` keeps its evidence.
- `docs/law-coverage.v1.json` records CD-0097 as proved once the tests above
  pass.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.
