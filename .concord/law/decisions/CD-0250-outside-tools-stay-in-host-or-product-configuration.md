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
  "product_wide_rationale": "The outside-tool boundary binds every child Domain at once: outside tools reach agents through host or Product configuration, and no Domain may admit a tool-specific mechanism inside its own scope. No single child Domain owns the boundary, because each could otherwise host an integration the others must refuse."
}
---
# CD-0250: Outside tools stay in host or Product configuration

- **Status:** Accepted
- **Date:** 2026-11-03
- **Scope:** Every surface Concord ships or generates: Go code and store state,
  CLI verbs, agent contracts, generated agent profiles, shipped skills, and
  helper integrations. It governs where an outside tool may enter a Concord
  agent's reach, not what an agent does with a tool that host or Product
  configuration already supplies.
- **Approval:** The operator set the direction ("We want Concord agents to be
  able to use things liek that, but it should not enter Concord as mechanisms
  in any way", POKE-58, quoted on CON-1618) and approved filing it as Concord
  law. Law wording alone needs no further approval; the retirement of
  Concord's own proposal doc and tests of retired behavior is covered by
  CD-0243 D3.
- **Related:** CD-0084 (D5 host tool availability), CD-0043, CD-0245 (D3
  shipped-skill portability), CD-0213
- **Preserves:** CD-0213 planning authority; the host-tool safety denials in
  the primary profile contracts; the Product tooling declaration

## Context

Outside tools — code search, MCP hosts, research providers, design tools,
anything whose publisher is not Concord — already reach Concord agents. The
host and the Product configure them: an `opencode.json` `mcp` entry, a
Vision-managed server, a skill in the Product repository, a host-installed
profile. Installed helper profiles allow `execute`, so Code Mode reaches
those tools without Concord knowing their names.

Two things were missing. No law forbade Concord itself adding a
tool-specific integration: CD-0084 D5 says the host owns tool availability
and Concord does not gate host tools, but nothing stopped a Concord agent
from building one as a CLI verb or a store table. And
`.concord/docs/vertical-integration.md` still proposed the opposite — that
Concord might OWN, SWALLOW, or product-scope lgrep and vision. Generated
helper profiles also named lgrep, Context7 and Exa, the kind of name a
shipped portable surface must not carry (CD-0245 D3 holds shipped skills to
this; generated profiles had no matching rule).

The operator's direction closes the question: agents may use such tools,
but they never enter Concord as mechanisms.

## Decision

### D1. Outside tools reach Concord agents only through host or Product configuration

An outside tool becomes reachable for a Concord agent only through host or
Product configuration: for example a Product `opencode.json` `mcp` entry,
Vision, or a skill in the Product repository. Concord adds no tool-specific
code, store state, CLI verb, contract, generated-profile rule, shipped
skill, or helper integration for an outside tool. A change that proposes one
is outside this boundary until the operator decides to change it.

"Outside tool" means any tool whose publisher is not Concord: code search,
MCP hosts and servers, research providers, design and planning tools. Host
tools the pinned host itself defines (edit, bash, task, execute) are not
outside tools; they are the host surface CD-0084 already binds.

### D2. Generated profiles and shipped skills name no connected tool

A generated agent profile, like a shipped skill under CD-0245 D3, names no
specific connected tool. Generated helpers tell agents to discover connected
tools by need — namespace discovery through `execute` — because which tools
are connected depends on host and Product configuration, not on Concord.
`scripts/check-primary-prompts.py` holds this: a generated profile that
names a connected tool is a finding.

### D3. What this decision does not touch

Three existing behaviors name or depend on a specific outside tool and stay
unchanged:

- CD-0213: planning and business grouping run through the Linear MCP server,
  and Concord records the Linear identity on work units. That is a planning
  authority record, not a tool integration.
- Safety denials in `contracts/agent-primary-profiles.v1.json` that name host
  tools (`*herdr*`, `ci_watch`). Denying a named tool strengthens a
  protection; this decision retires no denial.
- `.concord/tooling.v1.json`: Product-declared check commands that Concord
  reads. The Product declares its own tooling; Concord does not integrate
  the tools.

An outside-tool integration may still be proposed. This decision is the rule
it is measured against, and changing the rule is an operator decision.

## Consequences

- `.concord/docs/vertical-integration.md` is deleted; its question — whether
  Concord should own, swallow, or product-scope lgrep and vision — is
  answered never by D1. The docs index, the Product data-model relationship
  table, and clarifications.md C8 record the closure.
- The generated explore and lookup helper profiles no longer name lgrep,
  Context7 or Exa; agents discover connected tools by need.
- A future generated profile, contract, verb, store table, shipped skill, or
  helper that carries a tool-specific integration violates D1 even if it
  works; review rejects it citing this record.

## Rejected alternatives

**Owning, swallowing, or product-scoping lgrep and vision** — the retired
`vertical-integration.md` lean. Rejected by the operator's direction: agents
use such tools through configuration; the tools never enter Concord as
mechanisms.

**Naming connected tools in generated helper guidance.** Rejected because a
generated profile that names a host-configured tool stops being portable the
moment that tool is absent — the same failure CD-0245 D3 prevents for
skills.

**A per-Domain exception path.** Rejected because the boundary is one rule
for every Domain; a Domain-local exception would re-open exactly the growth
the direction closes.
