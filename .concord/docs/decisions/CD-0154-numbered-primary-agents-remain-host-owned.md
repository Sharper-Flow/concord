# CD-0154: Numbered primary agents remain host-owned

- **Status:** Accepted
- **Date:** 2026-09-16
- **Scope:** Ownership and distribution of the `concord-0`, `concord-1`, and
  `concord-2` OpenCode definitions
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
release archive manage only the worker definitions in `AGENT_FILES`.

Install, upgrade, repair, and uninstall operations leave existing numbered
primary-agent bytes unchanged. These operations continue to install, update,
repair, and remove worker definitions.

### D2. Concord provides inert examples

Portable examples live under `examples/opencode/agents/`. This directory is not
an OpenCode discovery path and is not an installer or release-package input.

A host can copy and adapt an example manually. Concord does not track or adopt
the resulting host file.

### D3. Machine policy stays outside the examples

The examples omit model routing, credentials, and named research providers.
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

## Consequences

- Public releases cannot overwrite local coordinator settings.
- Hosts can inspect and adapt current coordinator examples without accepting a
  migration contract.
- Worker-agent installation remains unchanged.
- The conduct corpus remains the single owner of technical evidence behavior.

## Verification

- `python3 scripts/check-primary-prompts.py` validates the inert examples and
  their portable boundaries.
- `python3 scripts/test-primary-prompts.py` proves the validator rejects broken
  examples.
- `python3 scripts/test-installer.py` proves all numbered primary-agent bytes
  survive install, upgrade, repair, and uninstall while worker agents remain
  managed.
- The release workflow has no numbered primary-agent packaging step.
