# CD-0046: Conformance population authority is invocation provenance, not measured host isolation

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** Population authority for ten-process correctness conformance
- **Related:** CD-0011, CD-0045,
  [`design-constraints.md`](../../docs/design-constraints.md) §1
- **Preserves:** SQLite retention, required ten-process correctness, and
  accepted-population zero escaped busy failures

## Context

A test entry point alone cannot distinguish a local invocation from the
required CI check. Conformance evidence must identify which invocation
produced it without claiming to measure host isolation. Native Task execution
supplies no conformance-population authority.

## Decision

### D1. Population authority is invocation provenance, not measured host isolation

The CI workflow sets `CONCORD_ACCEPTANCE_RUNNER=1` for the required acceptance
check. The signal records invocation provenance. It does not measure host
isolation: a busy CI runner still carries it, and a local process can export
it. Ordinary local runs must not set it as a convenience.

### D2. A pure resolver decides the authority

`resolvePopulationAuthority(profile, acceptanceRunnerSignal)` in
`internal/store/conformance_test.go` returns `accepted | diagnostic` and a
closed-set reason. The signal is passed in rather than read inside the
resolver, so tests can drive every combination deterministically.

Rules, in order:

- profile is not the acceptance entry point → `diagnostic`, reason
  `diagnostic_entry_point`;
- the acceptance-runner signal is not `"1"` → `diagnostic`, reason
  `required_check_signal_absent`;
- otherwise → `accepted`, reason `required_check`.

### D3. Correctness uses the resolved authority

Reports identify `population_authority` and `population_authority_reason`.
Any acceptance-population indicator derives from the resolved authority,
not from the profile alone. CD-0045 D2's zero-escaped-busy policy binds the
accepted population; lost effects, unexpected duplicates, and invariant
violations fail every population. Diagnostic evidence cannot substitute for
the required acceptance check.

### D4. The CI tripwire makes a missing signal a visible failure

When `GITHUB_ACTIONS == "true"`, the entry point is the acceptance one, and
the acceptance-runner signal is absent, the test fails with a message naming
the missing variable. A local invocation without `GITHUB_ACTIONS` continues
as diagnostic evidence.

`resolveCIRunnerTripwire` is a pure predicate. Tests cover every combination
of entry point, GitHub Actions state, and acceptance-runner signal.

### D5. Ten-process correctness requires no latency verdict

The acceptance workload and its retirement boundary are defined by CD-0045
D2 and D5. Population authority does not classify timing, require pacing,
or turn an unpaced correctness population into diagnostic-only evidence.
The shared provenance resolver and best-effort host-load reader remain
available for unrelated read-path P99 proof; host load never determines
population authority.

## Consequences

The required check fails visibly if its workflow drops
`CONCORD_ACCEPTANCE_RUNNER=1`. Local correctness evidence keeps its diagnostic
identity. Neither outcome asserts measured host isolation.

## Verification

- `internal/store/conformance_status_test.go` exercises the pure population
  resolver and CI tripwire across their input combinations, and verifies the
  accepted-population zero-escaped-busy policy.
- `.github/workflows/ci.yml` sets `CONCORD_ACCEPTANCE_RUNNER: "1"` on the
  acceptance-conformance step.
- `python3 scripts/check-doc-links.py`,
  `python3 scripts/check-knowledge-index.py`, and
  `python3 scripts/check-public-content.py` pass.
