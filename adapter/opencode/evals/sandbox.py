"""Bubblewrap launcher shared by OpenCode evaluation runners."""

import hashlib
import json
import os
import pwd
import re
import subprocess
from contextlib import contextmanager
from pathlib import Path


VERSION = "opencode v2.3.2"
_RESERVED = {"HOME", "PATH", "LANG", "LC_ALL", "OPENCODE_CONFIG_DIR", "OPENCODE_PASSWORD",
             "OPENCODE_DISABLE_EXTERNAL_SKILLS", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS",
             "OPENCODE_DISABLE_CLAUDE_CODE", "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_DISABLE_MODELS_FETCH"}


def profile_digest(profile):
    files = []
    plugins = profile.get("plugins", [])
    if not isinstance(plugins, list):
        raise ValueError("invalid plugins")
    for value in plugins:
        if not isinstance(value, str) or not os.path.isabs(value) or not Path(value).is_dir():
            raise ValueError("invalid plugin directory")
        root = Path(value)
        for path in sorted(root.rglob("*")):
            if path.is_symlink():
                raise ValueError("plugin symlinks are not supported")
            if path.is_file():
                files.append([value, str(path.relative_to(root)), hashlib.sha256(path.read_bytes()).hexdigest()])
    data = json.dumps({"profile": profile, "files": files}, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
    return hashlib.sha256(data).hexdigest()


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
        home = Path(self.home).resolve()
        forbidden_trees = [Path(p) for p in ("/proc", "/sys", "/dev", "/run")]
        forbidden_trees += [(home / p).resolve() for p in (".local/share/opencode", ".config/opencode", ".cache/opencode",
                                                           ".local/state/opencode", ".opencode")]
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
                        or any(resolved == base or base in resolved.parents for base in forbidden_trees)
                        or resolved == self.root or resolved in self.root.parents
                        or not path.exists() or (key == "plugins" and not path.is_dir())):
                    raise ValueError("unsafe profile bind")
                self.binds.append(str(path))
        self.profile_env = profile.get("env", {})
        if not isinstance(self.profile_env, dict):
            raise ValueError("invalid profile environment")
        binary_bytes = self.binary.read_bytes()
        self.binary_sha256 = hashlib.sha256(binary_bytes).hexdigest()
        for name, value in self.profile_env.items():
            if (not isinstance(name, str) or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", name)
                    or name in _RESERVED or name == "NODE_OPTIONS"
                    or name.startswith(("XDG_", "HARNESS_", "BUN_", "LD_"))
                    or name.encode("ascii") in binary_bytes
                    or not isinstance(value, str) or "\0" in value):
                raise ValueError("unsafe profile environment")

    def command(self, args, extra_env=None, cwd=None, readonly=()):
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
        for path in readonly:
            path = Path(path).resolve()
            if self.root not in path.parents or not path.exists():
                raise ValueError("read-only overlay must be inside the run root")
            argv.extend(["--ro-bind", str(path), str(path)])
        for key, value in env.items():
            argv.extend(["--setenv", key, value])
        directory = Path(cwd).resolve() if cwd is not None else self.root
        if directory != self.root and self.root not in directory.parents:
            raise ValueError("working directory must be inside the run root")
        return argv + ["--chdir", str(directory), str(self.binary), *args], {}

    @contextmanager
    def _sealed_args(self, args, extra_env=None, cwd=None, readonly=()):
        argv, env = self.command(args, extra_env, cwd, readonly)
        fd = os.memfd_create("concord-eval-bwrap-args", os.MFD_CLOEXEC)
        try:
            boundary = len(argv) - len(args) - 1
            data = b"".join(os.fsencode(arg) + b"\0" for arg in argv[1:boundary])
            with os.fdopen(os.dup(fd), "wb") as output:
                output.write(data)
            os.lseek(fd, 0, os.SEEK_SET)
            yield [argv[0], "--args", str(fd), *argv[boundary:]], env, fd
        finally:
            os.close(fd)

    def popen(self, args, *, extra_env=None, cwd=None, readonly=(), **kwargs):
        with self._sealed_args(args, extra_env, cwd, readonly) as (argv, env, fd):
            return subprocess.Popen(argv, env=env, pass_fds=(fd,), **kwargs)

    def verify_version(self, timeout=None):
        with self._sealed_args(["--version"]) as (argv, env, fd):
            result = subprocess.run(argv, env=env, pass_fds=(fd,), capture_output=True, text=True, timeout=timeout, check=False)
        if result.returncode or result.stdout.strip() != VERSION:
            raise ValueError(f"unexpected OpenCode version (exit {result.returncode}): {result.stderr.strip()}")
        return {"version": VERSION, "sha256": self.binary_sha256}
