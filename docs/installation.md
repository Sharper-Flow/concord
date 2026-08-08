# Installing Concord

Concord releases support Linux amd64 only. The published release contains the
binary, a reproducible bundle with the OpenCode adapter, a SHA-256 checksum
file, an SBOM, and the installer itself.

## Prerequisites

The installer performs all checks before changing the operator environment. It
refuses to continue when any of these are missing:

- Linux amd64;
- `git`, `opencode`, `secret-tool`, and `gnome-keyring-daemon` commands;
- a user-session D-Bus service named `org.freedesktop.secrets`; and
- the installer's binary directory on `PATH`.

The adapter's grant bootstrap requires Secret Service storage. Concord does not
install a keyring, store a private key in a file, or invent a file-based
fallback. If `secret-tool`, `gnome-keyring-daemon`, or
`org.freedesktop.secrets` is unavailable, installation refuses and says that
grant bootstrap will fail closed.

The installer also refuses to overwrite an existing user-authored adapter,
launcher, version directory, or incompatible OpenCode configuration. When it
cannot safely add the versioned skills path, it prints the exact `skills.paths`
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
versioned `skills.paths` entry at startup; the installer does not inject a
plugin or modify unrelated configuration keys.

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

Grant host resolution is deliberately strict. The signed `directory` or
`worktree` must be a real Git repository, and that repository must have a
registered Concord Project locator (`canonical_path` or matching `git_remote`).
The installer does not invent a Product, Project, locator, key, or grant, and
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
