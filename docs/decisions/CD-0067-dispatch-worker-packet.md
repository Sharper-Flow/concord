# CD-0067: Dispatch Binds the Worker Packet

Status: accepted
Date: 2026-08-24

## Context

Issue #253: the lane worker pipeline reaches no installation. The
dispatch_worker action (CD-0059) opens a single-use attempt window, but the
action fields carry only `attempt_id`, so the durable record binds the window
to an attempt identity and to nothing about the work the worker will do. The
lane packet — lane identity, step, and inputs — lives only in the adapter, and
the core cannot later prove which packet an authorized window was opened for.

## Decision

### D1. The packet is declared in the action contract

`work_transition_action_input.fields.worker_packet` is a closed, declared
object in contracts/agent-tool-surface-payloads.schema.json, required for
`dispatch_worker`. Rejected alternatives: deriving the packet in the adapter
without a contract field (the core then records an attempt bound to nothing it
can name), and splitting dispatch into two actions (a second action doubles the
window semantics CD-0059 D5 already owns).

### D2. The core digests the packet and records it as attempt evidence

The core does not interpret the packet. It enforces object-ness, two identity
equalities (packet.work_id equals the action's work_id, packet.attempt_id
equals fields.attempt_id), and records `worker_packet_digest` — sha256 over
canonicalJSON of the packet — on the WorkflowActionCompleted event beside
`worker_attempt_id`. `FindAuthorizedDispatchWindowTx` exposes it on
WorkerDispatchWindow. domain_events.payload is untyped JSON, so no store
migration is needed.

### D3. Closed-object fields get a registry value type

A new PayloadValueType `object` validates that a field is one strict JSON
object. dispatch_worker declares worker_packet with that type and
Required: true. Structural bounds beyond object-ness live in the contract
schema, which the generator embeds; the core gate stays cheap.

### D4. Reachability acceptance for #253

The lane pipeline is proved reachable when the installed archive ships the
lane files and a test drives dispatch through work_transition to the spawn
boundary with a stubbed runner. A live model round trip is not required: it
proves the runner, not reachability.

### D5. The dispatch entry surface is work_transition

The orchestrator cannot author a lane packet — bounds and lane digests are
machine-derived — so the adapter completes the request. The orchestrator calls
`concord_work_transition` workflow_action with `action_id: dispatch_worker` and
`fields.lane_id`, the one parameter that cannot be derived (workflow step ids
and lane ids do not map one-to-one). The adapter derives product identity and
step from `concord_work_trace.continuity`, generates the attempt id, builds the
packet from recorded state, and performs the core invoke with the enriched
fields. `lane_id` is tool-level vocabulary (shared fields enumeration) and is
never forwarded to the core, which records the packet digest instead. The
installed archive ships dispatch.ts, packet.ts, lane_dispatch.ts, and
generated-agent-lanes.ts.

### D6. The signed digest quotes the core

The dispatch assertion's packet_digest is the value the core returned in the
dispatch_worker response, not a value the adapter computes. Matching Go's
canonicalJSON byte-for-byte from TypeScript would couple the two languages'
JSON encoders for arbitrary narrative text; quoting the recorded digest needs
no such contract and still binds the evidence to the packet the core digested
and authorized. A window whose recorded digest is empty predates this
decision: the evidence boundary refuses it and the operator opens a fresh
authorization. The canonical assertion format gains packet_digest (signed
empty for complete and fail); the shared worker-evidence vector repins both
encoders.

## Consequences

- Existing dispatch_worker callers must supply the packet; in-repo tests are
  updated in the same change.
- The evidence boundary can later refuse worker evidence whose packet digest
  does not match the window (follow-up wiring in the adapter).
- contracts/agent-tool-surface payload digest and generated artifacts change;
  the generator owns them.