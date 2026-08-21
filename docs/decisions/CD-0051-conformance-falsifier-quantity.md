# CD-0051: The conformance falsifier weighs commit duration, not scheduler time

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** The sustained-P99 falsifier verdict in the ten-process acceptance
  conformance harness; issue #237
- **Approval:** Operator accepted the drafted decision as written on 2026-08-21; the
  public record is
  [issue #237 comment](https://github.com/Sharper-Flow/concord/issues/237#issuecomment-5369997670)
- **Related:** CD-0011, CD-0045 (refines D1), CD-0046,
  [`floor-readiness.md`](../floor-readiness.md)
- **Preserves:** the 100ms P99 target; correctness precedence; population
  authority; escaped-busy as a zero-quantity
- **Supersedes:** nothing; refines which measured quantity CD-0045's sustained
  falsifier weighs

## Context

CD-0045 D1 separates the concurrency law's measured quantities and states,
verbatim: "a rising admission wait on a loaded host is not a defect in Concord;
a rising commit duration is." The sustained falsifier nevertheless counted
rounds whose **wall** P99 exceeded the target, so a burstable-runner scheduling
hiccup produced `falsifier_status=fired` — a law-grade verdict CD-0011 lists
as reopening the storage authority — while every Concord-owned quantity
(escaped busy, lost effects, duplicates, invariant violations, commit
duration) was clean.

The 2026-08-21 PR-#232 failures are the measured signature: the unpaced
acceptance round held commit P99 at 15ms while paced rounds' overshoot lived
in begin-wait (111–179ms) under load 1.4–2.1. Wall and admission wait measure
scheduler-owned time; on a shared runner the scheduler is not Concord's.

## Decision

### D1. The sustained verdict weighs commit-duration P99

The falsifier's above-target count uses each production-like round's
commit-duration P99 against the unchanged 100ms target. Commit duration is the
time the write lock is held — the quantity CD-0045 names as Concord's own —
and a genuine lock-hold regression inflates it regardless of host load.

### D2. Wall and admission wait stay measured, diagnostic

Every round continues to report wall, begin-wait, and commit P99s separately,
plus load average. Overshoot in scheduler-owned quantities alone can no longer
fire the verdict; the numbers remain in the report for calibration and
regression triage. Nothing stops being measured.

### D3. Everything else is unchanged

The target value, correctness precedence (a failed correctness population
forces `inconclusive` and fails the run independently), population authority
(CD-0046), round counts, and the majority-of-rounds rule are untouched. This
decision moves one input: which quantity the above-target count reads.

### D4. The report names its verdict quantity

Production-like rounds carry `verdict_quantity: "commit_duration_p99"` so a
reader of a ConformanceReport never has to infer which number the verdict
weighed.

## Consequences

- Shared-runner scheduling noise no longer produces a law-grade `fired`
  verdict. The 2026-08-20/21 firing class (four documented false fires across
  three branches, two with zero `.go` changes) becomes `inconclusive` with
  full measurements retained.
- A real lock-hold regression still fires: it inflates commit duration in the
  same rounds, on any host.
- CD-0045's D1 bound on writer-admission wait remains law; on shared runners
  its sustained overshoot is reported-but-diagnostic, because that population
  cannot authoritatively measure scheduler-owned time. An isolated population
  that can measure it remains free to assert it.
- Rerun-as-triage stays available but should become rare, not institutional.

## Rejected alternatives

**Raise the 100ms threshold.** Rejected: weakens the falsifier for the
Concord-owned quantity to suppress noise in a quantity Concord does not own.

**Median-of-rounds.** Rejected: reduces variance by reducing sensitivity in
both quantities at once; the defect is which quantity is weighed, not the
aggregation.

**Runner-relative wall-clock ratio.** Rejected: derives a baseline from a
noisy control on the same host; commit duration already is the
host-independent signal.

**Formalize rerun-as-triage as law.** Rejected: institutionalizes not reading
red CI — the exact habit that would eventually wave a real `fired` through.

## Verification

- `internal/store.TestConformanceFalsifierFiresOnSustainedCommitDuration` —
  majority-of-rounds commit overshoot under accepted authority fires.
- `internal/store.TestConformanceSchedulerOvershootIsInconclusive` —
  wall+begin-wait overshoot with commit duration within target does not fire.
- Law coverage for this record is `satisfied` on those two anchors.
- Validators: `check-knowledge-index.py`, `check-law-coverage.py`,
  `check-json.py`, `check-doc-links.py`, `check-public-content.py`.
