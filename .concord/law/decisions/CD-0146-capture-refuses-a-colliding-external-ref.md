---
{
  "id": "CD-0146",
  "title": "One live work item per Linear issue",
  "authority": {"tier": "derived"},
  "home_domain_id": "durable-authority",
  "applies_to_domain_ids": ["work-coordination", "agent-contract"]
}
---
# CD-0146: One live work item per Linear issue

## Decision

`linear_issue_links` is the sole carrier of Linear issue identity. Capture takes
the human key, remote issue UUID, and URL and writes the link and work item in
one transaction. Attaching a link to existing work follows the same rule.

At most one live work item may hold a given Linear key or remote UUID. Capture
and link attachment refuse a collision without effects. A `raised_from` relation
does not bypass this refusal. Terminal links remain recorded and do not prevent
capture of a new item. Reopening a terminal item refuses if another live item
holds its key or UUID.

A collision refusal from capture, link attachment, or reopen names the live
holder's work ID only when the caller's grant could read that holder: the grant
holds `product_read` and its authorized Product scope includes a Product of the
holder. Otherwise the refusal leaves the holder unnamed, because naming it
would disclose work the caller cannot read.
