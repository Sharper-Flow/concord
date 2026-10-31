---
concord_work_id: work-851e4b5d8cb8fc788e29fb4e
work_type: implementation
title: Add the concord-research native investigation helper
completed_at: 2026-10-31T09:08:27Z
outcome_tag: shipped
lesson_tags: []
terminal_state: completed
priority: 0
summary: Shipped a read-only native research helper with bounded exploration and lookup delegation, generated distribution, permission tests and primary Task admission.
product_ids: [concord]
project_ids: [concord]
domain_ids: [agent-adapter, agent-contract, repository-verification]
tag_ids: []
---

# Native research investigation helper

## Outcome

[CON-1558](https://linear.app/sharper-flow/issue/CON-1558) lacked a native helper
that ran a whole framed investigation across repository and external sources.
The driving session had to combine separate exploration and lookup answers.

[PR #2200](https://github.com/Sharper-Flow/concord/pull/2200) added
`concord-research` through the existing generated native step-helper path.
The helper takes a framed question, known context, scope and budget. It resolves
law state through Concord reads, delegates only to `concord-explore` and
`concord-lookup`, and returns claim-level citations, uncertainty, gaps and caller
decisions. It edits no files and owns no workflow, planning, commit, publication
or operator-contact authority. The quick utilities and their nesting restrictions
remain unchanged.

The generated profile, installer inventory and primary Task admission carry the
helper without a dispatch window. The driving session retains conclusions,
evidence binding and delivery. Model choice stays host-owned.

Reviewed commit `86b24287405fce9f4279a0389581ff56cf33142c` and merged commit
`20ab7d0ef9b16ed6b8db54571941e05078877a3c` have the identical tree
`ad11676a9a6701b43f7611754fd475debf971f43`.
The automated release is [v34.1.0](https://github.com/Sharper-Flow/concord/releases/tag/v34.1.0).
No release was installed and no live-store cutover was performed.

## Law and retirement accounting

[CD-0154 D2](../../law/decisions/CD-0154-numbered-primary-agents-remain-host-owned.md)
received a compatible derived amendment. It adds the researcher to the existing
native helper distribution and authority obligation. The reason is that this
clause already owns generated helper delivery; no second mechanism was needed.
The accepted revision is
`sha256:cd71694fc77ff75234dcf9d2d596f45206304df4c12157bd776c52655c4c2f03`.
CD-0149 D4 and CD-0157 remain unchanged. This item retires no law.

Actual main was integrated after
[PR #2199](https://github.com/Sharper-Flow/concord/pull/2199) retired the duplicate
native verifier. This item removed its two proposed native helper recipes and
preserved main's deletion of the recipe generator, adapter recipe README and
recipe-only test file. No executor or compatibility path was restored.

PR #2200 deletes no test. Integration dropped this item's proposed
`TestNativeHelperRecipeSelectors` with its recipe files before merge. PR #2199
deleted the pre-existing `TestAdapterNativeRecipeSelector`,
`TestAdapterNativeRecipeGenerated` and `TestAdapterNativeRecipeRecordsReady`;
this item did not change them. All four test machinery retired by
[CD-0242 D8](../../law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d8-evidence-is-captured-by-its-producer-and-bound-to-its-subject)
and [workflow-engine-contract section 5.1](../../law/specs/workflow-engine-contract.md#51-tables).
The surviving profile, generator, installer and adapter tests retain proof of
the helper's current obligations.

## Evidence and consequential decisions

Independent Review supported both accepted criteria on the final candidate,
with no blocking findings or remaining obligations. The reviewer corrected one
stale native-proof phrase in the researcher role and regenerated its profile.
That repair was verified and committed exactly; unchanged code was not reviewed
again.

Final checks passed: 23 real-host profile tests; 24 adapter admission, role and
publication tests; two focused installer inventory/collision tests; primary
prompt and issue-resume tests; generated parity and knowledge/link/public
validators. The complete contract check passed 219 tests and Bun host typecheck.
The ordinary pre-push hook and all required PR checks passed.

[Merged-SHA CI](https://github.com/Sharper-Flow/concord/actions/runs/41186753261)
passed. The successful
[release workflow](https://github.com/Sharper-Flow/concord/actions/runs/41187177930)
admitted that exact verified merged SHA, so its full-CI fallback was correctly
skipped. Post-merge
[CodeQL](https://github.com/Sharper-Flow/concord/actions/runs/41187177611) passed.

The retained v30.2.0 adapter/core pair initially refused the new Review because
the final clean candidate had not been qualified. Treating that as a required
later cutover was wrong: CD-0111 D1/D4 preserve existing operations under the
pinned pair. Actual surviving helper checks ran through its supported command
route; independent Review assessed those fresh receipts, and typed Review and
criterion associations succeeded. One exact declared `go vet ./...` execution
satisfied the retained refine gate. No old receipt became a report, no schema
field was invented, and no delivered recipe was restored.

## Limits and follow-ups

Command-text restrictions are hygiene, not a sandbox. The operator accepted
that limitation as a known limit on
[CON-1560](https://linear.app/sharper-flow/issue/CON-1560#comment-a31ea4fd-14ad-49fb-a4fc-f407389ed0cd).
Nested helper-to-utility delegation requires host depth two.

[CON-1586](https://linear.app/sharper-flow/issue/CON-1586/host-process-listing-guard-rejects-the-unchanged-implement-and-verify)
records two baseline expectation failures in the supplementary host process
guard for unchanged installed implement/verify profiles. The researcher was not
installed; no safety denial or test was weakened.
[CON-1587](https://linear.app/sharper-flow/issue/CON-1587/review-and-implement-helper-roles-still-refer-to-retired-native-proof)
records stale native-proof wording in existing main-owned helper roles.
Both follow-ups remain outside this item's scope.
