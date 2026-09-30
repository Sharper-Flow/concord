# CD-0175: The decision record outline is versioned

- **Status:** Accepted
- **Date:** 2026-09-23
- **Scope:** Durable decision records and the document contract that checks them
- **Amends:** CD-0114
- **Related:** CD-0153
- **Approval:** The operator approved this decision in chat after the complete first-item contract was presented.

## Context

The document contract reads one required-section list per record kind. The
decision list was empty, so an accepted decision needed no outline at all.
Many accepted decisions omit a section this Product expects a decision to
carry, and the rationale those sections hold cannot be written after the fact.

The checker held a second gap. It forbids a section titled exactly
`Acceptance criteria` on a decision, because only a spec may carry acceptance
criteria. Fourteen accepted decisions carry the heading as `Acceptance
Criteria`, and the exact-title match passed those sections without reading
them. The rule held by accident of spelling, not by contract.

A decision also risks stating its Domain twice. The knowledge manifest gives
every accepted law-bearing record exactly one home Domain in its record shard
field. A Domain heading in the body would create a second source for one fact.

## Decision

### D1. A current outline for new decisions

The manifest head declares, inside the decision contract, a current outline of
five exact headings: Context, Decision, Alternatives considered, Consequences,
and Verification. A decision on the current profile must carry every one, and
the checker fails a missing heading while enforcement is on. A heading check
proves structure alone; a reviewer reads each section for a real rationale.

### D2. A legacy profile authored in each record shard

Each decision record shard authors one field, `doc_contract_profile`, with the
value legacy or current. A legacy shard keeps the old outline: no required
sections, and no invented alternatives. The profile is authored metadata,
never inferred from a record date. The rule is active while the head declares
the current outline. A head without it keeps the unversioned behavior.

The legacy claim is bounded by a closed historical set. The schema freezes the
identifiers of the decisions accepted before this record. The knowledge-index
checker and the shard generator refuse a legacy claim outside that set, and a
current claim inside it. The vocabulary check binds the three declarations in
both directions. A newly accepted decision cannot exempt itself from the
outline by authoring its own metadata, because its identifier is outside the
frozen set.

### D3. One Domain, in the record shard

The shard field stays the one authoritative Domain statement. The current
outline names no Domain heading, and the checker refuses one on a
current-profile decision, so the manifest join keeps a single source.

### D4. Optional acceptance criteria, parsed under one rule

A current-profile decision may carry an acceptance-criteria section. The
checker finds every such section under either heading spelling and parses
each criterion with the Given, When, Then rule the spec contract already
uses. A criterion stated inside a fenced block parses with the same rule,
because the fence is the corpus's recorded criteria style. An invalid
criterion fails under either spelling and in either position.

A legacy-profile decision keeps its criteria section exactly as recorded.
The section is a preserved historical exception: the checker leaves it
unparsed, review reads it, and no criterion of the frozen set is re-graded
after the fact. The spec rule is unchanged: a spec still requires its
acceptance-criteria section, with its spelling and parsing as recorded.

## Alternatives considered

- Apply the five headings to every accepted decision and write the sections
  they lack. Rejected: it fabricates the recorded rationale of historical law.
- Leave the decision sections advisory and unverified. Rejected: a contract
  no check holds is prose, not law structure.
- Turn the manifest enforcement flag off for every record kind. Rejected: it
  weakens every kind to repair one gap.
- Keep the legacy set as one aggregate list in the manifest head. Rejected:
  an append to a list is data, not law. A new decision could name itself in
  that list and skip the outline, and the corpus check could not refuse it.
- Repeat the Domain in the body and compare it with the shard field.
  Rejected: two sources for one fact invite a heuristic join.
- Treat `Acceptance Criteria` as a distinct title the checker never matches.
  Rejected: that is the accidental distinction this decision closes.
- Select the profile from the record date. Rejected: a date is context, not
  a decision, and an edit would flip the outline silently.

## Consequences

A new decision carries the five sections, and a missing heading fails the
checker while enforcement stays on. The previously accepted decisions keep
their recorded content: none gains an invented section. The frozen set never
grows, so the legacy profile never gains a member. The reviewer obligation
moves to the sections themselves, because the check cannot prove that a
rationale is real.

The profile selection spans three bound declarations. The schema freezes the
historical set, the index checker and the shard generator enforce it, and the
vocabulary check keeps them identical in both directions. No file moves, no
canonical law format changes, and no retrieval path changes.

## Verification

- `python3 scripts/test-doc-contract.py` proves the profile selection: the
  current outline fails a missing heading and a fenced fake heading, a
  legacy decision stays compliant without its sections, and a decision with
  no authored profile takes the current outline.
- `python3 scripts/test-doc-contract.py` proves the criteria rule: optional
  criteria parse under either spelling, criteria inside a fenced block
  parse, an invalid fenced criterion fails, a second criteria section is
  parsed, a legacy criteria section stays unparsed, and a Domain heading is
  refused.
- `python3 scripts/test-knowledge-index.py` and
  `python3 scripts/test-generate-knowledge-index.py` carry the boundary
  regression: a new decision that requests the legacy profile fails, a frozen
  decision cannot claim the current profile, and no other kind carries a
  profile.
- `python3 scripts/check-doc-contract.py` passes over the registered corpus
  with this record on the current profile.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-vocabulary.py` prove the amended schema,
  the frozen set, and the two enforcement points stay canonical and bound.
