#!/usr/bin/env python3
"""Safe tests of CI planning/guards; never provision or invoke sudo."""
from __future__ import annotations

import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("ci_native_provision", ROOT / "scripts/ci-native-provision.py")
assert SPEC and SPEC.loader
HELPER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HELPER)


class ProvisionTests(unittest.TestCase):
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
        with self.assertRaisesRegex(ValueError, "downgrade refused"):
            HELPER.transaction("Inst meson [9.0] (1.3.2-1ubuntu1 Ubuntu [all])", installed)
        for name in ("apparmor", "libc6"):
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "must already equal"):
                HELPER.transaction("", dict(HELPER.PINS, **{name: "old"}))
        with self.assertRaisesRegex(ValueError, "unsatisfied pin"):
            HELPER.transaction("", dict(HELPER.PINS, meson="old"))

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
        import re
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
