# A recurring defect shape needs root-cause analysis before any single report is fixed

## What happened

From 2026-09-28 to 2026-10-01, Concord received about 17 defect reports with one failure shape. A nonterminal work item reached a workflow state where every forward and recovery action refused. The reports were `CON-704`, 705, 708, 711, 714, 721, 728, 731, 748, 757, 764, 770, 788, 791, 794, 795, and 796.

Each report was accepted as ready work. Each fix added or widened one guard clause at one admission site. About 12 of the 23 commits that touched `internal/store/workflow_action_guards.go`, `workflow_correction.go`, `workflow_dispatch.go`, `workflow_preflight.go`, and `workpin.go` from 2026-09-24 had that shape. Every decision record in the same period (CD-0133, 0137, 0143, 0164, 0172, 0186, 0193) added one more gated recovery route.

Nobody compared a new report with its siblings. The shared cause surfaced only when the operator asked whether one existed.

## The shared cause

- Admission for each recovery action is computed by hand at four or more sites: preflight, the guard table, the work pin intents, the dispatch fold, and the correction binding. The copies drift. When two copies disagree on one state, the item strands. `CON-796` is one case: an accepted `no_ship` review counts as settled review debt and advances refine to delivery, while the delivery-gate `request_correction` requires an outstanding post-rejection review.
- No check asserts liveness. `internal/store/workflow_liveness_test.go` samples a bounded space, skips `dispatch_worker`, and calls `t.Skip` when inconclusive. The property every report broke had no guard.

## The lesson

1. Before a defect report is shaped into a fix, search for earlier defects with the same failure shape. Two or more matches make the report an RCA item first.
2. When a fix adds the second special case to one mechanism, stop and ask whether the mechanism is right (P35). A run of fixes that each add one clause is evidence of a missing model, not of many small bugs.
3. A capture that arrives phrased as a fix ("supply a typed recovery route") is not ready work. It is a reproduction plus a hypothesis.
4. The coordinator owns the diagnosis. A repository explore utility returns facts with `path:line`, counts, and commit ids. The coordinator reasons over those facts. Adopting an explore utility's inferred root cause skips the analysis the coordinator owes. The 2026-10-01 diagnosis of this cluster itself made that mistake.
5. A property that many defects violate needs an executable check that fails, not one that skips when inconclusive.

## Follow-up

- `CON-798` (work-97112522add701d86a7a9d94): derive admission once and add an exhaustive liveness check.
- `CON-797` (work-b0b08d6e28a8db2bad386ee6): repair defect intake and the explore return contract so this pattern is caught at intake.
