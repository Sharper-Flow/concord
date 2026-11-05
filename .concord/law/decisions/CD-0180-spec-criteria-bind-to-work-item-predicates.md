---
{
  "id": "CD-0180",
  "title": "Spec criteria bind to scenarios or exemptions",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": []
}
---
# CD-0180: Spec criteria bind to scenarios or exemptions

## Decision

### D1. Specification criterion bindings

A specification's `criterion_bindings` metadata binds a criterion to a
scenario ID or a recorded exemption reason. Authoring validates the binding's
closed shape. No repository check claims to decide whether the criterion is
discharged by a work item.

### D2. Law context

The law context carries each law's authored bindings from its metadata. The
contract names law by ID without freshness pins.
