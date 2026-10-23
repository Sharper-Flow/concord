# CD-0154: Numbered primary agents remain host-owned

- **Status:** Accepted
- **Date:** 2026-09-16
- **Scope:** Ownership and distribution of the `concord-0`, `concord-1`, and
  `concord-2` OpenCode definitions, and portable named primary profiles
- **Approval:** The operator approved host ownership with repository examples.
  The operator approved retirement of the shaping and driving examples as exact
  text on 2026-10-20 for
  [Concord (CON) issue 1274](https://linear.app/sharper-flow/issue/CON-1274).
  The operator approved the coordinator-routing amendment as exact text on
  2026-10-19 for
  [Concord (CON) issue 1212](https://linear.app/sharper-flow/issue/CON-1212).
  The operator approved the Concord-Planner amendment as exact text on
  2026-10-19 for
  [Concord (CON) issue 1183](https://linear.app/sharper-flow/issue/CON-1183).
- **Amendment reason:** Concord is the self-sufficient primary agent; a user
  may choose an optional release train engineer for concurrent work.
  A profile-level allow-all shell rule overrode the host's shell denials, so
  the profiles deny only named unsafe commands and leave every other command
  to the host.
  A work-item session brings the six operator approval classes directly to the
  operator; the release train engineer (RTE) coordinates dependencies, not those decisions (CD-0243 D1, D2).
- **Revision choice:** In-place amendment under CD-0036. Existing workflow pins
  and stored contracts keep their meaning; the managed profile name changes
  through the installer's existing retirement transaction.
- **P12:** Serious: the agent name users select, its permission boundary, and
  which choices reach the operator change.
- **Related:** CD-0043, CD-0063, CD-0102, and CD-0111

## Context

The three numbered coordinator definitions combine Concord authority with local
machine policy. The local policy includes model routing, tool providers,
credentials, and permission overrides. A public release cannot own that
machine-specific state.

Concord already manages worker-lane definitions. Those workers are release
assets with generated contracts. The numbered coordinator definitions are
operator entry points and remain host configuration.

## Decision

### D1. The host owns numbered primary agents

Concord does not install, update, migrate, remove, repair, or restore files named
`concord-0.md`, `concord-1.md`, or `concord-2.md`. The installer manifest and
release archive manage only the named primary profiles, worker lanes and
utilities listed in `AGENT_FILES`.

Install, upgrade, repair, and uninstall operations leave existing numbered
primary-agent bytes unchanged. These operations continue to install, update,
repair, and remove worker definitions.

### D2. Concord distributes portable named primary profiles

The intake example lives under `examples/opencode/agents/`. This directory is
not an OpenCode discovery path and is not an installer or release-package input.

A host can copy and adapt the example manually. Concord does not track or adopt
the resulting host file.

Concord generates and installs three named primary profiles from
`contracts/agent-primary-profiles.v1.json` and its instruction sources.
`Concord` is the self-sufficient primary agent. It owns design, implementation,
tests, independent review, law changes, publication, CI follow-through,
deployment, and planning for its item. It needs no release train engineer (RTE)
session, assignment, acceptance, or publication service to complete work.

The user may choose to start `Concord-RTE` when a Product or Project has many
simultaneous work streams or needs a coordinated burst of development. This
session is optional; it is not a required layer above Concord.

The user may also choose to start `Concord-Planner` as an assistant Product
Owner: it investigates needs, refines proposed product direction, and helps
maintain an actionable backlog. The operator owns product direction and
approval. It is optional and never a gate on Concord's work. It reads
code and law, runs bounded diagnostics, writes research notes and standalone
prototypes under the project's `.planning` directory, and updates Linear
records within the scope the operator approves. It surfaces where an executing
agent would have to guess, separates what it observed from what it recommends,
writes only approved decisions into a ticket's description, marks every
unapproved assumption as unapproved, and proposes a consequential batch of
record changes before applying it when an operator approval class applies.
Routine record maintenance within agreed scope needs no fresh approval.
It does not edit other files, commit,
push, open or merge pull requests, deploy, edit live configuration, change
Product law, start or advance managed work, or approve execution.

Each work-item session decides within its item. It asks the operator directly
for the six classes in
[CD-0243 D2](CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision).
It uses an advisor for a second opinion and a spawned review subagent for
external review. It needs neither an RTE nor a Planner to finish its item and
never waits for their acceptance.

The RTE spots shared owners and conflicts, sets merge order for dependent or
conflicting branches, relays facts, removes impediments, keeps Linear current,
and reports ship status to the operator. It schedules installation and release
windows with the operator. It never approves, accepts, or publishes for
another session's item or decides an operator-reserved choice. The item owner
keeps technical decisions and coordinates dependencies with the RTE.

`Concord` and `Concord-RTE` grant full editing and delegation permissions, with no
profile-level Concord-tool denial. Their shell rules only deny named unsafe
commands; the host's shell rules govern every other command.
`Concord-Planner`'s profile enforces its boundary in configuration: its edit
rules admit only the `.planning` directory; its shell rules allow named read-only and
diagnostic commands, deny commit, push, pull-request, and release commands, and
ask for every other command; its Task rules admit only Concord utilities; and it
is denied every Concord tool that writes work state.
Those capabilities do not transfer another session's workflow authority or the
human operator's approval.
The RTE never answers a Concord approval challenge; it relays it unchanged to
the human operator. Worker-lane and utility capability boundaries remain.
The host owns session procedures, model routing, and machine configuration.
Linear owns planning. Concord adds no session-launch or portfolio API.

The installer refuses a conflicting unmanaged named-profile file.
It preserves the numbered host files under D1.
The host selects the new role names explicitly and owns its launch defaults.

### D3. Machine policy stays outside the profiles and examples

The profiles and examples omit model routing, credentials, and named research providers.
They carry only portable posture, authority, and minimum permission boundaries.
The host supplies machine-specific tool grants and permission overrides.

### D4. Conduct and handoffs stay portable

The shared conduct corpus owns the source-appropriate evidence rule. A technical
unknown requires suitable evidence, and an unavailable or inconclusive lookup
must remain explicit.

Concord shapes and drives its item without a mandatory posture switch or RTE
session. Both primary agents may use subagents for bounded investigation and advice.
Utility results never become managed review or verification evidence.

## Consequences

- Public releases cannot overwrite local coordinator settings.
- Hosts can select portable named roles without surrendering machine policy.
- Named primary profiles use the existing managed-agent installation boundary.
- The conduct corpus remains the single owner of technical evidence behavior.

## Verification

- `python3 scripts/check-primary-prompts.py` validates the named profiles and the
  inert intake example and their portable boundaries.
- `python3 scripts/test-primary-prompts.py` proves the validator rejects broken
  profiles and intake examples.
- `python3 scripts/test-installer.py` proves all numbered primary-agent bytes
  survive install, upgrade, repair, and uninstall while worker agents remain
  managed.
- The release workflow packages named primary profiles, never numbered host files.
