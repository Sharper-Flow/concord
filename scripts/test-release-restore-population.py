#!/usr/bin/env python3
"""Tests for scripts/release_restore_population.py, with releases and tags injected as data."""

from __future__ import annotations

import unittest

import release_restore_population as population

TARGET = "a" * 40
SETTLED = population.SETTLED_UNPUBLISHED["v8.28.5"]


def release(tag: str, release_id: int, assets: tuple[str, ...] | None = None, draft: bool = False) -> dict:
    names = assets if assets is not None else (f"concord-{tag}", f"concord-{tag}.sha256")
    return {"id": release_id, "tag_name": tag, "draft": draft,
            "assets": [{"id": release_id * 10 + index, "name": name, "size": 1} for index, name in enumerate(names)]}


def tag_map(*tags: str) -> dict:
    return {tag: (f"{index:040x}", f"{index + 100:040x}") for index, tag in enumerate(tags, start=1)}


class PopulationTests(unittest.TestCase):
    def test_every_release_page_is_read(self) -> None:
        pages = {1: [release(f"v1.0.{n}", n) for n in range(population.PER_PAGE)], 2: [release("v2.0.0", 999)]}
        releases = population.list_releases(population.PER_PAGE + 1, lambda page: pages.get(page, []))
        self.assertEqual(releases[-1]["tag_name"], "v2.0.0")
        self.assertEqual(len(releases), population.PER_PAGE + 1)

    def test_short_or_long_pagination_refuses(self) -> None:
        pages = {1: [release("v1.0.0", 1)]}
        with self.assertRaisesRegex(population.GateError, "1 of 2"):
            population.list_releases(2, lambda page: pages.get(page, []))
        with self.assertRaisesRegex(population.GateError, "2 of 1"):
            population.list_releases(1, lambda page: [release("v1.0.0", 1), release("v1.0.1", 2)])

    def test_join_classifies_every_tag(self) -> None:
        releases = [release("v1.0.0", 1), release("v1.1.0", 2, assets=("concord-v1.1.0",)),
                    release("v1.2.0", 3), release("v1.3.0", 4, draft=True)]
        tags = tag_map("v1.0.0", "v1.1.0", "v1.4.0", "v9.1.0")
        tags["v8.28.5"] = SETTLED
        writers, unknown, unavailable, excluded = population.enumerate_writers(releases, tags, "v9.0.0", TARGET)
        self.assertEqual([writer.tag for writer in writers], ["v1.0.0"])
        self.assertEqual({(row["tag"], row["stage"]) for row in unknown},
                         {("v1.1.0", "artifact"), ("v1.2.0", "population"), ("v1.4.0", "population"), ("v9.1.0", "population")})
        self.assertEqual([row["tag"] for row in unavailable], ["v8.28.5"])
        self.assertEqual(excluded, [])

    def test_retargeted_settled_tag_is_unknown(self) -> None:
        tags = {"v8.28.5": (SETTLED[0], "f" * 40)}
        _, unknown, unavailable, _ = population.enumerate_writers([], tags, "v9.0.0", TARGET)
        self.assertEqual([row["tag"] for row in unknown], ["v8.28.5"])
        self.assertEqual(unavailable, [])

    def test_candidate_identity_rules(self) -> None:
        tags = tag_map("v1.0.0", "v2.0.0")
        tags["v2.0.0"] = ("b" * 40, TARGET)
        releases = [release("v1.0.0", 1), release("v2.0.0", 2)]
        writers, _, _, excluded = population.enumerate_writers(releases, tags, "v2.0.0", TARGET)
        self.assertEqual(([w.tag for w in writers], excluded), (["v1.0.0"], ["v2.0.0"]))
        writers, unknown, _, excluded = population.enumerate_writers(releases[:1], tags, "v2.0.0", TARGET)
        self.assertEqual((excluded, unknown), (["v2.0.0"], []))
        _, unknown, _, excluded = population.enumerate_writers(releases[:1], tags, "v2.0.0", "c" * 40)
        self.assertEqual((excluded, [row["tag"] for row in unknown]), ([], ["v2.0.0"]))
        with self.assertRaisesRegex(population.GateError, "not earlier"):
            population.enumerate_writers(releases, tags, "v2.0.0", "c" * 40)
        with self.assertRaisesRegex(population.GateError, "not earlier"):
            population.enumerate_writers(releases, tags, "v1.5.0", TARGET)

    def test_duplicate_or_unknown_grammar_refuses(self) -> None:
        tags = tag_map("v1.0.0")
        with self.assertRaisesRegex(population.GateError, "duplicate"):
            population.enumerate_writers([release("v1.0.0", 1), release("v1.0.0", 2)], tags, "v2.0.0", TARGET)
        with self.assertRaisesRegex(population.GateError, "strict release version"):
            population.enumerate_writers([release("v1.0", 1)], tags, "v2.0.0", TARGET)


if __name__ == "__main__":
    unittest.main()
