# CD-0249: Decisions carry no coverage records

- **Status:** Accepted
- **Date:** 2026-10-29
- **Scope:** Per-law coverage records — their shards, schema, checker and
  generator scripts, CI enforcement, store storage, and read output; the
  typed-anchor identity for law evidence; computed product reachability and
  its declared exceptions; the shared coverage-state vocabulary; removal of
  dead and superseded material
- **Approval:** The operator approved this deletion for
  [Concord (CON) issue 1510](https://linear.app/sharper-flow/issue/CON-1510).
  It retires Concord's own law and an unwanted mechanism without per-deletion
  approval under CD-0243 D3, and delivers through an ordinary pull request
  with independent review and required CI. Its authority tier is `derived`
  under CD-0159 D3.
- **Related:** CD-0026 (the evidence-path drift audit), CD-0036 (breaking law
  cutovers), CD-0055 (repository check authority), CD-0114 (the knowledge
  manifest is composed from shards), CD-0159 (authority tier for product
  law), CD-0222 (law homes and the footprint read), CD-0244 (Domain facts),
  [`reachability-exceptions.v1.json`](../../docs/reachability-exceptions.v1.json)
- **Preserves:** computed reachability with declared exceptions; the shared
  state vocabulary and its live-pointer rule; the closed typed-anchor kinds
  with resolution and required-check wiring; the evidence-path audit

## Context

A hash proves a document is unchanged; it does not prove the implementation
meets the document. A decision's Verification section names its tests without
a second per-record proof ledger. Independent reachability, tooling and
Domain-fact checks still need their own evidence rules.

## Decision

### D1. Decisions carry no coverage records, states or anchors; a decision Verification section alone names its tests

The knowledge index holds no per-law coverage record: no coverage shard, no
per-record coverage state, no stored anchor row. A decision names its proof
in its own Verification section; nothing else tracks whether it is proved.

Per-law coverage is retired end to end: the coverage schema, the coverage
shards, the coverage checker and generator scripts, their CI enforcement, the
`law_coverage_anchors` store table with its guard triggers, the read path
that loaded it, the per-law checks in the footprint output, and the tests of
all of them. The removal runs through the existing breaking-migration path,
and the verified-snapshot preservation requirement applies before the table
drop: a failed migration step rolls back and leaves pre-upgrade catalog and
rows unchanged.

The knowledge-index machinery is untouched: shard composition, record
identity, hashes, and the manifest schema keep their current shape.

### D2. Reachability is computed; only exceptions are declared, under one state vocabulary with live pointers

Law binding is a human claim, which test proves which decision cannot be
derived. Reachability can be: a pinned `deadcode` callgraph over `./cmd/...`
derives it, including the method plus tx-scoped-core pairs throughout
`internal/store` that defeat textual analysis. The declaring plane states
only its exceptions in
[`reachability-exceptions.v1.json`](../../docs/reachability-exceptions.v1.json). The
validator establishes the complete subject set itself rather than iterating
the manifest's rows: an undeclared unreachable function or package is a
finding, and a declaration whose subject has become reachable is stale and
equally a finding. Exceptions may cover several symbols under one reason
where a single accepted decision governs them. The analysis runs
version-pinned.

Every declaring plane uses one state vocabulary, defined once in
`scripts/coverage_state.py`: `satisfied`, `outstanding`, `unmeasured`, and
`out_of_scope`, with `evidence` required when satisfied and forbidden
otherwise, `issue` required when outstanding, and `reason` required for
`unmeasured` and `out_of_scope`. `out_of_scope` declares a current
obligation outside the analysis entry set; it does not authorize dead code.
An `outstanding` pointer must name an issue that is still open — a closed
pointer is the same defect as a record with no referent at all. The
issue-state snapshot keeps its independent collection and liveness checks.

### D3. Typed anchors keep one closed identity, and each anchor resolves into a required check

Executable evidence is cited as a typed anchor from a closed set. The kinds
are `go_test`, `scenario`, `validator`, `generated`, and `adapter_test`:

| Kind | Value | Resolution |
|---|---|---|
| `go_test` | `<package>.TestName` | the test is declared in that package, or in a same-package helper that reaches a testing failure call, and `go test ./...` is a required CI step |
| `scenario` | corpus scenario ID | the ID is a unique direct entry in a `.concord/scenarios/` corpus and nothing defers it |
| `validator` | `scripts/<prefix>-<name>.py` | the script exists and a required workflow invokes it directly or nests it through `scripts/check-json.py` |
| `generated` | `<path>#<symbol>` | the symbol appears in an artifact carrying the generator marker |
| `adapter_test` | `adapter/opencode/<file>.test.ts#<test>` | the named test is declared in the named adapter test file |

A repository path is not an anchor, and presence alone does not prove
behavior. An executable anchor must be reached by a required check.
Domain-fact `enforced_by` anchors and project-tooling evidence bind through
these kinds. The set is closed and stays minimal: a new kind requires a
current use and a resolver.

### D4. Dead and superseded material has zero tolerance

Concord keeps only material that serves its current accepted design. This
rule covers production code, tests, fixtures, scripts, instructions,
generated agents, documentation, and law records. The change that replaces a
mechanism removes its unused branches, aliases, instructions, and
descriptions in the same change. Git history holds replaced designs; a
retained file is not an archive.

A test or fixture stays when it proves current behavior, including rejection
of retired input. Never delete validation, error handling, or observability
merely to reduce a finding count. Generated artifacts change through their
source and generator, not by hand.

Law keeps one canonical current statement per obligation. A compatible
amendment edits the owning record in place and states only the current rule.
A breaking replacement deletes the old record and, when a rule replaces it,
publishes that rule under a new law ID; no record keeps a superseded status,
a successor, or a supersedes relation. A non-law document that claims to
describe the current design and has been fully replaced is deleted with its
manifest entry. Historical research and lessons that still serve current
evidence are not obsolete design descriptions.

Required checks refuse mechanically established gaps: undeclared unreachable
Go functions and packages, and drift from generated registries. A declared
reachability exception is valid only for a current obligation outside the CLI
callgraph, such as a used test seam, a generator, or accepted audit
reconstruction. A hypothetical user, an old fixture, or the effort of
deletion justifies retaining nothing; D2 refuses stale exceptions.

Review decides whether prose describes the current design and whether a
retained mechanism still has a purpose. CI does not claim to infer either
from keywords. An existing cleanup that cannot land safely with the
enforcement change receives an owned follow-up naming the exact finding and
its removal end state. That follow-up does not waive a required check or
excuse new dead material.

## Consequences

1. A law change edits the decision's own text and its record shard; no
   coverage bookkeeping moves with it.
2. Closing work no longer waits on a coverage record that points at it.
3. The footprint read answers with governing Domains and current law
   revisions and carries no per-law checks.

## Verification

- `internal/store.TestMigrateV165ToV166RetiresLawCoverageStorageAndPreservesCurrentState`
  proves migration 166 drops the coverage table and its guard triggers
  while current knowledge, law, package-map, and work rows are
  preserved unchanged (D1).
- `internal/store.TestMigrateV165ToV166FailedStepRollsBack` proves a failed
  drop leaves the pre-upgrade catalog and rows unchanged and the upgrade
  completes once the blocker is gone (D1).
- `internal/store.TestKnowledgeIndexRebuildIgnoresLeftoverCoverageShards`
  proves an index rebuild at a head that still carries a coverage shard
  ignores it and stores no anchors (D1).
- `internal/store.TestFootprintResultCarriesNoLawChecks` proves the
  marshaled footprint payload carries no per-law checks (D1).
- `cmd/concord.TestUpgradeRetiringLawCoverageSnapshotsTheCoverageTableBeforeTheDrop`
  proves the upgrade route verifies a snapshot containing the original table
  and rows before dropping it (D1).
- `cmd/concord.TestUpgradeSnapshotFailurePreventsTheCoverageDrop` proves a
  snapshot failure leaves the schema and rows unchanged (D1).
- `python3 scripts/test-reachability.py` proves the reachability refusals in
  both directions — an undeclared unreachable subject and a declaration that
  has become reachable — and that state obligations and the live-issue rule
  hold for exceptions (D2).
- `python3 scripts/test-update-issue-state.py` proves an outstanding pointer
  that names a closed issue is a finding (D2).
- `python3 scripts/test-evidence-anchors.py` proves each anchor kind's
  resolution and required-check wiring, and
  `internal/store.TestArchitectureAnchorConstantsServeDomainFacts` proves the
  store's domain-fact anchors share the same closed kinds (D3).
- `python3 scripts/check-knowledge-index.py` proves record identity and that
  no record names a dangling evidence path (CD-0026 D4).
