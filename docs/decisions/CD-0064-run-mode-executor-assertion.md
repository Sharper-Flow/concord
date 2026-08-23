# CD-0064: Run-mode lane dispatch asserts its executor

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** Lane agent selectability in run mode; executor identity evidence;
  the adapter's dispatch boundary; issue #426
- **Approval:** Operator accepted the fix direction as filed in
  [issue #426](https://github.com/Sharper-Flow/concord/issues/426) on 2026-08-23
- **Related:** CD-0017 (D2 dispatch flag unchanged), CD-0056 (D7 admission
  boundary), CD-0058 (D2 scope clarified), CD-0059 (D1 attempt window),
  CD-0063 (central agent definitions)
- **Preserves:** the closed lane registry and its digests; `agent-lane-packet.v1`
  and `agent-lane-report.v1`; CD-0058 D1 in full
- **Supersedes:** nothing

## Context

Every generated lane agent carried `mode: subagent`. The adapter dispatches
through `opencode run --agent concord-<lane>`, and run mode rejects a
subagent-mode agent: it prints a warning to stderr and falls back to the
default agent. Issue #426 reproduced this on opencode 1.18.21 with the exact
dispatch argv the adapter builds.

The fallback was silent end to end. The warning never reaches the adapter's
typed boundaries. The envelope recorded the lane agent unconditionally, and
the sanitized session export readback asserted model identity only. A
default-agent session that emitted a report-shaped final message was admitted
as `worker-complete` for a lane whose contract it never loaded: no evidence
obligations, no report format, and no nesting guard reached the worker.

The readback already carries the executor identity. Every message in a
sanitized export carries an `agent` field, so the latest assistant message
names the agent that produced the output the adapter is about to admit.

## Decision

### D1. Lane agents are selectable in run mode

The generator emits `mode: all` for lane agents. A lane definition stays
spawnable through the host Task tool and becomes selectable by run mode, so
the dispatch transport resolves the agent the adapter names. The nesting guard
is unchanged: lane frontmatter continues to deny task dispatch, and the
host-level permission map from CD-0017 keeps generic agents out.

### D2. The executor identity is asserted from the export readback

The sanitized export readback returns the executing agent beside the
executing model, both taken from the latest assistant message. Before any
evidence is recorded, the adapter asserts the readback agent equals the
dispatched `concord-<lane>` identity. A mismatch returns a typed
`agent_identity_mismatch` failure with `contact_operator` recovery and
records no worker evidence, so a substituted executor can never drive
`worker-complete`. An export whose assistant message carries no typed agent
string is not a readback; the dispatch fails closed.

### D3. CD-0058 D2 is model-scoped

CD-0058 withdrew model substitution detection because the adapter no longer
asserts a model. The agent axis differs in kind: the adapter still names the
executor, so executor identity is an intent the readback can violate. CD-0058
D6's "envelopes and identity only" validation covers this assertion, and
CD-0058 D1, D3 through D5 are untouched.

## Invariants

1. A lane dispatch that completes recorded an executing agent equal to the
   dispatched lane agent, taken from the sanitized export.
2. No dispatch whose executor differs from the lane identity records
   `worker-complete` or any worker evidence verb.
3. Lane frontmatter denies task dispatch in every generated definition.

## Consequences

- A host whose agent resolution substitutes the executor gets a typed failure
  naming both identities, instead of silently wrong evidence.
- `agent-lane` digests and both lane schemas are unchanged; only the
  generated frontmatter line and the adapter boundary move.
- The CD-0059 D1 attempt window opens before spawn, so a post-readback
  assertion failure returns a typed envelope without a worker event, the same
  posture as the existing session-identity and export failures.

## Verification

- `opencode run --agent concord-<lane>` executes as the lane agent on
  opencode 1.18.x with no fallback warning.
- The adapter test suite covers the substituted-executor case: typed
  `agent_identity_mismatch`, no worker evidence verbs.
- `scripts/generate-agent-lanes.py --check` passes with `mode: all`
  projections.
