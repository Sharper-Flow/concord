---
concord_work_id: work-1eaa5c6ff1daa923e92f9d58
work_type: implementation
title: Remove native repository verification and accept reported local or required CI evidence
completed_at: 2026-10-31T08:45:34Z
outcome_tag: shipped
lesson_tags: [reported-verification, verified-snapshot]
terminal_state: completed
priority: 0
summary: Retired Concord's repository test executor and accepted attributed reports while preserving independent Review, candidate consistency, external CI and verified-snapshot upgrades.
product_ids: [concord]
project_ids: [concord]
domain_ids: [agent-adapter, agent-call-boundary, agent-contract, durable-authority, operator-surface, product-memory, product-root:concord, repository-verification, work-coordination, workflow-engine]
tag_ids: []
---

# Reported verification replaces Concord's test executor

[CON-1498](https://linear.app/sharper-flow/issue/CON-1498) removed the duplicate repository test runner in [PR2199](https://github.com/Sharper-Flow/concord/pull/2199). It merged as `1c68214413f3d02c32630416396a733adf8af1d9` after independent Review and required PR checks on `ae24bff68058085d1cf292bac9380d656d5d7518`. The two commits have the same tree, `c8e8e61c48f0f815bc6270dd9e871ab9f7155b3c`.

## Delivered outcome

- Removed `worktree_verify`, its command/native executor, preparation and materializer framework, verification leases, retained output and receipts, CLI/tool contracts, and their retired tests. No compatibility runner remains.
- Existing `bind_evidence` accepts an attributed report with exactly `source`, `commit`, `result` and `run_ref`. Sources are local execution or required CI. Concord records the coordinator's report; it does not run tests, inspect GitHub or observe live Git HEAD to validate it.
- Independent Review judges adequacy. Consumers reject unresolved or failed reports, mixed recorded candidates, stale passes superseded by newer reports, and Review Projects outside the work's durable memberships. `work_pin.review` remains the candidate/dependency projection; its duplicate descriptor is gone.
- Breaking migration 171 preserves main's preceding migration 170. Mandatory verified snapshots precede destructive drops. Active verification leases refuse the upgrade; post-drop failures roll back. Native evidence is not converted into attributed reports.

## Evidence and acceptance

The final independent Review found no open findings and supported all four contract predicates: executor absence, reported evidence, recorded-candidate consistency and verified-snapshot upgrade. The coordinator recorded that outcome as `workflow-6e5dc7f51dca101b73eb6076:completed`, associated the criterion proof through typed verdicts, and confirmed the premise.

Focused tests covered report validation, latest-report behavior, candidate movement, membership, generated interfaces and migration rollback. Regression cases failed before their fixes. Contract parity, authoring/capacity bounds and the required host declaration typecheck passed without skipping host checking. Exact publication pins were measured rather than relaxed.

The normal pre-push hook passed. Full required PR CI passed after repairing stale module/test expectations and ShellCheck diagnostics. Dependency versions and `go.sum` stayed unchanged; `x/sys` became indirect. Worker-schema union rows, above-32 completion assertions and cleanup safety remain. Post-merge verification has its own [main CI run](https://github.com/Sharper-Flow/concord/actions/runs/41184868778) and [automated release run](https://github.com/Sharper-Flow/concord/actions/runs/41184869254).

The installed pre-cutover binary still required captured native proof for this item's Review and refine gates. Its admitted route captured criterion checks and one exact declared `go vet ./...` invocation on the unchanged reviewed candidate. These transition-time receipts do not restore an executor to the delivered product. The refusal listed nine available checks; source tracing established that only one declared invocation was required. No broad rerun or bypass was needed.

## Law changes

Each amendment retires duplicate execution or a stale proof claim. Revisions enacted without operator approval are derived under CD-0230 D4. No operator approval boundary changed. Independent Review, required external CI, host test admission and verified-snapshot safety remain required.

- [CD-0016](../../law/decisions/CD-0016-context-continuity.md): continuity retains report references instead of native verifier output, removing obsolete stream retention.
- [CD-0058](../../law/decisions/CD-0058-no-model-routing.md): replace native-receipt wording with attributed reports; model ownership does not change.
- [CD-0096](../../law/decisions/CD-0096-in-session-worktree-retargeting.md): retire the Verify tier, leases and output inspection; retain inspect/destroy authority.
- [CD-0104](../../law/decisions/CD-0104-a-session-worktree-is-its-actual-directory.md): remove verification-lease ownership and session-movement coupling.
- [CD-0110](../../law/decisions/CD-0110-work-start-chains-from-a-terminal-worktree.md): remove the terminal worktree verification-lease gate.
- [CD-0119](../../law/decisions/CD-0119-work-start-resumes-an-existing-item-by-work-identity.md): resume no longer handles native verification leases.
- [CD-0192](../../law/decisions/CD-0192-the-refine-pass-proves-a-declared-analysis-ran.md): refine consumes a passing attributed report for the recorded commit instead of executed argv/native receipts.
- [CD-0197](../../law/decisions/CD-0197-a-review-report-carries-a-typed-verdict-and-ranked-findings.md): Review judges recorded candidates and report adequacy; retire native qualification, live-Git freshness and the duplicate descriptor.
- [CD-0198](../../law/decisions/CD-0198-small-items-batch-merge-and-derive-routine-workflow-actions.md): routine derivations consume reported evidence instead of native runs.
- [CD-0201](../../law/decisions/CD-0201-workflow-action-admission-is-derived-once-and-every-reachable-state-stays-live.md): replace native preparation/execution with existing evidence/report admission, preserving one admission owner.
- [CD-0222](../../law/decisions/CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md): describe recorded-candidate reports rather than captured command proof; retain package and transaction ownership.
- [CD-0242 D8](../../law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md): replace native execution, preparation, receipts, output and live-HEAD proof with four-field reports; retain applicable-evidence reuse and verified snapshots.
- [CD-0248](../../law/decisions/CD-0248-footprint-checks-bind-to-executable-owners-and-use-existing-verification.md): footprint proof consumes reported local/required-CI evidence instead of native readiness.
- [PM8](../../law/specs/product-memory-evidence-store.md): remove native verification storage, lease and output obligations.
- [PM9](../../law/specs/product-memory-process-exhaust.md): remove native verification process exhaust and retained streams.
- [workflow-engine-contract](../../law/specs/workflow-engine-contract.md): retire native interfaces/storage/live-subject gates and define report attribution with recorded-candidate checks.

## Retired tests and replacement proof

- CD-0096 D3, CD-0242 D8 and workflow-engine-contract §5.1 retire executor, lease, isolation, preparation, output and native-only bootstrap/session/handoff tests. Ordinary claim, inspect, destroy, transport, recovery, identity, rollback and safety-owner checks remain.
- CD-0192 D2–D4, CD-0197 D6, CD-0242 D8 and §5.1 retire native/live-Git portions of refine, Review, candidate and acceptance tests. Replacement proof covers missing, failed, foreign, mixed and superseded reports, candidate movement, dependency reassessment and membership.
- CD-0197 D6/CD-0242 D8 retire only the duplicate `continuity_review_input` capacity assertion. Candidate, criterion, criteria and dependency bounds remain, with a regression for descriptor absence and retained `work_pin.review` content.
- CD-0242 D8/§5.1 retire only the native executable-child validation column from `TestOracleCaseJudgeUnion`; all worker union rows and resolution-proof assertions remain. The same clauses retire native fixture generation and its rapid-integration exception, not current CI safety checks.

The PR carries the full retirement inventory and its replacement proof. Repository acceptance scripts, upstream compatibility checks, `bin/oc-test` and toolbox admission stay in place.

## Remaining boundaries

- No release was installed and no live-store cutover was performed. A later breaking cutover remains operator-reserved; the earlier approval covered only v30.2.0.
- [CON-1583](https://linear.app/sharper-flow/issue/CON-1583) owns reassessment of the remaining bubblewrap compatibility output. Current CI was not waived.
- [CON-1585](https://linear.app/sharper-flow/issue/CON-1585/generator-and-schema-still-carry-worker-oracle-control-and-worker) owns orphan worker-oracle schema roots outside this contract. They were not opportunistically removed.
- The RTE sequenced PR2199 before the researcher-helper stream so that stream can integrate the removal. This item does not authorize restoring native oracle recipes or another test executor.
