# Installing Concord

Concord releases support Linux amd64 only. The published release contains the
binary, a reproducible bundle with the OpenCode adapter, a SHA-256 checksum
file, a software bill of materials (SBOM), and the installer itself.

## Prerequisites

The installer performs all checks before changing the operator environment. It
refuses to continue when any of these are missing:

- Linux amd64;
- `git`, `opencode`, `secret-tool`, `gnome-keyring-daemon`, `busctl`,
  `dbus-run-session`, and `systemctl` commands;
- a user-session D-Bus service named `org.freedesktop.secrets`; and
- the installer's binary directory on `PATH`.

The adapter reads its client signing key from Secret Service storage. If an
unlocked compatible collection exists, the installer uses it without changing
the provider. If no login collection exists, the installer creates an
unencrypted `gnome-keyring-daemon` collection and a user service that unlocks it after
the daemon starts. The keyring directory grants access only to its owner.

Concord does not store a private key outside Secret Service or invent a general
file-based fallback. Private keys do not enter process arguments, logs, stdout,
temporary files, or a workspace. If setup cannot prove an unlocked collection,
installation refuses and worker evidence signing fails closed.

Credential state and its unlock service remain after uninstall. Client keys can
outlive one installed release, and removing the unlock service would strand
those credentials. Remove or revoke client keys before removing that host
state.

The installer also refuses to overwrite an existing user-authored adapter,
launcher, version directory, or incompatible OpenCode configuration. When it
cannot safely add the stable skills path, it prints the exact `skills.paths`
entry to add manually.

## Install or upgrade

Download the installer from the intended public release, then run it with the
published version. The installer downloads the matching bundle and checksum,
verifies the bundle and its contained binary before installing anything, and
then installs files under a durable, journaled recovery protocol:

```sh
python3 concord-installer.py install --version v0.1.0
```

For local artifact verification, use a directory containing the published
`concord-v0.1.0.tar.gz` and `concord-v0.1.0.sha256` files:

```sh
python3 concord-installer.py install \
  --version v0.1.0 \
  --artifact-dir ./published-assets
```

Versioned assets live under:

```text
${XDG_DATA_HOME:-$HOME/.local/share}/concord/v0.1.0/
├── adapter/opencode/
├── bin/concord
└── skills/
```

The managed `concord` launcher is placed under `$HOME/.local/bin/`, and the
adapter files are placed under `~/.config/opencode/tools/`. Existing files at
those paths are never overwritten unless they are still the files recorded by
the Concord installer manifest. Re-running the same version makes no changes.
Installing a newer version replaces the prior Concord-managed version after
the new artifact is verified. It does not remove user-authored files.

The adapter loads through an OpenCode plugin entry module,
`~/.config/opencode/tools/concord-plugin.ts`. The installer registers its path
in the host `plugin` array so the typed tools load on the next OpenCode start.
OpenCode invokes every function-valued export of a plugin entry module as a
factory, so the entry module exports exactly one default factory and re-exports
the tool definitions; the tool modules are never entry points themselves. When
the installer cannot safely edit the `plugin` array (for example a non-array
value), it refuses and prints the exact entry to add manually.

### Crash recovery

Before the first managed change, the installer writes a bounded transaction
journal and durable backups under
`${XDG_DATA_HOME:-$HOME/.local/share}/concord/.concord-transactions/`.
The journal contains hashes, fixed target paths, phases, and backup locators,
not credentials or configuration contents; temporary backups are private and
removed after recovery.
All release files are staged and hash-checked on the same filesystem before
activation. The journal advances after version, adapter, launcher, config, and
manifest phases, and every later installer invocation (including `status`)
recovers an incomplete transaction before reading the normal manifest.
The invariant is strict: a phase is advanced only after every file and both
directory entries referenced by that phase have been fsync-durable.
Tree copies fsync every file and directory plus the destination parent;
cross-directory renames fsync both source and destination parents, while
same-directory replacements fsync their containing directory.

The recovery policy is deterministic: before `manifest_committed`, restore the
old coherent installation; after `manifest_committed`, verify the new coherent
installation and finish cleanup. If a managed file changed after the journal
was written, recovery refuses with a conflict instead of overwriting it. A
crash can leave the journal and backups temporarily; a subsequent invocation
removes them after successful recovery. Cross-file replacement is therefore
recoverable rather than one filesystem-wide atomic rename.

To explicitly trigger recovery and inspect the resulting state without
installing or uninstalling, run:

```sh
python3 concord-installer.py status
```

Restart OpenCode after installation or upgrade. OpenCode reads the registered
stable `skills.paths` entry and the plugin entry module at startup. The
installer manages only those two registrations; it does not modify unrelated
configuration keys.

## Prepare, migrate, activate

Every install consults the candidate release's own core before activating it.
The core inspects the store by reading only and reports whether activation may
proceed. A compatible release activates immediately, and an install keeps every
release a live session holds. The installer also asks the staged core, through
the side-effect-free `--version --json` descriptor, which maintenance-fence
protocol it speaks, and marks the staged tree with only the number the core
reported. A core that cannot run or does not identify as the staged release
refuses staging. A core whose descriptor is absent or names an unsupported
protocol installs unmarked. An incompatible migration names every unmarked tree and
proceeds only when the operator confirms that no session runs on one:
`echo '{"confirm_sessions_stopped":true}' | concord upgrade`.

