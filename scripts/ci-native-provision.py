#!/usr/bin/env python3
"""Fail-closed helpers for disposable Ubuntu upstream compatibility checks."""

from __future__ import annotations

import ctypes
import difflib
import hashlib
import json
import os
from pathlib import Path
import re
import resource
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import time


PINS = {
    "apparmor": "4.0.1really4.0.1-0ubuntu0.24.04.9",
    "meson": "1.3.2-1ubuntu1",
    "ninja-build": "1.11.1-2",
    "pkgconf": "1.8.1-2build1",
    "libcap-dev": "1:2.66-5ubuntu2.4",
    "gcc-13": "13.3.0-6ubuntu2~24.04.1",
    "binutils": "2.42-4ubuntu2.10",
    "python3.12": "3.12.3-1ubuntu0.17",
    "libc6-dev": "2.39-0ubuntu8.9",
    "libc6": "2.39-0ubuntu8.9",
    "libcap2": "1:2.66-5ubuntu2.4",
}
INSTALL_PINS = {name: version for name, version in PINS.items() if name != "apparmor"}
SOURCE_DIGEST = "9760d007363e3abba7c747489910f9f82d9fca53ba3bd3282e396fa3c97a3314"
UPSTREAM_TESTS = ("test-utils", "test-run.sh", "test-sandbox.py", "test-seccomp.py",
                  "test-specifying-pidns.sh", "test-specifying-userns.sh")
# Transitive compiler/header/tool dependencies only. A new package family needs
# review, not a wider solver allowance. Policy and service packages are excluded.
DEPENDENCIES = set("""
gcc-13-base cpp-13 cpp-13-x86-64-linux-gnu gcc-13-x86-64-linux-gnu
libgcc-13-dev libstdc++6 libgcc-s1 libasan8 libatomic1 libcc1-0 libgomp1
libhwasan0 libitm1 liblsan0 libquadmath0 libtsan2 libubsan1 libisl23 libmpc3
libmpfr6 libgmp10 binutils-common binutils-x86-64-linux-gnu libbinutils
libctf0 libctf-nobfd0 libgprofng0 libjansson4 libpython3.12-minimal
libpython3.12-stdlib python3.12-minimal libpython3.12t64 libcrypt-dev
linux-libc-dev libc-dev-bin pkgconf-bin libpkgconf3 python3-packaging
python3-setuptools python3-pkg-resources
""".split())
PROTECTED = {"apparmor", "libc6"}
INST = re.compile(r"^Inst ([a-z0-9+.-]+)(?::amd64)?(?: \[([^\]]+)\])? \((\S+) .+\)$")


def transaction(text: str, installed: dict[str, str]) -> list[str]:
    """Validate all solver changes, not just requested roots."""
    for name in PROTECTED & INSTALL_PINS.keys():
        if installed.get(name) != PINS[name]:
            observed = installed.get(name, "not installed")
            raise ValueError(f"{name} must already equal {PINS[name]}; observed {observed}; maintainer effects are not admitted")
    changes: dict[str, str] = {}
    for line in text.splitlines():
        if line.startswith(("Remv ", "Purg ")):
            raise ValueError(f"package removal refused: {line}")
        if not line.startswith("Inst "):
            continue
        match = INST.fullmatch(line)
        if not match:
            raise ValueError(f"unknown apt transaction line: {line}")
        name, old, version = match.groups()
        if name in changes:
            raise ValueError(f"duplicate apt change: {name}")
        if name in PROTECTED or name not in PINS.keys() | DEPENDENCIES:
            raise ValueError(f"incompatible package change: {line}")
        if name in PINS and version != PINS[name]:
            raise ValueError(f"pin mismatch: {line}")
        if old and installed.get(name) != old:
            raise ValueError(f"installed package drift: {line}")
        if old and subprocess.run(["dpkg", "--compare-versions", version, "lt", old], check=False).returncode == 0:
            raise ValueError(f"downgrade refused: {line}")
        changes[name] = version
    for name, version in INSTALL_PINS.items():
        if changes.get(name, installed.get(name)) != version:
            raise ValueError(f"unsatisfied pin: {name}={version}")
    return [f"{name}={version}" for name, version in sorted(changes.items())]


