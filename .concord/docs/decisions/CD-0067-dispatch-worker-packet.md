# CD-0067: Dispatch Binds the Worker Packet

Status: accepted
Date: 2026-08-24

Amended by: CD-0143 for the fresh packet and epoch required by verdict correction
after completed and accepted worker delivery.

Amended by: [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2 and D6 — dispatch writes current rows; no correction budget or attempt count exists.

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
The dispatch record and `FindAuthorizedDispatchWindowTx` expose that immutable commit,
never a replacement derived from current state or a worker assertion.
The integrated view carries only `subject_commit`, not a parallel candidate
field. This extends the existing packet-derived predicate binding pattern;
it does not retain packet bodies or create another authorization record.
A present commit must be a valid raw commit OID. Missing candidates remain
absent, including historical completions and non-oracle dispatches without
a candidate. No later write fabricates the field or rewrites an earlier completion.
Native oracle execution requires current clean `HEAD`, current core subject,
and this recorded dispatch commit to be nonempty and equal.
Missing or unequal values refuse with zero test-program launches.
Ordinary historical dispatch behavior remains unchanged; absent commit binding
grants no native oracle execution. No store migration is needed for this
completion-payload field.

The packet declares its output protocol. `inputs.report_protocol`, when
present, names the report framing its worker must use —
`concord-worker-result-v1` — and every new packet builder sets it. The field
travels inside the digested packet object, so `worker_packet_digest` pins it
with the admitted packet identity, typed context, and recorded subject binding
defined above. The core does not judge semantic adequacy. A packet without
the field is historical, and its output follows
the legacy report grammar; a packet that pins the protocol never falls back
to that grammar.

The dispatch completion also records `worker_context_frontier`, the
nonnegative source frontier of the admitted work-context view: the
highest sequence among the work item's context-source records at read time. A
present view carries its frontier, including zero, and a dispatch whose
admitted view is absent carries explicit zero. Absence of the recorded field
stays reserved for terminals recorded before this field existed, and no
reader turns that absence into zero. Every terminal record whose
authorization recorded the field derives `consumed_context_frontier` from
that authorization: a completed and a failed report carry it, a failure
without a report included. The field attributes the terminal input to the
exact authorization; it never claims the worker read every entry.

A completed implementation-lane report records `observed_delivery`, the
closed delivery observation the store took itself: one raw commit OID and
the `clean` or `dirty` state of the attempt's authorized claimed worktree.
The store observes before the terminal transaction opens, and the ingest
revalidates the sampled window, attempt, and claim identities with SQL only
before the append. A caller, a packet, or a worker value can neither supply
nor alter the field. Dirty but reachable worktrees record `dirty`. A Git
failure or deadline refuses the completion with zero terminal events,
derived failures, and nonce effects; no `unavailable` observation exists. An
exact acknowledgment of a recorded completion returns the stored observation and
probes no Git. A completion whose authorization recorded no worktree
binding, and every completion recorded before this observation existed,
keeps explicit absence. The store never fills that absence from current
state. The missing observation is an approved known cost, and the remedy is
a fresh implementation attempt, never fabricated clean evidence.

The dispatch admission is the one live subject-freshness owner. Before the
action transaction opens, the store observes the claimed worktree's `HEAD`
and clean status under one fixed deadline. Inside the transaction it
revalidates the sampled identities with SQL only, and a live clean `HEAD`
that does not equal the admitted `subject_commit` refuses before any effect.
The review lane's own `HEAD` and tree check against the packet subject stays
the backstop for drift after authorization; no adapter probe runs at the
dispatch boundary.

A review dispatch joins the newest accepted implementation delivery on the
review's own claimed worktree. The join runs through acceptance, the exact
completed attempt, and the dispatch identity. That delivery's clean
`observed_delivery` must equal the shared qualified `subject_commit`. A
delivery recorded under an earlier contract version of the same work item
may host the review under the active contract. The judging contract is the
packet's own binding, which the admission admits only at the active
contract. The accepted delivery's own contract version stays readable
through the existing records. Newer rejected, failed, or unaccepted results
cannot replace the accepted delivery, and they do not let an older one
through. A delivery from another work item, Project, or worktree cannot host
the review, and a cancelled or superseded work item cannot review. This join
opens no approval challenge. A review whose joined delivery carries no clean core observation
refuses; the approved remedy is a fresh implementation attempt.

The dispatch packet is admitted whole under the shared dispatch context
bound. The canonical JSON object of the packet's present
`inputs.work_context`, `inputs.checkpoint`, and `inputs.correction` members
may not exceed the bound CD-0016 declares. An over-bound packet refuses
whole and is never trimmed. A derived retry dispatch answers to the same
bound.

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
