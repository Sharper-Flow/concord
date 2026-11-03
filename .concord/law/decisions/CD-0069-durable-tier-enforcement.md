---
{
  "id": "CD-0069",
  "title": "The durable tier is enforced, not advised",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "agent-contract",
  "applies_to_domain_ids": []
}
---
# CD-0069: The Durable Tier Is Enforced, Not Advised

Status: accepted
Date: 2026-08-24

## Context

CD-0002 binds the durable git tier with four rules — markdown only,
distillation rather than mirroring, no duplication with SQLite, no process
exhaust — and a size intent: a page, not hundreds of kilobytes. Nothing
checked any of them. Issue #181 asked whether those rules are executable law
or documented intent.

The controlled trial that motivated this record held the same rules in
prose, and drifted into committing 49.9 MB of JSON state blobs across 526
archived bundles — median 80 KB, one bundle at 1.02 MB — into consuming
repositories' git history. Nothing failed at any step. Remediation of that
class is a history rewrite, which is why the drift is effectively permanent
there.

A rule that nothing checks is a rule that holds only while everyone remembers
it.

## Decision

The binding rules apply to independently published durable markdown and are
enforced by the repository check. Completion creates no note producer or
publication scenario under [PM6 §1](../specs/canonical-git-note-placement.md#1-decision).

### D1. The repository check

`scripts/check-durable-tier.py` runs in CI and fails on:

- any non-markdown artifact under a note root,
- any note over the budget's byte bound,
- any fenced JSON block over the budget's dump threshold,
- any non-markdown artifact under `.concord/law/decisions/` that is not in the
  budget's declared inventory, or any inventoried artifact that no longer
  exists.

The bounds live once in `.concord/docs/durable-tier-budget.v1.json`, shaped like
`.concord/docs/complexity-budget.v1.json`: the checker reads them, and the
repository check reads the numbers.
No surface restates a bound; restated bounds drift.

Enforcement is zero tolerance with no ratchet baseline. The note tier is
empty today, which is the one moment strict-from-zero costs nothing. Baseline
machinery exists for legacy corpora and carries its own failure mode: once a
ratchet clears, new regressions can be accepted as a fresh baseline.

An allowance is permission for one named note to exceed the byte bound while
an issue tracks it. Allowances never permit non-markdown artifacts or
embedded state dumps — those rules have no exceptions.

### D2. The byte bound is 16384, and it governs work and lesson notes

The bound applies to the note roots CD-0002 names for that
purpose: `<product-root>/.concord/docs/work/` and `<product-root>/.concord/docs/lessons/`. 16384 bytes is roughly eight
times a real distilled page and three orders of magnitude under the
drift that motivated this record, so it rejects the disease without rationing the cure.

Decision records are out of the byte bound, deliberately. They are a
different artifact class — CD-0013 is accepted law at 34.5 KB — and review,
not a size bound, holds their discipline.
The markdown-only rule still reaches them: every non-markdown artifact under
`.concord/law/decisions/` is enumerated in the budget inventory with a reason, so a
new one fails until acknowledged and drift becomes a reviewable manifest
diff. License evidence and the CD-0014 dependency manifest are the initial
inventory; both are legal or contract material, not state.

## Consequences

- The four CD-0002 binding rules and the page-size intent are falsifiable in
  CI from this record's merge.
- Independently authored notes remain subject to the repository check.
- Adding a non-markdown artifact to `.concord/law/decisions/` now requires a budget
  entry with a reason, which is the decision the drift never forced.
- The bound can be lowered as a ratchet without a new decision; raising it
  requires one, because raising a bound forgives debt nobody chose.

## Approval

Operator approved the layered direction and the 16384 bound on 2026-08-24,
recorded in
[issue #181 comment](https://github.com/Sharper-Flow/concord/issues/181#issuecomment-5399203988).
The scope correction — byte bound on compaction notes, inventory for
decision records — is in the same comment.