When a pending breaking store migration blocks activation, or the store's
readiness cannot be established by reading, the installer prepares the candidate
instead of activating it. The candidate tree lands under the data root, and the
active launcher, the `current` root, the tools, and the agents keep the release
the running sessions hold. A prepared record,
`${XDG_DATA_HOME:-$HOME/.local/share}/concord/prepared-release.json`, carries
the blockers and the exact operator commands. The install prints them and
`status` repeats them.

The operator completes the prepared release with the two recorded commands:

```sh
env -u CONCORD_DB_PATH XDG_DATA_HOME=/data /data/concord/v0.2.0/bin/concord upgrade
python3 concord-installer.py activate --version v0.2.0
```

Only one maintenance command runs at a time: the migration command and every
installer command share one lock. A second command refuses with `another
maintenance command is in progress`; re-run it after the first ends.
The migration command pins the store the plan read, so a shell that sets
another `CONCORD_DB_PATH` or data home cannot redirect it. While the boundary
is open, an install whose own activation is blocked or unknown refuses and
keeps the prepared record. Run the recorded activation command first.

The migration command opens the maintenance boundary: new session admission is
excluded before its final lease check, and the exclusion is held through the
migration and the activation. A session that starts inside the boundary fails
closed and names the recorded activation command. The activation verifies the
migration completed by reading, holds the same exclusion through the swap and
the release cleanup, and then reopens admission and discharges the record. The
boundary the migration opens is attributed to the prepared candidate's release
root, and that attribution is the ownership proof: the discharge closes only
the boundary its recorded identity owns. A fence that belongs to another
operation, one that carries no release attribution, or one no record owns,
stays open for the operator — an adoption or closure is never justified by a
prepared record merely existing. An orphaned boundary is closed through the
offline bootstrap below, never by the installer guessing at ownership.

Release cleanup follows the same admission-exclusion rule, not an observation
count: no re-read closes the window behind it, so the cleanup first proves that
every installed release tree honors the shared exclusion — the same
fence-protocol marker the staging probe records. While any runnable tree
without a recognized marker stays installed, cleanup retains every candidate
release and names the tree; remove or reinstall that tree through the offline
bootstrap, and the next install retries the removal.

Recovery follows the boundary. Before the migration commits, a failed or
interrupted command leaves the active release usable and the store unchanged,
and the operator runs the migration again. After the migration commits, the
older release cannot open the store, so a crashed activation recovers forward
to the prepared candidate; repair and uninstall refuse until it completes.

## Repair

An incomplete deployment of the installed release — missing runtime modules, a
stale manifest that records fewer files than the release ships, or a missing
registration — is repaired from an ordinary shell:

```sh
concord repair
```

The command reads the installed manifest, resolves the installer that the
installed release published, and verifies that installer against the release's
published checksums before running it. It then snapshots the work database
under `${XDG_DATA_HOME:-$HOME/.local/share}/concord/backups/` and hands the
mutation to the installer's `repair` subcommand. Repair keeps the installed
release: it never upgrades or downgrades. Pass `{"installer_version": "vX.Y.Z"}`
on JSON stdin to run another release's verified installer instead, for example
when the release-matched installer itself is defective. The report names the
selected release, the installer's release, the backup path, the repaired
files, and the verification result.

Offline repair uses locally published assets:

```sh
echo '{"artifact_dir": "/path/to/assets"}' | concord repair
```

The directory must hold the release archive, its checksum file, and
`concord-installer.py`.

If the `concord` executable itself is missing, the supported entry is the
published installer run directly:

```sh
python3 concord-installer.py repair
```

Repair refuses before changing anything when an asset fails its checksum, when
the release archive is incomplete, or when a managed file was modified so that
it matches neither the installed manifest nor the release. The refusal names
the offending artifact or path. The work database, worktrees, credentials, and
unrelated configuration are outside the managed paths and are never touched.

## Fold-guard recovery

When the core refuses to open the authority database because a committed
`fold_guard` row stranded without its owning fold scope, recover offline from
an ordinary shell:

```sh
concord recover-fold-guard
```

The verb runs without the network. It clears the stranded guard row and
rebuilds every projection from the append-only event log in one transaction,
so projection rows the log does not restore are discarded and the log itself
is preserved. A failed recovery rolls back, which restores the stranded row
and keeps the database refused; pass `{"path": "..."}` on JSON stdin to
recover a database outside the default location.

## Uninstall

Remove only files recorded as Concord-managed:

```sh
python3 concord-installer.py uninstall
```

If a managed file was edited, uninstall refuses to remove it rather than
deleting operator changes. User configuration and unrelated files remain.

## First-use requirements

The authority database is outside a project repository at
`${XDG_DATA_HOME:-$HOME/.local/share}/concord/concord.db`. `CONCORD_DB_PATH` may
select another location, but Concord refuses an override inside a Git
repository or worktree.

Host resolution is deliberately strict. The `directory` or
`worktree` must be a real Git repository, and that repository must have a
registered Concord Project locator (`canonical_path` or matching `git_remote`).
The installer does not invent a Product, Project, locator, or key, and
it does not modify a repository. Complete that operator bootstrap separately
before using adapter tools.

## Version and migration policy

One Concord semantic version covers the core, adapter, contracts, workflows,
and shipped skills. Conventional commits determine the next release. A release
is immutable: install or upgrade to a published version rather than changing
files inside its version directory. Concord does not perform implicit database
migrations during installation. A future schema change must ship an explicit,
versioned migration plan and preserve the database authority and recovery
contract.
