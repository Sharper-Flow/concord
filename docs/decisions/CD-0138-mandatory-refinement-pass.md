# CD-0138: Mandatory refinement pass before workflow verdict

- **Status:** Accepted
- **Date:** 2026-09-12
- **Scope:** Built-in implementation and break-fix workflow definitions
- **Approval:** The operator approved this bounded workflow change.
- **Related:** CD-0013, CD-0059, CD-0115, CD-0116
- **Preserves:** External analysis ownership, pinned workflow instances, and worker fences

## Context

A producing step can satisfy the approved outcome predicates while its first
iteration still contains inefficient or duplicated code. A direct edge to the
verdict step lets that first iteration reach evaluation without a required
improvement pass.

Concord coordinates the pass but does not implement an analysis scanner. The
external authority supplies the scope, report, and evidence. The agent that
works the step reviews that output, verifies it, and decides what to act on,
so no analysis verdict is obeyed without agent review.

## Decision

### D1. Insert a mandatory external-effect step

Add `refine` after `execution` and before `acceptance` in
`workflow.implementation`. Add `refine` after `repair` and before `verify` in
`workflow.break_fix`. The forward edges cannot skip the step.

### D2. Fence and checkpoint the pass

Each `refine` step carries `start_refine`, `checkpoint_refine`, `bind_evidence`,
and `record_delivery`, plus the shared continuity actions. The step carries a
retry edge to itself and uses the existing lane-step capability join.

### D3. Require artifact evidence and agent-exercised judgment

Each `refine` step requires `EvidenceArtifact`. The bound artifact records the
analysis findings the pass consumed and proves that the refinement pass ran.

The step is agent-worked, not scanner-driven. The lane that works `refine`
verifies each finding before acting on it and discards false positives with
reasons, because analysis output is advisory input rather than verdict. The
pass exists to improve the change itself: when review finds that a smaller
implementation would be cleaner than the first iteration, the working agent
rewrites toward the smaller implementation instead of applying findings
mechanically.

### D4. Ship new definition versions

Ship `workflow.implementation` version 8 and `workflow.break_fix` version 7.
Freeze the prior version content and digests. Live instances keep their pinned
definition triple.

## Verification

- The registry exposes `refine` in both new graphs with no optional bypass edge.
- The version pin test verifies all historical and current definition digests.
- The generated payload projections include the two new action IDs.
- The workflow contract documents the external analysis boundary and artifact requirement.
