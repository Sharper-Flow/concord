# CD-0160: The ci-wait utility delegates its wait to a deterministic CLI verb

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The `ci-wait` utility registry entry; the generated utility body;
  the `concord ci-wait` CLI verb in `cmd/concord`; the wait state file
- **Approval:** The operator requested deterministic bounded waiting and
  approved the investigation that produced this record.
- **Related:** CD-0017, CD-0081, CD-0149, CD-0157
- **Preserves:** The worker authority boundary; fail-closed command
  permissions; the utility distinction from lane evidence

## Context

The `ci-wait` utility waited by model reasoning. Its generated prompt told the
model to poll `gh` every 15 seconds, count its own iterations, and stop after
30 minutes or 120 iterations. No runtime surface enforced any of it. The
adapter's utility admission checks only managed-parent status, and the
generator consumed `time_seconds_max` as prompt prose alone.

Session logs bound the observed result. Each model-driven poll cost one `sleep`
command plus one model turn, and the measured turn cadence on a flagged session
ran 60 to 180 seconds per step. A 120-iteration wait therefore ran 90 to 210
minutes of wall time against a 30-minute instruction. One session logged 152
steps, past the iteration cap it was told to enforce. Three provider stream
errors hit `concord-ci-wait` sessions mid-wait, and each can end a wait with
nothing reported. The 30-minute and 120-iteration instructions were
incompatible under real turn cadence, and neither had a mechanism behind it.

Denied script writes in the same logs belong to other agents, not to this
utility: coordinators attempted to write polling shell scripts into
`/tmp/opencode` because the model-driven wait did not hold. That behavior is
corroborating evidence for a deterministic mechanism, and it is not part of
this change.

## Decision

### D1. The wait is a CLI verb, not a prompt

The core CLI gains `concord ci-wait`. It reads one JSON object from stdin with
`repo`, one `selector` of kind `pr`, `sha`, or `run`, an optional
`time_seconds_max`, an optional absolute `state_file`, and, for a `pr`
selector only, an optional 40-character `head_sha`. It queries GitHub
through read-only `gh` subcommands, classifies the observed state, and prints
one JSON report.

The verb is store-free. A CI wait touches GitHub and a state file, never
Concord authority, so it routes around the database open like the release
verbs. Every `gh` invocation is a query. The verb cannot mutate GitHub, write
a polling script, or start another agent.

### D2. One invocation is one bounded slice

One invocation polls and classifies for at most 100 seconds, then returns. The
host bash tool kills long-running commands at a hard maximum of 10 minutes, so
a single blocking 30-minute command cannot deliver a report. The 100-second
slice stays inside the default 2-minute command kill with margin.

The report status is closed: `pending` means re-invoke; `success`, `failure`,
`cancelled`, `timeout`, `superseded`, `error`, and `refused` are terminal. A
`pending` report carries the `state_file` path.

### D3. The deadline is carried by state, not by the model

The first invocation records `started_at`, `deadline_at`, and the iteration
count in a state file under the operator's state directory. Later invocations
resume that state. Checks mode defaults to and caps at 1800 seconds. Merge
mode defaults to and caps at 7200 seconds, so one watch can cover head checks
and a merge-queue run longer than thirty minutes. An explicit
`time_seconds_max` may shorten the selected mode's budget; a larger or
negative value is refused. A caller cannot extend a deadline that has started,
including by changing the budget or mode on resume.

At the deadline the wait returns `timeout`, and a pending check set at the
bound is a timeout. Per-poll sleeps stay inside the deadline, and the final
slice is truncated by a 5-second shutdown margin. A hung `gh` command runs in
its own process group and is killed at its command timeout, the slice end, or
the deadline, whichever lands first, so a hung command cannot prevent
termination.

A merge-mode timeout carries the last observed queue entry's state and
position, or its observed absence, in `merge_queue`. No successful queue read
means that field is absent, not that the pull request left the queue. The
state file preserves this observation across slices. The adapter's outer
watcher ceiling uses the same mode budget and retains the last queue
observation if that ceiling ends the watch before the verb reports.

