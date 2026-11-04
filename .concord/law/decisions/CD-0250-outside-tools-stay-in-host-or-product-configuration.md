---
{
  "id": "CD-0250",
  "title": "Outside tools stay in host or Product configuration",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0084"
    },
    {
      "kind": "refines",
      "target_id": "CD-0245"
    }
  ],
  "product_wide_rationale": "The outside-tool boundary binds every child Domain; no single child Domain owns all affected surfaces."
}
---
# CD-0250: Outside tools stay in host or Product configuration

- **Status:** Accepted
- **Date:** 2026-11-03
- **Scope:** Concord's code, store, CLI, contracts, generated profiles, shipped
  skills and helper integrations.
- **Related:** CD-0084 D5, CD-0245 D3, CD-0213, CD-0243, CD-0106

## Context

Agents need separately supplied connected tools without making those tools
Concord mechanisms. The host or Product supplies them, and availability varies
with configuration. The host API and Concord's runtime dependencies are not
outside tools under this decision. This decision closes clarification C8, the
open lgrep and vision lean.

## Decision

### D1. Outside tools reach agents through configuration

An outside tool is a separately supplied connected tool, such as a code-search,
MCP, research or design service. It reaches a Concord agent through host or
Product configuration, for example an `opencode.json` `mcp` entry, Vision, or
a skill in the Product repository. Concord adds no tool-specific code, store
state, CLI verb, contract, generated-profile rule, shipped skill or helper
integration for it.

### D2. Generated profiles and shipped skills

Generated profiles name no specific connected tool, except as D3 permits.
Helpers direct agents to discover connected tools by need. CD-0245 D3 governs
shipped skills.

### D3. Preserved boundaries

- CD-0213 retains Linear MCP planning and the Linear identity on work units;
  profiles may name Linear for that purpose.
- Host-tool permission rules remain, including the `*herdr*` and `ci_watch`
  denials and the read-only `gh` allowances in
  `contracts/agent-primary-profiles.v1.json`. Profiles may name a tool in such
  a rule and where their text states that rule's boundary.
- `.concord/tooling.v1.json` retains Product-declared check commands. Concord
  reads the declaration without integrating those tools; profiles may name
  Product tooling where needed to use the declaration.

## Verification

- Generator schema and grant checks validate utility tool boundaries;
  `scripts/generate-agent-profiles.py --check` verifies generated profile parity.
- `scripts/test-generate-agent-profiles.py` checks that every utility keeps
  `execute` and its discover-by-need guidance.
- `scripts/check-primary-prompts.py` reports known connected-tool name mentions
  as warn-only advisories. The list is incomplete and permitted denials can match.
  `scripts/test-primary-prompts.py` checks the advisory and blocking boundaries.
- Independent review checks D1 and D2 across affected surfaces and verifies D3's
  planning, permission and Product-tooling exceptions. Diagnostics do not enforce
  the normative rule.