def secure_path(path: Path) -> None:
    """Require root ownership and non-writable ancestors, including link targets."""
    for candidate in (path, *path.parents):
        info = candidate.lstat()
        if info.st_uid != 0 or (not stat.S_ISLNK(info.st_mode) and info.st_mode & 0o022):
            raise ValueError(f"unsafe privileged input: {candidate}")
        if candidate.is_symlink():
            secure_path(candidate.resolve(strict=True))


def identity(path: Path) -> dict:
    secure_path(path)
    info = path.stat()
    if not stat.S_ISREG(info.st_mode):
        raise ValueError(f"non-regular privileged input: {path}")
    return {"sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            "uid": info.st_uid, "gid": info.st_gid, "mode": stat.S_IMODE(info.st_mode),
            "resolved": str(path.resolve(strict=True))}


def audit_control(directory: Path) -> None:
    """Refuse service/policy effects before dpkg can execute staged scripts."""
    for path in sorted(directory.rglob("*")):
        if not path.is_file() or path.name not in {"preinst", "postinst", "prerm", "postrm", "triggers"}:
            continue
        text = path.read_text()
        if re.search(r"apparmor|sysctl|systemctl|invoke-rc\.d|update-rc\.d|deb-systemd|/etc/(?:pam|security)|policy-rc\.d", text, re.I):
            raise ValueError(f"unapproved policy/service maintainer effect: {path}")
        # Only the compiler/runtime cache trigger is admitted. Other triggers can
        # execute a different package's maintainer scripts outside this audit.
        if path.name == "triggers" and any(
            line.strip() and not line.startswith("#") and line.strip() != "activate-noawait ldconfig"
            for line in text.splitlines()
        ):
            raise ValueError(f"unapproved package trigger: {path}")


def fingerprint_files(paths: list[str]) -> None:
    """Record actual consumed files/links, not an image README's identities."""
    records = []
    for name in paths:
        path = Path(name)
        candidates = sorted(path.rglob("*")) if path.is_dir() else [path]
        for candidate in candidates:
            info = candidate.lstat()
            if candidate.is_symlink():
                records.append({"path": str(candidate), "mode": stat.S_IMODE(info.st_mode), "uid": info.st_uid,
                                "gid": info.st_gid, "link": os.readlink(candidate)})
                target = candidate.resolve(strict=True)
                if target.is_file():
                    target_info = target.stat()
                    records.append({"path": str(target), "mode": stat.S_IMODE(target_info.st_mode),
                                    "uid": target_info.st_uid, "gid": target_info.st_gid,
                                    "sha256": hashlib.sha256(target.read_bytes()).hexdigest()})
            elif candidate.is_file():
                records.append({"path": str(candidate), "mode": stat.S_IMODE(info.st_mode), "uid": info.st_uid, "gid": info.st_gid,
                                "sha256": hashlib.sha256(candidate.read_bytes()).hexdigest()})
    print(json.dumps(records, indent=2, sort_keys=True))


def runtime_files(executable: str) -> list[str]:
    output = subprocess.check_output(["ldd", executable], text=True)
    if "not found" in output:
        raise ValueError(f"incomplete installed runtime: {output}")
    return sorted({executable, *re.findall(r"(/[^\s()]+)", output)})


def installed() -> dict[str, str]:
    output = subprocess.check_output(
        ["dpkg-query", "-W", "-f=${binary:Package}\t${Version}\t${db:Status-Status}\n"], text=True
    )
    return {name.removesuffix(":amd64"): version for name, version, status in
            (line.split("\t") for line in output.splitlines()) if status == "installed"}


