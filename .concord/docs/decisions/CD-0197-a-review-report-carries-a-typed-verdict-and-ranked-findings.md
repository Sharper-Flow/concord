# CD-0197: A review report carries a typed verdict and ranked findings

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** The `agent-lane-report.v1` review block; the lane manifest's
  per-lane required-report-block contract; the `worker.completed` payload
  boundary; the adapter admission and attempt summary
- **Extends:** CD-0056 at D2, D4, D6, and D7, and the CD-0017 lane report
  contract
- **Preserves:** the worker authority boundary in full — a lane verdict
  records no transition, no workflow verdict, and no completion, and a worker
  still cannot accept its own result
- **Related:** CD-0017, CD-0043, CD-0056, CD-0058
- **Approval:** The operator accepted the work item contract that carries
  this record on 2026-09-30.

## Context

CD-0056 bound lane evidence to a closed obligation vocabulary. The review
lane declares a `severity` obligation, but the bound stayed free-text: any
detail of 1 to 512 bytes discharged it. Nothing in the report carried a
per-finding severity, a confidence, or a ship decision.

The coordinator therefore read the review outcome from prose. A lane report
that ranked nothing could discharge `severity` beside one that did, and
nothing could tell them apart. `verdict_kind` on the workflow step is a
different surface: it is the actor's own claim at `accept_worker_result`, not
a lane output.

A review exists to rank findings and to decide ship or no ship. That outcome
deserves a typed shape, the way `base_comparison` typed its checks in place.

## Decision

### D1. The report gains an optional typed review block

`agent-lane-report.v1` gains an optional top-level `review` object.
`schema_version` stays `1.0`; the `base_comparison` precedent
(CD-0043 D1) holds. The block carries:

- `verdict`: `ship` or `no_ship`.
- `findings`: an array of 0 to 64 findings.
- Each finding: `severity` from the closed `P0`-`P3` scale, `confidence`
  from the closed `low`, `medium`, `high` set, and a detail of 1 to 512
  bytes.

The scales are closed because closed tokens are checkable by schema and by
store. A numeric confidence invites a precision no reviewer can defend.

### D2. The lane manifest declares the requirement per lane, and the digest pins it

`contracts/agent-lanes.v1.json` gives each lane a `required_report_blocks`
list. The review lane requires the `review` block; every other lane requires
none. The field is part of each lane digest, so a dispatch pins the
requirement its worker ran under.

The generator projects the requirement into the Go registry, the TypeScript
registry, the lane contract docs, and the lane prompts. For the review lane,
the typed block discharges the `severity` obligation, so severity has one
source: a completed review report needs no separate free-text severity entry.
The store fold and the adapter admission refuse a free-text severity entry
beside the block. The gate is the carried block, not the live boundary, so
stored completions replay exactly as they were accepted.

### D3. The adapter and the store both refuse a review completion without the block

The adapter refuses at admission (CD-0056 D7), because it is the only
component that sees worker output. The store validates block shape at
`worker.completed` and enforces the per-lane requirement in the fold
(CD-0056 D4), because the fold is the one point where the attempt's lane and
the reported block are both in hand. A refusal is not a completion: the
caller records `worker.failed` with `invalid_report`.

The requirement binds completed reports only. A review that failed may have
ranked nothing, and demanding a block it does not have would push it toward
manufacturing one.

### D4. The verdict couplings are structural

The adapter and the store refuse a review block with a `ship` verdict and any
`P0` finding, and one with a `no_ship` verdict and zero findings. A `P0` is
by definition a ship blocker. A `no_ship` with no finding is an unexplained
verdict. Nothing else is ranked or judged: the content of a finding stays a
review question, not a validator question.

### D5. `worker.completed` moves to payload version 3

The v3 payload may carry the review block. Stored v1 and v2 completions
replay unchanged through an upcast (CD-0056 D6 pattern): the v2 upcast is the
bytes at the new version, and the fold forgives a missing block on replay.
No stored completion can satisfy a requirement that did not exist when it was
recorded, so the gate is live-only.

A dispatch recorded under the pre-CD-0197 review digest still resolves: the
digest joins the lane's legacy set, and the completion answers to the
requirement the current definition carries.

