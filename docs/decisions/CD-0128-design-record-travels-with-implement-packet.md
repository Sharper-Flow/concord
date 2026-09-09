# CD-0128: The design record travels with the implement packet

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** The typed design record for implementation work and its projection into the implement lane packet
- **Approval:** The operator approved this bounded change in Concord work-42d1699f7c11fa7e6341b607.
- **Related:** CD-0013, CD-0016, CD-0043, CD-0067, and CD-0115
- **Preserves:** CD-0043 D1. Lane methodology remains host-owned and is not Concord durable state.

## Context

The implementation workflow has a design step, but `record_design` carried no typed record into later execution. A worker could receive the approved objective without the decisions that selected its approach.

## Decision

`record_design` is a typed workflow action in the next implementation definition version. Its durable record contains an approach, one to sixteen typed decisions, and one to sixty-four touched references.

The continuity projection exposes the latest design record. The adapter renders that record before the work narrative in `inputs.context` for the implement lane. The adapter refuses a combined design record and narrative above the closed context bound.

The record describes the change's decisions. It does not describe lane procedure, review method, or verification method. Research inputs use the existing `research_bindings` field with `use_role=design_input`.

No new workflow gate is required. The existing forward-only graph already places design before planning and execution.

## Consequences

Implementation workers receive the coordinator's recorded approach and decisions without making an architecture choice. Released workflow definitions remain digest-pinned, and the prior implementation version keeps its generic design action.

`workflow.break_fix` and `workflow.generic_one_off` remain without a design step.

## Verification

- The internal store tests pass.
- The adapter packet tests pass, including design presence, design absence, and context overflow.
- Agent contracts regenerate without drift.
