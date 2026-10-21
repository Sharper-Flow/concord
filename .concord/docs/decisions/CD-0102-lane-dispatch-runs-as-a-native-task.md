# CD-0102: Lane dispatch runs as a native Task under a one-time authorization

- **Status:** Accepted
- **Date:** 2026-09-02
- **Scope:** Worker lane execution, the dispatch authorization window, and the
  separation of host Task permissions from lane authorization; issue #689
- **Approval:** The operator approved this execution model in-session on
  2026-09-02 and rejected a worker that renders no Task card. On 2026-09-08,
  the operator selected explicit managed-session scope for D2 in
  [issue #938](https://github.com/Sharper-Flow/concord/issues/938).
  On 2026-10-17, the operator approved program-owned native scheduling and
  host-derived report model identity in the [Concord (CON) issue 1059 amendment batch](https://linear.app/sharper-flow/issue/CON-1059/move-lane-capability-enforcement-report-metadata-and-dispatch).
  On 2026-10-18, the operator approved the exact D2/D3 text admitting ordinary
  host Tasks beside registered lanes in
  [CON-1189](https://linear.app/sharper-flow/issue/CON-1189/subagent-task-calls-are-refused-in-a-concord-claimed-worktree-for-a).
  On 2026-10-20, the operator approved this amendment's exact replacement text
  in
  [CON-1271](https://linear.app/sharper-flow/issue/CON-1271/use-native-opencode-task-or-separate-sessions-for-concord-worker#comment-c61eaa2b-eb1a-42ec-9458-eced361746cc).
- **Related:** CD-0059, CD-0088, CD-0092, CD-0093, CD-0096, issue #689
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D5
- **Amends:** CD-0059 D1 for the worker execution route, not for its
  authorize-before-start rule
- **Preserves:** The lane packet contract, the lane report contract, and the
  rule that a lane records no workflow state

## Context

Worker execution needs the native Task card, child navigation, progress, and
cancellation. A detached process or a directly created child session does not
provide that interface.

An authorized dispatch determines the lane and its recorded packet. The
coordinator invokes the host's native Task after authorization. The adapter
binds the recorded packet to that call without scheduling another call,
selecting a model, or gaining host permission authority.

## Decision

### D1. Typed dispatch authorizes one coordinator-issued native Task

`dispatch_worker` keeps its current validation. It resolves the lane, checks the
packet against the lane digest, records the attempt, and authorizes the action
against the core before anything starts.

After the existing packet, directory, provenance and evidence-credential
prerequisites succeed, the action opens exactly one consumable authorization
window. No window can bind while a required credential check is still pending
or has failed. The window carries the work item, the attempt, the lane
identity, and the requesting session. A window authorizes one worker start and
no more. An admitted combined rejection and retry reaches the same window owner
with its recorded authorization; it does not authorize a second dispatch.

Dispatch returns authorization, not launch. Its caller-visible result names the
exact lane and attempt reference and instructs the coordinator to issue one
native foreground Task from that requesting session. It states that dispatch
itself launches no worker. The coordinator carries the reference, not an
authored or reconstructed worker packet.

The adapter does not queue executable subtasks, prompt the parent to cause a
launch, invoke private Task internals, register a replacement Task tool, or run
a second worker launcher. The host's native Task enforces its own permission
policy and owns the card, child session, progress, cancellation, model and
provider selection. Core authorization never bypasses a host denial.

No worker start is inferred from authorization, an open window, or a posted
message. An uncertain or failed Task is not replayed automatically. The
recorded attempt must reconcile or close through the existing recovery routes
before another execution is admitted. Losing an in-memory window does not
authorize recreating it or relaunching its attempt.

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

The hook binds an authorized window only to a Task selecting its exact
registered Concord lane from its requesting session. It admits only
`subagent_type`, `prompt`, and `description`; a resume, background, command, or
other execution-control field refuses. Before any window mutation or
asynchronous read for lane binding, including ancestry and directory lookup, it
requires the selected lane to match the captured window and the Task prompt to
equal that window's exact attempt identifier. This reference selects an
existing authorization; it grants none. A different lane, stale reference,
queued packet, or refused execution variant starts no worker and leaves the
valid window and caller arguments unchanged and usable by its matching call.
The captured-record identity fence spans every asynchronous binding read. After
those reads, the hook verifies that the same captured record remains open; a
revoked, replaced, or concurrently consumed record authorizes nothing. Only
then does it take the lane, prompt, and description from the recorded packet,
bind the actual host call identity, and consume the window once. Existing
execution-directory and turn-move checks remain required. The caller cannot
widen, rename, resume, or re-aim that attempt. A stale queued subtask cannot
consume a fresh authorization.

After any resume target passes the checks below, an ordinary host Task from a
caller with no managed parent retains its arguments and host permission checks,
whether the caller is enrolled or unmanaged. It creates no Concord attempt or
evidence and cannot consume a lane window. Generated utilities use this host
path. A caller with a managed parent cannot start or resume another child
through Task.

Managed ancestry alone does not make an ordinary host child a lane. The adapter
uses authenticated host parentage and the original host-recorded agent selection
to distinguish native children from registered lanes before matching a packet
or binding an attempt's child. An original lane child must still satisfy its
live attempt, packet, unique-child, directory, and capability checks. A later
agent selection cannot remove that boundary or grant a lane attempt. A native
child uses host tool permissions, not lane capabilities. Copying packet text or
a report frame does not make it a lane. No managed descendant gains Concord
tool or workflow authority through this distinction.

An enrolled caller with no managed parent may resume an existing session only
when its authenticated host parent is that caller and its original host-recorded
agent is not a lane. Every other existing target is refused, including foreign
unmanaged sessions. Generated utility calls follow the same rule. An unmanaged
caller cannot resume a managed session. Authoritatively missing native resume
targets retain the host's behavior. Unavailable or malformed target identity
does not authorize a resume.

Participation still follows host identity and recorded enrollment. Agent names,
prompts, repository names, and path conventions cannot clear enrollment or grant
an attempt. Invalid metadata, broken ancestry, or unavailable scope refuses the
Task call rather than guessing that the caller is unmanaged. Ordinary host tools
remain outside this Task hook; lane tool checks still apply. A refusal fails one
tool call, not the whole session.

An existing unmarked session enters managed scope through `work_start` before
it resumes managed work, or through an admitted public dispatch. Installing the
adapter alone does not enroll every host session.

### D3. Host permissions and lane authorization remain separate

The host owns Task visibility and which agents a caller may invoke. Its rules
may permit ordinary host agents and generated utilities alongside registered
lanes. A host denial stays a denial. Host permission alone never authorizes a
Concord lane: the adapter still requires the D2 window. Concord does not expand
host permissions or select models to admit a native Task or a lane.

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
the native route: the adapter reads that child session through the host API
and obtains its executing model and agent. The adapter populates the report's
model identity before validation. Worker-authored model values are neither
required nor authoritative. CD-0058 D1 and CD-0017 D5 keep their evidence.

Dispatch and completion remain separate adapter entry points. Dispatch
authorizes the attempt and opens the window before returning the instruction
for one native Task call. The coordinator invokes that Task, and the host runs
the worker between the binding and completion hooks. Completion receives the
result body, reads the worker session, resolves the report, signs the dispatch
and terminal assertions, and records the attempt outcome. Raw Task output is
not workflow acceptance or delivery.

A failed native Task with authenticated child identity uses the existing
terminal-event settlement when no successful after-hook result exists. Native
permission denial can occur after binding but before child creation. A failure
without child identity retains the consumed attempt for the existing no-report
abandonment or reconciliation route; it neither fabricates a child nor reopens
the window. Permission denial creates no automatic retry authority.

A failed native subtask may supply no after-hook result before it publishes
the terminal tool-part event. That event owns failure settlement. Its part
identity must match the hook identity retained for the attempt, whether the
host hook identifies the part or its separate tool call.

*(Amended 2026-10-09, [Linear](https://linear.app/sharper-flow/issue/CON-891/give-the-workers-final-report-an-explicit-protocol-identity-instead-of) — the final part carries a framed report.)*
The worker emits its lane report as one Markdown fence with the info string
`concord-worker-result-v1`, as the last text part of its session, with no
report content after it. One shared report-protocol parser selects the report
wherever the adapter observes worker output — native Task completion and
streamed run admission alike — so neither route admits by rules the other
does not follow. Unrelated prose or JSON after the designated frame does not
compete with it, and a second designated frame refuses the attempt as
ambiguous. A dispatch whose packet declares the protocol and whose output
carries no designated frame fails the attempt; ordinary malformed JSON
invents no report. The dispatch window stays single-use: this changes how
the report is identified, not how the worker is authorized.

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
promise it.)* *Before `dispatch_worker` opens a window, the core resolves the worktree claim the assignment names and compares it with
the directory the worker will run in, by filesystem identity: absolute, symlink-resolved, cleaned.
No active claim, an unresolvable session path, or a mismatch refuses with
`unauthorized_dispatch`. The refusal names the sha256 identity of each path,
never the path. The same check runs in preflight and in lane registration, so a
worker cannot run in a worktree the work item does not own.*

## Consequences

- The operator sees the standard worker card, opens the worker session, and
  cancels it from the terminal. Cancellation cancels the attempt.
- Host configuration outside this repository owns Task visibility and agent
  permissions. Host-permitted ordinary agents, including authoring agents that
  may edit files, can run beside registered lanes. Their results are not lane
  evidence and carry no Concord workflow authority.
- Concord no longer parses a child process. One failure class, the detached
  worker, is removed with the route that produced it.
- The coordinator issues one native Task after typed authorization. The
  adapter binds the recorded packet and one exact attempt reference without a
  custom executable queue or a second launcher. Authorization, observed worker
  execution, recorded report, accepted result, and delivered work remain
  distinct facts.
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
- `adapter/opencode/dispatch-window.test.ts` proves D2 window binding: a call
  without the exact session, lane and attempt reference refuses, and one
  matching call consumes the window and replaces caller arguments with the
  recorded packet once.
- `adapter/opencode/session-scope.test.ts` exercises D2 through the plugin hook:
  ordinary Tasks from enrolled and unmanaged callers, registered-lane refusal
  without a window, window non-interference, nested-Task refusal, parent
  inheritance, agent changes, plugin recreation, resume-target ownership,
  enrollment readback, and invalid scope.
- `adapter/opencode/lane_capabilities.test.ts` proves that original host
  provenance separates native children from lane children, that copied packet
  bytes cannot take a lane's child slot, and that malformed openings or
  ancestry grant nothing.
- `adapter/opencode/concord.test.ts` verifies capture and resume enrollment and
  refusal before core effects when the host cannot persist participation.
- `adapter/opencode/lane_dispatch.test.ts` verifies that failed enrollment cannot
  authorize dispatch. `adapter/opencode/dispatch_route_end_to_end.test.ts` checks
  enrollment before real core dispatch and preserves the completion route.
- The adapter dispatch and hook regressions prove that ordinary dispatch and
  combined rejection/retry issue no executable subtask write. Their serialized
  responses say authorization only and identify the next native Task call. A
  stale queued packet, wrong lane or attempt reference, replayed call, resume
  target, background request, or command variant cannot start a worker or
  acquire a fresh authorization. Refused stale, wrong-lane and
  execution-control calls leave the valid window usable. A suspended or failing
  credential probe admits no concurrent Task. Native permission denial before
  child creation retains its exact attempt for no-report recovery without
  reopening it. The existing single-use, directory, evidence, native-permission
  and terminal-event tests retain their obligations.
- `adapter/opencode/lane_completion.test.ts` checks native failure-part identity
  and leaves an absent after-hook result for the terminal event to settle.
- The adapter test `lane report resolves from the worker result body` proves D5,
  including the missing-report failure.
- `TestDispatchRefusesImplementationLaneFromDefaultCheckout` proves D7.
- A repository check proves D6: no `opencode run` spawn remains in the adapter.
- `internal/agent.TestAgentJobsCorpus` runs the TS1 corpus against this dispatch
  path, so the floor item `fc2-ts1-corpus-executed` keeps naming the real route.
- The scenario `WF48-lane-pipeline-typed-evidence` drives the registered lanes
  through this path, so the floor item `fc3-lane-pipeline` keeps its evidence.
- The coverage shard `.concord/docs/knowledge/coverage/CD-0102.json`, composed by
  `scripts/knowledge_index.py`, records CD-0102 as proved once the tests above
  pass.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.
