#!/usr/bin/env python3
"""Exercise fixture Git isolation against synthetic outer repositories."""

from __future__ import annotations

import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

import git_environment


class GitEnvironmentTests(unittest.TestCase):
    def test_public_default_preserves_configuration_inputs(self):
        source = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        source.update({
            "GIT_CONFIG_GLOBAL": "/synthetic/global",
            "GIT_CONFIG_SYSTEM": "/synthetic/system",
            "GIT_TEMPLATE_DIR": "/synthetic/template",
        })
        for sanitize in (git_environment.sanitized_environment, self.scrub_copy):
            with self.subTest(sanitize=sanitize.__name__):
                actual = sanitize(source)
                for key in ("GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_TEMPLATE_DIR"):
                    self.assertEqual(actual.get(key), source[key])

    def test_fixture_policy_keeps_operational_inputs(self):
        source = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        source.update({
            "GIT_CONFIG_GLOBAL": "/synthetic/global",
            "GIT_CONFIG_SYSTEM": "/synthetic/system",
            "GIT_TEMPLATE_DIR": "/synthetic/template",
            "GIT_EXEC_PATH": "/synthetic/git-exec",
            "GIT_SSH_COMMAND": "synthetic-ssh",
        })
        for sanitize in (self.sanitize_fixture, self.scrub_fixture):
            with self.subTest(sanitize=sanitize.__name__):
                actual = sanitize(source)
                for key in ("GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_TEMPLATE_DIR"):
                    self.assertTrue(key not in actual, f"{key} survived fixture sanitization")
                self.assertEqual(actual["GIT_EXEC_PATH"], source["GIT_EXEC_PATH"])
                self.assertEqual(actual["GIT_SSH_COMMAND"], source["GIT_SSH_COMMAND"])

    @staticmethod
    def scrub_copy(source):
        result = dict(source)
        git_environment.scrub_inherited(result)
        return result

    @staticmethod
    def sanitize_fixture(source):
        return git_environment.sanitized_environment(source, fixture=True)

    @staticmethod
    def scrub_fixture(source):
        result = dict(source)
        git_environment.scrub_inherited(result, fixture=True)
        return result

    def test_outer_repository_survives_inherited_hooks(self):
        for sanitize in (self.sanitize_fixture, self.scrub_fixture):
            for poison in ("GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_TEMPLATE_DIR"):
                with self.subTest(sanitize=sanitize.__name__, poison=poison):
                    self.check_outer_repository(sanitize, poison)

    def check_outer_repository(self, sanitize, poison):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            outer, fixture, home, template = (root / name for name in ("outer", "fixture", "home", "template"))
            for directory in (outer, fixture, home, template / "hooks"):
                directory.mkdir(parents=True)
            clean = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
            clean.update(HOME=str(home), XDG_CONFIG_HOME=str(home), GIT_CONFIG_NOSYSTEM="1")

            def git(directory, *args, env=clean):
                subprocess.run(["git", "-C", str(directory), *args], env=env, check=True,
                               capture_output=True, text=True)

            git(outer, "init", "--template=", "-b", "main")
            (outer / "tracked").write_text("outer\n")
            git(outer, "add", "tracked")
            git(outer, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                "commit", "-m", "outer")
            hook = template / "hooks/pre-commit"
            hook.write_text("#!/bin/sh\nprintf 'changed\\n' > " + shlex.quote(str(outer / "tracked")) + "\n")
            hook.chmod(0o755)
            config = root / "inherited.config"
            git(outer, "config", "--file", str(config), "core.hooksPath", str(template / "hooks"))
            before = self.snapshot(outer)
            inherited = dict(clean)
            if poison == "GIT_CONFIG_SYSTEM":
                inherited.pop("GIT_CONFIG_NOSYSTEM")
            inherited[poison] = str(template if poison == "GIT_TEMPLATE_DIR" else config)
            isolated = sanitize(inherited)
            git(fixture, "init", "-b", "main", env=isolated)
            git(fixture, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                "commit", "--allow-empty", "-m", "fixture", env=isolated)
            self.assertEqual(before, self.snapshot(outer), "inherited hook changed the outer repository")

    @staticmethod
    def snapshot(root):
        return {str(path.relative_to(root)): (path.stat().st_mode, path.read_bytes())
                for path in root.rglob("*") if path.is_file()}


if __name__ == "__main__":
    unittest.main()
