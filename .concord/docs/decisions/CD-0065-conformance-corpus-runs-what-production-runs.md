# CD-0065: The conformance corpus runs what production runs

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** How `.concord/scenarios/workflow-engine.v1.json` represents Product-changing
  workflows; the shipped definition classification; issues #400, #405
- **Approval:** Operator accepted the Option 1 proposal on 2026-08-23, answering
  the reviewer questions in
  [issue #405 comment](https://github.com/Sharper-Flow/concord/issues/405#issuecomment-5387211188);
  direction to merge and the remaining resolutions are recorded in
  [issue #405 comment](https://github.com/Sharper-Flow/concord/issues/405#issuecomment-5388341229)
- **Related:** CD-0012, CD-0013, CD-0222, CD-0053,
  [`workflow-engine-contract.md`](../workflow-engine-contract.md)
- **Preserves:** Stable IDs for current scenarios; obligations the corpus does
  not carry require their own current behavioral controls.

## Context

A conformance corpus that runs a definition production never emits proves
nothing about production. A corpus that runs only non-Product-changing
definitions leaves Product-truth enforcement without conformance coverage, while
the evidence records citing it stay green.

## Decision

### D1. Shipped definitions classify; the corpus exercises the classification

The shipped built-in workflow definitions classify `changes_product_truth` per
work kind. No definition exists solely to give the conformance corpus a
non-Product-changing target.

### D2. Scenarios select what production runs

Every conformance scenario selects a current production definition by
reference. A scenario carries no definition version pin and may not select a
definition production cannot run
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

### D3. Product truth is declared, never defaulted

A Product-changing scenario reaches its step through a named proof case. The
proof case approves its contract through the production `approve_contract`
action with an explicit architecture binding, which that action requires. The
runner infers no Domain.

### D4. Each obligation is proved where it is owned

The corpus proves workflow-action obligations end to end: the request runs
through the production dispatch boundary and the assertions read authoritative
rows and events. An obligation the corpus does not carry, such as Domain overlap
refusal, is proved by its own current behavioral control. An evidence record
cites the control that proves its own text.

### D5. Current scenarios keep their IDs

The corpus runs each scenario and its nested cases through the same production
action boundary. Each keeps its stable ID.

## Consequences

- Product-truth enforcement on the implementation workflow carries conformance
  coverage: outcome contracts, verdict-actor distinctness, completion rollback,
  evidence-commit binding, and owner acceptance of a lane result.
- Domain overlap refusal is proved by store tests, not by the corpus.
- Each Product-changing proof case seeds a Domain registry and approves an
  explicit architecture binding. This setup cost follows from D1 and is accepted.

## Rejected alternatives

**Re-pin Product-changing scenarios to a non-Product-changing family.** This is
the smallest change, and it alters what each scenario tests. The outcome
contract would be tested where production never enforces Product truth, leaving
its records green while proving less than their text claims.

**Keep the corpus non-Product-changing and add a second corpus.** This needs a
shipped definition that exists only to keep the first corpus valid, and leaves
two corpora to keep in sync.

## Verification

- `.concord/scenarios/workflow-engine.v1.json` holds twelve scenarios and
  fifteen executable cases. Each selects `workflow.implementation` by reference
  with no version pin.
- `internal/store.TestWorkflowScenarioCorpusExecutesExactProductionActions`
  runs every case through the production action boundary over real SQLite and
  asserts authoritative rows and events.
- `internal/store.TestCorpusCurrentProofPreparationProfiles` proves each proof
  case prepares its declared step and the production action returns the
  expected outcome or the exact typed refusal.
- `internal/store.TestWorkflowScenarioCorpusMutationsRerunAgainstAuthoritativeState`
  and `internal/store.TestWorkflowScenarioAssertionInterpreterRejectsMutatedExpectation`
  prove a mutated scenario or expectation fails.
- `internal/store.TestOverlapBlocksRemovesWorkFromReady` proves a recorded
  `blocks` resolution removes the blocked work from the ready queue.
- `python3 scripts/check-agent-contracts.py`,
  `python3 scripts/check-json.py`, and `python3 scripts/check-doc-links.py` pass.
