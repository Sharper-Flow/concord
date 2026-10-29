# CD-0080: Capability scoping replaces the grant token

- **Status:** Accepted
- **Date:** 2026-08-28
- **Scope:** Per-call capability scoping; host approval bindings; the anchor
  for approval challenges; issue #465
- **Approval:** Operator selected capability scoping without tokens on
  2026-08-28, after review of the two candidates recorded in issue #465. The
  pull request is the public record.
- **Related:** CD-0071 (D1, D4, D6), CD-0044, CD-0037 D5, CD-0017 D4, CD-0067
  D6, CD-0008, CD-0003, CD-0042, issues #465, #450
- **Preserves:** Git-derived scope resolution and its refusals, the
  `cross_scope` gate, manifest-digest pinning, typed `unauthorized` refusals,
  the operator approval checkpoint with core-derived consequence summaries,
  `principal_ref` as checked identity, worker evidence signing under CD-0067 D6

## Context

CD-0071 D1 established that the authority system defends against authority
asserted in an agent's tool arguments, and not against an agent's process. D4
then divided the mechanisms in two and named the ones that do not follow from
that boundary: assertion signing, nonce replay protection, client key rotation,
grant expiry, and grant use counts. D4 anticipated a successor that replaced the
signed-assertion issuance path with a core-issued opaque session handle.

This record declines that handle. The reason is visible on the invoke path.

`internal/agent/authority.go:516` re-checks, on every invocation, that the
envelope's `client_ref`, `principal_ref`, `session_ref`, `agent_ref`,
`directory`, `worktree`, and `manifest_digest` all equal the stored grant row.
Git-derived scope resolution takes exactly `directory` and `worktree`
(`authority.go:294`). Both fields already ride every invoke envelope.

The grant record is therefore a memo of a computation the envelope re-supplies
on each call. An opaque handle preserves the memo and changes only the secret
that names it. It keeps a mint step, a stored row, and a lifetime, and it buys
no property that per-call resolution does not already deliver.

The memo also carries a defect. A grant pins a scope snapshot for the life of
the grant, so scope resolved at issuance can disagree with the working tree at
invocation. Per-call resolution cannot drift.

## Decision

### D1. Authorization is client policy intersected with resolved scope

The core authorizes each invocation from the envelope it already receives. It
reads the registered client by `client_ref` to obtain `principal_ref` and the
capability policy the operator registered. It resolves Product and Project scope
from `directory` and `worktree` through the existing resolver. Effective
authority is the registered policy intersected with the resolved scope,
intersected with the capabilities the call requests.

No token is presented, stored, or compared. Under CD-0071 D1 this concedes
nothing: a process able to forge a `client_ref` is able to write the client
registry the core reads.

The `cross_scope` gate, manifest-digest pinning, the trunk firewall refusal, and
every typed `unauthorized` refusal are evaluated at this point. They are
unchanged in effect and moved in site.

### D2. The grant token, its record, and its lifetime bounds are removed

`principal_ref` survives on `agent_clients` and continues to carry checked
identity for idempotency partitioning, worktree claim ownership, workflow actor
distinctness under CD-0017 D4, and durable operation attribution.

### D3. Host approval keeps its bindings and loses its signature

The operator approval checkpoint is load-bearing under CD-0071 D4 and CD-0037
D5. Its signature is not the mechanism that makes it so.

Three independent bindings already constrain an approval. The asserted scope and
versions must equal the values the core computed
(`internal/agent/authority.go:738`). The challenge must match on
`operation_digest`, `scope_json`, `version_json`, `consequence`, and
`host_assertion_digest` (`authority.go:755`). The challenge must be active,
unexpired, and within its use count. All three remain.

The Ed25519 verification and the nonce record on the host approval path are
removed. The operator still approves a core-derived consequence summary, and
tool input still cannot author or widen an approval.

### D4. Worker evidence signing is out of scope and unchanged

CD-0067 D6 binds worker evidence to the packet it came from, and the proof never
reaches the worker. That property is distinct from grant issuance and is not
reconsidered here.

`internal/agent/worker_evidence.go` continues to verify an Ed25519 signature
against the registered client key and to record a nonce. Three mechanisms
therefore survive this record that CD-0071 D4 listed as not load-bearing:
`agent_client_keys`, `agent_nonce_replay`, and client key rotation. They survive
because worker evidence uses them, not because grant issuance does. The
`client-register`, `client-key-rotate`, `client-revoke`, and
`client-policy-update` verbs remain reachable.

A later record may revisit worker evidence. This one does not.

### D5. Approval challenges bind to the session identity tuple

