# CD-0064: Native lane dispatch asserts its executor

- **Status:** Accepted
- **Date:** 2026-08-23
- **Scope:** Subagent-only lane definitions; executor identity evidence;
  the adapter's native Task dispatch boundary
- **Approval:** Operator accepted the fix direction as filed in
  [issue #426](https://github.com/Sharper-Flow/concord/issues/426) on 2026-08-23
- **Related:** CD-0017 (D2 dispatch flag unchanged), CD-0056 (D7 admission
  boundary), CD-0058 (D2 scope clarified), CD-0059 (D1 attempt window),
  CD-0063 (central agent definitions)
- **Preserves:** the closed lane registry and its digests; `agent-lane-packet.v1`
  and `agent-lane-report.v1`; CD-0058 D1 in full
- **P12:** Serious: generated execution lanes are subagent-only native Task
  targets, not primary-capable agents.

## Context

CD-0178 D1 dispatches lanes through the host's native Task tool in a child
session. A worker is not an operator session agent: it needs a bound packet,
attempt, capability ceiling, and report obligations. Making its definition
primary-capable grants an entry path that supplies none of those bindings.

The native Task route resolves the named subagent directly. Executor readback
must still establish that the assistant producing a report is the dispatched
lane, rather than trusting the agent name the adapter requested.

## Decision

### D1. Lane agents are subagent-only native Task targets

The generator emits `mode: subagent` for every execution lane. The native Task
route resolves its exact `concord-<lane>` identity; a lane is not a primary
session or CLI-run target. CD-0070 supplies `hidden: true` as the discovery
boundary. Neither field grants dispatch authority: CD-0178 D1's native route
still requires the typed packet and authorized attempt window.

Lane frontmatter continues to deny task dispatch, and the host-level permission
map from CD-0017 keeps generic agents out. Lane capabilities and general
Concord-tool denials do not change.

### D2. The executor identity is asserted from the native readback

The native child-session readback returns the executing agent beside the
executing model, both taken from the latest assistant message. Before any
evidence is recorded, the adapter asserts the readback agent equals the
dispatched `concord-<lane>` identity. A mismatch returns a typed
`agent_identity_mismatch` failure with `contact_operator` recovery and
records no worker evidence, so a substituted executor can never drive
`worker-complete`. A readback whose assistant message carries no typed agent
string is not a readback; the dispatch fails closed.

### D3. CD-0058 D2 is model-scoped

CD-0058 does not require model substitution detection: the host selects the
model. The agent axis differs in kind: the adapter still names the
executor, so executor identity is an intent the readback can violate. CD-0058
D6's "envelopes and identity only" validation covers this assertion, and
CD-0058 D1, D3 through D5 are untouched.

## Invariants

1. A lane dispatch that completes recorded an executing agent equal to the
   dispatched lane agent, taken from the native child-session readback.
2. No dispatch whose executor differs from the lane identity records
   `worker-complete` or any worker evidence verb.
3. Lane frontmatter denies task dispatch in every generated definition.
4. Every generated lane is subagent-only; no lane is a primary session agent.

## Consequences

- A host whose agent resolution substitutes the executor gets a typed failure
  naming both identities, instead of silently wrong evidence.
- `agent-lane` digests, both lane schemas, and the adapter dispatch boundary
  are unchanged by the definition mode.
- The CD-0059 D1 attempt window opens before spawn, so a post-readback
  assertion failure returns a typed envelope without a worker event, the same
  posture as the existing session-identity and export failures.

## Verification

- `adapter/opencode/evals/native-lane.native.case.ts` runs all five hidden
  subagent identities through the native Task executor on OpenCode 1.18.35,
  with a fixture model and exact child assistant-agent readback. It proves
  host mode compatibility, not managed worker-report acceptance.
- The adapter test suite covers the substituted-executor case: typed
  `agent_identity_mismatch`, no worker evidence verbs.
- `scripts/generate-agent-lanes.py --check` passes with `mode: subagent`
  projections.
