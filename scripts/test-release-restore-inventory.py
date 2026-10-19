#!/usr/bin/env python3
"""Tests for the release restore inventory (scripts/release-restore-inventory.go).

The real generator runs as a subprocess against a synthetic Git repository
whose tags carry each registry form the released history uses:

- form A composite literals, which name no payload type, are refused and
  marked as the accepted named exclusion;
- registerEventKind[T], registerScopedEventKind[T], and withRecordedSchemas
  entries are probed against the tag's own compiled source;
- any other entry form, and a source that does not compile, is refused
  without being excluded;
- tags with identical inputs share one probe group, and -previous reuses a
  group by its digest without compiling anything.

The fixture module is synthetic; no released Concord source is compiled.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import git_environment

git_environment.scrub_inherited()

ROOT = Path(__file__).resolve().parents[1]
GENERATOR = "scripts/release-restore-inventory.go"
UNTYPED = "registry entries name no payload type (form A); accepted named exclusion from shape coverage"

GO_MOD = "module example.com/fixture\n\ngo 1.26\n"

PRELUDE = """package store

import "encoding/json"

type Upcaster func(int) int

type EventKindRegistration struct {
	CurrentVersion  int
	MinSupported    int
	Upcasters       map[int]Upcaster
	RecordedSchemas map[int][]string
}

func registerEventKind[T any](current, minimum int, upcasters map[int]Upcaster) EventKindRegistration {
	return EventKindRegistration{CurrentVersion: current, MinSupported: minimum, Upcasters: upcasters}
}

func registerScopedEventKind[T any](current, minimum int, upcasters map[int]Upcaster) EventKindRegistration {
	return registerEventKind[T](current, minimum, upcasters)
}

func withRecordedSchemas(r EventKindRegistration, schemas map[int][]string) EventKindRegistration {
	r.RecordedSchemas = schemas
	return r
}

type Inner struct {
	Depth int `json:"depth"`
}

type hidden struct {
	Promoted string `json:"promoted"`
}

type Stamp struct{}

func (Stamp) MarshalJSON() ([]byte, error) { return []byte("0"), nil }

type Node struct {
	Next *Node `json:"next,omitempty"`
}

type Conflict struct {
	Inner
	Depth int `json:"depth"`
}

type Typed struct {
	hidden
	Name     string            `json:"name"`
	Count    int64             `json:"count,string"`
	Optional *Inner            `json:"optional,omitempty"`
	Tags     []string          `json:"tags"`
	Labels   map[string]Inner  `json:"labels"`
	Raw      json.RawMessage   `json:"raw"`
	When     Stamp             `json:"when"`
	Any      any               `json:"any"`
	Blob     []byte            `json:"blob"`
	Skipped  string            `json:"-"`
	Default  bool
	private  int
	Chain    Node              `json:"chain"`
}
"""

TYPED_REGISTRY = """
var eventKindRegistry = map[string]EventKindRegistration{
	"thing.typed":     registerEventKind[Typed](2, 1, map[int]Upcaster{1: nil}),
	"thing.scoped":    registerScopedEventKind[Inner](1, 1, nil),
	"thing.recorded":  withRecordedSchemas(registerEventKind[Inner](2, 1, map[int]Upcaster{1: nil}), map[int][]string{1: nil}),
	"thing.conflict":  registerEventKind[Conflict](1, 1, nil),
}
"""

FORM_A_REGISTRY = """
var eventKindRegistry = map[string]EventKindRegistration{
	"thing.typed": {CurrentVersion: 1, MinSupported: 1},
}
"""

UNKNOWN_FORM_REGISTRY = """
func build() EventKindRegistration { return EventKindRegistration{} }

