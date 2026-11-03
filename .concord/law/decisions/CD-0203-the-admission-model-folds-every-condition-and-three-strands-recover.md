---
{
  "id": "CD-0203",
  "title": "The admission model derives every condition and research cannot skip its contract",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0201"
    }
  ]
}
---
# CD-0203: The admission model derives every condition and research cannot skip its contract

- **Status:** Accepted
- **Date:** 2026-10-04
- **Scope:** The workflow liveness, well-formed-exit, and conformance checks;
  the contract-step admission those checks proved stranded
- **Preserves:** CD-0201 D2, the liveness law; CD-0112 D1, continuity actions never move
  the step; every other route refusal keeps its scope
- **Related:** CD-0016, CD-0173
- **Approval:** The operator directed in session chat on 2026-10-02 that the
  checks stay strict, with no named exception, and that the engine routes
  change until the law holds.

## Context

CD-0201 D2 states the liveness law. Each well-formed nonterminal admission
state admits an exit, and each reachable nonterminal state reaches a terminal
step. The first checks covered only a subset of the admission conditions. An
exit that the subset could not prove was not counted, and a state that the
subset could not reach was not checked.

The checks now derive every condition `workflowAdmit` reads, over the
registered definitions
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).
Research declares `frame_research` in advance mode beside `approve_contract`
on its frame step. A contractless advance can move the work past the only
step that binds a contract, and completion refuses without one.

## Decision

The same derived admission state owns premise confirmation and ordinary action
writes and preserves evidence and non-hold exits. CD-0013 D11
requires the authenticated coordinator for premise confirmation; operator
decisions follow CD-0243 in conversation.

### D1. The contract step requires contract recording

An advancing action with no active contract refuses at the contract step,
except `approve_contract`. The refusal kind is an illegal lifecycle
transition. A work item leaves the contract step only through the approval
it hosts.

### D2. Premise confirmation uses the authenticated coordinator

The active contract's coordinator confirmation records directly. Admission
requires verdict evidence, and forward
confirmation in delivery-producing families requires the current ready Review
under [workflow-engine contract §4.1](../specs/workflow-engine-contract.md#41-ordinary-contract-updates).
Ordinary record replacements follow
[workflow-engine contract §4.2](../specs/workflow-engine-contract.md#42-ordinary-retry-and-record-correction).
They supply no healthy verdict or fresh Review by themselves. Work-pin,
continuity, and action admission use the same folded prerequisites.

### D3. The checks derive every admission condition

The liveness and well-formed-exit checks enumerate the abstract states over
every condition `workflowAdmit` reads. The conformance replay compares the
loaded state with the abstract successor on the fields `workflowAdmit`
reads. The step-kind checks cover every registered definition and the
confirmation action uses the same folded prerequisites as the engine.

Admission code and definition content have separate owners.

## Alternatives considered

- Name the strands as exceptions in the checks. Rejected: the
  operator directed that the law holds without exceptions, and an excepted
  strand still strands real work.
- Repair admission by changing definition content. Rejected: the strand is in
  admission code, so changing definition content leaves the refusal in place.
- Rewrite a step's kind at read time.
  Rejected: a read-time rewrite would make runtime behavior diverge from the
  generated source (CD-0242 D14).

## Consequences

Research cannot skip its contract. A future change that
strands any registered definition fails the widened checks with a witness.

## Verification

```gherkin
Scenario: A contractless advance at the contract step refuses
  Given a version-1 research item at its frame step with no active contract
  When the agent requests frame_research
  Then admission refuses with an illegal lifecycle transition
  And approve_contract stays admitted

Scenario: Every registered definition stays live under the full model
  Given the registered definitions
  When the checks enumerate the full abstract admission state
  Then each well-formed nonterminal state has an exit and each reachable state reaches a terminal step
```

Verification-inventory update only; the obligations above are unchanged. Current
plan admission and preflight/dispatch checks are in
[`workflow_admission_conformance_test.go`](../../../internal/store/workflow_admission_conformance_test.go),
[`workflow_plan_preflight_test.go`](../../../internal/store/workflow_plan_preflight_test.go), and
[`workflow_plan_dispatch_test.go`](../../../internal/store/workflow_plan_dispatch_test.go).
These pointers do not assert proof of the abstract-state liveness scenario.
