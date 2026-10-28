#!/usr/bin/env python3
"""Fail-closed helpers for the disposable Ubuntu native-test provisioner.

The AppArmor package's postinst calls parse_profiles reload. Its authenticated
parser/config are extracted privately, never installed through maintainer scripts.
"""

from __future__ import annotations

import ctypes
import hashlib
import io
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
PROFILE_PIN = "apparmor-profiles=" + PINS["apparmor"]
XDG_SITE_DEFAULT = Path("tunables/xdg-user-dirs.d/site").with_suffix(".local")
INSTALL_PINS = {name: version for name, version in PINS.items() if name != "apparmor"}
VENDOR_DIGEST = "11d39094f044f0cda0febb3ad517b830301da6b2ce929664af09ee9e4dd264f9"
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


def derive_parser_defaults(root: Path) -> None:
    """Read only the pinned postinst's two comment-only empty-default outputs."""
    postinst = root / "apparmor-control/postinst"
    identity(postinst)
    control = postinst.read_bytes()
    # Fixed control-flow delimiters select the empty-debconf branch, without
    # evaluating shell, debconf, substitutions, or the nondefault branch.
    delimiters = (
        b'''\tdb_get apparmor/homedirs
\ttmp=`mktemp`
        cat > "$tmp" <<EOM
''',
        b'''EOM
        if [ -n "$RET" ]; then
            cat >> "$tmp" <<EOM
@{HOMEDIRS}+=$RET
EOM
        else
            cat >> "$tmp" <<EOM
''',
        f'''EOM
        fi
        mkdir -p /etc/apparmor.d/tunables/home.d 2>/dev/null || true
        mv -Z -f "$tmp" /etc/apparmor.d/tunables/home.d/ubuntu
        chmod 644 /etc/apparmor.d/tunables/home.d/ubuntu

        if [ ! -e /etc/apparmor.d/{XDG_SITE_DEFAULT} ]; then
            tmp=`mktemp`
            cat > "$tmp" <<EOM
'''.encode("ascii"),
        f'''EOM

            mkdir -p /etc/apparmor.d/tunables/xdg-user-dirs.d 2>/dev/null || true
            mv -Z -n "$tmp" /etc/apparmor.d/{XDG_SITE_DEFAULT}
            chmod 644 /etc/apparmor.d/{XDG_SITE_DEFAULT}
        fi
'''.encode("ascii"),
    )
    if len(control) > 131072 or b"\r" in control or b"\0" in control or control.count(delimiters[0]) != 1:
        raise ValueError("unsupported parser default control layout")
    pattern = (rb"(?m)^" + re.escape(delimiters[0]) + rb"((?:[^\n]*\n){1,16})" + re.escape(delimiters[1]) +
               rb"((?:[^\n]*\n){1,4})" + re.escape(delimiters[2]) + rb"((?:[^\n]*\n){1,40})" + re.escape(delimiters[3]))
    matches = list(re.finditer(pattern, control))
    if len(matches) != 1:
        raise ValueError("unsupported parser default control layout")
    header, empty_home, xdg = matches[0].groups()
    if empty_home != b"#@{HOMEDIRS}+=\n" or header.count(b"# $ sudo dpkg-reconfigure apparmor\n") != 1:
        raise ValueError("unsupported parser empty-home default")
    outputs = {"tunables/home.d/ubuntu": header + empty_home, str(XDG_SITE_DEFAULT): xdg}
    for name, data in outputs.items():
        # '#include' is policy syntax, not an inert AppArmor comment. '$ sudo'
        # is literal documentation; every other shell expansion marker refuses.
        checked = data.replace(b"# $ sudo dpkg-reconfigure apparmor\n", b"") if name.endswith("/ubuntu") else data
        if (not checked.isascii() or any(line and not line.startswith(b"#") for line in checked.splitlines()) or
                any(marker in checked for marker in (b"$", b"`", b"\\")) or re.search(rb"(?m)^#\s*include\b", checked)):
            raise ValueError(f"non-inert parser default refused: {name}")
        target_name = b"/etc/apparmor.d/" + name.encode("ascii")
        if control.count(target_name) != matches[0][0].count(target_name):
            raise ValueError("ambiguous parser default output references")
        if os.path.lexists(root / "base" / name):
            raise ValueError(f"parser default output collision: {name}")
    for name, data in outputs.items():
        target = root / "base" / name
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        secure_path(target.parent)
        with target.open("xb") as output:
            output.write(data)
        target.chmod(0o600)


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
            # The x86-64 loader excludes the plain libc6 (32-bit) cache ABI.
            # Unknown flags and hwcap alternatives still refuse, not disappear
            # behind a filter that could select a different runtime at execution.
            compatible = [path for flags, path in entries if flags == "(libc6,x86-64)"]
            if (any(flags not in {"(libc6)", "(libc6,x86-64)"} for flags, _ in entries) or
                    len(compatible) != 1 or
                    Path(compatible[0]).parent not in {Path("/lib/x86_64-linux-gnu"), Path("/usr/lib/x86_64-linux-gnu")} or
                    Path(compatible[0]).name != soname):
                observed = json.dumps(entries[:8])[:2048]
                raise ValueError(f"unsupported parser runtime resolution: {soname}; compatible={len(compatible)} total={len(entries)} entries={observed}")
            dependencies.append(compatible[0])
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
        # Control scripts are retained data only. Never execute or install them.
        subprocess.run(["dpkg-deb", "-e", str(archive), str(root / (package + "-control"))], check=True, timeout=30)
        extract_parser_inputs(archive, root, package)
    derive_parser_defaults(root)
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


