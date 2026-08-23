# CD-0044: Worker evidence writes authenticate their caller

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** Caller authentication for the `worker-dispatch` / `worker-complete` /
  `worker-fail` CLI verbs; the `worker_evidence` client capability; terminal-attempt
  protection; issue #185
- **Approval:** Operator accepted the mechanism and the implement-with-decision
  sequencing on 2026-08-20; the public record is
  [issue #185](https://github.com/Sharper-Flow/concord/issues/185) and its
  required-outcome section
- **Related:** CD-0017 (D2, D4, D5), CD-0034, CD-0013 D5, CD-0042 D3/D4
- **Preserves:** The worker authority boundary; pinned-model readback identity;
  reviewer/model distinctness; owner-only result acceptance; short-lived CLI
  transport with no daemon
- **Supersedes:** The unauthenticated worker-evidence path and its literal
  `worker:cli` actor string

## Context

Three CLI verbs append durable worker evidence. Before this decision they
validated payload shape, lane identity, routing identity, and attempt linkage —
and then recorded `Actor: "worker:cli"`, a literal string, without
authenticating the caller or its authority to write that attempt.

Excluding those verbs from the generated agent tool catalog is not an authority
boundary. Concord's operating envelope is one operator and many local agents;
an agent with process execution can invoke the installed binary directly. It
could therefore forge a dispatch, fabricate a completion, or terminate another
attempt's evidence without holding any grant.

This is a different boundary from CD-0017 D4. That decision stops a worker from
advancing workflow state. This one governs who may create the evidence an owner
later consumes. Both are required: authentic evidence that grants no authority.

## Decision

### D1. Every worker-evidence write carries a signed assertion

A `worker-evidence-v1` assertion authenticates the caller. It reuses the
mechanism the repository already has: the `agent_clients` / `agent_client_keys`
registry with its rotation history and revocation state, Ed25519 verification,
the length-prefixed canonical byte format, and the `agent_nonce_replay` table.

The signed bytes bind the caller and the exact evidence identity: client, verb,
work, attempt, lane identity and digest, the readback model, the failure kind,
the host provenance digest, issue time, and a nonce. A signature captured for
one attempt authorizes nothing for another, and a dispatch assertion cannot be
presented as a completion.

[CD-0058](CD-0058-no-model-routing.md) supersedes the model-routing part of that
enumeration. This decision originally bound the routing policy version and
digest and the declared model fields. Concord no longer resolves a model, so the
readback model is the only model field the assertion carries.

Each verb binds the subset that applies to it, and unused fields are signed as
empty. `worker-dispatch` binds the host provenance digest; both terminal verbs
bind lane identity, which the CLI reads from the stored attempt row;
`worker-fail` also binds the failure kind. The shared vector declares that field
set per verb, and both sides test against it.

The canonical encoding is pinned by a shared vector that the Go encoder and the
adapter mirror both test against. A drift between the two would let one side
sign bytes the other never verifies; the vector makes that a failing test rather
than a silently weakened boundary.

### D2. Authentication and evidence share one transaction

The assertion nonce is consumed in the same transaction that appends the event.
Authorization that commits without its evidence, or evidence that commits
without its authorization, would both be defects. This is the same transaction
discipline CD-0013 requires of approval consumption.

### D3. `worker_evidence` is client-policy authority, never grant authority

A new capability authorizes evidence writing. It is registrable in a client
policy and deliberately absent from the grant-request vocabulary, so no bearer
token an agent holds can carry it and no agent-tool operation maps to it.
Revoking the client stops evidence writing immediately.

This keeps the authority where the mechanism can defend it. A grant is a bearer
token bounded by use count; worker evidence is instead authorized per write, by
a signature over that write.

### D4. A terminal attempt refuses further evidence

Signing alone does not prevent an authorized caller from overwriting a recorded
outcome. Completion and failure now refuse an attempt that already reached a
terminal lifecycle state, checked inside the same transaction that would append
the event. A recorded result is final.

### D5. The recorded actor is a verified identity

The literal `worker:cli` actor is replaced by the verified client reference and
its principal. An actor string that no mechanism checks is not attribution.

### D6. The proof never reaches the worker

The adapter signs after the run, from its own credential. The assertion is not
part of the lane packet, the spawned argv, or the returned envelope. A lane run
therefore cannot capture the proof that authorizes its own evidence. When the
credential is unavailable the run fails rather than recording unsigned evidence.

## Consequences

- The three verb request shapes change. Under CD-0042 D3 the old unsigned shape
  is deleted rather than shimmed; there is no pre-go-live compatibility path.
- The adapter needs a credential to record evidence. It already held one for
  grant and host-approval assertions; credential access moves to its own module
  so the lane dispatcher does not pull the plugin runtime in to sign.
- Operators must register the adapter client with the `worker_evidence`
  capability before lane dispatch can record anything.
- CD-0034 provenance is now also signed: the dispatch assertion binds the
  provenance digest, so evidence cannot understate which prompt surfaces were
  injected.

## Rejected alternatives

**A core-issued single-use capability token.** The issue lists this as an
acceptable shape. Rejected because it duplicates a mechanism that already
exists: it would need a new issuance verb, a new table, and a new expiry
lifecycle to reach the authentication, replay protection, rotation, and
revocation the client registry already provides.

**Whole-payload digest instead of enumerated fields.** Rejected because it would
require a canonical JSON serialization mirrored across two languages, where the
enumerated form reuses the length-prefixed format already proven by two
assertion types. The enumerated set covers every authority-bearing field.

**Trusting the CLI invocation as host proof.** This was the prior model. It
holds only if no untrusted process can execute the binary, which is false in an
envelope defined by many local agents.

**Adding `worker_evidence` to the agent manifest capabilities.** Rejected
because it would make the capability grant-requestable, which is precisely the
reachability D3 removes.

## Verification

- Unauthenticated, forged-key, unknown-client, wrong-attempt, cross-work,
  verb-swapped, stale, and provenance-mismatched writes all fail before any
  event is appended.
- A byte-identical replay of an accepted assertion is refused.
- A client without the `worker_evidence` capability, and a revoked client, are
  both refused.
- Evidence recorded after a terminal outcome is refused and the projection is
  unchanged.
- The recorded actor is the verified client identity, not `worker:cli`.
- The Go and TypeScript canonical encoders agree with the shared vector.
- The adapter's spawned argv and returned envelope contain no signature.
- CD-0017 model readback, distinctness, and owner-acceptance checks are
  unchanged.
