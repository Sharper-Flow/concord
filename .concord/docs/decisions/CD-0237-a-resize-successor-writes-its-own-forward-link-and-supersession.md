# CD-0237: A resize successor writes its own forward link and supersession

- **Status:** Accepted
- **Date:** 2026-10-18
- **Scope:** Which writer creates the relations of a CD-0229 D6 resize
- **Amends:** [CD-0224](CD-0224-ten-planning-and-browsing-operations-are-retired-from-the-agent-surface.md),
  its list of relation writers and its Consequences bullet "Every surviving
  relation has exactly one writer and one stated approval rule"
- **Preserves:** CD-0224's three writers and their approval rules;
  [CD-0229](CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md)
  D6; the absence of any reopen operation
- **Approval:** 2026-10-18. Coordinator approval of this exact text under the
  operator's delegation for
  [Concord (CON) issue 1090](https://linear.app/sharper-flow/issue/CON-1090).
  The change is serious under P12 and CD-0230 D4: its computed footprint
  covers more than one Domain, and the change is `crucial`. It amends CD-0224
  rather than replacing it (CD-0036 D2), because it records only the writer
  CD-0229 D6 already accepted.

**Approval amendment:** [CD-0243 D7](CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain unchanged.

## Context

CD-0229 D6 says that when a size changes, "a successor work item at the new
size supersedes the work item through a forward link". CD-0224 names three
relation writers, and none of them writes that pair.

## Decision

### D1. The resize is a fourth relation writer

A size revision that changes a work item's effective size writes one
`forward_link` and one `supersedes` from the successor in the same transaction
that creates the successor and ends the item. Its approval rule is the size
decision that changed the size: the decision model's rerun or the operator's
override.

`supersedes` therefore has two writers, `resolve_overlap` and the resize, and
`forward_link` has two, `link_successor` and the resize. Each writer keeps its
own approval rule, and `resolve_overlap` keeps its operator approval. No other
route writes either kind.

## Alternatives considered

- Route the resize through `resolve_overlap` and `link_successor`. Rejected:
  `resolve_overlap` needs an operator approval pinned to both contracts,
  and the successor has none when the resize creates it, so a model rerun
  could never resize.
- Supersede without a forward link. Rejected: CD-0229 D6 names the forward
  link, and readers follow it from the item to its successor.

## Consequences

CD-0224 now names four relation writers, and its Consequences bullet reads
that every writer has one stated approval rule, with two writers each for
`supersedes` and `forward_link`. The superseded item keeps its events,
evidence, and approvals; its successor reads that evidence by reference and
copies none of it. No reopen operation exists, so a resize cannot be undone
except by a further size revision on the successor.

## Verification

- `internal/store.TestResizeSuccessorSupersedesWithoutCompletion` proves the
  resize writes exactly one `forward_link` and one `supersedes` and ends the
  item as `superseded`, not `completed`.
