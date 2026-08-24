# CD-0065: The conformance corpus runs what production runs

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** How `scenarios/workflow-engine.v1.json` represents Product-changing
  workflows; the shipped definition classification; issues #400, #405
- **Approval:** Operator accepted the Option 1 proposal on 2026-08-23, answering
  the reviewer questions in
  [issue #405 comment](https://github.com/Sharper-Flow/concord/issues/405#issuecomment-5387211188);
  direction to merge and the remaining resolutions are recorded in
  [issue #405 comment](https://github.com/Sharper-Flow/concord/issues/405#issuecomment-5388341229)
- **Related:** CD-0012, CD-0013, CD-0041, CD-0053,
  [`workflow-engine-contract.md`](../workflow-engine-contract.md)
- **Preserves:** Scenario count and 1..48 numbering; the six evidence records
  that cite the corpus, which regain the anchors their text already claims
- **Supersedes:** nothing

## Context

The corpus predates the #400 collapse, and under the historical derivation it
had only ever run with Product-truth enforcement switched off: the shipped
version 4 classified `changes_product_truth` per work kind, while 47 of 48
scenarios pinned version 1, where the classification was uniformly false.

Domain overlap resolution, architecture binding, notice severity, and audit
supersession therefore had no conformance coverage for the three families that
trigger them. Authoring the true classification into the collapsed definitions
made that gap visible as 18 failing scenarios rather than as a silent absence.

## Decision

### D1. Shipped definitions classify; the corpus exercises the classification

The shipped built-in workflow definitions classify `changes_product_truth` per
work kind. No definition exists solely to give the conformance corpus a
non-Product-changing target.

### D2. Scenarios pin what production emits

Every conformance scenario pins the shipped definition version. A scenario may
not pin a definition production cannot emit.

### D3. Product truth is declared, never defaulted

Each Product-changing scenario declares its Domain home and architecture
binding explicitly in setup. No default or implicit Domain.

### D4. Overlap is exercised, including its refusal

Scenarios whose bindings intersect declare their overlap resolution explicitly.
At least one scenario exercises unresolved overlap blocking, so the refusal path
carries conformance coverage. Domains are separate by default; intersections
exist only where they earn coverage, held to the minimum that proves the path.

### D5. Numbering is preserved

Scenario count and 1..48 numbering are preserved, per
[`scripts/check-agent-contracts.py`](../../scripts/check-agent-contracts.py).

## Resolutions recorded with acceptance

Scenario `law_revisions` carry synthetic law IDs rather than real Git hashes,
which would need maintenance the corpus cannot give them. The WF48 expectation
of `current_step: acceptance` was correct; the defect was the version-keyed
gate that suppressed strict checks below version 2, removed under #400 rather
than accommodated.

## Consequences

- Domain overlap, architecture binding, notice severity, and audit supersession
  gain their first conformance coverage.
- The six evidence records citing the corpus — CD-0012 and CD-0053 in
  law-coverage, four floor-condition records in floor-readiness — now anchor to
  scenarios that execute the behavior their text claims.
- Scenario setup carries more authored Product law per scenario: Domain homes,
  bindings, overlap resolutions, severity, and supersession. This is the cost of
  D1 and it is accepted.

## Rejected alternatives

**Re-pin the 18 to a non-Product-changing family.** Smallest diff, and it
changes what each scenario tests. WF04 is the sole anchor for CD-0012's outcome
contract; re-pinning it would test that contract somewhere production never
applies Product-truth enforcement, leaving the records green while proving less
than their text claims.

**Keep the corpus non-Product-changing and add a second corpus.** Preserves the
48 accepted scenarios but requires shipping a definition that exists only to
keep the first corpus valid — reintroducing the accretion #400 removed — and
leaves two corpora to keep in sync.

## Verification

- `scenarios/workflow-engine.v1.json` holds 48 scenarios, each pinning the
  shipped definition version and its seven digests.
- Every Product-changing scenario declares `domain_home` and an architecture
  binding in setup; at least one declares unresolved overlap and is blocked.
- `internal/store` suite passes against the corpus, including
  `initializeWorkflowRawTx`, `CompleteWorkflow`, `ObserveWorkflowCompletionInput`,
  and `RebuildFromLog` over real SQLite.
- `docs/workflow-engine-contract.md` describes the collapsed single-version
  definitions and no longer documents v1/v2/v3 pinned replay.
- `python3 scripts/check-agent-contracts.py`,
  `python3 scripts/check-json.py`, and `python3 scripts/check-doc-links.py` pass.
