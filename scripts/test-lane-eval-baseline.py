#!/usr/bin/env python3
"""Fixture tests for scripts/check-lane-eval-baseline.py (issue #212)."""

from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts/check-lane-eval-baseline.py"

spec = importlib.util.spec_from_file_location("lane_eval_baseline_checker", CHECKER)
if spec is None or spec.loader is None:
    raise RuntimeError("unable to load the lane eval baseline checker")
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


def lane(lane_id: str) -> dict:
    return {
        "id": lane_id,
        "version": 1,
        "digest": checker.canonical_digest({"id": lane_id, "version": 1, "payload": lane_id}),
    }


def write_tree(root: Path, baseline: dict, packets: list[str]) -> None:
    (root / "adapter/opencode/evals/baselines").mkdir(parents=True)
    (root / "adapter/opencode/evals/baselines/lane-baseline.v1.json").write_text(
        json.dumps(baseline, indent=2) + "\n", encoding="utf-8"
    )
    packets_dir = root / "adapter/opencode/evals/packets"
    packets_dir.mkdir(parents=True)
    for name in packets:
        (packets_dir / name).write_text("{}", encoding="utf-8")
    (root / "contracts").mkdir()
    (root / "contracts/agent-lanes.v1.json").write_text(
        json.dumps({"lanes": [lane("research"), lane("review")]}, indent=2) + "\n", encoding="utf-8"
    )


def run(root: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(CHECKER), "--root", str(root)],
        capture_output=True,
        text=True,
    )


def passing_baseline(packets: list[str]) -> tuple[dict, list[str]]:
    names = list(packets)
    seeded = [name for name in names if name.startswith("review-seeded-")]
    index = 0
    while len(seeded) < 3:
        name = f"review-seeded-pad{index}.json"
        names.append(name)
        seeded.append(name)
        index += 1
    baseline = {
        "schema_version": "1.0",
        "generated_at": "2026-08-29T00:00:00Z",
        "lane_registry": [lane("research"), lane("review")],
        "runs": [
            {
                "packet": name,
                "lane_id": "review",
                "outcome": "pass",
                "readback_model": "zai-coding-plan/glm-5.3",
            }
            for name in names
        ],
    }
    return baseline, names


class CanonicalDigestTests(unittest.TestCase):
    def test_digest_ignores_an_injected_digest_field_and_orders_keys(self):
        body = {"id": "review", "version": 1, "budgets": {"cost_usd_max": 1, "time_seconds_max": 2}}
        self.assertEqual(checker.canonical_digest(body), checker.canonical_digest(dict(body, digest="sha256:" + "0" * 64)))


class BaselineCheckTests(unittest.TestCase):
    def test_green_repository_tree_passes(self):
        packets = [
            "research-bounded-question.json",
            "review-bounded-review.json",
            "review-seeded-scope-violation.json",
            "review-seeded-evidence-gap.json",
            "review-seeded-validator-weakening.json",
        ]
        baseline, names = passing_baseline(packets)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_tree(root, baseline, names)
            result = run(root)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_a_registry_digest_change_orphans_the_baseline(self):
        packets = ["review-bounded-review.json", "review-seeded-a.json", "review-seeded-b.json", "review-seeded-c.json"]
        baseline, names = passing_baseline(packets)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_tree(root, baseline, names)
            manifest = json.loads((root / "contracts/agent-lanes.v1.json").read_text())
            manifest["lanes"][0]["digest"] = "sha256:" + "f" * 64
            (root / "contracts/agent-lanes.v1.json").write_text(json.dumps(manifest, indent=2) + "\n")
            result = run(root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("does not bind registered lane", result.stdout)

    def test_a_packet_without_a_recorded_run_fails(self):
        packets = ["review-bounded-review.json", "review-seeded-a.json", "review-seeded-b.json", "review-seeded-c.json"]
        baseline, names = passing_baseline(packets)
        baseline["runs"] = baseline["runs"][:-1]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_tree(root, baseline, names)
            result = run(root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("does not record a run for packet", result.stdout)

    def test_a_run_without_readback_model_fails(self):
        packets = ["review-bounded-review.json", "review-seeded-a.json", "review-seeded-b.json", "review-seeded-c.json"]
        baseline, names = passing_baseline(packets)
        baseline["runs"][0]["readback_model"] = "not-a-model-identifier"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_tree(root, baseline, names)
            result = run(root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("readback_model", result.stdout)

    def test_too_few_seeded_packets_fail(self):
        packets = ["review-bounded-review.json", "review-seeded-a.json"]
        baseline, names = passing_baseline(packets)
        baseline["runs"] = [entry for entry in baseline["runs"] if entry["packet"] in packets]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_tree(root, baseline, packets)
            result = run(root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("seeded-defect packets", result.stdout)


if __name__ == "__main__":
    unittest.main()
