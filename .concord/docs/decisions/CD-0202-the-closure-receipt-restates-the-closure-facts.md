# CD-0202: The closure receipt restates the closure facts

- **Status:** Accepted
- **Date:** 2026-10-02
- **Scope:** The closure receipt rows and content law, the `record_proposal`
  payload, the `workflow_proposal_records` projection, and the closure store
  read
- **Amends:** CD-0169 at D2 — the single-column table gains four optional
  sections and a prose cell width; CD-0170 at D1 — a completed receipt may
  restate recorded proposal, delivery, and relation facts beside the verified
  predicates
- **Preserves:** CD-0169 D1's one-renderer rule and D3's adapter delegation;
  CD-0170 D2's degrade-by-omission rule; every released definition digest
- **Related:** CD-0115, CD-0166, CD-0169, CD-0170, CD-0184
- **Approval:** The operator approved the approach in session chat on
  2026-10-02, including the dedicated closure read, the proposal
  `out_of_scope` list, the one-hundred-twenty-code-point prose width, and the
  direct `raised_from` follow-up scope.

## Context

A completed work item prints a closure receipt. The receipt shows the plane
line, the title, and one row per verified contract predicate. The operator
cannot see from it which problem the work solved, which change delivered it,
which scope the contract excluded, or which follow-up work the item raised.

The store holds each missing fact. The recorded proposal states the problem
and may exclude scope. The delivery assertion names the delivered change, and
a correction overlays it. The `raised_from` relations name the follow-up
work, and a confirmed Linear link names its issue key.

CD-0170 D1 limited receipt content to the verified predicates, so the gap is
law, not a rendering defect. This record widens that law with its own record
because CD-0169 rejected in-place amendment of a sibling record.

## Decision

### D1. One closure read owns the receipt's store facts

The store adds `ReadWorkClosure(ctx, s, workID)`. It opens one read-only
transaction and returns the pin beside four facts: the latest proposal
problem, the proposal's excluded scope, the effective delivery artifact with
the correction overlay applied, and the work items linked `raised_from` the
closed item with their confirmed Linear keys. `receipt.RenderForWork` calls
this one read. The facts do not join `WorkPin`: the pin rides every mutation
envelope and has a generated payload schema, while the closure facts serve
one consumer at one moment.

A proposal without scope, an item without a delivery assertion, and an item
without raised relations each read as an empty section. An unknown work item
and an unreadable projection stay store failures.

### D2. The table gains four optional sections

The receipt keeps its single-column markdown table. The rows render in this
order: the plane line, the title summary, the problem, the shipped artifact,
one row per verified predicate, one row per excluded-scope entry, and one row
per follow-up. The problem row carries U+2753, the shipped row U+1F4E6, each
left-out row U+2796, and each follow-up row U+1F517. A follow-up label joins
its Linear key, or its work ID when no confirmed key exists, to the item
title with the shared middle dot.

An empty section renders no row. A receipt whose closure carries none of the
new facts renders exactly the bytes the pin alone produced before this
record, and the golden tests hold those bytes.

### D3. Prose cells hold one hundred twenty code points

A problem statement, an excluded-scope entry, and a follow-up title truncate
to one hundred nineteen code points plus an ellipsis, with the visible-cut
rule the criterion labels already follow. The criterion labels keep the
sixty-four-code-point width. One long statement cannot dominate the banner,
and the problem row still carries its meaning.

### D4. The proposal carries the excluded scope

`record_proposal` gains an optional `out_of_scope` prose list of zero to
sixteen entries beside its sibling optional lists. The list is bounded by the
shared prose rules: each entry carries one to five hundred twelve characters,
and the entries are unique.

`workflow.implementation` version 22 ships the payload declaration. The
column `workflow_proposal_records.out_of_scope` is additive and defaulted to
an empty array, and the migration is recorded as non-breaking. Every released
definition version keeps its digest, and a pinned instance that submits the
field still refuses it as undeclared. The generated agent contracts project
the new payload for the current version and keep the released payloads for
the pinned versions.

### D5. Follow-ups are the direct raised_from successors

The receipt lists each work item whose `raised_from` relation targets the
closed item. The relation is not transitive, so the depth stays at one. Any
lifecycle counts, and the rows follow creation order. A confirmed Linear
issue key renders before the work ID fallback.