A challenge binds to `client_ref`, `session_ref`, `agent_ref`,
`worktree`, and `directory`, the identity tuple the envelope carries on every
call. Challenge uniqueness and consumption use this tuple.

### D6. Blocked-session Product scope moves to the session record

`internal/store/blocked_sessions.go:87` joins `agent_grants` to reach a blocked
session, and line 121 reads `product_scope_json` from the most recent grant for
a session. The grant table is serving here as a session-to-Product-scope memo
for an operator read, which is not authority at all.

Resolved Product scope is recorded beside the identity tuple D5 places on
`agent_approval_challenges`. The launcher read is unchanged in output.

This is the one place where D2 is not purely subtractive, and it is named here
so that it is not discovered during implementation.

Concord has no session table, no
session projection, and no session event type; `session_ref` is an opaque string
carried on grant and approval rows.

*Building the named record would have cost more than a table. Nothing creates a
session row today, because the `grant` verb was its creation moment and D2
removes that verb. The only remaining insertion point is lazily on first
authorization, which makes every authorized read a potential write. CD-0079
rejected an alternative for that reason, and the objection transposes without
modification.*

*Resolving Product scope when the launcher reads was also rejected.
`internal/store/blocked_sessions.go` would have to call the project resolver
that `cmd/concord` installs onto `agent.Service`, which inverts the dependency
direction between the store and the layer above it.*

*The challenge row carries the value instead. `blocked_sessions` then reads one
table with one query, where today it joins `agent_grants` and re-reads
`product_scope_json` once per session. The accepted cost is that Product scope
is captured when the challenge is created rather than when the launcher reads.
A challenge is short-lived and single-use, and the value only filters a display
list, so a stale value can misplace a row in a view and can never affect
authority.*

### D7. The three revocation methods are removed

The operator's revocation surface is `client-revoke` and `client-key-rotate`.
An approval is single-use, challenge-bound, and expiring. Under CD-0071 D1 a
caller able to exploit one is able to insert a replacement.

### D8. CD-0071 D4's "at issuance" wording is amended

Git-derived scope resolution and the refusals it produces apply at
authorization. CD-0071 D5 forecloses a process-level authority boundary.

## Consequences

- `agent_clients`, `agent_client_keys`,
  `agent_nonce_replay`, `agent_approvals`, `agent_approval_challenges`, and
  `agent_installation_keys` serve current consumers, the last for cursor
  signing, which is unrelated to agent authority.
- The worker evidence vector requires canonical encoder discipline and a
  Go-to-TypeScript agreement test.
- Scope is fresh per call. A working tree that moves between calls is
  authorized against its current state.
- The system retains Ed25519 signing for worker evidence only. An operator
  reading the schema will find a key registry and a nonce table with one
  consumer each, which D4 explains.

## Rejected alternatives

**The core-issued opaque session handle named in CD-0071 D4.** Rejected because
the invoke path already re-supplies every field the handle would name, so the
handle preserves a memo rather than removing one. It also preserves the scope
snapshot staleness that per-call resolution eliminates. Under P35 elimination
outranks substitution, and this candidate substitutes.

**Removing worker evidence signing in the same record.** Rejected as scope. The
packet-digest binding in CD-0067 D6 delivers a property grant issuance never
did, and replacing it needs its own argument. Bundling it would put two
unrelated authority questions behind one approval.

**Keeping the grant record as a read-only attribution row.** Rejected because
nothing outside the authority path reads it for attribution. Durable operations
attribute to `principal_ref`, and the one other consumer is the blocked-session
memo that D6 relocates to where it belongs.

**Amending `unbuilt-revocation-surface` instead of deleting it.** Rejected under
P41. An exception entry declaring three methods that no longer exist is a
tombstone, and version control is the record of what the file used to contain.

**Retaining grant expiry as defense in depth.** Rejected under P40, on the same
reasoning CD-0071 D6 applied to single-grant revocation. Defense in depth
requires an independently stated failure mode that already has a primary
control, and CD-0071 D1 establishes that no primary control exists against the
only caller expiry would bound.

## Verification

- Every refusal named in D1 has a test at authorization, including the trunk
  firewall, `cross_scope`, and manifest-digest refusals.
- D3 is proved by an approval test that tampers with asserted scope, with the
  challenge digest, and with the consequence, and observes a refusal in each
  case with no signature present.
- D5 is proved by a challenge test that binds, consumes, and refuses replay
  against the identity tuple.
- D6 is proved by a blocked-session read that returns unchanged output with no
  `agent_grants` table in the schema.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`,
  `python3 scripts/check-knowledge-index.py`, and
  `python3 scripts/check-law-coverage.py` pass.
