# Skills

[CD-0245](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md)
D1 admits a skill when it operates a structure that accepted Concord law defines,
and Concord's checks verify the result. CD-0246 D3 also admits a skill that
carries a portable engineering method: one that names no host tool, machine,
model, or private source.

## Skills that operate Concord structure

Load [Law writing](concord-law-writing/SKILL.md) to place or author Product
knowledge, or draft, amend, or prune Product law. It guides placement and meaning review;
the Product's existing authority and checks still own acceptance and publication.

Load [Architecture writing](concord-architecture-writing/SKILL.md) to write or
review a Product's durable architecture reference, glossary, or cross-cutting
concepts, to plan an architectural change, or to route a plan's lasting outputs
to law, reference, or glossary at final review.

Load [Ticket frontend](concord-ticket-frontend/SKILL.md) when work is driven
through the ticket frontend and an operator question or resume arises.

Load [Multi-repository](concord-multi-repository/SKILL.md) when a work item
must edit a second Project in another Git repository.

Load [Self-repair](concord-self-repair/SKILL.md) when a Concord defect blocks
safe workflow recording or execution of your own managed item.

## Portable engineering methods

- [Instruction writing](concord-instruction-writing/SKILL.md): write, edit, or
  prune agent profiles, shared instructions, skills and definition manifests.
- [Diagnosis](concord-diagnosis/SKILL.md): a defect or regression whose cause is
  not yet proven.
- [Codebase design](concord-codebase-design/SKILL.md): an interface, seam, or
  abstraction that must earn its place, or a repository check's authority.
- [Improve codebase architecture](concord-improve-codebase-architecture/SKILL.md):
  find and rank shallow modules and structural friction across a codebase.
- [Work shaping](concord-work-shaping/SKILL.md): triage a request and decompose
  it into independently verifiable outcomes.
- [Retrospective](concord-retrospective/SKILL.md): move a recurring correction
  to the instruction, check, or document that can hold it; publish a Product lesson.
- [Review](concord-review/SKILL.md): review a step or change against repository
  standards and the ticket's end state or plan, including a final clean review.

A method is guidance, not a gate. It adds no workflow step, approval, packet
field, capability, or check, and it authorizes no source or live-state change.

## Provenance

These skills adapt methods from
[Matt Pocock's skills](https://github.com/mattpocock/skills/commit/5beb35b040e3de97e54fa9e386d7e42d15c295c2):
writing-for-agents for instruction writing and law writing; diagnosing-bugs, codebase-design, triage,
to-tickets, and retro for the method skills. Each adapting skill keeps the
upstream MIT notice in its `LICENSE` file.

Review adapts the two-axis method from
[code-review](https://github.com/mattpocock/skills/blob/a496f5513d4a4cf889bc48e189e579f4b0bdf6aa/skills/engineering/code-review/SKILL.md)
and keeps its upstream MIT notice in `concord-review/LICENSE`.

Improve codebase architecture adapts
[improve-codebase-architecture](https://github.com/mattpocock/skills/blob/a496f5513d4a4cf889bc48e189e579f4b0bdf6aa/skills/engineering/improve-codebase-architecture/SKILL.md)
and keeps its upstream MIT notice in `concord-improve-codebase-architecture/LICENSE`.

Architecture writing adapts the glossary format, the ADR selection test, the
to-spec plan additions, and the wayfinder decision-map idea from
[mattpocock/skills](https://github.com/mattpocock/skills/commit/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d)
and keeps its upstream MIT notice in `concord-architecture-writing/LICENSE`.

## Delivery

A shipped skill lives here, installs under the versioned data root through the
[CD-0063 D2 route](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0063-shipped-operator-conduct-rules.md#d2-the-installation-is-central-a-project-holds-pointers),
and changes only with a release. No separate skill-versioning regime exists.
The installer registers this directory in the host configuration, so every
session on the machine can load a skill on demand. A skill calls only Concord
operations and the commands the Product declares in `.concord/tooling.v1.json`,
and names no machine, private repository, or host tool.

Host sessions and native subagents load methods from the shipped skill path.
Procedure that depends on a host's tools, models, or configuration stays
host-owned and reaches a worker through the
[CD-0043 D1](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0043-host-owned-lane-methodology.md)
host instruction surface.

Admission does not establish implementation or conformance. A shipped skill
never substitutes for a check and holds no independent evidence, approval, or
law authority.
