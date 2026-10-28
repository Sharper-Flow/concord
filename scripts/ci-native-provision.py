#!/usr/bin/env python3
"""Fail-closed helpers for the disposable Ubuntu native-test provisioner.

The AppArmor package's postinst calls parse_profiles reload. Its authenticated
parser/config are extracted privately, never installed through maintainer scripts.
"""

from __future__ import annotations

import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tarfile


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
INSTALL_PINS = {name: version for name, version in PINS.items() if name != "apparmor"}
VENDOR_DIGEST = "11d39094f044f0cda0febb3ad517b830301da6b2ce929664af09ee9e4dd264f9"
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


def authenticated_archive(source: Path, target: Path, package: str, architecture: str) -> dict:
    # Copy first: every later operation uses only root-private, digest-checked bytes.
    shutil.copyfile(source, target)
    target.chmod(0o600)
    expected = {"Package": package, "Version": PINS["apparmor"], "Architecture": architecture}
    for field, value in expected.items():
        observed = subprocess.check_output(["dpkg-deb", "-f", str(target), field], text=True, timeout=30).strip()
        if observed != value:
            raise ValueError(f"private archive {field} mismatch: {observed} expected {value}")
    output = subprocess.check_output(["apt-cache", "show", package + "=" + PINS["apparmor"]], text=True, timeout=30)
    digests = set()
    for stanza in output.strip().split("\n\n"):
        fields = dict(re.findall(r"^(\S+): (.*)$", stanza, re.M))
        if all(fields.get(key) == value for key, value in expected.items()):
            digests.add(fields.get("SHA256", ""))
    digest = hashlib.sha256(target.read_bytes()).hexdigest()
    if digests != {digest}:
        raise ValueError(f"archive digest differs from signed-index identity: {package}")
    return {**expected, "SHA256": digest}


def extract_parser_inputs(archive: Path, root: Path, package: str) -> None:
    """Extract only parser/config/ABI/tunables or the single vendor profile."""
    raw = subprocess.check_output(["dpkg-deb", "--fsys-tarfile", str(archive)], timeout=60)
    with tarfile.open(fileobj=io.BytesIO(raw)) as contents:
        written = set()
        for member in contents:
            name = member.name.removeprefix("./").rstrip("/") if member.isdir() else member.name.removeprefix("./")
            destination = None
            if package == "apparmor":
                if name in {"sbin/apparmor_parser", "usr/sbin/apparmor_parser"}:
                    destination = root / "apparmor_parser"
                elif name == "etc/apparmor/parser.conf":
                    destination = root / "parser.conf"
                elif name in {"etc/apparmor.d/abi", "etc/apparmor.d/tunables"} or name.startswith(("etc/apparmor.d/abi/", "etc/apparmor.d/tunables/")):
                    destination = root / "base" / name.removeprefix("etc/apparmor.d/")
            elif name == "usr/share/apparmor/extra-profiles/bwrap-userns-restrict":
                destination = root / "vendor-profile"
            if destination is None:
                continue
            if ".." in Path(name).parts or str(Path(name)) != name or destination in written:
                raise ValueError(f"ambiguous private archive member: {name}")
            written.add(destination)
            if not (member.isfile() or member.isdir()):
                raise ValueError(f"unsupported private archive link/type: {name}")
            destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            if member.isdir():
                destination.mkdir(mode=0o700, exist_ok=True)
            else:
                data = contents.extractfile(member)
                assert data is not None
                destination.write_bytes(data.read())
                destination.chmod(0o700 if destination.name == "apparmor_parser" else 0o600)


