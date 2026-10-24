# CD-0070: Lane definitions are hidden from the operator's agent cycle

- **Status:** Accepted
- **Date:** 2026-08-24
- **Scope:** Generated lane agent frontmatter; the boundary between a worker
  identity and an operator session; issue #466
- **Approval:** Operator accepted the fix direction as filed in
  [issue #466](https://github.com/Sharper-Flow/concord/issues/466) on 2026-08-24
- **Related:** CD-0017 (Invariant 4, worker authority boundary), CD-0064 (D1
  emits `mode: subagent`), CD-0043 (host-owned lane methodology)
- **Preserves:** CD-0064 D2 and D3 in full; the closed lane registry and its
  digests; `agent-lane-packet.v1` and `agent-lane-report.v1`
- **P12:** Serious: generated lanes are hidden, subagent-only native Task
  targets, not primary-capable agents.

## Context

An operator session selected as a worker lane holds no packet, no attempt window, no bound
host-instruction provenance, and no evidence obligations, while its body directs
it to return a lane report for a packet that does not exist.

CD-0017 Invariant 4 states that
worker runs never record workflow step transitions, verdicts, or completion.
That invariant governs worker actions. CD-0064 D1's subagent mode separates the
entry path; hiding the definition also removes it from ordinary discovery.
Neither property replaces the native dispatch authorization window.

## Decision

### D1. A generated lane definition declares `hidden: true`

The generator emits `hidden: true` beside `mode: subagent`. The property is
structural in the generated artifact rather than a convention an operator
observes, and it regenerates with every lane.

### D2. Hidden lanes remain native Task targets, not primary agents

CD-0064 D1 supplies `mode: subagent` for every execution lane. The native Task
route resolves the exact hidden lane name, and CD-0064 D2's executor readback
continues to assert identity. Hiding a definition is not a dispatch permission;
the typed packet, authorized attempt, nesting denial, and capability fences
remain required under CD-0178 D1.

### D3. The lane registry digest does not move

The registry digest covers the canonical manifest. A lane definition body is
a projection of that manifest and contributes no bytes to the digest.

## Invariants

1. A generated lane definition is not selectable as an operator session agent.
2. A generated lane definition is subagent-only and remains spawnable through
   the authorized native Task surface by its exact name.
3. Lane frontmatter continues to deny task dispatch.

## Consequences

- Execution lanes do not appear as selectable primary session agents.
- Frontmatter validation detects definition drift. The native host check
  separately verifies that hidden subagent names still resolve and execute.
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
- `adapter/opencode/evals/native-lane.native.case.ts` completes each of the five
  hidden subagent identities through the native Task executor on OpenCode
  1.18.35, with exact child assistant-agent readback and a fixture model.
- `contracts/agent-lanes.digest` derives from the canonical manifest. Generated
  lane frontmatter is validated against its generator.
