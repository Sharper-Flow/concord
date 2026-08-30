# CD-0031: Core-derived operator session boot

**Status:** Accepted under operator approval.
**Approval date:** 2026-08-16.
**Approval:** Operator-approved GitHub issue #100 and implementation direction.
**Related:** C18, CD-0016, CD-0017, CD-0018, CD-0029, CD-0030, issues #57, #99, #100.

> **Subsequent decision:** CD-0088 registers a per-turn runtime continuity hook in the adapter plugin entry. The "OpenCode host hooks and plugins are unnecessary" clause in §Consequences records the state when CD-0031 was accepted: the boot packet covered session start, and CD-0088 closes the mid-session window the boot packet cannot.

## Decision

A launcher-started operator session receives a bounded, versioned session-boot
packet before OpenCode starts. Concord derives the packet from the canonical
CD-0016 continuity projection. The launcher still hands Product/work identity
only; it does not read workflow state or decide what happens next.

The launcher starts the same Concord binary with its internal `session` command
and the existing identity environment. That child opens the authority database,
calls `ReadWorkflowContinuity`, renders the same `ContinuityPayload` returned by
`concord_work_trace.continuity`, validates the packet, and then starts OpenCode
with the exact JSON as its initial prompt. A derivation or validation failure
stops before OpenCode starts.

## Contract

`session_boot_packet` is a closed generated payload schema. Every packet binds:

- `session_type=operator` and `session_contract_version=1.0`;
- the current generated agent-tool manifest digest;
- the selected Product and work IDs;
- the canonical continuity payload, including workflow step, approved contract,
  complete `spec_mandate`, pending operator decision, latest checkpoint,
  unresolved failure, boundary watermark, pending-message count, un-promoted observations, and typed restart availability.

The packet is capped by the existing 65,536-byte agent-envelope bound. Session
boot reads one boundary record, not the full history. The packet is authoritative
at its watermark; consequential work still rereads continuity because CD-0016
requires current state on every call.

A workflow may exist before contract approval. In that state `contract` is
`null`; premise and required end-state appear once the operator approves the
contract. Missing state is represented, never invented.

## Boundaries

The operator session is not a CD-0017 worker lane. Issue #57 is therefore not a
blocking dependency: worker lane identity governs bounded delegated attempts,
while this packet identifies an operator session through its own closed type and
contract version. Neither surface accepts a generic fallback.

Version 1 does not infer successor work from relations. CD-0030 observations are
already part of canonical continuity, so a resumed session sees discoveries without
pretending they are approved work. Promotion remains the separate CD-0018
`raised_from` path. A later packet version may add a bounded relation projection if
operator use shows that observation visibility is insufficient.

C18 remains unchanged in substance:

- the launcher model and renderer carry identity only;
- launch adds no durable write, workflow action, or second state derivation;
- the core child uses the same typed projection and watermark as the agent read;
- a stale launcher screen is harmless because session boot rereads current state.

## Consequences

The top-level operator session now has a stronger context boundary than prose
instructions: it receives durable intent before its first action. OpenCode host
hooks and plugins are unnecessary. Product-only launches have no selected work,
so they remain identity-only and derive no workflow packet.

The hand-written OpenCode adapter version and range now come from the generated
manifest version. This removes a drift path that otherwise made any post-3.0
adapter reject the core despite shipping the matching generated contract.
