#!/usr/bin/env python3
"""Integration tests for the Concord installer using temporary roots."""
from __future__ import annotations

import contextlib
import fcntl
import hashlib
import io
import json
import os
import select
import shlex
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
from types import SimpleNamespace
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
        # The staged core is a shell script so the installer can ask it which
        # releases live sessions hold (CD-0111 D2), whether activation is
        # ready (CON-807), and which capability it carries (CON-807 descriptor
        # probe). A test controls the answers through CONCORD_TEST_HOST_LEASES
        # (a file holding the JSON report) or CONCORD_TEST_HOST_LEASES_FAIL (a
        # failed observation), through CONCORD_TEST_UPGRADE_PLAN or
        # CONCORD_TEST_UPGRADE_PLAN_FAIL (the readiness plan), and through the
        # descriptor knobs: CONCORD_TEST_CORE_IDENTITY_FAIL (an unknown core
        # that answers nothing), CONCORD_TEST_CORE_IDENTITY (a file the core
        # prints instead of its version), CONCORD_TEST_CORE_DESCRIPTOR_FAIL (a
        # known legacy core without the descriptor route), and
        # CONCORD_TEST_CORE_DESCRIPTOR (a file the core prints instead of its
        # descriptor). The default descriptor is truthful identity, not a
        # silent default: the core names the tree it actually sits in and the
        # digest the staged adapter actually pins (CON-807).
        synthetic_digest = self.synthetic_pair_digest(version)
        binary.write_text(
            "#!/bin/sh\n"
            f"# marker:{marker or version}\n"
            'case "$1" in\n'
            "  --version)\n"
            '    case "$2" in\n'
            "      --json)\n"
            '        if [ -n "$CONCORD_TEST_CORE_DESCRIPTOR_FAIL" ]; then exit 2; fi\n'
            '        if [ -n "$CONCORD_TEST_CORE_DESCRIPTOR" ] && [ -f "$CONCORD_TEST_CORE_DESCRIPTOR" ]; then cat "$CONCORD_TEST_CORE_DESCRIPTOR"; else pair_root="$(cd "$(dirname "$0")/.." && pwd)"; pair_bin="$0"; printf \'{"version":"'
            + version
            + f'","schema_version":111,"fence_protocol":1,"manifest_digest":"{synthetic_digest}","release_root":"%s","core_binary":"%s"}}\\n\' "$pair_root" "$pair_bin"; fi ;;\n'
            "      *)\n"
            '        if [ -n "$CONCORD_TEST_CORE_IDENTITY_FAIL" ]; then exit 2; fi\n'
            '        if [ -n "$CONCORD_TEST_CORE_IDENTITY" ] && [ -f "$CONCORD_TEST_CORE_IDENTITY" ]; then cat "$CONCORD_TEST_CORE_IDENTITY"; else printf \''
            + version
            + '\\n\'; fi ;;\n'
            "    esac ;;\n"
            "  host-leases)\n"
            '    if [ -n "$CONCORD_TEST_HOST_LEASES_FAIL" ]; then echo "leases unavailable" >&2; exit 1; fi\n'
            '    if [ -n "$CONCORD_TEST_HOST_LEASES" ] && [ -f "$CONCORD_TEST_HOST_LEASES" ]; then cat "$CONCORD_TEST_HOST_LEASES"; else printf \'{"leases":[]}\\n\'; fi ;;\n'
            "  upgrade)\n"
            '    if [ -n "$CONCORD_TEST_UPGRADE_PLAN_FAIL" ]; then echo "concord upgrade: readiness_unknown: the store cannot be read" >&2; exit 1; fi\n'
            '    if [ -n "$CONCORD_TEST_UPGRADE_PLAN" ] && [ -f "$CONCORD_TEST_UPGRADE_PLAN" ]; then cat "$CONCORD_TEST_UPGRADE_PLAN"; else printf \'{"store_present":false,"fresh_store":true,"applied_versions":[],"schema_version":0,"pending":[],"pending_breaking":[],"activation_blocked":false,"compatibility_floor":0,"migration_command":"concord upgrade","activation_command":"","maintenance_fence":null}\\n\'; fi ;;\n'
            "  *) exit 2 ;;\n"
            "esac\n",
            encoding="utf-8",
        )
        binary.chmod(0o755)
        for name in installer.ADAPTER_FILES:
            (source / "adapter" / "opencode" / name).write_text(f"{name}:{marker or version}\n", encoding="utf-8")
        # The staged adapter's contracts file carries the pinned-pair digest
        # the truthful core descriptor reports (CON-807): written after the
        # bulk loop so the digest constant is the file's content, with the
        # release marker kept so upgrade assertions still see provenance.
        (source / "adapter" / "opencode" / "generated-contracts.ts").write_text(
            f"// synthetic pinned-pair constant (CON-807) marker:{marker or version}\n"
            f'export const manifestDigest = "{synthetic_digest}" as const\n',
            encoding="utf-8",
        )
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

    def run_real_git(self, directory: Path, *arguments: str) -> subprocess.CompletedProcess[str]:
        git = shutil.which("git", path=os.defpath)
        self.assertIsNotNone(git)
        environment = self.env.copy()
        environment["PATH"] = os.defpath
        return subprocess.run([git, *arguments], cwd=directory, text=True, capture_output=True, env=environment)

    def reset_config(self) -> None:
        self.config.write_text(
            '{\n  "$schema": "https://opencode.ai/config.json",\n  "keep": true\n}\n',
            encoding="utf-8",
        )

    def configure_managed_credential_fixture(self) -> tuple[Path, Path, Path, Path]:
        state = self.root / "credential-state"
        command_log = self.root / "credential-commands.log"
        keyrings = self.root / "data" / "keyrings"
        self.env["TEST_CREDENTIAL_STATE"] = str(state)
        self.env["TEST_CREDENTIAL_LOG"] = str(command_log)
        self.env["TEST_KEYRINGS"] = str(keyrings)
        legacy_unit = self.root / "config" / "systemd" / "user" / installer.CREDENTIAL_UNIT_NAME
        legacy_unit.parent.mkdir(parents=True)
        legacy_unit.write_text(
            installer.legacy_credential_unit_text(str(self.commands / "gnome-keyring-daemon")), encoding="utf-8"
        )
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
  *" Items") printf 'ao 1 "/org/freedesktop/secrets/collection/session/1"\n' ;;
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
  *"restart gnome-keyring-daemon.service"*) printf 'ready\n' >"$TEST_CREDENTIAL_STATE" ;;
esac''',
        )
        dropin = (
            self.root
            / "config"
            / "systemd"
            / "user"
            / "gnome-keyring-daemon.service.d"
            / installer.CREDENTIAL_DROPIN_NAME
        )
        return state, command_log, keyrings, dropin

    def test_fresh_home_without_a_launcher_bin_dir_installs(self) -> None:
        # A PATH entry can name a directory that does not exist yet: a fresh
        # HOME has exactly that shape. The install must create the bin dir
        # itself instead of dying at the launcher symlink (#936).
        self.make_release("v1.0.0")
        stubs = self.root / "stubs"
        stubs.mkdir()
        for name in os.listdir(self.commands):
            (stubs / name).write_bytes((self.commands / name).read_bytes())
            os.chmod(stubs / name, 0o755)
        shutil.rmtree(self.commands)
        environment = self.env.copy()
        environment["PATH"] = str(self.commands) + os.pathsep + str(stubs) + os.pathsep + os.defpath
        result = self.run_installer(
            "install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts), env=environment
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        launcher = self.root / "bin" / "concord"
        self.assertTrue(launcher.is_symlink(), "launcher symlink was not placed into the created bin dir")
        self.assertIn("concord", os.listdir(self.root / "bin"))

    def test_checksum_mismatch_refuses_without_installing(self) -> None:
        self.make_release("v1.0.0")
        archive = self.artifacts / "concord-v1.0.0.tar.gz"
        archive.write_bytes(archive.read_bytes() + b"tampered")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum mismatch", result.stderr)
        self.assert_retained_empty_lock_root()
        self.assertNotIn("skills", self.config.read_text(encoding="utf-8"))

    def test_missing_binary_checksum_refuses_without_installing(self) -> None:
        self.make_release("v1.0.0", include_binary_checksum=False)
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not contain the binary entry", result.stderr)
        self.assert_retained_empty_lock_root()

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
        self.assert_retained_empty_lock_root()

    def test_headless_install_creates_noninteractive_persistent_credential_collection(self) -> None:
        self.make_release("v1.0.0")
        state, command_log, keyrings, unit = self.configure_managed_credential_fixture()

        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertTrue(unit.is_file())
        self.assertEqual(unit.stat().st_mode & 0o777, 0o644)
        self.assertEqual(keyrings.stat().st_mode & 0o777, 0o700)
        self.assertEqual((keyrings / "login.keyring").stat().st_mode & 0o777, 0o600)
        self.assertEqual((keyrings / "user.keystore").stat().st_mode & 0o777, 0o600)
        combined = first.stdout + first.stderr + unit.read_text(encoding="utf-8")
        manifest = json.loads(
            (self.root / "data" / "concord" / installer.MANIFEST_NAME).read_text(encoding="utf-8")
        )
        self.assertEqual(manifest["credential_directory"], str(keyrings))
        unit_text = unit.read_text(encoding="utf-8")
        self.assertIn("[Service]", unit_text)
        self.assertIn("ExecStart=\n", unit_text)
        self.assertIn("--unlock --foreground --components=pkcs11,secrets", unit_text)
        self.assertIn("StandardInputData=Cg==", unit_text)
        self.assertNotIn("concord-keyring-unlock.service", unit_text)
        service = self.root / "data" / "dbus-1" / "services" / installer.CREDENTIAL_SERVICE_NAME
        self.assertTrue(service.is_file())
        service_text = service.read_text(encoding="utf-8")
        self.assertIn("Name=org.freedesktop.secrets", service_text)
        self.assertIn("SystemdService=gnome-keyring-daemon.service", service_text)
        self.assertIn("--unlock --foreground --components=pkcs11,secrets", service_text)
        self.assertNotIn("base64:", combined)
        self.assertNotIn("private_key", combined)
        self.assertEqual(state.read_text(encoding="utf-8").strip(), "ready")
        commands = command_log.read_text(encoding="utf-8") if command_log.exists() else ""
        self.assertIn("dbus-run-session", commands)
        self.assertIn(f"disable --now {installer.CREDENTIAL_UNIT_NAME}", commands)
        self.assertIn("restart gnome-keyring-daemon.service", commands)

        second = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertIn("already installed", second.stdout)

        unit.unlink()
        state.write_text("created\n", encoding="utf-8")
        repaired = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(repaired.returncode, 0, repaired.stderr)
        self.assertTrue(unit.is_file())
        self.assertEqual(state.read_text(encoding="utf-8").strip(), "ready")

        restart_marker = self.root / "credential-restart-marker"
        self.env["TEST_RESTART_MARKER"] = str(restart_marker)
        self.write_command(
            "systemctl",
            r'''printf '%s\n' "$*" >>"$TEST_CREDENTIAL_LOG"
case "$*" in
  *"restart gnome-keyring-daemon.service"*)
    if [ -f "$TEST_RESTART_MARKER" ]; then printf 'ready\n' >"$TEST_CREDENTIAL_STATE"; else touch "$TEST_RESTART_MARKER"; fi ;;
esac''',
        )
        state.write_text("created\n", encoding="utf-8")
        unit.unlink()
        before_refusal_commands = command_log.read_text(encoding="utf-8").splitlines()
        refused = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(refused.returncode, 0)
        self.assertIn("remained locked", refused.stderr)
        self.assertIn("Check the user service", refused.stderr)
        self.assertFalse(unit.exists())
        commands = command_log.read_text(encoding="utf-8").splitlines()
        refusal_commands = commands[len(before_refusal_commands) :]
        self.assertEqual(refusal_commands.count("--user daemon-reload"), 2)
        self.assertEqual(refusal_commands.count("--user restart gnome-keyring-daemon.service"), 2)

    def test_setup_failure_rolls_back_a_new_dropin(self) -> None:
        self.make_release("v1.0.0")
        state, command_log, keyrings, dropin = self.configure_managed_credential_fixture()
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        dropin.unlink()
        state.write_text("created\n", encoding="utf-8")
        self.write_command("systemctl", 'case "$*" in *"daemon-reload"*) exit 1 ;; esac')

        refused = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertNotEqual(refused.returncode, 0)
        self.assertFalse(dropin.exists())

    def test_same_version_install_records_credential_ownership_during_migration(self) -> None:
        self.make_release("v1.0.0")
        _state, _command_log, keyrings, _dropin = self.configure_managed_credential_fixture()
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        manifest_path = self.root / "data" / "concord" / installer.MANIFEST_NAME
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        del manifest["credential_directory"]
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        legacy_unit = self.root / "config" / "systemd" / "user" / installer.CREDENTIAL_UNIT_NAME
        legacy_unit.write_text(
            installer.legacy_credential_unit_text(str(self.commands / "gnome-keyring-daemon")), encoding="utf-8"
        )

        migrated = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(migrated.returncode, 0, migrated.stderr)
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        self.assertEqual(manifest["credential_directory"], str(keyrings))

    def test_repair_keeps_a_preexisting_credential_dropin_when_unlock_fails(self) -> None:
        self.make_release("v1.0.0")
        state, command_log, keyrings, dropin = self.configure_managed_credential_fixture()

        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        original_dropin = dropin.read_bytes()

        restart_marker = self.root / "credential-restart-marker"
        self.env["TEST_RESTART_MARKER"] = str(restart_marker)
        self.write_command(
            "systemctl",
            r'''printf '%s\n' "$*" >>"$TEST_CREDENTIAL_LOG"
case "$*" in
  *"restart gnome-keyring-daemon.service"*)
    touch "$TEST_RESTART_MARKER" ;;
esac''',
        )
        state.write_text("created\n", encoding="utf-8")
        before_refusal_commands = command_log.read_text(encoding="utf-8").splitlines()

        refused = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertNotEqual(refused.returncode, 0)
        self.assertIn("remained locked", refused.stderr)
        self.assertIn("Check the user service", refused.stderr)
        self.assertEqual(dropin.read_bytes(), original_dropin)
        self.assertEqual(keyrings.joinpath("login.keyring").read_text(encoding="utf-8"), "login")
        commands = command_log.read_text(encoding="utf-8").splitlines()
        refusal_commands = commands[len(before_refusal_commands) :]
        self.assertEqual(refusal_commands.count("--user daemon-reload"), 1)
        self.assertEqual(refusal_commands.count("--user restart gnome-keyring-daemon.service"), 1)

    def test_install_keeps_an_existing_compatible_secret_service(self) -> None:
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(result.returncode, 0, result.stderr)
        unit = (
            self.root
            / "config"
            / "systemd"
            / "user"
            / "gnome-keyring-daemon.service.d"
            / installer.CREDENTIAL_DROPIN_NAME
        )
        self.assertFalse(unit.exists())

    def test_install_keeps_an_existing_unlocked_gnome_keyring(self) -> None:
        self.make_release("v1.0.0")
        keyrings = self.root / "data" / "keyrings"
        keyrings.mkdir(parents=True)
        login_keyring = keyrings / "login.keyring"
        login_keyring.write_text("encrypted", encoding="utf-8")

        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(login_keyring.read_text(encoding="utf-8"), "encrypted")
        self.assertFalse(
            (
                self.root
                / "config"
                / "systemd"
                / "user"
                / "gnome-keyring-daemon.service.d"
                / installer.CREDENTIAL_DROPIN_NAME
            ).exists()
        )

    def test_install_refuses_a_user_authored_legacy_credential_unit(self) -> None:
        self.make_release("v1.0.0")
        unit = self.root / "config" / "systemd" / "user" / installer.CREDENTIAL_UNIT_NAME
        unit.parent.mkdir(parents=True)
        unit.write_text("[Service]\nExecStart=/usr/local/bin/custom-keyring\n", encoding="utf-8")

        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("user-authored legacy credential unit", result.stderr)
        self.assertEqual(unit.read_text(encoding="utf-8"), "[Service]\nExecStart=/usr/local/bin/custom-keyring\n")

    def test_install_refuses_an_unowned_encrypted_login_collection(self) -> None:
        self.make_release("v1.0.0")
        keyrings = self.root / "data" / "keyrings"
        keyrings.mkdir(parents=True)
        login_keyring = keyrings / "login.keyring"
        login_keyring.write_text("encrypted", encoding="utf-8")
        self.write_command(
            "busctl",
            r'''case "$*" in
  "--user --list") printf 'org.freedesktop.secrets\n' ;;
  *"ReadAlias"*) printf 'o "/org/freedesktop/secrets/collection/login"\n' ;;
  *" Locked") printf 'b true\n' ;;
  *) exit 2 ;;
esac''',
        )
        command_log = self.root / "credential-commands.log"
        self.env["TEST_CREDENTIAL_LOG"] = str(command_log)
        self.write_command("systemctl", "printf '%s\\n' \"$*\" >>\"$TEST_CREDENTIAL_LOG\"; exit 0")

        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not owned by Concord", result.stderr)
        self.assertIn("Unlock it with the desktop login", result.stderr)
        self.assertFalse(
            (
                self.root
                / "config"
                / "systemd"
                / "user"
                / "gnome-keyring-daemon.service.d"
                / installer.CREDENTIAL_DROPIN_NAME
            ).exists()
        )
        self.assertEqual(login_keyring.read_text(encoding="utf-8"), "encrypted")
        commands = command_log.read_text(encoding="utf-8") if command_log.exists() else ""
        self.assertNotIn("daemon-reload", commands)
        self.assertNotIn("restart gnome-keyring-daemon.service", commands)

    def test_headless_install_refuses_to_discard_persistent_credentials(self) -> None:
        self.make_release("v1.0.0")
        self.write_command(
            "busctl",
            r'''case "$*" in
  "--user --list") printf 'org.freedesktop.secrets\n' ;;
  *"ReadAlias"*) printf 'o "/"\n' ;;
  *" Collections") printf 'ao 1 "/org/freedesktop/secrets/collection/login"\n' ;;
  *" Items") printf 'ao 1 "/org/freedesktop/secrets/collection/login/1"\n' ;;
  *) exit 2 ;;
