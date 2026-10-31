---
concord_work_id: work-748b4356ccb29ea9228cf989
work_type: break_fix
title: Report read-only WAL backup verification budgets on failure
completed_at: 2026-10-31T17:36:05Z
outcome_tag: shipped
lesson_tags: [backup, failure-only-observation, no-reproduction]
terminal_state: completed
priority: 0
summary: Added failure-only backup verifier phase evidence without weakening safety bounds; the original timeout remains unexplained.
product_ids: [concord]
project_ids: [concord]
domain_ids: [durable-authority, repository-verification]
tag_ids: []
---

# Failure-only backup verification phase evidence

[CON-1589](https://linear.app/sharper-flow/issue/CON-1589) delivered observation,
not a timeout repair, in [PR 2211](https://github.com/Sharper-Flow/concord/pull/2211).
The original ordinary-CI failure remains unexplained. The allocation permits this
reporting-only stop when the local signal does not reproduce the failure.

## Delivered behavior

A private path-carrying callback at `verifyBackupFile` entry lets the existing
read-only WAL child test record elapsed time, remaining context budget, path,
ordinal and context error. Timing stays test-side. Buffered marks print only
when the child test fails, including a later safety assertion failure.

The real `BackupDatabase` path reaches snapshot verification immediately after
copy completion, disposable restore-clone verification, then final verification.
The sequence test proves destination, clone, destination and plain-context
isolation. A deterministic test proves report formatting with an expired budget.
The sequence test failed with zero entries when the callback invocation was absent.

The two-second backup context, ten-second child timeout and fifteen-second
process bound are unchanged. Source checksums, 0444 permissions, failed-output
cleanup, interruption/no-hang protection and destination verification remain.
No threshold, assertion, dependency, migration, schema, CI or shard changed.

## Evidence and delivery

The ordinary-load baseline passed 200 of 200 selected sidecar executions.
Its one-minute start/end loads were 6.47/5.13 on sixteen cores. The instrumented
ordinary-load signal also passed 200 of 200, at loads 4.68/5.48. Both populations
retain their original results; neither is a retry substituted for a failed run.

The final candidate's seven selected safety, observer and report tests passed,
including both read-only source modes. Native Go reports record the same clean
commit before and after, Go 1.26.10 on Linux amd64, no test-cache reuse and no
parser errors. The [execution record](https://linear.app/sharper-flow/issue/CON-1589#comment-7c907366-b473-4567-bd97-d705e68c1600)
holds the bounded run identities and load observations.

Independent exact-head Review found no defects and supported both predicates.
Its recorded report is `workflow-9f4940635f9b82f015001a9e:completed` for
[candidate 5ccd98bd0bb31660a2be2a99c687b070c3a50a9b](https://github.com/Sharper-Flow/concord/commit/5ccd98bd0bb31660a2be2a99c687b070c3a50a9b).
The installed transition-time `go vet ./...` receipt qualifies that subject;
runtime proof comes from the separately retained Go reports, not that static check.

The normal shared pre-push hook and [required PR CI](https://github.com/Sharper-Flow/concord/actions/runs/41224022169)
passed. The [merge commit](https://github.com/Sharper-Flow/concord/commit/b26f46da96bf804d00cda4a1d70d1043969dc093)
and reviewed candidate have identical tree `f2dc8622b3680f2ebca995e14142d986be9a8b80`.
[Merge-group CI](https://github.com/Sharper-Flow/concord/actions/runs/41224475859)
passed on that exact merge commit. The default-branch
[CodeQL run](https://github.com/Sharper-Flow/concord/actions/runs/41224896041)
and [Release workflow](https://github.com/Sharper-Flow/concord/actions/runs/41224896834)
also passed. No installation or cutover occurred.

## Limits and remaining ownership

No qualifying idle-host window was obtained. The RTE prohibited forced quiet
or waiting for it and admitted this measurement gap. The local passes do not
establish reliability or identify which phase exhausted the original budget.
Buffered output is not guaranteed for a killed or non-returning child.

[CON-1582](https://linear.app/sharper-flow/issue/CON-1582) retains its own
failed-check, closed measurement population and acceptance holds. This delivery
provides no replacement sample, p90, acceptance or explanation for that work.
Its owner received the finding through a durable work-addressed message.

## Law changes

None. PM10's backup verification, source protection and cleanup obligations remain.
