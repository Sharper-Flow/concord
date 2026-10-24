# CD-0037: Approval consequence summaries are derived from validated facts

- **Status:** Accepted
- **Date:** 2026-08-17
- **Scope:** Core-issued approval challenges; TS5–TS7 approval boundary; issue #172
- **Approval:** Operator selected core-derived summaries on 2026-08-17
- **Related:** CD-0005, CD-0013, CD-0035, TS1 AJ8, TS5
  ([`agent-call-context-contract.md`](../agent-call-context-contract.md)), TS6
  ([`agent-adapter-transport-contract.md`](../agent-adapter-transport-contract.md)),
  TS7 ([`agent-result-envelope.md`](../agent-result-envelope.md))

**Approval amendment:** [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
limits operator consent to its six classes. Consequence summaries and operation binding do not themselves create an approval requirement; all other checks remain required.

## Context

TS5 says an approval binds the consequence summary. TS6 says a core-issued
approval challenge carries that summary into the host permission prompt. The
runtime does neither. It emits a generic string in `error.details["summary"]`,
and the adapter does not put that string or the consequence class in the prompt
metadata. TS1 `AJ8-approval-required` therefore cannot show what the operator is
being asked to approve.

The caller cannot fill this gap. Letting a requester describe its own
consequential operation would turn caller prose into approval authority, which
TS5 expressly rejects. Concord already holds the facts that matter: operation,
consequence class, canonical digest, resolved scope, expected versions, and
challenge expiry. It must summarize those facts itself.

## Decision

### D1. The summary is a typed error affordance

`TypedError` gains a bounded `consequence_summary` object. It is not a universal
envelope field and it does not live in the open-ended `details` map. The object
contains:

```text
tool
operation
consequence
operation_digest
scope[]
versions[]
expires_at
work_decision? { work_id, contract_version, decision_domains[] }
```

The object is closed. Scope and version bindings use the existing canonical,
sorted renderers. Agents branch on these fields; they do not parse a sentence.
The host may render the object for a person, but neither the adapter nor the
caller may add domain meaning.

### D2. Core-minted challenges always carry it

The summary is required whenever the core has minted an approval challenge,
including workflow actions, generic consequential mutations, and a governing-
requirement conflict that minted the challenge for an approved scope cut. The
coupling is challenge presence, not error kind alone.

An `approval_required` refusal that did not mint a challenge carries no summary.
Examples include a compaction publish called without the approval reference it
already requires, or a workflow completion awaiting premise confirmation. Those
errors tell the caller how to obtain or supply authority; they are not a new
consent prompt and must not fabricate one.

### D3. Only consumption-validated facts may enter

The summary is derived from the exact digest, consequence, scope, versions, and
expiry bound to the challenge. Tool and operation come from the validated
invocation. Caller-authored title, reason, value statement, tags, free-form
fields, workflow premise text, and labels resolved from caller input are
excluded. A workflow premise remains separate bounded context.

The sole classification exception is `work_decision`: validated typed decision domains from the proposed contract or recorded active contract, with its work and contract version. It is required on CD-0013 D11 decision-bearing challenges. Its values bind to the canonical input, scope, and versions and are revalidated on consumption. Missing historical classification never means engineering. The host displays these fields unchanged. Routine publication and engineering confirmation mint no consent challenge under that policy; challenge-less refusals still carry no fabricated summary.

### D4. Summary drift is unrepresentable

The summary is derived when the challenge is returned; it is not stored as an
independent mutable string. Approval consumption already compares the canonical
digest, scope JSON, version JSON, and consequence byte-for-byte with the
challenge. The summary uses those same values. Changing anything it describes
therefore invalidates the challenge instead of silently changing the prompt.

A fresh affirmative host assertion permits one pending-challenge re-arm policy.
The core may re-arm an active, unused, expired single-use challenge when every
decision and caller binding still matches. It must validate the answer's
timestamp, request digest, scope, versions, consequence, and host binding first.
The new deadline uses the original finite interval, bounded at challenge creation.
The core re-arms, creates approval, consumes approval, and records the authorized
intent in one transaction. Any failure rolls back the entire exchange.
The original prompt summary stays unchanged. Its deadline describes the pending
invitation. This explicit policy permits fresh consent after that deadline.
No other summary fact may change, and an issued approval is never re-armed.

The summary object itself need not enter the signed host assertion: the signed
challenge reference already binds to the stored facts from which it is derived.
This satisfies TS5 §5's consequence-summary binding: the approval binds the
facts from which the typed summary is derived, rather than a second stored
string that could drift.

### D5. The host renders; the adapter transports

The adapter copies the typed object unchanged into host permission metadata. The
host renders the operator prompt. This is presentation, not adapter-owned domain
logic. A renderer may choose layout or localization, but it must show the
consequence class, operation, scope, versions, and expiry without dropping or
rewriting them.

### D6. This is a TS7 and TS6 contract amendment

The canonical envelope schema, Go validation, generated TypeScript, adapter
metadata, and compatibility tests move together. Because a strict old client
cannot accept the field, and omitting it would recreate the unsafe approval
prompt this decision fixes, there is no lossless down-conversion for affected
approval paths. TS8 therefore treats the first release carrying this field as a
major surface amendment. Old clients fail negotiation before a domain call;
they do not receive approvals without a consequence summary.

The major-cutover obligations are closed here:

1. `AJ8-approval-required` is the failing/passing TS1 evidence.
2. Manifest, schema, generated clients, adapter metadata, and docs move in one
   implementation change.
3. Migration has no field alias: clients regenerate against the new major.
   Unconsumed challenges minted before cutover are invalidated because their
   original prompt lacked the required summary; consumed approvals remain audit
   history.
4. Old/new adapter-core compatibility tests prove fail-closed negotiation.
5. Durable domain operations and their current rows do not change.
6. This record carries explicit operator acceptance.

## Rejected alternatives

**Caller-authored summary.** The requester would author the description of its
own consequential request. Shape and length validation cannot make that prose
authoritative.

**Core facts plus caller rationale in the same prompt.** This puts trusted and
untrusted accounts side by side at the approval boundary. A future rationale
field needs its own decision.

**`error.details["summary"]`.** `details` is not a typed operator contract and
cannot enforce the challenge coupling or closed contents.

**Top-level envelope field.** Only challenge-bearing errors need the value.
Widening every outcome variant would add reach without adding safety.

**Persisted display string.** It could drift from the challenge facts. The
structured values are the authority; rendering is derived.

## Verification

- `AJ8-approval-required` binds and proves the operation did not start or rotate
  a credential.
- Every core-minted challenge carries a non-empty typed summary; challenge-less
  approval refusals do not.
- Map iteration cannot change the scope or version order.
- Adversarial caller prose cannot alter any summary field.
- Changing digest, scope, version, or consequence invalidates approval.
- Expiry refuses approval except for the pending-challenge policy in D4.
- That policy requires fresh exact host consent and consumes authority once.
- The adapter transports the object unchanged and does not generate the summary.
