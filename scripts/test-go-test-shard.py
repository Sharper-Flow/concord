#!/usr/bin/env python3
"""Tests for the CI test-shard pattern (CON-906)."""

from __future__ import annotations

import importlib.util
from pathlib import Path
import re
import unittest

SCRIPT = Path(__file__).resolve().parent / "go-test-shard.py"
spec = importlib.util.spec_from_file_location("go_test_shard", SCRIPT)
assert spec and spec.loader
go_test_shard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(go_test_shard)

LIST_OUTPUT = """TestOpen
TestAlpha
ExampleStore
FuzzParse
TestZeta
TestBeta
ok  \tgithub.com/example/pkg\t0.012s
"""


class ShardTests(unittest.TestCase):
    def test_listing_keeps_runnable_names_and_drops_the_status_line(self) -> None:
        self.assertEqual(
            go_test_shard.listed_names(LIST_OUTPUT),
            ["ExampleStore", "FuzzParse", "TestAlpha", "TestBeta", "TestOpen", "TestZeta"],
        )

    def test_shards_are_disjoint_and_cover_every_name(self) -> None:
        names = go_test_shard.listed_names(LIST_OUTPUT)
        for count in (1, 2, 3, 4):
            with self.subTest(count=count):
                shards = [go_test_shard.shard(names, index, count) for index in range(1, count + 1)]
                dealt = [name for part in shards for name in part]
                self.assertEqual(sorted(dealt), names)
                self.assertEqual(len(dealt), len(set(dealt)))

    def test_pattern_selects_exactly_its_names(self) -> None:
        names = go_test_shard.listed_names(LIST_OUTPUT)
        part = go_test_shard.shard(names, 2, 3)
        pattern = re.compile(go_test_shard.run_pattern(part))
        self.assertEqual([name for name in names if pattern.match(name)], part)
        self.assertIsNone(pattern.match("TestAlphaExtra"))

    def test_impossible_or_empty_shards_refuse(self) -> None:
        with self.assertRaises(ValueError):
            go_test_shard.shard(["TestA"], 0, 2)
        with self.assertRaises(ValueError):
            go_test_shard.shard(["TestA"], 3, 2)
        with self.assertRaises(ValueError):
            go_test_shard.run_pattern([])


if __name__ == "__main__":
    unittest.main()
