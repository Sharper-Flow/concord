---
concord_work_id: work-5b54181dd4c9a4b9d496022a
work_type: implementation
title: Ship concord-design as a native visual UI and UX helper
completed_at: 2026-10-31T10:10:01Z
outcome_tag: shipped
lesson_tags: []
terminal_state: completed
priority: 0
summary: Shipped the generated native designer helper with bounded visual edits, versioned preview material and operator-owned UI/UX approval.
product_ids: [concord]
project_ids: [concord]
domain_ids: [agent-contract, agent-adapter, operator-surface]
tag_ids: []
---

# Native designer helper

## Problem and delivered outcome

[CON-1559](https://linear.app/sharper-flow/issue/CON-1559) recorded that removal of
the dispatch-bound designer left UI-bearing Products without a helper to make a
bounded visual change and return inspectable material for operator approval.

[PR #2203](https://github.com/Sharper-Flow/concord/pull/2203) shipped
`concord-design` through the existing generated native Task helper path. It makes
one bounded visual change within assigned files in the caller's worktree, leaves
changes uncommitted, and returns exact paths, checks and an inspectable preview URL or screenshots naming
the version shown. Missing inputs or required approval return needs-decision.
Missing preview material remains a gap, not a completed result.

UI/UX approval stays with the operator. The designer may use Product browser
tooling exposed by the host; it adds no provider. It retains implementer-equivalent
editing permissions, Concord reads and explore/lookup-only delegation. Workflow
and planning writes, commits, publication, notices and operator contact remain
outside its authority. Host safety and model choice remain host-owned.

## Design and law

The implementation extends the merged researcher's profile, role, generator,
installer and surviving adapter-test path. It adds no dispatch window, packet,
report contract or native recipe. Its sources are
`contracts/agent-primary-profiles.v1.json` and `.concord/roles/design.md`; generated
profiles, installer inventory and staging fixtures are updated together.

The only law change is a compatible derived amendment to
[CD-0154 D2](../../law/decisions/CD-0154-numbered-primary-agents-remain-host-owned.md).
It adds the designer's bounded-edit, versioned-preview, approval and authority
obligations to the existing native helper distribution clause. That clause owns
the mechanism. Its verification section and knowledge shard were updated together.
The accepted revision hash is
`sha256:c6a57335fd4092c980873734b36e3dbfe4a0e0278dec153164d083199f3ea122`.
CD-0149 D4 and CD-0157 remain unchanged. No tests were deleted. No operator
approval boundary changed; the issue records approval of this helper capability.

## Review and verification

Independent native Review supported both contract predicates on final commit
`f45beab60c558a037bbbefd8030ed3c2fc4964a7`, tree
`53e7029b2cf1d05fe28c842c36f589c9e776ea52`. It repaired one Low finding: a missing
named-check input now returns needs-decision, and a regression test that does not
fail before the fix now stops with a gap. Its verified three-file repair is the
only final delta.
No blocking finding remains. The recorded Review is
`workflow-3242a4e05d547f6ec36d51f4:completed`.

Captured final-commit checks passed with unchanged tracked files:

- Profile permissions: 24 tests; one optional real-host probe skipped inside
  isolated capture. Separate real-host probes passed before and after repair.
  Receipt: `worktree_verify:sha256:49eb988dc7913bf39539aac87779c1a7c67c4d7f0222451b03fd8bbd05495ab3`.
- Native Task admission and role/publication boundaries: 25 tests passed.
  Receipt: `worktree_verify:sha256:eaa9e339dce5758d3971dea87e92da4bda8040dbf91193edcd7b34b53d4ba680`.
- Installer inventory and unmanaged-definition collision: two named tests passed.
  Receipt: `worktree_verify:sha256:a4bd7e888c965a3367737d2a5831004d407c90d86e319c3920a1d520af3906d0`.

Generator parity, staging, prompt, knowledge, link and public-content checks
passed. The final contract checker passed, including the pinned host typecheck.
The normal pre-push hook passed without bypass. The retained runtime required one
declared refine check; `go vet ./...` passed as
`worktree_verify:sha256:f6310e5a1bb1653db2a58a6c517ceced28139875015941b34b41f3afa5c8a1c1`.
That check qualifies refine; it is not designer behavior proof. Native receipts
were not converted into reports. Both criterion verdicts and unchanged-premise
confirmation were recorded before the workflow's release step.

## Publication

The implementation merged as `1a84047a067a6c7919d79b5d6e2d7c32dddf97f1`; its tree
matches the reviewed head. Required [PR CI](https://github.com/Sharper-Flow/concord/actions/runs/41190581764)
and [merge-queue CI](https://github.com/Sharper-Flow/concord/actions/runs/41191004705)
passed. The [default-branch checks](https://github.com/Sharper-Flow/concord/actions/runs/41191442549)
and [automated release](https://github.com/Sharper-Flow/concord/actions/runs/41191442799)
passed. The release admitted existing exact merged-SHA verification; its full-CI
fallback correctly skipped. [v34.2.0](https://github.com/Sharper-Flow/concord/releases/tag/v34.2.0)
contains the helper. No installation or cutover was performed.

## Limits and follow-up

No live UI-bearing Product smoke preview was produced: this item ships the
helper, not a Product visual change. Command-text restrictions are hygiene, not a
sandbox. Nested utility calls require the host's depth-two setting. Preview
availability remains the caller's responsibility; inaccessible material must be
reported as a gap.

[CON-1586](https://linear.app/sharper-flow/issue/CON-1586) owns the separate
installed Toolbox process-listing guard baseline. It was not repaired here, and
no inherited denial was weakened. Workflow input and fence refusals were
corrected through their admitted routes without changing scope or approval.
