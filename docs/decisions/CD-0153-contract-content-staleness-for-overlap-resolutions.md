# CD-0153: Contract-content staleness for overlap resolutions

- **Status:** Accepted
- **Date:** 2026-09-15
- **Scope:** CD-0041 D6 resolution staleness and the disposition of existing
  overlap-resolution rows
- **Approval:** The operator approved this amendment on 2026-09-15.
- **Related:** CD-0036, CD-0041 D5–D7, CD-0144, CD-0145, and [PR 1084](https://github.com/Sharper-Flow/concord/pull/1084)
- **Amends:** CD-0041 D6's rule for staleness after a contract revision
- **Preserves:** D6's closed resolution-kind set and operator approval;
  D7's seven action classes; CD-0036 breaking-law cutovers; and the
  execution-start claim boundary from PR 1084

## Context

CD-0041 D6 states that changing either contract makes its overlap resolution
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

### D2. Existing resolution rows remain preserved history

An earlier count cites 2,179 existing resolutions. The store currently contains
2,513 rows in the overlap-resolution table. This amendment therefore treats
2,513 rows as the counted population and does not claim that every row is active
or current.

The amendment preserves all 2,513 rows as history. It does not bulk invalidate,
rewrite, or migrate them. Each row is current unless a later revision changed
the judged content in D1. A row already marked stale remains historical, and a
row for which only unrelated contract content changed remains current.

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

The five D6 resolution kinds remain `compatible_with`, `depends_on`, `blocks`,
`merged_into`, and `supersedes`. Only the operator may approve a resolution.
D7's seven consequential action classes remain unchanged. CD-0036 remains the
authority for breaking law cutovers and its strict quiescence rule.

## Consequences

- Unrelated contract revisions no longer discard a valid overlap judgment.
- Changes to judged architecture content still require a fresh resolution.
- The 2,513-row store population is explicit, while the earlier 2,179 count is
  not treated as a complete population.
- Execution admission remains governed by the execution-start boundary from PR
  1084.

## Verification

- `python3 scripts/check-knowledge-index.py --update` recomputes the decision
  record hash and validates the composed knowledge index.
- `python3 scripts/check-doc-contract.py` validates the decision document.
- `python3 scripts/check-knowledge-closure.py --strict` validates record closure.
- `test -f docs/decisions/CD-0153-contract-content-staleness-for-overlap-resolutions.md`
  verifies the amendment file exists.
