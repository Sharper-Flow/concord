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
- **Supersedes:** The model-maintained wait procedure in the generated
  `ci-wait` body: polling by `sleep 15`, the 120-iteration count, and the
  `sleep *` and `date *` command allowances

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
`time_seconds_max`, and an optional absolute `state_file`. It queries GitHub
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
resume that state. The caller's `time_seconds_max` is a ceiling bounded at
1800 seconds; a larger or negative value is refused. A caller cannot extend a
deadline that has started.

At the deadline the wait returns `timeout`, and a pending check set at the
bound is a timeout. Per-poll sleeps stay inside the deadline, and the final
slice is truncated by a 5-second shutdown margin. A hung `gh` command runs in
its own process group and is killed at its command timeout, the slice end, or
the deadline, whichever lands first, so a hung command cannot prevent
termination.

### D4. Classification derives from observed results only

`success` requires at least one observed check and no failed, cancelled, or
pending check. Skipped checks are terminal and counted, not failures. Any
failed or cancelled check is a `failure`. An empty check set stays pending and
times out at the bound; it is never a success. The run-id selector queries
`gh run view`, the SHA selector queries `gh run list --commit`, and the PR
selector queries `gh pr checks`. Failure entries carry the observed name, URL,
and a coarse class derived from the name; a first-error line is quoted from a
failed-log read when one succeeds, and stays empty when it does not. An empty
field is missing detail, not a verdict.

### D5. A changed pull request head supersedes the wait

The PR selector reads the head SHA on every poll. When the head changes, the
observed checks no longer belong to the watched commit, so the wait stops with
`superseded` and names both SHAs. Waiting through the change would report a
verdict for a SHA the caller did not ask about.

### D6. The utility allowance narrows to the verb

The registry entry moves to version 2. Its bash allowance is `concord ci-wait`
and `concord ci-wait *`. The `gh` query allowances, `sleep *`, and `date *`
are removed: the verb owns polling, so the model has no command left that can
poll or wait outside it. The generated body instructs the single invocation
loop and forbids model-side polling. The iteration ceiling of 120 was subsumed
by the wall-time deadline and is not carried forward as a second bound.

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
- The old prompt's gh allowances are gone. A coordinator that wants ad-hoc
  GitHub reads during a wait needs a different surface; this record does not
  provide one.

## Verification

- `python3 scripts/generate-agent-lanes.py --check` proves every generated
  projection matches the manifest and templates.
- `python3 scripts/test_ci_wait_wait_contract.py` proves the CLI verb, its
  refusal paths, its deadline behavior, and the generated body's delegation.
- The Go tests in `cmd/concord/ci_wait_test.go` prove terminal outcomes for
  passing, failing, cancelled, skipped, pending, and empty check sets; the
  hung-command kill; the carried deadline; the head-SHA policy; and the
  selector routing for PR, SHA, and run id.
- `go test -race ./cmd/concord/` proves the verb inside the full command
  package suite.
