# CD-0073: A scenario override is honored by its binding or it is not in the corpus

- **Status:** Accepted
- **Date:** 2026-08-25
- **Scope:** `initial_state.fixture_override` in `scenarios/agent-jobs.v1.json`;
  the corpus runner's contract with a scenario binding; the two override keys on
  `AJ6-compact-terminal-work`; issue #179
- **Approval:** Operator accepted the consumption-tracking direction on
  2026-08-25 in the session that produced this record, choosing it over the three
  alternatives enumerated in
  [issue #179](https://github.com/Sharper-Flow/concord/issues/179)
- **Related:** CD-0033 (the corpus is the accepted oracle), #178 (which surfaced
  the unread keys while binding AJ6)
- **Preserves:** Every existing assertion, invariant, and deferral; the free-form
  typing of `fixture_override` in `contracts/agent-jobs-scenarios.schema.json`;
  AJ5's fold-replay override and the version accounting it depends on
- **Supersedes:** Nothing. This closes a gap the corpus never covered.

## Context

Two of the 23 agent-jobs scenarios declare `initial_state.fixture_override`, a
block stating the non-default world a scenario needs before its job runs.

`AJ5-atomic-supersession` declares two keys, and its binding honors them by
replaying two legitimate fold events through the store. `AJ6-compact-terminal-work`
declares two keys, and nothing reads either one.

Nothing detected the difference. The runner passes the whole scenario to a
binding and never asks what the binding did with it, so honoring an override is
a convention a binding author may follow or forget, with silence either way.

The AJ6 keys are worse than unread. Both are false:

1. **`work-cancelled.compaction_required: true`** names a field that does not
   exist. The string `compaction_required` appears exactly once in the
   repository, in the scenario itself. There is no such column on `work_items`,
   no such field on the fixture's `Work` struct, and no predicate by that name.
   Compaction eligibility is decided by a literal lifecycle test in
   `PublishCompactionLink` (`internal/store/knowledge_index_projection.go:131`):
   `lifecycle IN ('completed','cancelled','superseded')`. The fixture already
   seeds `work-cancelled` as `cancelled`, so the key restates an established
   fact in invented vocabulary.

2. **`canonical_home_project: "proj-api"`** contradicts what the system does.
   `pm1fixture.SeedKnowledge` registers `proj-web/repo-alpha-web` as the only
   `product_knowledge_homes` row, and `ResolveCompactionHome` returns that single
   candidate. The home belongs to `proj-web`. `proj-api` exists as a project and
   is `work-cancelled`'s primary project, but it carries no `canonical_path`
   locator and is nobody's product home. Had a loader honored this key, the
   scenario would have failed or demanded a fixture change.

A reader of AJ6 would reasonably conclude the scenario is pinned to `proj-api`.
It is not, and it never was. That is a false statement inside the artifact whose
whole function is to be the accepted oracle, and no check reports it.

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

The schema continues to type `fixture_override` as an open object. Closing the
key set was the cheaper option and was rejected, because it addresses a defect
that did not occur: AJ6's key names are well-formed, and a closed enumeration
would have admitted both.

Consumption tracking closes the class that enumeration cannot. Any key a future
scenario declares must be read by that scenario's binding, whether or not a
schema author anticipated the name. The open typing is now safe because the
runtime guard, not the schema, is what makes a key load-bearing.

### D4. The AJ6 override keys are removed from the accepted corpus

Both keys are deleted rather than implemented. Neither expresses a real
precondition: one names a nonexistent field, and the other asserts a home
ownership that contradicts the resolver. Implementing them would mean inventing
a `compaction_required` concept the system does not have, and re-homing the PM1
knowledge fixture onto `proj-api` to satisfy a line that was never read.

`AJ6-compact-terminal-work` keeps every assertion it had. It needs no override:
the PM1 fixture already seeds `work-cancelled` terminal, which is the only
precondition compaction actually requires.

## Consequences

- An unread override is now a test failure naming the exact keys, not silence.
- AJ5's binding reads its two keys and checks the replay implements what they
  state, so the corpus and the binding can no longer drift apart unnoticed.
- The stale comment in `agent_jobs_compaction_bindings_test.go` explaining that
  AJ6's override went unread is deleted along with the override.
- Future scenario authors get a runtime answer instead of a convention: declare a
  precondition and the binding must honor it, or the scenario fails.
- No production code changes. This record governs the oracle and its harness.

## Rejected alternatives

**Closing the `fixture_override` key set in the schema.** Rejected by D3. It is
the cheapest change and it would not have caught this defect. Both AJ6 keys are
spelled correctly and would appear in any enumeration written by the same author
who wrote them. A key-name check proves a name is legal, never that anything read
it.

**Teaching the runner to apply every override key generically.** Rejected because
the preconditions do not share a shape. AJ5's override is "replay two fold events
so version accounting stays intact," which does not reduce to assigning a value
to a path. A generic applier would grow a case per scenario inside the runner —
the same per-scenario code that exists today, moved somewhere it is harder to
read, and still with nothing checking that the case ran.

**Deleting the AJ6 keys and leaving the facility unguarded.** Rejected because it
fixes the instance and leaves the trap. The next author to declare an override
and forget the binding gets the same silence.

**Keeping the keys as documentation of intent.** Rejected. Neither key documents
a reachable intent: `compaction_required` describes a field the design does not
have, and `proj-api` describes a home the resolver does not produce. Retaining
them would preserve two false statements in the oracle for their narrative value.

## Verification

- The guard is proved against the real defect: restoring AJ6's two keys fails
  `TestAgentJobsCorpus/AJ6-compact-terminal-work` with both key names in the
  message.
- `TestFixtureOverrideConsumptionIsProven` exercises the tracker in isolation,
  covering an unread key, an undeclared key, a mistyped key, and a scenario with
  no overrides. The corpus alone cannot prove the guard works, because a passing
  corpus only shows the guard was quiet.
- `scripts/check-agent-jobs.py` passes with 23 scenarios, one of which now
  declares `fixture_override`.
- The claim in D4 is checkable: `compaction_required` appears nowhere in the
  repository after this change, and `ResolveCompactionHome` returns
  `HomeProjectID == "proj-web"` for `work-cancelled`.
