#!/usr/bin/env python3
"""Safe CI planning/guard tests; never invoke real privileged tools."""
from __future__ import annotations

import importlib.util
import contextlib
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
from unittest.mock import patch


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
''' + HOME_DEFAULT + b'''EOM
        fi
        mkdir -p /etc/apparmor.d/tunables/home.d 2>/dev/null || true
        mv -Z -f "$tmp" /etc/apparmor.d/tunables/home.d/ubuntu
        chmod 644 /etc/apparmor.d/tunables/home.d/ubuntu

        if [ ! -e /etc/apparmor.d/tunables/xdg-user-dirs.d/site.local ]; then
            tmp=`mktemp`
            cat > "$tmp" <<EOM
''' + XDG_DEFAULT + b'''EOM

            mkdir -p /etc/apparmor.d/tunables/xdg-user-dirs.d 2>/dev/null || true
            mv -Z -n "$tmp" /etc/apparmor.d/tunables/xdg-user-dirs.d/site.local
            chmod 644 /etc/apparmor.d/tunables/xdg-user-dirs.d/site.local
        fi
''')


class ProvisionTests(unittest.TestCase):
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
            (directory / "tunables/home.d/site").write_text("@{HOME}=/home/*\n")
        if generated_defaults:
            (base / "tunables/home.d/ubuntu").write_bytes(HOME_HEADER + HOME_DEFAULT)
            (base / "tunables/xdg-user-dirs.d/site.local").write_bytes(XDG_DEFAULT)
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
                    self.assertFalse((private / "base/tunables/xdg-user-dirs.d/site.local").exists())
                    HELPER.stage_parser(private, archives, base)
                    self.assertEqual((private / "base/tunables/home.d/ubuntu").read_bytes(), HOME_HEADER + HOME_DEFAULT)
                    self.assertEqual((private / "base/tunables/xdg-user-dirs.d/site.local").read_bytes(), XDG_DEFAULT)
                    self.assertFalse((private / "base/unrelated-policy").exists())
                    self.assertEqual([call.args[0][:2] for call in execute.call_args_list], [["dpkg-deb", "-e"]] * 2)
                    manifest = json.loads((private / "manifest.json").read_text())
                    self.assertEqual({source["Version"] for source in manifest["sources"]}, {HELPER.PINS["apparmor"]})
                    for retained in ("apparmor-control/postinst", "base/tunables/home.d/ubuntu", "base/tunables/xdg-user-dirs.d/site.local"):
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
                                 private / "base/tunables/xdg-user-dirs.d/site.local", base / "tunables/home.d/ubuntu",
                                 base / "tunables/xdg-user-dirs.d/site.local", base / "tunables/home.d/site", base / "bwrap-userns-restrict"):
                        original = path.read_bytes()
                        path.write_bytes(original + b" drift")
                        execute.reset_mock()
                        with self.subTest(drift=path.name), self.assertRaises(ValueError):
                            HELPER.parser_effect(private, "load", base)
                        execute.assert_not_called()
                        path.write_bytes(original)
                    for path in (private / "apparmor-control/postinst", private / "base/tunables/home.d/ubuntu", private / "base/tunables/xdg-user-dirs.d/site.local",
                                 base / "tunables/home.d/ubuntu", base / "tunables/xdg-user-dirs.d/site.local"):
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
                self.assertFalse((private / "base/tunables/xdg-user-dirs.d/site.local").exists())
                self.assertEqual(installed, {str(path): path.read_bytes() for path in base.rglob("*") if path.is_file()})
                execute.assert_not_called()
                output.assert_not_called()

    def test_private_default_derivation_refuses_collisions_and_missing_control(self):
        for generated in ("tunables/home.d/ubuntu", "tunables/xdg-user-dirs.d/site.local"):
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
                   ("tunables/xdg-user-dirs.d/site.local", b'@{XDG_MUSIC_DIR}+="Other"\n'),
                   ("tunables/home.d/extra", b"# even comment-only extras refuse\n"),
                   ("tunables/xdg-user-dirs.d/extra", b"# extra\n"),
                   ("tunables/home.d/ubuntu", None), ("tunables/xdg-user-dirs.d/site.local", None))
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
        for bad in (None, "dependency", "search", "interpreter", "machine", "resolution"):
            def command(args, **kwargs):
                if args == ["/usr/sbin/ldconfig", "-p"]:
                    return ("\tlibc.so.6 (libc6,x86-64) => /lib/x86_64-linux-gnu/libc.so.6\n" * (2 if bad == "resolution" else 1) +
                            "\tld-linux-x86-64.so.2 (libc6,x86-64) => /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2\n")
                self.assertEqual(args[0], "readelf")
                if args[1] == "-hW":
                    return "Class: ELF64\nMachine: " + ("AArch64" if bad == "machine" else "Advanced Micro Devices X86-64")
                if args[1] == "-lW":
                    return "[Requesting program interpreter: " + ("/unexpected" if bad == "interpreter" else "/lib64/ld-linux-x86-64.so.2") + "]" if args[2] == "/private/parser" else ""
                if args[2] == "/lib/x86_64-linux-gnu/libc.so.6":
                    # glibc's libc.so.6 itself records the loader as a NEEDED entry.
                    return "(NEEDED) Shared library: [ld-linux-x86-64.so.2]"
                if args[2] != "/private/parser":
                    return ""
                return ("(RPATH) [/tmp]\n" if bad == "search" else "") + "(NEEDED) [" + ("libunapproved.so" if bad == "dependency" else "libc.so.6") + "]"
            with self.subTest(bad=bad), patch.object(HELPER, "identity", return_value={}), \
                 patch.object(subprocess, "check_output", side_effect=command), patch.object(subprocess, "run") as execute:
                if bad:
                    with self.assertRaisesRegex(ValueError, "unsupported parser"):
                        HELPER.parser_runtime(Path("/private/parser"))
                else:
                    paths, linkage = HELPER.parser_runtime(Path("/private/parser"))
                    self.assertEqual(paths, ["/etc/ld.so.cache", "/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2", "/lib/x86_64-linux-gnu/libc.so.6",
                                             "/lib64/ld-linux-x86-64.so.2", "/private/parser"])
                    self.assertEqual(len(linkage), 4)
                execute.assert_not_called()

    def test_privileged_paths_reject_workload_owned_inputs_and_ancestors(self):
        import types
        for uid, mode in ((1000, 0o100600), (0, 0o100666), (0, 0o40775)):
            with self.subTest(uid=uid, mode=mode), patch.object(Path, "lstat", return_value=types.SimpleNamespace(st_uid=uid, st_mode=mode)):
                with self.assertRaisesRegex(ValueError, "unsafe privileged input"):
                    HELPER.secure_path(Path("/private/input"))
        def ownership(path):
            return types.SimpleNamespace(st_uid=1000 if path == Path("/private") else 0, st_mode=0o100600)
        with patch.object(Path, "lstat", autospec=True, side_effect=ownership), self.assertRaisesRegex(ValueError, "/private"):
            HELPER.secure_path(Path("/private/input"))

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

    def test_upstream_wrapper_changes_only_bwrap_after_meson_injection(self):
        with patch.dict(os.environ, {"BWRAP": "/build/bwrap", "UNCHANGED": "value"}, clear=True), patch.object(os, "execvpe") as execute:
            HELPER.upstream_wrapper("/build", ["/usr/bin/bash", "test-run.sh", "literal"])
            execute.assert_called_once_with("/usr/bin/bash", ["/usr/bin/bash", "test-run.sh", "literal"], os.environ)
            self.assertEqual(dict(os.environ), {"BWRAP": "/usr/bin/bwrap", "UNCHANGED": "value"})
        with patch.dict(os.environ, {"BWRAP": "/unexpected"}, clear=True), self.assertRaisesRegex(ValueError, "unexpected upstream"):
            HELPER.upstream_wrapper("/build", ["test"])

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
        process = subprocess.run(["bash", str(ROOT / "scripts/ci-provision-native-isolation.sh")],
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
