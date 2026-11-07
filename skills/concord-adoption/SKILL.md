---
name: concord-adoption
description: Adopt Concord into an existing repository with documents, decisions, and design notes. Use to inventory and route existing knowledge, establish its home and Domains, and co-author the Product vision.
---

# Adoption

Use this method when an existing repository already has READMEs, documentation,
decision records, or design notes. It guides adoption without replacing that
knowledge or granting approval. The operator owns Product direction and the
vision; existing authority owns law acceptance.

## 1. Establish the Product and knowledge home

The agent establishes the Product and its designated Git knowledge home from
the operator's instruction before changing documents. Neither directory layout
nor Git remotes designate either. Read the Project's repository mode through
`concord_product_view.resolve`: a new Project starts off, and the phases that
follow write knowledge, so the operator decides now — turn the Project on, or
designate a separate knowledge home for an off-mode Project. Knowledge commits
to the Product's own repository only in on mode. If a designation is missing,
ask the operator; do not guess. Finish when the Product, the home, and the
operator's recorded mode are explicit.

## 2. Inventory existing documents

Before changing anything, the agent inventories root READMEs, documentation
directories, ADR and decision folders, design notes, and agent-instruction pages
such as `AGENTS.md`. Record the source path of each item, including documents
that will remain in place. Finish when the inventory covers the existing sources.

## 3. Classify each document

The agent assigns exactly one class to every inventory item:

| Class | Meaning |
|---|---|
| Binding rule | A still-governing obligation. |
| Reference | A durable explanation readers consult. |
| History | A superseded or descriptive record. |

Cite the document's own status where present, including ADR status lines.
When standing is uncertain, classify the document as history, record why, and
confirm with the operator before treating any record as a still-governing rule.
Finish when every item has a class and the evidence or uncertainty is recorded.

## 4. Route the classes

Route every item's placement through `concord-law-writing`; it owns the four
`.concord/` placement directories and the amendment rules, and binding rules
become candidate law there. Route durable explanations and the glossary to
`concord-architecture-writing`, whose method owns its own test for what
deserves a lasting record, and README material to `concord-readme-writing`.
Keep history reachable where readers find it. Finish when every item has a
route or an explicit decision to leave it in place.

## 5. Bootstrap the Domain registry

Before the first law record lands, the agent establishes the registry at
`.concord/docs/knowledge/domain-registry.json` in the designated knowledge home.
Law needs a home Domain, and Domain and law reads refuse a Product without a
registry. Derive Domains from the real codebase and the operator's description
of the system, not filenames or directory guesses. Grow the registry lazily:
add a Domain when the first record needs it, not as speculation about future
work. Resolve the registry's Domains through `concord_domain.list`. Finish when
the registry validates and the first candidate law has a real home Domain.

## 6. Co-author the vision

The agent gathers the operator's own words and proposes a draft at
`.concord/docs/product-vision.md` in the designated knowledge home. Cover
purpose, goals with measures, and non-goals. Let the operator refine the draft
and approve it: changing the vision is Product direction and requires operator
approval. A fresh reader without the conversation must be able to state the
Product's purpose from the draft. Finish when that check succeeds and the
operator's approval is recorded.

## 7. Migrate with provenance

The agent records each migrated record's source path and the commit or link
where the source lived. Preserve decision status: binding content in an
accepted ADR is candidate law, while a superseded ADR is history. The reviewed
change that adds the migrated record removes or replaces the original, so one
canonical location remains; the recorded source keeps the provenance. Within
`.concord/law/`, change an accepted record only through `concord-law-writing`'s
amendment and replacement rules. Use the routed skills and
existing authority for validation, review, and acceptance; this method does
not authorize publication. Finish when each migration has provenance and its
acceptance status is explicit.

## 8. Confirm repository mode

Re-read the Project's repository mode through `concord_product_view.resolve`
after the knowledge has landed: on mode must show the knowledge committed as
ordinary Product files, and an off-mode Project must show its knowledge only
in the separately designated home. Finish when the read matches the placement
the adoption delivered.

## What a good result looks like

Every inventoried document is classified and routed or explicitly left in place.
The registry validates, the vision carries the operator's approval, and each
migrated record carries provenance. Nothing was deleted before acceptance.
The adopting Product's own accepted law governs it afterward.

## Common mistakes

| Mistake | Correction |
|---|---|
| Derive Product identity or Domains from layout or remotes. | Use the operator's designation and evidence of the actual system. |
| Invent registry entries for possible future work. | Add a real Domain when its first record needs it. |
| Migrate a superseded decision as binding law. | Preserve its status as history and route only still-governing obligations as candidate law. |
| Leave the original in place after its record migrates. | The reviewed change that adds the migrated record removes or replaces the original; provenance lives in the record. |
| Treat this order as approval to publish law. | Follow the Product's existing authority, review, and approval boundaries. |
| Set repository mode as the agent. | The operator sets each Project's mode in step 1; the agent reads and confirms it. |
| Fill the vision with agent-invented goals. | Gather the operator's words, propose a draft, and obtain their approval. |

## Canonical references

- [Admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-release-versioned-portable-skills.md).
- [Placement](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0194-product-knowledge-lives-under-concord.md).
- [Registry](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0060-domain-registry-enactment.md).
- [Knowledge home](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0200-product-knowledge-resolves-through-one-designated-git-home.md).
- [Product vision](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0254-product-vision-documents.md).
- [Repository mode](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0255-per-project-repository-mode.md).
