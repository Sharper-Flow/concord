#!/usr/bin/env python3
"""Tests for scripts/shard_format.py.

The module owns the one canonical byte form for an authored knowledge shard.
These tests pin that form, prove normalising is content-preserving, and prove a
shard the module cannot parse is left for the caller's own validation rather
than silently rewritten.
"""
from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("shard_format", Path(__file__).with_name("shard_format.py"))
assert SPEC and SPEC.loader
shard_format = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(shard_format)


class CanonicalForm(unittest.TestCase):
    def test_form_is_two_space_sorted_and_newline_terminated(self) -> None:
        raw = shard_format.canonical_bytes({"b": 1, "a": [2, 3]}).decode()
        self.assertEqual(raw, '{\n  "a": [\n    2,\n    3\n  ],\n  "b": 1\n}\n')

    def test_form_is_idempotent(self) -> None:
        once = shard_format.canonical_bytes({"id": "CD-0001", "state": "outstanding"})
        self.assertEqual(shard_format.canonical_bytes(json.loads(once)), once)

    def test_non_ascii_is_not_escaped(self) -> None:
        self.assertIn("é".encode(), shard_format.canonical_bytes({"title": "café"}))


class Drift(unittest.TestCase):
    def test_every_committed_shard_is_canonical(self) -> None:
        shards = sorted((ROOT / "docs/knowledge/records").glob("*.json"))
        shards += sorted((ROOT / "docs/knowledge/coverage").glob("*.json"))
        self.assertTrue(shards, "expected authored shards in the repository")
        self.assertEqual(shard_format.drifted(shards), [])

    def test_drift_is_reported_and_repaired_without_changing_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "CD-0001.json"
            record = {"id": "CD-0001", "issue": 7, "state": "outstanding"}
            path.write_text(json.dumps(record, indent=6))  # drifted: wrong indent, no newline

            self.assertEqual(shard_format.drifted([path]), [path])
            self.assertEqual(shard_format.normalise([path]), [path])
            self.assertEqual(json.loads(path.read_bytes()), record)
            self.assertEqual(shard_format.drifted([path]), [])
            self.assertEqual(shard_format.normalise([path]), [])

    def test_unparseable_shard_is_left_to_the_caller(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "CD-0002.json"
            path.write_text("{ not json")
            self.assertEqual(shard_format.drifted([path]), [])
            self.assertEqual(shard_format.normalise([path]), [])
            self.assertEqual(path.read_text(), "{ not json")


if __name__ == "__main__":
    unittest.main(verbosity=0 if "-q" in sys.argv else 1)
