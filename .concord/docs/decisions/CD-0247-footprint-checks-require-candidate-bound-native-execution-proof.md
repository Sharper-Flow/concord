# CD-0247: Footprint checks require candidate-bound native execution proof

- **Status:** Accepted
- **Date:** 2026-10-27
- **Scope:** Applicable footprint checks at shared delivery/completion admission;
  declared-tool bindings; native package-complete proof
- **Amends:** [CD-0205](CD-0205-worker-jobs-bind-dispatch-and-separate-local-acceptance-from-delivery.md) D1 and D2 for candidate-sourced package tests under a distinct native recipe kind
- **Refines:** [CD-0074](CD-0074-project-tooling-declaration.md), [CD-0090](CD-0090-per-turn-continuity-re-pin-hook.md), and [CD-0229](CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md) D5
- **Preserves:** [CD-0192](CD-0192-the-refine-pass-proves-a-declared-analysis-ran.md)'s default-ref declaration, one-declared-command refinement gate, and invocation grammar; accepted Review; existing packet/result limits
- **Approval:** The operator approved the exact seven rules and the bounded
  package-proof extension on 2026-10-27.

## Context

A footprint lists applicable checks. A tooling declaration names commands.
Neither establishes that a command executes an anchor or that its witnesses
passed on the candidate. Delivery and completion consumers need that distinction
to refuse unrelated green runs and incomplete proof.

CD-0205 D1 replaces candidate harness inputs with pinned recipe bytes. D2 says
the candidate's changed harness does not replace the pinned recipe. Those rules
serve independently pinned behavioral witnesses, but do not establish execution
of the candidate's complete package test inventory.

CD-0090's coverage shard claimed satisfaction while continuity scenario bindings
and execution proof remained incomplete. Source presence and registration do
not discharge those obligations.

## Decision

### D1. Shared delivery and completion require footprint execution proof

Shared delivery/completion admission requires the following proof for every applicable check in the current footprint, independent of work size. Existing admission requirements remain in force.

1. **Executable binding.** Each required check resolves by typed anchor (kind, value) from a default-ref declared tool to an existing pinned executable owner and native recipe case. The declared command must reach that owner. Several tools may share the same verified owner; an unresolved or conflicting binding refuses coverage.
2. **Scenario binding.** A scenario anchor resolves to its exact corpus entry and an existing test or harness that consumes its id and enforces its assertions. A deferred, unbound, duplicate or nested-only id leaves the check unproved.
3. **Execution proof.** Retained native evidence must identify the current clean candidate, pinned recipe/harness inputs, case and witnesses, and the required Project, contract, job and producer provenance. Every required witness must be observed executing and passing, with successful stages and complete results. Preparation establishes readiness; declarations establish bindings. Neither establishes execution.
4. **Proof scope.** A narrow control proves only its observed mapped anchors. A package check requires complete package execution evidence. A generated-artifact check requires its parity/assertion control. An unrelated green run, skipped witness, zero selection or incomplete package selection leaves the missing checks unproved. Execution of the declared broad command is a separate claim that requires its own evidence.
5. **Refusal.** When a required binding or execution proof is missing, ambiguous, deferred, failed, incomplete, stale, unsupported or mismatched, admission refuses. The refusal uses existing failure categories and names the uncovered check, the deficient binding or run when available, and the recovery. Recovery remains explicit: admission neither reruns checks nor widens execution automatically.
6. **Retained gates.** The existing one-declared-command refinement requirement and accepted Review requirement remain unchanged. A narrow native control discharges its mapped footprint obligations separately from the exact-default-ref-command refinement gate. Declaration argv equality and its lexical rules remain unchanged.
7. **Continuity obligations.** All eight currently unbound continuity scenarios remain required and unproved until their checked bindings and execution evidence satisfy these rules. Each stays in the corpus; none receives an exemption. A coverage claim becomes satisfied only when its required proof exists.