def policy_closure(profile: Path, base: Path) -> dict:
    """Mirror the producer's bounded closure, refusing links and path ambiguity."""
    seen: dict[str, object] = {}

    def visit(name: str, optional: bool = False) -> None:
        relative = Path(name)
        if relative.is_absolute() or ".." in relative.parts or str(relative) != name:
            raise ValueError(f"unsupported policy include: {name}")
        if name in seen:
            return
        if len(seen) >= 256:
            raise ValueError("policy closure exceeds producer bound")
        path = base / name
        if not os.path.lexists(path):
            if not optional:
                raise ValueError(f"required policy include missing: {name}")
            parent = path.parent
            while not parent.exists():
                parent = parent.parent
            secure_path(parent)
            seen[name] = "absent"
            return
        # The package does not need links; refusing them also excludes host-local
        # redirections the producer's confined root might otherwise traverse.
        for component in (path, *path.parents):
            if component.is_symlink():
                raise ValueError(f"policy include link refused: {path}")
        secure_path(path)
        if path.is_dir():
            names = sorted(child.name for child in path.iterdir() if not child.name.startswith(".") and not child.name.endswith("~"))
            seen[name] = names
            for child in names:
                visit(name + "/" + child)
        else:
            seen[name] = identity(path)["sha256"]
            includes(path.read_text())

    def includes(text: str) -> None:
        for directive in re.finditer(r'^\s*#?include\b([^\n]*)', text, re.M):
            match = re.fullmatch(r'\s+(if exists\s+)?<([^>\n]+)>\s*(?:#.*)?', directive[1])
            if match is None:
                raise ValueError("policy include syntax is outside the verified base-path subset")
            visit(match[2], bool(match[1]))
        # ABI rules also consume files, although the producer seeds abi/4.0
        # explicitly instead of traversing this syntax.
        for match in re.finditer(r'^\s*abi\s+<([^>\n]+)>\s*,', text, re.M):
            if match[1] != "abi/4.0":
                raise ValueError("ABI input is outside producer fingerprint closure")

    includes(profile.read_text())
    visit("abi/4.0")
    return seen


def verify_policy_base(root: Path, base: Path) -> dict:
    for name in ("bwrap", "bwrap-userns-restrict", "unpriv_bwrap"):
        if os.path.lexists(base / "local" / name):
            raise ValueError("local bwrap policy overrides are not admitted")
    for mode in ("disable", "force-complain"):
        if os.path.lexists(base / mode / "bwrap-userns-restrict"):
            raise ValueError("vendor profile mode override is not admitted")
    profile = root / "vendor-profile"
    if identity(profile)["sha256"] != VENDOR_DIGEST:
        raise ValueError("exact pinned vendor profile changed")
    expected = policy_closure(profile, root / "base")
    observed = policy_closure(profile, base)
    if observed != expected:
        raise ValueError("installed policy include closure differs from authenticated parser base")
    return observed


def parser_runtime(parser: Path) -> tuple[list[str], dict]:
    """Read ELF metadata without executing the staged binary (including via ldd)."""
    allowed = {"ld-linux-x86-64.so.2", "libc.so.6", "libm.so.6", "libpthread.so.0", "libdl.so.2", "librt.so.1", "libstdc++.so.6", "libgcc_s.so.1"}
    cache = subprocess.check_output(["/usr/sbin/ldconfig", "-p"], text=True, timeout=30)
    paths = {str(parser)}
    inspected = {}
    queue = [str(parser)]
    while queue:
        name = queue.pop()
        identity(Path(name))
        header = subprocess.check_output(["readelf", "-hW", name], text=True, timeout=30)
        program = subprocess.check_output(["readelf", "-lW", name], text=True, timeout=30)
        dynamic = subprocess.check_output(["readelf", "-dW", name], text=True, timeout=30)
        if not re.search(r"Class:\s+ELF64\b", header) or not re.search(r"Machine:\s+Advanced Micro Devices X86-64\b", header):
            raise ValueError(f"unsupported parser runtime ELF: {name}")
        if re.search(r"\((?:RPATH|RUNPATH|AUDIT|DEPAUDIT|FILTER|AUXILIARY)\)", dynamic):
            raise ValueError(f"unsupported parser runtime dynamic search/hook: {name}")
        inspected[name] = {"header": header, "program": program, "dynamic": dynamic}
        dependencies = []
        for interpreter in re.findall(r"Requesting program interpreter:\s*([^\]]+)", program):
            if interpreter != "/lib64/ld-linux-x86-64.so.2":
                raise ValueError(f"unsupported parser interpreter: {interpreter}")
            dependencies.append(interpreter)
        for soname in re.findall(r"\(NEEDED\).*\[([^\]]+)\]", dynamic):
            if soname not in allowed:
                raise ValueError(f"unsupported parser dependency: {soname}")
            entries = re.findall(r"^\s*" + re.escape(soname) + r" (\([^\n]+\)) => (\S+)$", cache, re.M)
            if len(entries) != 1 or entries[0][0] != "(libc6,x86-64)" or not entries[0][1].startswith(("/lib/x86_64-linux-gnu/", "/usr/lib/x86_64-linux-gnu/")):
                raise ValueError(f"unsupported parser runtime resolution: {soname}")
            dependencies.append(entries[0][1])
        for dependency in dependencies:
            if dependency not in paths:
                paths.add(dependency)
                queue.append(dependency)
    # The dynamic loader consumes these host inputs as well as ELF dependencies.
    preload = Path("/etc/ld.so.preload")
    if os.path.lexists(preload):
        raise ValueError("host loader preload is not admitted")
    paths.add("/etc/ld.so.cache")
    return sorted(paths), inspected


