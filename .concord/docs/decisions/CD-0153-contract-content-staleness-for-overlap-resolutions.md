# CD-0153: Contract-content staleness for overlap resolutions

- **Status:** Accepted
- **Date:** 2026-09-15
- **Scope:** CD-0222 D6 resolution staleness and the disposition of existing
  overlap-resolution rows
- **Approval:** The operator approved this amendment on 2026-09-15.
- **Related:** CD-0036, CD-0222 D5–D7, CD-0144, CD-0145, and [PR 1084](https://github.com/Sharper-Flow/concord/pull/1084)
- **Preserves:** D6's closed resolution-kind set and operator approval;
  D7's seven action classes; CD-0036 breaking-law cutovers; and the
  execution-start claim boundary from PR 1084

**Approval amendment:** [CD-0243 D7](CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain unchanged.

## Context

CD-0222 D6 states that changing either contract makes its overlap resolution
stale. A contract revision can change content unrelated to the architectural
write intersection that the operator judged. Such a revision does not change
the judged pair and must not void its resolution.

PR 1084 moved the active architecture-footprint boundary from contract approval
to execution start. That boundary controls which work items participate in
overlap checks. It does not define when a resolution becomes stale.

## Decision

### D1. Staleness follows judged contract content

A revision of either contract makes an overlap resolution stale only when the
revision changes one or more of these judged fields:

- `affected_domain_ids`;
- `law_additions` or `law_modifies`;
- `domain_modifies`; or
- `domain_relation_modifies`.

A revision that leaves all four fields unchanged leaves the resolution current.
The recorded contract versions remain the audit record of the approved inputs,
but a version change alone does not make the resolution stale.

### D2. Existing resolution rows stay counted

The store keeps all 2,513 recorded rows as current state
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
It does not bulk invalidate,
rewrite, or migrate them. An invalidation recorded before judged-content
comparison took effect stays invalidated. A content-neutral revision after that
leaves a non-invalidated resolution current.

### D3. The execution-start boundary remains independent

The claim-at-execution-start boundary from PR 1084 remains in force. Only a
nonterminal Product-changing item that has started execution holds an active
architecture footprint. An approved item that has not started execution does
not block a peer.

This amendment does not change peer enumeration, the subject's prospective
footprint during its claim, or the lifecycle transition into execution. It
changes only the content comparison used when an existing resolution is tested
for staleness.

### D4. Existing authority and resolution classes remain unchanged

The five resolution kinds remain. Merger and supersession require operator
approval because they abandon or replace a work identity. Compatibility,
blocking, and pure dependency answers require no consent. All answers retain
both work versions, contract pins, and judged-content freshness. A dependency
answer grants no standing Domain compatibility waiver.
D7's seven consequential action classes remain unchanged. CD-0036 remains the
authority for breaking law cutovers; a law edit triggers reassessment only of the obligations whose meaning it changes ([CD-0242 D10](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d10-knowledge-is-an-input-and-a-law-edit-changes-only-what-it-changes)).

## Consequences

- A revision that does not change judged content leaves a resolution applicable.
- Changes to judged architecture content still require a fresh resolution.
- Execution admission remains governed by the execution-start boundary from PR
  1084.

## Verification

- `python3 scripts/check-knowledge-index.py --update` recomputes the decision
  record hash and validates the composed knowledge index.
- `test -f .concord/docs/decisions/CD-0153-contract-content-staleness-for-overlap-resolutions.md`
  verifies the amendment file exists.
