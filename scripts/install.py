#!/usr/bin/env python3
"""Install, upgrade, and uninstall Concord release artifacts.

The installer uses only the Python standard library.  It verifies the published
checksum and archive before changing the operator's data, binary, adapter, or
OpenCode configuration paths.
"""
from __future__ import annotations

import argparse
import errno
import fcntl
import hashlib
import json
import os
import platform
import re
import select
import shlex
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import tempfile
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path


VERSION_RE = re.compile(r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")
MANIFEST_NAME = "install-manifest.json"
# The shipped adapter set follows the plugin entry's module import graph, not
# a hand list: an entry import referencing an unshipped module is a
# new-session outage, not a packaging choice (issue #681).
RELATIVE_IMPORT_RE = re.compile(r'(?:from\s+|import\(\s*)([\'"])(\.[^\'"]+)\1')
# The OpenCode plugin entry module, installed into the tools directory and
# registered by path in the host `plugin` array so OpenCode loads it as the
# adapter's plugin factory. The import walk starts here.
PLUGIN_ENTRY_FILE = "concord-plugin.ts"


class InstallerError(Exception):
    """An actionable refusal that must not leave a partial installation."""


def resolve_adapter_module(adapter_dir: Path, specifier: str) -> str | None:
    """Resolve one relative import specifier to its shipped module name.

    The adapter directory holds two module kinds: TypeScript sources the
    host runs directly, and plain JavaScript modules. The graph ships
    source modules, so a specifier resolves the exact file it names: an
    explicit .ts suffix resolves that .ts file, an explicit .js suffix
    resolves the plain JavaScript module of that exact name, and an
    extensionless specifier names a TypeScript source. A declared .js
    module with no such file refuses rather than substituting a same-stem
    .ts source, which would conceal the loss at release time.
    """
    module = specifier[2:]
    if not module.endswith((".ts", ".js")):
        module = f"{module}.ts"
    if (adapter_dir / module).is_file():
        return module
    return None


def derive_adapter_files(adapter_dir: Path) -> tuple[str, ...]:
    """Derive the deployable adapter set from the entry's import graph.

    A relative import may name a TypeScript source (extensionless or with
    an explicit .ts suffix) or a plain JavaScript module (an explicit .js
    suffix). Every specifier must resolve to a module inside the adapter
    directory. An unresolvable or escaping import refuses rather than
    shipping a set that breaks at module load.

    Regenerate the ADAPTER_FILES literal with, from the repository root::

        $ python3 -c "import sys; sys.path.insert(0, 'scripts'); import install; \
            print(install.derive_adapter_files(__import__('pathlib').Path('adapter/opencode')))"
    """
    shipped: set[str] = set()
    pending = [PLUGIN_ENTRY_FILE]
    while pending:
        name = pending.pop()
        if name in shipped:
            continue
        path = adapter_dir / name
        if not path.is_file():
            raise InstallerError(f"adapter module graph references missing file {name!r}")
        shipped.add(name)
        for match in RELATIVE_IMPORT_RE.finditer(path.read_text(encoding="utf-8")):
            target = match.group(2)
            if target.startswith("../"):
                raise InstallerError(f"adapter module graph leaves the adapter directory at {target!r}")
            resolved = resolve_adapter_module(adapter_dir, target)
            if resolved is None:
                raise InstallerError(f"adapter module graph references missing file {target!r}")
            pending.append(resolved)
    return tuple(sorted(shipped))


# The adapter set this installer manages. It is a literal, not a read of a
# sibling checkout, because the published installer ships alone: release.yml
# copies this file to concord-installer.py and the operator downloads that one
# file, so nothing else is on disk beside it.
#
# ADAPTER_FILES also constrains which paths a recorded manifest may name during
# status and uninstall, when no bundle has been downloaded. It is therefore a
# property of the installer, and cannot be derived from the archive it installs.
#
# derive_adapter_files remains the authority for this value; the function's
# docstring carries the command that regenerates this literal.
#
# test_real_checkout_derivation_includes_the_plugin_entry compares this literal
# against the live import graph and fails when the two drift.
ADAPTER_FILES = (
    "agent-switch-hook.ts",
    "ci-watch.ts",
    "claimed-worktree.ts",
    "concord-plugin.ts",
    "concord.ts",
    "continuity-hook.ts",
    "credentials.ts",
    "dispatch-window.ts",
    "dispatch.ts",
    "generated-agent-lanes.ts",
    "generated-contract-tests.ts",
    "generated-contracts.ts",
    "generated-lane-step-dispatch.ts",
    "generated-release.ts",
    "host-lease.ts",
    "lane_completion.ts",
    "lane_dispatch.ts",
    "manifest-pin.ts",
    "move-notice.ts",
    "move-session.ts",
    "packet.ts",
    "project-link.ts",
    "task-result.ts",
    "turn-move-boundary.ts",
    "worker-report-protocol.js",
    "worker_recovery.ts",
    "workflow-status.ts",
)
INSTRUCTION_FILES = (
    "asking.md",
    "change.md",
    "completion.md",
    "continuation.md",
    "evidence.md",
    "records.md",
    "voice.md",
)
AGENT_FILES = (
    "concord-design.md",
    "concord-explore.md",
    "concord-implement.md",
    "concord-lookup.md",
    "concord-research.md",
    "concord-review.md",
    "concord-verify.md",
    "concord-advisor.md",
)
STABLE_ROOT_NAME = "current"
AGENT_GLOB = "concord-*.md"
PROJECT_CONFIG_NAMES = ("opencode.json", "opencode.jsonc")
PROJECT_LINK_OWNERSHIP_NAME = "project-link-ownership.json"
PROJECT_LINK_PENDING_NAME = "project-link-pending.json"
MAX_PROJECT_LINKS = 1024
CREDENTIAL_UNIT_NAME = "concord-keyring-unlock.service"
CREDENTIAL_DROPIN_NAME = "concord-login.conf"
CREDENTIAL_SERVICE_NAME = "org.freedesktop.secrets.service"
SECRET_SERVICE_DESTINATION = "org.freedesktop.secrets"
SECRET_SERVICE_PATH = "/org/freedesktop/secrets"
SECRET_SERVICE_INTERFACE = "org.freedesktop.Secret.Service"
SECRET_SERVICE_SESSION_COLLECTION = f"{SECRET_SERVICE_PATH}/collection/session"

# An activating operation places a release; uninstall removes one. Repair
# activates the installed release again from verified assets, and activate
# activates the prepared candidate the operator already migrated to, so
# every activation-site branch that names "install" accepts both through
# this predicate instead of an equality test.
ACTIVATING_OPERATIONS = ("install", "repair", "activate")


def activating(operation: str) -> bool:
    return operation in ACTIVATING_OPERATIONS


@dataclass(frozen=True)
class Paths:
    home: Path
    data_home: Path
    config_home: Path
    bin_dir: Path
    data_root: Path
    config_file: Path
    tools_dir: Path
    agents_dir: Path
    systemd_user_dir: Path
    credential_unit: Path
    credential_dropin: Path
    credential_service: Path
    launcher: Path
    stable_root: Path


@dataclass(frozen=True)
class ConfigPlan:
    path: Path
    text: str
    changed: bool
    managed_fragment: str | None


def paths_for(root: Path | None) -> Paths:
    if root is not None:
        home = root.resolve()
        data_home = home / "data"
        config_home = home / "config"
        bin_dir = home / "bin"
    else:
        home = Path.home()
        durable_data_home = home / ".local" / "share"
        data_home = Path(os.environ.get("XDG_DATA_HOME", durable_data_home))
        # A per-project session shard (the `oc` wrapper sets XDG_DATA_HOME to
        # one) is not a durable data home: an install there scatters versioned
        # assets and the install manifest where no later run looks, and the
        # next install outside the session then classifies every installed
        # file as user-authored. The durable default is the only root a later
        # run consults, so resolve it and say so (#648).
        if "/opencode-projects/" in str(data_home):
            print(
                "installer data root: XDG_DATA_HOME="
                f"{data_home} is a per-project session shard; using the durable "
                f"{durable_data_home / 'concord'} instead",
                file=sys.stderr,
            )
            data_home = durable_data_home
        config_home = Path(os.environ.get("XDG_CONFIG_HOME", home / ".config"))
        bin_dir = home / ".local" / "bin"
    return Paths(
        home=home,
        data_home=data_home,
        config_home=config_home,
        bin_dir=bin_dir,
        data_root=data_home / "concord",
        config_file=config_home / "opencode" / "opencode.jsonc",
        tools_dir=config_home / "opencode" / "tools",
        agents_dir=config_home / "opencode" / "agents",
        systemd_user_dir=config_home / "systemd" / "user",
        credential_unit=config_home / "systemd" / "user" / CREDENTIAL_UNIT_NAME,
        credential_dropin=(
            config_home
            / "systemd"
            / "user"
            / "gnome-keyring-daemon.service.d"
            / CREDENTIAL_DROPIN_NAME
        ),
        credential_service=data_home / "dbus-1" / "services" / CREDENTIAL_SERVICE_NAME,
        launcher=bin_dir / "concord",
        stable_root=data_home / "concord" / STABLE_ROOT_NAME,
    )


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


# CD-0111 D1: the adapter calls the core at its own release path, never
# through PATH. The repository ships this file as an unstamped placeholder and
# the installer stamps the absolute release paths into the staged copy before
# any hash is recorded, so the version records, the adapter records, and the
# tools-dir copy all describe the stamped bytes.
RELEASE_CONSTANTS_FILE = "generated-release.ts"


def release_constants_source(version_root: Path) -> str:
    core = version_root / "bin" / "concord"
    return (
        "// Code stamped by scripts/install.py at install time; DO NOT EDIT.\n"
        "// CD-0111 D1: a session runs every core call against the release it\n"
        "// started on. These constants are absolute paths into that release.\n"
        f"export const releaseRoot: string = {json.dumps(str(version_root))}\n"
        f"export const coreBinary: string = {json.dumps(str(core))}\n"
    )


def stamp_release_constants(adapter_dir: Path, version_root: Path) -> None:
    target = adapter_dir / RELEASE_CONSTANTS_FILE
    if not target.is_file():
        raise InstallerError(f"release archive omits {RELEASE_CONSTANTS_FILE}; the adapter cannot bind to its release")
    target.write_text(release_constants_source(version_root), encoding="utf-8")


# The maintenance-fence protocol marker (CON-807). The core's
# unfenceable-participant check reads this file from every installed
# release tree: a tree whose core honors the shared session-admission
# exclusion carries the protocol number it speaks, and a tree without a
# recognized marker names a core the boundary cannot exclude. The number
# written is the one the staged core reported about itself; the Go consumer
# (internal/hostlease.CurrentFenceProtocol) owns what counts as recognized.
FENCE_PROTOCOL_MARKER_NAME = "fence-protocol"

# The fence protocols this installer recognizes (CON-807). Recognition is
# explicit, never a range: a higher number names semantics this installer has
# not implemented, so it grants no maintenance capability.
SUPPORTED_FENCE_PROTOCOLS = frozenset({1})


def probe_core_capability(version_tree: Path, version: str | None = None) -> int | None:
    """Ask the staged core which maintenance-fence protocol it speaks.

    The probe runs the bootstrap surface only, which answers before any
    stdin read, store open, or lease write. The core must run and identify
    as the release being staged: a core that cannot is unusable, and the
    staging refuses. Its descriptor then decides capability alone. A
    missing, malformed, or unsupported protocol grants no maintenance
    capability and leaves the tree unmarked; it does not refuse staging.
    """
    binary = version_tree / "bin" / "concord"
    if not binary.is_file():
        raise InstallerError(f"staged release has no core binary at {binary}")
    environment = {key: value for key, value in os.environ.items() if key != "CONCORD_DB_PATH"}
    try:
        identity = subprocess.run(
            [str(binary), "--version"],
            capture_output=True,
            env=environment,
            timeout=HOST_LEASE_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise InstallerError(f"cannot run the staged core at {binary}: {error}") from error
    reported = identity.stdout.decode("utf-8", "replace").strip()
    if identity.returncode != 0 or not reported:
        raise InstallerError(
            f"the staged core at {binary} is unusable: --version exited {identity.returncode}"
        )
    if version is not None and reported != version:
        raise InstallerError(
            f"the staged core at {binary} identifies as {reported!r}, not the release {version!r}"
        )
    try:
        described = subprocess.run(
            [str(binary), "--version", "--json"],
            capture_output=True,
            env=environment,
            timeout=HOST_LEASE_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        # The core already proved usable; a descriptor it cannot deliver
        # grants no capability, the same as an absent one.
        return None
    if described.returncode != 0:
        return None
    try:
        descriptor = json.loads(described.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None
    protocol = descriptor.get("fence_protocol") if isinstance(descriptor, dict) else None
    if not isinstance(protocol, int) or isinstance(protocol, bool) or protocol not in SUPPORTED_FENCE_PROTOCOLS:
        return None
    return protocol


def stamp_fence_protocol(version_tree: Path, version: str | None = None) -> None:
    """Mark the staged tree with the fence protocol its core reported.

    A tree without a supported protocol stays unmarked: its sessions would
    not check the fence, so release cleanup retains around it and an
    incompatible migration reports it for operator confirmation.
    """
    protocol = probe_core_capability(version_tree, version)
    if protocol is None:
        return
    marker = version_tree / FENCE_PROTOCOL_MARKER_NAME
    marker.write_text(f"{protocol}\n", encoding="utf-8")


# CD-0111 D2: the host owns session liveness. The installer asks the core of
# the release it just installed which releases live sessions hold, and keeps
# every one of them. A failed observation is not an empty one: the caller
# falls back to the bounded removal the decision names.
HOST_LEASE_TIMEOUT_SECONDS = 30


def observe_held_releases(paths: Paths, version: str) -> dict[str, list[dict[str, object]]] | None:
    """Return the live host sessions holding each release root, keyed by the
    resolved release root, or None when the observation failed. The holder
    sessions are the retention output's per-release restart list (CD-0191):
    a session the install made stale keeps working, and the operator needs
    to know which sessions to restart."""
    binary = paths.data_root / version / "bin" / "concord"
    environment = {key: value for key, value in os.environ.items() if key != "CONCORD_DB_PATH"}
    environment["XDG_DATA_HOME"] = str(paths.data_home)
    try:
        completed = subprocess.run(
            [str(binary), "host-leases"],
            input=b"{}",
            capture_output=True,
            env=environment,
            timeout=HOST_LEASE_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        print(f"host lease observation failed: {error}", file=sys.stderr)
        return None
    if completed.returncode != 0:
        print(f"host lease observation failed: exit {completed.returncode}: {completed.stderr.decode('utf-8', 'replace').strip()[:400]}", file=sys.stderr)
        return None
    try:
        report = json.loads(completed.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        print(f"host lease observation failed: {error}", file=sys.stderr)
        return None
    leases = report.get("leases") if isinstance(report, dict) else None
    if not isinstance(leases, list):
        print("host lease observation failed: response carries no lease list", file=sys.stderr)
        return None
    held: dict[str, list[dict[str, object]]] = {}
    for lease in leases:
        root = lease.get("release_root") if isinstance(lease, dict) else None
        if not isinstance(root, str) or not root:
            print("host lease observation failed: a lease names no release root", file=sys.stderr)
            return None
        holder: dict[str, object] = {}
        pid = lease.get("pid")
        if isinstance(pid, int) and not isinstance(pid, bool):
            holder["pid"] = pid
        for field in ("directory", "worktree"):
            value = lease.get(field)
            if isinstance(value, str) and value:
                holder[field] = value
        held.setdefault(str(Path(root).resolve()), []).append(holder)
    return held


def describe_release_holders(holders: list[dict[str, object]]) -> str:
    """Render holder sessions for the retention output. Each holder names its
    pid and, when the lease carries one, the directory or worktree to find it
    in; a lease without either still names itself as one holder."""
    parts: list[str] = []
    for holder in holders:
        pid = holder.get("pid")
        location = holder.get("worktree") or holder.get("directory")
        who = f"pid {pid}" if pid is not None else "a session"
        parts.append(f"{who} in {location}" if location else who)
    return "; ".join(parts)


# CON-807: the installer prepares a candidate release durably when the core's
# read-only readiness plan blocks activation, and activates the prepared
# candidate only after the operator runs the recorded migration command and
# this activation command. The prepared record is the durable
# prepared-versus-active state: the candidate tree and this record exist
# while the launcher, the current root, the tools, and the agents stay on the
# release the running sessions hold.
PREPARED_RELEASE_NAME = "prepared-release.json"
PREPARED_RELEASE_SCHEMA = "concord-prepared-release-v1"
MAINTENANCE_FENCE_NAME = "maintenance.json"


def prepared_release_path(paths: Paths) -> Path:
    return paths.data_root / PREPARED_RELEASE_NAME


def maintenance_fence_path(paths: Paths) -> Path:
    return paths.data_root / MAINTENANCE_FENCE_NAME


def load_prepared_release(paths: Paths) -> dict[str, object] | None:
    """Read and validate the prepared-release record, or None when absent.

    The record is installer-owned state: a malformed record is a refusal,
    never a guess, because a wrong version_files map would hand the next
    activation authority over files this install never wrote.
    """
    path = prepared_release_path(paths)
    if path.is_symlink():
        raise InstallerError(f"refusing symlinked prepared-release record {path}")
    if not path.exists():
        return None
    try:
        record = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise InstallerError(f"cannot read prepared-release record {path}: {error}") from error
    if not isinstance(record, dict) or record.get("schema") != PREPARED_RELEASE_SCHEMA:
        raise InstallerError(f"refusing unrecognized prepared-release record {path}")
    version = record.get("version")
    if not isinstance(version, str) or not VERSION_RE.fullmatch(version):
        raise InstallerError(f"prepared-release record {path} has an invalid version")
    files = record.get("version_files")
    if not isinstance(files, dict) or not files or len(files) > MAX_TRANSACTION_FILES:
        raise InstallerError(f"prepared-release record {path} has invalid version file records")
    root = safe_relative_target(paths.data_root, version, "prepared release")
    for relative, digest in files.items():
        if not isinstance(relative, str) or not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
            raise InstallerError(f"prepared-release record {path} has invalid version file records")
        safe_relative_target(root, relative, "prepared release file")
    blockers = record.get("blockers")
    if not isinstance(blockers, list) or not all(isinstance(line, str) and line for line in blockers):
        raise InstallerError(f"prepared-release record {path} has invalid blockers")
    for key in ("migration_command", "activation_command", "created_at"):
        if not isinstance(record.get(key), str) or not record.get(key):
            raise InstallerError(f"prepared-release record {path} is missing {key}")
    # boundary_fence_id is the identity of the open boundary this record's
    # activation adopted (written durably before its transaction): the
    # discharge closes exactly that identity and never a foreign fence.
    # boundary_owner_version names the release whose committed activation
    # discharges the boundary — the prepared version itself, or the
    # superseding release that adopted the boundary to end this path.
    fence_id = record.get("boundary_fence_id")
    if fence_id is not None and (not isinstance(fence_id, str) or not fence_id):
        raise InstallerError(f"prepared-release record {path} has an invalid boundary fence identity")
    owner = record.get("boundary_owner_version")
    if owner is not None and (not isinstance(owner, str) or not VERSION_RE.fullmatch(owner)):
        raise InstallerError(f"prepared-release record {path} has an invalid boundary owner version")
    if owner is not None and fence_id is None:
        raise InstallerError(f"prepared-release record {path} names a boundary owner with no boundary identity")
    return record


def write_prepared_release(paths: Paths, record: dict[str, object]) -> None:
    payload = (json.dumps(record, indent=2, sort_keys=True) + "\n").encode("utf-8")
    write_atomic(prepared_release_path(paths), payload)
    fsync_directory(paths.data_root)


def clear_prepared_release(paths: Paths) -> None:
    path = prepared_release_path(paths)
    if path.exists() or path.is_symlink():
        path.unlink()
        fsync_directory(paths.data_root)


def installer_invocation() -> str:
    """This installer's own absolute path, for recorded operator commands."""
    return str(Path(sys.argv[0]).resolve())


def activation_command_for(version: str, root: Path | None) -> str:
    """The exact activation command, quoted for a POSIX shell.

    shlex.join keeps argument boundaries intact when a recorded path carries
    a space, so splitting the recorded command reproduces exactly the
    arguments this installer would run.
    """
    parts = [sys.executable or "python3", installer_invocation(), "activate", "--version", version]
    if root is not None:
        parts.extend(["--root", str(root)])
    return shlex.join(parts)


@contextmanager
def admission_lock(paths: Paths):
    """Hold the data root's shared admission lock (CON-807).

    The core's hostlease package serializes session admission against
    maintenance-boundary changes through flock on this same lock file, so the
    installer takes it too: a fence the installer opens or removes while a
    session is mid-admission would let that session land on the wrong side of
    the boundary. Blocking here is correct — the installer waits for the
    admission to finish, then changes the boundary under the lock.
    """
    ensure_directory(paths.data_root, mode=0o700)
    handle = (paths.data_root / "admission.lock").open("a+b")
    try:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            yield handle
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
    finally:
        handle.close()


# The acquisition retries only this many times when the data root is removed
# or replaced between the open and the flock. The bound keeps a churning root
# from turning the lock into a wait; the operator re-runs the command (CON-807).
MAINTENANCE_ACQUIRE_ATTEMPTS = 3


def _acquire_maintenance_lock(path: Path) -> int:
    """Open and flock the data root directory, without waiting (CON-807).

    A first-install bootstrap locks the same way an established root does:
    the root is created before it is opened, so an absent root is never an
    unlocked window another command could recover a live transaction through.
    The creation is a plain mkdir of the missing levels; what it created is
    retained at release (obs:1ca149d633e69626), so no creation-attribution
    machinery follows it — attribution existed only to authorize deletion,
    and no path-based deletion is safe against a replacement holder.

    The lock identity is the directory the path names at admission: the root
    is opened without following symlinks, and after the flock the open
    descriptor's device and inode are compared with what the path names now,
    read with lstat so a replaced symlink is never followed. A root removed
    or replaced in between releases the stale descriptor and the next
    attempt re-opens the current path; the bounded sequence never sleeps.

    Returns the locked descriptor. Nothing removes the root afterwards:
    retained disk space is the accepted cost.
    """
    for attempt in range(1, MAINTENANCE_ACQUIRE_ATTEMPTS + 1):
        last = attempt == MAINTENANCE_ACQUIRE_ATTEMPTS
        os.makedirs(path, exist_ok=True)
        try:
            descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        except OSError as error:
            if error.errno in (errno.ELOOP, errno.ENOTDIR):
                # The open itself refused, without following the path; the
                # lstat only names the reason for the operator.
                if path.is_symlink():
                    raise InstallerError(f"refusing symlinked data root {path}") from error
                raise InstallerError(f"refusing data root {path}: it is not a directory") from error
            if error.errno == errno.ENOENT and not last:
                continue
            raise
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            os.close(descriptor)
            raise InstallerError(
                f"another maintenance command is in progress: {path} is locked; re-run when it finishes"
            ) from error
        except BaseException:
            os.close(descriptor)
            raise
        held = os.fstat(descriptor)
        try:
            current = os.lstat(path)
        except FileNotFoundError:
            os.close(descriptor)
            if not last:
                continue
            raise InstallerError(
                f"the data root {path} kept changing while the maintenance lock was taken; re-run the command"
            ) from None
        if not stat.S_ISDIR(current.st_mode) or (held.st_dev, held.st_ino) != (current.st_dev, current.st_ino):
            os.close(descriptor)
            continue
        return descriptor
    raise InstallerError(
        f"the data root {path} kept changing while the maintenance lock was taken; re-run the command"
    )


@contextmanager
def maintenance_lock(paths: Paths):
    """Hold the data root's exclusive maintenance lock (CON-807).

    The core's migration command (internal/hostlease.AcquireMaintenance)
    takes the same lock, so one maintenance command runs at a time. Every
    installer command recovers transactions and may open or close the
    boundary, so each one holds the lock: a concurrent command would
    otherwise recover a live transaction or close a boundary a running
    migration still needs. The lock does not wait; a second command refuses.

    The lock is a flock on the data root directory itself, so it leaves no
    file behind. A first-install bootstrap creates the root to lock it, and
    the release removes nothing (obs:1ca149d633e69626): the empty root, its
    ancestors, a root another participant created or recreated, and any
    state in them all survive a failed installation, a normal release, and
    an uninstall. A held directory flock cannot make a later path-based
    removal conditional on the inode the holder once observed, so no cleanup
    removes the data root at all, and retained disk space is the accepted
    cost. An uninstall removes the intended installed content only. Neither
    the installer nor the core promises exclusion against an arbitrary
    external replacement of the root directory.
    """
    descriptor = _acquire_maintenance_lock(paths.data_root)
    try:
        yield
    finally:
        os.close(descriptor)


# The operations that open a boundary on the prepare-migrate-activate path
# (CON-807). The core's incompatible migration writes the first
# (internal/hostlease.FenceOperationUpgrade); an activation that finds no
# boundary open writes the second. A boundary naming any other operation
# belongs to another maintenance step, which this path never adopts or closes.
CORE_UPGRADE_OPERATION = "concord-upgrade-incompatible-migration"
INSTALLER_ACTIVATION_OPERATION = "concord-installer-prepared-activation"
UPGRADE_PATH_OPERATIONS = frozenset({CORE_UPGRADE_OPERATION, INSTALLER_ACTIVATION_OPERATION})


def require_owned_fence(paths: Paths, fence: dict[str, object], owner_roots: set[str]) -> None:
    """Refuse an open boundary this upgrade path cannot prove it owns.

    Ownership needs both the operation and the release attribution: the
    boundary must be one this path opens, attributed to a candidate root the
    caller activates. Anything else is retained untouched for its owner or
    the operator's offline bootstrap (CON-807).
    """
    location = maintenance_fence_path(paths)
    operation = fence.get("operation")
    if operation not in UPGRADE_PATH_OPERATIONS:
        raise InstallerError(
            f"the open maintenance boundary at {location} belongs to operation {operation!r}, not this upgrade "
            "path; refusing to adopt or later close another maintenance step's boundary"
        )
    attributed = fence.get("release_root")
    if not isinstance(attributed, str) or not attributed:
        raise InstallerError(
            f"the open maintenance boundary at {location} carries no release attribution; "
            "its owner cannot be proven, so this transaction neither adopts it nor later closes it. "
            "Close it through the operator-owned offline bootstrap when no migration is in progress."
        )
    if str(Path(attributed).resolve()) not in {str(Path(root).resolve()) for root in owner_roots}:
        raise InstallerError(
            f"the open maintenance boundary at {location} belongs to the release root "
            f"{attributed}, not this transaction's candidate; refusing to adopt or later close another "
            "operation's boundary"
        )


def ensure_maintenance_fence(paths: Paths, notice: str, release_root: str | None = None) -> dict[str, object]:
    """Open the session-admission exclusion when it is not already open.

    The core's migration command opens the boundary before its final lease
    check and keeps it open after committing a breaking step; the activation
    re-ensures it (adopting the core's record when it survived) and removes
    it only after the candidate committed and release cleanup finished. An
    existing fence is adopted unchanged so the shared boundary keeps one
    identity across both commands. A fence file that cannot be parsed is a
    refusal, and removal is refused unless the fence in effect carries the
    same fence_id: a closer must own what it closes.

    A fence this function creates is attributed to the release root that
    opened it, so a later activation can tell the boundary of its own
    upgrade path from a boundary another operation holds (CON-807).
    """
    path = maintenance_fence_path(paths)
    with admission_lock(paths):
        if path.is_symlink():
            raise InstallerError(f"refusing symlinked maintenance fence {path}")
        if path.exists():
            try:
                fence = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as error:
                raise InstallerError(f"cannot read maintenance fence {path}: {error}") from error
            if not isinstance(fence, dict) or not isinstance(fence.get("fence_id"), str) or not fence.get("fence_id"):
                raise InstallerError(f"refusing malformed maintenance fence {path}")
            return fence
        ensure_directory(paths.data_root, mode=0o700)
        fence = {
            "fence_id": uuid.uuid4().hex,
            "created_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "notice": notice,
            "release_root": release_root or "",
            "operation": INSTALLER_ACTIVATION_OPERATION,
        }
        write_atomic(path, (json.dumps(fence, indent=2, sort_keys=True) + "\n").encode("utf-8"))
        return fence


def remove_maintenance_fence(paths: Paths, fence_id: str) -> None:
    """Close the maintenance boundary only by owned identity (CON-807).

    Positional removal — unlink whatever sits at the fence path — would let
    an operation that observed no fence on entry delete a boundary a
    concurrent operation opened in between, so every closer must name the
    fence_id it owns. A fence carrying a different identity belongs to
    another operation and is left in place untouched.
    """
    path = maintenance_fence_path(paths)
    with admission_lock(paths):
        if path.exists():
            try:
                current = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as error:
                raise InstallerError(f"cannot read maintenance fence {path}: {error}") from error
            if isinstance(current, dict) and current.get("fence_id") != fence_id:
                return  # the boundary in effect is not this caller's to close
        if path.exists() or path.is_symlink():
            path.unlink()
            fsync_directory(paths.data_root)


def upgrade_plan_command(paths: Paths, version: str) -> list[str]:
    return [str(paths.data_root / version / "bin" / "concord"), "upgrade"]


def read_upgrade_plan(paths: Paths, version: str, *, staged_binary: Path | None = None) -> dict[str, object]:
    """Run the candidate's read-only readiness plan against the live store.

    The candidate binary is the authority: it defines the migrations whose
    pending state decides activation. The environment matches the lease
    observation (the durable data home, no inline database override), so the
    plan inspects the same store every session and the installer use. Any
    failure to obtain a plan is readiness unknown, and the caller fails
    closed rather than activating against an unreadable store. A staged
    binary plans before any placement: install gates from the transaction
    stage so the active release's launcher, current root, tools, and agents
    are untouched until the decision is known.
    """
    binary = staged_binary if staged_binary is not None else paths.data_root / version / "bin" / "concord"
    if not binary.is_file():
        raise InstallerError(f"prepared candidate has no core binary at {binary}")
    environment = {key: value for key, value in os.environ.items() if key != "CONCORD_DB_PATH"}
    environment["XDG_DATA_HOME"] = str(paths.data_home)
    try:
        completed = subprocess.run(
            [str(binary), "upgrade"],
            input=b'{"plan":true}',
            capture_output=True,
            env=environment,
            timeout=HOST_LEASE_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise InstallerError(f"store readiness is unknown: the candidate plan failed: {error}") from error
    if completed.returncode != 0:
        detail = completed.stderr.decode("utf-8", "replace").strip()[:400]
        raise InstallerError(f"store readiness is unknown: the candidate plan refused: {detail}")
    try:
        report = json.loads(completed.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise InstallerError(f"store readiness is unknown: the candidate plan returned no report: {error}") from error
    if not isinstance(report, dict) or not isinstance(report.get("activation_blocked"), bool):
        raise InstallerError("store readiness is unknown: the candidate plan carries no activation decision")
    return report


def plan_blockers(report: dict[str, object]) -> list[str]:
    blockers = report.get("blockers")
    if isinstance(blockers, list) and all(isinstance(line, str) for line in blockers):
        return list(blockers)
    return []


def plan_migration_command(paths: Paths, version: str) -> str:
    """The exact migration command, in the environment the plan inspected.

    The plan read the store under this data home with no inline database
    override (read_upgrade_plan). The recorded command pins the same
    environment, so the operator's shell cannot point the migration at a
    different store or create an unrelated one. shlex.join keeps a data root
    containing spaces intact, the same argument-safe rule as
    activation_command_for.
    """
    return shlex.join(
        ["env", "-u", "CONCORD_DB_PATH", f"XDG_DATA_HOME={paths.data_home}", *upgrade_plan_command(paths, version)]
    )


def place_prepared_version(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    """Place the candidate tree durably without activating anything.

    This is the placement half of apply_version with the activation half
    removed: no stable-root swap, so `current` keeps naming the release the
    running sessions hold. The existing target, when a previous prepare left
    one, moves to the transaction's live-version backup like any replaced
    tree; the journal stays at the staged phase until prepare_release
    commits the record, so a crash before that rolls the placement back.
    The placement is idempotent inside its own phase: a crash after the new
    tree landed left the backup in place, and re-running recognizes the
    placed records instead of refusing on the surviving backup.
    """
    version = journal["new_version"]
    if not isinstance(version, str):
        raise InstallerError("transaction has no new version")
    target = paths.data_root / version
    live = transaction_root / "backup" / "live-version"
    staged = transaction_root / "stage" / "version"
    records = journal.get("new_version_records")
    if not isinstance(records, dict):
        raise InstallerError("transaction has no staged version records")
    if target.exists() or target.is_symlink():
        if live.exists() or live.is_symlink():
            if not version_matches(target, records):
                raise InstallerError("transaction live version backup already exists")
        else:
            replace_durable(target, live)
    if staged.exists():
        replace_durable(staged, target)


def prepare_release(
    transaction_root: Path,
    journal: dict[str, object],
    paths: Paths,
    version: str,
    version_records: dict[str, str],
    blockers: list[str],
    migration_command: str,
    root: Path | None,
) -> None:
    """Commit the prepared state and stop before any activation swap.

    place_prepared_version already made the candidate tree durable at the
    activation root, and nothing else has changed: the launcher, the current
    root, the tools, and the agents keep the release the running sessions
    hold. This record plus the candidate tree is the durable
    prepared-versus-active state; the transaction journal reaching the
    prepared phase makes the commit recoverable, and the record carries the
    exact commands the operator runs next.
    """
    record: dict[str, object] = {
        "schema": PREPARED_RELEASE_SCHEMA,
        "version": version,
        "version_files": version_records,
        "blockers": blockers,
        "migration_command": migration_command,
        "activation_command": activation_command_for(version, root),
        "created_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    write_prepared_release(paths, record)
    advance_phase(transaction_root, journal, "prepared")
    cleanup_transaction(transaction_root, journal, paths, remove_old_version=False)


def print_prepared_release(record: dict[str, object]) -> None:
    print(f"Prepared Concord {record['version']} without activating it.")
    print("The launcher, the current root, the tools, and the agents keep the active release.")
    for blocker in record["blockers"]:  # type: ignore[union-attr]
        print(f"Blocker: {blocker}")
    print(f"Run the migration command when the maintenance window opens: {record['migration_command']}")
    print(f"Then run the activation command: {record['activation_command']}")


def retained_release_records(manifest: dict[str, object] | None) -> dict[str, dict[str, str]]:
    value = manifest.get("retained_releases") if manifest else None
    return dict(value) if isinstance(value, dict) else {}


def validate_release_records(records: object, paths: Paths, what: str) -> None:
    if not isinstance(records, dict):
        raise InstallerError(f"{what} has invalid retained release records")
    for version, files in records.items():
        if not isinstance(version, str) or not VERSION_RE.fullmatch(version):
            raise InstallerError(f"{what} has an invalid retained release version")
        if not isinstance(files, dict) or not files or len(files) > MAX_TRANSACTION_FILES:
            raise InstallerError(f"{what} has invalid retained release file records")
        root = safe_relative_target(paths.data_root, version, "retained release")
        for relative, digest in files.items():
            if not isinstance(relative, str) or not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
                raise InstallerError(f"{what} has invalid retained release file records")
            safe_relative_target(root, relative, "retained release file")


def parse_version(value: str) -> str:
    if not VERSION_RE.fullmatch(value):
        raise InstallerError(f"invalid release version {value!r}; use vMAJOR.MINOR.PATCH")
    return value


def release_download_base_url(base_url: str, version: str) -> str:
    """Select the asset endpoint for a pinned or source-selected release."""
    parsed = urllib.parse.urlsplit(base_url)
    latest_suffix = "/releases/latest/download"
    path = parsed.path.rstrip("/")
    if not path.endswith(latest_suffix):
        return base_url
    tag_path = path[: -len(latest_suffix)] + f"/releases/download/{urllib.parse.quote(version, safe='')}"
    return urllib.parse.urlunsplit(parsed._replace(path=tag_path))


def resolve_latest_version(artifact_dir: Path | None, base_url: str) -> str:
    """Resolve the latest release the configured source can serve.

    With a local artifact directory the answer is the highest release whose
    checksum file is present. With the download base the answer follows the
    releases/latest redirect to its tag; a custom base that is not a latest
    endpoint has no latest to resolve and must be pinned with --version.
    """
    if artifact_dir:
        releases = sorted(
            (int(match.group(1)), int(match.group(2)), int(match.group(3)))
            for path in artifact_dir.glob("concord-*.sha256")
            if (match := VERSION_RE.fullmatch(path.name.removeprefix("concord-").removesuffix(".sha256")))
        )
        if not releases:
            raise InstallerError(f"no release checksum found under {artifact_dir}; pass --version")
        return "v%d.%d.%d" % releases[-1]
    latest_page = base_url.rstrip("/").removesuffix("/download")
    if not latest_page.endswith("/releases/latest"):
        raise InstallerError(f"cannot resolve a latest release from {base_url}; pass --version")
    try:
        with urllib.request.urlopen(latest_page, timeout=30) as response:
            final_url = response.geturl()
    except Exception as error:
        raise InstallerError(f"could not resolve the latest release at {latest_page}: {error}") from error
    tag = final_url.rstrip("/").rsplit("/", 1)[-1]
    if not VERSION_RE.fullmatch(tag):
        raise InstallerError(f"the latest release redirected to {final_url!r}, which is not a release tag; pass --version")
    return tag


def jsonc_data(text: str) -> object:
    without_comments: list[str] = []
    index = 0
    in_string = False
    escaped = False
    while index < len(text):
        char = text[index]
        if in_string:
            without_comments.append(char)
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                in_string = False
            index += 1
            continue
        if char == '"':
            in_string = True
            without_comments.append(char)
            index += 1
        elif text.startswith("//", index):
            newline = text.find("\n", index)
            index = len(text) if newline < 0 else newline
        elif text.startswith("/*", index):
            end = text.find("*/", index + 2)
            if end < 0:
                raise InstallerError("OpenCode config contains an unterminated comment")
            index = end + 2
        else:
            without_comments.append(char)
            index += 1
    cleaned = re.sub(r",(\s*[}\]])", r"\1", "".join(without_comments))
    try:
        return json.loads(cleaned)
    except json.JSONDecodeError as error:
        raise InstallerError(
            f"cannot safely edit OpenCode config {error.lineno}:{error.colno}; "
            "the installer will not guess or rewrite it"
        ) from error


def project_config_data(project_file: Path, text: str) -> object:
    """Parse a project config with the syntax its filename declares."""
    return json.loads(text) if project_file.suffix == ".json" else jsonc_data(text)


def validate_project_config(project_file: Path, text: str) -> None:
    """Verify project config text before the installer writes it."""
    try:
        project_config_data(project_file, text)
    except (InstallerError, json.JSONDecodeError) as error:
        raise InstallerError(f"cannot safely write project OpenCode config {project_file}: {error}") from error


def has_trailing_jsonc_comma(text: str) -> bool:
    """Return whether an object has a comma before its closing brace."""
    return bool(
        re.search(
            r",(?:(?:[ \t]*//[^\n]*(?:\n|$))|(?:[ \t]*/\*.*?\*/[ \t]*))*[ \t\r\n]*$",
            text,
            re.S,
        )
    )


def outer_object_end(text: str) -> int:
    start = text.find("{")
    if start < 0:
        raise InstallerError("OpenCode config has no JSON object to edit")
    depth = 0
    in_string = False
    escaped = False
    index = start
    while index < len(text):
        char = text[index]
        if in_string:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                in_string = False
        elif char == '"':
            in_string = True
        elif text.startswith("//", index):
            newline = text.find("\n", index)
            index = len(text) if newline < 0 else newline
        elif text.startswith("/*", index):
            end = text.find("*/", index + 2)
            if end < 0:
                raise InstallerError("OpenCode config contains an unterminated comment")
            index = end + 1
        elif char == "{":
            depth += 1
        elif char == "}":
            depth -= 1
            if depth == 0:
                return index
        index += 1
    raise InstallerError("OpenCode config has unbalanced braces")


def skip_jsonc_space(text: str, index: int) -> int:
    """Skip JSON whitespace and comments between a key and its value."""
    while index < len(text):
        while index < len(text) and text[index].isspace():
            index += 1
        if text.startswith("//", index):
            newline = text.find("\n", index + 2)
            index = len(text) if newline < 0 else newline + 1
            continue
        if text.startswith("/*", index):
            end = text.find("*/", index + 2)
            if end < 0:
                raise InstallerError("OpenCode config contains an unterminated comment")
            index = end + 2
            continue
        break
    return index


def jsonc_array_end(text: str, key: str) -> int | None:
    """Return the closing bracket of a named JSON or JSONC array."""
    quoted = json.dumps(key)
    index = 0
    object_depth = 0
    array_depth = 0
    while index < len(text):
        char = text[index]
        if char == '"':
            end = index + 1
            escaped = False
            while end < len(text):
                current = text[end]
                if escaped:
                    escaped = False
                elif current == "\\":
                    escaped = True
                elif current == '"':
                    break
                end += 1
            if end >= len(text):
                return None
            if object_depth == 1 and array_depth == 0 and text[index : end + 1] == quoted:
                after = skip_jsonc_space(text, end + 1)
                if after < len(text) and text[after] == ":":
                    after = skip_jsonc_space(text, after + 1)
                    if after < len(text) and text[after] == "[":
                        bracket_depth = 0
                        cursor = after
                        string = False
                        escaped = False
                        while cursor < len(text):
                            current = text[cursor]
                            following = text[cursor + 1] if cursor + 1 < len(text) else ""
                            if string:
                                if escaped:
                                    escaped = False
                                elif current == "\\":
                                    escaped = True
                                elif current == '"':
                                    string = False
                            elif current == '"':
                                string = True
                            elif current == "/" and following == "/":
                                newline = text.find("\n", cursor + 2)
                                if newline < 0:
                                    return None
                                cursor = newline + 1
                                continue
                            elif current == "/" and following == "*":
                                comment_end = text.find("*/", cursor + 2)
                                if comment_end < 0:
                                    raise InstallerError("OpenCode config contains an unterminated comment")
                                cursor = comment_end + 2
                                continue
                            elif current == "[":
                                bracket_depth += 1
                            elif current == "]":
                                bracket_depth -= 1
                                if bracket_depth == 0:
                                    return cursor
                            cursor += 1
            index = end + 1
            continue
        if text.startswith("//", index):
            newline = text.find("\n", index + 2)
            if newline < 0:
                return None
            index = newline + 1
            continue
        if text.startswith("/*", index):
            end = text.find("*/", index + 2)
            if end < 0:
                raise InstallerError("OpenCode config contains an unterminated comment")
            index = end + 2
            continue
        if char == "{":
            object_depth += 1
        elif char == "}":
            object_depth -= 1
        elif char == "[":
            array_depth += 1
        elif char == "]":
            array_depth -= 1
        index += 1
    return None


def registration_snippet(skill_path: str) -> str:
    return f'"skills": {{"paths": [{json.dumps(skill_path)}]}}'


def stable_skill_path(paths: Paths) -> str:
    """Return the version-stable skills directory registered with OpenCode."""
    return str(paths.stable_root / "skills")


def plan_config(path: Path, skill_path: str, old_skill_path: str | None) -> ConfigPlan:
    if not path.exists():
        text = '{\n  "skills": {\n    "paths": [\n      ' + json.dumps(skill_path) + '\n    ]\n  }\n}\n'
        return ConfigPlan(path, text, True, text)
    original = path.read_text(encoding="utf-8")
    parsed = jsonc_data(original)
    if not isinstance(parsed, dict):
        raise InstallerError(
            f"OpenCode config {path} is not an object; add {registration_snippet(skill_path)} manually"
        )
    skills = parsed.get("skills")
    if skills is None:
        end = outer_object_end(original)
        before = original[:end]
        separator = "" if before.rstrip().endswith("{") else ","
        addition = (
            f'{separator}\n  "skills": {{\n    "paths": [\n      {json.dumps(skill_path)}\n    ]\n  }}\n'
        )
        return ConfigPlan(path, original[:end] + addition + original[end:], True, addition)
    if not isinstance(skills, dict) or not isinstance(skills.get("paths"), list):
        raise InstallerError(
            f"OpenCode config {path} has an incompatible skills.paths value; "
            f"add {registration_snippet(skill_path)} manually"
        )
    values = skills["paths"]
    if not all(isinstance(value, str) for value in values):
        raise InstallerError(
            f"OpenCode config {path} has non-string skills.paths entries; "
            f"add {json.dumps(skill_path)} manually"
        )
    if skill_path in values:
        return ConfigPlan(path, original, False, None)
    if old_skill_path and old_skill_path in values:
        old_json = json.dumps(old_skill_path)
        if original.count(old_json) != 1:
            raise InstallerError(
                f"cannot safely replace the managed skills path in {path}; "
                f"replace {old_skill_path!r} with {skill_path!r} manually"
            )
        replacement = json.dumps(skill_path)
        return ConfigPlan(path, original.replace(old_json, replacement), True, old_json)
    raise InstallerError(
        f"OpenCode config {path} already defines skills.paths; the installer will not "
        f"clobber it. Add {json.dumps(skill_path)} to that array manually"
    )


def remove_path_from_config(path: Path, skill_path: str) -> str:
    original = path.read_text(encoding="utf-8")
    parsed = jsonc_data(original)
    if not isinstance(parsed, dict) or not isinstance(parsed.get("skills"), dict):
        return original
    values = parsed["skills"].get("paths")
    if not isinstance(values, list) or skill_path not in values:
        return original
    token = json.dumps(skill_path)
    if original.count(token) != 1:
        raise InstallerError(f"cannot safely remove the managed skills path from {path}")
    start = original.index(token)
    end = start + len(token)
    after = end
    while after < len(original) and original[after].isspace():
        after += 1
    if after < len(original) and original[after] == ",":
        end = after + 1
    else:
        before = start - 1
        while before >= 0 and original[before].isspace():
            before -= 1
        if before >= 0 and original[before] == ",":
            start = before
    return original[:start] + original[end:]


def plugin_entry_path(paths: Paths) -> str:
    """Absolute, version-stable path of the installed plugin entry module."""
    return str((paths.tools_dir / PLUGIN_ENTRY_FILE).resolve())


def drop_string_token(original: str, token: str) -> str:
    """Remove one exact JSON string token and a single adjacent comma.

    Shared by the skills and plugin deregistration paths. The comma hunt
    skips whitespace and comments, so a comment beside the token never
    hides its separating comma. The caller guarantees the token occurs
    exactly once in the original text.
    """
    start = original.index(token)
    end = start + len(token)
    start, end = span_without_adjacent_comma(original, start, end)
    return original[:start] + original[end:]


def drop_empty_instructions_property(original: str) -> str:
    """Remove an empty instructions property without changing other text."""
    token = json.dumps("instructions")
    if original.count(token) != 1:
        raise InstallerError("cannot safely remove the empty instructions property")
    key_start = original.index(token)
    array_end = jsonc_array_end(original, "instructions")
    if array_end is None:
        raise InstallerError("cannot locate the instructions array")
    start = key_start
    end = array_end + 1
    before = original[:key_start]
    previous = len(before.rstrip()) - 1
    if previous >= 0 and before[previous] == ",":
        start = previous
        if original[:start].endswith("\n") and original[end:end + 1] == "\n":
            end += 1
    else:
        after = end
        while after < len(original) and original[after].isspace():
            after += 1
        if after < len(original) and original[after] == ",":
            end = after + 1
    return original[:start] + original[end:]


def is_plugin_entry(candidate: object, entry_path: str) -> bool:
    """Report whether one parsed plugin-array entry registers entry_path.

    The managed entry takes either form: the bare path string, or the tuple
    form ``[entry_path, options]`` whose options carry operator-owned
    configuration such as the session opener (CD-0182). The path as element
    zero claims the slot either way, so an upgrade never adds a duplicate
    bare entry beside a tuple that keeps its options.
    """
    if isinstance(candidate, str):
        return candidate == entry_path
    return isinstance(candidate, list) and len(candidate) >= 1 and candidate[0] == entry_path


def plan_plugin_entry(text: str, entry_path: str) -> str:
    """Ensure the plugin entry module path is registered in the host plugin array.

    The plugin path is version-stable, so this is an idempotent ensure-present:
    it adds the path when absent and leaves an existing registration — bare or
    tuple form, options included — untouched. Raises InstallerError carrying
    the exact manual entry when the existing plugin configuration cannot be
    edited safely.
    """
    parsed = jsonc_data(text)
    if not isinstance(parsed, dict):
        raise InstallerError(
            f"OpenCode config is not an object; add {json.dumps(entry_path)} to its plugin array manually"
        )
    token = json.dumps(entry_path)
    plugin = parsed.get("plugin")
    if plugin is None:
        end = outer_object_end(text)
        before = text[:end]
        separator = "" if before.rstrip().endswith("{") else ","
        addition = f'{separator}\n  "plugin": [\n    {token}\n  ]\n'
        return text[:end] + addition + text[end:]
    if not isinstance(plugin, list):
        raise InstallerError(
            f"OpenCode config plugin value is not an array; add {token} to it manually"
        )
    if any(is_plugin_entry(entry, entry_path) for entry in plugin):
        return text
    matches = list(re.finditer(r'"plugin"\s*:\s*\[', text))
    if len(matches) != 1:
        raise InstallerError(
            f"cannot locate the plugin array in the OpenCode config; add {token} to it manually"
        )
    insert_at = matches[0].end()
    insertion = f"\n    {token}\n  " if len(plugin) == 0 else f"\n    {token},"
    return text[:insert_at] + insertion + text[insert_at:]


def _line_comment_start(line: str) -> int:
    """Return the offset of the first ``//`` outside strings in line, or -1."""
    index = 0
    in_string = False
    while index < len(line):
        char = line[index]
        if in_string:
            if char == "\\":
                index += 1
            elif char == '"':
                in_string = False
        elif char == '"':
            in_string = True
        elif char == "/" and line.startswith("//", index):
            return index
        index += 1
    return -1


def _skip_ws_comments_right(text: str, pos: int) -> int:
    """Return the first significant offset at or above pos.

    Whitespace, ``//`` line comments, and ``/*`` block comments carry no
    structure, so a comma hunt walks over them.
    """
    index = pos
    length = len(text)
    while index < length:
        char = text[index]
        if char.isspace():
            index += 1
        elif text.startswith("//", index):
            newline = text.find("\n", index)
            index = length if newline < 0 else newline
        elif text.startswith("/*", index):
            terminator = text.find("*/", index + 2)
            index = length if terminator < 0 else terminator + 2
        else:
            break
    return index


def _skip_ws_comments_left(text: str, pos: int) -> int:
    """Return the first significant offset at or below pos, or -1.

    Walks backwards over whitespace and comments. A line that ends in a
    ``//`` comment is skipped to the comment start, so a comma hidden under
    it is still found; anything else significant stops the walk, including
    strings, whose quotes the walk never crosses.
    """
    index = pos
    while index >= 0:
        char = text[index]
        if char.isspace():
            if char == "\n":
                line_start = text.rfind("\n", 0, index) + 1
                comment = _line_comment_start(text[line_start:index])
                if comment >= 0:
                    index = line_start + comment - 1
                    continue
            index -= 1
        elif index >= 1 and char == "/" and text[index - 1] == "*":
            opener = text.rfind("/*", 0, index)
            if opener < 0:
                break
            index = opener - 1
        elif index >= 1 and char == "/" and text[index - 1] == "/":
            index -= 2
        else:
            break
    return index


def span_without_adjacent_comma(original: str, start: int, end: int) -> tuple[int, int]:
    """Grow one removal span by exactly one adjacent comma, either side.

    The hunt skips whitespace and comments, so a comment between the removed
    span and its separating comma never hides that comma. When no comma is
    adjacent the skipped text stays and the span is unchanged.
    """
    after = _skip_ws_comments_right(original, end)
    if after < len(original) and original[after] == ",":
        return start, after + 1
    before = _skip_ws_comments_left(original, start - 1)
    if before >= 0 and original[before] == ",":
        return before, end
    return start, end


def jsonc_array_span_around(original: str, token_start: int) -> tuple[int, int]:
    """Return the (start, end) offsets of the JSON array open at token_start.

    The walk parses the JSONC structure — strings, ``//`` line comments, and
    ``/*`` block comments are skipped — so a bracket inside a comment or an
    option string never opens or closes a span. The token must occur once and
    outside comments; the parsed document the caller checked guarantees both.
    The innermost array open at the token is the element span the caller
    removes. A token position the walk never reaches, an unterminated span,
    or a non-array element refuses instead of guessing.
    """
    refuse = "cannot safely remove the managed plugin entry from the OpenCode config"
    stack: list[int] = []
    close_at: dict[int, int] = {}
    open_at_token = -1
    index = 0
    length = len(original)
    while index < length:
        if index == token_start and stack:
            open_at_token = stack[-1]
        character = original[index]
        if character == '"':
            index += 1
            while index < length:
                if original[index] == "\\":
                    index += 2
                    continue
                if original[index] == '"':
                    break
                index += 1
            index += 1
            continue
        if character == "/" and index + 1 < length and original[index + 1] == "/":
            newline = original.find("\n", index)
            index = length if newline < 0 else newline
            continue
        if character == "/" and index + 1 < length and original[index + 1] == "*":
            terminator = original.find("*/", index + 2)
            index = length if terminator < 0 else terminator + 2
            continue
        if character in "[{":
            stack.append(index)
        elif character in "]}":
            if not stack:
                raise InstallerError(refuse)
            close_at[stack.pop()] = index
        index += 1
    if open_at_token < 0 or original[open_at_token] != "[" or open_at_token not in close_at:
        raise InstallerError(refuse)
    return open_at_token, close_at[open_at_token] + 1


def drop_json_array_span(original: str, token: str) -> str:
    """Remove the whole JSON array that contains token, plus one adjacent comma.

    The tuple form of the plugin entry carries operator options beside the
    managed path, so deregistration removes the parsed element span — located
    by walking the JSONC structure, so a bracket inside a comment or an option
    string never ends the scan — rather than the path token alone, which would
    strand an option fragment. The caller guarantees the token occurs exactly
    once in the original text.
    """
    open_index, end = jsonc_array_span_around(original, original.index(token))
    start, end = span_without_adjacent_comma(original, open_index, end)
    return original[:start] + original[end:]


def remove_plugin_entry(text: str, entry_path: str) -> str:
    """Remove the managed plugin entry registration. No-op when it is absent.

    A tuple-form registration removes whole, options included; the bare form
    removes as one string token.
    """
    parsed = jsonc_data(text)
    if not isinstance(parsed, dict):
        return text
    plugin = parsed.get("plugin")
    if not isinstance(plugin, list) or not any(is_plugin_entry(entry, entry_path) for entry in plugin):
        return text
    token = json.dumps(entry_path)
    if text.count(token) != 1:
        raise InstallerError("cannot safely remove the managed plugin entry from the OpenCode config")
    tupled = any(isinstance(entry, list) and len(entry) >= 1 and entry[0] == entry_path for entry in plugin)
    if tupled:
        return drop_json_array_span(text, token)
    return drop_string_token(text, token)


def safe_relative_target(root: Path, relative: str, label: str, allow_final_symlink: bool = False) -> Path:
    candidate = Path(relative)
    if candidate.is_absolute() or not relative or ".." in candidate.parts:
        raise InstallerError(f"refusing {label} target {relative!r}: path escapes the installation root")
    if root.is_symlink():
        raise InstallerError(f"refusing {label} root {root}: it is a symlink")
    resolved_root = root.resolve(strict=False)
    target = root / candidate
    if allow_final_symlink and target.is_symlink():
        resolved_candidate = target.parent.resolve(strict=False) / target.name
    else:
        resolved_candidate = target.resolve(strict=False)
    if resolved_candidate != resolved_root and resolved_root not in resolved_candidate.parents:
        raise InstallerError(f"refusing {label} target {relative!r}: path escapes the installation root")
    current = root
    for index, part in enumerate(candidate.parts):
        current /= part
        if current.is_symlink() and not (allow_final_symlink and index == len(candidate.parts) - 1):
            raise InstallerError(f"refusing {label} target {relative!r}: symlinked target")
    return target


def validate_manifest(paths: Paths, manifest: dict[str, object]) -> None:
    required = {
        "managed_by",
        "version",
        "version_files",
        "adapter_files",
        "agent_files",
        "skill_path",
        "stable_root",
        "launcher_target",
        "config_path",
    }
    # Older manifests have no retained release or credential ownership record.
    # Both fields remain optional on read so those manifests can be upgraded safely.
    optional = {"credential_directory", "retained_releases"}
    if not required <= set(manifest) <= required | optional or manifest.get("managed_by") != "concord-installer-v1":
        missing = required - set(manifest)
        if manifest.get("managed_by") == "concord-installer-v1" and missing and missing <= {"agent_files", "stable_root"}:
            raise InstallerError(
                "installed by an older installer that records no central agents or stable root; "
                "run uninstall, then install, to upgrade"
            )
        raise InstallerError("refusing installer manifest with unknown or missing fields")
    version = manifest.get("version")
    if not isinstance(version, str) or not VERSION_RE.fullmatch(version):
        raise InstallerError("installer manifest has an invalid version")
    if paths.data_root.is_symlink():
        raise InstallerError(f"refusing installer data root {paths.data_root}: it is a symlink")
    version_root = safe_relative_target(paths.data_root, version, "version")
    if version_root.is_symlink():
        raise InstallerError(f"refusing installer version root {version_root}: it is a symlink")
    if "retained_releases" in manifest:
        validate_release_records(manifest["retained_releases"], paths, "installer manifest")
        if version in manifest["retained_releases"]:
            raise InstallerError("installer manifest retains the installed version")

    version_files = manifest.get("version_files")
    if not isinstance(version_files, dict) or not version_files:
        raise InstallerError("installer manifest has invalid version file records")
    allowed_fixed = {"bin/concord", FENCE_PROTOCOL_MARKER_NAME, *(f"adapter/opencode/{name}" for name in ADAPTER_FILES)}
    for relative, digest in version_files.items():
        if not isinstance(relative, str) or not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
            raise InstallerError("installer manifest has invalid version file records")
        safe_relative_target(version_root, relative, "version file")
        if (
            relative not in allowed_fixed
            and not relative.startswith("skills/")
            and not relative.startswith("instructions/")
            and not relative.startswith("agents/")
        ):
            raise InstallerError(f"refusing unknown managed version file {relative!r}")
    # A prior installation can legitimately predate a later adapter file. The
    # manifest records the files that its installer managed, not the files this
    # installer now ships. Keep the core binary mandatory, while the loop above
    # still rejects every unknown or malformed recorded path.
    if "bin/concord" not in version_files:
        raise InstallerError("installer manifest omits a required managed version file")

    # The manifest records what a previous install managed, which is a subset of
    # what this version installs whenever an adapter file has since been added.
    # Requiring equality would make adding one an upgrade-breaking change. Keys
    # outside ADAPTER_FILES stay refused, so an unknown or traversing key cannot
    # reach a delete.
    adapter_files = manifest.get("adapter_files")
    if (
        not isinstance(adapter_files, dict)
        or not set(adapter_files).issubset(ADAPTER_FILES)
        or not all(isinstance(value, str) and SHA256_RE.fullmatch(value) for value in adapter_files.values())
    ):
        raise InstallerError("installer manifest has invalid adapter file records")
    for name in ADAPTER_FILES:
        safe_relative_target(paths.tools_dir, name, "adapter")

    # Central agent definitions: filenames only (the path inside the version
    # tree lives in version_files). The shape mirrors adapter_files: a dict of
    # filename -> sha256, with the filename constrained so a manifest attack
    # cannot reach a delete outside paths.agents_dir.
    agent_files = manifest.get("agent_files")
    if (
        not isinstance(agent_files, dict)
        or not all(isinstance(name, str) for name in agent_files)
        or not all(isinstance(value, str) and SHA256_RE.fullmatch(value) for value in agent_files.values())
    ):
        raise InstallerError("installer manifest has invalid agent file records")
    for name in agent_files:
        if not name.startswith("concord-") or not name.endswith(".md"):
            raise InstallerError(f"installer manifest has invalid agent file name {name!r}")
        safe_relative_target(paths.agents_dir, name, "agent file")

    expected_skill = paths.stable_root / "skills"
    legacy_skill = paths.data_root / version / "skills"
    skill_path = manifest.get("skill_path")
    if not isinstance(skill_path, str) or skill_path not in {str(expected_skill), str(legacy_skill)}:
        raise InstallerError("installer manifest has a redirected skills path")
    # Accept the versioned path only when reading a pre-stable-path manifest.
    # The next upgrade or repair writes the stable path back to the manifest.
    safe_relative_target(paths.data_root, f"{version}/skills", "skills")

    # The stable root is the path projects embed so they survive upgrades; the
    # value here is the symlink's location (not its target), and any drift is a
    # redirect that warrants refusal. An unmanaged directory or regular file at
    # that path must never be clobbered into a symlink.
    stable_root_value = manifest.get("stable_root")
    if not isinstance(stable_root_value, str) or stable_root_value != str(paths.stable_root):
        raise InstallerError("installer manifest has a redirected stable root")
    if paths.stable_root.exists() and not paths.stable_root.is_symlink():
        raise InstallerError(f"refusing {paths.stable_root}: not a symlink")
    if paths.stable_root.is_symlink():
        link_target = Path(os.readlink(paths.stable_root))
        expected_target = paths.data_root / version
        if link_target != expected_target:
            raise InstallerError(f"installer stable root points outside its expected version target: {paths.stable_root}")

    expected_launcher = paths.data_root / version / "bin" / "concord"
    if manifest.get("launcher_target") != str(expected_launcher.resolve(strict=False)):
        raise InstallerError("installer manifest has a redirected launcher target")
    safe_relative_target(paths.data_root, f"{version}/bin/concord", "launcher")

    config_path = manifest.get("config_path")
    if not isinstance(config_path, str) or config_path != str(paths.config_file.resolve(strict=False)):
        raise InstallerError("installer manifest has a redirected OpenCode config path")
    safe_relative_target(paths.config_file.parent, paths.config_file.name, "OpenCode config")
    if "credential_directory" in manifest:
        credential_directory = manifest.get("credential_directory")
        if not isinstance(credential_directory, str) or credential_directory != str(paths.data_home / "keyrings"):
            raise InstallerError("installer manifest has a redirected Secret Service credential directory")


def load_manifest(paths: Paths) -> dict[str, object] | None:
    manifest_path = paths.data_root / MANIFEST_NAME
    if paths.data_root.is_symlink() or manifest_path.is_symlink():
        raise InstallerError(f"refusing installer manifest {manifest_path}: symlinked target")
    if not manifest_path.exists():
        return None
    try:
        value = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise InstallerError(f"cannot read installer manifest {manifest_path}: {error}") from error
    if not isinstance(value, dict):
        raise InstallerError(f"refusing unrecognized installer manifest {manifest_path}")
    validate_manifest(paths, value)
    return value


def command_status(command: str) -> str | None:
    return shutil.which(command)


def secret_service_status() -> tuple[bool, str]:
    busctl = command_status("busctl")
    if busctl:
        result = subprocess.run([busctl, "--user", "--list"], capture_output=True, text=True)
        if result.returncode == 0 and "org.freedesktop.secrets" in result.stdout:
            return True, "org.freedesktop.secrets is available"
        return False, "org.freedesktop.secrets is not available on the user D-Bus session"
    gdbus = command_status("gdbus")
    if gdbus:
        result = subprocess.run(
            [gdbus, "introspect", "--session", "--dest", "org.freedesktop.secrets", "--object-path", "/org/freedesktop/secrets"],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            return True, "org.freedesktop.secrets is available"
        return False, "org.freedesktop.secrets is not available on the user D-Bus session"
    return False, "neither busctl nor gdbus is available to verify org.freedesktop.secrets"


def run_checked(arguments: list[str], *, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(arguments, capture_output=True, text=True, env=env)
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit {result.returncode}"
        raise InstallerError(f"command failed: {arguments[0]}: {detail}")
    return result


def busctl_words(arguments: list[str]) -> list[str]:
    busctl = command_status("busctl")
    if busctl is None:
        raise InstallerError("busctl is required for noninteractive Secret Service setup")
    return shlex.split(run_checked([busctl, "--user", *arguments]).stdout.strip())


def secret_service_alias(alias: str) -> str | None:
    words = busctl_words(
        ["call", SECRET_SERVICE_DESTINATION, SECRET_SERVICE_PATH, SECRET_SERVICE_INTERFACE, "ReadAlias", "s", alias]
    )
    if len(words) != 2 or words[0] != "o":
        raise InstallerError(f"Secret Service returned an invalid {alias!r} alias result")
    return None if words[1] == "/" else words[1]


def secret_service_property(path: str, interface: str, name: str) -> list[str]:
    words = busctl_words(["get-property", SECRET_SERVICE_DESTINATION, path, interface, name])
    if not words:
        raise InstallerError(f"Secret Service returned no {name} property")
    return words


def secret_service_collections() -> list[str]:
    words = secret_service_property(SECRET_SERVICE_PATH, SECRET_SERVICE_INTERFACE, "Collections")
    if len(words) < 2 or words[0] != "ao" or not words[1].isdigit():
        raise InstallerError("Secret Service returned an invalid Collections property")
    count = int(words[1])
    if len(words[2:]) != count:
        raise InstallerError("Secret Service returned an inconsistent Collections property")
    return words[2:]


def persistent_secret_service_collections() -> list[str]:
    return [path for path in secret_service_collections() if path != SECRET_SERVICE_SESSION_COLLECTION]


def secret_service_collection_locked(path: str) -> bool:
    words = secret_service_property(path, "org.freedesktop.Secret.Collection", "Locked")
    if words == ["b", "true"]:
        return True
    if words == ["b", "false"]:
        return False
    raise InstallerError("Secret Service returned an invalid Locked property")


def secret_service_collection_has_items(path: str) -> bool:
    words = secret_service_property(path, "org.freedesktop.Secret.Collection", "Items")
    if len(words) < 2 or words[0] != "ao" or not words[1].isdigit():
        raise InstallerError("Secret Service returned an invalid Items property")
    count = int(words[1])
    if len(words[2:]) != count:
        raise InstallerError("Secret Service returned an inconsistent Items property")
    return count != 0


def secret_service_owner_pid() -> int:
    busctl = command_status("busctl")
    if busctl is None:
        raise InstallerError("busctl is required for noninteractive Secret Service setup")
    result = run_checked([busctl, "--user", "status", SECRET_SERVICE_DESTINATION])
    for line in result.stdout.splitlines():
        if line.startswith("PID=") and line[4:].isdigit():
            return int(line[4:])
    return 0


def credential_dropin_text(daemon: str) -> str:
    if any(character.isspace() for character in daemon):
        raise InstallerError(f"gnome-keyring-daemon path contains whitespace: {daemon}")
    return f"""# Unlock the login keyring when the GNOME Keyring daemon starts.
[Service]
ExecStart=
ExecStart={daemon} --unlock --foreground --components=pkcs11,secrets --control-directory=%t/keyring
StandardInput=data
StandardInputData=Cg==
"""


def credential_service_text(daemon: str) -> str:
    if any(character.isspace() for character in daemon):
        raise InstallerError(f"gnome-keyring-daemon path contains whitespace: {daemon}")
    return f'''[D-BUS Service]
Name={SECRET_SERVICE_DESTINATION}
SystemdService=gnome-keyring-daemon.service
Exec=/bin/sh -c 'printf "\\n" | {daemon} --unlock --foreground --components=pkcs11,secrets --control-directory=$XDG_RUNTIME_DIR/keyring'
'''


def legacy_credential_unit_text(daemon: str) -> str:
    if any(character.isspace() for character in daemon):
        raise InstallerError(f"gnome-keyring-daemon path contains whitespace: {daemon}")
    return f"""[Unit]
Description=Unlock the user Secret Service collection for Concord
Requires=gnome-keyring-daemon.service
After=gnome-keyring-daemon.service
PartOf=gnome-keyring-daemon.service

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'printf \"\\n\" | {daemon} --unlock --control-directory=%t/keyring'

[Install]
WantedBy=default.target gnome-keyring-daemon.service
"""


def verify_credential_permissions(keyrings: Path) -> None:
    if keyrings.is_symlink() or not keyrings.is_dir():
        raise InstallerError(f"Secret Service credential directory is missing or unsafe: {keyrings}")
    for path in (keyrings, *keyrings.iterdir()):
        if path.is_symlink() or path.stat().st_uid != os.getuid():
            raise InstallerError(f"Secret Service credential path has unsafe ownership: {path}")
        if stat.S_IMODE(path.stat().st_mode) & 0o077:
            raise InstallerError(f"Secret Service credential path grants group or other access: {path}")


def initialize_login_collection(paths: Paths, daemon: str) -> bool:
    keyrings = paths.data_home / "keyrings"
    if keyrings.is_symlink():
        raise InstallerError(f"refusing symlinked Secret Service credential directory {keyrings}")
    if keyrings.exists() and any(keyrings.iterdir()):
        allowed = {"login.keyring", "user.keystore"}
        names = {path.name for path in keyrings.iterdir()}
        if not names <= allowed or "login.keyring" not in names:
            raise InstallerError(
                f"Secret Service has no login alias but {keyrings} contains unknown state; refusing to replace it"
            )
        verify_credential_permissions(keyrings)
        return False
    dbus_run_session = command_status("dbus-run-session")
    if dbus_run_session is None:
        raise InstallerError("dbus-run-session is required to initialize the headless Secret Service collection")
    helper = (
        "import subprocess,sys;"
        "daemon,control=sys.argv[1:3];"
        "subprocess.run([daemon,'--login','--control-directory='+control],input=b'\\n',check=True,"
        "stdout=subprocess.DEVNULL,stderr=subprocess.PIPE);"
        "subprocess.run([daemon,'--start','--components=secrets','--control-directory='+control],check=True,"
        "stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)"
    )
    with tempfile.TemporaryDirectory(prefix="concord-keyring-") as runtime:
        os.chmod(runtime, 0o700)
        environment = os.environ.copy()
        for name in ("DBUS_SESSION_BUS_ADDRESS", "DBUS_STARTER_ADDRESS", "DBUS_STARTER_BUS_TYPE", "GNOME_KEYRING_CONTROL"):
            environment.pop(name, None)
        environment["HOME"] = str(paths.home)
        environment["XDG_DATA_HOME"] = str(paths.data_home)
        environment["XDG_RUNTIME_DIR"] = runtime
        run_checked(
            [dbus_run_session, "--", sys.executable, "-c", helper, daemon, str(Path(runtime) / "keyring")],
            env=environment,
        )
    verify_credential_permissions(keyrings)
    return True


def stop_unmanaged_secret_service(systemctl: str, daemon: str) -> None:
    run_checked([systemctl, "--user", "start", "gnome-keyring-daemon.socket"])
    owner = secret_service_owner_pid()
    main_result = run_checked(
        [systemctl, "--user", "show", "--property=MainPID", "--value", "gnome-keyring-daemon.service"]
    )
    main_pid = int(main_result.stdout.strip() or "0")
    if owner and owner != main_pid:
        for collection in persistent_secret_service_collections():
            if secret_service_collection_has_items(collection):
                raise InstallerError(
                    "the active non-systemd Secret Service contains items in a persistent collection; "
                    "refusing to stop it during setup. Remove or migrate those credentials, then re-run install or repair"
                )
        executable = (Path("/proc") / str(owner) / "exe").resolve()
        if executable.name != Path(daemon).name or executable.stat().st_uid != os.getuid():
            raise InstallerError("the active Secret Service is not the current user's gnome-keyring-daemon")
        descriptor = os.pidfd_open(owner)
        try:
            os.kill(owner, signal.SIGTERM)
            ready, _, _ = select.select([descriptor], [], [], 5)
            if not ready:
                raise InstallerError("the prior Secret Service did not stop within 5 seconds")
        finally:
            os.close(descriptor)
    run_checked([systemctl, "--user", "restart", "gnome-keyring-daemon.service"])


def remove_legacy_credential_unit(paths: Paths, systemctl: str) -> None:
    unit = paths.credential_unit
    if not unit.exists() and not unit.is_symlink():
        return
    daemon = command_status("gnome-keyring-daemon")
    if daemon is None:
        raise InstallerError("gnome-keyring-daemon is required to validate the legacy credential unit")
    if (
        unit.is_symlink()
        or not unit.is_file()
        or unit.read_text(encoding="utf-8") != legacy_credential_unit_text(daemon)
    ):
        raise InstallerError(f"refusing to remove user-authored legacy credential unit {unit}")
    run_checked([systemctl, "--user", "disable", "--now", CREDENTIAL_UNIT_NAME])
    unit.unlink()
    fsync_directory(paths.systemd_user_dir)


def legacy_credential_unit_owned(paths: Paths, daemon: str) -> bool:
    unit = paths.credential_unit
    if not unit.exists() and not unit.is_symlink():
        return False
    if (
        unit.is_symlink()
        or not unit.is_file()
        or unit.read_text(encoding="utf-8") != legacy_credential_unit_text(daemon)
    ):
        raise InstallerError(f"refusing to remove user-authored legacy credential unit {unit}")
    return True


def rollback_created_credential_dropin(paths: Paths, systemctl: str) -> None:
    if paths.credential_dropin.exists() or paths.credential_dropin.is_symlink():
        paths.credential_dropin.unlink()
        fsync_directory(paths.credential_dropin.parent)
    run_checked([systemctl, "--user", "daemon-reload"])
    run_checked([systemctl, "--user", "restart", "gnome-keyring-daemon.service"])


def rollback_created_credential_files(
    paths: Paths, systemctl: str, created_dropin: bool, created_service: bool
) -> None:
    if created_service and (paths.credential_service.exists() or paths.credential_service.is_symlink()):
        paths.credential_service.unlink()
        fsync_directory(paths.credential_service.parent)
    if created_dropin:
        rollback_created_credential_dropin(paths, systemctl)


def ensure_secret_service_ready(paths: Paths, old_manifest: dict[str, object] | None) -> bool:
    daemon = command_status("gnome-keyring-daemon")
    systemctl = command_status("systemctl")
    if daemon is None or systemctl is None:
        raise InstallerError("gnome-keyring-daemon and systemctl are required for credential setup")
    legacy_owned = legacy_credential_unit_owned(paths, daemon)
    login = secret_service_alias("login")
    keyrings = paths.data_home / "keyrings"
    manifest_owns_keyrings = old_manifest is not None and old_manifest.get("credential_directory") == str(keyrings)
    if login is not None and not secret_service_collection_locked(login) and not manifest_owns_keyrings and not legacy_owned:
        # A compatible provider such as KeePassXC can own Secret Service
        # without GNOME Keyring files. It already satisfies the contract, so
        # do not start a competing provider or install an unrelated unit.
        remove_legacy_credential_unit(paths, systemctl)
        return False
    if login is not None and (manifest_owns_keyrings or legacy_owned) and not keyrings.exists():
        raise InstallerError(
            f"Secret Service credential directory is missing: {keyrings}; "
            "restore the Concord keyring, then re-run install or repair"
        )
    initialized = False
    if login is None:
        for collection in persistent_secret_service_collections():
            if secret_service_collection_has_items(collection):
                raise InstallerError(
                    f"Secret Service persistent collection {collection} contains items; "
                    "refusing headless collection initialization. Remove or migrate those credentials, "
                    "then re-run install or repair"
                )
        initialized = initialize_login_collection(paths, daemon)
    if not initialized and not manifest_owns_keyrings and not legacy_owned:
        raise InstallerError(
            f"Secret Service login collection at {keyrings} is not owned by Concord; refusing to write the unlock drop-in "
            "or restart the daemon. Unlock it with the desktop login, then re-run install or repair"
        )
    if keyrings.exists():
        verify_credential_permissions(keyrings)
    dropin_text = credential_dropin_text(daemon)
    service_text = credential_service_text(daemon)
    created_dropin = False
    created_service = False
    try:
        if paths.credential_dropin.exists() or paths.credential_dropin.is_symlink():
            if (
                not paths.credential_dropin.is_file()
                or paths.credential_dropin.read_text(encoding="utf-8") != dropin_text
            ):
                raise InstallerError(
                    f"refusing to overwrite user-authored credential drop-in {paths.credential_dropin}"
                )
        else:
            created_dropin = True
            write_atomic(paths.credential_dropin, dropin_text.encode("utf-8"), 0o644)
        if paths.credential_service.exists() or paths.credential_service.is_symlink():
            if (
                not paths.credential_service.is_file()
                or paths.credential_service.read_text(encoding="utf-8") != service_text
            ):
                raise InstallerError(
                    f"refusing to overwrite user-authored Secret Service activation file {paths.credential_service}"
                )
        else:
            created_service = True
            write_atomic(paths.credential_service, service_text.encode("utf-8"), 0o644)

        remove_legacy_credential_unit(paths, systemctl)
        run_checked([systemctl, "--user", "daemon-reload"])
        if login is None:
            stop_unmanaged_secret_service(systemctl, daemon)
        else:
            run_checked([systemctl, "--user", "restart", "gnome-keyring-daemon.service"])
        login = secret_service_alias("login")
        if login is None or secret_service_collection_locked(login):
            raise InstallerError(
                "Secret Service login collection remained locked after the gnome-keyring-daemon restart; "
                "the unlock drop-in did not unlock it. Check the user service and re-run install or repair"
            )
    except Exception:
        try:
            rollback_created_credential_files(paths, systemctl, created_dropin, created_service)
        except Exception:
            pass
        raise
    return True



def unmanaged_manifest_note(old_manifest: dict[str, object] | None, paths: Paths) -> str:
    """Name the manifest a preflight refusal consulted, so an install run from
    a redirected data root explains why it sees no managed files (#648)."""
    if old_manifest:
        return ""
    return f" (no install manifest at {paths.data_root / MANIFEST_NAME})"

def preflight(paths: Paths, version: str, old_manifest: dict[str, object] | None, staged_adapters: dict[str, str] | None = None) -> ConfigPlan:
    failures: list[str] = []
    failures.extend(
        f"refusing symlinked managed path {managed_parent}"
        for managed_parent in (
            paths.data_root,
            paths.tools_dir,
            paths.agents_dir,
            paths.systemd_user_dir,
            paths.credential_dropin.parent,
            paths.credential_service.parent,
            paths.bin_dir,
            paths.config_file.parent,
        )
        if managed_parent.is_symlink()
    )
    if paths.stable_root.exists() and not paths.stable_root.is_symlink():
        failures.append(f"refusing {paths.stable_root}: not a symlink")
    system = platform.system()
    machine = platform.machine().lower()
    if system != "Linux":
        failures.append(f"platform is {system}; Linux amd64 is required")
    if machine not in {"x86_64", "amd64"}:
        failures.append(f"architecture is {platform.machine()}; Linux amd64 is required")
    for command, consequence in (
        ("git", "host resolution will not work without Git"),
        ("opencode", "the global Concord custom tool cannot be used without OpenCode"),
        ("secret-tool", "worker evidence signing fails closed because Concord cannot read the client signing key"),
        ("gnome-keyring-daemon", "the Secret Service provider holding the client signing key is missing"),
        ("busctl", "noninteractive Secret Service inspection is unavailable"),
        ("dbus-run-session", "the headless login collection cannot be initialized safely"),
        ("systemctl", "the user Secret Service cannot be restarted or unlocked after login"),
    ):
        if command_status(command) is None:
            failures.append(f"missing command {command}; {consequence}")
    service_ok, service_reason = secret_service_status()
    if not service_ok:
        failures.append(f"Secret Service unavailable: {service_reason}; worker evidence signing will not work")
    if str(paths.bin_dir) not in os.environ.get("PATH", "").split(os.pathsep):
        failures.append(
            f"{paths.bin_dir} is not on PATH; the adapter's default concord command will not resolve. "
            f"Add {paths.bin_dir} to PATH and re-run"
        )
    target_root = paths.data_root / version
    if target_root.exists() and old_manifest is None:
        failures.append(f"refusing existing unmanaged installation path {target_root}")
    if paths.launcher.exists() or paths.launcher.is_symlink():
        expected_launcher = old_manifest.get("launcher_target") if old_manifest else None
        if not old_manifest or not paths.launcher.is_symlink() or os.readlink(paths.launcher) != expected_launcher:
            failures.append(f"refusing to overwrite user-authored launcher {paths.launcher}")
    daemon = command_status("gnome-keyring-daemon")
    if daemon and (paths.credential_unit.exists() or paths.credential_unit.is_symlink()):
        try:
            legacy_credential_unit_owned(paths, daemon)
        except InstallerError as error:
            failures.append(str(error))
    if daemon and (paths.credential_dropin.exists() or paths.credential_dropin.is_symlink()):
        expected_dropin = credential_dropin_text(daemon)
        if (
            paths.credential_dropin.is_symlink()
            or not paths.credential_dropin.is_file()
            or paths.credential_dropin.read_text(encoding="utf-8") != expected_dropin
        ):
            failures.append(f"refusing to overwrite user-authored credential drop-in {paths.credential_dropin}")
    if daemon and (paths.credential_service.exists() or paths.credential_service.is_symlink()):
        expected_service = credential_service_text(daemon)
        if (
            paths.credential_service.is_symlink()
            or not paths.credential_service.is_file()
            or paths.credential_service.read_text(encoding="utf-8") != expected_service
        ):
            failures.append(f"refusing to overwrite user-authored Secret Service activation file {paths.credential_service}")
    adapter_records = managed_adapter_records(old_manifest)
    manifest_note = unmanaged_manifest_note(old_manifest, paths)
    for name in ADAPTER_FILES:
        destination = paths.tools_dir / name
        if (destination.exists() or destination.is_symlink()) and (
            not old_manifest or name not in adapter_records
        ):
            # Repair accepts a file an incomplete deployment placed that
            # the stale manifest never recorded, when its content is the
            # release's own.
                if (
                    staged_adapters is not None
                    and destination.is_file()
                    and not destination.is_symlink()
                    and sha256(destination) == staged_adapters.get(name)
                ):
                    continue
                failures.append(f"refusing to overwrite user-authored adapter file {destination}{manifest_note}")
    agent_records = managed_agent_records(old_manifest)
    for name in agent_records:
        destination = paths.agents_dir / name
        if destination.exists() or destination.is_symlink():
            expected = agent_records[name]
            if not destination.is_file() or sha256(destination) != expected:
                failures.append(f"refusing to overwrite user-authored agent file {destination}")
    if old_manifest:
        old_version = old_manifest.get("version")
        if not isinstance(old_version, str):
            failures.append("existing installer manifest has no valid version")
    old_skill = old_manifest.get("skill_path") if old_manifest else None
    if old_skill is not None and not isinstance(old_skill, str):
        failures.append("existing installer manifest has no valid skills path")
    try:
        config_plan = plan_config(paths.config_file, stable_skill_path(paths), old_skill)
        plugin_text = plan_plugin_entry(config_plan.text, plugin_entry_path(paths))
        if plugin_text != config_plan.text:
            config_plan = ConfigPlan(config_plan.path, plugin_text, True, config_plan.managed_fragment)
    except InstallerError as error:
        failures.append(str(error))
        config_plan = ConfigPlan(paths.config_file, "", False, None)
    if failures:
        raise InstallerError("Preflight refused installation:\n- " + "\n- ".join(failures))
    return config_plan


def parse_checksums(path: Path) -> dict[str, str]:
    checksums: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        parts = line.split(maxsplit=1)
        if len(parts) != 2 or not re.fullmatch(r"[0-9a-fA-F]{64}", parts[0]):
            continue
        name = parts[1].lstrip("*")
        checksums[name] = parts[0].lower()
    return checksums


def download(url: str, destination: Path) -> None:
    try:
        with urllib.request.urlopen(url, timeout=30) as response, destination.open("wb") as stream:
            shutil.copyfileobj(response, stream)
    except Exception as error:  # urllib has several platform-specific errors.
        raise InstallerError(f"could not download {url}: {error}") from error


def artifact_file(artifact_dir: Path | None, base_url: str, name: str, directory: Path) -> Path:
    destination = directory / name
    if artifact_dir:
        source = artifact_dir / name
        if not source.exists():
            raise InstallerError(f"published artifact is missing {source}")
        shutil.copyfile(source, destination)
    else:
        download(urllib.parse.urljoin(base_url.rstrip("/") + "/", name), destination)
    return destination


def extract_verified_artifact(version: str, artifact_dir: Path | None, base_url: str, workspace: Path) -> tuple[Path, dict[str, str]]:
    prefix = f"concord-{version}"
    checksum_path = artifact_file(artifact_dir, base_url, f"{prefix}.sha256", workspace)
    checksums = parse_checksums(checksum_path)
    archive_name = f"{prefix}.tar.gz"
    archive = artifact_file(artifact_dir, base_url, archive_name, workspace)
    expected = checksums.get(archive_name)
    if expected is None:
        raise InstallerError(f"checksum file does not contain {archive_name}")
    actual = sha256(archive)
    if actual != expected:
        raise InstallerError(
            f"checksum mismatch for {archive_name}: expected {expected}, got {actual}; "
            "nothing was installed"
        )
    extracted = workspace / "extracted"
    extracted.mkdir()
    try:
        with tarfile.open(archive, "r:gz") as bundle:
            for member in bundle.getmembers():
                member_path = (extracted / member.name).resolve()
                if not str(member_path).startswith(str(extracted.resolve()) + os.sep):
                    raise InstallerError(f"release archive contains unsafe path {member.name!r}")
                if member.issym() or member.islnk():
                    raise InstallerError(f"release archive contains unsafe link {member.name!r}")
            bundle.extractall(extracted, filter="data")
    except tarfile.TarError as error:
        raise InstallerError(f"release archive is invalid: {error}") from error
    binary = extracted / "bin" / "concord"
    if not binary.is_file():
        raise InstallerError("release archive has no bin/concord Linux amd64 binary")
    expected_binary = checksums.get(prefix)
    if expected_binary is None:
        raise InstallerError(f"checksum file does not contain the binary entry {prefix}")
    if sha256(binary) != expected_binary:
        raise InstallerError("archive binary does not match the published checksum; nothing was installed")
    adapter_dir = extracted / "adapter" / "opencode"
    if any(not (adapter_dir / name).is_file() for name in ADAPTER_FILES):
        raise InstallerError("release archive is missing the OpenCode adapter files")
    return extracted, checksums


def file_records(root: Path, paths: list[str]) -> dict[str, str]:
    return {relative: sha256(root / relative) for relative in paths}


def validate_owned_tree(root: Path, records: dict[str, str], *, allow_missing: bool = False) -> None:
    """Validate a tree the installer owns against recorded file hashes.

    allow_missing serves repair: an incomplete deployment may lack recorded
    files, because restoring them is repair's purpose. Everything else —
    symlinks, unknown files, modified content — refuses as before.
    """
    if not root.exists():
        if allow_missing:
            return
        raise InstallerError(f"managed installation path is missing: {root}")
    if root.is_symlink():
        raise InstallerError(f"refusing managed path {root}: it is a symlink")
    for path in root.rglob("*"):
        if path.is_symlink():
            raise InstallerError(f"refusing managed path {path}: it is a symlink")
    for relative in records:
        target = safe_relative_target(root, relative, "managed file")
        if target.is_symlink():
            raise InstallerError(f"refusing managed file {target}: it is a symlink")
    actual = {
        str(path.relative_to(root))
        for path in root.rglob("*")
        if path.is_file()
    }
    expected = set(records)
    if actual != expected:
        extra = sorted(actual - expected)
        missing = sorted(expected - actual)
        detail = []
        if extra:
            detail.append("user-authored or unknown files: " + ", ".join(extra))
        if missing and not allow_missing:
            detail.append("missing managed files: " + ", ".join(missing))
        if detail:
            raise InstallerError(f"refusing to replace managed path {root}; " + "; ".join(detail))
    for relative, expected_hash in records.items():
        target = root / relative
        if allow_missing and not target.exists():
            continue
        actual_hash = sha256(target)
        if actual_hash != expected_hash:
            raise InstallerError(f"refusing to replace modified managed file {root / relative}")


def write_atomic(path: Path, content: bytes, mode: int | None = None) -> None:
    ensure_directory(path.parent)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        if mode is not None:
            os.chmod(temporary, mode)
        elif path.exists():
            os.chmod(temporary, stat.S_IMODE(path.stat().st_mode))
        os.replace(temporary, path)
        fsync_directory(path.parent)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def fsync_directory(path: Path) -> None:
    """Persist directory entry updates on filesystems that support fsync."""
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def ensure_directory(path: Path, mode: int = 0o755) -> None:
    """Create a directory tree and persist every newly-created entry."""
    missing: list[Path] = []
    current = path
    while not current.exists():
        missing.append(current)
        current = current.parent
    if path.is_symlink():
        raise InstallerError(f"refusing symlinked directory {path}")
    path.mkdir(mode=mode, parents=True, exist_ok=True)
    for created in missing:
        fsync_directory(created.parent)


def managed_adapter_records(manifest: dict[str, object] | None) -> dict[str, str]:
    if not manifest:
        return {}
    value = manifest.get("adapter_files", {})
    if not isinstance(value, dict) or not all(isinstance(k, str) and isinstance(v, str) for k, v in value.items()):
        raise InstallerError("existing installer manifest has invalid adapter file records")
    return value  # type: ignore[return-value]


def managed_agent_records(manifest: dict[str, object] | None) -> dict[str, str]:
    if not manifest:
        return {}
    value = manifest.get("agent_files", {})
    if not isinstance(value, dict) or not all(isinstance(k, str) and isinstance(v, str) for k, v in value.items()):
        raise InstallerError("existing installer manifest has invalid agent file records")
    return value  # type: ignore[return-value]


TRANSACTION_PARENT = ".concord-transactions"
MAX_TRANSACTION_JOURNAL_BYTES = 1024 * 1024
MAX_TRANSACTION_FILES = 4096
TRANSACTION_PHASES = (
    "staged",
    "version_activated",
    "agents_swapped",
    "adapter_swapped",
    "launcher_swapped",
    "config_swapped",
    "manifest_committed",
    "cleanup",
    "rollback",
    # CON-807: the transaction stopped because the readiness plan blocked
    # activation. The candidate tree and the prepared-release record are the
    # committed state; recovery only cleans the transaction scaffolding.
    "prepared",
)


def fsync_tree(root: Path) -> None:
    for path in sorted(root.rglob("*"), key=lambda value: len(value.parts), reverse=True):
        if path.is_file() and not path.is_symlink():
            descriptor = os.open(path, os.O_RDONLY)
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
        elif path.is_dir() and not path.is_symlink():
            fsync_directory(path)
    fsync_directory(root)


def durable_copy_tree(source: Path, destination: Path) -> None:
    if destination.exists():
        raise InstallerError(f"staging destination already exists: {destination}")
    shutil.copytree(source, destination, symlinks=False)
    fsync_tree(destination)
    fsync_directory(destination.parent)


def copy_tree_if_present(source: Path, destination: Path) -> None:
    """Mirror durable_copy_tree for optional directories that may be absent.

    The archive for a minimal Concord release does not have to ship skills,
    instructions, or agents; the corpus is part of CD-0063, but the surface
    itself ships empty under CD-0043 D3. Writing the loop three times when
    every branch is the same `if source.exists()` copy would let one copy
    drift from another, so the loop lives here.
    """
    if not source.exists():
        return
    if destination.exists():
        raise InstallerError(f"staging destination already exists: {destination}")
    shutil.copytree(source, destination, symlinks=False)
    fsync_tree(destination)
    fsync_directory(destination.parent)


def replace_durable(source: Path, destination: Path) -> None:
    """Rename across directories, persisting both directory entries."""
    os.replace(source, destination)
    fsync_directory(source.parent)
    if source.parent != destination.parent:
        fsync_directory(destination.parent)


def file_state(path: Path) -> dict[str, object]:
    if not path.exists() and not path.is_symlink():
        return {"exists": False}
    if path.is_symlink():
        return {"exists": True, "kind": "symlink", "target": os.readlink(path)}
    if not path.is_file():
        raise InstallerError(f"managed target is not a regular file: {path}")
    return {"exists": True, "kind": "file", "sha256": sha256(path)}


def state_matches(path: Path, expected: dict[str, object]) -> bool:
    actual = file_state(path)
    if not expected.get("exists"):
        return not actual.get("exists")
    if expected.get("kind") == "symlink":
        return actual == expected
    return actual.get("kind") == "file" and actual.get("sha256") == expected.get("sha256")


def capture_file(path: Path, backup: Path, label: str) -> dict[str, object]:
    state = file_state(path)
    if not state.get("exists"):
        return {"exists": False, "backup": None}
    if state.get("kind") != "file":
        raise InstallerError(f"refusing transaction over symlinked or non-file {label}: {path}")
    ensure_directory(backup.parent, mode=0o700)
    shutil.copy2(path, backup)
    os.chmod(backup, 0o600)
    with backup.open("rb") as stream:
        os.fsync(stream.fileno())
    fsync_directory(backup.parent)
    return {**state, "backup": str(backup.name)}


def current_manifest_state(paths: Paths) -> dict[str, object]:
    return file_state(paths.data_root / MANIFEST_NAME)


def version_matches(root: Path, records: dict[str, str] | None, *, allow_missing: bool = False) -> bool:
    if records is None:
        return not root.exists() and not root.is_symlink()
    if not root.exists() or root.is_symlink():
        return False
    try:
        validate_owned_tree(root, records, allow_missing=allow_missing)
    except InstallerError:
        return False
    return True


def journal_path(transaction_root: Path) -> Path:
    return transaction_root / "journal.json"


def write_journal(transaction_root: Path, journal: dict[str, object]) -> None:
    payload = (json.dumps(journal, indent=2, sort_keys=True) + "\n").encode("utf-8")
    if len(payload) > MAX_TRANSACTION_JOURNAL_BYTES:
        raise InstallerError("transaction journal exceeds the bounded 1 MiB recovery limit")
    write_atomic(journal_path(transaction_root), payload)


def advance_phase(transaction_root: Path, journal: dict[str, object], phase: str) -> None:
    journal["phase"] = phase
    write_journal(transaction_root, journal)
    if os.environ.get("CONCORD_INSTALLER_STOP_AFTER_PHASE") == phase:
        os._exit(97)


def validate_transaction(journal: dict[str, object], transaction_root: Path, paths: Paths) -> None:
    required = {
        "schema",
        "operation",
        "phase",
        "old_version",
        "new_version",
        "activation_version",
        "cleanup_version",
        "cleanup_records",
        "cleanup_candidates",
        "old_version_records",
        "new_version_records",
        "old_adapter",
        "new_adapter",
        "old_agents",
        "new_agents",
        "old_config",
        "new_config",
        "old_launcher",
        "new_launcher",
        "old_stable_root",
        "new_stable_root",
        "old_manifest",
        "new_manifest",
        "targets",
        "backup_dir",
        "stage_dir",
        "version_backup",
        "live_version_backup",
    }
    # prepared_activation marks the CON-807 activation of a prepared
    # candidate: the incompatible migration already committed, so recovery
    # resumes the candidate forward and never restores the older release.
    # maintenance_fence_id records which open boundary this transaction
    # adopted, so recovery closes that identity and never a foreign fence.
    optional = {"prepared_activation", "maintenance_fence_id"}
    if set(journal) - required - optional or required - set(journal) or journal.get("schema") != 1:
        raise InstallerError(f"refusing malformed transaction journal {journal_path(transaction_root)}")
    if journal.get("prepared_activation") is not None and not isinstance(journal.get("prepared_activation"), bool):
        raise InstallerError(f"refusing malformed prepared-activation flag {journal_path(transaction_root)}")
    if journal.get("maintenance_fence_id") is not None and (
        not isinstance(journal.get("maintenance_fence_id"), str) or not journal.get("maintenance_fence_id")
    ):
        raise InstallerError(f"refusing malformed maintenance fence identity {journal_path(transaction_root)}")
    if journal.get("operation") not in ACTIVATING_OPERATIONS and journal.get("operation") != "uninstall" or journal.get("phase") not in TRANSACTION_PHASES:
        raise InstallerError(f"refusing malformed transaction journal {journal_path(transaction_root)}")
    for key in ("old_version", "new_version", "activation_version", "cleanup_version"):
        value = journal.get(key)
        if value is not None and (not isinstance(value, str) or not VERSION_RE.fullmatch(value)):
            raise InstallerError(f"refusing malformed transaction version in {journal_path(transaction_root)}")
    if activating(str(journal.get("operation", ""))) and not isinstance(journal.get("new_version_records"), dict):
        raise InstallerError(f"refusing malformed transaction records {journal_path(transaction_root)}")
    if journal.get("operation") == "uninstall" and journal.get("new_version_records") is not None:
        raise InstallerError(f"refusing malformed uninstall transaction {journal_path(transaction_root)}")
    if journal.get("backup_dir") != "backup" or journal.get("stage_dir") != "stage" or journal.get("version_backup") != "backup/version" or journal.get("live_version_backup") != "backup/live-version":
        raise InstallerError(f"refusing redirected transaction backup locator {journal_path(transaction_root)}")
    if journal.get("cleanup_candidates") is not None:
        validate_release_records(journal.get("cleanup_candidates"), paths, "transaction journal")
    for field in ("old_version_records", "new_version_records", "cleanup_records"):
        records = journal.get(field)
        if records is None:
            continue
        if not isinstance(records, dict):
            raise InstallerError(f"refusing malformed transaction records {journal_path(transaction_root)}")
        if len(records) > MAX_TRANSACTION_FILES:
            raise InstallerError(f"refusing oversized transaction records {journal_path(transaction_root)}")
        record_root = paths.data_root / str(journal.get("activation_version"))
        if field == "cleanup_records":
            record_root = paths.data_root / str(journal.get("cleanup_version"))
        for relative, digest in records.items():
            if not isinstance(relative, str) or not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
                raise InstallerError(f"refusing malformed transaction file record {journal_path(transaction_root)}")
            safe_relative_target(record_root, relative, "transaction file")
    old_adapter = journal.get("old_adapter")
    if not isinstance(old_adapter, dict) or set(old_adapter) != set(ADAPTER_FILES):
        raise InstallerError(f"refusing malformed transaction adapter records {journal_path(transaction_root)}")
    for name, state in old_adapter.items():
        if not isinstance(state, dict) or not isinstance(state.get("exists"), bool):
            raise InstallerError(f"refusing malformed transaction adapter state {journal_path(transaction_root)}")
        if state["exists"] and (state.get("kind") != "file" or not isinstance(state.get("sha256"), str) or not SHA256_RE.fullmatch(state["sha256"])):
            raise InstallerError(f"refusing malformed transaction adapter state {journal_path(transaction_root)}")
        if state["exists"] and state.get("backup") != f"{name}":
            raise InstallerError(f"refusing malformed transaction adapter backup {journal_path(transaction_root)}")
    new_adapter = journal.get("new_adapter")
    if new_adapter is not None and (
        not isinstance(new_adapter, dict)
        or set(new_adapter) != set(ADAPTER_FILES)
        or not all(isinstance(value, str) and SHA256_RE.fullmatch(value) for value in new_adapter.values())
    ):
        raise InstallerError(f"refusing malformed transaction adapter targets {journal_path(transaction_root)}")
    old_agents = journal.get("old_agents")
    if not isinstance(old_agents, dict):
        raise InstallerError(f"refusing malformed transaction agent records {journal_path(transaction_root)}")
    if len(old_agents) > MAX_TRANSACTION_FILES:
        raise InstallerError(f"refusing oversized transaction agent records {journal_path(transaction_root)}")
    for name, state in old_agents.items():
        if not isinstance(name, str) or not isinstance(state, dict) or not isinstance(state.get("exists"), bool):
            raise InstallerError(f"refusing malformed transaction agent state {journal_path(transaction_root)}")
        if state["exists"] and (state.get("kind") != "file" or not isinstance(state.get("sha256"), str) or not SHA256_RE.fullmatch(state["sha256"])):
            raise InstallerError(f"refusing malformed transaction agent state {journal_path(transaction_root)}")
        if state["exists"] and state.get("backup") != name:
            raise InstallerError(f"refusing malformed transaction agent backup {journal_path(transaction_root)}")
        safe_relative_target(paths.agents_dir, name, "transaction agent")
    new_agents = journal.get("new_agents")
    if new_agents is not None and (
        not isinstance(new_agents, dict)
        or not all(isinstance(name, str) for name in new_agents)
        or not all(isinstance(value, str) and SHA256_RE.fullmatch(value) for value in new_agents.values())
    ):
        raise InstallerError(f"refusing malformed transaction agent targets {journal_path(transaction_root)}")
    for relative in ("stage", "backup"):
        safe_relative_target(transaction_root.parent, transaction_root.name + "/" + relative, "transaction")
    targets = journal.get("targets")
    if not isinstance(targets, dict) or set(targets) != {"version", "adapters", "agents", "launcher", "config", "manifest", "stable_root"}:
        raise InstallerError(f"refusing malformed transaction targets {journal_path(transaction_root)}")
    if targets.get("config") != str(paths.config_file.resolve(strict=False)):
        raise InstallerError(f"refusing redirected transaction config target {journal_path(transaction_root)}")
    if targets.get("launcher") != str(paths.launcher):
        raise InstallerError(f"refusing redirected transaction launcher target {journal_path(transaction_root)}")
    if targets.get("stable_root") != str(paths.stable_root):
        raise InstallerError(f"refusing redirected transaction stable root target {journal_path(transaction_root)}")
    if not isinstance(targets.get("agents"), list):
        raise InstallerError(f"refusing malformed transaction agents target {journal_path(transaction_root)}")
    activation = journal.get("activation_version")
    if not isinstance(activation, str) or targets.get("version") != str((paths.data_root / activation).resolve(strict=False)):
        raise InstallerError(f"refusing redirected transaction version target {journal_path(transaction_root)}")
    old_stable_root = journal.get("old_stable_root")
    if not isinstance(old_stable_root, dict) or not isinstance(old_stable_root.get("exists"), bool):
        raise InstallerError(f"refusing malformed transaction stable root state {journal_path(transaction_root)}")
    if old_stable_root.get("exists") and (
        old_stable_root.get("kind") != "symlink" or not isinstance(old_stable_root.get("target"), str)
    ):
        raise InstallerError(f"refusing malformed transaction stable root state {journal_path(transaction_root)}")
    new_stable_root = journal.get("new_stable_root")
    if not isinstance(new_stable_root, dict) or not isinstance(new_stable_root.get("exists"), bool):
        raise InstallerError(f"refusing malformed transaction stable root target {journal_path(transaction_root)}")
    if new_stable_root.get("exists") and (
        new_stable_root.get("kind") != "symlink" or not isinstance(new_stable_root.get("target"), str)
    ):
        raise InstallerError(f"refusing malformed transaction stable root target {journal_path(transaction_root)}")


def make_transaction(
    paths: Paths,
    operation: str,
    old_manifest: dict[str, object] | None,
    new_version: str | None,
    stage_source: Path | None,
    new_version_records: dict[str, str] | None,
    new_adapter: dict[str, str] | None,
    new_agents: dict[str, str] | None,
    new_config: str | None,
    new_manifest_bytes: bytes | None,
    cleanup_candidates: dict[str, dict[str, str]] | None = None,
    reinstalled_records: dict[str, str] | None = None,
    recovery_marks: dict[str, object] | None = None,
) -> tuple[Path, dict[str, object]]:
    """Stage a transaction and write its first durable journal.

    recovery_marks (prepared_activation, maintenance_fence_id) land in that
    first journal, so a crash at any later point, the staged phase included,
    recovers with the direction the operation already committed to.
    """
    ensure_directory(paths.data_root)
    parent = paths.data_root / TRANSACTION_PARENT
    if parent.exists() and parent.is_symlink():
        raise InstallerError(f"refusing symlinked transaction directory {parent}")
    ensure_directory(parent, mode=0o700)
    os.chmod(parent, 0o700)
    transaction_root = parent / uuid.uuid4().hex
    backup = transaction_root / "backup"
    stage = transaction_root / "stage"
    ensure_directory(transaction_root, mode=0o700)
    ensure_directory(backup, mode=0o700)
    ensure_directory(stage, mode=0o700)
    if stage_source is not None:
        durable_copy_tree(stage_source, stage / "version")
        if not isinstance(new_version_records, dict) or file_records(stage / "version", list(new_version_records)) != new_version_records:
            raise InstallerError("staged version files failed hash verification")
        if len(new_version_records) > MAX_TRANSACTION_FILES:
            raise InstallerError("staged version contains too many managed files")
    if new_config is not None:
        write_atomic(stage / "config", new_config.encode("utf-8"))
    if new_manifest_bytes is not None:
        write_atomic(stage / "manifest", new_manifest_bytes)

    old_version = old_manifest.get("version") if old_manifest else None
    old_records = old_manifest.get("version_files", {}) if old_manifest else None
    activation_version = new_version if activating(operation) else old_version
    # A retained release that this install reinstalls is owned by the records
    # the manifest kept for it, so the transaction backs it up like any other
    # activation root it replaces.
    activation_old_records = old_records if activation_version == old_version else reinstalled_records
    activation_root = paths.data_root / str(activation_version) if activation_version else paths.data_root / "unused"
    version_backup = backup / "version"
    if operation == "repair":
        # The deployment converges to the staged release, so the activation
        # check names the staged records: known files may be absent (that is
        # what repair restores) while unknown or modified files still refuse.
        if not isinstance(new_version_records, dict):
            raise InstallerError("repair requires staged version records")
        validate_owned_tree(activation_root, new_version_records, allow_missing=True)
        durable_copy_tree(activation_root, version_backup)
    elif activation_old_records is not None:
        if not isinstance(activation_old_records, dict):
            raise InstallerError("existing manifest has invalid version records")
        validate_owned_tree(activation_root, activation_old_records)
        durable_copy_tree(activation_root, version_backup)
    elif operation == "uninstall":
        raise InstallerError("cannot uninstall without a managed version directory")
    old_adapter: dict[str, object] = {}
    for name in ADAPTER_FILES:
        old_adapter[name] = capture_file(paths.tools_dir / name, backup / "adapter" / name, f"adapter {name}")
    # Capture only agent paths this transaction owns or will write. A broad
    # concord-*.md snapshot would give uninstall deletion authority over
    # operator-authored primary agents that the manifest never managed.
    old_agents: dict[str, object] = {}
    managed_agents = managed_agent_records(old_manifest)
    agent_names = set(managed_agents)
    if isinstance(new_agents, dict):
        agent_names.update(new_agents)
    for name in sorted(agent_names):
        target = paths.agents_dir / name
        if target.exists() or target.is_symlink():
            expected = managed_agents.get(name)
            if expected is None:
                staged_agents = new_agents if isinstance(new_agents, dict) else {}
                if operation == "repair" and target.is_file() and staged_agents.get(name) == sha256(target):
                    # An incomplete deployment placed the file; the stale
                    # manifest predates it. Content matching the release owns
                    # the slot, so the transaction adopts rather than refuses.
                    pass
                else:
                    raise InstallerError(f"refusing to overwrite user-authored agent file {target}")
            if expected is not None and (not target.is_file() or sha256(target) != expected):
                raise InstallerError(f"refusing to overwrite modified managed agent file {target}")
        old_agents[name] = capture_file(
            target,
            backup / "agents" / name,
            f"agent {name}",
        )
    old_config = capture_file(paths.config_file, backup / "config", "OpenCode config")
    old_manifest_state = capture_file(paths.data_root / MANIFEST_NAME, backup / "manifest", "installer manifest")
    old_launcher = file_state(paths.launcher)
    if old_launcher.get("exists") and old_launcher.get("kind") != "symlink":
        raise InstallerError(f"refusing transaction over user-authored launcher {paths.launcher}")
    old_stable_root = file_state(paths.stable_root)
    if old_stable_root.get("exists") and old_stable_root.get("kind") != "symlink":
        raise InstallerError(f"refusing transaction over unmanaged stable root {paths.stable_root}")
    new_config_state = {"exists": new_config is not None, "kind": "file", "sha256": sha256(stage / "config")} if new_config is not None else {"exists": False}
    new_manifest_state = {"exists": new_manifest_bytes is not None, "kind": "file", "sha256": sha256(stage / "manifest")} if new_manifest_bytes is not None else {"exists": False}
    new_launcher_state = {"exists": True, "kind": "symlink", "target": str((paths.data_root / str(new_version) / "bin" / "concord").resolve())} if activating(operation) else {"exists": False}
    # The uninstall branch records what apply_stable_root will produce: a
    # removal when the current symlink points inside the data root, a no-op
    # otherwise. verify_states compares the journal entry against the on-disk
    # state, so the recorded target must match what apply actually leaves.
    if activating(operation):
        new_stable_root_state = {"exists": True, "kind": "symlink", "target": str((paths.data_root / str(new_version)).resolve())}
    elif old_stable_root.get("exists") and _stable_root_targets_inside_data_root(paths.stable_root, paths.data_root):
        new_stable_root_state = {"exists": False}
    else:
        new_stable_root_state = old_stable_root
    journal: dict[str, object] = {
        "schema": 1,
        "operation": operation,
        "phase": "staged",
        "old_version": old_version,
        "new_version": new_version,
        "activation_version": activation_version,
        "cleanup_version": old_version if activating(operation) and old_version != new_version else None,
        "cleanup_records": old_records if activating(operation) and old_version != new_version else None,
        "cleanup_candidates": cleanup_candidates,
        "old_version_records": activation_old_records,
        "new_version_records": new_version_records,
        "old_adapter": old_adapter,
        "new_adapter": new_adapter,
        "old_agents": old_agents,
        "new_agents": new_agents,
        "old_config": old_config,
        "new_config": new_config_state,
        "old_launcher": old_launcher,
        "new_launcher": new_launcher_state,
        "old_stable_root": old_stable_root,
        "new_stable_root": new_stable_root_state,
        "old_manifest": old_manifest_state,
        "new_manifest": new_manifest_state,
        "backup_dir": "backup",
        "stage_dir": "stage",
        "version_backup": "backup/version",
        "live_version_backup": "backup/live-version",
        "targets": {
            "version": str(activation_root.resolve(strict=False)),
            "adapters": [str((paths.tools_dir / name).resolve(strict=False)) for name in ADAPTER_FILES],
            "agents": [str(paths.agents_dir)],
            "launcher": str(paths.launcher),
            "config": str(paths.config_file.resolve(strict=False)),
            "manifest": str((paths.data_root / MANIFEST_NAME).resolve(strict=False)),
            "stable_root": str(paths.stable_root),
        },
    }
    if recovery_marks:
        journal.update(recovery_marks)
    write_journal(transaction_root, journal)
    fsync_directory(parent)
    if os.environ.get("CONCORD_INSTALLER_STOP_AFTER_PHASE") == "staged":
        os._exit(97)
    return transaction_root, journal


def apply_version(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    """Place the activation version and swap the stable root.

    The placement is idempotent inside its own phase (CON-807): a crash
    after the new tree landed leaves the live-version backup in place, and
    a forward recovery re-running this step recognizes the placed records
    instead of refusing on the surviving backup. The staged tree is moved
    at most once; a re-run verifies the placed target against the records
    the journal staged.
    """
    version = journal["activation_version"]
    if not isinstance(version, str):
        raise InstallerError("transaction has no activation version")
    target = paths.data_root / version
    live = transaction_root / "backup" / "live-version"
    staged = transaction_root / "stage" / "version"
    records = journal.get("new_version_records") if activating(journal["operation"]) else None
    if target.exists() or target.is_symlink():
        if live.exists() or live.is_symlink():
            if not (isinstance(records, dict) and version_matches(target, records)):
                raise InstallerError("transaction live version backup already exists")
        else:
            replace_durable(target, live)
    if activating(journal["operation"]):
        if staged.exists():
            replace_durable(staged, target)
        elif not (isinstance(records, dict) and version_matches(target, records)):
            raise InstallerError("transaction stage is absent and the placed version does not match its records")
    apply_stable_root(transaction_root, journal, paths)


def _stable_root_targets_inside_data_root(target: Path, data_root: Path) -> bool:
    """Whether a stable_root symlink points inside the data root.

    A symlink that escaped the data root cannot have been placed by a Concord
    installation; removing it would clobber an unrelated path. Relative links
    resolve against the symlink's parent so the comparison uses absolute paths.
    """
    link_target = Path(os.readlink(target))
    if not link_target.is_absolute():
        link_target = (target.parent / link_target).resolve()
    return link_target == data_root or data_root in link_target.parents


def apply_stable_root(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    """Maintain paths.stable_root as a symlink to the activation version.

    Projects embed this path so they survive upgrades without rewriting their
    configuration. Replacement uses a temp symlink beside the target so the
    change is atomic; removal is the symmetric path for uninstall and only
    fires when the existing symlink targets inside the data root.
    """
    target = paths.stable_root
    if activating(journal["operation"]):
        new_version = journal["new_version"]
        if not isinstance(new_version, str):
            raise InstallerError("transaction has no new version")
        link_target = (paths.data_root / new_version).resolve()
        temporary = target.parent / f".concord-stable-{transaction_root.name}"
        if temporary.exists() or temporary.is_symlink():
            temporary.unlink()
        temporary.symlink_to(link_target)
        replace_durable(temporary, target)
    elif target.is_symlink():
        if not _stable_root_targets_inside_data_root(target, paths.data_root):
            return
        target.unlink()
        fsync_directory(target.parent)
    elif target.exists():
        target.unlink()
        fsync_directory(target.parent)


def apply_agents(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    """Place central agent definitions from the version tree or remove them.

    Central visibility is the only mechanism projects have to invoke a Concord
    lane, which is why the file lives outside the version tree: a project
    dispatch reads ~/.config/opencode/agents/<name>.md, not a versioned path.
    Apply mirrors apply_adapters so an upgrade leaves an existing project
    pointing at the new file content without rewriting the project itself.
    The set is dynamic, so install iterates new_agents and uninstall iterates
    old_agents (the snapshot the transaction captured).
    """
    touched = False
    if activating(journal["operation"]):
        new_agents = journal.get("new_agents") or {}
        if not isinstance(new_agents, dict):
            raise InstallerError("transaction has malformed agent targets")
        old_agents = journal.get("old_agents") or {}
        if not isinstance(old_agents, dict):
            raise InstallerError("transaction has malformed agent records")
        for name, state in old_agents.items():
            if name in new_agents:
                continue
            if not isinstance(name, str) or not isinstance(state, dict):
                raise InstallerError("transaction has malformed agent record")
            target = paths.agents_dir / name
            if not target.exists() and not target.is_symlink():
                # A replayed transaction (CON-807 forward recovery) reaches
                # this step with the removal already done; a target that is
                # absent cannot conflict, and the transaction was going to
                # remove it anyway.
                continue
            if not state_matches(target, state):
                raise InstallerError(f"transaction conflict at agent {name}; refusing removal")
            if target.exists() or target.is_symlink():
                target.unlink()
                touched = True
        version = journal["new_version"]
        if not isinstance(version, str):
            raise InstallerError("transaction has no new version")
        for name, digest in new_agents.items():
            if not isinstance(name, str) or not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
                raise InstallerError("transaction has malformed agent target record")
            source = paths.data_root / str(version) / "agents" / name
            if not source.is_file():
                raise InstallerError(f"version tree is missing agent source {source}")
            write_atomic(paths.agents_dir / name, source.read_bytes())
            touched = True
    else:
        old_agents = journal.get("old_agents") or {}
        if not isinstance(old_agents, dict):
            raise InstallerError("transaction has malformed agent records")
        for name in old_agents:
            if not isinstance(name, str):
                raise InstallerError("transaction has malformed agent record")
            target = paths.agents_dir / name
            if target.exists() or target.is_symlink():
                target.unlink()
                touched = True
    if touched and paths.agents_dir.exists():
        fsync_directory(paths.agents_dir)


def apply_adapters(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    for name in ADAPTER_FILES:
        target = paths.tools_dir / name
        if activating(journal["operation"]):
            version = journal["new_version"]
            source = paths.data_root / str(version) / "adapter" / "opencode" / name
            write_atomic(target, source.read_bytes())
        elif target.exists() or target.is_symlink():
            target.unlink()
    fsync_directory(paths.tools_dir)


def apply_launcher(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    target = paths.launcher
    if activating(journal["operation"]):
        # Preflight only proves the bin dir is on PATH; a PATH entry can name
        # a directory that does not exist yet, and a fresh HOME has exactly
        # that shape. Create it before the symlink needs a parent (#936).
        ensure_directory(paths.bin_dir)
        version = journal["new_version"]
        temporary = paths.bin_dir / f".concord-link-{transaction_root.name}"
        if temporary.exists() or temporary.is_symlink():
            temporary.unlink()
        temporary.symlink_to((paths.data_root / str(version) / "bin" / "concord").resolve())
        replace_durable(temporary, target)
    elif target.exists() or target.is_symlink():
        target.unlink()
        fsync_directory(paths.bin_dir)


def apply_config(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    target = paths.config_file
    if journal["new_config"]["exists"]:
        content = (transaction_root / "stage" / "config").read_bytes()
        if target.is_file() and not target.is_symlink() and target.read_bytes() == content:
            return
        write_atomic(target, content)
    elif target.exists() or target.is_symlink():
        target.unlink()
        fsync_directory(target.parent)


def apply_manifest(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    target = paths.data_root / MANIFEST_NAME
    if journal["new_manifest"]["exists"]:
        write_atomic(target, (transaction_root / "stage" / "manifest").read_bytes())
    elif target.exists() or target.is_symlink():
        target.unlink()
        fsync_directory(target.parent)


def restore_file(path: Path, state: dict[str, object], transaction_root: Path, backup_name: str) -> None:
    if state.get("exists"):
        backup = transaction_root / "backup" / backup_name
        write_atomic(path, backup.read_bytes())
    elif path.exists() or path.is_symlink():
        path.unlink()
        fsync_directory(path.parent)


def restore_launcher(paths: Paths, state: dict[str, object]) -> None:
    removed = False
    if paths.launcher.exists() or paths.launcher.is_symlink():
        paths.launcher.unlink()
        removed = True
    if state.get("exists"):
        ensure_directory(paths.bin_dir)
        temporary = paths.bin_dir / f".concord-restore-{os.getpid()}"
        temporary.symlink_to(state["target"])
        replace_durable(temporary, paths.launcher)
    elif removed:
        fsync_directory(paths.bin_dir)


def restore_stable_root(paths: Paths, state: dict[str, object]) -> None:
    """Restore the stable_root symlink to its pre-transaction state.

    Symmetric with restore_launcher: a no-op removal, or a temp symlink beside
    the target replaced via os.replace so a crash leaves one of two valid
    pointers. The preflight refuses any non-symlink at paths.stable_root, so a
    managed state here can only be absent or a symlink pointing at a version
    directory.
    """
    removed = False
    if paths.stable_root.exists() or paths.stable_root.is_symlink():
        paths.stable_root.unlink()
        removed = True
    if state.get("exists"):
        ensure_directory(paths.data_root)
        temporary = paths.data_root / f".concord-stable-restore-{os.getpid()}"
        temporary.symlink_to(state["target"])
        replace_durable(temporary, paths.stable_root)
    elif removed:
        fsync_directory(paths.data_root)


def rollback_version(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    version = journal["activation_version"]
    if not isinstance(version, str):
        return
    target = paths.data_root / version
    live = transaction_root / "backup" / "live-version"
    old_records = journal["old_version_records"]
    new_records = journal["new_version_records"]
    if live.exists():
        if target.exists():
            if not version_matches(target, new_records if activating(journal["operation"]) else None, allow_missing=journal["operation"] == "repair"):
                raise InstallerError(f"transaction conflict at {target}; refusing rollback")
            shutil.rmtree(target)
        replace_durable(live, target)
    elif activating(journal["operation"]):
        if target.exists() and not version_matches(target, old_records) and not version_matches(target, new_records):
            raise InstallerError(f"transaction conflict at {target}; refusing rollback")
        if version_matches(target, new_records) and not version_matches(target, old_records):
            shutil.rmtree(target)
            fsync_directory(paths.data_root)
        if old_records is not None and not version_matches(target, old_records):
            durable_copy_tree(transaction_root / "backup" / "version", target)
    elif not version_matches(target, old_records):
        durable_copy_tree(transaction_root / "backup" / "version", target)


def verify_states(journal: dict[str, object], paths: Paths, committed: bool) -> None:
    operation = journal["operation"]
    if committed:
        version = journal["new_version"]
        records = journal["new_version_records"]
        if activating(operation) and not version_matches(paths.data_root / str(version), records):
            raise InstallerError("transaction conflict: new version data is not intact")
        if operation == "uninstall" and journal["activation_version"] and (paths.data_root / str(journal["activation_version"])).exists():
            raise InstallerError("transaction conflict: uninstalled version data reappeared")
        adapter_expected = journal["new_adapter"]
        agents_expected = journal.get("new_agents") or {}
        launcher_expected = journal["new_launcher"]
        config_expected = journal["new_config"]
        manifest_expected = journal["new_manifest"]
        stable_root_expected = journal.get("new_stable_root", {"exists": False})
    else:
        version = journal["activation_version"]
        records = journal["old_version_records"]
        # Repair starts from an incomplete deployment, so the pre-state it
        # rolls back to may itself lack recorded files.
        if records is not None and not version_matches(paths.data_root / str(version), records, allow_missing=operation == "repair"):
            raise InstallerError("transaction conflict: old version data is not intact")
        cleanup_version = journal.get("cleanup_version")
        cleanup_records = journal.get("cleanup_records")
        if isinstance(cleanup_version, str) and not version_matches(paths.data_root / cleanup_version, cleanup_records):
            raise InstallerError("transaction conflict: prior version data is not intact")
        adapter_expected = journal["old_adapter"]
        agents_expected = journal.get("old_agents") or {}
        launcher_expected = journal["old_launcher"]
        config_expected = journal["old_config"]
        manifest_expected = journal["old_manifest"]
        stable_root_expected = journal.get("old_stable_root", {"exists": False})
    for name in ADAPTER_FILES:
        if committed:
            digest = adapter_expected.get(name) if isinstance(adapter_expected, dict) else None
            expected = {"exists": True, "kind": "file", "sha256": digest} if digest else {"exists": False}
        else:
            expected = adapter_expected[name] if isinstance(adapter_expected, dict) and name in adapter_expected else {"exists": False}
        if not state_matches(paths.tools_dir / name, expected):
            raise InstallerError(f"transaction conflict at adapter {name}")
    # Agents are dynamic, so iterate over the union of expected names rather
    # than a static list. committed=True expects every name in new_agents;
    # committed=False expects every name in old_agents (the pre-transaction set).
    if committed:
        expected_agent_names = set(agents_expected) if isinstance(agents_expected, dict) else set()
        expected_digests = agents_expected if isinstance(agents_expected, dict) else {}
    else:
        expected_agent_names = set(agents_expected) if isinstance(agents_expected, dict) else set()
        expected_digests = {}
    for name in sorted(expected_agent_names):
        if committed:
            digest = expected_digests.get(name)
            expected_state = {"exists": True, "kind": "file", "sha256": digest} if digest else {"exists": False}
        else:
            state = agents_expected.get(name) if isinstance(agents_expected, dict) else None
            expected_state = state if isinstance(state, dict) else {"exists": False}
        if not state_matches(paths.agents_dir / name, expected_state):
            raise InstallerError(f"transaction conflict at agent {name}")
    if not state_matches(paths.launcher, launcher_expected):
        raise InstallerError("transaction conflict at launcher")
    if not state_matches(paths.stable_root, stable_root_expected):
        raise InstallerError("transaction conflict at stable root")
    if not state_matches(paths.config_file, config_expected):
        raise InstallerError("transaction conflict at OpenCode config")
    if not state_matches(paths.data_root / MANIFEST_NAME, manifest_expected):
            raise InstallerError("transaction conflict at installer manifest")


def ensure_rollback_safe(journal: dict[str, object], paths: Paths) -> None:
    """Allow only states produced by this transaction or its original state."""
    old_adapter = journal["old_adapter"]
    new_adapter = journal["new_adapter"]
    for name in ADAPTER_FILES:
        old = old_adapter[name]
        digest = new_adapter.get(name) if isinstance(new_adapter, dict) else None
        new = {"exists": True, "kind": "file", "sha256": digest} if digest else {"exists": False}
        path = paths.tools_dir / name
        current = file_state(path)
        if current.get("exists") and not state_matches(path, old) and not state_matches(path, new):
            raise InstallerError(f"transaction conflict at adapter {name}; refusing rollback")
    old_agents = journal.get("old_agents") or {}
    new_agents = journal.get("new_agents") or {}
    if isinstance(old_agents, dict) and isinstance(new_agents, dict):
        candidate_names = set(old_agents) | set(new_agents)
        for name in candidate_names:
            old_state = old_agents.get(name) if isinstance(old_agents.get(name), dict) else {"exists": False}
            new_digest = new_agents.get(name)
            new_state = {"exists": True, "kind": "file", "sha256": new_digest} if isinstance(new_digest, str) else {"exists": False}
            path = paths.agents_dir / name
            current = file_state(path)
            if current.get("exists") and not state_matches(path, old_state) and not state_matches(path, new_state):
                raise InstallerError(f"transaction conflict at agent {name}; refusing rollback")
    current_launcher = file_state(paths.launcher)
    if current_launcher.get("exists") and not state_matches(paths.launcher, journal["old_launcher"]) and not state_matches(paths.launcher, journal["new_launcher"]):
        raise InstallerError("transaction conflict at launcher; refusing rollback")
    current_stable_root = file_state(paths.stable_root)
    if current_stable_root.get("exists") and not state_matches(paths.stable_root, journal["old_stable_root"]) and not state_matches(paths.stable_root, journal["new_stable_root"]):
        raise InstallerError("transaction conflict at stable root; refusing rollback")
    current_config = file_state(paths.config_file)
    if current_config.get("exists") and not state_matches(paths.config_file, journal["old_config"]) and not state_matches(paths.config_file, journal["new_config"]):
        raise InstallerError("transaction conflict at OpenCode config; refusing rollback")
    current_manifest = current_manifest_state(paths)
    if current_manifest.get("exists") and not state_matches(paths.data_root / MANIFEST_NAME, journal["old_manifest"]) and not state_matches(paths.data_root / MANIFEST_NAME, journal["new_manifest"]):
        raise InstallerError("transaction conflict at installer manifest; refusing rollback")
    version = journal["activation_version"]
    if isinstance(version, str):
        target = paths.data_root / version
        old_records = journal["old_version_records"]
        new_records = journal["new_version_records"]
        if target.exists() and not version_matches(target, old_records) and not version_matches(target, new_records):
            raise InstallerError(f"transaction conflict at version data {target}; refusing rollback")


def cleanup_transaction(transaction_root: Path, journal: dict[str, object], paths: Paths, remove_old_version: bool = True) -> None:
    # The replaced release and every retained one leave through the one
    # lease-aware path (CD-0111 D2); nothing here removes a version root by
    # itself anymore. The data root itself is never touched here: it is the
    # maintenance lock's root, and no cleanup removes it (CON-807,
    # obs:1ca149d633e69626). A mid-command rmdir would delete a directory
    # the command never created and keep making state effects after it.
    if remove_old_version:
        remove_unheld_releases(journal, paths)
        if journal.get("operation") == "uninstall":
            unlink_worktrees_root(paths)
    live = transaction_root / "backup" / "live-version"
    if live.exists() or live.is_symlink():
        live.unlink() if live.is_symlink() else shutil.rmtree(live)
    if transaction_root.exists():
        shutil.rmtree(transaction_root)
        fsync_directory(transaction_root.parent)
    try:
        transaction_root.parent.rmdir()
        fsync_directory(transaction_root.parent.parent)
    except OSError:
        pass
    for directory in (paths.tools_dir, paths.agents_dir, paths.bin_dir):
        try:
            directory.rmdir()
        except OSError:
            pass


def admitted_release_roots(paths: Paths) -> set[str] | None:
    """Release roots the shared admission record names, read fresh (CON-807).

    A lease lands by atomic rename into the hosts directory, so a file
    present now was admitted — including one admitted after the core's lease
    observation. Returns None when the record cannot be read: nothing is
    then provably unreferenced, and the caller retains every candidate.
    """
    names = admission_record_names(paths)
    if names is None:
        return None
    roots: set[str] = set()
    for name in sorted(names):
        try:
            lease = json.loads((paths.data_root / "hosts" / name).read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            print(f"admission record observation failed at {paths.data_root / 'hosts' / name}: {error}", file=sys.stderr)
            return None
        root = lease.get("release_root") if isinstance(lease, dict) else None
        if not isinstance(root, str) or not root:
            print(f"admission record observation failed at {paths.data_root / 'hosts' / name}: a lease names no release root", file=sys.stderr)
            return None
        roots.add(str(Path(root).resolve()))
    return roots


def admission_record_names(paths: Paths) -> set[str] | None:
    """Names of the admission records present now, or None when unreadable.

    This is the fence-point observation the deletion section compares
    against: a compliant admission lands its lease while holding the shared
    admission lock, so a name that appears inside a section that already
    holds that lock was written by a participant the exclusion cannot fence.
    Every such participant makes the whole section refuse to delete.
    """
    directory = paths.data_root / "hosts"
    try:
        entries = list(directory.iterdir())
    except FileNotFoundError:
        return set()
    except OSError as error:
        print(f"admission record observation failed: {error}", file=sys.stderr)
        return None
    return {entry.name for entry in entries if entry.name.endswith(".json")}


def unfenceable_installed_trees(paths: Paths) -> list[str] | None:
    """Installed release trees whose cores cannot honor the shared
    session-admission exclusion (CON-807).

    A tree with a runnable core and no recognized fence-protocol marker — or
    one below the minimum — names a core that admits sessions without
    reading the fence or taking the shared admission lock, so no held
    exclusion can keep it out of a deletion section: an unfenceable
    participant source. Directories without bin/concord are inert. Returns
    None when the enumeration itself fails: unreadable means nothing is
    provably fenceable, and the caller retains every candidate.
    """
    try:
        entries = sorted(paths.data_root.iterdir())
    except FileNotFoundError:
        return []
    except OSError as error:
        print(f"installed release enumeration failed: {error}", file=sys.stderr)
        return None
    unfenceable: list[str] = []
    for entry in entries:
        if entry.is_symlink() or not entry.is_dir():
            continue
        if not (entry / "bin" / "concord").is_file():
            continue
        marker = entry / FENCE_PROTOCOL_MARKER_NAME
        try:
            raw = marker.read_text(encoding="utf-8")
        except FileNotFoundError:
            unfenceable.append(str(entry.resolve()))
            continue
        except OSError as error:
            print(f"fence-protocol marker read failed at {marker}: {error}", file=sys.stderr)
            return None
        try:
            protocol = int(raw.strip())
        except ValueError:
            unfenceable.append(str(entry.resolve()))
            continue
        if protocol not in SUPPORTED_FENCE_PROTOCOLS:
            # Recognition is explicit here too (CON-807): a higher protocol
            # names semantics this installer has not implemented, so the
            # tree grants no cleanup capability and stays retained.
            unfenceable.append(str(entry.resolve()))
    return unfenceable


def remove_unheld_releases(journal: dict[str, object], paths: Paths) -> set[str]:
    """CD-0111 D2: remove every candidate release no live session holds.

    An install observes the live session set through the core it just
    installed. When the observation fails, the bounded rule applies: the
    release this transaction replaced stays, because a session that started
    before the launcher moved may still hold it, and every older candidate is
    removed. An uninstall removes every candidate. A release that is retained
    stays a candidate in the manifest, so the next install retries the removal.

    Deletion is an admission-exclusion invariant, not an observation count
    (CON-807). Observation cannot close the window behind it: a participant
    that never takes the shared admission lock can land a lease after any
    final read, so no re-read authorizes the irreversible step. Cleanup
    instead establishes, before deleting anything, that every relevant
    admission participant can actually honor the held exclusion: the whole
    deletion section runs while holding the shared admission lock a
    compliant session lands its lease under, and every installed tree that
    could still admit a session carries a recognized fence-protocol marker.
    A tree that cannot honor the exclusion — legacy, unmarked, or an
    unreadable enumeration — retains every candidate: the operator removes
    or reinstalls it through the offline bootstrap, and the next install
    retries. A record that appears inside the held section was written
    without the lock by an unfenceable writer and stops the section the same
    way. A prepared activation is stricter still: its cleanup runs under the
    maintenance fence the migration opened, and deletion is authorized only
    while that exclusion is held — without the fence nothing is deleted, and
    a failed observation removes nothing at all.

    Returns the set of candidate versions actually removed.
    """
    candidates = journal.get("cleanup_candidates")
    if not isinstance(candidates, dict) or not candidates:
        return set()
    replaced = journal.get("cleanup_version")
    prepared_activation = journal.get("prepared_activation") is True
    if prepared_activation and not maintenance_fence_path(paths).exists():
        # The fence is the authority that makes an unreferenced observation
        # decisive. Without it no deletion is authorized at all.
        print(
            "keeping every candidate release: the maintenance fence is not held; a prepared activation deletes releases only under its session-admission exclusion",
            file=sys.stderr,
        )
        return set()
    held: dict[str, list[dict[str, object]]] | None = {}
    if activating(str(journal.get("operation", ""))):
        held = observe_held_releases(paths, str(journal["new_version"]))
    if prepared_activation and held is None:
        # The fence excludes new admissions, so the observation should have
        # been obtainable; without it nothing is provably unreferenced, and
        # every candidate stays for the next install to retry.
        print(
            "keeping every candidate release: no host observation under the maintenance fence; a referenced release is never deleted on a snapshot alone",
            file=sys.stderr,
        )
        return set()
    # The capability gate: only trees that provably honor the exclusion may
    # be cleaned up around. Anything else can admit a session that lands a
    # lease after every observation, so every candidate is retained.
    unfenceable = unfenceable_installed_trees(paths)
    if unfenceable is None:
        print(
            "keeping every candidate release: the installed release trees cannot be enumerated to prove every admission participant fenceable",
            file=sys.stderr,
        )
        return set()
    if unfenceable:
        print(
            "keeping every candidate release: installed release tree(s) cannot honor the shared session-admission exclusion - "
            + "; ".join(unfenceable)
            + "; remove or reinstall them with a current installer (the operator-owned offline bootstrap: no session may run during it), then re-run the install",
            file=sys.stderr,
        )
        return set()
    removed: set[str] = set()
    with admission_lock(paths):
        # One held exclusion across the final observation and every deletion
        # (CON-807). A compliant session lands its lease under this same
        # lock, so it either landed before the section — and the record read
        # below names it — or it waits until the section ends, after the
        # unreferenced trees are already gone.
        section_names = admission_record_names(paths)
        if section_names is None:
            print(
                "keeping every candidate release: the shared admission record is unreadable",
                file=sys.stderr,
            )
            return set()
        for version in sorted(candidates, key=version_sort_key):
            root = paths.data_root / version
            if not root.exists() and not root.is_symlink():
                continue
            resolved = str(root.resolve())
            if held is None:
                if version == replaced:
                    print(f"keeping release {version}: no host observation; a live session may still hold it", file=sys.stderr)
                    continue
            elif resolved in held:
                holders = describe_release_holders(held[resolved])
                print(f"keeping release {version}: a live session holds it ({holders})", file=sys.stderr)
                continue
            records = candidates[version]
            if not isinstance(records, dict) or not version_matches(root, records):
                raise InstallerError(f"transaction conflict at old version cleanup target {root}")
            # The admission record is read once, by content, inside the held
            # section: a lease a compliant session landed before the section
            # keeps its release, and an unreadable record retains the
            # candidate, because nothing is then provably unreferenced.
            admitted = admitted_release_roots(paths)
            if admitted is None:
                print(f"keeping release {version}: the shared admission record is unreadable", file=sys.stderr)
                continue
            if resolved in admitted:
                print(f"keeping release {version}: the admission record names it; a session was admitted after the observation", file=sys.stderr)
                continue
            names_now = admission_record_names(paths)
            if names_now is None or names_now != section_names:
                # A record landed inside the held exclusion, which a
                # compliant admission cannot do: an unfenceable participant
                # is writing admission state. Nothing further is provably
                # unreferenced, so every remaining candidate stays.
                print(
                    f"keeping release {version} and every later candidate: an admission record changed inside the held exclusion; an unfenceable participant is active",
                    file=sys.stderr,
                )
                break
            shutil.rmtree(root)
            removed.add(version)
    if removed:
        fsync_directory(paths.data_root)
    return removed


def version_sort_key(version: str) -> tuple[int, int, int]:
    match = VERSION_RE.fullmatch(version)
    if not match:
        raise InstallerError(f"invalid release version {version!r}")
    return int(match.group(1)), int(match.group(2)), int(match.group(3))


def complete_prepared_boundary(paths: Paths) -> None:
    """Discharge the prepared state once its activation committed: close the
    owned maintenance fence, then drop the prepared-release record. Both
    steps are idempotent, so a recovery that already ran one converges.

    Closing is by durable recorded identity only (CON-807): the one boundary
    this record names through boundary_fence_id. An explicit caller-passed
    identity authorizes nothing — a fence observed in place but never
    recorded is foreign or of no provable owner, and is retained untouched
    for the operator's offline bootstrap, never closed by this installer's
    guess. Callers gate the discharge on the committed manifest naming the
    record's version or its recorded boundary_owner_version. The record is
    removed only after the fence it owns is closed, so a crash between the
    two steps always leaves a recorded owner for whatever is still open.
    """
    record = load_prepared_release(paths)
    if record is None:
        return
    close_id = record.get("boundary_fence_id")
    if isinstance(close_id, str) and close_id:
        remove_maintenance_fence(paths, close_id)
    clear_prepared_release(paths)


def record_boundary_ownership(paths: Paths, record: dict[str, object], fence_id: str, owner_version: str) -> dict[str, object]:
    """Record, durably and before any activation swap, the boundary this
    transaction adopted and the release whose committed activation discharges
    it (CON-807).

    The prepared record — not the transaction journal — is the durable owner
    of an adopted boundary: cleanup removes the journal inside the same call
    that finishes the transaction, so a crash after cleanup leaves the record
    as the only recoverable owner. Writing the adoption here, before the
    transaction starts, means every recovery path (activation, superseding
    install, status) can close exactly this boundary by identity.
    """
    updated = dict(record)
    updated["boundary_fence_id"] = fence_id
    updated["boundary_owner_version"] = owner_version
    write_prepared_release(paths, updated)
    return updated


def claim_open_fence(
    paths: Paths,
    notice: str,
    owner_roots: set[str],
) -> dict[str, object] | None:
    """Claim the open maintenance fence for an activating transaction.

    An open boundary is claimable only when it is provably this upgrade
    path's own: attributed by release_root to the candidate this transaction
    activates or the prepared candidate it supersedes — exactly the
    attribution the core's migration command writes when it opens the
    boundary (hostlease.EnsureFence stamps the migrating binary's release
    root). An unattributed boundary has no provable owner: a prepared record
    happening to exist does not make it ours, and claiming it would let this
    transaction later close a boundary another operation (or an orphan)
    holds. Unattributed and foreign-attributed boundaries are both refused
    and retained untouched (CON-807). Creating a fence here is never
    allowed: adoption happens only when one is already open.
    """
    if not maintenance_fence_path(paths).exists():
        return None
    fence = ensure_maintenance_fence(paths, notice)
    require_owned_fence(paths, fence, owner_roots)
    return fence


def prepared_record_matches(journal: dict[str, object], paths: Paths) -> bool:
    """Whether the on-disk prepared record commits this transaction's candidate."""
    record = load_prepared_release(paths)
    if record is None:
        return False
    return record.get("version") == journal.get("new_version") and record.get("version_files") == journal.get("new_version_records")


# The forward-resume order after the version tree is in place. Each step is
# idempotent per file, so a crash inside a step re-runs it safely.
FORWARD_PHASE_STEPS: tuple[tuple[str, str, str], ...] = (
    ("version_activated", "agents_swapped", "apply_agents"),
    ("agents_swapped", "adapter_swapped", "apply_adapters"),
    ("adapter_swapped", "launcher_swapped", "apply_launcher"),
    ("launcher_swapped", "config_swapped", "apply_config"),
    ("config_swapped", "manifest_committed", "apply_manifest"),
)


def resume_forward(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    """Complete an activation transaction forward from its journal phase.

    Used only for a prepared activation (CON-807): the incompatible
    migration already committed, so the older release is unusable and
    rollback is forbidden — recovery resumes the prepared candidate. The
    version tree must already match the journal's records; each remaining
    swap is idempotent, and the walk advances the journal after each one.
    """
    phase = journal["phase"]
    if phase == "staged":
        apply_version(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "version_activated")
        phase = "version_activated"
    else:
        version = journal["new_version"]
        records = journal["new_version_records"]
        if not version_matches(paths.data_root / str(version), records):
            raise InstallerError(f"transaction conflict at {paths.data_root / str(version)}; refusing forward recovery")
        apply_stable_root(transaction_root, journal, paths)
    applies = {
        "apply_agents": apply_agents,
        "apply_adapters": apply_adapters,
        "apply_launcher": apply_launcher,
        "apply_config": apply_config,
        "apply_manifest": apply_manifest,
    }
    for current, target, name in FORWARD_PHASE_STEPS:
        if phase != current:
            continue
        applies[name](transaction_root, journal, paths)
        advance_phase(transaction_root, journal, target)
        phase = target
    verify_states(journal, paths, committed=True)
    advance_phase(transaction_root, journal, "cleanup")
    cleanup_transaction(transaction_root, journal, paths)
    # The durable prepared record owns the adopted boundary (written before
    # the transaction started); the journal identity it carries must agree.
    fence_id = journal.get("maintenance_fence_id")
    if isinstance(fence_id, str) and fence_id:
        record = load_prepared_release(paths)
        if record is not None and record.get("boundary_fence_id") not in (None, fence_id):
            raise InstallerError("the transaction journal and the prepared record name different boundaries; refusing recovery")
    complete_prepared_boundary(paths)
    link_worktrees_root(paths)


def recover_transaction(transaction_root: Path, paths: Paths) -> None:
    if transaction_root.is_symlink() or journal_path(transaction_root).is_symlink():
        raise InstallerError(f"refusing symlinked transaction target {transaction_root}")
    try:
        journal = json.loads(journal_path(transaction_root).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise InstallerError(f"cannot read transaction journal {journal_path(transaction_root)}: {error}") from error
    if not isinstance(journal, dict):
        raise InstallerError(f"refusing malformed transaction journal {journal_path(transaction_root)}")
    validate_transaction(journal, transaction_root, paths)
    phase = journal["phase"]
    new_manifest = journal["new_manifest"]
    current_manifest_state(paths)
    # A committed prepared state recovers by cleaning the scaffolding only:
    # the candidate tree and the prepared-release record are the committed
    # state, and no activation swap may follow (CON-807).
    if phase == "prepared":
        if not prepared_record_matches(journal, paths):
            raise InstallerError("prepared transaction does not match the prepared-release record; refusing recovery")
        cleanup_transaction(transaction_root, journal, paths, remove_old_version=False)
        return
    if phase not in {"manifest_committed", "cleanup"} and new_manifest.get("exists") and state_matches(paths.data_root / MANIFEST_NAME, new_manifest):
        advance_phase(transaction_root, journal, "manifest_committed")
        phase = "manifest_committed"
    if phase in {"manifest_committed", "cleanup"}:
        verify_states(journal, paths, committed=True)
        if phase != "cleanup":
            advance_phase(transaction_root, journal, "cleanup")
        prepared_activation = journal.get("prepared_activation") is True
        fence_id = journal.get("maintenance_fence_id")
        if prepared_activation and isinstance(fence_id, str) and fence_id:
            record = load_prepared_release(paths)
            if record is not None and record.get("boundary_fence_id") not in (None, fence_id):
                raise InstallerError("the transaction journal and the prepared record name different boundaries; refusing recovery")
        cleanup_transaction(transaction_root, journal, paths)
        if prepared_activation:
            # Closing is by the durable recorded identity; a fence the
            # record never named is not this recovery's to remove.
            complete_prepared_boundary(paths)
        if activating(str(journal["operation"])):
            link_worktrees_root(paths)
        return
    # A prepared activation never rolls back: the incompatible migration
    # already committed, and the older release it would restore cannot open
    # the store. Recovery resumes the candidate forward instead (CON-807).
    # The authorization is the transaction's own durable prepared-activation
    # phase, written by the operation itself only once it owns this upgrade
    # path — claimed boundary or superseding prepared record — and always
    # before the first activation effect. A boundary merely observed in
    # place authorizes nothing: a fence a refused or uncertain transaction
    # never recorded is foreign or of no provable owner, and a pre-effect
    # refusal must never later activate (CON-807).
    if activating(str(journal["operation"])) and journal.get("prepared_activation") is True:
        resume_forward(transaction_root, journal, paths)
        return
    # An install that crashed between placing the prepared candidate and
    # committing the prepared record converges to the prepared state when
    # the record landed anyway: the alternative rollback would discard a
    # durable prepare the operator was already told about.
    if activating(str(journal["operation"])) and phase == "staged" and prepared_record_matches(journal, paths):
        advance_phase(transaction_root, journal, "prepared")
        cleanup_transaction(transaction_root, journal, paths, remove_old_version=False)
        return
    ensure_rollback_safe(journal, paths)
    rollback_version(transaction_root, journal, paths)
    for name in ADAPTER_FILES:
        state = journal["old_adapter"][name]
        restore_file(paths.tools_dir / name, state, transaction_root, f"adapter/{name}")
    old_agents = journal.get("old_agents") or {}
    if isinstance(old_agents, dict):
        for name, state in old_agents.items():
            if isinstance(name, str) and isinstance(state, dict):
                restore_file(paths.agents_dir / name, state, transaction_root, f"agents/{name}")
    # On rollback, anything apply_agents may have placed but old_agents did not
    # record (i.e. brand-new files in this release) would otherwise be left
    # orphaned in the central agents dir. The preflight refuses user-authored
    # files there, so anything we wrote under this transaction is safe to
    # remove. Build the orphan set from new_agents (those we may have placed)
    # minus old_agents (those we captured before).
    new_agents = journal.get("new_agents") or {}
    if isinstance(new_agents, dict):
        for name in list(new_agents):
            if name in old_agents:
                continue
            target = paths.agents_dir / name
            if target.exists() or target.is_symlink():
                target.unlink()
    restore_launcher(paths, journal["old_launcher"])
    restore_stable_root(paths, journal["old_stable_root"])
    restore_file(paths.config_file, journal["old_config"], transaction_root, "config")
    restore_file(paths.data_root / MANIFEST_NAME, journal["old_manifest"], transaction_root, "manifest")
    verify_states(journal, paths, committed=False)
    journal["phase"] = "rollback"
    write_journal(transaction_root, journal)
    cleanup_transaction(transaction_root, journal, paths, remove_old_version=False)


def recover_transactions(paths: Paths) -> None:
    parent = paths.data_root / TRANSACTION_PARENT
    if parent.is_symlink():
        raise InstallerError(f"refusing symlinked transaction directory {parent}")
    if not parent.exists():
        return
    entries = list(parent.iterdir())
    if any(path.is_symlink() or not path.is_dir() for path in entries):
        raise InstallerError(f"refusing unknown transaction entry under {parent}")
    transactions = sorted(entries)
    if len(transactions) > 1:
        raise InstallerError(f"multiple incomplete Concord transactions require manual recovery: {parent}")
    if transactions:
        transaction = transactions[0]
        if not journal_path(transaction).exists():
            shutil.rmtree(transaction)
            fsync_directory(parent)
        else:
            recover_transaction(transaction, paths)


def install(args: argparse.Namespace) -> int:
    version = parse_version(args.version) if args.version else resolve_latest_version(Path(args.artifact_dir).resolve() if args.artifact_dir else None, args.base_url)
    download_base_url = release_download_base_url(args.base_url, version) if args.version else args.base_url
    paths = paths_for(args.root)
    recover_transactions(paths)
    recover_pending_project_links(paths)
    manifest = load_manifest(paths)
    prepared = load_prepared_release(paths)
    config_plan = preflight(paths, version, manifest)
    if manifest and manifest.get("version") == version:
        records = manifest.get("version_files")
        if not isinstance(records, dict):
            raise InstallerError("existing installer manifest has invalid version file records")
        validate_owned_tree(paths.data_root / version, records)
        adapter_records = managed_adapter_records(manifest)
        for relative, expected in adapter_records.items():
            destination = paths.tools_dir / relative
            if not destination.is_file() or sha256(destination) != expected:
                raise InstallerError(f"refusing to overwrite modified managed adapter file {destination}")
        agent_records = managed_agent_records(manifest)
        for name, expected in agent_records.items():
            destination = paths.agents_dir / name
            if not destination.is_file() or sha256(destination) != expected:
                raise InstallerError(f"refusing to overwrite modified managed agent file {destination}")
        if not paths.launcher.is_symlink() or os.readlink(paths.launcher) != manifest.get("launcher_target"):
            raise InstallerError(f"refusing to repair modified managed launcher {paths.launcher}")
        if not paths.stable_root.is_symlink() or os.readlink(paths.stable_root) != str(paths.data_root / version):
            raise InstallerError(f"refusing to repair modified managed stable root {paths.stable_root}")
        skill_path = stable_skill_path(paths)
        if config_plan.changed or manifest.get("skill_path") != skill_path:
            raise InstallerError("existing installation registration is incomplete; refusing an unsafe repair")
        credential_directory_owned = ensure_secret_service_ready(paths, manifest)
        # An unchanged install still retries the release cleanup a previous
        # install skipped: a held release stays a candidate, so the removal
        # is retried at the next install (CD-0111 D2). The retry runs through
        # the one lease-aware cleanup path, so the same held admission
        # exclusion covers the final observation and the deletion here too
        # (CON-807): a same-version reinstall deletes nothing a concurrent
        # or later admission holds. The manifest keeps the entries whose
        # directory survives.
        retained = retained_release_records(manifest)
        manifest_changed = False
        if retained:
            removed_versions = remove_unheld_releases(
                {"operation": "install", "new_version": version, "cleanup_candidates": retained},
                paths,
            )
            survivors = {
                candidate: records
                for candidate, records in retained.items()
                if candidate not in removed_versions and (paths.data_root / candidate).exists()
            }
            if survivors != retained:
                manifest["retained_releases"] = survivors
                manifest_changed = True
        # A prepared record naming the now-active release — directly or as
        # the recorded boundary owner — means an activation committed and
        # only its discharge crashed: the durable record closes its own
        # boundary by its recorded identity, and a fence no record names is
        # foreign and stays for the operator (CON-807).
        if prepared is not None and (
            manifest.get("version") == prepared.get("version")
            or manifest.get("version") == prepared.get("boundary_owner_version")
        ):
            complete_prepared_boundary(paths)
            if maintenance_fence_path(paths).exists() or maintenance_fence_path(paths).is_symlink():
                print(
                    "An open maintenance boundary remains that no prepared record owns; "
                    "it is not this installer's to close. Close it through the operator-owned "
                    "offline bootstrap when no migration is in progress."
                )
        if credential_directory_owned and manifest.get("credential_directory") != str(paths.data_home / "keyrings"):
            manifest["credential_directory"] = str(paths.data_home / "keyrings")
            manifest_changed = True
        if manifest_changed:
            manifest_bytes = json.dumps(manifest, indent=2, sort_keys=True) + "\n"
            write_atomic(paths.data_root / MANIFEST_NAME, manifest_bytes.encode("utf-8"))
        linked_worktrees = link_worktrees_root(paths)
        if linked_worktrees:
            print(f"Concord {version} was already installed; restored worktree project conduct links.")
        else:
            print(f"Concord {version} is already installed; no changes made.")
        return 0

    with tempfile.TemporaryDirectory(prefix="concord-installer-") as temporary:
        workspace = Path(temporary)
        extracted, _checksums = extract_verified_artifact(
            version,
            Path(args.artifact_dir).resolve() if args.artifact_dir else None,
            download_base_url,
            workspace,
        )
        source_stage = workspace / "version"
        (source_stage / "bin").mkdir(parents=True)
        (source_stage / "adapter" / "opencode").mkdir(parents=True)
        shutil.copy2(extracted / "bin" / "concord", source_stage / "bin" / "concord")
        os.chmod(source_stage / "bin" / "concord", 0o755)
        for name in ADAPTER_FILES:
            shutil.copy2(extracted / "adapter" / "opencode" / name, source_stage / "adapter" / "opencode" / name)
        version_root = paths.data_root / version
        stamp_release_constants(source_stage / "adapter" / "opencode", version_root.resolve())
        # The staged tree declares the fence protocol its core speaks, so
        # the migration's boundary can exclude sessions this tree admits
        # (CON-807). The marker is a managed version file like any other.
        stamp_fence_protocol(source_stage, version)
        # skills, instructions, and agents are copied only when the archive
        # carries them. The three branches share the helper so a future
        # required surface cannot drift from the others.
        copy_tree_if_present(extracted / "skills", source_stage / "skills")
        copy_tree_if_present(extracted / "instructions", source_stage / "instructions")
        copy_tree_if_present(extracted / "agents", source_stage / "agents")
        fsync_tree(source_stage)
        managed_version_paths = [str(path.relative_to(source_stage)) for path in source_stage.rglob("*") if path.is_file()]
        version_records = file_records(source_stage, managed_version_paths)
        adapter_stage_records = {name: sha256(source_stage / "adapter" / "opencode" / name) for name in ADAPTER_FILES}
        agent_stage_records = {
            path.name: sha256(path)
            for path in sorted((source_stage / "agents").glob(AGENT_GLOB))
        }
        old_version = manifest.get("version") if manifest else None
        old_records = manifest.get("version_files", {}) if manifest else None
        # CD-0111 D2: every release the installer still owns besides the new
        # one is a cleanup candidate. Candidates already gone leave the set;
        # the replaced release joins it; a candidate this install reinstalls
        # is owned by its recorded files and leaves the set.
        retained = {
            candidate: records
            for candidate, records in retained_release_records(manifest).items()
            if (paths.data_root / candidate).exists()
        }
        reinstalled_records = retained.pop(version, None)
        # A prepare of this same version owns the tree through its recorded
        # files, so reinstalling over it is an activation of the prepared
        # candidate, not an overwrite of an unmanaged path.
        if reinstalled_records is None and prepared is not None and prepared.get("version") == version:
            reinstalled_records = prepared["version_files"]
        # A prepared candidate a different version supersedes becomes a
        # cleanup candidate: it never served a session, and the committed
        # install drops its record and closes any open boundary.
        if prepared is not None and prepared.get("version") != version:
            superseded = str(prepared["version"])
            if (paths.data_root / superseded).exists() and superseded not in retained:
                retained[superseded] = prepared["version_files"]
        if version_root.exists() and (not manifest or old_version != version):
            if reinstalled_records is None:
                raise InstallerError(f"refusing to overwrite existing unmanaged path {version_root}")
            validate_owned_tree(version_root, reinstalled_records)
        if manifest and old_version != version and old_version and not isinstance(old_records, dict):
            raise InstallerError("existing installer manifest has invalid version records")
        if manifest and old_version != version and isinstance(old_version, str) and isinstance(old_records, dict):
            validate_owned_tree(paths.data_root / old_version, old_records)
            retained[old_version] = old_records
        plan_worktree_links(paths)
        credential_directory_owned = ensure_secret_service_ready(paths, manifest)
        new_manifest = {
            "managed_by": "concord-installer-v1",
            "version": version,
            "version_files": version_records,
            "adapter_files": adapter_stage_records,
            "agent_files": agent_stage_records,
            "skill_path": stable_skill_path(paths),
            "stable_root": str(paths.stable_root),
            "launcher_target": str((version_root / "bin" / "concord").resolve()),
            "config_path": str(paths.config_file.resolve()),
            "retained_releases": retained,
        }
        if credential_directory_owned:
            new_manifest["credential_directory"] = str(paths.data_home / "keyrings")
        new_manifest_bytes = (json.dumps(new_manifest, indent=2, sort_keys=True) + "\n").encode("utf-8")
        transaction_root, journal = make_transaction(
            paths,
            "install",
            manifest,
            version,
            source_stage,
            version_records,
            adapter_stage_records,
            agent_stage_records,
            config_plan.text,
            new_manifest_bytes,
            retained,
            reinstalled_records,
        )
        # CON-807 readiness gate: the candidate's own core reports, by
        # reading the store from the transaction stage, whether this
        # activation may proceed — before any placement touches the active
        # release. A blocked or unknown plan places the candidate tree
        # durably without activation: the active launcher, current root,
        # tools, and agents keep serving the running sessions, and the
        # operator receives the exact migration and activation commands.
        try:
            plan_report: dict[str, object] | None = read_upgrade_plan(
                paths,
                version,
                staged_binary=transaction_root / "stage" / "version" / "bin" / "concord",
            )
            blockers = plan_blockers(plan_report)
        except InstallerError as error:
            plan_report = None
            blockers = [str(error)]
        if plan_report is None or plan_report.get("activation_blocked"):
            # An open boundary means an incompatible migration may already
            # have committed for the prepared candidate. Replacing that
            # record would strand the store with no release recorded to
            # activate, so a blocked or unknown install refuses and leaves
            # the record and the boundary for the original activation.
            if maintenance_fence_path(paths).exists() or maintenance_fence_path(paths).is_symlink():
                pending = f" Run its activation command first: {prepared['activation_command']}" if prepared else ""
                raise InstallerError(
                    f"an open maintenance boundary sits at {maintenance_fence_path(paths)} and {version} cannot "
                    f"activate ({'; '.join(blockers) or 'readiness unknown'}); refusing to replace the prepared "
                    f"release while the boundary is open.{pending}"
                )
            place_prepared_version(transaction_root, journal, paths)
            prepare_release(
                transaction_root,
                journal,
                paths,
                version,
                version_records,
                blockers,
                plan_migration_command(paths, version),
                args.root,
            )
            record = load_prepared_release(paths)
            assert record is not None
            print_prepared_release(record)
            return 0
        # An install that activates over its own prepared candidate is the
        # prepared activation by another route: whether the operator runs
        # the recorded activate command or re-runs install after the
        # migration, recovery resumes the candidate forward and never
        # restores the older release (CON-807).
        #
        # An open boundary is claimable only through the prepared record
        # that owns its upgrade path. With no record, an open fence is
        # another operation's or an orphan: the install refuses rather than
        # adopt an exclusion it may never close, and the operator closes an
        # orphan through the documented offline bootstrap (CON-807).
        adopted_fence = None
        if maintenance_fence_path(paths).exists() or maintenance_fence_path(paths).is_symlink():
            if prepared is None:
                raise InstallerError(
                    f"an open maintenance boundary sits at {maintenance_fence_path(paths)} and no prepared "
                    "release owns its upgrade path; another operation may be mid-maintenance. Run status; an "
                    "orphaned boundary closes only through the operator-owned offline bootstrap."
                )
            candidate_roots = {str(version_root.resolve())}
            superseded_root = paths.data_root / str(prepared["version"])
            candidate_roots.add(str(superseded_root.resolve()))
            adopted_fence = claim_open_fence(
                paths,
                f"session admission reopens when {version} activates; activation command: {activation_command_for(version, args.root)}",
                candidate_roots,
            )
            assert adopted_fence is not None
            # The durable owner is written before the transaction starts:
            # cleanup removes the journal inside this same flow, so the
            # record is the only owner a post-cleanup crash can recover
            # (CON-807). boundary_owner_version names this install's
            # release, whose committed activation discharges the boundary.
            prepared = record_boundary_ownership(paths, prepared, str(adopted_fence["fence_id"]), version)
        if prepared is not None and prepared.get("version") == version:
            journal["prepared_activation"] = True
        if adopted_fence is not None:
            # An open boundary commits this activation to forward recovery
            # as well, including when it supersedes the prepared candidate
            # the migration belonged to: the store may already be past the
            # older release's last compatible step, so rollback has no
            # provably usable target (CON-807).
            journal["prepared_activation"] = True
            journal["maintenance_fence_id"] = adopted_fence["fence_id"]
        if "prepared_activation" in journal or "maintenance_fence_id" in journal:
            write_journal(transaction_root, journal)
        apply_version(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "version_activated")
        apply_agents(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "agents_swapped")
        apply_adapters(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "adapter_swapped")
        apply_launcher(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "launcher_swapped")
        apply_config(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "config_swapped")
        apply_manifest(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "manifest_committed")
        advance_phase(transaction_root, journal, "cleanup")
        verify_states(journal, paths, committed=True)
        cleanup_transaction(transaction_root, journal, paths)
    # This activation completes whatever prepared boundary was open: the
    # record is discharged or superseded, and session admission reopens
    # with a usable active release (CON-807). Closing is by the durable
    # recorded identity only; a fence the record never named is retained.
    if prepared is not None:
        complete_prepared_boundary(paths)
    link_worktrees_root(paths)
    print(f"Installed Concord {version} under {version_root}.")
    print(f"OpenCode custom tools installed under {paths.tools_dir}.")
    print(f"Concord agent definitions installed under {paths.agents_dir}.")
    print("Restart OpenCode before using the newly registered stable skills path.")
    return 0


def activate(args: argparse.Namespace) -> int:
    """Activate the prepared release after the operator's migration (CON-807).

    The prepared record names the candidate, the exact migration command,
    and this activation command. Activation requires the candidate's own
    readiness plan to read unblocked — the migration must have committed —
    and holds the maintenance fence from before its final lease observation
    until the candidate committed, release cleanup finished, and the record
    is discharged. It never downloads: the prepared tree is the verified
    asset. Recovery of a crashed activation resumes the candidate forward
    and never restores the older release the migration made unusable.
    """
    paths = paths_for(args.root)
    recover_transactions(paths)
    recover_pending_project_links(paths)
    manifest = load_manifest(paths)
    prepared = load_prepared_release(paths)
    if prepared is None:
        # A recovery may have already completed and discharged this
        # activation; re-running the recorded command stays a success. A
        # boundary still open after that carries no recorded owner this
        # command can prove: it is foreign or uncertain, so it is retained
        # for the operator (the offline bootstrap closes it), never closed
        # by position (CON-807).
        requested = parse_version(args.version) if args.version else None
        if requested is not None and manifest is not None and manifest.get("version") == requested:
            if maintenance_fence_path(paths).exists() or maintenance_fence_path(paths).is_symlink():
                print(
                    "An open maintenance boundary remains and no prepared record owns it; "
                    "it is not this command's to close. Close it through the operator-owned "
                    "offline bootstrap when no migration is in progress."
                )
            print(f"Concord {requested} is already the active release; the prepared record is discharged.")
            return 0
        raise InstallerError(f"no prepared release is recorded at {prepared_release_path(paths)}; run install first")
    version = str(prepared["version"])
    if args.version and parse_version(args.version) != version:
        raise InstallerError(f"the prepared release is {version}, not {args.version}; refusing to activate a different release")
    if manifest is not None and manifest.get("version") == version:
        # Idempotent completion: a recovery already committed this
        # activation, or the operator activated it by reinstalling. The
        # record discharges and closes the boundary it owns by identity; a
        # fence no record of this discharge owns is foreign and stays in
        # place (CON-807).
        complete_prepared_boundary(paths)
        if maintenance_fence_path(paths).exists() or maintenance_fence_path(paths).is_symlink():
            print(
                "An open maintenance boundary remains that this activation does not own; "
                "it is retained for the operator (the offline bootstrap closes it)."
            )
        print(f"Concord {version} is already the active release; the prepared record is discharged.")
        return 0
    version_root = paths.data_root / version
    version_records = prepared["version_files"]
    assert isinstance(version_records, dict)
    validate_owned_tree(version_root, version_records)
    plan_report = read_upgrade_plan(paths, version)
    if plan_report.get("activation_blocked"):
        print("The prepared release is still blocked; the active release is unchanged.")
        for blocker in plan_blockers(plan_report):
            print(f"Blocker: {blocker}")
        print(f"Run the migration command inside the maintenance window: {prepared['migration_command']}")
        print(f"Then run the activation command: {prepared['activation_command']}")
        return 1
    # The boundary the migration command opened is re-ensured before the
    # final lease observation, and closes only after commit and cleanup.
    # A boundary attributed to a different release root belongs to another
    # operation's upgrade path: the activation refuses rather than adopt an
    # exclusion it may never close (CON-807). Its identity is recorded in
    # the prepared record before the transaction starts, so any crash
    # afterward recovers an owner for the fence it must close — never a
    # foreign one (CON-807).
    version_root_resolved = str(version_root.resolve())
    fence = ensure_maintenance_fence(
        paths,
        f"session admission reopens when {version} activates; activation command: {prepared['activation_command']}",
        release_root=version_root_resolved,
    )
    require_owned_fence(paths, fence, {version_root_resolved})
    prepared = record_boundary_ownership(paths, prepared, str(fence["fence_id"]), version)
    config_plan = preflight(paths, version, manifest)
    with tempfile.TemporaryDirectory(prefix="concord-activator-") as temporary:
        source_stage = Path(temporary) / "version"
        shutil.copytree(version_root, source_stage, symlinks=False)
        fsync_tree(source_stage)
        adapter_stage_records = {name: sha256(source_stage / "adapter" / "opencode" / name) for name in ADAPTER_FILES}
        agent_stage_records = {
            path.name: sha256(path)
            for path in sorted((source_stage / "agents").glob(AGENT_GLOB))
        }
        old_version = manifest.get("version") if manifest else None
        old_records = manifest.get("version_files", {}) if manifest else None
        retained = {
            candidate: records
            for candidate, records in retained_release_records(manifest).items()
            if (paths.data_root / candidate).exists()
        }
        if manifest and old_version != version and isinstance(old_version, str) and isinstance(old_records, dict):
            validate_owned_tree(paths.data_root / old_version, old_records)
            retained[old_version] = old_records
        plan_worktree_links(paths)
        credential_directory_owned = ensure_secret_service_ready(paths, manifest)
        new_manifest = {
            "managed_by": "concord-installer-v1",
            "version": version,
            "version_files": version_records,
            "adapter_files": adapter_stage_records,
            "agent_files": agent_stage_records,
            "skill_path": stable_skill_path(paths),
            "stable_root": str(paths.stable_root),
            "launcher_target": str((version_root / "bin" / "concord").resolve()),
            "config_path": str(paths.config_file.resolve()),
            "retained_releases": retained,
        }
        if credential_directory_owned:
            new_manifest["credential_directory"] = str(paths.data_home / "keyrings")
        new_manifest_bytes = (json.dumps(new_manifest, indent=2, sort_keys=True) + "\n").encode("utf-8")
        transaction_root, journal = make_transaction(
            paths,
            "activate",
            manifest,
            version,
            source_stage,
            version_records,
            adapter_stage_records,
            agent_stage_records,
            config_plan.text,
            new_manifest_bytes,
            retained,
            version_records,
            # Candidate-forward recovery: a crash anywhere in this
            # transaction resumes the prepared candidate and never restores
            # the older release. The journal carries the adopted fence
            # identity so the recovered run closes exactly that boundary.
            recovery_marks={"prepared_activation": True, "maintenance_fence_id": fence["fence_id"]},
        )
        apply_version(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "version_activated")
        apply_agents(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "agents_swapped")
        apply_adapters(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "adapter_swapped")
        apply_launcher(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "launcher_swapped")
        apply_config(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "config_swapped")
        apply_manifest(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "manifest_committed")
        advance_phase(transaction_root, journal, "cleanup")
        verify_states(journal, paths, committed=True)
        cleanup_transaction(transaction_root, journal, paths)
    complete_prepared_boundary(paths)
    link_worktrees_root(paths)
    print(f"Activated the prepared Concord {version} under {version_root}.")
    print("Session admission reopened; new sessions start on the activated release.")
    return 0


def plan_repair(
    paths: Paths,
    manifest: dict[str, object],
    config_plan: ConfigPlan,
    version_records: dict[str, str],
    adapter_records: dict[str, str],
    agent_records: dict[str, str],
) -> set[str]:
    """Diff the deployment against the verified release and list repairs.

    A managed file is restorable when it is absent, or when its content is
    what the installed manifest recorded and the release ships something
    else. It is modified when it differs from both the manifest and the
    release, and repair refuses rather than overwrite an operator change.
    Unknown files refuse for the same reason install refuses them.
    """
    installed = manifest.get("version")
    if not isinstance(installed, str):
        raise InstallerError("existing installer manifest has no version to repair")
    version_root = paths.data_root / installed
    manifest_version_files = manifest.get("version_files")
    if not isinstance(manifest_version_files, dict):
        manifest_version_files = {}
    manifest_adapter = managed_adapter_records(manifest)
    manifest_agents = managed_agent_records(manifest)
    present = {str(path.relative_to(version_root)) for path in version_root.rglob("*") if path.is_file()}
    for relative in sorted(present):
        if relative not in version_records:
            raise InstallerError(
                f"refusing to replace managed path {version_root / relative}; "
                "unknown file outside the release's module graph"
            )
    repairs: set[str] = set()
    for relative, expected in version_records.items():
        target = version_root / relative
        if not target.exists():
            repairs.add(f"restored {relative}")
            continue
        digest = sha256(target)
        if digest == expected:
            continue
        if digest == manifest_version_files.get(relative):
            repairs.add(f"restored {relative}")
            continue
        raise InstallerError(f"refusing to repair modified managed file {target}")
    for name, expected in adapter_records.items():
        target = paths.tools_dir / name
        if not target.exists():
            repairs.add(f"restored adapter {name}")
            continue
        if not target.is_file() or target.is_symlink():
            raise InstallerError(f"refusing to repair managed adapter path {target}: it is not a regular file")
        digest = sha256(target)
        if digest == expected:
            continue
        if digest == manifest_adapter.get(name):
            repairs.add(f"restored adapter {name}")
            continue
        raise InstallerError(f"refusing to repair modified managed file {target}")
    agent_names = set(manifest_agents) | set(agent_records)
    for name in sorted(agent_names):
        expected = agent_records.get(name)
        target = paths.agents_dir / name
        if expected is None:
            if target.exists() or target.is_symlink():
                raise InstallerError(f"refusing to remove managed agent file {target}; the release no longer ships it")
            continue
        if not target.exists():
            repairs.add(f"restored agent {name}")
            continue
        if not target.is_file() or target.is_symlink():
            raise InstallerError(f"refusing to repair managed agent path {target}: it is not a regular file")
        digest = sha256(target)
        if digest == expected:
            continue
        if digest == manifest_agents.get(name):
            repairs.add(f"restored agent {name}")
            continue
        raise InstallerError(f"refusing to repair modified managed file {target}")
    if not paths.launcher.exists() and not paths.launcher.is_symlink():
        repairs.add("restored the launcher")
    if not paths.stable_root.exists() and not paths.stable_root.is_symlink():
        repairs.add("restored the stable root")
    if config_plan.changed:
        repairs.add("restored the OpenCode registration")
    worktree_links = plan_worktree_links(paths)
    _, _, legacy_changed = plan_worktrees_root_unlink(paths)
    if any(changed for _, _, changed in worktree_links) or legacy_changed:
        repairs.add("restored worktree project conduct links")
    return repairs


def repair(args: argparse.Namespace) -> int:
    """Complete an incomplete installation of the installed release.

    The installed manifest says which release owns the deployment; the
    verified release archive says which files that release requires. Repair
    converges the deployment to the archive through the same journaled
    transaction install uses. It never upgrades or downgrades: a --version
    that disagrees with the installed release refuses. The work database,
    worktrees, credentials, and user configuration are outside the managed
    paths and are never touched.
    """
    paths = paths_for(args.root)
    recover_transactions(paths)
    recover_pending_project_links(paths)
    manifest = load_manifest(paths)
    if manifest is None:
        raise InstallerError(f"no installer manifest at {paths.data_root / MANIFEST_NAME}; run install")
    # CON-807: after a committed incompatible migration the prepared
    # candidate is the only forward route. Repairing the installed older
    # release would restore a release the store can no longer open, so it
    # refuses and names the activation command instead.
    prepared = load_prepared_release(paths)
    if prepared is not None:
        raise InstallerError(
            f"a prepared release {prepared['version']} waits for activation; run: {prepared['activation_command']}"
        )
    if maintenance_fence_path(paths).exists() or maintenance_fence_path(paths).is_symlink():
        raise InstallerError(
            "a maintenance boundary is open; complete the prepared activation before repairing the installed release"
        )
    installed = manifest.get("version")
    if not isinstance(installed, str):
        raise InstallerError("existing installer manifest has no version to repair; run install")
    requested = parse_version(args.version) if args.version else installed
    if requested != installed:
        raise InstallerError(
            f"repair keeps the installed release {installed}; "
            f"install --version {requested} changes releases"
        )
    version_root = paths.data_root / installed
    if not version_root.exists() or version_root.is_symlink():
        raise InstallerError(f"installed release directory is missing: {version_root}; run install")
    with tempfile.TemporaryDirectory(prefix="concord-repair-") as temporary:
        workspace = Path(temporary)
        extracted, _checksums = extract_verified_artifact(
            installed,
            Path(args.artifact_dir).resolve() if args.artifact_dir else None,
            release_download_base_url(args.base_url, installed),
            workspace,
        )
        source_stage = workspace / "version"
        (source_stage / "bin").mkdir(parents=True)
        (source_stage / "adapter" / "opencode").mkdir(parents=True)
        shutil.copy2(extracted / "bin" / "concord", source_stage / "bin" / "concord")
        os.chmod(source_stage / "bin" / "concord", 0o755)
        for name in ADAPTER_FILES:
            shutil.copy2(extracted / "adapter" / "opencode" / name, source_stage / "adapter" / "opencode" / name)
        stamp_release_constants(source_stage / "adapter" / "opencode", version_root.resolve())
        # Repair restages the tree exactly as install staged it, fence
        # protocol marker included (CON-807).
        stamp_fence_protocol(source_stage, installed)
        copy_tree_if_present(extracted / "skills", source_stage / "skills")
        copy_tree_if_present(extracted / "instructions", source_stage / "instructions")
        copy_tree_if_present(extracted / "agents", source_stage / "agents")
        fsync_tree(source_stage)
        managed_version_paths = [str(path.relative_to(source_stage)) for path in source_stage.rglob("*") if path.is_file()]
        version_records = file_records(source_stage, managed_version_paths)
        adapter_stage_records = {name: sha256(source_stage / "adapter" / "opencode" / name) for name in ADAPTER_FILES}
        agent_stage_records = {
            path.name: sha256(path)
            for path in sorted((source_stage / "agents").glob(AGENT_GLOB))
        }
        config_plan = preflight(paths, installed, manifest, staged_adapters=adapter_stage_records)
        repairs = plan_repair(paths, manifest, config_plan, version_records, adapter_stage_records, agent_stage_records)
        credential_directory_owned = ensure_secret_service_ready(paths, manifest)
        new_manifest = {
            "managed_by": "concord-installer-v1",
            "version": installed,
            "version_files": version_records,
            "adapter_files": adapter_stage_records,
            "agent_files": agent_stage_records,
            "skill_path": stable_skill_path(paths),
            "stable_root": str(paths.stable_root),
            "launcher_target": str((version_root / "bin" / "concord").resolve()),
            "config_path": str(paths.config_file.resolve()),
            "retained_releases": retained_release_records(manifest),
        }
        if credential_directory_owned:
            new_manifest["credential_directory"] = str(paths.data_home / "keyrings")
        new_manifest_bytes = (json.dumps(new_manifest, indent=2, sort_keys=True) + "\n").encode("utf-8")
        manifest_target = paths.data_root / MANIFEST_NAME
        if not repairs and manifest_target.is_file() and manifest_target.read_bytes() == new_manifest_bytes:
            # Record ownership even when the deployment itself needs no file
            # repair. This adopts links made by an older installer without
            # guessing that an unchanged config is safe to remove.
            link_worktrees_root(paths)
            print(f"Concord {installed} is complete; no repair needed.")
            return 0
        if not repairs:
            repairs.add("refreshed the installer manifest")
        transaction_root, journal = make_transaction(
            paths,
            "repair",
            manifest,
            installed,
            source_stage,
            version_records,
            adapter_stage_records,
            agent_stage_records,
            config_plan.text,
            new_manifest_bytes,
            retained_release_records(manifest),
            manifest.get("version_files"),
        )
        apply_version(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "version_activated")
        apply_agents(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "agents_swapped")
        apply_adapters(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "adapter_swapped")
        apply_launcher(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "launcher_swapped")
        apply_config(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "config_swapped")
        apply_manifest(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "manifest_committed")
        advance_phase(transaction_root, journal, "cleanup")
        verify_states(journal, paths, committed=True)
        cleanup_transaction(transaction_root, journal, paths)
    link_worktrees_root(paths)
    print(f"Repaired Concord {installed}: {', '.join(sorted(repairs))}.")
    print("Verified the deployment against the release archive; the manifest matches the deployed files.")
    return 0


def uninstall(args: argparse.Namespace) -> int:
    paths = paths_for(args.root)
    recover_transactions(paths)
    recover_pending_project_links(paths)
    manifest = load_manifest(paths)
    if manifest is None:
        print("No Concord installer manifest found; nothing was changed.")
        return 0
    # CON-807: removing releases mid-boundary could delete the only usable
    # candidate, and an uninstalled active release strands every session
    # the fence is excluding for the prepared activation.
    prepared = load_prepared_release(paths)
    if prepared is not None:
        raise InstallerError(
            f"a prepared release {prepared['version']} waits for activation; run: {prepared['activation_command']}"
        )
    if maintenance_fence_path(paths).exists() or maintenance_fence_path(paths).is_symlink():
        raise InstallerError(
            "a maintenance boundary is open; complete the prepared activation before uninstalling"
        )
    version = manifest["version"]
    records = manifest["version_files"]
    assert isinstance(version, str) and isinstance(records, dict)
    version_root = paths.data_root / version
    validate_owned_tree(version_root, records)
    adapter_records = managed_adapter_records(manifest)
    for relative, expected in adapter_records.items():
        destination = paths.tools_dir / relative
        if not destination.is_file() or sha256(destination) != expected:
            raise InstallerError(f"refusing to remove modified adapter file {destination}")
    agent_records = managed_agent_records(manifest)
    for name, expected in agent_records.items():
        destination = paths.agents_dir / name
        if destination.exists() and (not destination.is_file() or sha256(destination) != expected):
            raise InstallerError(f"refusing to remove modified agent file {destination}")
    launcher_target = manifest["launcher_target"]
    launcher = safe_relative_target(paths.bin_dir, "concord", "launcher", allow_final_symlink=True)
    if (launcher.exists() or launcher.is_symlink()) and (
        not launcher.is_symlink() or os.readlink(launcher) != launcher_target
    ):
        raise InstallerError(f"refusing to remove user-authored launcher {paths.launcher}")
    if paths.stable_root.exists() and not paths.stable_root.is_symlink():
        raise InstallerError(f"refusing to remove unmanaged stable root {paths.stable_root}")
    skill_path = manifest["skill_path"]
    assert isinstance(skill_path, str)
    config_path = safe_relative_target(paths.config_file.parent, paths.config_file.name, "OpenCode config")
    current_config = config_path.read_text(encoding="utf-8") if config_path.exists() else None
    if current_config is not None:
        new_config = remove_plugin_entry(remove_path_from_config(config_path, skill_path), plugin_entry_path(paths))
    else:
        new_config = None
    # Validate every project-scoped target before opening the uninstall
    # transaction. A symlinked worktree config must refuse without removing
    # any managed release state.
    plan_worktree_links(paths)
    plan_worktrees_root_unlink(paths)
    validate_project_link_ownership_for_uninstall(paths)
    retained = retained_release_records(manifest)
    transaction_root, journal = make_transaction(
        paths,
        "uninstall",
        manifest,
        None,
        None,
        None,
        None,
        None,
        new_config,
        None,
        retained,
    )
    apply_version(transaction_root, journal, paths)
    advance_phase(transaction_root, journal, "version_activated")
    apply_agents(transaction_root, journal, paths)
    advance_phase(transaction_root, journal, "agents_swapped")
    apply_adapters(transaction_root, journal, paths)
    advance_phase(transaction_root, journal, "adapter_swapped")
    apply_launcher(transaction_root, journal, paths)
    advance_phase(transaction_root, journal, "launcher_swapped")
    apply_config(transaction_root, journal, paths)
    advance_phase(transaction_root, journal, "config_swapped")
    apply_manifest(transaction_root, journal, paths)
    advance_phase(transaction_root, journal, "manifest_committed")
    advance_phase(transaction_root, journal, "cleanup")
    verify_states(journal, paths, committed=True)
    cleanup_transaction(transaction_root, journal, paths)
    # The data root is the maintenance lock's root and is retained: an
    # uninstall removes the intended installed content only, and no cleanup
    # may remove a directory a replacement holder could own (CON-807,
    # obs:1ca149d633e69626).
    for directory in (paths.tools_dir, paths.agents_dir, paths.bin_dir):
        try:
            directory.rmdir()
        except OSError:
            pass
    print(f"Uninstalled Concord {version} managed files.")
    print("Restart OpenCode before assuming the removed stable skills path is gone.")
    return 0


def status(args: argparse.Namespace) -> int:
    paths = paths_for(args.root)
    recover_transactions(paths)
    manifest = load_manifest(paths)
    prepared = load_prepared_release(paths)
    if prepared is not None and manifest is not None and (
        manifest.get("version") == prepared.get("version")
        or manifest.get("version") == prepared.get("boundary_owner_version")
    ):
        # The final crash phase (CON-807): the activation — of the prepared
        # release or of a superseding release that adopted its boundary —
        # committed, its transaction journal is gone, and the prepared
        # record with the boundary identity it recorded survived the crash
        # after cleanup. The committed state plus the surviving record
        # complete the boundary here: the recorded identity closes by
        # identity, and a fence with no recorded identity is foreign and
        # stays untouched.
        complete_prepared_boundary(paths)
        prepared = None
    report: dict[str, object] = {"installed": manifest is not None}
    if manifest is not None:
        report["version"] = manifest["version"]
    if prepared is not None:
        report["prepared_release"] = {
            "version": prepared["version"],
            "blockers": prepared["blockers"],
            "migration_command": prepared["migration_command"],
            "activation_command": prepared["activation_command"],
        }
    if maintenance_fence_path(paths).exists():
        report["maintenance_boundary"] = "open"
    print(json.dumps(report, sort_keys=True))
    return 0


def project_opencode_json(project_dir: Path) -> Path:
    """Return the existing project config, or the canonical JSON path."""
    config_dir = project_dir / ".opencode"
    for name in PROJECT_CONFIG_NAMES:
        candidate = config_dir / name
        if candidate.is_symlink():
            raise InstallerError(f"refusing symlinked project OpenCode config {candidate}")
        if candidate.exists() or candidate.is_symlink():
            return candidate
    return config_dir / PROJECT_CONFIG_NAMES[0]


def conduct_instruction_entry(paths: Paths) -> str:
    """Absolute glob the project should add to its instructions[] array.

    The literal `*.md` is part of the entry; OpenCode resolves it at load
    time, and an upgrade to a new corpus is picked up without rewriting the
    project file.
    """
    return str(paths.stable_root / "instructions" / "*.md")


def plan_project_link(project_file: Path, conduct_entry: str) -> tuple[str, bool]:
    """Compute the new project file contents and whether they differ.

    Returns (new_text, changed). Refuses to clobber a project file whose
    instructions key is present but not an array of strings — the operator
    owns that shape and a programmatic rewrite could destroy it.
    """
    if project_file.parent.is_symlink() or project_file.is_symlink():
        raise InstallerError(f"refusing symlinked project OpenCode config {project_file}")
    if not project_file.exists():
        new_text = '{\n  "instructions": [\n    ' + json.dumps(conduct_entry) + "\n  ]\n}\n"
        validate_project_config(project_file, new_text)
        return new_text, True
    original = project_file.read_text(encoding="utf-8")
    try:
        parsed = project_config_data(project_file, original)
    except (InstallerError, json.JSONDecodeError) as error:
        raise InstallerError(f"cannot parse project opencode config {project_file}: {error}") from error
    if not isinstance(parsed, dict):
        raise InstallerError(
            f"project opencode config {project_file} is not an object; add "
            f"{conduct_entry!r} to its instructions array manually"
        )
    instructions = parsed.get("instructions")
    if instructions is None:
        # Insert "instructions": [...] before the closing brace.
        end = outer_object_end(original)
        before = original[:end]
        separator = "" if before.rstrip().endswith("{") or has_trailing_jsonc_comma(before) else ","
        addition = f'{separator}\n  "instructions": [\n    {json.dumps(conduct_entry)}\n  ]\n'
        new_text = original[:end] + addition + original[end:]
        validate_project_config(project_file, new_text)
        return new_text, True
    if not isinstance(instructions, list) or not all(isinstance(value, str) for value in instructions):
        raise InstallerError(
            f"project opencode config {project_file} has a non-array instructions entry; "
            f"add {conduct_entry!r} to that array manually"
        )
    if conduct_entry in instructions:
        return original, False
    # Add the value before the closing bracket. This keeps JSONC comments and
    # unrelated formatting in place instead of round-tripping the whole file.
    array_end = jsonc_array_end(original, "instructions")
    if array_end is None:
        raise InstallerError(f"cannot locate the instructions array in {project_file}")
    before = original[:array_end]
    trailing = before[before.rfind("[") + 1:]
    has_trailing_comma = bool(re.search(r",(?:(?:[ \t]*//[^\n]*(?:\n|$))|(?:[ \t]*/\*.*?\*/[ \t]*))*[ \t\r\n]*$", trailing, re.S))
    if not instructions:
        separator = "\n  "
    else:
        separator = "\n  " if has_trailing_comma else "\n  ,\n  "
    new_text = before + separator + json.dumps(conduct_entry) + "\n" + original[array_end:]
    validate_project_config(project_file, new_text)
    return new_text, True


def remove_conduct_entry(project_file: Path, conduct_entry: str) -> tuple[str, bool]:
    """Compute new project file contents removing the conduct entry.

    Returns (new_text_or_empty, changed). When the file would have no keys
    left after the removal, returns ("", True) so the caller deletes it.
    """
    if project_file.parent.is_symlink() or project_file.is_symlink():
        raise InstallerError(f"refusing symlinked project OpenCode config {project_file}")
    if not project_file.exists():
        return "", False
    original = project_file.read_text(encoding="utf-8")
    try:
        parsed = project_config_data(project_file, original)
    except (InstallerError, json.JSONDecodeError) as error:
        raise InstallerError(f"cannot parse project opencode config {project_file}: {error}") from error
    if not isinstance(parsed, dict):
        raise InstallerError(
            f"project opencode config {project_file} is not an object; "
            f"remove {conduct_entry!r} from its instructions array manually"
        )
    instructions = parsed.get("instructions")
    if not isinstance(instructions, list) or conduct_entry not in instructions:
        return original, False
    token = json.dumps(conduct_entry)
    if original.count(token) != 1:
        raise InstallerError(f"cannot safely remove the managed conduct path from {project_file}")
    new_text = drop_string_token(original, token)
    updated = project_config_data(project_file, new_text)
    if isinstance(updated, dict) and updated.get("instructions") == []:
        if len(updated) == 1:
            return "", True
        new_text = drop_empty_instructions_property(new_text)
    validate_project_config(project_file, new_text)
    return new_text, True


def worktrees_root(paths: Paths) -> Path:
    """Return the shared ancestor for Concord-created worktrees."""
    return paths.data_root / "worktrees"


def worktree_directories(paths: Paths) -> list[Path]:
    """Return direct Concord worktree directories under the managed root."""
    root = worktrees_root(paths)
    for path, label in (
        (paths.data_home, "installer data home"),
        (paths.data_root, "installer data root"),
        (root, "worktrees root"),
    ):
        if path.is_symlink():
            raise InstallerError(f"refusing symlinked {label} {path}")
    if not root.exists():
        return []
    if not root.is_dir():
        raise InstallerError(f"refusing non-directory worktrees root {root}")
    result: list[Path] = []
    for project in sorted(root.iterdir()):
        if project.name == ".opencode":
            continue
        if project.is_symlink():
            raise InstallerError(f"refusing symlinked worktree project {project}")
        if not project.is_dir():
            continue
        for worktree in sorted(project.iterdir()):
            if worktree.is_symlink():
                raise InstallerError(f"refusing symlinked Concord worktree {worktree}")
            if worktree.is_dir():
                result.append(worktree)
    return result


def worktrees_project_file(paths: Paths) -> Path:
    """Return the legacy ancestor config after validating its components."""
    root = worktrees_root(paths)
    project_dir = root / ".opencode"
    project_file = project_dir / "opencode.json"
    for path, label in (
        (paths.data_home, "installer data home"),
        (paths.data_root, "installer data root"),
        (root, "worktrees root"),
        (project_dir, "worktrees OpenCode directory"),
        (project_file, "worktrees OpenCode config"),
    ):
        if path.is_symlink():
            raise InstallerError(f"refusing symlinked {label} {path}")
    return project_file


def project_link_ownership_path(paths: Paths) -> Path:
    """Return the installer-owned record for project configuration edits."""
    path = paths.data_root / PROJECT_LINK_OWNERSHIP_NAME
    if path.is_symlink():
        raise InstallerError(f"refusing symlinked project link ownership record {path}")
    return path


def load_project_link_ownership(paths: Paths) -> dict[str, dict[str, object]]:
    """Read and validate the durable project link ownership record."""
    path = project_link_ownership_path(paths)
    if not path.exists():
        return {}
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise InstallerError(f"cannot read project link ownership record {path}: {error}") from error
    if not isinstance(value, dict) or value.get("schema") != 1 or not isinstance(value.get("links"), dict):
        raise InstallerError(f"refusing malformed project link ownership record {path}")
    links = value["links"]
    if len(links) > MAX_PROJECT_LINKS:
        raise InstallerError(f"project link ownership record contains too many entries: {path}")
    result: dict[str, dict[str, object]] = {}
    for raw_path, raw_record in links.items():
        if (
            not isinstance(raw_path, str)
            or not raw_path.startswith("/")
            or "\x00" in raw_path
            or Path(raw_path).as_posix() != os.path.normpath(raw_path)
            or not isinstance(raw_record, dict)
        ):
            raise InstallerError(f"refusing invalid project link ownership entry in {path}")
        if set(raw_record) - {"action", "scope", "expected", "original"}:
            raise InstallerError(f"refusing unknown project link ownership fields for {raw_path}")
        action = raw_record.get("action")
        scope = raw_record.get("scope")
        expected = raw_record.get("expected")
        if action not in {"remove", "restore", "preserve"} or scope not in {"project", "worktree", "legacy"}:
            raise InstallerError(f"refusing invalid project link ownership action for {raw_path}")
        if (
            not isinstance(expected, dict)
            or set(expected) - {"exists", "sha256"}
            or not isinstance(expected.get("exists"), bool)
            or (expected["exists"] and (not isinstance(expected.get("sha256"), str) or not SHA256_RE.fullmatch(expected["sha256"])))
            or (not expected["exists"] and "sha256" in expected)
        ):
            raise InstallerError(f"refusing invalid expected project config state for {raw_path}")
        original = raw_record.get("original")
        if action == "restore" and not isinstance(original, str):
            raise InstallerError(f"refusing missing original project config for {raw_path}")
        if action != "restore" and "original" in raw_record:
            raise InstallerError(f"refusing unexpected original project config for {raw_path}")
        result[raw_path] = raw_record
    return result


def save_project_link_ownership(paths: Paths, links: dict[str, dict[str, object]]) -> None:
    """Atomically save the installer-owned project link record."""
    path = project_link_ownership_path(paths)
    if not links:
        if path.exists() or path.is_symlink():
            if path.is_symlink():
                raise InstallerError(f"refusing symlinked project link ownership record {path}")
            path.unlink()
            fsync_directory(path.parent)
        return
    if len(links) > MAX_PROJECT_LINKS:
        raise InstallerError("too many project link ownership entries")
    payload = {"schema": 1, "links": {key: links[key] for key in sorted(links)}}
    write_atomic(path, (json.dumps(payload, indent=2, sort_keys=True) + "\n").encode("utf-8"))


def project_link_pending_path(paths: Paths) -> Path:
    """Return the installer recovery record for project link writes."""
    path = paths.data_root / PROJECT_LINK_PENDING_NAME
    if path.is_symlink():
        raise InstallerError(f"refusing symlinked project link recovery record {path}")
    return path


def pending_write_removed_entry(project_file: Path, updated: str, conduct_entry: str) -> bool:
    """Return whether a pending project write's target text lacks the entry.

    An installer write always lands the conduct entry; an uninstall write
    never keeps it. Recovery uses the distinction to retire an ownership
    record whose removal already happened instead of retargeting it at an
    entry-less state the uninstall guard would refuse.
    """
    if updated == "":
        return True
    try:
        parsed = project_config_data(project_file, updated)
    except (InstallerError, json.JSONDecodeError) as error:
        raise InstallerError(f"cannot parse the pending project config write for {project_file}: {error}") from error
    instructions = parsed.get("instructions") if isinstance(parsed, dict) else None
    return not (isinstance(instructions, list) and conduct_entry in instructions)


def recover_pending_project_links(paths: Paths) -> None:
    """Finish or discard project writes interrupted before ownership was saved."""
    path = project_link_pending_path(paths)
    if not path.exists():
        return
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise InstallerError(f"cannot read project link recovery record {path}: {error}") from error
    if not isinstance(value, dict) or value.get("schema") != 1 or not isinstance(value.get("links"), dict):
        raise InstallerError(f"refusing malformed project link recovery record {path}")
    pending = value["links"]
    if len(pending) > MAX_PROJECT_LINKS:
        raise InstallerError(f"project link recovery record contains too many entries: {path}")
    ownership = load_project_link_ownership(paths)
    conduct_entry = conduct_instruction_entry(paths)
    changed = False
    for raw_path, raw_record in pending.items():
        if (
            not isinstance(raw_path, str)
            or not raw_path.startswith("/")
            or Path(raw_path).as_posix() != os.path.normpath(raw_path)
            or not isinstance(raw_record, dict)
            or set(raw_record) != {"scope", "action", "before", "original", "updated"}
            or raw_record.get("scope") not in {"project", "worktree", "legacy"}
            or raw_record.get("action") not in {"remove", "restore"}
            or (raw_record.get("before") is not None and not isinstance(raw_record.get("before"), str))
            or (raw_record.get("original") is not None and not isinstance(raw_record.get("original"), str))
            or not isinstance(raw_record.get("updated"), str)
            or (raw_record.get("action") == "restore" and not isinstance(raw_record.get("original"), str))
            or (raw_record.get("action") == "remove" and raw_record.get("original") is not None)
        ):
            raise InstallerError(f"refusing malformed project link recovery entry in {path}")
        project_file = Path(raw_path)
        scope = raw_record["scope"]
        if scope == "worktree":
            validate_managed_worktree_config_path(paths, project_file)
        elif scope == "legacy":
            expected_legacy = worktrees_root(paths) / ".opencode" / "opencode.json"
            if project_file != expected_legacy or project_file.resolve(strict=False) != project_file:
                raise InstallerError(f"refusing project link recovery path outside the managed worktrees root: {project_file}")
        before = raw_record["before"]
        original = raw_record["original"]
        updated = raw_record["updated"]
        assert isinstance(before, (str, type(None))) and isinstance(original, (str, type(None))) and isinstance(updated, str)
        current = project_file_state(project_file)
        expected = {"exists": bool(updated)}
        if updated:
            expected["sha256"] = hashlib.sha256(updated.encode("utf-8")).hexdigest()
        before_state = {"exists": False} if before is None else {
            "exists": True,
            "sha256": hashlib.sha256(before.encode("utf-8")).hexdigest(),
        }
        previous = ownership.get(raw_path)
        if previous is not None:
            if current == previous.get("expected"):
                continue
            if current == expected:
                if pending_write_removed_entry(project_file, updated, conduct_entry):
                    # The pending removal already landed, so the record has
                    # nothing left to own; retargeting it would describe an
                    # entry-less state the uninstall guard refuses.
                    ownership.pop(raw_path, None)
                else:
                    previous["expected"] = expected
                changed = True
                continue
            if current == before_state:
                # The pending write never reached the file: the recorded
                # before-state is still the live state, including any host
                # keys outside the managed entry. Drop the recovery record
                # and let the caller redo its own write.
                continue
            raise InstallerError(f"refusing to recover a modified project OpenCode config {project_file}")
        if current == expected:
            action = raw_record["action"]
            record: dict[str, object] = {"action": action, "scope": raw_record["scope"], "expected": expected}
            if action == "restore":
                assert isinstance(original, str)
                record["original"] = original
            ownership[raw_path] = record
            changed = True
            continue
        if current != before_state:
            raise InstallerError(f"refusing to recover a modified project OpenCode config {project_file}")
    if changed:
        save_project_link_ownership(paths, ownership)
    path.unlink()
    fsync_directory(path.parent)


def validate_managed_worktree_config_path(paths: Paths, project_file: Path) -> None:
    """Require a worktree recovery target inside a managed worktree config."""
    if project_file.name not in PROJECT_CONFIG_NAMES or project_file.parent.name != ".opencode":
        raise InstallerError(f"refusing project link recovery path outside a managed worktree config: {project_file}")
    root = paths.data_root / "worktrees"
    if root.is_symlink():
        raise InstallerError(f"refusing symlinked worktrees root {root}")
    try:
        project_file.relative_to(root)
    except ValueError as error:
        raise InstallerError(f"refusing project link recovery path outside the managed worktrees root: {project_file}") from error
    if project_file.resolve(strict=False) != project_file:
        raise InstallerError(f"refusing symlinked managed worktree config {project_file}")
    worktree = project_file.parent.parent
    if not worktree.is_dir():
        raise InstallerError(f"refusing project link recovery path outside a managed worktree: {project_file}")


def project_link_record(
    project_file: Path,
    original: str | None,
    linked: str,
    changed: bool,
    scope: str,
    previous: dict[str, object] | None = None,
) -> dict[str, object]:
    """Describe how uninstall must handle one project config."""
    expected = {"exists": True, "sha256": hashlib.sha256(linked.encode("utf-8")).hexdigest()}
    if previous and previous.get("action") in {"remove", "restore"}:
        previous_expected = previous.get("expected")
        if not isinstance(previous_expected, dict):
            raise InstallerError(f"refusing malformed project link ownership record for {project_file}")
        # Keep the adopted record's expected bytes instead of rehashing the
        # current text: they are the bytes this installer wrote, so the
        # uninstall restore shortcut's byte match still proves the file
        # carries nothing beyond the managed edit. Refreshing the digest here
        # would absorb host keys into "expected" and the uninstall's
        # whole-file restore would then delete them. A config that lost the
        # managed entry is replanned by the caller's planning pass; the
        # recorded base kept here still governs what uninstall restores.
        action = previous["action"]
        record: dict[str, object] = {"action": action, "scope": scope, "expected": previous_expected}
        if action == "restore":
            record["original"] = previous["original"]
        return record
    if not changed:
        return {"action": "preserve", "scope": scope, "expected": expected}
    if original is None:
        return {"action": "remove", "scope": scope, "expected": expected}
    return {"action": "restore", "scope": scope, "expected": expected, "original": original}


def project_file_state(path: Path) -> dict[str, object]:
    """Return the bounded state used to compare a config before removal."""
    if path.is_symlink():
        raise InstallerError(f"refusing symlinked project OpenCode config {path}")
    if not path.exists():
        return {"exists": False}
    if not path.is_file() or path.parent.is_symlink():
        raise InstallerError(f"refusing non-file or symlinked project OpenCode config {path}")
    return {"exists": True, "sha256": sha256(path)}


def project_config_contains_entry(path: Path, conduct_entry: str) -> bool:
    """Return whether a parseable project config carries the managed entry."""
    try:
        parsed = project_config_data(path, path.read_text(encoding="utf-8"))
    except (InstallerError, OSError, UnicodeDecodeError, json.JSONDecodeError):
        return False
    if not isinstance(parsed, dict):
        return False
    instructions = parsed.get("instructions")
    return isinstance(instructions, list) and conduct_entry in instructions


def ownership_state_matches(path: Path, expected: dict[str, object], conduct_entry: str) -> bool:
    """Decide on the entry the installer owns, not on the whole file.

    The writer preserves keys the installer does not own, so the guard must
    too: a config that still carries the conduct entry and still parses is
    owned state, whatever else a host tool wrote into it. A missing file is
    nothing left to protect.
    """
    actual = project_file_state(path)
    if not expected.get("exists"):
        return not actual["exists"]
    if not actual["exists"]:
        return True
    return project_config_contains_entry(path, conduct_entry)


def validate_project_link_ownership_for_uninstall(paths: Paths) -> None:
    """Validate owned project states before uninstall changes release files.

    A legacy record is excluded because its expected state is the state
    *after* the conduct entry was removed, so the entry is absent by design.
    The owned-entry test applies only to a record whose expected state still
    carries the entry, which is every worktree-scoped record.
    """
    conduct_entry = conduct_instruction_entry(paths)
    for raw_path, record in load_project_link_ownership(paths).items():
        if record["action"] == "preserve" or record.get("scope") == "legacy":
            continue
        expected = record["expected"]
        if not isinstance(expected, dict) or not ownership_state_matches(Path(raw_path), expected, conduct_entry):
            raise InstallerError(f"refusing to restore user-modified project OpenCode config {raw_path}")


def plan_worktree_links(paths: Paths) -> list[tuple[Path, str, bool]]:
    """Plan one project-scoped pointer for each existing managed worktree."""
    entry = conduct_instruction_entry(paths)
    ownership = load_project_link_ownership(paths)
    planned: list[tuple[Path, str, bool]] = []
    for worktree in worktree_directories(paths):
        project_file = project_opencode_json(worktree)
        previous = ownership.get(str(project_file.resolve(strict=False)))
        if (
            previous
            and previous.get("action") in {"remove", "restore"}
            and not isinstance(previous.get("expected"), dict)
        ):
            raise InstallerError(f"refusing malformed project link ownership record for {project_file}")
        new_text, changed = plan_project_link(project_file, entry)
        planned.append((project_file, new_text, changed))
    return planned


def sync_worktree_links(paths: Paths, remove: bool = False) -> bool:
    """Converge project pointers and preserve every preexisting config."""
    recover_pending_project_links(paths)
    links = load_project_link_ownership(paths)
    entry = conduct_instruction_entry(paths)
    if remove:
        changed = False
        pending: dict[str, dict[str, object]] = {}
        for raw_path, record in sorted(links.items()):
            project_file = Path(raw_path)
            if record["action"] == "preserve" or record.get("scope") == "legacy":
                continue
            expected = record["expected"]
            if not isinstance(expected, dict) or not ownership_state_matches(project_file, expected, entry):
                raise InstallerError(f"refusing to restore user-modified project OpenCode config {project_file}")
            before = project_file.read_text(encoding="utf-8") if project_file.exists() else None
            if before is None:
                continue
            new_text, removed = remove_conduct_entry(project_file, entry)
            if not removed:
                raise InstallerError(f"refusing to remove the managed conduct path from {project_file}")
            action = record["action"]
            if action == "restore" and expected.get("sha256") == hashlib.sha256(before.encode("utf-8")).hexdigest():
                new_text = str(record["original"])
            pending[raw_path] = {
                "scope": record["scope"],
                "action": action,
                "before": before,
                "original": record.get("original") if action == "restore" else None,
                "updated": new_text,
            }
        pending_path = project_link_pending_path(paths)
        if pending:
            write_atomic(
                pending_path,
                (json.dumps({"schema": 1, "links": {key: pending[key] for key in sorted(pending)}}, indent=2, sort_keys=True) + "\n").encode("utf-8"),
            )
        for raw_path, record in sorted(links.items()):
            project_file = Path(raw_path)
            if record["action"] == "preserve" or record.get("scope") == "legacy":
                continue
            if raw_path not in pending:
                continue
            updated = str(pending[raw_path]["updated"])
            if updated == "":
                project_file.unlink()
                changed = True
                try:
                    project_file.parent.rmdir()
                except OSError:
                    pass
            else:
                write_atomic(project_file, updated.encode("utf-8"))
                changed = True
        save_project_link_ownership(paths, {})
        if pending:
            pending_path.unlink()
            fsync_directory(pending_path.parent)
        return changed

    changed = False
    legacy = worktrees_project_file(paths)
    legacy_original: str | None = None
    legacy_text: str | None = None
    if legacy.exists():
        legacy_original = legacy.read_text(encoding="utf-8")
        legacy_key = str(legacy.resolve(strict=False))
        new_text, planned = remove_conduct_entry(legacy, entry)
        if planned:
            legacy_text = new_text
            links[legacy_key] = {
                "action": "remove",
                "scope": "legacy",
                "expected": {"exists": bool(new_text), **({"sha256": hashlib.sha256(new_text.encode("utf-8")).hexdigest()} if new_text else {})},
            }
            changed = True

    planned_links = plan_worktree_links(paths)
    seen = set()
    writes: list[tuple[Path, str]] = []
    pending: dict[str, dict[str, object]] = {}
    if legacy_text is not None and legacy_original is not None:
        pending[str(legacy.resolve(strict=False))] = {
            "scope": "legacy",
            "action": "remove",
            "before": legacy_original,
            "original": None,
            "updated": legacy_text,
        }
    for project_file, new_text, planned in planned_links:
        key = str(project_file.resolve(strict=False))
        seen.add(key)
        original = project_file.read_text(encoding="utf-8") if project_file.exists() else None
        links[key] = project_link_record(project_file, original, new_text, planned, "worktree", links.get(key))
        if planned:
            pending[key] = {
                "scope": "worktree",
                "action": links[key]["action"],
                "before": original,
                "original": links[key].get("original") if links[key]["action"] == "restore" else None,
                "updated": new_text,
            }
            writes.append((project_file, new_text))
            changed = True
    links = {
        key: record
        for key, record in links.items()
        if record.get("scope") != "worktree" or key in seen
    }
    if pending:
        pending_path = project_link_pending_path(paths)
        write_atomic(
            pending_path,
            (json.dumps({"schema": 1, "links": {key: pending[key] for key in sorted(pending)}}, indent=2, sort_keys=True) + "\n").encode("utf-8"),
        )
    if legacy_text is not None:
        if legacy_text == "":
            legacy.unlink()
            try:
                legacy.parent.rmdir()
            except OSError:
                pass
        else:
            write_atomic(legacy, legacy_text.encode("utf-8"))
    for project_file, new_text in writes:
        ensure_directory(project_file.parent, mode=0o755)
        write_atomic(project_file, new_text.encode("utf-8"))
    save_project_link_ownership(paths, links)
    if pending:
        pending_path = project_link_pending_path(paths)
        pending_path.unlink()
        fsync_directory(pending_path.parent)
    return changed


def plan_worktrees_root_unlink(paths: Paths) -> tuple[Path, str, bool]:
    """Compatibility wrapper that validates legacy pointer removal."""
    plan_worktree_links(paths)
    project_file = worktrees_project_file(paths)
    if not project_file.exists():
        return project_file, "", False
    new_text, changed = remove_conduct_entry(project_file, conduct_instruction_entry(paths))
    return project_file, new_text, changed


def link_worktrees_root(paths: Paths) -> bool:
    """Register the installed conduct corpus in existing worktrees."""
    return sync_worktree_links(paths)


def unlink_worktrees_root(paths: Paths) -> bool:
    """Remove only installer-owned pointers from existing worktrees."""
    return sync_worktree_links(paths, remove=True)


def link(args: argparse.Namespace) -> int:
    paths = paths_for(args.root)
    recover_transactions(paths)
    recover_pending_project_links(paths)
    manifest = load_manifest(paths)
    if manifest is None:
        raise InstallerError("cannot link a project: no Concord installation is present")
    project_dir = Path(args.project).resolve()
    project_file = project_opencode_json(project_dir)
    conduct_entry = conduct_instruction_entry(paths)
    ownership = load_project_link_ownership(paths)
    original = project_file.read_text(encoding="utf-8") if project_file.exists() else None
    new_text, changed = plan_project_link(project_file, conduct_entry)
    ownership[str(project_file.resolve(strict=False))] = project_link_record(
        project_file,
        original,
        new_text,
        changed,
        "project",
        ownership.get(str(project_file.resolve(strict=False))),
    )
    if not changed:
        save_project_link_ownership(paths, ownership)
        print(f"Project {project_dir} already points at the conduct corpus; no changes made.")
        return 0
    pending_path = project_link_pending_path(paths)
    write_atomic(
        pending_path,
        (json.dumps(
            {
                "schema": 1,
                "links": {
                    str(project_file.resolve(strict=False)): {
                        "scope": "project",
                        "action": ownership[str(project_file.resolve(strict=False))]["action"],
                        "before": original,
                        "original": ownership[str(project_file.resolve(strict=False))].get("original") if ownership[str(project_file.resolve(strict=False))]["action"] == "restore" else None,
                        "updated": new_text,
                    }
                },
            },
            indent=2,
            sort_keys=True,
        ) + "\n").encode("utf-8"),
    )
    ensure_directory(project_file.parent, mode=0o755)
    write_atomic(project_file, new_text.encode("utf-8"))
    save_project_link_ownership(paths, ownership)
    pending_path.unlink()
    fsync_directory(pending_path.parent)
    print(f"Linked project {project_dir} to {conduct_entry}.")
    return 0


def unlink(args: argparse.Namespace) -> int:
    paths = paths_for(args.root)
    recover_transactions(paths)
    recover_pending_project_links(paths)
    project_dir = Path(args.project).resolve()
    project_file = project_opencode_json(project_dir)
    conduct_entry = conduct_instruction_entry(paths)
    ownership = load_project_link_ownership(paths)
    new_text, changed = remove_conduct_entry(project_file, conduct_entry)
    if not changed:
        ownership.pop(str(project_file.resolve(strict=False)), None)
        save_project_link_ownership(paths, ownership)
        print(f"Project {project_dir} has no conduct corpus entry; no changes made.")
        return 0
    if new_text == "":
        project_file.unlink()
        try:
            project_file.parent.rmdir()
        except OSError:
            pass
    else:
        write_atomic(project_file, new_text.encode("utf-8"))
    ownership.pop(str(project_file.resolve(strict=False)), None)
    save_project_link_ownership(paths, ownership)
    print(f"Unlinked project {project_dir} from the conduct corpus.")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    install_parser = subparsers.add_parser("install", help="install or upgrade a release")
    install_parser.add_argument("--version", help="release tag; defaults to the latest the source serves")
    install_parser.add_argument("--artifact-dir", help="use local published assets instead of downloading")
    install_parser.add_argument(
        "--base-url",
        default="https://github.com/Sharper-Flow/concord/releases/latest/download",
        help="release asset base URL when --artifact-dir is absent",
    )
    uninstall_parser = subparsers.add_parser("uninstall", help="remove the managed release")
    activate_parser = subparsers.add_parser(
        "activate",
        help="activate the prepared release after its migration completed (CON-807)",
    )
    activate_parser.add_argument("--version", help="confirm the prepared release tag; a different tag refuses")
    repair_parser = subparsers.add_parser("repair", help="complete an incomplete installation of the installed release")
    repair_parser.add_argument("--version", help="confirm the installed release tag; a different tag refuses")
    repair_parser.add_argument("--artifact-dir", help="use local published assets instead of downloading")
    repair_parser.add_argument(
        "--base-url",
        default="https://github.com/Sharper-Flow/concord/releases/latest/download",
        help="release asset base URL when --artifact-dir is absent",
    )
    status_parser = subparsers.add_parser("status", help="recover and report installation state")
    link_parser = subparsers.add_parser(
        "link", help="register the conduct corpus for a project (per-project, not global)"
    )
    link_parser.add_argument("--project", type=Path, required=True, help="project directory to update")
    unlink_parser = subparsers.add_parser("unlink", help="remove the conduct corpus entry from a project")
    unlink_parser.add_argument("--project", type=Path, required=True, help="project directory to update")
    for command_parser in (install_parser, uninstall_parser, activate_parser, repair_parser, status_parser, link_parser, unlink_parser):
        command_parser.add_argument("--root", type=Path, help="test root; maps home/data/config/bin under it")
    args = parser.parse_args()
    commands = {
        "install": install,
        "activate": activate,
        "repair": repair,
        "uninstall": uninstall,
        "link": link,
        "unlink": unlink,
        "status": status,
    }
    try:
        # Every command recovers transactions first, so every command is a
        # maintenance command and runs alone. The release removes nothing:
        # the lock root is retained through failed installation, normal
        # release, and uninstall (CON-807, obs:1ca149d633e69626).
        with maintenance_lock(paths_for(args.root)):
            return commands[args.command](args)
    except InstallerError as error:
        print(str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
