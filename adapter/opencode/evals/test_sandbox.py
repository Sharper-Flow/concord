"""Offline tests for the eval-owned OpenCode sandbox."""

import os
import pwd
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from adapter.opencode.evals import sandbox


class SandboxTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / "run"
        self.root.mkdir()
        self.binary = Path(temporary.name) / "opencode"
        self.binary.write_text("synthetic executable")
        self.plugin = Path(temporary.name) / "plugin"
        self.plugin.mkdir()
        self.extra = Path(temporary.name) / "credentials"
        self.extra.mkdir()
        self.profile = {"plugins": [str(self.plugin)], "ro_binds": [str(self.extra)],
                        "env": {"SYNTHETIC_PROVIDER_KEY": "synthetic"}, "model": "fake/synthetic"}

    def test_launch_isolated_environment_and_mounts(self):
        with patch.dict(os.environ, {"HOME": "/poison", "SECRET": "poison"}, clear=False):
            launcher = sandbox.Launcher(self.root, self.binary, self.profile)
            argv, env = launcher.command(["serve", "--hostname", "127.0.0.1", "--port", "1234"])
        self.assertEqual(env, {})
        self.assertIn("--clearenv", argv)
        self.assertIn("--unshare-pid", argv)
        self.assertIn("--die-with-parent", argv)
        self.assertNotIn("--unshare-net", argv)
        self.assertEqual(argv[argv.index("--ro-bind") + 1:argv.index("--ro-bind") + 3], ["/", "/"])
        for path in (str(self.binary), str(self.plugin), str(self.extra)):
            self.assertTrue(any(argv[i:i + 3] == ["--ro-bind", path, path] for i in range(len(argv) - 2)))
        self.assertTrue(any(argv[i:i + 3] == ["--bind", str(self.root), str(self.root)] for i in range(len(argv) - 2)))
        for path in (pwd.getpwuid(os.getuid()).pw_dir, "/tmp", "/run"):
            self.assertTrue(any(argv[i:i + 2] == ["--tmpfs", path] for i in range(len(argv) - 1)))
        self.assertNotIn("poison", argv)
        self.assertTrue(any(argv[i:i + 3] == ["--setenv", "SYNTHETIC_PROVIDER_KEY", "synthetic"]
                            for i in range(len(argv) - 2)))

    def test_unsafe_profile_binds_and_environment_are_refused(self):
        for path in ("/", "/proc", "/sys", "/dev", "/run", pwd.getpwuid(os.getuid()).pw_dir, "relative"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                sandbox.Launcher(self.root, self.binary, {**self.profile, "ro_binds": [path]})
        for name in ("HOME", "PATH", "XDG_CONFIG_HOME", "OPENCODE_CONFIG_DIR", "bad=name"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                sandbox.Launcher(self.root, self.binary, {**self.profile, "env": {name: "x"}})

    def test_wrong_version_is_refused_before_server_start(self):
        launcher = sandbox.Launcher(self.root, self.binary, self.profile)
        with patch("adapter.opencode.evals.sandbox.subprocess.run") as run:
            run.return_value.returncode = 0
            run.return_value.stdout = "opencode v1.18.35\n"
            with self.assertRaises(ValueError):
                launcher.verify_version()
            self.assertEqual(run.call_args.kwargs["env"], {})


if __name__ == "__main__":
    unittest.main()
