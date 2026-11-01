---
name: concord-law-writing
description: Place or author Product knowledge held by Concord, and draft, amend, or prune Product law. Use when choosing a home for Product documentation, instructions or scenarios, writing decisions, specifications or standing rules, or shortening an existing law without changing its obligations.
---

# Law writing

Work on one selected Product and agreed change. This skill operates Concord's
knowledge structure; it supplies neither law nor approval and does not prescribe
a worker's lane methodology. Keep proposed wording distinct from accepted law.

## Canonical references

These Concord references define the knowledge and authority machinery, not the
adopting Product's behavior. Read that Product's own accepted law for its rules.
Concord's decision-number conventions and repository-development checks do not
automatically apply to another Product.

- [Skill admission and portability](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Domain ownership](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md).
- [Compatible amendment and breaking replacement](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0036-breaking-law-cutovers.md).
- [Law-change authority and conflict rules](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md).
- [Operator approval boundary](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md).

## Find the owner

Use `concord_product_view.resolve` and `concord_domain.list` to establish the
Product and Domain. Resolve current law through the workflow's resolved law
context; a heading, filename, or confident sentence is not acceptance. A
missing record is not permission to invent one. If a required source is
unavailable, stop the dependent claim and name the missing evidence.

Read the owning clause and its references before drafting. State what fails,
who is affected, and the end state the change must make decidable. Domain and law
reads establish ownership, not permission to expand scope. Classify a compatible
amendment or breaking replacement under the linked law.

## Place Product knowledge

[Placement](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0194-product-knowledge-lives-under-concord.md)
uses four canonical directories: `.concord/law` for law, `.concord/docs` for
lessons, research and reference, `.concord/instructions` for conduct, and
`.concord/scenarios` for acceptance scenarios. Keep one canonical copy in the
Product's [designated Git knowledge home](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0200-product-knowledge-resolves-through-one-designated-git-home.md).
Project membership or repository discovery does not designate that home.
For lesson authoring and publication, load `concord-retrospective`.

An outside placement needs an accepted decision naming the path, Product,
record carrying the operator's instruction, and reason. Its document must carry
this closed HTML comment, with the Product and repository-relative path matching
the override exactly:

    <!-- concord-operator-override
    product: <product-id>
    path: <repository-relative-path>
    decision: approve
    -->

Only `product`, `path`, and `decision` are fields. Unknown, repeated or missing
fields, an unclosed block, duplicate blocks for one Product/path, or a decision
other than `approve` or `deny` refuse the override. Only `approve` admits it;
`deny` records a denial. Prose, silence, a superseded decision or another record
kind cannot admit placement. An override permits non-law placement only: it
never makes a file outside `.concord/law/` law. Concord's root-tree absence rule
is specific to Concord, not a ban on every other Product's root directories.

## Shape the law file

Under [folder authority](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/constitution/law-folder-authority.md),
current law is the default-branch files in `.concord/law/constitution/`,
`.concord/law/specs/`, and `.concord/law/decisions/` at the designated home.
Branch text is candidate law. No registration confers acceptance. Start each
file with JSON frontmatter between `---` delimiters, for example:

    ---
    {
      "id": "request-limits",
      "title": "Request limits",
      "authority": {"tier": "derived"},
      "home_domain_id": "service-api",
      "applies_to_domain_ids": []
    }
    ---

Use the Product's stable law ID and an actual registry Domain, not the example's
names. The designated home holds one independent Git-authored Domain registry
at `.concord/docs/knowledge/domain-registry.json`. Resolve it at the same commit
as law. Each law declares exactly one `home_domain_id`; optional bounded
`applies_to_domain_ids` adds applicability, never ownership. Home a cross-cutting
rule to its enforcing Domain. A Product-root home needs
`product_wide_rationale` explaining why no child Domain owns it; root is not a
catch-all. Follow
[Domain homing](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0060-domain-registry-enactment.md),
not a title or path guess.

[Authority standing](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0159-authority-tier-for-product-law.md)
uses `authority.tier`: `derived` or `legislated`. Its optional fields
`legislated_by` (a work ID) and `contract_version` (a positive integer) are
required for `legislated` standing. Outside the closed default cohort, that
standing needs the named operator-approved contract to meet CD-0159 D2's tests.
An ordinary approval, issue or reviewed PR does not promote law. Outside-workflow
law carries `derived` without those fields; an agent-owned revision without
operator approval is also `derived`, whatever its prior tier.

Decision/spec metadata may declare `law_relations` entries with `kind` and
`target_id`. The [closed kinds](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0015-typed-law-relations.md)
are `supersedes`, `refines`, `subordinate_to`, and `conflicts_with`; targets stay
within the same law folder. A conflict grants no precedence or amendment.
Name decision/spec law IDs as targets. Add no self-edges, duplicate edges or
directed cycles.
Kind and acceptance come from the folder; path and digest come from committed
bytes, not authored metadata. Preserve specification criterion bindings where
the retained consumer requires them.

## Draft the rule

Write the obligation first: who must do what, under which conditions, with which
observable result. Make conjunctions, exceptions, refusal behavior and authority
explicit where they change the outcome. Keep implementation recipes, evidence
logs and delivery plans in their owning artifacts, not inside the obligation.

Load the shipped `concord-instruction-writing` skill for the general writing
method: canonical ownership, pointers, organization and pruning. Use the Product's
vocabulary for its obligations.

## Prune without changing meaning

Classify each changed obligation as preserved, changed or retired in the work
record. A shorter rule that admits another case is a behavior change. Preserve
current protections during an editorial edit; route an intended change through
the existing law-change path.

## Authorize, verify and publish

Apply the existing operator approval classes to the meaning change. Law editing,
seriousness and Domain count do not independently require operator approval.
When a class applies, obtain the required approval before implementation; a
change to the approval boundary requires approval of its exact wording. Otherwise
record the agent-owned reason and required authority tier. An implementation gap
or tool refusal does not waive a current gate.

For assigned work on Concord itself, apply CD-0243 D3's deletion exemption
before classifying a retirement. Deleting Concord law, unwanted mechanisms and
systems, and tests of retired behavior alone needs no product-direction, UI/UX
or governed-scope-cut approval. Record why, retire governing requirements with
or before behavior, and deliver an ordinary independently reviewed pull request
with required CI. List retired law and meaning changes in the pull request and
completion report; cite the retiring clause for each deleted test. Keep D3's
exclusions and current gates. Other Products receive no such exemption.

Read the selected Product's `.concord/tooling.v1.json` and use only its declared
checker commands. The [tooling declaration](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0074-project-tooling-declaration.md)
holds facts: tool `id`, `purpose`, `invocation`, cost `tier` (`fast`, `standard`,
or `slow`), and optional `cost_hint`, `config_path`, `automation_path`, and
`notes`. Cost and automation pointers do not prescribe when to choose a tool;
methodology stays outside the declaration.

Update the canonical file, Domain home, applicability and relations together.
Regenerate generated material through its owner. Structural checks prove
identity, closure or shape, not that prose preserves meaning; review owns that
comparison. An independently reviewed PR with required checks enacts law by
adding its file to the default branch and retires law by deleting it. A
compatible amendment keeps the ID; a breaking replacement deletes the old file
and uses a new ID through the existing cutover route.

Report the exact wording, meaning changes, applicable approval and observed
checks in the existing work record. Distinguish candidate validation from
acceptance and publication. After publication, verify the record and revision
in the Product's current law folder at its designated Git home; a local edit or
passing check alone does not establish current accepted law.
