"""Offline tests for the eval-owned OpenCode sandbox."""

import os
import pwd
import shutil
import socket
import subprocess
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
        self.binary.write_bytes(b"synthetic executable OPENCODE_CONFIG_CONTENT PROVIDER_NATIVE")
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

    def test_resolver_file_is_bound_only_when_target_is_inside_run(self):
        resolver = self.root.parent / "resolv.conf"
        resolver.symlink_to("/run/synthetic-resolver/resolv.conf")
        launcher = sandbox.Launcher(self.root, self.binary, self.profile, resolver_path=resolver)
        argv, _ = launcher.command(["--version"])
        target = "/run/synthetic-resolver/resolv.conf"
        bind = ["--ro-bind", target, target]
        index = next(i for i in range(len(argv) - 2) if argv[i:i + 3] == bind)
        run_tmpfs = next(i for i in range(len(argv) - 1) if argv[i:i + 2] == ["--tmpfs", "/run"])
        self.assertGreater(index, run_tmpfs)
        self.assertEqual([argv[i:i + 3] for i in range(len(argv) - 2)
                          if argv[i] in ("--ro-bind", "--bind") and (argv[i + 1] == "/run" or argv[i + 1].startswith("/run/"))], [bind])
        resolver.unlink()
        resolver.write_text("nameserver 192.0.2.1\n")
        launcher = sandbox.Launcher(self.root, self.binary, self.profile, resolver_path=resolver)
        argv, _ = launcher.command(["--version"])
        self.assertNotIn(bind, [argv[i:i + 3] for i in range(len(argv) - 2)])
        self.assertFalse(any(argv[i] in ("--ro-bind", "--bind") and (argv[i + 1] == "/run" or argv[i + 1].startswith("/run/"))
                             for i in range(len(argv) - 1)))

    @unittest.skipUnless(os.environ.get("CONCORD_EVAL_OPENCODE_BIN"), "set CONCORD_EVAL_OPENCODE_BIN for sandbox integration")
    def test_real_sandbox_resolver(self):
        getent = shutil.which("getent")
        self.assertIsNotNone(getent)
        try:
            socket.getaddrinfo("api.anthropic.com", 443)
            online = True
        except socket.gaierror:
            online = False
        binary = getent if online else shutil.which("test")
        self.assertIsNotNone(binary)
        launcher = sandbox.Launcher(self.root, binary, {})
        args = ["hosts", "api.anthropic.com"] if online else ["-r", "/etc/resolv.conf"]
        process = launcher.popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        stdout, stderr = process.communicate(timeout=30)
        self.assertEqual(process.returncode, 0, stderr.decode(errors="replace"))
        if online:
            self.assertTrue(stdout.strip())
        else:
            target = Path("/etc/resolv.conf").resolve()
            if target.is_relative_to("/run"):
                argv, _ = launcher.command(args)
                self.assertIn(["--ro-bind", str(target), str(target)],
                              [argv[i:i + 3] for i in range(len(argv) - 2)])

    def test_unsafe_profile_binds_and_environment_are_refused(self):
        home = Path(pwd.getpwuid(os.getuid()).pw_dir)
        for path in ("/", "/proc", "/proc/self", "/sys", "/dev", "/run", str(home),
                     str(home / ".local/share/opencode"), str(home / ".config/opencode"), "relative"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                sandbox.Launcher(self.root, self.binary, {**self.profile, "ro_binds": [path]})
        for name in ("HOME", "PATH", "XDG_CONFIG_HOME", "HARNESS_TOKEN", "OPENCODE_CONFIG_DIR", "NODE_OPTIONS", "BUN_FOO", "LD_PRELOAD", "bad=name"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                sandbox.Launcher(self.root, self.binary, {**self.profile, "env": {name: "x"}})

    def test_binary_owned_names_are_refused_but_plugin_names_are_allowed(self):
        for name in ("OPENCODE_CONFIG_CONTENT", "PROVIDER_NATIVE"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                sandbox.Launcher(self.root, self.binary, {**self.profile, "env": {name: "x"}})
        launcher = sandbox.Launcher(self.root, self.binary, {**self.profile, "env": {"OPENCODE_CLAUDE_MAX_CONFIG": "fixture"}})
        argv, _ = launcher.command(["--version"])
        self.assertIn(["--setenv", "OPENCODE_CLAUDE_MAX_CONFIG", "fixture"],
                      [argv[i:i + 3] for i in range(len(argv) - 2)])

    def test_wrong_version_is_refused_before_server_start(self):
        launcher = sandbox.Launcher(self.root, self.binary, self.profile)
        with patch("adapter.opencode.evals.sandbox.subprocess.run") as run:
            run.return_value.returncode = 0
            run.return_value.stdout = "opencode v1.18.35\n"
            with self.assertRaises(ValueError):
                launcher.verify_version()
            self.assertEqual(run.call_args.kwargs["env"], {})

    def test_spawn_keeps_secrets_out_of_process_arguments(self):
        launcher = sandbox.Launcher(self.root, self.binary, self.profile)
        def inspect(argv, **kwargs):
            self.assertNotIn("synthetic", " ".join(argv))
            self.assertNotIn("private-token", " ".join(argv))
            self.assertEqual(kwargs["env"], {})
            fd = kwargs["pass_fds"][0]
            data = os.read(fd, 65536).split(b"\0")
            self.assertIn(b"private-token", data)
            self.assertIn(b"synthetic", data)
            return object()
        with patch("adapter.opencode.evals.sandbox.subprocess.Popen", side_effect=inspect):
            launcher.popen(["serve"], extra_env={"HARNESS_TOKEN": "private-token"})

    def test_sandbox_never_shares_the_caller_terminal_session(self):
        launcher = sandbox.Launcher(self.root, self.binary, self.profile)
        for requested in ({}, {"start_new_session": False}):
            with self.subTest(requested=requested), patch("adapter.opencode.evals.sandbox.subprocess.Popen") as popen:
                launcher.popen(["serve"], **requested)
                self.assertIs(popen.call_args.kwargs["start_new_session"], True)
        with patch("adapter.opencode.evals.sandbox.subprocess.run") as run:
            run.return_value.returncode = 0
            run.return_value.stdout = sandbox.VERSION + "\n"
            launcher.verify_version()
            self.assertIs(run.call_args.kwargs["start_new_session"], True)


if __name__ == "__main__":
    unittest.main()
