# CD-0085: Client signing keys live behind a single OS-custody adapter

- **Status:** Accepted
- **Date:** 2026-08-30
- **Scope:** Custody of the worker-evidence client signing key; the installer
  preflight; the adapter read path in `adapter/opencode/credentials.ts`. No
  change to the signing law, the key registry, or any other surface
- **Approval:** Operator questioned the platform dependency on 2026-08-30,
  accepted the boundary analysis, and directed this record
- **Related:** CD-0044 (caller authentication), CD-0067 (D6 worker evidence
  signing), CD-0080 (retains the client key registry and nonce table),
  CD-0007 (Linux amd64 platform boundary — this record bounds the
  userspace-service dimension that boundary never assessed)
- **Preserves:** the fail-closed installer preflight and the worker-evidence
  signing law in full
- **Supersedes:** nothing. The custody choice already existed in code; this
  record gives it authority, rationale, and a portability seam

## Context

The installer preflight refuses installation when `secret-tool` is missing or
no Secret Service provider answers on the user D-Bus session. The refusal
reason names worker-evidence signing: the client signing key is held by the
OS credential store, and Concord fails closed when it cannot read the key.

No decision record held that choice. An operator review on 2026-08-30
flagged the gap: this is a dependency on a desktop userspace service, a
dimension CD-0007's Linux-amd64 boundary never assessed, and it existed only
as installer behavior and one adapter call.

## Decision

### D1. One custody adapter, one read path

The private signing key is read at exactly one point: the custody adapter in
`adapter/opencode/credentials.ts`, which shells `secret-tool lookup service
concord account <client_ref>`. The Go core holds only public keys in the
client key registry and validates assertions against them. Nothing else
reads, copies, or persists the private key, and no file under any
Concord-managed path holds it.

### D2. The dependency is the freedesktop standard, not a provider

The depended-on surface is `org.freedesktop.secrets` on the user session
bus, reached through the `secret-tool` CLI. gnome-keyring is the reference
provider on this host; KWallet, KeePassXC's secret service, and standalone
daemons implement the same standard. Swapping providers changes no Concord
code.

### D3. The adapter is the porting seam

A platform port swaps the custody adapter only — Keychain, Credential
Manager, or a headless provider — with no change to the signing law, the key
registry, or the evidence contract. The rest of Concord never learns which
provider answered.

### D4. Fail-closed stands, and plaintext is not a fallback

Without a readable key, worker-evidence signing refuses and the installer
preflight refuses installation. No plaintext key file exists as a fallback.
Accepting one requires superseding this record with an explicit
threat-model statement.

## Accepted residual

With the session keyring unlocked, any process in the session can read the
key. OS custody protects against file-level exposure — backups, state-dir
copies, configuration sync — and out-of-session forgery. It does not protect
against a fully compromised session; no userspace scheme does on a
single-user machine. That residual is accepted and recorded rather than
hidden.

## Invariants

1. The private signing key is read through exactly one custody adapter.
2. The installer preflight fails closed when no Secret Service provider is
   reachable.
3. Swapping the provider changes no Concord code.
4. No plaintext copy of the private key exists under any Concord-managed
   path.

## Consequences

- The userspace-service dependency is recorded law with its portability
  seam, not implicit installer behavior.
- Headless hosts must run a Secret Service provider; that is an
  installation requirement, not an architectural change.
- The preflight message and the adapter comment can cite this record.

## Verification

- `internal/agent.TestWorkerEvidenceBindingMismatchOnPacketDigest` and the
  worker-evidence assertion tests still pass unchanged: the signing law is
  untouched.
- `scripts/test-installer.py` exercises the installer surface whose
  preflight enforces custody availability.
- A repository search finds `secret-tool` only in the custody adapter, the
  installer, and their tests.
