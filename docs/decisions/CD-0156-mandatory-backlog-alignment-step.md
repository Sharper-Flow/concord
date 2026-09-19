# CD-0156: Mandatory backlog alignment step in planned and defect work

- **Status:** Accepted
- **Date:** 2026-09-19
- **Scope:** Built-in implementation and break-fix workflow definitions, and the alignment projection
- **Approval:** The operator approved this bounded workflow change.
- **Related:** CD-0013, CD-0115, CD-0138
- **Preserves:** Pinned workflow instances, worker fences, and relation authority

## Context

Concord mandates no backlog check. An implementation or defect item can reach
design and planning while an open item already covers the same ground, and
nothing in the workflow forces the agent to look. Absence of a relation cannot
carry that fact: absence is also what a skipped search looks like.

Admission is action-declared on the step, and progress is execution-mode
driven. An action added to an existing first step is available, never
required, because that step's existing advance action still moves it forward.

## Decision

### D1. A new step with exactly one advance action

Splice one new step named `alignment` into `workflow.implementation` between
`proposal` and `discovery`, and into `workflow.break_fix` between `reproduce`
and `diagnose`. The step declares one advance action, `record_alignment`, plus
the shared continuity holds. Because `workflowNextStep` follows the single
forward edge and only an advance-mode action moves the step, `record_alignment`
is the only route from `alignment` to the next step, and the search becomes
structural rather than discretionary. The worker-action set stays off the step,
so a dispatched attempt cannot accept its way past the search.

### D2. Placement after the first content step

The step sits immediately after `proposal` and after `reproduce`. The proposal
or reproduction supplies the terms the search needs, so the step cannot come
first. Placing it before `discovery` and `diagnose` means a duplicate is found
before analysis effort is spent on it.

### D3. Explicit empty result

`record_alignment` is internal_sqlite, approval none, execution mode advance,
typed event. Its payload carries `searched` (required, the search performed and
its scope), `outcome` (required enum `related_found` or `none_found`), and
`related_ids` (1–64 work ids, present only when the outcome is
`related_found`). A guard refuses `related_found` with no ids, and
`none_found` with ids. A later reader must be able to tell checked-and-found-
nothing from never-checked; passing the step is the proof the search happened,
and the enum records what it returned.

### D4. A new fold-only projection table

The fold writes a new fold-only projection table, `workflow_backlog_alignment`,
keyed by `work_id`, holding the declaration row and zero or more related-work
rows. It does not reuse `workflow_candidate_sets`: that table's foreign key
binds to `workflow_contracts(work_id, contract_version)`, and the alignment
step runs before planning approves a contract, so no parent row exists.

### D5. No automatic relation

The action records the candidate set only. It creates no relation: the relation
kind is a semantic judgement that stays with `concord_work_relate.link` and
`resolve_overlap`.

### D6. Frozen version chain

New wrapper builders raise `workflow.implementation` from 12 to 13 and
`workflow.break_fix` from 10 to 11, and the prior versions stay registered in
`builtinWorkflowDefinitionsWithHistory`, so every pinned in-flight item replays
its original graph unchanged. Two digest rows are appended to
`workflowDefinitionVersionPins`.

### D7. Two workflow types only

`workflow.implementation` and `workflow.break_fix` take the step. These two
carry the Product's planned and defect work. `research` and
`architecture_spike` already revise a candidate set mid-flow through
`revise_candidates`. `ops_runbook`, `static_analysis`, and `generic_one_off`
do not create durable Product work that another item can duplicate. The change
does not touch the domain-overlap preflight, the capture-time external_ref
collision refusal, or the other five workflow types.

## Verification

- `TestAlignmentStepHasOnlyRecordAlignmentAsItsAdvanceExit` proves the step
  cannot be skipped in either new graph.
- `TestRecordAlignmentRefusesInconsistentOutcomeAndRelatedIds` proves the
  guard refuses an inconsistent payload and a consistent one records and
  advances.
- `TestPriorPinnedDefinitionVersionsReplayUnchanged` proves every pinned prior
  version verifies and carries no alignment step.
- The workflow contract documents the new event, table, and graphs.
