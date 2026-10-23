# CD-0238: Point-in-time reconstruction runs outside the core

- **Status:** Superseded by [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
- **Date:** 2026-10-18
- **Scope:** Reading one subject's state at a past event sequence
- **Amends:** [CD-0008](CD-0008-concord-mechanism-hardening.md) D6 (point-in-time
  reconstruction bullet); [CD-0013](CD-0013-workflow-engine-mechanism.md) D13
  (`ReconstructSubjectAt` paragraph) and acceptance criterion 36;
  [CD-0002](CD-0002-concord-state-authority.md) §7 (the point-in-time
  reconstruction item of the CD-0008 D6 summary)
- **Preserves:** CD-0008 D6's upcaster, fail-closed, pinned-version,
  historical-fork, and snapshot rules; CD-0013 D13's total rebuild and
  acceptance criterion 35; the retained `domain_events` log as sole state
  authority
- **Approval:** 2026-10-18. Operator approval of this text for
  [Concord (CON) issue 1184](https://linear.app/sharper-flow/issue/CON-1184).

## Context

CD-0008 D6 allows read-only point-in-time reconstruction for audit and
diagnosis. CD-0013 D13 names `ReconstructSubjectAt` as the mechanism, and
criterion 36 requires it. No command, tool, or agent route calls it. Only tests
reach it, and the reachability manifest declared it out of scope. It kept a
second fold path, into a scratch database, that every new event family had to
keep working. The retained event log already answers the question it answered.

## Decision

### D1. The core ships no point-in-time reconstruction

Concord has no command, tool, store function, or conformance scenario that
reconstructs a subject at a past event sequence. The retained event log and
`RebuildFromLog` remain the history authority. A historical fork still creates
new linked work and never rewrites history.

### D2. Reconstruction is an operator-machine activity

An operator who needs a past state for audit or diagnosis reconstructs it on
the operator's machine, from a copy of the store, by folding a prefix of the
retained log. That activity never writes the live store and carries no Concord
workflow authority or evidence. An accepted decision that adds an operator verb
may restore a core route.

## Alternatives considered

- Keep the function as test-only code. Rejected: only tests exercised its fold
  path, and the reachability manifest had to excuse it.
- Add an operator verb now. Rejected: no accepted need asks for one, and R4 row
  12 places point-in-time diagnosis after the floor.

## Consequences

The `ReconstructSubjectAt` source file, its unreachable helpers, its unit tests,
and corpus scenario `WF36-point-in-time-reconstruction` are deleted. Corpus
number 36 is retired, not reused. Tests that used reconstruction as an oracle
assert through `RebuildFromLog` or a restored log prefix instead. CD-0008 D6's
reconstruction bullet now places reconstruction on the operator's machine.
CD-0013 D13 now states that the core has no point-in-time reconstruction, and
criterion 36 now reads: a restored prefix of the retained log, rebuilt, yields
the workflow state recorded at that sequence. CD-0002 §7 drops reconstruction
from its summary of CD-0008 D6. Floor-readiness requirement
`fc5-append-only-authority` keeps `WF35-rebuild-byte-equal` as its scenario
evidence.

## Verification

- [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) retires frozen-journal replay in favor of verified snapshots and current-row reconciliation; `python3 scripts/check-knowledge-index.py` checks this record's registration, not historical reconstruction.
- `internal/store.TestWorkflowScenarioCorpusExecutesExactProductionActions`
  runs the corpus with number 36 retired.