### D2. A distinct package-complete recipe proves the package-level claim

A whole-package check uses the candidate commit's complete build-selected test inventory under a recipe whose bytes match the reviewed default branch. Native evidence qualifies only when every inventory entry is accounted for, no test fails, and the package passes. A test explicitly declared by the reviewed recipe as an expected skip may skip and remains unproved; the result names it. Every separately required test or scenario witness must actually run and pass. Implement this as one bounded package-complete recipe kind in the existing native Go producer, retaining existing packet/result limits and independent Review.

### D3. The package test source explicitly amends CD-0205 D1 and D2

For D2's distinct package-complete kind, candidate-sourced package test bodies
and the complete build-selected inventory replace CD-0205 D1's pinned-test
replacement rule for the package-level claim only. The recipe bytes still
match the reviewed default branch.

CD-0205 D2's statement that the candidate's changed harness does not replace
the pinned recipe continues to govern independently pinned behavioral
witnesses. It does not require replacing candidate package tests for D2's
distinct package-level claim. The existing selected-test family keeps its
independently pinned test source.

The package result cannot replace a separately required test or scenario
witness. Existing Project, contract, job, attempt, candidate, recipe, producer,
and independence joins remain required. This amendment changes no default-ref
pin or CD-0192 refinement grammar.

## Alternatives considered

- **Accept declarations or unrelated green commands as execution.** Rejected:
  neither observes the required witnesses on the candidate.
- **Treat a selected test subset as complete package proof.** Rejected: the
  omitted inventory remains unproved.
- **Keep package-complete proof unsupported.** Rejected in favor of D2's
  bounded kind. Independently pinned witnesses alone do not prove execution
  of the candidate's complete package inventory.
- **Add a parallel catalog or runner.** Rejected: the tooling declaration,
  recipe cases, corpus owners, native producer, and receipt path already own
  the required facts.

## Consequences

Missing bindings or adequate native proof can block active work at shared
delivery/completion admission. Recovery is explicit, not automatic execution.

Candidate package tests can be weakened by the candidate. Their package-level
result therefore does not replace independently pinned required witnesses or
accepted semantic Review. Expected skips remain named and unproved.

CD-0090 coverage remains outstanding until all eight continuity obligations
have checked bindings and qualifying execution proof. Publication, source
presence, and registration establish no native execution claim.

CD-0229 remains outstanding. This decision does not settle its separate
preparation-depth/proof-preservation or timing/adoption gaps, and does not
establish the complete shared-route outcome by itself.

### Required continuity population

The required continuity population in
[the corpus](../../scenarios/adapter-continuity.v1.json) is:

- `adapter-continuity-byte-stable-across-turns`
- `adapter-continuity-state-change-replaces-block`
- `adapter-continuity-failure-is-absence`
- `adapter-continuity-ttl-gates-spawn`
- `adapter-continuity-work-start-retarget`
- `adapter-continuity-lane-leak-absent`
- `adapter-continuity-post-boundary-re-pin`
- `adapter-continuity-session-vacate-then-reclaim-passes-occupancy-gate`

Every entry remains required under D1.7. The coverage shards record outstanding
proof, not satisfaction from this list. Integrated native execution and
independent Review remain separate from the structural checks below.

## Verification

- `python3 scripts/check-knowledge-index.py` validates the record, content hash,
  Domain placement, and typed law relations.
- `python3 scripts/check-knowledge-closure.py` checks that this document has a
  record rather than remaining unprocessed.
- `python3 scripts/generate-knowledge-index.py --check` and
  `python3 scripts/generate-law-coverage.py --check` check canonical shards.
- `python3 scripts/check-law-coverage.py` checks the outstanding implementation
  pointers; it does not establish native execution.
- `python3 scripts/check-doc-contract.py` and
  `python3 scripts/check-doc-links.py` check document structure and references.
- `python3 scripts/check-cd-allocation.py --no-fetch` checks identifier allocation
  against the available Git references.