esac''',
        )
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("persistent collection", result.stderr)
        self.assertIn("Remove or migrate", result.stderr)
        self.assertFalse(
            (self.root / "config" / "systemd" / "user" / "gnome-keyring-daemon.service.d" / installer.CREDENTIAL_DROPIN_NAME).exists()
        )
        self.assert_retained_empty_lock_root()

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
        self.assertIn(
            "marker:new", (self.root / "data" / "concord" / "v1.1.0" / "bin" / "concord").read_text()
        )
        adapter = self.root / "config" / "opencode" / "tools" / "concord.ts"
        self.assertIn("new", adapter.read_text(encoding="utf-8"))
        config = self.config.read_text(encoding="utf-8")
        self.assertIn("concord/current/skills", config)
        self.assertNotIn("v1.0.0/skills", config)

    def test_upgrade_keeps_stable_skills_registration_unchanged(self) -> None:
        self.make_release("v1.0.0", "old")
        self.make_release("v1.1.0", "new")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        before_bytes = self.config.read_bytes()
        before_mtime = self.config.stat().st_mtime_ns

        second = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(self.config.read_bytes(), before_bytes)
        self.assertEqual(self.config.stat().st_mtime_ns, before_mtime)

    def test_upgrade_migrates_a_versioned_skills_registration_once(self) -> None:
        self.make_release("v1.0.0", "old")
        self.make_release("v1.1.0", "new")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)

        old_skill = str(self.root / "data" / "concord" / "v1.0.0" / "skills")
        stable_skill = str(self.root / "data" / "concord" / "current" / "skills")
        self.config.write_text(self.config.read_text(encoding="utf-8").replace(stable_skill, old_skill), encoding="utf-8")
        manifest_path = self.root / "data" / "concord" / installer.MANIFEST_NAME
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["skill_path"] = old_skill
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

        upgraded = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(upgraded.returncode, 0, upgraded.stderr)
        config = self.config.read_text(encoding="utf-8")
        self.assertIn(stable_skill, config)
        self.assertNotIn(old_skill, config)
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        self.assertEqual(manifest["skill_path"], stable_skill)

    def retention_report(self, *release_roots: str) -> Path:
        report = self.root / "host-leases.json"
        payload = {"leases": [
            {"pid": 4242 + index, "release_root": root, "schema_version": 1}
            for index, root in enumerate(release_roots)
        ]}
        report.write_text(json.dumps(payload), encoding="utf-8")
        return report

    def test_retention_keeps_a_release_a_live_lease_holds(self) -> None:
        """CD-0111 D2: a held release stays, and the next install retries it.

        A live session holds the replaced release, so the upgrade keeps it and
        records it as retained. The next upgrade sees no lease, removes it,
        and keeps its own replaced release only while a lease holds that one.
        """
        self.make_release("v1.0.0", "old")
        self.make_release("v1.1.0", "new")
        self.make_release("v1.2.0", "newer")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        held = str(self.root / "data" / "concord" / "v1.0.0")
        self.env["CONCORD_TEST_HOST_LEASES"] = str(self.retention_report(held))
        second = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertTrue((self.root / "data" / "concord" / "v1.0.0").exists(), "a held release was removed")
        manifest = json.loads((self.root / "data" / "concord" / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertIn("v1.0.0", manifest["retained_releases"])

        # No lease holds v1.0.0 anymore; one holds the replaced v1.1.0.
        held = str(self.root / "data" / "concord" / "v1.1.0")
        self.env["CONCORD_TEST_HOST_LEASES"] = str(self.retention_report(held))
        third = self.run_installer("install", "--version", "v1.2.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(third.returncode, 0, third.stderr)
        self.assertFalse((self.root / "data" / "concord" / "v1.0.0").exists(), "an unheld retained release was kept")
        self.assertTrue((self.root / "data" / "concord" / "v1.1.0").exists(), "a held release was removed")
        # The manifest is written before cleanup runs, so it still names the
        # just-removed v1.0.0; the next install drops entries whose directory
        # is gone. With no lease anywhere, both old releases disappear.
        manifest = json.loads((self.root / "data" / "concord" / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertIn("v1.1.0", manifest["retained_releases"])
        self.env["CONCORD_TEST_HOST_LEASES"] = str(self.retention_report())
        fourth = self.run_installer("install", "--version", "v1.2.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(fourth.returncode, 0, fourth.stderr)
        self.assertFalse((self.root / "data" / "concord" / "v1.0.0").exists())
        self.assertFalse((self.root / "data" / "concord" / "v1.1.0").exists())
        manifest = json.loads((self.root / "data" / "concord" / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertEqual(manifest["retained_releases"], {})

    def test_retention_output_lists_holder_sessions(self) -> None:
        """CD-0191: the retention output names the holder sessions per
        retained release, so the operator knows exactly which sessions to
        restart after the install makes them stale."""
        self.make_release("v1.0.0", "old")
        self.make_release("v1.1.0", "new")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        held_root = str(self.root / "data" / "concord" / "v1.0.0")
        report = self.root / "host-leases-holders.json"
        report.write_text(json.dumps({"leases": [
            {"pid": 4242, "release_root": held_root, "schema_version": 1, "directory": "/srv/site"},
            {"pid": 4243, "release_root": held_root, "schema_version": 1, "worktree": "/srv/site/.worktrees/wt"},
        ]}), encoding="utf-8")
        self.env["CONCORD_TEST_HOST_LEASES"] = str(report)
        second = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertTrue((self.root / "data" / "concord" / "v1.0.0").exists(), "a held release was removed")
        self.assertIn("keeping release v1.0.0: a live session holds it (pid 4242 in /srv/site; pid 4243 in /srv/site/.worktrees/wt)", second.stderr)
        manifest = json.loads((self.root / "data" / "concord" / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertIn("v1.0.0", manifest["retained_releases"])

    def test_a_failed_observation_keeps_the_replaced_release_only(self) -> None:
        """CD-0111 D2 bounded fallback: with no host observation the replaced
        release stays, because a session that started before the launcher
        moved may still hold it, and older candidates are removed."""
        self.make_release("v1.0.0", "old")
        self.make_release("v1.1.0", "new")
        self.make_release("v1.2.0", "newer")
        self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.env["CONCORD_TEST_HOST_LEASES"] = str(self.retention_report(str(self.root / "data" / "concord" / "v1.0.0")))
        self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts))
        self.assertTrue((self.root / "data" / "concord" / "v1.0.0").exists())

        self.env["CONCORD_TEST_HOST_LEASES"] = ""
        self.env["CONCORD_TEST_HOST_LEASES_FAIL"] = "1"
        third = self.run_installer("install", "--version", "v1.2.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(third.returncode, 0, third.stderr)
        self.assertTrue((self.root / "data" / "concord" / "v1.1.0").exists(), "the replaced release was removed without an observation")
        self.assertFalse((self.root / "data" / "concord" / "v1.0.0").exists(), "an older candidate was kept without an observation")
        self.assertIn("no host observation", third.stderr)

    # ---- CON-807: prepare-then-activate, the maintenance fence, recovery ---

    @property
    def data_root(self) -> Path:
        return self.root / "data" / "concord"

    def assert_retained_empty_lock_root(self) -> None:
        """The approved retained-directory result (CON-807,
        obs:1ca149d633e69626): the lock root and its ancestor survive the
        command, and no installed or command state hides behind retention."""
        self.assertTrue(self.data_root.is_dir(), "the retained lock root must survive the command")
        self.assertTrue((self.root / "data").is_dir(), "the lock root's ancestor must survive the command")
        self.assertFalse(any(self.data_root.iterdir()), "retention must not keep installed or command state")

    @property
    def prepared_path(self) -> Path:
        return self.data_root / installer.PREPARED_RELEASE_NAME

    @property
    def fence_path(self) -> Path:
        return self.data_root / installer.MAINTENANCE_FENCE_NAME

    def synthetic_pair_digest(self, version: str) -> str:
        """The pinned-pair digest make_release bakes into one synthetic
        release's adapter contracts and truthful core descriptor."""
        return "sha256:" + hashlib.sha256(f"concord-pinned-pair:{version}".encode("utf-8")).hexdigest()

    def readiness_plan(self, blocked: bool, blockers: list[str] | None = None) -> Path:
        path = self.root / "upgrade-plan.json"
        plan: dict[str, object] = {
            "store_present": True,
            "fresh_store": False,
            "applied_versions": [111],
            "schema_version": 111,
            "pending": [],
            "pending_breaking": [],
            "activation_blocked": blocked,
            "compatibility_floor": 111,
            "migration_command": "concord upgrade",
            "activation_command": "",
            "maintenance_fence": None,
        }
        if blocked:
            plan["pending_breaking"] = [{"version": 112, "name": "example_breaking", "breaking": True}]
            plan["blockers"] = blockers or [
                "breaking migration 112 (example_breaking) is pending; activation would strand every session that predates it"
            ]
        path.write_text(json.dumps(plan) + "\n", encoding="utf-8")
        return path

    def plan_env(self, blocked: bool | None = None, *, fail: bool = False) -> dict[str, str]:
        environment = self.env.copy()
        environment.pop("CONCORD_TEST_UPGRADE_PLAN", None)
        environment.pop("CONCORD_TEST_UPGRADE_PLAN_FAIL", None)
        if fail:
            environment["CONCORD_TEST_UPGRADE_PLAN_FAIL"] = "1"
        elif blocked is not None:
            environment["CONCORD_TEST_UPGRADE_PLAN"] = str(self.readiness_plan(blocked))
        return environment

    def install_release(self, version: str, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
        self.make_release(version)
        return self.run_installer("install", "--version", version, "--artifact-dir", str(self.artifacts), env=env)

    def assert_active_release(self, version: str, marker: str) -> None:
        launcher = self.root / "bin" / "concord"
        self.assertTrue(launcher.is_symlink(), "the launcher is not a symlink")
        self.assertEqual(os.readlink(launcher), str(self.data_root / version / "bin" / "concord"))
        self.assertEqual(os.readlink(self.data_root / "current"), str(self.data_root / version))
        tools_marker = (self.root / "config" / "opencode" / "tools" / "concord.ts").read_text(encoding="utf-8").strip()
        self.assertEqual(tools_marker, f"concord.ts:{marker}")
        agents_marker = (self.root / "config" / "opencode" / "agents" / "concord-implement.md").read_text(encoding="utf-8").strip()
        self.assertEqual(agents_marker, f"agent:concord-implement.md:{marker}")

    def prepare_a_blocked_release(self) -> None:
        self.install_release("v1.0.0")
        result = self.install_release("v1.1.0", env=self.plan_env(blocked=True))
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_a_blocked_readiness_prepares_without_activating(self) -> None:
        """CON-807: a pending breaking step must leave the running sessions'
        release fully in place while the candidate waits durably."""
        self.prepare_a_blocked_release()
        self.assert_active_release("v1.0.0", "v1.0.0")
        self.assertTrue((self.data_root / "v1.1.0" / "bin" / "concord").is_file(), "the candidate tree is missing")
        self.assertTrue(self.prepared_path.is_file(), "no prepared-release record was written")
        record = json.loads(self.prepared_path.read_text(encoding="utf-8"))
        self.assertEqual(record["version"], "v1.1.0")
        self.assertTrue(record["blockers"], "the record carries no blockers")
        self.assertIn(str(self.data_root / "v1.1.0" / "bin" / "concord"), record["migration_command"])
        self.assertIn("activate --version v1.1.0", record["activation_command"])
        self.assertIn(str(SCRIPT), record["activation_command"])
        manifest = json.loads((self.data_root / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertEqual(manifest["version"], "v1.0.0", "a blocked activation changed the manifest")
        self.assertFalse(self.fence_path.exists(), "prepare opened a maintenance fence")

    def assert_superseding_install_refused_under_open_boundary(self, plan: dict[str, str]) -> None:
        """An open boundary means the prepared candidate's incompatible
        migration may have committed. A blocked or unknown install must not
        replace that record: it refuses, and the original activation still
        finishes and closes the boundary (CON-807)."""
        self.prepare_a_blocked_release()
        fence = {
            "fence_id": "committed-migration",
            "operation": installer.CORE_UPGRADE_OPERATION,
            "release_root": str(self.data_root / "v1.1.0"),
        }
        self.fence_path.write_text(json.dumps(fence), encoding="utf-8")
        superseding = self.install_release("v1.2.0", env=plan)
        self.assertNotEqual(superseding.returncode, 0, "a blocked install replaced the prepared release")
        self.assertIn("activate --version v1.1.0", superseding.stderr)
        self.assertEqual(json.loads(self.prepared_path.read_text(encoding="utf-8"))["version"], "v1.1.0")
        self.assertTrue(self.fence_path.exists(), "the refused install closed the boundary")
        activation = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertEqual(activation.returncode, 0, activation.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")
        self.assertFalse(self.fence_path.exists(), "the owning activation left the boundary open")
        self.assertFalse(self.prepared_path.exists())

    def test_every_command_refuses_while_another_maintenance_command_runs(self) -> None:
        """The installer and the core's migration command share one
        maintenance lock (CON-807). While another command holds it, every
        installer command refuses before it recovers a transaction or
        touches the boundary, and changes nothing."""
        self.prepare_a_blocked_release()
        fence = {
            "fence_id": "running-migration",
            "operation": installer.CORE_UPGRADE_OPERATION,
            "release_root": str(self.data_root / "v1.1.0"),
        }
        self.fence_path.write_text(json.dumps(fence), encoding="utf-8")
        record_before = self.prepared_path.read_bytes()
        held = os.open(self.data_root, os.O_RDONLY | os.O_DIRECTORY)
        try:
            fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
            for arguments in (
                ("status",),
                ("activate", "--version", "v1.1.0"),
                ("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts)),
                ("uninstall",),
            ):
                with self.subTest(command=arguments[0]):
                    result = self.run_installer(*arguments, env=self.plan_env(blocked=False))
                    self.assertEqual(result.returncode, 1, result.stdout)
                    self.assertIn("another maintenance command is in progress", result.stderr)
        finally:
            os.close(held)
        self.assertEqual(json.loads(self.fence_path.read_text(encoding="utf-8"))["fence_id"], "running-migration")
        self.assertEqual(self.prepared_path.read_bytes(), record_before)
        self.assert_active_release("v1.0.0", "v1.0.0")
        activation = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertEqual(activation.returncode, 0, activation.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")

    def test_a_blocked_install_refuses_while_a_maintenance_boundary_is_open(self) -> None:
        self.assert_superseding_install_refused_under_open_boundary(self.plan_env(blocked=True))

    def test_a_first_install_holds_the_maintenance_lock_on_an_absent_root(self) -> None:
        """A first-install bootstrap creates the data root to lock it, so
        even the first command runs inside the shared maintenance exclusion
        (CON-807): a second command must not recover a live transaction or
        stage beside it while the first one bootstraps."""
        self.assertFalse(self.data_root.exists(), "the fixture must start without a data root")
        with installer.maintenance_lock(installer.paths_for(self.root)):
            self.assertTrue(self.data_root.is_dir(), "the acquisition must create and hold the data root")
            for arguments in (("status",), ("uninstall",)):
                with self.subTest(command=arguments[0]):
                    result = self.run_installer(*arguments)
                    self.assertEqual(result.returncode, 1, result.stdout)
                    self.assertIn("another maintenance command is in progress", result.stderr)
        # Retention is the approved outcome (obs:1ca149d633e69626): the
        # empty bootstrapped root and its ancestors stay, because no cleanup
        # may remove a directory a replacement holder could own.
        self.assertTrue(self.data_root.is_dir(), "a failed first command must retain the empty bootstrapped root")
        self.assertTrue((self.root / "data").is_dir(), "a failed first command must retain the bootstrapped ancestors")
        self.assertFalse(any(self.data_root.iterdir()), "retention must not keep command state")

    def test_the_maintenance_lock_reacquires_a_replaced_data_root(self) -> None:
        """The lock identity is the directory the path names at admission:
        when the root is removed and recreated between the open and the
        flock, the stale descriptor is released and the lock is taken on the
        root that is current now (CON-807)."""
        paths = installer.paths_for(self.root)
        paths.data_root.mkdir(parents=True)
        real_flock = fcntl.flock
        calls = 0

        def replace_between_open_and_flock(fd: int, operation: int) -> None:
            nonlocal calls
            calls += 1
            if calls == 1:
                shutil.rmtree(paths.data_root)
                paths.data_root.mkdir(mode=0o700)
            real_flock(fd, operation)

        with mock.patch.object(installer.fcntl, "flock", side_effect=replace_between_open_and_flock):
            with installer.maintenance_lock(paths):
                probe = os.open(paths.data_root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
                try:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
                finally:
                    os.close(probe)
        self.assertGreaterEqual(calls, 2, "the acquisition retried without a bounded local sequence")
        probe = os.open(paths.data_root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fcntl.flock(probe, fcntl.LOCK_UN)
        finally:
            os.close(probe)

    def test_the_maintenance_lock_refuses_a_symlinked_data_root(self) -> None:
        """The data root is opened without following symlinks, so a replaced
        or planted link can never move the lock onto another directory
        (CON-807)."""
        target = self.root / "elsewhere"
        target.mkdir()
        (self.root / "data").mkdir()
        self.data_root.symlink_to(target, target_is_directory=True)
        with self.assertRaises(installer.InstallerError) as raised:
            with installer.maintenance_lock(installer.paths_for(self.root)):
                self.fail("a symlinked data root was locked")
        self.assertIn("symlink", str(raised.exception))

    def test_the_maintenance_lock_is_released_when_the_holder_dies(self) -> None:
        """The lock dies with its holder: the kernel releases a flock when
        the open file description closes, so a killed command leaves the
        root immediately acquirable (CON-807)."""
        paths = installer.paths_for(self.root)
        read_fd, write_fd = os.pipe()
        pid = os.fork()
        if pid == 0:
            os.close(read_fd)
            try:
                with installer.maintenance_lock(paths):
                    os.write(write_fd, b"held")
                    time.sleep(10)
            finally:
                os._exit(0)
        os.close(write_fd)
        held = os.read(read_fd, 4)
        self.assertEqual(held, b"held")
        os.kill(pid, signal.SIGKILL)
        os.waitpid(pid, 0)
        os.close(read_fd)
        with installer.maintenance_lock(paths):
            pass

    def test_a_failed_command_retains_the_bootstrapped_root_and_ancestors(self) -> None:
        """A command that fails mid-hold keeps the empty root it created
        (CON-807): retention (obs:1ca149d633e69626) is the approved outcome
        for failed installation, because no cleanup can prove a replacement
        holder has not taken the directory over."""
        paths = installer.paths_for(self.root)
        with self.assertRaises(RuntimeError):
            with installer.maintenance_lock(paths):
                raise RuntimeError("command failed mid-hold")
        self.assertTrue(self.data_root.is_dir(), "a failed command must retain the empty bootstrapped root")
        self.assertTrue((self.root / "data").is_dir(), "a failed command must retain the bootstrapped ancestors")
        self.assertFalse(any(self.data_root.iterdir()), "retention must not keep command state")

    def test_a_normal_release_retains_the_bootstrapped_root_and_ancestors(self) -> None:
        """A normal maintenance release keeps the empty root it created
        (CON-807): retained disk space is the accepted cost of never letting
        a cleanup delete a directory another participant may hold."""
        paths = installer.paths_for(self.root)
        with installer.maintenance_lock(paths):
            pass
        self.assertTrue(self.data_root.is_dir(), "a normal release must retain the empty bootstrapped root")
        self.assertTrue((self.root / "data").is_dir(), "a normal release must retain the bootstrapped ancestors")
        self.assertFalse(any(self.data_root.iterdir()), "retention must not keep command state")

    def test_final_cleanup_never_removes_a_replacement_published_under_the_lock(self) -> None:
        """The reproduced final-cleanup interleaving, promoted to
        deterministic coverage (CON-807): while the holder's cleanup removes
        the root path, another participant replaces the root and a second
        real acquisition holds the replacement. Against the deleting cleanup
        this test fails exactly there — the removal deleted the second
        holder's root. Under retention (obs:1ca149d633e69626) the release
        removes no data root at all, so the interleaving never fires."""
        paths = installer.paths_for(self.root)
        real_rmdir = os.rmdir
        interleaved: list[bool] = []

        def rmdir_with_replacement(path):
            if os.fspath(path) == str(paths.data_root) and not interleaved:
                interleaved.append(True)
                os.rename(paths.data_root, self.root / "aside")  # the replacement owner moves the root away
                paths.data_root.mkdir(mode=0o700)  # and publishes its own empty root at the path
                with installer.maintenance_lock(paths):  # a second real acquisition holds the replacement
                    interleaved.append(True)
            return real_rmdir(path)

        with mock.patch.object(installer.os, "rmdir", side_effect=rmdir_with_replacement):
            with installer.maintenance_lock(paths):
                pass
        self.assertFalse(interleaved, "the release still removes the data root; the promoted interleaving fired")
        self.assertTrue(self.data_root.is_dir(), "the release must retain the bootstrapped root")
        self.assertTrue((self.root / "data").is_dir(), "the release must retain the bootstrapped ancestors")

    def test_an_uninstall_of_a_fresh_root_retains_the_lock_root(self) -> None:
        """Uninstall over a machine that never installed keeps the lock root
        it bootstrapped (CON-807): retention (obs:1ca149d633e69626) replaces
        the earlier no-residue rule, and only intended installed content is
        removed."""
        result = self.run_installer("uninstall")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("nothing was changed", result.stdout)
        self.assertTrue((self.root / "data" / "concord").is_dir(), "uninstall of a fresh root left no lock root")
        self.assertTrue((self.root / "data").is_dir(), "uninstall of a fresh root left no lock-root ancestor")

    def test_an_unchanged_active_release_survives_a_validation_failure(self) -> None:
        """A release-tree validation failure must refuse before any swap:
        the active launcher, current root, and manifest keep the release the
        running sessions hold (CON-807)."""
        self.install_release("v1.0.0")
        marker = self.data_root / "v1.0.0" / "bin" / "concord"
        marker.write_bytes(marker.read_bytes() + b"tampered")
        self.make_release("v1.1.0")
        result = self.run_installer(
            "install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts), env=self.plan_env(blocked=False)
        )
        self.assertNotEqual(result.returncode, 0, "a tampered installed tree let an install proceed")
        self.assert_active_release("v1.0.0", "v1.0.0")

    def test_a_symlinked_replacement_never_admits_the_lock(self) -> None:
        """A root moved aside and replaced by a symlink to itself must not
        be admitted: the post-flock confirmation reads the path with lstat,
        never following the link back to the inode the descriptor holds
        (CON-807)."""
        paths = installer.paths_for(self.root)
        paths.data_root.mkdir(parents=True)
        moved = self.root / "moved-aside"
        real_flock = fcntl.flock
        calls = 0

        def move_aside_and_link(fd: int, operation: int) -> None:
            nonlocal calls
            calls += 1
            if calls == 1:
                os.rename(paths.data_root, moved)
                paths.data_root.symlink_to(moved, target_is_directory=True)
            real_flock(fd, operation)

        with mock.patch.object(installer.fcntl, "flock", side_effect=move_aside_and_link):
            with self.assertRaises(installer.InstallerError) as raised:
                with installer.maintenance_lock(paths):
                    self.fail("a replaced symlink admitted the maintenance lock")
        self.assertIn("symlink", str(raised.exception))
        self.assertTrue(paths.data_root.is_symlink(), "the probe must leave the planted symlink in place")
        # The moved-aside directory is unlocked again: the stale descriptor
        # was released with the refused acquisition.
        probe = os.open(moved, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fcntl.flock(probe, fcntl.LOCK_UN)
        finally:
            os.close(probe)

    def test_a_foreign_created_data_root_survives_a_crashed_install_recovery(self) -> None:
        """Transaction cleanup never removes the data root: it is the
        maintenance lock's root, and no cleanup removes it. A root another
        participant created survives a crashed install and the recovery that
        rolls it back (CON-807)."""
        self.data_root.mkdir(parents=True)  # foreign: no installer command created it
        self.make_release("v1.0.0")
        crash = self.run_after_phase("staged", "install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(crash.returncode, 97, crash.stderr)
        self.assertTrue(self.data_root.is_dir(), "the crash itself removed the foreign data root")
        recovery = self.run_installer("status")
        self.assertEqual(recovery.returncode, 0, recovery.stderr)
        self.assertTrue(self.data_root.is_dir(), "recovery deleted a foreign-created data root")
        self.assertFalse(any(self.data_root.iterdir()), "recovery left transaction state behind")

    def test_a_failed_first_install_retains_the_empty_root_and_ancestors(self) -> None:
        """A first install that fails keeps the empty root it bootstrapped
        (CON-807): retention (obs:1ca149d633e69626) replaces the earlier
        no-residue rule, and only the root's emptiness is checked — no
        command state may hide behind retention."""
        result = self.run_installer("install", "--version", "v9.9.9", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0, "an install of a missing artifact succeeded")
        self.assertTrue(self.data_root.is_dir(), "a failed first install must retain the empty bootstrapped root")
        self.assertTrue((self.root / "data").is_dir(), "a failed first install must retain the bootstrapped ancestors")
        self.assertFalse(any(self.data_root.iterdir()), "retention must not keep command state")

    def test_an_uninstall_removes_installed_content_and_retains_the_lock_root(self) -> None:
        """Uninstall removes the intended installed content — the release
        tree, the current root, and the manifest — and retains the lock root
        it ends with (CON-807, obs:1ca149d633e69626): the retained empty
        directory is the accepted cost of never letting a cleanup delete a
        directory a replacement holder may own."""
        self.install_release("v1.0.0")
        self.assertTrue(self.data_root.is_dir(), "the fixture must start with an established root")
        result = self.run_installer("uninstall")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.data_root / "v1.0.0").exists(), "uninstall left the release tree behind")
        self.assertFalse((self.data_root / "current").exists(), "uninstall left the current root behind")
        self.assertFalse((self.data_root / installer.MANIFEST_NAME).exists(), "uninstall left the manifest behind")
        self.assertTrue(self.data_root.is_dir(), "uninstall must retain the lock root")

    def test_an_uninstall_retains_a_foreign_root_and_its_state(self) -> None:
        """Uninstall removes intended installed content only: a foreign root
        and the state another participant wrote into it survive the command
        (CON-807)."""
        paths = installer.paths_for(self.root)
        paths.data_root.mkdir(parents=True)
        (paths.data_root / "foreign-state").write_text("keep", encoding="utf-8")
        result = self.run_installer("uninstall")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("nothing was changed", result.stdout)
        self.assertEqual((paths.data_root / "foreign-state").read_text(encoding="utf-8"), "keep")
        self.assertTrue(paths.data_root.is_dir(), "uninstall deleted a foreign data root")

    def test_an_unknown_install_refuses_while_a_maintenance_boundary_is_open(self) -> None:
        self.assert_superseding_install_refused_under_open_boundary(self.plan_env(fail=True))

    def test_an_unknown_readiness_fails_closed_into_a_prepared_release(self) -> None:
        """A store the candidate cannot read by looking must never reach an
        activation decision; the installer prepares and names the unknown."""
        self.install_release("v1.0.0")
        result = self.install_release("v1.1.0", env=self.plan_env(fail=True))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_active_release("v1.0.0", "v1.0.0")
        record = json.loads(self.prepared_path.read_text(encoding="utf-8"))
        self.assertTrue(any("readiness" in blocker for blocker in record["blockers"]), record["blockers"])

    def test_activate_completes_the_prepared_release(self) -> None:
        self.prepare_a_blocked_release()
        result = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")
        self.assertFalse(self.prepared_path.exists(), "the prepared record survived its activation")
        self.assertFalse(self.fence_path.exists(), "the maintenance fence survived the completed activation")
        manifest = json.loads((self.data_root / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertEqual(manifest["version"], "v1.1.0")

    def test_activate_refuses_while_the_migration_is_still_blocked(self) -> None:
        self.prepare_a_blocked_release()
        result = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=True))
        self.assertEqual(result.returncode, 1)
        self.assertIn("still blocked", result.stdout)
        self.assert_active_release("v1.0.0", "v1.0.0")
        self.assertTrue(self.prepared_path.exists())

    def test_activate_holds_the_fence_through_the_swap_and_closes_it(self) -> None:
        """The shared session-admission exclusion is open while the swap
        runs and closes only after the activation completes."""
        self.prepare_a_blocked_release()
        environment = self.plan_env(blocked=False)
        environment["CONCORD_INSTALLER_STOP_AFTER_PHASE"] = "launcher_swapped"
        stopped = self.run_installer("activate", "--version", "v1.1.0", env=environment)
        self.assertEqual(stopped.returncode, 97)
        self.assertTrue(self.fence_path.is_file(), "the fence was not held during the activation swap")
        fence = json.loads(self.fence_path.read_text(encoding="utf-8"))
        self.assertTrue(fence.get("fence_id"), "the fence carries no identity")
        # A crashed activation recovers forward on the next invocation and
        # closes the boundary: never back to the older release.
        completed = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")
        self.assertFalse(self.fence_path.exists())

    def test_activate_crashes_at_every_phase_recover_forward(self) -> None:
        """After the incompatible migration committed, recovery resumes the
        prepared candidate from every crash phase and never restores the
        older release (CON-807 candidate-forward recovery)."""
        for phase in (
            "staged",
            "version_activated",
            "agents_swapped",
            "adapter_swapped",
            "launcher_swapped",
            "config_swapped",
            "manifest_committed",
        ):
            with self.subTest(phase=phase):
                self.tearDown()
                self.setUp()
                self.prepare_a_blocked_release()
                environment = self.plan_env(blocked=False)
                environment["CONCORD_INSTALLER_STOP_AFTER_PHASE"] = phase
                stopped = self.run_installer("activate", "--version", "v1.1.0", env=environment)
                self.assertEqual(stopped.returncode, 97)
                # Status recovery finishes the committed activation forward;
                # a re-run activate is not required (CON-807).
                recovered = self.run_installer("status", env=self.plan_env(blocked=False))
                self.assertEqual(recovered.returncode, 0, recovered.stderr)
                self.assert_active_release("v1.1.0", "v1.1.0")
                self.assertFalse(self.prepared_path.exists())
                self.assertFalse(self.fence_path.exists())
                self.tearDown()
                self.setUp()
                self.prepare_a_blocked_release()
                environment = self.plan_env(blocked=False)
                environment["CONCORD_INSTALLER_STOP_AFTER_PHASE"] = phase
                stopped = self.run_installer("activate", "--version", "v1.1.0", env=environment)
                self.assertEqual(stopped.returncode, 97)
                completed = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
                self.assertEqual(completed.returncode, 0, completed.stderr)
                self.assert_active_release("v1.1.0", "v1.1.0")
                self.assertFalse(self.prepared_path.exists())
                self.assertFalse(self.fence_path.exists())

    def test_a_prepared_install_crash_converges_to_the_prepared_state(self) -> None:
        """A crash after the prepared record landed recovers to the prepared
        state, and a re-run install re-prepares coherently."""
        self.install_release("v1.0.0")
        self.make_release("v1.1.0")
        environment = self.plan_env(blocked=True)
        environment["CONCORD_INSTALLER_STOP_AFTER_PHASE"] = "prepared"
        stopped = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts), env=environment)
        self.assertEqual(stopped.returncode, 97)
        status = self.run_installer("status")
        self.assertEqual(status.returncode, 0, status.stderr)
        report = json.loads(status.stdout)
        self.assertEqual(report["prepared_release"]["version"], "v1.1.0")
        self.assert_active_release("v1.0.0", "v1.0.0")
        again = self.run_installer(
            "install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts), env=self.plan_env(blocked=True)
        )
        self.assertEqual(again.returncode, 0, again.stderr)
        self.assert_active_release("v1.0.0", "v1.0.0")
        self.assertTrue(self.prepared_path.exists())

    def test_a_compatible_install_activates_immediately_and_retains_a_held_release(self) -> None:
        """CON-807 rolling-first: with readiness unblocked the install
        activates now, an old session keeps its release, and the new session
        surface is in place — supported compatible pairs coexist."""
        self.install_release("v1.0.0")
        held = str(self.data_root / "v1.0.0")
        report = self.root / "held-lease.json"
        report.write_text(json.dumps({"leases": [
            {"pid": 4242, "release_root": held, "schema_version": 111, "directory": "/srv/site"},
        ]}), encoding="utf-8")
        environment = self.plan_env(blocked=False)
        environment["CONCORD_TEST_HOST_LEASES"] = str(report)
        result = self.install_release("v1.1.0", env=environment)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")
        self.assertTrue((self.data_root / "v1.0.0").exists(), "a release a live session holds was removed")
        self.assertFalse(self.prepared_path.exists(), "a compatible activation left a prepared record")
        self.assertFalse(self.fence_path.exists())

    def test_a_failed_observation_keeps_every_release_under_the_fence(self) -> None:
        """A prepared activation with no host observation deletes nothing:
        a referenced release is never deleted on a snapshot's word alone."""
        self.prepare_a_blocked_release()
        environment = self.plan_env(blocked=False)
        environment["CONCORD_TEST_HOST_LEASES"] = ""
        environment["CONCORD_TEST_HOST_LEASES_FAIL"] = "1"
        result = self.run_installer("activate", "--version", "v1.1.0", env=environment)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")
        self.assertTrue((self.data_root / "v1.0.0").exists(), "the replaced release was removed without an observation")
        manifest = json.loads((self.data_root / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertIn("v1.0.0", manifest["retained_releases"])
        self.assertFalse(self.fence_path.exists())

    def test_uninstall_and_repair_refuse_while_a_release_is_prepared(self) -> None:
        self.prepare_a_blocked_release()
        uninstalled = self.run_installer("uninstall")
        self.assertEqual(uninstalled.returncode, 1)
        self.assertIn("prepared release v1.1.0 waits for activation", uninstalled.stderr)
        repaired = self.run_installer("repair")
        self.assertEqual(repaired.returncode, 1)
        self.assertIn("prepared release v1.1.0 waits for activation", repaired.stderr)
        self.assert_active_release("v1.0.0", "v1.0.0")

    def test_status_reports_the_prepared_release_and_its_commands(self) -> None:
        self.prepare_a_blocked_release()
        result = self.run_installer("status")
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        self.assertEqual(report["version"], "v1.0.0")
        self.assertEqual(report["prepared_release"]["version"], "v1.1.0")
        self.assertIn("activate --version v1.1.0", report["prepared_release"]["activation_command"])
        self.assertNotIn("maintenance_boundary", report)

    def test_the_stamped_release_constants_bind_the_adapter_to_its_release(self) -> None:
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(result.returncode, 0, result.stderr)
        stamped = (self.root / "config" / "opencode" / "tools" / installer.RELEASE_CONSTANTS_FILE).read_text(encoding="utf-8")
        release_root = str((self.root / "data" / "concord" / "v1.0.0").resolve())
        self.assertIn(f'export const coreBinary: string = "{release_root}/bin/concord"', stamped)
        self.assertIn(f'export const releaseRoot: string = "{release_root}"', stamped)
        archive_copy = (self.root / "data" / "concord" / "v1.0.0" / "adapter" / "opencode" / installer.RELEASE_CONSTANTS_FILE).read_text(encoding="utf-8")
        self.assertEqual(archive_copy, stamped)

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
        for dev_only in ("concord.test.ts", "dispatch.test.ts", "worker-evidence-vector.json", "worker-cli-required-fields.json"):
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
            expected = "new" if name != installer.RELEASE_CONSTANTS_FILE else str(self.root / "data" / "concord" / "v1.1.0")
            self.assertIn(expected, placed.read_text(encoding="utf-8"))

    def _shrink_installation_to(self, names: tuple[str, ...]) -> None:
        """Rewrite the installed manifest and delete files, as an older
        installer with a shorter adapter list leaves behind: the manifest
        records only the files it deployed, and no more exist on disk."""
        manifest_path = self.root / "data" / "concord" / installer.MANIFEST_NAME
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        for name in names:
            del manifest["adapter_files"][name]
            del manifest["version_files"][f"adapter/opencode/{name}"]
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        version_tree = self.root / "data" / "concord" / "v1.0.0" / "adapter" / "opencode"
        for name in names:
            (version_tree / name).unlink()
            (self.root / "config" / "opencode" / "tools" / name).unlink()

    def test_repair_restores_an_incomplete_deployment(self) -> None:
        missing = ("host-lease.ts", installer.RELEASE_CONSTANTS_FILE)
        self.make_release("v1.0.0")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        self._shrink_installation_to(missing)

        result = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(result.returncode, 0, result.stderr)
        manifest = json.loads((self.root / "data" / "concord" / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        for name in missing:
            placed = self.root / "config" / "opencode" / "tools" / name
            self.assertTrue(placed.is_file(), f"{name} was not restored to the tools directory")
            self.assertIn(name, manifest["adapter_files"], f"{name} missing from repaired manifest adapter_files")
            self.assertIn(f"adapter/opencode/{name}", manifest["version_files"])
        stamped = (self.root / "config" / "opencode" / "tools" / installer.RELEASE_CONSTANTS_FILE).read_text(encoding="utf-8")
        self.assertIn(str(self.root / "data" / "concord" / "v1.0.0"), stamped)
        again = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(again.returncode, 0, again.stderr)
        self.assertIn("already installed", again.stdout)

    def test_repair_reports_nothing_to_do_when_complete(self) -> None:
        self.make_release("v1.0.0")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        manifest_path = self.root / "data" / "concord" / installer.MANIFEST_NAME
        before = manifest_path.read_bytes()

        result = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("no repair needed", result.stdout)
        self.assertEqual(manifest_path.read_bytes(), before)

    def test_repair_restores_a_missing_worktree_project_link(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        shutil.rmtree(worktree / ".opencode")

        repaired = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(repaired.returncode, 0, repaired.stderr)
        worktrees_config = worktree / ".opencode" / "opencode.json"
        self.assertEqual(
            json.loads(worktrees_config.read_text(encoding="utf-8")),
            {"instructions": [str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")]},
        )

    def test_repair_restores_every_missing_worktree_project_link(self) -> None:
        self.make_release("v1.0.0")
        first = self.root / "data" / "concord" / "worktrees" / "project-a" / "work"
        second = self.root / "data" / "concord" / "worktrees" / "project-b" / "work"
        first.mkdir(parents=True)
        second.mkdir(parents=True)
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        shutil.rmtree(first / ".opencode")
        shutil.rmtree(second / ".opencode")

        repaired = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(repaired.returncode, 0, repaired.stderr)
        expected = {"instructions": [str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")]}
        self.assertEqual(json.loads((first / ".opencode" / "opencode.json").read_text(encoding="utf-8")), expected)
        self.assertEqual(json.loads((second / ".opencode" / "opencode.json").read_text(encoding="utf-8")), expected)

    def test_repair_replans_a_worktree_config_that_lost_the_conduct_entry(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        config = worktree / ".opencode" / "opencode.json"
        config.parent.mkdir(parents=True)
        config.write_text('{"keep": true}\n', encoding="utf-8")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        linked = config.read_text(encoding="utf-8")
        config.write_text('{"keep": false}\n', encoding="utf-8")

        repaired = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(repaired.returncode, 0, repaired.stderr)
        replanned = json.loads(config.read_text(encoding="utf-8"))
        self.assertEqual(replanned["keep"], False)
        self.assertIn(str(self.root / "data" / "concord" / "current" / "instructions" / "*.md"), replanned["instructions"])
        # The record keeps the bytes it owned before the host drifted, so
        # uninstall still reasons from what this installer wrote.
        ownership = json.loads((self.root / "data" / "concord" / installer.PROJECT_LINK_OWNERSHIP_NAME).read_text(encoding="utf-8"))
        record = ownership["links"][str(config.resolve())]
        self.assertEqual(record["action"], "restore")
        self.assertEqual(record["original"], '{"keep": true}\n')
        self.assertEqual(record["expected"], {"exists": True, "sha256": hashlib.sha256(linked.encode("utf-8")).hexdigest()})
        self.assertTrue((self.root / "data" / "concord" / "current").is_symlink())

        # Uninstall stays surgical: the drifted host keys survive and only
        # the managed entry leaves.
        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(json.loads(config.read_text(encoding="utf-8")), {"keep": False})

    def test_install_accepts_a_neutral_worktree_config_key(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        config = worktree / ".opencode" / "opencode.json"
        updated = json.loads(config.read_text(encoding="utf-8"))
        updated["$schema"] = "https://opencode.ai/config.json"
        config.write_text(json.dumps(updated, indent=2) + "\n", encoding="utf-8")

        repaired = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(repaired.returncode, 0, repaired.stderr)
        self.assertEqual(json.loads(config.read_text(encoding="utf-8"))["$schema"], "https://opencode.ai/config.json")

    def test_uninstall_removes_only_the_conduct_entry_from_a_neutral_worktree_config(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        config = worktree / ".opencode" / "opencode.json"
        updated = json.loads(config.read_text(encoding="utf-8"))
        updated["$schema"] = "https://opencode.ai/config.json"
        config.write_text(json.dumps(updated, indent=2) + "\n", encoding="utf-8")

        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(json.loads(config.read_text(encoding="utf-8")), {"$schema": "https://opencode.ai/config.json"})

    def test_uninstall_preserves_neutral_changes_to_a_preexisting_worktree_config(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        config = worktree / ".opencode" / "opencode.json"
        config.parent.mkdir(parents=True)
        config.write_text('{"theme": "dark"}\n', encoding="utf-8")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        updated = json.loads(config.read_text(encoding="utf-8"))
        updated["$schema"] = "https://opencode.ai/config.json"
        config.write_text(json.dumps(updated, indent=2) + "\n", encoding="utf-8")

        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(
            json.loads(config.read_text(encoding="utf-8")),
            {"theme": "dark", "$schema": "https://opencode.ai/config.json"},
        )

    def test_uninstall_tolerates_a_deleted_worktree_with_an_ownership_record(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        shutil.rmtree(worktree)

        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)

    def test_neutral_key_absorbed_survives_relink_then_uninstall(self) -> None:
        """check: neutral-key-absorbed — a relink must not hand the host key
        to the uninstall's whole-file restore."""
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        config = worktree / ".opencode" / "opencode.json"
        config.parent.mkdir(parents=True)
        config.write_text('{"theme": "dark"}\n', encoding="utf-8")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        updated = json.loads(config.read_text(encoding="utf-8"))
        updated["$schema"] = "https://opencode.ai/config.json"
        config.write_text(json.dumps(updated, indent=2) + "\n", encoding="utf-8")
        relinked = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(relinked.returncode, 0, relinked.stderr)
        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(
            json.loads(config.read_text(encoding="utf-8")),
            {"theme": "dark", "$schema": "https://opencode.ai/config.json"},
        )

    def test_uninstall_recovers_a_pending_removal_write_that_never_landed(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        config = worktree / ".opencode" / "opencode.json"
        updated = json.loads(config.read_text(encoding="utf-8"))
        updated["$schema"] = "https://opencode.ai/config.json"
        drifted = json.dumps(updated, indent=2) + "\n"
        config.write_text(drifted, encoding="utf-8")
        stripped = json.dumps({"$schema": "https://opencode.ai/config.json"}, indent=2) + "\n"
        pending = self.root / "data" / "concord" / installer.PROJECT_LINK_PENDING_NAME
        pending.write_text(
            json.dumps({"schema": 1, "links": {str(config.resolve()): {
                "scope": "worktree",
                "action": "remove",
                "before": drifted,
                "original": None,
                "updated": stripped,
            }}}) + "\n",
            encoding="utf-8",
        )

        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(
            json.loads(config.read_text(encoding="utf-8")),
            {"$schema": "https://opencode.ai/config.json"},
        )
        self.assertFalse(pending.exists())

    def test_uninstall_tolerates_an_applied_pending_removal(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        config = worktree / ".opencode" / "opencode.json"
        config.parent.mkdir(parents=True)
        config.write_text('{"theme": "dark"}\n', encoding="utf-8")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        linked = config.read_text(encoding="utf-8")
        stripped = '{"theme": "dark"}\n'
        config.write_text(stripped, encoding="utf-8")
        pending = self.root / "data" / "concord" / installer.PROJECT_LINK_PENDING_NAME
        pending.write_text(
            json.dumps({"schema": 1, "links": {str(config.resolve()): {
                "scope": "worktree",
                "action": "restore",
                "before": linked,
                "original": '{"theme": "dark"}\n',
                "updated": stripped,
            }}}) + "\n",
            encoding="utf-8",
        )

        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(config.read_text(encoding="utf-8"), '{"theme": "dark"}\n')
        self.assertFalse(pending.exists())
        self.assertFalse((self.root / "data" / "concord" / installer.PROJECT_LINK_OWNERSHIP_NAME).exists())

    def test_repeated_link_then_uninstall_restores_the_preexisting_config(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer"
        project_file = project_dir / ".opencode" / "opencode.json"
        project_file.parent.mkdir(parents=True)
        original = '{\n  "keep": true\n}\n'
        project_file.write_text(original, encoding="utf-8")

        first = self.run_installer("link", "--project", str(project_dir))
        self.assertEqual(first.returncode, 0, first.stderr)
        second = self.run_installer("link", "--project", str(project_dir))
        self.assertEqual(second.returncode, 0, second.stderr)
        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(project_file.read_text(encoding="utf-8"), original)

    def test_uninstall_refuses_a_symlinked_worktrees_config_parent(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        outside_config = self.root / "outside" / "opencode.json"
        outside_config.parent.mkdir()
        outside_config.write_text(
            json.dumps(
                {"instructions": [str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")]}
            )
            + "\n",
            encoding="utf-8",
        )
        shutil.rmtree(worktree / ".opencode")
        (worktree / ".opencode").symlink_to(outside_config.parent, target_is_directory=True)

        result = self.run_installer("uninstall")

        self.assertNotEqual(result.returncode, 0)
        self.assertIn(str(worktree / ".opencode"), result.stderr)
        self.assertEqual(
            outside_config.read_text(encoding="utf-8"),
            json.dumps(
                {"instructions": [str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")]}
            )
            + "\n",
        )
        self.assertTrue((self.root / "data" / "concord" / installer.MANIFEST_NAME).exists())

    def test_repair_refuses_a_checksum_mismatch_before_changing_anything(self) -> None:
        self.make_release("v1.0.0")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        archive = self.artifacts / "concord-v1.0.0.tar.gz"
        archive.write_bytes(archive.read_bytes() + b"tampered")

        result = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum mismatch", result.stderr)

    def test_repair_refuses_a_modified_managed_file_and_names_it(self) -> None:
        self.make_release("v1.0.0")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        target = self.root / "config" / "opencode" / "tools" / "concord.ts"
        target.write_text("operator edited this file\n", encoding="utf-8")

        result = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("modified managed file", result.stderr)
        self.assertIn("concord.ts", result.stderr)
        self.assertEqual(target.read_text(encoding="utf-8"), "operator edited this file\n")

    def test_repair_preserves_unmanaged_state_and_user_configuration(self) -> None:
        self.make_release("v1.0.0")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        user_agent = self.root / "config" / "opencode" / "agents" / "concord-operator-notes.md"
        user_agent.write_text("operator authored\n", encoding="utf-8")
        database = self.root / "data" / "concord" / "store.db"
        database.write_bytes(b"work database bytes")
        worktree = self.root / "data" / "concord" / "worktrees" / "concord" / "work-x"
        worktree.mkdir(parents=True)
        (worktree / "note.txt").write_text("session artifact\n", encoding="utf-8")
        self._shrink_installation_to((installer.RELEASE_CONSTANTS_FILE,))

        result = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(user_agent.read_text(encoding="utf-8"), "operator authored\n")
        self.assertEqual(database.read_bytes(), b"work database bytes")
        self.assertEqual((worktree / "note.txt").read_text(encoding="utf-8"), "session artifact\n")
        self.assertIn("keep", self.config.read_text(encoding="utf-8"))

    def test_repair_process_death_at_every_phase_recovers(self) -> None:
        self.make_release("v1.0.0")
        first = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(first.returncode, 0, first.stderr)
        for phase in ("staged", "version_activated", "agents_swapped", "adapter_swapped", "launcher_swapped", "config_swapped", "manifest_committed"):
            with self.subTest(phase=phase):
                self._shrink_installation_to((installer.RELEASE_CONSTANTS_FILE,))
                died = self.run_after_phase(phase, "repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
                self.assertEqual(died.returncode, 97, died.stderr)
                recovered = self.run_installer("repair", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
                self.assertEqual(recovered.returncode, 0, recovered.stderr)
                placed = self.root / "config" / "opencode" / "tools" / installer.RELEASE_CONSTANTS_FILE
                self.assertTrue(placed.is_file(), f"{phase}: stamped constants were not restored after recovery")

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
        self.assert_retained_empty_lock_root()
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
        self.assert_retained_empty_lock_root()

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
        self.assert_retained_empty_lock_root()

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

    def test_install_recognizes_a_tuple_entry_and_keeps_its_options(self) -> None:
        """CD-0182: an operator tuple entry with a session opener survives an upgrade."""
        opener = ["my-tabs", "new-tab", "--cwd", "{directory}", "--title", "{title}", "--", "{command}"]
        entry = self.plugin_entry_path()
        self.config.write_text(
            json.dumps({"keep": True, "plugin": [[entry, {"session_opener": opener}], "/operator/other"]}, indent=2),
            encoding="utf-8",
        )
        self.make_release("v1.0.0")
        result = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(result.returncode, 0, result.stderr)
        config = self.config.read_text(encoding="utf-8")
        plugin = installer.jsonc_data(config)["plugin"]
        self.assertEqual(config.count(entry), 1, "the upgrade must not add a duplicate bare entry")
        tuples = [item for item in plugin if isinstance(item, list)]
        self.assertEqual(len(tuples), 1)
        self.assertEqual(tuples[0][0], entry)
        self.assertEqual(tuples[0][1]["session_opener"], opener)
        self.assertIn("/operator/other", plugin)

    def test_uninstall_removes_a_tuple_entry_whole(self) -> None:
        """The tuple form deregisters with its options; no option fragment stays."""
        entry = self.plugin_entry_path()
        tuple_text = (
            "    [\n"
            + f"      {json.dumps(entry)},\n"
            + '      {"session_opener": ["my-tabs", "new", "--x[]y", "{command}"]}\n'
            + "    ]"
        )
        self.config.write_text(
            '{\n  "keep": true,\n  "plugin": [\n' + tuple_text + ',\n    "/operator/other"\n  ]\n}\n',
            encoding="utf-8",
        )
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        # One registration, in tuple form: the path occurs once, inside the tuple.
        self.assertEqual(self.config.read_text(encoding="utf-8").count(entry), 1)
        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        config = self.config.read_text(encoding="utf-8")
        self.assertNotIn(entry, config)
        self.assertNotIn("session_opener", config)
        plugin = installer.jsonc_data(config)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

    def test_uninstall_survives_a_comment_before_the_tuple(self) -> None:
        """A ``/* [ */`` comment beside the tuple corrupts nothing on uninstall."""
        entry = self.plugin_entry_path()
        self.config.write_text(
            '{\n  "keep": true,\n  "plugin": [\n'
            '    /* [ comment bracket */ "/operator/other",\n'
            '    ["' + entry + '", {"session_opener": ["my-tabs", "{command}"]}]\n'
            '  ]\n}\n',
            encoding="utf-8",
        )
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        config = self.config.read_text(encoding="utf-8")
        self.assertNotIn(entry, config)
        self.assertNotIn("session_opener", config)
        # The comment stays, the neighbour stays, and the result still parses.
        self.assertIn("comment bracket", config)
        plugin = installer.jsonc_data(config)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

    def test_uninstall_survives_a_comment_after_the_tuple(self) -> None:
        """A comment between the tuple and its comma corrupts nothing on uninstall."""
        entry = self.plugin_entry_path()
        self.config.write_text(
            '{\n  "keep": true,\n  "plugin": [\n'
            '    ["' + entry + '", {"session_opener": ["my-tabs", "{command}"]}] /* after */,\n'
            '    "/operator/other"\n  ]\n}\n',
            encoding="utf-8",
        )
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        config = self.config.read_text(encoding="utf-8")
        self.assertNotIn(entry, config)
        self.assertNotIn("session_opener", config)
        plugin = installer.jsonc_data(config)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

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
        self.assert_retained_empty_lock_root()

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
                if phase in {"manifest_committed", "cleanup"}:
                    pointer = self.root / "data" / "concord" / "worktrees" / ".opencode" / "opencode.json"
                    self.assertFalse(pointer.exists(), f"{phase}: legacy worktrees root conduct link survived")
                installed = self.run_installer("install", "--version", version, "--artifact-dir", str(self.artifacts))
                self.assertEqual(installed.returncode, 0, installed.stderr)
                self.assertIn(f'"version": "{version}"', self.run_installer("status").stdout)
                removed = self.run_installer("uninstall")
                self.assertEqual(removed.returncode, 0, removed.stderr)
                self.reset_config()
                self.assert_retained_empty_lock_root()

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
                self.assert_retained_empty_lock_root()

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

    def test_paths_for_redirects_opencode_projects_shard_to_the_durable_root(self) -> None:
        shard = self.root / ".local" / "share" / "opencode-projects" / "abc123"
        durable = self.root / ".local" / "share" / "concord"
        stderr = io.StringIO()
        with mock.patch.dict(installer.os.environ, {"XDG_DATA_HOME": str(shard)}, clear=False), mock.patch.object(
            installer.Path, "home", return_value=self.root
        ), contextlib.redirect_stderr(stderr):
            paths = installer.paths_for(None)
        self.assertEqual(paths.data_root, durable)
        self.assertEqual(paths.stable_root, durable / installer.STABLE_ROOT_NAME)
        message = stderr.getvalue()
        self.assertIn("per-project session shard", message)
        self.assertIn(str(shard), message)
        self.assertIn(str(durable), message)

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

    def test_link_preserves_jsonc_comments_and_uses_existing_jsonc_config(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer"
        project_file = project_dir / ".opencode" / "opencode.jsonc"
        project_file.parent.mkdir(parents=True)
        project_file.write_text(
            '{\n  // keep this operator comment\n  "theme": "dark",\n  "instructions": [\n    "/operator/rules.md", // keep this entry\n  ],\n}\n',
            encoding="utf-8",
        )

        result = self.run_installer("link", "--project", str(project_dir))

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(project_file.exists())
        self.assertFalse((project_dir / ".opencode" / "opencode.json").exists())
        content = project_file.read_text(encoding="utf-8")
        self.assertIn("keep this operator comment", content)
        self.assertIn("/operator/rules.md", content)
        self.assertIn(str(self.root / "data" / "concord" / "current" / "instructions" / "*.md"), content)

    def test_link_ignores_instruction_key_decoys_in_jsonc_values_and_arrays(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer"
        project_file = project_dir / ".opencode" / "opencode.jsonc"
        project_file.parent.mkdir(parents=True)
        project_file.write_text(
            '{\n  "description": "https://example.test//instructions",\n  "comment-text": "/* not a comment */",\n  "decoy": [],\n  "instructions": [\n    "/operator/rules.md"\n  ]\n}\n',
            encoding="utf-8",
        )

        result = self.run_installer("link", "--project", str(project_dir))

        self.assertEqual(result.returncode, 0, result.stderr)
        parsed = installer.jsonc_data(project_file.read_text(encoding="utf-8"))
        self.assertIsInstance(parsed, dict)
        self.assertEqual(parsed["decoy"], [])
        self.assertIn(str(self.root / "data" / "concord" / "current" / "instructions" / "*.md"), parsed["instructions"])

    def test_link_skips_comments_between_instruction_key_and_colon(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer-comments"
        project_file = project_dir / ".opencode" / "opencode.jsonc"
        project_file.parent.mkdir(parents=True)
        project_file.write_text(
            '{\n  "instructions" // comment one\n  /* comment two */ : [\n    "/operator/rules.md"\n  ]\n}\n',
            encoding="utf-8",
        )

        result = self.run_installer("link", "--project", str(project_dir))

        self.assertEqual(result.returncode, 0, result.stderr)
        parsed = installer.jsonc_data(project_file.read_text(encoding="utf-8"))
        self.assertIn(str(self.root / "data" / "concord" / "current" / "instructions" / "*.md"), parsed["instructions"])

    def test_link_handles_empty_whitespace_and_trailing_comma_instruction_arrays(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        cases = (
            ("opencode.json", '{\n  "instructions": []\n}\n'),
            ("opencode.jsonc", '{\n  "instructions": [   ]\n}\n'),
            ("opencode.jsonc", '{\n  "instructions": [\n    "/operator/rules.md",\n  ],\n}\n'),
            ("opencode.jsonc", '{\n  "theme": "dark",\n}\n'),
        )
        expected_entry = str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")
        for index, (name, source) in enumerate(cases):
            with self.subTest(name=name, index=index):
                project_dir = self.root / f"consumer-{index}"
                project_file = project_dir / ".opencode" / name
                project_file.parent.mkdir(parents=True)
                project_file.write_text(source, encoding="utf-8")

                result = self.run_installer("link", "--project", str(project_dir))

                self.assertEqual(result.returncode, 0, result.stderr)
                parsed = json.loads(project_file.read_text(encoding="utf-8")) if name == "opencode.json" else installer.jsonc_data(project_file.read_text(encoding="utf-8"))
                self.assertIn(expected_entry, parsed["instructions"])

    def test_uninstall_preserves_a_preexisting_identical_worktree_config(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        config = worktree / ".opencode" / "opencode.json"
        config.parent.mkdir(parents=True)
        entry = str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")
        original = json.dumps({"instructions": [entry]}, indent=2) + "\n"
        config.write_text(original, encoding="utf-8")

        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(config.read_text(encoding="utf-8"), original)

    def test_uninstall_restores_a_preexisting_jsonc_worktree_config_exactly(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        config = worktree / ".opencode" / "opencode.jsonc"
        config.parent.mkdir(parents=True)
        original = '{\n  // operator-owned comment\n  "theme": "dark",\n}\n'
        config.write_text(original, encoding="utf-8")

        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(config.read_text(encoding="utf-8"), original)

    def test_uninstall_refuses_a_worktree_config_that_lost_the_conduct_entry(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        config = worktree / ".opencode" / "opencode.json"
        config.parent.mkdir(parents=True)
        config.write_text('{"theme": "dark"}\n', encoding="utf-8")

        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        config.write_text('{"theme": "light"}\n', encoding="utf-8")

        removed = self.run_installer("uninstall")

        self.assertNotEqual(removed.returncode, 0)
        self.assertIn("user-modified", removed.stderr)
        self.assertTrue((self.root / "data" / "concord" / "current").is_symlink())

    def test_unlink_preserves_existing_config_and_removes_created_config(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        existing = self.root / "existing-project" / ".opencode" / "opencode.json"
        existing.parent.mkdir(parents=True)
        original = '{\n  "keep": true\n}\n'
        existing.write_text(original, encoding="utf-8")
        created_project = self.root / "created-project"

        self.assertEqual(self.run_installer("link", "--project", str(self.root / "existing-project")).returncode, 0)
        self.assertEqual(self.run_installer("link", "--project", str(created_project)).returncode, 0)
        self.assertEqual(self.run_installer("unlink", "--project", str(self.root / "existing-project")).returncode, 0)
        self.assertEqual(self.run_installer("unlink", "--project", str(created_project)).returncode, 0)

        self.assertEqual(existing.read_text(encoding="utf-8"), original)
        self.assertFalse((created_project / ".opencode").exists())

    def test_uninstall_recovers_worktree_config_write_before_ownership_update(self) -> None:
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        config = worktree / ".opencode" / "opencode.json"
        before = config.read_text(encoding="utf-8")
        config.unlink()
        pending = self.root / "data" / "concord" / installer.PROJECT_LINK_PENDING_NAME
        pending.write_text(
            json.dumps(
                {"schema": 1, "links": {str(config.resolve()): {
                    "scope": "worktree",
                    "action": "remove",
                    "before": before,
                    "original": None,
                    "updated": "",
                }}}
            ) + "\n",
            encoding="utf-8",
        )

        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertFalse(config.exists())
        self.assertFalse(pending.exists())

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

    def test_numbered_primary_agents_survive_install_upgrade_repair_and_uninstall(self) -> None:
        agents_dir = self.root / "config" / "opencode" / "agents"
        agents_dir.mkdir(parents=True)
        primary_agents = {
            "concord-0.md": b"operator intake\n",
            "concord-1.md": b"operator shaping\n",
            "concord-2.md": b"operator driving\n",
        }
        for name, content in primary_agents.items():
            (agents_dir / name).write_bytes(content)

        def assert_primary_agents_unchanged() -> None:
            for name, content in primary_agents.items():
                self.assertEqual((agents_dir / name).read_bytes(), content)

        self.make_release("v1.0.0", marker="old")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        assert_primary_agents_unchanged()

        managed_worker = agents_dir / installer.AGENT_FILES[0]
        self.assertEqual(managed_worker.read_text(encoding="utf-8"), f"agent:{installer.AGENT_FILES[0]}:old\n")

        self.make_release("v2.0.0", marker="new")
        upgraded = self.run_installer("install", "--version", "v2.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(upgraded.returncode, 0, upgraded.stderr)
        assert_primary_agents_unchanged()
        self.assertEqual(managed_worker.read_text(encoding="utf-8"), f"agent:{installer.AGENT_FILES[0]}:new\n")

        managed_worker.unlink()
        repaired = self.run_installer("repair", "--version", "v2.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(repaired.returncode, 0, repaired.stderr)
        assert_primary_agents_unchanged()
        self.assertEqual(managed_worker.read_text(encoding="utf-8"), f"agent:{installer.AGENT_FILES[0]}:new\n")

        manifest = json.loads((self.root / "data" / "concord" / installer.MANIFEST_NAME).read_text(encoding="utf-8"))
        self.assertEqual(set(manifest["agent_files"]), set(installer.AGENT_FILES))
        self.assertTrue(set(primary_agents).isdisjoint(manifest["agent_files"]))

        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        assert_primary_agents_unchanged()
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

    def test_install_links_each_existing_worktree_to_the_conduct_corpus(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)

        worktrees_config = worktree / ".opencode" / "opencode.json"
        self.assertEqual(
            json.loads(worktrees_config.read_text(encoding="utf-8")),
            {"instructions": [str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")]},
        )

    def test_install_recovers_a_worktree_config_written_before_ownership(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        config = worktree / ".opencode" / "opencode.json"
        config.parent.mkdir(parents=True)
        updated = json.dumps(
            {"instructions": [str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")]},
            indent=2,
        ) + "\n"
        config.write_text(updated, encoding="utf-8")
        pending = self.root / "data" / "concord" / installer.PROJECT_LINK_PENDING_NAME
        pending.write_text(
            json.dumps({"schema": 1, "links": {str(config.resolve()): {"scope": "worktree", "action": "remove", "before": None, "original": None, "updated": updated}}}) + "\n",
            encoding="utf-8",
        )

        recovered = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        ownership = json.loads((self.root / "data" / "concord" / installer.PROJECT_LINK_OWNERSHIP_NAME).read_text(encoding="utf-8"))
        self.assertEqual(ownership["links"][str(config.resolve())]["action"], "remove")
        self.assertFalse(pending.exists())

    def test_link_recovers_a_project_config_written_before_ownership(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        project_dir = self.root / "consumer-recovery"
        config = project_dir / ".opencode" / "opencode.json"
        config.parent.mkdir(parents=True)
        updated = json.dumps(
            {"keep": True, "instructions": [str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")]},
            indent=2,
        ) + "\n"
        config.write_text(updated, encoding="utf-8")
        pending = self.root / "data" / "concord" / installer.PROJECT_LINK_PENDING_NAME
        pending.write_text(
            json.dumps({"schema": 1, "links": {str(config.resolve()): {"scope": "project", "action": "remove", "before": None, "original": None, "updated": updated}}}) + "\n",
            encoding="utf-8",
        )

        recovered = self.run_installer("link", "--project", str(project_dir))

        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        ownership = json.loads((self.root / "data" / "concord" / installer.PROJECT_LINK_OWNERSHIP_NAME).read_text(encoding="utf-8"))
        self.assertEqual(ownership["links"][str(config.resolve())]["scope"], "project")
        self.assertFalse(pending.exists())

    def test_install_recovers_an_interrupted_legacy_config_deletion(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        legacy = self.root / "data" / "concord" / "worktrees" / ".opencode" / "opencode.json"
        original = json.dumps(
            {"instructions": [str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")]},
            indent=2,
        ) + "\n"
        legacy.parent.mkdir(parents=True)
        legacy.write_text(original, encoding="utf-8")
        legacy.unlink()
        pending = self.root / "data" / "concord" / installer.PROJECT_LINK_PENDING_NAME
        pending.write_text(
            json.dumps({"schema": 1, "links": {str(legacy.resolve()): {"scope": "legacy", "action": "remove", "before": original, "original": None, "updated": ""}}}) + "\n",
            encoding="utf-8",
        )

        recovered = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        ownership = json.loads((self.root / "data" / "concord" / installer.PROJECT_LINK_OWNERSHIP_NAME).read_text(encoding="utf-8"))
        self.assertEqual(ownership["links"][str(legacy.resolve())]["expected"], {"exists": False})
        self.assertFalse(pending.exists())

    def test_legacy_ancestor_registration_is_removed_without_restore_on_uninstall(self) -> None:
        self.make_release("v1.0.0")
        legacy = self.root / "data" / "concord" / "worktrees" / ".opencode" / "opencode.json"
        entry = str(self.root / "data" / "concord" / "current" / "instructions" / "*.md")
        legacy.parent.mkdir(parents=True)
        legacy.write_text(json.dumps({"keep": True, "instructions": [entry]}, indent=2) + "\n", encoding="utf-8")

        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))

        self.assertEqual(installed.returncode, 0, installed.stderr)
        self.assertEqual(json.loads(legacy.read_text(encoding="utf-8")), {"keep": True})
        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(json.loads(legacy.read_text(encoding="utf-8")), {"keep": True})
        self.assertNotIn(entry, legacy.read_text(encoding="utf-8"))

    def test_worktree_links_preserve_tracked_and_untracked_config_state(self) -> None:
        self.make_release("v1.0.0")
        tracked = self.root / "data" / "concord" / "worktrees" / "tracked" / "work"
        untracked = self.root / "data" / "concord" / "worktrees" / "untracked" / "work"
        tracked.mkdir(parents=True)
        untracked.mkdir(parents=True)
        for worktree in (tracked, untracked):
            self.assertEqual(self.run_real_git(worktree, "init", "--quiet").returncode, 0)
        tracked_config = tracked / ".opencode" / "opencode.json"
        tracked_config.parent.mkdir()
        tracked_original = '{\n  "keep": true\n}\n'
        tracked_config.write_text(tracked_original, encoding="utf-8")
        self.assertEqual(self.run_real_git(tracked, "add", ".opencode/opencode.json").returncode, 0)
        self.assertEqual(
            self.run_real_git(
                tracked,
                "-c",
                "user.name=Concord Test",
                "-c",
                "user.email=concord-test@example.invalid",
                "commit",
                "--quiet",
                "-m",
                "initial",
            ).returncode,
            0,
        )

        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        self.assertIn(" M .opencode/opencode.json", self.run_real_git(tracked, "status", "--short").stdout)
        self.assertIn("?? .opencode/", self.run_real_git(untracked, "status", "--short").stdout)

        removed = self.run_installer("uninstall")
        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertEqual(tracked_config.read_text(encoding="utf-8"), tracked_original)
        self.assertEqual(self.run_real_git(tracked, "status", "--short").stdout, "")
        self.assertEqual(self.run_real_git(untracked, "status", "--short").stdout, "")

    def test_uninstall_unlinks_worktree_pointers_and_preserves_worktrees(self) -> None:
        self.make_release("v1.0.0")
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        (worktree / "operator-note.txt").write_text("keep\n", encoding="utf-8")

        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertFalse((worktree / ".opencode").exists())
        self.assertEqual((worktree / "operator-note.txt").read_text(encoding="utf-8"), "keep\n")

    def test_uninstall_preserves_jsonc_comments_in_worktree_config(self) -> None:
        self.make_release("v1.0.0")
        worktree = self.root / "data" / "concord" / "worktrees" / "project" / "work"
        worktree.mkdir(parents=True)
        config = worktree / ".opencode" / "opencode.jsonc"
        config.parent.mkdir(parents=True)
        config.write_text(
            '{\n  // operator-owned comment\n  "theme": "dark",\n  "instructions": [\n    "/operator/rules.md",\n  ],\n}\n',
            encoding="utf-8",
        )
        installed = self.run_installer("install", "--version", "v1.0.0", "--artifact-dir", str(self.artifacts))
        self.assertEqual(installed.returncode, 0, installed.stderr)
        linked = config.read_text(encoding="utf-8")
        self.assertIn(str(self.root / "data" / "concord" / "current" / "instructions" / "*.md"), linked)

        removed = self.run_installer("uninstall")

        self.assertEqual(removed.returncode, 0, removed.stderr)
        restored = config.read_text(encoding="utf-8")
        self.assertIn("operator-owned comment", restored)
        self.assertIn("/operator/rules.md", restored)
        self.assertNotIn("current/instructions", restored)

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


    def test_install_without_version_picks_highest_artifact_dir_release(self) -> None:
        self.make_release("v7.10.1")
        self.make_release("v7.10.2")
        self.make_release("v7.9.0")
        result = self.run_installer("install", "--artifact-dir", str(self.artifacts))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Installed Concord v7.10.2", result.stdout)

    def test_install_without_version_refuses_when_no_release_resolves(self) -> None:
        result = self.run_installer("install", "--artifact-dir", str(self.artifacts))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--version", result.stderr)

    def test_pinned_install_passes_the_tag_download_base_to_artifact_fetch(self) -> None:
        base_url = "https://github.com/Sharper-Flow/concord/releases/latest/download"
        args = SimpleNamespace(
            version="v7.10.2", artifact_dir=None, base_url=base_url, root=self.root
        )
        with mock.patch.dict(os.environ, self.env, clear=True), mock.patch.object(
            installer, "extract_verified_artifact", side_effect=installer.InstallerError("stop after routing")
        ) as extract:
            with self.assertRaises(installer.InstallerError):
                installer.install(args)
        self.assertEqual(
            extract.call_args.args[2],
            "https://github.com/Sharper-Flow/concord/releases/download/v7.10.2",
        )

    def test_unpinned_install_passes_the_latest_download_base_to_artifact_fetch(self) -> None:
        base_url = "https://github.com/Sharper-Flow/concord/releases/latest/download"
        args = SimpleNamespace(version=None, artifact_dir=None, base_url=base_url, root=self.root)
        with mock.patch.dict(os.environ, self.env, clear=True), mock.patch.object(
            installer, "resolve_latest_version", return_value="v7.10.2"
        ), mock.patch.object(
            installer, "extract_verified_artifact", side_effect=installer.InstallerError("stop after routing")
        ) as extract:
            with self.assertRaises(installer.InstallerError):
                installer.install(args)
        self.assertEqual(extract.call_args.args[2], base_url)

    def test_install_extracts_without_deprecation_warning(self) -> None:
        self.make_release("v7.10.2")
        environment = self.env.copy()
        environment["PYTHONWARNINGS"] = "error::DeprecationWarning"
        result = self.run_installer(
            "install", "--version", "v7.10.2", "--artifact-dir", str(self.artifacts), env=environment
        )
        self.assertEqual(result.returncode, 0, result.stderr)


    # ---- CON-807 review probes: quoting, forward recovery, cleanup races --

    def test_the_recorded_activation_command_preserves_argument_boundaries(self) -> None:
        """A recorded command with a space in a path must split back into the
        exact arguments: shlex round-trips the recording (CON-807)."""
        with mock.patch.object(sys, "argv", ["/tmp/installer tools/install.py"]):
            command = installer.activation_command_for("v1.1.0", Path("/tmp/test root"))
        self.assertEqual(
            shlex.split(command),
            [sys.executable, "/tmp/installer tools/install.py", "activate", "--version", "v1.1.0", "--root", "/tmp/test root"],
        )

    def test_a_reinstall_after_the_migration_recovers_forward(self) -> None:
        """An install that activates its own prepared candidate after the
        migration committed is a prepared activation: a crash mid-swap
        recovers the candidate, never the older release the migration made
        unusable (CON-807 candidate-forward recovery)."""
        self.prepare_a_blocked_release()
        environment = self.plan_env(blocked=False)
        environment["CONCORD_INSTALLER_STOP_AFTER_PHASE"] = "launcher_swapped"
        stopped = self.run_installer("install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts), env=environment)
        self.assertEqual(stopped.returncode, 97, stopped.stderr)
        recovered = self.run_installer("status", env=self.plan_env(blocked=False))
        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")
        self.assertFalse(self.prepared_path.exists(), "the recovered activation kept its prepared record")

    def test_an_intra_swap_crash_resumes_inside_the_rename_phase(self) -> None:
        """A crash after the version placement but before the journal advance
        resumes forward inside the same phase instead of refusing on the
        surviving live-version backup (CON-807 idempotent rename phases)."""
        self.prepare_a_blocked_release()
        original = installer.apply_stable_root

        def crash(*args: object) -> None:
            original(*args)
            raise RuntimeError("synthetic crash after version placement before journal advance")

        with mock.patch.dict(os.environ, self.plan_env(blocked=False), clear=True):
            with mock.patch.object(installer, "apply_stable_root", side_effect=crash):
                with self.assertRaises(RuntimeError):
                    installer.activate(SimpleNamespace(root=self.root, version="v1.1.0"))
        recovered = self.run_installer("status", env=self.plan_env(blocked=False))
        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")

    def test_cleanup_never_deletes_a_release_admitted_after_the_snapshot(self) -> None:
        """A lease snapshot alone never authorizes a deletion (CON-807): a
        session admitted after the observation keeps its release, because
        cleanup re-checks the shared admission record at deletion time."""
        self.install_release("v1.0.0")
        paths = installer.paths_for(self.root)
        manifest = installer.load_manifest(paths)
        root = paths.data_root / "v1.0.0"
        leases = paths.data_root / "hosts"
        journal = {
            "operation": "install",
            "new_version": "v1.1.0",
            "cleanup_version": "v1.0.0",
            "cleanup_candidates": {"v1.0.0": manifest["version_files"]},
        }
        original = installer.version_matches

        def admit_after_snapshot(*args: object, **kwargs: object) -> bool:
            leases.mkdir(exist_ok=True)
            (leases / "holder.json").write_text(json.dumps({"pid": os.getpid(), "release_root": str(root)}))
            return original(*args, **kwargs)

        with mock.patch.object(installer, "observe_held_releases", return_value={}):
            with mock.patch.object(installer, "version_matches", side_effect=admit_after_snapshot):
                installer.remove_unheld_releases(journal, paths)
        self.assertTrue(root.exists(), "cleanup deleted the release admitted after the snapshot")

    def test_the_recorded_migration_command_preserves_argument_boundaries(self) -> None:
        """The migration command the prepared record carries must split back
        into the exact staged-binary arguments, even under a data root with
        a space, and must pin the environment the plan inspected (CON-807)."""
        paths = installer.paths_for(Path("/tmp/synthetic test root"))
        command = installer.plan_migration_command(paths, "v1.1.0")
        self.assertEqual(
            shlex.split(command),
            [
                "env",
                "-u",
                "CONCORD_DB_PATH",
                f"XDG_DATA_HOME={paths.data_home}",
                str(paths.data_root / "v1.1.0/bin/concord"),
                "upgrade",
            ],
        )

    def test_the_recorded_migration_command_ignores_the_operator_shell_store(self) -> None:
        """An operator shell that names another store must not redirect the
        recorded migration: the command reaches the store the plan read."""
        self.prepare_a_blocked_release()
        record = json.loads(self.prepared_path.read_text(encoding="utf-8"))
        probe = self.root / "probe-env.sh"
        probe.write_text('#!/bin/sh\nprintf "%s|%s" "${CONCORD_DB_PATH-unset}" "$XDG_DATA_HOME"\n', encoding="utf-8")
        probe.chmod(0o755)
        argv = shlex.split(record["migration_command"])
        self.assertEqual(argv[-2:], [str(self.data_root / "v1.1.0" / "bin" / "concord"), "upgrade"])
        argv[-2:] = [str(probe)]
        ambient = dict(os.environ, CONCORD_DB_PATH=str(self.root / "other" / "concord.db"), XDG_DATA_HOME=str(self.root / "other"))
        result = subprocess.run(argv, capture_output=True, text=True, env=ambient, check=True)
        self.assertEqual(result.stdout, f"unset|{installer.paths_for(self.root).data_home}")

    def test_the_fence_serializes_with_session_admission(self) -> None:
        """Opening and removing the maintenance fence both wait for the
        shared admission lock, so a session mid-admission can never land on
        the wrong side of a boundary change (CON-807)."""
        paths = installer.paths_for(self.root)
        paths.data_root.mkdir(parents=True, exist_ok=True)
        for phase, operate in (
            ("open", lambda: installer.ensure_maintenance_fence(paths, "synthetic")),
            (
                "remove",
                lambda: installer.remove_maintenance_fence(
                    paths, installer.ensure_maintenance_fence(paths, "synthetic")["fence_id"]
                ),
            ),
        ):
            with self.subTest(phase=phase):
                with (paths.data_root / "admission.lock").open("a+b") as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX)
                    read_fd, write_fd = os.pipe()
                    pid = os.fork()
                    if pid == 0:
                        os.close(read_fd)
                        lock.close()
                        try:
                            operate()
                            os.write(write_fd, b"done")
                        finally:
                            os._exit(0)
                    os.close(write_fd)
                    readable, _, _ = select.select([read_fd], [], [], 2)
                    bypassed = bool(readable)
                    fcntl.flock(lock, fcntl.LOCK_UN)
                    os.waitpid(pid, 0)
                    os.close(read_fd)
                self.assertFalse(bypassed, f"the fence {phase} bypassed a held admission lock")

    def test_cleanup_retains_releases_written_by_an_unfenceable_participant(self) -> None:
        """An admission record that lands inside the held exclusion was
        written without the shared lock: an unfenceable participant. Every
        candidate stays, including the ones after the landing (CON-807)."""
        self.install_release("v1.0.0")
        report = self.root / "held-report.json"
        report.write_text(json.dumps({"leases": [{"pid": os.getpid(), "release_root": str(self.data_root / "v1.0.0")}]}))
        env = self.plan_env(blocked=False)
        env["CONCORD_TEST_HOST_LEASES"] = str(report)
        result = self.install_release("v1.0.1", env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        paths = installer.paths_for(self.root)
        manifest = installer.load_manifest(paths)
        hosts = paths.data_root / "hosts"
        journal = {
            "operation": "install",
            "new_version": "v1.2.0",
            "cleanup_candidates": {
                "v1.0.0": manifest["retained_releases"]["v1.0.0"],
                "v1.0.1": manifest["version_files"],
            },
        }
        original = installer.admitted_release_roots

        def write_without_the_lock(*args: object) -> set[str]:
            snapshot = original(*args)
            hosts.mkdir(exist_ok=True)
            (hosts / "holder.json").write_text(json.dumps({"pid": os.getpid(), "release_root": str(paths.data_root / "v1.0.1")}))
            return snapshot

        with mock.patch.object(installer, "observe_held_releases", return_value={}):
            with mock.patch.object(installer, "admitted_release_roots", side_effect=write_without_the_lock):
                removed = installer.remove_unheld_releases(journal, paths)
        self.assertEqual(removed, set(), "an unfenceable participant authorized deletions")
        self.assertTrue((paths.data_root / "v1.0.0").exists(), "the first candidate was deleted beside an unfenceable write")
        self.assertTrue((paths.data_root / "v1.0.1").exists(), "the unfenceable participant's release was deleted")
        self.assertTrue((hosts / "holder.json").exists())

    def test_a_compliant_concurrent_admission_serializes_with_cleanup(self) -> None:
        """A compliant admission — one that lands its lease while holding the
        shared admission lock — cannot lose its release to a concurrent
        cleanup, because cleanup holds the same exclusion across its final
        observation and every deletion (CON-807)."""
        self.install_release("v1.0.0")
        paths = installer.paths_for(self.root)
        manifest = installer.load_manifest(paths)
        root = paths.data_root / "v1.0.0"
        hosts = paths.data_root / "hosts"
        journal = {
            "operation": "install",
            "new_version": "v1.1.0",
            "cleanup_candidates": {"v1.0.0": manifest["version_files"]},
        }
        read_fd, write_fd = os.pipe()
        pid = os.fork()
        if pid == 0:
            os.close(read_fd)
            try:
                lock = (paths.data_root / "admission.lock").open("a+b")
                fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
                hosts.mkdir(exist_ok=True)
                (hosts / "holder.json").write_text(json.dumps({"pid": os.getpid(), "release_root": str(root)}))
                os.write(write_fd, b"held")
                time.sleep(1.0)
                fcntl.flock(lock.fileno(), fcntl.LOCK_UN)
            finally:
                os._exit(0)
        os.close(write_fd)
        try:
            readable, _, _ = select.select([read_fd], [], [], 5)
            self.assertTrue(readable, "the concurrent admission never held the exclusion")
            removed = installer.remove_unheld_releases(journal, paths)
        finally:
            os.waitpid(pid, 0)
            os.close(read_fd)
        self.assertEqual(removed, set(), "cleanup deleted the release of a compliantly admitted session")
        self.assertTrue(root.exists(), "a compliant concurrent admission lost its release to cleanup")

    def test_cleanup_fails_closed_on_unreadable_admission_records(self) -> None:
        """An admission record the cleanup cannot parse names an unknown
        participant set: nothing is provably unreferenced and every
        candidate stays (CON-807 fail-closed rule)."""
        self.install_release("v1.0.0")
        paths = installer.paths_for(self.root)
        manifest = installer.load_manifest(paths)
        root = paths.data_root / "v1.0.0"
        journal = {
            "operation": "install",
            "new_version": "v1.1.0",
            "cleanup_candidates": {"v1.0.0": manifest["version_files"]},
        }
        hosts = paths.data_root / "hosts"
        for name, content in (
            ("broken.json", "{not json"),
            ("noroot.json", json.dumps({"pid": os.getpid()})),
        ):
            with self.subTest(record=name):
                hosts.mkdir(exist_ok=True)
                (hosts / name).write_text(content)
                removed = installer.remove_unheld_releases(journal, paths)
                self.assertEqual(removed, set(), f"an unreadable record ({name}) authorized a deletion")
                self.assertTrue(root.exists(), f"an unreadable record ({name}) lost its release")
                (hosts / name).unlink()

    def test_a_same_version_reinstall_never_deletes_an_admitted_release(self) -> None:
        """Reinstalling the active version retries the retained-release
        cleanup through the same held exclusion: a release admitted around
        the retry is never deleted on its snapshot (CON-807)."""
        self.install_release("v1.0.0")
        report = self.root / "held-report.json"
        old_root = self.data_root / "v1.0.0"
        report.write_text(json.dumps({"leases": [{"pid": os.getpid(), "release_root": str(old_root)}]}))
        env = self.plan_env(blocked=False)
        env["CONCORD_TEST_HOST_LEASES"] = str(report)
        result = self.install_release("v1.1.0", env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(old_root.exists())
        paths = installer.paths_for(self.root)
        original = installer.version_matches

        def admit_after_snapshot(*args: object, **kwargs: object) -> bool:
            hosts = paths.data_root / "hosts"
            hosts.mkdir(exist_ok=True)
            (hosts / "holder.json").write_text(json.dumps({"pid": os.getpid(), "release_root": str(old_root)}))
            return original(*args, **kwargs)

        args = SimpleNamespace(root=self.root, version="v1.1.0", artifact_dir=str(self.artifacts), base_url="unused")
        with mock.patch.dict(os.environ, self.env, clear=True):
            with mock.patch.object(installer, "observe_held_releases", return_value={}):
                with mock.patch.object(installer, "version_matches", side_effect=admit_after_snapshot):
                    installer.install(args)
        self.assertTrue(old_root.exists(), "a same-version reinstall deleted a release admitted around its snapshot")

    def test_superseding_a_migrated_prepared_candidate_recovers_forward(self) -> None:
        """Installing a different version while the prepared candidate's
        maintenance boundary is open must recover forward on a crash: the
        migration may have committed, so the pre-migration release is not a
        provably usable rollback target (CON-807). The fence carries the
        release attribution the prepared candidate's own migration command
        writes (hostlease.EnsureFence stamps the migrating binary's root),
        which is what makes the boundary provably this upgrade path's."""
        self.prepare_a_blocked_release()
        self.fence_path.write_text(json.dumps({
            "fence_id": "synthetic-committed-boundary",
            "operation": installer.CORE_UPGRADE_OPERATION,
            "release_root": str(self.data_root / "v1.1.0"),
            "notice": "session admission reopens when the prepared release activates",
        }))
        self.make_release("v1.2.0")
        env = self.plan_env(blocked=False)
        env["CONCORD_INSTALLER_STOP_AFTER_PHASE"] = "launcher_swapped"
        stopped = self.run_installer("install", "--version", "v1.2.0", "--artifact-dir", str(self.artifacts), env=env)
        self.assertEqual(stopped.returncode, 97, stopped.stderr)
        recovered = self.run_installer("status", env=self.plan_env(blocked=False))
        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        active = os.readlink(self.data_root / "current")
        self.assertNotEqual(active, str(self.data_root / "v1.0.0"), "a superseding install restored the unusable pre-migration release")
        self.assertEqual(active, str(self.data_root / "v1.2.0"))
        self.assertFalse(self.prepared_path.exists(), "the superseded prepared record survived")
        self.assertFalse(self.fence_path.exists(), "the committed boundary stayed open after recovery")

    def test_a_cleanup_crash_leaves_a_status_path_to_discharge_the_boundary(self) -> None:
        """A crash after the cleanup removed the transaction journal but
        before the boundary discharged leaves a recoverable state: the next
        status invocation completes the final crash phase by closing the
        fence the activation adopted (CON-807)."""
        self.prepare_a_blocked_release()
        original = installer.cleanup_transaction

        def crash_after_cleanup(*args: object, **kwargs: object) -> None:
            original(*args, **kwargs)
            raise RuntimeError("synthetic crash after cleanup before boundary discharge")

        with mock.patch.dict(os.environ, self.plan_env(blocked=False), clear=True):
            with mock.patch.object(installer, "cleanup_transaction", side_effect=crash_after_cleanup):
                with self.assertRaises(RuntimeError):
                    installer.activate(SimpleNamespace(root=self.root, version="v1.1.0"))
        recovered = self.run_installer("status", env=self.plan_env(blocked=False))
        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        self.assertFalse(self.fence_path.exists(), "status has no path left to close the committed activation boundary")
        self.assertFalse(self.prepared_path.exists(), "the discharged record survived")

    def test_status_never_closes_a_foreign_fence(self) -> None:
        """A fence whose identity no prepared record adopted belongs to
        another operation: a committed activation's discharge clears its
        record but leaves that fence untouched (CON-807)."""
        self.prepare_a_blocked_release()
        result = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertEqual(result.returncode, 0, result.stderr)
        foreign = {"fence_id": "another-operations-boundary", "notice": "synthetic foreign fence"}
        self.fence_path.write_text(json.dumps(foreign))
        record = json.loads(self.prepared_path.read_text(encoding="utf-8")) if self.prepared_path.exists() else None
        self.assertIsNone(record, "the discharged record reappeared")
        # Rebuild the committed-activation state by hand: the manifest names
        # v1.1.0 and a prepared record for it exists with no adopted fence.
        synthetic = {
            "schema": installer.PREPARED_RELEASE_SCHEMA,
            "version": "v1.1.0",
            "version_files": json.loads((self.data_root / "install-manifest.json").read_text(encoding="utf-8"))["version_files"],
            "blockers": ["synthetic committed record"],
            "migration_command": "concord upgrade",
            "activation_command": "concord activate",
            "created_at": "2026-01-01T00:00:00Z",
        }
        self.prepared_path.write_text(json.dumps(synthetic), encoding="utf-8")
        recovered = self.run_installer("status", env=self.plan_env(blocked=False))
        self.assertEqual(recovered.returncode, 0, recovered.stderr)
        self.assertFalse(self.prepared_path.exists(), "the committed record was not discharged")
        self.assertTrue(self.fence_path.exists(), "status removed a fence no record adopted")
        self.assertEqual(json.loads(self.fence_path.read_text(encoding="utf-8"))["fence_id"], foreign["fence_id"])

    def test_a_compatible_install_retains_an_unowned_open_fence(self) -> None:
        """CON-807: an open maintenance boundary with no prepared release
        owning its upgrade path belongs to another operation or is an
        orphan. A compatible install must neither adopt it nor close it:
        the install refuses, the boundary survives byte-for-byte, and an
        orphan closes only through the operator-owned offline bootstrap."""
        self.install_release("v1.0.0")
        foreign = {"fence_id": "foreign-in-progress", "notice": "another operation holds this boundary"}
        self.fence_path.write_text(json.dumps(foreign))
        result = self.install_release("v1.1.0", env=self.plan_env(blocked=False))
        self.assertNotEqual(result.returncode, 0, "a compatible install ran over an unowned boundary")
        self.assertTrue(self.fence_path.exists(), "the install closed another operation's boundary")
        self.assertEqual(json.loads(self.fence_path.read_text(encoding="utf-8"))["fence_id"], foreign["fence_id"])
        self.assert_active_release("v1.0.0", "v1.0.0")

    def test_a_prepared_activation_refuses_a_fence_attributed_to_another_candidate(self) -> None:
        """CON-807: a boundary attributed to a different release root serves
        another candidate's upgrade path. The prepared activation refuses
        rather than adopt an exclusion it may never close, and the foreign
        boundary is retained exactly as written."""
        self.prepare_a_blocked_release()
        foreign = {"fence_id": "foreign-in-progress", "release_root": "/other/candidate"}
        self.fence_path.write_text(json.dumps(foreign))
        result = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertNotEqual(result.returncode, 0, "the activation adopted another candidate's boundary")
        self.assertTrue(self.fence_path.exists(), "the activation closed another operation's boundary")
        self.assertEqual(json.loads(self.fence_path.read_text(encoding="utf-8"))["fence_id"], foreign["fence_id"])
        self.assert_active_release("v1.0.0", "v1.0.0")

    def test_a_superseding_cleanup_crash_keeps_a_recoverable_boundary_owner(self) -> None:
        """CON-807: cleanup removes the transaction journal inside the same
        call that finishes the transaction, so the durable owner of an
        adopted boundary is the prepared record, written before the
        transaction starts. A crash after cleanup still recovers: the next
        status discharge closes the recorded boundary identity, and no
        journal-less, record-less fence is left behind. The fence carries
        the prepared candidate's own attribution, the shape its migration
        command writes."""
        self.prepare_a_blocked_release()
        self.fence_path.write_text(json.dumps({
            "fence_id": "synthetic-committed-boundary",
            "operation": installer.CORE_UPGRADE_OPERATION,
            "release_root": str(self.data_root / "v1.1.0"),
            "notice": "session admission reopens when the prepared release activates",
        }))
        self.make_release("v1.2.0")
        original = installer.cleanup_transaction

        def crash_after_cleanup(*args: object, **kwargs: object) -> None:
            original(*args, **kwargs)
            raise RuntimeError("synthetic crash after journal removal")

        with mock.patch.dict(os.environ, self.plan_env(blocked=False), clear=True):
            with mock.patch.object(installer, "cleanup_transaction", side_effect=crash_after_cleanup):
                with self.assertRaises(RuntimeError):
                    installer.install(SimpleNamespace(root=self.root, version="v1.2.0", artifact_dir=str(self.artifacts), base_url="unused"))
        # The adoption was recorded durably before the transaction: the
        # superseding release owns the discharge even with the journal gone.
        record = json.loads(self.prepared_path.read_text(encoding="utf-8"))
        self.assertEqual(record["boundary_fence_id"], "synthetic-committed-boundary")
        self.assertEqual(record["boundary_owner_version"], "v1.2.0")
        result = self.run_installer("status", env=self.plan_env(blocked=False))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.fence_path.exists(), "the journal's removal left no durable owner for the boundary")
        self.assertFalse(self.prepared_path.exists(), "the discharged record survived")
        self.assertEqual(os.readlink(self.data_root / "current"), str(self.data_root / "v1.2.0"))

    def test_a_legacy_admission_after_the_last_snapshot_is_retained(self) -> None:
        """CON-807: an unfenceable legacy participant takes no admission
        lock, so no names snapshot excludes it. The deletion gate re-reads
        the admission records by content immediately before the removal, and
        a lease that landed after the last snapshot keeps its release."""
        environment = self.plan_env(blocked=False)
        environment["CONCORD_TEST_CORE_DESCRIPTOR_FAIL"] = "1"
        self.install_release("v1.0.0", env=environment)
        paths = installer.paths_for(self.root)
        manifest = installer.load_manifest(paths)
        old_root = paths.data_root / "v1.0.0"
        self.assertFalse((old_root / "fence-protocol").exists(), "the fixture must be a known legacy core")
        journal = {"operation": "install", "new_version": "v1.1.0", "cleanup_candidates": {"v1.0.0": manifest["version_files"]}}
        original = installer.admission_record_names
        calls = {"count": 0}

        def inject_at_last_observation(*args: object) -> set[str] | None:
            names = original(*args)
            calls["count"] += 1
            # section_names, admitted_release_roots' names, names_now: the
            # lease lands after the last snapshot's listing, the exact seam
            # a legacy writer that holds no admission lock can use.
            if calls["count"] == 3:
                hosts = paths.data_root / "hosts"
                hosts.mkdir(exist_ok=True)
                pid = installer.os.getpid()
                start = int(Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[19])
                (hosts / "legacy.json").write_text(
                    json.dumps({"pid": pid, "pid_start": start, "release_root": str(old_root), "fence_protocol": 0})
                )
            return names

        with mock.patch.object(installer, "observe_held_releases", return_value={}):
            with mock.patch.object(installer, "admission_record_names", side_effect=inject_at_last_observation):
                installer.remove_unheld_releases(journal, paths)
        self.assertTrue(old_root.exists(), "an unfenceable lease landed after the last names snapshot and its release was deleted")

    def test_a_legacy_admission_at_delete_time_retains_the_release(self) -> None:
        """CON-807, promoted coordinator probe: a protocol-0 participant can
        land its lease at the instant of deletion — after any observation the
        section could make. No re-read closes that window, so cleanup never
        reaches the irreversible step while a tree that cannot honor the
        shared exclusion stays installed: the capability gate retains the
        candidate before any deletion begins."""
        environment = self.plan_env(blocked=False)
        environment["CONCORD_TEST_CORE_DESCRIPTOR_FAIL"] = "1"
        self.install_release("v1.0.0", env=environment)
        paths = installer.paths_for(self.root)
        manifest = installer.load_manifest(paths)
        old_root = paths.data_root / "v1.0.0"
        self.assertFalse((old_root / "fence-protocol").exists(), "the fixture must be a known legacy core")
        journal = {
            "operation": "install",
            "new_version": "v1.1.0",
            "cleanup_candidates": {"v1.0.0": manifest["version_files"]},
        }
        original = installer.shutil.rmtree
        attempted = {"deletions": 0}

        def admit_then_delete(root: object, *args: object, **kwargs: object) -> object:
            attempted["deletions"] += 1
            if Path(str(root)) == old_root:
                hosts = paths.data_root / "hosts"
                hosts.mkdir(exist_ok=True)
                pid = installer.os.getpid()
                start = int(Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[19])
                (hosts / "legacy.json").write_text(json.dumps({
                    "pid": pid,
                    "pid_start": start,
                    "release_root": str(old_root),
                    "fence_protocol": 0,
                }))
            return original(root, *args, **kwargs)

        with mock.patch.object(installer, "observe_held_releases", return_value={}):
            with mock.patch.object(installer.shutil, "rmtree", side_effect=admit_then_delete):
                removed = installer.remove_unheld_releases(journal, paths)
        self.assertEqual(removed, set(), "cleanup deleted beside a tree that cannot honor the exclusion")
        self.assertEqual(attempted["deletions"], 0, "cleanup reached the irreversible step while a legacy tree was installed")
        self.assertTrue(old_root.exists(), "the legacy-referenced release was deleted")

    def test_cleanup_resumes_once_the_unfenceable_tree_is_bootstrapped_away(self) -> None:
        """The capability gate is the offline bootstrap's counterpart: while
        an unmarked runnable tree stays installed, cleanup retains every
        candidate and names the tree; once the operator removes that tree —
        the documented bootstrap step — the same cleanup deletes the
        unreferenced candidate."""
        self.install_release("v1.0.0")
        legacy_environment = self.plan_env(blocked=False)
        legacy_environment["CONCORD_TEST_CORE_DESCRIPTOR_FAIL"] = "1"
        self.install_release("v1.0.1", env=legacy_environment)
        paths = installer.paths_for(self.root)
        manifest = installer.load_manifest(paths)
        old_root = paths.data_root / "v1.0.0"
        legacy_root = paths.data_root / "v1.0.1"
        self.assertFalse((legacy_root / "fence-protocol").exists(), "the fixture must be a known legacy core")
        journal = {
            "operation": "install",
            "new_version": "v1.0.1",
            "cleanup_candidates": {"v1.0.0": manifest["retained_releases"]["v1.0.0"]},
        }
        with mock.patch.object(installer, "observe_held_releases", return_value={}):
            removed = installer.remove_unheld_releases(journal, paths)
        self.assertEqual(removed, set(), "cleanup deleted beside an unmarked runnable tree")
        self.assertTrue(old_root.exists(), "the candidate was deleted while the gate should have retained it")
        # The operator-owned bootstrap: no session runs, the legacy tree goes.
        shutil.rmtree(legacy_root)
        with mock.patch.object(installer, "observe_held_releases", return_value={}):
            removed = installer.remove_unheld_releases(journal, paths)
        self.assertEqual(removed, {"v1.0.0"}, "cleanup did not resume after the bootstrap removed the unfenceable tree")
        self.assertFalse(old_root.exists(), "the unreferenced candidate survived a fenceable store")

    def test_a_prepared_activation_retains_an_unattributed_foreign_fence(self) -> None:
        """CON-807, promoted coordinator probe: an open boundary carrying no
        release attribution is not owned because a prepared record happens to
        exist. The prepared activation refuses rather than adopt it, and the
        unattributed boundary survives byte-for-byte for the operator's
        offline bootstrap."""
        self.prepare_a_blocked_release()
        foreign = {"fence_id": "unattributed-foreign-operation", "notice": "an unattributed boundary"}
        self.fence_path.write_text(json.dumps(foreign))
        result = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertNotEqual(result.returncode, 0, "the activation adopted an unattributed boundary")
        self.assertTrue(self.fence_path.exists(), "the activation removed an unattributed boundary")
        self.assertEqual(
            json.loads(self.fence_path.read_text(encoding="utf-8")),
            foreign,
            "the retained boundary changed",
        )
        self.assert_active_release("v1.0.0", "v1.0.0")
        self.assertTrue(self.prepared_path.exists(), "a refused activation discharged the prepared record")

    def test_a_superseding_install_retains_an_unattributed_open_fence(self) -> None:
        """The same ownership rule binds the superseding route: a fence with
        no attribution is nobody's to adopt, so installing over the prepared
        candidate refuses while it is open (CON-807)."""
        self.prepare_a_blocked_release()
        foreign = {"fence_id": "unattributed-foreign-operation"}
        self.fence_path.write_text(json.dumps(foreign))
        self.make_release("v1.2.0")
        result = self.run_installer(
            "install", "--version", "v1.2.0", "--artifact-dir", str(self.artifacts), env=self.plan_env(blocked=False)
        )
        self.assertNotEqual(result.returncode, 0, "a superseding install adopted an unattributed boundary")
        self.assertTrue(self.fence_path.exists(), "the install removed an unattributed boundary")
        self.assertEqual(json.loads(self.fence_path.read_text(encoding="utf-8"))["fence_id"], foreign["fence_id"])
        self.assert_active_release("v1.0.0", "v1.0.0")

    def test_a_staged_release_carries_the_core_reported_fence_protocol_marker(self) -> None:
        """The tree this installer stages declares exactly the fence protocol
        the staged core reported about itself, and the declaration is a
        recorded managed file of the release: the core's
        unfenceable-participant check accepts the tree, and a tampered marker
        is a transaction conflict (CON-807)."""
        result = self.install_release("v1.0.0")
        self.assertEqual(result.returncode, 0, result.stderr)
        marker = self.data_root / "v1.0.0" / "fence-protocol"
        self.assertTrue(marker.is_file(), "the staged tree carries no fence-protocol marker")
        self.assertEqual(marker.read_text(encoding="utf-8").strip(), "1", "the marker must carry the core-reported number")
        manifest = installer.load_manifest(installer.paths_for(self.root))
        self.assertIn("fence-protocol", manifest["version_files"], "the marker is not a recorded managed file")
        # A prepared candidate carries the same declaration in its own
        # recorded file set, so its activation keeps the tree fenceable.
        blocked = self.install_release("v1.1.0", env=self.plan_env(blocked=True))
        self.assertEqual(blocked.returncode, 0, blocked.stderr)
        self.assertTrue((self.data_root / "v1.1.0" / "fence-protocol").is_file(), "the prepared tree carries no marker")
        record = json.loads(self.prepared_path.read_text(encoding="utf-8"))
        self.assertIn("fence-protocol", record["version_files"])
        marker = self.data_root / "v1.1.0" / "fence-protocol"
        marker.write_text("0\n", encoding="utf-8")
        conflicted = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertNotEqual(conflicted.returncode, 0, "a tampered fence-protocol marker still activated")
        marker.write_text("1\n", encoding="utf-8")
        repaired = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertEqual(repaired.returncode, 0, repaired.stderr)

    def test_an_unknown_core_gains_no_protocol_marker_and_refuses_the_install(self) -> None:
        """CON-807 capability truth belongs to the staged core: a core that
        answers no --version identity is unknown, and the installer refuses
        to stage it rather than asserting admission support on its behalf."""
        with tempfile.TemporaryDirectory() as directory:
            tree = Path(directory)
            (tree / "bin").mkdir()
            core = tree / "bin" / "concord"
            core.write_text("#!/bin/sh\nexit 2\n", encoding="utf-8")
            core.chmod(0o755)
            with self.assertRaises(installer.InstallerError):
                installer.stamp_fence_protocol(tree)
            self.assertFalse(
                (tree / "fence-protocol").exists(),
                "the installer asserted admission support for a core with no recognized capability",
            )
        # The full install path fails closed the same way: nothing is placed,
        # nothing is prepared, and the active release keeps serving.
        self.install_release("v1.0.0")
        self.make_release("v1.1.0")
        environment = self.env.copy()
        environment["CONCORD_TEST_CORE_IDENTITY_FAIL"] = "1"
        result = self.run_installer(
            "install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts), env=environment
        )
        self.assertNotEqual(result.returncode, 0, "an unknown core was installed")
        self.assertFalse((self.data_root / "v1.1.0" / "fence-protocol").exists())
        self.assertFalse(self.prepared_path.exists(), "an unknown core was prepared for activation")
        self.assert_active_release("v1.0.0", "v1.0.0")

    def test_a_known_legacy_core_installs_unmarked(self) -> None:
        """A core that identifies as its release but predates the descriptor
        route is known-legacy, not unknown: it installs on the rolling
        compatible path with no marker, and the incompatible-migration
        boundary refuses on the unmarked tree instead (CON-807 fail-closed
        at the boundary, not at compatible install)."""
        self.install_release("v1.0.0")
        self.make_release("v1.1.0")
        environment = self.plan_env(blocked=False)
        environment["CONCORD_TEST_CORE_DESCRIPTOR_FAIL"] = "1"
        result = self.run_installer(
            "install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts), env=environment
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")
        self.assertFalse((self.data_root / "v1.1.0" / "fence-protocol").exists(), "a legacy core was marked fence-capable")
        manifest = installer.load_manifest(installer.paths_for(self.root))
        self.assertNotIn("fence-protocol", manifest["version_files"])

    def test_a_descriptor_timeout_after_a_valid_identity_leaves_the_tree_unmarked(self) -> None:
        """A core that identifies as its release but cannot deliver its
        descriptor in time is usable and grants no capability (CON-807): the
        probe leaves the tree unmarked instead of refusing staging."""
        with tempfile.TemporaryDirectory() as temporary:
            tree = Path(temporary)
            (tree / "bin").mkdir()
            binary = tree / "bin" / "concord"
            binary.write_text('#!/bin/sh\nif [ "$2" = "--json" ]; then sleep 2; else printf "v1.1.0\\n"; fi\n', encoding="utf-8")
            binary.chmod(0o755)
            with mock.patch.object(installer, "HOST_LEASE_TIMEOUT_SECONDS", 0.5):
                self.assertIsNone(installer.probe_core_capability(tree, "v1.1.0"))
                installer.stamp_fence_protocol(tree, "v1.1.0")
            self.assertFalse((tree / "fence-protocol").exists())

    def test_an_unsupported_descriptor_grants_no_capability(self) -> None:
        """Capability comes from the staged core's descriptor alone (CON-807).
        A descriptor that cannot be parsed or reports no protocol this
        installer implements, a higher number included, grants no maintenance
        capability: the compatible release still activates, unmarked."""
        cases = {
            "garbage": "not-json",
            "no-protocol": json.dumps({"version": "v1.1.0", "fence_protocol": 0}),
            "boolean-protocol": json.dumps({"version": "v1.1.0", "fence_protocol": True}),
            "higher-protocol": json.dumps({"version": "v1.1.0", "fence_protocol": 2}),
        }
        for index, (name, body) in enumerate(cases.items()):
            with self.subTest(descriptor=name):
                previous, candidate = f"v1.{2 * index}.0", f"v1.{2 * index + 1}.0"
                self.install_release(previous)
                self.make_release(candidate)
                descriptor = self.root / "descriptor.json"
                descriptor.write_text(body.replace("v1.1.0", candidate), encoding="utf-8")
                environment = self.plan_env(blocked=False)
                environment["CONCORD_TEST_CORE_DESCRIPTOR"] = str(descriptor)
                result = self.run_installer(
                    "install", "--version", candidate, "--artifact-dir", str(self.artifacts), env=environment
                )
                self.assertEqual(result.returncode, 0, f"{name}: {result.stdout}{result.stderr}")
                self.assert_active_release(candidate, candidate)
                self.assertFalse(
                    (self.data_root / candidate / "fence-protocol").exists(),
                    f"a {name} descriptor was marked fence-capable",
                )

    def test_an_identity_mismatch_fails_closed(self) -> None:
        """A core that answers --version with a different release than the
        one being staged is not the candidate it claims to be."""
        self.install_release("v1.0.0")
        self.make_release("v1.1.0")
        lying = self.root / "identity.txt"
        lying.write_text("v9.9.9\n", encoding="utf-8")
        environment = self.env.copy()
        environment["CONCORD_TEST_CORE_IDENTITY"] = str(lying)
        result = self.run_installer(
            "install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts), env=environment
        )
        self.assertNotEqual(result.returncode, 0, "a core with a mismatched identity was installed")
        self.assertFalse((self.data_root / "v1.1.0" / "fence-protocol").exists())
        self.assert_active_release("v1.0.0", "v1.0.0")

    def test_activation_refuses_a_candidate_attributed_unrelated_operation(self) -> None:
        """CON-807 promoted probe: release attribution alone is not ownership.
        A boundary attributed to the candidate but naming another maintenance
        operation is refused, and it stays open exactly as written."""
        self.prepare_a_blocked_release()
        foreign = {
            "fence_id": "unrelated-maintenance",
            "operation": "unrelated-maintenance-operation",
            "release_root": str(self.data_root / "v1.1.0"),
            "notice": "another maintenance step holds this boundary",
        }
        self.fence_path.write_text(json.dumps(foreign), encoding="utf-8")
        result = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("unrelated-maintenance-operation", result.stdout + result.stderr)
        self.assert_active_release("v1.0.0", "v1.0.0")
        self.assertEqual(json.loads(self.fence_path.read_text(encoding="utf-8")), foreign)

    def test_a_refused_foreign_boundary_cannot_activate_on_status(self) -> None:
        """CON-807 promoted probe: a refused install leaves only a staged
        journal. Status recovery may activate forward from a durable
        authorized activation phase the operation itself wrote — never from
        a boundary merely observed in place — so the refused install's
        staging rolls back and the active release is unchanged."""
        self.install_release("v1.0.0")
        self.make_release("v1.1.0")
        self.fence_path.write_text(json.dumps({"fence_id": "foreign-operation"}), encoding="utf-8")
        refused = self.run_installer(
            "install", "--version", "v1.1.0", "--artifact-dir", str(self.artifacts), env=self.plan_env(blocked=False)
        )
        self.assertNotEqual(refused.returncode, 0, refused.stdout + refused.stderr)
        status = self.run_installer("status", env=self.plan_env(blocked=False))
        manifest = installer.load_manifest(installer.paths_for(self.root))
        self.assertEqual(
            manifest["version"], "v1.0.0",
            f"a refused install activated on status: rc={status.returncode} stdout={status.stdout} stderr={status.stderr}",
        )
        # The foreign boundary a refused transaction never adopted stays.
        self.assertTrue(self.fence_path.is_file(), "status removed a fence no transaction adopted")
        self.assertEqual(
            json.loads(self.fence_path.read_text(encoding="utf-8"))["fence_id"], "foreign-operation"
        )

    def test_idempotent_activation_retains_a_foreign_maintenance_fence(self) -> None:
        """CON-807: re-running the activation of an already-active release —
        with or without a surviving prepared record — must never close a
        maintenance fence it cannot prove it owns. A foreign boundary stays
        exactly as written for its owner (the operator's offline bootstrap
        closes an orphaned one)."""
        self.install_release("v1.0.0")
        foreign = {"fence_id": "foreign-running-maintenance", "notice": "another operation holds this boundary"}
        self.fence_path.write_text(json.dumps(foreign))
        result = self.run_installer("activate", "--version", "v1.0.0")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.fence_path.exists(), "already-active activation closed a foreign exclusion")
        self.assertEqual(json.loads(self.fence_path.read_text(encoding="utf-8"))["fence_id"], foreign["fence_id"])
        # The same retention holds when a discharged record left no owner:
        # a synthetic committed-activation record with no adopted fence
        # discharges without touching the foreign boundary.
        synthetic = {
            "schema": installer.PREPARED_RELEASE_SCHEMA,
            "version": "v1.0.0",
            "version_files": json.loads((self.data_root / "install-manifest.json").read_text(encoding="utf-8"))["version_files"],
            "blockers": ["synthetic committed record"],
            "migration_command": "concord upgrade",
            "activation_command": "concord activate",
            "created_at": "2026-01-01T00:00:00Z",
        }
        self.prepared_path.write_text(json.dumps(synthetic), encoding="utf-8")
        again = self.run_installer("activate", "--version", "v1.0.0")
        self.assertEqual(again.returncode, 0, again.stderr)
        self.assertFalse(self.prepared_path.exists(), "the committed record was not discharged")
        self.assertTrue(self.fence_path.exists(), "idempotent completion closed a fence no record adopted")
        self.assertEqual(json.loads(self.fence_path.read_text(encoding="utf-8"))["fence_id"], foreign["fence_id"])

    def test_discharge_closes_the_owned_fence_before_dropping_its_record(self) -> None:
        """The prepared record is the durable owner of the fence identity, so
        it outlives the fence: a crash between the two discharge steps always
        leaves a recorded owner for whatever is still open, and the re-run
        converges (CON-807 safe discharge ordering)."""
        self.prepare_a_blocked_release()
        with mock.patch.dict(os.environ, self.plan_env(blocked=False), clear=True):
            def crash_after_fence_close(*args, **kwargs):
                raise RuntimeError("synthetic crash after the owned fence closed, before the record dropped")

            with mock.patch.object(installer, "clear_prepared_release", side_effect=crash_after_fence_close):
                with self.assertRaises(RuntimeError):
                    installer.activate(SimpleNamespace(root=self.root, version="v1.1.0"))
        # The owned fence is already closed; the record — the only owner —
        # survived the crash and names the activation to finish.
        self.assertFalse(self.fence_path.exists(), "the owned fence outlived its record")
        self.assertTrue(self.prepared_path.exists(), "the record was dropped before the fence it owns")
        completed = self.run_installer("activate", "--version", "v1.1.0", env=self.plan_env(blocked=False))
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assert_active_release("v1.1.0", "v1.1.0")
        self.assertFalse(self.prepared_path.exists())
        self.assertFalse(self.fence_path.exists())


class PluginEntryTupleUnitTest(unittest.TestCase):
    """CD-0182: the installer recognizes the tuple form of the managed entry."""

    ENTRY = "/tools/concord-plugin.ts"

    def test_plan_keeps_a_tuple_entry_untouched(self) -> None:
        text = '{\n  "plugin": [\n    ["%s", {"session_opener": ["x", "{command}"]}]\n  ]\n}\n' % self.ENTRY
        self.assertEqual(installer.plan_plugin_entry(text, self.ENTRY), text)

    def test_plan_still_adds_a_bare_entry_when_absent(self) -> None:
        text = '{\n  "keep": true,\n  "plugin": [\n    ["/operator/plugin", {"agents": {}}]\n  ]\n}\n'
        planned = installer.plan_plugin_entry(text, self.ENTRY)
        self.assertIn(self.ENTRY, installer.jsonc_data(planned)["plugin"])

    def test_remove_drops_the_whole_tuple_and_keeps_neighbours(self) -> None:
        text = (
            '{\n  "keep": true,\n  "plugin": [\n    ["/operator/plugin", {"agents": {}}],\n'
            '    ["%s", {"session_opener": ["x", "a]b", "{command}"]}],\n    "/operator/other"\n  ]\n}\n' % self.ENTRY
        )
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        self.assertNotIn("session_opener", result)
        self.assertIn("/operator/plugin", result)
        self.assertIn("/operator/other", result)
        plugin = installer.jsonc_data(result)["plugin"]
        self.assertEqual(plugin, [["/operator/plugin", {"agents": {}}], "/operator/other"])

    def test_remove_ignores_a_bracket_inside_a_block_comment(self) -> None:
        """A ``/* [ */`` comment before the tuple never becomes the open bracket."""
        text = (
            '{\n  "keep": true,\n  "plugin": [\n'
            '    /* [ comment bracket */ "/operator/other",\n'
            '    ["%s", {"session_opener": ["x", "{command}"]}]\n  ]\n}\n' % self.ENTRY
        )
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        self.assertNotIn("session_opener", result)
        self.assertIn("comment bracket", result)
        self.assertIn("/operator/other", result)
        plugin = installer.jsonc_data(result)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

    def test_remove_ignores_a_bracket_inside_a_line_comment(self) -> None:
        text = (
            '{\n  "plugin": [\n'
            '    // [ comment bracket\n'
            '    ["%s", {"session_opener": ["x", "{command}"]}],\n    "/operator/other"\n  ]\n}\n' % self.ENTRY
        )
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        plugin = installer.jsonc_data(result)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

    def test_remove_ignores_a_close_bracket_inside_a_comment(self) -> None:
        """A ``/* ] */`` comment inside the tuple's options cannot end the span."""
        text = (
            '{\n  "keep": true,\n  "plugin": [\n'
            '    ["/operator/other"],\n'
            '    ["%s", /* ] comment */ {"session_opener": ["x", "{command}"]}]\n  ]\n}\n' % self.ENTRY
        )
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        self.assertNotIn("session_opener", result)
        # The neighbour survives whole and the remainder still parses: a span
        # that ended at the comment's ']' would strand the comment body.
        plugin = installer.jsonc_data(result)["plugin"]
        self.assertEqual(plugin, [["/operator/other"]])

    def test_remove_skips_a_block_comment_between_the_tuple_and_its_comma(self) -> None:
        """A comment between the tuple and its separating comma hides nothing."""
        text = (
            '{\n  "keep": true,\n  "plugin": [\n'
            '    ["%s", {"session_opener": ["x", "{command}"]}] /* after */, "/operator/other"\n  ]\n}\n'
            % self.ENTRY
        )
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        self.assertNotIn("session_opener", result)
        # The neighbour survives and the remainder still parses: a span that
        # stopped at the comment would strand a leading comma before it.
        plugin = installer.jsonc_data(result)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

    def test_remove_skips_a_line_comment_between_the_tuple_and_its_comma(self) -> None:
        text = (
            '{\n  "keep": true,\n  "plugin": [\n'
            '    ["%s", {"session_opener": ["x", "{command}"]}] // tuple note\n'
            '    ,\n    "/operator/other"\n  ]\n}\n' % self.ENTRY
        )
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        self.assertNotIn("session_opener", result)
        plugin = installer.jsonc_data(result)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

    def test_remove_skips_a_block_comment_before_the_trailing_tuples_comma(self) -> None:
        text = (
            '{\n  "keep": true,\n  "plugin": [\n'
            '    "/operator/other", /* tuple note */\n'
            '    ["%s", {"session_opener": ["x", "{command}"]}]\n  ]\n}\n' % self.ENTRY
        )
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        self.assertNotIn("session_opener", result)
        # The comma before the comment goes with the tuple; a span that kept
        # it would leave a trailing comma after "/operator/other".
        plugin = installer.jsonc_data(result)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

    def test_remove_skips_a_full_line_comment_before_the_tuples_comma(self) -> None:
        text = (
            '{\n  "keep": true,\n  "plugin": [\n'
            '    "/operator/other",\n'
            '    // tuple note\n'
            '    ["%s", {"session_opener": ["x", "{command}"]}]\n  ]\n}\n' % self.ENTRY
        )
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        self.assertNotIn("session_opener", result)
        plugin = installer.jsonc_data(result)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

    def test_remove_skips_a_comment_between_the_bare_token_and_its_comma(self) -> None:
        text = (
            '{\n  "keep": true,\n  "plugin": [\n'
            '    "%s" /* note */, "/operator/other"\n  ]\n}\n' % self.ENTRY
        )
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        plugin = installer.jsonc_data(result)["plugin"]
        self.assertEqual(plugin, ["/operator/other"])

    def test_remove_keeps_the_bare_form_behavior(self) -> None:
        text = '{\n  "keep": true,\n  "plugin": [\n    "/operator/other",\n    "%s"\n  ]\n}\n' % self.ENTRY
        result = installer.remove_plugin_entry(text, self.ENTRY)
        self.assertNotIn(self.ENTRY, result)
        self.assertEqual(installer.jsonc_data(result)["plugin"], ["/operator/other"])

    def test_remove_is_a_noop_when_absent(self) -> None:
        text = '{\n  "keep": true,\n  "plugin": ["/operator/other"]\n}\n'
        self.assertEqual(installer.remove_plugin_entry(text, self.ENTRY), text)


class DeriveAdapterFilesTest(unittest.TestCase):
    def test_shipped_set_follows_the_entry_import_graph(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "concord-plugin.ts").write_text(
                'import { a } from "./concord"\nimport { b } from "./agent-switch-hook"\n',
                encoding="utf-8",
            )
            (root / "concord.ts").write_text('import { c } from "./credentials"\n', encoding="utf-8")
            (root / "agent-switch-hook.ts").write_text("export const b = 2\n", encoding="utf-8")
            (root / "credentials.ts").write_text("export const c = 3\n", encoding="utf-8")
            (root / "unreferenced.test.ts").write_text("", encoding="utf-8")
            self.assertEqual(
                installer.derive_adapter_files(root),
                ("agent-switch-hook.ts", "concord-plugin.ts", "concord.ts", "credentials.ts"),
            )

    def test_unresolvable_entry_import_refuses(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "concord-plugin.ts").write_text('import { a } from "./missing"\n', encoding="utf-8")
            with self.assertRaises(installer.InstallerError):
                installer.derive_adapter_files(root)

    def test_import_escaping_the_adapter_directory_refuses(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "concord-plugin.ts").write_text('import { a } from "../escape"\n', encoding="utf-8")
            with self.assertRaises(installer.InstallerError):
                installer.derive_adapter_files(root)

    def test_real_checkout_derivation_includes_the_plugin_entry(self) -> None:
        repo_adapter = SCRIPT.parent.parent / "adapter" / "opencode"
        derived = installer.derive_adapter_files(repo_adapter)
        self.assertIn(installer.PLUGIN_ENTRY_FILE, derived)
        self.assertEqual(derived, installer.ADAPTER_FILES)


class StandaloneInstallerTest(unittest.TestCase):
    """The documented procedure runs the installer with no checkout present.

    The installation guide at .concord/docs/installation.md tells the
    operator to download concord-installer.py
    from the release and run it. The release tarball ships the binary, the
    adapter modules, and the skills, but not the installer, so nothing else is
    on disk beside that one file.

    Every other test here reaches the installer through `scripts/`, where a
    read of a sibling checkout resolves by accident of location. That is why a
    module-scope read of `../adapter/opencode` shipped in fourteen releases
    without a failure: the suite never stood where the operator stands.
    """

    def run_standalone(self, *arguments: str) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as tmp:
            script = Path(tmp) / "concord-installer.py"
            script.write_bytes(SCRIPT.read_bytes())
            return subprocess.run(
                [sys.executable, str(script), *arguments],
                text=True,
                capture_output=True,
                cwd=tmp,
            )

    def test_help_runs_with_no_checkout_present(self) -> None:
        result = self.run_standalone("--help")
        self.assertNotIn("Traceback", result.stderr)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_status_runs_with_no_checkout_present(self) -> None:
        result = self.run_standalone("status")
        # status may report nothing installed; it must not fail to start.
        self.assertNotIn("Traceback", result.stderr)
        self.assertNotIn("NameError", result.stderr)

    def test_a_failure_to_derive_reports_its_cause(self) -> None:
        """The raise path must name the module, not NameError.

        InstallerError is raised inside derive_adapter_files but defined later
        in the module. A caller after import sees the real class, which is why
        test_unresolvable_entry_import_refuses passes. A module-scope caller
        sees NameError instead, so the one context that fails is the one that
        cannot say why.
        """
        result = self.run_standalone("--help")
        self.assertNotIn("NameError", result.stderr)


class ResolveLatestVersionTest(unittest.TestCase):
    def test_pinned_version_changes_the_default_download_base_to_tag_path(self) -> None:
        self.assertEqual(
            installer.release_download_base_url(
                "https://github.com/Sharper-Flow/concord/releases/latest/download", "v9.9.9"
            ),
            "https://github.com/Sharper-Flow/concord/releases/download/v9.9.9",
        )

    def test_non_release_download_base_stays_unchanged(self) -> None:
        base_url = "https://downloads.example.test/concord"
        self.assertEqual(installer.release_download_base_url(base_url, "v9.9.9"), base_url)

    def test_download_base_follows_the_latest_redirect_to_its_tag(self) -> None:
        class Response:
            def geturl(self):
                return "https://github.com/Sharper-Flow/concord/releases/tag/v9.9.9"

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

        with mock.patch.object(installer.urllib.request, "urlopen", return_value=Response()):
            resolved = installer.resolve_latest_version(
                None, "https://github.com/Sharper-Flow/concord/releases/latest/download"
            )
        self.assertEqual(resolved, "v9.9.9")

    def test_download_base_refuses_a_redirect_without_a_release_tag(self) -> None:
        class Response:
            def geturl(self):
                return "https://github.com/Sharper-Flow/concord/releases"

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

        with mock.patch.object(installer.urllib.request, "urlopen", return_value=Response()):
            with self.assertRaises(installer.InstallerError) as raised:
                installer.resolve_latest_version(
                    None, "https://github.com/Sharper-Flow/concord/releases/latest/download"
                )
        self.assertIn("--version", str(raised.exception))

    def test_custom_base_without_a_latest_endpoint_refuses(self) -> None:
        with self.assertRaises(installer.InstallerError) as raised:
            installer.resolve_latest_version(None, "https://example.invalid/concord/download")
        self.assertIn("--version", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
