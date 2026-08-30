## Problem

Concord's CD-0016 fixes a law: a summary "cannot carry authority, approval, law,
workflow position, or evidence claims." Concord satisfies that law at two moments
and misses a third.

| Moment | Mechanism | State |
|---|---|---|
| Session boot | CD-0031 `session_boot_packet`, derived from the CD-0016 projection, validated, injected as initial prompt | Covered |
| Any `concord_*` call | CD-0016 pinned projection re-derived per call | Covered |
| Mid-session with no Concord call | none | **Uncovered** |

The uncovered window is reached in practice. Host compaction fires mid-session
during stretches of file editing, shell work, and reads. Once it does, the boot
packet sits behind the compaction boundary and no call has re-derived the
projection. From that turn forward the orchestrator's workflow position is carried
by the summary alone — the precise condition CD-0016 forbids. The host runs
automatic compaction, so this is a reached state, not a hypothetical one.

Concord already owns the surface that would close it. `scripts/install.py` ships a
plugin entry module and registers it in the host `plugin` array as the adapter's
plugin factory. The adapter uses one hook key, `tool`. The remaining lifecycle
hooks are available at no new surface cost.

A second, related absence sits on the same mechanism. CD-0034 and CD-0043 give a
lane worker its methodology through `CONCORD_HOST_INSTRUCTIONS`, bound by content
hash into the dispatch provenance manifest. The orchestrator session has no
equivalent: nothing derives its instruction from durable workflow position. The
predecessor met this need with agent-selected command contracts, which lets the
agent choose the wrong contract. Concord holds the workflow step in durable state
and could derive it instead.

## Why this matters

- It is an unenforced internal invariant, not a missing feature. Concord's
  continuity design is stronger than the predecessor's; it has no trigger during
  the window that needs it most.
- The payload already exists. `concord_work_trace.continuity` returns
  `ContinuitySnapshot` with work identity, workflow step, approved contract, the
  full `spec_mandate` set, pending operator decision, latest checkpoint,
  unresolved failure, and `PendingMessages` peer signal.
- Closing it also delivers the peer-session awareness that currently requires the
  agent to choose to look.

## Constraints

1. The adapter never owns validation, authorization, or approval. This is a
   runtime surface that re-asserts state the core derives; it decides nothing.
2. CD-0016 holds: summaries stay advisory and never carry authority.
3. Prompt-cache safety is a hard design constraint. Content that varies per turn
   must not be placed where it invalidates the cached prompt prefix — a rewritten
   prefix is re-billed as cache creation on every call. Placement must be chosen
   against measured cache behavior, not assumed.
4. If derived instruction is injected, CD-0043 D2's provenance discipline
   (enumerated path, kind, content hash) is the governing precedent.

## Open scope question for design

Two candidate boundaries, materially different in blast radius:

- **A — re-pin only.** Adapter-side wiring. The payload is complete today; no core
  change. Closes the CD-0016 window.
- **B — re-pin plus derived orchestrator instruction.** Requires joining the
  workflow definition's `available_actions` for the current step into the
  continuity read, which changes a generated core contract and its surface
  version. Closes the window and replaces agent-selected command contracts with
  state-derived instruction.

Design must settle this boundary before tasks are synthesized.
