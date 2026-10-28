#!/usr/bin/env python3
"""Safe CI planning/guard tests; never invoke real privileged tools."""
from __future__ import annotations

import importlib.util
import ast
import contextlib
import ctypes
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import tarfile
import unittest
import types
from unittest.mock import Mock, patch


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("ci_native_provision", ROOT / "scripts/ci-native-provision.py")
assert SPEC and SPEC.loader
HELPER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HELPER)


# Exact heredoc bytes and surrounding control flow from the pinned official
# debian/apparmor.postinst. Retained control fixtures are data, never shell input.
HOME_HEADER = b"""# This file is auto-generated. It is recommended you update it using:
# $ sudo dpkg-reconfigure apparmor
#
# The following is a space-separated list of where additional user home
# directories are stored, each must have a trailing '/'. Directories added
# here are appended to @{HOMEDIRS}.  See tunables/home for details.
"""
HOME_DEFAULT = b"#@{HOMEDIRS}+=\n"
XDG_SITE_DEFAULT = Path("tunables/xdg-user-dirs.d/site").with_suffix(".local")
XDG_DEFAULT = b"""# ------------------------------------------------------------------
#
#    Copyright (C) 2014 Canonical Ltd.
#    This program is free software; you can redistribute it and/or
#    modify it under the terms of version 2 of the GNU General Public
#    License published by the Free Software Foundation.
#
# ------------------------------------------------------------------

# The following may be used to add additional entries such as for
# translations. See tunables/xdg-user-dirs for details. Eg:
#@{XDG_MUSIC_DIR}+="Musique"

#@{XDG_DESKTOP_DIR}+=""
#@{XDG_DOWNLOAD_DIR}+=""
#@{XDG_TEMPLATES_DIR}+=""
#@{XDG_PUBLICSHARE_DIR}+=""
#@{XDG_DOCUMENTS_DIR}+=""
#@{XDG_MUSIC_DIR}+=""
#@{XDG_PICTURES_DIR}+=""
#@{XDG_VIDEOS_DIR}+=""
"""
POSTINST_DEFAULTS = (b'''\tdb_get apparmor/homedirs
\ttmp=`mktemp`
        cat > "$tmp" <<EOM
''' + HOME_HEADER + b'''EOM
        if [ -n "$RET" ]; then
            cat >> "$tmp" <<EOM
@{HOMEDIRS}+=$RET
EOM
        else
            cat >> "$tmp" <<EOM
''' + HOME_DEFAULT + f'''EOM
        fi
        mkdir -p /etc/apparmor.d/tunables/home.d 2>/dev/null || true
        mv -Z -f "$tmp" /etc/apparmor.d/tunables/home.d/ubuntu
        chmod 644 /etc/apparmor.d/tunables/home.d/ubuntu

        if [ ! -e /etc/apparmor.d/{XDG_SITE_DEFAULT} ]; then
            tmp=`mktemp`
            cat > "$tmp" <<EOM
'''.encode("ascii") + XDG_DEFAULT + f'''EOM

            mkdir -p /etc/apparmor.d/tunables/xdg-user-dirs.d 2>/dev/null || true
            mv -Z -n "$tmp" /etc/apparmor.d/{XDG_SITE_DEFAULT}
            chmod 644 /etc/apparmor.d/{XDG_SITE_DEFAULT}
        fi
'''.encode("ascii"))

# Public pinned source contexts; fixtures are parsed as data, never executed.
STATFS_CONTEXT = b'''        class statfs_t(ctypes.Structure):
            _fields_ = [('f_type', ctypes.c_long), ('f_bsize', ctypes.c_long)]
        buf = statfs_t()
'''
EXIT_CONTEXT = b'''    def __exit__(self, *exc):
        try:
            self._send({'exit': True})
        except OSError:
            pass

        self._parent_sock.close()

        if self.proc:
            self.proc.wait(timeout=5)
        return False
'''


