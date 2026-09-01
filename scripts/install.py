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
from pathlib import Path


VERSION_RE = re.compile(r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")
MANIFEST_NAME = "install-manifest.json"
ADAPTER_FILES = (
    "concord.ts",
    "concord-plugin.ts",
    "credentials.ts",
    "continuity-hook.ts",
    "dispatch.ts",
    "generated-agent-lanes.ts",
    "generated-contract-tests.ts",
    "generated-contracts.ts",
    "lane_dispatch.ts",
    "packet.ts",
)
# The OpenCode plugin entry module, shipped in ADAPTER_FILES and installed into
# the tools directory. Unlike the tool modules it is registered by path in the
# host `plugin` array so OpenCode loads it as the adapter's plugin factory.
PLUGIN_ENTRY_FILE = "concord-plugin.ts"
INSTRUCTION_FILES = (
    "README.md",
    "asking.md",
    "change.md",
    "completion.md",
    "evidence.md",
    "voice.md",
)
AGENT_FILES = (
    "concord-design.md",
    "concord-implement.md",
    "concord-research.md",
    "concord-review.md",
    "concord-verify.md",
)
STABLE_ROOT_NAME = "current"
AGENT_GLOB = "concord-*.md"
CREDENTIAL_UNIT_NAME = "concord-keyring-unlock.service"
SECRET_SERVICE_DESTINATION = "org.freedesktop.secrets"
SECRET_SERVICE_PATH = "/org/freedesktop/secrets"
SECRET_SERVICE_INTERFACE = "org.freedesktop.Secret.Service"


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
    launcher: Path
    stable_root: Path


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
        # A per-project session shard (the `oc` wrapper sets XDG_DATA_HOME to
        # one) is not a durable data home: an install there scatters versioned
        # assets and the install manifest where no later run looks, and the
        # next install outside the session then classifies every installed
        # file as user-authored. Refuse by name instead (#648).
        if "/opencode-projects/" in str(data_home):
            raise InstallerError(
                "refusing installer data root under a per-project session shard: "
                f"XDG_DATA_HOME={data_home} would install under {data_home / 'concord'} "
                f"instead of the durable {home / '.local' / 'share' / 'concord'}; "
                "rerun with `env -u XDG_DATA_HOME` or a durable XDG_DATA_HOME"
            )
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
        launcher=bin_dir / "concord",
        stable_root=data_home / "concord" / STABLE_ROOT_NAME,
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


def plugin_entry_path(paths: Paths) -> str:
    """Absolute, version-stable path of the installed plugin entry module."""
    return str((paths.tools_dir / PLUGIN_ENTRY_FILE).resolve())


def drop_string_token(original: str, token: str) -> str:
    """Remove one exact JSON string token and a single adjacent comma.

    Shared by the skills and plugin deregistration paths. The caller guarantees
    the token occurs exactly once in the original text.
    """
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


def plan_plugin_entry(text: str, entry_path: str) -> str:
    """Ensure the plugin entry module path is registered in the host plugin array.

    The plugin path is version-stable, so this is an idempotent ensure-present:
    it adds the path when absent and leaves an existing registration untouched.
    Raises InstallerError carrying the exact manual entry when the existing
    plugin configuration cannot be edited safely.
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
    if any(isinstance(entry, str) and entry == entry_path for entry in plugin):
        return text
    matches = list(re.finditer(r'"plugin"\s*:\s*\[', text))
    if len(matches) != 1:
        raise InstallerError(
            f"cannot locate the plugin array in the OpenCode config; add {token} to it manually"
        )
    insert_at = matches[0].end()
    insertion = f"\n    {token}\n  " if len(plugin) == 0 else f"\n    {token},"
    return text[:insert_at] + insertion + text[insert_at:]


def remove_plugin_entry(text: str, entry_path: str) -> str:
    """Remove the managed plugin entry registration. No-op when it is absent."""
    parsed = jsonc_data(text)
    if not isinstance(parsed, dict):
        return text
    plugin = parsed.get("plugin")
    if not isinstance(plugin, list) or not any(isinstance(entry, str) and entry == entry_path for entry in plugin):
        return text
    token = json.dumps(entry_path)
    if text.count(token) != 1:
        raise InstallerError("cannot safely remove the managed plugin entry from the OpenCode config")
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
    if set(manifest) != required or manifest.get("managed_by") != "concord-installer-v1":
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

    version_files = manifest.get("version_files")
    if not isinstance(version_files, dict) or not version_files:
        raise InstallerError("installer manifest has invalid version file records")
    allowed_fixed = {"bin/concord", *(f"adapter/opencode/{name}" for name in ADAPTER_FILES)}
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

    expected_skill = paths.data_root / version / "skills"
    skill_path = manifest.get("skill_path")
    if not isinstance(skill_path, str) or Path(skill_path).resolve(strict=False) != expected_skill.resolve(strict=False):
        raise InstallerError("installer manifest has a redirected skills path")
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


def credential_unit_text(daemon: str) -> str:
    if any(character.isspace() for character in daemon):
        raise InstallerError(f"gnome-keyring-daemon path contains whitespace: {daemon}")
    return f"""[Unit]
Description=Unlock the user Secret Service collection for Concord
Requires=gnome-keyring-daemon.service
After=gnome-keyring-daemon.service
PartOf=gnome-keyring-daemon.service

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'printf "\\n" | {daemon} --unlock --control-directory=%t/keyring'

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


def initialize_login_collection(paths: Paths, daemon: str) -> None:
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
        return
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


def stop_unmanaged_secret_service(systemctl: str, daemon: str) -> None:
    run_checked([systemctl, "--user", "start", "gnome-keyring-daemon.socket"])
    owner = secret_service_owner_pid()
    main_result = run_checked(
        [systemctl, "--user", "show", "--property=MainPID", "--value", "gnome-keyring-daemon.service"]
    )
    main_pid = int(main_result.stdout.strip() or "0")
    if owner and owner != main_pid:
        for collection in secret_service_collections():
            if secret_service_collection_has_items(collection):
                raise InstallerError(
                    "the active non-systemd Secret Service contains session items; refusing to stop it during setup"
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


def ensure_secret_service_ready(paths: Paths) -> None:
    daemon = command_status("gnome-keyring-daemon")
    systemctl = command_status("systemctl")
    if daemon is None or systemctl is None:
        raise InstallerError("gnome-keyring-daemon and systemctl are required for credential setup")
    login = secret_service_alias("login")
    keyrings = paths.data_home / "keyrings"
    if login is not None and not secret_service_collection_locked(login) and not keyrings.exists():
        # A compatible provider such as KeePassXC can own Secret Service
        # without GNOME Keyring files. It already satisfies the contract, so
        # do not start a competing provider or install an unrelated unit.
        return
    if login is None:
        for collection in secret_service_collections():
            if secret_service_collection_has_items(collection):
                raise InstallerError("Secret Service contains session items; refusing headless collection initialization")
        initialize_login_collection(paths, daemon)
    unit_text = credential_unit_text(daemon)
    if paths.credential_unit.exists():
        if not paths.credential_unit.is_file() or paths.credential_unit.read_text(encoding="utf-8") != unit_text:
            raise InstallerError(f"refusing to overwrite user-authored credential unit {paths.credential_unit}")
    else:
        write_atomic(paths.credential_unit, unit_text.encode("utf-8"), 0o644)

    if login is None:
        stop_unmanaged_secret_service(systemctl, daemon)

    run_checked([systemctl, "--user", "daemon-reload"])
    run_checked([systemctl, "--user", "enable", "--now", CREDENTIAL_UNIT_NAME])
    login = secret_service_alias("login")
    if login is None or secret_service_collection_locked(login):
        raise InstallerError("Secret Service login collection is unavailable after noninteractive setup")
    if keyrings.exists():
        verify_credential_permissions(keyrings)



def unmanaged_manifest_note(old_manifest: dict[str, object] | None, paths: Paths) -> str:
    """Name the manifest a preflight refusal consulted, so an install run from
    a redirected data root explains why it sees no managed files (#648)."""
    if old_manifest:
        return ""
    return f" (no install manifest at {paths.data_root / MANIFEST_NAME})"

def preflight(paths: Paths, version: str, old_manifest: dict[str, object] | None) -> ConfigPlan:
    failures: list[str] = []
    for managed_parent in (
        paths.data_root,
        paths.tools_dir,
        paths.agents_dir,
        paths.systemd_user_dir,
        paths.bin_dir,
        paths.config_file.parent,
    ):
        if managed_parent.is_symlink():
            failures.append(f"refusing symlinked managed path {managed_parent}")
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
        expected_unit = credential_unit_text(daemon)
        if (
            paths.credential_unit.is_symlink()
            or not paths.credential_unit.is_file()
            or paths.credential_unit.read_text(encoding="utf-8") != expected_unit
        ):
            failures.append(f"refusing to overwrite user-authored credential unit {paths.credential_unit}")
    adapter_records = managed_adapter_records(old_manifest)
    manifest_note = unmanaged_manifest_note(old_manifest, paths)
    for name in ADAPTER_FILES:
        destination = paths.tools_dir / name
        if destination.exists() or destination.is_symlink():
            if not old_manifest or name not in adapter_records:
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
        config_plan = plan_config(paths.config_file, str((paths.data_root / version / "skills").resolve()), old_skill)
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
                raise InstallerError(f"refusing to overwrite user-authored agent file {target}")
            if not target.is_file() or sha256(target) != expected:
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
    new_launcher_state = {"exists": operation == "install", "kind": "symlink", "target": str((paths.data_root / str(new_version) / "bin" / "concord").resolve())} if operation == "install" else {"exists": False}
    # The uninstall branch records what apply_stable_root will produce: a
    # removal when the current symlink points inside the data root, a no-op
    # otherwise. verify_states compares the journal entry against the on-disk
    # state, so the recorded target must match what apply actually leaves.
    if operation == "install":
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
        "cleanup_version": old_version if operation == "install" and old_version != new_version else None,
        "cleanup_records": old_records if operation == "install" and old_version != new_version else None,
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
    if journal["operation"] == "install":
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
    if journal["operation"] == "install":
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
        agents_expected = journal.get("new_agents") or {}
        launcher_expected = journal["new_launcher"]
        config_expected = journal["new_config"]
        manifest_expected = journal["new_manifest"]
        stable_root_expected = journal.get("new_stable_root", {"exists": False})
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
    for directory in (paths.tools_dir, paths.agents_dir, paths.bin_dir, paths.data_root):
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
        agent_records = managed_agent_records(manifest)
        for name, expected in agent_records.items():
            destination = paths.agents_dir / name
            if not destination.is_file() or sha256(destination) != expected:
                raise InstallerError(f"refusing to overwrite modified managed agent file {destination}")
        if not paths.launcher.is_symlink() or os.readlink(paths.launcher) != manifest.get("launcher_target"):
            raise InstallerError(f"refusing to repair modified managed launcher {paths.launcher}")
        if not paths.stable_root.is_symlink() or os.readlink(paths.stable_root) != str(paths.data_root / version):
            raise InstallerError(f"refusing to repair modified managed stable root {paths.stable_root}")
        skill_path = str((paths.data_root / version / "skills").resolve())
        if config_plan.changed or manifest.get("skill_path") != skill_path:
            raise InstallerError("existing installation registration is incomplete; refusing an unsafe repair")
        ensure_secret_service_ready(paths)
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
        shutil.copy2(extracted / "bin" / "concord", source_stage / "bin" / "concord")
        os.chmod(source_stage / "bin" / "concord", 0o755)
        for name in ADAPTER_FILES:
            shutil.copy2(extracted / "adapter" / "opencode" / name, source_stage / "adapter" / "opencode" / name)
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
            "agent_files": agent_stage_records,
            "skill_path": str((version_root / "skills").resolve()),
            "stable_root": str(paths.stable_root),
            "launcher_target": str((version_root / "bin" / "concord").resolve()),
            "config_path": str(paths.config_file.resolve()),
        }
        new_manifest_bytes = (json.dumps(new_manifest, indent=2, sort_keys=True) + "\n").encode("utf-8")
        ensure_secret_service_ready(paths)
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
    print(f"Installed Concord {version} under {version_root}.")
    print(f"OpenCode custom tools installed under {paths.tools_dir}.")
    print(f"Concord agent definitions installed under {paths.agents_dir}.")
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
    agent_records = managed_agent_records(manifest)
    for name, expected in agent_records.items():
        destination = paths.agents_dir / name
        if destination.exists() and (not destination.is_file() or sha256(destination) != expected):
            raise InstallerError(f"refusing to remove modified agent file {destination}")
    launcher_target = manifest["launcher_target"]
    launcher = safe_relative_target(paths.bin_dir, "concord", "launcher", allow_final_symlink=True)
    if launcher.exists() or launcher.is_symlink():
        if not launcher.is_symlink() or os.readlink(launcher) != launcher_target:
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
    for directory in (paths.tools_dir, paths.agents_dir, paths.bin_dir, paths.data_root):
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


def project_opencode_json(project_dir: Path) -> Path:
    """The per-project OpenCode config the installer reads and edits."""
    return project_dir / ".opencode" / "opencode.json"


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
    if not project_file.exists():
        new_text = '{\n  "instructions": [\n    ' + json.dumps(conduct_entry) + "\n  ]\n}\n"
        return new_text, True
    original = project_file.read_text(encoding="utf-8")
    try:
        parsed = json.loads(original)
    except json.JSONDecodeError as error:
        raise InstallerError(f"cannot parse project opencode config {project_file}: {error}") from error
    if not isinstance(parsed, dict):
        raise InstallerError(
            f"project opencode config {project_file} is not an object; add "
            f"{conduct_entry!r} to its instructions array manually"
        )
    instructions = parsed.get("instructions")
    if instructions is None:
        # Insert "instructions": [...] before the closing brace.
        end = original.rfind("}")
        if end < 0:
            raise InstallerError(f"project opencode config {project_file} has no JSON object to edit")
        before = original[:end]
        separator = "" if before.rstrip().endswith("{") else ","
        addition = f'{separator}\n  "instructions": [\n    {json.dumps(conduct_entry)}\n  ]\n'
        return original[:end] + addition + original[end:], True
    if not isinstance(instructions, list) or not all(isinstance(value, str) for value in instructions):
        raise InstallerError(
            f"project opencode config {project_file} has a non-array instructions entry; "
            f"add {conduct_entry!r} to that array manually"
        )
    if conduct_entry in instructions:
        return original, False
    # Append the entry. JSONC-style comments would be lost on round-trip, but
    # the spec says this is plain .json.
    new_instructions = instructions + [conduct_entry]
    parsed["instructions"] = new_instructions
    return json.dumps(parsed, indent=2) + "\n", True


def remove_conduct_entry(project_file: Path, conduct_entry: str) -> tuple[str, bool]:
    """Compute new project file contents removing the conduct entry.

    Returns (new_text_or_empty, changed). When the file would have no keys
    left after the removal, returns ("", True) so the caller deletes it.
    """
    if not project_file.exists():
        return "", False
    original = project_file.read_text(encoding="utf-8")
    try:
        parsed = json.loads(original)
    except json.JSONDecodeError as error:
        raise InstallerError(f"cannot parse project opencode config {project_file}: {error}") from error
    if not isinstance(parsed, dict):
        raise InstallerError(
            f"project opencode config {project_file} is not an object; "
            f"remove {conduct_entry!r} from its instructions array manually"
        )
    instructions = parsed.get("instructions")
    if not isinstance(instructions, list) or conduct_entry not in instructions:
        return original, False
    new_instructions = [value for value in instructions if value != conduct_entry]
    if not new_instructions:
        parsed.pop("instructions", None)
    else:
        parsed["instructions"] = new_instructions
    if not parsed:
        return "", True
    return json.dumps(parsed, indent=2) + "\n", True


def link(args: argparse.Namespace) -> int:
    paths = paths_for(args.root)
    recover_transactions(paths)
    manifest = load_manifest(paths)
    if manifest is None:
        raise InstallerError("cannot link a project: no Concord installation is present")
    project_dir = Path(args.project).resolve()
    project_file = project_opencode_json(project_dir)
    conduct_entry = conduct_instruction_entry(paths)
    new_text, changed = plan_project_link(project_file, conduct_entry)
    if not changed:
        print(f"Project {project_dir} already points at the conduct corpus; no changes made.")
        return 0
    ensure_directory(project_file.parent, mode=0o755)
    write_atomic(project_file, new_text.encode("utf-8"))
    print(f"Linked project {project_dir} to {conduct_entry}.")
    return 0


def unlink(args: argparse.Namespace) -> int:
    paths = paths_for(args.root)
    recover_transactions(paths)
    project_dir = Path(args.project).resolve()
    project_file = project_opencode_json(project_dir)
    conduct_entry = conduct_instruction_entry(paths)
    new_text, changed = remove_conduct_entry(project_file, conduct_entry)
    if not changed:
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
    print(f"Unlinked project {project_dir} from the conduct corpus.")
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
    link_parser = subparsers.add_parser(
        "link", help="register the conduct corpus for a project (per-project, not global)"
    )
    link_parser.add_argument("--project", type=Path, required=True, help="project directory to update")
    unlink_parser = subparsers.add_parser("unlink", help="remove the conduct corpus entry from a project")
    unlink_parser.add_argument("--project", type=Path, required=True, help="project directory to update")
    for command_parser in (install_parser, uninstall_parser, status_parser, link_parser, unlink_parser):
        command_parser.add_argument("--root", type=Path, help="test root; maps home/data/config/bin under it")
    args = parser.parse_args()
    try:
        if args.command == "install":
            return install(args)
        if args.command == "uninstall":
            return uninstall(args)
        if args.command == "link":
            return link(args)
        if args.command == "unlink":
            return unlink(args)
        return status(args)
    except InstallerError as error:
        print(str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
