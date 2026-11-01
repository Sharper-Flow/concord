---
{
  "id": "CD-0073",
  "title": "A scenario override is honored by its binding or it is not in the corpus",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "repository-verification",
  "applies_to_domain_ids": []
}
---
# CD-0073: A scenario override is honored by its binding or it is not in the corpus

- **Status:** Accepted
- **Date:** 2026-08-25
- **Scope:** `initial_state.fixture_override` in `.concord/scenarios/agent-jobs.v1.json`
  and the corpus runner's contract with a scenario binding
- **Approval:** Operator accepted the consumption-tracking direction on
  2026-08-25 in the session that produced this record, choosing it over the three
  alternatives enumerated in
  [issue #179](https://github.com/Sharper-Flow/concord/issues/179)
- **Related:** CD-0033 (the corpus is the accepted oracle), PM6 §1
- **Preserves:** Every current assertion, invariant, and deferral; the free-form
  typing of `fixture_override` in `contracts/agent-jobs-scenarios.schema.json`;
  AJ5's initial-state override and the version accounting it depends on

## Context

`initial_state.fixture_override` states the non-default world a scenario needs
before its job runs. A binding that ignores a declared override can pass while
testing a different precondition. Consumption tracking makes that mismatch a
test failure rather than a convention the binding author must remember.

## Decision

### D1. A binding proves it read every override its scenario declares

The runner installs an override tracker before invoking a binding and fails the
scenario when any declared key goes unread. A binding reads an override through
`sc.override` or `sc.overrideString`, which record consumption as a side effect
of returning the value.

Honoring an override and proving it are therefore the same act. A binding author
cannot forget the proof without also forgetting the behavior.

### D2. Reading an undeclared key fails

`sc.override` fails the test when the scenario declares no such key, rather than
returning a zero value. The guard runs in both directions: a scenario cannot
state a precondition nothing enforces, and a binding cannot claim to honor a
precondition the corpus never stated.

### D3. `fixture_override` stays free-form in the schema

The schema continues to type `fixture_override` as an open object. A closed key
enumeration would establish that a name is legal, not that its binding read it.

Consumption tracking closes the class that enumeration cannot. Any key a future
scenario declares must be read by that scenario's binding, whether or not a
schema author anticipated the name. The open typing is now safe because the
runtime guard, not the schema, is what makes a key load-bearing.

### D4. Publication scenarios have no completion obligation

[PM6 §1](../specs/canonical-git-note-placement.md#1-decision) requires no compaction
publication or reconciliation. AJ6 publication scenarios, including
`AJ6-compact-terminal-work` and `AJ6-partial-publication`, their assertions,
bindings and publication-only fixtures are not required and may be removed.
The consumed-override checks in D1–D3 and every assertion of current scenarios
remain required; publication retirement does not weaken them.

## Consequences

- An unread override is now a test failure naming the exact keys, not silence.
- AJ5's binding reads its two keys and checks the recorded state implements what
  they state, so the corpus and the binding can no longer drift apart unnoticed.
- Future scenario authors get a runtime answer instead of a convention: declare a
  precondition and the binding must honor it, or the scenario fails.
- No production code changes. This record governs the oracle and its harness.

## Rejected alternatives

**Closing the `fixture_override` key set in the schema.** Rejected by D3.
A key-name check proves a name is legal, never that anything read it.

**Teaching the runner to apply every override key generically.** Rejected because
the preconditions do not share a shape. AJ5's override requires writing two
work-state transitions so version accounting stays intact, which does not reduce
to assigning a value
to a path. A generic applier would grow a case per scenario inside the runner —
the same per-scenario code that exists today, moved somewhere it is harder to
read, and still with nothing checking that the case ran.

**Leaving the facility unguarded after removing an unused override.** Rejected
because the next author to declare an override and forget its binding would
receive the same silence.

## Verification

- `TestFixtureOverrideConsumptionIsProven` exercises the tracker in isolation,
  covering an unread key, an undeclared key, a mistyped key, and a scenario with
  no overrides. The corpus alone cannot prove the guard works, because a passing
  corpus only shows the guard was quiet.
- `scripts/check-agent-jobs.py` validates the current scenario corpus without
  requiring publication scenarios or a historical scenario count.
