---
{
  "id": "CD-0252",
  "title": "Session boot renders the resume plan and handoff",
  "authority": {"tier": "derived"},
  "home_domain_id": "operator-surface",
  "applies_to_domain_ids": [
    "agent-adapter",
    "agent-call-boundary",
    "agent-contract",
    "agent-surface",
    "workflow-engine"
  ]
}
---
# CD-0252: Session boot renders the resume plan and handoff

## D1. Core-derived preparation

For explicitly selected work, `session-prepare` supplies a bounded, versioned
operator-session boot packet. The host supplies identity and placement, not
workflow state or its derivation. Preparation writes no durable state and
records no workflow action. Without selected work, the host start stays
identity-only and derives no packet.

Concord reads the work's Product identity, resume plan and Project-addressed
handoff frontier at one store revision. Unknown or unpublished work refuses.
The selected Product must belong to the work. Project resolution and handoff
selection preserve [CD-0182 D5](CD-0182-session-opener.md#d5-the-project-selected-session-carries-a-bounded-addressed-handoff).

## D2. Packet contents

The closed generated `session_boot_packet` schema binds its schema version,
operator-session type, session-contract version `2.0`, current agent-tool
manifest digest, Product, Project, work and source watermark.

For work with a workflow instance, the packet carries the canonical resume
document: the stored step plan, derived next steps and work version. Before
type selection it carries the remaining opening steps. An imported work item
without an instance carries explicit `workflow_instance=absent` and
`resume=null`, never a fabricated workflow position. The pending handoff
frontier rides beside the resume document when one exists.

Boot carries neither the continuity snapshot nor a WorkPin, approved contract,
checkpoint, boundary history, work-context projection or restart projection.
Those facts are not copied into a second boot-only workflow view. The public
continuity read and its other consumers retain their own current obligations
until their separate retirement.

## D3. Bounds and current-read references

The complete packet must fit its 65,536-byte bound and validate before a
managed action. When its complete documents do not fit, deterministic
references replace them without truncating their contents. Each reference
names the digest and serialized byte length of its exact document and the
existing canonical read with identity-bound input. The resume reference uses
`concord_work_trace.resume`; the handoff reference uses item-scoped
`concord_work_browse.list` with `detail=full` and selects the item's
`pending_project_handoff` member.

References carry no complete authority. Before use, reconstruct through the
existing authenticated paging, extract the named document, and verify its
digest and byte length. A reference retains no historical snapshot: source
drift, a consumed or replaced handoff, or an unavailable read requires fresh
preparation. No stale reference grants permission to proceed.

## D4. Identity and freshness

Preparation or validation failure stops before managed action. Unsupported
session-contract versions, an incorrect manifest digest, and inconsistent
document or read-pointer identities refuse. A prepared packet is authoritative
only at its captured revision; consequential actions reread the current resume
and item-scoped full record and remain subject to current workflow admission.
Neither host placement nor packet preparation advances the workflow.