var eventKindRegistry = map[string]EventKindRegistration{
	"thing.typed": build(),
}
"""

BROKEN_REGISTRY = TYPED_REGISTRY + "\nvar broken int = \"not an int\"\n"


def run(args: list[str], cwd: Path, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, check=False, timeout=600)


class InventoryTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.scratch = Path(tempfile.mkdtemp(prefix="release-restore-inventory-test-"))
        cls.binary = cls.scratch / "inventory"
        built = run(["go", "build", "-o", str(cls.binary), GENERATOR], ROOT)
        if built.returncode != 0:
            raise AssertionError(built.stderr)
        cls.repo = cls.scratch / "repo"
        cls.repo.mkdir()
        git = lambda *args: run(["git", *args], cls.repo)
        for command in (["init", "-q"], ["config", "user.email", "fixture@example.invalid"], ["config", "user.name", "fixture"]):
            assert git(*command).returncode == 0
        (cls.repo / "go.mod").write_text(GO_MOD)
        (cls.repo / "go.sum").write_text("")
        store = cls.repo / "internal" / "store"
        store.mkdir(parents=True)
        # (tag, registry, annotated); v1.0.1 differs outside internal/ only.
        for tag, registry, annotated in (
            ("v0.1.0", FORM_A_REGISTRY, False),
            ("v1.0.0", TYPED_REGISTRY, True),
            ("v1.0.1", TYPED_REGISTRY, False),
            ("v1.0.1-rc.1", TYPED_REGISTRY, False),
            ("v2.0.0", UNKNOWN_FORM_REGISTRY, False),
            ("v3.0.0", BROKEN_REGISTRY, False),
        ):
            (store / "operation.go").write_text(PRELUDE + registry)
            (cls.repo / "README").write_text(tag)
            assert git("add", "-A").returncode == 0
            assert git("commit", "-q", "-m", tag).returncode == 0
            assert git("tag", *(["-a", "-m", tag] if annotated else []), tag).returncode == 0
        cls.first = cls.generate("first.json")

    @classmethod
    def tearDownClass(cls) -> None:
        shutil.rmtree(cls.scratch, ignore_errors=True)

    @classmethod
    def generate(cls, name: str, *extra: str, env: dict[str, str] | None = None) -> dict:
        output = cls.scratch / name
        result = run([str(cls.binary), "-out", str(output), *extra], cls.repo, env)
        if result.returncode != 0:
            raise AssertionError(result.stderr)
        return json.loads(output.read_text())

    def rows(self) -> dict[str, dict]:
        return {row["tag"]: row for row in self.first["rows"]}

    def shape(self, kind: str, tag: str = "v1.0.0") -> dict:
        registration = next(r for r in self.first["groups"][self.rows()[tag]["group"]] if r["kind"] == kind)
        return self.first["types"][registration["type"]]

    def test_population_is_every_strict_tag_in_version_order(self) -> None:
        self.assertEqual([row["tag"] for row in self.first["rows"]], ["v0.1.0", "v1.0.0", "v1.0.1", "v2.0.0", "v3.0.0"])
        self.assertEqual(self.first["schema"], "concord.release-restore-writers.v1")
        self.assertEqual(self.first["generator"], "go run scripts/release-restore-inventory.go")

    def test_annotated_tag_keeps_tag_object_and_peeled_commit(self) -> None:
        row = self.rows()["v1.0.0"]
        self.assertNotEqual(row["tag_object"], row["commit"])
        commit = run(["git", "rev-parse", "v1.0.0^{commit}"], self.repo).stdout.strip()
        self.assertEqual(row["commit"], commit)
        light = self.rows()["v1.0.1"]
        self.assertEqual(light["tag_object"], light["commit"])

    def test_form_a_is_the_named_exclusion(self) -> None:
        row = self.rows()["v0.1.0"]
        self.assertEqual((row["status"], row.get("excluded"), row["detail"]), ("inventory_refused", True, UNTYPED))
        self.assertNotIn("group", row)

    def test_unrecognized_entry_refuses_without_exclusion(self) -> None:
        row = self.rows()["v2.0.0"]
        self.assertEqual(row["status"], "inventory_refused")
        self.assertNotIn("excluded", row)
        self.assertIn('"thing.typed" has an unrecognized form', row["detail"])

    def test_compile_failure_refuses_without_exclusion(self) -> None:
        row = self.rows()["v3.0.0"]
        self.assertEqual(row["status"], "inventory_refused")
        self.assertNotIn("excluded", row)
        self.assertIn("probe ", row["detail"])
        self.assertNotIn(row["group"], self.first["groups"])

    def test_identical_inputs_share_one_group(self) -> None:
        rows = self.rows()
        self.assertEqual(rows["v1.0.0"]["status"], "inventoried")
        self.assertEqual(rows["v1.0.0"]["group"], rows["v1.0.1"]["group"])
        self.assertEqual(set(self.first["groups"]), {rows["v1.0.0"]["group"]})

    def test_registry_metadata_comes_from_the_compiled_registry(self) -> None:
        group = {r["kind"]: r for r in self.first["groups"][self.rows()["v1.0.0"]["group"]]}
        self.assertEqual(sorted(group), ["thing.conflict", "thing.recorded", "thing.scoped", "thing.typed"])
        self.assertEqual((group["thing.typed"]["current_version"], group["thing.typed"]["upcasters"]), (2, [1]))
        self.assertEqual(group["thing.recorded"]["recorded_schemas"], [1])
        self.assertEqual(group["thing.scoped"]["recorded_schemas"], [])
        self.assertEqual(group["thing.scoped"]["payload_type"], "Inner")
        self.assertEqual(group["thing.scoped"]["type"], group["thing.recorded"]["type"])

    def test_descriptor_follows_encoding_json(self) -> None:
        shape = self.shape("thing.typed")
        self.assertEqual(shape["kind"], "object")
        fields = {field["name"]: field for field in shape["fields"]}
        self.assertEqual(list(fields), ["promoted", "name", "count", "optional", "tags", "labels", "raw", "when", "any", "blob", "Default", "chain"])
        self.assertTrue(fields["count"]["string"])
        self.assertTrue(fields["optional"]["omitempty"])
        self.assertEqual(fields["optional"]["shape"], {"kind": "pointer", "elem": {"kind": "object", "fields": [{"name": "depth", "shape": {"kind": "int"}}]}})
        self.assertEqual(fields["tags"]["shape"], {"kind": "array", "elem": {"kind": "string"}})
        self.assertEqual(fields["labels"]["shape"]["kind"], "map")
        self.assertEqual(fields["labels"]["shape"]["key"], "string")
        self.assertEqual(fields["raw"]["shape"], {"kind": "custom", "type": "json.RawMessage"})
        self.assertEqual(fields["when"]["shape"], {"kind": "custom", "type": "store.Stamp"})
        self.assertEqual(fields["any"]["shape"], {"kind": "custom", "type": "interface {}"})
        self.assertEqual(fields["blob"]["shape"], {"kind": "bytes"})
        chain = fields["chain"]["shape"]["fields"][0]["shape"]
        self.assertEqual(chain, {"kind": "pointer", "elem": {"kind": "recursive", "type": "store.Node"}})

    def test_types_are_content_addressed(self) -> None:
        for key, shape in self.first["types"].items():
            compact = json.dumps(shape, separators=(",", ":"), sort_keys=True).encode()
            self.assertEqual(key, "sha256:" + hashlib.sha256(compact).hexdigest())

    def test_ambiguous_embedding_is_custom(self) -> None:
        self.assertEqual(self.shape("thing.conflict"), {"kind": "custom", "type": "store.Conflict", "reason": "embedded field name conflict"})

    def test_previous_inventory_is_reused_without_compiling(self) -> None:
        shim = self.scratch / "shim"
        shim.mkdir(exist_ok=True)
        calls = self.scratch / "go-calls"
        (shim / "go").write_text(f'#!/bin/sh\necho "$PWD" >> "{calls}"\nexit 1\n')
        (shim / "go").chmod(0o755)
        env = {**os.environ, "PATH": f"{shim}{os.pathsep}{os.environ['PATH']}"}
        reused = self.generate("reused.json", "-previous", str(self.scratch / "first.json"), env=env)
        # Only the refused group is probed again; it fails at the shim.
        self.assertEqual(len(calls.read_text().splitlines()), 1)
        for row in (*reused["rows"], *self.first["rows"]):
            if row["tag"] == "v3.0.0":
                row.pop("detail")
        self.assertEqual(reused, self.first)

if __name__ == "__main__":
    unittest.main(verbosity=2)
