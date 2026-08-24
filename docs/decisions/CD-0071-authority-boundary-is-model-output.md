# CD-0071: The authority boundary is the model's output, not the agent's process

- **Status:** Accepted
- **Date:** 2026-08-24
- **Scope:** What the agent authority system defends against; the adversary named
  in CD-0044; the tamper-evidence deferral in CD-0008; which authority mechanisms
  follow from the corrected boundary; issue #450
- **Approval:** Operator asked whether the grant system is warranted before any
  extension of it, and accepted this record as the first answer
- **Related:** CD-0005 D5, CD-0008 (storage tamper-evidence deferral), CD-0044,
  CD-0037 D5, CD-0017 D4, CD-0003, CD-0042
- **Preserves:** Git-derived scope resolution; the `cross_scope` gate; manifest
  digest pinning; the operator approval checkpoint; `principal_ref` as checked
  identity; every refusal the authority system currently returns
- **Supersedes:** The process-level adversary asserted in CD-0044's context and
  its third rejected alternative

## Context

The authority system authenticates agent calls with Ed25519 assertions, nonce
replay protection, key rotation, grant expiry, and use counts. CD-0044 states
what it defends against:

> Concord's operating envelope is one operator and many local agents; an agent
> with process execution can invoke the installed binary directly. It could
> therefore forge a dispatch, fabricate a completion, or terminate another
> attempt's evidence without holding any grant.

Its third rejected alternative repeats the claim, against the prior design:

> **Trusting the CLI invocation as host proof.** This was the prior model. It
> holds only if no untrusted process can execute the binary, which is false in
> an envelope defined by many local agents.

That sentence is true, and it also describes the mechanism CD-0044 accepted. A
process that can execute the binary can write the database the binary reads.

Three facts establish this, and none of them is a defect to be repaired:

1. **The authority tables carry no integrity guard.** `internal/store/schema.go`
   declares 204 triggers. They protect projection tables, archived work, the
   workflow plane, and `domain_events` against UPDATE and DELETE. Not one
   protects `agent_clients`, `agent_client_keys`, `agent_grants`,
   `agent_approvals`, `agent_approval_challenges`, `agent_nonce_replay`, or
   `agent_installation_keys`. The tables the authority system trusts are the
   only ones in the schema with nothing standing over them.
2. **Nothing separates an agent from the file.** The database is `0600` under a
   `0700` directory (`internal/store/store.go`). The adapter reaches the core
   through `Bun.spawn` with an inherited UID, and resolves the binary from
   `CONCORD_BIN`, an environment variable inside the agent's own process tree.
   There is no container, namespace, socket, daemon, or network hop anywhere in
   the repository.
3. **Signing does not survive a writable verifier.** Signatures gate grant
   *issuance*. A row inserted directly into `agent_grants` — with a chosen
   `grant_hash`, chosen `capabilities_json`, and a `manifest_digest` copied from
   any existing row — is read by `validateGrantRecord` and passes. Ed25519
   verification never executes on the invoke path.

CD-0008 already reached the compatible conclusion for the storage layer, holding
that signed attestations "are an additive future strengthening only after a
named tamper/adversary need. They are not a v1 requirement." No record
reconciled that deferral with CD-0044's named adversary. This one does.

The mechanism is nonetheless valuable, and the value is real rather than
residual. It is simply a different boundary from the one the law claims.

## Decision

### D1. The boundary is the model's output stream

The authority system defends against authority asserted by an agent's *tool
arguments*. It does not defend against an agent's *process*, and no design that
trusts a same-UID-writable database can.

`docs/agent-call-context-contract.md` §4.1 already states this property
precisely, without naming an adversary:

> Tool input cannot name or impersonate a principal, widen scope, or add
> capabilities. Model name, prompt text, or an agent-asserted role is never a
> grant.

That is the property the mechanism actually delivers. The model never receives
the grant token or the client private key, so it cannot widen its own scope by
emitting different JSON, however it is prompted, confused, or steered. This
holds against prompt injection, tool-argument manipulation, and model error —
the failure modes that occur in an envelope of many local agents. It does not
hold against a shell, and it was never able to.

### D2. CD-0044's process-level adversary is withdrawn

The context paragraph and the third rejected alternative in CD-0044 assert a
process-level threat model. Both are withdrawn. CD-0044's decisions D1 through
D6 stand unchanged: they are correctly built, and every one of them constrains
what a lane's model output can cause. D6 in particular — the proof never reaches
the worker, so a lane run cannot capture the assertion that authorizes its own
evidence — remains true against the model and was never true against a process
holding a shell.

Withdrawing a threat model is not withdrawing a mechanism. The verification list
in CD-0044 is unchanged and still binding.

### D3. Absent tamper-evidence on the authority tables is accepted, not deferred

The seven `agent_*` tables carry no trigger, hash chain, or attestation, and
none will be added on the strength of a tamper argument. Under D1 there is no
adversary such a mechanism would stop: a writer able to forge a row is able to
drop a trigger, and `checkManifest` compares migration SQL text against compiled
constants, so it would not observe the difference.

