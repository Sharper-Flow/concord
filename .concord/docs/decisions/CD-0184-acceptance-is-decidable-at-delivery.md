# CD-0184: Acceptance is decidable at delivery

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** Approved-contract outcome predicates, the add_condition hold
  condition, and the generated authoring schema for the work-transition tool
  surface
- **Related:** CD-0012, CD-0013, CD-0018
- **Approval:** The operator approved this decision in contract version 1 of
  work item `work-8392894d5f50a158bf6910c1`.

## Context

An approved contract can name a post-delivery production observation as
acceptance. A check predicate whose subject is a production metric over a
seven-day window cannot reach a result when the change is delivered, so the
item cannot pass verification and strands. The live store carried five such
items in one Product, each stranded at execution or verify and later
superseded. The store reads `check_ref` and `immutable_subject_ref` as
pattern-only strings, so no registered evaluator can decide when a predicate
would run. No Product law said that acceptance must be decidable at delivery,
and the authoring schema did not teach the rule.

## Decision

### D1. A predicate names an end state verifiable at delivery

An approved contract's outcome predicates name only end states that
verification can decide when the change is delivered. A predicate whose result
depends on production behavior over a future window, such as traffic or an
error rate over hours or days, is not acceptance. That observation belongs to
a separate follow-up work item. The delivering item captures the follow-up and
links it `raised_from` before it completes (CD-0018 D3). The follow-up's own
contract is approved when the observation is due, so its acceptance is
decidable at its own delivery.

### D2. A hold must not wait for observation

An `add_condition` hold waits for an event that a declared authority resolves.
It must not hold the delivering item open to observe production over a time
window. A hold for observation strands the item for the whole window, and this
rule removes that failure.

### D3. One-shot live checks stay allowed

A check against a live system stays allowed when verification can decide its
result at delivery. An `ops_runbook` health check run right after the change
is delivered is acceptance, because its result exists when verification runs.
The line is the time of evaluation, not whether the subject is a production
system.

### D4. The authoring schema teaches the rule

The generated schema that agents read when they write contracts states the
rule at the field. The `outcome_predicates` array shared by `approve_contract`
and `supersede_contract` carries the rule in its description. The
`add_condition` `expected_within_seconds` field carries it as well.
`advertisedAdmissionTeachingGaps` reports a gap when the published schema
drops the rule, so generation and its tests fail on drift.

### D5. Runtime refusal stays deferred

This change adds no store refusal. The store reads check subjects as
pattern-only strings and cannot know a predicate's evaluation timing until
registered check evaluators declare it (CD-0013 D4). A refusal today could
only match text, and a heuristic cannot own correctness. Runtime refusal is a
named follow-up work item (`work-1c891f4b4249eb9ae6bbbf17`).

## Alternatives considered

- Refuse production-observation predicates in `approve_contract` now.
  Rejected: the store cannot know evaluation timing, so refusal would match
  text against `check_ref`, and the pattern gives both false positives and
  misses.
- Denylist duration and production tokens in `check_ref`, for example `7d` or
  `prod:`. Rejected: a text denylist is a heuristic with false positives and
  misses.
- Close `immutable_subject_ref` to commit or artifact subjects. Rejected: the
  delivering commit is unknown at approval, and live contracts use other
  subject prefixes.
- Hold the delivering item open with an `add_condition` timer. Rejected: the
  item strands for the whole window, which is the failure this rule removes.
- Amend CD-0012 D6 in place. Rejected: an accepted record changes through a
  new record, and D6 governs discovered scope, not predicate content.
- Build the registered check-evaluator registry in this change. Rejected: it
  touches thousands of live free-form check refs across Products, and it is
  larger than this rule.

## Consequences

A coordinator sees the rule in the schema the tool surface publishes, in every
Product, at the moment of authoring. New contracts name a time-window
observation only after the author reads the rule and chooses the follow-up
route. Existing stranded contracts stay unchanged, and their repair stays with
their Products. The rule is taught, not enforced, so operator review at
approval remains the check until the store can decide evaluation timing. The
structural closure, a registry of check evaluators that declare evaluation
timing, stays a named follow-up.

## Verification

- `python3 scripts/generate-agent-contracts.py --check` fails when any derived
  artifact drops the rule descriptions from the published schema.
- `bun test adapter/opencode/host-publication.test.ts -t delivery-decidable-rule`
  proves the published `concord_work_transition` schema teaches the rule and
  that `advertisedAdmissionTeachingGaps` reports its absence.
- `python3 scripts/test-agent-contracts.py` proves the generator refuses a
  projection without the rule.
- `python3 scripts/check-knowledge-closure.py` proves this record is
  registered in the manifest shards.
- `python3 scripts/check-doc-contract.py` proves this record's outline and
  style.
