# CD-0057: The commit-duration falsifier requires a clean unpaced control

- **Status:** Accepted
- **Date:** 2026-08-22
- **Scope:** CD-0051 D1 refinement; issue #341
- **Approval:** Operator accepted the drafted decision as written on 2026-08-22; the
  public record is
  [PR #341 comment](https://github.com/Sharper-Flow/concord/pull/341#issuecomment-5381037907); the orchestrator posts acceptance on PR #341 after review, and the PR thread is the record.
- **Related:** CD-0051 D1, CD-0045, CD-0046, CD-0011, issue #341
- **Preserves:** the 100ms P99 target; correctness precedence; population authority; the majority-of-paced-rounds rule; CD-0051 D2's diagnostic wall and begin-wait measurements
- **Supersedes:** CD-0051 D1's assumption that commit-duration tails are host-load-independent

## Context

The PR #341 conformance run showed paced production-like commit-duration P99s of
131ms, 49ms, and 117ms, with p50 at 1ms, correctness fully green, and load
between 1.0 and 1.2. The paced majority therefore exceeded the 100ms target.
The same run's unpaced acceptance round had commit-duration P99 of 2ms. The
data shows that scheduler descheduling can inflate a paced commit tail while a
write lock is held even when lock-held work is unchanged; the uniformly low p50
does not make that tail a real lock-hold regression.

| Round | P99 commit duration | P50 commit duration | Host load | Correctness |
| --- | ---: | ---: | ---: | --- |
| Paced production-like 1 | 131ms | 1ms | 1.0–1.2 | Green |
| Paced production-like 2 | 49ms | 1ms | 1.0–1.2 | Green |
| Paced production-like 3 | 117ms | 1ms | 1.0–1.2 | Green |
| Unpaced acceptance control | 2ms | — | 1.0–1.2 | Green |

## Decision

### D1. The sustained verdict requires an unpaced control gate

The production-like rounds continue to determine `threshold_status`: a majority
of paced rounds whose commit-duration P99 exceeds the unchanged 100ms target
sets `threshold_status=exceeded`. The accepted `fired` verdict additionally
requires the unpaced acceptance round's commit-duration P99 to exceed the same
target. The control is the in-run control: it uses the same binary, database,
and runner without pacing, so it removes arrival-burst scheduling pressure from
the comparison.

When the paced majority exceeds the target but the unpaced control is clean,
the result is `falsifier_status=inconclusive` while
`threshold_status=exceeded` remains. The report logs the paced worst P99 and
the control P99 with the reason `host scheduling`.

### D2. Existing precedence and diagnostic measurements stay unchanged

Correctness still precedes every latency verdict, and only the resolved
acceptance population may emit `passed` or `fired`. Wall and begin-wait P99s,
load average, and the other conformance measurements remain reported and
diagnostic. CD-0051 D2 is unchanged.

## Consequences

- The paced scheduler-descheduling noise class is closed for commit-duration
  verdicts: a paced overshoot with a clean control is inconclusive, not fired.
- A real lock-hold regression still fires because it inflates the unpaced
  control's commit-duration tail as well as the paced rounds.
- The 100ms target, correctness gate, population-authority gate, and
  majority-of-paced-rounds threshold remain unchanged.
- The production-like report carries `control_commit_p99_ms`, making the
  control value visible beside the verdict inputs.

## Rejected alternatives

**Rerun as triage.** Rejected: a rerun is outside the original evidence and
cannot make the first law-grade verdict reproducible.

**Raise the target.** Rejected: it would weaken detection of a real
commit-duration regression to suppress scheduler noise.

**Gate on p50.** Rejected: p50 catches only gross regressions and would miss a
tail regression that the falsifier must detect.

## Verification

- `internal/store.TestConformanceFalsifierFiresOnSustainedCommitDuration` —
  paced majority overshoot with a dirty control fires.
- `internal/store.TestConformanceSchedulerOvershootIsInconclusive` —
  wall/begin-wait overshoot with clean commit duration remains passed.
- `internal/store.TestConformancePacedCommitOvershootWithCleanControlIsInconclusive` —
  the PR #341 paced-overshoot signature is inconclusive with a clean control.
- `internal/store.TestConformanceControlCommitOvershootFires` — paced majority
  and control overshoot fire.
- `check-cd-allocation.py` confirms CD-0056 has no number collision.
- The knowledge index and law coverage are regenerated from the per-record
  shards and pass their validators.
