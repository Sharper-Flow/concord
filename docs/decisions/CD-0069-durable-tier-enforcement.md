# CD-0069: The Durable Tier Is Enforced, Not Advised

Status: accepted
Date: 2026-08-24

## Context

CD-0002 binds the durable git tier with four rules — markdown only,
distillation rather than mirroring, no duplication with SQLite, no process
exhaust — and a size intent: a page, not hundreds of kilobytes. Nothing
checked any of them. Issue #181 asked whether those rules are executable law
or documented intent.

The predecessor system is the controlled trial. It held the same rules in
prose, and drifted into committing 49.9 MB of JSON state blobs across 526
archived bundles — median 80 KB, one bundle at 1.02 MB — into consuming
repositories' git history. Nothing failed at any step. Remediation of that
class is a history rewrite, which is why the drift is effectively permanent
there.

A rule that nothing checks is a rule that holds only while everyone remembers
it.

## Decision

The binding rules are executable law, enforced in three layers. Each layer
binds a different writer population; no single layer covers all three.

### D1. Layer 1 — the detective check — lands now

`scripts/check-durable-tier.py` runs in CI and fails on:

- any non-markdown artifact under a note root,
- any note over the budget's byte bound,
- any fenced JSON block over the budget's dump threshold,
- any non-markdown artifact under `docs/decisions/` that is not in the
  budget's declared inventory, or any inventoried artifact that no longer
  exists.

The bounds live once in `docs/durable-tier-budget.v1.json`, shaped like
`docs/complexity-budget.v1.json`: the checker reads them, and the
producer-side parse and the AJ6 scenario extension read the same numbers.
No surface restates a bound; restated bounds drift.

Enforcement is zero tolerance with no ratchet baseline. The note tier is
empty today, which is the one moment strict-from-zero costs nothing. Baseline
machinery exists for legacy corpora and carries its own failure mode: once a
ratchet clears, new regressions can be accepted as a fresh baseline.

An allowance is permission for one named note to exceed the byte bound while
an issue tracks it. Allowances never permit non-markdown artifacts or
embedded state dumps — those rules have no exceptions.

### D2. The byte bound is 16384, and it governs compaction notes

CD-0002's page-size sentence is written about the note a terminal entity is
distilled into. The bound applies to the note roots CD-0002 names for that
purpose: `docs/work/` and `docs/lessons/`. 16384 bytes is roughly eight
times a real distilled page and three orders of magnitude under the
predecessor's failure, so it rejects the disease without rationing the cure.

Decision records are out of the byte bound, deliberately. They are a
different artifact class — CD-0013 is accepted law at 34.5 KB — and their
discipline is structural (the doc contract, knowledge closure), not size.
The markdown-only rule still reaches them: every non-markdown artifact under
`docs/decisions/` is enumerated in the budget inventory with a reason, so a
new one fails until acknowledged and drift becomes a reviewable manifest
diff. License evidence and the CD-0014 dependency manifest are the initial
inventory; both are legal or contract material, not state.

### D3. Layer 3 — the producer-side parse — is the contract the compaction pipeline must satisfy

When the terminal-compaction producer is built, its note writer must parse
into a strict typed shape and reject before writing: unknown fields, total
size over the budget bound, any embedded JSON block over the dump threshold.
A compliant producer can never emit a violation. The bounds come from the
same budget file; the producer adds no numbers of its own. This layer is
tracked by issue #463.

### D4. Layer 2 — the corpus scenario — lands with the producer

`AJ6-compact-terminal-work` gains an assertion that the terminal compaction
path emits a note that passes the durable-tier budget. This guards
correct-shaped-but-wrong producer behavior over time — a regression that
starts serializing state again — which neither the schema nor the byte bound
can express. Tracked with layer 3 by issue #463.

### D5. Why three layers

The detective layer binds every writer, including manual commits and
out-of-band tools; git accepts bytes from anyone, so only a check at the
merge chokepoint is universal. The producer layer catches violations at the
moment of writing, where they are cheapest to fix, but binds only producers
that comply. The scenario layer observes behavior over time, catching
pipeline regressions neither other layer can express. The pattern is the one
mature systems run: compiler types plus conformance suites plus audit
checks, each catching what the others structurally cannot.

## Consequences

- The four CD-0002 binding rules and the page-size intent are falsifiable in
  CI from this record's merge.
- The note tier starts empty and strict; the first note the producer writes
  is born under enforcement.
- Adding a non-markdown artifact to `docs/decisions/` now requires a budget
  entry with a reason, which is the decision the predecessor never forced.
- The bound can be lowered as a ratchet without a new decision; raising it
  requires one, because raising a bound forgives debt nobody chose.

## Approval

Operator approved the layered direction and the 16384 bound on 2026-08-24,
recorded in
[issue #181 comment](https://github.com/Sharper-Flow/concord/issues/181#issuecomment-5399203988).
The scope correction — byte bound on compaction notes, inventory for
decision records — is in the same comment.
