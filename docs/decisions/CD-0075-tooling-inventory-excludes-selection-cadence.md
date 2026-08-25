# CD-0075: A tooling inventory excludes command-selection cadence

- **Status:** Accepted
- **Date:** 2026-08-25
- **Scope:** Removal of `cadence` from
  `.concord/tooling.v1.json`,
  [`contracts/project-tooling.v1.schema.json`](../../contracts/project-tooling.v1.schema.json),
  and `scripts/check-project-tooling.py`; mandatory schema/checker parity proof;
  remediation of PR #513
- **Approval:** Operator requested code review and authorized an in-scope merge
  when the result was sound on 2026-08-25. The remediation pull request is the
  public record.
- **Related:** CD-0043 (D1), CD-0074, issue #510
- **Preserves:** CD-0074 D2's path-containment rules, D4's probe exclusion, the
  `tier` cost fact, optional `cost_hint`, `config_path`, and `automation_path`
- **Supersedes:** CD-0074 D1 only where it requires `cadence`; D2's bad-cadence
  claim; D3's classification of `on_demand` as a fact; D5 in full; and the
  cadence clauses in CD-0074's consequences and verification

## Context

The post-merge review of PR #513 found that `cadence` crossed the boundary the
decision claimed to preserve. `routine` and `on_demand` describe when a project
expects an agent to select a command. CD-0043 D1 places command-selection method
in the host, never in Concord durable state. Calling the field declared intent
did not change its behavior.

The same review found that the schema/checker lexical parity test skipped when
the optional `jsonschema` package was absent. CI does not install that package,
so the required environment passed without running the proof.

## Decision

### D1. The manifest carries no command-selection schedule

`cadence` is removed from the schema, checker, tests, and dogfood manifest. An
entry describes a ready tool through its identity, purpose, invocation, cost
tier, optional cost hint, and referenced repository files. None of these fields
states when an agent should select the tool. A former `cadence` key is an
unknown field and fails validation.

This decision keeps infrequent tools visible without classifying their use.
`automation_path` can point to related automation, but the checker proves only
safe file resolution. It does not inspect that file or infer execution.

### D2. Contract parity has no optional dependency

The checker exposes its field sets, required fields, enum, regex, text bounds,
and path bounds as constants used by validation. The test reads the published
schema and compares those declarations directly. The test uses only the Python
standard library and cannot skip when a site package is absent.

Fixture tests still prove the lexical edge cases against the checker. The new
constant comparison proves that the schema declares the same edges without
requiring a second JSON Schema implementation.

### D3. Accepted law changes by narrow supersession

CD-0074 remains the historical record of the original surface. This decision
narrowly supersedes its cadence clauses instead of rewriting accepted law. All
other CD-0074 decisions remain in force.

## Consequences

- Agents can discover tools that never run in CI without receiving a command-
  selection policy from Concord.
- `tier` continues to describe cost. It never implies required or normal use.
- Schema/checker drift fails in the repository's required Python test suite,
  including under `python3 -S`.
- Projects with a `cadence` key must remove it when they adopt this corrected
  contract. PR #513 introduced no other project manifest before this correction.

## Rejected alternatives

**Rename `cadence` to `availability`.** Rejected because availability on the
current host is an external observation that CD-0074 D4 deliberately excludes.

**Keep `cadence` as operator intent.** Rejected because `routine` and
`on_demand` still tell an agent when to choose a command. Provenance does not
move methodology across CD-0043 D1.

**Install `jsonschema` in CI.** Rejected because the repository has no Python
dependency lock, the checker itself uses only the standard library, and direct
constant parity proves this contract without adding a dependency.

## Verification

- `python3 scripts/test-project-tooling.py` runs 24 tests with no skips.
- `python3 -S scripts/test-project-tooling.py` runs the same 24 tests.
- `python3 scripts/check-project-tooling.py` rejects `cadence` and accepts the
  dogfood manifest.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`,
  `python3 scripts/check-law-coverage.py`, and
  `python3 scripts/check-knowledge-index.py` pass.
