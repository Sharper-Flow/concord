#!/usr/bin/env python3
"""Integration tests for the Concord installer using temporary roots."""
from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock
from pathlib import Path

import install as installer


SCRIPT = Path(__file__).with_name("install.py")


class InstallerTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        self.commands = self.root / "bin"
        self.commands.mkdir()
        self.artifacts = self.root / "published"
        self.artifacts.mkdir()
        self.write_command("git", "exit 0")
        self.write_command("opencode", "exit 0")
        self.write_command("secret-tool", "exit 0")
        self.write_command("gnome-keyring-daemon", "exit 0")
        self.write_command(
            "busctl",
            r'''case "$*" in
  "--user --list") printf 'org.freedesktop.secrets\n' ;;
  *"ReadAlias"*) printf 'o "/org/freedesktop/secrets/collection/login"\n' ;;
  *" Locked") printf 'b false\n' ;;
  *) exit 2 ;;
esac''',
        )
        self.write_command("dbus-run-session", "exit 0")
        self.write_command("systemctl", "exit 0")
        self.env = os.environ.copy()
        self.env["PATH"] = str(self.commands) + os.pathsep + os.defpath
        self.config.parent.mkdir(parents=True)
        self.config.write_text(
            '{\n  "$schema": "https://opencode.ai/config.json",\n  "keep": true\n}\n',
            encoding="utf-8",
        )

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    @property
    def config(self) -> Path:
        return self.root / "config" / "opencode" / "opencode.jsonc"

    def write_command(self, name: str, body: str) -> None:
        path = self.commands / name
        path.write_text(f"#!/bin/sh\n{body}\n", encoding="utf-8")
        path.chmod(0o755)

    def make_release(
        self,
        version: str,
        marker: str | None = None,
        include_binary_checksum: bool = True,
        agent_names: tuple[str, ...] | None = None,
    ) -> None:
        prefix = f"concord-{version}"
        source = self.root / f"source-{version}"
        (source / "bin").mkdir(parents=True)
        (source / "adapter" / "opencode").mkdir(parents=True)
        (source / "skills").mkdir()
        (source / "skills" / "concord-demo.md").write_text(f"skill:{marker or version}\n", encoding="utf-8")
        # CD-0063 ships an always-on conduct corpus and the central agent
        # definitions. The fixture must include them so the install path under
        # test exercises the staging, sha256, and central placement logic.
        (source / "instructions").mkdir()
        for name in installer.INSTRUCTION_FILES:
            (source / "instructions" / name).write_text(f"rule:{name}:{marker or version}\n", encoding="utf-8")
        (source / "agents").mkdir()
        for name in agent_names if agent_names is not None else installer.AGENT_FILES:
            (source / "agents" / name).write_text(f"agent:{name}:{marker or version}\n", encoding="utf-8")
        binary = (source / "bin" / "concord")
        binary.write_bytes((marker or version).encode("utf-8"))
        for name in installer.ADAPTER_FILES:
            (source / "adapter" / "opencode" / name).write_text(f"{name}:{marker or version}\n", encoding="utf-8")
        archive = self.artifacts / f"{prefix}.tar.gz"
        with tarfile.open(archive, "w:gz") as bundle:
            for path in sorted(source.rglob("*")):
                bundle.add(path, path.relative_to(source))
        checksum = self.artifacts / f"{prefix}.sha256"
        lines = [f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {prefix}.tar.gz"]
        if include_binary_checksum:
            lines.insert(0, f"{hashlib.sha256(binary.read_bytes()).hexdigest()}  {prefix}")
        checksum.write_text("\n".join(lines) + "\n", encoding="utf-8")

    def run_installer(self, *arguments: str, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
        command = [sys.executable, str(SCRIPT), *arguments, "--root", str(self.root)]
        return subprocess.run(command, text=True, capture_output=True, env=self.env if env is None else env)

    def run_after_phase(self, phase: str, *arguments: str) -> subprocess.CompletedProcess[str]:
        environment = self.env.copy()
        environment["CONCORD_INSTALLER_STOP_AFTER_PHASE"] = phase
        return self.run_installer(*arguments, env=environment)

    def reset_config(self) -> None:
        self.config.write_text(
            '{\n  "$schema": "https://opencode.ai/config.json",\n  "keep": true\n}\n',
            encoding="utf-8",
        )

    def test_checksum_mismatch_refuses_without_installing(self) -> None:
        self.make_release("v1.0.0")
        archive = self.artifacts / "concord-v1.0.0.tar.gz"
        archive.write_bytes(archive.read_bytes() + b"tampered")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum mismatch", result.stderr)
        self.assertFalse((self.root / "data").exists())
        self.assertNotIn("skills", self.config.read_text(encoding="utf-8"))

    def test_missing_binary_checksum_refuses_without_installing(self) -> None:
        self.make_release("v1.0.0", include_binary_checksum=False)
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not contain the binary entry", result.stderr)
        self.assertFalse((self.root / "data").exists())

    def test_missing_prerequisite_refuses_without_installing(self) -> None:
        self.make_release("v1.0.0")
        (self.commands / "secret-tool").unlink()
        environment = self.env.copy()
        environment["PATH"] = str(self.commands)
        result = self.run_installer(
            "install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts), env=environment
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing command secret-tool", result.stderr)
        self.assertIn("worker evidence signing fails closed", result.stderr)
        self.assertFalse((self.root / "data").exists())

    def test_headless_install_creates_noninteractive_persistent_credential_collection(self) -> None:
        self.make_release("v1.0.0")
        state = self.root / "credential-state"
        command_log = self.root / "credential-commands.log"
        keyrings = self.root / "data" / "keyrings"
        self.env["TEST_CREDENTIAL_STATE"] = str(state)
        self.env["TEST_CREDENTIAL_LOG"] = str(command_log)
        self.env["TEST_KEYRINGS"] = str(keyrings)
        self.write_command(
            "busctl",
            r'''printf '%s\n' "$*" >>"$TEST_CREDENTIAL_LOG"
case "$*" in
  "--user --list") printf 'org.freedesktop.secrets\n' ;;
  *"ReadAlias"*)
    if [ -f "$TEST_CREDENTIAL_STATE" ]; then
      printf 'o "/org/freedesktop/secrets/collection/login"\n'
    else
      printf 'o "/"\n'
    fi ;;
  *" Collections") printf 'ao 1 "/org/freedesktop/secrets/collection/session"\n' ;;
  *" Items") printf 'ao 0\n' ;;
  *" Locked")
    if [ "$(cat "$TEST_CREDENTIAL_STATE" 2>/dev/null)" = ready ]; then printf 'b false\n'; else printf 'b true\n'; fi ;;
  *"status org.freedesktop.secrets") printf 'PID=0\n' ;;
  *) exit 2 ;;
esac''',
        )
        self.write_command(
            "dbus-run-session",
            r'''printf 'dbus-run-session %s\n' "$*" >>"$TEST_CREDENTIAL_LOG"
mkdir -p "$TEST_KEYRINGS"
printf 'store' >"$TEST_KEYRINGS/user.keystore"
printf 'login' >"$TEST_KEYRINGS/login.keyring"
chmod 700 "$TEST_KEYRINGS"
chmod 600 "$TEST_KEYRINGS/user.keystore" "$TEST_KEYRINGS/login.keyring"
printf 'created\n' >"$TEST_CREDENTIAL_STATE"''',
        )
        self.write_command(
            "systemctl",
            r'''printf '%s\n' "$*" >>"$TEST_CREDENTIAL_LOG"
case "$*" in
  *"show"*) printf '0\n' ;;
  *"enable --now concord-keyring-unlock.service"*) printf 'ready\n' >"$TEST_CREDENTIAL_STATE" ;;
esac''',
        )

        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        unit = self.root / "config" / "systemd" / "user" / "concord-keyring-unlock.service"
        self.assertTrue(unit.is_file())
        self.assertEqual(unit.stat().st_mode & 0o777, 0o644)
        self.assertEqual(keyrings.stat().st_mode & 0o777, 0o700)
        self.assertEqual((keyrings / "login.keyring").stat().st_mode & 0o777, 0o600)
        self.assertEqual((keyrings / "user.keystore").stat().st_mode & 0o777, 0o600)
        combined = first.stdout + first.stderr + unit.read_text(encoding="utf-8")
        self.assertNotIn("base64:", combined)
        self.assertNotIn("private_key", combined)
        self.assertEqual(state.read_text(encoding="utf-8").strip(), "ready")
        commands = command_log.read_text(encoding="utf-8")
        self.assertIn("dbus-run-session", commands)
        self.assertIn("enable --now concord-keyring-unlock.service", commands)

        second = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertIn("already installed", second.stdout)

    def test_install_keeps_an_existing_compatible_secret_service(self) -> None:
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(result.returncode, 0, result.stderr)
        unit = self.root / "config" / "systemd" / "user" / "concord-keyring-unlock.service"
        self.assertFalse(unit.exists())

    def test_headless_install_refuses_to_discard_session_credentials(self) -> None:
        self.make_release("v1.0.0")
        self.write_command(
            "busctl",
            r'''case "$*" in
  "--user --list") printf 'org.freedesktop.secrets\n' ;;
  *"ReadAlias"*) printf 'o "/"\n' ;;
  *" Collections") printf 'ao 1 "/org/freedesktop/secrets/collection/session"\n' ;;
  *" Items") printf 'ao 1 "/org/freedesktop/secrets/collection/session/1"\n' ;;
  *) exit 2 ;;
esac''',
        )
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("session items", result.stderr)
        self.assertFalse((self.root / "config" / "systemd" / "user" / installer.CREDENTIAL_UNIT_NAME).exists())
        self.assertFalse((self.root / "data" / "concord").exists())

    def test_install_is_idempotent(self) -> None:
        self.make_release("v1.0.0")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        managed_files = [
            self.root / "data" / "concord" / "v1.0.0" / "bin" / "concord",
            self.root / "config" / "opencode" / "tools" / "concord.ts",
            self.root / "bin" / "concord",
            self.config,
        ]
        before = [(path.read_bytes() if not path.is_symlink() else os.readlink(path), path.stat().st_mtime_ns) for path in managed_files]
        second = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertIn("no changes", second.stdout)
        after = [(path.read_bytes() if not path.is_symlink() else os.readlink(path), path.stat().st_mtime_ns) for path in managed_files]
        self.assertEqual(after, before)

    def test_upgrade_replaces_prior_version_cleanly(self) -> None:
        self.make_release("v1.0.0", "old")
        self.make_release("v1.1.0", "new")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        second = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertFalse((self.root / "data" / "concord" / "v1.0.0").exists())
        self.assertEqual(
            (self.root / "data" / "concord" / "v1.1.0" / "bin" / "concord").read_text(), "new"
        )
        adapter = self.root / "config" / "opencode" / "tools" / "concord.ts"
        self.assertIn("new", adapter.read_text(encoding="utf-8"))
        config = self.config.read_text(encoding="utf-8")
        self.assertIn("v1.1.0/skills", config)
        self.assertNotIn("v1.0.0/skills", config)

    def test_upgrade_from_a_manifest_recording_fewer_adapter_files(self) -> None:
        """An installation predating an added adapter file upgrades cleanly.

        The manifest of an existing installation records the adapter files that
        shipped at the time. Adding one must not turn a broken adapter into a
        broken upgrade, so the new file is placed rather than refused as
        user-authored.
        """
        added = "credentials.ts"
        self.assertIn(added, installer.ADAPTER_FILES)
        # CD-0067 D4: the lane pipeline ships as four adapter files. Removing
        # any of them from the installed archive would break #253 reachability,
        # so the set the installer packs is asserted here by name.
        for shipped in ("dispatch.ts", "generated-agent-lanes.ts", "lane_dispatch.ts", "packet.ts"):
            self.assertIn(shipped, installer.ADAPTER_FILES, f"missing lane pipeline file {shipped}")
        # Test suites and their vectors are dev-only inputs, not adapter runtime
        # inputs. Shipping them would put test code and fixtures in the
        # installed archive, so the installer's adapter file list is asserted to
        # exclude them by name.
        for dev_only in ("concord.test.ts", "dispatch.test.ts", "worker-evidence-vector.json"):
            self.assertNotIn(dev_only, installer.ADAPTER_FILES, f"dev-only file {dev_only} must not ship in the adapter archive")
        self.make_release("v1.0.0", "old")
        self.make_release("v1.1.0", "new")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)

        manifest_path = self.root / "data" / "concord" / installer.MANIFEST_NAME
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        del manifest["adapter_files"][added]
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        (self.root / "config" / "opencode" / "tools" / added).unlink()

        second = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        for name in installer.ADAPTER_FILES:
            placed = self.root / "config" / "opencode" / "tools" / name
            self.assertTrue(placed.is_file(), f"{name} was not placed by the upgrade")
            self.assertIn("new", placed.read_text(encoding="utf-8"))

    def test_upgrade_from_a_pre_plugin_entry_manifest(self) -> None:
        """An installation before the plugin entry module upgrades cleanly."""
        added = "concord-plugin.ts"
        self.assertIn(added, installer.ADAPTER_FILES)
        self.make_release("v1.0.0", "old")
        self.make_release("v1.1.0", "new")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)

        manifest_path = self.root / "data" / "concord" / installer.MANIFEST_NAME
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        del manifest["adapter_files"][added]
        del manifest["version_files"][f"adapter/opencode/{added}"]
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        (self.root / "data" / "concord" / "v1.0.0" / "adapter" / "opencode" / added).unlink()
        (self.root / "config" / "opencode" / "tools" / added).unlink()

        second = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertTrue((self.root / "config" / "opencode" / "tools" / added).is_file())

    def test_upgrade_from_a_pre_agents_manifest_refuses_with_the_remedy(self) -> None:
        """An installation predating central agents refuses with instructions.

        The manifest key set is an equality invariant, so an older manifest
        without agent_files and stable_root cannot be upgraded in place. The
        refusal must name the remedy rather than leave the operator guessing,
        and it must not fire for manifests that are wrong in some other way.
        """
        self.make_release("v1.0.0")
        self.make_release("v1.1.0")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)

        manifest_path = self.root / "data" / "concord" / installer.MANIFEST_NAME
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        del manifest["agent_files"]
        del manifest["stable_root"]
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

        second = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(second.returncode, 0)
        self.assertIn("run uninstall, then install", second.stdout + second.stderr)

        # A manifest missing an unrelated field keeps the generic refusal.
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        del manifest["skill_path"]
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        third = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(third.returncode, 0)
        self.assertIn("unknown or missing fields", third.stdout + third.stderr)
        self.assertNotIn("run uninstall, then install", third.stdout + third.stderr)

    def test_uninstall_removes_managed_residue_and_keeps_user_config(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertFalse((self.root / "data" / "concord").exists())
        self.assertFalse((self.root / "config" / "opencode" / "tools" / "concord.ts").exists())
        self.assertFalse((self.root / "bin" / "concord").is_symlink())
        config = self.config.read_text(encoding="utf-8")
        self.assertIn('"keep": true', config)
        self.assertNotIn("/skills", config)

    def test_user_authored_adapter_is_never_overwritten(self) -> None:
        tools = self.root / "config" / "opencode" / "tools"
        tools.mkdir(parents=True)
        adapter = tools / "concord.ts"
        adapter.write_text("operator-authored\n", encoding="utf-8")
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("user-authored adapter file", result.stderr)
        self.assertEqual(adapter.read_text(encoding="utf-8"), "operator-authored\n")
        self.assertFalse((self.root / "data").exists())

    def plugin_entry_path(self) -> str:
        return str((self.root / "config" / "opencode" / "tools" / installer.PLUGIN_ENTRY_FILE).resolve())

    def test_install_registers_plugin_entry(self) -> None:
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(result.returncode, 0, result.stderr)
        entry = self.plugin_entry_path()
        self.assertTrue((self.root / "config" / "opencode" / "tools" / installer.PLUGIN_ENTRY_FILE).is_file())
        config = self.config.read_text(encoding="utf-8")
        self.assertIn(f'"{entry}"', config)
        # The entry must be a member of the plugin array, not only a comment.
        self.assertIn(entry, installer.jsonc_data(config)["plugin"])

    def test_install_appends_plugin_entry_to_existing_array(self) -> None:
        self.config.write_text(
            '{\n  "keep": true,\n  "plugin": [\n    [\n      "/operator/plugin",\n      {"agents": {}}\n    ],\n    "/operator/other"\n  ]\n}\n',
            encoding="utf-8",
        )
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(result.returncode, 0, result.stderr)
        plugin = installer.jsonc_data(self.config.read_text(encoding="utf-8"))["plugin"]
        self.assertIn(self.plugin_entry_path(), plugin)
        # Existing entries are preserved, including the tuple form.
        self.assertIn("/operator/other", plugin)
        self.assertTrue(any(isinstance(item, list) and item[0] == "/operator/plugin" for item in plugin))

    def test_install_plugin_entry_registration_is_idempotent(self) -> None:
        self.make_release("v1.0.0")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        before = self.config.read_text(encoding="utf-8")
        second = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertIn("no changes", second.stdout)
        self.assertEqual(self.config.read_text(encoding="utf-8"), before)

    def test_install_refuses_a_non_array_plugin_value(self) -> None:
        self.config.write_text('{\n  "keep": true,\n  "plugin": "not-an-array"\n}\n', encoding="utf-8")
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("plugin value is not an array", result.stderr)
        self.assertIn("manually", result.stderr)
        self.assertFalse((self.root / "data").exists())

    def test_uninstall_removes_plugin_entry(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        self.assertIn(self.plugin_entry_path(), self.config.read_text(encoding="utf-8"))
        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        config = self.config.read_text(encoding="utf-8")
        self.assertNotIn(self.plugin_entry_path(), config)
        self.assertIn('"keep": true', config)

    def test_existing_skills_config_and_launcher_are_not_clobbered(self) -> None:
        self.config.write_text(
            '{\n  "keep": true,\n  "skills": {"paths": ["/operator-authored/skill"]}\n}\n',
            encoding="utf-8",
        )
        launcher = self.commands / "concord"
        launcher.write_text("#!/bin/sh\nexit 23\n", encoding="utf-8")
        launcher.chmod(0o755)
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("user-authored launcher", result.stderr)
        self.assertEqual(launcher.read_text(encoding="utf-8"), "#!/bin/sh\nexit 23\n")
        self.assertEqual(
            self.config.read_text(encoding="utf-8"),
            '{\n  "keep": true,\n  "skills": {"paths": ["/operator-authored/skill"]}\n}\n',
        )
        self.assertFalse((self.root / "data").exists())

    def install_for_manifest_attack(self) -> Path:
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(result.returncode, 0, result.stderr)
        return self.root / "data" / "concord" / "install-manifest.json"

    def test_manifest_path_traversal_cannot_delete_outside_file(self) -> None:
        manifest_path = self.install_for_manifest_attack()
        outside = self.root / "outside-traversal.txt"
        outside.write_text("untouched", encoding="utf-8")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["version_files"]["../../outside-traversal.txt"] = "0" * 64
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        result = self.run_installer("uninstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(outside.read_text(encoding="utf-8"), "untouched")

    def test_manifest_absolute_path_cannot_delete_outside_file(self) -> None:
        manifest_path = self.install_for_manifest_attack()
        outside = self.root / "outside-absolute.txt"
        outside.write_text("untouched", encoding="utf-8")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["version_files"][str(outside)] = "0" * 64
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        result = self.run_installer("uninstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(outside.read_text(encoding="utf-8"), "untouched")

    def test_manifest_unknown_adapter_key_cannot_delete_outside_file(self) -> None:
        manifest_path = self.install_for_manifest_attack()
        outside = self.root / "outside-adapter.txt"
        outside.write_text("untouched", encoding="utf-8")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["adapter_files"]["../../outside-adapter.txt"] = "0" * 64
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        result = self.run_installer("uninstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(outside.read_text(encoding="utf-8"), "untouched")

    def test_manifest_symlinked_adapter_target_is_rejected(self) -> None:
        manifest_path = self.install_for_manifest_attack()
        outside = self.root / "outside-symlink.txt"
        outside.write_text("untouched", encoding="utf-8")
        adapter = self.root / "config" / "opencode" / "tools" / "concord.ts"
        adapter.unlink()
        adapter.symlink_to(outside)
        result = self.run_installer("uninstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(outside.read_text(encoding="utf-8"), "untouched")
        self.assertTrue(manifest_path.exists())

    def test_manifest_redirected_config_cannot_edit_outside_file(self) -> None:
        manifest_path = self.install_for_manifest_attack()
        outside = self.root / "outside-config.jsonc"
        outside.write_text('{"keep": true}\n', encoding="utf-8")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["config_path"] = str(outside)
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        result = self.run_installer("uninstall")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(outside.read_text(encoding="utf-8"), '{"keep": true}\n')

    def test_install_process_death_at_every_phase_recovers(self) -> None:
        phases = ("staged", "version_activated", "agents_swapped", "adapter_swapped", "launcher_swapped", "config_swapped", "manifest_committed", "cleanup")
        for index, phase in enumerate(phases, 1):
            with self.subTest(phase=phase):
                version = f"v2.0.{index}"
                self.make_release(version)
                stopped = self.run_after_phase("staged", "install", "--version", version, "--artifact-dir", str(self.artifacts)) if phase == "staged" else self.run_after_phase(phase, "install", "--version", version, "--artifact-dir", str(self.artifacts))
                self.assertEqual(stopped.returncode, 97, stopped.stderr)
                recovered = self.run_installer("status")
                self.assertEqual(recovered.returncode, 0, recovered.stderr)
                installed = self.run_installer("install", "--version", version, "--artifact-dir", str(self.artifacts))
                self.assertEqual(installed.returncode, 0, installed.stderr)
                self.assertIn(f'"version": "{version}"', self.run_installer("status").stdout)
                removed = self.run_installer("uninstall")
                self.assertEqual(removed.returncode, 0, removed.stderr)
                self.reset_config()
                self.assertFalse((self.root / "data" / "concord").exists())

    def test_uninstall_process_death_at_every_phase_recovers(self) -> None:
        phases = ("staged", "version_activated", "agents_swapped", "adapter_swapped", "launcher_swapped", "config_swapped", "manifest_committed", "cleanup")
        for index, phase in enumerate(phases, 1):
            with self.subTest(phase=phase):
                version = f"v3.0.{index}"
                self.make_release(version)
                installed = self.run_installer("install", "--version", version, "--artifact-dir", str(self.artifacts))
                self.assertEqual(installed.returncode, 0, installed.stderr)
                stopped = self.run_after_phase(phase, "uninstall")
                self.assertEqual(stopped.returncode, 97, stopped.stderr)
                recovered = self.run_installer("status")
                self.assertEqual(recovered.returncode, 0, recovered.stderr)
                removed = self.run_installer("uninstall")
                self.assertEqual(removed.returncode, 0, removed.stderr)
                self.reset_config()
                self.assertFalse((self.root / "data" / "concord").exists())

    def test_upgrade_process_death_at_every_phase_recovers_old_or_new_coherently(self) -> None:
        old_version = "v4.9.0"
        self.make_release(old_version, "old")
        phases = ("staged", "version_activated", "agents_swapped", "adapter_swapped", "launcher_swapped", "config_swapped", "manifest_committed", "cleanup")
        for index, phase in enumerate(phases, 1):
            with self.subTest(phase=phase):
                new_version = f"v4.0.{index}"
                self.make_release(new_version, "new")
                installed = self.run_installer("install", "--version", old_version, "--artifact-dir", str(self.artifacts))
                self.assertEqual(installed.returncode, 0, installed.stderr)
                stopped = self.run_after_phase(phase, "install", "--version", new_version, "--artifact-dir", str(self.artifacts))
                self.assertEqual(stopped.returncode, 97, stopped.stderr)
                recovered = self.run_installer("status")
                self.assertEqual(recovered.returncode, 0, recovered.stderr)
                expected = new_version if phase in {"manifest_committed", "cleanup"} else old_version
                self.assertIn(f'"version": "{expected}"', recovered.stdout)
                upgraded = self.run_installer("install", "--version", new_version, "--artifact-dir", str(self.artifacts))
                self.assertEqual(upgraded.returncode, 0, upgraded.stderr)
                removed = self.run_installer("uninstall")
                self.assertEqual(removed.returncode, 0, removed.stderr)
                self.reset_config()

    def test_recovery_refuses_post_transaction_user_change(self) -> None:
        version = "v5.0.0"
        self.make_release(version)
        stopped = self.run_after_phase("adapter_swapped", "install", "--version", version, "--artifact-dir", str(self.artifacts))
        self.assertEqual(stopped.returncode, 97, stopped.stderr)
        adapter = self.root / "config" / "opencode" / "tools" / "concord.ts"
        adapter.write_text("operator changed this after the crash\n", encoding="utf-8")
        recovered = self.run_installer("status")
        self.assertNotEqual(recovered.returncode, 0)
        self.assertIn("transaction conflict at adapter concord.ts", recovered.stderr)
        self.assertEqual(adapter.read_text(encoding="utf-8"), "operator changed this after the crash\n")

    def test_staging_and_backup_fsync_destination_parents(self) -> None:
        source = self.root / "copy-source"
        source.mkdir()
        (source / "payload").write_text("payload", encoding="utf-8")
        stage = self.root / "transaction" / "stage" / "version"
        backup = self.root / "transaction" / "backup" / "version"
        with mock.patch.object(installer, "fsync_tree") as tree_fsync, mock.patch.object(installer, "fsync_directory") as directory_fsync:
            installer.durable_copy_tree(source, stage)
            installer.durable_copy_tree(source, backup)
        self.assertEqual(tree_fsync.call_args_list, [mock.call(stage), mock.call(backup)])
        self.assertEqual(directory_fsync.call_args_list, [mock.call(stage.parent), mock.call(backup.parent)])

    def test_cross_directory_replace_fsyncs_both_parents_in_order(self) -> None:
        source = self.root / "source" / "entry"
        destination = self.root / "destination" / "entry"
        source.parent.mkdir()
        destination.parent.mkdir()
        source.write_text("entry", encoding="utf-8")
        events: list[tuple[str, Path]] = []

        def record_fsync(path: Path) -> None:
            events.append(("fsync", path))

        with mock.patch.object(installer, "fsync_directory", side_effect=record_fsync), mock.patch.object(
            installer.os, "replace", side_effect=lambda old, new: events.append(("replace", old.parent))
        ):
            installer.replace_durable(source, destination)
        self.assertEqual(
            events,
            [("replace", source.parent), ("fsync", source.parent), ("fsync", destination.parent)],
        )

    def test_write_and_cleanup_fsync_their_directory_entries(self) -> None:
        target = self.root / "journal-parent" / "journal.json"
        target.parent.mkdir()
        with mock.patch.object(installer, "fsync_directory") as directory_fsync:
            installer.write_atomic(target, b"journal")
        self.assertEqual(directory_fsync.call_args_list, [mock.call(target.parent)])

        transaction_root = self.root / "cleanup" / "tx"
        transaction_root.mkdir(parents=True)
        paths = installer.paths_for(self.root / "operator")
        events: list[tuple[str, Path]] = []
        with mock.patch.object(installer.shutil, "rmtree", side_effect=lambda path: events.append(("rmtree", path))), mock.patch.object(
            installer, "fsync_directory", side_effect=lambda path: events.append(("fsync", path))
        ):
            installer.cleanup_transaction(transaction_root, {"cleanup_version": None}, paths)
        self.assertEqual(events[:2], [("rmtree", transaction_root), ("fsync", transaction_root.parent)])


    # --- #648: per-project session shards are not durable data homes ----

    def test_paths_for_refuses_opencode_projects_shard(self) -> None:
        shard = self.root / ".local" / "share" / "opencode-projects" / "abc123"
        with mock.patch.dict(installer.os.environ, {"XDG_DATA_HOME": str(shard)}, clear=False), mock.patch.object(
            installer.Path, "home", return_value=self.root
        ):
            with self.assertRaises(installer.InstallerError) as raised:
                installer.paths_for(None)
        message = str(raised.exception)
        self.assertIn("per-project session shard", message)
        self.assertIn(str(shard / "concord"), message)
        self.assertIn("env -u XDG_DATA_HOME", message)

    def test_paths_for_honors_a_durable_xdg_override(self) -> None:
        durable = self.root / "data"
        with mock.patch.dict(installer.os.environ, {"XDG_DATA_HOME": str(durable)}, clear=False):
            paths = installer.paths_for(None)
        self.assertEqual(paths.data_root, durable / "concord")

    def test_unmanaged_note_names_the_manifest(self) -> None:
        paths = installer.paths_for(self.root / "operator-note")
        self.assertIn(str(paths.data_root / installer.MANIFEST_NAME), installer.unmanaged_manifest_note(None, paths))
        self.assertEqual(installer.unmanaged_manifest_note({"version": "v"}, paths), "")

    # --- CD-0063: shipped operator conduct rules -----------------------

    def test_install_places_instructions_and_agents_in_version_tree_and_manifest(self) -> None:
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(result.returncode, 0, result.stderr)
        version_root = self.root / "data" / "concord" / "v1.0.0"
        instructions_dir = version_root / "instructions"
        agents_dir = version_root / "agents"
        self.assertTrue(instructions_dir.is_dir(), "version tree is missing instructions/")
        for name in installer.INSTRUCTION_FILES:
            self.assertTrue((instructions_dir / name).is_file(), f"missing instruction file {name} in version tree")
        self.assertTrue(agents_dir.is_dir(), "version tree is missing agents/")
        for name in installer.AGENT_FILES:
            self.assertTrue((agents_dir / name).is_file(), f"missing agent file {name} in version tree")
        manifest = json.loads((self.root / "data" / "concord" / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertEqual(set(manifest["agent_files"]), set(installer.AGENT_FILES))
        for name, digest in manifest["agent_files"].items():
            self.assertTrue(installer.SHA256_RE.fullmatch(digest))
        for name in installer.AGENT_FILES:
            placed = self.root / "config" / "opencode" / "agents" / name
            self.assertTrue(placed.is_file(), f"agent file {name} was not placed centrally")
            self.assertEqual(installer.sha256(placed), manifest["agent_files"][name])
        stable = self.root / "data" / "concord" / "current"
        self.assertTrue(stable.is_symlink(), "stable root is not a symlink after install")
        self.assertEqual(os.readlink(stable), str(self.root / "data" / "concord" / "v1.0.0"))
        self.assertEqual(manifest["stable_root"], str(stable))

    def test_upgrade_switches_stable_root_without_rewriting_project(self) -> None:
        self.make_release("v1.0.0", "old")
        self.make_release("v1.1.0", "new")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        project_dir = self.root / "consumer"
        project_dir.mkdir()
        linked = self.run_installer("link", "--project", str(project_dir))
        self.assertEqual(linked.returncode, 0, linked.stderr)
        project_file = project_dir / ".opencode" / "opencode.json"
        self.assertTrue(project_file.is_file())
        before_bytes = project_file.read_bytes()
        before_mtime = project_file.stat().st_mtime_ns
        # An upgrade must not rewrite the project file: the stable root
        # indirection is what guarantees that, so verify the symlink target
        # moves while the project file is untouched.
        second = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        stable = self.root / "data" / "concord" / "current"
        self.assertTrue(stable.is_symlink())
        self.assertEqual(os.readlink(stable), str(self.root / "data" / "concord" / "v1.1.0"))
        self.assertEqual(project_file.read_bytes(), before_bytes)
        self.assertEqual(project_file.stat().st_mtime_ns, before_mtime)
        # The project pointer still resolves to the (now-new) corpus via the
        # symlink, so its resolved entries point at v1.1.0.
        instructions_root = (self.root / "data" / "concord" / "current" / "instructions").resolve()
        self.assertEqual(instructions_root, (self.root / "data" / "concord" / "v1.1.0" / "instructions").resolve())

    def test_link_is_idempotent(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer"
        project_dir.mkdir()
        first = self.run_installer("link", "--project", str(project_dir))
        self.assertEqual(first.returncode, 0, first.stderr)
        project_file = project_dir / ".opencode" / "opencode.json"
        first_payload = project_file.read_text(encoding="utf-8")
        second = self.run_installer("link", "--project", str(project_dir))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(project_file.read_text(encoding="utf-8"), first_payload)
        self.assertIn("no changes", second.stdout)

    def test_link_preserves_unrelated_keys_and_entries(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer"
        project_file = project_dir / ".opencode" / "opencode.json"
        project_file.parent.mkdir(parents=True)
        unrelated_entry = "/elsewhere/conductor/notes.md"
        project_file.write_text(
            json.dumps(
                {
                    "$schema": "https://opencode.ai/config.json",
                    "theme": "dark",
                    "instructions": [unrelated_entry],
                },
                indent=2,
            )
            + "\n",
            encoding="utf-8",
        )
        result = self.run_installer("link", "--project", str(project_dir))
        self.assertEqual(result.returncode, 0, result.stderr)
        config = json.loads(project_file.read_text(encoding="utf-8"))
        self.assertEqual(config["theme"], "dark")
        self.assertEqual(config["$schema"], "https://opencode.ai/config.json")
        self.assertIn(unrelated_entry, config["instructions"])
        expected_entry = str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")
        self.assertIn(expected_entry, config["instructions"])
        self.assertEqual(len(config["instructions"]), 2)

    def test_link_refuses_non_array_instructions(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer"
        project_file = project_dir / ".opencode" / "opencode.json"
        project_file.parent.mkdir(parents=True)
        project_file.write_text(json.dumps({"instructions": 42}, indent=2) + "\n", encoding="utf-8")
        result = self.run_installer("link", "--project", str(project_dir))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("non-array", result.stderr)
        # Original file untouched.
        self.assertEqual(json.loads(project_file.read_text(encoding="utf-8")), {"instructions": 42})

    def test_unlink_removes_only_managed_entry(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer"
        linked = self.run_installer("link", "--project", str(project_dir))
        self.assertEqual(linked.returncode, 0, linked.stderr)
        project_file = project_dir / ".opencode" / "opencode.json"
        config = json.loads(project_file.read_text(encoding="utf-8"))
        unrelated_entry = "/elsewhere/notes.md"
        config["instructions"].append(unrelated_entry)
        project_file.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
        result = self.run_installer("unlink", "--project", str(project_dir))
        self.assertEqual(result.returncode, 0, result.stderr)
        config = json.loads(project_file.read_text(encoding="utf-8"))
        self.assertNotIn(str(self.root / "data" / "concord" / "current" / "instructions" / "*.md"), config.get("instructions", []))
        self.assertIn(unrelated_entry, config["instructions"])
        # A second unlink without the managed entry present is silent.
        silent = self.run_installer("unlink", "--project", str(project_dir))
        self.assertEqual(silent.returncode, 0, silent.stderr)
        self.assertIn("no conduct corpus entry", silent.stdout)
        # A unlink that drops the last remaining managed entry removes the
        # file and the .opencode directory because nothing is left.
        project_file.write_text(json.dumps({"instructions": [str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")]}, indent=2) + "\n", encoding="utf-8")
        self.run_installer("unlink", "--project", str(project_dir))
        self.assertFalse(project_file.exists())
        self.assertFalse((project_dir / ".opencode").exists())

    def test_project_never_linked_has_no_config(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer"
        project_dir.mkdir()
        self.assertFalse((project_dir / ".opencode" / "opencode.json").exists())
        # An unlink without prior link is silent and idempotent.
        result = self.run_installer("unlink", "--project", str(project_dir))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((project_dir / ".opencode" / "opencode.json").exists())

    def test_uninstall_preserves_user_owned_concord_agents(self) -> None:
        self.make_release("v1.0.0")
        agents_dir = self.root / "config" / "opencode" / "agents"
        agents_dir.mkdir(parents=True)
        user_agents = {
            "concord-0.md": b"operator intake\n",
            "concord-orchestrator.md": b"operator shaping\n",
            "concord-2.md": b"operator driving\n",
        }
        for name, content in user_agents.items():
            (agents_dir / name).write_bytes(content)

        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)

        for name, content in user_agents.items():
            self.assertEqual((agents_dir / name).read_bytes(), content)
        for name in installer.AGENT_FILES:
            self.assertFalse((agents_dir / name).exists())

    def test_upgrade_refuses_new_managed_name_over_user_agent(self) -> None:
        introduced = installer.AGENT_FILES[-1]
        self.make_release("v1.0.0", agent_names=installer.AGENT_FILES[:-1])
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        target = self.root / "config" / "opencode" / "agents" / introduced
        target.write_bytes(b"operator-owned\n")
        self.make_release("v2.0.0")
        upgraded = self.run_installer("install", "--version", "v2.0.0", "--artifact-dir", str(self.artifacts))

        self.assertNotEqual(upgraded.returncode, 0)
        self.assertIn("user-authored agent file", upgraded.stderr)
        self.assertEqual(target.read_bytes(), b"operator-owned\n")
        manifest = json.loads((self.root / "data" / "concord" / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertEqual(manifest["version"], "v1.0.0")

    def test_upgrade_refuses_new_managed_name_over_user_symlink(self) -> None:
        introduced = installer.AGENT_FILES[-1]
        self.make_release("v1.0.0", agent_names=installer.AGENT_FILES[:-1])
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        source = self.root / "operator-agent.md"
        source.write_bytes(b"operator-owned\n")
        target = self.root / "config" / "opencode" / "agents" / introduced
        target.symlink_to(source)
        self.make_release("v2.0.0")
        upgraded = self.run_installer("install", "--version", "v2.0.0", "--artifact-dir", str(self.artifacts))

        self.assertNotEqual(upgraded.returncode, 0)
        self.assertIn("user-authored agent file", upgraded.stderr)
        self.assertTrue(target.is_symlink())
        self.assertEqual(source.read_bytes(), b"operator-owned\n")

    def test_upgrade_restores_missing_managed_agent(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        target = self.root / "config" / "opencode" / "agents" / installer.AGENT_FILES[0]
        target.unlink()
        self.make_release("v2.0.0")
        upgraded = self.run_installer("install", "--version", "v2.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(upgraded.returncode, 0, upgraded.stderr)
        self.assertEqual(target.read_text(encoding="utf-8"), f"agent:{installer.AGENT_FILES[0]}:v2.0.0\n")

    def test_upgrade_removes_agent_retired_by_new_manifest(self) -> None:
        retired = installer.AGENT_FILES[-1]
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        target = self.root / "config" / "opencode" / "agents" / retired
        self.make_release("v2.0.0", agent_names=installer.AGENT_FILES[:-1])
        upgraded = self.run_installer("install", "--version", "v2.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(upgraded.returncode, 0, upgraded.stderr)
        self.assertFalse(target.exists())

        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertFalse(target.exists())

    def test_upgrade_rollback_restores_agent_retired_by_new_manifest(self) -> None:
        retired = installer.AGENT_FILES[-1]
        self.make_release("v1.0.0", marker="old")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        target = self.root / "config" / "opencode" / "agents" / retired
        old_content = target.read_bytes()
        self.make_release("v2.0.0", marker="new", agent_names=installer.AGENT_FILES[:-1])
        stopped = self.run_after_phase(
            "agents_swapped", "install", "--version", "v2.0.0", "--artifact-dir", str(self.artifacts)
        )
        self.assertEqual(stopped.returncode, 97, stopped.stderr)
        self.assertFalse(target.exists())

        recovered = self.run_installer("status")
        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        self.assertEqual(target.read_bytes(), old_content)
        self.assertIn('"version": "v1.0.0"', recovered.stdout)

    def test_uninstall_removes_central_agents_and_current_symlink(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        for name in installer.AGENT_FILES:
            self.assertTrue((self.root / "config" / "opencode" / "agents" / name).is_file())
        self.assertTrue((self.root / "data" / "concord" / "current").is_symlink())
        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        for name in installer.AGENT_FILES:
            self.assertFalse((self.root / "config" / "opencode" / "agents" / name).exists())
        self.assertFalse((self.root / "data" / "concord" / "current").exists())

    def test_install_refuses_modified_central_agent_file(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        target = self.root / "config" / "opencode" / "agents" / installer.AGENT_FILES[0]
        target.write_text("operator-tampered\n", encoding="utf-8")
        again = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(again.returncode, 0)
        # Preflight refuses with the user-authored wording before any
        # transaction is opened, and the idempotent re-install guard refuses
        # with the modified-managed wording; both surfaces meet the spec.
        self.assertTrue(
            "modified managed agent file" in again.stderr or "user-authored agent file" in again.stderr,
            f"unexpected refusal message: {again.stderr!r}",
        )
        # Tampered file is preserved; the installer did not silently overwrite.
        self.assertEqual(target.read_text(encoding="utf-8"), "operator-tampered\n")


if __name__ == "__main__":
    unittest.main()
