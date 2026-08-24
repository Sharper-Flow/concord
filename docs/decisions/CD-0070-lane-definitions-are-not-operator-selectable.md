# CD-0070: Lane definitions are hidden from the operator's agent cycle

- **Status:** Accepted
- **Date:** 2026-08-24
- **Scope:** Generated lane agent frontmatter; the boundary between a worker
  identity and an operator session; issue #466
- **Approval:** Operator accepted the fix direction as filed in
  [issue #466](https://github.com/Sharper-Flow/concord/issues/466) on 2026-08-24
- **Related:** CD-0017 (Invariant 4, worker authority boundary), CD-0064 (D1
  emits `mode: all`), CD-0043 (host-owned lane methodology)
- **Preserves:** CD-0064 D1, D2, and D3 in full; the closed lane registry and its
  digests; `agent-lane-packet.v1` and `agent-lane-report.v1`
- **Supersedes:** nothing

## Context

CD-0064 D1 emits `mode: all` for every generated lane agent, because run mode
refuses a subagent-mode target. That premise still holds. Measured against
opencode 1.18.22, `opencode run --agent` answers a `mode: subagent` name with a
stderr warning, substitutes the default agent, and exits zero.

`mode: all` is both subagent-capable and primary-capable. The host cycles every
primary-capable agent through one keybind, so the four lanes became selectable
as the operator's session agent in any project carrying the generated
definitions.

A lane selected that way holds no packet, no attempt window, no bound
host-instruction provenance, and no evidence obligations, while its body directs
it to return a lane report for a packet that does not exist.

The exposure is not the confusing session. CD-0017 Invariant 4 states that
worker runs never record workflow step transitions, verdicts, or completion.
That invariant governs what a worker does. Nothing governed who may become one,
so the separation rested on the operator not pressing a key.

### The host mechanism was present and was misread

The host builds its cycle list from `agent.mode !== "subagent" && !agent.hidden`.
The `hidden` term is evaluated for every mode, and it is the only mechanism that
excludes the host's own `compaction`, `title`, and `summary` agents, each of
which is declared primary.

The published host documentation states that `hidden` applies only to subagent
mode. That statement is wrong for the cycle, and it is why the property read as
unreachable on first inspection. The inaccuracy is reported upstream. Concord
does not depend on that report, because the shipped behavior already supports
the exclusion.

## Decision

### D1. A generated lane definition declares `hidden: true`

The generator emits `hidden: true` beside `mode: all`. The property becomes
structural in the generated artifact rather than a convention an operator
observes, and it regenerates with every lane.

### D2. Run-mode selectability is unchanged

`mode: all` stays. CD-0064 D1 is preserved, not amended. `hidden` governs
operator-facing listing only, so the dispatch transport resolves the lane exactly
as before and the CD-0064 D2 executor readback continues to assert identity.

### D3. The lane registry digest does not move

The registry digest covers the manifest. A lane definition body is a projection
of that manifest and contributes no bytes to the digest, so this change alters
no digest and no schema version.

## Invariants

1. A generated lane definition is not selectable as an operator session agent.
2. A generated lane definition stays resolvable by run mode and spawnable
   through the host task surface.
3. Lane frontmatter continues to deny task dispatch.

## Consequences

- An operator cycling primary agents in a Concord project reaches orchestrator
  identities only.
- The property depends on host behavior Concord does not own. A host that stops
  honoring `hidden` for primary-capable agents reopens the exposure, and the
  repository check below detects only frontmatter drift, never a host change.
- Lane definitions leave the host mention menu as well as the cycle. No Concord
  path uses that menu, and typed dispatch is unaffected.
- The generated body changes, so every consuming project regenerates its lane
  definitions to acquire the property.

## Verification

- The generator emits `hidden: true` for every lane, and
  `scripts/generate-agent-lanes.py --check` fails when a definition on disk
  omits it.
- Applying the host cycle predicate to the resolved host configuration in a
  project carrying the generated definitions returns no lane.
- `opencode run --agent concord-<lane>` resolves that lane, with no fallback
  warning and no executor substitution.
- A hidden primary-capable agent stays spawnable through the host task surface.
- `contracts/agent-lanes.digest` is byte-identical before and after the change.
