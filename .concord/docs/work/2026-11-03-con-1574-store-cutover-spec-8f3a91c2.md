# CON-1574 store cutover specification (final, 2026-11-03)

Work item: work-b9f108db022fd65badf49200 (CON-1574). Branch
`refactor/con-1574-workflow-step-plan`, base 0784bae57. Landed commits:
48cef4e2b (foundation), 54d731ad9 (complete_step on every template step),
d7c2b356f + fa458cb6c + 172240b17 (law retirement riding this PR).

This note is the authoritative implementation spec for the remaining
internal/store cutover. Six review rounds converged it; do not reopen settled
decisions without recording why.

## Settled semantics (tested in workflow_step_plan_test.go)

- Inserted steps anchor via their OWN `after` edges; an existing step's
  `after` edges never change. Insertion is temporal, not rewiring.
- An incomplete inserted step may be removed; a completed step never is; a
  required step is never removed.
- A repeated step appends an attempt; completed attempts are immutable.
- A reviewed step's attempt requires reviewer, result (pass|fixed), commit,
  link; an unreviewed attempt refuses those fields.
- `complete` is plan-gated (only when every step is complete);
  `insert_step` and a second `pick_type` (type switch, adds-only) are
  plan-level; `complete_step` may name a frontier step or a completed step.

## Law state (committed on this branch; do not edit further)

workflow-engine-contract.md reads as the step-plan model: §2 templates +
plans + one plan-write check; §3 opens at `intake` (planned/ready are
pre-cutover rows only); §5.1 tables carry workflow_type/step_plan and
contracts carry no definition columns; §5.2 row effects include
intake/clarify/type-pick/step-insert/step-completion; §11 dispatch reads the
plan; §12 prelude, insertion, reviewed steps, completion-follows-plan.
CD-0138/CD-0156/CD-0166/CD-0192 deleted; citations and law_relations pruned.
CD-0243 D3 exemption applies. There is no rebuild-from-log: the event log is
retired; only the fold switch must handle historical event kinds
deterministically (check operation.go:179 — the definition-selection fold
still registers there).

## Remaining implementation (internal/store only; other packages break until
the follow-up pass)

1. Schema + migration 183 `workflow_step_plan_cutover` (Breaking): never edit
   shipped migrations (base CREATE is migration 15); migration 183 alone
   defines the new shape; derive carry-over columns from the shape 181/182
   leave. New columns workflow_type (NULL, closed enum) and step_plan (NULL,
   JSON object); drop definition_ref/definition_version/definition_digest/
   current_step; instance_state gains 'intake'. workflow_contracts loses its
   definition columns. Rewire workflow_takeover.go:35-56 and
   project_handoffs.go:976-983 off workflow_step_completions (derive from the
   plan), then DROP that table with its folds/triggers. Migration mapping
   precedence: terminal lifecycle => matching terminal state; else if a
   workflow_delivery_state row exists => 'completed' + lifecycle 'completed'
   (bookkeeping of a finished fact; held live items carry no delivery link —
   fixture proves a held undelivered item parks); else 'intake' with NULL
   type/plan. Fence per workflow_correction_migration.go wording if needed.
   NOTE: scripts/test-migration-compatibility.py:1844 pins the breaking floor
   at 182 — bump it for 183.
2. Engine: capture initializes at 'intake' with no definition (delete
   DefaultWorkflowRefForKind, WorkflowDefinitionSelected emission,
   RepinWorkflowTx). Admission per the settled rules above: pre-plan prelude
   (record_intake once; record_clarify only when intake found open questions;
   pick_type after intake proceed or clarify), plan-level actions, frontier
   actions from WorkflowStepPlanTemplateActions (template steps) or the
   inserted-step set {complete_step, record_review, record_delivery,
   link_successor, bind_evidence}. Typed actions record data and never
   complete steps. approve_contract + one-active-contract survive on frontier
   steps declaring them. Intake outcomes already_handled/decline use the
   EXISTING lifecycle cancel path; split_or_merge records only. Work pin:
   WorkflowType from the column, Step 'intake' or first frontier step,
   intents from admission; drop workflow_definition_digest.
3. Deletions: workflow_definitions.go; the DefinitionRegistry stack and
   digest machinery; workflow_live_authoring family lists; archived-family
   refusals; preflight definition verification; pinnedWorkflowDefinitionTx
   becomes a plan reader. Retired actions record_proposal, record_alignment,
   record_design leave policies/catalog (event mappings stay as replay
   history). Keep the action catalog, policies, cross-field validation.
4. Tests: delete retired-behavior files wholesale (cite the retiring clause
   in the PR list, not the tests); repair survivors; add prelude/pick/switch/
   complete_step/insert/pin-intent/takeover/migration tests (pattern:
   workflow_records_migration_test.go, predecessor at 182).
5. Follow-up pass (separate): internal/agent + adapter/opencode non-generated
   surfaces to workflow_type + derived step; scripts/workflow-action-contracts
   switches to the template contract; run
   python3 scripts/generate-agent-contracts.py; check-agent-contracts.py;
   then full store shards via bin/oc-test store-shard i n.
6. Delivery: CON-1512 PR2260 (payload mirror removal, generator arms gone)
   merges first — integrate by rebase-equivalent + regenerate, no hand-merge.
   Independent review of the exact head, required CI, PR `Related to
   CON-1574` plus the law-change list (see commit d7c2b356f message).

Verification floor for any implementation pass: go build ./internal/store/...,
go vet ./internal/store/, bin/oc-test targeted -- go test ./internal/store/
-run '...' (targeted tier requires -run), migration compatibility script,
then the generator pass.

## Closing note

This is a historical work record. The cutover and restoration of the recorded
premise-confirmation and delivery facts at completion are on
[PR 2286](https://github.com/Sharper-Flow/concord/pull/2286). The pull request
remains open; it has not landed on the default branch.
