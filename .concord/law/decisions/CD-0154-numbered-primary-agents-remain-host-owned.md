# CD-0154: Numbered primary agents remain host-owned

- **Status:** Accepted
- **Date:** 2026-09-16
- **Scope:** Ownership and distribution of the `concord-0`, `concord-1`, and
  `concord-2` OpenCode definitions, portable named primary profiles and native
  step helpers
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
- **Profile installation:** Existing workflow pins and stored contracts keep
  their meaning; the managed profile name changes
  through the installer's existing retirement transaction.
- **P12:** Serious: the agent name users select, its permission boundary, and
  which choices reach the operator change.
- **Related:** CD-0043, CD-0063, CD-0242, and CD-0111

## Context

The three numbered coordinator definitions combine Concord authority with local
machine policy. The local policy includes model routing, tool providers,
credentials, and permission overrides. A public release cannot own that
machine-specific state.

The numbered coordinator definitions are operator entry points and remain host
configuration. Named portable profiles are release assets.

## Decision

### D1. The host owns numbered primary agents

Concord does not install, update, migrate, remove, repair, or restore files named
`concord-0.md`, `concord-1.md`, or `concord-2.md`. The installer manifest and
release archive manage only the named primary profiles, native step helpers and
utilities listed in `AGENT_FILES`.

Install, upgrade, repair, and uninstall operations leave existing numbered
primary-agent bytes unchanged.

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
The RTE never answers a Concord operator question on the work's behalf; it
relays it unchanged to the human operator. Utility capability boundaries remain.
The host owns session procedures, model routing, and machine configuration.
Linear owns planning. Concord adds no session-launch or portfolio API.

Concord also generates and installs `concord-review` as a native Task step helper
from the profile manifest. It loads the shipped review method, reviews the
assigned step and repairs its in-scope findings. It leaves changes uncommitted;
the caller commits and owns delivery and workflow state. There is no read-only
review mode. It may read Concord law and work context, but must not write Concord
state, publish, contact the operator or send session notices. It may delegate
only to exploration and external lookup utilities. Host safety and assigned-path
permissions remain in force; model selection remains host-owned.

`concord-verify` uses the same generated native Task distribution boundary. It
runs the caller's named test-only steps or local suite under repository test
admission and the caller's explicit budget, or diagnoses an already-completed
named CI run. It reports commands, exits and failure output; CI findings include
job URLs and assertions. A known-flake classification requires matching evidence
and a linked record; missing evidence remains unknown.

The verifier must not edit files, repair failures, wait on CI, write planning or
Concord state, commit, publish or contact the operator. The caller owns waits,
evidence binding, verdicts and delivery. The verifier may read Concord law and
work context and delegate only to exploration and external lookup utilities.
Its profile denies editing tools, CI watch and workflow writers, and declares
no bash wildcard rule that could replace inherited host safety denies. Command-
text restrictions are conduct controls, not a sandbox; connected tool exposure
and model selection remain host-owned. Its result alone is not captured native
verification proof. Utility boundaries remain unchanged.

`concord-implement` uses the same generated native Task distribution boundary
for one bounded Change, Fix or Removal in the caller's worktree. Its assignment
names the paths or subsystem, outcome and check. Missing inputs or a needed
change outside the assignment return needs-decision rather than an expanded scope.
For a Fix, it adds a regression test first and proves that it fails before the
fix for the reported behavior and passes after. It leaves changes uncommitted
and reports exact changed paths, commands, exits and gaps; the caller commits
and owns workflow state, evidence binding and delivery.

The implementer may read Concord law and work context and delegate only to
exploration and external lookup utilities. It must not write planning or
Concord state, publish, contact the operator or send session notices. Its profile
preserves inherited host safety denies without a bash wildcard rule and denies
CLI write bypasses. The command-text rules are not a sandbox; the same boundary
applies to direct-store, wrapper and connected-service access. Model selection
remains host-owned, and utility nesting restrictions remain unchanged.

`concord-research` uses the same generated native Task distribution boundary to
investigate a framed question across repository and external sources. It accepts
the caller's known context, scope and budget, resolves Product-law state through
`concord_knowledge` before law-dependent delegation, and delegates only to
`concord-explore` and `concord-lookup`. It chooses follow-up questions from findings
and stops at sufficient evidence or the assigned budget. It returns claim-level
citations, conflicting evidence, uncertainty, gaps and caller decisions; the
driving session records the conclusion and owns workflow state and delivery.

The researcher must not edit files, write planning or Concord state, commit,
publish, contact the operator or send session notices. It may run bounded
read-only diagnostics. Its profile keeps Concord reads, denies editing tools and
workflow writers, preserves inherited host safety denies without a bash wildcard
rule, and denies CLI write bypasses. Command-text restrictions are hygiene, not a
sandbox; direct-store, wrapper and connected-service access grant no bypass.
Model selection remains host-owned. Exploration and lookup remain single-question
utilities with their existing no-delegation boundaries.

The installer refuses a conflicting unmanaged named-profile file.
It preserves the numbered host files under D1.
The host selects the role names explicitly and owns its launch defaults.

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
  survive install, upgrade, repair, and uninstall while named profiles remain
  managed.
- The release workflow packages named primary profiles, never numbered host files.
- `scripts/test-generate-agent-profiles.py` checks verifier tool and command
  permissions, inherited safety denies and test-only instructions.
- The same profile suite checks the implementer's bounded assignment, Fix
  regression order, uncommitted results and effective permission rules.
- It also checks the researcher's investigation loop, read-only permissions,
  two-delegate boundary, CLI hygiene and inherited host safety denies.
- `adapter/opencode/session-scope.test.ts` checks native helper Task pass-through
  without dispatch; `adapter/opencode/primary-role.test.ts` checks read access
  and write refusal before transport.
