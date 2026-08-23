# CD-0061: A root Domain home states why no child Domain owns it

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** Law-record homing; the knowledge manifest record vocabulary; the
  law-home projection; issue #398
- **Approval:** Operator accepted the drafted decision as written on 2026-08-23;
  the public record is the pull request that lands this record
- **Related:** CD-0060 (partitions the corpus; D2 gives the root Product-wide
  law), CD-0041 D2/D3/D9.2, CD-0042 D1 (pre-go-live), CD-0002, PM1.Q10
- **Preserves:** CD-0041 D3's one-home invariant; the derived-projection role of
  SQLite; the CD-0041 D9.2 upcast
- **Supersedes:** nothing

## Context

CD-0060 partitioned the law corpus. The root Domain fell from 65 records to 8.

It was 9 within hours. CD-0059 was authored while the registry still held only
`product-root:concord`, so the root was the only home it could carry, and it
landed after the classification that produced the partition had run. Nothing
was wrong with that change. The record had nowhere else to go.

That is the general shape. CD-0041 D2 makes the root correct for Product-wide
law and simultaneously the only home an author reaches by deciding nothing. A
record that defaulted to the root and a record that belongs there are
indistinguishable once written. The corpus drifts back toward a catch-all one
record at a time, and every intermediate state validates.

`scripts/check-domain-registry.py`, added by CD-0060, proves the referential
half: every Domain owns law, every home resolves, relations name current law.
It passed on CD-0059 throughout. "This record's behavior fits no child Domain"
is a judgement about what a document governs, and counting cannot reach it.

## Decision

### D1. Law homed to the root states its claim

A law-bearing record whose `home_domain_id` is the Product root declares
`product_wide_rationale`: a stated reason, non-empty, trimmed, at most 512
characters. Omission is refused.

This does not make the judgement machine-checkable, and it is not meant to. It
converts a silent default into an assertion the author had to write and a
reviewer can dispute. The root stops being reachable by deciding nothing.

The bound is part of the rule. A reason needing more than a few sentences is
describing law that belongs in a child Domain.

### D2. Only root-homed law states it

A child home has already decided. A rationale there would assert a Product-wide
reach the home contradicts, so carrying the field on a child-homed record is
refused. Non-law records carry no law-home fields at all, and this field
follows them.

### D3. The upcast marks an undecided home rather than inventing a claim

CD-0041 D9.2 assigns the root to legacy law carrying zero or several component
IDs. That home is undecided by construction. The migration cannot invent a
reason, and emitting none would recreate the silent default this record
removes.

`MigrateLegacyKnowledgeManifest` therefore writes an explicit undecided marker.
The marker is a legal transient and an illegal resting state:
`scripts/check-domain-registry.py` fails on any record still carrying it, so an
unreviewed home cannot age into a claim nobody made.

### D4. The claim is projected, not merely parsed

PM1.Q10 rebuilds a declared record from SQLite and requires byte equality with
the manifest. A field the projection drops therefore makes every root-homed
record fail its own proof.

`law_domain_homes` gains `product_wide_rationale` under migration 45, the
projection writes it, and Q10 reads it back. Child-homed rows keep the empty
default, which is what their absent claim means.

### D5. Manifest version 1.2 tightens in place

This adds a requirement to records that already exist at schema version 1.2,
which no earlier version can express: the conditional fires only on a record
carrying `home_domain_id`, and that field arrived with 1.2.

No new manifest version is cut. CD-0042 D1 keeps Concord pre-go-live until an
accepted decision names the first supported release, clients, and compatibility
cohort. There is no cohort to break, and this repository holds the only 1.2
manifest, updated in the same change. A version bump would buy negotiation
nobody is party to.

## Consequences

- Eight root-homed records state a rationale. Each names why no child Domain
  owns it rather than repeating the clause it comes from.
- The manifest vocabulary gains one record field, declared once in
  `contracts/concord-knowledge-index.v1.schema.json` and bound to both readers
  by `scripts/check-knowledge-vocabulary.py` and
  `internal/store/knowledge_vocabulary_test.go`.
- Root homing now costs a sentence. That is the intended cost, paid by the
  author who is deciding, at the moment the decision is made.
- The rule does not decide whether a stated claim is *true*. It guarantees a
  claim exists, is attributable, and survives review. Nothing here prevents a
  wrong rationale, and no mechanism proposed in #398 could.