### D4. Classification derives from observed results only

All selectors apply the common classification and reporting rule in
[CD-0228 D1](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md#d1-common-classification-and-reporting-rule).
Skipped checks are terminal and counted, not failures. Without a relevant
failure, an empty check set stays pending and times out at the bound; it is
never a success. The run-id selector
queries `gh run view`, the SHA selector reads the commit's workflow runs, and the
PR selector uses the rollup and page boundary in CD-0168 D1. Workflow-run and
job reads follow CD-0228 D1. Failure entries carry the observed name, URL, and a
coarse class derived from the name; a first-error line quotes available
diagnostic evidence and stays empty when unavailable. A failed-log read must
not delay failure delivery from an unfinished workflow. An empty field is
missing detail, not a verdict.

### D5. A changed pull request head supersedes the wait

The PR selector reads the head SHA on every poll. When the head changes, the
observed checks no longer belong to the watched commit, so the wait stops with
`superseded` and names both SHAs. Waiting through the change would report a
verdict for a SHA the caller did not ask about.

The watched commit is the one `head_sha` names, or the head the first poll
observes when the caller names none. A wait started beside a push can poll
before the push lands, and the head it then observes is the one the push
replaces. With `head_sha`, the wait stays `pending` and judges no checks
while the pull request head differs from it. A pull request merged or closed
at another head never reaches the commit, so the wait ends `superseded`. Once
the head reaches the commit, the wait latches it and a later change
supersedes the wait as above.

### D6. The utility allowance narrows to the verb

The utility's bash allowance is `concord ci-wait` and `concord ci-wait *`.
The verb owns polling, so the model has no command that can poll or wait
outside it. The generated body instructs the single invocation loop and
forbids model-side polling. The wall-time deadline bounds the wait.

### D7. Provider failures are report content, not wait defects

A stream error in the serving model can still end a utility session mid-wait.
The verb removes the per-poll model turns that made waits fragile and
expensive, but it cannot make a provider transport reliable, and no claim is
made that it does. A `gh` failure is an explicit `error` report quoting gh's
own words, never a success.

## Consequences

- The manifest digest moves, and all generated projections regenerate.
- The installer ships the new body with the existing utility file name.
- The verb is operator-visible in `concord --help` and reads JSON stdin like
  the other store-free verbs.
- The wait state file lives under the operator's state directory and outlives
  one invocation. A state file past its deadline refuses to resume and is
  removed on the timeout report.
- A coordinator that wants ad-hoc
  GitHub reads during a wait needs a different surface; this record does not
  provide one.

## Verification

- `python3 scripts/generate-agent-lanes.py --check` proves every generated
  projection matches the manifest and templates.
- `python3 scripts/test-ci-wait-contract.py` proves the CLI verb, its
  refusal paths, its deadline behavior, and the generated body's delegation.
- [CD-0228 Verification](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md#verification)
  names the required fail-fast, queued-workflow and newest-attempt checks.
- The Go tests in `internal/ciwait/ci_wait_test.go` prove terminal outcomes for
  passing, failing, cancelled, skipped, pending, and empty check sets; the
  hung-command kill; the carried deadline; the head-SHA policy, including
  `TestCiWaitPRExpectedHeadJudgesOnlyThePushedCommit` and
  `TestCiWaitPRExpectedHeadNeverReachedIsSuperseded` for a named commit; and
  the selector routing for PR, SHA, and run id.
- `TestCiWaitMergeOutcomeAfterThirtyMinutes` proves one carried merge wait
  reports merged, dropped, and failed-check outcomes after thirty minutes.
  `TestCiWaitModeBudgets` proves the defaults and refusal bounds;
  `TestCiWaitMergeTimeoutRetainsQueueObservation` proves timeout detail
  survives a resumed slice.
- `go test -race ./internal/ciwait/ ./cmd/concord/` proves the verb inside its
  engine and command package suites, including `TestCiWaitIngress`.
