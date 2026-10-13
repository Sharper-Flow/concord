# CD-0154: Numbered primary agents remain host-owned

- **Status:** Accepted
- **Date:** 2026-09-16
- **Scope:** Ownership and distribution of the `concord-0`, `concord-1`, and
  `concord-2` OpenCode definitions, and portable named primary profiles
- **Approval:** The operator approved host ownership with repository examples.
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

Portable examples live under `examples/opencode/agents/`. This directory is not
an OpenCode discovery path and is not an installer or release-package input.

A host can copy and adapt an example manually. Concord does not track or adopt
the resulting host file.

Concord generates and installs two named primary profiles from
`contracts/agent-primary-profiles.v1.json` and its instruction sources.
`concord-coordinator` supervises live sessions across authorized Projects.
`concord-operator` drives one work item in one repository.

The central coordinator holds no candidate-authoring or workflow-write authority.
It delegates local execution to repository-local operators through the host.
The host owns terminal placement and session lifecycle. Linear owns planning records.
Concord adds no session-launch or portfolio API for these profiles.
The central coordinator never answers a Concord approval challenge.
It relays each such challenge unchanged to the human operator. Delegated host
questions and permissions remain subject to the host's exact scope grant.
The adapter refuses each workflow-write tool that the step agent's profile
denies. It reads the agent the host supplies with each tool call, so a
session permission granted under another agent does not reach the coordinator.

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

The shaping example recommends the driving posture once when an approved
executable contract leaves implementation or verification work. The driving
example recommends the shaping posture once only for substantial
reconsideration.

Both recommendations are advisory. A declined recommendation does not repeat,
and no otherwise permitted work pauses.

The local operator shapes and drives its item without a mandatory posture switch.
The central coordinator uses generated read-only utilities for bounded advice.
Utility results never become managed review or verification evidence.

## Consequences

- Public releases cannot overwrite local coordinator settings.
- Hosts can select portable named roles without surrendering machine policy.
- Named primary profiles use the existing managed-agent installation boundary.
- The conduct corpus remains the single owner of technical evidence behavior.

## Verification

- `python3 scripts/check-primary-prompts.py` validates the named profiles and the inert examples and
  their portable boundaries.
- `python3 scripts/test-primary-prompts.py` proves the validator rejects broken
  examples.
- `python3 scripts/test-installer.py` proves all numbered primary-agent bytes
  survive install, upgrade, repair, and uninstall while worker agents remain
  managed.
- The release workflow packages named primary profiles, never numbered host files.
