---
{
  "id": "CD-0209",
  "title": "Declared recovery routes preserve artifact truth",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0143"
    },
    {
      "kind": "refines",
      "target_id": "CD-0201"
    },
    {
      "kind": "refines",
      "target_id": "CD-0204"
    }
  ]
}
---
# CD-0209: Declared recovery routes preserve artifact truth

- **Status:** Accepted
- **Date:** 2026-10-07
- **Scope:** Declared correction routes, artifact staleness, current work pins,
  registration, liveness and conformance
- **Related:** CD-0013, CD-0122, CD-0124, CD-0138, CD-0143,
  CD-0166, CD-0201, CD-0204, CD-0242

## Context

Independent step-order heuristics can disagree about where correction returns.
A changed verdict alone must not make stale production healthy. Runtime
admission and its liveness model need one declared producer route.

## Decision

Correction needs authenticated
coordinator identity, exact diagnosis, strategy, affected predicates/findings
and bound evidence. Recovery fabricates no workflow record.

### D1. One declared recovery-route table per family

`recovery_routes` is an optional closed table of step, trigger, action and
target. An absent or empty table has the same omitted canonical encoding.
`unhealthy_verdict` pairs only with `request_correction`.
The unhealthy route belongs to a verdict, premise-confirmation or declared
late-verdict step.

The target must be a declared, nonterminal upstream producer, distinct from
the source step, and must not declare `approve_contract`. The route table
admits its engine-owned recovery action without a duplicate root declaration.
Same-step and late-evidence recovery keep their own declared owners and do not
gain a guessed cross-step route.

### D2. Registration refuses missing or ambiguous routes

A step recording verdicts, confirming a premise or hosting a late verdict needs
its required route. Unknown steps, triggers, actions or targets, mismatched
trigger/action pairs, duplicate step/trigger pairs, non-upstream or nonproducing
targets, self-targets, terminal targets, approval targets and a second failure
edge refuse. Definitions derive from one canonical source; no historical
version, work-kind whitelist or guessed table fills a missing declaration.

### D3. One reader supplies every consumer

`workflowRecoveryRoutes` returns a defensive copy of the declared table.
Admission, effects, staleness, work-pin intents,
late-verdict recovery, liveness and conformance use that same reader.
No graph walk, nearest-effect ordering or correction prose selects the target.

### D4. Production is a typed result, not a start or Review

A target must declare a fenced producing mechanism or a direct
`record_finding`, retained `record_decision` or `record_delivery` action.
Its typed target result owns production: research uses `record_finding`,
retained architecture-spike work uses `record_decision`, and other producers
use `record_delivery`. CD-0218 limits which families admit new execution.
A fenced start alone, Review outcome, verdict, evidence binding, checkpoint,
candidate revision, successor link or step advance is
never production. Native Task execution supplies no production authority.

### D5. Artifact staleness is current state

Every step in the declared producer-to-evaluator span carries that producer's
staleness. A verdict that is not healthy or is incomparable makes it stale.
An ordinary contract update invalidates only dependencies whose digests changed
under CD-0242 D7. Current rows, expected versions and the admission model own
the same staleness fact; a failed read is not a false bit.

Only fresh typed production at the declared target after the stale cause
clears it. A repeated healthy verdict, start, Review, evidence binding,
refinement, return or intermediate advance cannot clear it. While stale,
single or batched healthy verdict recording and premise confirmation refuse
and name the declared recovery. A correction returns to the target without
claiming production happened.

### D6. The pin names the declared recovery sequence

An admitted unhealthy-verdict correction records its exact bindings and returns
to the producer. The pin names fresh production, re-evaluation and the remaining
verdict or premise action. An ordinary contract update holds the step;
a retained fresh unhealthy verdict still needs its declared correction.

Contract content changes follow [workflow-engine contract §4.1](../specs/workflow-engine-contract.md#41-ordinary-contract-updates).
An update does not manufacture fresh production or bypass an unhealthy verdict's
declared correction route.

### D7. Liveness and conformance cover every declared route

Every registered definition must have a non-continuity exit and every reachable
well-formed state must reach terminal. Safety checks reject a seeded false
healthy state. Conformance exercises the real engine and compares its current
staleness with the model at correction, production and evaluator re-entry.
No route or definition receives a carveout. Public-tool journeys cover each
declared route, admission/effect agreement, exact bindings, unhealthy staleness,
fresh target production, re-evaluation and completion. Late association keeps
its own declared return and cannot use artifact staleness as authority.

## Alternatives considered

- Guess the nearest producing step. Rejected: runtime and model would drift.
- Treat a start or Review as production. Rejected: neither establishes a new
  delivered artifact.
- Clear staleness with a healthy verdict alone. Rejected: it hides missing production.

## Consequences

Recovery retains one table, one reader and fail-closed production evidence.
No worker authorization, acceptance, capability origin or launcher failure
record participates in that boundary.

## Verification

- `go test ./internal/store` checks registration refusals, defensive route
  reads, typed production, current staleness, seeded safety, exhaustive
  liveness and real-engine conformance.
- `go test ./internal/agent` checks public route journeys, exact evidence,
  no-effect refusals, replay and current Review correction before delivery.
- Committed law-folder snapshot validation checks registration and closure.
