#!/usr/bin/env python3
"""Fail when the pre-push preflight drifts from the CI steps that own it.

`lefthook.yml` promises that its pre-push gates are the CI commands, selected
cheaply by the pushed file set. This validator reads both sides with the real
YAML parser (PyYAML), fails visibly when PyYAML is unavailable, and reports:

- `pyyaml-required`: the real YAML parser is missing.
- `hook-config` / `hook-pin`: lefthook.yml is absent, malformed, or loses the
  pinned parallel/no-auto-install/assert-installed/min-version settings.
- `gate-missing` / `gate-unknown`: a required gate disappeared or an
  unaudited command appeared in pre-push.
- `gate-parity`: a gate command is not byte-identical (modulo whitespace) to
  a CI `run:` command segment, so a changed or removed CI step drops the
  gate's owner.
- `gate-env-parity`: a gate environment differs from the owning CI step's.
- `selector-parity` / `placeholder`: the `{push_files}` selector command
  changed, or another gate grew a file placeholder.
- `selector-module` / `battery-derivation` / `suite-missing`: the selector
  cannot be loaded, its tooling battery cannot be derived from the CI
  workflow, or a battery suite is absent on disk.

Suite and battery parsing live in `scripts/run-script-tests.py`; this
validator imports that module instead of re-deriving them, so one parser
serves both sides. Glob routing carries no hand model here: the required
routes run as executable conformance fixtures against the real pinned
Lefthook in `scripts/test-pre-push.py`. This is a drift check, not a proof;
preflight stays cheaper than CI and CI stays the verification owner.
"""

from __future__ import annotations

import importlib.util
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
HOOK_CONFIG = "lefthook.yml"
CI_WORKFLOW = ".github/workflows/ci.yml"
SELECTOR_GATE = "script-tests"
SELECTOR_RUN = "python3 scripts/run-script-tests.py -- {push_files}"

# The pinned upstream release this repository's preflight is written against.
# Installed as an external binary (`go install ...@v2.1.14`), never a go.mod
# dependency; bin/oc-test preflight falls back to
# `go run github.com/evilmartians/lefthook/v2@v2.1.14` when no binary is on
# PATH. v2.1.14 builds with the Go minor this repository already pins.
PINNED_LEFTHOOK_VERSION = "2.1.14"

REQUIRED_GATES = (
    "gofmt",
    "go-vet",
    "go-build",
    "go-compile-tests",
    "go-lint",
    "check-json",
    "check-reachability",
    "check-complexity",
    SELECTOR_GATE,
)

PLACEHOLDER_RE = re.compile(r"\{[a-z_]+\}")

try:
    import yaml
except ImportError:  # pragma: no cover - CI runner images carry PyYAML
    yaml = None


def normalize(command: str) -> str:
    return " ".join(command.split())


def load_hook_config(root: Path) -> dict:
    path = root / HOOK_CONFIG
    if yaml is None:
        raise RuntimeError("PyYAML is required to parse lefthook.yml")
    return yaml.safe_load(path.read_text(encoding="utf-8"))


def ci_inventory(ci_document: object) -> list[dict]:
    """Every runnable CI command segment with its owning step environment."""
    inventory: list[dict] = []
    jobs = ci_document.get("jobs") if isinstance(ci_document, dict) else None
    if not isinstance(jobs, dict):
        return inventory
    for job_id, job in jobs.items():
        steps = job.get("steps") if isinstance(job, dict) else None
        if not isinstance(steps, list):
            continue
        for step in steps:
            if not isinstance(step, dict) or not isinstance(step.get("run"), str):
                continue
            env = step.get("env") if isinstance(step.get("env"), dict) else {}
            for line in step["run"].splitlines():
                for segment in line.split("&&"):
                    if segment.strip():
                        inventory.append(
                            {
                                "job": job_id,
                                "step": step.get("name"),
                                "env": {str(key): str(value) for key, value in env.items()},
                                "command": normalize(segment),
                            }
                        )
    return inventory


def _load_selector(root: Path):
    path = root / "scripts/run-script-tests.py"
    spec = importlib.util.spec_from_file_location("run_script_tests_for_pre_push", path)
    if spec is None or spec.loader is None:
        return None
    module = importlib.util.module_from_spec(spec)
    sys.modules["run_script_tests_for_pre_push"] = module
    try:
        spec.loader.exec_module(module)
    except Exception:
        sys.modules.pop("run_script_tests_for_pre_push", None)
        return None
    finally:
        sys.modules.pop("run_script_tests_for_pre_push", None)
    return module


