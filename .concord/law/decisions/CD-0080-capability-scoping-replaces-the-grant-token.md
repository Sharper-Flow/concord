# CD-0080: Capability scoping replaces the grant token

- **Status:** Accepted
- **Date:** 2026-08-28
- **Scope:** Per-call capability scoping; issue #465
- **Approval:** Operator selected capability scoping without tokens on
  2026-08-28, after review of the two candidates recorded in issue #465. The
  pull request is the public record.
- **Related:** CD-0071 (D1, D4, D6), CD-0008, CD-0003,
  CD-0042, CD-0242, issues #465, #450
- **Preserves:** Git-derived scope resolution and its refusals, the
  `cross_scope` gate, manifest-digest pinning, typed `unauthorized` refusals,
  `principal_ref` as checked identity

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
identity for idempotency partitioning, workflow
authority and durable operation attribution.

### D7. The three revocation methods are removed

The operator's client-policy revocation surface is `client-revoke`.

### D8. CD-0071 D4's "at issuance" wording is amended

Git-derived scope resolution and the refusals it produces apply at
authorization. CD-0071 D5 forecloses a process-level authority boundary.

## Consequences

- `agent_clients` and
  `agent_installation_keys` serve current consumers, the last for cursor
  signing, which is unrelated to agent authority.
- Scope is fresh per call. A working tree that moves between calls is
  authorized against its current state.

## Rejected alternatives

**The core-issued opaque session handle named in CD-0071 D4.** Rejected because
the invoke path already re-supplies every field the handle would name, so the
handle preserves a memo rather than removing one. It also preserves the scope
snapshot staleness that per-call resolution eliminates. Under P35 elimination
outranks substitution, and this candidate substitutes.

**Keeping the grant record as a read-only attribution row.** Rejected because
durable operations attribute to `principal_ref`.

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
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`, and
  `python3 scripts/check-knowledge-index.py` pass.