class ProvisionTests(unittest.TestCase):
    def test_harness_repairs_bind_authenticated_source_and_record_only_two_diffs(self):
        files = {"tests/test-sandbox.py": b"unchanged prefix\n" + STATFS_CONTEXT + b"unchanged assertions\n",
                 "tests/test-helper.py": b"unchanged prefix\n" + EXIT_CONTEXT,
                 "bubblewrap.c": b"unchanged launcher\n"}
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            source = root / "bubblewrap-0.12.0"
            (source / "tests").mkdir(parents=True)
            archive = root / "source.tar"
            data = self.package_tar({"bubblewrap-0.12.0/" + key: value for key, value in files.items()})
            archive.write_bytes(data)
            for key, value in files.items():
                (source / key).write_bytes(value)
            with patch.object(HELPER, "SOURCE_DIGEST", hashlib.sha256(data).hexdigest()), \
                 patch.object(subprocess, "run") as execute:
                records = HELPER.repair_upstream_source(archive, source)
                self.assertEqual(set(records), {"tests/test-sandbox.py", "tests/test-helper.py"})
                for key, record in records.items():
                    self.assertEqual(record["original_sha256"], hashlib.sha256(files[key]).hexdigest())
                    self.assertEqual(record["patched_sha256"], hashlib.sha256((source / key).read_bytes()).hexdigest())
                    self.assertTrue(record["diff"].startswith("--- a/" + key + "\n+++ b/" + key + "\n"))
                self.assertEqual((source / "bubblewrap.c").read_bytes(), files["bubblewrap.c"])
                self.assertEqual(HELPER.verify_upstream_source(archive, source, repaired=True), records)
                with self.assertRaisesRegex(ValueError, "source differs"):
                    HELPER.repair_upstream_source(archive, source)
                for key in files:
                    original = (source / key).read_bytes()
                    (source / key).write_bytes(original + b"extra change\n")
                    with self.subTest(drift=key), self.assertRaisesRegex(ValueError, "source differs"):
                        HELPER.verify_upstream_source(archive, source, repaired=True)
                    (source / key).write_bytes(original)
                archive.write_bytes(data + b"archive drift")
                with self.assertRaisesRegex(ValueError, "authentication failed"):
                    HELPER.verify_upstream_source(archive, source, repaired=True)
                execute.assert_not_called()
        with patch.object(os, "geteuid", return_value=0), \
             patch.object(sys, "argv", ["helper", "repair-upstream-source", "archive", "source"]), \
             patch.object(HELPER, "repair_upstream_source") as repair:
            with self.assertRaisesRegex(ValueError, "unprivileged runner"):
                HELPER.main()
            repair.assert_not_called()

    def test_harness_repairs_refuse_missing_duplicate_mutated_or_extra_context_before_writes(self):
        for change in ("missing", "duplicate", "mutated", "helper mutated", "extra", "link"):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as name:
                root = Path(name)
                source = root / "bubblewrap-0.12.0"
                (source / "tests").mkdir(parents=True)
                sandbox = STATFS_CONTEXT
                if change == "missing":
                    sandbox = b"no known context\n"
                elif change == "duplicate":
                    sandbox *= 2
                elif change == "mutated":
                    sandbox = sandbox.replace(b"c_long", b"c_int")
                files = {"tests/test-sandbox.py": sandbox, "tests/test-helper.py": EXIT_CONTEXT}
                if change == "helper mutated":
                    files["tests/test-helper.py"] = EXIT_CONTEXT.replace(b"timeout=5", b"timeout=6")
                archive = root / "source.tar"
                data = self.package_tar({"bubblewrap-0.12.0/" + key: value for key, value in files.items()})
                archive.write_bytes(data)
                for key, value in files.items():
                    (source / key).write_bytes(value)
                if change == "extra":
                    (source / "extra").write_bytes(b"unrelated")
                if change == "link":
                    (source / "tests/test-helper.py").unlink()
                    (source / "tests/test-helper.py").symlink_to("test-sandbox.py")
                before = (source / "tests/test-sandbox.py").read_bytes()
                with patch.object(HELPER, "SOURCE_DIGEST", hashlib.sha256(data).hexdigest()), self.assertRaises(ValueError):
                    HELPER.repair_upstream_source(archive, source)
                self.assertEqual((source / "tests/test-sandbox.py").read_bytes(), before)

    def test_statfs_repair_has_complete_amd64_layout_without_libc_or_upstream_execution(self):
        repaired = HELPER.repaired_upstream_file("tests/test-sandbox.py", STATFS_CONTEXT)
        tree = ast.parse("def synthetic():\n" + repaired.decode())
        fields = tree.body[0].body[0].body[0].value
        expected = [("f_type", ctypes.c_long), ("f_bsize", ctypes.c_long),
                    *[(key, ctypes.c_ulong) for key in ("f_blocks", "f_bfree", "f_bavail", "f_files", "f_ffree")],
                    ("f_fsid", ctypes.c_int * 2),
                    *[(key, ctypes.c_long) for key in ("f_namelen", "f_frsize", "f_flags")],
                    ("f_spare", ctypes.c_long * 4)]
        expressions = ["ctypes.c_long", "ctypes.c_long", *(["ctypes.c_ulong"] * 5), "ctypes.c_int * 2",
                       *(["ctypes.c_long"] * 3), "ctypes.c_long * 4"]
        self.assertEqual([(item.elts[0].value, ast.unparse(item.elts[1])) for item in fields.elts],
                         [(key, expression) for (key, _), expression in zip(expected, expressions)])
        layout = type("SyntheticStatfs", (ctypes.Structure,), {"_fields_": expected})
        self.assertEqual(ctypes.sizeof(layout), 120)
        self.assertEqual([getattr(layout, key).offset for key, _ in expected], list(range(0, 96, 8)))
        self.assertEqual(repaired.split(b"        buf =")[1], STATFS_CONTEXT.split(b"        buf =")[1])

    def test_exit_repair_checks_every_nonzero_status_after_original_wait_and_cleanup(self):
        repaired = HELPER.repaired_upstream_file("tests/test-helper.py", EXIT_CONTEXT)
        method = ast.parse("class Synthetic:\n" + repaired.decode()).body[0].body[0]
        self.assertEqual(ast.dump(method.body[0]), ast.dump(ast.parse("class Synthetic:\n" + EXIT_CONTEXT.decode()).body[0].body[0].body[0]))
        self.assertEqual(ast.unparse(method.body[1]), "self._parent_sock.close()")
        conditional = method.body[2]
        self.assertEqual(ast.unparse(conditional.test), "self.proc")
        self.assertEqual(ast.unparse(conditional.body[0]), "returncode = self.proc.wait(timeout=5)")
        self.assertEqual(ast.unparse(conditional.body[1].test), "returncode != 0")
        self.assertIsInstance(conditional.body[1].body[0], ast.Raise)
        self.assertEqual(ast.unparse(conditional.body[1].body[0].exc.func), "AssertionError")
        self.assertEqual(ast.unparse(method.body[-1]), "return False")

    def test_temp_inventory_keeps_all_owners_and_tolerates_disappearing_entries(self):
        def entries(directory):
            return [Path("/tmp/root"), Path("/tmp/user"), Path("/tmp/gone")] if str(directory) == "/tmp" else [Path("/var/tmp/root")]
        def info(path):
            if path.name == "gone":
                raise FileNotFoundError("synthetic lifecycle churn")
            return types.SimpleNamespace(st_uid=1000 if path.name == "user" else 0)
        with patch.object(Path, "iterdir", autospec=True, side_effect=entries), \
             patch.object(Path, "lstat", autospec=True, side_effect=info):
            self.assertEqual(HELPER.compatibility_temp_entries(), {"/tmp/root": 0, "/tmp/user": 1000, "/var/tmp/root": 0})

    def test_expected_upstream_temp_requires_one_new_regular_root_file_and_exact_bytes(self):
        candidate = "/tmp/bwrap-test-aB123456"
        info = types.SimpleNamespace(st_mode=0o100600, st_uid=0, st_size=6, st_dev=1, st_ino=2)
        before = {"/tmp/unrelated": 0, "/var/tmp/old": 1000}
        after = {"/tmp/unrelated-new": 0, candidate: 0}
        for bad in (None, "absent", "preexisting", "multiple", "directory", "link", "owner", "size", "contents", "replaced"):
            observed = types.SimpleNamespace(**vars(info))
            if bad in ("directory", "link"):
                observed.st_mode = 0o40700 if bad == "directory" else 0o120777
            elif bad == "owner":
                observed.st_uid = 1000
            elif bad == "size":
                observed.st_size = 7
            opened = types.SimpleNamespace(**vars(observed))
            if bad == "replaced":
                opened.st_ino += 1
            with self.subTest(bad=bad), patch.object(Path, "lstat", return_value=observed), \
                 patch.object(os, "open", return_value=41) as open_file, patch.object(os, "fstat", return_value=opened), \
                 patch.object(os, "read", return_value=b"wrong\n" if bad == "contents" else b"hello\n"), \
                 patch.object(os, "close") as close, patch.object(Path, "unlink") as unlink:
                original = {**before, **({candidate: 1000} if bad == "preexisting" else {})}
                current = {**after, **({"/tmp/bwrap-test-12345678": 0} if bad == "multiple" else {})}
                if bad == "absent":
                    current = before
                if bad is None:
                    proof = HELPER.verify_upstream_temp(original, current)
                    self.assertEqual(proof["path"], candidate)
                    self.assertEqual(proof["sha256"], hashlib.sha256(b"hello\n").hexdigest())
                    self.assertEqual(open_file.call_args.args, (candidate, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK))
                    close.assert_called_once_with(41)
                else:
                    with self.assertRaisesRegex(ValueError, "upstream temporary"):
                        HELPER.verify_upstream_temp(original, current)
                unlink.assert_not_called()

    def test_root_qualification_keeps_temp_diagnostics_but_gates_every_other_host_and_input_field(self):
        work = Path("/var/lib/concord-upstream.synthetic/work")
        before = {key: "bound" for key in ("uids", "gids", "supplementary_groups", "label", "capabilities", "userns",
                                          "sysctls", "profiles", "mounts", "installed_bwrap")}
        tree = ast.parse((ROOT / "scripts/ci-native-provision.py").read_text())
        host = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == "compatibility_host_state")
        self.assertEqual({key.value for key in host.body[-1].value.keys}, set(before))
        before_temp = {"/tmp/before": 0}
        after_temp = {"/tmp/after": 1000, "/tmp/bwrap-test-12345678": 0}
        inventory = self.compatibility_inventory(work)
        records = [{"name": item["name"], "command": item["cmd"], "env": dict(item["env"], BWRAP_MUST_WORK="1"),
                    "returncode": 0, "result": "OK", "stdout": "1..1\nok 1 positive\n"} for item in inventory]
        texts = {work / "build/meson-info/intro-tests.json": json.dumps(inventory),
                 work / "build/meson-logs/testlog.json": "\n".join(json.dumps(item) for item in records)}
        for drift in (None, *before.keys(), "inputs", "source", "temp observation"):
            after = dict(before)
            if drift in before:
                after[drift] = "changed"
            output = io.StringIO()
            with self.subTest(drift=drift), patch.object(HELPER, "secure_path"), patch.object(os, "geteuid", return_value=0), \
                 patch.object(HELPER, "verify_root_context"), patch.object(HELPER, "identity", return_value={"sha256": "a" * 64}), \
                 patch.object(HELPER, "compatibility_host_state", side_effect=[before, after]), \
                 patch.object(HELPER, "compatibility_temp_entries", side_effect=[before_temp,
                              OSError("temporary inventory unavailable") if drift == "temp observation" else after_temp]), \
                 patch.object(HELPER, "compatibility_inputs", side_effect=[{"input": "bound"}, {"input": "changed" if drift == "inputs" else "bound"}]), \
                 patch.object(HELPER, "verify_upstream_source", side_effect=[{}, ValueError("source drift") if drift == "source" else {}]) as source, \
                 patch.object(HELPER, "supervise_compatibility"), patch.object(HELPER, "verify_upstream_temp", return_value={"proof": "bound"}) as temporary, \
                 patch.object(Path, "read_text", autospec=True, side_effect=lambda path: texts[path]), \
                 patch.object(Path, "glob", return_value=[]), patch.object(Path, "exists", return_value=False), \
                 patch.object(Path, "unlink") as unlink, contextlib.redirect_stdout(output):
                if drift is None:
                    HELPER.upstream_compatibility(work)
                else:
                    with self.assertRaisesRegex(ValueError, "compatibility failed"):
                        HELPER.upstream_compatibility(work)
                report = json.loads(output.getvalue())
                self.assertEqual(report["before_host"], before)
                self.assertEqual(report["after_host"], after)
                self.assertEqual(report["before_temp_entries"], before_temp)
                if drift != "temp observation":
                    self.assertEqual(report["after_temp_entries"], after_temp)
                    self.assertEqual(report["expected_temp_file"], {"proof": "bound"})
                    temporary.assert_called_once_with(before_temp, after_temp)
                else:
                    temporary.assert_not_called()
                self.assertEqual(source.call_count, 2)
                self.assertTrue(all(call.kwargs == {"repaired": True} for call in source.call_args_list))
                self.assertEqual(bool(unlink.call_count), drift in before)

    def test_crash_qualification_requires_available_nontruncated_empty_metadata(self):
        for availability, records in (("captured", []), ("captured", [{}]), ("captured", [{"COREDUMP_UID": "0"}]),
                                      ("unavailable", []), ("possibly truncated", [])):
            result = {"availability": availability, "records": records}
            output = io.StringIO()
            with self.subTest(result=result), patch.object(sys, "argv", ["helper", "qualify-compatibility-crashes", "synthetic"]), \
                 patch.object(Path, "read_text", return_value='{"errors": []}'), \
                 patch.object(HELPER, "compatibility_crash_metadata", return_value=result), contextlib.redirect_stdout(output):
                if availability == "captured" and not records:
                    HELPER.main()
                else:
                    with self.assertRaisesRegex(ValueError, "core-dump metadata"):
                        HELPER.main()
                self.assertEqual(json.loads(output.getvalue()), result)
        with patch.object(os, "geteuid", return_value=1000), \
             patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0, '{}\n', '')):
            self.assertEqual(HELPER.compatibility_crash_metadata({"started_unix_seconds": 100, "finished_unix_seconds": 110})["records"], [{}])
        for stdout, availability in (("-- No entries --\n", "captured"), ("-- No entries --\n{}\n", "unavailable"),
                                     ("-- no entries --\n", "unavailable")):
            with self.subTest(stdout=stdout), patch.object(os, "geteuid", return_value=1000), \
                 patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout, "")):
                metadata = HELPER.compatibility_crash_metadata({"started_unix_seconds": 100, "finished_unix_seconds": 110})
                self.assertEqual((metadata["availability"], metadata["records"]), (availability, []))

    def test_compatibility_digest_is_published_only_after_nonroot_crash_qualification(self):
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        block = re.search(r'(?ms)^    # Ownership is hygiene.*?^    exit 0$', script)[0]
        self.assertIn('python3 "$helper" qualify-compatibility-crashes', block)
        self.assertNotIn('sudo python3', block)
        self.assertLess(script.index('repair-upstream-source'), script.index('/usr/bin/meson setup'))
        self.assertIn('if [[ $mode == compatibility ]]; then\n    python3 "$helper" repair-upstream-source', script)
        for root_status, crash_status in ((0, 0), (37, 0), (0, 1)):
            with self.subTest(root_status=root_status, crash_status=crash_status), tempfile.TemporaryDirectory() as name:
                command = (f"set -Eeuo pipefail; evidence={shlex.quote(name)}; GITHUB_OUTPUT={shlex.quote(name + '/output')}; " +
                    "work=synthetic; compat_root=synthetic; helper=synthetic; " +
                    f"sudo() {{ if [[ $1 == -n ]]; then printf '{{}}'; return {root_status}; fi; }}; " +
                    "python3() { if [[ $2 == qualify-compatibility-crashes ]]; then " +
                    f"printf '{{\"availability\":\"captured\",\"records\":[]}}'; return {crash_status}; " +
                    "else printf '%064d\\n' 0; fi; }; " + block)
                result = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, root_status or crash_status, result.stderr)
                self.assertEqual((Path(name) / "output").exists(), root_status == crash_status == 0)
                self.assertEqual((Path(name) / "crash-metadata.json").exists(), root_status == 0)
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        self.assertIn('if [[ ! -f "$RUNNER_TEMP/upstream-compatibility-evidence/crash-metadata.json" ]]; then', workflow)
        diagnostic = re.search(r'(?ms)^          if \[\[ ! -f .*?^          fi$', workflow)[0]
        for existing in (False, True):
            with self.subTest(existing=existing), tempfile.TemporaryDirectory() as name:
                evidence = Path(name) / "upstream-compatibility-evidence"
                evidence.mkdir()
                metadata = evidence / "crash-metadata.json"
                if existing:
                    metadata.write_text("qualification failure evidence")
                command = f"set -Eeuo pipefail; RUNNER_TEMP={shlex.quote(name)}; python3() {{ printf 'fallback diagnostic'; }}; " + diagnostic
                result = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(metadata.read_text(), "qualification failure evidence" if existing else "fallback diagnostic")

    def test_default_control_fixture_bytes(self):
        self.assertEqual(HELPER.XDG_SITE_DEFAULT, XDG_SITE_DEFAULT)
        # Pin the public control data, including the constructed output filename.
        for data, digest in (
            (HOME_HEADER, "84de102586fb59f752cd6a386dea1086c7073c8549387f0783bea15c171a4c21"),
            (HOME_DEFAULT, "34ee94b20f507958109cabde0e372ebc710afe0297c7d5b402ad5f80e39a56ba"),
            (XDG_DEFAULT, "d9705b1ab368e32f4cce94f2242c6947b8624985b9c6de2ab4ee01b398dc4703"),
            (POSTINST_DEFAULTS, "d21f23b8265b3fcf994c2f8cd13e1e210d6887bbb5f0f79d6ea602260f97adff"),
        ):
            self.assertEqual(hashlib.sha256(data).hexdigest(), digest)

    @staticmethod
    def policy_fixture(root, generated_defaults=False):
        private = root / "private"
        base = root / "installed-base"
        private.mkdir()
        profile = b'abi <abi/4.0>,\ninclude <tunables/global>\ninclude if exists <local/unpriv_bwrap>\n'
        for directory in (private / "base", base):
            (directory / "abi").mkdir(parents=True)
            (directory / "abi/4.0").write_text("authenticated ABI\n")
            (directory / "tunables/home.d").mkdir(parents=True)
            (directory / "tunables/xdg-user-dirs.d").mkdir()
            (directory / "tunables/global").write_text("include <tunables/home.d>\ninclude <tunables/xdg-user-dirs.d>\n")
            (directory / "tunables/home.d/site").write_text("@{HOME}=/srv/example-users/*\n")
        if generated_defaults:
            (base / "tunables/home.d/ubuntu").write_bytes(HOME_HEADER + HOME_DEFAULT)
            (base / XDG_SITE_DEFAULT).write_bytes(XDG_DEFAULT)
        return private, base, profile

    @staticmethod
    def package_tar(files):
        output = io.BytesIO()
        with tarfile.open(fileobj=output, mode="w") as archive:
            for name, data in files.items():
                member = tarfile.TarInfo("./" + name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        return output.getvalue()

    def test_private_parser_staging_and_effects_bind_all_inputs_without_host_execution(self):
        for revision in ("8", "9"):
            with self.subTest(installed_revision=revision), tempfile.TemporaryDirectory() as name:
                root = Path(name)
                private, base, profile = self.policy_fixture(root, generated_defaults=True)
                archives = [root / "apparmor.deb", root / "apparmor-profiles.deb"]
                for archive in archives:
                    archive.write_bytes(b"authenticated " + archive.name.encode())
                files = {"sbin/apparmor_parser": b"synthetic ELF; never execute", "etc/apparmor/parser.conf": b"# authenticated config\n"}
                for path in (private / "base").rglob("*"):
                    if path.is_file():
                        files["etc/apparmor.d/" + str(path.relative_to(private / "base"))] = path.read_bytes()
                files["etc/apparmor.d/unrelated-policy"] = b"must not be extracted"
                payloads = {"apparmor.deb": self.package_tar(files),
                            "apparmor-profiles.deb": self.package_tar({"usr/share/apparmor/extra-profiles/bwrap-userns-restrict": profile})}
                runtime = root / "runtime"
                runtime.write_text("bound installed runtime")

                def command(args, **kwargs):
                    if args[:2] == ["dpkg-deb", "-f"]:
                        package = Path(args[2]).stem
                        return {"Package": package, "Version": HELPER.PINS["apparmor"],
                                "Architecture": "amd64" if package == "apparmor" else "all"}[args[3]] + "\n"
                    if args[:2] == ["apt-cache", "show"]:
                        package = args[2].split("=")[0]
                        digest = hashlib.sha256((root / (package + ".deb")).read_bytes()).hexdigest()
                        return f"Package: {package}\nVersion: {HELPER.PINS['apparmor']}\nArchitecture: {'amd64' if package == 'apparmor' else 'all'}\nSHA256: {digest}\n"
                    if args[:2] == ["dpkg-deb", "--fsys-tarfile"]:
                        return payloads[Path(args[2]).name]
                    self.fail(f"unexpected host command: {args}")

                def execute_command(args, **kwargs):
                    if args[:2] == ["dpkg-deb", "-e"]:
                        control = Path(args[3])
                        control.mkdir()
                        (control / "control").write_text("Package: " + Path(args[2]).stem + "\n")
                        if Path(args[2]).stem == "apparmor":
                            (control / "postinst").write_bytes(b"#!/bin/sh\nset -e\n" + POSTINST_DEFAULTS + b"exit 0\n")
                    else:
                        self.assertEqual(args[0], str(private / "apparmor_parser"))
                    return subprocess.CompletedProcess(args, 0)

                with patch.object(HELPER, "secure_path"), patch.object(os, "geteuid", return_value=0), \
                     patch.object(HELPER, "VENDOR_DIGEST", hashlib.sha256(profile).hexdigest()), \
                     patch.object(HELPER, "parser_runtime", return_value=([str(runtime)], {"mock ELF": "bound"})), \
                     patch.object(subprocess, "check_output", side_effect=command), patch.object(subprocess, "run", side_effect=execute_command) as execute, \
                     contextlib.redirect_stdout(io.StringIO()):
                    # Both installed revisions use this identical authenticated .9
                    # route; installed package identity never chooses the parser.
                    self.assertFalse((private / "base/tunables/home.d/ubuntu").exists())
                    self.assertFalse((private / "base" / XDG_SITE_DEFAULT).exists())
                    HELPER.stage_parser(private, archives, base)
                    self.assertEqual((private / "base/tunables/home.d/ubuntu").read_bytes(), HOME_HEADER + HOME_DEFAULT)
                    self.assertEqual((private / "base" / XDG_SITE_DEFAULT).read_bytes(), XDG_DEFAULT)
                    self.assertFalse((private / "base/unrelated-policy").exists())
                    self.assertEqual([call.args[0][:2] for call in execute.call_args_list], [["dpkg-deb", "-e"]] * 2)
                    manifest = json.loads((private / "manifest.json").read_text())
                    self.assertEqual({source["Version"] for source in manifest["sources"]}, {HELPER.PINS["apparmor"]})
                    for retained in ("apparmor-control/postinst", "base/tunables/home.d/ubuntu", Path("base") / XDG_SITE_DEFAULT):
                        path = private / retained
                        self.assertEqual(manifest["private"][str(path)]["sha256"], hashlib.sha256(path.read_bytes()).hexdigest())
                    execute.reset_mock()
                    HELPER.parser_effect(private, "preprocess", base)
                    HELPER.parser_effect(private, "load", base)
                    for call, action in zip(execute.call_args_list, ("--preprocess", "--replace")):
                        self.assertEqual(call.args[0], [str(private / "apparmor_parser"), "--config-file=" + str(private / "parser.conf"),
                            "--base=" + str(base), "--skip-cache", "--jobs=1", action, str(base / "bwrap-userns-restrict")])
                        self.assertEqual(call.kwargs["cwd"], "/")
                        self.assertNotIn("LD_LIBRARY_PATH", call.kwargs["env"])
                        self.assertLessEqual(call.kwargs["timeout"], 300)
                    for path in (private / "apparmor.deb", private / "apparmor_parser", private / "parser.conf",
                                 private / "base/abi/4.0", private / "vendor-profile", runtime,
                                 private / "apparmor-control/postinst", private / "base/tunables/home.d/ubuntu",
                                 private / "base" / XDG_SITE_DEFAULT, base / "tunables/home.d/ubuntu",
                                 base / XDG_SITE_DEFAULT, base / "tunables/home.d/site", base / "bwrap-userns-restrict"):
                        original = path.read_bytes()
                        path.write_bytes(original + b" drift")
                        execute.reset_mock()
                        with self.subTest(drift=path.name), self.assertRaises(ValueError):
                            HELPER.parser_effect(private, "load", base)
                        execute.assert_not_called()
                        path.write_bytes(original)
                    for path in (private / "apparmor-control/postinst", private / "base/tunables/home.d/ubuntu", private / "base" / XDG_SITE_DEFAULT,
                                 base / "tunables/home.d/ubuntu", base / XDG_SITE_DEFAULT):
                        original = path.read_bytes()
                        mode = path.stat().st_mode & 0o777
                        path.unlink()
                        execute.reset_mock()
                        with self.subTest(missing=path), self.assertRaises(ValueError):
                            HELPER.parser_effect(private, "load", base)
                        execute.assert_not_called()
                        path.write_bytes(original)
                        path.chmod(mode)
                    (base / "tunables/home.d/extra").write_text("unapproved included input")
                    with self.assertRaisesRegex(ValueError, "closure differs"):
                        HELPER.parser_effect(private, "load", base)
                    execute.assert_not_called()

    def test_private_default_derivation_rejects_control_changes_without_shell_execution(self):
        variants = {
            "commented selector": b"#" + POSTINST_DEFAULTS,
            "wrong condition": POSTINST_DEFAULTS.replace(b'if [ -n "$RET" ]', b'if [ -z "$RET" ]'),
            "wrong output": POSTINST_DEFAULTS.replace(b'home.d/ubuntu', b'home.d/other'),
            "wrong redirect": POSTINST_DEFAULTS.replace(b'cat >> "$tmp"', b'cat > "$tmp"'),
            "missing delimiter": POSTINST_DEFAULTS.replace(b'EOM\n', b'', 1),
            "duplicate": POSTINST_DEFAULTS * 2,
            "extra output reference": POSTINST_DEFAULTS + b'# /etc/apparmor.d/tunables/home.d/ubuntu\n',
            "over bound": POSTINST_DEFAULTS + b'#' * 131072,
            "nonempty home default": POSTINST_DEFAULTS.replace(HOME_DEFAULT, b'#@{HOMEDIRS}+=/srv/\n'),
            "active home default": POSTINST_DEFAULTS.replace(HOME_DEFAULT, b'@{HOMEDIRS}+=/srv/\n'),
            "active header": POSTINST_DEFAULTS.replace(b'# The following is', b'@{HOMEDIRS}=/srv/\n# The following is'),
            "active XDG": POSTINST_DEFAULTS.replace(b'#@{XDG_MUSIC_DIR}', b'@{XDG_MUSIC_DIR}'),
            "policy include": POSTINST_DEFAULTS.replace(b'# translations.', b'#include <outside>\n# translations.'),
            "parameter": POSTINST_DEFAULTS.replace(b'# translations.', b'# $RET translations.'),
            "braced parameter": POSTINST_DEFAULTS.replace(b'# translations.', b'# ${RET} translations.'),
            "command substitution": POSTINST_DEFAULTS.replace(b'# translations.', b'# $(touch marker) translations.'),
            "backtick": POSTINST_DEFAULTS.replace(b'# translations.', b'# `touch marker` translations.'),
            "backslash": POSTINST_DEFAULTS.replace(b'# translations.', b'# \\ translations.'),
        }
        for label, control in variants.items():
            with self.subTest(control=label), tempfile.TemporaryDirectory() as name, patch.object(HELPER, "secure_path"), \
                 patch.object(subprocess, "run") as execute, patch.object(subprocess, "check_output") as output:
                private, base, _ = self.policy_fixture(Path(name), generated_defaults=True)
                (private / "apparmor-control").mkdir()
                (private / "apparmor-control/postinst").write_bytes(control)
                installed = {str(path): path.read_bytes() for path in base.rglob("*") if path.is_file()}
                with self.assertRaises(ValueError):
                    HELPER.derive_parser_defaults(private)
                self.assertFalse((private / "base/tunables/home.d/ubuntu").exists())
                self.assertFalse((private / "base" / XDG_SITE_DEFAULT).exists())
                self.assertEqual(installed, {str(path): path.read_bytes() for path in base.rglob("*") if path.is_file()})
                execute.assert_not_called()
                output.assert_not_called()

    def test_private_default_derivation_refuses_collisions_and_missing_control(self):
        for generated in ("tunables/home.d/ubuntu", XDG_SITE_DEFAULT):
            for kind in ("file", "link", "directory"):
                with self.subTest(generated=generated, kind=kind), tempfile.TemporaryDirectory() as name, patch.object(HELPER, "secure_path"), \
                     patch.object(subprocess, "run") as execute, patch.object(subprocess, "check_output") as output:
                    private, _, _ = self.policy_fixture(Path(name))
                    (private / "apparmor-control").mkdir()
                    (private / "apparmor-control/postinst").write_bytes(POSTINST_DEFAULTS)
                    target = private / "base" / generated
                    if kind == "file":
                        target.write_bytes(b"archive collision; do not overwrite")
                    elif kind == "link":
                        target.symlink_to("missing-target")
                    else:
                        target.mkdir()
                    before = sorted(str(path) for path in private.rglob("*"))
                    with self.assertRaisesRegex(ValueError, "collision"):
                        HELPER.derive_parser_defaults(private)
                    self.assertEqual(before, sorted(str(path) for path in private.rglob("*")))
                    if kind == "file":
                        self.assertEqual(target.read_bytes(), b"archive collision; do not overwrite")
                    execute.assert_not_called()
                    output.assert_not_called()
        with tempfile.TemporaryDirectory() as name, patch.object(HELPER, "secure_path"), patch.object(subprocess, "run") as execute:
            with self.assertRaises(OSError):
                HELPER.derive_parser_defaults(Path(name))
            execute.assert_not_called()

    def test_derived_defaults_still_refuse_installed_nondefaults_extras_and_absence(self):
        changes = (("tunables/home.d/ubuntu", b"@{HOMEDIRS}+=/srv/\n"),
                   (XDG_SITE_DEFAULT, b'@{XDG_MUSIC_DIR}+="Other"\n'),
                   ("tunables/home.d/extra", b"# even comment-only extras refuse\n"),
                   ("tunables/xdg-user-dirs.d/extra", b"# extra\n"),
                   ("tunables/home.d/ubuntu", None), (XDG_SITE_DEFAULT, None))
        for name, data in changes:
            with self.subTest(installed=name, data=data), tempfile.TemporaryDirectory() as scratch, patch.object(HELPER, "secure_path"), \
                 patch.object(subprocess, "run") as execute, patch.object(subprocess, "check_output") as output:
                private, base, profile = self.policy_fixture(Path(scratch), generated_defaults=True)
                (private / "apparmor-control").mkdir()
                (private / "apparmor-control/postinst").write_bytes(POSTINST_DEFAULTS)
                (private / "vendor-profile").write_bytes(profile)
                HELPER.derive_parser_defaults(private)
                with patch.object(HELPER, "VENDOR_DIGEST", hashlib.sha256(profile).hexdigest()):
                    HELPER.verify_policy_base(private, base)
                    if data is None:
                        (base / name).unlink()
                    else:
                        (base / name).write_bytes(data)
                    with self.assertRaisesRegex(ValueError, "closure differs"):
                        HELPER.verify_policy_base(private, base)
                execute.assert_not_called()
                output.assert_not_called()

    def test_archive_authentication_refuses_metadata_and_digest_drift(self):
        with tempfile.TemporaryDirectory() as name:
            source, target = Path(name) / "source", Path(name) / "private"
            source.write_bytes(b"synthetic package")
            version = HELPER.PINS["apparmor"]
            fields = {"Package": "apparmor", "Version": version, "Architecture": "amd64"}
            stanza = f"Package: apparmor\nVersion: {version}\nArchitecture: amd64\nSHA256: {hashlib.sha256(source.read_bytes()).hexdigest()}\n"
            for changed in (None, "Package", "Version", "Architecture", "SHA256"):
                def command(args, **kwargs):
                    if args[0] == "dpkg-deb":
                        return "drift" if args[-1] == changed else fields[args[-1]]
                    self.assertEqual(args[:2], ["apt-cache", "show"])
                    return stanza.replace("SHA256: ", "SHA256: bad") if changed == "SHA256" else stanza
                with self.subTest(changed=changed), patch.object(subprocess, "check_output", side_effect=command), patch.object(subprocess, "run") as execute:
                    if changed:
                        with self.assertRaises(ValueError):
                            HELPER.authenticated_archive(source, target, "apparmor", "amd64")
                    else:
                        HELPER.authenticated_archive(source, target, "apparmor", "amd64")
                    execute.assert_not_called()

    def test_policy_closure_missing_mismatched_and_local_inputs_refuse(self):
        with tempfile.TemporaryDirectory() as name, patch.object(HELPER, "secure_path"):
            private, base, profile = self.policy_fixture(Path(name))
            (private / "vendor-profile").write_bytes(profile)
            with patch.object(HELPER, "VENDOR_DIGEST", hashlib.sha256(profile).hexdigest()):
                HELPER.verify_policy_base(private, base)
                missing = base / "tunables/home.d/site"
                missing.unlink()
                with self.assertRaisesRegex(ValueError, "closure differs"):
                    HELPER.verify_policy_base(private, base)
                (base / "tunables/global").unlink()
                with self.assertRaisesRegex(ValueError, "required policy include missing"):
                    HELPER.verify_policy_base(private, base)
                (base / "local").mkdir()
                (base / "local/unpriv_bwrap").symlink_to("missing")
                with self.assertRaisesRegex(ValueError, "local bwrap"):
                    HELPER.verify_policy_base(private, base)

    def test_parser_runtime_uses_readelf_not_execution_and_refuses_unknown_linkage(self):
        for bad in (None, "foreign first", "foreign last", "dependency", "RPATH", "RUNPATH", "AUDIT", "DEPAUDIT", "FILTER", "AUXILIARY", "interpreter", "machine", "resolution",
                    "foreign only", "unknown ABI", "hwcap", "hwcap only", "outside", "nested", "renamed", "library class", "library machine", "preload"):
            def command(args, **kwargs):
                if args == ["/usr/sbin/ldconfig", "-p"]:
                    native = "\tlibc.so.6 (libc6,x86-64) => /lib/x86_64-linux-gnu/libc.so.6\n"
                    foreign = "\tlibc.so.6 (libc6) => /lib32/libc.so.6\n"
                    variants = {
                        "foreign first": foreign + native, "foreign last": native + foreign,
                        "foreign only": foreign, "resolution": native * 2,
                        "unknown ABI": native + "\tlibc.so.6 (unknown) => /lib/other/libc.so.6\n",
                        "hwcap": native + '\tlibc.so.6 (libc6,x86-64, hwcap: "x86-64-v3") => /lib/x86_64-linux-gnu/glibc-hwcaps/x86-64-v3/libc.so.6\n',
                        "hwcap only": '\tlibc.so.6 (libc6,x86-64, hwcap: "x86-64-v3") => /lib/x86_64-linux-gnu/glibc-hwcaps/x86-64-v3/libc.so.6\n',
                        "outside": native.replace("/lib/x86_64-linux-gnu/", "/opt/runtime/"),
                        "nested": native.replace("/libc.so.6", "/alternate/libc.so.6"),
                        "renamed": native.replace("=> /lib/x86_64-linux-gnu/libc.so.6", "=> /lib/x86_64-linux-gnu/libc-alternate.so.6"),
                    }
                    return variants.get(bad, native) + "\tld-linux-x86-64.so.2 (libc6,x86-64) => /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2\n"
                self.assertEqual(args[0], "readelf")
                if args[1] == "-hW":
                    if args[2] == "/lib/x86_64-linux-gnu/libc.so.6":
                        if bad == "library class":
                            return "Class: ELF32\nMachine: Advanced Micro Devices X86-64"
                        if bad == "library machine":
                            return "Class: ELF64\nMachine: AArch64"
                    return "Class: ELF64\nMachine: " + ("AArch64" if bad == "machine" else "Advanced Micro Devices X86-64")
                if args[1] == "-lW":
                    return "[Requesting program interpreter: " + ("/unexpected" if bad == "interpreter" else "/lib64/ld-linux-x86-64.so.2") + "]" if args[2] == "/synthetic/parser" else ""
                if args[2] == "/lib/x86_64-linux-gnu/libc.so.6":
                    # glibc's libc.so.6 itself records the loader as a NEEDED entry.
                    return "(NEEDED) Shared library: [ld-linux-x86-64.so.2]"
                if args[2] != "/synthetic/parser":
                    return ""
                hook = f"({bad}) [/opt/alternate]\n" if bad in {"RPATH", "RUNPATH", "AUDIT", "DEPAUDIT", "FILTER", "AUXILIARY"} else ""
                return hook + "(NEEDED) [" + ("libunapproved.so" if bad == "dependency" else "libc.so.6") + "]"
            with self.subTest(bad=bad), patch.object(HELPER, "identity", return_value={}) as identity, \
                 patch.object(os.path, "lexists", return_value=bad == "preload"), \
                 patch.object(subprocess, "check_output", side_effect=command), patch.object(subprocess, "run") as execute:
                if bad not in (None, "foreign first", "foreign last"):
                    with self.assertRaisesRegex(ValueError, "unsupported parser|host loader preload"):
                        HELPER.parser_runtime(Path("/synthetic/parser"))
                else:
                    paths, linkage = HELPER.parser_runtime(Path("/synthetic/parser"))
                    self.assertEqual(paths, ["/etc/ld.so.cache", "/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2", "/lib/x86_64-linux-gnu/libc.so.6",
                                             "/lib64/ld-linux-x86-64.so.2", "/synthetic/parser"])
                    self.assertEqual(len(linkage), 4)
                    self.assertNotIn("/lib32/libc.so.6", paths)
                    self.assertEqual({str(call.args[0]) for call in identity.call_args_list}, set(paths) - {"/etc/ld.so.cache"})
                execute.assert_not_called()

    def test_cleanup_trap_keeps_both_paths_and_producer_exit_status(self):
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        cleanup = re.search(r"(?ms)^cleanup\(\) \{\n.*?^\}\ntrap cleanup EXIT$", script)
        self.assertIsNotNone(cleanup)
        for parser_root in ("", "/synthetic/root parser"):
            for status in (0, 37):
                for early in (False, True):
                    with self.subTest(parser_root=parser_root, status=status, early=early):
                        command = ("set -Eeuo pipefail; work='/synthetic/build scratch'; parser_root=''; compat_root=''; "
                                   "rm() { printf 'rm'; printf ' <%s>' \"$@\"; printf '\\n'; }; "
                                   "sudo() { printf 'sudo '; \"$@\"; };\n" + cleanup[0] + "\n" +
                                   "parser_root=" + shlex.quote(parser_root) + "; " +
                                   (f"(exit {status})" if early else f"exit {status}"))
                        process = subprocess.run(["bash", "--noprofile", "--norc", "-c", command],
                                                 capture_output=True, text=True, timeout=10)
                        self.assertEqual(process.returncode, status, process.stderr)
                        expected = "rm <-rf> <--> </synthetic/build scratch>\n"
                        if parser_root:
                            expected += "sudo rm <-rf> <--> <" + parser_root + ">\n"
                        self.assertEqual(process.stdout, expected + "cleanup_status=0\n")

    def test_privileged_paths_reject_workload_owned_inputs_and_ancestors(self):
        import types
        for uid, mode in ((1000, 0o100600), (0, 0o100666), (0, 0o40775)):
            with self.subTest(uid=uid, mode=mode), patch.object(Path, "lstat", return_value=types.SimpleNamespace(st_uid=uid, st_mode=mode)):
                with self.assertRaisesRegex(ValueError, "unsafe privileged input"):
                    HELPER.secure_path(Path("/synthetic/input"))
        def ownership(path):
            return types.SimpleNamespace(st_uid=1000 if path == Path("/synthetic") else 0, st_mode=0o100600)
        with patch.object(Path, "lstat", autospec=True, side_effect=ownership), self.assertRaisesRegex(ValueError, "/synthetic"):
            HELPER.secure_path(Path("/synthetic/input"))

    def test_selected_parser_pin_is_not_an_installed_package_requirement(self):
        for mode in ("pins", "parser-pin", "profile-pin", "verify-pins"):
            output = io.StringIO()
            with self.subTest(mode=mode), patch.object(sys, "argv", ["helper", mode]), contextlib.redirect_stdout(output), \
                 patch.object(HELPER, "installed", return_value=dict(HELPER.PINS, apparmor="4.0.1really4.0.1-0ubuntu0.24.04.8")), \
                 patch.object(subprocess, "run") as execute:
                HELPER.main()
                if mode == "pins":
                    self.assertNotIn("apparmor=", output.getvalue())
                    self.assertIn("libc6=" + HELPER.PINS["libc6"], output.getvalue())
                elif mode != "verify-pins":
                    self.assertEqual(output.getvalue().strip(), ("apparmor" if mode == "parser-pin" else "apparmor-profiles") + "=" + HELPER.PINS["apparmor"])
                execute.assert_not_called()

    def test_private_extraction_refuses_links_and_escaping_archive_members(self):
        for invalid in ("link", "escape"):
            output = io.BytesIO()
            with tarfile.open(fileobj=output, mode="w") as archive:
                member = tarfile.TarInfo("./sbin/apparmor_parser" if invalid == "link" else "./etc/apparmor.d/abi/../../outside")
                if invalid == "link":
                    member.type = tarfile.SYMTYPE
                    member.linkname = "/user-writable/parser"
                archive.addfile(member)
            with self.subTest(invalid=invalid), tempfile.TemporaryDirectory() as name, \
                 patch.object(subprocess, "check_output", return_value=output.getvalue()), patch.object(subprocess, "run") as execute:
                with self.assertRaises(ValueError):
                    HELPER.extract_parser_inputs(Path("synthetic.deb"), Path(name), "apparmor")
                self.assertEqual(list(Path(name).iterdir()), [])
                execute.assert_not_called()

    def test_policy_include_paths_and_syntax_fail_closed(self):
        for include in ('include <../outside>', 'include </outside>', 'include "tunables/global"', 'abi <abi/other>,'):
            with self.subTest(include=include), tempfile.TemporaryDirectory() as name, patch.object(HELPER, "secure_path"):
                private, base, _ = self.policy_fixture(Path(name))
                profile = private / "vendor-profile"
                profile.write_text(include + "\n")
                with self.assertRaises(ValueError):
                    HELPER.policy_closure(profile, base)

    def test_transaction_checks_complete_closure_and_pins(self):
        installed = dict(HELPER.PINS)
        del installed["meson"]
        plan = "Inst meson (1.3.2-1ubuntu1 Ubuntu:24.04/noble [all])\nInst python3-packaging (24.0-1 Ubuntu:24.04/noble [all])\n"
        self.assertEqual(HELPER.transaction(plan, installed), ["meson=1.3.2-1ubuntu1", "python3-packaging=24.0-1"])
        self.assertEqual(HELPER.transaction("", HELPER.PINS), [])

    def test_transaction_refuses_removal_downgrade_unknown_and_policy_changes(self):
        for plan, diagnostic in [
            ("Remv gcc-13 [13.3.0]", "removal refused"),
            ("Inst systemd (1 Ubuntu [amd64])", "incompatible package"),
            ("Inst apparmor (4.0.1 Ubuntu [amd64])", "incompatible package"),
            ("Inst ninja-build (9 Ubuntu [amd64])", "pin mismatch"),
            ("Inst ninja-build ???", "unknown apt transaction"),
            ("Inst meson (1.3.2-1ubuntu1 Ubuntu [all])\nInst meson (1.3.2-1ubuntu1 Ubuntu [all])", "duplicate apt change"),
        ]:
            with self.subTest(plan=plan), self.assertRaisesRegex(ValueError, diagnostic):
                HELPER.transaction(plan, HELPER.PINS)
        installed = dict(HELPER.PINS, meson="9.0")
        with patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0)), self.assertRaisesRegex(ValueError, "downgrade refused"):
            HELPER.transaction("Inst meson [9.0] (1.3.2-1ubuntu1 Ubuntu [all])", installed)
        for name in ("libc6",):
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "must already equal"):
                HELPER.transaction("", dict(HELPER.PINS, **{name: "old"}))
        with self.assertRaisesRegex(ValueError, "unsatisfied pin"):
            HELPER.transaction("", dict(HELPER.PINS, meson="old"))

    def test_both_image_versions_use_private_pinned_source_without_package_mutation(self):
        for revision in ("8", "9"):
            installed = dict(HELPER.PINS, apparmor="4.0.1really4.0.1-0ubuntu0.24.04." + revision)
            with self.subTest(revision=revision), patch.object(subprocess, "run") as execute:
                self.assertEqual(HELPER.transaction("", installed), [])
                with self.assertRaisesRegex(ValueError, "incompatible package"):
                    HELPER.transaction("Inst apparmor (" + HELPER.PINS["apparmor"] + " Ubuntu [amd64])", installed)
                execute.assert_not_called()
        self.assertEqual(HELPER.PINS["apparmor"], "4.0.1really4.0.1-0ubuntu0.24.04.9")
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        self.assertNotIn("sudo /usr/sbin/apparmor_parser", script)
        self.assertIn('stage-parser', script)
        # Ubuntu's initramfs mounts /run noexec; the staged parser must stay executable.
        self.assertNotIn("mktemp -d /run/", script)

    def test_offline_install_consumes_only_the_audited_archive_directory(self):
        # Execute only the production install statement with a synthetic sudo/
        # APT boundary. Never source the provisioner or run a real apt command.
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        statements = re.findall(r"^ +sudo env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get .+$", script, re.M)
        self.assertEqual(len(statements), 1)
        with tempfile.TemporaryDirectory(prefix="native-archive-test-") as name:
            work = Path(name) / "private scratch"
            archives = work / "debs"
            archives.mkdir(parents=True)
            debs = [archives / filename for filename in (
                "libcap-dev_1%3a2.66-5ubuntu2.4_amd64.deb", "meson_1.3.2-1ubuntu1_all.deb", "ninja-build_1.11.1-2_amd64.deb")]
            for path in debs:
                path.write_bytes(b"synthetic audited archive")
            fake_sudo = Path(name) / "sudo"
            fake_sudo.write_text(f"#!{sys.executable}\n" + '''import json, os, pathlib, sys
args = sys.argv[1:]
assert args[:4] == ["env", "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l", "apt-get"], args
assert all(flag in args for flag in ["-y", "--no-download", "--no-install-recommends", "--no-remove"]), args
assert not any(flag in args for flag in ["--ignore-missing", "--fix-missing", "--allow-unauthenticated", "--allow-downgrades"]), args
expected = pathlib.Path(os.environ["SYNTHETIC_APT_ARCHIVES"])
cache = None
for index, arg in enumerate(args):
    if arg == "-o" and args[index + 1].startswith("Dir::Cache::archives="):
        cache = pathlib.Path(args[index + 1].split("=", 1)[1])
packages = args[args.index("install") + 1:]
assert packages == json.loads(os.environ["SYNTHETIC_APT_PACKAGES"]), packages
if cache != expected or any(not (cache / pathlib.Path(package).name).is_file() for package in packages):
    sys.exit("E: Unable to fetch some archives, maybe run apt-get update or try with --fix-missing?")
print(json.dumps(args))
''')
            fake_sudo.chmod(0o700)
            command = f"set -Eeuo pipefail; work={shlex.quote(str(work))}; install_debs=(" + " ".join(shlex.quote(str(path)) for path in debs) + "); " + statements[0]
            environment = {"PATH": f"{name}:/usr/bin:/bin", "SYNTHETIC_APT_ARCHIVES": str(archives),
                           "SYNTHETIC_APT_PACKAGES": json.dumps([str(path) for path in debs])}
            process = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], env=environment,
                                     capture_output=True, text=True, timeout=10)
            self.assertEqual(process.returncode, 0, process.stderr)
            self.assertIn("Dir::Cache::archives=" + str(archives), json.loads(process.stdout))
            debs[0].unlink()
            missing = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], env=environment,
                                     capture_output=True, text=True, timeout=10)
            self.assertNotEqual(missing.returncode, 0)
            self.assertIn("E: Unable to fetch some archives", missing.stderr)

    def test_staged_archive_drift_refuses_before_installation(self):
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        checksum = '    sha256sum --check --strict "$evidence/package-archives.sha256"'
        self.assertIn(checksum, script)
        install = re.findall(r"^ +sudo env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get .+$", script, re.M)[0]
        self.assertLess(script.index(checksum), script.index(install))
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            deb = root / "meson_1.3.2-1ubuntu1_all.deb"
            deb.write_bytes(b"synthetic audited archive")
            (root / "package-archives.sha256").write_text(hashlib.sha256(deb.read_bytes()).hexdigest() + "  " + str(deb) + "\n")
            command = f"set -Eeuo pipefail; evidence={shlex.quote(name)}; work={shlex.quote(name)}; install_debs=(); sudo() {{ printf 'synthetic install\\n'; }}; " + checksum + "; " + install
            valid = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], capture_output=True, text=True, timeout=10)
            self.assertEqual(valid.returncode, 0, valid.stderr)
            self.assertIn("synthetic install", valid.stdout)
            deb.write_bytes(b"synthetic changed archive")
            changed = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], capture_output=True, text=True, timeout=10)
            self.assertNotEqual(changed.returncode, 0)
            self.assertIn("FAILED", changed.stdout)
            self.assertNotIn("synthetic install", changed.stdout)

    def test_privileged_observation_capture_keeps_user_writes_and_producer_failure(self):
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        captures = [line.strip() for line in script.splitlines() if line.strip().startswith("sudo ") and ' | tee "$evidence/' in line]
        self.assertEqual(len(captures), 6)
        self.assertIn("set -Eeuo pipefail", script)
        with tempfile.TemporaryDirectory() as name:
            for line in captures:
                destination = re.search(r'\$evidence/([^"\s]+)', line)
                self.assertIsNotNone(destination)
                for status in (0, 37):
                    command = f"set -Eeuo pipefail; evidence={shlex.quote(name)}; parser_root=synthetic; parser_deb=synthetic; profile_deb=synthetic; pins=(); install_debs=(); sudo() {{ printf 'synthetic observation\\n'; return {status}; }}; " + line
                    process = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], capture_output=True, text=True, timeout=10)
                    self.assertEqual(process.returncode, status, process.stderr)
                    self.assertEqual((Path(name) / destination[1]).read_text(), "synthetic observation\n")

    def test_probe_loop_variable_expands_only_in_the_workload_shell(self):
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        invocation = re.search(r'(?ms)^/usr/bin/bwrap --unshare-user .*?^ +> "\$evidence/installed-runtime.txt"$', script)
        self.assertIsNotNone(invocation)
        # Replace the absolute launcher token before evaluating shell quoting.
        # The callback only records argv; no launcher or workload is executed.
        capture = invocation[0].replace("/usr/bin/bwrap ", "capture_probe ", 1)
        with tempfile.TemporaryDirectory() as name:
            command = f"set -Eeuo pipefail; evidence={shlex.quote(name)}; n=outer-poison; capture_probe() {{ printf '%s\\0' \"$@\"; }}; " + capture
            process = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], capture_output=True, text=True, timeout=10)
            self.assertEqual(process.returncode, 0, process.stderr)
            args = (Path(name) / "installed-runtime.txt").read_bytes().split(b"\0")[:-1]
            self.assertEqual(args[-1].decode(), "cat /proc/self/attr/current; for n in user mnt pid net ipc uts; do readlink /proc/self/ns/$n; done")
            self.assertEqual(args[-3:-1], [b"/bin/sh", b"-ec"])

    def test_maintainer_policy_effects_and_unreviewed_triggers_refuse(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            for content in ["parse_profiles reload # apparmor", "sysctl -w kernel.foo=0", "systemctl restart service"]:
                (root / "postinst").write_text(content)
                with self.assertRaisesRegex(ValueError, "policy/service maintainer effect"):
                    HELPER.audit_control(root)
            (root / "postinst").write_text("#!/bin/sh\nset -e\nldconfig\n")
            (root / "triggers").write_text("activate-noawait ldconfig\n")
            HELPER.audit_control(root)
            (root / "triggers").write_text("interest /etc/apparmor.d\n")
            with self.assertRaisesRegex(ValueError, "policy/service maintainer effect"):
                HELPER.audit_control(root)
            (root / "triggers").write_text("activate arbitrary\n")
            with self.assertRaisesRegex(ValueError, "unapproved package trigger"):
                HELPER.audit_control(root)

    def test_compatibility_digest_refuses_missing_malformed_or_different_bytes(self):
        with tempfile.TemporaryDirectory() as name, patch.object(subprocess, "run") as execute:
            binary = Path(name) / "bwrap"
            binary.write_bytes(b"synthetic launcher; never execute")
            expected = hashlib.sha256(binary.read_bytes()).hexdigest()
            self.assertEqual(HELPER.verify_compatibility_digest(binary, expected), expected)
            for bad in ("", "A" * 64, "0" * 63, expected + "\n", "0" * 64):
                with self.subTest(digest=bad), self.assertRaisesRegex(ValueError, "compatibility digest"):
                    HELPER.verify_compatibility_digest(binary, bad)
            execute.assert_not_called()
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        self.assertLess(script.index('verify-compatibility-digest "$build/bwrap"'),
                        script.index('sudo install -o root -g root -m 0755 "$build/bwrap"'))
        self.assertNotIn("upstream-wrapper", script)

    def test_native_digest_statement_fails_before_mock_install(self):
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        verify = re.search(r'^python3 .*verify-compatibility-digest .+$', script, re.M)[0]
        install = re.search(r'^sudo install -o root -g root -m 0755 .*$', script, re.M)[0]
        with tempfile.TemporaryDirectory() as name:
            binary = Path(name) / "bwrap"
            binary.write_bytes(b"synthetic launcher; never execute")
            expected = hashlib.sha256(binary.read_bytes()).hexdigest()
            for value in (expected, "", "invalid", "0" * 64):
                command = ("set -Eeuo pipefail; " +
                    f"build={shlex.quote(name)}; evidence={shlex.quote(name)}; helper={shlex.quote(str(ROOT / 'scripts/ci-native-provision.py'))}; " +
                    f"CONCORD_UPSTREAM_BWRAP_SHA256={shlex.quote(value)}; sudo() {{ printf 'synthetic installation\\n'; }}; " +
                    verify + "; " + install)
                result = subprocess.run(["bash", "--noprofile", "--norc", "-c", command],
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode == 0, value == expected, result.stderr)
                self.assertEqual("synthetic installation" in result.stdout, value == expected)

    def test_compatibility_root_context_requires_actual_ids_label_and_capabilities(self):
        good = {"uids": [0, 0, 0], "gids": [0, 0, 0], "label": "unconfined",
                "capabilities": {key: "000001ffffffffff" for key in ("CapEff", "CapPrm", "CapBnd")},
                "userns": "user:[4026531837]"}
        HELPER.verify_root_context(good)
        for key, value in (("uids", [1001, 0, 0]), ("uids", [0, 1001, 0]), ("gids", [0, 1001, 0]),
                           ("label", "named-unconfined (unconfined)"), ("label", "bwrap (enforce)"),
                           ("capabilities", {"CapEff": "0", "CapPrm": "0", "CapBnd": "0"}), ("userns", "")):
            with self.subTest(key=key, value=value), self.assertRaisesRegex(ValueError, "root compatibility"):
                HELPER.verify_root_context(dict(good, **{key: value}))

    def test_compatibility_source_authentication_rejects_changed_extra_and_link_inputs(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            archive = root / "source.tar"
            data = self.package_tar({"bubblewrap-0.12.0/tests/meson.build": b"pinned test inventory\n"})
            archive.write_bytes(data)
            source = root / "bubblewrap-0.12.0"
            (source / "tests").mkdir(parents=True)
            target = source / "tests/meson.build"
            target.write_bytes(b"pinned test inventory\n")
            with patch.object(HELPER, "SOURCE_DIGEST", hashlib.sha256(data).hexdigest()):
                HELPER.verify_upstream_source(archive, source)
                target.write_bytes(b"changed")
                with self.assertRaisesRegex(ValueError, "source"):
                    HELPER.verify_upstream_source(archive, source)
                target.write_bytes(b"pinned test inventory\n")
                extra = source / "extra-hook.py"
                extra.write_text("must not execute")
                with self.assertRaisesRegex(ValueError, "source"):
                    HELPER.verify_upstream_source(archive, source)
                extra.unlink()
                target.unlink()
                target.symlink_to(archive)
                with self.assertRaisesRegex(ValueError, "source"):
                    HELPER.verify_upstream_source(archive, source)

    @staticmethod
    def source_tar(links):
        # The pinned release archive carries COPYING.LIB and LICENSE as
        # relative symlinks to COPYING; this fixture keeps that shape.
        output = io.BytesIO()
        with tarfile.open(fileobj=output, mode="w") as archive:
            data = b"license text\n"
            member = tarfile.TarInfo("bubblewrap-0.12.0/COPYING")
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))
            for name, target in links.items():
                member = tarfile.TarInfo("bubblewrap-0.12.0/" + name)
                member.type, member.linkname = tarfile.SYMTYPE, target
                archive.addfile(member)
        return output.getvalue()

    def test_compatibility_source_authentication_binds_pinned_in_tree_file_links(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            archive = root / "source.tar"
            source = root / "bubblewrap-0.12.0"
            source.mkdir()
            (source / "COPYING").write_bytes(b"license text\n")
            data = self.source_tar({"COPYING.LIB": "COPYING", "LICENSE": "COPYING"})
            archive.write_bytes(data)
            for link in ("COPYING.LIB", "LICENSE"):
                (source / link).symlink_to("COPYING")
            with patch.object(HELPER, "SOURCE_DIGEST", hashlib.sha256(data).hexdigest()):
                HELPER.verify_upstream_source(archive, source)
                (source / "LICENSE").unlink()
                (source / "LICENSE").write_bytes(b"license text\n")
                with self.assertRaisesRegex(ValueError, "source"):
                    HELPER.verify_upstream_source(archive, source)
                (source / "LICENSE").unlink()
                (source / "LICENSE").symlink_to(str(source / "COPYING"))
                with self.assertRaisesRegex(ValueError, "source"):
                    HELPER.verify_upstream_source(archive, source)
            for target in ("/etc/hostname", "../COPYING", "missing", "COPYING.LIB"):
                data = self.source_tar({"COPYING.LIB": "COPYING", "LICENSE": target})
                archive.write_bytes(data)
                (source / "LICENSE").unlink()
                (source / "LICENSE").symlink_to(target)
                with self.subTest(target=target), patch.object(HELPER, "SOURCE_DIGEST", hashlib.sha256(data).hexdigest()), \
                     self.assertRaisesRegex(ValueError, "source archive"):
                    HELPER.verify_upstream_source(archive, source)

    def test_compatibility_inputs_bind_source_file_links_and_refuse_other_links(self):
        real_rglob = Path.rglob
        system = {Path("/usr/lib/python3/dist-packages/mesonbuild"), Path("/usr/lib/python3.12")}
        with tempfile.TemporaryDirectory() as name, \
             patch.object(HELPER, "secure_path"), patch.object(HELPER, "runtime_files", return_value=[]), \
             patch.object(HELPER, "identity", side_effect=lambda path: {"resolved": str(path.resolve())}), \
             patch.object(HELPER.shutil, "which", side_effect=lambda tool, path: "/synthetic/" + tool), \
             patch.object(HELPER.os.path, "lexists", return_value=False), \
             patch.object(Path, "rglob", autospec=True,
                          side_effect=lambda path, pattern: [] if path in system else real_rglob(path, pattern)):
            work = Path(name)
            source = work / "bubblewrap-0.12.0"
            (work / "build").mkdir()
            (source / "tests").mkdir(parents=True)
            (work / "native.ini").write_text("synthetic\n")
            (work / "bubblewrap-0.12.0.tar.xz").write_bytes(b"synthetic")
            (source / "COPYING").write_bytes(b"license text\n")
            (source / "LICENSE").symlink_to("COPYING")
            records = HELPER.compatibility_inputs(work)
            self.assertEqual(records[str(source / "LICENSE")], {"resolved": str(source / "COPYING")})
            self.assertIn("/synthetic/strace", records)
            with patch.object(HELPER.shutil, "which", side_effect=lambda tool, path: None if tool == "strace" else "/synthetic/" + tool):
                self.assertNotIn("/synthetic/strace", HELPER.compatibility_inputs(work))
            for link, target in (("bubblewrap-0.12.0/escape", "/etc"), ("bubblewrap-0.12.0/directory", "tests"),
                                 ("build/bwrap-link", "../bubblewrap-0.12.0/COPYING")):
                with self.subTest(link=link):
                    (work / link).symlink_to(target)
                    with self.assertRaisesRegex(ValueError, "compatibility input"):
                        HELPER.compatibility_inputs(work)
                    (work / link).unlink()

    @staticmethod
    def compatibility_inventory(work):
        names = ("test-utils", "test-run.sh", "test-sandbox.py", "test-seccomp.py",
                 "test-specifying-pidns.sh", "test-specifying-userns.sh")
        return [{"name": name, "cmd": [str(work / "build/tests/test-utils")] if name == "test-utils" else
                 (["/usr/bin/python3.12"] if name.endswith(".py") else ["/usr/bin/bash"]) +
                 [str(work / "bubblewrap-0.12.0/tests" / name)],
                 "env": {"BWRAP": str(work / "build/bwrap"), "G_TEST_BUILDDIR": str(work / "build/tests/.."),
                         "G_TEST_SRCDIR": str(work / "bubblewrap-0.12.0/tests/..")},
                 "protocol": "tap", "workdir": None, "timeout": 30, "suite": ["bubblewrap"]} for name in names]

    def test_compatibility_inventory_and_results_keep_all_six_and_native_skips(self):
        work = Path("/synthetic/work")
        inventory = self.compatibility_inventory(work)
        HELPER.verify_upstream_inventory(inventory, work)
        for change in ("missing", "duplicate", "wrapper", "environment", "timeout"):
            altered = json.loads(json.dumps(inventory))
            if change == "missing":
                altered.pop()
            elif change == "duplicate":
                altered.append(altered[0])
            elif change == "wrapper":
                altered[1]["cmd"].insert(0, "/arbitrary/hook")
            elif change == "environment":
                altered[1]["env"]["BWRAP"] = "/usr/bin/bwrap"
            else:
                altered[1]["timeout"] = 0
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, "inventory"):
                HELPER.verify_upstream_inventory(altered, work)
        records = [{"name": item["name"], "command": item["cmd"], "env": dict(item["env"], BWRAP_MUST_WORK="1"),
                    "result": "OK", "returncode": 0, "stdout": "1..2\nok 1 positive\nok 2 # SKIP optional FUSE\n"}
                   for item in inventory]
        records[3].update(result="SKIP", stdout="1..0 # SKIP cannot import seccomp Python module\n")
        summary = HELPER.verify_upstream_results(records, inventory)
        self.assertEqual(summary["test-run.sh"], {"records": 2, "passed": 1, "skips": ["ok 2 # SKIP optional FUSE"]})
        for bad in ("incomplete", "whole-program", "all-skipped", "not-ok", "missing-env", "wrong-command", "missing-program"):
            altered = json.loads(json.dumps(records))
            if bad == "missing-program":
                altered.pop()
            elif bad == "missing-env":
                altered[0]["env"].pop("BWRAP_MUST_WORK")
            elif bad == "wrong-command":
                altered[0]["command"].insert(0, "/wrapper")
            else:
                altered[0]["stdout"] = {"incomplete": "ok 1 positive\n", "whole-program": "1..0 # SKIP no bwrap\n",
                    "all-skipped": "1..1\nok 1 # SKIP bwrap unavailable\n", "not-ok": "1..1\nnot ok 1 failed\n"}[bad]
            with self.subTest(result=bad), self.assertRaisesRegex(ValueError, "compatibility result"):
                HELPER.verify_upstream_results(altered, inventory)

    def test_workflow_separates_compatibility_and_binds_every_native_build(self):
        text = (ROOT / ".github/workflows/ci.yml").read_text()
        jobs = dict(re.findall(r"^  ([a-z][a-z0-9-]+):\n(.*?)(?=^  [a-z][a-z0-9-]+:|\Z)", text, re.M | re.S))
        self.assertIn("verify-upstream-compatibility", jobs)
        compatibility = jobs["verify-upstream-compatibility"]
        for required in ("runs-on: ubuntu-24.04", "contents: read", "persist-credentials: false", "timeout-minutes:",
                         "bwrap-sha256: ${{ steps.compatibility.outputs.bwrap-sha256 }}",
                         "run: bash scripts/ci-provision-native-isolation.sh --upstream-compatibility", "if: always()"):
            self.assertIn(required, compatibility)
        for forbidden in ("id-token:", "secrets:", "setup-bun", "setup-node", "go test", "warm-native", "--wrapper"):
            self.assertNotIn(forbidden, compatibility)
        native = {name: body for name, body in jobs.items() if "Warm the uv fixture cache" in body}
        self.assertEqual(len(native), 11)
        for name, body in native.items():
            with self.subTest(job=name):
                self.assertIn("needs: [verify-upstream-compatibility]", body)
                self.assertIn("CONCORD_UPSTREAM_BWRAP_SHA256: ${{ needs.verify-upstream-compatibility.outputs.bwrap-sha256 }}", body)
        for aggregate in ("verify", "verify-tests", "verify-adapter"):
            self.assertIn("needs.verify-upstream-compatibility.result", jobs[aggregate])
            self.assertIn('test "$COMPATIBILITY_RESULT" = success', jobs[aggregate])

    def test_compatibility_supervisor_captures_status_and_always_checks_descendants(self):
        for outcome in (0, 37, subprocess.TimeoutExpired("meson", 300), OSError("mock spawn failure")):
            with self.subTest(outcome=outcome), tempfile.TemporaryDirectory() as name:
                root = Path(name)
                work = root / "work"
                work.mkdir()
                marker = root / "cleanup-safe"
                marker.write_text("before tests")
                process = Mock()
                process.wait.side_effect = outcome if isinstance(outcome, Exception) else None
                process.wait.return_value = outcome
                report = {}
                def reap(record):
                    record.update(descendants_gone=True, terminated_children=[])
                with patch.object(HELPER, "enable_subreaper"), patch.object(HELPER, "direct_children", return_value=[]), \
                     patch.object(HELPER.resource, "setrlimit") as limit, patch.object(HELPER.signal, "signal"), \
                     patch.object(HELPER, "reap_compatibility_children", side_effect=reap) as cleanup, \
                     patch.object(subprocess, "Popen", return_value=process) as spawn:
                    if isinstance(outcome, OSError):
                        spawn.side_effect = outcome
                    if outcome == 0:
                        HELPER.supervise_compatibility(["synthetic-meson"], work, {"BWRAP_MUST_WORK": "1"}, report)
                    else:
                        with self.assertRaises((ValueError, OSError, subprocess.TimeoutExpired)):
                            HELPER.supervise_compatibility(["synthetic-meson"], work, {"BWRAP_MUST_WORK": "1"}, report)
                    cleanup.assert_called_once_with(report)
                    self.assertTrue(marker.is_file())
                    self.assertTrue(report["descendants_gone"])
                    limit.assert_called_once()
                    self.assertEqual(spawn.call_args.kwargs["env"], {"BWRAP_MUST_WORK": "1"})
                    self.assertEqual(spawn.call_args.kwargs["cwd"], work)
                    if not isinstance(outcome, OSError):
                        process.wait.assert_called_once_with(timeout=300)

    def test_compatibility_reaps_only_its_adopted_children_and_refuses_unfinished_cleanup(self):
        report = {}
        with patch.object(os, "waitpid", side_effect=[(0, 0), (201, 9), (0, 0), (202, 9), ChildProcessError()]), \
             patch.object(HELPER, "direct_children", side_effect=[[201], [202]]), \
             patch.object(os, "pidfd_open", side_effect=[41, 42]) as opened, patch.object(os, "close") as closed, \
             patch.object(HELPER.signal, "pidfd_send_signal") as killed, patch.object(HELPER.time, "sleep"):
            HELPER.reap_compatibility_children(report)
            self.assertEqual([call.args[0] for call in opened.call_args_list], [201, 202])
            self.assertEqual([call.args for call in killed.call_args_list], [(41, HELPER.signal.SIGKILL), (42, HELPER.signal.SIGKILL)])
            self.assertEqual(closed.call_count, 2)
            self.assertTrue(report["descendants_gone"])
        with patch.object(os, "waitpid", return_value=(0, 0)), patch.object(HELPER, "direct_children", return_value=[203]), \
             patch.object(HELPER.time, "monotonic", side_effect=[0, 11]), patch.object(os, "pidfd_open") as opened:
            with self.assertRaisesRegex(ValueError, "cleanup refused"):
                HELPER.reap_compatibility_children({})
            opened.assert_not_called()

    def test_compatibility_cleanup_cannot_hide_failure_or_delete_unproved_root_tree(self):
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        cleanup = re.search(r"(?ms)^cleanup\(\) \{\n.*?^\}\ntrap cleanup EXIT$", script)[0]
        for safe, removed, status in ((True, True, 0), (True, False, 0), (False, True, 0), (True, True, 37)):
            command = ("set -Eeuo pipefail; work=synthetic; parser_root=''; compat_root='/synthetic/root'; " +
                       "sudo() { if [[ $1 == test ]]; then return " + str(0 if safe else 1) + "; fi; " +
                       "printf 'bounded removal <%s>\\n' \"$3\"; return " + str(0 if removed else 1) + "; };\n" +
                       cleanup + f"\nexit {status}")
            result = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, status if safe and removed else 1, result.stderr)
            self.assertEqual("bounded removal" in result.stdout, safe)

    def test_compatibility_pipeline_retains_output_and_privileged_producer_failure(self):
        script = (ROOT / "scripts/ci-provision-native-isolation.sh").read_text()
        invocation = re.search(r'(?m)^    sudo -n -u root -g root -- env -i .*\\\n.*qualification.json.*$', script)
        self.assertIsNotNone(invocation)
        with tempfile.TemporaryDirectory() as name:
            for status in (0, 37):
                command = (f"set -Eeuo pipefail; evidence={shlex.quote(name)}; compat_root=synthetic; work=synthetic; " +
                           f"sudo() {{ printf 'synthetic root observation\\n'; return {status}; }}; " + invocation[0])
                result = subprocess.run(["bash", "--noprofile", "--norc", "-c", command], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, status, result.stderr)
                self.assertEqual((Path(name) / "qualification.json").read_text(), "synthetic root observation\n")

    def test_compatibility_failure_still_records_post_state_and_native_logs(self):
        work = Path("/var/lib/concord-upstream.synthetic/work")
        inventory = self.compatibility_inventory(work)
        with tempfile.TemporaryDirectory() as name:
            log = Path(name) / "testlog.json"
            log.write_text('{"native": "failure retained"}\n')
            output = io.StringIO()
            def read_text(path, *args, **kwargs):
                self.assertEqual(path, work / "build/meson-info/intro-tests.json")
                return json.dumps(inventory)
            real_read = Path.read_text
            with patch.object(HELPER, "secure_path"), patch.object(os, "geteuid", return_value=0), \
                 patch.object(HELPER, "compatibility_host_state", side_effect=[{"sysctls": "before"}, {"sysctls": "drift"}]), \
                 patch.object(HELPER, "compatibility_temp_entries", return_value={}), \
                 patch.object(HELPER, "verify_root_context"), patch.object(HELPER, "verify_upstream_source", return_value={}), \
                 patch.object(HELPER, "compatibility_inputs", return_value={"source": "bound"}), \
                 patch.object(HELPER, "identity", return_value={"sha256": "a" * 64}), \
                 patch.object(HELPER, "supervise_compatibility", side_effect=ValueError("synthetic native failure")), \
                 patch.object(Path, "unlink") as unlink, \
                 patch.object(Path, "glob", return_value=[log]), \
                 patch.object(Path, "read_text", autospec=True, side_effect=lambda path, *a, **k: real_read(path, *a, **k) if path == log else read_text(path)), \
                 contextlib.redirect_stdout(output):
                with self.assertRaisesRegex(ValueError, "compatibility failed"):
                    HELPER.upstream_compatibility(work)
            report = json.loads(output.getvalue())
            self.assertIsInstance(report["started_unix_seconds"], int)
            self.assertGreaterEqual(report["finished_unix_seconds"], report["started_unix_seconds"])
            self.assertEqual(report["after_inputs"], report["before_inputs"])
            self.assertIn("synthetic native failure", report["errors"])
            self.assertIn("unexplained compatibility drift: after_host", report["errors"])
            self.assertEqual(report["logs"][str(log)], '{"native": "failure retained"}\n')
            unlink.assert_called_once_with(missing_ok=True)

    def test_crash_diagnostics_use_only_unprivileged_bounded_metadata(self):
        report = {"started_unix_seconds": 100, "finished_unix_seconds": 110, "errors": ["host drift"]}
        row = {"COREDUMP_EXE": "/synthetic/bwrap", "COREDUMP_PID": "123", "COREDUMP_SIGNAL": "11",
               "COREDUMP_ENVIRON": "secret", "COREDUMP_PROC_MAPS": "secret", "MESSAGE": "secret",
               "COREDUMP": "secret", "COREDUMP_CMDLINE": "secret"}
        process = subprocess.CompletedProcess([], 0, json.dumps(row) + "\n", "")
        with patch.object(os, "geteuid", return_value=1000), patch.object(subprocess, "run", return_value=process) as run:
            result = HELPER.compatibility_crash_metadata(report)
        self.assertEqual(result["records"], [{"COREDUMP_EXE": "/synthetic/bwrap", "COREDUMP_PID": "123", "COREDUMP_SIGNAL": "11"}])
        self.assertNotIn("secret", json.dumps(result))
        self.assertEqual(report["errors"], ["host drift"])
        argv = run.call_args.args[0]
        self.assertEqual(argv[0], "/usr/bin/journalctl")
        for flag in ("--since=@100", "--until=@111", "--lines=256", "--no-pager", "--output=json"):
            self.assertIn(flag, argv)
        self.assertNotIn("sudo", argv)
        self.assertIn("MESSAGE_ID=fc2e22bc6ee647b6b90729ab34a250b1", argv)
        self.assertEqual(run.call_args.kwargs["timeout"], 10)
        self.assertNotIn("COREDUMP_ENVIRON", " ".join(argv))
        with patch.object(os, "geteuid", return_value=0), patch.object(subprocess, "run") as run:
            with self.assertRaisesRegex(ValueError, "unprivileged"):
                HELPER.compatibility_crash_metadata(report)
            run.assert_not_called()
        process.stdout = (json.dumps(row) + "\n") * 256
        with patch.object(os, "geteuid", return_value=1000), patch.object(subprocess, "run", return_value=process):
            self.assertEqual(HELPER.compatibility_crash_metadata(report)["availability"], "possibly truncated")
        for start, end in ((None, 110), (True, 110), (100, 99), (100, 701)):
            with self.subTest(start=start, end=end), patch.object(os, "geteuid", return_value=1000), \
                 patch.object(subprocess, "run") as run:
                result = HELPER.compatibility_crash_metadata({"started_unix_seconds": start, "finished_unix_seconds": end})
                self.assertEqual(result["availability"], "unavailable")
                run.assert_not_called()

    def test_crash_diagnostics_never_turn_missing_evidence_into_success(self):
        report = {"started_unix_seconds": 100, "finished_unix_seconds": 110}
        for process in (subprocess.CompletedProcess([], 1, "", "private error"),
                        subprocess.CompletedProcess([], 0, "", "permission warning"),
                        subprocess.CompletedProcess([], 0, "not json", ""),
                        subprocess.TimeoutExpired("journalctl", 10), FileNotFoundError("private path")):
            with self.subTest(process=type(process).__name__), patch.object(os, "geteuid", return_value=1000), \
                 patch.object(subprocess, "run") as run:
                if isinstance(process, Exception):
                    run.side_effect = process
                else:
                    run.return_value = process
                result = HELPER.compatibility_crash_metadata(report)
                self.assertNotEqual(result["availability"], "captured")
                self.assertEqual(result["records"], [])
                self.assertNotIn("private", json.dumps(result))
        text = (ROOT / ".github/workflows/ci.yml").read_text()
        self.assertIn('python3 scripts/ci-native-provision.py compatibility-crash-metadata', text)

    def test_crash_diagnostic_cli_retains_unavailable_for_missing_or_malformed_report(self):
        for data in (FileNotFoundError("private path"), "", "not json", "[]", "null"):
            output = io.StringIO()
            with self.subTest(data=type(data).__name__), patch.object(os, "geteuid", return_value=1000), \
                 patch.object(sys, "argv", ["helper", "compatibility-crash-metadata", "synthetic"]), \
                 patch.object(Path, "read_text") as read, patch.object(subprocess, "run") as run, \
                 contextlib.redirect_stdout(output):
                if isinstance(data, Exception):
                    read.side_effect = data
                else:
                    read.return_value = data
                HELPER.main()
                result = json.loads(output.getvalue())
                self.assertEqual(result["availability"], "unavailable")
                self.assertEqual(result["records"], [])
                self.assertNotIn("private", output.getvalue())
                run.assert_not_called()

    def test_runtime_fingerprints_include_loader_and_symlink_target(self):
        with patch.object(subprocess, "check_output", return_value="libcap.so.2 => /lib/libcap.so.2 (0x1)\n/lib/ld.so (0x2)\n"):
            self.assertEqual(HELPER.runtime_files("/usr/bin/bwrap"), ["/lib/ld.so", "/lib/libcap.so.2", "/usr/bin/bwrap"])
        with patch.object(subprocess, "check_output", return_value="libcap.so.2 => not found"), self.assertRaisesRegex(ValueError, "incomplete installed runtime"):
            HELPER.runtime_files("/usr/bin/bwrap")
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            (root / "binary").write_bytes(b"consumed bytes")
            (root / "link").symlink_to("binary")
            import contextlib
            import io
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                HELPER.fingerprint_files([str(root / "link")])
            self.assertIn('"link": "binary"', output.getvalue())
            self.assertIn('"sha256":', output.getvalue())

    def test_installed_smoke_requires_observed_profile_and_all_private_namespaces(self):
        names = ("user", "mnt", "pid", "net", "ipc", "uts")
        host = "\n".join(f"{name}:[1]" for name in names)
        isolated = "bwrap//&unpriv_bwrap (enforce)\n" + "\n".join(f"{name}:[2]" for name in names)
        HELPER.verify_smoke(host, isolated)
        with self.assertRaisesRegex(ValueError, "private net namespace"):
            HELPER.verify_smoke(host, isolated.replace("net:[2]", "net:[1]"))
        for bad in ["unconfined\n" + isolated.split("\n", 1)[1], isolated.replace("(enforce)", "(complain)"), isolated + "\nextra"]:
            with self.subTest(observation=bad), self.assertRaisesRegex(ValueError, "incomplete installed"):
                HELPER.verify_smoke(host, bad)

    def test_host_guard_refuses_before_any_provisioning(self):
        for args in ([], ["--upstream-compatibility"]):
            process = subprocess.run(["bash", str(ROOT / "scripts/ci-provision-native-isolation.sh"), *args],
                                     env={"PATH": os.environ["PATH"]}, capture_output=True, text=True, timeout=10)
            self.assertNotEqual(process.returncode, 0)
            self.assertIn("restricted to explicitly authorized disposable", process.stderr)

    def test_safe_warming_unit_tests(self):
        subprocess.run(["bun", "test", "./scripts/ci-warm-native-npm.test.ts", "--max-concurrency=1"],
                       cwd=ROOT, check=True, timeout=60)

    def test_shell_syntax_and_shellcheck(self):
        script = str(ROOT / "scripts/ci-provision-native-isolation.sh")
        subprocess.run(["bash", "-n", script], check=True, timeout=10)
        if not shutil.which("shellcheck"):
            if os.environ.get("GITHUB_ACTIONS") == "true":
                self.fail("ShellCheck is required on disposable CI")
            self.skipTest("ShellCheck is not installed; CI must run it")
        subprocess.run(["shellcheck", script], check=True, timeout=30)

    def test_workflow_keeps_every_uv_warming_owner_qualified(self):
        # GitHub's job format is parsed without adding a repository dependency.
        # Ruby/Python YAML variants disagree about the top-level `on` key; only
        # jobs are inspected here. The repository workflow validator owns YAML.
        text = (ROOT / ".github/workflows/ci.yml").read_text()
        jobs = {}
        for match in re.finditer(r"^  ([a-z][a-z0-9-]+):\n(.*?)(?=^  [a-z][a-z0-9-]+:|\Z)", text, re.M | re.S):
            jobs[match[1]] = match[2]
        owners = {name: body for name, body in jobs.items() if "Warm the uv fixture cache" in body}
        self.assertEqual(len(owners), 11)
        for name, body in owners.items():
            with self.subTest(job=name):
                # runner is unavailable in job-level env. Resolve its paths in
                # a step and publish them for subsequent steps via GITHUB_ENV.
                self.assertNotIn("${{ runner.", body.split("    steps:", 1)[0])
                for required in ["runs-on: ubuntu-24.04", "OC_NATIVE_ISOLATION_REQUIRED: \"1\"",
                                 "printf 'OC_NATIVE_NPM_CACHE=%s/native-target-npm-cache\\n' \"$RUNNER_TEMP\" >> \"$GITHUB_ENV\"",
                                 "printf 'OC_NATIVE_ISOLATION_EVIDENCE=%s/native-isolation-evidence\\n' \"$RUNNER_TEMP\" >> \"$GITHUB_ENV\"",
                                 "node-version: 24.15.0", "architecture: x64",
                                 "check-latest: false", "package-manager-cache: false", "bun-version: 1.4.0",
                                 "actions/setup-node@249970729cb0ef3589644e2896645e5dc5ba9c38",
                                 "run: bash scripts/ci-provision-native-isolation.sh",
                                 "run: bun scripts/ci-warm-native-npm.ts", "native-isolation-${{ github.job }}"]:
                    self.assertIn(required, body)


if __name__ == "__main__":
    unittest.main(verbosity=2)
