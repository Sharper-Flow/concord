# Knowledge formalization procedure

## Purpose

This document defines how a document in this repository becomes accepted
Product law, becomes registered non-law, or is explicitly dispositioned.

The procedure below is a decision path. Followed on the same document by two
different readers, it must produce the same kind, the same authority claim,
and the same obligations every time.

This document states rules and links to the mechanism that enforces each one.
It does not restate the manifest, the schema, or any validator. Those
artifacts are the authority for their own contents; a prose copy of them
would drift with no check to catch it.

## The four states

Every Markdown document under the manifest's declared knowledge roots is in
exactly one of three states.

**Unprocessed.** The manifest holds no record for the file. The document is
not law for this Product, however much it reads like a specification. An
agent must never treat an unprocessed document as authority. The negative is
authoritative only through `concord_knowledge.resolve_note` returning
`knowledge_missing` for the record the document would need; text search is not
a substitute for it.

**Law.** A record exists, its kind is law-bearing, and its status is
`accepted`. Only `constitution`, `decision`, and `spec` can reach this state.

**Out of date.** A record exists but its recorded content hash no longer
matches the file. The record and the artifact disagree, so neither can be
trusted until the hash is repaired.

Out-of-date records are detected by
[`scripts/check-knowledge-index.py`](../../scripts/check-knowledge-index.py).

## Three destinations for an unprocessed document

An unprocessed document has exactly three legitimate destinations. Leaving it
unprocessed indefinitely is not one of them.

1. **A manifest record.** The document is registered, either law-bearing or
   registered non-law. Its kind, status, path, hash, and scope are recorded.
2. **A disposition.** The document is source material the Product decides not
   to formalize. It is recorded as `archived` with a required reason.
3. **An exclusion.** The document is not knowledge at all. Generated build
   output is the clear case: it has no author to hold accountable and no
   content to accept.

The disposition and exclusion shapes are declared in
[`contracts/concord-knowledge-index.v1.schema.json`](../../contracts/concord-knowledge-index.v1.schema.json).

## The kind taxonomy

Two tiers exist, and the split is enforced structurally rather than by
convention.

**Law-bearing kinds** are `constitution`, `decision`, and `spec`. Their
accepted status value is `accepted`.

**Registered non-law kinds** are `lesson`, `reference`, and `research`. Their
published status value is `published`. A record of these kinds cannot carry
status `accepted`; the schema forbids it, so the reservation of `accepted`
for law is a structural fact and not a naming habit.

`reference` covers navigation material and reference material. There is
deliberately no `navigation` kind. Adding one would split a single decision
across two names with no rule to separate them.

## Kind selection

Answer these questions in order. Stop at the first `yes`. Each terminal
answer names exactly one destination.

1. **Is the file generated build output, or otherwise not authored
   knowledge?** If yes, it is an exclusion. It takes no kind and no record.
2. **Has the Product decided not to formalize this source material?** If yes,
   record a disposition of `archived` with a reason. It takes no kind.
3. **Is the document accepted under this repository's authority model?** If
   no, it cannot take a law-bearing kind. Continue from question 7. A
   non-authorizing candidate document is reference material until it is
   accepted. Acceptance is recorded, never read out of the prose: a document
   is accepted only when its own header carries an acceptance marker this
   repository recognizes — a binding-until-superseded status bound to an
   allocated record number, or a citation as binding from an accepted
   constitution or decision. A header that names another document as
   canonical for its subject, or carries no authority claim at all, means
   not accepted — however binding the body reads.
4. **Does the document state standing rules that govern other documents or
   decisions?** This covers rules about how records are made, classified, or
   accepted. It also covers rules about which document wins when two
   disagree, and rankings that later decisions must honour. If yes, the kind
   is `constitution`.
5. **Does the document record one choice, its alternatives, and its
   consequences, under an allocated decision number?** If yes, the kind is
   `decision`.
6. **Does the document state a testable contract that an implementation must
   satisfy?** If yes, the kind is `spec`. The discriminator is prescription,
   not testability: a spec prescribes what an implementation must satisfy,
   and a violation is a defect in the implementation. A document that
   describes what the shipped implementation already does — its behavior
   proved by that implementation's own test suite — prescribes nothing, and
   testable content alone does not make a spec. Such a document is reference
   material, and the record's summary names the tests that verify the
   described behavior.
7. **Does the document record what was learned from work already completed?**
   If yes, the kind is `lesson`.
8. **Does the document report the findings of an investigation?** If yes, the
   kind is `research`.
9. **Otherwise** the kind is `reference`.

Question 3 is placed before the law-bearing questions on purpose. A document
that reads like a specification but has not been accepted is not a `spec`;
it is reference material that describes a proposal.

Questions 4 through 6 are ordered from widest scope to narrowest. Question 4
asks about authority over documents and decisions. Question 6 asks about
authority over an implementation. A document that binds how other documents
are written is a `constitution`. A document that binds what code must do is a
`spec`. Where a document does both, the earlier question wins. That is a
rule, not a preference: without it, the two readers disagree again.

## Record identity and paths

Record identifier conventions below are read from the live record shards
under [`.concord/docs/knowledge/records/`](./knowledge/records/).

