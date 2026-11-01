---
{
  "id": "CD-0192",
  "title": "The refine pass uses a verification report",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0138"
    }
  ]
}
---
# CD-0192: The refine pass uses a verification report

- **Status:** Accepted
- **Date:** 2026-09-29
- **Scope:** The refine exit of `workflow.implementation` and
  `workflow.break_fix`, the Project tooling manifest, and the argv equality
  the guard enforces
- **Preserves:** CD-0138 D1, D2, and D4, the agent-judgment rule of CD-0138 D3,
  CD-0055 D4 and the digests derived from the one canonical
  source ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D14)
- **Related:** CD-0013, CD-0055, CD-0138, CD-0156
- **Approval:** The operator approved the work item contract that carries this
  record on 2026-09-29.

## Context

CD-0138 requires every code change to pass through the `refine` step before
its verdict step. The step demands an artifact evidence binding, and the bind
route checks only the evidence kind enum and the reference syntax. Research on
completed code-change items found that refine bindings name workflow operation
ids, attempt ids, or arbitrary strings almost every time. The declared tools
in a Project's `.concord/tooling.v1.json` were bound at refine zero times. The
pass could reach its exit without any analysis running on the change.

A declared check also cannot come from the change itself. A branch under
refine could add a trivial check to its own working tree and pass it. The
declaration a gate consumes must live where review owns it, on the Project's
default ref.

## Decision

### D1. The refine exit runs a guard at record_delivery

Leaving `refine` through `record_delivery` in the implementation and
break-fix families runs a
guard registered next to the delivery admission guards. The guard is active
only at the refinement step.

### D2. The proof is a bound passing verification report

The guard admits the advance only when the work item has a bound passing
verification report for the recorded candidate commit under CD-0242 D8. A
contract revision alone does not discard it (CD-0242 D7). Failed, unresolved or
mixed-candidate reports do not qualify.
Concord does not execute the check or establish the report by live Git reads.

### D3. Review judges the checks

Independent Review judges whether the checks and evidence are adequate for the
change. The refine guard does not compare commands to a default-ref tooling
manifest. Required CI remains an external delivery requirement.

### D4. The report does not prove adequacy

The attributed report is a claim about a result, not independent attestation
that a check ran or that its assertions are sufficient. Review decides adequacy.

## Alternatives considered

- Validate each `bind_evidence` call at refine. Rejected: a bind cannot decide
  whether refine is proved, because no route knows which binding is the final
  proof.
- Accept any verification reference. Rejected: that is the present gap.

## Consequences

Refinement uses recorded reports and Review's judgment rather than a Concord
runner, lease, epoch-timed acquisition or exact-argv gate.

## Verification

- Refine tests cover bound passing reports and refusal of failed, unresolved or
  mixed-candidate evidence.
- `python3 scripts/test-project-tooling.py` proves the checker and the schema
  refuse shell quoting and shell operators in an invocation.
- Committed law-folder snapshot validation registers the record; its content
  digest is derived from the committed bytes at every read.