def verify_root_context(context: dict) -> None:
    # Include the capabilities used by the upstream root/capability branches.
    required = sum(1 << bit for bit in (0, 1, 3, 5, 6, 7, 10, 12, 18, 21))
    if (context["uids"] != [0, 0, 0] or context["gids"] != [0, 0, 0] or
            context["label"] != "unconfined" or not re.fullmatch(r"user:\[\d+\]", context["userns"]) or
            any(int(context["capabilities"].get(key, "0"), 16) & required != required
                for key in ("CapEff", "CapPrm", "CapBnd"))):
        raise ValueError("root compatibility requires actual root IDs, default unconfined and required capabilities")


def repaired_upstream_file(name: str, data: bytes) -> bytes:
    # Exactly two source-bound harness repairs, not a caller-selected patch route.
    if name == "tests/test-sandbox.py":
        original = b'''        class statfs_t(ctypes.Structure):
            _fields_ = [('f_type', ctypes.c_long), ('f_bsize', ctypes.c_long)]
        buf = statfs_t()
'''
        patched = b'''        class statfs_t(ctypes.Structure):
            _fields_ = [
                ('f_type', ctypes.c_long), ('f_bsize', ctypes.c_long),
                ('f_blocks', ctypes.c_ulong), ('f_bfree', ctypes.c_ulong),
                ('f_bavail', ctypes.c_ulong), ('f_files', ctypes.c_ulong),
                ('f_ffree', ctypes.c_ulong), ('f_fsid', ctypes.c_int * 2),
                ('f_namelen', ctypes.c_long), ('f_frsize', ctypes.c_long),
                ('f_flags', ctypes.c_long), ('f_spare', ctypes.c_long * 4),
            ]
        buf = statfs_t()
'''
    elif name == "tests/test-helper.py":
        original = b'''    def __exit__(self, *exc):
        try:
            self._send({'exit': True})
        except OSError:
            pass

        self._parent_sock.close()

        if self.proc:
            self.proc.wait(timeout=5)
        return False
'''
        patched = original.replace(b"            self.proc.wait(timeout=5)\n", b'''            returncode = self.proc.wait(timeout=5)
            if returncode != 0:
                raise AssertionError(f'sandbox helper exited with status {returncode}')
''')
    else:
        raise ValueError("unsupported upstream harness repair")
    if data.count(original) != 1:
        raise ValueError(f"upstream harness repair context missing, duplicate or changed: {name}")
    return data.replace(original, patched, 1)


def repair_upstream_source(archive: Path, source: Path) -> dict:
    verify_upstream_source(archive, source)
    # Validate both contexts before writing either file; reapplication refuses.
    changes = {name: repaired_upstream_file(name, (source / name).read_bytes())
               for name in ("tests/test-sandbox.py", "tests/test-helper.py")}
    for name, data in changes.items():
        (source / name).write_bytes(data)
    return verify_upstream_source(archive, source, repaired=True)


def verify_upstream_source(archive: Path, source: Path, *, repaired: bool = False) -> dict:
    if hashlib.sha256(archive.read_bytes()).hexdigest() != SOURCE_DIGEST:
        raise ValueError("upstream source archive authentication failed")
    expected, links, repairs = {}, {}, {}
    with tarfile.open(archive) as contents:
        for member in contents:
            name = Path(member.name.removeprefix("./"))
            if name.parts[0] != source.name or ".." in name.parts or name.is_absolute():
                raise ValueError("upstream source archive path refused")
            relative = name.relative_to(source.name)
            if member.isdir():
                continue
            if str(relative) in expected:
                raise ValueError("upstream source archive duplicate refused")
            if member.issym():
                # The pinned release links COPYING.LIB and LICENSE to COPYING.
                # Only relative links to regular in-tree archive files are admitted.
                target = Path(member.linkname)
                if target.is_absolute() or ".." in target.parts:
                    raise ValueError("upstream source archive link refused")
                expected[str(relative)] = "link:" + member.linkname
                links[str(relative)] = str(relative.parent / target)
                continue
            if not member.isfile():
                raise ValueError("upstream source archive type refused")
            data = contents.extractfile(member)
            assert data is not None
            original = data.read()
            if repaired and str(relative) in {"tests/test-sandbox.py", "tests/test-helper.py"}:
                patched = repaired_upstream_file(str(relative), original)
                repairs[str(relative)] = {
                    "original_sha256": hashlib.sha256(original).hexdigest(),
                    "patched_sha256": hashlib.sha256(patched).hexdigest(),
                    "diff": "".join(difflib.unified_diff(original.decode().splitlines(keepends=True),
                                                       patched.decode().splitlines(keepends=True),
                                                       fromfile="a/" + str(relative), tofile="b/" + str(relative))),
                }
                original = patched
            expected[str(relative)] = hashlib.sha256(original).hexdigest()
    if repaired and set(repairs) != {"tests/test-sandbox.py", "tests/test-helper.py"}:
        raise ValueError("upstream source archive lacks the two regular harness files")
    if any(expected.get(target, "link:").startswith("link:") for target in links.values()):
        raise ValueError("upstream source archive link target refused")
    observed = {}
    for path in source.rglob("*"):
        if path.is_symlink():
            observed[str(path.relative_to(source))] = "link:" + os.readlink(path)
        elif path.is_file():
            observed[str(path.relative_to(source))] = hashlib.sha256(path.read_bytes()).hexdigest()
        elif not path.is_dir():
            raise ValueError("upstream source type refused")
    if not expected or observed != expected:
        raise ValueError("upstream source differs from authenticated archive")
    return repairs