| Kind | Identifier convention | Basis |
|---|---|---|
| `constitution` | file basename without the `.md` suffix | the `constitution` records in the live manifest |
| `decision` | `CD-` followed by four digits | every decision record in the live manifest |
| `spec` | a predecessor catalogue code such as PM1, PM3, PM6, TS3, or TS7; otherwise the file basename | both forms appear in the live manifest |
| `lesson` | file basename without the `.md` suffix | the single `lesson` record in the live manifest |
| `reference` | file basename without the `.md` suffix | the `reference` record in the live manifest |
| `research` | file basename without the `.md` suffix | the `research` record in the live manifest |

Take the basename exactly as the file spells it. Do not change its case and
do not replace its punctuation. A file named `README.md` takes the identifier
`README`.

The `date` field records when the document reached its current status. For a
law-bearing kind that is the date it was accepted. For a non-law kind that is
the date it was published. It is not the date the document was first written.
It is not the date its body last changed.

Two path constraints bind authorship directly.

A `research` record cannot live in the `research` directory under `.concord/docs/`.
The manifest excludes that directory, and it is ineligible as a record path. A research
document that must hold a record has to live elsewhere under `.concord/docs/`.

A `decision` record's path is bound to the decisions directory and the
decision-number filename shape. The schema enforces it, so a decision filed
anywhere else cannot be registered at all.

Law-bearing relations are defined over `decision` and `spec` only. A
`constitution` record does not currently participate in `law_relations`, so a
constitution cannot declare that it refines or supersedes another record.

## Domain home

A law-bearing record with status `accepted` must name a home domain. A
registered non-law record must not name one, and must not name applied
domains either. A record that names applied domains must also name a home
domain.

These rules are conditional clauses in
[the schema](../../contracts/concord-knowledge-index.v1.schema.json), which is
the authority for their exact form. The domain identifiers themselves come
from the manifest's domain registry.

## External systems

A record about an external system describes how this Product uses it. It
stores a locator to the vendor's documentation, such as a URL or a context7
library id, and never the vendor's schema or documentation content. Agents
fetch the vendor side live, so freshness is never a stored property and no
stored copy can age. CD-0106 D4 owns this rule.

A record that carries learned usage of a resource is tagged
`resource:<resource_id>`.

## The acceptance-criteria rule

### Binary by kind

Acceptance criteria are required for `spec` and forbidden for every other
kind. There is no middle position and no optional case.

A reader determines whether a document should carry an acceptance-criteria
section from its `kind` alone. Inspecting the document to find out is wrong,
because a document that carries the section when its kind forbids it is
exactly the defect the rule detects.

A Gherkin acceptance-criteria section therefore implies `spec`. Any other
kind implies that no such section exists.

### Graduation to executable scenarios

Every acceptance criterion in a `spec` must resolve to an executable scenario
identifier, to a typed exemption carrying a reason, or to a work-item
predicate reference naming the Concord work item and outcome predicate that
discharge it (CD-0180). A criterion that resolves to none of these is a claim
with no way to fail.

The manifest record carries `criterion_bindings`. The document body does not
carry binding syntax. An exemption is a recorded reason, not silence.

## Atomicity of the edit and its record

A document edit and the update of its recorded content hash must land
together. A commit on the default branch must never contain a document whose
body disagrees with the hash recorded for it.

This is structurally safe here rather than merely intended. The repository
merges pull requests by squash, with the pull-request title as the commit
title, as recorded in
[`.github/workflows/pr-title.yml`](../../.github/workflows/pr-title.yml). Each
pull request therefore becomes exactly one commit on the default branch. A
branch may hold an intermediate state where the edit and the hash disagree,
but that state has no commit of its own on the default branch and so cannot
land.

The executable proof is
[`scripts/check-knowledge-index.py`](../../scripts/check-knowledge-index.py),
which fails when a record's hash does not match the bytes of the file it
points at. It runs on every branch, because
[`scripts/check-json.py`](../../scripts/check-json.py) invokes it and
[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml) runs that. A
mismatch therefore fails the required checks, not merely a local run. The
same mismatch fails at read time in the store, so a stale record cannot be
served as law either.

## Candidate to accepted transition

When a document stops being a non-authorizing candidate and becomes accepted,
its record eligibility must be re-evaluated as part of that acceptance. The
person who accepts the document owns that re-evaluation.

Re-evaluation means three concrete questions. Is the document's path still
ineligible for a record? Does its kind still match the selection path above,
now that its authority has changed? Does it now require a home domain that it
did not require as a candidate?

## Dispositions carry a reason

A disposition must not quietly absorb work that formalization should have
done. Every disposition carries a required reason. A disposition without a stated
reason is a deletion of the question, not an answer to it.

## Approval evidence

A formalization decision is accepted when this repository's ordinary
authority model says it is. Knowledge work is not special-cased. The
authority model is stated in the repository agent instructions.

Three pieces of evidence are required together.

| Evidence | What it establishes | Authority |
|---|---|---|
| An issue | the planned work or the defect being closed | [`AGENTS.md`](../../AGENTS.md) |
| A pull request | the change itself, carrying both the document and its manifest record | [`AGENTS.md`](../../AGENTS.md) |
| Passing required checks | that the change satisfies the verification contract | [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml) |

No other route confers acceptance. A document is not accepted because it says
it is accepted in its own body, and a record is not valid because someone
intended to add it.
