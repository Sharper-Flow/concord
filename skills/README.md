# Skills

[CD-0245](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md)
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

Load [README writing](concord-readme-writing/SKILL.md) to create or improve a
repository README, with audience resolution and repository-mode-aware key links.

Load [Adoption](concord-adoption/SKILL.md) to adopt Concord into an existing
repository: inventory its documents, classify each as binding rule, reference or
history, route them through the skills above, bootstrap the Domain registry,
co-author the product vision with the operator, and confirm repository mode.

Load [Ticket frontend](concord-ticket-frontend/SKILL.md) when work is driven
through the ticket frontend and an operator question or resume arises.

Load [Ticket writing](concord-ticket-writing/SKILL.md) to write a Linear issue,
a Linear comment or a captured work item.

Load [Project updates](concord-project-updates/SKILL.md) to write or edit a
Linear project description or status update.

Load [PR writing](concord-pr-writing/SKILL.md) to write a PR body or completion
report.

Load [Research writing](concord-research-writing/SKILL.md) to write research
findings or a research note.

Load [Handoff writing](concord-handoff-writing/SKILL.md) to write an assignment
brief, handoff, or subagent prompt.

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
- [UI design](concord-ui-design/SKILL.md): visual UI or UX work — match the
  Product's existing design system, hold an accessibility floor with WCAG
  criteria, preview with an exact version before implementation, and verify in
  a browser with screenshots.
- [Test design](concord-test-design/SKILL.md): choose what a change's tests must
  prove and at which seam.
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

README writing adapts .agents/writing-docs.md and skills/in-progress/writing-shape
from [mattpocock/skills](https://github.com/mattpocock/skills/commit/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d)
and keeps its upstream MIT notice in `concord-readme-writing/LICENSE`.

UI design adapts the design point of view, the preview artifact and the
browser verification method from
[anthropics/skills](https://github.com/anthropics/skills/tree/2c5958e)
(frontend-design, web-artifacts-builder, webapp-testing; Apache-2.0 notice
kept), the checkable interface rules from
[vercel-labs/web-interface-guidelines](https://github.com/vercel-labs/web-interface-guidelines/blob/434b7f9/command.md)
(MIT notice kept), and the throwaway preview discipline from
[mattpocock/skills](https://github.com/mattpocock/skills/commit/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d)
(engineering/prototype; MIT notice kept). All notices live in
`concord-ui-design/LICENSE`.

Cite-only sources are linked here, never copied: the skill names the WCAG 2.2
success criteria it checks and this README pins their W3C links —
[1.4.3 contrast (minimum)](https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum.html),
[1.4.11 non-text contrast](https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html),
[2.4.7 focus visible](https://www.w3.org/WAI/WCAG22/Understanding/focus-visible.html),
[2.4.11 focus not obscured (minimum)](https://www.w3.org/WAI/WCAG22/Understanding/focus-not-obscured-minimum.html),
[2.5.8 target size (minimum)](https://www.w3.org/WAI/WCAG22/Understanding/target-size-minimum.html)
and
[2.3.3 animation from interactions](https://www.w3.org/WAI/WCAG22/Understanding/animation-from-interactions.html)
— under the
[W3C document licence](https://www.w3.org/copyright/document-license-2023/),
which forbids derivatives.
[vercel-labs/agent-skills](https://github.com/vercel-labs/agent-skills/tree/1a2c79d)
web-design-guidelines carries no licence file at its pinned ref and is cited
as a related thin fetch-wrapper, never adapted.

Ticket writing, PR writing, research writing and handoff writing adapt to-tickets,
pr, research and handoff from [mattpocock/skills](https://github.com/mattpocock/skills/commit/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d)
and keep the upstream MIT notice in each skill's `LICENSE`. Project updates is
original Concord text carrying the repository license.

Adoption adapts the brownfield onboarding order and the common-mistakes table
from [wagglebot](https://github.com/swiknaba/wagglebot/blob/c974c2135de31747531e7400c8f9a0fab638e668/skills/onboarding-a-repository/SKILL.md),
lazy registry growth and propose-then-confirm setup from
[mattpocock/skills](https://github.com/mattpocock/skills/commit/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d)
(domain-modeling and setup-matt-pocock-skills), and the binding-rule layer with
an amendment path from
[spec-kit](https://github.com/github/spec-kit/blob/f3d6fb762c8e6718bf2dedcba38cc5dc93f65645/templates/constitution-template.md).
Its `LICENSE` keeps each upstream MIT notice. It also draws on cited-only
sources, linked and never copied: the ADR status lifecycle of
[madr](https://github.com/adr/madr/blob/ba75bb1b20d42af5746b246ad348c202419ae681/template/adr-template.md)
and [adr-tools](https://github.com/npryce/adr-tools/blob/b3279baf9be2207d1a4f4bbd608fd0b591c72aee/README.md),
owner co-authoring with a fresh-reader test from
[anthropics/skills doc-coauthoring](https://github.com/anthropics/skills/blob/2c5958ecac7cee2b583967a2c4981d0f4a75c579/skills/doc-coauthoring/SKILL.md)
(no licence file at that commit), [agents.md](https://github.com/agentsmd/agents.md/blob/d001185d792eb6402a58e4cbef1c228b309ec25d/README.md)
on where agent instructions live, and the [Diátaxis](https://diataxis.org/)
document taxonomy.

Test design adapts [tdd](https://github.com/mattpocock/skills/blob/4ae2317ddb22b046e26455e4f9c9b3aaaf62212d/skills/engineering/tdd/SKILL.md)
from mattpocock/skills, [test-driven development](https://github.com/obra/superpowers/blob/22f0b463e7a5e579904dd6211a520321035526f7/skills/test-driven-development/SKILL.md)
and [verification before completion](https://github.com/obra/superpowers/blob/22f0b463e7a5e579904dd6211a520321035526f7/skills/verification-before-completion/SKILL.md)
from obra/superpowers, and the independent-slice idea from the [specification template](https://github.com/github/spec-kit/blob/f3d6fb762c8e6718bf2dedcba38cc5dc93f65645/templates/spec-template.md)
from github/spec-kit.
It keeps their MIT notices in `concord-test-design/LICENSE`. Further reading,
cited but not copied: [Software Engineering at Google, chapter 12](https://abseil.io/resources/swe-book/html/ch12.html),
[What to look for in a code review](https://google.github.io/eng-practices/review/reviewer/looking-for.html),
and [The Practical Test Pyramid](https://martinfowler.com/articles/practical-test-pyramid.html).

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
host-owned and reaches agents through the
[CD-0043 D1](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0043-host-specific-methodology-stays-host-owned.md)
host instruction surface.

Admission does not establish implementation or conformance. A shipped skill
never substitutes for a check and holds no independent evidence, approval, or
law authority.
