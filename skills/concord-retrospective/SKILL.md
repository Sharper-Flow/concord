---
name: concord-retrospective
description: Find what in the agents' environment caused recurring friction and move each correction to the owner that can hold it. Use after a session or delivery with repeated steering, a mistake a check could have caught, slow navigation, missing information, or costly tool use.
---

# Retrospective

Use this method when friction recurred or cost material time. It is not a
mandatory step after every task. It proposes improvements to instructions,
checks, documents, and tools; it does not authorize changing them, and a
finding outside the assigned scope becomes a follow-up item.

## Canonical references

- [Shipped skill delivery and portability](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
- [Shipped conduct corpus](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0063-shipped-operator-conduct-rules.md):
  Concord's always-on conduct changes only with a Concord release.
- [Knowledge ladder](https://github.com/Sharper-Flow/concord/tree/02c0bae9c28eafe6414cab2f390f940dc1f79625/.concord/docs/decisions/CD-0106-lesson-owns-the-knowledge-ladder.md):
  a procedure belongs to instructions or a skill, a fact about one Product to
  a lesson, and accepted law to a decision or specification.

## Read the primary sources

Read the work's own records: the work record, review findings, failed and
repaired checks, and the corrections the operator gave. Use only sources the
work already holds or the operator names. Do not sweep private logs, and redact
secrets in anything you quote.

## Look for these causes

- **Navigation**: finding the right file or record took long. A pointer at the
  place the agent first looked may fix it.
- **Missing check**: the agent made a mistake a deterministic check could catch.
  First look for an existing check that is unwired, skipped, or broken; repairing
  it beats writing a new one. A repository with no check gate at all is itself a
  finding.
- **Rule placement**: for a recurring mechanical mistake (a banned call, a file
  location, a fixed pattern), prefer an applicable existing check over more
  prose. Add a check only when the observed risk warrants its maintenance cost.
  A judgement rule belongs in review guidance rather than in always-on text.
- **No-op instructions**: steering text that changes no behavior, or repeats a
  rule its owner already states. Delete it.
- **Information access**: a fact the agent needed was not reachable. Name the
  source that would have answered it.
- **Tool economy**: a call that was repeatedly expensive for what it returned.

## Report candidates

Order candidates by severity. For each, give the observation and its evidence,
the owner that should hold the correction, and the smallest change at that
owner. Prefer deleting or relocating text to adding text. Apply in-scope
corrections through the normal change route. Record the rest once, as follow-up
items in the Product's planning system.
