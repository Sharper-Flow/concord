#!/usr/bin/env python3
"""Generate the pinned native adapter acceptance recipe; --check refuses drift."""
import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / ".concord/oracles/adapter/native-acceptance.v1.json"
PACKAGE = ROOT / "adapter/opencode/package.json"
LOCK = PACKAGE.with_name("bun.lock")
CHECKS = {
    "adapter-transport": ["concord.test.ts"],
    "adapter-move": ["worktree-claim-move.test.ts", "session-vacate-move.test.ts", "turn-move-boundary.test.ts"],
    "adapter-publication": ["concord-plugin.test.ts"],
}


def render() -> dict[Path, str]:
    package = json.loads(PACKAGE.read_text())
    if set(package) != {"name", "private"} or package["private"] is not True:
        raise SystemExit("adapter acceptance package must remain dependency-free")
    harness = ["acceptance.py", "acceptance-preload.ts", "test_preload.ts", "fixture-temp-root.ts",
               "plugin-tool.fixture.ts", "host-version.test-support.ts", "generated-contract-tests.ts"]
    harness += [name for files in CHECKS.values() for name in files]
    manifest = {
        "kind": "target_runner_v1", "materializer": "bun", "runner": "check", "root": "adapter/opencode",
        "harness_files": [f"adapter/opencode/{name}" for name in harness], "support_files": [],
        "cases": {"case:adapter-native": list(CHECKS)},
        "checks": [{"witness": witness, "argv": ["python3", "acceptance.py", *files]} for witness, files in CHECKS.items()],
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
