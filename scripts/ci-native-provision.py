#!/usr/bin/env python3
"""Fail-closed helpers for the disposable Ubuntu native-test provisioner.

The AppArmor package's postinst calls parse_profiles reload. It must already be
installed at the approved pin: running its maintainer scripts would reload more
than the one vendor profile this provisioner is allowed to load.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
import re
import subprocess
import sys


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
PROFILE_PIN = "apparmor-profiles=" + PINS["apparmor"]
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
    for name in PROTECTED:
        if installed.get(name) != PINS[name]:
            raise ValueError(f"{name} must already equal {PINS[name]}; maintainer effects are not admitted")
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
    for name, version in PINS.items():
        if changes.get(name, installed.get(name)) != version:
            raise ValueError(f"unsatisfied pin: {name}={version}")
    return [f"{name}={version}" for name, version in sorted(changes.items())]


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
    import hashlib
    import stat
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


def verify_smoke(host: str, observed: str) -> None:
    host_lines, lines = host.splitlines(), observed.splitlines()
    if len(host_lines) != 6 or len(lines) != 7 or "unpriv_bwrap" not in lines[0] or "(enforce)" not in lines[0]:
        raise ValueError("incomplete installed namespace/profile smoke observation")
    for name, original, isolated in zip(("user", "mnt", "pid", "net", "ipc", "uts"), host_lines, lines[1:]):
        if not re.fullmatch(name + r":\[\d+\]", isolated) or original == isolated:
            raise ValueError(f"installed launcher did not establish a private {name} namespace")


def installed() -> dict[str, str]:
    output = subprocess.check_output(
        ["dpkg-query", "-W", "-f=${binary:Package}\t${Version}\t${db:Status-Status}\n"], text=True
    )
    return {name.removesuffix(":amd64"): version for name, version, status in
            (line.split("\t") for line in output.splitlines()) if status == "installed"}


def upstream_wrapper(build: str, command: list[str]) -> None:
    # Meson injects the build-tree BWRAP after the invoking environment. A test
    # wrapper runs after that injection; no upstream test/assertion is patched.
    if os.environ.get("BWRAP") != str(Path(build).resolve() / "bwrap") or not command:
        raise ValueError("unexpected upstream test BWRAP/command")
    os.environ["BWRAP"] = "/usr/bin/bwrap"
    os.execvpe(command[0], command, os.environ)


def main() -> None:
    mode, *args = sys.argv[1:]
    if mode == "pins":
        print("\n".join(f"{name}={version}" for name, version in PINS.items()))
    elif mode == "profile-pin":
        print(PROFILE_PIN)
    elif mode == "transaction":
        snapshot = installed()
        Path(args[1]).write_text(json.dumps(snapshot, sort_keys=True, indent=2) + "\n")
        changes = transaction(Path(args[0]).read_text(), snapshot)
        sys.stdout.write("".join(change + "\n" for change in changes))
    elif mode == "audit-control":
        audit_control(Path(args[0]))
    elif mode == "fingerprint-files":
        fingerprint_files(args)
    elif mode == "fingerprint-runtime":
        fingerprint_files(runtime_files(args[0]))
    elif mode == "verify-smoke":
        verify_smoke(Path(args[0]).read_text(), Path(args[1]).read_text())
    elif mode == "verify-pins":
        snapshot = installed()
        for name, version in PINS.items():
            if snapshot.get(name) != version:
                raise ValueError(f"installed pin mismatch: {name}={snapshot.get(name)} expected {version}")
    elif mode == "upstream-wrapper":
        upstream_wrapper(args[0], args[1:])
    else:
        raise ValueError(f"unknown mode: {mode}")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