def private_snapshot(root: Path) -> dict:
    return {str(path): identity(path) for path in sorted(root.rglob("*")) if path.is_file() and path.name != "manifest.json"}


def stage_parser(root: Path, archives: list[Path], base: Path = Path("/etc/apparmor.d")) -> None:
    secure_path(root)
    if os.geteuid() != 0 or len(archives) != 2:
        raise ValueError("root-private parser staging requires two authenticated archives")
    sources = []
    for source, package, architecture in zip(archives, ("apparmor", "apparmor-profiles"), ("amd64", "all")):
        archive = root / (package + ".deb")
        sources.append(authenticated_archive(source, archive, package, architecture))
        # Control scripts are evidence only. Never execute or install them.
        subprocess.run(["dpkg-deb", "-e", str(archive), str(root / (package + "-control"))], check=True, timeout=30)
        extract_parser_inputs(archive, root, package)
    config = (root / "parser.conf").read_text()
    if any(line.strip() and not line.lstrip().startswith("#") and
           not re.fullmatch(r"(?:quiet|verbose|(?:Optimize|optimize|warn)=[a-z0-9,-]+)", line.strip()) for line in config.splitlines()):
        raise ValueError("unsupported authenticated parser configuration")
    closure = verify_policy_base(root, base)
    runtime, linkage = parser_runtime(root / "apparmor_parser")
    manifest = {"sources": sources, "private": private_snapshot(root), "policy_closure": closure,
                "runtime": {name: identity(Path(name)) for name in runtime}, "linkage": linkage}
    (root / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    (root / "manifest.json").chmod(0o600)
    print(json.dumps(manifest, indent=2, sort_keys=True))


def parser_effect(root: Path, action: str, base: Path = Path("/etc/apparmor.d")) -> None:
    if os.geteuid() != 0 or action not in {"preprocess", "load"}:
        raise ValueError("unsupported privileged parser action")
    secure_path(root / "manifest.json")
    manifest = json.loads((root / "manifest.json").read_text())
    if private_snapshot(root) != manifest["private"]:
        raise ValueError("private parser/archive/config/base identity drift")
    if os.path.lexists("/etc/ld.so.preload") or any(identity(Path(name)) != record for name, record in manifest["runtime"].items()):
        raise ValueError("parser runtime identity drift")
    if verify_policy_base(root, base) != manifest["policy_closure"]:
        raise ValueError("policy closure identity drift")
    profile = base / "bwrap-userns-restrict"
    secure_path(base)
    if os.path.lexists(profile):
        if profile.is_symlink():
            raise ValueError("installed vendor profile link refused")
        secure_path(profile)
    if action == "preprocess":
        with profile.open("wb") as output:
            output.write((root / "vendor-profile").read_bytes())
        profile.chmod(0o644)
    if identity(profile)["sha256"] != VENDOR_DIGEST:
        raise ValueError("installed vendor profile drift")
    # Explicit config, no cache, one file, unchanged installed include base: the
    # producer fingerprints exactly these policy bytes. No directory/global load.
    subprocess.run([str(root / "apparmor_parser"), "--config-file=" + str(root / "parser.conf"),
                    "--base=" + str(base), "--skip-cache", "--jobs=1",
                    "--preprocess" if action == "preprocess" else "--replace", str(profile)],
                   check=True, timeout=240, cwd="/", env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8"})


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
        print("\n".join(f"{name}={version}" for name, version in INSTALL_PINS.items()))
    elif mode == "parser-pin":
        print("apparmor=" + PINS["apparmor"])
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
        for name, version in INSTALL_PINS.items():
            if snapshot.get(name) != version:
                raise ValueError(f"installed pin mismatch: {name}={snapshot.get(name)} expected {version}")
    elif mode == "upstream-wrapper":
        upstream_wrapper(args[0], args[1:])
    elif mode == "stage-parser":
        stage_parser(Path(args[0]), [Path(name) for name in args[1:]])
    elif mode == "parser-effect":
        parser_effect(Path(args[0]), args[1])
    else:
        raise ValueError(f"unknown mode: {mode}")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError, tarfile.TarError) as error:
        sys.exit(str(error))
