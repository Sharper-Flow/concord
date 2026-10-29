# CD-0199: The plugin-owned watcher replaces the ci-wait utility

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** The `concord_ci_watch` tool in `adapter/opencode/ci-watch.ts` and
  `adapter/opencode/concord-plugin.ts`; the
  `concord ci-wait` verb in `cmd/concord` stays unchanged
- **Preserves:** CD-0160 D1 through D5 and D7 in full (the verb half), CD-0161
  and CD-0168 in full (both bind the verb), CD-0166 D1 and D4 (no core prober,
  no timer), CD-0028 D5, and CD-0149's remaining utilities
- **Related:** CD-0149, CD-0160, CD-0161, CD-0166, CD-0168
- **Approval:** The operator approved the work item contract that carries this
  record on 2026-09-30.

## Context

CD-0160 made one wait slice deterministic, but the utility model session
stayed the loop owner. The generated body re-invoked the verb from a subagent,
so every slice cost one subagent model turn, and the parent coordinator's turn
blocked on the Task call for the whole wait. A 30-minute wait ran up to 18
subagent turns and held the session busy instead of idle.

The wait itself needs no model. Two earlier wake routes failed. An external
process that posted to the plugin `serverUrl` got connection refused on every
wake, because the TUI opens no listener. The in-process `promptAsync` route
works only with care: a busy session persists the prompt but never schedules a
turn, and an idle session can drop one.

## Decision

### D1. The watcher is a plugin tool, and the loop has no model

`concord_ci_watch` spawns the existing `concord ci-wait` verb slice by slice
as a plugin-owned child process, carries the state file across slices, and
returns the tool call at once. The verb keeps polling, pacing, the deadline,
and classification. The core gains nothing: no prober, no GitHub client, and
no timer (CD-0166 D1/D4, CD-0028 D5).

### D2. Delivery rides the host's in-process client

The plugin delivers the terminal report through the client the host handed
the plugin, which reaches the server in process when no listener exists. The
delivery is one `prompt_async` call with a synthetic text part and the
session's persisted agent, model, and model variant. Every prompt the watcher
posts carries that identity, because the host persists each prompt's choice
for later turns.

### D3. Delivery survives the lost-wake defects

The watcher delivers only to an idle session. After the injection it confirms
that an assistant reply follows the injected message. Any failure to deliver
or to confirm queues the report, and the next `chat.message` carries the
queued report into the session as a synthetic part. Each queued report also
gets one more try through the same confirmed delivery: at once when the host
reports the session idle as the report is queued, and otherwise when the host
next reports the session idle. A report that try does not confirm stays
queued. The watcher logs every delivery failure and swallows none.

### D4. The utility is removed in this change, behind the live proof

Coordinators route CI waits through `concord_ci_watch`. Verification requires
one live CI wait that proves zero model turns during the wait and a delivered
wake before shipment. The verb keeps its contract.

## Alternatives considered

- One blocking command for the whole wait. Rejected: the host shell tool ends
  a command at its timeout, so a 30-minute blocking call dies mid-wait.
- Keep the utility subagent beside the watcher. Rejected: it keeps paying one
  model turn per slice for a route the watcher replaces.
- An external process posting to the plugin `serverUrl`. Rejected: the TUI
  opens no listener, so every wake got connection refused.
- A GitHub webhook receiver. Rejected: it needs a public endpoint and adds a
  service for no gain over the verb's own pacing on one host.
- A core-side CI poller or automatic await-condition resolution in the store.
  Rejected: CD-0166 D1/D4 and CD-0028 D5 forbid core-side pollers and timers.

## Consequences

A coordinator that starts a wait ends its turn at once and stays idle. Zero
model turns run during the wait. The session wakes with the terminal JSON
report and acts on it in the same session. A lost wake is detected, queued,
and delivered on the next message instead of vanishing.

The registry carries no `ci-wait` utility. A host that starts
`concord-ci-wait` fails closed, and coordinators route waits through
`concord_ci_watch`. The verb keeps the CD-0160 contract: the same JSON stdin,
the same closed status set, and the same state-file rules. The installer
stops shipping the utility body and removes a previously placed copy on
upgrade.

## Verification

```gherkin
Scenario: A watch costs no model turns and wakes the session
  Given a coordinator session that started concord_ci_watch
  When the verb reports pending and then a terminal status
  Then the tool call returned at once, no prompt ran before the terminal slice, and one synthetic wake carried the report with the session's agent, model, and model variant, and the session answered it

Scenario: A lost wake is queued and delivered on the next message
  Given a terminal report still queued because its injection was refused, dropped, or unconfirmed
  When the session receives its next chat message
  Then the queued report joins that message as a synthetic part, and the queue drains exactly once

Scenario: The utility is gone and the projections agree
  Given the registry without the ci-wait utility
  When the generator runs
  Then no generated projection carries the utility, and the installer file list omits its body

Scenario: The verb keeps its contract
  Given the unchanged concord ci-wait verb
  When its tests run
  Then every terminal outcome, deadline, and selector rule passes as before
```

- `bun test adapter/opencode/ci-watch.test.ts` proves the first two scenarios.
- `python3 scripts/generate-agent-profiles.py --check` checks current utility
  projections. Directory and installer checks must prove the third scenario.
- `go test ./internal/ciwait/ ./cmd/concord/ -run 'TestCiWait|TestCIWait|TestGHRunID'`
  proves the fourth scenario, including `TestCiWaitIngress` in the command
  package.
- `python3 scripts/test-ci-wait-contract.py` proves the verb contract offline.
- `bun test adapter/opencode/session-scope.test.ts` checks session scope.
  Utility admission retains no worker dispatch authority.
- One live CI wait on this host starts `concord_ci_watch` and observes zero
  model turns and one delivered wake. Its delivery protocol is the one
  `bun test adapter/opencode/ci-watch.test.ts` proves. This is D4's gate, and
  the work's completion verdicts own the decision.
