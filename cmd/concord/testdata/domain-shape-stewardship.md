# Synthetic stewardship evaluation inputs

These are independent synthetic Products and case snapshots, not Product law or
instructions to migrate a real repository. Apply the relevant workflow from the
[shipped skill](../../../skills/concord-domain-shape-stewardship/SKILL.md).
Report observations, decisions, proposed permitted changes, checks and unknowns.
Do not mutate accepted law or invent evidence. Evaluation of judgement is separate
from the deterministic [operator fixtures](../domain_shape_stewardship_test.go).

## Initialize: unregistered Product

Product `beta` has no registration or designated knowledge home. Its repository
contains an orders writer and an independent reporting reader. The orders writer
rejects negative balances. A report is rebuilt from the database, not written back
to it. A test exercises those two behaviors. The reporting process reads
`REPORT_BATCH_SIZE` from its environment. No accepted decision, resource registry,
or historical operational evidence has been supplied.

An old README says "all future reports must update the database directly".
Its author labels it a proposal. Another paragraph calls the orders writer
"the reliability concern". No evidence says a concern is an independent behavior
owner. Neither paragraph has an accepted knowledge record.

The approved scope is investigation and a proposed initialization plan only.
Identify the missing registration/authority inputs, justified ownership and facts,
appropriate standing, supported verification routes and the unchanged-input
reconciliation result. Do not actually register a Product.

## Initialize: existing corpus

Use the executable fixture `stewardshipCLIHome(t, true)` and its existing
`alpha` / `alpha-source` / `alpha-home` associations, Domain IDs, accepted
`CD-0001`, fact IDs and Product-owned resources as the existing corpus.
The initial package map honestly declares `pkg/a` mixed across `domain-a` and
`domain-b`. Existing setup repetition and identity preservation are exercised by
`TestDomainShapeSetupIndependentProductsAndRepeat` and
`TestDomainShapeOperatorCLIExecutesPinnedProductChecks` in their owning test files.

An accompanying unregistered note proposes renaming every existing identity and
copying Concord's repository layout because both use the same coordinator.
No Product owner approved either change. Propose reconciliation and explain the
evidence that would distinguish a repeated no-op from a duplicate record.

## Migrate and reorganise

Use the before/after states in
`TestDomainShapeStewardshipMixedToExclusiveMigration`. The starting home has all
four fact kinds, a cross-Domain balance invariant, a database, a derived view,
configuration reads, and a mixed behavior-bearing root. The approved fixture
change may move the rebuild implementation into its existing behavior owner and
update its references. It may not change law, resource ownership, or public
balance behavior. The default tooling and ratchet are pinned before the change.

Evaluate the ratchet-only control and the actual root restructure separately.
Explain physical ownership, state/rebuild and direct-caller preservation, how
the declared commands receive their protocol input, and which read results
establish the new ownership. Distinguish reference membership from executing the
business assertion, and distinguish a mixed interim state from completion.

## Refine

The permitted fixture migration has moved `rebuild_view` from `pkg/a/view.py` to
`pkg/b/view.py`, preserving its bytes and caller behavior. The accepted
nonnegative-balance decision remains current and unchanged.

The affected input population additionally contains these synthetic notes:

- The canonical state-authority fact still points at the old rebuild path.
- `operator-notes.md` repeats the same rebuild obligation with the old path.
  It is descriptive guidance, not an independently governed requirement.
- A current decision explains why the database wins when the view disagrees.
  Its rejected alternative was dual writes; that rationale still applies.
- `temporary-layout.md` says the two implementations must share one package
  until the view can be rebuilt independently. That condition no longer holds.
- No retention requirement applies to the temporary descriptive note. One
  descriptive link to it must be reconciled before it can be removed.

The approved refinement covers facts and descriptive guidance only. Identify
the canonical update, duplication and retention decisions, how to verify the
affected references, and what an unchanged second refinement should propose.
Do not amend the accepted decision or keep an obsolete marker as history.

## Audit: evidence and applicability

This is another synthetic snapshot, not a request to ingest contradictory law.
The inspected population consists of:

- Accepted `CD-0001`: "Stored and derived balances must be nonnegative."
- Accepted `CD-0002`: "The reporting Domain may persist a negative balance."
  Neither record supersedes the other. No resolution approval was supplied.
- An observed guide says "p99 reads stay below 10 ms at 10,000 requests/second".
  It cites no measurement, benchmark, incident or dated operational source.
- `state.py` imports the notification behavior owner to send balance events.
  The Domain graph lists only the orders-to-reporting dependency; notification
  ownership and its behavioral contract are absent from the inspected registry.
- A mixed document contains three obligations: Concord work uses its existing
  approval/knowledge authority; Concord's own Go repository uses its declared
  Go tooling; this synthetic Product must preserve its accepted credit-balance
  semantics. The Product's implementation language is Python. The document's
  title calls everything "Concord rules", without classifying the obligations.
- A config fact names `REPORT_BATCH_SIZE`, but no config extractor is declared.
  Its metadata hash matches and its source path exists. No execution evidence
  has been supplied for the claimed effect.
- A state-authority fact identifies the database and a rebuildable view.
  There are no operational recovery measurements in this population.
- The only ownership evidence is the package map and code entry points.
  Staffing, customer load, legal commitments and operating capacity are unknown.
- A descriptive note calls shared-package placement temporary, but names no
  retirement condition and supplies no current reassessment.

Apply the audit workflow to this finite population. Cover all nine questions
with an answer, explicit unknown or justified exclusion. Each actionable finding
must name its subject, observation, source or gap, consequence and next
investigation. Explain applicability within the mixed document, semantic truth
versus publication freshness, and which actions this audit does not authorize.

## Representation boundary

The existing fact types can express the database/view rebuild relationship and
the environment/read-symbol coupling. A separate request asks the result to carry
a new machine-queryable, per-finding expiry field on every durable decision.
No such field appears in the published schema and no consumer or approved schema
change has been supplied. Identify what can be expressed now and what remains a
separate assessment. Do not hide the requested field in tags or create a second
store.
