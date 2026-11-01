---
{
  "id": "CD-0062",
  "title": "A root Domain home states why no child Domain owns it",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "product-memory",
  "applies_to_domain_ids": [
    "durable-authority",
    "repository-verification"
  ]
}
---
# CD-0062: A root Domain home states why no child Domain owns it

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** Law-record homing; issue #398
- **Approval:** Operator accepted the drafted decision as written on 2026-08-23;
  the public record is the pull request that lands this record
- **Related:** CD-0060 (partitions the corpus; D2 gives the root Product-wide
  law), CD-0222 D2/D3/D9.2, CD-0042 D1 (pre-go-live), CD-0002
- **Preserves:** CD-0222 D3's one-home invariant

## Context

CD-0060 partitioned the law corpus. The root Domain fell from 65 records to 8.

It was 9 within hours. CD-0059 was authored while the registry still held only
`product-root:concord`, so the root was the only home it could carry, and it
landed after the classification that produced the partition had run. Nothing
was wrong with that change. The record had nowhere else to go.

That is the general shape. CD-0222 D2 makes the root correct for Product-wide
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

### D3. No legacy upcast exists

Root-home claims follow D1 and D2.

## Consequences

- Eight root-homed records state a rationale. Each names why no child Domain
  owns it rather than repeating the clause it comes from.
- The rationale is authored metadata inside each law file; readers refuse a
  record that omits it.
- Root homing now costs a sentence. That is the intended cost, paid by the
  author who is deciding, at the moment the decision is made.
- The rule does not decide whether a stated claim is *true*. It guarantees a
  claim exists, is attributable, and survives review. Nothing here prevents a
  wrong rationale, and no mechanism proposed in #398 could.