def verify_upstream_inventory(inventory: list[dict], work: Path) -> None:
    if [entry["name"] for entry in inventory] != list(UPSTREAM_TESTS):
        raise ValueError("upstream inventory must retain the exact six entries")
    for entry in inventory:
        name = entry["name"]
        command = ([str(work / "build/tests/test-utils")] if name == "test-utils" else
                   ["/usr/bin/python3.12" if name.endswith(".py") else "/usr/bin/bash",
                    str(work / "bubblewrap-0.12.0/tests" / name)])
        env = {"BWRAP": str(work / "build/bwrap"), "G_TEST_BUILDDIR": str(work / "build"),
               "G_TEST_SRCDIR": str(work / "bubblewrap-0.12.0")}
        if (entry["protocol"] != "tap" or entry["workdir"] is not None or entry["timeout"] != 30 or
                entry["suite"] != ["bubblewrap"] or len(entry["cmd"]) != len(command) or
                [os.path.normpath(arg) for arg in entry["cmd"]] != command or
                {key: os.path.normpath(value) for key, value in entry["env"].items()} != env):
            raise ValueError(f"upstream inventory command/environment/control drift: {name}")


def verify_upstream_results(records: list[dict], inventory: list[dict]) -> dict:
    if [record["name"] for record in records] != list(UPSTREAM_TESTS):
        raise ValueError("incomplete compatibility results")
    summary = {}
    for record, entry in zip(records, inventory):
        text = record["stdout"]
        name = record["name"]
        if (record["command"] != entry["cmd"] or record["returncode"] != 0 or
                any(record["env"].get(key) != value for key, value in entry["env"].items()) or
                record["env"].get("BWRAP_MUST_WORK") != "1"):
            raise ValueError(f"compatibility result command/environment/exit mismatch: {name}")
        optional_seccomp = name == "test-seccomp.py" and text.strip() == "1..0 # SKIP cannot import seccomp Python module"
        plans = re.findall(r"^1\.\.(\d+)(?:\s*#.*)?$", text, re.M)
        tests = re.findall(r"^(ok|not ok)\b([^\n]*)$", text, re.M)
        skips = [kind + rest for kind, rest in tests if re.search(r"#\s*SKIP\b", rest, re.I)]
        if optional_seccomp and record["result"] == "SKIP":
            summary[name] = {"records": 0, "passed": 0, "skips": [text.strip()]}
            continue
        if (record["result"] != "OK" or len(plans) != 1 or int(plans[0]) != len(tests) or
                not tests or len(skips) == len(tests) or any(kind != "ok" for kind, _ in tests) or
                re.search(r"^Bail out!", text, re.M)):
            raise ValueError(f"incomplete, unavailable or failed compatibility result: {name}")
        summary[name] = {"records": len(tests), "passed": len(tests) - len(skips), "skips": skips}
    return summary