def check(*, root: Path = ROOT) -> list[str]:
    findings: list[str] = []
    if yaml is None:
        return [
            "pyyaml-required: a real YAML parser (PyYAML) is required to check pre-push parity; "
            "install PyYAML instead of running degraded"
        ]

    try:
        config = load_hook_config(root)
    except (OSError, UnicodeDecodeError) as error:
        return [f"hook-config: cannot read {HOOK_CONFIG}: {error}"]
    except yaml.YAMLError as error:
        return [f"hook-config: {HOOK_CONFIG} is not valid YAML: {error}"]
    if not isinstance(config, dict):
        return [f"hook-config: {HOOK_CONFIG} must parse to a YAML mapping"]

    if config.get("min_version") != PINNED_LEFTHOOK_VERSION:
        findings.append(
            f"hook-pin: min_version must stay {PINNED_LEFTHOOK_VERSION!r}, got {config.get('min_version')!r}"
        )
    for option in ("assert_lefthook_installed", "no_auto_install"):
        if config.get(option) is not True:
            findings.append(f"hook-pin: {option} must be true; the preflight loses its fail-closed behavior")
    pre_push = config.get("pre-push")
    commands = pre_push.get("commands") if isinstance(pre_push, dict) else None
    if not isinstance(pre_push, dict) or not isinstance(commands, dict):
        findings.append("hook-config: pre-push.commands must be a mapping of gates")
        return findings
    if pre_push.get("parallel") is not False:
        findings.append("hook-config: pre-push.parallel must be false so concurrent pushes do not multiply whole-repository Go gates on a shared host")

    try:
        ci_document = yaml.safe_load((root / CI_WORKFLOW).read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, yaml.YAMLError) as error:
        findings.append(f"hook-config: cannot parse {CI_WORKFLOW} with the real YAML parser: {error}")
        return findings
    inventory = ci_inventory(ci_document)

    for gate in REQUIRED_GATES:
        if gate not in commands:
            findings.append(f"gate-missing: {gate} is a required pre-push gate; removing it drops a CI-owned check")
    for name, command in commands.items():
        if name not in REQUIRED_GATES:
            findings.append(f"gate-unknown: {name} is not an audited pre-push gate")
            continue
        if not isinstance(command, dict) or not isinstance(command.get("run"), str):
            findings.append(f"gate-parity: {name} must carry a run command")
            continue
        run = command["run"]
        if name == SELECTOR_GATE:
            if normalize(run) != normalize(SELECTOR_RUN):
                findings.append(
                    f"selector-parity: {name} must stay exactly {SELECTOR_RUN!r}; "
                    "scripts/check-pre-push.py audits that single selector invocation"
                )
            continue
        placeholders = PLACEHOLDER_RE.findall(run)
        if placeholders:
            findings.append(
                f"placeholder: {name} must not use file placeholders ({', '.join(placeholders)}); "
                "only the {push_files} selector may receive files"
            )
        normalized = normalize(run)
        owners = [entry for entry in inventory if entry["command"] == normalized]
        if not owners:
            findings.append(
                f"gate-parity: {name} command is not identical to any CI step command: {normalized!r}; "
                "a changed or removed CI step must drop the gate here too, never leave a divergent copy"
            )
            continue
        gate_env = command.get("env") if isinstance(command.get("env"), dict) else {}
        gate_env = {str(key): str(value) for key, value in gate_env.items()}
        if not any(owner["env"] == gate_env for owner in owners):
            findings.append(
                f"gate-env-parity: {name} environment {gate_env!r} differs from its owning CI step "
                f"(expected one of {[owner['env'] for owner in owners]})"
            )

    selector = _load_selector(root)
    if selector is None:
        findings.append(
            f"selector-module: scripts/run-script-tests.py cannot be loaded; "
            "the {push_files} selector invocation has no implementation to audit"
        )
        return findings
    try:
        battery = selector.tooling_battery(root)
    except Exception as error:
        findings.append(f"battery-derivation: the tooling battery cannot be derived from {CI_WORKFLOW}: {error}")
    else:
        for suite in battery:
            if not (root / suite).is_file():
                findings.append(f"suite-missing: tooling battery suite {suite} does not exist on disk")

    return findings


def main() -> int:
    findings = check()
    for finding in findings:
        print(finding)
    if findings:
        print(f"pre-push preflight check failed: {len(findings)} finding(s)", file=sys.stderr)
        return 1
    print("pre-push preflight check passed: every gate is identical to its owning CI step")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