### D6. Content law

CD-0170 D1 as amended: a completed receipt restates the verified contract
predicates and the recorded proposal, delivery, exclusion, and relation facts
the store holds. The verified predicates remain the only proof rows. The
degrade rule stays CD-0170 D2, and the adapter keeps zero formatting logic
under CD-0169 D3: it shells the `concord receipt` verb and queues the bytes.

## Alternatives considered

- Add the closure facts to `WorkPin`. Rejected: the pin rides every mutation
  envelope and carries a generated payload schema, and the closure facts
  serve one consumer at one moment.
- Reuse the proposal's constraints list for left-out scope. Rejected: a
  constraint limits how the work is done, not what it excludes, so the
  receipt would mislabel it.
- Add exclusions to `approve_contract`. Rejected: the same definition-version
  cost plus a contracts column, and the contract owns verifiable predicates,
  not prose scope.
- Show only follow-ups as left-out scope. Rejected: scope dropped without a
  follow-up item would stay invisible.
- Keep the sixty-four-code-point width for every row. Rejected: the cap cuts
  most problem statements to a fragment.
- State no width for prose rows. Rejected: one long problem dominates the
  banner.
- Include `depends_on` and `blocks` edges among the follow-ups. Rejected:
  those are scheduling relations, not follow-ups of this item.
- Amend CD-0169 and CD-0170 in place. Rejected: that rewrites accepted law
  without a record of the change.

## Consequences

The operator reads, at close, what problem the work answered, what change
delivered it, what scope the work excluded, and what follows. The receipt
gains the sections without a second renderer: one package owns the bytes,
one verb prints them, and the adapter stays a delegate. Old items render
byte-identical receipts, so no consumer of the old shape breaks.

The proposal's excluded scope becomes recorded fact rather than chat memory.
Pinned workflow instances keep their released payloads, and new instances
record scope on the current version.

## Verification

```gherkin
Scenario: The receipt renders the four sections in order
  Given a completed work item with a recorded proposal, a delivery
    assertion, excluded scope, and two raised follow-ups
  When the receipt renders for the item
  Then the rows follow the plane line, the title, the problem, the shipped
    artifact, the verified predicates, the left-out entries, and the
    follow-ups, and the first follow-up names its confirmed Linear key

Scenario: A receipt without the closure facts keeps the pin bytes
  Given a completed work item whose closure carries none of the new facts
  When the receipt renders for the item
  Then the bytes match the receipt the pin alone produced before this record

Scenario: A correction overlays the shipped row
  Given a completed item whose delivery assertion a correction overlays
  When the closure read runs for the item
  Then the shipped artifact is the correction's merge evidence

Scenario: The current definition accepts the excluded scope
  Given workflow.implementation version 22 and a bounded out_of_scope list
  When record_proposal validates the payload
  Then the list persists, survives a rebuild, and renders as left-out rows

Scenario: A released pin refuses the excluded scope
  Given workflow.implementation version 21 pinned before this record
  When record_proposal validates a payload that carries out_of_scope
  Then the field refuses as undeclared and the pin's digest holds

Scenario: The prose width truncates visibly
  Given a problem statement longer than one hundred twenty code points
  When the receipt renders for the item
  Then the problem cell ends with an ellipsis at the prose width
```

- `go test ./internal/receipt/` pins the table bytes, the section order, the
  marks, the legacy bytes, the prose width, and the degrade paths.
- `go test ./internal/store/ -run 'TestReadWorkClosure|TestRecordProposalOutOfScope'`
  proves the closure read, the correction overlay, the bare degrade, and the
  version boundary.
- `go test ./internal/store/ -run 'TestWorkflowDefinitionVersionPins|TestBuiltinDefinitions'`
  proves every released digest holds and the new version registers once.
- `go test ./internal/store/ -run TestMigrationChecksumPinsMatchDefinitions`
  proves the additive migration and its checksum pin.
- `python3 scripts/generate-agent-contracts.py --check` proves the projected
  contracts match the registry.
- `python3 scripts/check-doc-contract.py` proves this record carries the
  current decision outline and passes the writing rules.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0202
  identifier allocates once.