def compatibility_host_state() -> dict:
    status = Path("/proc/self/status").read_text()
    launcher = Path("/usr/bin/bwrap")
    return {
        "uids": list(os.getresuid()), "gids": list(os.getresgid()),
        "supplementary_groups": os.getgroups(),
        "label": Path("/proc/self/attr/current").read_text().strip(),
        "capabilities": dict(re.findall(r"^(Cap\w+):\s+(\w+)$", status, re.M)),
        "userns": os.readlink("/proc/self/ns/user"),
        "sysctls": {name: Path("/proc/sys/" + name.replace(".", "/")).read_text()
                    for name in ("kernel.apparmor_restrict_unprivileged_userns", "kernel.unprivileged_userns_clone",
                                 "user.max_user_namespaces")},
        "profiles": sorted(Path("/sys/kernel/security/apparmor/profiles").read_text().splitlines()),
        "mounts": Path("/proc/self/mountinfo").read_text(),
        "installed_bwrap": identity(launcher) if os.path.lexists(launcher) else "absent",
    }


def compatibility_temp_entries() -> dict[str, int]:
    # Include every owner so a preexisting user file cannot count as new. Entries
    # can disappear during enumeration; global churn is diagnostic, not a gate.
    entries = {}
    for directory in ("/tmp", "/var/tmp"):
        for path in Path(directory).iterdir():
            try:
                entries[str(path)] = path.lstat().st_uid
            except FileNotFoundError:
                pass
    return entries


def verify_upstream_temp(before: dict[str, int], after: dict[str, int]) -> dict:
    candidates = [name for name in set(after) - set(before) if re.fullmatch(r"/tmp/bwrap-test-[a-zA-Z0-9]{8}", name)]
    if len(candidates) != 1:
        raise ValueError("expected exactly one new upstream temporary file")
    name = candidates[0]
    info = Path(name).lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_size != 6:
        raise ValueError("unexpected upstream temporary file type, owner or size")
    fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        opened = os.fstat(fd)
        if (not stat.S_ISREG(opened.st_mode) or opened.st_uid != 0 or opened.st_size != 6 or
                (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino) or os.read(fd, 7) != b"hello\n"):
            raise ValueError("upstream temporary file identity or contents mismatch")
        return {"path": name, "uid": opened.st_uid, "mode": stat.S_IMODE(opened.st_mode),
                "device": opened.st_dev, "inode": opened.st_ino, "size": opened.st_size,
                "sha256": hashlib.sha256(b"hello\n").hexdigest()}
    finally:
        os.close(fd)


def compatibility_inputs(work: Path) -> dict:
    if os.path.lexists("/etc/ld.so.preload"):
        raise ValueError("compatibility host loader preload refused")
    source = work / "bubblewrap-0.12.0"
    paths = [Path(__file__), Path("/etc/ld.so.cache"), work / "native.ini", work / "bubblewrap-0.12.0.tar.xz"]
    paths += list(source.rglob("*"))
    paths += [path for path in (work / "build").rglob("*") if "meson-logs" not in path.relative_to(work / "build").parts]
    records = {}
    for path in sorted(paths):
        secure_path(path)
        # Only the archive-bound source links resolve, to regular source files.
        if path.is_symlink() and not (path.is_relative_to(source) and path.is_file() and
                                      path.resolve(strict=True).is_relative_to(source)):
            raise ValueError(f"compatibility input link refused: {path}")
        if path.is_file():
            records[str(path)] = identity(path)
        elif not path.is_dir():
            raise ValueError(f"compatibility input type refused: {path}")
    # The image supplies these prerequisites; a missing tool fails, never installs
    # another package or bypasses an upstream assertion. Optional tools stay optional.
    tools = ("bash", "python3.12", "python3", "meson", "ninja", "env", "capsh", "getpcaps", "findmnt", "unshare",
             "cat", "chmod", "cmp", "cp", "cut", "df", "diff", "dirname", "echo", "false", "find", "grep", "head", "id",
             "ln", "locale", "ls", "mkdir", "mkfifo", "mktemp", "mountpoint", "readlink", "realpath", "rm", "sed", "seq",
             "sh", "sleep", "sort", "stat", "touch", "tr", "true", "wc", "awk")
    for name in tools:
        tool = shutil.which(name, path="/usr/sbin:/usr/bin:/sbin:/bin")
        if tool is None:
            raise ValueError(f"required upstream prerequisite missing: {name}")
        records[tool] = identity(Path(tool))
    # The unchanged upstream suite uses fault injection only when strace exists.
    strace = shutil.which("strace", path="/usr/sbin:/usr/bin:/sbin:/bin")
    if strace is not None:
        records[strace] = identity(Path(strace))
    for directory in (Path("/usr/lib/python3/dist-packages/mesonbuild"), Path("/usr/lib/python3.12")):
        secure_path(directory)
        for path in sorted(directory.rglob("*")):
            if path.is_file():
                records[str(path)] = identity(path)
    for program in (work / "build/bwrap", work / "build/tests/test-utils", work / "build/tests/try-syscall",
                    Path("/usr/bin/bash"), Path("/usr/bin/python3.12")):
        for name in runtime_files(str(program)):
            records[name] = identity(Path(name))
    return records


