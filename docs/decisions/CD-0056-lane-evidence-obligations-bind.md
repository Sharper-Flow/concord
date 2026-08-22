# CD-0056: A lane report discharges its lane's evidence obligations

- **Status:** Accepted
- **Date:** 2026-08-22
- **Scope:** The `agent-lane-report.v1` evidence shape; the lane evidence
  obligation vocabulary; the `worker.completed` evidence boundary; issues #333
  and #334
- **Approval:** Operator directed the change on 2026-08-22; the public record is
  [issue #333 comment](https://github.com/Sharper-Flow/concord/issues/333#issuecomment-5377928019),
  covering issues
  [#333](https://github.com/Sharper-Flow/concord/issues/333) and
  [#334](https://github.com/Sharper-Flow/concord/issues/334)
- **Related:** CD-0017 (extends D4 and D6, the lane report contract),
  CD-0043 (does not disturb the methodology boundary), CD-0044 (evidence
  boundary), CD-0034 (host provenance, whose payload-version pattern this
  follows), CD-0008 (D6 payload evolution)
- **Preserves:** the worker authority boundary in full — a worker still records
  no transition, no verdict, and no completion, and still cannot accept its own
  result
- **Supersedes:** nothing

## Context

Every lane declares what it owes. `contracts/agent-lanes.v1.json` gives the
research lane `["source_citations", "bounded_findings", "uncertainties"]`, the
verify lane `["commands", "exit_codes", "failure_classification"]`, and so on
for all four.

Nothing reads those declarations. `LaneDefinition.EvidenceObligations` reaches
exactly two code paths: shape validation and the lane digest. Both treat the
entries as opaque tokens. No Go or TypeScript code compares a returned report
against them.

The report cannot support that comparison in its current shape.
`contracts/agent-lane-report.schema.json` types `evidence` as an array of bare
strings, so a lane's obligations and a worker's evidence share no vocabulary. A
report of `{"status": "completed", "evidence": ["done"]}` satisfies the contract.
Apart from the two-valued `status`, there is no shape a worker can return that
the contract distinguishes from that one.

The evidence then stops at the adapter. `WorkerCompletedPayload` carries
`attempt_id`, `readback_model`, and `report_schema_version`. What the worker
actually reported never becomes durable, so `worker.completed` records that an
attempt ended, not what it produced.

This reproduces the predecessor lesson recorded at
`docs/advance-predecessor-lessons.md`: a delegated result must be bounded and
validated by its producer. Concord declares the bound and validates nothing.

The failure vocabulary already anticipated this. `WorkerFailureInvalidReport`
has existed since the lane surface landed, with no code path that can reach it.

## Decision

### D1. A report entry names the obligation it discharges

`agent-lane-report.v1` evidence entries become closed objects:

```json
{"obligation": "verification_commands", "detail": "go test ./internal/store"}
```

`obligation` is drawn from the closed vocabulary in D2. `detail` keeps the
existing 1-512 character bound. The array bounds are unchanged.

### D2. The obligation vocabulary is closed and shared

The eleven tokens the four lanes already declare become a closed enum:
`source_citations`, `bounded_findings`, `uncertainties`, `files_touched`,
`verification_commands`, `unresolved_issues`, `contract_findings`, `severity`,
`commands`, `exit_codes`, `failure_classification`.

The same enum constrains `evidence_obligations` in `agent-lanes.schema.json` and
`obligation` in `agent-lane-report.schema.json`. Two enums that must agree are a
join, and an unvalidated join is a defect waiting for the first divergence, so
`scripts/check-agent-contracts.py` proves the two are identical and that every
lane's declared obligations are drawn from it.

Adding a lane obligation is therefore a contract edit in one vocabulary, not a
free string in a manifest.

### D3. A completion carries the discharged evidence

`worker.completed` gains the reported evidence, so what a worker produced becomes
durable at the same moment its attempt becomes terminal. The evidence is recorded
as reported. Concord does not summarize, score, or rewrite it.

### D4. An undischarged obligation is not a completion

A `worker.completed` event whose evidence leaves any of the dispatching lane's
declared obligations undischarged is refused with a typed failure. The refusal
happens in the fold, inside the transaction that would have made the attempt
terminal, because that is the only point where the attempt's lane identity and
the reported evidence are both in hand.

A refused completion is not a silent retry. The caller records `worker.failed`
with the existing `invalid_report` kind, which gives that constant its first
reachable producer.

Coverage is the bar: every declared obligation appears at least once. Concord
does not count entries, rank them, or judge their content. Whether the detail
text is any good is a review question, and review is a lane, not a validator.

### D5. The report contract changes in place at `schema_version` 1.0

No durable artifact holds a v1 report. Reports have never been parsed, never been
stored, and never reached an event payload; the only v1 producers in the tree are
test fixtures and eval packets, which this change updates. A version move would
migrate nothing.

It would also cost something real. `report_schema_version` is pinned by a SQLite
CHECK constraint on `worker_attempts` and asserted across the scenario corpus, so
moving it forces a migration to record an evolution that no stored row underwent.

This is the CD-0054 precedent — `contracts/agent-lanes.schema.json` lost
`pinned_model` in place — rather than the CD-0015 enum-and-conditional pattern,
which exists for manifests with readers in the field.

### D6. `worker.completed` moves to payload version 2, and legacy events say so

Stored v1 completions carry no evidence, and no upcaster can invent it. Rather
than tolerate an absent field and let one shape mean both "reported nothing" and
"predates the contract", the payload records which it is: `evidence_origin` is
`reported` or `legacy_unavailable`.

`upcastWorkerCompletedV1` sets `legacy_unavailable` with empty evidence. Every
newly appended completion must set `reported` and satisfy D4. Validation is total
in both directions, and a legacy completion is visibly legacy rather than
indistinguishable from an empty one.

### D7. The adapter parses the report it already receives

The adapter reads the worker's output, validates it against
`agent-lane-report.v1`, and carries the evidence into `worker-complete`. An
output that does not parse or does not validate becomes `worker.failed` with
`invalid_report`, not a completion.

This is where the boundary belongs. The adapter is the only component that sees
worker output, and CD-0044 already places evidence admission at the boundary
rather than inside the worker.

### D8. What this decision does not do

It does not connect lane evidence to the workflow `EvidenceKind` enum. Those
answer different questions — an obligation is what a lane owes its dispatcher, an
`EvidenceKind` is what a workflow requires before it may complete — and binding a
worker report to `workflow.evidence_bound` would give a worker reach into
completion evidence that CD-0017's authority boundary denies it.

It does not touch `agent-lane-packet.v1`. CD-0043 D5's no-version-move,
no-digest-churn condition holds: the obligation tokens are unchanged, so
`LaneDefinitionDigest` hashes the same content and every lane digest is stable.

It does not judge evidence quality, and it sets no bound on how large a
dispatched task may be. Granularity remains a property of the registered workflow
step graph under CD-0013 D1.

## Verification

- The obligation enum in `agent-lanes.schema.json` and
  `agent-lane-report.schema.json` is proved identical, and every lane's
  obligations proved a subset, by `scripts/check-agent-contracts.py`.
- A completion whose evidence covers its lane's obligations folds; one that
  leaves any obligation undischarged is refused with a typed failure and no
  projection change.
- A completion naming an obligation the dispatching lane does not declare is
  refused.
- A stored v1 completion upcasts to v2 as `legacy_unavailable`, deterministically
  and idempotently, and replays to the same projection.
- A v2 completion claiming `legacy_unavailable` is refused, and one claiming
  `reported` with empty evidence is refused.
- Every lane digest is unchanged from before this decision.
- The adapter turns an unparseable or invalid report into `worker.failed` with
  `invalid_report` rather than a completion.

## Rejected alternatives

**Type report evidence with the workflow `EvidenceKind` enum.** This was the
first shape considered, on the reasoning that Concord should not hold two
evidence vocabularies. Mapping the lanes to it settles the question:
`source_citations` is not a `verification`, an `artifact`, or a `durable_note`,
and forcing the choice would record a mapping decision as though it were the
worker's claim. The two vocabularies are not duplicates. See D8.

**Enforce in the adapter alone.** The adapter is a host component and the check
would be one implementation's behavior rather than Product law. A second adapter
would be free to skip it, and no stored event would show whether it ran.

**Enforce in `validateWorkerCompletedPayload`.** The validator receives the event
and its payload but not the attempt, so it cannot reach the dispatching lane.
Passing the lane identity in the completion payload would let a caller name a
lane other than the one it was dispatched under, which replaces a lookup with a
claim.

**Accept free-form obligation strings and match them loosely.** A lookup with a
silent default against externally authored keys is an unvalidated join that may
be matching nothing at all, and it would fail open on the first typo.

**Require every obligation in a `failed` report too.** A worker that failed may
have produced nothing to report, and demanding evidence it does not have would
push it toward manufacturing some. Coverage binds `completed` only.
