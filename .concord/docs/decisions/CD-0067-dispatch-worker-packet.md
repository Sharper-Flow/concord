# CD-0067: Dispatch Binds the Worker Packet

Status: accepted
Date: 2026-08-24

Amended by: CD-0143 for the fresh packet and epoch required by verdict correction
after completed and accepted worker delivery.

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

The core validates packet identity and declared typed context against its
current records. It records `worker_packet_digest`, SHA-256 over canonicalJSON
of that same admitted packet, beside `worker_attempt_id` on the completion.
The completion builder also records `worker_subject_commit` from the same
tx-scoped core view whose `subject_commit` the packet validation admitted.
The fold and `FindAuthorizedDispatchWindowTx` expose that immutable commit,
never a replacement derived from current state or a worker assertion.
The integrated view carries only `subject_commit`, not a parallel candidate
field. This extends the existing packet-derived predicate binding pattern;
it does not retain packet bodies or create another authorization record.
A present commit must be a valid raw commit OID. Missing candidates remain
absent, including historical completions and non-oracle dispatches without
a candidate. No upcast fabricates the field or changes historical authority.
Native oracle execution requires current clean `HEAD`, current core subject,
and this recorded dispatch commit to be nonempty and equal.
Missing or unequal values refuse with zero test-program launches.
Ordinary historical dispatch behavior remains unchanged; absent commit binding
grants no native oracle execution. No store migration is needed for this
completion-payload field.

Amended 2026-10-09 for the packet output-protocol pin
([Linear](https://linear.app/sharper-flow/issue/CON-891/give-the-workers-final-report-an-explicit-protocol-identity-instead-of)):
the packet declares its output protocol. `inputs.report_protocol`, when
present, names the report framing its worker must use —
`concord-worker-result-v1` — and every new packet builder sets it. The field
travels inside the digested packet object, so `worker_packet_digest` pins it
with the admitted packet identity, typed context, and recorded subject binding
defined above. The core does not judge semantic adequacy. A packet without
the field is historical, and its output follows
the legacy report grammar; a packet that pins the protocol never falls back
to that grammar.

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
