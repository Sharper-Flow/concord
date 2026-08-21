#!/usr/bin/env python3
"""Install, upgrade, and uninstall Concord release artifacts.

The installer uses only the Python standard library.  It verifies the published
checksum and archive before changing the operator's data, binary, adapter, or
OpenCode configuration paths.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass
from pathlib import Path


VERSION_RE = re.compile(r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")
MANIFEST_NAME = "install-manifest.json"
ADAPTER_FILES = (
    "concord.ts",
    "credentials.ts",
    "generated-contracts.ts",
    "generated-contract-tests.ts",
)


@dataclass(frozen=True)
class Paths:
    home: Path
    data_home: Path
    config_home: Path
    bin_dir: Path
    data_root: Path
    config_file: Path
    tools_dir: Path
    launcher: Path


@dataclass(frozen=True)
class ConfigPlan:
    path: Path
    text: str
    changed: bool
    managed_fragment: str | None


class InstallerError(Exception):
    """An actionable refusal that must not leave a partial installation."""


def paths_for(root: Path | None) -> Paths:
    if root is not None:
        home = root.resolve()
        data_home = home / "data"
        config_home = home / "config"
        bin_dir = home / "bin"
    else:
        home = Path.home()
        data_home = Path(os.environ.get("XDG_DATA_HOME", home / ".local" / "share"))
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
        launcher=bin_dir / "concord",
    )


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_version(value: str) -> str:
    if not VERSION_RE.fullmatch(value):
        raise InstallerError(f"invalid release version {value!r}; use vMAJOR.MINOR.PATCH")
    return value


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


def registration_snippet(skill_path: str) -> str:
    return f'"skills": {{"paths": [{json.dumps(skill_path)}]}}'


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
        "skill_path",
        "launcher_target",
        "config_path",
    }
    if set(manifest) != required or manifest.get("managed_by") != "concord-installer-v1":
        raise InstallerError("refusing installer manifest with unknown or missing fields")
    version = manifest.get("version")
    if not isinstance(version, str) or not VERSION_RE.fullmatch(version):
        raise InstallerError("installer manifest has an invalid version")
    if paths.data_root.is_symlink():
        raise InstallerError(f"refusing installer data root {paths.data_root}: it is a symlink")
    version_root = safe_relative_target(paths.data_root, version, "version")
    if version_root.is_symlink():
        raise InstallerError(f"refusing installer version root {version_root}: it is a symlink")

    version_files = manifest.get("version_files")
    if not isinstance(version_files, dict) or not version_files:
        raise InstallerError("installer manifest has invalid version file records")
    allowed_fixed = {"bin/concord", *(f"adapter/opencode/{name}" for name in ADAPTER_FILES)}
    for relative, digest in version_files.items():
        if not isinstance(relative, str) or not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
            raise InstallerError("installer manifest has invalid version file records")
        safe_relative_target(version_root, relative, "version file")
        if relative not in allowed_fixed and not relative.startswith("skills/"):
            raise InstallerError(f"refusing unknown managed version file {relative!r}")
    if not allowed_fixed.issubset(version_files):
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

    expected_skill = paths.data_root / version / "skills"
    skill_path = manifest.get("skill_path")
    if not isinstance(skill_path, str) or Path(skill_path).resolve(strict=False) != expected_skill.resolve(strict=False):
        raise InstallerError("installer manifest has a redirected skills path")
    safe_relative_target(paths.data_root, f"{version}/skills", "skills")

    expected_launcher = paths.data_root / version / "bin" / "concord"
    if manifest.get("launcher_target") != str(expected_launcher.resolve(strict=False)):
        raise InstallerError("installer manifest has a redirected launcher target")
    safe_relative_target(paths.data_root, f"{version}/bin/concord", "launcher")

    config_path = manifest.get("config_path")
    if not isinstance(config_path, str) or config_path != str(paths.config_file.resolve(strict=False)):
        raise InstallerError("installer manifest has a redirected OpenCode config path")
    safe_relative_target(paths.config_file.parent, paths.config_file.name, "OpenCode config")


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


def preflight(paths: Paths, version: str, old_manifest: dict[str, object] | None) -> ConfigPlan:
    failures: list[str] = []
    for managed_parent in (paths.data_root, paths.tools_dir, paths.bin_dir, paths.config_file.parent):
        if managed_parent.is_symlink():
            failures.append(f"refusing symlinked managed path {managed_parent}")
    system = platform.system()
    machine = platform.machine().lower()
    if system != "Linux":
        failures.append(f"platform is {system}; Linux amd64 is required")
    if machine not in {"x86_64", "amd64"}:
        failures.append(f"architecture is {platform.machine()}; Linux amd64 is required")
    for command, consequence in (
        ("git", "grant host resolution will not work without Git"),
        ("opencode", "the global Concord custom tool cannot be used without OpenCode"),
        ("secret-tool", "grant bootstrap fails closed because Concord cannot read the signing key"),
        ("gnome-keyring-daemon", "the Secret Service provider required by grant bootstrap is missing"),
    ):
        if command_status(command) is None:
            failures.append(f"missing command {command}; {consequence}")
    service_ok, service_reason = secret_service_status()
    if not service_ok:
        failures.append(f"Secret Service unavailable: {service_reason}; grant bootstrap will not work")
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
    adapter_records = managed_adapter_records(old_manifest)
    for name in ADAPTER_FILES:
        destination = paths.tools_dir / name
        if destination.exists() or destination.is_symlink():
            if not old_manifest or name not in adapter_records:
                failures.append(f"refusing to overwrite user-authored adapter file {destination}")
    if old_manifest:
        old_version = old_manifest.get("version")
        if not isinstance(old_version, str):
            failures.append("existing installer manifest has no valid version")
    old_skill = old_manifest.get("skill_path") if old_manifest else None
    if old_skill is not None and not isinstance(old_skill, str):
        failures.append("existing installer manifest has no valid skills path")
    try:
        config_plan = plan_config(paths.config_file, str((paths.data_root / version / "skills").resolve()), old_skill)
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
            bundle.extractall(extracted)
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


def validate_owned_tree(root: Path, records: dict[str, str]) -> None:
    if not root.exists():
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
        if missing:
            detail.append("missing managed files: " + ", ".join(missing))
        raise InstallerError(f"refusing to replace managed path {root}; " + "; ".join(detail))
    for relative, expected_hash in records.items():
        actual_hash = sha256(root / relative)
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


TRANSACTION_PARENT = ".concord-transactions"
MAX_TRANSACTION_JOURNAL_BYTES = 1024 * 1024
MAX_TRANSACTION_FILES = 4096
TRANSACTION_PHASES = (
    "staged",
    "version_activated",
    "adapter_swapped",
    "launcher_swapped",
    "config_swapped",
    "manifest_committed",
    "cleanup",
    "rollback",
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


def version_matches(root: Path, records: dict[str, str] | None) -> bool:
    if records is None:
        return not root.exists() and not root.is_symlink()
    if not root.exists() or root.is_symlink():
        return False
    try:
        validate_owned_tree(root, records)
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
        "old_version_records",
        "new_version_records",
        "old_adapter",
        "new_adapter",
        "old_config",
        "new_config",
        "old_launcher",
        "new_launcher",
        "old_manifest",
        "new_manifest",
        "targets",
        "backup_dir",
        "stage_dir",
        "version_backup",
        "live_version_backup",
    }
    if set(journal) != required or journal.get("schema") != 1:
        raise InstallerError(f"refusing malformed transaction journal {journal_path(transaction_root)}")
    if journal.get("operation") not in {"install", "uninstall"} or journal.get("phase") not in TRANSACTION_PHASES:
        raise InstallerError(f"refusing malformed transaction journal {journal_path(transaction_root)}")
    for key in ("old_version", "new_version", "activation_version", "cleanup_version"):
        value = journal.get(key)
        if value is not None and (not isinstance(value, str) or not VERSION_RE.fullmatch(value)):
            raise InstallerError(f"refusing malformed transaction version in {journal_path(transaction_root)}")
    if journal.get("operation") == "install" and not isinstance(journal.get("new_version_records"), dict):
        raise InstallerError(f"refusing malformed transaction records {journal_path(transaction_root)}")
    if journal.get("operation") == "uninstall" and journal.get("new_version_records") is not None:
        raise InstallerError(f"refusing malformed uninstall transaction {journal_path(transaction_root)}")
    if journal.get("backup_dir") != "backup" or journal.get("stage_dir") != "stage" or journal.get("version_backup") != "backup/version" or journal.get("live_version_backup") != "backup/live-version":
        raise InstallerError(f"refusing redirected transaction backup locator {journal_path(transaction_root)}")
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
    for relative in ("stage", "backup"):
        safe_relative_target(transaction_root.parent, transaction_root.name + "/" + relative, "transaction")
    targets = journal.get("targets")
    if not isinstance(targets, dict) or set(targets) != {"version", "adapters", "launcher", "config", "manifest"}:
        raise InstallerError(f"refusing malformed transaction targets {journal_path(transaction_root)}")
    if targets.get("config") != str(paths.config_file.resolve(strict=False)):
        raise InstallerError(f"refusing redirected transaction config target {journal_path(transaction_root)}")
    if targets.get("launcher") != str(paths.launcher):
        raise InstallerError(f"refusing redirected transaction launcher target {journal_path(transaction_root)}")
    activation = journal.get("activation_version")
    if not isinstance(activation, str) or targets.get("version") != str((paths.data_root / activation).resolve(strict=False)):
        raise InstallerError(f"refusing redirected transaction version target {journal_path(transaction_root)}")


def make_transaction(
    paths: Paths,
    operation: str,
    old_manifest: dict[str, object] | None,
    new_version: str | None,
    stage_source: Path | None,
    new_version_records: dict[str, str] | None,
    new_adapter: dict[str, str] | None,
    new_config: str | None,
    new_manifest_bytes: bytes | None,
) -> tuple[Path, dict[str, object]]:
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
    old_records = old_manifest.get("version_files") if old_manifest else None
    activation_version = new_version if operation == "install" else old_version
    activation_old_records = old_records if activation_version == old_version else None
    activation_root = paths.data_root / str(activation_version) if activation_version else paths.data_root / "unused"
    version_backup = backup / "version"
    if activation_old_records is not None:
        if not isinstance(activation_old_records, dict):
            raise InstallerError("existing manifest has invalid version records")
        validate_owned_tree(activation_root, activation_old_records)
        durable_copy_tree(activation_root, version_backup)
    elif operation == "uninstall":
        raise InstallerError("cannot uninstall without a managed version directory")
    old_adapter: dict[str, object] = {}
    for name in ADAPTER_FILES:
        old_adapter[name] = capture_file(paths.tools_dir / name, backup / "adapter" / name, f"adapter {name}")
    old_config = capture_file(paths.config_file, backup / "config", "OpenCode config")
    old_manifest_state = capture_file(paths.data_root / MANIFEST_NAME, backup / "manifest", "installer manifest")
    old_launcher = file_state(paths.launcher)
    if old_launcher.get("exists") and old_launcher.get("kind") != "symlink":
        raise InstallerError(f"refusing transaction over user-authored launcher {paths.launcher}")
    new_config_state = {"exists": new_config is not None, "kind": "file", "sha256": sha256(stage / "config")} if new_config is not None else {"exists": False}
    new_manifest_state = {"exists": new_manifest_bytes is not None, "kind": "file", "sha256": sha256(stage / "manifest")} if new_manifest_bytes is not None else {"exists": False}
    journal: dict[str, object] = {
        "schema": 1,
        "operation": operation,
        "phase": "staged",
        "old_version": old_version,
        "new_version": new_version,
        "activation_version": activation_version,
        "cleanup_version": old_version if operation == "install" and old_version != new_version else None,
        "cleanup_records": old_records if operation == "install" and old_version != new_version else None,
        "old_version_records": activation_old_records,
        "new_version_records": new_version_records,
        "old_adapter": old_adapter,
        "new_adapter": new_adapter,
        "old_config": old_config,
        "new_config": new_config_state,
        "old_launcher": old_launcher,
        "new_launcher": {"exists": operation == "install", "kind": "symlink", "target": str((paths.data_root / str(new_version) / "bin" / "concord").resolve())} if operation == "install" else {"exists": False},
        "old_manifest": old_manifest_state,
        "new_manifest": new_manifest_state,
        "backup_dir": "backup",
        "stage_dir": "stage",
        "version_backup": "backup/version",
        "live_version_backup": "backup/live-version",
        "targets": {
            "version": str(activation_root.resolve(strict=False)),
            "adapters": [str((paths.tools_dir / name).resolve(strict=False)) for name in ADAPTER_FILES],
            "launcher": str(paths.launcher),
            "config": str(paths.config_file.resolve(strict=False)),
            "manifest": str((paths.data_root / MANIFEST_NAME).resolve(strict=False)),
        },
    }
    write_journal(transaction_root, journal)
    fsync_directory(parent)
    if os.environ.get("CONCORD_INSTALLER_STOP_AFTER_PHASE") == "staged":
        os._exit(97)
    return transaction_root, journal


def apply_version(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    version = journal["activation_version"]
    if not isinstance(version, str):
        raise InstallerError("transaction has no activation version")
    target = paths.data_root / version
    live = transaction_root / "backup" / "live-version"
    if target.exists() or target.is_symlink():
        if live.exists() or live.is_symlink():
            raise InstallerError("transaction live version backup already exists")
        replace_durable(target, live)
    if journal["operation"] == "install":
        replace_durable(transaction_root / "stage" / "version", target)


def apply_adapters(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    for name in ADAPTER_FILES:
        target = paths.tools_dir / name
        if journal["operation"] == "install":
            version = journal["new_version"]
            source = paths.data_root / str(version) / "adapter" / "opencode" / name
            write_atomic(target, source.read_bytes())
        elif target.exists() or target.is_symlink():
            target.unlink()
    fsync_directory(paths.tools_dir)


def apply_launcher(transaction_root: Path, journal: dict[str, object], paths: Paths) -> None:
    target = paths.launcher
    if journal["operation"] == "install":
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
        write_atomic(target, (transaction_root / "stage" / "config").read_bytes())
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
            if not version_matches(target, new_records if journal["operation"] == "install" else None):
                raise InstallerError(f"transaction conflict at {target}; refusing rollback")
            shutil.rmtree(target)
        replace_durable(live, target)
    elif journal["operation"] == "install":
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
        if operation == "install" and not version_matches(paths.data_root / str(version), records):
            raise InstallerError("transaction conflict: new version data is not intact")
        if operation == "uninstall" and journal["activation_version"] and (paths.data_root / str(journal["activation_version"])).exists():
            raise InstallerError("transaction conflict: uninstalled version data reappeared")
        adapter_expected = journal["new_adapter"]
        launcher_expected = journal["new_launcher"]
        config_expected = journal["new_config"]
        manifest_expected = journal["new_manifest"]
    else:
        version = journal["activation_version"]
        records = journal["old_version_records"]
        if records is not None and not version_matches(paths.data_root / str(version), records):
            raise InstallerError("transaction conflict: old version data is not intact")
        cleanup_version = journal.get("cleanup_version")
        cleanup_records = journal.get("cleanup_records")
        if isinstance(cleanup_version, str) and not version_matches(paths.data_root / cleanup_version, cleanup_records):
            raise InstallerError("transaction conflict: prior version data is not intact")
        adapter_expected = journal["old_adapter"]
        launcher_expected = journal["old_launcher"]
        config_expected = journal["old_config"]
        manifest_expected = journal["old_manifest"]
    for name in ADAPTER_FILES:
        if committed:
            digest = adapter_expected.get(name) if isinstance(adapter_expected, dict) else None
            expected = {"exists": True, "kind": "file", "sha256": digest} if digest else {"exists": False}
        else:
            expected = adapter_expected[name] if isinstance(adapter_expected, dict) and name in adapter_expected else {"exists": False}
        if not state_matches(paths.tools_dir / name, expected):
            raise InstallerError(f"transaction conflict at adapter {name}")
    if not state_matches(paths.launcher, launcher_expected):
        raise InstallerError("transaction conflict at launcher")
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
    current_launcher = file_state(paths.launcher)
    if current_launcher.get("exists") and not state_matches(paths.launcher, journal["old_launcher"]) and not state_matches(paths.launcher, journal["new_launcher"]):
        raise InstallerError("transaction conflict at launcher; refusing rollback")
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
    cleanup_version = journal.get("cleanup_version") if remove_old_version else None
    if isinstance(cleanup_version, str):
        root = paths.data_root / cleanup_version
        records = journal.get("cleanup_records")
        if root.exists():
            if not isinstance(records, dict) or not version_matches(root, records):
                raise InstallerError(f"transaction conflict at old version cleanup target {root}")
            shutil.rmtree(root)
            fsync_directory(paths.data_root)
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
    for directory in (paths.tools_dir, paths.bin_dir, paths.data_root):
        try:
            directory.rmdir()
        except OSError:
            pass


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
    current_manifest = current_manifest_state(paths)
    if phase not in {"manifest_committed", "cleanup"} and new_manifest.get("exists") and state_matches(paths.data_root / MANIFEST_NAME, new_manifest):
        advance_phase(transaction_root, journal, "manifest_committed")
        phase = "manifest_committed"
    if phase in {"manifest_committed", "cleanup"}:
        verify_states(journal, paths, committed=True)
        if phase != "cleanup":
            advance_phase(transaction_root, journal, "cleanup")
        cleanup_transaction(transaction_root, journal, paths)
        return
    ensure_rollback_safe(journal, paths)
    rollback_version(transaction_root, journal, paths)
    for name in ADAPTER_FILES:
        state = journal["old_adapter"][name]
        restore_file(paths.tools_dir / name, state, transaction_root, f"adapter/{name}")
    restore_launcher(paths, journal["old_launcher"])
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
    version = parse_version(args.version)
    paths = paths_for(args.root)
    recover_transactions(paths)
    manifest = load_manifest(paths)
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
        if not paths.launcher.is_symlink() or os.readlink(paths.launcher) != manifest.get("launcher_target"):
            raise InstallerError(f"refusing to repair modified managed launcher {paths.launcher}")
        skill_path = str((paths.data_root / version / "skills").resolve())
        if config_plan.changed or manifest.get("skill_path") != skill_path:
            raise InstallerError("existing installation registration is incomplete; refusing an unsafe repair")
        print(f"Concord {version} is already installed; no changes made.")
        return 0

    with tempfile.TemporaryDirectory(prefix="concord-installer-") as temporary:
        workspace = Path(temporary)
        extracted, _checksums = extract_verified_artifact(
            version,
            Path(args.artifact_dir).resolve() if args.artifact_dir else None,
            args.base_url,
            workspace,
        )
        source_stage = workspace / "version"
        (source_stage / "bin").mkdir(parents=True)
        (source_stage / "adapter" / "opencode").mkdir(parents=True)
        (source_stage / "skills").mkdir(parents=True)
        shutil.copy2(extracted / "bin" / "concord", source_stage / "bin" / "concord")
        os.chmod(source_stage / "bin" / "concord", 0o755)
        for name in ADAPTER_FILES:
            shutil.copy2(extracted / "adapter" / "opencode" / name, source_stage / "adapter" / "opencode" / name)
        source_skills = extracted / "skills"
        if source_skills.exists():
            for source in source_skills.rglob("*"):
                relative = source.relative_to(source_skills)
                destination = source_stage / "skills" / relative
                if source.is_dir():
                    destination.mkdir(parents=True, exist_ok=True)
                elif source.is_file():
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(source, destination)
        fsync_tree(source_stage)
        managed_version_paths = [str(path.relative_to(source_stage)) for path in source_stage.rglob("*") if path.is_file()]
        version_records = file_records(source_stage, managed_version_paths)
        adapter_stage_records = {name: sha256(source_stage / "adapter" / "opencode" / name) for name in ADAPTER_FILES}
        old_version = manifest.get("version") if manifest else None
        old_records = manifest.get("version_files", {}) if manifest else None
        version_root = paths.data_root / version
        if version_root.exists() and (not manifest or old_version != version):
            raise InstallerError(f"refusing to overwrite existing unmanaged path {version_root}")
        if manifest and old_version != version and old_version and not isinstance(old_records, dict):
            raise InstallerError("existing installer manifest has invalid version records")
        if manifest and old_version != version and isinstance(old_version, str) and isinstance(old_records, dict):
            validate_owned_tree(paths.data_root / old_version, old_records)
        new_manifest = {
            "managed_by": "concord-installer-v1",
            "version": version,
            "version_files": version_records,
            "adapter_files": adapter_stage_records,
            "skill_path": str((version_root / "skills").resolve()),
            "launcher_target": str((version_root / "bin" / "concord").resolve()),
            "config_path": str(paths.config_file.resolve()),
        }
        new_manifest_bytes = (json.dumps(new_manifest, indent=2, sort_keys=True) + "\n").encode("utf-8")
        transaction_root, journal = make_transaction(
            paths,
            "install",
            manifest,
            version,
            source_stage,
            version_records,
            adapter_stage_records,
            config_plan.text,
            new_manifest_bytes,
        )
        apply_version(transaction_root, journal, paths)
        advance_phase(transaction_root, journal, "version_activated")
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
    print(f"Installed Concord {version} under {version_root}.")
    print(f"OpenCode custom tools installed under {paths.tools_dir}.")
    print("Restart OpenCode before using the newly registered versioned skills path.")
    return 0


def uninstall(args: argparse.Namespace) -> int:
    paths = paths_for(args.root)
    recover_transactions(paths)
    manifest = load_manifest(paths)
    if manifest is None:
        print("No Concord installer manifest found; nothing was changed.")
        return 0
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
    launcher_target = manifest["launcher_target"]
    launcher = safe_relative_target(paths.bin_dir, "concord", "launcher", allow_final_symlink=True)
    if launcher.exists() or launcher.is_symlink():
        if not launcher.is_symlink() or os.readlink(launcher) != launcher_target:
            raise InstallerError(f"refusing to remove user-authored launcher {paths.launcher}")
    skill_path = manifest["skill_path"]
    assert isinstance(skill_path, str)
    config_path = safe_relative_target(paths.config_file.parent, paths.config_file.name, "OpenCode config")
    current_config = config_path.read_text(encoding="utf-8") if config_path.exists() else None
    new_config = remove_path_from_config(config_path, skill_path) if current_config is not None else None
    transaction_root, journal = make_transaction(
        paths,
        "uninstall",
        manifest,
        None,
        None,
        None,
        None,
        new_config,
        None,
    )
    apply_version(transaction_root, journal, paths)
    advance_phase(transaction_root, journal, "version_activated")
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
    for directory in (paths.tools_dir, paths.bin_dir, paths.data_root):
        try:
            directory.rmdir()
        except OSError:
            pass
    print(f"Uninstalled Concord {version} managed files.")
    print("Restart OpenCode before assuming the removed versioned skills path is gone.")
    return 0


def status(args: argparse.Namespace) -> int:
    paths = paths_for(args.root)
    recover_transactions(paths)
    manifest = load_manifest(paths)
    if manifest is None:
        print(json.dumps({"installed": False}, sort_keys=True))
    else:
        print(json.dumps({"installed": True, "version": manifest["version"]}, sort_keys=True))
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    install_parser = subparsers.add_parser("install", help="install or upgrade a release")
    install_parser.add_argument("--version", required=True)
    install_parser.add_argument("--artifact-dir", help="use local published assets instead of downloading")
    install_parser.add_argument(
        "--base-url",
        default="https://github.com/Sharper-Flow/concord/releases/latest/download",
        help="release asset base URL when --artifact-dir is absent",
    )
    uninstall_parser = subparsers.add_parser("uninstall", help="remove the managed release")
    status_parser = subparsers.add_parser("status", help="recover and report installation state")
    for command_parser in (install_parser, uninstall_parser, status_parser):
        command_parser.add_argument("--root", type=Path, help="test root; maps home/data/config/bin under it")
    args = parser.parse_args()
    try:
        if args.command == "install":
            return install(args)
        if args.command == "uninstall":
            return uninstall(args)
        return status(args)
    except InstallerError as error:
        print(str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
