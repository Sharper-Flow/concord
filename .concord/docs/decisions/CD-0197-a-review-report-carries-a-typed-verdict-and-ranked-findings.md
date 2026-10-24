# CD-0197: A review report carries a typed verdict and ranked findings

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** The `agent-lane-report.v1` review block; the lane manifest's
  per-lane required-report-block contract; the `worker.completed` payload
  boundary; the adapter admission and attempt summary
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
The dispatch pins the report schema identity: `1.0` for pre-job reports,
`1.1` for job-capable reports.
A review report names one disposition, continue, correction required, revision required, or ready for final proof ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)). The block
carries:

- `findings`: an array of 0 to 64 findings.
- Each finding: `severity` from the closed `P0`-`P3` scale, `confidence`
  from the closed `low`, `medium`, `high` set, and a detail of 1 to 512
  bytes.

The scales are closed because closed tokens are checkable by schema and by
store. A numeric confidence invites a precision no reviewer can defend.

On an oracle-bound dispatch each ranked finding also carries a closed
`oracle` tie: `delivery_blocker`, `uncovered_case`, `follow_up`, or
`oracle_defect`. A delivery blocker binds a declared owner, a predicate,
pinned law, or control, and reproduction evidence. An uncovered case names
the declared owner, an omitted entry path or transition, and reproduction
evidence. It remains a legitimate blocker without an inventoried control.
An oracle defect names the defective case or control and evidence of that
defect. A follow-up does not become an unrelated repair obligation.

The core derives `finding:<event_seq>:<ordinal>` identities. Context findings
keep their zero-based ordinals; ranked findings follow at the context count
offset, with source kind `review_finding`. A supported `continues_finding_id`
retains the earlier owner, predicate/law binding, failure family, and canonical
open identity. A `variant_of` names an earlier finding of the same owner but
mints a new identity. Prose similarity never owns identity or shrink credit.
`resolved_findings` carries at most 32 closed claims within 8 KiB, each naming
one canonical finding and 1 to 8 unique evidence references (CD-0056).

### D2. The lane manifest declares the requirement per lane, and the digest pins it

`contracts/agent-lanes.v1.json` gives each lane a `required_report_blocks`
list. The review lane requires the `review` block; every other lane requires
none. The field is part of each lane digest, so a dispatch pins the
requirement its worker ran under.

The generator projects the requirement into the Go registry, the TypeScript
registry, the lane contract docs, and the lane prompts. For the review lane,
the typed block discharges the `severity` obligation, so severity has one
source: a completed review report needs no separate free-text severity entry.
The store admission and the adapter admission refuse a free-text severity entry
beside the block. The gate is the carried block, not the live boundary; completions are current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### D3. The adapter and the store both refuse a review completion without the block

The adapter refuses at admission (CD-0056 D7), because it is the only
component that sees worker output. The store validates block shape at
`worker.completed` acceptance and enforces the per-lane requirement there
(CD-0056 D4), because acceptance is the one point where the attempt's lane and
the reported block are both in hand. A refusal is not a completion: the
caller records `worker.failed` with `invalid_report`.

The requirement binds completed reports only. A review that failed may have
ranked nothing, and demanding a block it does not have would push it toward
manufacturing one.

Oracle enforcement joins the attempt's dispatched job revision, not the
current definition. Historical reports retain their recorded content and
requirements; nothing fabricates a tie, receipt, or closure ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### D4. The review reports one of four dispositions

The review reports one disposition, continue, correction required, revision required, or ready for final proof, and Main records its acceptance; an open in-scope blocking finding blocks both a new assignment and leaving execution ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).

### D5. A recorded completion reads back unchanged

Each terminal result is a recorded current fact, and no payload version, upcast, or replay reinterprets a recorded result ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

A dispatch recorded under the pre-CD-0197 review digest still resolves: the
digest joins the lane's legacy set, and the completion answers to its
recorded dispatch requirements.

### D6. The lane verdict is report content only

Main records its acceptance of the review's disposition, and the review report itself records no transition, no workflow verdict, and no completion ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).

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
and names one disposition (D1). The coordinator reads the disposition and
the finding counts as typed fields, and severity discharges through one
source instead of prose.

The lane disposition holds no workflow authority: Main records its acceptance
(D6), and the report itself records no transition.

Stored completions are current rows, so no replay or rebuild participates ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). An
in-flight attempt completes under its recorded dispatch requirements.

## Verification

```gherkin
Scenario: A review completion carries the typed block
  Given a dispatched review attempt
  When the coordinator records a completion that carries a valid review block
  Then the attempt completes and the block stays durable in the recorded completion

Scenario: A review completion without the block is not a completion
  Given a dispatched review attempt
  When the coordinator records a completion that carries no review block
  Then the store refuses with a typed failure and the attempt stays dispatched

Scenario: A disposition outside the four refuses
  Given a dispatched review attempt
  When a completion names a disposition other than continue, correction required, revision required, or ready for final proof
  Then the store refuses it and the attempt stays dispatched

Scenario: Severity has one source
  Given a dispatched review attempt
  When a completion carries the typed block and a free-text severity entry
  Then the store refuses it, and a block-only completion records and reads back unchanged

Scenario: A stored completion stays a recorded fact
  Given a stored review completion recorded before this requirement
  When the store reads it back
  Then the recorded content is returned unchanged, with no replay or rebuild

Scenario: The adapter refuses at admission and the summary carries typed counts
  Given a completed review report without the block
  When the adapter admits the worker result
  Then the attempt records worker.failed with invalid_report, and a carried block reaches worker-complete and the attempt summary with per-severity counts
```

- `go test ./internal/store/ -run TestReviewLaneCompletionRequiresTheTypedReviewBlock` proves the first and second scenarios.
- `go test ./internal/store/ -run TestReviewLaneCompletionRefusesAFreeTextSeverityEntryBesideTheBlock` proves the fourth scenario's refusal half; the implementing change binds the current-state anchor for stored completions, which read back unchanged with no fold or replay (CD-0242 D2).
- `go test ./internal/store/ -run TestWorkerCompletionReviewBlockShapeIsClosed` proves the closed scales at the store boundary.
- `go test ./internal/store/ -run TestLegacyReviewLaneDigestCompletesWithTheRequiredBlock` proves the pre-CD-0197 dispatch identity still completes, answering to its recorded dispatch requirements.
- `bun test adapter/opencode/dispatch.test.ts` proves the adapter refusals and the typed ride-through.
- `bun test adapter/opencode/lane_completion.test.ts` proves the attempt summary carries the verdict and the per-severity counts.
- `python3 scripts/generate-agent-lanes.py --check` proves the generated Go, TypeScript, docs, and lane prompts carry the requirement.
- `python3 scripts/check-doc-contract.py` proves this record carries the current decision outline and passes the writing rules.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0197 identifier allocates once.
- `TestOwnerOracleFinding`, `TestOwnerOracleLineage`, and `TestOwnerOracleReplay`
  cover classified verdicts and canonical lineage; the cutover retires the
  replay half, and its implementing change binds the current-state anchor for
  recorded results (CD-0242 D2).
