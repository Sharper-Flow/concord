# CD-0046: Conformance population authority is invocation provenance, not measured host isolation

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** The "structural" population authority CD-0011 §*Accepted load
  profile calibration* claimed for the ten-process conformance verdict; the
  test entry point `TestTenProcessAcceptanceConformance`; the
  `ConformanceReport` vocabulary the harness emits; issue #223
- **Approval:** Operator accepted the drafted decision as written on 2026-08-20; the
  public record is
  [issue #223 comment](https://github.com/Sharper-Flow/concord/issues/223#issuecomment-5360747462)
- **Related:** CD-0011 §*Accepted load profile calibration* and §*Calibration
  record*, CD-0044 §D3, [`design-constraints.md`](../design-constraints.md),
  issue #223, issue #189, issue #187, issue #55
- **Preserves:** CD-0011's falsifier list, its retention decision, the
  accepted load profile, the correctness-precedes-latency precedence, and
  the diagnostic entry point under `CONCORD_CONFORMANCE_UNPACED=1`. The
  threshold itself and the pacing profile are untouched.
- **Supersedes:** nothing; amends the wording of a population-authority claim
  CD-0011 already decided the substance of

## Context

CD-0011 §*Accepted load profile calibration* states:

> Population authority is structural: only the isolated acceptance test entry
> point, run in the required `verify` check, may report falsifier `passed` or
> `fired`. Local and development runs are diagnostic and remain
> `inconclusive` even when the threshold is exceeded.

Two conditions appear in that sentence. Only the first was enforced.

The acceptance entry point
`TestTenProcessAcceptanceConformance`
(`internal/store/conformance_test.go`) called
`runTenProcessConformance(t, runnerProfileIsolatedAcceptance, true)` with the
profile hardcoded as a literal argument bound to the test function name. The
only environment gate was `CONCORD_CONFORMANCE_LONG=1`, which
`.github/workflows/ci.yml` set and nothing else. A developer running the same
command on a loaded workstation produced a report with
`runner_profile: "isolated_acceptance"`, `acceptance_population: true`, and
an accepted falsifier verdict of `passed` or `fired`. There was no observable
difference between that run and the required check.

Issue #189 had already recorded `begin_wait_latency` tracking host load
average while `commit_latency` stayed flat across configurations — the same
host-CPU-contention effect the CD-0011 calibration record had to correct once,
in issue #55. The gate measured the host as much as it measured Concord and
labeled the result authoritative. CD-0011's second condition — that local
runs "remain `inconclusive` even when the threshold is exceeded" — had no
code path implementing it.

CD-0044 §D3 already promoted the population-identity half of this clause into
the concurrency invariant. The remaining half — *who* may report an accepted
verdict — is what this decision binds.

## Decision

### D1. Population authority is invocation provenance, not measured host isolation

The conformance verdict's population authority is established by the CI
workflow setting the required-check signal
(`CONCORD_ACCEPTANCE_RUNNER=1`). That signal is the *provenance of the
invocation*. It does not measure host isolation: a busy CI runner still
passes the check; a quiet laptop that exports the signal would too. The
mechanism being replaced overstated itself as "structural", and this
replacement does not. No code or document describes the signal as
establishing isolation.

The signal's role is narrower:

- it is set by the workflow that runs the required check;
- it is *not* something an ordinary local run sets by accident;
- a CI run missing it must fail visibly rather than silently downgrading the
  required check to advisory.

The first and last clauses are what makes the signal meaningful; the second
is what keeps it from being a developer convenience.

### D2. A pure resolver decides the authority

`internal/store/conformance_test.go` exposes
`resolvePopulationAuthority(profile, acceptanceRunnerSignal)` that returns
`accepted | diagnostic` and a closed-set reason. The signal is passed in
rather than read from the environment so tests drive every combination
without spawning child processes or mutating the global environment.

Rules, in order:

- profile is not the acceptance entry point → `diagnostic`, reason
  `diagnostic_entry_point`;
- the acceptance-runner signal is not `"1"` → `diagnostic`, reason
  `required_check_signal_absent`;
- otherwise → `accepted`, reason `required_check`.

A `diagnostic` authority gates the falsifier verdict to `inconclusive`
regardless of the numbers observed. Correctness precedence is preserved: a
failed correctness population still forces `inconclusive` and still fails
the run independently.

### D3. The verdict is gated on the resolved authority, not the profile

`classifySustainedFalsifier` previously tested the runner profile literal.
It now takes the resolved authority and gates on it. An accepted `passed`
or `fired` is reachable only when the authority is accepted; every other
authority, including one with the threshold exceeded, remains `inconclusive`.

The falsifier threshold and the pacing profile are not weakened. This
decision is about who may report a verdict, not about the threshold or the
pacing.

### D4. The CI tripwire makes a missing signal a visible failure

When the process is running under GitHub Actions (`GITHUB_ACTIONS == "true"`)
and the entry point is the acceptance one and the acceptance-runner signal
is absent, the test fails with a message naming the missing environment
variable. A local run — no `GITHUB_ACTIONS` — must not fail; it reports
`inconclusive` and continues.

This tripwire is a pure predicate (`resolveCIRunnerTripwire`) so the four
combinations (entry point × `GITHUB_ACTIONS` × signal) can be tested
deterministically. The predicate fires only on the failing combination;
the other three pass through to the existing `inconclusive` reporting.

### D5. CD-0011's "structural" wording is amended to match the enforceable claim

CD-0011 §*Accepted load profile calibration* replaces "Population authority
is structural" with a sentence stating that population authority is
established by the required-check signal the workflow sets, not by host
isolation. The falsifier list, the retention decision, the accepted load
profile, and the correctness-precedes-latency precedence are preserved.

The diagnostic entry point under `CONCORD_CONFORMANCE_UNPACED=1` is
preserved: unpaced max-rate spin remains diagnostic-only stress evidence
and cannot produce an accepted verdict. The refusal lives in
`validateLoadPacing` and is unchanged.

### D6. Host load average is recorded as provenance, never as a gate

A reader of an existing report cannot tell a quiet run from a loaded one,
which is the confusion that produced this issue. `ConformanceReport`
records a best-effort 1-minute load average read from `/proc/loadavg` on
Linux. The field is omitted silently when unreadable.

This is provenance only. The value does not gate, classify, or influence
any verdict. A comment on the field and a comment on the reader record
this so a later reader does not wire it into the threshold.

## Consequences

- `TestTenProcessAcceptanceConformance` cannot emit `passed` or `fired`
  unless the resolved authority is `accepted`. A local invocation emits
  `inconclusive` regardless of the threshold outcome, and a CI invocation
  missing the signal fails visibly.
- `ConformanceReport` adds `population_authority`,
  `population_authority_reason`, and an optional
  `load_average_one_minute` field. `acceptance_population` is derived from
  the resolved authority, not the profile literal, so the two stay
  consistent.
- `.github/workflows/ci.yml` sets
  `CONCORD_ACCEPTANCE_RUNNER: "1"` next to the existing
  `CONCORD_CONFORMANCE_LONG: "1"` on the acceptance-conformance step. A
  workflow that drops the env var fails the required check instead of
  silently downgrading it.
- The acceptance profile's refusal of `CONCORD_CONFORMANCE_UNPACED=1` is
  preserved: unpaced max-rate spin remains diagnostic-only stress
  evidence.
- No third-party dependency. No generated file is touched. The CD-0044
  population-identity clause is preserved and the CD-0011 falsifier list
  is unchanged.

## Rejected alternatives

**Treat the signal as establishing isolation.** Rejected: a CI variable
records who ran the command, not how loaded the host was. Calling it a
host-isolation measurement would repeat the bug this decision exists to
fix.

**Strengthen the gate to require multiple signals or a host probe.**
Rejected: the only available host-isolation evidence is structural
(scheduler, container, cgroup) and Concord owns none of it. Adding
cgroup probing or a second env var would re-introduce the overstatement
the issue rejects: claiming to measure isolation while actually measuring
provenance. The honest claim is provenance, not isolation.

**Remove population authority from the gate entirely.** Rejected: that
makes every run authoritative, including a laptop under load, which is
the same defect the issue identifies from the other side. The falsifier's
reopen conditions are binding; the population guard keeps them pointed
at evidence Concord can trust.

**Reopen the falsifier threshold.** Rejected: out of scope, and the
issue's constraints forbid it. The threshold and pacing profile are
preserved; this decision is about who may report a verdict.

**Patch only the test, not the resolver.** Rejected: a patch at the test
call site is exactly the structural-overstatement shape that produced the
bug. The resolver makes the rule visible to any future reader of
`ConformanceReport`, not just to the test process.

## Verification

- `internal/store/conformance_test.go` exposes
  `resolvePopulationAuthority` and `resolveCIRunnerTripwire` as pure
  functions.
- `internal/store/conformance_status_test.go` adds table-driven tests
  for both predicates covering every input combination described in D2
  and D4, and for `classifySustainedFalsifier` covering diagnostic
  authority, missing signal, and non-`"1"` signal all remaining
  `inconclusive` when the threshold is exceeded.
- `internal/store/conformance_test.go` records
  `population_authority`, `population_authority_reason`, and
  `load_average_one_minute` on every `ConformanceReport`.
- `.github/workflows/ci.yml` sets `CONCORD_ACCEPTANCE_RUNNER: "1"` on the
  acceptance-conformance step.
- CD-0011 §*Accepted load profile calibration* is amended to state
  provenance, not "structural" isolation.
- `docs/concord-knowledge-index.v1.json` records CD-0046 once.
- `python3 scripts/check-doc-links.py`,
  `python3 scripts/check-knowledge-index.py`,
  `python3 scripts/check-floor-readiness.py`,
  `python3 scripts/check-public-content.py`, `gofmt -l .`, `go vet ./...`
  all pass.
