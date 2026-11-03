---
name: concord-improve-codebase-architecture
description: Scan a codebase for shallow modules and structural friction, rank improvement candidates, and recommend where to start.
---

# Improve codebase architecture

This method is guidance under [CD-0246 D4](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0246-concord-ships-portable-engineering-methods.md#d4-a-method-is-guidance-not-a-gate).
It adds no authority, workflow step, approval or check, and authorizes no source
or live-state change. Accepted law and assigned scope govern the work.

## Scope and context

Take the direction the operator names: an area, module, or pain point. Otherwise,
rank areas by recent Git history and start with recurring change hotspots. If
history has no clear hotspot, widen the exploration.

Identify the Product's relevant Domains with concord_domain.list and read their
purpose, documents and current law paths with concord_domain.detail. Read the
relevant Domain documents and committed law in the Product's designated law
folder. Resolve acceptance through the workflow's resolved law context, not a
filename or branch text. If the context is unavailable, report the gap rather
than assume law is absent.

## Explore and test

Explore the selected area in a fresh context where the host offers one. Follow
callers and dependencies to find friction: scattered knowledge, shallow modules,
leaking seams, and behavior hard to test through its interface. Ground each
finding in the code and its callers, not in a pattern alone.

Load `concord-codebase-design` and use its vocabulary and tests. Apply the
deletion test first: if deleting a module makes its complexity vanish, propose
removal rather than wrapping it; if complexity moves into callers, it earns its
place. Then consider depth, locality, leverage, seam placement, and the interface
as the test surface. Do not propose a new seam merely to add indirection.

## Report and choice

Return a ranked candidate list as a planning record, such as a research note or
ticket draft, with one top recommendation and its reason. For each candidate
state:

- The observed problem, with paths and callers, and the affected party.
- A proposed end state that a check or reviewer can decide.
- Strength: **Strong**, **Worth exploring**, or **Speculative**, with the
  evidence and uncertainty behind that rating.
- Any accepted law it conflicts with and why the observed friction warrants
  reopening that law; otherwise state that no conflict was found. Do not treat
  a proposal as permission to change law.

The operator chooses which candidate, if any, becomes work. Do not create a
ticket automatically. Once a candidate is chosen, use the Compare designs section
of `concord-codebase-design` for its interface choice under the work's existing
scope and authority.
