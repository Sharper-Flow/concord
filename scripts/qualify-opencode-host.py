#!/usr/bin/env python3
"""Qualify generated Concord agents against the pinned OpenCode host.

This is a real-tool check; it does not replace generated-contract validation.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
OBLIGATIONS = ("permissions", "session_prepare", "tool_result_envelope",
               "session_directory", "abort_oversize", "background_subagent")


class EnvironmentRefusal(Exception):
    pass


def pin_version(document):
    versions = [p["version"] for p in document["packages"] if p["name"] == "@opencode/plugin"]
    if len(versions) != 1 or not isinstance(versions[0], str) or not versions[0]:
        raise ValueError("host pin must contain exactly one @opencode/plugin version")
    return versions[0]


def declarations(directory):
    agents = {}
    for path in sorted(directory.glob("*.md")):
        lines = path.read_text().splitlines()
        if not lines or lines[0] != "---":
            raise ValueError(f"missing frontmatter: {path.name}")
        end = lines.index("---", 1)
        mode, hidden, rules, in_rules = None, False, [], False
        for line in lines[1:end]:
            if line.startswith("mode: "):
                mode = line.removeprefix("mode: ")
            elif line.startswith("hidden: "):
                value = line.removeprefix("hidden: ")
                if value not in ("true", "false"):
                    raise ValueError(f"invalid hidden in {path.name}")
                hidden = value == "true"
            elif line == "permissions:":
                in_rules = True
            elif line.startswith("  - ") and in_rules:
                rules.append(json.loads(line[4:]))
            elif in_rules:
                raise ValueError(f"unexpected permissions line in {path.name}: {line}")
        if mode not in ("primary", "subagent", "all") or not in_rules:
            raise ValueError(f"missing agent mode or permissions: {path.name}")
        agents[path.stem] = {"mode": mode, "hidden": hidden, "permissions": rules}
    if not agents:
        raise ValueError("no agent definitions")
    return agents


def compare_agents(expected, registry):
    errors = []
    observed = {}
    for agent in registry:
        name = agent.get("id")
        if name in observed:
            errors.append(f"duplicate agent: {name}")
        observed[name] = agent
    for name, declaration in expected.items():
        actual = observed.get(name)
        if actual is None:
            errors.append(f"missing agent: {name}")
            continue
        for field in ("mode", "hidden"):
            if actual.get(field) != declaration[field]:
                errors.append(f"{name}: {field} differs")
        # OpenCode prepends defaults and may append host rules. Preserve declared order.
        remaining = iter(actual.get("permissions", []))
        if not all(any(rule == item for item in remaining) for rule in declaration["permissions"]):
            errors.append(f"{name}: declared permissions not an ordered subsequence")
    return errors


def exit_code(report, selected):
    if report.get("environment_refusal"):
        return 2
    return 0 if all(report["obligations"][name]["status"] == "pass" for name in selected) else 1


def sandbox_base(project, cli):
    home = str(Path.home())
    bwrap = shutil.which("bwrap")
    if not bwrap:
        raise EnvironmentRefusal("bwrap is missing")
    bwrap = os.path.abspath(bwrap)
    return [bwrap, "--clearenv", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc",
            "--tmpfs", home, "--tmpfs", "/tmp", "--ro-bind", str(cli), "/tmp/oc",
            "--bind", str(project), "/tmp/work", "--unshare-net", "--unshare-pid",
            "--die-with-parent", "--chdir", "/tmp/work", "--setenv", "HOME", home,
            "--setenv", "PATH", "/tmp/oc/node_modules/.bin:/usr/bin:/bin",
            "--setenv", "LANG", "C.UTF-8", "--setenv", "LC_ALL", "C.UTF-8",
            "--setenv", "OPENCODE_DISABLE_MODELS_FETCH", "true",
            "--setenv", "XDG_CONFIG_HOME", home + "/.config",
            "--setenv", "XDG_DATA_HOME", home + "/.local/share",
            "--setenv", "XDG_CACHE_HOME", home + "/.cache",
            "--setenv", "XDG_STATE_HOME", home + "/.local/state"]


def run_sandbox(project, cli, argv):
    # argv may also be an inner-stage interpreter that starts multiple processes.
    return subprocess.run(sandbox_base(project, cli) + argv, capture_output=True, text=True, timeout=110, env={})


def native_integrity(cli, executable):
    digest = hashlib.sha256(executable.read_bytes()).digest()
    packages = cli / "node_modules/@opencode"
    matches = []
    for package in packages.glob("cli-*"):
        binary = package / "bin/opencode"
        if binary.is_file() and hashlib.sha256(binary.read_bytes()).digest() == digest:
            matches.append(package)
    linked = [package for package in matches if os.path.samefile(package / "bin/opencode", executable)]
    if len(linked) == 1:
        matches = linked
    if len(matches) != 1:
        raise ValueError(f"cannot uniquely identify the native executable package: {[p.name for p in matches]}")
    package = matches[0]
    metadata = json.loads((package / "package.json").read_text())
    name = metadata["name"]
    npm = cli / "package-lock.json"
    if npm.is_file():
        entry = json.loads(npm.read_text())["packages"][str(package.relative_to(cli))]
        return {"package": name, "version": entry["version"], "integrity": entry["integrity"]}
    lock = cli / "bun.lock"
    if lock.is_file():
        entry = bun_lock_json(lock.read_text())["packages"][name]
        return {"package": name, "version": metadata["version"], "integrity": entry[-1]}
    raise ValueError("native package integrity not available in a supported lockfile")


def bun_lock_json(text):
    # Bun's text lockfile uses JSON with trailing commas. Do not alter strings.
    result = []
    quoted = escaped = False
    for index, char in enumerate(text):
        if quoted:
            result.append(char)
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quoted = False
        elif char == '"':
            quoted = True
            result.append(char)
        elif char == "," and text[index + 1:].lstrip().startswith(("}", "]")):
            continue
        else:
            result.append(char)
    return json.loads("".join(result))


def install_cli(cli, pin):
    # The package postinstall executes the host outside bwrap; select its native
    # binary ourselves instead. This qualification supports Linux x86-64.
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "amd64"):
        raise EnvironmentRefusal("automatic host staging requires Linux x86-64")
    subprocess.run(["bun", "add", "--ignore-scripts", "--exact", f"@opencode/cli@{pin}"],
                   cwd=cli, check=True, capture_output=True, text=True, timeout=110)
    cpuinfo = Path("/proc/cpuinfo").read_text().lower().split()
    baseline = "avx2" not in cpuinfo
    musl = Path("/etc/alpine-release").exists() or platform.libc_ver()[0] == "musl"
    if musl:
        suffixes = (["-baseline-musl", "-musl", "-baseline", ""] if baseline
                    else ["-musl", "-baseline-musl", "", "-baseline"])
    else:
        suffixes = (["-baseline", "", "-baseline-musl", "-musl"] if baseline
                    else ["", "-baseline", "-musl", "-baseline-musl"])
    base = cli / "node_modules/@opencode"
    metadata = json.loads((base / "cli/package.json").read_text())
    for suffix in suffixes:
        name = "@opencode/cli-linux-x64" + suffix
        package = base / ("cli-linux-x64" + suffix)
        source = package / "bin/opencode"
        if source.is_file() and json.loads((package / "package.json").read_text())["version"] == metadata["optionalDependencies"][name]:
            target = base / "cli/bin/opencode.exe"
            target.parent.mkdir(parents=True, exist_ok=True)
            target.unlink(missing_ok=True)
            os.link(source, target)
            return
    raise ValueError("no matching installed native OpenCode package")


def registry_probe(project, cli):
    probe = run_sandbox(project, cli, ["/tmp/oc/node_modules/.bin/opencode", "debug", "agents"])
    if probe.returncode:
        raise ValueError(f"agent registry failed: {probe.stderr.strip()}")
    return probe.stdout


def qualify_session_prepare(project, cli, captures):
    source = project / ".opencode/agents/Concord.md"
    original = source.read_text()
    (captures / "full.json").write_text(registry_probe(project, cli))
    source.unlink()
    (captures / "missing.json").write_text(registry_probe(project, cli))
    wrong = original.replace("\nmode: primary\n", "\nmode: subagent\n", 1)
    if wrong == original:
        raise ValueError("Concord agent has no primary mode to replace")
    source.write_text(wrong)
    (captures / "wrong-mode.json").write_text(registry_probe(project, cli))
    source.write_text(original)
    env = dict(os.environ, OPENCODE_QUALIFY_REGISTRY_DIR=str(captures))
    test = subprocess.run(["go", "test", "./cmd/concord", "-run", "^TestHostRegistryQualification$", "-count=1", "-v"],
                          cwd=ROOT, env=env, capture_output=True, text=True, timeout=110)
    lines = test.stdout.splitlines()
    passed = any(line.startswith("--- PASS: TestHostRegistryQualification ") or line == "--- PASS: TestHostRegistryQualification" for line in lines)
    skipped = any(line.lstrip().startswith("--- SKIP:") for line in lines)
    return {"status": "pass" if test.returncode == 0 and passed and not skipped else "fail", "exit_code": test.returncode,
            "stdout": test.stdout, "stderr": test.stderr}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--opencode-dir", type=Path)
    parser.add_argument("--only", help="comma-separated obligation names")
    args = parser.parse_args()
    selected = args.only.split(",") if args.only is not None else list(OBLIGATIONS)
    report = {"obligations": {name: {"status": "not_run"} for name in OBLIGATIONS}}
    try:
        if not selected or any(name not in OBLIGATIONS for name in selected):
            raise ValueError("invalid --only obligation")
        with tempfile.TemporaryDirectory(prefix="concord-host-qualification-") as temporary:
            temp = Path(temporary)
            project = temp / "project"
            project.mkdir()
            cli = args.opencode_dir.resolve() if args.opencode_dir else temp / "cli"
            if not args.opencode_dir:
                cli.mkdir()
            check = run_sandbox(project, cli, ["/usr/bin/true"])
            if check.returncode:
                raise EnvironmentRefusal(f"bwrap unavailable: {check.stderr.strip()}")
            pin = pin_version(json.loads((ROOT / ".concord/docs/adapter-host-pin.v1.json").read_text()))
            if not args.opencode_dir:
                install_cli(cli, pin)
            executable = (cli / "node_modules/@opencode/cli/bin/opencode.exe").resolve(strict=True)
            if not executable.is_relative_to(cli):
                raise ValueError("resolved executable is outside the installation")
            identity = run_sandbox(project, cli, ["/tmp/oc/node_modules/.bin/opencode", "--version"])
            if identity.returncode or identity.stdout.strip() != f"opencode v{pin}":
                raise ValueError(f"host version mismatch: {identity.stdout.strip()} {identity.stderr.strip()}")
            report["host"] = {"version": pin, "sha256": hashlib.sha256(executable.read_bytes()).hexdigest(),
                              "native_package": native_integrity(cli, executable)}
            if "permissions" in selected or "session_prepare" in selected:
                agent_dir = project / ".opencode/agents"
                agent_dir.parent.mkdir()
                shutil.copytree(ROOT / ".opencode/agents", agent_dir)
            if "permissions" in selected:
                errors = compare_agents(declarations(agent_dir), json.loads(registry_probe(project, cli)))
                report["obligations"]["permissions"] = {"status": "fail" if errors else "pass", "errors": errors}
            if "session_prepare" in selected:
                captures = temp / "registries"
                captures.mkdir()
                report["obligations"]["session_prepare"] = qualify_session_prepare(project, cli, captures)
            inner_scenarios = [("tool_result_envelope", "envelope"), ("background_subagent", "background"),
                               ("session_directory", "directory"), ("abort_oversize", "abort"),
                               ("abort_oversize", "oversize")]
            inner_scenarios = [(name, scenario) for name, scenario in inner_scenarios if name in selected]
            if inner_scenarios:
                subprocess.run(["go", "build", "-o", str(project / "concord"), "./cmd/concord"],
                               cwd=ROOT, check=True, capture_output=True, text=True, timeout=110)
                stage = subprocess.run(sandbox_base(project, cli) + ["--ro-bind", str(ROOT), "/tmp/repo",
                    "/usr/bin/python3", "/tmp/repo/scripts/testdata/qualify-opencode-host/inner.py",
                    *[scenario for _, scenario in inner_scenarios]],
                     capture_output=True, text=True, timeout=170, env={})
                inner_results = {}
                for line in stage.stdout.splitlines():
                    values = json.loads(line)
                    inner_results.update(values)
                    for name, _ in inner_scenarios:
                        if name in values:
                            report["obligations"][name] = values[name]
                if "abort_oversize" in selected:
                    abort = inner_results.get("abort", {"status": "not_run"})
                    oversize = inner_results.get("oversize", {"status": "not_run"})
                    if abort["status"] != "not_run" or oversize["status"] != "not_run":
                        report["obligations"]["abort_oversize"] = {
                            "status": "pass" if abort["status"] == oversize["status"] == "pass" else "fail",
                            "core": "shim", "abort": abort, "oversize": oversize}
                if stage.returncode:
                    for name, _ in inner_scenarios:
                        if report["obligations"][name]["status"] == "not_run":
                            report["obligations"][name] = {"status": "fail", "stderr": stage.stderr}
    except EnvironmentRefusal as exc:
        report["environment_refusal"] = str(exc)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as exc:
        report["error"] = str(exc)
        print(json.dumps(report, indent=2))
        return 1
    print(json.dumps(report, indent=2))
    return exit_code(report, selected)


if __name__ == "__main__":
    raise SystemExit(main())