def verify_compatibility_digest(binary: Path, expected: str) -> str:
    if not re.fullmatch(r"[0-9a-f]{64}", expected):
        raise ValueError("missing or malformed compatibility digest")
    if binary.is_symlink() or hashlib.sha256(binary.read_bytes()).hexdigest() != expected:
        raise ValueError("independent build differs from compatibility digest")
    return expected


def verify_root_context(context: dict) -> None:
    # Include the capabilities used by the upstream root/capability branches.
    required = sum(1 << bit for bit in (0, 1, 3, 5, 6, 7, 10, 12, 18, 21))
    if (context["uids"] != [0, 0, 0] or context["gids"] != [0, 0, 0] or
            context["label"] != "unconfined" or not re.fullmatch(r"user:\[\d+\]", context["userns"]) or
            any(int(context["capabilities"].get(key, "0"), 16) & required != required
                for key in ("CapEff", "CapPrm", "CapBnd"))):
        raise ValueError("root compatibility requires actual root IDs, default unconfined and required capabilities")


def verify_upstream_source(archive: Path, source: Path) -> None:
    if hashlib.sha256(archive.read_bytes()).hexdigest() != SOURCE_DIGEST:
        raise ValueError("upstream source archive authentication failed")
    expected, links = {}, {}
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
            expected[str(relative)] = hashlib.sha256(data.read()).hexdigest()
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
        "root_temp_entries": sorted(str(path) for directory in ("/tmp", "/var/tmp")
                                    for path in Path(directory).iterdir() if path.lstat().st_uid == 0),
    }


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


def upstream_compatibility(work: Path) -> None:
    secure_path(work)
    if (os.geteuid() != 0 or work.name != "work" or work.parent.parent != Path("/var/lib") or
            not re.fullmatch(r"concord-upstream\.[a-zA-Z0-9]+", work.parent.name)):
        raise ValueError("unsupported root compatibility workspace")
    report = {"evidence_kind": "upstream compatibility only; not native isolation",
              "coverage_limits": ["root skips unreadable-directory checks", "caller-readable /etc/shadow checks short-circuit",
                                  "root capability branch replaces non-root branch", "native optional seccomp/FUSE skips retained"],
              "errors": []}
    build = work / "build"
    env = {"HOME": str(work), "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "NINJA": "/usr/bin/ninja",
           "BWRAP_MUST_WORK": "1", "PYTHONDONTWRITEBYTECODE": "1"}
    command = ["/usr/bin/python3.12", "/usr/bin/meson", "test", "-C", str(build),
               "--no-rebuild", "--num-processes", "1", "--print-errorlogs"]
    report["command"] = command
    try:
        report["before_host"] = compatibility_host_state()
        verify_root_context(report["before_host"])
        verify_upstream_source(work / "bubblewrap-0.12.0.tar.xz", work / "bubblewrap-0.12.0")
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
        print(json.dumps(report, indent=2, sort_keys=True), flush=True)
    if report["errors"]:
        raise ValueError("upstream compatibility failed; see retained qualification report")


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
    elif mode == "verify-compatibility-digest":
        print(verify_compatibility_digest(Path(args[0]), args[1]))
    elif mode == "upstream-compatibility":
        upstream_compatibility(Path(args[0]))
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