### D6. The lane verdict is report content only

The lane verdict maps to no workflow field and records no transition. The
coordinator reads the verdict and the per-severity finding counts from the
attempt summary, and records the workflow verdict through `record_verdict`
as today (CD-0056 D8; CD-0017 D4). An automatic mapping would hand a lane
workflow authority, and reusing the `verdict_kind` vocabulary in the report
would conflate two authorities.

## Alternatives considered

- Numeric confidence from 0 to 1. Rejected: false precision, no checkable
  meaning.
- Free-text severity with a keyword convention. Rejected: a heuristic, not a
  structure.
- Derive `verdict_kind` from the lane verdict at `accept_worker_result`.
  Rejected: it grants a lane workflow authority.
- Enforce the requirement in the adapter alone. Rejected: the store would
  admit an untyped review completion, and a second adapter could skip the
  check.
- Bump the report to `schema_version` 2.0. Rejected: one lane's block would
  force every lane and the SQLite CHECK to move for a migration no stored row
  underwent.
- Also refuse `ship` on a `P1` finding. Rejected: it turns a severity scale
  into policy the operator has not set.

## Consequences

A review report carries each finding with a closed severity and confidence,
and carries an explicit ship decision. The coordinator reads the verdict and
the finding counts as typed fields, and severity discharges through one
source instead of prose.

The lane verdict holds no workflow authority. The coordinator still owns the
workflow verdict, so a ship verdict and a `verdict_kind` can disagree without
either surface recording the other.

Stored completions replay unchanged, and a full rebuild succeeds. An
in-flight attempt dispatched before this change completes under the
requirement the current registry carries.

## Verification

```gherkin
Scenario: A review completion carries the typed block
  Given a dispatched review attempt
  When the coordinator records a completion that carries a valid review block
  Then the attempt completes and the block stays durable in the event payload

Scenario: A review completion without the block is not a completion
  Given a dispatched review attempt
  When the coordinator records a completion that carries no review block
  Then the store refuses with a typed failure and the attempt stays dispatched

Scenario: The verdict couplings refuse the two broken shapes
  Given a dispatched review attempt
  When a completion carries a ship verdict with a P0 finding
  Then the store refuses it, and a no_ship verdict with zero findings refuses too

Scenario: Severity has one source
  Given a dispatched review attempt
  When a completion carries the typed block and a free-text severity entry
  Then the store refuses it, and a block-only completion folds and replays

Scenario: A stored completion replays without the block
  Given a stored review completion recorded before this requirement
  When the log rebuilds
  Then the replay reaches the same projection, twice

Scenario: The adapter refuses at admission and the summary carries typed counts
  Given a completed review report without the block
  When the adapter admits the worker result
  Then the attempt records worker.failed with invalid_report, and a carried block reaches worker-complete and the attempt summary with per-severity counts
```

- `go test ./internal/store/ -run TestReviewLaneCompletionRequiresTheTypedReviewBlock` proves the first and second scenarios.
- `go test ./internal/store/ -run 'TestReviewLaneCompletionRefusesAFreeTextSeverityEntryBesideTheBlock|TestReviewLaneBlockOnlyCompletionFoldsAndReplays'` proves the fifth scenario.
- `go test ./internal/store/ -run TestWorkerCompletionReviewBlockShapeIsClosed` proves the verdict couplings and the closed scales at the store boundary.
- `go test ./internal/store/ -run TestWorkerCompletedV2ReplaysWithoutTheReviewBlock` proves the fourth scenario.
- `go test ./internal/store/ -run TestLegacyReviewLaneDigestCompletesWithTheRequiredBlock` proves the pre-CD-0197 dispatch identity still completes.
- `bun test adapter/opencode/dispatch.test.ts` proves the adapter refusals and the typed ride-through.
- `bun test adapter/opencode/lane_completion.test.ts` proves the attempt summary carries the verdict and the per-severity counts.
- `python3 scripts/generate-agent-lanes.py --check` proves the generated Go, TypeScript, docs, and lane prompts carry the requirement.
- `python3 scripts/check-doc-contract.py` proves this record carries the current decision outline and passes the writing rules.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0197 identifier allocates once.
