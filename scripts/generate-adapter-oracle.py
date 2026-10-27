#!/usr/bin/env python3
"""Generate the pinned adapter dispatch recipe; --check refuses stale output.

The adapter package declares no third-party dependencies. Bun deletes an empty
lock on install; the native materializer requires an explicit locked closure,
so this generator records that empty closure from the real package manifest.
"""
import argparse
import json
from pathlib import Path

from evidence_anchors import scenario_adapter_test, scenario_declarations

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / ".concord/oracles/adapter/dispatch-acceptance.v1.json"
PACKAGE = ROOT / "adapter/opencode/package.json"
LOCK = PACKAGE.with_name("bun.lock")
CHECKS = {
    "adapter-dispatch": ["concord.test.ts", "lane_dispatch.test.ts"],
    "adapter-guards": ["dispatch-window.test.ts", "native-task-refusal.test.ts", "session-scope.test.ts"],
    "adapter-publication": ["concord-plugin.test.ts"],
}


def render() -> dict[Path, str]:
    package = json.loads(PACKAGE.read_text())
    if set(package) != {"name", "private"} or package["private"] is not True:
        raise SystemExit("adapter acceptance package must remain dependency-free")
    harness = ["acceptance.py", "acceptance-preload.ts", "test_preload.ts", "fixture-temp-root.ts",
               "plugin-tool.fixture.ts", "host-version.test-support.ts", "generated-contract-tests.ts"]
    harness += [name for files in CHECKS.values() for name in files]
    continuity_corpus = ".concord/scenarios/adapter-continuity.v1.json"
    continuity = sorted(identity for identity, (path, _) in scenario_declarations().items()
                        if path == continuity_corpus and scenario_adapter_test(identity) is not None)
    checks = [{"witness": witness, "argv": ["python3", "acceptance.py", *files]} for witness, files in CHECKS.items()]
    checks += [{"witness": identity, "argv": ["python3", "acceptance.py", "continuity_hook.test.ts", "--expect", identity]}
               for identity in continuity]
    manifest = {
        "kind": "target_runner_v1", "materializer": "bun", "runner": "check", "root": "adapter/opencode",
        "harness_files": [f"adapter/opencode/{name}" for name in [*harness, "continuity_hook.test.ts"]],
        "support_files": [continuity_corpus],
        "cases": {"case:adapter-dispatch": list(CHECKS), "case:adapter-continuity": continuity},
        "checks": checks,
        "pin": {"bun": "1.4.0", "python": "3.13.7", "dep_manifest": "package.json", "dep_lock": "bun.lock"},
    }
    lock = {"lockfileVersion": 1, "configVersion": 1,
            "workspaces": {"": {"name": package["name"]}}, "packages": {}}
    return {OUTPUT: json.dumps(manifest, indent=2) + "\n", LOCK: json.dumps(lock, indent=2) + "\n"}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    for output, expected in render().items():
        if args.check:
            if not output.is_file() or output.read_text() != expected:
                raise SystemExit(f"{output.relative_to(ROOT)} is stale; run python3 scripts/generate-adapter-oracle.py")
        else:
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_text(expected)


if __name__ == "__main__":
    main()