This closes the question CD-0008 left open rather than deferring it again. A
future record may reopen it, but only alongside D5.

### D4. Mechanisms follow from the boundary they serve

Under D1 the authority system divides in two, and the division governs future
work on it.

**Load-bearing.** Git-derived scope resolution at issuance, and the refusals it
produces; the `cross_scope` capability gate; manifest-digest pinning; typed
`unauthorized` refusals; the operator approval checkpoint with core-derived
consequence summaries (CD-0037 D5); and `principal_ref`, on which idempotency
partitioning, worktree claim ownership, workflow actor distinctness (CD-0017
D4), and durable operation attribution all depend. These constrain model output
or carry checked identity. They stay.

**Not load-bearing.** Ed25519 assertion signing, nonce replay protection, client
key rotation, grant expiry, and grant use counts. Each bounds a caller who holds
a token but cannot write the database. D1 establishes that no such caller
exists. They are retained today because removing them is a larger change than
this record authorizes, and because `principal_ref` and the client registry are
threaded through them.

A successor decision may replace the signed-assertion issuance path with a
core-issued opaque session handle, preserving every load-bearing property above.
This record does not adopt that design; it establishes the argument that would
justify one.

### D5. A process boundary requires an OS boundary, which the architecture forecloses

Making CD-0044's original adversary real requires the agent to run as a
different UID from the database owner, with the core reachable only through a
UID-checked socket. That contradicts CD-0003's short-lived CLI with no daemon
and CD-0042's single-path rule.

Concord does not adopt it. Any future record proposing a process-level authority
boundary must supersede CD-0003 and CD-0042 first, and must state which of them
it is willing to give up. A mechanism that leaves the database same-UID writable
does not satisfy this decision however much cryptography it adds.

### D6. Per-grant revocation does not follow from this boundary

Issue #450 surfaced `Service.RevokeGrant`, `Service.RevokeApproval`, and
`Service.RevokeApprovalChallenge` as unreachable from `cmd/concord`. They are
not superseded code; nothing else performs their function. They are unbuilt
capability, and under D4 they belong to the half that is not load-bearing.

Per-grant revocation bounds the window in which a leaked token is usable. Under
D1 an agent that could leak a token could equally insert a replacement, so
narrowing that window changes no outcome. `client-revoke` and
`client-key-rotate` both already revoke every grant a client holds, and both
remain reachable.

Concord does not build a `grant-revoke` verb, nor the grant enumeration,
grant-level audit attribution, and revocation read-back that would be required
to make one usable. The three methods are removal candidates under the successor
decision D4 anticipates, not connection candidates.

## Consequences

- CD-0044 keeps every decision and every verification item; only its stated
  adversary changes. No code changes as a result of this record.
- `docs/reachability-exceptions.v1.json` continues to declare the three
  revocation methods, and the entry that holds them is renamed to state that
  they are unbuilt rather than superseded. Its owning issue changes from a
  build-the-verb issue to the successor-design issue D4 anticipates.
- Issue #450 is answered rather than implemented. Its build-the-verbs reading is
  closed by D6.
- Any future proposal to strengthen the authority system against a process-level
  adversary is answerable by D5 without re-litigating the evidence.
- The security posture is unchanged in fact and corrected in description. An
  operator reading CD-0044 alone would have believed the system defends against
  a hostile local process. It does not, and nothing it does today defended
  against one yesterday.

## Rejected alternatives

**Adding triggers or hash chains to the `agent_*` tables.** Rejected by D3. A
writer that can forge a grant row can drop a trigger in the same session, and
no startup check inspects trigger presence. It would raise the cost of the
forgery by one SQL statement and the cost of the codebase by a schema migration.

**Leaving CD-0044's context unamended and treating the gap as an implementation
shortfall.** Rejected because it is not a shortfall. No implementation reachable
from the accepted architecture closes it, so recording it as outstanding work
would create a permanently unsatisfiable obligation and misdescribe the system
to its next reader.

**Deleting the non-load-bearing mechanisms in this record.** Rejected as scope.
The argument must be accepted before roughly 1,300 lines and three tables are
removed on the strength of it, and `principal_ref` threading needs its own
design pass. D4 names the successor; it does not pre-empt it.

**Building single-grant revocation anyway, for defense in depth.** Rejected
under P40: defense in depth requires an independently stated failure mode that
already has a primary control. Here the primary control does not exist, and the
depth would be measured against an adversary D1 establishes cannot be stopped.

## Verification

- Every refusal in the authority system is unchanged; this record adds no
  behavior and removes none.
- `scripts/check-reachability.py` continues to pass with the three revocation
  methods declared, under an entry naming them unbuilt.
- The claim in D1 is checkable: `internal/store/schema.go` declares no trigger
  whose target is an `agent_*` table.
- The claim in D5 is checkable: no socket, daemon, or UID check exists in the
  repository, and `adapter/opencode/concord.ts` spawns the core as a child
  process with an inherited UID.
