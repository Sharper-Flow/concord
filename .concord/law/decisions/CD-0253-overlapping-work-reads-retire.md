---
{
  "id": "CD-0253",
  "title": "Overlapping work reads retire",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "agent-contract",
  "applies_to_domain_ids": [],
  "law_relations": [
    {"kind": "refines", "target_id": "TS3"},
    {"kind": "refines", "target_id": "PM1"},
    {"kind": "refines", "target_id": "CD-0005"},
    {"kind": "refines", "target_id": "CD-0113"},
    {"kind": "refines", "target_id": "CD-0224"}
  ]
}
---
# CD-0253: Overlapping work reads retire

- **Status:** Accepted
- **Date:** 2026-11-04
- **Scope:** Overlapping work reads and their projections
- **Related:** CD-0005, CD-0113, CD-0224, CD-0243

## Context

Several reads overlap with the work resume, list, and relation views. Their
separate projections make a caller choose between ways to inspect the same work.

## Decision

The agent surface retires `concord_work_browse.ready`, `blocked`, and `scope`.
No operation replaces them and no compatibility path remains. The list read
retains `lifecycle` and the per-item `ready` value. A needed item is ready
exactly when no live blocker holds it. A needed item with `ready` absent or
false is blocked; the relations read returns the bounded blocker graph.
Item-scoped list with full detail supplies the work facts needed by version
and membership refresh intents.

List full detail no longer embeds a WorkPin. Mutation results retain their pins
and derived intents under CD-0113 D1–D3. The continuity read is unchanged.
History no longer carries the `workflow` sub-projection; resume and item-scoped
list full detail carry current step state and per-item facts.
The history read's `contract_version` selector retires with that sub-projection.

The Product snapshot and work list retain their derived readiness behavior. The
relation vocabulary and the effect of `blocks` on readiness remain unchanged;
the list exposes that readiness.

## Verification

The generated contracts and affected read and mutation tests establish the
remaining surface and its derived behavior. Repository checks validate the
law and its references.