def enable_subreaper() -> None:
    libc = ctypes.CDLL("libc.so.6", use_errno=True)
    if libc.prctl(36, 1, 0, 0, 0) != 0:  # PR_SET_CHILD_SUBREAPER
        raise OSError(ctypes.get_errno(), "cannot supervise upstream descendants")


def direct_children() -> list[int]:
    return [int(pid) for pid in Path(f"/proc/self/task/{os.getpid()}/children").read_text().split()]


def reap_compatibility_children(report: dict) -> None:
    # Only our direct children are signalled; subreaping adopts double-forked
    # descendants when their parents die. Unreaped children cannot reuse a PID.
    deadline = time.monotonic() + 10
    report["terminated_children"] = []
    while True:
        try:
            while os.waitpid(-1, os.WNOHANG)[0]:
                pass
        except ChildProcessError:
            report["descendants_gone"] = True
            return
        children = direct_children()
        if time.monotonic() >= deadline:
            raise ValueError(f"upstream descendants remain; cleanup refused: {children}")
        for pid in children:
            try:
                fd = os.pidfd_open(pid)
                try:
                    signal.pidfd_send_signal(fd, signal.SIGKILL)
                    report["terminated_children"].append(pid)
                finally:
                    os.close(fd)
            except ProcessLookupError:
                pass
        time.sleep(0.05)


def supervise_compatibility(command: list[str], work: Path, env: dict, report: dict) -> None:
    enable_subreaper()
    if direct_children():
        raise ValueError("compatibility supervisor already has children")
    marker = work.parent / "cleanup-safe"
    marker.unlink()
    report["descendants_gone"] = False
    def cancelled(signum, _frame):
        raise ValueError(f"compatibility cancelled by signal {signum}")
    previous = {sig: signal.signal(sig, cancelled) for sig in (signal.SIGTERM, signal.SIGINT)}
    try:
        resource.setrlimit(resource.RLIMIT_FSIZE, (64 * 1024 * 1024, 64 * 1024 * 1024))
        report["limits"] = {"suite_seconds": 300, "descendant_cleanup_seconds": 10, "file_bytes": 64 * 1024 * 1024}
        with (work.parent / "console.log").open("xb") as output:
            process = subprocess.Popen(command, cwd=work, env=env, stdin=subprocess.DEVNULL,
                                       stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
            report["meson_exit"] = process.wait(timeout=300)
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)
        reap_compatibility_children(report)
        marker.write_text("no root test descendants\n")
    if report["meson_exit"] != 0 or report["terminated_children"]:
        raise ValueError("upstream test failure or lingering descendants; see retained native results")


