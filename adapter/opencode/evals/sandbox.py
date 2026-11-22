"""Bubblewrap launcher shared by OpenCode evaluation runners."""

import hashlib
import os
import pwd
import re
import subprocess
from pathlib import Path


VERSION = "opencode v2.3.2"
_RESERVED = {"HOME", "PATH", "LANG", "LC_ALL", "OPENCODE_CONFIG_DIR", "OPENCODE_PASSWORD",
             "OPENCODE_DISABLE_EXTERNAL_SKILLS", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS",
             "OPENCODE_DISABLE_CLAUDE_CODE", "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_DISABLE_MODELS_FETCH"}


class Launcher:
    def __init__(self, root, binary, profile):
        self.root = Path(root).resolve()
        self.binary = Path(binary).resolve()
        if not self.root.is_dir() or not self.binary.is_file():
            raise ValueError("run root and executable must exist")
        if not isinstance(profile, dict):
            raise ValueError("invalid provider profile")
        self.home = pwd.getpwuid(os.getuid()).pw_dir
        forbidden = {"/", "/proc", "/sys", "/dev", "/run", self.home}
        self.binds = []
        for key in ("plugins", "ro_binds"):
            paths = profile.get(key, [])
            if not isinstance(paths, list):
                raise ValueError("invalid profile binds")
            for value in paths:
                if not isinstance(value, str) or not os.path.isabs(value):
                    raise ValueError("profile binds must be absolute")
                path = Path(os.path.normpath(value))
                resolved = path.resolve()
                if (str(path) in forbidden or str(resolved) in forbidden or resolved in Path(self.home).parents
                        or resolved == self.root or resolved in self.root.parents
                        or not path.exists() or (key == "plugins" and not path.is_dir())):
                    raise ValueError("unsafe profile bind")
                self.binds.append(str(path))
        self.profile_env = profile.get("env", {})
        if not isinstance(self.profile_env, dict):
            raise ValueError("invalid profile environment")
        for name, value in self.profile_env.items():
            if (not isinstance(name, str) or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", name)
                    or name in _RESERVED or name.startswith(("XDG_", "HARNESS_"))
                    or not isinstance(value, str) or "\0" in value):
                raise ValueError("unsafe profile environment")

    def command(self, args, extra_env=None, cwd=None):
        env = {"HOME": str(self.root / "home"), "XDG_CONFIG_HOME": str(self.root / "config"),
               "XDG_CACHE_HOME": str(self.root / "cache"), "XDG_STATE_HOME": str(self.root / "state"),
               "XDG_DATA_HOME": str(self.root / "data"), "PATH": "/usr/local/bin:/usr/bin:/bin",
               "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", **self.profile_env}
        if extra_env:
            if (not isinstance(extra_env, dict) or set(extra_env) & set(env)
                    or any(not isinstance(k, str) or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", k)
                           or not isinstance(v, str) or "\0" in v for k, v in extra_env.items())):
                raise ValueError("invalid or conflicting harness environment")
            env.update(extra_env)
        argv = ["bwrap", "--clearenv", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc",
                "--tmpfs", self.home, "--tmpfs", "/tmp", "--tmpfs", "/run", "--unshare-pid", "--die-with-parent",
                "--bind", str(self.root), str(self.root), "--ro-bind", str(self.binary), str(self.binary)]
        for path in self.binds:
            argv.extend(["--ro-bind", path, path])
        for key, value in env.items():
            argv.extend(["--setenv", key, value])
        directory = Path(cwd).resolve() if cwd is not None else self.root
        if directory != self.root and self.root not in directory.parents:
            raise ValueError("working directory must be inside the run root")
        return argv + ["--chdir", str(directory), str(self.binary), *args], {}

    def verify_version(self, timeout=None):
        argv, env = self.command(["--version"])
        result = subprocess.run(argv, env=env, capture_output=True, text=True, timeout=timeout, check=False)
        if result.returncode or result.stdout.strip() != VERSION:
            raise ValueError("unexpected OpenCode version")
        return {"version": VERSION, "sha256": hashlib.sha256(self.binary.read_bytes()).hexdigest()}
