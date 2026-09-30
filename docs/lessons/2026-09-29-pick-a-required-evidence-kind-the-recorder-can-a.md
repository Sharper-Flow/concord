## What happened

Contract v1 on work-774a021db61550e890b1f184 declared `required_evidence: ["native_run", "commit"]`, meaning "the tests ran and passed". Every attempt to bind a `go test` invocation as `native_run` refused:

```
native_run evidence names no captured attributed record on this work
```

## Why

internal/store/workflow.go admits `native_run` only when the locator resolves to a row in `workflow_native_runs` or `external_observations` for that work item, and only while that row carries `verification_state = verified`. That mechanism (CD-0039, CD-0040) captures an attributed external or worker run report. A coordinator running a command in its claimed worktree produces no such row, so no locator of that kind can ever exist for the item.

The contract had to be superseded to v2 with `required_evidence: ["verification", "commit"]`, which cost one supersession and its audit trail.

## The rule

Before naming a `required_evidence` kind at contract approval, establish which recorder produces that kind and whether the recorder is in play for this work:

- `verification` — a command the recording session ran itself.
- `native_run` — an attributed run report already captured and verified against the work item.
- `commit` — a git commit SHA.
- `review`, `approval` — a reviewer or operator decision record.
- `durable_note`, `artifact` — a durable note or an artifact reference that the work's evidence binding admits.

A required kind no participant can produce is not a stricter contract. It is an unsatisfiable one.

## At completion

`complete` refuses when `evidence_commit` differs from the current commit (workflowCompletionBoundaryPreflight, internal/store/workflow_dispatch.go). After a squash merge the branch commit is no longer current, and `bind_evidence` is not a declared action at the `complete` step. Re-run verification on the merged commit and name that commit, rather than the branch commit the evidence was first taken on.
