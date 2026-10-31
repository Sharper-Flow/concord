# Installing Concord

Concord releases support Linux amd64 only. The published release contains the
binary, a reproducible bundle with the OpenCode adapter, a SHA-256 checksum
file, a software bill of materials (SBOM), and the installer itself.

## Prerequisites

The installer performs all checks before changing the operator environment. It
refuses to continue when any of these are missing:

- Linux amd64;
- `git` and `opencode` commands; and
- the installer's binary directory on `PATH`.

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
├── agents/
├── bin/concord
├── instructions/
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
installer also registers project-scoped conduct when linking a project; it does
not modify unrelated configuration keys.

### Shared requirements and Product knowledge

The release ships generated primary profiles, native step helpers and read-only utilities in its agents
directory and the shared conduct corpus in its instructions directory. The
installer owns these assets and registers the corpus through an absolute
`current/instructions/*.md` pointer in each linked project's OpenCode
configuration. It does not copy Concord's source documentation into the adopting
Product.

| Requirement | Canonical owner and authority | Applicability and delivery |
|---|---|---|
| Planning through Linear MCP | [CD-0213](../law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md) | Reusable across Products; the generated primary states the operative planning rule. |
| Source-edit boundary | [CD-0220](../law/decisions/CD-0220-repository-authoring-follows-lane-capability-and-approved-scope.md) | Approved scope and law-change authority remain distinct from host tool permission. |
| Committed-content conduct | [Shared conduct](../instructions/change.md) under [CD-0063](../law/decisions/CD-0063-shipped-operator-conduct-rules.md) | Loaded from the installed corpus, not inferred from a Product-relative filename. |
| Concord repository engineering | [Concord's AGENTS.md](../../AGENTS.md) and the explicitly Concord-specific items 2 and 5 in [development authority](../law/specs/managed-development-authority.md#preserved-boundaries) | Repository instruction, not part of the shipped conduct corpus. Other boundaries keep their stated scope. |
| An adopting Product's law and facts | That Product's canonical knowledge home | Product-scoped knowledge reads and declared reading sources; not inherited from Concord's source checkout. |

Decision identifiers in a generated profile identify Concord's governing source;
they are not paths to similarly named files in the adopting Product. The profile
states its required behavior without requiring that source checkout. Product law
needed for an assignment retains its declared source identity and approval
scope. The coordinator supplies native Task assignments; Concord does not
package or attest subagent prompts.

Intentional user instruction entries continue to load alongside the installed
corpus. A local `.concord/instructions/change.md` is not the shipped rule merely
because its name matches; it loads only through a supported instruction entry.

### Customize agents without editing shipped files

Concord owns the generated agent files it records in the install manifest and
updates them from each verified release. Do not edit those files in
`~/.config/opencode/agents/`: a modified managed file blocks installation or
repair rather than losing your edits. Other agent files in that directory stay
host-owned. If a new release ships a name you already use for a host-authored
agent, installation refuses; it does not merge, adopt, or delete your file.

Keep machine-wide agent overrides in `~/.opencode/opencode.jsonc` (or an
existing `~/.opencode/opencode.json`), and extra instructions in a separate file
listed in your global `opencode.jsonc`:

```json
{
  "agent": {
    "concord-explore": {
      "model": "provider/model-id",
      "tools": { "read": false },
      "permission": {
        "read": "deny",
        "bash": {
          "git status *": "deny",
          "custom-command *": "allow"
        }
      }
    }
  }
}
```

```json
{
  "instructions": ["~/my-agent-instructions.md"]
}
```

OpenCode loads `~/.opencode/opencode.jsonc` after Concord's generated agents and
after project configuration, with no launcher setting. A same-key value there
replaces a release's model, tool key, or permission value, so an upgrade that
adds a competing default does not change your choice. A replaced permission
key keeps the shipped key's position, and OpenCode applies the last matching
rule, so a later shipped pattern that also matches still wins; check the result
as shown below. Move existing machine-specific `agent.<name>` keys from the
global JSON to this file when they must override shipped settings.

Keep only `agent` settings in that file. Do not put agent markdown in
`~/.opencode/agents/`: it loads last and overrides every field it defines,
including your JSON settings in the same directory. Concord never writes or
removes anything under `~/.opencode/` unless you link your home directory itself
as a project.

OpenCode loads the listed instructions for every agent, alongside its agent
prompt. Use an instruction such as “For concord-explore, …” when a rule applies
only to that role. Concord never writes the instruction file or changes those
user agent keys. It edits only its own plugin, skills, and project conduct registrations.

OpenCode merges global JSON before discovered markdown agents. Fields the
shipped markdown defines win over conflicting global JSON fields: its body
replaces `agent.<name>.prompt`, and its permission rules can override your
global rules. Do not use a global `prompt` value to append instructions or
assume a global permission addition overrides a shipped deny.

For project-specific settings, add an `agent` entry to `opencode.jsonc` (or the
existing `opencode.json`) inside the consuming project's `.opencode` directory.
That file loads after the global agent definition and before
`~/.opencode/opencode.jsonc`.
Machine settings therefore also win over project settings for the same key.

Do not duplicate the agent markdown to customize it. Linking, upgrading,
repairing, unlinking, and uninstalling preserve these user keys. Host permission
grants do not grant Concord workflow authority. Check the result with
`opencode debug agent concord-explore` and `opencode debug config`, then start
a new session to use changed host configuration. The examples follow
[OpenCode's native merge order](https://github.com/anomalyco/opencode/blob/v1.18.35/packages/opencode/src/config/config.ts#L412-L489)
and [directory ordering](https://github.com/anomalyco/opencode/blob/v1.18.35/packages/opencode/src/config/paths.ts#L23-L40),
with [instruction loading](https://github.com/anomalyco/opencode/blob/v1.18.35/packages/opencode/src/session/instruction.ts#L135-L168).

Installer regression tests use temporary home and configuration roots. To also check the
resolved agent with an existing OpenCode binary, run:

```sh
bin/oc-test targeted -- env CONCORD_INSTALLER_HOST_PROBE="$(command -v opencode)" \
  python3 scripts/test-installer.py \
  InstallerTests.test_user_agent_layers_survive_upgrade_repair_and_uninstall
```

The host probe copies the installed agent bytes and user settings into an
isolated config root. It excludes the test release's synthetic adapter files,
uses `--pure`, and makes no provider request. It checks resolved model and
permissions, tool visibility, and instruction registration against an upgraded
definition with conflicting defaults, not a captured model request.

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

From an ordinary shell, the operator runs the recorded prepared-cutover command
when the maintenance window opens:

```sh
python3 concord-installer.py cutover --version v0.2.0
```

It refuses while **any** live session remains, naming the holders before recovery,
snapshot or migration. An unknown session observation also refuses. Otherwise it
sends explicit JSON to the candidate's upgrade command, verifies that no migration
remains pending, activates the prepared tree, and checks `current`, the active
manifest and schema. It uses the recorded candidate, without downloading a release.
An inherited `CONCORD_DB_PATH` cannot redirect the upgrade to another store.

The command never confirms or cancels on the operator's behalf. If an installed
tree cannot honor the fence, inspect the named trees and use
`--confirm-sessions-stopped` only after confirming none runs a session. If the
authoring-cutover gate names pinned work, `--cancel-cutover-pinned-work` explicitly
requests the existing snapshot-backed cancellation of every selected item.
Without those flags, the original refusals remain in force.

Only one maintenance command runs at a time: the migration command and every
installer command share one lock. A second command refuses with `another
maintenance command is in progress`; re-run it after the first ends.
Cutover holds that lock for the whole command. Its upgrade child inherits the
same open directory descriptor and validates it through the native lock owner.
The child closes only its duplicate, without unlocking the parent's descriptor.
Each upgrade child also holds the retained `native-upgrade.lock` through a
separate open descriptor, so two children sharing a parent cannot migrate together.
Every recovery, migration, activation and verification stays inside this one
exclusion; another install cannot substitute a different prepared release.
While the boundary is open, an install whose own activation is blocked or unknown
refuses and keeps the prepared record. Re-run the recorded cutover command to
complete it.

The separate migration and `activate` routes remain available for diagnosis.
`concord upgrade` refuses terminal stdin immediately with the required input;
it does not wait for terminal EOF. Pipe `{}` for defaults or `{"plan":true}`
for a read-only plan.

When breaking migrations are pending, the migration command takes and verifies
a database snapshot after session admission closes and the final session checks
pass, before applying any migration. The snapshot uses the backup command's
Online Backup API and verification rules and remains in the data root's
backups directory. Success reports its path as `backup` in the upgrade JSON
report. A snapshot or verification failure names the cause and path, applies no
migration, and closes only the boundary that this run opened. An adopted boundary
stays unchanged. A read-only upgrade plan reports `snapshot_required` without
creating a snapshot or directory. A fresh store with no database file needs no
snapshot. An existing empty file is not exempt. A later migration or finish-open
failure names the retained verified snapshot on stderr.

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
and the operator re-runs cutover. After the migration commits, the
older release cannot open the store, so a crashed activation recovers forward
to the prepared candidate; repair and uninstall refuse until it completes.
Re-running cutover after migration or activation completes verifies the same
release without repeating a completed migration or taking another snapshot.

## Repair

An incomplete deployment of the installed release — missing runtime modules, a
stale manifest that records fewer files than the release ships, or a missing
registration — is repaired with the published installer from an ordinary shell.
Download `concord-installer.py` from the intended public release. If the installed
core runs and a work database exists, first create a verified backup at a fresh
absolute path outside any repository:

```sh
printf '{"destination":"%s"}\n' "$HOME/concord-before-repair-$(date -u +%Y%m%dT%H%M%SZ).db" | concord backup &&
python3 concord-installer.py repair
```

Stop if the backup fails. The installer does not create a database backup or
open the work database. Repair keeps the installed release: it never upgrades
or downgrades. The report names the repaired files and verification result.

Offline repair uses locally published assets:

```sh
python3 concord-installer.py repair --artifact-dir /path/to/assets
```

The directory must hold the release archive and its checksum file. Run the
same separate backup step first when the installed core is usable. If the
`concord` executable itself is missing, run the published installer directly
and retain existing verified database backups.

Repair refuses before changing anything when an asset fails its checksum, when
the release archive is incomplete, or when a managed file was modified so that
it matches neither the installed manifest nor the release. The refusal names
the offending artifact or path. The work database, worktrees, credentials, and
unrelated configuration are outside the managed paths and are never touched.

### Managed snapshot retention

After a successful upgrade that made a verified snapshot, Concord
keeps at most eight verified managed snapshots no older than 30 days. It ranks
them by verified manifest creation time, newest first, with descending filename
as the stable tie-break. The newest verified snapshot and the snapshot the
finishing operation just made count toward eight but are never deleted, even
when older than 30 days or outranked by a clock-skewed creation time.

Cleanup owns only regular snapshot/manifest pairs in the data root's `backups`
directory with the reserved `pre-upgrade-<release>-<timestamp>` or
`pre-repair-<release>-<timestamp>` name. Each manifest must pass backup verification.
Foreign names, invalid or incomplete pairs, symlinks, and snapshots with pending
SQLite sidecar data remain untouched. Operator-selected exports outside this
managed namespace are not subject to cleanup.

The maintenance lock serializes cleanup with upgrade and installer operations.
Restore from a managed snapshot holds its source data root's maintenance lock
even when its destination is elsewhere; it never starts cleanup.
Any maintenance fence (`maintenance.json`), prepared activation
(`prepared-release.json`), entry in `.concord-transactions`, or
`.concord-restore-*` staging in the data root or its backups directory excludes
the entire snapshot set from cleanup. Malformed records also exclude cleanup;
their age never proves recovery finished. No new recovery record or per-snapshot
pin is created. A breaking upgrade that retains its activation fence therefore
reports deferred cleanup rather than pruning.

Upgrade reports `snapshot_retention` in its JSON result, including the
deleted count, protected excess, and any exclusion reason. Protected excess
counts verified snapshots outside the ordinary count or age limit that safety
rules kept. Recovery can therefore keep more than eight snapshots indefinitely.
Failed snapshot creation or a failed upgrade never prunes. Restore
never prunes. A cleanup failure reports that the operation finished but cleanup
failed, in the upgrade's `snapshot_retention.error`,
and keeps the operation's success exit status. An interrupted pair removal can
leave an orphan manifest, which is not treated as a verified snapshot on a
later pass.

## Recovery

Recovery restores a verified snapshot and reconciles current rows, and a refusal to open the authority database states the recovery action the caller may take ([CD-0242 D2](../law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history), [CD-0242 D12](../law/decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d12-every-refusal-states-an-action-the-caller-may-take)); the store keeps no append-only event log to rebuild from.

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
