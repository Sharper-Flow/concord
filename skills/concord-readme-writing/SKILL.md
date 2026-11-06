---
name: concord-readme-writing
description: Use when asked to create, rewrite, refresh, or improve a README. Guide audience, evidence, structure, and repository-mode-aware links.
---

# README writing

Write a README that helps its intended readers use and understand the repository.
Every claim and command needs evidence; repository mode controls which Concord
links belong in the page. This method adds no authority or workflow gate.

## Resolve the audience

Establish repository visibility from its hosting service's repository page or
from the operator. A public repository has a public audience; a private or
internal repository has a non-public audience. If visibility cannot be
established, ask the operator before writing. Audience changes presentation,
not the evidence or safety standard.

## Gather evidence

Inventory the existing README and nearby documentation, manifests, source
directories and entrypoints, and scripts for installing, running, testing,
building and verification. Read license, contribution, support, security and
continuous-integration files where present, and consider the operator's context.
Prefer repository source and commands, then maintained documentation, then
operator input. Do not invent commands, compatibility, support channels,
deployment paths, security promises or license terms.

Classify each existing section before changing it:

| Classification | Action |
|---|---|
| Retain | Keep accurate, relevant material supported by evidence. |
| Update | Revise sound intent with evidence where details have gone stale. |
| Remove | Remove unsupported, contradictory, misleading or obsolete material. |
| Ask | Resolve user-authored intent that conflicts with evidence or cannot be safely resolved. |

Preserve useful context and the project's clear voice; do not replace the page
without considering what its current readers need.

## Shape the page

Settle what the reader already knows before the opening. Lead with the job and
its defining constraint in plain prose. Say when to reach for the thing and
how the reader can tell it is working. Introduce each concept before a later
section relies on it.

Include a section only when evidence supports it: title and purpose, what the
project does, prerequisites, install, run and test commands, layout,
configuration, usage examples, and contribution, support, security and license.
Where another maintained document owns a procedure such as installation, link
to it instead of copying its commands; copies drift. Size each section to the
evidence. Cut a block if its removal breaks nothing.

Use direct, scannable headings. Put branches and choices in a table or list,
never a paragraph. For each other block, choose and justify prose, list, table
or code according to what it helps the reader do. Use absolute links when
the README is published beyond the repository; otherwise choose links that work
where the page is read. Verify each destination.

A public README supports external adoption and omits non-public internals. A
non-public README may include verified internal context useful to its readers.
Neither may expose credentials, tokens, private endpoints, customer data or
secret-bearing commands.

## Repository mode

Read mode through `concord_product_view.resolve`; its payload names each
Project's `repository_mode`. Never infer mode from git state.

| Mode | README and repository treatment |
|---|---|
| On | .concord/ and Concord's agent files are committed as ordinary Product files. Address people and agents who do not use Concord. Include a key-links section to the designated knowledge home's product vision at .concord/docs/product-vision.md and its .concord/law/ folder, the architecture reference where one exists, and contribution guidance. Link only destinations the page's readers can reach; where the knowledge home is another repository, link absolutely. Where readers cannot reach a destination, say it exists without linking it, and never name a non-public repository in a public README. |
| Off | Concord's files stay out of git. Name no Concord file, agent file or Concord process in the README. Treat a repository the resolver cannot resolve as off. |

## Verify

Re-read the README from disk before each write and never overwrite edits made
meanwhile. Re-read the finished README and verify every path and command against
repository evidence. Where the Product's .concord/tooling.v1.json declares a
documentation or link check, run it; the check verifies structure and links,
and the re-read verifies meaning. State uncertainty rather than fabricate, and
report omitted sections. This skill never commits or pushes.

## Canonical references

- [Admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md).
- [Repository mode](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0255-per-project-repository-mode.md).
- [Product vision](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0254-product-vision-documents.md).
- [Architecture reference](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0251-architecture-reference-is-not-product-law.md).
