# CD-0087: Provider-specific provisioning may initialize OS custody

- **Status:** Accepted
- **Date:** 2026-08-30
- **Scope:** First-use credential provisioning on headless Linux hosts; the
  installer; no change to credential reads, signing law, or the client key
  registry
- **Approval:** The operator required fully noninteractive setup, accepted the
  user-account boundary without encryption, and selected provider-specific
  provisioning when CD-0085 exposed the conflict
- **Related:** CD-0085, issue #600
- **Supersedes:** CD-0085 D2-D3 only where they require provider-independent
  provisioning. Credential reads remain provider-independent
- **Preserves:** CD-0085 D1 and D4, the single custody read path, fail-closed
  signing, and the ban on a general plaintext-key fallback

## Context

The installer accepted an active Secret Service with no login collection. The
first credential write then failed, and an agent could not finish setup without
an operator password.

The standard Secret Service surface cannot satisfy the approved headless
contract by itself. Its `CreateCollection` call accepts properties and an alias,
then returns a collection or prompt. It has no input for a noninteractive empty
password. The runtime evidence in issue #600 confirmed that direct unlock calls
also did not initialize the D-Bus-activated provider.

CD-0085 correctly keeps credential reads behind the standard Secret Service
adapter. It overreached when it extended provider independence to setup. A
provider must create and unlock its own persistent storage before the standard
read path can use it.

## Decision

### D1. Custody reads remain provider-independent

`adapter/opencode/credentials.ts` remains the only private-key read path. It
continues to call `secret-tool` through the Secret Service standard. The Go
core, signing law, client key registry, and evidence contract do not learn
which provider supplied the key.

### D2. Provisioning may use a provider-specific adapter

First-use setup may call provider and host-process controls that the Secret
Service standard does not define. That code is provisioning, not custody read
logic. Replacing the provider can change the provisioning adapter without
changing D1.

### D3. The Linux reference provisioner uses the installed provider

When no login collection exists, the installer can initialize
`gnome-keyring-daemon` with an empty password in an isolated D-Bus session. It
then transfers service ownership to the user systemd service and installs a
user unit that unlocks the collection after each daemon start.

The installer does not replace an unlocked compatible provider. It uses the
provider that already satisfies D1.

### D4. Setup must not discard live credential state

Before the installer stops a D-Bus-activated provider, it proves that every
reported collection contains zero items. It also proves that the process
belongs to the current user and is `gnome-keyring-daemon`. Any mismatch refuses
setup without stopping the provider.

### D5. The user-account boundary is sufficient for this collection

The provisioned collection does not require encryption at rest. Its directory
and credential files grant no access to group or other users. Private keys
still enter Secret Service only through standard input. They never enter
process arguments, logs, stdout, temporary files, or a workspace.

### D6. Availability is part of setup completion

Setup succeeds only after it proves that the login alias exists and is
unlocked. The unlock unit runs after each provider start, so a daemon restart
does not require operator input.

## Consequences

Headless first use becomes agent-completable after package installation. The
read path stays portable, while provider setup becomes an explicit adapter
surface rather than an implicit assumption.

The installer now requires `busctl`, `dbus-run-session`, and `systemctl` in
addition to the existing provider commands. A future provider can supply a
different provisioner while preserving D1, D4, D5, and D6.

Credential state and the unlock unit remain after Concord uninstall. Client
keys can outlive one installed release, and removing the unit would strand
those credentials.

## Rejected alternatives

**Require an operator password.** Rejected because the approved contract is
fully noninteractive after package installation.

**Use only the standard Secret Service calls.** Rejected because the standard
creation call returns a provider prompt and has no noninteractive empty-password
input.

**Store the signing key in a Concord file.** Rejected because CD-0085 D1 and D4
require the single Secret Service custody read path and forbid a general
plaintext-key fallback.

## Verification

`InstallerTests.test_headless_install_creates_noninteractive_persistent_credential_collection`
proves creation, permissions, bounded output, restart setup, and idempotence.
`InstallerTests.test_install_keeps_an_existing_compatible_secret_service`
proves that setup leaves an existing provider unchanged.
`InstallerTests.test_headless_install_refuses_to_discard_session_credentials`
proves the live-state refusal.

The host reproduction in issue #600 proves the same provider sequence against
the released package service and verifies an unlocked collection after a daemon
restart.