def compatibility_crash_metadata(report: dict) -> dict:
    if os.geteuid() == 0:
        raise ValueError("crash diagnostics require an unprivileged runner")
    fields = ("__REALTIME_TIMESTAMP", "_BOOT_ID", "COREDUMP_PID", "COREDUMP_UID", "COREDUMP_SIGNAL",
              "COREDUMP_EXE", "COREDUMP_UNIT", "COREDUMP_CGROUP")
    result = {"availability": "unavailable", "records": [], "record_limit": 256,
              "absence_is_not_proof": True, "evidence_kind": "bounded core-dump metadata; no core data"}
    try:
        if not isinstance(report, dict):
            raise ValueError("invalid diagnostic report")
        start, end = report["started_unix_seconds"], report["finished_unix_seconds"]
        if type(start) is not int or type(end) is not int or not 0 < start <= end <= start + 600:
            raise ValueError("invalid diagnostic time window")
        process = subprocess.run(
            ["/usr/bin/journalctl", "--no-pager", "--output=json", "--output-fields=" + ",".join(fields),
             "--lines=256", f"--since=@{start}", f"--until=@{end + 1}",
             "MESSAGE_ID=fc2e22bc6ee647b6b90729ab34a250b1"],
            stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=10,
            env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"})
        if process.returncode != 0 or process.stderr:
            raise ValueError("journal access incomplete")
        lines = process.stdout.splitlines()
        # systemd 255 prints this exact notice on stdout for an empty JSON result.
        rows = [] if lines == ["-- No entries --"] else [json.loads(line) for line in lines]
        if not all(isinstance(row, dict) for row in rows):
            raise ValueError("invalid journal record")
        result["records"] = [{key: value for key, value in row.items()
                              if key in fields and isinstance(value, str)}
                              for row in rows]
        result["availability"] = "possibly truncated" if len(rows) >= 256 else "captured"
    except (KeyError, ValueError, OSError, subprocess.SubprocessError):
        # Never retain stderr, free-form messages, environment, command lines or
        # core data. No journal permission triggers a sudo or package fallback.
        pass
    return result


def upstream_compatibility(work: Path) -> None:
    secure_path(work)
    if (os.geteuid() != 0 or work.name != "work" or work.parent.parent != Path("/var/lib") or
            not re.fullmatch(r"concord-upstream\.[a-zA-Z0-9]+", work.parent.name)):
        raise ValueError("unsupported root compatibility workspace")
    report = {"evidence_kind": "upstream compatibility only; not native isolation",
              "coverage_limits": ["root skips unreadable-directory checks", "caller-readable /etc/shadow checks short-circuit",
                                  "root capability branch replaces non-root branch", "native optional seccomp/FUSE skips retained"],
              "errors": [], "started_unix_seconds": int(time.time())}
    build = work / "build"
    env = {"HOME": str(work), "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "NINJA": "/usr/bin/ninja",
           "BWRAP_MUST_WORK": "1", "PYTHONDONTWRITEBYTECODE": "1"}
    command = ["/usr/bin/python3.12", "/usr/bin/meson", "test", "-C", str(build),
               "--no-rebuild", "--num-processes", "1", "--print-errorlogs"]
    report["command"] = command
    try:
        report["before_host"] = compatibility_host_state()
        verify_root_context(report["before_host"])
        report["before_temp_entries"] = compatibility_temp_entries()
        report["source_repairs"] = verify_upstream_source(work / "bubblewrap-0.12.0.tar.xz", work / "bubblewrap-0.12.0", repaired=True)
        report["before_inputs"] = compatibility_inputs(work)
        # Read the build's JSON control output without executing repository hooks.
        inventory = json.loads((build / "meson-info/intro-tests.json").read_text())
        report["inventory"] = inventory
        verify_upstream_inventory(inventory, work)
        report["bwrap_sha256"] = identity(build / "bwrap")["sha256"]
        supervise_compatibility(command, work, env, report)
        records = [json.loads(line) for line in (build / "meson-logs/testlog.json").read_text().splitlines()]
        report["summary"] = verify_upstream_results(records, inventory)
    except (ValueError, OSError, subprocess.SubprocessError, tarfile.TarError) as error:
        report["errors"].append(str(error))
    finally:
        for key, observe in (("after_host", compatibility_host_state), ("after_inputs", lambda: compatibility_inputs(work))):
            try:
                report[key] = observe()
                if key.replace("after", "before") in report and report[key] != report[key.replace("after", "before")]:
                    report["errors"].append(f"unexplained compatibility drift: {key}")
                    if key == "after_host":
                        # A host mount drift must not turn bounded workspace
                        # removal into traversal of an unexpected mounted tree.
                        (work.parent / "cleanup-safe").unlink(missing_ok=True)
            except (ValueError, OSError) as error:
                report["errors"].append(str(error))
                if key == "after_host":
                    (work.parent / "cleanup-safe").unlink(missing_ok=True)
        try:
            verify_upstream_source(work / "bubblewrap-0.12.0.tar.xz", work / "bubblewrap-0.12.0", repaired=True)
            report["source_verified_after"] = True
        except (ValueError, OSError, tarfile.TarError) as error:
            report["errors"].append(str(error))
        try:
            report["after_temp_entries"] = compatibility_temp_entries()
            if "before_temp_entries" in report:
                report["expected_temp_file"] = verify_upstream_temp(report["before_temp_entries"], report["after_temp_entries"])
        except (ValueError, OSError) as error:
            report["errors"].append(str(error))
        report["logs"] = {}
        # Full native logs are embedded, not reinterpreted or truncated to passes.
        for path in [work.parent / "console.log", *sorted((build / "meson-logs").glob("testlog*"))]:
            try:
                if path.exists():
                    identity(path)
                    if path.stat().st_size > 64 * 1024 * 1024:
                        raise ValueError(f"compatibility log exceeds capture bound: {path}")
                    report["logs"][str(path)] = path.read_text()
            except (ValueError, OSError) as error:
                report["errors"].append(str(error))
        report["finished_unix_seconds"] = int(time.time())
        print(json.dumps(report, indent=2, sort_keys=True), flush=True)
    if report["errors"]:
        raise ValueError("upstream compatibility failed; see retained qualification report")


def main() -> None:
    mode, *args = sys.argv[1:]
    if mode == "pins":
        print("\n".join(f"{name}={version}" for name, version in INSTALL_PINS.items()))
    elif mode == "transaction":
        snapshot = installed()
        Path(args[1]).write_text(json.dumps(snapshot, sort_keys=True, indent=2) + "\n")
        changes = transaction(Path(args[0]).read_text(), snapshot)
        sys.stdout.write("".join(change + "\n" for change in changes))
    elif mode == "audit-control":
        audit_control(Path(args[0]))
    elif mode == "fingerprint-files":
        fingerprint_files(args)
    elif mode == "verify-pins":
        snapshot = installed()
        for name, version in INSTALL_PINS.items():
            if snapshot.get(name) != version:
                raise ValueError(f"installed pin mismatch: {name}={snapshot.get(name)} expected {version}")
    elif mode == "upstream-compatibility":
        upstream_compatibility(Path(args[0]))
    elif mode == "repair-upstream-source":
        if os.geteuid() == 0:
            raise ValueError("upstream harness repair requires an unprivileged runner")
        print(json.dumps(repair_upstream_source(Path(args[0]), Path(args[1])), indent=2, sort_keys=True))
    elif mode in {"compatibility-crash-metadata", "qualify-compatibility-crashes"}:
        try:
            report = json.loads(Path(args[0]).read_text())
        except (OSError, ValueError):
            report = {}
        metadata = compatibility_crash_metadata(report)
        print(json.dumps(metadata, indent=2), flush=True)
        if mode == "qualify-compatibility-crashes" and (metadata["availability"] != "captured" or metadata["records"]):
            raise ValueError("compatibility requires available, nontruncated, empty core-dump metadata")
    else:
        raise ValueError(f"unknown mode: {mode}")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError, tarfile.TarError) as error:
        sys.exit(str(error))
