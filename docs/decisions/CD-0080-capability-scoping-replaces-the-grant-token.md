# CD-0080: Capability scoping replaces the grant token

- **Status:** Accepted
- **Date:** 2026-08-28
- **Scope:** The successor design CD-0071 D4 anticipated; the grant token and its
  record; host approval assertion signing; the anchor for approval challenges;
  the three unbuilt revocation methods; issue #465
- **Approval:** Operator selected capability scoping without tokens on
  2026-08-28, after review of the two candidates recorded in issue #465. The
  pull request is the public record.
- **Related:** CD-0071 (D1, D4, D6), CD-0044, CD-0037 D5, CD-0017 D4, CD-0067
  D6, CD-0008, CD-0003, CD-0042, issues #465, #450
- **Preserves:** Git-derived scope resolution and its refusals, the
  `cross_scope` gate, manifest-digest pinning, typed `unauthorized` refusals,
  the operator approval checkpoint with core-derived consequence summaries,
  `principal_ref` as checked identity, worker evidence signing under CD-0067 D6
- **Supersedes:** The core-issued opaque session handle named in CD-0071 D4;
  the phrase "at issuance" in CD-0071 D4's load-bearing list

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

The `agent_grants` table is dropped. Grant expiry, `max_uses`, `used_count`, and
the consume path are removed with it, because each bounds a caller who holds a
token that no longer exists. The `grant` CLI verb is removed.

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

`agent_approval_challenges.grant_ref` is a non-null foreign key to
`agent_grants(grant_ref)` with restricted delete
(`internal/store/schema.go:710`). D2 removes that parent, so the challenge needs
a new anchor.

A challenge binds instead to `client_ref`, `session_ref`, `agent_ref`, and
`worktree`, the identity tuple the envelope carries on every call. This is the
same tuple the grant row stored, read from its source rather than from a copy.
The uniqueness and consumption semantics of a challenge do not change.

### D6. Blocked-session Product scope moves to the session record

`internal/store/blocked_sessions.go:87` joins `agent_grants` to reach a blocked
session, and line 121 reads `product_scope_json` from the most recent grant for
a session. The grant table is serving here as a session-to-Product-scope memo
for an operator read, which is not authority at all.

Resolved Product scope is recorded on the session identity record, where it is
session state. The launcher read is unchanged in output.

This is the one place where D2 is not purely subtractive, and it is named here
so that it is not discovered during implementation.

### D7. The three revocation methods are removed

CD-0071 D6 held `Service.RevokeGrant`, `Service.RevokeApproval`, and
`Service.RevokeApprovalChallenge` to be unbuilt capability and removal
candidates under this successor. All three are removed.

`RevokeGrant` has no subject once D2 lands. The argument in CD-0071 D6 extends
to the other two without modification: an approval is single-use, challenge
bound, and expiring, and under CD-0071 D1 a caller able to exploit one is able
to insert a replacement. `client-revoke` and `client-key-rotate` remain the
operator's revocation surface.

The `unbuilt-revocation-surface` entry in
`docs/reachability-exceptions.v1.json` is deleted rather than amended, because
the methods it declares no longer exist.

### D8. CD-0071 D4's "at issuance" wording is amended

CD-0071 D4 lists "Git-derived scope resolution at issuance" as load-bearing.
There is no issuance under D1. The load-bearing property is git-derived scope
resolution and the refusals it produces, at whichever point the system
authorizes a call. The phrase "at issuance" is struck from that list.

No other clause of CD-0071 changes. D1, D3, D5, and D6 stand as written, and D5
in particular continues to foreclose a process-level authority boundary.

## Consequences

- One table is dropped. `agent_clients`, `agent_client_keys`,
  `agent_nonce_replay`, `agent_approvals`, `agent_approval_challenges`, and
  `agent_installation_keys` all survive, the last for cursor signing, which is
  unrelated to agent authority.
- Two of the three cross-language assertion vectors are retired. The worker
  evidence vector remains, so the canonical encoder discipline and its
  Go-to-TypeScript agreement test stay in force.
- Every test in `internal/agent` that bootstraps a grant is rewritten. The
  client and key fixtures survive, so the change is to the authorization setup
  and not to the fixtures beneath it.
- Scope becomes fresh per call. A working tree that moves between calls is
  authorized against its current state rather than against a snapshot up to the
  former grant lifetime old.
- The system retains Ed25519 signing for worker evidence only. An operator
  reading the schema will find a key registry and a nonce table with one
  consumer each, which D4 explains.
- CD-0071's estimate of roughly 1,300 lines and three tables does not hold.
  Retaining worker evidence keeps two of those tables and their machinery.

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

- The claim in the Context is checkable: `internal/agent/authority.go:516`
  compares seven envelope fields against the stored grant row, and
  `authority.go:294` resolves scope from `directory` and `worktree` alone.
- Every refusal named in D1 keeps a test. The trunk firewall refusal moves from
  `TestIssueGrantRefusesMutationOnMainWorktree` to an invocation-time
  equivalent, and the `cross_scope` and manifest-digest refusals keep their
  current assertions against the new authorization site.
- D3 is proved by an approval test that tampers with asserted scope, with the
  challenge digest, and with the consequence, and observes a refusal in each
  case with no signature present.
- D5 is proved by a challenge test that binds, consumes, and refuses replay
  against the identity tuple.
- D6 is proved by a blocked-session read that returns unchanged output with no
  `agent_grants` table in the schema.
- D7 is proved by `scripts/check-reachability.py` passing with no
  `unbuilt-revocation-surface` entry present.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  `python3 scripts/check-law-coverage.py`, and
  `python3 scripts/check-cd-allocation.py` pass.
