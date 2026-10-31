---
{
  "id": "CD-0031",
  "title": "Core-derived operator session boot",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "operator-surface",
  "applies_to_domain_ids": [
    "agent-adapter",
    "agent-call-boundary",
    "agent-evolution",
    "agent-surface",
    "workflow-engine"
  ]
}
---
# CD-0031: Core-derived operator session boot

**Status:** Accepted under operator approval.
**Approval date:** 2026-08-16.
**Approval:** Operator-approved GitHub issue #100 and implementation direction.
**Related:** C18, CD-0016, CD-0018, CD-0029, CD-0030, issues #57, #99, #100.

## Decision

A host-started operator session receives a bounded, versioned session-boot
packet through work-start preparation. Concord derives the packet from the
canonical CD-0016 continuity projection. The host hands Product/work identity
only; it does not read workflow state or decide what happens next.

Work-start preparation reads one boundary record, renders the
same `ContinuityPayload` returned by `concord_work_trace.continuity` through
the retained packet renderer, and validates the packet before the session
takes a managed action. A derivation or validation failure stops before
managed action. The per-turn hook re-renders the same bytes through
`sessionboot.Build` (CD-0090).

## Contract

`session_boot_packet` is a closed generated payload schema. Every packet binds:

- `session_type=operator` and `session_contract_version=1.0`;
- the current generated agent-tool manifest digest;
- the selected Product and work IDs;
- the canonical continuity payload, including workflow step, approved contract,
  complete `spec_mandate`, latest checkpoint,
  boundary watermark, pending-message count, un-promoted observations, and typed restart availability.

The packet is capped by its own 65,536-byte boot-packet bound, independently of
the agent-envelope bound. The envelope's request and response
budgets are different populations and move independently. Session boot reads
one boundary record, not the full history. The packet is authoritative
at its watermark; consequential work still rereads continuity because CD-0016
requires current state on every call.

A workflow may exist before contract approval. In that state `contract` is
`null`; premise and required end-state appear once the operator approves the
contract. Missing state is represented, never invented.

## Boundaries

The operator session has its own managed identity and context,
while this packet identifies an operator session through its own closed type and
contract version. Neither surface accepts a generic fallback.

Version 1 does not infer successor work from relations. CD-0030 observations are
already part of canonical continuity, so a resumed session sees discoveries without
pretending they are approved work. Promotion remains the separate CD-0018
`raised_from` path. A later packet version may add a bounded relation projection if
operator use shows that observation visibility is insufficient.

C18 remains unchanged in substance:

- the host start carries identity only;
- work-start preparation adds no durable write, workflow action, or second
  state derivation;
- preparation and the per-turn hook use the same typed projection and
  watermark as the agent read;
- a stale prepared packet is harmless because managed action rereads current
  state.

## Consequences

The top-level operator session has a stronger context boundary than prose
instructions: it receives durable intent before its first managed action.
CD-0090 registers the per-turn runtime continuity hook in the adapter plugin entry.
A host-started session with
no selected work stays identity-only and derives no workflow packet.

The OpenCode adapter and core bind the exact current generated manifest digest.
